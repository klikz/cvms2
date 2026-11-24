//go:build linux

package svc

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const autostartSupported = true

func mapErr(err error) error { return err }

// canManage: systemd system units need root.
func canManage() bool { return os.Geteuid() == 0 }

func unitPath(name string) string { return "/etc/systemd/system/" + name + ".service" }

func queryAutostart(name string) (installed, enabled bool, err error) {
	if _, err := os.Stat(unitPath(name)); err != nil {
		return false, false, nil
	}
	out, _ := exec.Command("systemctl", "is-enabled", name+".service").Output()
	return true, strings.TrimSpace(string(out)) == "enabled", nil
}

func setAutostart(name string, on bool) error {
	action := "disable"
	if on {
		action = "enable"
	}
	out, err := exec.Command("systemctl", action, name+".service").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %s", action, strings.TrimSpace(string(out)))
	}
	return nil
}
