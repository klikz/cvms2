// Command stopapp stops a running CVMS app (used by `make stop`):
//  1. stops its OS service (Windows Service / systemd) if it is installed and running;
//  2. stops console copies started with `make run` (go run) or a built binary.
//
// Usage: go run cvms/shared/tools/stopapp <server|client>
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kardianos/service"
)

type nopProgram struct{}

func (nopProgram) Start(service.Service) error { return nil }
func (nopProgram) Stop(service.Service) error  { return nil }

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "server" && os.Args[1] != "client") {
		fmt.Fprintln(os.Stderr, "usage: stopapp <server|client>")
		os.Exit(2)
	}
	app := os.Args[1]
	failed := false

	// 1. OS service
	name := "cvms-" + app
	s, err := service.New(nopProgram{}, &service.Config{Name: name})
	if err == nil {
		if st, err := s.Status(); err == nil && st == service.StatusRunning {
			if err := s.Stop(); err != nil {
				fmt.Fprintf(os.Stderr, "service %s: stop failed: %v (run as administrator / with sudo)\n", name, err)
				failed = true
			} else {
				fmt.Printf("service %s stopped\n", name)
			}
		}
	}

	// 2. console processes
	n := 0
	for _, p := range listProcs() {
		if p.pid == os.Getpid() || !matches(app, p.exe) {
			continue
		}
		if err := kill(p.pid); err != nil {
			fmt.Fprintf(os.Stderr, "pid %d (%s): %v\n", p.pid, p.exe, err)
			failed = true
			continue
		}
		fmt.Printf("stopped pid %d (%s)\n", p.pid, p.exe)
		n++
	}
	if n == 0 && !failed {
		fmt.Printf("cvms %s: no running process found\n", app)
	}
	if failed {
		os.Exit(1)
	}
}

// matches reports whether exe is the given app: a `go run` binary
// (…/go-build…/exe/server) or a built one (cvms-server-<version>-…).
func matches(app, exe string) bool {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(exe)), ".exe")
	if strings.HasPrefix(base, "cvms-"+app) {
		return true
	}
	return base == app && strings.Contains(strings.ToLower(filepath.ToSlash(exe)), "go-build")
}

type proc struct {
	pid int
	exe string
}
