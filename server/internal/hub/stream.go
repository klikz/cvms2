package hub

import (
	"log"
	"sync"
	"time"

	"cvms/server/internal/db"
	"cvms/shared/media"
	"cvms/shared/protocol"
)

// Frame is one access unit delivered to viewers.
type Frame struct {
	Codec string // h264 | h265
	AU    media.AU
	TS    int64 // microseconds
	Key   bool
	VPS   []byte
	SPS   []byte
	PPS   []byte
}

// Viewer receives frames of one stream in one codec.
type Viewer struct {
	ch      chan Frame
	done    chan struct{}
	once    sync.Once
	needKey bool // guarded by stream.mu
	s       *stream
	Camera  *db.Camera
	Profile string
	// Codec is what this viewer receives: h264 or h265.
	Codec string
}

// Frames returns the frame channel.
func (v *Viewer) Frames() <-chan Frame { return v.ch }

// Done is closed when the hub drops the viewer.
func (v *Viewer) Done() <-chan struct{} { return v.done }

func (v *Viewer) close() { v.once.Do(func() { close(v.done) }) }

// push is called with stream.mu held.
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

// streamKey identifies one upstream from a client. Codec is "" for the
// camera's native codec or "h264" when the client transcodes.
type streamKey struct {
	cam     int64
	profile string
	codec   string
}

// run is one upstream session (stream_start .. stop).
type run struct {
	id         uint32
	codec      string // codec pushed by the client
	transcoded bool   // the client transcoded
	tc         *transcoder
	tcStarting bool
	inParams   media.ParamCache // H.265 input (conn goroutine only)
	out264     media.ParamCache // H.264 output (single producer)
	out265     media.ParamCache // H.265 passthrough (conn goroutine only)
}

type stream struct {
	key      streamKey
	clientID int64
	camUID   string

	mu          sync.Mutex
	viewers     map[*Viewer]struct{}
	n264        int // viewers that want H.264
	run         *run
	lastFrame   time.Time
	lastAttempt time.Time
	lastErr     string
	idleTimer   *time.Timer
}

// Subscribe adds a viewer. h265OK tells whether the browser can decode
// H.265; if the camera sends H.265 and the browser cannot, the viewer gets
// H.264 transcoded on the server or on the client (h265_fallback).
func (h *Hub) Subscribe(cam *db.Camera, profile string, h265OK bool) (*Viewer, error) {
	if profile != protocol.ProfileMain {
		profile = protocol.ProfileSub
	}
	native := cam.Main.Codec
	if profile == protocol.ProfileSub && cam.Sub.Available {
		native = cam.Sub.Codec
	}
	want := protocol.CodecH264
	key := streamKey{cam: cam.ID, profile: profile}
	if native == protocol.CodecH265 {
		if h265OK {
			want = protocol.CodecH265
		} else if h.cfg.H265Fallback == "client" {
			key.codec = protocol.CodecH264
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.streams[key]
	if s == nil {
		s = &stream{key: key, clientID: cam.ClientID, camUID: cam.CamUID, viewers: map[*Viewer]struct{}{}}
		h.streams[key] = s
	}
	v := &Viewer{ch: make(chan Frame, 90), done: make(chan struct{}), needKey: true, s: s, Camera: cam, Profile: profile, Codec: want}
	s.mu.Lock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	s.viewers[v] = struct{}{}
	if want == protocol.CodecH264 {
		s.n264++
	}
	running := s.run != nil
	h.ensureTranscoderLocked(s)
	s.mu.Unlock()
	if !running {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.viewers[v]; !ok {
		return
	}
	delete(s.viewers, v)
	if v.Codec == protocol.CodecH264 {
		s.n264--
	}
	h.ensureTranscoderLocked(s)
	if len(s.viewers) > 0 {
		return
	}
	idle := time.Duration(h.cfg.StreamIdleSeconds) * time.Second
	s.idleTimer = time.AfterFunc(idle, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		s.mu.Lock()
		empty := len(s.viewers) == 0
		s.mu.Unlock()
		if empty && h.streams[s.key] == s {
			h.stopLocked(s, true)
			delete(h.streams, s.key)
		}
	})
}

// StreamInfo returns the pushed codec, whether H.264 is produced by
// transcoding for this viewer, and the last error.
func (h *Hub) StreamInfo(v *Viewer) (codec string, transcoded bool, lastErr string) {
	s := v.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.run; r != nil {
		return r.codec, r.transcoded || (r.codec == protocol.CodecH265 && v.Codec == protocol.CodecH264), s.lastErr
	}
	return "", false, s.lastErr
}

// dropStreamsLocked stops matching streams and closes their viewers.
func (h *Hub) dropStreamsLocked(match func(*stream) bool) {
	for k, s := range h.streams {
		if !match(s) {
			continue
		}
		h.stopLocked(s, true)
		s.mu.Lock()
		for v := range s.viewers {
			v.close()
		}
		if s.idleTimer != nil {
			s.idleTimer.Stop()
		}
		s.mu.Unlock()
		delete(h.streams, k)
	}
}

func (h *Hub) startClientStreamsLocked(clientID int64) {
	for _, s := range h.streams {
		if s.clientID != clientID {
			continue
		}
		s.mu.Lock()
		need := s.run == nil && len(s.viewers) > 0
		s.mu.Unlock()
		if need {
			h.startLocked(s)
		}
	}
}

// startLocked asks the client to push the stream. Requires hub.mu.
func (h *Hub) startLocked(s *stream) {
	s.mu.Lock()
	s.lastAttempt = time.Now()
	s.mu.Unlock()
	c := h.conns[s.clientID]
	if c == nil || c.status != protocol.StatusApproved {
		return
	}
	r := &run{id: h.newIDLocked()}
	r.out264.Codec = protocol.CodecH264
	r.out265.Codec = protocol.CodecH265
	r.inParams.Codec = protocol.CodecH265
	s.mu.Lock()
	s.run = r
	s.lastFrame = time.Now()
	s.mu.Unlock()
	h.byID[r.id] = s
	c.sendJSON(protocol.TypeStreamStart, protocol.StreamStart{StreamID: r.id, CameraID: s.camUID, Profile: s.key.profile, Codec: s.key.codec})
}

// stopLocked ends the current run. Requires hub.mu.
func (h *Hub) stopLocked(s *stream, notify bool) {
	s.mu.Lock()
	r := s.run
	s.run = nil
	if r != nil && r.tc != nil {
		r.tc.Close()
		r.tc = nil
	}
	s.mu.Unlock()
	if r == nil {
		return
	}
	delete(h.byID, r.id)
	if notify {
		if c := h.conns[s.clientID]; c != nil {
			c.sendJSON(protocol.TypeStreamStop, protocol.StreamStop{StreamID: r.id})
		}
	}
}

// ensureTranscoderLocked starts the server-side H.265 -> H.264 transcoder
// only while at least one H.264 viewer watches an H.265 run, and stops it
// when the last one leaves. Requires stream.mu.
func (h *Hub) ensureTranscoderLocked(s *stream) {
	r := s.run
	if r == nil || r.codec != protocol.CodecH265 {
		return
	}
	switch {
	case s.n264 > 0 && r.tc == nil && !r.tcStarting:
		r.tcStarting = true
		go h.startTranscoder(s, r)
	case s.n264 == 0 && r.tc != nil:
		r.tc.Close()
		r.tc = nil
	}
}

func (h *Hub) startTranscoder(s *stream, r *run) {
	pipe := h.Pipeline()
	tc, err := newTranscoder(h.ffmpeg, pipe, func(au media.AU) {
		h.deliver(s, r, protocol.CodecH264, au, time.Now().UnixMicro())
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	r.tcStarting = false
	if err != nil {
		log.Printf("transcoder start: %v", err)
		s.lastErr = "transcoder: " + err.Error()
		return
	}
	if s.run != r || s.n264 == 0 {
		tc.Close()
		return
	}
	r.tc = tc
}

func (h *Hub) lookup(c *conn, id uint32) (*stream, *run) {
	h.mu.Lock()
	s := h.byID[id]
	h.mu.Unlock()
	if s == nil || s.clientID != c.clientID {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil || s.run.id != id {
		return nil, nil
	}
	return s, s.run
}

// onStreamInfo is called when the client reports the codec it pushes.
func (h *Hub) onStreamInfo(c *conn, m protocol.StreamInfoMsg) {
	s, r := h.lookup(c, m.StreamID)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != r {
		return
	}
	r.codec = m.Codec
	r.transcoded = m.Transcoded
	s.lastErr = ""
	h.ensureTranscoderLocked(s)
}

func (h *Hub) onStreamError(c *conn, m protocol.StreamError) {
	s, _ := h.lookup(c, m.StreamID)
	if s == nil {
		return
	}
	log.Printf("stream cam=%d %s error: %s", s.key.cam, s.key.profile, m.Error)
	s.mu.Lock()
	s.lastErr = m.Error
	s.mu.Unlock()
	h.mu.Lock()
	h.stopLocked(s, false)
	h.mu.Unlock()
}

// onVideo handles an access unit pushed by a client.
func (h *Hub) onVideo(c *conn, f protocol.BinFrame) {
	s, r := h.lookup(c, f.ID)
	if s == nil {
		return
	}
	s.mu.Lock()
	codec, tc := r.codec, r.tc
	s.mu.Unlock()

	au := media.AU(media.SplitNALUs(f.Payload))
	if len(au) == 0 {
		return
	}
	switch codec {
	case protocol.CodecH264:
		h.deliver(s, r, protocol.CodecH264, au, f.TS)
	case protocol.CodecH265:
		au = r.inParams.Fix(au)
		h.deliver(s, r, protocol.CodecH265, au, f.TS)
		if tc != nil {
			tc.Write(au, media.IsKeyframe(protocol.CodecH265, au))
		}
	}
}

// deliver sends an access unit to every viewer that wants this codec.
func (h *Hub) deliver(s *stream, r *run, codec string, au media.AU, ts int64) {
	pc := &r.out264
	if codec == protocol.CodecH265 {
		pc = &r.out265
	}
	au = pc.Fix(au)
	f := Frame{Codec: codec, AU: au, TS: ts, Key: media.IsKeyframe(codec, au), VPS: pc.VPS, SPS: pc.SPS, PPS: pc.PPS}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != r {
		return
	}
	s.lastFrame = time.Now()
	for v := range s.viewers {
		switch {
		case v.Codec == codec:
			v.push(f)
		case codec == protocol.CodecH264 && r.codec == protocol.CodecH264 && v.Codec == protocol.CodecH265:
			// metadata said H.265 but the camera sends H.264: let the
			// player reconnect asking for H.264
			v.close()
		}
	}
}
