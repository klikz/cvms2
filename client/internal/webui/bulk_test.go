package webui

import "testing"

func TestParseRange(t *testing.T) {
	cases := map[string]int{
		"192.168.1.64":              1,
		"192.168.1.10-50":           41,
		"192.168.1.250-192.168.2.5": 12,
		"10.0.0.0/30":               2,
		"10.0.0.0/24":               254,
	}
	for in, n := range cases {
		ips, err := parseRange(in)
		if err != nil || len(ips) != n {
			t.Errorf("%s: got %d (%v), want %d", in, len(ips), err, n)
		}
	}
	for _, bad := range []string{"", "abc", "10.0.0.50-10", "10.0.0.0/16", "10.0.0.1-999"} {
		if _, err := parseRange(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	ips, _ := parseRange("192.168.1.10-12")
	if ips[0].String() != "192.168.1.10" || ips[2].String() != "192.168.1.12" {
		t.Fatalf("%v", ips)
	}
}
