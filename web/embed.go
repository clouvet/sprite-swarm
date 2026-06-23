// Package web embeds the PWA chat UI (the reference client) into the binary so
// a sprite-agent deploys as a single static artifact. Assets are lifted/trimmed
// from sprite-mobile v1.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:assets
var embedded embed.FS

// FS returns the embedded web asset filesystem rooted at the assets directory.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}
