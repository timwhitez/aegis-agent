//go:build linux

package tools

import (
	"strings"
	"testing"
)

func TestSandboxCommandRejectsUnsupportedLinuxSandbox(t *testing.T) {
	_, _, status, err := sandboxCommand("firejail", t.TempDir(), "", []string{"/bin/sh", "-c", "true"})
	if err == nil || !strings.Contains(err.Error(), "unsupported shell sandbox") {
		t.Fatalf("expected unsupported sandbox error, got status=%q err=%v", status, err)
	}
	if status != "unsupported" {
		t.Fatalf("expected unsupported status, got %q", status)
	}
}
