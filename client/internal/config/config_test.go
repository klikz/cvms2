package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cvms/client/internal/secret"
)

func TestDefaultCreatedAndPasswordEncrypted(t *testing.T) {
	dir := t.TempDir()
	box, err := secret.Open(filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	s, created, err := Load(path, box)
	if err != nil || !created {
		t.Fatalf("load: %v created=%v", err, created)
	}
	if s.Get().ClientUID == "" || s.Get().UI.Listen == "" {
		t.Fatal("defaults not applied")
	}
	if err := s.Update(func(c *Config) error {
		c.Cameras = append(c.Cameras, Camera{ID: "a", Name: "cam", Username: "u", Password: "s3cret!", Main: Stream{URL: "rtsp://10.0.0.5/main"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "s3cret!") || !strings.Contains(string(raw), "enc:") {
		t.Fatalf("password not encrypted on disk:\n%s", raw)
	}
	s2, created, err := Load(path, box)
	if err != nil || created {
		t.Fatalf("reload: %v", err)
	}
	cam, ok := s2.Camera("a")
	if !ok || cam.Password != "s3cret!" {
		t.Fatalf("password not decrypted: %+v", cam)
	}
	u, _ := cam.StreamURL("sub")
	if u != "rtsp://u:s3cret%21@10.0.0.5/main" {
		t.Fatalf("bad url %s", u)
	}
}

func TestWithCredentials(t *testing.T) {
	if got := WithCredentials("rtsp://1.2.3.4:554/s", "ad min", "p@ss"); got != "rtsp://ad%20min:p%40ss@1.2.3.4:554/s" {
		t.Fatal(got)
	}
	if got := WithCredentials("rtsp://u:p@1.2.3.4/s", "x", "y"); got != "rtsp://u:p@1.2.3.4/s" {
		t.Fatal(got)
	}
}

func TestBrokenStreamURLRepaired(t *testing.T) {
	dir := t.TempDir()
	box, err := secret.Open(filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	yml := "client_uid: x\ncameras:\n  - id: a\n    name: cam\n    main:\n      url: rtsp://10.0.0.5:80:554/cam/realmonitor?channel=1&subtype=0\n    sub:\n      url: rtsp://10.0.0.5:554/sub\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, err := Load(path, box)
	if err != nil {
		t.Fatal(err)
	}
	cam, _ := s.Camera("a")
	if cam.Main.URL != "rtsp://10.0.0.5:554/cam/realmonitor?channel=1&subtype=0" || cam.Sub.URL != "rtsp://10.0.0.5:554/sub" {
		t.Fatalf("not repaired on load: %+v", cam)
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), ":80:554") {
		t.Fatalf("repair not saved:\n%s", raw)
	}
	// any write path must normalize too
	if err := s.Update(func(c *Config) error {
		c.Cameras = append(c.Cameras, Camera{ID: "b", Main: Stream{URL: "rtsp://u:p@10.0.0.6:80:554/s"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if cam, _ := s.Camera("b"); cam.Main.URL != "rtsp://u:p@10.0.0.6:554/s" {
		t.Fatalf("not repaired on update: %s", cam.Main.URL)
	}
}
