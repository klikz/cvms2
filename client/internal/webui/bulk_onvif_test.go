package webui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cvms/client/internal/cameras"
	"cvms/client/internal/config"
	"cvms/client/internal/secret"
	"cvms/client/internal/tunnel"
)

// fakeONVIF answers the SOAP calls the client makes. Requests without a
// WS-Security header for protected calls get a NotAuthorized fault when
// wantPass does not match.
func fakeONVIF(t *testing.T, wantUser string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		w.Header().Set("Content-Type", "application/soap+xml")
		env := func(inner string) {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>` + inner + `</s:Body></s:Envelope>`))
		}
		if !strings.Contains(body, "GetSystemDateAndTime") && !strings.Contains(body, "<Username>"+wantUser+"</Username>") {
			w.WriteHeader(400)
			env(`<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Code><s:Reason><s:Text>Not Authorized</s:Text></s:Reason></s:Fault>`)
			return
		}
		host := r.Host
		switch {
		case strings.Contains(body, "GetSystemDateAndTime"):
			env(`<GetSystemDateAndTimeResponse><SystemDateAndTime><UTCDateTime><Date><Year>2026</Year><Month>1</Month><Day>1</Day></Date><Time><Hour>0</Hour><Minute>0</Minute><Second>0</Second></Time></UTCDateTime></SystemDateAndTime></GetSystemDateAndTimeResponse>`)
		case strings.Contains(body, "GetDeviceInformation"):
			env(`<GetDeviceInformationResponse><Manufacturer>Acme</Manufacturer><Model>Cam9</Model><FirmwareVersion>1.0</FirmwareVersion></GetDeviceInformationResponse>`)
		case strings.Contains(body, "GetCapabilities"):
			env(`<GetCapabilitiesResponse><Capabilities><Media><XAddr>http://` + host + `/onvif/media</XAddr></Media></Capabilities></GetCapabilitiesResponse>`)
		case strings.Contains(body, "GetProfiles"):
			env(`<GetProfilesResponse><Profiles token="p0"><Name>main</Name><VideoEncoderConfiguration><Encoding>H265</Encoding><Resolution><Width>1920</Width><Height>1080</Height></Resolution></VideoEncoderConfiguration></Profiles><Profiles token="p1"><Name>sub</Name><VideoEncoderConfiguration><Encoding>H265</Encoding><Resolution><Width>640</Width><Height>360</Height></Resolution></VideoEncoderConfiguration></Profiles></GetProfilesResponse>`)
		case strings.Contains(body, "GetStreamUri"):
			tok := "p0"
			if strings.Contains(body, ">p1<") {
				tok = "p1"
			}
			env(`<GetStreamUriResponse><MediaUri><Uri>rtsp://10.9.9.9:554/` + tok + `</Uri></MediaUri></GetStreamUriResponse>`)
		case strings.Contains(body, "GetSnapshotUri"):
			env(`<GetSnapshotUriResponse><MediaUri><Uri>http://10.9.9.9/snap.jpg</Uri></MediaUri></GetSnapshotUriResponse>`)
		default:
			w.WriteHeader(400)
		}
	}))
}

func testServer(t *testing.T) *Server {
	dir := t.TempDir()
	box, err := secret.Open(filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	store, _, err := config.Load(filepath.Join(dir, "config.yaml"), box)
	if err != nil {
		t.Fatal(err)
	}
	cams := cameras.New(store)
	return New(store, cams, tunnel.New(store, cams, "test"), nil, "test")
}

func TestBulkAddONVIF(t *testing.T) {
	onv := fakeONVIF(t, "admin")
	defer onv.Close()
	host, portS, _ := net.SplitHostPort(strings.TrimPrefix(onv.URL, "http://"))
	port, _ := strconv.Atoi(portS)

	s := testServer(t)
	body, _ := json.Marshal(map[string]any{"username": "admin", "password": "pw",
		"devices": []map[string]any{{"host": host, "port": port}, {"host": "127.0.0.1", "port": 1}}})
	rec := httptest.NewRecorder()
	s.handleBulk(rec, httptest.NewRequest("POST", "/api/cameras/bulk", bytes.NewReader(body)))
	var out struct {
		Results []bulkResult `json:"results"`
		Added   int          `json:"added"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if out.Added != 1 || !out.Results[0].OK || out.Results[1].OK {
		t.Fatalf("unexpected results: %+v", out)
	}
	cams := s.store.Get().Cameras
	if len(cams) != 1 {
		t.Fatalf("want 1 camera, got %d", len(cams))
	}
	c := cams[0]
	if c.Name != "Acme Cam9" || c.Password != "pw" || c.Main.URL != "rtsp://"+host+":554/p0" || c.Sub.URL != "rtsp://"+host+":554/p1" ||
		c.Main.SnapshotURL != "http://"+host+"/snap.jpg" {
		t.Fatalf("bad camera: %+v", c)
	}

	// wrong user: auth error code, nothing added
	body, _ = json.Marshal(map[string]any{"username": "bob", "password": "x", "devices": []map[string]any{{"host": "localhost", "port": port}}})
	rec = httptest.NewRecorder()
	s.handleBulk(rec, httptest.NewRequest("POST", "/api/cameras/bulk", bytes.NewReader(body)))
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Added != 0 || out.Results[0].Code != "onvif_auth" {
		t.Fatalf("want onvif_auth, got %+v", out.Results)
	}
}

// fakeRTSPServer answers DESCRIBE on the Hikvision path without auth.
func fakeRTSPServer(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					req := string(buf[:n])
					cseq := "1"
					if i := strings.Index(req, "CSeq: "); i >= 0 {
						cseq = strings.Fields(req[i+6:])[0]
					}
					if strings.Contains(req, "/Streaming/Channels/10") {
						sdp := "v=0\r\nm=video 0 RTP/AVP 96\r\n"
						fmt.Fprintf(c, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Length: %d\r\n\r\n%s", cseq, len(sdp), sdp)
					} else {
						fmt.Fprintf(c, "RTSP/1.0 404 Not Found\r\nCSeq: %s\r\n\r\n", cseq)
					}
				}
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestBulkFallsBackToRTSP(t *testing.T) {
	onv := fakeONVIF(t, "someone-else") // ONVIF rejects our login
	defer onv.Close()
	host, portS, _ := net.SplitHostPort(strings.TrimPrefix(onv.URL, "http://"))
	port, _ := strconv.Atoi(portS)
	rtspPort := fakeRTSPServer(t)

	s := testServer(t)
	body, _ := json.Marshal(map[string]any{"username": "admin", "password": "pw",
		"devices": []map[string]any{{"host": host, "port": port, "rtsp_port": rtspPort}}})
	rec := httptest.NewRecorder()
	s.handleBulk(rec, httptest.NewRequest("POST", "/api/cameras/bulk", bytes.NewReader(body)))
	var out struct {
		Results []bulkResult `json:"results"`
		Added   int          `json:"added"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Added != 1 || out.Results[0].Via != "rtsp" {
		t.Fatalf("want RTSP fallback, got %+v", out.Results)
	}
	c := s.store.Get().Cameras[0]
	if !strings.HasSuffix(c.Main.URL, "/Streaming/Channels/101") || !strings.HasSuffix(c.Sub.URL, "/Streaming/Channels/102") || c.Manufacturer != "Hikvision" {
		t.Fatalf("bad camera %+v", c)
	}
}
