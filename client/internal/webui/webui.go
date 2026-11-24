// Package webui serves the client's local web UI and its JSON API.
package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/crypto/bcrypt"

	"cvms/client/internal/cameras"
	"cvms/client/internal/config"
	"cvms/client/internal/onvif"
	"cvms/client/internal/streamurl"
	"cvms/client/internal/tunnel"
	"cvms/shared/ffmpegx"
	"cvms/shared/loginlimit"
	"cvms/shared/protocol"
	"cvms/shared/svc"
)

// Server is the local web UI server.
type Server struct {
	store   *config.Store
	cams    *cameras.Manager
	tun     *tunnel.Tunnel
	web     fs.FS
	version string
	limiter *loginlimit.Limiter

	mu       sync.Mutex
	sessions map[string]time.Time
	spec     svc.Spec
}

// New creates the UI server.
func New(store *config.Store, cams *cameras.Manager, tun *tunnel.Tunnel, web fs.FS, version string) *Server {
	c := store.Get()
	block := time.Duration(c.UI.LoginBlockMinutes) * time.Minute
	return &Server{
		store: store, cams: cams, tun: tun, web: web, version: version,
		limiter:  loginlimit.New(c.UI.LoginMaxAttempts, block, block),
		sessions: map[string]time.Time{},
	}
}

// HashPassword hashes a UI password.
func HashPassword(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(b), err
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			next.ServeHTTP(w, r)
		})
	})
	r.Route("/api", func(r chi.Router) {
		r.Post("/login", s.handleLogin)
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"version": s.version}) })
		r.Group(func(r chi.Router) {
			r.Use(s.auth(true))
			r.Post("/logout", s.handleLogout)
			r.Get("/me", s.handleMe)
			r.Post("/me/password", s.handlePassword)
		})
		r.Group(func(r chi.Router) {
			r.Use(s.auth(false))
			r.Get("/status", s.handleStatus)
			r.Get("/settings", s.handleSettings)
			r.Put("/settings", s.handleSettingsSave)
			r.Post("/server/disconnect", s.handleServerDisconnect)
			r.Get("/cameras", s.handleCameras)
			r.Post("/cameras", s.handleCameraSave)
			r.Put("/cameras/{id}", s.handleCameraSave)
			r.Delete("/cameras/{id}", s.handleCameraDelete)
			r.Get("/cameras/{id}/snapshot", s.handleSnapshot)
			r.Get("/autostart", s.handleAutostart)
			r.Put("/autostart", s.handleAutostartSet)
			r.Post("/cameras/bulk", s.handleBulk)
			r.Post("/scan", s.handleScan)
			r.Post("/rtsp/find", s.handleRTSPFind)
			r.Post("/nvr/channels", s.handleNVRChannels)
			r.Post("/nvr/add", s.handleNVRAdd)
			r.Post("/discover", s.handleDiscover)
			r.Post("/onvif/probe", s.handleONVIFProbe)
			r.Post("/rtsp/probe", s.handleRTSPProbe)
		})
	})
	static := http.FileServer(http.FS(s.web))
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})
	return r
}

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, code, msg string, vars ...map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	body := map[string]any{"error": msg, "code": code}
	if len(vars) > 0 {
		body["vars"] = vars[0]
	}
	_ = json.NewEncoder(w).Encode(body)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func remoteIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

var okResp = map[string]bool{"ok": true}

// ---------------------------------------------------------------- auth

const cookieName = "cvms_client_sid"

func (s *Server) auth(allowMustChange bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(cookieName)
			if err != nil {
				apiError(w, http.StatusUnauthorized, "unauthorized", "not logged in")
				return
			}
			idle := time.Duration(s.store.Get().UI.SessionIdleMinutes) * time.Minute
			s.mu.Lock()
			last, ok := s.sessions[c.Value]
			if ok && time.Since(last) > idle {
				delete(s.sessions, c.Value)
				ok = false
			}
			if ok {
				s.sessions[c.Value] = time.Now()
			}
			s.mu.Unlock()
			if !ok {
				apiError(w, http.StatusUnauthorized, "session_expired", "session expired")
				return
			}
			if !allowMustChange && s.store.Get().UI.MustChange {
				apiError(w, http.StatusForbidden, "must_change", "password change required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	key := remoteIP(r) + "|" + strings.ToLower(req.Username)
	if blocked, left := s.limiter.Blocked(key); blocked {
		n := int(math.Ceil(left.Minutes()))
		apiError(w, http.StatusTooManyRequests, "login_blocked", fmt.Sprintf("too many attempts, try again in %d min", n), map[string]any{"n": n})
		return
	}
	ui := s.store.Get().UI
	if !strings.EqualFold(req.Username, ui.Username) || bcrypt.CompareHashAndPassword([]byte(ui.PasswordHash), []byte(req.Password)) != nil {
		if s.limiter.Fail(key) {
			log.Printf("login blocked for %s", key)
		}
		apiError(w, http.StatusUnauthorized, "bad_credentials", "invalid username or password")
		return
	}
	s.limiter.Success(key)
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[tok] = time.Now()
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	s.handleMe(w, r)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, okResp)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c := s.store.Get()
	writeJSON(w, map[string]any{"username": c.UI.Username, "must_change": c.UI.MustChange, "version": s.version, "role": "admin"})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct{ Old, New string }
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	ui := s.store.Get().UI
	if bcrypt.CompareHashAndPassword([]byte(ui.PasswordHash), []byte(req.Old)) != nil {
		apiError(w, http.StatusBadRequest, "wrong_password", "current password is wrong")
		return
	}
	if len(req.New) < 6 {
		apiError(w, http.StatusBadRequest, "weak_password", "password must be at least 6 characters")
		return
	}
	if req.New == req.Old {
		apiError(w, http.StatusBadRequest, "same_password", "new password must differ")
		return
	}
	h, err := HashPassword(req.New)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if err := s.store.Update(func(c *config.Config) error { c.UI.PasswordHash, c.UI.MustChange = h, false; return nil }); err != nil {
		apiError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// drop other sessions
	cur, _ := r.Cookie(cookieName)
	s.mu.Lock()
	for k := range s.sessions {
		if cur == nil || k != cur.Value {
			delete(s.sessions, k)
		}
	}
	s.mu.Unlock()
	s.handleMe(w, r)
}

// ---------------------------------------------------------------- status / settings

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	online := 0
	for _, c := range cfg.Cameras {
		if s.cams.Online(c.ID) {
			online++
		}
	}
	writeJSON(w, map[string]any{
		"version":           s.version,
		"name":              cfg.Name,
		"client_uid":        cfg.ClientUID,
		"server":            s.tun.Status(),
		"cameras":           len(cfg.Cameras),
		"cameras_online":    online,
		"ffmpeg":            ffmpegx.Available(s.cams.FFmpeg()),
		"ffmpeg_path":       s.cams.FFmpeg(),
		"config_path":       s.store.Path(),
		"snapshot_interval": cfg.SnapshotInterval,
		"pipeline":          s.cams.Pipeline(),
	})
}

type settingsDTO struct {
	Name                string `json:"name"`
	ServerURL           string `json:"server_url"`
	SnapshotInterval    int    `json:"snapshot_interval"`
	StatusInterval      int    `json:"status_interval"`
	SnapshotConcurrency int    `json:"snapshot_concurrency"`
	HWAccel             string `json:"hwaccel"`
	GPUDevice           string `json:"gpu_device"`
	H264Encoder         string `json:"h264_encoder"`
	FFmpegPath          string `json:"ffmpeg_path"`
	UIListen            string `json:"ui_listen"`
	SessionIdle         int    `json:"session_idle_minutes"`
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	c := s.store.Get()
	writeJSON(w, settingsDTO{Name: c.Name, ServerURL: c.ServerURL, SnapshotInterval: c.SnapshotInterval, StatusInterval: c.StatusInterval,
		SnapshotConcurrency: c.SnapshotConcurrency, HWAccel: c.HWAccel, GPUDevice: c.GPUDevice,
		H264Encoder: c.H264Encoder, FFmpegPath: c.FFmpegPath, UIListen: c.UI.Listen, SessionIdle: c.UI.SessionIdleMinutes})
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	var d settingsDTO
	if err := readJSON(r, &d); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	d.ServerURL = strings.TrimRight(strings.TrimSpace(d.ServerURL), "/")
	if d.ServerURL != "" {
		if _, err := tunnel.WSURL(d.ServerURL); err != nil {
			apiError(w, http.StatusBadRequest, "bad_server_url", err.Error())
			return
		}
		if !strings.Contains(d.ServerURL, "://") {
			d.ServerURL = "http://" + d.ServerURL
		}
	}
	if strings.TrimSpace(d.Name) == "" || len(d.Name) > 100 {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid name")
		return
	}
	if d.UIListen != "" {
		if _, _, err := net.SplitHostPort(d.UIListen); err != nil {
			apiError(w, http.StatusBadRequest, "bad_listen", "invalid listen address (host:port)")
			return
		}
		if d.UIListen != s.store.Get().UI.Listen {
			ln, err := net.Listen("tcp", d.UIListen)
			if err != nil {
				apiError(w, http.StatusConflict, "port_busy", "port is already in use: "+d.UIListen)
				return
			}
			ln.Close()
		}
	}
	var serverChanged, nameChanged, restart bool
	err := s.store.Update(func(c *config.Config) error {
		serverChanged = c.ServerURL != d.ServerURL
		nameChanged = c.Name != strings.TrimSpace(d.Name)
		if serverChanged {
			c.ServerToken = "" // new server: needs a new approval
		}
		c.Name = strings.TrimSpace(d.Name)
		c.ServerURL = d.ServerURL
		if d.SnapshotInterval >= 1 && d.SnapshotInterval <= 3600 {
			c.SnapshotInterval = d.SnapshotInterval
		}
		if d.StatusInterval >= 5 && d.StatusInterval <= 3600 {
			c.StatusInterval = d.StatusInterval
		}
		switch d.HWAccel {
		case "auto", "cuda", "qsv", "vaapi", "d3d11va", "none":
			if c.HWAccel != d.HWAccel {
				restart = true
			}
			c.HWAccel = d.HWAccel
		}
		if strings.TrimSpace(d.GPUDevice) != c.GPUDevice {
			c.GPUDevice = strings.TrimSpace(d.GPUDevice)
			restart = true
		}
		if d.SnapshotConcurrency >= 1 && d.SnapshotConcurrency <= 64 && d.SnapshotConcurrency != c.SnapshotConcurrency {
			c.SnapshotConcurrency = d.SnapshotConcurrency
			restart = true
		}
		if d.H264Encoder != "" {
			if d.H264Encoder != c.H264Encoder {
				restart = true
			}
			c.H264Encoder = d.H264Encoder
		}
		c.FFmpegPath = strings.TrimSpace(d.FFmpegPath)
		if d.UIListen != "" && d.UIListen != c.UI.Listen {
			c.UI.Listen = d.UIListen
			restart = true
		}
		if d.SessionIdle >= 5 && d.SessionIdle <= 1440 {
			c.UI.SessionIdleMinutes = d.SessionIdle
		}
		return nil
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if serverChanged || nameChanged {
		s.tun.Reconnect()
	}
	writeJSON(w, map[string]any{"ok": true, "restart_required": restart})
}

// handleServerDisconnect drops the connection to the current server and
// forgets it (address and approval token), so the client stays offline until
// a new address is saved.
func (s *Server) handleServerDisconnect(w http.ResponseWriter, r *http.Request) {
	err := s.store.Update(func(c *config.Config) error {
		c.ServerURL, c.ServerToken = "", ""
		return nil
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.tun.Reconnect()
	writeJSON(w, okResp)
}

// ---------------------------------------------------------------- cameras

type cameraDTO struct {
	config.Camera
	Password    string `json:"password,omitempty"`
	HasPassword bool   `json:"has_password"`
	Online      bool   `json:"online"`
}

func (s *Server) handleCameras(w http.ResponseWriter, r *http.Request) {
	cams := s.store.Get().Cameras
	out := make([]cameraDTO, 0, len(cams))
	for _, c := range cams {
		out = append(out, cameraDTO{Camera: c, HasPassword: c.Password != "", Online: s.cams.Online(c.ID)})
	}
	writeJSON(w, out)
}

type cameraReq struct {
	Name         string  `json:"name"`
	Host         string  `json:"host"`
	ONVIFPort    int     `json:"onvif_port"`
	Username     string  `json:"username"`
	Password     *string `json:"password"`
	Manufacturer string  `json:"manufacturer"`
	Model        string  `json:"model"`
	MainURL      string  `json:"main_url"`
	SubURL       string  `json:"sub_url"`
	MainSnapshot string  `json:"main_snapshot"`
	SubSnapshot  string  `json:"sub_snapshot"`
	Enabled      *bool   `json:"enabled"`
}

// saveError carries an API error code.
type saveError struct {
	status int
	code   string
	msg    string
}

func (e *saveError) Error() string { return e.msg }

func (s *Server) handleCameraSave(w http.ResponseWriter, r *http.Request) {
	var req cameraReq
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	cam, probeErr, err := s.saveCamera(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		var se *saveError
		if errors.As(err, &se) {
			apiError(w, se.status, se.code, se.msg)
			return
		}
		apiError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.tun.CamerasChanged()
	writeJSON(w, map[string]any{"camera": cameraDTO{Camera: cam, HasPassword: cam.Password != ""}, "probe_error": probeErr})
}

// saveCamera validates, probes codecs and stores a camera (new when id is
// empty). It is shared by single and bulk add.
func (s *Server) saveCamera(ctx context.Context, id string, req cameraReq) (config.Camera, string, error) {
	var existing config.Camera
	if id != "" {
		var ok bool
		if existing, ok = s.store.Camera(id); !ok {
			return config.Camera{}, "", &saveError{http.StatusNotFound, "not_found", "camera not found"}
		}
	}
	cam := existing
	cam.Name = strings.TrimSpace(req.Name)
	cam.Username = strings.TrimSpace(req.Username)
	if req.Password != nil {
		cam.Password = *req.Password
	}
	cam.ONVIFPort = req.ONVIFPort
	if req.Manufacturer != "" || req.Model != "" {
		cam.Manufacturer, cam.Model = req.Manufacturer, req.Model
	}
	if req.Enabled != nil {
		cam.Enabled = *req.Enabled
	} else if id == "" {
		cam.Enabled = true
	}
	rawMain, _ := streamurl.Normalize(req.MainURL)
	rawSub, _ := streamurl.Normalize(req.SubURL)
	mainURL, user, pass := splitCreds(rawMain)
	subURL, _, _ := splitCreds(rawSub)
	if user != "" && cam.Username == "" {
		cam.Username, cam.Password = user, pass
	}
	if mainURL == "" {
		return config.Camera{}, "", &saveError{http.StatusBadRequest, "main_url_required", "main stream URL is required"}
	}
	if !strings.HasPrefix(strings.ToLower(mainURL), "rtsp://") && !strings.HasPrefix(strings.ToLower(mainURL), "rtsps://") {
		return config.Camera{}, "", &saveError{http.StatusBadRequest, "bad_rtsp_url", "stream URL must start with rtsp://"}
	}
	if cam.Main.URL != mainURL {
		cam.Main = config.Stream{URL: mainURL}
	}
	if cam.Sub.URL != subURL {
		cam.Sub = config.Stream{URL: subURL}
	}
	if req.MainSnapshot != "" || cam.Main.SnapshotURL == "" {
		cam.Main.SnapshotURL = strings.TrimSpace(req.MainSnapshot)
	}
	if req.SubSnapshot != "" || cam.Sub.SnapshotURL == "" {
		cam.Sub.SnapshotURL = strings.TrimSpace(req.SubSnapshot)
	}
	if cam.Host == "" || req.Host != "" {
		cam.Host = strings.TrimSpace(req.Host)
	}
	if cam.Host == "" {
		if u, err := url.Parse(mainURL); err == nil {
			cam.Host = u.Hostname()
		}
	}
	if cam.Name == "" {
		cam.Name = cam.Host
	}
	config.NormalizeCamera(&cam)
	// detect codecs (best effort, short timeout)
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	probeErr := ""
	if cam.Main.Codec == "" {
		if res, err := s.cams.Probe(ctx, config.WithCredentials(cam.Main.URL, cam.Username, cam.Password)); err == nil {
			cam.Main.Codec, cam.Main.Width, cam.Main.Height = res.Codec, res.Width, res.Height
		} else {
			probeErr = err.Error()
		}
	}
	if cam.Sub.URL != "" && cam.Sub.Codec == "" && probeErr == "" {
		if res, err := s.cams.Probe(ctx, config.WithCredentials(cam.Sub.URL, cam.Username, cam.Password)); err == nil {
			cam.Sub.Codec, cam.Sub.Width, cam.Sub.Height = res.Codec, res.Width, res.Height
		}
	}
	if id == "" {
		cam.ID = config.NewCameraID()
	}
	err := s.store.Update(func(c *config.Config) error {
		if id == "" {
			for _, x := range c.Cameras {
				if x.Main.URL == cam.Main.URL {
					return fmt.Errorf("duplicate")
				}
			}
			c.Cameras = append(c.Cameras, cam)
			return nil
		}
		for i := range c.Cameras {
			if c.Cameras[i].ID == id {
				c.Cameras[i] = cam
				return nil
			}
		}
		return fmt.Errorf("not found")
	})
	if err != nil {
		if err.Error() == "duplicate" {
			return config.Camera{}, "", &saveError{http.StatusConflict, "camera_exists", "this camera is already added"}
		}
		return config.Camera{}, "", err
	}
	s.cams.Forget(cam.ID)
	s.cams.Kick()
	return cam, probeErr, nil
}

// splitCreds removes user:pass@ from an RTSP URL and returns them.
func splitCreds(raw string) (clean, user, pass string) {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw, "", ""
	}
	user = u.User.Username()
	pass, _ = u.User.Password()
	u.User = nil
	return u.String(), user, pass
}

func (s *Server) handleCameraDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	found := false
	err := s.store.Update(func(c *config.Config) error {
		out := c.Cameras[:0]
		for _, x := range c.Cameras {
			if x.ID == id {
				found = true
				continue
			}
			out = append(out, x)
		}
		c.Cameras = out
		return nil
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !found {
		apiError(w, http.StatusNotFound, "not_found", "camera not found")
		return
	}
	s.cams.Forget(id)
	s.tun.CamerasChanged()
	writeJSON(w, okResp)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	profile := r.URL.Query().Get("profile")
	maxAge, _ := strconv.Atoi(r.URL.Query().Get("max_age"))
	if maxAge <= 0 {
		maxAge = s.store.Get().SnapshotInterval
	}
	if profile == protocol.ProfileMain && r.URL.Query().Get("max_age") == "" {
		maxAge = 1
	}
	data, err := s.cams.Snapshot(r.Context(), id, profile, time.Duration(maxAge)*time.Second)
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "snapshot_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// ---------------------------------------------------------------- discovery / probing

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	devs, err := onvif.Discover(r.Context(), 3*time.Second)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "discover_failed", err.Error())
		return
	}
	added := s.onvifAdded()
	type devDTO struct {
		onvif.Device
		Added bool `json:"added"`
	}
	out := make([]devDTO, 0, len(devs))
	for _, d := range devs {
		out = append(out, devDTO{Device: d, Added: added[onvifKey(d.IP, d.Port)]})
	}
	writeJSON(w, out)
}

func (s *Server) handleONVIFProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		CameraID string `json:"camera_id"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Host) == "" {
		apiError(w, http.StatusBadRequest, "bad_request", "host is required")
		return
	}
	if req.Password == "" && req.CameraID != "" {
		if c, ok := s.store.Camera(req.CameraID); ok {
			req.Password = c.Password
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cl := onvif.NewClient(strings.TrimSpace(req.Host), req.Port, req.Username, req.Password)
	info, err := cl.Probe(ctx)
	if err != nil {
		code := "onvif_failed"
		if err == onvif.ErrAuth {
			code = "onvif_auth"
		}
		apiError(w, http.StatusBadGateway, code, err.Error())
		return
	}
	main, sub := onvif.PickStreams(info.Profiles)
	writeJSON(w, map[string]any{"info": info, "main": main, "sub": sub})
}

func (s *Server) handleRTSPProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		CameraID string `json:"camera_id"`
	}
	if err := readJSON(r, &req); err != nil || req.URL == "" {
		apiError(w, http.StatusBadRequest, "bad_request", "url is required")
		return
	}
	if req.Password == "" && req.CameraID != "" {
		if c, ok := s.store.Camera(req.CameraID); ok {
			req.Password = c.Password
		}
	}
	clean, u, p := splitCreds(req.URL)
	if req.Username == "" {
		req.Username, req.Password = u, p
	}
	res, err := s.cams.Probe(r.Context(), config.WithCredentials(clean, req.Username, req.Password))
	if err != nil {
		apiError(w, http.StatusBadGateway, "rtsp_failed", err.Error())
		return
	}
	writeJSON(w, res)
}
