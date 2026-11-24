package nvr

import (
	"context"
	"errors"
	"testing"
)

var dahuaPages = map[string]string{
	"/cgi-bin/magicBox.cgi?action=getSystemInfo":      "deviceType=DHI-NVR4208-8P-4KS2\r\nprocessor=ST7108\r\nserialNumber=4L0123PAZ00001\r\n",
	"/cgi-bin/magicBox.cgi?action=getMachineName":     "name=Warehouse NVR\r\n",
	"/cgi-bin/magicBox.cgi?action=getSoftwareVersion": "version=4.001.0000000.4,build:2023-01-01\r\n",
	"/cgi-bin/configManager.cgi?action=getConfig&name=ChannelTitle": "table.ChannelTitle[0].Name=Entrance\r\n" +
		"table.ChannelTitle[1].Name=\r\ntable.ChannelTitle[2].Name=Channel3\r\ntable.ChannelTitle[3].Name=Channel4\r\n",
	"/cgi-bin/LogicDeviceManager.cgi?action=getCameraAll": "camera[0].Channel=0\r\ncamera[0].Enable=true\r\ncamera[0].Type=Remote\r\n" +
		"camera[0].DeviceInfo.Address=10.1.1.11\r\ncamera[0].DeviceInfo.HttpPort=80\r\ncamera[0].DeviceInfo.Name=IPC-A\r\n" +
		"camera[1].Channel=1\r\ncamera[1].Enable=true\r\ncamera[1].Type=Remote\r\n" +
		"camera[1].DeviceInfo.Address=10.1.1.12\r\ncamera[1].DeviceInfo.Name=IPC-B\r\n" +
		"camera[2].Channel=2\r\ncamera[2].Enable=false\r\ncamera[2].Type=Remote\r\ncamera[2].DeviceInfo.Address=\r\n" +
		"camera[3].Channel=3\r\ncamera[3].Enable=true\r\ncamera[3].Type=Local\r\n",
	"/cgi-bin/LogicDeviceManager.cgi?action=getCameraState&uniqueChannels[0]=-1": "states[0].channel=0\r\nstates[0].connectionState=Connected\r\n" +
		"states[1].channel=1\r\nstates[1].connectionState=Disconnected\r\n",
	"/cgi-bin/configManager.cgi?action=getConfig&name=RTSP": "table.RTSP.Enable=true\r\ntable.RTSP.Port=10554\r\n",
}

func TestDahuaNVR(t *testing.T) {
	host, port := fakeServer(t, "admin", "pw", dahuaPages)
	ctx := context.Background()
	r, chs, err := Open(ctx, Auto, host, port, "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if r.Vendor() != Dahua {
		t.Fatalf("vendor = %s", r.Vendor())
	}
	want := []Channel{
		{ID: 1, Name: "Entrance", IP: "10.1.1.11", Port: 80, Online: true},
		{ID: 2, Name: "IPC-B", IP: "10.1.1.12", Online: false},
		{ID: 4, Name: "Channel4", Online: true, Analog: true},
	}
	if len(chs) != len(want) {
		t.Fatalf("channels = %+v", chs)
	}
	for i := range want {
		if chs[i] != want[i] {
			t.Errorf("channel %d = %+v, want %+v", i, chs[i], want[i])
		}
	}
	d, err := r.DeviceInfo(ctx)
	if err != nil || d.Name != "Warehouse NVR" || d.Model != "DHI-NVR4208-8P-4KS2" || d.Serial != "4L0123PAZ00001" {
		t.Errorf("DeviceInfo = %+v, %v", d, err)
	}
	if p := r.RTSPPort(ctx); p != 10554 {
		t.Errorf("RTSPPort = %d", p)
	}
}

func TestDahuaFallback(t *testing.T) {
	host, port := fakeServer(t, "admin", "pw", map[string]string{
		"/cgi-bin/configManager.cgi?action=getConfig&name=ChannelTitle": "table.ChannelTitle[0].Name=Cam 1\r\ntable.ChannelTitle[1].Name=Cam 2\r\n",
		"/cgi-bin/devVideoInput.cgi?action=getCollect":                  "result=2\r\n",
	})
	_, chs, err := Open(context.Background(), Dahua, host, port, "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 2 || chs[0].ID != 1 || chs[0].Name != "Cam 1" || chs[1].ID != 2 {
		t.Fatalf("channels = %+v", chs)
	}
}

func TestOpenAuto(t *testing.T) {
	host, port := fakeServer(t, "admin", "secret", nvrPages)
	r, chs, err := Open(context.Background(), Auto, host, port, "admin", "secret")
	if err != nil || r.Vendor() != Hikvision || len(chs) != 2 {
		t.Fatalf("hikvision auto: %v %v", err, chs)
	}
	if _, _, err := Open(context.Background(), Auto, host, port, "admin", "wrong"); !errors.Is(err, ErrAuth) {
		t.Errorf("wrong password: %v", err)
	}
	empty, eport := fakeServer(t, "a", "b", map[string]string{})
	if _, _, err := Open(context.Background(), Auto, empty, eport, "a", "b"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("unknown device: %v", err)
	}
}

func TestDahuaURLs(t *testing.T) {
	r, _ := New(Dahua, "10.0.0.7", 0, "", "")
	if u := r.MainURL(554, 3); u != "rtsp://10.0.0.7:554/cam/realmonitor?channel=3&subtype=0" {
		t.Error(u)
	}
	if u := r.SubURL(0, 3); u != "rtsp://10.0.0.7:554/cam/realmonitor?channel=3&subtype=1" {
		t.Error(u)
	}
	if u := r.SnapshotURL(3); u != "http://10.0.0.7:80/cgi-bin/snapshot.cgi?channel=3" {
		t.Error(u)
	}
}
