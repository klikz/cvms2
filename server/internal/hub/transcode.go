package hub

import (
	"bufio"
	"context"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"

	"cvms/shared/ffmpegx"
	"cvms/shared/media"
)

// transcoder converts an H.265 Annex-B stream into H.264 with ffmpeg.
type transcoder struct {
	cancel context.CancelFunc
	in     chan []byte
	once   sync.Once
}

// newTranscoder starts ffmpeg. out is called for every H.264 access unit.
func newTranscoder(ffmpeg, encoder string, out func(media.AU)) (*transcoder, error) {
	ctx, cancel := context.WithCancel(context.Background())
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, ffmpegx.InputArgs(encoder)...)
	args = append(args,
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-probesize", "1000000", "-analyzeduration", "0",
		"-use_wallclock_as_timestamps", "1",
		"-f", "hevc", "-i", "pipe:0",
		"-an", "-fps_mode", "passthrough")
	args = append(args, ffmpegx.EncoderArgs(encoder)...)
	args = append(args, "-f", "h264", "pipe:1")
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	t := &transcoder{cancel: cancel, in: make(chan []byte, 120)}
	// writer
	go func() {
		w := bufio.NewWriterSize(stdin, 256*1024)
		for {
			select {
			case <-ctx.Done():
				stdin.Close()
				return
			case b := <-t.in:
				if _, err := w.Write(b); err != nil {
					cancel()
					return
				}
				if len(t.in) == 0 {
					if err := w.Flush(); err != nil {
						cancel()
						return
					}
				}
			}
		}
	}()
	// reader
	go func() {
		r := media.NewAUReader(stdout, "h264")
		for {
			au, err := r.Next()
			if err != nil {
				break
			}
			out(au)
		}
		_ = cmd.Wait()
		if s := strings.TrimSpace(stderr.String()); s != "" && ctx.Err() == nil {
			log.Printf("transcoder: %s", s)
		}
		cancel()
	}()
	return t, nil
}

// Write queues an H.265 access unit; it drops data if ffmpeg falls behind.
func (t *transcoder) Write(au media.AU) {
	select {
	case t.in <- media.JoinAnnexB(au):
	default:
	}
}

// Close stops ffmpeg.
func (t *transcoder) Close() { t.once.Do(t.cancel) }

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	_, _ = l.w.Write(q)
	return len(p), nil
}
