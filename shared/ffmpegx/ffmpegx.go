// Package ffmpegx locates ffmpeg and picks an H.264 encoder.
package ffmpegx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Resolve returns the ffmpeg binary path. configured may be empty, a
// directory or a file path.
func Resolve(configured string) string {
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	if configured != "" {
		if st, err := os.Stat(configured); err == nil {
			if st.IsDir() {
				return filepath.Join(configured, name)
			}
			return configured
		}
		if p, err := exec.LookPath(configured); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return name
}

// Probe returns the ffprobe path that sits next to ffmpeg.
func Probe(ffmpeg string) string {
	dir := filepath.Dir(ffmpeg)
	name := "ffprobe"
	if runtime.GOOS == "windows" {
		name = "ffprobe.exe"
	}
	if dir != "." && dir != "" {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p
	}
	return name
}

// Available reports whether ffmpeg can be executed.
func Available(ffmpeg string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-version").Run() == nil
}

var (
	encMu    sync.Mutex
	encCache = map[string]string{}
)

var hwEncoders = []string{"h264_nvenc", "h264_qsv", "h264_vaapi", "h264_amf", "h264_videotoolbox"}

// PickEncoder returns the H.264 encoder to use. pref is "auto", "libx264"
// or a specific encoder name. With "auto" each hardware encoder is tried
// with a tiny test encode, falling back to libx264.
func PickEncoder(ffmpeg, pref string) string {
	if pref != "" && pref != "auto" {
		return pref
	}
	encMu.Lock()
	defer encMu.Unlock()
	if e, ok := encCache[ffmpeg]; ok {
		return e
	}
	enc := "libx264"
	for _, e := range hwEncoders {
		if testEncoder(ffmpeg, e) {
			enc = e
			break
		}
	}
	encCache[ffmpeg] = enc
	return enc
}

func testEncoder(ffmpeg, enc string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error"}
	if enc == "h264_vaapi" {
		args = append(args, "-vaapi_device", "/dev/dri/renderD128")
	}
	args = append(args, "-f", "lavfi", "-i", "color=c=black:s=256x144:r=10", "-frames:v", "3")
	if enc == "h264_vaapi" {
		args = append(args, "-vf", "format=nv12,hwupload")
	}
	args = append(args, "-c:v", enc, "-f", "null", "-")
	return exec.CommandContext(ctx, ffmpeg, args...).Run() == nil
}

// EncoderArgs returns low-latency H.264 output arguments for enc.
// The output keeps a short GOP so new viewers start fast.
func EncoderArgs(enc string) []string {
	var a []string
	switch {
	case enc == "libx264":
		a = []string{"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-profile:v", "baseline", "-pix_fmt", "yuv420p"}
	case strings.HasSuffix(enc, "_nvenc"):
		a = []string{"-c:v", enc, "-preset", "p1", "-tune", "ull", "-zerolatency", "1", "-bf", "0", "-pix_fmt", "yuv420p"}
	case strings.HasSuffix(enc, "_qsv"):
		a = []string{"-c:v", enc, "-preset", "veryfast", "-bf", "0", "-low_power", "1"}
	case strings.HasSuffix(enc, "_vaapi"):
		a = []string{"-vf", "format=nv12,hwupload", "-c:v", enc, "-bf", "0"}
	case strings.HasSuffix(enc, "_amf"):
		a = []string{"-c:v", enc, "-usage", "ultralowlatency", "-bf", "0"}
	default:
		a = []string{"-c:v", enc, "-bf", "0"}
	}
	return append(a, "-g", "50", "-keyint_min", "25", "-sc_threshold", "0")
}

// InputArgs returns encoder-specific global arguments placed before -i.
func InputArgs(enc string) []string {
	if strings.HasSuffix(enc, "_vaapi") {
		return []string{"-vaapi_device", "/dev/dri/renderD128"}
	}
	return nil
}
