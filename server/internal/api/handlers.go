package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"cvms/server/internal/db"
	"cvms/server/internal/hub"
	"cvms/server/internal/stream"
	"cvms/shared/protocol"
)

// ---------------------------------------------------------------- info

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	clients, err := s.db.Clients()
	if err != nil {
		internalError(w, err)
		return
	}
	cams, err := s.db.Cameras(0)
	if err != nil {
		internalError(w, err)
		return
	}
	u := userFrom(r)
	var allowed map[int64]bool
	if !u.IsAdmin() {
		allowed, _ = s.db.PermittedClients(u.ID)
	}
	var nClients, nOnline, nPending, nCams, nCamsOnline int
	status := map[int64]string{}
	for _, c := range clients {
		status[c.ID] = c.Status
		if c.Status == protocol.StatusPending {
			nPending++
		}
		if c.Status != protocol.StatusApproved || (allowed != nil && !allowed[c.ID]) {
			continue
		}
		nClients++
		if s.hub.IsOnline(c.ID) {
			nOnline++
		}
	}
	for _, c := range cams {
		if status[c.ClientID] != protocol.StatusApproved || (allowed != nil && !allowed[c.ClientID]) {
			continue
		}
		nCams++
		if c.Online && s.hub.IsOnline(c.ClientID) {
			nCamsOnline++
		}
	}
	out := map[string]any{
		"version":        s.version,
		"clients":        nClients,
		"clients_online": nOnline,
		"cameras":        nCams,
		"cameras_online": nCamsOnline,
	}
	if u.IsAdmin() {
		out["pending"] = nPending
		users, _ := s.db.CountUsers()
		out["users"] = users
	}
	writeJSON(w, out)
}

// ---------------------------------------------------------------- clients

type clientView struct {
	*db.Client
	Online  bool         `json:"online"`
	Cameras []*db.Camera `json:"cameras"`
}

// visibleClients returns clients the user may see; admins see all.
func (s *Server) visibleClients(u *db.User) ([]*clientView, error) {
	clients, err := s.db.Clients()
	if err != nil {
		return nil, err
	}
	cams, err := s.db.Cameras(0)
	if err != nil {
		return nil, err
	}
	var allowed map[int64]bool
	if !u.IsAdmin() {
		if allowed, err = s.db.PermittedClients(u.ID); err != nil {
			return nil, err
		}
	}
	byID := map[int64]*clientView{}
	out := []*clientView{}
	for _, c := range clients {
		if !u.IsAdmin() && (c.Status != protocol.StatusApproved || !allowed[c.ID]) {
			continue
		}
		cv := &clientView{Client: c, Online: s.hub.IsOnline(c.ID), Cameras: []*db.Camera{}}
		if !u.IsAdmin() {
			cv.IP = ""
		}
		out = append(out, cv)
		byID[c.ID] = cv
	}
	for _, cam := range cams {
		if cv := byID[cam.ClientID]; cv != nil {
			if !cv.Online {
				cam.Online = false
			}
			if !u.IsAdmin() {
				cam.Host = ""
			}
			cv.Cameras = append(cv.Cameras, cam)
		}
	}
	return out, nil
}

func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	out, err := s.visibleClients(userFrom(r))
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleClientRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" || len(req.Name) > 100 {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid name")
		return
	}
	c, err := s.db.ClientByID(idParam(r))
	if err != nil {
		apiError(w, http.StatusNotFound, "not_found", "client not found")
		return
	}
	if err := s.db.RenameClient(c.ID, strings.TrimSpace(req.Name)); err != nil {
		internalError(w, err)
		return
	}
	s.auditReq(r, "client_rename", c.Name, req.Name)
	s.hub.Events.PublishDebounced("clients")
	writeJSON(w, okResp)
}

func (s *Server) clientAction(w http.ResponseWriter, r *http.Request, action string, fn func(int64) error) {
	c, err := s.db.ClientByID(idParam(r))
	if err != nil {
		apiError(w, http.StatusNotFound, "not_found", "client not found")
		return
	}
	if err := fn(c.ID); err != nil {
		internalError(w, err)
		return
	}
	s.auditReq(r, action, c.Name, c.IP)
	writeJSON(w, okResp)
}

func (s *Server) handleClientApprove(w http.ResponseWriter, r *http.Request) {
	s.clientAction(w, r, "client_approve", s.hub.Approve)
}

func (s *Server) handleClientBlock(w http.ResponseWriter, r *http.Request) {
	s.clientAction(w, r, "client_block", s.hub.Block)
}

func (s *Server) handleClientDelete(w http.ResponseWriter, r *http.Request) {
	s.clientAction(w, r, "client_delete", s.hub.Delete)
}

// ---------------------------------------------------------------- cameras

func (s *Server) handleCameraMeta(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName string `json:"display_name"`
		Location    string `json:"location"`
	}
	if err := readJSON(r, &req); err != nil || len(req.DisplayName) > 100 || len(req.Location) > 100 {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	cam, err := s.db.CameraByID(idParam(r))
	if err != nil {
		apiError(w, http.StatusNotFound, "not_found", "camera not found")
		return
	}
	if err := s.db.UpdateCameraMeta(cam.ID, strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.Location)); err != nil {
		internalError(w, err)
		return
	}
	s.auditReq(r, "camera_edit", cam.Name, strings.TrimSpace(req.DisplayName+" "+req.Location))
	s.hub.Events.PublishDebounced("clients")
	writeJSON(w, okResp)
}

// camera loads a camera and checks the user may view it.
func (s *Server) camera(w http.ResponseWriter, r *http.Request) *db.Camera {
	cam, err := s.db.CameraByID(idParam(r))
	if err != nil || !cam.Present {
		apiError(w, http.StatusNotFound, "not_found", "camera not found")
		return nil
	}
	c, err := s.db.ClientByID(cam.ClientID)
	if err != nil || c.Status != protocol.StatusApproved {
		apiError(w, http.StatusNotFound, "not_found", "camera not found")
		return nil
	}
	u := userFrom(r)
	if !u.IsAdmin() {
		allowed, err := s.db.PermittedClients(u.ID)
		if err != nil || !allowed[cam.ClientID] {
			apiError(w, http.StatusForbidden, "forbidden", "no access to this camera")
			return nil
		}
	}
	return cam
}

func profileParam(r *http.Request) string {
	if r.URL.Query().Get("profile") == protocol.ProfileMain {
		return protocol.ProfileMain
	}
	return protocol.ProfileSub
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	cam := s.camera(w, r)
	if cam == nil {
		return
	}
	data, err := s.hub.Snapshot(r.Context(), cam, profileParam(r))
	if err != nil {
		code := "snapshot_failed"
		if errors.Is(err, hub.ErrClientOffline) {
			code = "client_offline"
		}
		apiError(w, http.StatusServiceUnavailable, code, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) viewAudit(r *http.Request, cam *db.Camera, profile string) func() {
	u, ip := userFrom(r), s.clientIP(r)
	start := time.Now()
	s.audit(u, ip, "view_start", cam.Name, profile)
	return func() {
		s.audit(u, ip, "view_stop", cam.Name, profile+" "+time.Since(start).Round(time.Second).String())
	}
}

func (s *Server) handleWebRTC(w http.ResponseWriter, r *http.Request) {
	cam := s.camera(w, r)
	if cam == nil {
		return
	}
	var req struct {
		SDP string `json:"sdp"`
	}
	if err := readJSON(r, &req); err != nil || req.SDP == "" {
		apiError(w, http.StatusBadRequest, "bad_request", "missing sdp")
		return
	}
	if s.rtc == nil {
		apiError(w, http.StatusServiceUnavailable, "webrtc_unavailable", "webrtc disabled")
		return
	}
	profile := profileParam(r)
	v, err := s.hub.Subscribe(cam, profile)
	if err != nil {
		internalError(w, err)
		return
	}
	answer, err := s.rtc.Answer(req.SDP, s.hub, v, s.viewAudit(r, cam, profile))
	if err != nil {
		s.hub.Unsubscribe(v)
		apiError(w, http.StatusBadRequest, "webrtc_failed", err.Error())
		return
	}
	codec, transcoded, _ := s.hub.StreamInfo(v)
	writeJSON(w, map[string]any{"sdp": answer, "codec": codec, "transcoded": transcoded})
}

func (s *Server) handleMSE(w http.ResponseWriter, r *http.Request) {
	cam := s.camera(w, r)
	if cam == nil {
		return
	}
	profile := profileParam(r)
	v, err := s.hub.Subscribe(cam, profile)
	if err != nil {
		internalError(w, err)
		return
	}
	stream.ServeMSE(w, r, s.hub, v, s.viewAudit(r, cam, profile))
}

// ---------------------------------------------------------------- events

var eventsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		return o == "" || o == "http://"+r.Host || o == "https://"+r.Host
	},
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ws, err := eventsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ch, unsub := s.hub.Events.Subscribe()
	defer unsub()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-closed:
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if ws.WriteMessage(websocket.TextMessage, b) != nil {
				return
			}
		case <-ping.C:
			_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if ws.WriteMessage(websocket.PingMessage, nil) != nil {
				return
			}
		}
	}
}

// ---------------------------------------------------------------- layouts

func (s *Server) handleLayouts(w http.ResponseWriter, r *http.Request) {
	ls, err := s.db.Layouts(userFrom(r).ID)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, ls)
}

func (s *Server) handleLayoutSave(w http.ResponseWriter, r *http.Request) {
	var l db.Layout
	if err := readJSON(r, &l); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	l.Name = strings.TrimSpace(l.Name)
	if l.Name == "" || len(l.Name) > 80 || len(l.Cameras) > 256 {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid layout")
		return
	}
	if l.Grid < 1 || l.Grid > 4 {
		l.Grid = 2
	}
	if l.Cameras == nil {
		l.Cameras = []int64{}
	}
	l.ID = 0
	if r.Method == http.MethodPut {
		l.ID = idParam(r)
	}
	l.UserID = userFrom(r).ID
	if err := s.db.SaveLayout(&l); err != nil {
		if isNotFound(err) {
			apiError(w, http.StatusNotFound, "not_found", "layout not found")
			return
		}
		internalError(w, err)
		return
	}
	writeJSON(w, l)
}

func (s *Server) handleLayoutDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteLayout(idParam(r), userFrom(r).ID); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, okResp)
}

// ---------------------------------------------------------------- users

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{3,32}$`)

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.db.Users()
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, us)
}

type userReq struct {
	Username   string  `json:"username"`
	Password   string  `json:"password"`
	Role       string  `json:"role"`
	Disabled   bool    `json:"disabled"`
	MustChange bool    `json:"must_change"`
	Clients    []int64 `json:"clients"`
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var req userReq
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	if !usernameRe.MatchString(req.Username) {
		apiError(w, http.StatusBadRequest, "bad_username", "username: 3-32 latin letters, digits, . _ @ -")
		return
	}
	if len(req.Password) < 6 {
		apiError(w, http.StatusBadRequest, "weak_password", "password must be at least 6 characters")
		return
	}
	if req.Role != db.RoleAdmin {
		req.Role = db.RoleUser
	}
	h, err := HashPassword(req.Password)
	if err != nil {
		internalError(w, err)
		return
	}
	u := &db.User{Username: req.Username, PasswordHash: h, Role: req.Role, Disabled: req.Disabled, MustChange: req.MustChange}
	if err := s.db.CreateUser(u); err != nil {
		if errors.Is(err, db.ErrExists) {
			apiError(w, http.StatusConflict, "user_exists", "username already exists")
			return
		}
		internalError(w, err)
		return
	}
	if err := s.db.SetPermissions(u.ID, req.Clients); err != nil {
		internalError(w, err)
		return
	}
	s.auditReq(r, "user_create", u.Username, u.Role)
	writeJSON(w, u)
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	var req userReq
	if err := readJSON(r, &req); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", "invalid request")
		return
	}
	u, err := s.db.UserByID(idParam(r))
	if err != nil {
		apiError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	me := userFrom(r)
	if req.Role != db.RoleAdmin {
		req.Role = db.RoleUser
	}
	// never lock out the last admin
	if u.IsAdmin() && (req.Role != db.RoleAdmin || req.Disabled) {
		if u.ID == me.ID {
			apiError(w, http.StatusBadRequest, "self_demote", "you cannot demote or disable yourself")
			return
		}
		if n, _ := s.db.CountAdmins(); n <= 1 {
			apiError(w, http.StatusBadRequest, "last_admin", "at least one admin is required")
			return
		}
	}
	changes := []string{}
	if u.Role != req.Role {
		changes = append(changes, "role="+req.Role)
	}
	if u.Disabled != req.Disabled {
		changes = append(changes, "disabled="+strconv.FormatBool(req.Disabled))
	}
	u.Role, u.Disabled = req.Role, req.Disabled
	if req.Password != "" {
		if len(req.Password) < 6 {
			apiError(w, http.StatusBadRequest, "weak_password", "password must be at least 6 characters")
			return
		}
		h, err := HashPassword(req.Password)
		if err != nil {
			internalError(w, err)
			return
		}
		u.PasswordHash = h
		u.MustChange = req.MustChange
		changes = append(changes, "password reset")
	}
	if err := s.db.UpdateUser(u); err != nil {
		internalError(w, err)
		return
	}
	if req.Password != "" || u.Disabled {
		keep := ""
		if u.ID == me.ID {
			keep = sessionFrom(r)
		}
		_ = s.db.DeleteUserSessions(u.ID, keep)
	}
	if err := s.db.SetPermissions(u.ID, req.Clients); err != nil {
		internalError(w, err)
		return
	}
	changes = append(changes, "clients="+strconv.Itoa(len(req.Clients)))
	s.auditReq(r, "user_update", u.Username, strings.Join(changes, ", "))
	writeJSON(w, okResp)
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	u, err := s.db.UserByID(idParam(r))
	if err != nil {
		apiError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	if u.ID == userFrom(r).ID {
		apiError(w, http.StatusBadRequest, "self_delete", "you cannot delete yourself")
		return
	}
	if u.IsAdmin() {
		if n, _ := s.db.CountAdmins(); n <= 1 {
			apiError(w, http.StatusBadRequest, "last_admin", "at least one admin is required")
			return
		}
	}
	if err := s.db.DeleteUser(u.ID); err != nil {
		internalError(w, err)
		return
	}
	s.auditReq(r, "user_delete", u.Username, "")
	writeJSON(w, okResp)
}

// ---------------------------------------------------------------- audit

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := db.AuditFilter{Username: q.Get("user"), Action: q.Get("action"), Query: q.Get("q")}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	f.From, _ = strconv.ParseInt(q.Get("from"), 10, 64)
	f.To, _ = strconv.ParseInt(q.Get("to"), 10, 64)
	rows, total, err := s.db.Audit(f)
	if err != nil {
		internalError(w, err)
		return
	}
	actions, _ := s.db.AuditActions()
	writeJSON(w, map[string]any{"rows": rows, "total": total, "actions": actions})
}
