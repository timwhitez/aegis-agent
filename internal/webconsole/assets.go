package webconsole

import (
	"embed"
	"io/fs"
)

//go:embed assets/*
var embeddedAssets embed.FS

func assetFS() (fs.FS, error) {
	return fs.Sub(embeddedAssets, "assets")
}
