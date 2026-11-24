package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// An old database without users.viewer_idle_minutes is migrated, and
// null / 0 / n round-trip.
func TestViewerIdleMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'user',
		lang TEXT NOT NULL DEFAULT '',
		must_change INTEGER NOT NULL DEFAULT 0,
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		last_login INTEGER NOT NULL DEFAULT 0);
		INSERT INTO users(username, password_hash, created_at) VALUES('legacy', 'x', 1);`)
	old.Close()
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ { // reopening must not fail
		d, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		d.Close()
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	u, err := d.UserByName("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if u.ViewerIdleMinutes != nil {
		t.Fatalf("legacy user: want nil (default), got %d", *u.ViewerIdleMinutes)
	}
	for _, want := range []*int{new(0), new(30), nil} {
		u.ViewerIdleMinutes = want
		if err := d.UpdateUser(u); err != nil {
			t.Fatal(err)
		}
		got, err := d.UserByID(u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if (got.ViewerIdleMinutes == nil) != (want == nil) || (want != nil && *got.ViewerIdleMinutes != *want) {
			t.Fatalf("want %v, got %v", want, got.ViewerIdleMinutes)
		}
	}

	nu := &User{Username: "fresh", PasswordHash: "x", Role: RoleUser, ViewerIdleMinutes: new(5)}
	if err := d.CreateUser(nu); err != nil {
		t.Fatal(err)
	}
	got, err := d.UserByID(nu.ID)
	if err != nil || got.ViewerIdleMinutes == nil || *got.ViewerIdleMinutes != 5 {
		t.Fatalf("create: %v %v", err, got)
	}
}
