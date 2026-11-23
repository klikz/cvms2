// Package media contains helpers for H.264/H.265 Annex-B elementary streams.
package media

import (
	"bytes"
	"io"
)

var startCode = []byte{0, 0, 1}

// AU is one access unit: a list of NAL units without start codes.
type AU [][]byte

// SplitNALUs splits an Annex-B buffer into NAL units (start codes removed).
func SplitNALUs(b []byte) [][]byte {
	var out [][]byte
	i := bytes.Index(b, startCode)
	if i < 0 {
		if len(b) > 0 {
			return [][]byte{b}
		}
		return nil
	}
	b = b[i+3:]
	for {
		j := bytes.Index(b, startCode)
		if j < 0 {
			if n := trimTrailingZeros(b); len(n) > 0 {
				out = append(out, n)
			}
			return out
		}
		if n := trimTrailingZeros(b[:j]); len(n) > 0 {
			out = append(out, n)
		}
		b = b[j+3:]
	}
}

// JoinAnnexB serializes NAL units with 4-byte start codes.
func JoinAnnexB(au AU) []byte {
	n := 0
	for _, nal := range au {
		n += 4 + len(nal)
	}
	out := make([]byte, 0, n)
	for _, nal := range au {
		out = append(out, 0, 0, 0, 1)
		out = append(out, nal...)
	}
	return out
}

func trimTrailingZeros(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}

// NALType returns the NAL unit type for the codec.
func NALType(codec string, nal []byte) int {
	if len(nal) == 0 {
		return -1
	}
	if codec == "h265" {
		return int(nal[0]>>1) & 0x3f
	}
	return int(nal[0] & 0x1f)
}

func isVCL(codec string, nal []byte) bool {
	t := NALType(codec, nal)
	if codec == "h265" {
		return t >= 0 && t <= 31
	}
	return t >= 1 && t <= 5
}

// startsNewAU reports whether nal begins a new access unit, given that the
// current AU already contains a VCL NAL.
func startsNewAU(codec string, nal []byte) bool {
	t := NALType(codec, nal)
	if codec == "h265" {
		switch {
		case t >= 0 && t <= 31:
			// first_slice_segment_in_pic_flag
			return len(nal) > 2 && nal[2]&0x80 != 0
		case t == 32, t == 33, t == 34, t == 35, t == 39, (t >= 41 && t <= 44), (t >= 48 && t <= 55):
			return true
		}
		return false
	}
	switch {
	case t == 1 || t == 5:
		// first_mb_in_slice == 0 -> ue(v) starts with bit 1
		return len(nal) > 1 && nal[1]&0x80 != 0
	case t == 6, t == 7, t == 8, t == 9, (t >= 14 && t <= 18):
		return true
	}
	return false
}

// IsKeyframe reports whether the AU is a random access point.
func IsKeyframe(codec string, au AU) bool {
	for _, nal := range au {
		t := NALType(codec, nal)
		if codec == "h265" {
			if t >= 16 && t <= 21 {
				return true
			}
		} else if t == 5 {
			return true
		}
	}
	return false
}

// AUReader reads an Annex-B stream and yields complete access units.
type AUReader struct {
	r      io.Reader
	codec  string
	buf    []byte
	scan   int
	synced bool
	cur    AU
	curVCL bool
	tmp    []byte
	eof    bool
}

// NewAUReader creates a reader for codec "h264" or "h265".
func NewAUReader(r io.Reader, codec string) *AUReader {
	return &AUReader{r: r, codec: codec, tmp: make([]byte, 64*1024)}
}

func (a *AUReader) readNAL() ([]byte, error) {
	for {
		if !a.synced {
			if i := bytes.Index(a.buf, startCode); i >= 0 {
				a.buf = a.buf[i+3:]
				a.synced = true
				a.scan = 0
				continue
			}
			if len(a.buf) > 2 {
				a.buf = a.buf[len(a.buf)-2:]
			}
		} else {
			if a.scan > len(a.buf) {
				a.scan = 0
			}
			if i := bytes.Index(a.buf[a.scan:], startCode); i >= 0 {
				i += a.scan
				nal := trimTrailingZeros(a.buf[:i])
				a.buf = a.buf[i+3:]
				a.scan = 0
				if len(nal) > 0 {
					return nal, nil
				}
				continue
			}
			if len(a.buf) > 2 {
				a.scan = len(a.buf) - 2
			}
		}
		if a.eof {
			if a.synced && len(a.buf) > 0 {
				nal := trimTrailingZeros(a.buf)
				a.buf = nil
				a.scan = 0
				if len(nal) > 0 {
					return nal, nil
				}
			}
			return nil, io.EOF
		}
		n, err := a.r.Read(a.tmp)
		if n > 0 {
			a.buf = append(a.buf, a.tmp[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				a.eof = true
				continue
			}
			return nil, err
		}
	}
}

// Next returns the next complete access unit.
func (a *AUReader) Next() (AU, error) {
	for {
		nal, err := a.readNAL()
		if err != nil {
			if len(a.cur) > 0 && a.curVCL {
				au := a.cur
				a.cur, a.curVCL = nil, false
				return au, nil
			}
			return nil, err
		}
		if a.curVCL && startsNewAU(a.codec, nal) {
			au := a.cur
			a.cur = AU{nal}
			a.curVCL = isVCL(a.codec, nal)
			return au, nil
		}
		a.cur = append(a.cur, nal)
		if isVCL(a.codec, nal) {
			a.curVCL = true
		}
	}
}

// ParamCache remembers the latest parameter sets (SPS/PPS, and VPS for H.265)
// and inserts them before keyframes that lack them.
type ParamCache struct {
	Codec string
	VPS   []byte
	SPS   []byte
	PPS   []byte
}

// Update stores parameter sets found in au.
func (p *ParamCache) Update(au AU) {
	for _, nal := range au {
		t := NALType(p.Codec, nal)
		if p.Codec == "h265" {
			switch t {
			case 32:
				p.VPS = append([]byte(nil), nal...)
			case 33:
				p.SPS = append([]byte(nil), nal...)
			case 34:
				p.PPS = append([]byte(nil), nal...)
			}
		} else {
			switch t {
			case 7:
				p.SPS = append([]byte(nil), nal...)
			case 8:
				p.PPS = append([]byte(nil), nal...)
			}
		}
	}
}

// Ready reports whether all required parameter sets are known.
func (p *ParamCache) Ready() bool {
	if p.Codec == "h265" {
		return p.VPS != nil && p.SPS != nil && p.PPS != nil
	}
	return p.SPS != nil && p.PPS != nil
}

// Fix updates the cache and, for keyframes missing parameter sets,
// returns the AU with cached parameter sets prepended.
func (p *ParamCache) Fix(au AU) AU {
	p.Update(au)
	if !IsKeyframe(p.Codec, au) || !p.Ready() {
		return au
	}
	hasSPS := false
	for _, nal := range au {
		t := NALType(p.Codec, nal)
		if (p.Codec == "h265" && t == 33) || (p.Codec != "h265" && t == 7) {
			hasSPS = true
			break
		}
	}
	if hasSPS {
		return au
	}
	out := make(AU, 0, len(au)+3)
	if p.Codec == "h265" {
		out = append(out, p.VPS)
	}
	out = append(out, p.SPS, p.PPS)
	return append(out, au...)
}
