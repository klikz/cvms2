// Package stream delivers hub frames to browsers via WebRTC or, as a
// fallback, fragmented MP4 over WebSocket (MSE).
package stream

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	pmedia "github.com/pion/webrtc/v4/pkg/media"

	"cvms/server/internal/config"
	"cvms/server/internal/hub"
	"cvms/shared/media"
)

// WebRTC creates peer connections that send one H.264 track each.
type WebRTC struct {
	api       *webrtc.API
	iceServer []webrtc.ICEServer
}

const (
	h264Fmtp = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"
	h265Fmtp = "level-id=93;profile-id=1;tier-flag=0;tx-mode=SRST"
)

// OfferHasH265 reports whether a browser SDP offer can receive H.265.
func OfferHasH265(offer string) bool {
	return strings.Contains(strings.ToUpper(offer), "H265/90000")
}

// NewWebRTC sets up the shared WebRTC API (single UDP port, optional ICE-TCP).
func NewWebRTC(cfg config.WebRTC) (*WebRTC, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: h264Fmtp,
			RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}}},
		PayloadType: 102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH265, ClockRate: 90000, SDPFmtpLine: h265Fmtp,
			RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}}},
		PayloadType: 104,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, err
	}
	se := webrtc.SettingEngine{}
	nets := []webrtc.NetworkType{}
	var udpMux ice.UDPMux
	if cfg.UDPPort > 0 {
		mux, err := ice.NewMultiUDPMuxFromPort(cfg.UDPPort)
		if err != nil {
			return nil, fmt.Errorf("webrtc udp port %d: %w", cfg.UDPPort, err)
		}
		udpMux = mux
		se.SetICEUDPMux(mux)
		nets = append(nets, webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6)
	} else {
		nets = append(nets, webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6)
	}
	if cfg.TCPPort > 0 {
		ln, err := net.ListenTCP("tcp", &net.TCPAddr{Port: cfg.TCPPort})
		if err != nil {
			if udpMux != nil {
				udpMux.Close()
			}
			return nil, fmt.Errorf("webrtc tcp port %d: %w", cfg.TCPPort, err)
		}
		se.SetICETCPMux(webrtc.NewICETCPMux(nil, ln, 8))
		nets = append(nets, webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6)
	}
	se.SetNetworkTypes(nets)
	if len(cfg.PublicIPs) > 0 {
		se.SetNAT1To1IPs(cfg.PublicIPs, webrtc.ICECandidateTypeHost)
	}
	se.SetIncludeLoopbackCandidate(true)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se))
	w := &WebRTC{api: api}
	for _, s := range cfg.STUNServers {
		if s = strings.TrimSpace(s); s != "" {
			w.iceServer = append(w.iceServer, webrtc.ICEServer{URLs: []string{s}})
		}
	}
	return w, nil
}

// Answer negotiates a peer connection for the browser offer and starts
// sending frames from v. onEnd is called once when the session ends.
func (w *WebRTC) Answer(offer string, h *hub.Hub, v *hub.Viewer, onEnd func()) (string, error) {
	pc, err := w.api.NewPeerConnection(webrtc.Configuration{ICEServers: w.iceServer})
	if err != nil {
		return "", err
	}
	capab := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: h264Fmtp}
	if v.Codec == "h265" {
		capab = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH265, ClockRate: 90000, SDPFmtpLine: h265Fmtp}
	}
	track, err := webrtc.NewTrackLocalStaticSample(capab, "video", "cvms")
	if err != nil {
		pc.Close()
		return "", err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		pc.Close()
		return "", err
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()

	end := make(chan struct{})
	var endOnce sync.Once
	finish := func() { endOnce.Do(func() { close(end) }) }
	connected := make(chan struct{}, 1)
	var (
		graceMu sync.Mutex
		grace   *time.Timer
	)
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		graceMu.Lock()
		defer graceMu.Unlock()
		if grace != nil && s != webrtc.PeerConnectionStateDisconnected {
			grace.Stop()
			grace = nil
		}
		switch s {
		case webrtc.PeerConnectionStateConnected:
			select {
			case connected <- struct{}{}:
			default:
			}
		case webrtc.PeerConnectionStateDisconnected:
			// often transient (packet loss, Wi-Fi hiccup): ICE may recover,
			// so give it time before tearing the session down
			if grace == nil {
				grace = time.AfterFunc(10*time.Second, finish)
			}
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			finish()
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		pc.Close()
		return "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return "", err
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return "", err
	}
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
	}
	ld := pc.LocalDescription()
	if ld == nil {
		pc.Close()
		return "", errors.New("no local description")
	}

	go func() {
		defer func() {
			pc.Close()
			h.Unsubscribe(v)
			if onEnd != nil {
				onEnd()
			}
		}()
		timeout := time.NewTimer(20 * time.Second)
		select {
		case <-connected:
			timeout.Stop()
		case <-timeout.C:
			return
		case <-end:
			return
		case <-v.Done():
			return
		}
		var last int64
		for {
			select {
			case <-end:
				return
			case <-v.Done():
				return
			case f := <-v.Frames():
				d := 40 * time.Millisecond
				if last != 0 {
					if dd := time.Duration(f.TS-last) * time.Microsecond; dd > 0 && dd < time.Second {
						d = dd
					}
				}
				last = f.TS
				if err := track.WriteSample(pmedia.Sample{Data: media.JoinAnnexB(f.AU), Duration: d}); err != nil {
					log.Printf("webrtc write: %v", err)
					return
				}
			}
		}
	}()
	return ld.SDP, nil
}
