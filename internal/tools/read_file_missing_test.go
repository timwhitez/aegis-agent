package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"aegis-agent/internal/config"
)

func TestReadFileMissingPathAcrossModes(t *testing.T) {
	for _, mode := range []string{"line", "byte"} {
		for _, pathKind := range []string{"relative_leaf", "relative_parent", "absolute"} {
			t.Run(mode+"_"+pathKind, func(t *testing.T) {
				cfg := config.Default()
				workdir := t.TempDir()
				registry, err := NewRegistry(cfg, nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				path := "missing.txt"
				if pathKind == "relative_parent" {
					path = "absent/nested.txt"
				} else if pathKind == "absolute" {
					path = filepath.Join(workdir, path)
				}
				args := map[string]any{"path": path}
				if mode == "byte" {
					args["byte_offset"], args["byte_limit"] = 0, 128
				}
				raw, _ := json.Marshal(args)
				result, err := registry.Execute(context.Background(), "read_file", ExecContext{Workdir: workdir, Config: cfg}, raw)
				if err != nil || !result.IsError || result.Metadata[MetadataFailureClass] != FailureClassNotFound {
					t.Fatalf("expected typed missing-file result: %#v, %v", result, err)
				}
				if result.Metadata["path"] != path {
					t.Fatalf("changed supplied path: %#v", result.Metadata)
				}
				for _, output := range []string{result.LLMOutput, result.DisplayOutput} {
					if !strings.Contains(output, path) || !strings.Contains(output, "Do not keep guessing source paths") {
						t.Fatalf("lost missing path or discovery hint: %q", output)
					}
					if pathKind != "absolute" && strings.Contains(output, workdir) {
						t.Fatalf("expanded relative path into host workspace: %q", output)
					}
				}
			})
		}
	}
}
