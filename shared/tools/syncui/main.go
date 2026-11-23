// Command syncui copies the shared UI design system into an app's web folder.
// go:embed cannot reach outside a module, so each app keeps a synced copy.
//
//	go run cvms/shared/tools/syncui <src> <dst>
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: syncui <src> <dst>")
		os.Exit(2)
	}
	src, dst := os.Args[1], os.Args[2]
	if err := os.RemoveAll(dst); err != nil {
		fail(err)
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		fail(err)
	}
	fmt.Printf("ui synced: %s -> %s\n", src, dst)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "syncui:", err)
	os.Exit(1)
}
