// Package hikvision reads the channel list of a Hikvision NVR/DVR over ISAPI
// so its cameras can be added one by one.
package hikvision

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cvms/client/internal/digest"
)

var (
	ErrAuth          = errors.New("NVR refused the login")
	ErrUnreachable   = errors.New("NVR is unreachable")
	ErrNotHikvision  = errors.New("device does not answer Hikvision ISAPI")
	DefaultRTSPPort  = 554
	defaultHTTPPort  = 80
	requestTimeout   = 8 * time.Second
	maxResponseBytes = int64(4 << 20)
)

var httpClient = &http.Client{Timeout: requestTimeout}

// Client talks to one device over ISAPI (HTTP, Basic or Digest auth).
type Client struct {
	Host string
	Port int
	User string
	Pass string
}

func NewClient(host string, port int, user, pass string) *Client {
	if port == 0 {
		port = defaultHTTPPort
	}
	return &Client{Host: host, Port: port, User: user, Pass: pass}
}

func (c *Client) base() string {
	return "http://" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// get fetches an ISAPI path, answering a 401 challenge once.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
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
		u, _ := url.Parse(raw)
		auth, err := digest.Header(chal, http.MethodGet, u.RequestURI(), c.User, c.Pass)
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
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s returned 404", ErrNotHikvision, path)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: http %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
}

func (c *Client) do(ctx context.Context, raw, auth string) (*http.Response, error) {
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

func (c *Client) getXML(ctx context.Context, path string, v any) error {
	b, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrNotHikvision, path, err)
	}
	return nil
}

// ---------------------------------------------------------------- device info

type DeviceInfo struct {
	Name     string `xml:"deviceName" json:"name"`
	Model    string `xml:"model" json:"model"`
	Serial   string `xml:"serialNumber" json:"serial"`
	Firmware string `xml:"firmwareVersion" json:"firmware"`
}

func (c *Client) DeviceInfo(ctx context.Context) (DeviceInfo, error) {
	var d DeviceInfo
	err := c.getXML(ctx, "/ISAPI/System/deviceInfo", &d)
	d.Name, d.Model = strings.TrimSpace(d.Name), strings.TrimSpace(d.Model)
	return d, err
}

// ---------------------------------------------------------------- channels

// Channel is one video input of the recorder. ID is the ISAPI channel
// number: its streams are /Streaming/Channels/{ID}01 and {ID}02.
type Channel struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	IP     string `json:"ip,omitempty"`   // camera address (IP channels only)
	Port   int    `json:"port,omitempty"` // camera management port
	Online bool   `json:"online"`
	Analog bool   `json:"analog,omitempty"`
}

type portDescriptor struct {
	IP   string `xml:"ipAddress"`
	Port int    `xml:"managePortNo"`
}

type proxyChannelList struct {
	Channels []struct {
		ID     int            `xml:"id"`
		Name   string         `xml:"name"`
		Source portDescriptor `xml:"sourceInputPortDescriptor"`
	} `xml:"InputProxyChannel"`
}

type proxyStatusList struct {
	Status []struct {
		ID     int    `xml:"id"`
		Online string `xml:"online"`
	} `xml:"InputProxyChannelStatus"`
}

type analogChannelList struct {
	Channels []struct {
		ID      int    `xml:"id"`
		Name    string `xml:"name"`
		Enabled string `xml:"videoInputEnabled"`
	} `xml:"VideoInputChannel"`
}

// Channels lists IP channels (InputProxy) and, on DVRs, analog inputs.
func (c *Client) Channels(ctx context.Context) ([]Channel, error) {
	var ip proxyChannelList
	ipErr := c.getXML(ctx, "/ISAPI/ContentMgmt/InputProxy/channels", &ip)
	if errors.Is(ipErr, ErrAuth) || errors.Is(ipErr, ErrUnreachable) || ctx.Err() != nil {
		return nil, ipErr
	}
	online := map[int]bool{}
	if ipErr == nil {
		var st proxyStatusList
		if err := c.getXML(ctx, "/ISAPI/ContentMgmt/InputProxy/channels/status", &st); err == nil {
			for _, s := range st.Status {
				online[s.ID] = strings.EqualFold(strings.TrimSpace(s.Online), "true")
			}
		}
	}
	var an analogChannelList
	anErr := c.getXML(ctx, "/ISAPI/System/Video/inputs/channels", &an)
	if ipErr != nil && anErr != nil {
		return nil, ipErr
	}

	seen := map[int]bool{}
	var out []Channel
	for _, a := range an.Channels {
		if a.ID <= 0 || strings.EqualFold(strings.TrimSpace(a.Enabled), "false") {
			continue
		}
		seen[a.ID] = true
		out = append(out, Channel{ID: a.ID, Name: strings.TrimSpace(a.Name), Online: true, Analog: true})
	}
	for _, p := range ip.Channels {
		if p.ID <= 0 || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		out = append(out, Channel{
			ID: p.ID, Name: strings.TrimSpace(p.Name),
			IP: strings.TrimSpace(p.Source.IP), Port: p.Source.Port,
			Online: online[p.ID],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ---------------------------------------------------------------- ports & URLs

type adminAccessList struct {
	Protocols []struct {
		Protocol string `xml:"protocol"`
		Port     int    `xml:"portNo"`
		Enabled  string `xml:"enabled"`
	} `xml:"AdminAccessProtocol"`
}

// RTSPPort returns the recorder's RTSP port, 554 when it cannot be read.
func (c *Client) RTSPPort(ctx context.Context) int {
	var l adminAccessList
	if err := c.getXML(ctx, "/ISAPI/Security/adminAccess", &l); err == nil {
		for _, p := range l.Protocols {
			if strings.EqualFold(strings.TrimSpace(p.Protocol), "RTSP") && p.Port > 0 {
				return p.Port
			}
		}
	}
	return DefaultRTSPPort
}

func streamURL(host string, rtspPort, ch, stream int) string {
	if rtspPort == 0 {
		rtspPort = DefaultRTSPPort
	}
	return fmt.Sprintf("rtsp://%s/Streaming/Channels/%d%02d", net.JoinHostPort(host, strconv.Itoa(rtspPort)), ch, stream)
}

// MainURL is the main stream of a channel through the recorder.
func MainURL(host string, rtspPort, ch int) string { return streamURL(host, rtspPort, ch, 1) }

// SubURL is the sub stream of a channel through the recorder.
func SubURL(host string, rtspPort, ch int) string { return streamURL(host, rtspPort, ch, 2) }

// SnapshotURL is the JPEG snapshot of a channel through the recorder.
func SnapshotURL(host string, httpPort, ch int) string {
	if httpPort == 0 {
		httpPort = defaultHTTPPort
	}
	return fmt.Sprintf("http://%s/ISAPI/Streaming/channels/%d01/picture", net.JoinHostPort(host, strconv.Itoa(httpPort)), ch)
}
