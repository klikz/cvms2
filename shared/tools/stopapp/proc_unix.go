//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func listProcs() []proc {
	ents, _ := os.ReadDir("/proc")
	var out []proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			// not our process: fall back to argv[0]
			b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
			if err != nil || len(b) == 0 {
				continue
			}
			exe = string(bytes.SplitN(b, []byte{0}, 2)[0])
		}
		out = append(out, proc{pid: pid, exe: exe})
	}
	return out
}

// kill asks the process to shut down gracefully (SIGTERM: it stops its
// ffmpeg children itself), then forces it after 10 seconds.
func kill(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 100; i++ {
		time.Sleep(100 * time.Millisecond)
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}
