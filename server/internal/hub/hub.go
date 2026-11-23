// Package hub keeps client connections, routes camera streams from clients
// to browser viewers, and handles snapshots.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"cvms/server/internal/config"
	"cvms/server/internal/db"
	"cvms/shared/ffmpegx"
	"cvms/shared/media"
	"cvms/shared/protocol"
)

// Errors returned to API callers.
var (
	ErrClientOffline = errors.New("client offline")
	ErrTimeout       = errors.New("timeout")
	ErrNoCamera      = errors.New("camera not found")
)

// Frame is one H.264 access unit delivered to viewers.
type Frame struct {
	AU  media.AU
	TS  int64 // microseconds
	Key bool
	SPS []byte
	PPS []byte
}

// Viewer receives frames of one stream.
type Viewer struct {
	ch      chan Frame
	done    chan struct{}
	once    sync.Once
	needKey bool
	s       *stream
	Camera  *db.Camera
	Profile string
}

// Frames returns the frame channel.
func (v *Viewer) Frames() <-chan Frame { return v.ch }

// Done is closed when the hub drops the viewer (camera removed, client blocked).
func (v *Viewer) Done() <-chan struct{} { return v.done }

func (v *Viewer) close() { v.once.Do(func() { close(v.done) }) }

// push is called with hub.mu held.
func (v *Viewer) push(f Frame) {
	if v.needKey {
		if !f.Key {
			return
		}
		v.needKey = false
	}
	select {
	case v.ch <- f:
	default:
		v.needKey = true // viewer too slow: drop until next keyframe
	}
}

type streamKey struct {
	cam     int64
	profile string
}

// run is one upstream session of a stream (one stream_start .. stop).
type run struct {
	id         uint32
	codec      string // codec pushed by the client
	transcoded bool   // client transcoded H.265 -> H.264
	tc         *transcoder
	inParams   media.ParamCache
	outParams  media.ParamCache
	gotKey     bool
}

type stream struct {
	key         streamKey
	clientID    int64
	camUID      string
	viewers     map[*Viewer]struct{}
	run         *run
	lastFrame   time.Time
	lastAttempt time.Time
	lastErr     string
	idleTimer   *time.Timer
}

type snapResult struct {
	data []byte
	err  error
}

type snapCall struct {
	done chan struct{}
	data []byte
	err  error
}

type snapEntry struct {
	data []byte
	at   time.Time
}

// Hub is the central router.
type Hub struct {
	db     *db.DB
	cfg    *config.Config
	Events *Events
	ffmpeg string

	mu           sync.Mutex
	conns        map[int64]*conn
	streams      map[streamKey]*stream
	byID         map[uint32]*stream
	nextID       uint32
	snaps        map[uint32]chan snapResult
	snapInflight map[string]*snapCall
	snapCache    map[string]snapEntry
}

// New creates a hub.
func New(d *db.DB, cfg *config.Config) *Hub {
	h := &Hub{
		db:           d,
		cfg:          cfg,
		Events:       NewEvents(),
		ffmpeg:       ffmpegx.Resolve(cfg.FFmpegPath),
		conns:        map[int64]*conn{},
		streams:      map[streamKey]*stream{},
		byID:         map[uint32]*stream{},
		snaps:        map[uint32]chan snapResult{},
		snapInflight: map[string]*snapCall{},
		snapCache:    map[string]snapEntry{},
	}
	return h
}

// FFmpeg returns the resolved ffmpeg path.
func (h *Hub) FFmpeg() string { return h.ffmpeg }

// Run runs background maintenance until ctx is done.
func (h *Hub) Run(ctx context.Context) {
	if ffmpegx.Available(h.ffmpeg) {
		go func() {
			enc := ffmpegx.PickEncoder(h.ffmpeg, h.cfg.H264Encoder)
			log.Printf("transcode encoder for H.265 -> H.264: %s", enc)
		}()
	} else {
		log.Printf("WARNING: ffmpeg not found (%s); H.265 cameras need transcode_mode=client on clients", h.ffmpeg)
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for _, c := range h.conns {
				c.close()
			}
			for _, s := range h.streams {
				h.stopLocked(s, false)
			}
			h.mu.Unlock()
			return
		case <-t.C:
			h.watchdog()
		}
	}
}

func (h *Hub) watchdog() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for _, s := range h.streams {
		if len(s.viewers) == 0 {
			continue
		}
		if s.run != nil && now.Sub(s.lastFrame) > 20*time.Second {
			log.Printf("stream cam=%d %s: no data, restarting", s.key.cam, s.key.profile)
			h.stopLocked(s, true)
		}
		if s.run == nil && now.Sub(s.lastAttempt) >= 5*time.Second {
			h.startLocked(s)
		}
	}
	// expire snapshot cache
	ttl := time.Duration(h.cfg.SnapshotCacheSeconds) * time.Second * 6
	for k, e := range h.snapCache {
		if now.Sub(e.at) > ttl {
			delete(h.snapCache, k)
		}
	}
}

// ---------------------------------------------------------------- client status API

// IsOnline reports whether a client is connected.
func (h *Hub) IsOnline(clientID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[clientID] != nil
}

// Approve approves a client and sends it a token if connected.
func (h *Hub) Approve(clientID int64) error {
	tok := db.RandomToken(32)
	if err := h.db.SetClientStatus(clientID, protocol.StatusApproved, tok); err != nil {
		return err
	}
	h.mu.Lock()
	if c := h.conns[clientID]; c != nil {
		c.status = protocol.StatusApproved
		c.sendJSON(protocol.TypeStatus, protocol.Status{Status: protocol.StatusApproved, Token: tok})
		h.startClientStreamsLocked(clientID)
	}
	h.mu.Unlock()
	h.Events.PublishDebounced("clients")
	return nil
}

// Block blocks a client, stops its streams and drops its viewers.
func (h *Hub) Block(clientID int64) error {
	if err := h.db.SetClientStatus(clientID, protocol.StatusBlocked, ""); err != nil {
		return err
	}
	h.mu.Lock()
	if c := h.conns[clientID]; c != nil {
		c.status = protocol.StatusBlocked
		c.sendJSON(protocol.TypeStatus, protocol.Status{Status: protocol.StatusBlocked})
	}
	h.dropClientStreamsLocked(clientID)
	h.mu.Unlock()
	h.Events.PublishDebounced("clients")
	return nil
}

// Delete removes a client entirely.
func (h *Hub) Delete(clientID int64) error {
	h.mu.Lock()
	h.dropClientStreamsLocked(clientID)
	c := h.conns[clientID]
	if c != nil {
		c.sendJSON(protocol.TypeStatus, protocol.Status{Status: protocol.StatusUnauthorized, Message: "deleted"})
	}
	h.mu.Unlock()
	if err := h.db.DeleteClient(clientID); err != nil {
		return err
	}
	if c != nil {
		time.AfterFunc(500*time.Millisecond, c.close)
	}
	h.Events.PublishDebounced("clients")
	return nil
}

func (h *Hub) dropClientStreamsLocked(clientID int64) {
	for k, s := range h.streams {
		if s.clientID != clientID {
			continue
		}
		h.stopLocked(s, true)
		for v := range s.viewers {
			v.close()
		}
		if s.idleTimer != nil {
			s.idleTimer.Stop()
		}
		delete(h.streams, k)
	}
}

func (h *Hub) startClientStreamsLocked(clientID int64) {
	for _, s := range h.streams {
		if s.clientID == clientID && s.run == nil && len(s.viewers) > 0 {
			h.startLocked(s)
		}
	}
}

// ---------------------------------------------------------------- streams

// Subscribe adds a viewer to the camera stream, starting it if needed.
func (h *Hub) Subscribe(cam *db.Camera, profile string) (*Viewer, error) {
	if profile != protocol.ProfileMain {
		profile = protocol.ProfileSub
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := streamKey{cam.ID, profile}
	s := h.streams[key]
	if s == nil {
		s = &stream{key: key, clientID: cam.ClientID, camUID: cam.CamUID, viewers: map[*Viewer]struct{}{}}
		h.streams[key] = s
	}
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	v := &Viewer{ch: make(chan Frame, 90), done: make(chan struct{}), needKey: true, s: s, Camera: cam, Profile: profile}
	s.viewers[v] = struct{}{}
	if s.run == nil {
		h.startLocked(s)
	}
	return v, nil
}

// Unsubscribe removes a viewer. The upstream stops after the idle delay.
func (h *Hub) Unsubscribe(v *Viewer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v.close()
	s := v.s
	if _, ok := s.viewers[v]; !ok {
		return
	}
	delete(s.viewers, v)
	if len(s.viewers) > 0 {
		return
	}
	idle := time.Duration(h.cfg.StreamIdleSeconds) * time.Second
	s.idleTimer = time.AfterFunc(idle, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(s.viewers) == 0 && h.streams[s.key] == s {
			h.stopLocked(s, true)
			delete(h.streams, s.key)
		}
	})
}

// StreamInfo returns the input codec and whether it is transcoded.
func (h *Hub) StreamInfo(v *Viewer) (codec string, transcoded bool, lastErr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := v.s.run; r != nil {
		return r.codec, r.codec == protocol.CodecH265 || r.transcoded, v.s.lastErr
	}
	return "", false, v.s.lastErr
}

func (h *Hub) startLocked(s *stream) {
	s.lastAttempt = time.Now()
	c := h.conns[s.clientID]
	if c == nil || c.status != protocol.StatusApproved {
		return
	}
	h.nextID++
	if h.nextID == 0 {
		h.nextID = 1
	}
	r := &run{id: h.nextID}
	r.outParams.Codec = protocol.CodecH264
	s.run = r
	s.lastFrame = time.Now()
	h.byID[r.id] = s
	c.sendJSON(protocol.TypeStreamStart, protocol.StreamStart{StreamID: r.id, CameraID: s.camUID, Profile: s.key.profile})
}

func (h *Hub) stopLocked(s *stream, notify bool) {
	r := s.run
	if r == nil {
		return
	}
	s.run = nil
	delete(h.byID, r.id)
	if r.tc != nil {
		r.tc.Close()
	}
	if notify {
		if c := h.conns[s.clientID]; c != nil {
			c.sendJSON(protocol.TypeStreamStop, protocol.StreamStop{StreamID: r.id})
		}
	}
}

// onStreamInfo is called when the client reports the codec it pushes.
func (h *Hub) onStreamInfo(c *conn, m protocol.StreamInfoMsg) {
	h.mu.Lock()
	s := h.byID[m.StreamID]
	if s == nil || s.clientID != c.clientID || s.run == nil {
		h.mu.Unlock()
		return
	}
	r := s.run
	r.codec = m.Codec
	r.transcoded = m.Transcoded
	r.inParams.Codec = m.Codec
	s.lastErr = ""
	h.mu.Unlock()

	if m.Codec == protocol.CodecH265 {
		go h.startTranscoder(s, r)
	}
}

func (h *Hub) startTranscoder(s *stream, r *run) {
	enc := ffmpegx.PickEncoder(h.ffmpeg, h.cfg.H264Encoder)
	tc, err := newTranscoder(h.ffmpeg, enc, func(au media.AU) {
		h.deliver(s, r, au, time.Now().UnixMicro())
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		log.Printf("transcoder start: %v", err)
		s.lastErr = "transcoder: " + err.Error()
		if s.run == r {
			h.stopLocked(s, true)
		}
		return
	}
	if s.run != r {
		tc.Close()
		return
	}
	r.tc = tc
}

func (h *Hub) onStreamError(c *conn, m protocol.StreamError) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.byID[m.StreamID]
	if s == nil || s.clientID != c.clientID {
		return
	}
	log.Printf("stream cam=%d %s error: %s", s.key.cam, s.key.profile, m.Error)
	s.lastErr = m.Error
	h.stopLocked(s, false)
}

// onVideo handles an access unit pushed by a client.
func (h *Hub) onVideo(c *conn, f protocol.BinFrame) {
	h.mu.Lock()
	s := h.byID[f.ID]
	if s == nil || s.clientID != c.clientID || s.run == nil {
		h.mu.Unlock()
		return
	}
	r := s.run
	codec, tc := r.codec, r.tc
	s.lastFrame = time.Now()
	h.mu.Unlock()

	au := media.AU(media.SplitNALUs(f.Payload))
	if len(au) == 0 {
		return
	}
	switch codec {
	case protocol.CodecH264:
		h.deliver(s, r, au, f.TS)
	case protocol.CodecH265:
		if tc == nil {
			return
		}
		au = r.inParams.Fix(au)
		if !r.gotKey {
			if !media.IsKeyframe(protocol.CodecH265, au) {
				return
			}
			r.gotKey = true
		}
		tc.Write(au)
	}
}

func (h *Hub) deliver(s *stream, r *run, au media.AU, ts int64) {
	au = r.outParams.Fix(au)
	f := Frame{AU: au, TS: ts, Key: media.IsKeyframe(protocol.CodecH264, au), SPS: r.outParams.SPS, PPS: r.outParams.PPS}
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.run != r {
		return
	}
	s.lastFrame = time.Now()
	for v := range s.viewers {
		v.push(f)
	}
}

// ---------------------------------------------------------------- snapshots

// Snapshot returns a JPEG of the camera, from cache when fresh.
func (h *Hub) Snapshot(ctx context.Context, cam *db.Camera, profile string) ([]byte, error) {
	if profile != protocol.ProfileMain {
		profile = protocol.ProfileSub
	}
	key := fmt.Sprintf("%d/%s", cam.ID, profile)
	fresh := time.Duration(h.cfg.SnapshotCacheSeconds) * time.Second
	h.mu.Lock()
	if e, ok := h.snapCache[key]; ok && time.Since(e.at) < fresh {
		h.mu.Unlock()
		return e.data, nil
	}
	if call := h.snapInflight[key]; call != nil {
		h.mu.Unlock()
		select {
		case <-call.done:
			return call.data, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c := h.conns[cam.ClientID]
	if c == nil || c.status != protocol.StatusApproved {
		// serve a stale snapshot if we have one
		if e, ok := h.snapCache[key]; ok {
			h.mu.Unlock()
			return e.data, nil
		}
		h.mu.Unlock()
		return nil, ErrClientOffline
	}
	h.nextID++
	id := h.nextID
	ch := make(chan snapResult, 1)
	h.snaps[id] = ch
	call := &snapCall{done: make(chan struct{})}
	h.snapInflight[key] = call
	c.sendJSON(protocol.TypeSnapshotReq, protocol.SnapshotReq{ReqID: id, CameraID: cam.CamUID, Profile: profile})
	h.mu.Unlock()

	var res snapResult
	timer := time.NewTimer(15 * time.Second)
	select {
	case res = <-ch:
	case <-timer.C:
		res.err = ErrTimeout
	case <-ctx.Done():
		res.err = ctx.Err()
	}
	timer.Stop()

	h.mu.Lock()
	delete(h.snaps, id)
	delete(h.snapInflight, key)
	if res.err == nil {
		h.snapCache[key] = snapEntry{data: res.data, at: time.Now()}
	} else if e, ok := h.snapCache[key]; ok {
		res = snapResult{data: e.data}
	}
	h.mu.Unlock()
	call.data, call.err = res.data, res.err
	close(call.done)
	return res.data, res.err
}

func (h *Hub) resolveSnapshot(id uint32, data []byte, err error) {
	h.mu.Lock()
	ch := h.snaps[id]
	h.mu.Unlock()
	if ch == nil {
		return
	}
	if data != nil {
		data = append([]byte(nil), data...)
	}
	select {
	case ch <- snapResult{data: data, err: err}:
	default:
	}
}

// ---------------------------------------------------------------- client connections

type outMsg struct {
	typ  int
	data []byte
}

type conn struct {
	h        *Hub
	ws       *websocket.Conn
	clientID int64
	name     string
	status   string // guarded by hub.mu
	out      chan outMsg
	done     chan struct{}
	once     sync.Once
}

func (c *conn) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.ws.Close()
	})
}

// sendJSON queues a control message (never blocks).
func (c *conn) sendJSON(typ string, data any) {
	b, err := protocol.Encode(typ, data)
	if err != nil {
		return
	}
	select {
	case c.out <- outMsg{websocket.TextMessage, b}:
	case <-c.done:
	default:
		log.Printf("client %d: send queue full, closing", c.clientID)
		go c.close()
	}
}

func (c *conn) writer() {
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.done:
			return
		case m := <-c.out:
			_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.ws.WriteMessage(m.typ, m.data); err != nil {
				c.close()
				return
			}
		case <-ping.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.close()
				return
			}
		}
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 16 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true }, // clients are not browsers
}

// HandleClientWS serves the WebSocket endpoint that clients connect to.
func (h *Hub) HandleClientWS(w http.ResponseWriter, r *http.Request, ip string, audit func(action, target, details string)) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(32 << 20)
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	typ, msg, err := ws.ReadMessage()
	if err != nil || typ != websocket.TextMessage {
		ws.Close()
		return
	}
	var env protocol.Envelope
	var hello protocol.Hello
	if json.Unmarshal(msg, &env) != nil || env.Type != protocol.TypeHello || json.Unmarshal(env.Data, &hello) != nil ||
		hello.ClientUID == "" || len(hello.ClientUID) > 64 {
		ws.Close()
		return
	}
	name := hello.Name
	if name == "" {
		name = hello.Hostname
	}
	cl, err := h.db.ClientByUID(hello.ClientUID)
	if errors.Is(err, db.ErrNotFound) {
		cl = &db.Client{UID: hello.ClientUID, Name: name, Hostname: hello.Hostname, OS: hello.OS, Version: hello.Version, IP: ip}
		if err := h.db.CreateClient(cl); err != nil {
			log.Printf("create client: %v", err)
			ws.Close()
			return
		}
		audit("client_register", name, ip)
		log.Printf("new client registered (pending approval): %s [%s]", name, ip)
	} else if err != nil {
		ws.Close()
		return
	} else {
		cl.Hostname, cl.OS, cl.Version, cl.IP = hello.Hostname, hello.OS, hello.Version, ip
		_ = h.db.UpdateClientSeen(cl)
	}

	c := &conn{h: h, ws: ws, clientID: cl.ID, name: cl.Name, status: cl.Status, out: make(chan outMsg, 512), done: make(chan struct{})}
	go c.writer()

	if cl.Status == protocol.StatusApproved && hello.Token != cl.Token {
		audit("client_unauthorized", cl.Name, ip)
		c.sendJSON(protocol.TypeStatus, protocol.Status{Status: protocol.StatusUnauthorized, Message: "invalid token"})
		time.AfterFunc(time.Second, c.close)
		<-c.done
		return
	}
	c.sendJSON(protocol.TypeStatus, protocol.Status{Status: cl.Status})

	h.mu.Lock()
	if old := h.conns[cl.ID]; old != nil {
		old.close()
	}
	h.conns[cl.ID] = c
	if c.status == protocol.StatusApproved {
		h.startClientStreamsLocked(cl.ID)
	}
	h.mu.Unlock()
	log.Printf("client connected: %s (%s) status=%s", cl.Name, ip, cl.Status)
	h.Events.PublishDebounced("clients")

	defer func() {
		c.close()
		h.mu.Lock()
		if h.conns[cl.ID] == c {
			delete(h.conns, cl.ID)
			for _, s := range h.streams {
				if s.clientID == cl.ID {
					h.stopLocked(s, false)
				}
			}
		}
		h.mu.Unlock()
		_ = h.db.SetClientCamerasOffline(cl.ID)
		_ = h.db.TouchClient(cl.ID)
		log.Printf("client disconnected: %s", cl.Name)
		h.Events.PublishDebounced("clients")
	}()

	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(45 * time.Second))
	})
	lastTouch := time.Now()
	for {
		_ = ws.SetReadDeadline(time.Now().Add(45 * time.Second))
		typ, msg, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if time.Since(lastTouch) > time.Minute {
			lastTouch = time.Now()
			_ = h.db.TouchClient(cl.ID)
		}
		if typ == websocket.BinaryMessage {
			f, err := protocol.DecodeBin(msg)
			if err != nil {
				continue
			}
			switch f.Type {
			case protocol.BinVideo:
				h.onVideo(c, f)
			case protocol.BinSnapshot:
				h.resolveSnapshot(f.ID, f.Payload, nil)
			}
			continue
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		h.handleControl(c, env)
	}
}

func (h *Hub) handleControl(c *conn, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeCameras:
		var m protocol.Cameras
		if json.Unmarshal(env.Data, &m) != nil {
			return
		}
		ids, err := h.db.SyncCameras(c.clientID, m.List)
		if err != nil {
			log.Printf("sync cameras: %v", err)
			return
		}
		// drop viewers of cameras the client no longer has
		h.mu.Lock()
		for k, s := range h.streams {
			if s.clientID != c.clientID {
				continue
			}
			if _, ok := ids[s.camUID]; !ok {
				h.stopLocked(s, true)
				for v := range s.viewers {
					v.close()
				}
				delete(h.streams, k)
			}
		}
		h.mu.Unlock()
		h.Events.PublishDebounced("clients")
	case protocol.TypeCameraStatus:
		var m protocol.CameraStatus
		if json.Unmarshal(env.Data, &m) != nil {
			return
		}
		_ = h.db.SetCameraOnline(c.clientID, m.ID, m.Online)
		h.Events.PublishDebounced("clients")
	case protocol.TypeStreamInfo:
		var m protocol.StreamInfoMsg
		if json.Unmarshal(env.Data, &m) == nil {
			h.onStreamInfo(c, m)
		}
	case protocol.TypeStreamError:
		var m protocol.StreamError
		if json.Unmarshal(env.Data, &m) == nil {
			h.onStreamError(c, m)
		}
	case protocol.TypeSnapshotErr:
		var m protocol.SnapshotErr
		if json.Unmarshal(env.Data, &m) == nil {
			h.resolveSnapshot(m.ReqID, nil, errors.New(m.Error))
		}
	}
}
