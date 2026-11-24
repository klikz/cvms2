package nvr

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// hikvision speaks ISAPI (XML over HTTP).
type hikvision struct{ conn }

func (h *hikvision) Vendor() string { return Hikvision }

func (h *hikvision) getXML(ctx context.Context, path string, v any) error {
	b, err := h.get(ctx, path)
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnsupported, path, err)
	}
	return nil
}

func (h *hikvision) DeviceInfo(ctx context.Context) (DeviceInfo, error) {
	var d struct {
		Name     string `xml:"deviceName"`
		Model    string `xml:"model"`
		Serial   string `xml:"serialNumber"`
		Firmware string `xml:"firmwareVersion"`
	}
	err := h.getXML(ctx, "/ISAPI/System/deviceInfo", &d)
	return DeviceInfo{
		Name: strings.TrimSpace(d.Name), Model: strings.TrimSpace(d.Model),
		Serial: strings.TrimSpace(d.Serial), Firmware: strings.TrimSpace(d.Firmware),
	}, err
}

type hikProxyChannels struct {
	Channels []struct {
		ID     int    `xml:"id"`
		Name   string `xml:"name"`
		Source struct {
			IP   string `xml:"ipAddress"`
			Port int    `xml:"managePortNo"`
		} `xml:"sourceInputPortDescriptor"`
	} `xml:"InputProxyChannel"`
}

type hikProxyStatus struct {
	Status []struct {
		ID     int    `xml:"id"`
		Online string `xml:"online"`
	} `xml:"InputProxyChannelStatus"`
}

type hikAnalogChannels struct {
	Channels []struct {
		ID      int    `xml:"id"`
		Name    string `xml:"name"`
		Enabled string `xml:"videoInputEnabled"`
	} `xml:"VideoInputChannel"`
}

// Channels lists IP channels (InputProxy) and, on DVRs, analog inputs.
func (h *hikvision) Channels(ctx context.Context) ([]Channel, error) {
	var ip hikProxyChannels
	ipErr := h.getXML(ctx, "/ISAPI/ContentMgmt/InputProxy/channels", &ip)
	if errors.Is(ipErr, ErrAuth) || errors.Is(ipErr, ErrUnreachable) || ctx.Err() != nil {
		return nil, ipErr
	}
	online := map[int]bool{}
	if ipErr == nil {
		var st hikProxyStatus
		if err := h.getXML(ctx, "/ISAPI/ContentMgmt/InputProxy/channels/status", &st); err == nil {
			for _, s := range st.Status {
				online[s.ID] = strings.EqualFold(strings.TrimSpace(s.Online), "true")
			}
		}
	}
	var an hikAnalogChannels
	anErr := h.getXML(ctx, "/ISAPI/System/Video/inputs/channels", &an)
	if ipErr != nil && anErr != nil {
		return nil, ipErr
	}

	seen := map[int]bool{}
	var out []Channel
	for _, a := range an.Channels {
		if a.ID <= 0 || strings.EqualFold(strings.TrimSpace(a.Enabled), "false") {
			continue
		}
		seen[a.ID] = true
		out = append(out, Channel{ID: a.ID, Name: strings.TrimSpace(a.Name), Online: true, Analog: true})
	}
	for _, p := range ip.Channels {
		if p.ID <= 0 || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		out = append(out, Channel{
			ID: p.ID, Name: strings.TrimSpace(p.Name),
			IP: strings.TrimSpace(p.Source.IP), Port: p.Source.Port,
			Online: online[p.ID],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (h *hikvision) RTSPPort(ctx context.Context) int {
	var l struct {
		Protocols []struct {
			Protocol string `xml:"protocol"`
			Port     int    `xml:"portNo"`
		} `xml:"AdminAccessProtocol"`
	}
	if err := h.getXML(ctx, "/ISAPI/Security/adminAccess", &l); err == nil {
		for _, p := range l.Protocols {
			if strings.EqualFold(strings.TrimSpace(p.Protocol), "RTSP") && p.Port > 0 {
				return p.Port
			}
		}
	}
	return DefaultRTSPPort
}

func (h *hikvision) stream(rtspPort, ch, n int) string {
	return fmt.Sprintf("rtsp://%s/Streaming/Channels/%d%02d", hostPort(h.host, rtspPort, DefaultRTSPPort), ch, n)
}

func (h *hikvision) MainURL(rtspPort, ch int) string { return h.stream(rtspPort, ch, 1) }
func (h *hikvision) SubURL(rtspPort, ch int) string  { return h.stream(rtspPort, ch, 2) }

func (h *hikvision) SnapshotURL(ch int) string {
	return fmt.Sprintf("http://%s/ISAPI/Streaming/channels/%d01/picture", hostPort(h.host, h.port, defaultHTTPPort), ch)
}
