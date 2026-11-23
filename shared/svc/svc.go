// Package svc runs an app either in the console or as an OS service
// (Windows Service / systemd) using kardianos/service.
package svc

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/kardianos/service"
)

// RunFunc is the app main loop. It must return when ctx is cancelled.
type RunFunc func(ctx context.Context) error

type program struct {
	run    RunFunc
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (p *program) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		if err := p.run(ctx); err != nil {
			log.Printf("fatal: %v", err)
			if service.Interactive() {
				os.Exit(1)
			}
			_ = s.Stop()
		}
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	p.once.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
	})
	if p.done != nil {
		<-p.done
	}
	return nil
}

// Commands lists the supported service control commands.
var Commands = []string{"install", "uninstall", "start", "stop", "restart", "status"}

// IsCommand reports whether arg is a service control command.
func IsCommand(arg string) bool {
	for _, c := range Commands {
		if c == arg {
			return true
		}
	}
	return false
}

// Main runs the app. If cmd is a service command it is executed instead.
// dir is passed to the installed service as "-dir <dir>".
func Main(name, display, desc, dir, cmd string, run RunFunc) {
	cfg := &service.Config{
		Name:             name,
		DisplayName:      display,
		Description:      desc,
		Arguments:        []string{"-dir", dir},
		WorkingDirectory: dir,
	}
	prg := &program{run: run}
	s, err := service.New(prg, cfg)
	if err != nil {
		log.Fatalf("service: %v", err)
	}
	if cmd != "" {
		if cmd == "status" {
			st, err := s.Status()
			if err != nil {
				log.Fatalf("status: %v", err)
			}
			names := map[service.Status]string{service.StatusRunning: "running", service.StatusStopped: "stopped", service.StatusUnknown: "unknown"}
			fmt.Println(names[st])
			return
		}
		if err := service.Control(s, cmd); err != nil {
			log.Fatalf("%s: %v", cmd, err)
		}
		fmt.Printf("%s: %s ok\n", name, cmd)
		return
	}
	if err := s.Run(); err != nil {
		log.Fatalf("run: %v", err)
	}
}
