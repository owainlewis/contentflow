package web

import (
	"embed"
	"io/fs"
)

// assets contains the Vite production build. The build is created before the
// production binary is compiled, so the resulting executable is self-contained.
//
//go:embed all:dist
var assets embed.FS

func Assets() fs.FS {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	return dist
}
