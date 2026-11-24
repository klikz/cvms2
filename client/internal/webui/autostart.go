package webui

import (
	"errors"
	"log"
	"net/http"
	"path/filepath"

	"cvms/client/internal/config"
	"cvms/shared/svc"
)

// SetService sets the OS service description used for autostart.
func (s *Server) SetService(spec svc.Spec) { s.spec = spec }

// pinFFmpeg stores the absolute ffmpeg path in the config: a service runs
// under a system account that may not see the user's PATH.
func (s *Server) pinFFmpeg() {
	if s.store.Get().FFmpegPath != "" {
		return
	}
	p := s.cams.FFmpeg()
	if !filepath.IsAbs(p) {
		return
	}
	if err := s.store.Update(func(c *config.Config) error { c.FFmpegPath = p; return nil }); err != nil {
		log.Printf("save config: %v", err)
	}
}

// EnsureAutostart applies the default "autostart: true" at startup.
func (s *Server) EnsureAutostart() {
	if s.spec.Name == "" {
		return
	}
	want := s.store.Get().Autostart
	st, err := svc.EnsureAutostart(s.spec, want)
	switch {
	case errors.Is(err, svc.ErrNeedAdmin):
		log.Printf("autostart: cannot install the service without administrator rights; run once as administrator or use `install`")
	case err != nil:
		log.Printf("autostart: %v", err)
	case want && st.Installed && st.Enabled:
		s.pinFFmpeg()
	}
}

func (s *Server) autostartResponse() map[string]any {
	return map[string]any{"status": svc.AutoStatusOf(s.spec), "wanted": s.store.Get().Autostart}
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
	if err := s.store.Update(func(c *config.Config) error { c.Autostart = req.Enabled; return nil }); err != nil {
		apiError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if req.Enabled {
		s.pinFFmpeg()
	}
	writeJSON(w, s.autostartResponse())
}
