package app

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
)

func writeWebCommandConfig(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	t.Setenv("AEGIS_AGENT_ENV_FILE", filepath.Join(t.TempDir(), "test.env"))
	cfg := config.Default()
	if mutate != nil {
		mutate(cfg)
	}
	data, err := config.MarshalYAML(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe free port: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("close probe: %v", err)
	}
	return addr
}

func TestWebCommandBindFailureSkipsReadinessAuditAndWorkers(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()
	sessionRoot := filepath.Join(t.TempDir(), "sessions")
	configPath := writeWebCommandConfig(t, func(cfg *config.Config) {
		cfg.Session.Dir = sessionRoot
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err = Run(ctx, []string{"web", "--listen", occupied.Addr().String(), "--workers", "0", "--config", configPath}, stdout, stderr)
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("expected listen failure, got %v", err)
	}
	if strings.Contains(stdout.String(), "web console listening on") {
		t.Fatalf("failed bind must not publish readiness, got %q", stdout.String())
	}
	if _, err := os.Stat(sessionRoot); !os.IsNotExist(err) {
		t.Fatalf("failed bind must not initialize session root or audit log, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionRoot, "webconsole-audit.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("failed bind must not create the audit log, got %v", err)
	}
}

func TestWebCommandPublishesActualPortAndServesIt(t *testing.T) {
	sessionRoot := filepath.Join(t.TempDir(), "sessions")
	configPath := writeWebCommandConfig(t, func(cfg *config.Config) {
		cfg.Session.Dir = sessionRoot
	})

	stdoutReader, stdoutWriter := ioPipe()
	stderr := &bytes.Buffer{}
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		errCh <- Run(ctx, []string{"web", "--listen", "127.0.0.1:0", "--workers", "0", "--config", configPath}, stdoutWriter, stderr)
	}()

	listening := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutReader)
		for scanner.Scan() {
			line := scanner.Text()
			if addr, ok := strings.CutPrefix(line, "web console listening on http://"); ok {
				listening <- addr
				return
			}
		}
		close(listening)
	}()

	var addr string
	select {
	case a, ok := <-listening:
		if !ok {
			t.Fatalf("no readiness marker produced, stderr=%s", stderr.String())
		}
		addr = a
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for readiness, stderr=%s", stderr.String())
	}
	// The OS-assigned port must be a real one, not the requested zero.
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" || port == "0" {
		t.Fatalf("readiness must report the OS-assigned port, got %q", addr)
	}

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("published address must accept connections: %v", err)
	}
	_ = conn.Close()

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("clean shutdown must succeed, got %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for web command shutdown")
	}
}

func TestWebCommandAuditInitFailureReleasesListenerWithoutReadiness(t *testing.T) {
	// A regular file where the session root's parent directory belongs makes
	// audit preparation fail after the listener is already bound.
	imposter := filepath.Join(t.TempDir(), "imposter")
	if err := os.WriteFile(imposter, []byte("occupied\n"), 0o600); err != nil {
		t.Fatalf("write imposter: %v", err)
	}
	addr := freeLoopbackPort(t)
	configPath := writeWebCommandConfig(t, func(cfg *config.Config) {
		cfg.Session.Dir = filepath.Join(imposter, "sessions")
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := Run(ctx, []string{"web", "--listen", addr, "--workers", "0", "--config", configPath}, stdout, stderr)
	if err == nil || !strings.Contains(err.Error(), "initialize web audit log") {
		t.Fatalf("expected audit initialization failure, got %v", err)
	}
	if strings.Contains(stdout.String(), "web console listening on") {
		t.Fatalf("audit init failure must not publish readiness, got %q", stdout.String())
	}
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("failed initialization must release the listener, got %v", err)
	}
	_ = rebound.Close()
}

func TestWebCommandServiceInitFailureReleasesListenerWithoutReadiness(t *testing.T) {
	addr := freeLoopbackPort(t)
	sessionRoot := filepath.Join(t.TempDir(), "sessions")
	// Half-configured basic auth makes webconsole.New fail after audit
	// preparation succeeded, exercising the last pre-serve failure branch.
	configPath := writeWebCommandConfig(t, func(cfg *config.Config) {
		cfg.Session.Dir = sessionRoot
		cfg.Web.BasicAuth.Username = "operator"
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := Run(ctx, []string{"web", "--listen", addr, "--workers", "0", "--config", configPath}, stdout, stderr)
	if err == nil || !strings.Contains(err.Error(), "web.basic_auth") {
		t.Fatalf("expected service construction failure, got %v", err)
	}
	if strings.Contains(stdout.String(), "web console listening on") {
		t.Fatalf("service init failure must not publish readiness, got %q", stdout.String())
	}
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("failed initialization must release the listener, got %v", err)
	}
	_ = rebound.Close()
}

func ioPipe() (*io.PipeReader, *io.PipeWriter) {
	return io.Pipe()
}
