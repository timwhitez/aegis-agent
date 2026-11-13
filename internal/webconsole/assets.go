package webconsole

import (
	"embed"
	"io/fs"
)

// The Web console frontend is a no-build ES module tree. The whole ui/
// directory is embedded recursively and served from /ui/*; ui/index.html is
// also the application shell for every SPA route.
//
//go:embed ui
var embeddedUI embed.FS

type namespacedAssetFS struct {
	fs.FS
	namespace string
}

func (n namespacedAssetFS) cacheNamespace() string {
	return n.namespace
}

func uiAssetFS() (fs.FS, error) {
	files, err := fs.Sub(embeddedUI, "ui")
	if err != nil {
		return nil, err
	}
	return namespacedAssetFS{FS: files, namespace: "ui"}, nil
}
