package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogRejectsSymlinkedSkillMDEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	evil := filepath.Join(root, "evil")
	if err := os.MkdirAll(evil, 0o700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	secret := filepath.Join(t.TempDir(), "secret-skill.md")
	if err := os.WriteFile(secret, []byte("---\nname: evil\n---\nsecret body\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(evil, "SKILL.md")); err != nil {
		t.Fatalf("symlink skill: %v", err)
	}

	_, err := Scan([]string{root})
	if err == nil || !strings.Contains(err.Error(), "escapes skill root") {
		t.Fatalf("expected symlink escape error, got %v", err)
	}
}

func TestCatalogRejectsSymlinkedToolYAMLEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	evil := filepath.Join(root, "evil")
	toolsDir := filepath.Join(evil, "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(evil, "SKILL.md"), []byte("---\nname: evil\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	externalTool := filepath.Join(t.TempDir(), "tool.yaml")
	if err := os.WriteFile(externalTool, []byte("name: escaped_tool\ncommand: [\"echo\", \"x\"]\ninput_schema:\n  type: object\n"), 0o600); err != nil {
		t.Fatalf("write external tool: %v", err)
	}
	if err := os.Symlink(externalTool, filepath.Join(toolsDir, "tool.yaml")); err != nil {
		t.Fatalf("symlink tool: %v", err)
	}

	_, err := Scan([]string{root})
	if err == nil || !strings.Contains(err.Error(), "escapes skill root") {
		t.Fatalf("expected tool symlink escape error, got %v", err)
	}
}

func TestCatalogRejectsSymlinkedSkillMDInsideRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(root, "linked")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "manifest.md"), []byte("---\nname: linked\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write manifest target: %v", err)
	}
	if err := os.Symlink("manifest.md", filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatalf("symlink skill manifest: %v", err)
	}

	_, err := Scan([]string{root})
	if err == nil || !strings.Contains(err.Error(), "symlinked skill catalog file") {
		t.Fatalf("expected symlinked manifest to be rejected, got %v", err)
	}
}

func TestCatalogRejectsSymlinkedToolYAMLInsideRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(root, "linked")
	toolsDir := filepath.Join(skillDir, "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatalf("mkdir skill tools: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: linked\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "real.yaml"), []byte("name: linked_tool\ncommand: [\"echo\", \"x\"]\ninput_schema:\n  type: object\n"), 0o600); err != nil {
		t.Fatalf("write tool target: %v", err)
	}
	if err := os.Symlink("real.yaml", filepath.Join(toolsDir, "tool.yaml")); err != nil {
		t.Fatalf("symlink tool yaml: %v", err)
	}

	_, err := Scan([]string{root})
	if err == nil || !strings.Contains(err.Error(), "symlinked skill catalog file") {
		t.Fatalf("expected symlinked tool yaml to be rejected, got %v", err)
	}
}

func TestCatalogAllowsRegularNestedSkillAndTools(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(root, "demo")
	toolsDir := filepath.Join(skillDir, "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo\ndescription: demo skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "tool.yaml"), []byte("name: demo_tool\ncommand: [\"echo\", \"x\"]\ninput_schema:\n  type: object\n"), 0o600); err != nil {
		t.Fatalf("write tool: %v", err)
	}

	catalog, err := Scan([]string{root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	body, err := catalog.LoadBody("demo")
	if err != nil {
		t.Fatalf("load body: %v", err)
	}
	if body != "body" {
		t.Fatalf("unexpected body %q", body)
	}
	tools := catalog.CommandTools()
	if len(tools) != 1 || tools[0].Name != "demo_tool" || tools[0].SkillName != "demo" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestCatalogAllowsLooseSkillFrontmatterDescription(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(root, "loose")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	content := "---\nname: loose\ndescription: Use when context includes key: value text that common Markdown frontmatter accepts.\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	catalog, err := Scan([]string{root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	skill, err := catalog.Load("loose")
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	if !strings.Contains(skill.Description, "key: value") {
		t.Fatalf("expected loose description to be preserved, got %q", skill.Description)
	}
}

func TestCatalogParsesFoldedBlockScalarDescription(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(root, "humanizer")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	content := "---\nname: humanizer\ndescription: >-\n  Silent, always-on writing layer that removes the AI-flavored feel\n  in Chinese and English without the user asking.\n---\n\n# Humanizer\nbody\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	catalog, err := Scan([]string{root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	skill, err := catalog.Load("humanizer")
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	want := "Silent, always-on writing layer that removes the AI-flavored feel in Chinese and English without the user asking."
	if skill.Description != want {
		t.Fatalf("folded block scalar description mismatch:\n got %q\nwant %q", skill.Description, want)
	}
}

func TestParseManifestHandlesBlockScalarsAndCRLF(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantName string
		wantDesc string
	}{
		{
			name:     "folded",
			content:  "---\nname: folded\ndescription: >-\n  line one\n  line two\n---\nbody\n",
			wantName: "folded",
			wantDesc: "line one line two",
		},
		{
			name:     "literal",
			content:  "---\nname: literal\ndescription: |-\n  line one\n  line two\n---\nbody\n",
			wantName: "literal",
			wantDesc: "line one\nline two",
		},
		{
			name:     "crlf",
			content:  "---\r\nname: crlfskill\r\ndescription: >-\r\n  windows line one\r\n  windows line two\r\n---\r\nbody\r\n",
			wantName: "crlfskill",
			wantDesc: "windows line one windows line two",
		},
		{
			name:     "bom",
			content:  "\ufeff---\nname: bomskill\ndescription: bom description\n---\nbody\n",
			wantName: "bomskill",
			wantDesc: "bom description",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := ParseManifest([]byte(tc.content))
			if err != nil {
				t.Fatalf("parse manifest: %v", err)
			}
			if meta.Name != tc.wantName {
				t.Fatalf("name mismatch: got %q want %q", meta.Name, tc.wantName)
			}
			if meta.Description != tc.wantDesc {
				t.Fatalf("description mismatch:\n got %q\nwant %q", meta.Description, tc.wantDesc)
			}
		})
	}
}

func TestCatalogReportsSkillFrontmatterPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(root, "bad")
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte("---\n[bad\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	_, err := Scan([]string{root})
	if err == nil || !strings.Contains(err.Error(), skillPath) {
		t.Fatalf("expected skill path in parse error, got %v", err)
	}
}
