// Package ffmpegx locates ffmpeg and picks a hardware (GPU) pipeline for
// decoding and H.264 encoding, falling back to the CPU.
package ffmpegx

import (
	"context"
	"fmt"
	"log"
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

// Pipeline describes how video is decoded and encoded to H.264.
type Pipeline struct {
	// Name is a human readable label, e.g. "NVIDIA CUDA → h264_nvenc".
	Name string `json:"name"`
	// HWAccel is the ffmpeg hwaccel: cuda | qsv | vaapi | d3d11va | "" (CPU).
	HWAccel string `json:"hwaccel"`
	// Encoder is the H.264 encoder, e.g. h264_nvenc or libx264.
	Encoder string `json:"encoder"`
	// Device selects a GPU (index for cuda/qsv/d3d11va, render node path for vaapi).
	Device string `json:"device"`
}

// GPU reports whether the pipeline uses hardware acceleration.
func (p Pipeline) GPU() bool { return p.HWAccel != "" || p.Encoder != "libx264" }

var cpuPipeline = Pipeline{Name: "CPU → libx264", Encoder: "libx264"}

func vaapiDevice(dev string) string {
	if dev == "" {
		return "/dev/dri/renderD128"
	}
	return dev
}

// hwInput returns the hwaccel input arguments. keepOnGPU keeps decoded
// frames in GPU memory (for a full GPU transcode); otherwise frames are
// downloaded to system memory automatically (for snapshots).
func (p Pipeline) hwInput(keepOnGPU bool) []string {
	switch p.HWAccel {
	case "cuda":
		a := []string{"-hwaccel", "cuda"}
		if p.Device != "" {
			a = append(a, "-hwaccel_device", p.Device)
		}
		if keepOnGPU {
			a = append(a, "-hwaccel_output_format", "cuda")
		}
		return a
	case "qsv":
		// The QSV (libmfx) HEVC decoder stalls when a live RTSP stream starts
		// mid-GOP, so Intel GPUs decode through the generic hwaccel
		// (D3D11VA on Windows, VAAPI on Linux) and encode with h264_qsv.
		if runtime.GOOS == "windows" {
			a := []string{"-hwaccel", "d3d11va"}
			if p.Device != "" {
				a = append(a, "-hwaccel_device", p.Device)
			}
			return a
		}
		return []string{"-hwaccel", "vaapi", "-vaapi_device", vaapiDevice(p.Device)}
	case "vaapi":
		a := []string{"-hwaccel", "vaapi", "-vaapi_device", vaapiDevice(p.Device)}
		if keepOnGPU {
			a = append(a, "-hwaccel_output_format", "vaapi")
		}
		return a
	case "d3d11va":
		a := []string{"-hwaccel", "d3d11va"}
		if p.Device != "" {
			a = append(a, "-hwaccel_device", p.Device)
		}
		return a
	}
	return nil
}

// TranscodeInputArgs are placed before -i for a transcode.
func (p Pipeline) TranscodeInputArgs() []string { return p.hwInput(true) }

// DecodeInputArgs are placed before -i when only decoding (snapshots):
// the GPU decodes, frames come back to system memory.
func (p Pipeline) DecodeInputArgs() []string { return p.hwInput(false) }

// EncodeArgs returns filter and low-latency H.264 encoder arguments. A short
// GOP lets new viewers start quickly.
func (p Pipeline) EncodeArgs() []string { return p.EncodeArgsScaled(0, 0) }

// EncodeArgsScaled is EncodeArgs with the output scaled down to w×h
// (0 keeps the source size). The CPU scaler keeps the aspect ratio
// (height derived from w); GPU scalers use w×h as given.
func (p Pipeline) EncodeArgsScaled(w, h int) []string {
	var a, vf []string
	size := ""
	if w > 0 && h > 0 {
		size = fmt.Sprintf("w=%d:h=%d:", w, h)
	}
	cpuScale := func() {
		if w > 0 {
			vf = append(vf, fmt.Sprintf("scale=%d:-2", w))
		}
	}
	switch p.HWAccel {
	case "cuda":
		vf = append(vf, "scale_cuda="+size+"format=nv12")
	case "vaapi":
		vf = append(vf, "scale_vaapi="+size+"format=nv12")
	default:
		cpuScale()
	}
	enc := p.Encoder
	var ea []string
	switch {
	case enc == "libx264":
		ea = []string{"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-profile:v", "baseline", "-pix_fmt", "yuv420p"}
	case strings.HasSuffix(enc, "_nvenc"):
		ea = []string{"-c:v", enc, "-preset", "p1", "-tune", "ull", "-zerolatency", "1", "-bf", "0"}
		if p.HWAccel != "cuda" {
			ea = append(ea, "-pix_fmt", "yuv420p")
		}
	case strings.HasSuffix(enc, "_qsv"):
		ea = []string{"-pix_fmt", "nv12", "-c:v", enc, "-preset", "veryfast", "-bf", "0", "-async_depth", "1"}
	case strings.HasSuffix(enc, "_vaapi"):
		if p.HWAccel != "vaapi" {
			a = append(a, "-vaapi_device", vaapiDevice(p.Device))
			vf = append(vf, "format=nv12", "hwupload")
		}
		ea = []string{"-c:v", enc, "-bf", "0"}
	case strings.HasSuffix(enc, "_amf"):
		ea = []string{"-c:v", enc, "-usage", "ultralowlatency", "-bf", "0", "-pix_fmt", "nv12"}
	default:
		ea = []string{"-c:v", enc, "-bf", "0"}
	}
	if len(vf) > 0 {
		a = append(a, "-vf", strings.Join(vf, ","))
	}
	a = append(a, ea...)
	return append(a, "-g", "50", "-keyint_min", "25", "-sc_threshold", "0")
}

// candidates lists GPU pipelines to try for an hwaccel setting.
func candidates(hwaccel, encoder, device string) []Pipeline {
	all := map[string]Pipeline{
		"cuda":    {Name: "NVIDIA CUDA → h264_nvenc", HWAccel: "cuda", Encoder: "h264_nvenc"},
		"qsv":     {Name: "Intel GPU → h264_qsv", HWAccel: "qsv", Encoder: "h264_qsv"},
		"vaapi":   {Name: "VAAPI → h264_vaapi", HWAccel: "vaapi", Encoder: "h264_vaapi"},
		"d3d11va": {Name: "D3D11VA → h264_amf", HWAccel: "d3d11va", Encoder: "h264_amf"},
	}
	var order []string
	switch hwaccel {
	case "", "auto":
		if runtime.GOOS == "windows" {
			order = []string{"cuda", "qsv", "d3d11va"}
		} else {
			order = []string{"cuda", "vaapi", "qsv"}
		}
	case "none", "cpu":
		return nil
	default:
		order = []string{hwaccel}
	}
	var out []Pipeline
	for _, k := range order {
		p, ok := all[k]
		if !ok {
			continue
		}
		p.Device = device
		if encoder != "" && encoder != "auto" {
			p.Encoder = encoder
			p.Name = strings.SplitN(p.Name, " → ", 2)[0] + " → " + encoder
		}
		out = append(out, p)
	}
	return out
}

type pipeEntry struct {
	p       Pipeline
	final   bool          // a GPU pipeline, or no GPU candidates: never re-checked
	retryAt time.Time     // when a CPU fallback is checked again
	backoff time.Duration // next retry delay
}

var (
	pipeMu    sync.Mutex
	pipeCache = map[string]*pipeEntry{}
	testClip  string
	clipOnce  sync.Once
)

const (
	retryFirst = time.Minute
	retryMax   = 30 * time.Minute
)

// PickPipeline returns the pipeline to use. hwaccel is auto | cuda | qsv |
// vaapi | d3d11va | none; encoder is auto or a specific H.264 encoder.
// With auto every candidate is verified with a real H.265 decode + H.264
// encode, so a GPU without drivers falls back cleanly. Results are cached;
// a CPU fallback is re-checked with backoff (1 min .. 30 min), because at
// boot an OS service can start before the GPU driver is ready.
func PickPipeline(ffmpeg, hwaccel, encoder, device string) Pipeline {
	key := ffmpeg + "|" + hwaccel + "|" + encoder + "|" + device
	pipeMu.Lock()
	defer pipeMu.Unlock()
	e, ok := pipeCache[key]
	if ok && (e.final || time.Now().Before(e.retryAt)) {
		return e.p
	}
	p := cpuPipeline
	if encoder != "" && encoder != "auto" && (hwaccel == "none" || hwaccel == "cpu") {
		p = Pipeline{Name: "CPU → " + encoder, Encoder: encoder}
	}
	cands := candidates(hwaccel, encoder, device)
	clip := clipPath(ffmpeg)
	found := false
	for _, c := range cands {
		if err := testPipeline(ffmpeg, clip, c); err != nil {
			if !ok { // log reasons once, not on every retry
				log.Printf("ffmpeg: GPU pipeline %q unavailable: %v", c.Name, err)
			}
			continue
		}
		p, found = c, true
		break
	}
	if !ok {
		e = &pipeEntry{backoff: retryFirst}
		pipeCache[key] = e
	} else if found {
		log.Printf("ffmpeg: GPU became available, switching to %q", p.Name)
	}
	e.p = p
	e.final = found || len(cands) == 0
	if !e.final {
		e.retryAt = time.Now().Add(e.backoff)
		e.backoff = min(e.backoff*2, retryMax)
	}
	return p
}

// clipPath creates a tiny H.265 test clip once (H.264 if libx265 is missing).
// The file name includes the pid, so a server and a client on the same host
// never read each other's half-written clip.
func clipPath(ffmpeg string) string {
	clipOnce.Do(func() {
		dir := os.TempDir()
		for _, c := range []struct{ codec, ext string }{{"libx265", "hevc"}, {"libx264", "h264"}} {
			f := filepath.Join(dir, fmt.Sprintf("cvms-gputest-%d.%s", os.Getpid(), c.ext))
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=s=640x360:r=10",
				"-frames:v", "10", "-pix_fmt", "yuv420p", "-c:v", c.codec, "-f", c.ext, f).Run()
			cancel()
			if err == nil {
				testClip = f
				return
			}
		}
	})
	return testClip
}

// TestArgs builds the ffmpeg arguments used to verify a pipeline.
func TestArgs(p Pipeline, clip string) []string {
	args := []string{"-hide_banner", "-loglevel", "error"}
	args = append(args, p.TranscodeInputArgs()...)
	if clip != "" {
		format := "hevc"
		if strings.HasSuffix(clip, ".h264") {
			format = "h264"
		}
		args = append(args, "-f", format, "-i", clip)
	} else {
		args = append(args, "-f", "lavfi", "-i", "color=c=black:s=640x360:r=10")
	}
	args = append(args, "-frames:v", "10")
	args = append(args, p.EncodeArgs()...)
	return append(args, "-f", "null", "-")
}

// testPipeline runs a short transcode; the error carries ffmpeg's message.
func testPipeline(ffmpeg, clip string, p Pipeline) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpeg, TestArgs(p, clip)...).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if len(msg) > 400 {
		msg = msg[len(msg)-400:]
	}
	if msg == "" {
		return err
	}
	return fmt.Errorf("%v: %s", err, strings.ReplaceAll(msg, "\n", " | "))
}

// SnapshotPipeline returns the pipeline used for GPU-decoded snapshots;
// it is the transcode pipeline but only its decoder part is used.
func SnapshotPipeline(ffmpeg, hwaccel, encoder, device string) Pipeline {
	return PickPipeline(ffmpeg, hwaccel, encoder, device)
}
