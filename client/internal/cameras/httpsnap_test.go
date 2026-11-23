package cameras

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var jpeg = []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}

func TestHTTPSnapshotDigest(t *testing.T) {
	const realm, nonce = "cam", "abc123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		if !strings.HasPrefix(a, "Digest ") {
			w.Header().Set("WWW-Authenticate", `Digest realm="`+realm+`", qop="auth", nonce="`+nonce+`", opaque="o1"`)
			w.WriteHeader(401)
			return
		}
		p := parseChallenge(a)
		ha1 := md5hex("admin:" + realm + ":secret")
		ha2 := md5hex("GET:" + p["uri"])
		want := md5hex(ha1 + ":" + nonce + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
		if p["response"] != want || p["opaque"] != "o1" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write(jpeg)
	}))
	defer srv.Close()
	b, err := httpSnapshot(context.Background(), srv.URL+"/snap.jpg?ch=1", "admin", "secret")
	if err != nil || len(b) != len(jpeg) {
		t.Fatalf("digest snapshot: %v", err)
	}
	if _, err := httpSnapshot(context.Background(), srv.URL+"/snap.jpg", "admin", "wrong"); err == nil {
		t.Fatal("wrong password must fail")
	}
}

func TestHTTPSnapshotBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" {
			w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write(jpeg)
	}))
	defer srv.Close()
	if _, err := httpSnapshot(context.Background(), srv.URL, "u", "p"); err != nil {
		t.Fatal(err)
	}
}
