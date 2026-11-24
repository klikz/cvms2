// Command client is the CVMS client: it manages local cameras and pushes
// them to the central server on demand.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cvms/client/internal/cameras"
	"cvms/client/internal/config"
	"cvms/client/internal/secret"
	"cvms/client/internal/tunnel"
	"cvms/client/internal/webui"
	"cvms/client/web"
	"cvms/shared/appdir"
	"cvms/shared/svc"
)

var version = "dev"

func main() {
	dir := flag.String("dir", "", "data directory (config.yaml, client.key); default: next to the executable")
	resetPw := flag.Bool("reset-password", false, "reset the web UI password and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "CVMS client %s\n\nUsage: client [flags] [install|uninstall|start|stop|restart|status]\n\n", version)
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
	if abs, err := filepath.Abs(*dir); err == nil {
		*dir = abs
	}
	cmd := flag.Arg(0)
	if cmd != "" && !svc.IsCommand(cmd) {
		flag.Usage()
		os.Exit(2)
	}
	if *resetPw {
		store, err := load(*dir)
		if err != nil {
			log.Fatal(err)
		}
		pw := randomPassword()
		if err := setPassword(store, pw); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("web UI password reset. username: %s  password: %s\n", store.Get().UI.Username, pw)
		return
	}
	spec = svc.Spec{Name: "cvms-client", Display: "CVMS Client", Description: "Central Video Management System client", Dir: *dir}
	svc.Main(spec.Name, spec.Display, spec.Description, *dir, cmd, func(ctx context.Context) error {
		return run(ctx, *dir)
	})
}

var spec svc.Spec

func load(dir string) (*config.Store, error) {
	box, err := secret.Open(filepath.Join(dir, "client.key"))
	if err != nil {
		return nil, err
	}
	store, created, err := config.Load(filepath.Join(dir, "config.yaml"), box)
	if err != nil {
		return nil, err
	}
	if created {
		log.Printf("default config created: %s", store.Path())
	}
	return store, nil
}

func run(ctx context.Context, dir string) error {
	log.Printf("CVMS client %s starting", version)
	store, err := load(dir)
	if err != nil {
		return err
	}
	if store.Get().UI.PasswordHash == "" {
		pw := randomPassword()
		if err := setPassword(store, pw); err != nil {
			return err
		}
		log.Printf("================================================================")
		log.Printf(" First start: web UI account created")
		log.Printf("   username: %s", store.Get().UI.Username)
		log.Printf("   password: %s", pw)
		log.Printf(" You must change this password at first login.")
		log.Printf("================================================================")
	}

	cams := cameras.New(store)
	go cams.Run(ctx)
	tun := tunnel.New(store, cams, version)
	go tun.Run(ctx)

	ui := webui.New(store, cams, tun, web.FS(), version)
	ui.SetService(spec)
	go ui.EnsureAutostart()
	addr := store.Get().UI.Listen
	hs := &http.Server{Addr: addr, Handler: ui.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Printf("web UI: http://%s", addr)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	log.Printf("client stopped")
	return nil
}

func setPassword(store *config.Store, pw string) error {
	h, err := webui.HashPassword(pw)
	if err != nil {
		return err
	}
	return store.Update(func(c *config.Config) error {
		c.UI.PasswordHash, c.UI.MustChange = h, true
		return nil
	})
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
