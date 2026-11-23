package media

import (
	"bytes"
	"io"
	"testing"
)

// slowReader returns data in tiny chunks to exercise buffer boundaries.
type slowReader struct {
	b []byte
	n int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if len(s.b) == 0 {
		return 0, io.EOF
	}
	n := s.n
	if n > len(s.b) {
		n = len(s.b)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, s.b[:n])
	s.b = s.b[n:]
	return n, nil
}

func h264Stream(frames int) []byte {
	var buf bytes.Buffer
	sc := []byte{0, 0, 0, 1}
	for i := 0; i < frames; i++ {
		if i%5 == 0 {
			buf.Write(sc)
			buf.Write([]byte{0x67, 0x42, 0xc0, 0x1e, 0xaa}) // SPS
			buf.Write(sc)
			buf.Write([]byte{0x68, 0xce, 0x3c, 0x80}) // PPS
			buf.Write(sc)
			buf.Write([]byte{0x65, 0x88, 1, 2, 3, 0, 0, 3, 1}) // IDR, first_mb=0
		} else {
			buf.Write([]byte{0, 0, 1})
			buf.Write([]byte{0x41, 0x9a, 4, 5, 6}) // P slice, first_mb=0
		}
	}
	return buf.Bytes()
}

func TestAUReaderH264(t *testing.T) {
	for _, chunk := range []int{1, 2, 3, 7, 64 * 1024} {
		r := NewAUReader(&slowReader{b: h264Stream(12), n: chunk}, "h264")
		var aus []AU
		for {
			au, err := r.Next()
			if err != nil {
				break
			}
			aus = append(aus, au)
		}
		if len(aus) != 12 {
			t.Fatalf("chunk %d: got %d AUs, want 12", chunk, len(aus))
		}
		if !IsKeyframe("h264", aus[0]) || len(aus[0]) != 3 || IsKeyframe("h264", aus[1]) {
			t.Fatalf("chunk %d: bad AU split: %v", chunk, aus[0])
		}
		// EOF must be sticky and must not panic
		for i := 0; i < 3; i++ {
			if _, err := r.Next(); err != io.EOF {
				t.Fatalf("want EOF, got %v", err)
			}
		}
	}
}

func TestParamCacheFix(t *testing.T) {
	p := ParamCache{Codec: "h264"}
	p.Update(AU{{0x67, 1}, {0x68, 2}})
	out := p.Fix(AU{{0x65, 0x88}})
	if len(out) != 3 || NALType("h264", out[0]) != 7 || NALType("h264", out[1]) != 8 {
		t.Fatalf("params not prepended: %v", out)
	}
	h := ParamCache{Codec: "h265"}
	h.Update(AU{{0x40, 1}, {0x42, 1}, {0x44, 1}})
	out = h.Fix(AU{{0x26, 0x01, 0x80}}) // IDR_W_RADL
	if len(out) != 4 {
		t.Fatalf("h265 params not prepended: %v", out)
	}
}

func TestSplitNALUs(t *testing.T) {
	got := SplitNALUs([]byte{0, 0, 0, 1, 0x67, 1, 0, 0, 1, 0x68, 2, 0})
	if len(got) != 2 || !bytes.Equal(got[0], []byte{0x67, 1}) || !bytes.Equal(got[1], []byte{0x68, 2}) {
		t.Fatalf("%v", got)
	}
}
