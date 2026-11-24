//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func listProcs() []proc {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var out []proc
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		out = append(out, proc{pid: int(e.ProcessID), exe: imagePath(e.ProcessID, windows.UTF16ToString(e.ExeFile[:]))})
	}
	return out
}

// imagePath returns the full executable path, or name if it cannot be read.
func imagePath(pid uint32, name string) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return name
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return name
	}
	return windows.UTF16ToString(buf[:n])
}

// kill ends the process and its children (ffmpeg).
func kill(pid int) error {
	out, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("taskkill: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
