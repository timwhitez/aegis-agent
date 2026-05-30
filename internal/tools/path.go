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

var deniedWorkspaceWriteFilePaths = []string{
	".m2/settings.xml",
	".m2/settings-security.xml",
	".gradle/gradle.properties",
	".nuget/NuGet.Config",
	".pip/pip.conf",
	".config/pip/pip.conf",
}

var deniedWorkspaceWriteFiles = []string{
	".env",
	".envrc",
	".npmrc",
	".netrc",
	"_netrc",
	".pypirc",
	".git-credentials",
	".dockercfg",
	".yarnrc",
	".yarnrc.yml",
	".pnpmrc",
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
	if err := checkWorkspaceWriteDisplayPath(displayPath); err != nil {
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
	return checkWorkspaceWriteDisplayPath(displayPath)
}

func checkWorkspaceWriteDisplayPath(displayPath string) error {
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
	if pattern := deniedWorkspaceWriteFilePathPattern(parts); pattern != "" {
		return fmt.Errorf("write denied: path '%s' matches deny pattern '%s'", displayPath, pattern)
	}
	for _, part := range parts {
		if pattern := deniedWorkspaceWritePathComponentPattern(part); pattern != "" {
			return fmt.Errorf("write denied: path '%s' matches deny pattern '%s'", displayPath, pattern)
		}
	}
	return nil
}

func deniedWorkspaceWritePathComponentPattern(name string) string {
	for _, denied := range deniedWorkspaceWriteFiles {
		if strings.EqualFold(name, denied) {
			return denied
		}
	}
	return deniedWorkspaceWriteFilePattern(name)
}

func deniedWorkspaceWriteFilePathPattern(parts []string) string {
	for _, denied := range deniedWorkspaceWriteFilePaths {
		if displayPathContainsDirPath(parts, denied) {
			return denied
		}
	}
	return ""
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
	for _, denied := range deniedWorkspaceWriteFilePaths {
		deniedPath, ok, err := resolveWorkspacePolicyPathWithExistingParent(base, denied)
		if err != nil {
			return err
		}
		if ok && resolvedAliasMatches(deniedPath, resolvedPath) {
			return fmt.Errorf("write denied: path '%s' resolves to deny pattern '%s'", displayPath, denied)
		}
	}
	for _, denied := range deniedWorkspaceWriteFiles {
		deniedPath, ok, err := resolveExistingWorkspacePolicyPath(base, denied)
		if err != nil {
			return err
		}
		if ok && resolvedAliasMatches(deniedPath, resolvedPath) {
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
		if ok && resolvedAliasMatches(deniedPath, resolvedPath) {
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
	case allowedEnvTemplateName(name):
		return ""
	case strings.HasPrefix(name, ".env."):
		return ".env.*"
	case name == "identity":
		return "identity"
	case strings.HasPrefix(name, "id_"):
		return "id_*"
	case strings.Contains(name, "private_key"):
		return "*private_key*"
	case strings.Contains(name, "private-key"):
		return "*private-key*"
	case strings.Contains(name, "client_secret"):
		return "*client_secret*"
	case strings.Contains(name, "client-secret"):
		return "*client-secret*"
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

func allowedEnvTemplateName(name string) bool {
	switch name {
	case ".env.example", ".env.sample", ".env.template":
		return true
	default:
		return false
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
	resolved, err := resolvePolicyPathWithExistingParent(path)
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

func resolveWorkspacePolicyPathWithExistingParent(base, rel string) (string, bool, error) {
	resolved, err := resolvePolicyPathWithExistingParent(filepath.Join(base, rel))
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

func resolvePolicyPathWithExistingParent(path string) (string, error) {
	return resolvePolicyPathWithExistingParentDepth(filepath.Clean(path), 0)
}

func resolvePolicyPathWithExistingParentDepth(path string, depth int) (string, error) {
	if depth > 40 {
		return "", errors.New("too many symlink levels")
	}
	var suffix []string
	current := filepath.Clean(path)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			resolved, err := resolveExistingPolicyPath(current, info, depth)
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

func resolveExistingPolicyPath(path string, info os.FileInfo, depth int) (string, error) {
	if info.Mode()&os.ModeSymlink == 0 {
		return filepath.EvalSymlinks(path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	target, readErr := os.Readlink(path)
	if readErr != nil {
		return "", readErr
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return resolvePolicyPathWithExistingParentDepth(target, depth+1)
}

func sameCleanPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func resolvedAliasMatches(aliasPath, resolvedPath string) bool {
	return sameCleanPath(aliasPath, resolvedPath) || isWithin(aliasPath, resolvedPath)
}
