// Package web embeds the client web UI. The shared/ folder is a copy of
// ../../shared/ui created by `make sync-ui` (run automatically by make).
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html app shared
var files embed.FS

// FS returns the web UI file system.
func FS() fs.FS { return files }
