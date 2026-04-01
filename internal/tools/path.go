package tools

import (
	"errors"
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
