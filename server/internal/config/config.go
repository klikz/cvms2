// Package config loads the server configuration, creating a default file
// when none exists.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// WebRTC holds ICE/network settings.
type WebRTC struct {
	// UDPPort is the single UDP port used for all WebRTC media (ICE UDP mux).
	UDPPort int `yaml:"udp_port"`
	// TCPPort is used for ICE-TCP when UDP is blocked. 0 disables it.
	TCPPort int `yaml:"tcp_port"`
	// PublicIPs are announced to browsers (set when the server is behind NAT).
	PublicIPs []string `yaml:"public_ips"`
	// STUNServers are optional STUN URLs, e.g. stun:stun.l.google.com:19302.
	STUNServers []string `yaml:"stun_servers"`
}

// Config is the server configuration.
type Config struct {
	Listen             string `yaml:"listen"`
	DBPath             string `yaml:"db_path"`
	TrustProxy         bool   `yaml:"trust_proxy"`
	SessionIdleMinutes int    `yaml:"session_idle_minutes"`
	LoginMaxAttempts   int    `yaml:"login_max_attempts"`
	LoginBlockMinutes  int    `yaml:"login_block_minutes"`
	AuditRetentionDays int    `yaml:"audit_retention_days"`
	// StreamIdleSeconds: upstream keeps running this long after the last viewer leaves.
	StreamIdleSeconds int `yaml:"stream_idle_seconds"`
	// ViewerIdleMinutes: browsers stop streams after this much user inactivity (0 = never).
	ViewerIdleMinutes    int    `yaml:"viewer_idle_minutes"`
	SnapshotCacheSeconds int    `yaml:"snapshot_cache_seconds"`
	FFmpegPath           string `yaml:"ffmpeg_path"`
	// HWAccel for the H.265 -> H.264 fallback: auto | cuda | qsv | vaapi | d3d11va | none.
	HWAccel string `yaml:"hwaccel"`
	// GPUDevice selects a GPU when there are several (index, or render node path for vaapi).
	GPUDevice string `yaml:"gpu_device"`
	// H264Encoder: auto (matches hwaccel) | libx264 | h264_nvenc | h264_qsv | h264_vaapi | h264_amf.
	H264Encoder string `yaml:"h264_encoder"`
	// H265Fallback: where H.265 is converted for browsers that cannot play
	// H.265: server | client (the camera's client GPU does it).
	H265Fallback string `yaml:"h265_fallback"`
	WebRTC       WebRTC `yaml:"webrtc"`
	// AuditViews: which camera views are logged: main | all | none.
	AuditViews string `yaml:"audit_views"`
	// Autostart installs the server as an OS service that starts at boot.
	Autostart bool `yaml:"autostart"`

	path string `yaml:"-"`
}

// Default returns the default configuration.
func Default() *Config {
	return &Config{
		Listen:               ":8080",
		DBPath:               "cvms.db",
		SessionIdleMinutes:   60,
		LoginMaxAttempts:     5,
		LoginBlockMinutes:    15,
		AuditRetentionDays:   90,
		StreamIdleSeconds:    30,
		ViewerIdleMinutes:    15,
		SnapshotCacheSeconds: 10,
		HWAccel:              "auto",
		H264Encoder:          "auto",
		H265Fallback:         "server",
		AuditViews:           "main",
		Autostart:            true,
		WebRTC: WebRTC{
			UDPPort:   8189,
			TCPPort:   8189,
			PublicIPs: []string{},
		},
	}
}

const header = `# CVMS server configuration.
# This file was created automatically. Restart the server after editing.
`

// Load reads path, or writes and returns the default config when missing.
// The second return value is true when a new file was created.
func Load(path string) (*Config, bool, error) {
	cfg := Default()
	cfg.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := cfg.Save(); err != nil {
			return nil, false, err
		}
		return cfg, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.path = path
	cfg.fix()
	return cfg, false, nil
}

func (c *Config) fix() {
	d := Default()
	if c.Listen == "" {
		c.Listen = d.Listen
	}
	if c.DBPath == "" {
		c.DBPath = d.DBPath
	}
	if c.SessionIdleMinutes <= 0 {
		c.SessionIdleMinutes = d.SessionIdleMinutes
	}
	if c.LoginMaxAttempts <= 0 {
		c.LoginMaxAttempts = d.LoginMaxAttempts
	}
	if c.LoginBlockMinutes <= 0 {
		c.LoginBlockMinutes = d.LoginBlockMinutes
	}
	if c.StreamIdleSeconds <= 0 {
		c.StreamIdleSeconds = d.StreamIdleSeconds
	}
	if c.SnapshotCacheSeconds <= 0 {
		c.SnapshotCacheSeconds = d.SnapshotCacheSeconds
	}
	if c.H264Encoder == "" {
		c.H264Encoder = d.H264Encoder
	}
	if c.HWAccel == "" {
		c.HWAccel = d.HWAccel
	}
	if c.H265Fallback != "client" {
		c.H265Fallback = "server"
	}
	switch c.AuditViews {
	case "main", "all", "none":
	default:
		c.AuditViews = d.AuditViews
	}
}

// Save writes the config to its file.
func (c *Config) Save() error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(c.path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	return os.WriteFile(c.path, append([]byte(header), b...), 0o600)
}

// Path returns the config file path.
func (c *Config) Path() string { return c.path }

// Resolve makes p absolute relative to the config directory.
func (c *Config) Resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(c.path), p)
}
