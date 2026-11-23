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
