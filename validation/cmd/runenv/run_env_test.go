package runenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func cleanAgentEnv() []string {
	out := []string{}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "AEGIS_AGENT_") || strings.HasPrefix(entry, "OPENAI_API_KEY=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func TestDotEnvIsNeverEvaluatedByLauncher(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	marker := filepath.Join(t.TempDir(), "executed")
	envFile := filepath.Join(t.TempDir(), ".env")
	payload := "OPENAI_API_KEY=$(touch " + marker + ")\nAEGIS_AGENT_BIN=/bin/false\n"
	if err := os.WriteFile(envFile, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join(repoRoot, "run.sh"), "foreground")
	cmd.Env = append(cleanAgentEnv(),
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN=/bin/true",
		"AEGIS_AGENT_ENV_FILE="+envFile,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launcher failed after receiving dotenv data: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("dotenv command substitution executed; stat error=%v", err)
	}
}

func TestEnvFilePathIsPassedToBinaryWithoutImportingItsVariables(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("OPENAI_API_KEY=secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "capture")
	fakeBinary := filepath.Join(t.TempDir(), "fake-aegis-agent")
	script := "#!/bin/sh\nprintf '%s\\n%s\\n' \"$AEGIS_AGENT_ENV_FILE\" \"${OPENAI_API_KEY-}\" > \"$CAPTURE\"\n"
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join(repoRoot, "run.sh"), "foreground")
	cmd.Env = append(cleanAgentEnv(),
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN="+fakeBinary,
		"AEGIS_AGENT_ENV_FILE="+envFile,
		"CAPTURE="+capture,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launcher failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 || lines[0] != envFile || lines[1] != "" {
		t.Fatalf("unexpected child environment: %q", data)
	}
}
