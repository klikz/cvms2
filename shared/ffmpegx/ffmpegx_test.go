package ffmpegx

import (
	"runtime"
	"strings"
	"testing"
)

func TestPipelineArgs(t *testing.T) {
	cases := []struct {
		p         Pipeline
		in, enc   string
		decodeHas string
	}{
		{Pipeline{HWAccel: "cuda", Encoder: "h264_nvenc"}, "-hwaccel cuda -hwaccel_output_format cuda", "scale_cuda=format=nv12 -c:v h264_nvenc", "-hwaccel cuda"},
		{Pipeline{HWAccel: "qsv", Encoder: "h264_qsv"}, qsvInput(), "-pix_fmt nv12 -c:v h264_qsv", qsvInput()},
		{Pipeline{HWAccel: "vaapi", Encoder: "h264_vaapi", Device: "/dev/dri/renderD129"}, "-hwaccel vaapi -vaapi_device /dev/dri/renderD129 -hwaccel_output_format vaapi", "scale_vaapi=format=nv12 -c:v h264_vaapi", "-vaapi_device /dev/dri/renderD129"},
		{Pipeline{HWAccel: "d3d11va", Encoder: "h264_amf"}, "-hwaccel d3d11va", "-c:v h264_amf", "-hwaccel d3d11va"},
		{cpuPipeline, "", "-c:v libx264", ""},
	}
	for _, c := range cases {
		in := strings.Join(c.p.TranscodeInputArgs(), " ")
		enc := strings.Join(c.p.EncodeArgs(), " ")
		dec := strings.Join(c.p.DecodeInputArgs(), " ")
		if in != c.in {
			t.Errorf("%v input: got %q want %q", c.p, in, c.in)
		}
		if !strings.Contains(enc, c.enc) || !strings.Contains(enc, "-g 50") {
			t.Errorf("%v encode: %q missing %q", c.p, enc, c.enc)
		}
		if c.decodeHas != "" && (!strings.Contains(dec, c.decodeHas) || strings.Contains(dec, "hwaccel_output_format")) {
			t.Errorf("%v decode: %q", c.p, dec)
		}
	}
}

func TestEncodeArgsScaled(t *testing.T) {
	cases := []struct {
		p    Pipeline
		want string
	}{
		{Pipeline{HWAccel: "cuda", Encoder: "h264_nvenc"}, "-vf scale_cuda=w=640:h=360:format=nv12 -c:v h264_nvenc"},
		{Pipeline{HWAccel: "vaapi", Encoder: "h264_vaapi"}, "-vf scale_vaapi=w=640:h=360:format=nv12 -c:v h264_vaapi"},
		{Pipeline{HWAccel: "d3d11va", Encoder: "h264_amf"}, "-vf scale=640:-2 -c:v h264_amf"},
		{Pipeline{HWAccel: "qsv", Encoder: "h264_qsv"}, "-vf scale=640:-2 -pix_fmt nv12 -c:v h264_qsv"},
		{Pipeline{Encoder: "h264_vaapi"}, "-vaapi_device /dev/dri/renderD128 -vf scale=640:-2,format=nv12,hwupload -c:v h264_vaapi"},
		{cpuPipeline, "-vf scale=640:-2 -c:v libx264"},
	}
	for _, c := range cases {
		got := strings.Join(c.p.EncodeArgsScaled(640, 360), " ")
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%v: got %q want prefix %q", c.p, got, c.want)
		}
	}
	if got := strings.Join((Pipeline{Encoder: "h264_vaapi"}).EncodeArgs(), " "); !strings.HasPrefix(got, "-vaapi_device /dev/dri/renderD128 -vf format=nv12,hwupload -c:v h264_vaapi") {
		t.Errorf("unscaled vaapi upload: %q", got)
	}
}

func qsvInput() string {
	if runtime.GOOS == "windows" {
		return "-hwaccel d3d11va"
	}
	return "-hwaccel vaapi -vaapi_device /dev/dri/renderD128"
}

func TestCandidates(t *testing.T) {
	if len(candidates("none", "auto", "")) != 0 {
		t.Fatal("none must have no gpu candidates")
	}
	c := candidates("cuda", "auto", "1")
	if len(c) != 1 || c[0].HWAccel != "cuda" || c[0].Device != "1" {
		t.Fatalf("%+v", c)
	}
	c = candidates("qsv", "h264_qsv", "")
	if c[0].Encoder != "h264_qsv" {
		t.Fatalf("%+v", c)
	}
}
