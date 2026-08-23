package runnetwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runForeground(t *testing.T, repoRoot string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(repoRoot, "run.sh"), "foreground")
	cleanEnv := []string{}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "AEGIS_AGENT_") {
			continue
		}
		cleanEnv = append(cleanEnv, entry)
	}
	cmd.Env = append(cleanEnv,
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN=/bin/true",
	)
	cmd.Env = append(cmd.Env, env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestDefaultListenIsLoopback(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	output, err := runForeground(t, repoRoot)
	if err != nil {
		t.Fatalf("default foreground launch failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "URL: http://127.0.0.1:3940") {
		t.Fatalf("default URL is not loopback-only: %s", output)
	}
}

func TestNonLoopbackRequiresExplicitProcessOptIn(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	output, err := runForeground(t, repoRoot, "AEGIS_AGENT_LISTEN=0.0.0.0:3940")
	if err == nil {
		t.Fatalf("non-loopback launch unexpectedly succeeded: %s", output)
	}
	if !strings.Contains(output, "refusing non-loopback listen address") {
		t.Fatalf("missing fail-closed error: %s", output)
	}
}

func TestHostnamesBeginningWith127AreNotLoopback(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	for _, address := range []string{
		"127.attacker.example:3940",
		"127.0.0.1.attacker.example:3940",
		"127.999.0.1:3940",
		"127.0.0.01:3940",
		"127.000.000.001:3940",
		"localhost.:3940",
	} {
		output, err := runForeground(t, repoRoot, "AEGIS_AGENT_LISTEN="+address)
		if err == nil {
			t.Fatalf("hostile/non-IP address %q unexpectedly bypassed opt-in: %s", address, output)
		}
		if !strings.Contains(output, "refusing non-loopback listen address") {
			t.Fatalf("address %q missing fail-closed diagnostic: %s", address, output)
		}
	}
}

func TestCanonicalLoopbackAddressesRemainAllowed(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	for _, address := range []string{
		"127.0.0.1:3940",
		"127.255.255.254:3940",
		"localhost:3940",
		"LOCALHOST:3940",
		"[::1]:3940",
	} {
		output, err := runForeground(t, repoRoot, "AEGIS_AGENT_LISTEN="+address)
		if err != nil {
			t.Fatalf("loopback address %q was rejected: %v\n%s", address, err, output)
		}
		if strings.Contains(output, "WARNING: web console is reachable") {
			t.Fatalf("loopback address %q emitted a LAN warning: %s", address, output)
		}
	}
}

func TestExplicitNetworkOptInAllowsConfiguredAddress(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	output, err := runForeground(t, repoRoot,
		"AEGIS_AGENT_LISTEN=0.0.0.0:3940",
		"AEGIS_AGENT_ALLOW_NETWORK=1",
	)
	if err != nil {
		t.Fatalf("explicitly opted-in launch failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "WARNING: web console is reachable") {
		t.Fatalf("missing non-loopback warning: %s", output)
	}
}

func TestDotEnvCannotGrantNetworkOptIn(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("AEGIS_AGENT_LISTEN=0.0.0.0:3940\nAEGIS_AGENT_ALLOW_NETWORK=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runForeground(t, repoRoot, "AEGIS_AGENT_ENV_FILE="+envFile)
	if err != nil {
		t.Fatalf("launcher should ignore project-controlled listen/opt-in values: %v\n%s", err, output)
	}
	if !strings.Contains(output, "URL: http://127.0.0.1:3940") {
		t.Fatalf("dotenv changed the listen policy: %s", output)
	}
}
