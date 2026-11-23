//go:build !windows

package hub

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
