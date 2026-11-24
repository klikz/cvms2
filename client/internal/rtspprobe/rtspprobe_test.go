package rtspprobe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"

	"cvms/client/internal/digest"
)

// fakeRTSP serves DESCRIBE for the given paths with Digest auth.
func fakeRTSP(t *testing.T, user, pass string, paths map[string]bool) (string, int) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	const realm, nonce = "cam", "n0nce"
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					parts := strings.Fields(line)
					hdr := map[string]string{}
					for {
						l, err := r.ReadString('\n')
						if err != nil {
							return
						}
						l = strings.TrimSpace(l)
						if l == "" {
							break
						}
						k, v, _ := strings.Cut(l, ":")
						hdr[strings.ToLower(k)] = strings.TrimSpace(v)
					}
					cseq := hdr["cseq"]
					auth := hdr["authorization"]
					ok := false
					if auth != "" {
						p := digest.ParseChallenge(auth)
						ha1 := digest.MD5Hex(user + ":" + realm + ":" + pass)
						ha2 := digest.MD5Hex("DESCRIBE:" + p["uri"])
						ok = p["response"] == digest.MD5Hex(ha1+":"+nonce+":"+ha2)
					}
					if !ok {
						fmt.Fprintf(c, "RTSP/1.0 401 Unauthorized\r\nCSeq: %s\r\nWWW-Authenticate: Digest realm=\"%s\", nonce=\"%s\"\r\n\r\n", cseq, realm, nonce)
						continue
					}
					path := parts[1][strings.Index(parts[1][7:], "/")+7:]
					if !paths[path] {
						fmt.Fprintf(c, "RTSP/1.0 404 Not Found\r\nCSeq: %s\r\n\r\n", cseq)
						continue
					}
					sdp := "v=0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H265/90000\r\n"
					fmt.Fprintf(c, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s", cseq, len(sdp), sdp)
				}
			}(c)
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return host, p
}

func TestFindDahua(t *testing.T) {
	host, port := fakeRTSP(t, "admin", "pa:ss", map[string]bool{
		"/cam/realmonitor?channel=1&subtype=0": true,
		"/cam/realmonitor?channel=1&subtype=1": true,
	})
	f, err := Find(context.Background(), host, port, "admin", "pa:ss")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.Main, "subtype=0") || !strings.Contains(f.Sub, "subtype=1") || !strings.HasPrefix(f.Vendor, "Dahua") {
		t.Fatalf("%+v", f)
	}
}

func TestFindWrongPassword(t *testing.T) {
	host, port := fakeRTSP(t, "admin", "right", map[string]bool{"/Streaming/Channels/101": true})
	if _, err := Find(context.Background(), host, port, "admin", "wrong"); !errors.Is(err, ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
}

func TestFindUnreachable(t *testing.T) {
	if _, err := Find(context.Background(), "127.0.0.1", 1, "a", "b"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
}
