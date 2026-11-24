package api

import (
	"errors"
	"log"
	"net/http"
	"path/filepath"

	"cvms/shared/ffmpegx"
	"cvms/shared/svc"
)

// SetService sets the OS service description used for autostart.
func (s *Server) SetService(spec svc.Spec) { s.spec = spec }

// pinFFmpeg stores the absolute ffmpeg path in the config: a service runs
// under a system account that may not see the user's PATH.
func (s *Server) pinFFmpeg() {
	if s.cfg.FFmpegPath != "" {
		return
	}
	p := ffmpegx.Resolve("")
	if filepath.IsAbs(p) && ffmpegx.Available(p) {
		s.cfg.FFmpegPath = p
		if err := s.cfg.Save(); err != nil {
			log.Printf("save config: %v", err)
		}
	}
}

// EnsureAutostart applies the default "autostart: true" at startup.
func (s *Server) EnsureAutostart() {
	if s.spec.Name == "" {
		return
	}
	st, err := svc.EnsureAutostart(s.spec, s.cfg.Autostart)
	switch {
	case errors.Is(err, svc.ErrNeedAdmin):
		log.Printf("autostart: cannot install the service without administrator rights; run once as administrator or use `install`")
	case err != nil:
		log.Printf("autostart: %v", err)
	case s.cfg.Autostart && st.Installed && st.Enabled:
		s.pinFFmpeg()
	}
}

func (s *Server) autostartResponse() map[string]any {
	st := svc.AutoStatusOf(s.spec)
	return map[string]any{"status": st, "wanted": s.cfg.Autostart}
}

func (s *Server) handleAutostart(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.autostartResponse())
}

func (s *Server) handleAutostartSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	if err := svc.SetAutostart(s.spec, req.Enabled); err != nil {
		code := "autostart_failed"
		switch {
		case errors.Is(err, svc.ErrNeedAdmin):
			code = "need_admin"
		case errors.Is(err, svc.ErrDevMode):
			code = "dev_mode"
		case errors.Is(err, svc.ErrUnsupported):
			code = "unsupported"
		}
		apiError(w, http.StatusConflict, code, err.Error())
		return
	}
	s.cfg.Autostart = req.Enabled
	if req.Enabled && s.cfg.FFmpegPath == "" {
		s.pinFFmpeg() // also saves the config
	} else if err := s.cfg.Save(); err != nil {
		internalError(w, err)
		return
	}
	action := "autostart_off"
	if req.Enabled {
		action = "autostart_on"
	}
	s.auditReq(r, action, s.spec.Name, "")
	writeJSON(w, s.autostartResponse())
}
