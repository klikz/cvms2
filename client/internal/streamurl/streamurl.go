// Package streamurl repairs camera URLs that devices report in a broken form.
//
// Some camera firmwares (seen on Dahua IPCs) build the ONVIF stream URI from
// the HTTP Host header, so a request to "10.0.0.5:80" comes back as
// "rtsp://10.0.0.5:80:554/...". Go's url.Parse rejects such hosts, which made
// the camera look invalid to the status checker while ffmpeg still played it.
package streamurl

import (
	"strings"
)

// Normalize returns raw with a multi-port authority ("host:80:554") reduced to
// its last port ("host:554"). Valid URLs, IPv6 literals and anything it does
// not understand are returned unchanged (apart from surrounding whitespace).
// changed reports whether the URL was modified.
func Normalize(raw string) (fixed string, changed bool) {
	s := strings.TrimSpace(raw)
	i := strings.Index(s, "://")
	if i <= 0 {
		return s, s != raw
	}
	rest := s[i+3:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority, tail := rest[:end], rest[end:]
	userinfo, hostport := "", authority
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		userinfo, hostport = authority[:at+1], authority[at+1:]
	}
	if strings.HasPrefix(hostport, "[") || strings.Count(hostport, ":") < 2 {
		return s, s != raw
	}
	parts := strings.Split(hostport, ":")
	host, port := parts[0], parts[len(parts)-1]
	if host == "" || !digits(port) {
		return s, s != raw
	}
	// every middle segment must be a port too, otherwise this may be an
	// unbracketed IPv6 address we should not touch
	for _, p := range parts[1 : len(parts)-1] {
		if !digits(p) {
			return s, s != raw
		}
	}
	return s[:i+3] + userinfo + host + ":" + port + tail, true
}

func digits(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
