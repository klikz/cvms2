//go:build windows

package cameras

import (
	"os/exec"
	"syscall"
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// HideWindow is exported for other client packages that spawn ffmpeg.
func HideWindow(cmd *exec.Cmd) { hideWindow(cmd) }
