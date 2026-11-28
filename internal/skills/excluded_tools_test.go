package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogExcludedCommandToolsReportsCwdAndWorkdirContainment(t *testing.T) {
	parent := t.TempDir()
	cwd := filepath.Join(parent, "repo")
	skillsRoot := filepath.Join(cwd, "local-skills")
	toolsDir := filepath.Join(skillsRoot, "checker", "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsRoot, "checker", "SKILL.md"), []byte("---\nname: checker\ndescription: checker skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "check.yaml"), []byte("name: check_cmd\ncommand: [\"echo\", \"ok\"]\ninput_schema:\n  type: object\n"), 0o600); err != nil {
		t.Fatalf("write tool.yaml: %v", err)
	}
	catalog, err := Scan([]string{skillsRoot})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	// When invoked from inside `repo`, check_cmd is excluded by cwd containment
	// and ExcludedCommandTools must report it explicitly.
	t.Chdir(cwd)
	outsideWorkdir := t.TempDir()
	if trusted := catalog.TrustedCommandTools(outsideWorkdir); len(trusted) != 0 {
		t.Fatalf("expected 0 trusted tools under cwd containment, got %#v", trusted)
	}
	excluded := catalog.ExcludedCommandTools(outsideWorkdir)
	if len(excluded) != 1 || excluded[0].Tool.Name != "check_cmd" || !strings.Contains(excluded[0].Reason, "working directory") {
		t.Fatalf("expected explicit excluded tool report, got %#v", excluded)
	}

	// Moving cwd outside `repo` admits the tool cleanly with 0 excluded.
	otherCwd := t.TempDir()
	t.Chdir(otherCwd)
	if trusted := catalog.TrustedCommandTools(outsideWorkdir); len(trusted) != 1 || trusted[0].Name != "check_cmd" {
		t.Fatalf("expected check_cmd trusted outside cwd/workdir, got %#v", trusted)
	}
	if excluded := catalog.ExcludedCommandTools(outsideWorkdir); len(excluded) != 0 {
		t.Fatalf("expected 0 excluded tools outside cwd/workdir, got %#v", excluded)
	}
}
