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
	"cvms/shared/protocol"
)

// Errors returned to API callers.
var (
	ErrClientOffline = errors.New("client offline")
	ErrTimeout       = errors.New("timeout")
	ErrNoCamera      = errors.New("camera not found")
)

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
//
// Locking: hub.mu guards the maps (conns, streams, byID, snapshots) and
// conn.status. Each stream has its own mutex for its viewers and run, so
// frame delivery never takes hub.mu. Order: hub.mu before stream.mu.
type Hub struct {
	db     *db.DB
	cfg    *config.Config
	Events *Events
	ffmpeg string

	pipeMu   sync.Mutex
	pipe     ffmpegx.Pipeline
	pipeDone bool

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
	return &Hub{
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
}

// FFmpeg returns the resolved ffmpeg path.
func (h *Hub) FFmpeg() string { return h.ffmpeg }

// Pipeline returns the decode/encode pipeline used for H.265 -> H.264.
func (h *Hub) Pipeline() ffmpegx.Pipeline {
	h.pipeMu.Lock()
	defer h.pipeMu.Unlock()
	if h.pipeDone { // fixed pipeline (tests)
		return h.pipe
	}
	// ffmpegx caches the result and re-checks a CPU fallback later, so a GPU
	// whose driver was not ready at boot is picked up without a restart.
	return ffmpegx.PickPipeline(h.ffmpeg, h.cfg.HWAccel, h.cfg.H264Encoder, h.cfg.GPUDevice)
}

// Run runs background maintenance until ctx is done.
func (h *Hub) Run(ctx context.Context) {
	if ffmpegx.Available(h.ffmpeg) {
		go func() {
			log.Printf("H.265 -> H.264 fallback pipeline: %s (fallback location: %s)", h.Pipeline().Name, h.cfg.H265Fallback)
		}()
	} else {
		log.Printf("WARNING: ffmpeg not found (%s); browsers without H.265 support need h265_fallback: client", h.ffmpeg)
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
		s.mu.Lock()
		n := len(s.viewers)
		running := s.run != nil
		stale := running && now.Sub(s.lastFrame) > 20*time.Second
		retry := !running && now.Sub(s.lastAttempt) >= 5*time.Second
		s.mu.Unlock()
		if n == 0 {
			continue
		}
		if stale {
			log.Printf("stream cam=%d %s: no data, restarting", s.key.cam, s.key.profile)
			h.stopLocked(s, true)
		} else if retry {
			h.startLocked(s)
		}
	}
	ttl := time.Duration(h.cfg.SnapshotCacheSeconds) * time.Second * 6
	for k, e := range h.snapCache {
		if now.Sub(e.at) > ttl {
			delete(h.snapCache, k)
		}
	}
}

// Stats returns live counters for the UI.
func (h *Hub) Stats() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := map[string]int{"streams": 0, "viewers": 0, "transcodes": 0, "clients_connected": len(h.conns)}
	for _, s := range h.streams {
		s.mu.Lock()
		if s.run != nil {
			st["streams"]++
			if s.run.tc != nil {
				st["transcodes"]++
			}
		}
		st["viewers"] += len(s.viewers)
		s.mu.Unlock()
	}
	return st
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
	h.dropStreamsLocked(func(s *stream) bool { return s.clientID == clientID })
	h.mu.Unlock()
	h.Events.PublishDebounced("clients")
	return nil
}

// Delete removes a client entirely.
func (h *Hub) Delete(clientID int64) error {
	h.mu.Lock()
	h.dropStreamsLocked(func(s *stream) bool { return s.clientID == clientID })
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
		if e, ok := h.snapCache[key]; ok {
			h.mu.Unlock()
			return e.data, nil
		}
		h.mu.Unlock()
		return nil, ErrClientOffline
	}
	id := h.newIDLocked()
	ch := make(chan snapResult, 1)
	h.snaps[id] = ch
	call := &snapCall{done: make(chan struct{})}
	h.snapInflight[key] = call
	c.sendJSON(protocol.TypeSnapshotReq, protocol.SnapshotReq{ReqID: id, CameraID: cam.CamUID, Profile: profile})
	h.mu.Unlock()

	var res snapResult
	timer := time.NewTimer(20 * time.Second)
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

func (h *Hub) newIDLocked() uint32 {
	h.nextID++
	if h.nextID == 0 {
		h.nextID = 1
	}
	return h.nextID
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
		// The client lost or never received its token (config reset,
		// disconnect/reconnect, reinstall). Rejecting forever would leave it
		// stuck; instead drop the approval and ask the admin to approve again.
		audit("client_unauthorized", cl.Name, ip)
		log.Printf("client %s presented an invalid token; approval reset to pending", cl.Name)
		if err := h.db.SetClientStatus(cl.ID, protocol.StatusPending, ""); err != nil {
			log.Printf("reset client status: %v", err)
			c.close()
			return
		}
		cl.Status, cl.Token = protocol.StatusPending, ""
		c.status = protocol.StatusPending
		h.Events.PublishDebounced("clients")
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
	h.Events.Publish("client_status", map[string]any{"client_id": cl.ID, "online": true, "last_seen": db.Now()})

	defer func() {
		c.close()
		h.mu.Lock()
		current := h.conns[cl.ID] == c
		if current {
			delete(h.conns, cl.ID)
			for _, s := range h.streams {
				if s.clientID == cl.ID {
					h.stopLocked(s, false)
				}
			}
		}
		h.mu.Unlock()
		if !current {
			return
		}
		_ = h.db.SetClientCamerasOffline(cl.ID)
		_ = h.db.TouchClient(cl.ID)
		log.Printf("client disconnected: %s", cl.Name)
		h.Events.Publish("client_status", map[string]any{"client_id": cl.ID, "online": false, "last_seen": db.Now()})
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
		h.mu.Lock()
		h.dropStreamsLocked(func(s *stream) bool {
			if s.clientID != c.clientID {
				return false
			}
			_, ok := ids[s.camUID]
			return !ok
		})
		h.mu.Unlock()
		h.Events.PublishDebounced("clients")
	case protocol.TypeCameraStatus:
		var m protocol.CameraStatus
		if json.Unmarshal(env.Data, &m) != nil {
			return
		}
		id, err := h.db.SetCameraOnline(c.clientID, m.ID, m.Online)
		if err == nil && id > 0 {
			h.Events.Publish("camera_status", map[string]any{"camera_id": id, "client_id": c.clientID, "online": m.Online})
		}
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
