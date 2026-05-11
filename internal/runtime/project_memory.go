package runtime

import (
	"os"
	"path/filepath"
	"strings"

	"go-cli-agent/internal/tools"
)

type projectMemoryFile struct {
	Name    string
	Path    string
	Present bool
	Excerpt string
}

type projectMemoryStack struct {
	Files []projectMemoryFile
}

func loadProjectMemoryStack(workdir string) projectMemoryStack {
	files := []projectMemoryFile{
		loadProjectMemoryFile(workdir, "spec", filepath.Join("reports", "spec.md")),
		loadProjectMemoryFile(workdir, "plan", filepath.Join("reports", "plan.md")),
		loadProjectMemoryFile(workdir, "progress", filepath.Join("reports", "progress.md")),
		loadProjectMemoryFile(workdir, "validation", filepath.Join("reports", "validation.md")),
	}
	return projectMemoryStack{Files: files}
}

func loadProjectMemoryFile(workdir, name, rel string) projectMemoryFile {
	entry := projectMemoryFile{Name: name, Path: rel}
	path, err := tools.ResolveWorkspacePath(workdir, rel)
	if err != nil {
		return entry
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return entry
	}
	entry.Present = true
	entry.Excerpt = truncateText(projectMemoryExcerpt(string(data), 8), 320)
	return entry
}

func (s projectMemoryStack) PresentPaths() []string {
	var out []string
	for _, file := range s.Files {
		if file.Present {
			out = append(out, file.Path)
		}
	}
	return out
}

func (s projectMemoryStack) MissingPaths() []string {
	var out []string
	for _, file := range s.Files {
		if !file.Present {
			out = append(out, file.Path)
		}
	}
	return out
}

func (s projectMemoryStack) Summary() map[string]any {
	files := make([]map[string]any, 0, len(s.Files))
	for _, file := range s.Files {
		item := map[string]any{
			"name":    file.Name,
			"path":    file.Path,
			"present": file.Present,
		}
		if file.Excerpt != "" {
			item["excerpt"] = file.Excerpt
		}
		files = append(files, item)
	}
	return map[string]any{
		"present": s.PresentPaths(),
		"missing": s.MissingPaths(),
		"files":   files,
	}
}

func firstNonEmptyLines(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	selected := make([]string, 0, limit)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		selected = append(selected, trimmed)
		if len(selected) >= limit {
			break
		}
	}
	return strings.Join(selected, " ")
}

func projectMemoryExcerpt(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	selected := make([]string, 0, limit)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if looksProjectMemorySignal(trimmed) {
			selected = append(selected, trimmed)
		}
		if len(selected) >= limit {
			break
		}
	}
	if len(selected) == 0 {
		return firstNonEmptyLines(text, limit)
	}
	return strings.Join(selected, " ")
}

func looksProjectMemorySignal(line string) bool {
	switch {
	case strings.HasPrefix(line, "#"):
		return true
	case strings.HasPrefix(line, "-"), strings.HasPrefix(line, "*"):
		return true
	case len(line) >= 2 && line[0] >= '0' && line[0] <= '9' && strings.Contains(line, "."):
		return true
	default:
		return false
	}
}
