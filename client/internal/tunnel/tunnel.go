// Package tunnel keeps the outbound WebSocket connection to the server,
// handles approval, and pushes camera streams and snapshots on request.
package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"cvms/client/internal/cameras"
	"cvms/client/internal/config"
	"cvms/shared/ffmpegx"
	"cvms/shared/media"
	"cvms/shared/protocol"
)

// Connection states.
const (
	StateDisabled     = "disabled" // no server URL configured
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
)

// Status is shown in the client UI.
type Status struct {
	State      string    `json:"state"`
	Approval   string    `json:"approval"` // pending | approved | blocked | unauthorized
	ServerURL  string    `json:"server_url"`
	Error      string    `json:"error"`
	Since      time.Time `json:"since"`
	Streams    int       `json:"streams"`
	Transcodes int       `json:"transcodes"`
}

// Tunnel is the server connection manager.
type Tunnel struct {
	store   *config.Store
	cams    *cameras.Manager
	version string

	mu      sync.Mutex
	status  Status
	conn    *conn
	cancel  context.CancelFunc // aborts the current session, including a dial in progress
	reset   chan struct{}
	streams map[uint32]context.CancelFunc

	transcodes atomic.Int32
}

// New creates the tunnel.
func New(store *config.Store, cams *cameras.Manager, version string) *Tunnel {
	t := &Tunnel{store: store, cams: cams, version: version, reset: make(chan struct{}, 1), streams: map[uint32]context.CancelFunc{}}
	t.status.State = StateDisconnected
	cams.OnStatusChange(func(id string, online bool) {
		t.sendJSON(protocol.TypeCameraStatus, protocol.CameraStatus{ID: id, Online: online})
	})
	return t
}

// Status returns the current status.
func (t *Tunnel) Status() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.status
	s.Streams = len(t.streams)
	s.Transcodes = int(t.transcodes.Load())
	return s
}

func (t *Tunnel) setStatus(fn func(s *Status)) {
	t.mu.Lock()
	fn(&t.status)
	t.mu.Unlock()
}

// Reconnect drops the current connection (e.g. server URL changed).
func (t *Tunnel) Reconnect() {
	t.mu.Lock()
	c, cancel := t.conn, t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c != nil {
		c.close()
	}
	select {
	case t.reset <- struct{}{}:
	default:
	}
}

// CamerasChanged sends the updated camera list to the server.
func (t *Tunnel) CamerasChanged() {
	t.sendJSON(protocol.TypeCameras, protocol.Cameras{List: t.cams.Info()})
}

func (t *Tunnel) sendJSON(typ string, data any) {
	t.mu.Lock()
	c := t.conn
	t.mu.Unlock()
	if c != nil {
		c.sendJSON(typ, data)
	}
}

// WSURL converts an http(s) server URL into the client WebSocket URL.
func WSURL(server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", errors.New("empty server url")
	}
	if !strings.Contains(server, "://") {
		server = "http://" + server
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid server url %q", server)
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + protocol.ClientWSPath
	return u.String(), nil
}

// Run keeps connecting until ctx is done.
func (t *Tunnel) Run(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		cfg := t.store.Get()
		if strings.TrimSpace(cfg.ServerURL) == "" {
			t.setStatus(func(s *Status) { *s = Status{State: StateDisabled} })
			select {
			case <-ctx.Done():
				return
			case <-t.reset:
				continue
			}
		}
		start := time.Now()
		err := t.session(ctx, cfg)
		t.stopAllStreams()
		if ctx.Err() != nil {
			return
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		t.setStatus(func(s *Status) { s.State = StateDisconnected; s.Error = msg; s.Since = time.Now() })
		if time.Since(start) > time.Minute {
			backoff = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-t.reset:
			backoff = 2 * time.Second
		case <-time.After(backoff + time.Duration(rand.Int64N(int64(backoff/2)+1))):
			// jitter spreads reconnects of many clients after a server restart
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
}

func (t *Tunnel) session(ctx context.Context, cfg config.Config) error {
	wsURL, err := WSURL(cfg.ServerURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()
	t.setStatus(func(s *Status) { s.State = StateConnecting; s.ServerURL = cfg.ServerURL; s.Error = "" })
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, ReadBufferSize: 16 * 1024, WriteBufferSize: 256 * 1024}
	ws, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	c := &conn{ws: ws, ctrl: make(chan []byte, 256), media: make(chan []byte, 600), done: make(chan struct{})}
	go c.writer()
	defer c.close()
	go func() {
		select {
		case <-ctx.Done():
			c.close()
		case <-c.done:
		}
	}()

	host, _ := os.Hostname()
	c.sendJSON(protocol.TypeHello, protocol.Hello{
		ProtocolVersion: protocol.ProtocolVersion,
		ClientUID:       cfg.ClientUID,
		Token:           cfg.ServerToken,
		Name:            cfg.Name,
		Hostname:        host,
		OS:              runtime.GOOS + "/" + runtime.GOARCH,
		Version:         t.version,
	})
	c.sendJSON(protocol.TypeCameras, protocol.Cameras{List: t.cams.Info()})

	t.mu.Lock()
	t.conn = c
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		if t.conn == c {
			t.conn = nil
		}
		t.mu.Unlock()
	}()
	t.setStatus(func(s *Status) { s.State = StateConnected; s.Since = time.Now() })
	log.Printf("connected to server %s", cfg.ServerURL)

	ws.SetPingHandler(func(data string) error {
		_ = ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		c.sendRaw(websocket.PongMessage, []byte(data))
		return nil
	})
	for {
		_ = ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		typ, msg, err := ws.ReadMessage()
		if err != nil {
			return fmt.Errorf("connection lost: %w", err)
		}
		if typ != websocket.TextMessage {
			continue
		}
		var env protocol.Envelope
		if json.Unmarshal(msg, &env) != nil {
			continue
		}
		if err := t.handle(ctx, c, env); err != nil {
			return err
		}
	}
}

func (t *Tunnel) handle(ctx context.Context, c *conn, env protocol.Envelope) error {
	switch env.Type {
	case protocol.TypeStatus:
		var st protocol.Status
		if json.Unmarshal(env.Data, &st) != nil {
			return nil
		}
		t.setStatus(func(s *Status) { s.Approval = st.Status })
		switch st.Status {
		case protocol.StatusApproved:
			if st.Token != "" {
				if err := t.store.Update(func(cfg *config.Config) error { cfg.ServerToken = st.Token; return nil }); err != nil {
					log.Printf("save token: %v", err)
				}
				log.Printf("client approved by server")
			}
		case protocol.StatusPending:
			log.Printf("waiting for server admin approval")
		case protocol.StatusBlocked:
			log.Printf("client is blocked by server admin")
			t.stopAllStreams()
		case protocol.StatusUnauthorized:
			log.Printf("server rejected client token (%s); re-registering", st.Message)
			_ = t.store.Update(func(cfg *config.Config) error { cfg.ServerToken = ""; return nil })
			return errors.New("unauthorized: " + st.Message)
		}
	case protocol.TypeStreamStart:
		var m protocol.StreamStart
		if json.Unmarshal(env.Data, &m) == nil {
			t.startStream(ctx, c, m)
		}
	case protocol.TypeStreamStop:
		var m protocol.StreamStop
		if json.Unmarshal(env.Data, &m) == nil {
			t.stopStream(m.StreamID)
		}
	case protocol.TypeSnapshotReq:
		var m protocol.SnapshotReq
		if json.Unmarshal(env.Data, &m) == nil {
			go func() {
				data, err := t.cams.Snapshot(ctx, m.CameraID, m.Profile, time.Duration(t.store.Get().SnapshotInterval)*time.Second)
				if err != nil {
					c.sendJSON(protocol.TypeSnapshotErr, protocol.SnapshotErr{ReqID: m.ReqID, Error: err.Error()})
					return
				}
				c.sendCtrlBin(protocol.EncodeBin(protocol.BinSnapshot, m.ReqID, time.Now().UnixMicro(), data))
			}()
		}
	}
	return nil
}

// ---------------------------------------------------------------- streams

func (t *Tunnel) stopStream(id uint32) {
	t.mu.Lock()
	cancel := t.streams[id]
	delete(t.streams, id)
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (t *Tunnel) stopAllStreams() {
	t.mu.Lock()
	for id, cancel := range t.streams {
		cancel()
		delete(t.streams, id)
	}
	t.mu.Unlock()
}

func (t *Tunnel) startStream(parent context.Context, c *conn, m protocol.StreamStart) {
	t.stopStream(m.StreamID)
	ctx, cancel := context.WithCancel(parent)
	t.mu.Lock()
	t.streams[m.StreamID] = cancel
	t.mu.Unlock()
	go func() {
		defer cancel()
		err := t.pushStream(ctx, c, m)
		t.mu.Lock()
		_, active := t.streams[m.StreamID]
		delete(t.streams, m.StreamID)
		t.mu.Unlock()
		if active { // not stopped by the server: report the failure
			msg := "stream ended"
			if err != nil && !errors.Is(err, context.Canceled) {
				msg = err.Error()
			}
			c.sendJSON(protocol.TypeStreamError, protocol.StreamError{StreamID: m.StreamID, Error: msg})
		}
	}()
}

// pushStream runs ffmpeg for one stream and sends access units.
func (t *Tunnel) pushStream(ctx context.Context, c *conn, m protocol.StreamStart) error {
	cam, ok := t.store.Camera(m.CameraID)
	if !ok || !cam.Enabled {
		return errors.New("unknown camera")
	}
	cfg := t.store.Get()
	// no sub stream: make one from main, scaled down (sub_from_main)
	virtual := m.Profile == protocol.ProfileSub && cam.VirtualSub(cfg.SubFromMain)
	rtspURL, st := cam.StreamURL(m.Profile)
	if rtspURL == "" {
		return errors.New("no stream url")
	}
	codec := st.Codec
	if codec != protocol.CodecH264 && codec != protocol.CodecH265 {
		res, err := t.cams.Probe(ctx, rtspURL)
		if err != nil {
			return err
		}
		codec = res.Codec
		if codec != protocol.CodecH264 && codec != protocol.CodecH265 {
			// MJPEG/MPEG4 etc.: always convert to H.264
			codec = "other"
		}
	}
	ffmpeg := t.cams.FFmpeg()
	// The server asks for H.264 when a browser cannot play H.265
	// (h265_fallback: client); non-H.264/H.265 sources are always converted.
	transcode := virtual || codec == "other" || (codec == protocol.CodecH265 && m.Codec == protocol.CodecH264)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	var pipe ffmpegx.Pipeline
	if transcode {
		pipe = ffmpegx.PickPipeline(ffmpeg, cfg.HWAccel, cfg.H264Encoder, cfg.GPUDevice)
		args = append(args, pipe.TranscodeInputArgs()...)
		t.transcodes.Add(1)
		defer t.transcodes.Add(-1)
	}
	args = append(args, "-fflags", "nobuffer", "-flags", "low_delay", "-rtsp_transport", "tcp", "-timeout", "8000000",
		"-i", rtspURL, "-an", "-map", "0:v:0")
	outCodec := codec
	if virtual {
		args = append(args, pipe.EncodeArgsScaled(config.VirtualSubWidth, config.VirtualSubHeight)...)
		args = append(args, "-f", "h264", "pipe:1")
		outCodec = protocol.CodecH264
	} else if transcode {
		args = append(args, pipe.EncodeArgs()...)
		args = append(args, "-f", "h264", "pipe:1")
		outCodec = protocol.CodecH264
	} else if codec == protocol.CodecH265 {
		args = append(args, "-c:v", "copy", "-bsf:v", "dump_extra=freq=keyframe", "-f", "hevc", "pipe:1")
	} else {
		args = append(args, "-c:v", "copy", "-bsf:v", "dump_extra=freq=keyframe", "-f", "h264", "pipe:1")
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cameras.HideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr tailBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	src := codec
	if src == "other" {
		src = st.Codec
	}
	c.sendJSON(protocol.TypeStreamInfo, protocol.StreamInfoMsg{StreamID: m.StreamID, Codec: outCodec, SourceCodec: src, Transcoded: transcode})
	if virtual {
		log.Printf("stream %d start: %s sub from main (%s -> %s %dx%d via %s)", m.StreamID, cam.Name, src, outCodec, config.VirtualSubWidth, config.VirtualSubHeight, pipe.Name)
	} else if transcode {
		log.Printf("stream %d start: %s %s (%s -> %s via %s)", m.StreamID, cam.Name, m.Profile, src, outCodec, pipe.Name)
	} else {
		log.Printf("stream %d start: %s %s (%s)", m.StreamID, cam.Name, m.Profile, src)
	}

	r := media.NewAUReader(stdout, outCodec)
	needKey := true
	var lastAlive time.Time
	for {
		au, err := r.Next()
		if err != nil {
			break
		}
		if time.Since(lastAlive) > 10*time.Second {
			// video is flowing: keep the camera online even if a status
			// check dial fails meanwhile
			lastAlive = time.Now()
			t.cams.MarkAlive(cam.ID)
		}
		key := media.IsKeyframe(outCodec, au)
		if needKey && !key {
			continue
		}
		frame := protocol.EncodeBin(protocol.BinVideo, m.StreamID, time.Now().UnixMicro(), media.JoinAnnexB(au))
		if !c.sendMedia(frame) {
			needKey = true // queue full: skip until the next keyframe
			continue
		}
		needKey = false
	}
	werr := cmd.Wait()
	log.Printf("stream %d stop: %s %s", m.StreamID, cam.Name, m.Profile)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s := stderr.String(); s != "" {
		return errors.New(s)
	}
	if werr != nil {
		return werr
	}
	return errors.New("stream ended")
}

type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b = t.b[len(t.b)-2048:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.TrimSpace(string(t.b))
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// ---------------------------------------------------------------- connection

type conn struct {
	ws    *websocket.Conn
	ctrl  chan []byte // text JSON or prebuilt binary control frames
	media chan []byte
	done  chan struct{}
	once  sync.Once
	wmu   sync.Mutex
}

func (c *conn) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.ws.Close()
	})
}

func (c *conn) sendJSON(typ string, data any) {
	b, err := protocol.Encode(typ, data)
	if err != nil {
		return
	}
	select {
	case c.ctrl <- append([]byte{'T'}, b...):
	case <-c.done:
	case <-time.After(5 * time.Second):
		c.close()
	}
}

// sendCtrlBin queues a binary frame on the control channel (snapshots).
func (c *conn) sendCtrlBin(b []byte) {
	select {
	case c.ctrl <- append([]byte{'B'}, b...):
	case <-c.done:
	case <-time.After(5 * time.Second):
	}
}

// sendMedia queues a video frame; returns false if the queue is full.
func (c *conn) sendMedia(b []byte) bool {
	select {
	case c.media <- b:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

func (c *conn) sendRaw(typ int, data []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = c.ws.WriteMessage(typ, data)
}

func (c *conn) write(typ int, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return c.ws.WriteMessage(typ, data)
}

func (c *conn) writer() {
	for {
		// control messages first
		select {
		case <-c.done:
			return
		case b := <-c.ctrl:
			if c.writeCtrl(b) != nil {
				c.close()
				return
			}
			continue
		default:
		}
		select {
		case <-c.done:
			return
		case b := <-c.ctrl:
			if c.writeCtrl(b) != nil {
				c.close()
				return
			}
		case b := <-c.media:
			if c.write(websocket.BinaryMessage, b) != nil {
				c.close()
				return
			}
		}
	}
}

func (c *conn) writeCtrl(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if b[0] == 'B' {
		return c.write(websocket.BinaryMessage, b[1:])
	}
	return c.write(websocket.TextMessage, b[1:])
}
