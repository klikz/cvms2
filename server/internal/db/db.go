// Package db is the SQLite storage of the CVMS server.
package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// DB wraps the SQLite connection.
type DB struct {
	*sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	role          TEXT NOT NULL DEFAULT 'user',
	lang          TEXT NOT NULL DEFAULT '',
	must_change   INTEGER NOT NULL DEFAULT 0,
	disabled      INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL,
	last_login    INTEGER NOT NULL DEFAULT 0,
	viewer_idle_minutes INTEGER
);
CREATE TABLE IF NOT EXISTS sessions (
	token      TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	ip         TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS clients (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	uid         TEXT NOT NULL UNIQUE,
	name        TEXT NOT NULL DEFAULT '',
	hostname    TEXT NOT NULL DEFAULT '',
	os          TEXT NOT NULL DEFAULT '',
	version     TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'pending',
	token       TEXT NOT NULL DEFAULT '',
	ip          TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL DEFAULT 0,
	approved_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS cameras (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	client_id    INTEGER NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
	cam_uid      TEXT NOT NULL,
	name         TEXT NOT NULL DEFAULT '',
	host         TEXT NOT NULL DEFAULT '',
	model        TEXT NOT NULL DEFAULT '',
	online       INTEGER NOT NULL DEFAULT 0,
	present      INTEGER NOT NULL DEFAULT 1,
	main_json    TEXT NOT NULL DEFAULT '{}',
	sub_json     TEXT NOT NULL DEFAULT '{}',
	display_name TEXT NOT NULL DEFAULT '',
	location     TEXT NOT NULL DEFAULT '',
	UNIQUE(client_id, cam_uid)
);
CREATE TABLE IF NOT EXISTS permissions (
	user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	client_id INTEGER NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
	PRIMARY KEY (user_id, client_id)
);
CREATE TABLE IF NOT EXISTS layouts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	grid       INTEGER NOT NULL DEFAULT 2,
	cameras    TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS favorites (
	user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	camera_id INTEGER NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
	PRIMARY KEY (user_id, camera_id)
);
CREATE TABLE IF NOT EXISTS audit_logs (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	ts       INTEGER NOT NULL,
	user_id  INTEGER NOT NULL DEFAULT 0,
	username TEXT NOT NULL DEFAULT '',
	ip       TEXT NOT NULL DEFAULT '',
	action   TEXT NOT NULL,
	target   TEXT NOT NULL DEFAULT '',
	details  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_logs(ts);
CREATE INDEX IF NOT EXISTS idx_cameras_client ON cameras(client_id);
`

// Open opens (and migrates) the database at path.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(1)
	if _, err := sdb.Exec(schema); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrateColumns(sdb); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Cameras are reported again when clients reconnect.
	_, _ = sdb.Exec(`UPDATE cameras SET online=0`)
	return &DB{sdb}, nil
}

// migrateColumns adds columns introduced after a table was first created.
func migrateColumns(sdb *sql.DB) error {
	for _, c := range []struct{ table, column, def string }{
		{"users", "viewer_idle_minutes", "INTEGER"},
	} {
		has, err := hasColumn(sdb, c.table, c.column)
		if err != nil {
			return err
		}
		if !has {
			if _, err := sdb.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.column + ` ` + c.def); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasColumn(sdb *sql.DB, table, column string) (bool, error) {
	rows, err := sdb.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Now returns the current time in unix milliseconds.
func Now() int64 { return time.Now().UnixMilli() }

// RandomToken returns n random bytes hex-encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
