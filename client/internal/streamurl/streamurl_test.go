package streamurl

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
		changed  bool
	}{
		{"rtsp://10.10.10.40:80:554/cam/realmonitor?channel=1&subtype=1", "rtsp://10.10.10.40:554/cam/realmonitor?channel=1&subtype=1", true},
		{"rtsp://admin:pw@10.0.0.5:80:554/s?a=1", "rtsp://admin:pw@10.0.0.5:554/s?a=1", true},
		{"rtsp://cam.local:8080:554", "rtsp://cam.local:554", true},
		{"  rtsp://1.2.3.4:554/s ", "rtsp://1.2.3.4:554/s", true},
		{"rtsp://1.2.3.4:554/s", "rtsp://1.2.3.4:554/s", false},
		{"rtsp://1.2.3.4/s", "rtsp://1.2.3.4/s", false},
		{"rtsp://[fe80::1]:554/s", "rtsp://[fe80::1]:554/s", false},
		{"rtsp://fe80::1/s", "rtsp://fe80::1/s", false},
		{"http://10.0.0.5:80/onvifsnapshot", "http://10.0.0.5:80/onvifsnapshot", false},
		{"rtsp://u:p:x@1.2.3.4:554/s", "rtsp://u:p:x@1.2.3.4:554/s", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, changed := Normalize(c.in)
		if got != c.want || changed != c.changed {
			t.Errorf("Normalize(%q) = %q,%v; want %q,%v", c.in, got, changed, c.want, c.changed)
		}
	}
}
