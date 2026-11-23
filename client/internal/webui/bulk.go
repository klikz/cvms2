package webui

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cvms/client/internal/onvif"
)

// ---------------------------------------------------------------- bulk add

type bulkDevice struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Name string `json:"name"`
}

type bulkResult struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	OK       bool   `json:"ok"`
	Code     string `json:"code,omitempty"`
	Error    string `json:"error,omitempty"`
	CameraID string `json:"camera_id,omitempty"`
	Name     string `json:"name,omitempty"`
	Warning  string `json:"warning,omitempty"`
}

// handleBulk adds several ONVIF cameras with shared credentials:
// probe → pick main/sub streams → detect codecs → save. 4 run in parallel.
func (s *Server) handleBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string       `json:"username"`
		Password string       `json:"password"`
		Devices  []bulkDevice `json:"devices"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Devices) == 0 || len(req.Devices) > 256 {
		apiError(w, http.StatusBadRequest, "bad_request", "1-256 devices required")
		return
	}
	results := make([]bulkResult, len(req.Devices))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, d := range req.Devices {
		wg.Add(1)
		go func(i int, d bulkDevice) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = s.bulkOne(r.Context(), d, req.Username, req.Password)
		}(i, d)
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

func (s *Server) bulkOne(ctx context.Context, d bulkDevice, user, pass string) bulkResult {
	res := bulkResult{Host: strings.TrimSpace(d.Host), Port: d.Port}
	if res.Port == 0 {
		res.Port = 80
	}
	if net.ParseIP(res.Host) == nil && res.Host == "" {
		res.Code, res.Error = "bad_request", "host is required"
		return res
	}
	if s.onvifAdded()[onvifKey(res.Host, res.Port)] {
		res.Code, res.Error = "camera_exists", "this camera is already added"
		return res
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	info, err := onvif.NewClient(res.Host, res.Port, user, pass).Probe(pctx)
	cancel()
	if err != nil {
		res.Code, res.Error = "onvif_failed", err.Error()
		if errors.Is(err, onvif.ErrAuth) {
			res.Code = "onvif_auth"
		}
		return res
	}
	main, sub := onvif.PickStreams(info.Profiles)
	name := strings.TrimSpace(d.Name)
	if name == "" {
		name = strings.TrimSpace(info.Manufacturer + " " + info.Model)
	}
	if name == "" {
		name = res.Host
	}
	p := pass
	cam, probeErr, err := s.saveCamera(ctx, "", cameraReq{
		Name: name, Host: res.Host, ONVIFPort: res.Port, Username: user, Password: &p,
		Manufacturer: info.Manufacturer, Model: info.Model,
		MainURL: main.URI, SubURL: sub.URI, MainSnapshot: main.SnapshotURI, SubSnapshot: sub.SnapshotURI,
	})
	if err != nil {
		var se *saveError
		if errors.As(err, &se) {
			res.Code = se.code
		}
		res.Error = err.Error()
		return res
	}
	res.OK, res.CameraID, res.Name, res.Warning = true, cam.ID, cam.Name, probeErr
	return res
}

func onvifKey(host string, port int) string {
	if port == 0 {
		port = 80
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// onvifAdded returns host:port of cameras added through ONVIF. Several
// devices may share one IP (NVRs, encoders), so the ONVIF port counts too.
func (s *Server) onvifAdded() map[string]bool {
	out := map[string]bool{}
	for _, c := range s.store.Get().Cameras {
		if c.ONVIFPort > 0 {
			out[onvifKey(c.Host, c.ONVIFPort)] = true
		}
	}
	return out
}

// ---------------------------------------------------------------- IP range scan

const maxScanHosts = 1024

// parseRange accepts "10.0.0.5", "10.0.0.10-50", "10.0.0.10-10.0.1.20"
// or "10.0.0.0/24" and returns the IPv4 addresses.
func parseRange(s string) ([]net.IP, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty range")
	}
	toU := func(ip net.IP) uint32 { return binary.BigEndian.Uint32(ip.To4()) }
	fromU := func(u uint32) net.IP {
		b := make(net.IP, 4)
		binary.BigEndian.PutUint32(b, u)
		return b
	}
	var lo, hi uint32
	switch {
	case strings.Contains(s, "/"):
		_, n, err := net.ParseCIDR(s)
		if err != nil || n.IP.To4() == nil {
			return nil, fmt.Errorf("invalid range %q", s)
		}
		ones, _ := n.Mask.Size()
		lo = toU(n.IP)
		hi = lo | (1<<(32-ones) - 1)
		if ones <= 30 {
			lo, hi = lo+1, hi-1 // skip network and broadcast
		}
	case strings.Contains(s, "-"):
		a, b, _ := strings.Cut(s, "-")
		start := net.ParseIP(strings.TrimSpace(a)).To4()
		if start == nil {
			return nil, fmt.Errorf("invalid range %q", s)
		}
		lo = toU(start)
		b = strings.TrimSpace(b)
		if end := net.ParseIP(b).To4(); end != nil {
			hi = toU(end)
		} else if n, err := strconv.Atoi(b); err == nil && n >= 0 && n <= 255 {
			hi = lo&^0xff | uint32(n)
		} else {
			return nil, fmt.Errorf("invalid range %q", s)
		}
	default:
		ip := net.ParseIP(s).To4()
		if ip == nil {
			return nil, fmt.Errorf("invalid IP %q", s)
		}
		lo, hi = toU(ip), toU(ip)
	}
	if hi < lo {
		return nil, fmt.Errorf("invalid range %q", s)
	}
	if hi-lo+1 > maxScanHosts {
		return nil, fmt.Errorf("range too large (max %d addresses)", maxScanHosts)
	}
	out := make([]net.IP, 0, hi-lo+1)
	for u := lo; ; u++ {
		out = append(out, fromU(u))
		if u == hi {
			break
		}
	}
	return out, nil
}

type scanHit struct {
	IP    string `json:"ip"`
	Port  int    `json:"port"`
	ONVIF bool   `json:"onvif"`
	RTSP  bool   `json:"rtsp"`
	Added bool   `json:"added"`
}

// handleScan probes an IP range for ONVIF (HTTP port) and RTSP (554).
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Range string `json:"range"`
		Port  int    `json:"port"`
	}
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	ips, err := parseRange(req.Range)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_range", err.Error())
		return
	}
	if req.Port <= 0 || req.Port > 65535 {
		req.Port = 80
	}
	added := s.onvifAdded()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	open := func(ip string, port int) bool {
		d := net.Dialer{Timeout: 700 * time.Millisecond}
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	hits := make([]*scanHit, len(ips))
	sem := make(chan struct{}, 64)
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			h := &scanHit{IP: ip, Port: req.Port, Added: added[onvifKey(ip, req.Port)]}
			if open(ip, req.Port) {
				pctx, pcancel := context.WithTimeout(ctx, 3*time.Second)
				h.ONVIF = onvif.NewClient(ip, req.Port, "", "").Ping(pctx)
				pcancel()
			}
			h.RTSP = open(ip, 554)
			if h.ONVIF || h.RTSP {
				hits[i] = h
			}
		}(i, ip.String())
	}
	wg.Wait()
	out := []*scanHit{}
	for _, h := range hits {
		if h != nil {
			out = append(out, h)
		}
	}
	writeJSON(w, out)
}
