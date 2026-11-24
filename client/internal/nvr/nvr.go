// Package nvr reads the channel list of a network video recorder
// (Hikvision ISAPI or Dahua CGI) so its cameras can be added one by one.
package nvr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cvms/client/internal/digest"
)

var (
	ErrAuth        = errors.New("NVR refused the login")
	ErrUnreachable = errors.New("NVR is unreachable")
	// ErrUnsupported: the device does not answer this vendor's API.
	ErrUnsupported = errors.New("device does not answer the NVR API")
)

const (
	DefaultRTSPPort  = 554
	defaultHTTPPort  = 80
	maxResponseBytes = 4 << 20
)

var httpClient = &http.Client{Timeout: 8 * time.Second}

// Vendor names accepted by Open.
const (
	Auto      = "auto"
	Hikvision = "hikvision"
	Dahua     = "dahua"
)

// DeviceInfo describes the recorder.
type DeviceInfo struct {
	Name     string `json:"name"`
	Model    string `json:"model"`
	Serial   string `json:"serial"`
	Firmware string `json:"firmware"`
}

// Channel is one video input of the recorder. ID is the vendor channel
// number used in stream URLs (Hikvision: {ID}01, Dahua: channel={ID}).
type Channel struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	IP     string `json:"ip,omitempty"`   // camera address (IP channels only)
	Port   int    `json:"port,omitempty"` // camera management port
	Online bool   `json:"online"`
	Analog bool   `json:"analog,omitempty"`
}

// Recorder is one vendor's API on one device.
type Recorder interface {
	Vendor() string
	Host() string
	DeviceInfo(ctx context.Context) (DeviceInfo, error)
	Channels(ctx context.Context) ([]Channel, error)
	// RTSPPort returns the RTSP port, DefaultRTSPPort when it cannot be read.
	RTSPPort(ctx context.Context) int
	MainURL(rtspPort, ch int) string
	SubURL(rtspPort, ch int) string
	SnapshotURL(ch int) string
}

// New returns the recorder API of a vendor without contacting the device.
func New(vendor, host string, port int, user, pass string) (Recorder, error) {
	c := newConn(host, port, user, pass)
	switch vendor {
	case Hikvision:
		return &hikvision{c}, nil
	case Dahua:
		return &dahua{c}, nil
	}
	return nil, fmt.Errorf("unknown NVR vendor %q", vendor)
}

// Open connects to a recorder and reads its channels. With vendor Auto it
// tries Hikvision, then Dahua.
func Open(ctx context.Context, vendor, host string, port int, user, pass string) (Recorder, []Channel, error) {
	vendors := []string{vendor}
	if vendor == "" || vendor == Auto {
		vendors = []string{Hikvision, Dahua}
	}
	var lastErr error
	for _, v := range vendors {
		r, err := New(v, host, port, user, pass)
		if err != nil {
			return nil, nil, err
		}
		chs, err := r.Channels(ctx)
		if err == nil {
			return r, chs, nil
		}
		// a login or network failure is the answer; trying another vendor
		// would only hide it
		if !errors.Is(err, ErrUnsupported) {
			return nil, nil, err
		}
		lastErr = err
	}
	return nil, nil, lastErr
}

// ---------------------------------------------------------------- HTTP

// conn is an HTTP connection to a device with Basic/Digest auth.
type conn struct {
	host string
	port int
	user string
	pass string
}

func newConn(host string, port int, user, pass string) conn {
	if port == 0 {
		port = defaultHTTPPort
	}
	return conn{host: host, port: port, user: user, pass: pass}
}

func (c conn) Host() string { return c.host }

func (c conn) base() string { return "http://" + net.JoinHostPort(c.host, strconv.Itoa(c.port)) }

// get fetches a path (with query), answering a 401 challenge once.
func (c conn) get(ctx context.Context, path string) ([]byte, error) {
	raw := c.base() + path
	resp, err := c.do(ctx, raw, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		chal := digest.Pick(resp.Header.Values("WWW-Authenticate"))
		resp.Body.Close()
		if chal == "" {
			return nil, ErrAuth
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		auth, err := digest.Header(chal, http.MethodGet, u.RequestURI(), c.user, c.pass)
		if err != nil {
			return nil, err
		}
		if resp, err = c.do(ctx, raw, auth); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrAuth
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotImplemented:
		return nil, fmt.Errorf("%w: %s returned http %d", ErrUnsupported, path, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: http %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
}

func (c conn) do(ctx context.Context, raw, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return resp, nil
}

func hostPort(host string, port, def int) string {
	if port == 0 {
		port = def
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
