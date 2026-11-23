package stream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
	"github.com/gorilla/websocket"

	"cvms/server/internal/hub"
)

var mseUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 64 * 1024,
	// same-origin is enforced by the session cookie (SameSite) and the
	// Origin check below.
	CheckOrigin: sameOrigin,
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	return o == "http://"+r.Host || o == "https://"+r.Host
}

// ServeMSE upgrades to WebSocket and streams fragmented MP4: first a JSON
// text message {codec,width,height}, then the init segment, then one
// fragment per frame.
func ServeMSE(w http.ResponseWriter, r *http.Request, h *hub.Hub, v *hub.Viewer, onEnd func()) {
	ws, err := mseUpgrader.Upgrade(w, r, nil)
	if err != nil {
		h.Unsubscribe(v)
		if onEnd != nil {
			onEnd()
		}
		return
	}
	defer func() {
		ws.Close()
		h.Unsubscribe(v)
		if onEnd != nil {
			onEnd()
		}
	}()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}()

	const timescale = 90000
	var (
		inited   bool
		sps      []byte
		prev     *hub.Frame
		baseTime uint64
		seq      uint32
	)
	write := func(t int, b []byte) error {
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return ws.WriteMessage(t, b)
	}
	emit := func(f *hub.Frame, dur uint32) error {
		avcc, err := h264.AVCC(f.AU).Marshal()
		if err != nil {
			return nil
		}
		seq++
		part := fmp4.Part{
			SequenceNumber: seq,
			Tracks: []*fmp4.PartTrack{{
				ID:       1,
				BaseTime: baseTime,
				Samples:  []*fmp4.Sample{{Duration: dur, IsNonSyncSample: !f.Key, Payload: avcc}},
			}},
		}
		baseTime += uint64(dur)
		var buf seekablebuffer.Buffer
		if err := part.Marshal(&buf); err != nil {
			return err
		}
		return write(websocket.BinaryMessage, buf.Bytes())
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-closed:
			return
		case <-v.Done():
			return
		case <-ping.C:
			_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case fr := <-v.Frames():
			f := fr
			if !inited {
				if !f.Key || f.SPS == nil || f.PPS == nil {
					continue
				}
				var s h264.SPS
				if err := s.Unmarshal(f.SPS); err != nil {
					continue
				}
				sps = f.SPS
				meta, _ := json.Marshal(map[string]any{
					"codec":  fmt.Sprintf("avc1.%02x%02x%02x", sps[1], sps[2], sps[3]),
					"width":  s.Width(),
					"height": s.Height(),
				})
				if write(websocket.TextMessage, meta) != nil {
					return
				}
				init := fmp4.Init{Tracks: []*fmp4.InitTrack{{ID: 1, TimeScale: timescale, Codec: &codecs.H264{SPS: f.SPS, PPS: f.PPS}}}}
				var buf seekablebuffer.Buffer
				if err := init.Marshal(&buf); err != nil {
					return
				}
				if write(websocket.BinaryMessage, buf.Bytes()) != nil {
					return
				}
				inited = true
			}
			if prev != nil {
				dur := uint32(3600)
				if d := f.TS - prev.TS; d > 0 && d < 1_000_000 {
					dur = uint32(d * timescale / 1_000_000)
					if dur == 0 {
						dur = 1
					}
				}
				if emit(prev, dur) != nil {
					return
				}
			}
			prev = &f
		}
	}
}
