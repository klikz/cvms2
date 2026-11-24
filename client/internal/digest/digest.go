// Package digest builds HTTP/RTSP Basic and Digest (RFC 2617/7616, MD5 and
// SHA-256) Authorization headers from a WWW-Authenticate challenge.
package digest

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
)

// ParseChallenge parses `Digest realm="x", nonce="y", qop="auth"` into a
// lowercase key map. The scheme is returned under the key "scheme".
func ParseChallenge(h string) map[string]string {
	out := map[string]string{}
	h = strings.TrimSpace(h)
	if i := strings.IndexByte(h, ' '); i >= 0 {
		out["scheme"] = strings.ToLower(h[:i])
		h = h[i+1:]
	} else {
		out["scheme"] = strings.ToLower(h)
		return out
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

// Pick chooses the best challenge among several WWW-Authenticate values
// (Digest SHA-256 > Digest MD5 > Basic).
func Pick(challenges []string) string {
	best, score := "", -1
	for _, c := range challenges {
		p := ParseChallenge(c)
		s := 0
		switch {
		case p["scheme"] == "digest" && strings.Contains(strings.ToUpper(p["algorithm"]), "SHA-256"):
			s = 3
		case p["scheme"] == "digest":
			s = 2
		case p["scheme"] == "basic":
			s = 1
		}
		if s > score {
			best, score = c, s
		}
	}
	return best
}

func hexHash(h func() hash.Hash, s string) string {
	x := h()
	x.Write([]byte(s))
	return hex.EncodeToString(x.Sum(nil))
}

// Header returns an Authorization header value answering challenge for
// method and uri (the request path for HTTP, the full URL for RTSP).
func Header(challenge, method, uri, user, pass string) (string, error) {
	c := ParseChallenge(challenge)
	switch c["scheme"] {
	case "basic":
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)), nil
	case "digest":
	default:
		return "", fmt.Errorf("unsupported auth scheme %q", c["scheme"])
	}
	realm, nonce := c["realm"], c["nonce"]
	if nonce == "" {
		return "", errors.New("digest: no nonce")
	}
	alg := strings.ToUpper(c["algorithm"])
	h := md5.New
	if strings.HasPrefix(alg, "SHA-256") {
		h = sha256.New
	}
	ha1 := hexHash(h, user+":"+realm+":"+pass)
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	cnonce := hex.EncodeToString(b)
	if strings.HasSuffix(alg, "-SESS") {
		ha1 = hexHash(h, ha1+":"+nonce+":"+cnonce)
	}
	ha2 := hexHash(h, method+":"+uri)
	const nc = "00000001"
	qop := ""
	for _, q := range strings.Split(c["qop"], ",") {
		if strings.TrimSpace(q) == "auth" {
			qop = "auth"
		}
	}
	var resp string
	if qop == "auth" {
		resp = hexHash(h, ha1+":"+nonce+":"+nc+":"+cnonce+":auth:"+ha2)
	} else {
		resp = hexHash(h, ha1+":"+nonce+":"+ha2)
	}
	out := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`, user, realm, nonce, uri, resp)
	if qop != "" {
		out += fmt.Sprintf(`, qop=auth, nc=%s, cnonce="%s"`, nc, cnonce)
	}
	if o := c["opaque"]; o != "" {
		out += fmt.Sprintf(`, opaque="%s"`, o)
	}
	if alg != "" {
		out += ", algorithm=" + c["algorithm"]
	}
	return out, nil
}

// MD5Hex is exposed for tests that verify digest responses.
func MD5Hex(s string) string { return hexHash(md5.New, s) }
