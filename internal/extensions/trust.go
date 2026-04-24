package extensions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type TrustMode string

const (
	TrustUntrusted TrustMode = "untrusted"
	TrustExplicit  TrustMode = "explicit_user"
)

type Candidate struct {
	Name          string    `json:"name"`
	QualifiedName string    `json:"qualified_name"`
	Path          string    `json:"path"`
	Trust         TrustMode `json:"trust"`
	Disabled      bool      `json:"disabled,omitempty"`
}

type DiscoveryResult struct {
	Candidates []Candidate `json:"candidates"`
}

func Discover(workdir string, trusted bool) (DiscoveryResult, error) {
	root, err := safeRoot(workdir)
	if err != nil {
		return DiscoveryResult{}, err
	}
	agentDir := filepath.Join(root, ".agent")
	entries, err := os.ReadDir(agentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DiscoveryResult{}, nil
		}
		return DiscoveryResult{}, err
	}
	trust := TrustUntrusted
	if trusted {
		trust = TrustExplicit
	}
	var candidates []Candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(agentDir, entry.Name())
		resolved, err := safeChild(root, path)
		if err != nil {
			continue
		}
		candidates = append(candidates, Candidate{
			Name:          entry.Name(),
			QualifiedName: "workspace/" + entry.Name(),
			Path:          resolved,
			Trust:         trust,
			Disabled:      !trusted,
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].QualifiedName < candidates[j].QualifiedName })
	if err := ValidateNoAmbiguousShortNames(candidates); err != nil {
		return DiscoveryResult{}, err
	}
	return DiscoveryResult{Candidates: candidates}, nil
}

func ValidateNoAmbiguousShortNames(candidates []Candidate) error {
	byName := map[string]string{}
	for _, candidate := range candidates {
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			continue
		}
		qualified := strings.TrimSpace(candidate.QualifiedName)
		if existing, ok := byName[name]; ok && existing != qualified {
			return fmt.Errorf("ambiguous workspace extension short name %q: %s and %s", name, existing, qualified)
		}
		byName[name] = qualified
	}
	return nil
}

func ValidateToolName(name string, reserved map[string]struct{}) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("extension tool name is required")
	}
	if _, ok := reserved[trimmed]; ok {
		return fmt.Errorf("extension tool name is reserved: %s", trimmed)
	}
	if strings.Contains(trimmed, "/") {
		return nil
	}
	return fmt.Errorf("extension tool name must be qualified: %s", trimmed)
}

func safeRoot(workdir string) (string, error) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace is not a directory: %s", workdir)
	}
	return resolved, nil
}

func safeChild(root, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("extension path escapes workspace: %s", path)
	}
	return resolved, nil
}
