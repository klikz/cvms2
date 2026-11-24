package webui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cvms/client/internal/hikvision"
)

// ---------------------------------------------------------------- Hikvision NVR

type nvrReq struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (q *nvrReq) client() *hikvision.Client {
	return hikvision.NewClient(strings.TrimSpace(q.Host), q.Port, strings.TrimSpace(q.Username), q.Password)
}

func nvrCode(err error) string {
	switch {
	case errors.Is(err, hikvision.ErrAuth):
		return "nvr_auth"
	case errors.Is(err, hikvision.ErrUnreachable), errors.Is(err, context.DeadlineExceeded):
		return "nvr_unreachable"
	case errors.Is(err, hikvision.ErrNotHikvision):
		return "nvr_not_hikvision"
	}
	return "nvr_failed"
}

type nvrChannelDTO struct {
	hikvision.Channel
	AddedNVR    bool `json:"added_nvr"`
	AddedDirect bool `json:"added_direct"`
}

// handleNVRChannels reads the device info and channel list of an NVR.
func (s *Server) handleNVRChannels(w http.ResponseWriter, r *http.Request) {
	var req nvrReq
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Host) == "" {
		apiError(w, http.StatusBadRequest, "bad_request", "host is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	c := req.client()
	chs, err := c.Channels(ctx)
	if err != nil {
		apiError(w, http.StatusBadGateway, nvrCode(err), err.Error())
		return
	}
	dev, _ := c.DeviceInfo(ctx)
	rtspPort := c.RTSPPort(ctx)

	urls, hosts := map[string]bool{}, map[string]bool{}
	for _, cam := range s.store.Get().Cameras {
		urls[cam.Main.URL] = true
		hosts[cam.Host] = true
	}
	out := make([]nvrChannelDTO, 0, len(chs))
	for _, ch := range chs {
		out = append(out, nvrChannelDTO{
			Channel:     ch,
			AddedNVR:    urls[hikvision.MainURL(c.Host, rtspPort, ch.ID)],
			AddedDirect: ch.IP != "" && hosts[ch.IP],
		})
	}
	writeJSON(w, map[string]any{"device": dev, "rtsp_port": rtspPort, "channels": out})
}

type nvrChannelReq struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// handleNVRAdd adds the selected NVR channels as cameras. Mode "nvr" pulls
// the streams through the recorder; "direct" connects to each camera's own
// address (ONVIF, then RTSP fallback) with the camera credentials.
func (s *Server) handleNVRAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		nvrReq
		RTSPPort    int             `json:"rtsp_port"`
		Mode        string          `json:"mode"`
		CamUsername string          `json:"cam_username"`
		CamPassword string          `json:"cam_password"`
		DeviceName  string          `json:"device_name"`
		Model       string          `json:"model"`
		Channels    []nvrChannelReq `json:"channels"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Host) == "" || len(req.Channels) == 0 || len(req.Channels) > 256 {
		apiError(w, http.StatusBadRequest, "bad_request", "host and 1-256 channels required")
		return
	}
	if req.Mode != "direct" {
		req.Mode = "nvr"
	}
	host := strings.TrimSpace(req.Host)
	results := make([]bulkResult, len(req.Channels))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, ch := range req.Channels {
		wg.Add(1)
		go func(i int, ch nvrChannelReq) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			name := strings.TrimSpace(ch.Name)
			if name == "" {
				name = strings.TrimSpace(fmt.Sprintf("%s CH %d", req.DeviceName, ch.ID))
			}
			var res bulkResult
			if req.Mode == "direct" {
				res = s.nvrDirect(r.Context(), ch, name, req.CamUsername, req.CamPassword)
			} else {
				res = s.nvrThrough(r.Context(), host, req.Port, req.RTSPPort, ch.ID, name, req.Username, req.Password, req.Model)
			}
			res.Host, res.Port = host, ch.ID
			results[i] = res
		}(i, ch)
	}
	wg.Wait()
	added := 0
	for _, res := range results {
		if res.OK {
			added++
		}
	}
	if added > 0 {
		s.tun.CamerasChanged()
	}
	writeJSON(w, map[string]any{"results": results, "added": added})
}

// nvrThrough saves a channel whose streams come from the recorder. ONVIFPort
// stays 0 so many channels of one NVR do not collide in onvifAdded.
func (s *Server) nvrThrough(ctx context.Context, host string, httpPort, rtspPort, ch int, name, user, pass, model string) bulkResult {
	var res bulkResult
	p := pass
	cam, probeErr, err := s.saveCamera(ctx, "", cameraReq{
		Name: name, Host: host, Username: strings.TrimSpace(user), Password: &p,
		Manufacturer: "Hikvision", Model: model,
		MainURL:      hikvision.MainURL(host, rtspPort, ch),
		SubURL:       hikvision.SubURL(host, rtspPort, ch),
		MainSnapshot: hikvision.SnapshotURL(host, httpPort, ch),
	})
	if err != nil {
		var se *saveError
		if errors.As(err, &se) {
			res.Code = se.code
		}
		res.Error = err.Error()
		return res
	}
	res.OK, res.CameraID, res.Name, res.Warning, res.Via = true, cam.ID, cam.Name, probeErr, "nvr"
	return res
}

// nvrDirect adds the camera behind a channel by its own address.
func (s *Server) nvrDirect(ctx context.Context, ch nvrChannelReq, name, user, pass string) bulkResult {
	ip := strings.TrimSpace(ch.IP)
	if ip == "" {
		return bulkResult{Code: "no_ip", Error: "channel has no camera IP address"}
	}
	return s.bulkOne(ctx, bulkDevice{Host: ip, Port: 80, Name: name}, strings.TrimSpace(user), pass)
}
