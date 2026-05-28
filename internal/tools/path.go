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
	".azure",
	".oci",
	".gnupg",
	".kube",
	".docker",
}

var deniedWorkspaceWriteDirPaths = []string{
	".config/gcloud",
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
	if !isWithin(base, resolvedPath) {
		return errors.New("path escapes workspace")
	}
	rel, err := filepath.Rel(base, resolvedPath)
	if err != nil {
		return err
	}
	displayPath := filepath.ToSlash(rel)
	if err := checkWorkspaceWriteDisplayPath(displayPath, filepath.Base(resolvedPath)); err != nil {
		return err
	}
	return checkWorkspaceWriteResolvedAlias(base, resolvedPath, displayPath)
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
	for _, denied := range deniedWorkspaceWriteDirPaths {
		if displayPathContainsDirPath(parts, denied) {
			return fmt.Errorf("write denied: path '%s' matches deny pattern '%s/'", displayPath, denied)
		}
	}
	for _, denied := range deniedWorkspaceWriteFiles {
		if strings.EqualFold(baseName, denied) {
			return fmt.Errorf("write denied: path '%s' matches deny pattern '%s'", displayPath, denied)
		}
	}
	if pattern := deniedWorkspaceWriteFilePattern(baseName); pattern != "" {
		return fmt.Errorf("write denied: path '%s' matches deny pattern '%s'", displayPath, pattern)
	}
	return nil
}

func checkWorkspaceWriteResolvedAlias(base, resolvedPath, displayPath string) error {
	for _, denied := range deniedWorkspaceWriteDirs {
		deniedPath, ok, err := resolveExistingWorkspacePolicyPath(base, denied)
		if err != nil {
			return err
		}
		if ok && isWithin(deniedPath, resolvedPath) {
			return fmt.Errorf("write denied: path '%s' resolves to deny pattern '%s/'", displayPath, denied)
		}
	}
	for _, denied := range deniedWorkspaceWriteDirPaths {
		deniedPath, ok, err := resolveExistingWorkspacePolicyPath(base, denied)
		if err != nil {
			return err
		}
		if ok && isWithin(deniedPath, resolvedPath) {
			return fmt.Errorf("write denied: path '%s' resolves to deny pattern '%s/'", displayPath, denied)
		}
	}
	for _, denied := range deniedWorkspaceWriteFiles {
		deniedPath, ok, err := resolveExistingWorkspacePolicyPath(base, denied)
		if err != nil {
			return err
		}
		if ok && sameCleanPath(deniedPath, resolvedPath) {
			return fmt.Errorf("write denied: path '%s' resolves to deny pattern '%s'", displayPath, denied)
		}
	}
	if err := checkWorkspaceWriteResolvedPatternAliases(base, resolvedPath, displayPath); err != nil {
		return err
	}
	return nil
}

func checkWorkspaceWriteResolvedPatternAliases(base, resolvedPath, displayPath string) error {
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		pattern := deniedWorkspaceWriteFilePattern(entry.Name())
		if pattern == "" {
			continue
		}
		deniedPath, ok, err := resolveExistingWorkspacePolicyPath(base, entry.Name())
		if err != nil {
			return err
		}
		if ok && sameCleanPath(deniedPath, resolvedPath) {
			return fmt.Errorf("write denied: path '%s' resolves to deny pattern '%s'", displayPath, pattern)
		}
	}
	return nil
}

func deniedWorkspaceWriteFilePattern(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	switch {
	case name == "identity":
		return "identity"
	case strings.HasPrefix(name, "id_"):
		return "id_*"
	case strings.Contains(name, "private_key"):
		return "*private_key*"
	case strings.Contains(name, "private-key"):
		return "*private-key*"
	case strings.HasSuffix(name, ".pem"):
		return "*.pem"
	case strings.HasSuffix(name, ".key"):
		return "*.key"
	case strings.HasSuffix(name, ".p12"):
		return "*.p12"
	case strings.HasSuffix(name, ".pfx"):
		return "*.pfx"
	case strings.HasPrefix(name, "credentials."):
		return "credentials.*"
	case strings.HasSuffix(name, "_credentials.json"):
		return "*_credentials.json"
	case strings.HasSuffix(name, "-credentials.json"):
		return "*-credentials.json"
	case strings.HasSuffix(name, ".credentials"):
		return "*.credentials"
	default:
		return ""
	}
}

func displayPathContainsDirPath(parts []string, pattern string) bool {
	patternParts := strings.Split(filepath.ToSlash(pattern), "/")
	if len(patternParts) == 0 || len(patternParts) > len(parts) {
		return false
	}
	for i := 0; i+len(patternParts) <= len(parts); i++ {
		matched := true
		for j, patternPart := range patternParts {
			if !strings.EqualFold(parts[i+j], patternPart) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func resolveExistingWorkspacePolicyPath(base, rel string) (string, bool, error) {
	path := filepath.Join(base, rel)
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !isWithin(base, resolved) {
		return "", false, nil
	}
	return resolved, true, nil
}

func sameCleanPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func pathBaseFromSlash(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
