package nvr

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// dahua speaks the Dahua HTTP CGI API (key=value text over HTTP).
// Dahua channels are 0-based in the API and 1-based in stream URLs;
// Channel.ID is the 1-based number.
type dahua struct{ conn }

func (d *dahua) Vendor() string { return Dahua }

// kv fetches a CGI path and parses its "key=value" lines.
func (d *dahua) kv(ctx context.Context, path string) (map[string]string, error) {
	b, err := d.get(ctx, path)
	if err != nil {
		return nil, err
	}
	m := parseKV(b)
	if len(m) == 0 {
		return nil, fmt.Errorf("%w: %s: no key=value data", ErrUnsupported, path)
	}
	return m, nil
}

func parseKV(b []byte) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), maxResponseBytes)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		if k = strings.TrimSpace(k); k != "" {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m
}

// indexed groups keys like "camera[3].DeviceInfo.Address" by the index
// after prefix: {3: {"DeviceInfo.Address": ...}}.
func indexed(m map[string]string, re *regexp.Regexp) map[int]map[string]string {
	out := map[int]map[string]string{}
	for k, v := range m {
		s := re.FindStringSubmatch(k)
		if s == nil {
			continue
		}
		i, err := strconv.Atoi(s[1])
		if err != nil {
			continue
		}
		if out[i] == nil {
			out[i] = map[string]string{}
		}
		out[i][s[2]] = v
	}
	return out
}

var (
	reCamera = regexp.MustCompile(`^camera\[(\d+)\]\.(.+)$`)
	reState  = regexp.MustCompile(`^states\[(\d+)\]\.(.+)$`)
	reTitle  = regexp.MustCompile(`^table\.ChannelTitle\[(\d+)\]\.(.+)$`)
)

func (d *dahua) DeviceInfo(ctx context.Context) (DeviceInfo, error) {
	var info DeviceInfo
	sys, err := d.kv(ctx, "/cgi-bin/magicBox.cgi?action=getSystemInfo")
	if err != nil {
		return info, err
	}
	info.Model = firstNonEmpty(sys["deviceType"], sys["updateSerial"])
	info.Serial = sys["serialNumber"]
	if m, err := d.kv(ctx, "/cgi-bin/magicBox.cgi?action=getMachineName"); err == nil {
		info.Name = m["name"]
	}
	if m, err := d.kv(ctx, "/cgi-bin/magicBox.cgi?action=getSoftwareVersion"); err == nil {
		info.Firmware = m["version"]
	}
	return info, nil
}

// Channels lists the logical channels (getCameraAll) with their camera
// address and connection state. Old firmware without LogicDeviceManager
// falls back to the channel count and titles.
func (d *dahua) Channels(ctx context.Context) ([]Channel, error) {
	titles := map[int]string{}
	tm, titleErr := d.kv(ctx, "/cgi-bin/configManager.cgi?action=getConfig&name=ChannelTitle")
	if errors.Is(titleErr, ErrAuth) || errors.Is(titleErr, ErrUnreachable) || ctx.Err() != nil {
		return nil, titleErr
	}
	for i, f := range indexed(tm, reTitle) {
		titles[i] = f["Name"]
	}

	cams, camErr := d.kv(ctx, "/cgi-bin/LogicDeviceManager.cgi?action=getCameraAll")
	if camErr == nil {
		// some firmware has no getCameraState (501): the state is then
		// unknown and an enabled channel counts as online
		var online map[int]bool
		if st, err := d.kv(ctx, "/cgi-bin/LogicDeviceManager.cgi?action=getCameraState&uniqueChannels[0]=-1"); err == nil {
			online = map[int]bool{}
			for _, f := range indexed(st, reState) {
				if ch, err := strconv.Atoi(f["channel"]); err == nil {
					online[ch] = strings.EqualFold(f["connectionState"], "Connected")
				}
			}
		}
		var out []Channel
		seen := map[int]bool{}
		for _, f := range indexed(cams, reCamera) {
			// UniqueChannel is the recorder's channel; on many firmwares
			// Channel is the input on the remote camera (0 for all of them)
			ch, err := strconv.Atoi(firstNonEmpty(f["UniqueChannel"], f["Channel"]))
			if err != nil || ch < 0 || seen[ch] {
				continue
			}
			local := strings.EqualFold(f["Type"], "Local")
			// Compose and other virtual channels have no camera of their own
			if !local && !strings.EqualFold(f["Type"], "Remote") {
				continue
			}
			addr := f["DeviceInfo.Address"]
			enabled := strings.EqualFold(f["Enable"], "true")
			// unconfigured remote channels are off (their address is empty
			// or a placeholder like 192.168.0.0)
			if !local && !enabled {
				continue
			}
			seen[ch] = true
			isOnline := local || enabled
			if online != nil && !local {
				isOnline = online[ch]
			}
			c := Channel{
				ID:     ch + 1,
				Name:   firstNonEmpty(titles[ch], f["DeviceInfo.VideoInputs[0].Name"], f["DeviceInfo.Name"]),
				Analog: local,
				Online: isOnline,
			}
			if !local {
				c.IP = addr
				c.Port, _ = strconv.Atoi(f["DeviceInfo.HttpPort"])
			}
			out = append(out, c)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	if errors.Is(camErr, ErrAuth) || errors.Is(camErr, ErrUnreachable) || ctx.Err() != nil {
		return nil, camErr
	}

	// fallback: number of channels and their titles only
	n := 0
	if m, err := d.kv(ctx, "/cgi-bin/devVideoInput.cgi?action=getCollect"); err == nil {
		n, _ = strconv.Atoi(m["result"])
	}
	if n == 0 {
		n = len(titles)
	}
	if n == 0 {
		if titleErr != nil {
			return nil, titleErr
		}
		return nil, camErr
	}
	out := make([]Channel, 0, n)
	for ch := 0; ch < n; ch++ {
		out = append(out, Channel{ID: ch + 1, Name: titles[ch], Online: true})
	}
	return out, nil
}

func (d *dahua) RTSPPort(ctx context.Context) int {
	if m, err := d.kv(ctx, "/cgi-bin/configManager.cgi?action=getConfig&name=RTSP"); err == nil {
		if p, err := strconv.Atoi(m["table.RTSP.Port"]); err == nil && p > 0 {
			return p
		}
	}
	return DefaultRTSPPort
}

func (d *dahua) stream(rtspPort, ch, subtype int) string {
	return fmt.Sprintf("rtsp://%s/cam/realmonitor?channel=%d&subtype=%d", hostPort(d.host, rtspPort, DefaultRTSPPort), ch, subtype)
}

func (d *dahua) MainURL(rtspPort, ch int) string { return d.stream(rtspPort, ch, 0) }
func (d *dahua) SubURL(rtspPort, ch int) string  { return d.stream(rtspPort, ch, 1) }

func (d *dahua) SnapshotURL(ch int) string {
	return fmt.Sprintf("http://%s/cgi-bin/snapshot.cgi?channel=%d", hostPort(d.host, d.port, defaultHTTPPort), ch)
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x = strings.TrimSpace(x); x != "" {
			return x
		}
	}
	return ""
}
