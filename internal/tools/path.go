package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ResolveWorkspacePath(workdir, input string) (string, error) {
	base, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}

	target := input
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	target = filepath.Clean(target)
	resolved, err := resolveWithExistingParent(target)
	if err != nil {
		return "", err
	}
	if !isWithin(base, resolved) {
		return "", errors.New("path escapes workspace")
	}
	return resolved, nil
}

func resolveWithExistingParent(path string) (string, error) {
	var suffix []string
	current := path
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("unable to resolve path")
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func isWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

var deniedWorkspaceWriteDirs = []string{
	".git",
	".go-cli-agent",
	".ssh",
	".aws",
	".gnupg",
	".kube",
	".docker",
}

var deniedWorkspaceWriteFiles = []string{
	".env",
	"id_rsa",
	"id_ed25519",
	"credentials",
}

func CheckWorkspaceWriteAllowed(workdir, resolvedPath string) error {
	base, err := filepath.Abs(workdir)
	if err != nil {
		return err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(base, resolvedPath)
	if err != nil {
		return err
	}
	displayPath := filepath.ToSlash(rel)
	return checkWorkspaceWriteDisplayPath(displayPath, filepath.Base(resolvedPath))
}

func CheckWorkspaceWriteInputAllowed(workdir, inputPath string) error {
	displayPath := filepath.Clean(inputPath)
	if filepath.IsAbs(displayPath) {
		base, err := filepath.Abs(workdir)
		if err != nil {
			return err
		}
		if rel, err := filepath.Rel(base, displayPath); err == nil {
			displayPath = rel
		}
	}
	displayPath = filepath.ToSlash(displayPath)
	return checkWorkspaceWriteDisplayPath(displayPath, pathBaseFromSlash(displayPath))
}

func checkWorkspaceWriteDisplayPath(displayPath, baseName string) error {
	parts := strings.Split(displayPath, "/")
	for _, part := range parts {
		for _, denied := range deniedWorkspaceWriteDirs {
			if strings.EqualFold(part, denied) {
				return fmt.Errorf("write denied: path '%s' matches deny pattern '%s/'", displayPath, denied)
			}
		}
	}
	for _, denied := range deniedWorkspaceWriteFiles {
		if strings.EqualFold(baseName, denied) {
			return fmt.Errorf("write denied: path '%s' matches deny pattern '%s'", displayPath, denied)
		}
	}
	return nil
}

func pathBaseFromSlash(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
