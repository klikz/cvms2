package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"cvms/server/internal/db"
)

const sessionCookie = "cvms_sid"

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSession
)

func userFrom(r *http.Request) *db.User {
	u, _ := r.Context().Value(ctxUser).(*db.User)
	return u
}

func sessionFrom(r *http.Request) string {
	s, _ := r.Context().Value(ctxSession).(string)
	return s
}

// HashPassword hashes a password with bcrypt.
func HashPassword(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(b), err
}

func checkPassword(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

// authMiddleware loads the session. allowMustChange lets users with a
// pending forced password change through (only for /me endpoints).
func (s *Server) authMiddleware(allowMustChange bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(sessionCookie)
			if err != nil || c.Value == "" {
				apiError(w, http.StatusUnauthorized, "unauthorized", "not logged in")
				return
			}
			sess, err := s.db.SessionByToken(c.Value)
			if err != nil {
				apiError(w, http.StatusUnauthorized, "unauthorized", "session expired")
				return
			}
			idle := time.Duration(s.cfg.SessionIdleMinutes) * time.Minute
			last := time.UnixMilli(sess.LastSeen)
			if time.Since(last) > idle {
				_ = s.db.DeleteSession(sess.Token)
				apiError(w, http.StatusUnauthorized, "session_expired", "session expired")
				return
			}
			u, err := s.db.UserByID(sess.UserID)
			if err != nil || u.Disabled {
				_ = s.db.DeleteSession(sess.Token)
				apiError(w, http.StatusUnauthorized, "unauthorized", "not logged in")
				return
			}
			if u.MustChange && !allowMustChange {
				apiError(w, http.StatusForbidden, "must_change", "password change required")
				return
			}
			if time.Since(last) > 30*time.Second && !strings.HasSuffix(r.URL.Path, "/events") {
				_ = s.db.TouchSession(sess.Token)
			}
			ctx := context.WithValue(r.Context(), ctxUser, u)
			ctx = context.WithValue(ctx, ctxSession, sess.Token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := userFrom(r); u == nil || !u.IsAdmin() {
			apiError(w, http.StatusForbidden, "forbidden", "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, tok string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge:   maxAge,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	ip := s.clientIP(r)
	key := ip + "|" + strings.ToLower(strings.TrimSpace(req.Username))
	if blocked, left := s.limiter.Blocked(key); blocked {
		mins := int(math.Ceil(left.Minutes()))
		apiError(w, http.StatusTooManyRequests, "login_blocked", fmt.Sprintf("too many attempts, try again in %d min", mins), map[string]any{"n": mins})
		return
	}
	u, err := s.db.UserByName(strings.TrimSpace(req.Username))
	if err != nil || u.Disabled || !checkPassword(u.PasswordHash, req.Password) {
		if err == nil && u.Disabled {
			s.audit(u, ip, "login_failed", u.Username, "disabled")
		} else {
			s.audit(nil, ip, "login_failed", req.Username, "")
		}
		if s.limiter.Fail(key) {
			s.audit(nil, ip, "login_blocked", req.Username, "")
		}
		apiError(w, http.StatusUnauthorized, "bad_credentials", "invalid username or password")
		return
	}
	s.limiter.Success(key)
	tok, err := s.db.CreateSession(u.ID, ip)
	if err != nil {
		internalError(w, err)
		return
	}
	_ = s.db.SetUserLogin(u.ID)
	s.setSessionCookie(w, r, tok, 0)
	s.audit(u, ip, "login", u.Username, "")
	writeJSON(w, s.meResponse(u))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	_ = s.db.DeleteSession(sessionFrom(r))
	s.setSessionCookie(w, r, "", -1)
	s.auditReq(r, "logout", userFrom(r).Username, "")
	writeJSON(w, okResp)
}

func (s *Server) meResponse(u *db.User) map[string]any {
	return map[string]any{
		"id":                  u.ID,
		"username":            u.Username,
		"role":                u.Role,
		"lang":                u.Lang,
		"must_change":         u.MustChange,
		"version":             s.version,
		"viewer_idle_minutes": s.cfg.ViewerIdleMinutes,
	}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.meResponse(userFrom(r)))
}

func (s *Server) handleMyPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	u := userFrom(r)
	if !checkPassword(u.PasswordHash, req.Old) {
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
		internalError(w, err)
		return
	}
	u.PasswordHash, u.MustChange = h, false
	if err := s.db.UpdateUser(u); err != nil {
		internalError(w, err)
		return
	}
	_ = s.db.DeleteUserSessions(u.ID, sessionFrom(r))
	s.auditReq(r, "password_change", u.Username, "")
	writeJSON(w, s.meResponse(u))
}

func (s *Server) handleMyLang(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lang string `json:"lang"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Lang) > 8 {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	u := userFrom(r)
	u.Lang = req.Lang
	if err := s.db.UpdateUser(u); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, okResp)
}
