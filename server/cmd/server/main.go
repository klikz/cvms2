// Command server is the CVMS central server.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cvms/server/internal/api"
	"cvms/server/internal/config"
	"cvms/server/internal/db"
	"cvms/server/internal/hub"
	"cvms/server/internal/stream"
	"cvms/server/web"
	"cvms/shared/appdir"
	"cvms/shared/svc"
)

var version = "dev"

func main() {
	dir := flag.String("dir", "", "data directory (config, database); default: next to the executable")
	cfgPath := flag.String("config", "", "config file path (default <dir>/config.yaml)")
	resetAdmin := flag.Bool("reset-admin", false, "reset the 'admin' password and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "CVMS server %s\n\nUsage: server [flags] [install|uninstall|start|stop|restart|status]\n\n", version)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *dir == "" {
		*dir = appdir.Dir()
	}
	abs, err := filepath.Abs(*dir)
	if err == nil {
		*dir = abs
	}
	if *cfgPath == "" {
		*cfgPath = filepath.Join(*dir, "config.yaml")
	}
	cmd := flag.Arg(0)
	if cmd != "" && !svc.IsCommand(cmd) {
		flag.Usage()
		os.Exit(2)
	}
	if *resetAdmin {
		if err := doResetAdmin(*cfgPath); err != nil {
			log.Fatal(err)
		}
		return
	}
	spec = svc.Spec{Name: "cvms-server", Display: "CVMS Server", Description: "Central Video Management System server", Dir: *dir}
	svc.Main(spec.Name, spec.Display, spec.Description, *dir, cmd, func(ctx context.Context) error {
		return run(ctx, *cfgPath)
	})
}

var spec svc.Spec

func openDB(cfgPath string) (*config.Config, *db.DB, error) {
	cfg, created, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if created {
		log.Printf("default config created: %s", cfgPath)
	}
	d, err := db.Open(cfg.Resolve(cfg.DBPath))
	if err != nil {
		return nil, nil, err
	}
	return cfg, d, nil
}

func run(ctx context.Context, cfgPath string) error {
	log.Printf("CVMS server %s starting", version)
	cfg, d, err := openDB(cfgPath)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := ensureAdmin(d); err != nil {
		return err
	}

	h := hub.New(d, cfg)
	go h.Run(ctx)

	rtc, err := stream.NewWebRTC(cfg.WebRTC)
	if err != nil {
		log.Printf("WARNING: WebRTC disabled: %v (browsers will use the WebSocket fallback)", err)
		log.Printf("WARNING: if the port is busy, change \"webrtc.udp_port\" / \"webrtc.tcp_port\" in %s and restart", cfg.Path())
		rtc = nil
	}

	srv := api.New(cfg, d, h, rtc, web.FS(), version)
	srv.SetService(spec)
	go srv.RunMaintenance(ctx)
	go srv.EnsureAutostart()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w; the port may be busy: change \"listen\" in %s and restart", cfg.Listen, err, cfg.Path())
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Printf("web UI: %s  (webrtc udp/tcp port %d/%d)", displayAddr(cfg.Listen), cfg.WebRTC.UDPPort, cfg.WebRTC.TCPPort)
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	log.Printf("server stopped")
	return nil
}

func displayAddr(listen string) string {
	if len(listen) > 0 && listen[0] == ':' {
		return "http://localhost" + listen
	}
	return "http://" + listen
}

func randomPassword() string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 12)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

func ensureAdmin(d *db.DB) error {
	n, err := d.CountUsers()
	if err != nil || n > 0 {
		return err
	}
	pw := randomPassword()
	h, err := api.HashPassword(pw)
	if err != nil {
		return err
	}
	if err := d.CreateUser(&db.User{Username: "admin", PasswordHash: h, Role: db.RoleAdmin, MustChange: true}); err != nil {
		return err
	}
	log.Printf("================================================================")
	log.Printf(" First start: admin account created")
	log.Printf("   username: admin")
	log.Printf("   password: %s", pw)
	log.Printf(" You must change this password at first login.")
	log.Printf("================================================================")
	return nil
}

func doResetAdmin(cfgPath string) error {
	_, d, err := openDB(cfgPath)
	if err != nil {
		return err
	}
	defer d.Close()
	pw := randomPassword()
	h, err := api.HashPassword(pw)
	if err != nil {
		return err
	}
	u, err := d.UserByName("admin")
	if errors.Is(err, db.ErrNotFound) {
		u = &db.User{Username: "admin", PasswordHash: h, Role: db.RoleAdmin, MustChange: true}
		err = d.CreateUser(u)
	} else if err == nil {
		u.PasswordHash, u.Role, u.Disabled, u.MustChange = h, db.RoleAdmin, false, true
		err = d.UpdateUser(u)
		if err == nil {
			err = d.DeleteUserSessions(u.ID, "")
		}
	}
	if err != nil {
		return err
	}
	fmt.Printf("admin password reset. username: admin  password: %s\n", pw)
	return nil
}
