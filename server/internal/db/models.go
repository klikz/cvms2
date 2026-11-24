package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"cvms/shared/protocol"
)

// ---------------------------------------------------------------- users

// User is a server account.
type User struct {
	ID           int64   `json:"id"`
	Username     string  `json:"username"`
	PasswordHash string  `json:"-"`
	Role         string  `json:"role"`
	Lang         string  `json:"lang"`
	MustChange   bool    `json:"must_change"`
	Disabled     bool    `json:"disabled"`
	CreatedAt    int64   `json:"created_at"`
	LastLogin    int64   `json:"last_login"`
	Clients      []int64 `json:"clients"`
}

// Roles.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// IsAdmin reports whether the user is an admin.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

const userCols = `id, username, password_hash, role, lang, must_change, disabled, created_at, last_login`

func scanUser(s interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	var mc, dis int
	err := s.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Lang, &mc, &dis, &u.CreatedAt, &u.LastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.MustChange, u.Disabled = mc == 1, dis == 1
	return u, nil
}

// UserByID loads a user.
func (d *DB) UserByID(id int64) (*User, error) {
	return scanUser(d.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
}

// UserByName loads a user by username (case-insensitive).
func (d *DB) UserByName(name string) (*User, error) {
	return scanUser(d.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, name))
}

// CountUsers returns the number of users.
func (d *DB) CountUsers() (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// Users lists all users with their permitted clients.
func (d *DB) Users() ([]*User, error) {
	rows, err := d.Query(`SELECT ` + userCols + ` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	var out []*User
	byID := map[int64]*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		u.Clients = []int64{}
		out = append(out, u)
		byID[u.ID] = u
	}
	rows.Close()
	prow, err := d.Query(`SELECT user_id, client_id FROM permissions`)
	if err != nil {
		return nil, err
	}
	defer prow.Close()
	for prow.Next() {
		var uid, cid int64
		if err := prow.Scan(&uid, &cid); err != nil {
			return nil, err
		}
		if u := byID[uid]; u != nil {
			u.Clients = append(u.Clients, cid)
		}
	}
	return out, prow.Err()
}

// CreateUser inserts a user.
func (d *DB) CreateUser(u *User) error {
	res, err := d.Exec(`INSERT INTO users(username, password_hash, role, lang, must_change, disabled, created_at) VALUES(?,?,?,?,?,?,?)`,
		u.Username, u.PasswordHash, u.Role, u.Lang, boolInt(u.MustChange), boolInt(u.Disabled), Now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrExists
		}
		return err
	}
	u.ID, _ = res.LastInsertId()
	return nil
}

// ErrExists is returned on unique constraint violations.
var ErrExists = errors.New("already exists")

// UpdateUser saves role, disabled and optionally password.
func (d *DB) UpdateUser(u *User) error {
	_, err := d.Exec(`UPDATE users SET role=?, disabled=?, password_hash=?, must_change=?, lang=? WHERE id=?`,
		u.Role, boolInt(u.Disabled), u.PasswordHash, boolInt(u.MustChange), u.Lang, u.ID)
	return err
}

// SetUserLogin stores the last login time.
func (d *DB) SetUserLogin(id int64) error {
	_, err := d.Exec(`UPDATE users SET last_login=? WHERE id=?`, Now(), id)
	return err
}

// DeleteUser removes a user.
func (d *DB) DeleteUser(id int64) error {
	_, err := d.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

// CountAdmins counts enabled admins.
func (d *DB) CountAdmins() (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&n)
	return n, err
}

// SetPermissions replaces the clients a user may view.
func (d *DB) SetPermissions(userID int64, clientIDs []int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM permissions WHERE user_id=?`, userID); err != nil {
		return err
	}
	for _, c := range clientIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO permissions(user_id, client_id) SELECT ?, id FROM clients WHERE id=?`, userID, c); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PermittedClients returns the client ids a user may view.
func (d *DB) PermittedClients(userID int64) (map[int64]bool, error) {
	rows, err := d.Query(`SELECT client_id FROM permissions WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- sessions

// Session is a logged-in browser session.
type Session struct {
	Token     string
	UserID    int64
	CreatedAt int64
	LastSeen  int64
}

// CreateSession creates a session and returns its token.
func (d *DB) CreateSession(userID int64, ip string) (string, error) {
	tok := RandomToken(32)
	now := Now()
	_, err := d.Exec(`INSERT INTO sessions(token, user_id, created_at, last_seen, ip) VALUES(?,?,?,?,?)`, tok, userID, now, now, ip)
	return tok, err
}

// SessionByToken loads a session.
func (d *DB) SessionByToken(tok string) (*Session, error) {
	s := &Session{}
	err := d.QueryRow(`SELECT token, user_id, created_at, last_seen FROM sessions WHERE token=?`, tok).Scan(&s.Token, &s.UserID, &s.CreatedAt, &s.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

// TouchSession updates last_seen.
func (d *DB) TouchSession(tok string) error {
	_, err := d.Exec(`UPDATE sessions SET last_seen=? WHERE token=?`, Now(), tok)
	return err
}

// DeleteSession removes one session.
func (d *DB) DeleteSession(tok string) error {
	_, err := d.Exec(`DELETE FROM sessions WHERE token=?`, tok)
	return err
}

// DeleteUserSessions removes all sessions of a user except keep.
func (d *DB) DeleteUserSessions(userID int64, keep string) error {
	_, err := d.Exec(`DELETE FROM sessions WHERE user_id=? AND token<>?`, userID, keep)
	return err
}

// PurgeSessions removes sessions idle since before cutoff (unix ms).
func (d *DB) PurgeSessions(cutoff int64) error {
	_, err := d.Exec(`DELETE FROM sessions WHERE last_seen<?`, cutoff)
	return err
}

// ---------------------------------------------------------------- clients

// Client is a registered CVMS client.
type Client struct {
	ID         int64  `json:"id"`
	UID        string `json:"uid"`
	Name       string `json:"name"`
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Version    string `json:"version"`
	Status     string `json:"status"`
	Token      string `json:"-"`
	IP         string `json:"ip"`
	CreatedAt  int64  `json:"created_at"`
	LastSeen   int64  `json:"last_seen"`
	ApprovedAt int64  `json:"approved_at"`
}

const clientCols = `id, uid, name, hostname, os, version, status, token, ip, created_at, last_seen, approved_at`

func scanClient(s interface{ Scan(...any) error }) (*Client, error) {
	c := &Client{}
	err := s.Scan(&c.ID, &c.UID, &c.Name, &c.Hostname, &c.OS, &c.Version, &c.Status, &c.Token, &c.IP, &c.CreatedAt, &c.LastSeen, &c.ApprovedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ClientByID loads a client.
func (d *DB) ClientByID(id int64) (*Client, error) {
	return scanClient(d.QueryRow(`SELECT `+clientCols+` FROM clients WHERE id=?`, id))
}

// ClientByUID loads a client by its self-generated uid.
func (d *DB) ClientByUID(uid string) (*Client, error) {
	return scanClient(d.QueryRow(`SELECT `+clientCols+` FROM clients WHERE uid=?`, uid))
}

// Clients lists all clients.
func (d *DB) Clients() ([]*Client, error) {
	rows, err := d.Query(`SELECT ` + clientCols + ` FROM clients ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateClient registers a new pending client.
func (d *DB) CreateClient(c *Client) error {
	now := Now()
	res, err := d.Exec(`INSERT INTO clients(uid, name, hostname, os, version, status, ip, created_at, last_seen) VALUES(?,?,?,?,?,?,?,?,?)`,
		c.UID, c.Name, c.Hostname, c.OS, c.Version, protocol.StatusPending, c.IP, now, now)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	c.Status, c.CreatedAt, c.LastSeen = protocol.StatusPending, now, now
	return nil
}

// UpdateClientSeen stores connection metadata. The display name is only
// taken from the client while the admin has not renamed it.
func (d *DB) UpdateClientSeen(c *Client) error {
	_, err := d.Exec(`UPDATE clients SET hostname=?, os=?, version=?, ip=?, last_seen=? WHERE id=?`,
		c.Hostname, c.OS, c.Version, c.IP, Now(), c.ID)
	return err
}

// TouchClient updates last_seen.
func (d *DB) TouchClient(id int64) error {
	_, err := d.Exec(`UPDATE clients SET last_seen=? WHERE id=?`, Now(), id)
	return err
}

// SetClientStatus changes approval state and token.
func (d *DB) SetClientStatus(id int64, status, token string) error {
	approved := int64(0)
	if status == protocol.StatusApproved {
		approved = Now()
	}
	_, err := d.Exec(`UPDATE clients SET status=?, token=?, approved_at=? WHERE id=?`, status, token, approved, id)
	return err
}

// RenameClient sets the client display name.
func (d *DB) RenameClient(id int64, name string) error {
	_, err := d.Exec(`UPDATE clients SET name=? WHERE id=?`, name, id)
	return err
}

// DeleteClient removes a client and its cameras.
func (d *DB) DeleteClient(id int64) error {
	_, err := d.Exec(`DELETE FROM clients WHERE id=?`, id)
	return err
}

// ---------------------------------------------------------------- cameras

// Camera is a camera reported by a client.
type Camera struct {
	ID          int64               `json:"id"`
	ClientID    int64               `json:"client_id"`
	CamUID      string              `json:"cam_uid"`
	Name        string              `json:"name"`
	OrigName    string              `json:"orig_name"`
	Host        string              `json:"host"`
	Model       string              `json:"model"`
	Online      bool                `json:"online"`
	Present     bool                `json:"-"`
	Main        protocol.StreamInfo `json:"main"`
	Sub         protocol.StreamInfo `json:"sub"`
	DisplayName string              `json:"display_name"`
	Location    string              `json:"location"`
}

const cameraCols = `id, client_id, cam_uid, name, host, model, online, present, main_json, sub_json, display_name, location`

func scanCamera(s interface{ Scan(...any) error }) (*Camera, error) {
	c := &Camera{}
	var online, present int
	var mj, sj string
	err := s.Scan(&c.ID, &c.ClientID, &c.CamUID, &c.OrigName, &c.Host, &c.Model, &online, &present, &mj, &sj, &c.DisplayName, &c.Location)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Online, c.Present = online == 1, present == 1
	_ = json.Unmarshal([]byte(mj), &c.Main)
	_ = json.Unmarshal([]byte(sj), &c.Sub)
	c.Name = c.OrigName
	if c.DisplayName != "" {
		c.Name = c.DisplayName
	}
	return c, nil
}

// CameraByID loads a camera.
func (d *DB) CameraByID(id int64) (*Camera, error) {
	return scanCamera(d.QueryRow(`SELECT `+cameraCols+` FROM cameras WHERE id=?`, id))
}

// Cameras lists present cameras, optionally of one client (clientID > 0).
func (d *DB) Cameras(clientID int64) ([]*Camera, error) {
	q := `SELECT ` + cameraCols + ` FROM cameras WHERE present=1`
	var args []any
	if clientID > 0 {
		q += ` AND client_id=?`
		args = append(args, clientID)
	}
	q += ` ORDER BY client_id, COALESCE(NULLIF(display_name,''), name) COLLATE NOCASE, id`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Camera
	for rows.Next() {
		c, err := scanCamera(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SyncCameras upserts the full camera list of a client. Cameras no longer
// reported are marked not present (kept so admin names survive re-adding).
func (d *DB) SyncCameras(clientID int64, list []protocol.CameraInfo) (map[string]int64, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE cameras SET present=0, online=0 WHERE client_id=?`, clientID); err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	for _, c := range list {
		mj, _ := json.Marshal(c.Main)
		sj, _ := json.Marshal(c.Sub)
		_, err := tx.Exec(`INSERT INTO cameras(client_id, cam_uid, name, host, model, online, present, main_json, sub_json)
			VALUES(?,?,?,?,?,?,1,?,?)
			ON CONFLICT(client_id, cam_uid) DO UPDATE SET name=excluded.name, host=excluded.host, model=excluded.model,
			online=excluded.online, present=1, main_json=excluded.main_json, sub_json=excluded.sub_json`,
			clientID, c.ID, c.Name, c.Host, c.Model, boolInt(c.Online), string(mj), string(sj))
		if err != nil {
			return nil, err
		}
		var id int64
		if err := tx.QueryRow(`SELECT id FROM cameras WHERE client_id=? AND cam_uid=?`, clientID, c.ID).Scan(&id); err != nil {
			return nil, err
		}
		ids[c.ID] = id
	}
	return ids, tx.Commit()
}

// SetCameraOnline updates a camera online flag and returns the camera id.
func (d *DB) SetCameraOnline(clientID int64, camUID string, online bool) (int64, error) {
	var id int64
	err := d.QueryRow(`UPDATE cameras SET online=? WHERE client_id=? AND cam_uid=? RETURNING id`, boolInt(online), clientID, camUID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// SetClientCamerasOffline marks every camera of a client offline.
func (d *DB) SetClientCamerasOffline(clientID int64) error {
	_, err := d.Exec(`UPDATE cameras SET online=0 WHERE client_id=?`, clientID)
	return err
}

// UpdateCameraMeta sets the admin display name and location.
func (d *DB) UpdateCameraMeta(id int64, displayName, location string) error {
	_, err := d.Exec(`UPDATE cameras SET display_name=?, location=? WHERE id=?`, displayName, location, id)
	return err
}

// ---------------------------------------------------------------- layouts

// Layout is a saved camera view of a user.
type Layout struct {
	ID        int64   `json:"id"`
	UserID    int64   `json:"-"`
	Name      string  `json:"name"`
	Grid      int     `json:"grid"`
	Cameras   []int64 `json:"cameras"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// Layouts lists layouts of a user.
func (d *DB) Layouts(userID int64) ([]*Layout, error) {
	rows, err := d.Query(`SELECT id, user_id, name, grid, cameras, created_at, updated_at FROM layouts WHERE user_id=? ORDER BY name COLLATE NOCASE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Layout{}
	for rows.Next() {
		l := &Layout{}
		var cams string
		if err := rows.Scan(&l.ID, &l.UserID, &l.Name, &l.Grid, &cams, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(cams), &l.Cameras)
		if l.Cameras == nil {
			l.Cameras = []int64{}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SaveLayout inserts or updates a layout owned by l.UserID.
func (d *DB) SaveLayout(l *Layout) error {
	cams, _ := json.Marshal(l.Cameras)
	now := Now()
	if l.ID == 0 {
		res, err := d.Exec(`INSERT INTO layouts(user_id, name, grid, cameras, created_at, updated_at) VALUES(?,?,?,?,?,?)`, l.UserID, l.Name, l.Grid, string(cams), now, now)
		if err != nil {
			return err
		}
		l.ID, _ = res.LastInsertId()
		l.CreatedAt, l.UpdatedAt = now, now
		return nil
	}
	res, err := d.Exec(`UPDATE layouts SET name=?, grid=?, cameras=?, updated_at=? WHERE id=? AND user_id=?`, l.Name, l.Grid, string(cams), now, l.ID, l.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	l.UpdatedAt = now
	return nil
}

// DeleteLayout removes a layout owned by userID.
func (d *DB) DeleteLayout(id, userID int64) error {
	_, err := d.Exec(`DELETE FROM layouts WHERE id=? AND user_id=?`, id, userID)
	return err
}

// ---------------------------------------------------------------- favorites

// Favorites lists the camera ids a user starred.
func (d *DB) Favorites(userID int64) ([]int64, error) {
	rows, err := d.Query(`SELECT camera_id FROM favorites WHERE user_id=? ORDER BY rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetFavorite stars or unstars a camera for a user.
func (d *DB) SetFavorite(userID, camID int64, on bool) error {
	if on {
		_, err := d.Exec(`INSERT OR IGNORE INTO favorites(user_id, camera_id) VALUES(?,?)`, userID, camID)
		return err
	}
	_, err := d.Exec(`DELETE FROM favorites WHERE user_id=? AND camera_id=?`, userID, camID)
	return err
}

// ---------------------------------------------------------------- audit

// AuditEntry is one audit log row.
type AuditEntry struct {
	ID       int64  `json:"id"`
	TS       int64  `json:"ts"`
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	IP       string `json:"ip"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Details  string `json:"details"`
}

// AddAudit inserts an audit entry.
func (d *DB) AddAudit(e *AuditEntry) error {
	if e.TS == 0 {
		e.TS = Now()
	}
	_, err := d.Exec(`INSERT INTO audit_logs(ts, user_id, username, ip, action, target, details) VALUES(?,?,?,?,?,?,?)`,
		e.TS, e.UserID, e.Username, e.IP, e.Action, e.Target, e.Details)
	return err
}

// AuditFilter filters audit queries.
type AuditFilter struct {
	Username string
	Action   string
	Query    string
	From, To int64
	Limit    int
	Offset   int
}

// Audit queries audit logs, newest first. It returns rows and total count.
func (d *DB) Audit(f AuditFilter) ([]*AuditEntry, int, error) {
	where := []string{"1=1"}
	var args []any
	if f.Username != "" {
		where = append(where, "username=?")
		args = append(args, f.Username)
	}
	if f.Action != "" {
		where = append(where, "action=?")
		args = append(args, f.Action)
	}
	if f.Query != "" {
		where = append(where, "(target LIKE ? OR details LIKE ? OR ip LIKE ? OR username LIKE ?)")
		q := "%" + f.Query + "%"
		args = append(args, q, q, q, q)
	}
	if f.From > 0 {
		where = append(where, "ts>=?")
		args = append(args, f.From)
	}
	if f.To > 0 {
		where = append(where, "ts<=?")
		args = append(args, f.To)
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	rows, err := d.Query(`SELECT id, ts, user_id, username, ip, action, target, details FROM audit_logs WHERE `+w+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*AuditEntry{}
	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.TS, &e.UserID, &e.Username, &e.IP, &e.Action, &e.Target, &e.Details); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// AuditActions lists distinct actions.
func (d *DB) AuditActions() ([]string, error) {
	rows, err := d.Query(`SELECT DISTINCT action FROM audit_logs ORDER BY action`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PurgeAudit removes entries older than cutoff (unix ms).
func (d *DB) PurgeAudit(cutoff int64) error {
	_, err := d.Exec(`DELETE FROM audit_logs WHERE ts<?`, cutoff)
	return err
}
