package hub

import (
	"encoding/json"
	"testing"
	"time"

	"cvms/server/internal/config"
	"cvms/server/internal/db"
	"cvms/shared/ffmpegx"
	"cvms/shared/media"
	"cvms/shared/protocol"
)

func testHub(t *testing.T, fallback string) (*Hub, *conn) {
	t.Helper()
	cfg := config.Default()
	cfg.H265Fallback = fallback
	h := New(nil, cfg)
	h.pipe, h.pipeDone = ffmpegx.Pipeline{Name: "CPU", Encoder: "libx264"}, true
	c := &conn{clientID: 1, status: protocol.StatusApproved, out: make(chan outMsg, 100), done: make(chan struct{})}
	h.conns[1] = c
	return h, c
}

func nextStart(t *testing.T, c *conn) protocol.StreamStart {
	t.Helper()
	select {
	case m := <-c.out:
		var env protocol.Envelope
		_ = json.Unmarshal(m.data, &env)
		if env.Type != protocol.TypeStreamStart {
			t.Fatalf("want stream_start, got %s", env.Type)
		}
		var s protocol.StreamStart
		_ = json.Unmarshal(env.Data, &s)
		return s
	case <-time.After(time.Second):
		t.Fatal("no stream_start sent")
	}
	return protocol.StreamStart{}
}

var h265Key = media.AU{{0x40, 0x01, 1}, {0x42, 0x01, 1}, {0x44, 0x01, 1}, {0x26, 0x01, 0x80, 5}} // VPS SPS PPS IDR

func h265Cam() *db.Camera {
	return &db.Camera{ID: 7, ClientID: 1, CamUID: "cam", Main: protocol.StreamInfo{Available: true, Codec: "h265"}, Sub: protocol.StreamInfo{Available: true, Codec: "h265"}}
}

func TestH265PassthroughAndLazyTranscoder(t *testing.T) {
	h, c := testHub(t, "server")
	v1, _ := h.Subscribe(h265Cam(), "sub", true)
	if v1.Codec != "h265" {
		t.Fatalf("browser with H.265 should get h265, got %s", v1.Codec)
	}
	st := nextStart(t, c)
	if st.Codec != "" {
		t.Fatalf("native upstream expected, got %q", st.Codec)
	}
	h.onStreamInfo(c, protocol.StreamInfoMsg{StreamID: st.StreamID, Codec: "h265"})
	if v1.s.run.tc != nil || v1.s.run.tcStarting {
		t.Fatal("transcoder must not run without H.264 viewers")
	}
	h.onVideo(c, protocol.BinFrame{Type: protocol.BinVideo, ID: st.StreamID, TS: 1, Payload: media.JoinAnnexB(h265Key)})
	select {
	case f := <-v1.Frames():
		if f.Codec != "h265" || !f.Key || f.VPS == nil {
			t.Fatalf("bad frame %+v", f)
		}
	case <-time.After(time.Second):
		t.Fatal("h265 viewer got no frame")
	}

	v2, _ := h.Subscribe(h265Cam(), "sub", false)
	if v2.Codec != "h264" || v2.s != v1.s {
		t.Fatalf("H.264 viewer should share the native upstream: %s", v2.Codec)
	}
	v1.s.mu.Lock()
	starting := v1.s.run.tc != nil || v1.s.run.tcStarting
	v1.s.mu.Unlock()
	if !starting {
		t.Fatal("transcoder should start for the H.264 viewer")
	}
	h.Unsubscribe(v2)
	time.Sleep(200 * time.Millisecond) // let startTranscoder settle
	v1.s.mu.Lock()
	tc := v1.s.run.tc
	v1.s.mu.Unlock()
	if tc != nil {
		t.Fatal("transcoder must stop when the last H.264 viewer leaves")
	}
	h.Unsubscribe(v1)
}

func TestClientFallbackUsesSeparateUpstream(t *testing.T) {
	h, c := testHub(t, "client")
	v, _ := h.Subscribe(h265Cam(), "main", false)
	st := nextStart(t, c)
	if st.Codec != "h264" || v.Codec != "h264" {
		t.Fatalf("client fallback should request h264 from client: %+v", st)
	}
	h.onStreamInfo(c, protocol.StreamInfoMsg{StreamID: st.StreamID, Codec: "h264", Transcoded: true})
	if v.s.run.tc != nil || v.s.run.tcStarting {
		t.Fatal("server must not transcode in client fallback mode")
	}
	v2, _ := h.Subscribe(h265Cam(), "main", true)
	if v2.s == v.s {
		t.Fatal("H.265 viewer should use the native upstream")
	}
	if nextStart(t, c).Codec != "" {
		t.Fatal("native upstream expected")
	}
}
