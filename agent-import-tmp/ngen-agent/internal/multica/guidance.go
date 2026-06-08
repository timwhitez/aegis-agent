package multica

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ngen/internal/task"
)

const (
	maxGuidanceBytes = 32 * 1024
	maxSkillFiles    = 32
)

func CollectWorkspaceGuidance(taskID, workdir string) task.WorkspaceGuidanceArtifact {
	guidance := task.WorkspaceGuidanceArtifact{
		ObjectKind:    "workspace_guidance",
		SchemaVersion: task.SchemaVersion,
		TaskID:        taskID,
		GeneratedAt:   task.Now(),
	}
	if doc, ok := readGuidanceDocument(workdir, "AGENTS.md"); ok {
		guidance.Documents = append(guidance.Documents, doc)
		guidance.Refs = append(guidance.Refs, doc.Ref)
	}
	for _, skill := range readWorkspaceSkills(workdir) {
		guidance.Skills = append(guidance.Skills, skill)
		guidance.Refs = append(guidance.Refs, skill.Ref)
	}
	return guidance
}

func readGuidanceDocument(workdir, rel string) (task.WorkspaceGuidanceDocument, bool) {
	data, truncated, err := readBoundedText(filepath.Join(workdir, filepath.FromSlash(rel)))
	if err != nil {
		return task.WorkspaceGuidanceDocument{}, false
	}
	return task.WorkspaceGuidanceDocument{
		Ref:       "workspace:" + filepath.ToSlash(rel),
		Path:      filepath.ToSlash(rel),
		Content:   data,
		Truncated: truncated,
	}, true
}

func readWorkspaceSkills(workdir string) []task.WorkspaceSkill {
	root := filepath.Join(workdir, "skills")
	entries := []task.WorkspaceSkill{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(entries) >= maxSkillFiles {
			if d.IsDir() && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".ngen" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "SKILL.md" {
			return nil
		}
		rel, err := filepath.Rel(workdir, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil
		}
		content, truncated, err := readBoundedText(path)
		if err != nil {
			return nil
		}
		name := filepath.Base(filepath.Dir(path))
		entries = append(entries, task.WorkspaceSkill{
			Name:      name,
			Ref:       "workspace:" + filepath.ToSlash(rel),
			Path:      filepath.ToSlash(rel),
			Summary:   firstNonEmptyLine(content),
			Content:   content,
			Truncated: truncated,
		})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries
}

func readBoundedText(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	truncated := false
	if len(data) > maxGuidanceBytes {
		data = data[:maxGuidanceBytes]
		truncated = true
	}
	return strings.TrimRight(string(data), "\n"), truncated, nil
}

func firstNonEmptyLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
