package webconsole

import (
	"embed"
	"io/fs"
)

//go:embed assets/* assets-v2/*
var embeddedAssets embed.FS

type namespacedAssetFS struct {
	fs.FS
	namespace string
}

func (n namespacedAssetFS) cacheNamespace() string {
	return n.namespace
}

func assetFS() (fs.FS, error) {
	assets, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		return nil, err
	}
	return namespacedAssetFS{FS: assets, namespace: "legacy-shared"}, nil
}

func assetV2FS() (fs.FS, error) {
	assets, err := fs.Sub(embeddedAssets, "assets-v2")
	if err != nil {
		return nil, err
	}
	return namespacedAssetFS{FS: assets, namespace: "v2"}, nil
}
