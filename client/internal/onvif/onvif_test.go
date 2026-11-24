package onvif

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cvms/client/internal/digest"
)

type devOpts struct {
	httpDigest bool // require HTTP Digest (Dahua style)
	noMillis   bool // reject WS-Security Created with milliseconds
	noAuth     bool // ONVIF auth disabled: reject requests WITH a Security header
}

func fakeDevice(t *testing.T, o devOpts) (string, int) {
	const user, pass, realm, nonce = "admin", "Secr3t!", "IPC", "abc"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		env := func(inner string) {
			w.Header().Set("Content-Type", "application/soap+xml")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>` + inner + `</s:Body></s:Envelope>`))
		}
		fault := func() {
			w.WriteHeader(400)
			env(`<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Code><s:Reason><s:Text>Sender not Authorized</s:Text></s:Reason></s:Fault>`)
		}
		if strings.Contains(body, "GetSystemDateAndTime") {
			env(`<GetSystemDateAndTimeResponse><SystemDateAndTime><UTCDateTime><Date><Year>2026</Year><Month>1</Month><Day>2</Day></Date><Time><Hour>3</Hour><Minute>4</Minute><Second>5</Second></Time></UTCDateTime></SystemDateAndTime></GetSystemDateAndTimeResponse>`)
			return
		}
		switch {
		case o.httpDigest:
			a := r.Header.Get("Authorization")
			p := digest.ParseChallenge(a)
			ha1 := digest.MD5Hex(user + ":" + realm + ":" + pass)
			ha2 := digest.MD5Hex("POST:" + p["uri"])
			if p["response"] != digest.MD5Hex(ha1+":"+nonce+":"+p["nc"]+":"+p["cnonce"]+":auth:"+ha2) {
				w.Header().Set("WWW-Authenticate", `Digest realm="`+realm+`", qop="auth", nonce="`+nonce+`"`)
				w.WriteHeader(401)
				return
			}
		case o.noAuth:
			if strings.Contains(body, "<Security") {
				fault()
				return
			}
		default:
			if !strings.Contains(body, "<Username>"+user+"</Username>") {
				fault()
				return
			}
			if o.noMillis && strings.Contains(body, ".000Z</Created>") {
				fault()
				return
			}
		}
		switch {
		case strings.Contains(body, "GetDeviceInformation"):
			env(`<GetDeviceInformationResponse><Manufacturer>Dahua</Manufacturer><Model>IPC-1</Model></GetDeviceInformationResponse>`)
		case strings.Contains(body, "GetCapabilities"):
			env(`<GetCapabilitiesResponse><Capabilities><Media><XAddr>http://` + r.Host + `/onvif/media</XAddr></Media></Capabilities></GetCapabilitiesResponse>`)
		case strings.Contains(body, "GetProfiles"):
			env(`<GetProfilesResponse><Profiles token="p0"><Name>main</Name><VideoEncoderConfiguration><Encoding>H265</Encoding><Resolution><Width>2560</Width><Height>1440</Height></Resolution></VideoEncoderConfiguration></Profiles></GetProfilesResponse>`)
		case strings.Contains(body, "GetStreamUri"):
			env(`<GetStreamUriResponse><MediaUri><Uri>rtsp://10.0.0.9:554/cam/realmonitor?channel=1&amp;subtype=0</Uri></MediaUri></GetStreamUriResponse>`)
		default:
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	return host, p
}

func TestProbeAuthVariants(t *testing.T) {
	for name, o := range map[string]devOpts{"http-digest": {httpDigest: true}, "no-millis": {noMillis: true}, "auth-disabled": {noAuth: true}, "plain": {}} {
		t.Run(name, func(t *testing.T) {
			host, port := fakeDevice(t, o)
			c := NewClient(host, port, "admin", "Secr3t!")
			info, err := c.Probe(context.Background())
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			if len(info.Profiles) != 1 || !strings.Contains(info.Profiles[0].URI, host+":554/cam/realmonitor") {
				t.Fatalf("profiles: %+v", info.Profiles)
			}
			t.Logf("auth used: %s", c.LastAuth)
		})
	}
}

func TestProbeWrongPassword(t *testing.T) {
	for name, o := range map[string]devOpts{"http-digest": {httpDigest: true}, "wsse": {}} {
		t.Run(name, func(t *testing.T) {
			host, port := fakeDevice(t, o)
			_, err := NewClient(host, port, "admin", "wrong").Probe(context.Background())
			if !errors.Is(err, ErrAuth) && name == "http-digest" {
				t.Fatalf("want ErrAuth, got %v", err)
			}
			if name == "wsse" && err != nil && !errors.Is(err, ErrAuth) {
				t.Fatalf("want ErrAuth, got %v", err)
			}
		})
	}
}

func TestNewClientOmitsDefaultPort(t *testing.T) {
	if u := NewClient("10.0.0.5", 80, "", "").DeviceURL; u != "http://10.0.0.5/onvif/device_service" {
		t.Fatal(u)
	}
	if u := NewClient("fe80::1", 80, "", "").DeviceURL; u != "http://[fe80::1]/onvif/device_service" {
		t.Fatal(u)
	}
	if u := NewClient("10.0.0.5", 8080, "", "").DeviceURL; u != "http://10.0.0.5:8080/onvif/device_service" {
		t.Fatal(u)
	}
}

func TestFixHostRepairsDoublePort(t *testing.T) {
	c := NewClient("10.10.10.40", 80, "", "")
	if got := c.fixHost("rtsp://10.10.10.40:80:554/cam/realmonitor?channel=1&subtype=1"); got != "rtsp://10.10.10.40:554/cam/realmonitor?channel=1&subtype=1" {
		t.Fatal(got)
	}
	if got := c.fixHost("http://192.168.1.10:80/onvif/media_service"); got != "http://10.10.10.40/onvif/media_service" {
		t.Fatal(got)
	}
}
