package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
	Name        string
	Description string
	Path        string
	Body        string
	Tools       []CommandTool
}

type Catalog struct {
	skills map[string]Skill
	order  []string
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
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || d.Name() != "SKILL.md" {
				return nil
			}
			skill, loadErr := loadSkill(path)
			if loadErr != nil {
				return loadErr
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

func (c *Catalog) Load(name string) (Skill, error) {
	skill, ok := c.skills[name]
	if !ok {
		return Skill{}, fmt.Errorf("unknown skill %q", name)
	}
	return skill, nil
}

func (c *Catalog) CommandTools() []CommandTool {
	var out []CommandTool
	for _, name := range c.order {
		out = append(out, c.skills[name].Tools...)
	}
	return out
}

func loadSkill(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	text := string(data)
	meta, body, err := parseFrontmatter(text)
	if err != nil {
		return Skill{}, err
	}
	name := strings.TrimSpace(meta["name"])
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	description := strings.TrimSpace(meta["description"])
	if description == "" {
		description = firstParagraph(body)
	}
	tools, err := loadTools(filepath.Join(filepath.Dir(path), "tools"), name, path)
	if err != nil {
		return Skill{}, err
	}
	return Skill{
		Name:        name,
		Description: description,
		Path:        path,
		Body:        body,
		Tools:       tools,
	}, nil
}

func loadTools(dir, skillName, skillPath string) ([]CommandTool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []CommandTool{}, nil
		}
		return nil, err
	}
	var out []CommandTool
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var tool CommandTool
		if err := yaml.Unmarshal(data, &tool); err != nil {
			return nil, err
		}
		tool.SkillName = skillName
		tool.SkillPath = skillPath
		if tool.TimeoutSec <= 0 {
			tool.TimeoutSec = 120
		}
		out = append(out, tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func parseFrontmatter(text string) (map[string]string, string, error) {
	match := regexp.MustCompile(`(?s)^---\n(.*?)\n---\n?(.*)$`).FindStringSubmatch(text)
	if match == nil {
		return map[string]string{}, strings.TrimSpace(text), nil
	}
	meta := map[string]string{}
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(match[1]), &raw); err != nil {
		return nil, "", err
	}
	for key, value := range raw {
		meta[key] = fmt.Sprint(value)
	}
	return meta, strings.TrimSpace(match[2]), nil
}

func firstParagraph(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "No description"
	}
	parts := strings.Split(body, "\n\n")
	return strings.TrimSpace(parts[0])
}
