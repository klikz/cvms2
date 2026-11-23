//go:build !windows

package cameras

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}

// HideWindow is exported for other client packages that spawn ffmpeg.
func HideWindow(cmd *exec.Cmd) { hideWindow(cmd) }
