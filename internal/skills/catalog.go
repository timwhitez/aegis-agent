package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"go-cli-agent/internal/fileutil"
	"gopkg.in/yaml.v3"
)

type Summary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

type CommandTool struct {
	SkillName   string         `yaml:"-"`
	SkillPath   string         `yaml:"-"`
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Command     []string       `yaml:"command"`
	TimeoutSec  int            `yaml:"timeout_sec"`
	InputSchema map[string]any `yaml:"input_schema"`
}

type Skill struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Path        string        `json:"path"`
	Body        string        `json:"body,omitempty"`
	Tools       []CommandTool `json:"tools"`
}

type Catalog struct {
	skills map[string]Skill
	order  []string
	mu     sync.Mutex
}

func Scan(dirs []string) (*Catalog, error) {
	catalog := &Catalog{skills: map[string]Skill{}}
	for _, root := range dirs {
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		rootReal, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, err
		}
		rootReal = filepath.Clean(rootReal)
		err = filepath.WalkDir(rootReal, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || d.Name() != "SKILL.md" {
				return nil
			}
			skill, loadErr := loadSkill(rootReal, path)
			if loadErr != nil {
				return fmt.Errorf("load skill %s: %w", path, loadErr)
			}
			if _, exists := catalog.skills[skill.Name]; exists {
				return fmt.Errorf("duplicate skill name: %s", skill.Name)
			}
			catalog.skills[skill.Name] = skill
			catalog.order = append(catalog.order, skill.Name)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(catalog.order)
	return catalog, nil
}

func (c *Catalog) Summaries() []Summary {
	var out []Summary
	for _, name := range c.order {
		skill := c.skills[name]
		out = append(out, Summary{
			Name:        skill.Name,
			Description: skill.Description,
			Path:        skill.Path,
		})
	}
	return out
}

func (c *Catalog) Names() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.order))
	copy(out, c.order)
	return out
}

func (c *Catalog) Load(name string) (Skill, error) {
	skill, ok := c.skills[name]
	if !ok {
		return Skill{}, fmt.Errorf("unknown skill %q", name)
	}
	return skill, nil
}

func (c *Catalog) LoadBody(name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	skill, ok := c.skills[name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q", name)
	}

	if skill.Body != "" {
		return skill.Body, nil
	}

	data, _, err := fileutil.ReadRegularFileNoSymlink(skill.Path)
	if err != nil {
		return "", err
	}

	_, body, err := parseFrontmatter(string(data))
	if err != nil {
		return "", fmt.Errorf("parse skill frontmatter %s: %w", skill.Path, err)
	}

	skill.Body = body
	c.skills[name] = skill

	return body, nil
}

func (c *Catalog) CommandTools() []CommandTool {
	var out []CommandTool
	for _, name := range c.order {
		out = append(out, c.skills[name].Tools...)
	}
	return out
}

func (c *Catalog) TrustedCommandTools(cwd string) []CommandTool {
	if c == nil {
		return nil
	}
	anchors := []string{}
	if strings.TrimSpace(cwd) != "" {
		anchors = append(anchors, cwd)
	}
	if processCwd, err := os.Getwd(); err == nil && strings.TrimSpace(processCwd) != "" {
		anchors = append(anchors, processCwd)
	}
	anchorReals := make([]string, 0, len(anchors))
	for _, anchor := range anchors {
		real, err := realOrAbs(anchor)
		if err != nil {
			continue
		}
		anchorReals = append(anchorReals, real)
	}
	if len(anchorReals) == 0 {
		return nil
	}
	var out []CommandTool
	for _, tool := range c.CommandTools() {
		toolPath, err := realOrAbs(tool.SkillPath)
		if err != nil {
			continue
		}
		if pathWithinAny(anchorReals, toolPath) {
			continue
		}
		out = append(out, tool)
	}
	return out
}

func pathWithinAny(anchors []string, target string) bool {
	for _, anchor := range anchors {
		if pathWithin(anchor, target) {
			return true
		}
	}
	return false
}

func realOrAbs(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(real), nil
	}
	if os.IsNotExist(err) {
		return filepath.Clean(abs), nil
	}
	return "", err
}

func loadSkill(rootReal, path string) (Skill, error) {
	skillDir := filepath.Dir(path)
	skillDirReal, err := filepath.EvalSymlinks(skillDir)
	if err != nil {
		return Skill{}, err
	}
	data, realPath, err := readCatalogFile(rootReal, skillDirReal, path)
	if err != nil {
		return Skill{}, err
	}
	text := string(data)
	meta, _, err := parseFrontmatter(text)
	if err != nil {
		return Skill{}, err
	}
	name := strings.TrimSpace(meta["name"])
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	description := strings.TrimSpace(meta["description"])
	if description == "" {
		description = "No description"
	}
	tools, err := loadTools(rootReal, skillDirReal, filepath.Join(skillDir, "tools"), name, realPath)
	if err != nil {
		return Skill{}, err
	}
	return Skill{
		Name:        name,
		Description: description,
		Path:        realPath,
		Body:        "",
		Tools:       tools,
	}, nil
}

func loadTools(rootReal, skillDirReal, dir, skillName, skillPath string) ([]CommandTool, error) {
	toolDirReal, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []CommandTool{}, nil
		}
		return nil, err
	}
	if !pathWithin(rootReal, toolDirReal) || !pathWithin(skillDirReal, toolDirReal) {
		return nil, fmt.Errorf("skill tools directory escapes skill root: %s", dir)
	}
	entries, err := os.ReadDir(toolDirReal)
	if err != nil {
		return nil, err
	}
	var out []CommandTool
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, _, err := readCatalogFile(rootReal, toolDirReal, filepath.Join(toolDirReal, entry.Name()))
		if err != nil {
			return nil, err
		}
		var tool CommandTool
		if err := yaml.Unmarshal(data, &tool); err != nil {
			return nil, fmt.Errorf("parse skill tool %s: %w", filepath.Join(toolDirReal, entry.Name()), err)
		}
		tool.SkillName = skillName
		tool.SkillPath = skillPath
		out = append(out, tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readCatalogFile(rootReal, allowedDirReal, path string) ([]byte, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", err
	}
	realPath = filepath.Clean(realPath)
	if !pathWithin(rootReal, realPath) || !pathWithin(allowedDirReal, realPath) {
		return nil, "", fmt.Errorf("skill catalog file escapes skill root: %s", path)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("symlinked skill catalog file is not allowed: %s", path)
	}
	data, _, err := fileutil.ReadRegularFileNoSymlink(realPath)
	if err != nil {
		return nil, "", err
	}
	return data, realPath, nil
}

func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel))
}

// ManifestMeta holds the SKILL.md front matter fields callers outside this
// package need (e.g. the web console skill listing). It is produced by the
// same YAML-aware parser the runtime catalog uses so every surface reports
// identical metadata.
type ManifestMeta struct {
	Name        string
	Description string
	Body        string
}

// ParseManifest parses raw SKILL.md content into name/description/body using the
// canonical front matter logic. Callers that only need listing metadata should
// use this instead of ad-hoc line scanning, which mishandles YAML block scalars
// (e.g. `description: >-`), quoted values, and CRLF/BOM inputs.
func ParseManifest(data []byte) (ManifestMeta, error) {
	meta, body, err := parseFrontmatter(string(data))
	if err != nil {
		return ManifestMeta{}, err
	}
	return ManifestMeta{
		Name:        strings.TrimSpace(meta["name"]),
		Description: strings.TrimSpace(meta["description"]),
		Body:        body,
	}, nil
}

func parseFrontmatter(text string) (map[string]string, string, error) {
	// Normalize inputs so manifests authored on Windows or exported with a UTF-8
	// BOM parse the same as LF/no-BOM ones; otherwise the leading `---` anchor
	// fails to match and the entire front matter is silently dropped.
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	match := regexp.MustCompile(`(?s)^---\n(.*?)\n---\n?(.*)$`).FindStringSubmatch(text)
	if match == nil {
		return map[string]string{}, strings.TrimSpace(text), nil
	}
	meta := map[string]string{}
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(match[1]), &raw); err != nil {
		meta, fallbackOK := parseLooseFrontmatterScalars(match[1])
		if fallbackOK {
			return meta, strings.TrimSpace(match[2]), nil
		}
		return nil, "", err
	}
	for key, value := range raw {
		meta[key] = fmt.Sprint(value)
	}
	return meta, strings.TrimSpace(match[2]), nil
}

func parseLooseFrontmatterScalars(text string) (map[string]string, bool) {
	meta := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		validKey := true
		for _, r := range key {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
				continue
			}
			validKey = false
			break
		}
		if !validKey {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		meta[key] = value
	}
	_, hasName := meta["name"]
	_, hasDescription := meta["description"]
	return meta, hasName || hasDescription
}

func firstParagraph(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "No description"
	}
	parts := strings.Split(body, "\n\n")
	return strings.TrimSpace(parts[0])
}
