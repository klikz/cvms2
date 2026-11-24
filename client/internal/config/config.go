// Package config holds the client configuration file. All settings live in
// config.yaml; a default file is created on first start.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"cvms/client/internal/secret"
	"cvms/shared/protocol"
)

// Stream describes one RTSP stream of a camera.
type Stream struct {
	URL string `yaml:"url" json:"url"`
	// SnapshotURL is an optional HTTP JPEG URL (from ONVIF GetSnapshotUri).
	SnapshotURL string `yaml:"snapshot_url,omitempty" json:"snapshot_url"`
	Codec       string `yaml:"codec,omitempty" json:"codec"`
	Width       int    `yaml:"width,omitempty" json:"width"`
	Height      int    `yaml:"height,omitempty" json:"height"`
}

// Camera is one configured camera.
type Camera struct {
	ID        string `yaml:"id" json:"id"`
	Name      string `yaml:"name" json:"name"`
	Host      string `yaml:"host" json:"host"`
	ONVIFPort int    `yaml:"onvif_port,omitempty" json:"onvif_port"`
	Username  string `yaml:"username,omitempty" json:"username"`
	// Password is plain text in memory and encrypted ("enc:...") on disk.
	Password     string `yaml:"password,omitempty" json:"-"`
	Manufacturer string `yaml:"manufacturer,omitempty" json:"manufacturer"`
	Model        string `yaml:"model,omitempty" json:"model"`
	Main         Stream `yaml:"main" json:"main"`
	Sub          Stream `yaml:"sub" json:"sub"`
	Enabled      bool   `yaml:"enabled" json:"enabled"`
}

// UI holds local web UI settings.
type UI struct {
	Listen             string `yaml:"listen"`
	Username           string `yaml:"username"`
	PasswordHash       string `yaml:"password_hash"`
	MustChange         bool   `yaml:"must_change"`
	SessionIdleMinutes int    `yaml:"session_idle_minutes"`
	LoginMaxAttempts   int    `yaml:"login_max_attempts"`
	LoginBlockMinutes  int    `yaml:"login_block_minutes"`
}

// Config is the client configuration.
type Config struct {
	Name      string `yaml:"name"`
	ClientUID string `yaml:"client_uid"`
	ServerURL string `yaml:"server_url"`
	// ServerToken is issued by the server after admin approval.
	ServerToken string `yaml:"server_token"`
	UI          UI     `yaml:"ui"`
	// SnapshotInterval is the grid image refresh period in seconds.
	SnapshotInterval int `yaml:"snapshot_interval"`
	// StatusInterval is the camera online check period in seconds.
	StatusInterval int `yaml:"status_interval"`
	// SnapshotConcurrency limits parallel snapshot ffmpeg processes.
	SnapshotConcurrency int    `yaml:"snapshot_concurrency"`
	FFmpegPath          string `yaml:"ffmpeg_path"`
	// HWAccel for decoding and H.265 -> H.264 transcoding (when the server
	// asks for H.264): auto | cuda | qsv | vaapi | d3d11va | none.
	HWAccel string `yaml:"hwaccel"`
	// GPUDevice selects a GPU when there are several.
	GPUDevice   string `yaml:"gpu_device"`
	H264Encoder string `yaml:"h264_encoder"`
	// Autostart installs the client as an OS service that starts at boot.
	Autostart bool     `yaml:"autostart"`
	Cameras   []Camera `yaml:"cameras"`
}

func defaults() *Config {
	host, _ := os.Hostname()
	return &Config{
		Name:                host,
		ClientUID:           randomID(16),
		UI:                  UI{Listen: "127.0.0.1:8081", Username: "admin", SessionIdleMinutes: 60, LoginMaxAttempts: 5, LoginBlockMinutes: 15},
		SnapshotInterval:    5,
		StatusInterval:      30,
		SnapshotConcurrency: 4,
		HWAccel:             "auto",
		H264Encoder:         "auto",
		Autostart:           true,
		Cameras:             []Camera{},
	}
}

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewCameraID returns a random camera id.
func NewCameraID() string { return randomID(6) }

// Store is the thread-safe config holder.
type Store struct {
	mu   sync.RWMutex
	cfg  *Config
	path string
	box  *secret.Box
}

const header = `# CVMS client configuration.
# Created automatically. Camera passwords are encrypted with client.key.
# Edit while the client is stopped, or use the web UI.
# Port: ui.listen ("host:port") - local web UI; change it if the port is already in use.
`

// Load reads path or creates a default file. created is true for a new file.
func Load(path string, box *secret.Box) (s *Store, created bool, err error) {
	cfg := defaults()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		created = true
	} else if err != nil {
		return nil, false, err
	} else if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	s = &Store{cfg: cfg, path: path, box: box}
	dirty := created
	d := defaults()
	if cfg.ClientUID == "" {
		cfg.ClientUID, dirty = d.ClientUID, true
	}
	if cfg.Name == "" {
		cfg.Name = d.Name
	}
	if cfg.UI.Listen == "" {
		cfg.UI.Listen = d.UI.Listen
	}
	if cfg.UI.Username == "" {
		cfg.UI.Username = d.UI.Username
	}
	if cfg.UI.SessionIdleMinutes <= 0 {
		cfg.UI.SessionIdleMinutes = d.UI.SessionIdleMinutes
	}
	if cfg.UI.LoginMaxAttempts <= 0 {
		cfg.UI.LoginMaxAttempts = d.UI.LoginMaxAttempts
	}
	if cfg.UI.LoginBlockMinutes <= 0 {
		cfg.UI.LoginBlockMinutes = d.UI.LoginBlockMinutes
	}
	if cfg.SnapshotInterval <= 0 {
		cfg.SnapshotInterval = d.SnapshotInterval
	}
	if cfg.StatusInterval <= 0 {
		cfg.StatusInterval = d.StatusInterval
	}
	if cfg.SnapshotConcurrency <= 0 {
		cfg.SnapshotConcurrency = d.SnapshotConcurrency
	}
	if cfg.HWAccel == "" {
		cfg.HWAccel = d.HWAccel
	}
	if cfg.H264Encoder == "" {
		cfg.H264Encoder = d.H264Encoder
	}
	if cfg.Cameras == nil {
		cfg.Cameras = []Camera{}
	}
	for i := range cfg.Cameras {
		c := &cfg.Cameras[i]
		if c.ID == "" {
			c.ID, dirty = NewCameraID(), true
		}
		if c.Password != "" && !secret.IsEncrypted(c.Password) {
			dirty = true // plain text typed by hand: encrypt on save
		}
		p, err := box.Decrypt(c.Password)
		if err != nil {
			return nil, false, fmt.Errorf("camera %q password: %w", c.Name, err)
		}
		c.Password = p
	}
	if dirty {
		if err := s.save(); err != nil {
			return nil, false, err
		}
	}
	return s, created, nil
}

// Get returns a deep copy of the config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := *s.cfg
	c.Cameras = append([]Camera(nil), s.cfg.Cameras...)
	return c
}

// Update applies fn to the config and saves it.
func (s *Store) Update(fn func(c *Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(s.cfg); err != nil {
		return err
	}
	return s.save()
}

// Camera returns a camera by id.
func (s *Store) Camera(id string) (Camera, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.cfg.Cameras {
		if c.ID == id {
			return c, true
		}
	}
	return Camera{}, false
}

func (s *Store) save() error {
	out := *s.cfg
	out.Cameras = make([]Camera, len(s.cfg.Cameras))
	for i, c := range s.cfg.Cameras {
		enc, err := s.box.Encrypt(c.Password)
		if err != nil {
			return err
		}
		c.Password = enc
		out.Cameras[i] = c
	}
	b, err := yaml.Marshal(&out)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), b...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Path returns the config file path.
func (s *Store) Path() string { return s.path }

// StreamURL returns the RTSP URL of a camera stream with credentials
// injected. profile "sub" falls back to main when no sub stream exists.
func (c Camera) StreamURL(profile string) (string, Stream) {
	st := c.Main
	if profile == protocol.ProfileSub && c.Sub.URL != "" {
		st = c.Sub
	}
	return WithCredentials(st.URL, c.Username, c.Password), st
}

// WithCredentials injects user:pass into an rtsp:// URL unless already present.
func WithCredentials(raw, user, pass string) string {
	if user == "" || raw == "" {
		return raw
	}
	i := strings.Index(raw, "://")
	if i < 0 {
		return raw
	}
	rest := raw[i+3:]
	host := rest
	if j := strings.IndexAny(rest, "/?"); j >= 0 {
		host = rest[:j]
	}
	if strings.Contains(host, "@") {
		return raw
	}
	return raw[:i+3] + urlEscape(user) + ":" + urlEscape(pass) + "@" + rest
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, r := range []byte(s) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.IndexByte("-._~", r) >= 0 {
			b.WriteByte(r)
		} else {
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}
