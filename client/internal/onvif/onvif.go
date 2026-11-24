// Package onvif implements the small subset of ONVIF the client needs:
// WS-Discovery, device information and media profile stream URIs.
package onvif

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"cvms/client/internal/digest"
	"cvms/client/internal/streamurl"
)

// ---------------------------------------------------------------- tiny XML tree

type node struct {
	Name     string
	Attrs    map[string]string
	Text     string
	Children []*node
}

func parseXML(b []byte) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	root := &node{Name: "#root"}
	stack := []*node{root}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{Name: t.Name.Local, Attrs: map[string]string{}}
			for _, a := range t.Attr {
				n.Attrs[a.Name.Local] = a.Value
			}
			p := stack[len(stack)-1]
			p.Children = append(p.Children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			stack[len(stack)-1].Text += string(t)
		}
	}
	return root, nil
}

func (n *node) find(name string) *node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
		if f := c.find(name); f != nil {
			return f
		}
	}
	return nil
}

func (n *node) findAll(name string) []*node {
	var out []*node
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Name == name {
			out = append(out, c)
		} else {
			out = append(out, c.findAll(name)...)
		}
	}
	return out
}

func (n *node) text(path ...string) string {
	cur := n
	for _, p := range path {
		cur = cur.find(p)
		if cur == nil {
			return ""
		}
	}
	return strings.TrimSpace(cur.Text)
}

// ---------------------------------------------------------------- discovery

// Device is a camera found by WS-Discovery.
type Device struct {
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	XAddr    string `json:"xaddr"`
	Name     string `json:"name"`
	Hardware string `json:"hardware"`
}

const probeTpl = `<?xml version="1.0" encoding="UTF-8"?>
<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope" xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl">
<e:Header><w:MessageID>uuid:%s</w:MessageID><w:To e:mustUnderstand="true">urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To><w:Action e:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action></e:Header>
<e:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></e:Body></e:Envelope>`

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Discover sends WS-Discovery probes on every IPv4 interface and collects
// answers until timeout.
func Discover(ctx context.Context, timeout time.Duration) ([]Device, error) {
	group := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}
	var addrs []net.IP
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, _ := ifc.Addrs()
		for _, a := range as {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				addrs = append(addrs, ipn.IP.To4())
			}
		}
	}
	if len(addrs) == 0 {
		addrs = []net.IP{net.IPv4zero}
	}
	found := map[string]Device{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	deadline := time.Now().Add(timeout)
	for _, ip := range addrs {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: 0})
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(conn *net.UDPConn) {
			defer wg.Done()
			defer conn.Close()
			msg := []byte(fmt.Sprintf(probeTpl, uuid()))
			for i := 0; i < 2; i++ {
				_, _ = conn.WriteToUDP(msg, group)
			}
			_ = conn.SetReadDeadline(deadline)
			buf := make([]byte, 65535)
			for ctx.Err() == nil {
				n, from, err := conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				d, ok := parseProbeMatch(buf[:n], from.IP.String())
				if !ok {
					continue
				}
				mu.Lock()
				found[d.XAddr] = d
				mu.Unlock()
			}
		}(conn)
	}
	wg.Wait()
	out := make([]Device, 0, len(found))
	seen := map[string]bool{}
	for _, d := range found {
		if seen[d.IP] {
			continue
		}
		seen[d.IP] = true
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return ipLess(out[i].IP, out[j].IP) })
	return out, nil
}

func ipLess(a, b string) bool {
	pa, pb := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	if pa == nil || pb == nil {
		return a < b
	}
	return bytes.Compare(pa, pb) < 0
}

func parseProbeMatch(b []byte, fromIP string) (Device, bool) {
	root, err := parseXML(b)
	if err != nil || root.find("ProbeMatch") == nil {
		return Device{}, false
	}
	d := Device{IP: fromIP, Port: 80}
	for _, xa := range strings.Fields(root.text("XAddrs")) {
		u, err := url.Parse(xa)
		if err != nil || u.Hostname() == "" {
			continue
		}
		if ip := net.ParseIP(u.Hostname()); ip != nil && ip.To4() == nil {
			continue // skip IPv6 xaddrs
		}
		d.XAddr = xa
		d.IP = u.Hostname()
		if p := u.Port(); p != "" {
			fmt.Sscanf(p, "%d", &d.Port)
		}
		break
	}
	if d.XAddr == "" {
		d.XAddr = "http://" + fromIP + "/onvif/device_service"
	}
	for _, s := range strings.Fields(root.text("Scopes")) {
		switch {
		case strings.Contains(s, "/name/"):
			d.Name, _ = url.PathUnescape(s[strings.Index(s, "/name/")+6:])
		case strings.Contains(s, "/hardware/"):
			d.Hardware, _ = url.PathUnescape(s[strings.Index(s, "/hardware/")+10:])
		}
	}
	return d, true
}

// ---------------------------------------------------------------- SOAP client

// Client talks to one ONVIF device.
type Client struct {
	DeviceURL string
	Username  string
	Password  string
	http      *http.Client
	timeShift time.Duration
	// wsMode selects the WS-Security UsernameToken variant; cameras differ
	// in what they accept (see wsModes). The working mode is kept.
	wsMode int
	// LastAuth describes the authentication that worked (for logs/UI).
	LastAuth string
}

// WS-Security variants tried in order when a device answers NotAuthorized.
var wsModes = []struct {
	name     string
	plain    bool // PasswordText instead of PasswordDigest
	millis   bool // Created with milliseconds
	syncTime bool // use the device clock for Created
}{
	{"digest", false, true, true},
	{"digest-nomillis", false, false, true},
	{"digest-localtime", false, false, false},
	{"plaintext", true, false, true},
}

// NewClient creates a client for host:port.
func NewClient(host string, port int, user, pass string) *Client {
	if port == 0 {
		port = 80
	}
	// Leave the default port out of the URL: some firmwares (Dahua) build
	// stream URIs from the Host header and would answer "host:80:554".
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	if port == 80 {
		addr = host
		if strings.Contains(host, ":") {
			addr = "[" + host + "]"
		}
	}
	return &Client{
		DeviceURL: "http://" + addr + "/onvif/device_service",
		Username:  user,
		Password:  pass,
		http:      &http.Client{Timeout: 8 * time.Second},
	}
}

func (c *Client) security() string {
	if c.Username == "" {
		return ""
	}
	m := wsModes[c.wsMode]
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	now := time.Now()
	if m.syncTime {
		now = now.Add(c.timeShift)
	}
	layout := "2006-01-02T15:04:05Z"
	if m.millis {
		layout = "2006-01-02T15:04:05.000Z"
	}
	created := now.UTC().Format(layout)
	var user, pass bytes.Buffer
	_ = xml.EscapeText(&user, []byte(c.Username))
	pwType, pwValue := "PasswordDigest", ""
	if m.plain {
		pwType = "PasswordText"
		_ = xml.EscapeText(&pass, []byte(c.Password))
		pwValue = pass.String()
	} else {
		h := sha1.New()
		h.Write(nonce)
		h.Write([]byte(created))
		h.Write([]byte(c.Password))
		pwValue = base64.StdEncoding.EncodeToString(h.Sum(nil))
	}
	return `<s:Header><Security s:mustUnderstand="1" xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"><UsernameToken>` +
		`<Username>` + user.String() + `</Username>` +
		`<Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#` + pwType + `">` + pwValue + `</Password>` +
		`<Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">` + base64.StdEncoding.EncodeToString(nonce) + `</Nonce>` +
		`<Created xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">` + created + `</Created>` +
		`</UsernameToken></Security></s:Header>`
}

// ErrAuth is returned when the device rejects the credentials.
var ErrAuth = errors.New("onvif: authentication failed")

// AuthError carries details of a rejected login.
type AuthError struct{ Detail string }

func (e *AuthError) Error() string   { return "onvif: authentication failed (" + e.Detail + ")" }
func (e *AuthError) Is(t error) bool { return t == ErrAuth }

// call sends one SOAP request. With auth, it adds WS-Security and answers
// an HTTP 401 challenge with HTTP Digest/Basic using the same credentials
// (many Dahua/Uniview/newer Hikvision devices require HTTP Digest).
func (c *Client) call(ctx context.Context, endpoint, body string, auth bool) (*node, error) {
	root, status, chal, err := c.do(ctx, endpoint, body, auth, "")
	if err == nil || !auth || c.Username == "" || status != http.StatusUnauthorized || len(chal) == 0 {
		return root, err
	}
	u, _ := url.Parse(endpoint)
	h, herr := digest.Header(digest.Pick(chal), http.MethodPost, u.RequestURI(), c.Username, c.Password)
	if herr != nil {
		return nil, err
	}
	root, status, _, err = c.do(ctx, endpoint, body, auth, h)
	if err == nil {
		c.LastAuth = "http-" + digest.ParseChallenge(digest.Pick(chal))["scheme"] + "+" + wsModes[c.wsMode].name
	} else if status == http.StatusUnauthorized {
		err = &AuthError{Detail: "HTTP 401 with " + digest.ParseChallenge(digest.Pick(chal))["scheme"]}
	}
	return root, err
}

func (c *Client) do(ctx context.Context, endpoint, body string, auth bool, authHeader string) (*node, int, []string, error) {
	hdr := ""
	if auth {
		hdr = c.security()
	}
	env := `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">` + hdr + `<s:Body>` + body + `</s:Body></s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(env))
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Content-Type", `application/soap+xml; charset=utf-8`)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	chal := resp.Header.Values("WWW-Authenticate")
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, chal, err
	}
	root, perr := parseXML(b)
	if perr == nil {
		if f := root.find("Fault"); f != nil {
			reason := root.text("Reason", "Text")
			sub := strings.ToLower(root.text("Subcode", "Value") + " " + reason)
			if resp.StatusCode == 401 || strings.Contains(sub, "notauthorized") || strings.Contains(sub, "not authorized") || strings.Contains(sub, "authenticat") {
				return nil, resp.StatusCode, chal, &AuthError{Detail: "SOAP " + strings.TrimSpace(root.text("Subcode", "Value"))}
			}
			return nil, resp.StatusCode, chal, fmt.Errorf("onvif fault: %s", reason)
		}
	}
	if resp.StatusCode == 401 {
		return nil, resp.StatusCode, chal, &AuthError{Detail: "HTTP 401"}
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, chal, fmt.Errorf("onvif: http %d", resp.StatusCode)
	}
	if perr != nil {
		return nil, resp.StatusCode, chal, fmt.Errorf("onvif: bad response (%d)", resp.StatusCode)
	}
	if auth && c.LastAuth == "" {
		c.LastAuth = wsModes[c.wsMode].name
	}
	return root, resp.StatusCode, chal, nil
}

// callAuth is call with credentials, trying the WS-Security variants in
// turn when the device rejects the login. The first working one is kept.
func (c *Client) callAuth(ctx context.Context, endpoint, body string) (*node, error) {
	start := c.wsMode
	var firstErr error
	for i := start; i < len(wsModes); i++ {
		c.wsMode = i
		root, err := c.call(ctx, endpoint, body, true)
		if err == nil || !errors.Is(err, ErrAuth) || c.Username == "" {
			return root, err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	c.wsMode = start
	return nil, firstErr
}

// Ping checks that an ONVIF device service answers (no credentials needed).
func (c *Client) Ping(ctx context.Context) bool {
	root, err := c.call(ctx, c.DeviceURL, `<GetSystemDateAndTime xmlns="http://www.onvif.org/ver10/device/wsdl"/>`, false)
	return err == nil && root.find("SystemDateAndTime") != nil
}

// syncTime adjusts the WS-Security timestamp to the device clock.
func (c *Client) syncTime(ctx context.Context) {
	root, err := c.call(ctx, c.DeviceURL, `<GetSystemDateAndTime xmlns="http://www.onvif.org/ver10/device/wsdl"/>`, false)
	if err != nil {
		return
	}
	utc := root.find("UTCDateTime")
	if utc == nil {
		return
	}
	var y, mo, d, h, mi, s int
	fmt.Sscan(utc.text("Date", "Year"), &y)
	fmt.Sscan(utc.text("Date", "Month"), &mo)
	fmt.Sscan(utc.text("Date", "Day"), &d)
	fmt.Sscan(utc.text("Time", "Hour"), &h)
	fmt.Sscan(utc.text("Time", "Minute"), &mi)
	fmt.Sscan(utc.text("Time", "Second"), &s)
	if y < 2000 {
		return
	}
	dev := time.Date(y, time.Month(mo), d, h, mi, s, 0, time.UTC)
	c.timeShift = time.Until(dev)
}

// Profile is a media profile with its stream URI.
type Profile struct {
	Token  string `json:"token"`
	Name   string `json:"name"`
	Codec  string `json:"codec"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	URI    string `json:"uri"`
	// SnapshotURI is an HTTP JPEG URL (cheaper than decoding RTSP).
	SnapshotURI string `json:"snapshot_uri"`
}

// Info is the result of probing a device.
type Info struct {
	Manufacturer string    `json:"manufacturer"`
	Model        string    `json:"model"`
	Firmware     string    `json:"firmware"`
	Serial       string    `json:"serial"`
	Profiles     []Profile `json:"profiles"`
}

// Probe reads device info and all media profiles with RTSP URIs.
func (c *Client) Probe(ctx context.Context) (*Info, error) {
	c.syncTime(ctx)
	info := &Info{}
	// Device info is optional: some devices protect only some calls, so an
	// auth error here does not stop the probe.
	root, authErr := c.callAuth(ctx, c.DeviceURL, `<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>`)
	if authErr == nil {
		info.Manufacturer = root.text("Manufacturer")
		info.Model = root.text("Model")
		info.Firmware = root.text("FirmwareVersion")
		info.Serial = root.text("SerialNumber")
	} else if !errors.Is(authErr, ErrAuth) {
		authErr = nil
	}
	mediaURL := c.DeviceURL
	caps := `<GetCapabilities xmlns="http://www.onvif.org/ver10/device/wsdl"><Category>Media</Category></GetCapabilities>`
	if root, err := c.callAuth(ctx, c.DeviceURL, caps); err == nil {
		if m := root.find("Media"); m != nil {
			if x := m.text("XAddr"); x != "" {
				mediaURL = c.fixHost(x)
			}
		}
	} else if root, err := c.call(ctx, c.DeviceURL, caps, false); err == nil {
		if m := root.find("Media"); m != nil {
			if x := m.text("XAddr"); x != "" {
				mediaURL = c.fixHost(x)
			}
		}
	}
	profiles := `<GetProfiles xmlns="http://www.onvif.org/ver10/media/wsdl"/>`
	root, err := c.callAuth(ctx, mediaURL, profiles)
	authed := true
	if err != nil && errors.Is(err, ErrAuth) {
		// last resort: devices with ONVIF authentication turned off
		if r, err2 := c.call(ctx, mediaURL, profiles, false); err2 == nil {
			root, err, authed = r, nil, false
		}
	}
	if err != nil {
		if authErr != nil && errors.Is(err, ErrAuth) {
			return nil, authErr
		}
		return nil, err
	}
	callMedia := func(body string) (*node, error) {
		if authed {
			return c.callAuth(ctx, mediaURL, body)
		}
		return c.call(ctx, mediaURL, body, false)
	}
	for _, p := range root.findAll("Profiles") {
		pr := Profile{Token: p.Attrs["token"], Name: p.text("Name")}
		if vec := p.find("VideoEncoderConfiguration"); vec != nil {
			pr.Codec = normCodec(vec.text("Encoding"))
			fmt.Sscan(vec.text("Resolution", "Width"), &pr.Width)
			fmt.Sscan(vec.text("Resolution", "Height"), &pr.Height)
		}
		if pr.Token == "" {
			continue
		}
		body := `<GetStreamUri xmlns="http://www.onvif.org/ver10/media/wsdl"><StreamSetup><Stream xmlns="http://www.onvif.org/ver10/schema">RTP-Unicast</Stream><Transport xmlns="http://www.onvif.org/ver10/schema"><Protocol>RTSP</Protocol></Transport></StreamSetup><ProfileToken>` + xmlEscape(pr.Token) + `</ProfileToken></GetStreamUri>`
		if r, err := callMedia(body); err == nil {
			pr.URI = c.fixHost(r.text("MediaUri", "Uri"))
		}
		snapBody := `<GetSnapshotUri xmlns="http://www.onvif.org/ver10/media/wsdl"><ProfileToken>` + xmlEscape(pr.Token) + `</ProfileToken></GetSnapshotUri>`
		if r, err := callMedia(snapBody); err == nil {
			if u := r.text("MediaUri", "Uri"); strings.HasPrefix(strings.ToLower(u), "http") {
				pr.SnapshotURI = c.fixHost(u)
			}
		}
		if pr.URI != "" {
			info.Profiles = append(info.Profiles, pr)
		}
	}
	if len(info.Profiles) == 0 {
		return nil, errors.New("onvif: no video profiles with RTSP URI")
	}
	return info, nil
}

// fixHost replaces the host of a device-reported URL with the address we
// used to reach the device (cameras behind NAT often report internal IPs).
func (c *Client) fixHost(raw string) string {
	raw, _ = streamurl.Normalize(raw) // "host:80:554" from Host-header-echoing firmwares
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	d, err := url.Parse(c.DeviceURL)
	if err != nil {
		return raw
	}
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = "" // default port: keep it out of the Host header (see NewClient)
	}
	if port == "" {
		u.Host = d.Hostname()
	} else {
		u.Host = net.JoinHostPort(d.Hostname(), port)
	}
	return u.String()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func normCodec(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "265") || strings.Contains(s, "hevc"):
		return "h265"
	case strings.Contains(s, "264"):
		return "h264"
	case strings.Contains(s, "jpeg"):
		return "mjpeg"
	}
	return s
}

// PickStreams chooses main (largest) and sub (smallest other) profiles.
func PickStreams(ps []Profile) (main, sub Profile) {
	if len(ps) == 0 {
		return
	}
	sorted := append([]Profile(nil), ps...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Width*sorted[i].Height > sorted[j].Width*sorted[j].Height })
	main = sorted[0]
	for i := len(sorted) - 1; i > 0; i-- {
		if sorted[i].URI != main.URI && sorted[i].Codec != "mjpeg" {
			sub = sorted[i]
			break
		}
	}
	return
}
