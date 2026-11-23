// Package api is the HTTP layer of the server: auth, REST endpoints,
// WebRTC/MSE signaling, live events and the embedded web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"cvms/server/internal/config"
	"cvms/server/internal/db"
	"cvms/server/internal/hub"
	"cvms/server/internal/stream"
	"cvms/shared/loginlimit"
	"cvms/shared/protocol"
)

// Server holds API dependencies.
type Server struct {
	cfg     *config.Config
	db      *db.DB
	hub     *hub.Hub
	rtc     *stream.WebRTC
	limiter *loginlimit.Limiter
	web     fs.FS
	version string
}

// New creates the API server.
func New(cfg *config.Config, d *db.DB, h *hub.Hub, rtc *stream.WebRTC, web fs.FS, version string) *Server {
	return &Server{
		cfg:     cfg,
		db:      d,
		hub:     h,
		rtc:     rtc,
		web:     web,
		version: version,
		limiter: loginlimit.New(cfg.LoginMaxAttempts, time.Duration(cfg.LoginBlockMinutes)*time.Minute, time.Duration(cfg.LoginBlockMinutes)*time.Minute),
	}
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)

	r.Get(protocol.ClientWSPath, func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
		s.hub.HandleClientWS(w, r, ip, func(action, target, details string) {
			s.audit(nil, ip, action, target, details)
		})
	})

	r.Route("/api", func(r chi.Router) {
		r.Post("/login", s.handleLogin)
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"version": s.version}) })
		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware(true))
			r.Post("/logout", s.handleLogout)
			r.Get("/me", s.handleMe)
			r.Post("/me/password", s.handleMyPassword)
			r.Put("/me/lang", s.handleMyLang)
		})
		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware(false))
			r.Get("/info", s.handleInfo)
			r.Get("/clients", s.handleClients)
			r.Get("/cameras/{id}/snapshot", s.handleSnapshot)
			r.Post("/cameras/{id}/webrtc", s.handleWebRTC)
			r.Get("/cameras/{id}/mse", s.handleMSE)
			r.Get("/events", s.handleEvents)
			r.Post("/activity", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, okResp) })

			r.Get("/layouts", s.handleLayouts)
			r.Post("/layouts", s.handleLayoutSave)
			r.Put("/layouts/{id}", s.handleLayoutSave)
			r.Delete("/layouts/{id}", s.handleLayoutDelete)

			r.Group(func(r chi.Router) {
				r.Use(adminOnly)
				r.Put("/clients/{id}", s.handleClientRename)
				r.Post("/clients/{id}/approve", s.handleClientApprove)
				r.Post("/clients/{id}/block", s.handleClientBlock)
				r.Delete("/clients/{id}", s.handleClientDelete)
				r.Put("/cameras/{id}", s.handleCameraMeta)

				r.Get("/users", s.handleUsers)
				r.Post("/users", s.handleUserCreate)
				r.Put("/users/{id}", s.handleUserUpdate)
				r.Delete("/users/{id}", s.handleUserDelete)

				r.Get("/audit", s.handleAudit)
			})
		})
	})

	static := http.FileServer(http.FS(s.web))
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})
	return r
}

// RunMaintenance purges old sessions and audit logs periodically.
func (s *Server) RunMaintenance(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		idle := time.Duration(s.cfg.SessionIdleMinutes) * time.Minute
		_ = s.db.PurgeSessions(time.Now().Add(-idle).UnixMilli())
		if s.cfg.AuditRetentionDays > 0 {
			_ = s.db.PurgeAudit(time.Now().AddDate(0, 0, -s.cfg.AuditRetentionDays).UnixMilli())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------------------------------------------------------------- helpers

var okResp = map[string]bool{"ok": true}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// apiError writes {"error": msg, "code": code}.
func apiError(w http.ResponseWriter, status int, code, msg string, vars ...map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	body := map[string]any{"error": msg, "code": code}
	if len(vars) > 0 {
		body["vars"] = vars[0]
	}
	_ = json.NewEncoder(w).Encode(body)
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("api error: %v", err)
	apiError(w, http.StatusInternalServerError, "internal", "internal error")
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(v)
}

func idParam(r *http.Request) int64 {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			return strings.TrimSpace(strings.Split(xf, ",")[0])
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) audit(u *db.User, ip, action, target, details string) {
	e := &db.AuditEntry{IP: ip, Action: action, Target: target, Details: details}
	if u != nil {
		e.UserID, e.Username = u.ID, u.Username
	}
	if err := s.db.AddAudit(e); err != nil {
		log.Printf("audit: %v", err)
	}
}

func (s *Server) auditReq(r *http.Request, action, target, details string) {
	s.audit(userFrom(r), s.clientIP(r), action, target, details)
}

func isNotFound(err error) bool { return errors.Is(err, db.ErrNotFound) }
