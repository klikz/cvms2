package hikvision

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cvms/client/internal/digest"
)

const (
	realm = "IP Camera(C1234)"
	nonce = "abc123"
)

// fakeNVR answers ISAPI paths with Digest (MD5, no qop) auth.
func fakeNVR(t *testing.T, user, pass string, pages map[string]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		want := digest.MD5Hex(digest.MD5Hex(user+":"+realm+":"+pass) + ":" + nonce + ":" + digest.MD5Hex("GET:"+r.URL.RequestURI()))
		if !strings.Contains(h, `response="`+want+`"`) {
			w.Header().Set("WWW-Authenticate", `Digest realm="`+realm+`", nonce="`+nonce+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	return NewClient(host, p, user, pass)
}

const ns = `xmlns="http://www.hikvision.com/ver20/XMLSchema" version="2.0"`

var nvrPages = map[string]string{
	"/ISAPI/System/deviceInfo": `<?xml version="1.0" encoding="UTF-8"?>
<DeviceInfo ` + ns + `><deviceName>Office NVR</deviceName><model>DS-7608NI-K2</model><serialNumber>DS-7608NI0001</serialNumber><firmwareVersion>V4.30</firmwareVersion></DeviceInfo>`,
	"/ISAPI/ContentMgmt/InputProxy/channels": `<?xml version="1.0" encoding="UTF-8"?>
<InputProxyChannelList ` + ns + `>
 <InputProxyChannel><id>2</id><name>Gate</name><sourceInputPortDescriptor><proxyProtocol>HIKVISION</proxyProtocol><ipAddress>192.168.254.3</ipAddress><managePortNo>8000</managePortNo></sourceInputPortDescriptor></InputProxyChannel>
 <InputProxyChannel><id>1</id><name>Lobby</name><sourceInputPortDescriptor><ipAddress>192.168.254.2</ipAddress><managePortNo>8000</managePortNo></sourceInputPortDescriptor></InputProxyChannel>
</InputProxyChannelList>`,
	"/ISAPI/ContentMgmt/InputProxy/channels/status": `<?xml version="1.0" encoding="UTF-8"?>
<InputProxyChannelStatusList ` + ns + `>
 <InputProxyChannelStatus><id>1</id><online>true</online></InputProxyChannelStatus>
 <InputProxyChannelStatus><id>2</id><online>false</online></InputProxyChannelStatus>
</InputProxyChannelStatusList>`,
	"/ISAPI/Security/adminAccess": `<?xml version="1.0" encoding="UTF-8"?>
<AdminAccessProtocolList ` + ns + `>
 <AdminAccessProtocol><id>1</id><enabled>true</enabled><protocol>HTTP</protocol><portNo>80</portNo></AdminAccessProtocol>
 <AdminAccessProtocol><id>4</id><enabled>true</enabled><protocol>RTSP</protocol><portNo>8554</portNo></AdminAccessProtocol>
</AdminAccessProtocolList>`,
}

func TestNVR(t *testing.T) {
	c := fakeNVR(t, "admin", "secret", nvrPages)
	ctx := context.Background()

	d, err := c.DeviceInfo(ctx)
	if err != nil || d.Name != "Office NVR" || d.Model != "DS-7608NI-K2" {
		t.Fatalf("DeviceInfo = %+v, %v", d, err)
	}
	chs, err := c.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 2 {
		t.Fatalf("got %d channels: %+v", len(chs), chs)
	}
	if chs[0].ID != 1 || chs[0].Name != "Lobby" || chs[0].IP != "192.168.254.2" || !chs[0].Online || chs[0].Analog {
		t.Errorf("channel 1 = %+v", chs[0])
	}
	if chs[1].ID != 2 || chs[1].Online {
		t.Errorf("channel 2 = %+v", chs[1])
	}
	if p := c.RTSPPort(ctx); p != 8554 {
		t.Errorf("RTSPPort = %d", p)
	}
}

func TestDVRAnalog(t *testing.T) {
	pages := map[string]string{
		"/ISAPI/System/Video/inputs/channels": `<VideoInputChannelList ` + ns + `>
 <VideoInputChannel><id>1</id><inputPort>1</inputPort><videoInputEnabled>true</videoInputEnabled><name>Camera 01</name></VideoInputChannel>
 <VideoInputChannel><id>2</id><inputPort>2</inputPort><videoInputEnabled>false</videoInputEnabled><name>Camera 02</name></VideoInputChannel>
</VideoInputChannelList>`,
	}
	chs, err := fakeNVR(t, "admin", "pw", pages).Channels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 1 || chs[0].ID != 1 || !chs[0].Analog || chs[0].IP != "" {
		t.Fatalf("channels = %+v", chs)
	}
}

func TestErrors(t *testing.T) {
	c := fakeNVR(t, "admin", "secret", nvrPages)
	c.Pass = "wrong"
	if _, err := c.Channels(context.Background()); !errors.Is(err, ErrAuth) {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := fakeNVR(t, "a", "b", map[string]string{}).Channels(context.Background()); !errors.Is(err, ErrNotHikvision) {
		t.Errorf("no ISAPI: %v", err)
	}
	if _, err := NewClient("127.0.0.1", 1, "a", "b").DeviceInfo(context.Background()); !errors.Is(err, ErrUnreachable) {
		t.Errorf("closed port: %v", err)
	}
	if p := fakeNVR(t, "a", "b", map[string]string{}).RTSPPort(context.Background()); p != DefaultRTSPPort {
		t.Errorf("RTSPPort fallback = %d", p)
	}
}

func TestURLs(t *testing.T) {
	if u := MainURL("10.0.0.5", 554, 3); u != "rtsp://10.0.0.5:554/Streaming/Channels/301" {
		t.Error(u)
	}
	if u := SubURL("10.0.0.5", 0, 12); u != "rtsp://10.0.0.5:554/Streaming/Channels/1202" {
		t.Error(u)
	}
	if u := SnapshotURL("10.0.0.5", 8080, 2); u != "http://10.0.0.5:8080/ISAPI/Streaming/channels/201/picture" {
		t.Error(u)
	}
}
