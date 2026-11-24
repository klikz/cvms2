// Package rtspprobe checks RTSP URLs with a DESCRIBE request (answering
// Basic/Digest authentication) and finds a camera's stream URLs by trying
// the paths used by common vendors. It is much faster than ffprobe, so
// many candidates can be tried when ONVIF is not available.
package rtspprobe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cvms/client/internal/digest"
)

// Errors returned by Find.
var (
	ErrAuth        = errors.New("rtsp: authentication failed")
	ErrNotFound    = errors.New("rtsp: no known stream path answered")
	ErrUnreachable = errors.New("rtsp: port not reachable")
)

// Result of one DESCRIBE.
type Result struct {
	Status int  // RTSP status code (200, 401, 404, ...)
	Video  bool // the SDP contains a video track
}

type response struct {
	status int
	header map[string][]string
	body   []byte
}

func readResponse(r *bufio.Reader) (*response, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	parts := strings.Fields(line)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "RTSP/") {
		return nil, fmt.Errorf("rtsp: bad status line %q", strings.TrimSpace(line))
	}
	resp := &response{header: map[string][]string{}}
	resp.status, _ = strconv.Atoi(parts[1])
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		k, v, ok := strings.Cut(l, ":")
		if ok {
			k = strings.ToLower(strings.TrimSpace(k))
			resp.header[k] = append(resp.header[k], strings.TrimSpace(v))
		}
	}
	if cl := resp.header["content-length"]; len(cl) > 0 {
		n, _ := strconv.Atoi(cl[0])
		if n > 0 && n < 1<<20 {
			resp.body = make([]byte, n)
			if _, err := io.ReadFull(r, resp.body); err != nil {
				return nil, err
			}
		}
	}
	return resp, nil
}

// Describe sends DESCRIBE to rawURL (credentials in the URL are ignored;
// user/pass are used) and answers one auth challenge.
func Describe(ctx context.Context, rawURL, user, pass string) (Result, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return Result{}, fmt.Errorf("rtsp: bad url %q", rawURL)
	}
	u.User = nil
	clean := u.String()
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "554")
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return Result{}, ErrUnreachable
	}
	defer conn.Close()
	deadline := time.Now().Add(4 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	r := bufio.NewReader(conn)
	send := func(cseq int, auth string) (*response, error) {
		req := "DESCRIBE " + clean + " RTSP/1.0\r\nCSeq: " + strconv.Itoa(cseq) + "\r\nAccept: application/sdp\r\nUser-Agent: CVMS\r\n"
		if auth != "" {
			req += "Authorization: " + auth + "\r\n"
		}
		if _, err := io.WriteString(conn, req+"\r\n"); err != nil {
			return nil, err
		}
		return readResponse(r)
	}
	resp, err := send(1, "")
	if err != nil {
		return Result{}, err
	}
	if resp.status == 401 && user != "" {
		chal := digest.Pick(resp.header["www-authenticate"])
		auth, err := digest.Header(chal, "DESCRIBE", clean, user, pass)
		if err != nil {
			return Result{Status: 401}, nil
		}
		if resp, err = send(2, auth); err != nil {
			return Result{}, err
		}
	}
	return Result{Status: resp.status, Video: strings.Contains(string(resp.body), "m=video")}, nil
}

// Candidate is a pair of main/sub paths used by a vendor.
type Candidate struct {
	Vendor string
	Main   string
	Sub    string
}

// Candidates lists common RTSP paths, most widespread first.
var Candidates = []Candidate{
	{"Hikvision", "/Streaming/Channels/101", "/Streaming/Channels/102"},
	{"Dahua / Amcrest / IMOU", "/cam/realmonitor?channel=1&subtype=0", "/cam/realmonitor?channel=1&subtype=1"},
	{"Uniview", "/media/video1", "/media/video2"},
	{"Uniview (unicast)", "/unicast/c1/s0/live", "/unicast/c1/s1/live"},
	{"TP-Link Tapo / VIGI", "/stream1", "/stream2"},
	{"Reolink (H.264)", "/h264Preview_01_main", "/h264Preview_01_sub"},
	{"Reolink (H.265)", "/h265Preview_01_main", "/h265Preview_01_sub"},
	{"Axis", "/axis-media/media.amp", "/axis-media/media.amp?resolution=640x360"},
	{"Milesight", "/main", "/sub"},
	{"Hanwha / Wisenet", "/profile2/media.smp", "/profile3/media.smp"},
	{"Vivotek", "/live1s1.sdp", "/live1s2.sdp"},
	{"Generic (live/ch00)", "/live/ch00_0", "/live/ch00_1"},
	{"Generic (11/12)", "/11", "/12"},
	{"Generic (ch0)", "/ch0_0.h264", "/ch0_1.h264"},
	{"Generic (live/main)", "/live/main", "/live/sub"},
	{"Generic (h264)", "/h264", ""},
	{"Generic (onvif1)", "/onvif1", "/onvif2"},
	{"Generic (stream0)", "/stream0", "/stream1"},
	{"Generic (live)", "/live", ""},
	{"Generic (root)", "/", ""},
}

// Found is the result of Find.
type Found struct {
	Vendor string `json:"vendor"`
	Main   string `json:"main"`
	Sub    string `json:"sub"`
}

func buildURL(host string, port int, path string) string {
	return "rtsp://" + net.JoinHostPort(host, strconv.Itoa(port)) + path
}

// Find tries the candidate paths on host:port (554 when 0) and returns the
// first one that answers DESCRIBE with a video SDP, plus its sub stream.
func Find(ctx context.Context, host string, port int, user, pass string) (Found, error) {
	if port == 0 {
		port = 554
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	authFailed := false
	for _, c := range Candidates {
		if ctx.Err() != nil {
			break
		}
		res, err := Describe(ctx, buildURL(host, port, c.Main), user, pass)
		if errors.Is(err, ErrUnreachable) {
			return Found{}, ErrUnreachable
		}
		if err != nil {
			continue
		}
		if res.Status == 401 {
			authFailed = true
			continue
		}
		if res.Status != 200 || !res.Video {
			continue
		}
		f := Found{Vendor: c.Vendor, Main: buildURL(host, port, c.Main)}
		if c.Sub != "" {
			if r2, err := Describe(ctx, buildURL(host, port, c.Sub), user, pass); err == nil && r2.Status == 200 && r2.Video {
				f.Sub = buildURL(host, port, c.Sub)
			}
		}
		return f, nil
	}
	if authFailed {
		return Found{}, ErrAuth
	}
	return Found{}, ErrNotFound
}
