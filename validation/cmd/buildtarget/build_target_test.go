package buildtarget

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildScriptRejectsUnsupportedGOOSBeforeBuild(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "go.log")
	fakeGo := filepath.Join(fakeBin, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_GO_LOG\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join(repoRoot, "build.sh"))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_GO_LOG="+logPath,
		"AEGIS_AGENT_GOOS=windows",
		"AEGIS_AGENT_GOARCH=amd64",
		"AEGIS_AGENT_BUILD_OUT="+filepath.Join(t.TempDir(), "aegis-agent.exe"),
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected unsupported Windows build to fail, output: %s", output)
	}
	if !strings.Contains(string(output), "unsupported target GOOS=windows") {
		t.Fatalf("missing explicit unsupported-target error: %s", output)
	}
	if data, readErr := os.ReadFile(logPath); readErr == nil && len(data) > 0 {
		t.Fatalf("go was invoked for a rejected target: %s", data)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
}

func TestBuildScriptAllowsSupportedTarget(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "go.log")
	fakeGo := filepath.Join(fakeBin, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_GO_LOG\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join(repoRoot, "build.sh"))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_GO_LOG="+logPath,
		"AEGIS_AGENT_GOOS=linux",
		"AEGIS_AGENT_GOARCH=amd64",
		"AEGIS_AGENT_BUILD_OUT="+filepath.Join(t.TempDir(), "aegis-agent"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("supported Linux build was rejected: %v\n%s", err, output)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "build -trimpath") {
		t.Fatalf("expected go build invocation, got %s", data)
	}
}
