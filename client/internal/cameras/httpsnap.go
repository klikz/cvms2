package cameras

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var snapHTTP = &http.Client{Timeout: 6 * time.Second}

// httpSnapshot fetches a JPEG from a camera HTTP snapshot URL, answering
// Basic or Digest authentication challenges.
func httpSnapshot(ctx context.Context, rawURL, user, pass string) ([]byte, error) {
	resp, err := snapGet(ctx, rawURL, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && user != "" {
		chal := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		var auth string
		switch {
		case strings.HasPrefix(strings.ToLower(chal), "digest"):
			u, _ := url.Parse(rawURL)
			auth, err = digestAuth(chal, http.MethodGet, u.RequestURI(), user, pass)
			if err != nil {
				return nil, err
			}
		default:
			req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
			req.SetBasicAuth(user, pass)
			auth = req.Header.Get("Authorization")
		}
		resp, err = snapGet(ctx, rawURL, auth)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snapshot http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, errors.New("snapshot is not a JPEG")
	}
	return b, nil
}

func snapGet(ctx context.Context, u, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return snapHTTP.Do(req)
}

// parseChallenge parses `Digest realm="x", nonce="y", qop="auth"`.
func parseChallenge(h string) map[string]string {
	out := map[string]string{}
	h = strings.TrimSpace(h)
	if i := strings.IndexByte(h, ' '); i >= 0 {
		h = h[i+1:]
	}
	for len(h) > 0 {
		h = strings.TrimLeft(h, " ,")
		eq := strings.IndexByte(h, '=')
		if eq < 0 {
			break
		}
		k := strings.ToLower(strings.TrimSpace(h[:eq]))
		h = h[eq+1:]
		var v string
		if strings.HasPrefix(h, `"`) {
			end := strings.IndexByte(h[1:], '"')
			if end < 0 {
				v, h = h[1:], ""
			} else {
				v, h = h[1:end+1], h[end+2:]
			}
		} else {
			end := strings.IndexByte(h, ',')
			if end < 0 {
				v, h = h, ""
			} else {
				v, h = h[:end], h[end:]
			}
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// digestAuth builds an RFC 2617 Authorization header (MD5, qop=auth).
func digestAuth(challenge, method, uri, user, pass string) (string, error) {
	c := parseChallenge(challenge)
	realm, nonce := c["realm"], c["nonce"]
	if nonce == "" {
		return "", errors.New("digest: no nonce")
	}
	ha1 := md5hex(user + ":" + realm + ":" + pass)
	ha2 := md5hex(method + ":" + uri)
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	cnonce := hex.EncodeToString(b)
	const nc = "00000001"
	qop := ""
	for _, q := range strings.Split(c["qop"], ",") {
		if strings.TrimSpace(q) == "auth" {
			qop = "auth"
		}
	}
	var resp string
	if qop == "auth" {
		resp = md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	} else {
		resp = md5hex(ha1 + ":" + nonce + ":" + ha2)
	}
	h := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`, user, realm, nonce, uri, resp)
	if qop != "" {
		h += fmt.Sprintf(`, qop=auth, nc=%s, cnonce="%s"`, nc, cnonce)
	}
	if o := c["opaque"]; o != "" {
		h += fmt.Sprintf(`, opaque="%s"`, o)
	}
	return h + `, algorithm=MD5`, nil
}
