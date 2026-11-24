// Package appdir finds the directory where an app keeps config and data.
package appdir

import (
	"os"
	"path/filepath"
	"strings"
)

// Dir returns the directory next to the executable. When running via
// `go run` (binary in a temp go-build dir) it returns the working directory.
func Dir() string {
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		d := filepath.Dir(exe)
		if !isTemp(d) {
			return d
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// IsDev reports whether the program runs via `go run` (binary in a temp
// build folder), where installing it as a service makes no sense.
func IsDev() bool {
	exe, err := os.Executable()
	if err != nil {
		return true
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return isTemp(filepath.Dir(exe))
}

func isTemp(d string) bool {
	if strings.Contains(d, "go-build") {
		return true
	}
	tmp := os.TempDir()
	return tmp != "" && strings.HasPrefix(strings.ToLower(d), strings.ToLower(tmp))
}
