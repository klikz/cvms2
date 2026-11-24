// Package cameras manages camera status checks, codec probing and
// JPEG snapshots (via ffmpeg/ffprobe).
package cameras

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"cvms/client/internal/config"
	"cvms/client/internal/streamurl"
	"cvms/shared/ffmpegx"
	"cvms/shared/protocol"
)

// Manager owns runtime camera state.
type Manager struct {
	store  *config.Store
	ffmpeg string
	probe  string

	mu       sync.Mutex
	online   map[string]bool
	checked  map[string]time.Time
	fails    map[string]int       // consecutive failed reachability checks
	alive    map[string]time.Time // last proof of life from a running stream
	snaps    map[string]snap
	inflight map[string]*call
	onChange func(id string, online bool)
	kick     chan struct{}
	sem      chan struct{}
}

type snap struct {
	data []byte
	at   time.Time
	err  error
}

type call struct {
	done chan struct{}
	data []byte
	err  error
}

// New creates a manager.
func New(store *config.Store) *Manager {
	ff := ffmpegx.Resolve(store.Get().FFmpegPath)
	return &Manager{
		store:    store,
		ffmpeg:   ff,
		probe:    ffmpegx.Probe(ff),
		online:   map[string]bool{},
		checked:  map[string]time.Time{},
		fails:    map[string]int{},
		alive:    map[string]time.Time{},
		snaps:    map[string]snap{},
		inflight: map[string]*call{},
		kick:     make(chan struct{}, 1),
		sem:      make(chan struct{}, store.Get().SnapshotConcurrency),
	}
}

// FFmpeg returns the ffmpeg path.
func (m *Manager) FFmpeg() string { return m.ffmpeg }

// OnStatusChange registers a callback for camera online changes.
func (m *Manager) OnStatusChange(f func(id string, online bool)) { m.onChange = f }

// Online reports the last known status of a camera.
func (m *Manager) Online(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.online[id]
}

// Kick triggers an immediate status check.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Run checks camera reachability periodically.
func (m *Manager) Run(ctx context.Context) {
	if !ffmpegx.Available(m.ffmpeg) {
		log.Printf("WARNING: ffmpeg not found (%s). Install ffmpeg or set ffmpeg_path in config.yaml", m.ffmpeg)
	} else {
		go func() { log.Printf("video pipeline: %s", m.Pipeline().Name) }()
	}
	for {
		m.checkAll(ctx)
		interval := time.Duration(m.store.Get().StatusInterval) * time.Second
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		case <-m.kick:
		}
	}
}

// offlineAfter is how many consecutive failed checks mark a camera offline.
// A single failed dial is common (busy camera, NVR refusing parallel
// connections, packet loss) and must not flap the camera in the UI.
const offlineAfter = 3

func (m *Manager) checkAll(ctx context.Context) {
	cams := m.store.Get().Cameras
	interval := time.Duration(m.store.Get().StatusInterval) * time.Second

	// Cameras behind one NVR share host:port: dial each address once and
	// apply the result to all of them, instead of hitting the NVR with many
	// parallel connections that it may refuse.
	addrs := map[string]error{}
	for _, c := range cams {
		if c.Enabled {
			if a := rtspAddr(c); a != "" {
				addrs[a] = nil
			}
		}
	}
	var (
		wg  sync.WaitGroup
		rmu sync.Mutex
		sem = make(chan struct{}, 16)
	)
	for a := range addrs {
		a := a
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			err := reachable(ctx, a)
			rmu.Lock()
			addrs[a] = err
			rmu.Unlock()
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}

	for _, c := range cams {
		switch a := rtspAddr(c); {
		case !c.Enabled:
			m.applyCheck(c, false, errors.New("disabled"), true, interval)
		case a == "":
			// not final: if ffmpeg still plays the URL the camera stays online
			m.applyCheck(c, false, errors.New("invalid stream url"), false, interval)
		default:
			err := addrs[a]
			m.applyCheck(c, err == nil, err, false, interval)
		}
	}
	// forget removed cameras
	m.mu.Lock()
	ids := map[string]bool{}
	for _, c := range cams {
		ids[c.ID] = true
	}
	for id := range m.online {
		if !ids[id] {
			delete(m.online, id)
			delete(m.fails, id)
			delete(m.alive, id)
		}
	}
	m.mu.Unlock()
}

// applyCheck records one reachability result. A camera goes online on the
// first success but offline only after offlineAfter consecutive failures
// (or at once when final, e.g. disabled). A camera whose stream delivered
// frames recently is kept online.
func (m *Manager) applyCheck(c config.Camera, ok bool, err error, final bool, interval time.Duration) {
	if ok {
		m.setOnline(c.ID, true)
		return
	}
	m.mu.Lock()
	prev, known := m.online[c.ID]
	if !final && time.Since(m.alive[c.ID]) < 2*interval {
		// the stream is pushing frames: the dial failure is not real
		m.fails[c.ID] = 0
		m.checked[c.ID] = time.Now()
		m.mu.Unlock()
		return
	}
	m.fails[c.ID]++
	n := m.fails[c.ID]
	m.mu.Unlock()
	if !final && known && prev && n < offlineAfter {
		return
	}
	if known && prev && err != nil {
		log.Printf("camera %s offline: %v", c.Name, err)
	}
	m.setOnline(c.ID, false)
}

func (m *Manager) setOnline(id string, ok bool) {
	m.mu.Lock()
	prev, known := m.online[id]
	m.online[id] = ok
	m.checked[id] = time.Now()
	if ok {
		m.fails[id] = 0
	}
	m.mu.Unlock()
	if (!known || prev != ok) && m.onChange != nil {
		m.onChange(id, ok)
	}
}

// MarkAlive records that the camera is delivering video right now.
func (m *Manager) MarkAlive(id string) {
	m.mu.Lock()
	m.alive[id] = time.Now()
	m.mu.Unlock()
	m.setOnline(id, true)
}

// rtspAddr returns host:port of the main stream, or "".
func rtspAddr(c config.Camera) string {
	raw, _ := streamurl.Normalize(c.Main.URL)
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "554"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// reachable dials the RTSP port, retrying once after a short pause.
func reachable(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	var err error
	for i := 0; i < 2; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		var conn net.Conn
		if conn, err = d.DialContext(ctx, "tcp", addr); err == nil {
			conn.Close()
			return nil
		}
	}
	return err
}

// Info converts the config into protocol camera info for the server.
func (m *Manager) Info() []protocol.CameraInfo {
	cams := m.store.Get().Cameras
	out := make([]protocol.CameraInfo, 0, len(cams))
	for _, c := range cams {
		if !c.Enabled {
			continue
		}
		out = append(out, protocol.CameraInfo{
			ID:     c.ID,
			Name:   c.Name,
			Host:   c.Host,
			Model:  strings.TrimSpace(c.Manufacturer + " " + c.Model),
			Online: m.Online(c.ID),
			Main:   protocol.StreamInfo{Available: c.Main.URL != "", Codec: c.Main.Codec, Width: c.Main.Width, Height: c.Main.Height},
			Sub:    protocol.StreamInfo{Available: c.Sub.URL != "", Codec: c.Sub.Codec, Width: c.Sub.Width, Height: c.Sub.Height},
		})
	}
	return out
}

// ---------------------------------------------------------------- probing

// ProbeResult is the codec/resolution of a stream.
type ProbeResult struct {
	Codec  string `json:"codec"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Probe detects codec and resolution of an RTSP URL with ffprobe.
func (m *Manager) Probe(ctx context.Context, rtspURL string) (ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.probe, "-v", "error", "-rtsp_transport", "tcp", "-timeout", "8000000",
		"-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height", "-of", "json", rtspURL)
	hideWindow(cmd)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return ProbeResult{}, ffError(err, stderr.String())
	}
	var res struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || len(res.Streams) == 0 {
		return ProbeResult{}, errors.New("no video stream found")
	}
	s := res.Streams[0]
	codec := s.CodecName
	switch codec {
	case "hevc":
		codec = protocol.CodecH265
	case "h264":
		codec = protocol.CodecH264
	}
	return ProbeResult{Codec: codec, Width: s.Width, Height: s.Height}, nil
}

func ffError(err error, stderr string) error {
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
		return errors.New("ffmpeg/ffprobe not found")
	}
	s := strings.TrimSpace(stderr)
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "401") || strings.Contains(low, "unauthorized"):
		return errors.New("authentication failed (401)")
	case strings.Contains(low, "404") || strings.Contains(low, "not found"):
		return errors.New("stream not found (404)")
	case strings.Contains(low, "timed out") || strings.Contains(low, "timeout") || strings.Contains(low, "connection refused"):
		return errors.New("camera not reachable")
	}
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" {
		return err
	}
	return errors.New(s)
}

// ---------------------------------------------------------------- snapshots

// Snapshot returns a JPEG of the camera. maxAge allows serving a cached
// image; concurrent requests for the same stream share one ffmpeg run.
func (m *Manager) Snapshot(ctx context.Context, id, profile string, maxAge time.Duration) ([]byte, error) {
	cam, ok := m.store.Camera(id)
	if !ok {
		return nil, errors.New("camera not found")
	}
	if profile != protocol.ProfileMain {
		profile = protocol.ProfileSub
	}
	key := id + "/" + profile
	m.mu.Lock()
	if s, ok := m.snaps[key]; ok && time.Since(s.at) < maxAge && s.err == nil {
		m.mu.Unlock()
		return s.data, nil
	}
	if c := m.inflight[key]; c != nil {
		m.mu.Unlock()
		select {
		case <-c.done:
			return c.data, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c := &call{done: make(chan struct{})}
	m.inflight[key] = c
	m.mu.Unlock()

	u, st := cam.StreamURL(profile)
	if st.SnapshotURL != "" {
		hctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		c.data, c.err = httpSnapshot(hctx, st.SnapshotURL, cam.Username, cam.Password)
		cancel()
	}
	if st.SnapshotURL == "" || c.err != nil {
		c.data, c.err = m.grab(u, profile == protocol.ProfileMain, st.Codec)
	}

	m.mu.Lock()
	delete(m.inflight, key)
	if c.err == nil {
		m.snaps[key] = snap{data: c.data, at: time.Now()}
	}
	m.mu.Unlock()
	close(c.done)
	if c.err == nil {
		m.setOnline(id, true)
	}
	return c.data, c.err
}

// Pipeline returns the GPU/CPU pipeline used for decoding and transcoding.
func (m *Manager) Pipeline() ffmpegx.Pipeline {
	c := m.store.Get()
	return ffmpegx.PickPipeline(m.ffmpeg, c.HWAccel, c.H264Encoder, c.GPUDevice)
}

// grab runs ffmpeg to capture one frame as JPEG. At most
// snapshot_concurrency grabs run at once. Large (main) H.265 frames are
// decoded on the GPU when one is available.
func (m *Manager) grab(rtspURL string, main bool, codec string) ([]byte, error) {
	if rtspURL == "" {
		return nil, errors.New("no stream url")
	}
	m.sem <- struct{}{}
	defer func() { <-m.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if main && codec == protocol.CodecH265 {
		args = append(args, m.Pipeline().DecodeInputArgs()...)
	}
	limit := main
	args = append(args, "-rtsp_transport", "tcp", "-timeout", "8000000", "-i", rtspURL, "-frames:v", "1", "-an")
	if limit {
		args = append(args, "-vf", "scale='min(1920,iw)':-2")
	}
	args = append(args, "-q:v", "4", "-f", "image2", "-c:v", "mjpeg", "pipe:1")
	cmd := exec.CommandContext(ctx, m.ffmpeg, args...)
	hideWindow(cmd)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, ffError(err, stderr.String())
	}
	if out.Len() < 100 {
		return nil, fmt.Errorf("empty snapshot")
	}
	return out.Bytes(), nil
}

// Forget drops cached snapshots of a camera.
func (m *Manager) Forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.snaps {
		if strings.HasPrefix(k, id+"/") {
			delete(m.snaps, k)
		}
	}
	delete(m.online, id)
}
