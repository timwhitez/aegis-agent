//go:build linux

package tools

import (
	"path/filepath"
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

func TestBwrapWorkspaceTargetArgsCreateNamespacePathAfterBaseMounts(t *testing.T) {
	installFakeBwrap(t)
	workdir := filepath.Join("/tmp", "aegis-target", "workspace")
	_, args, status, err := sandboxCommand("bwrap", workdir, workdir, []string{"/bin/true"})
	if err != nil || status != "bwrap" {
		t.Fatalf("sandbox command: status=%q err=%v", status, err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--tmpfs /tmp",
		"--dir /tmp/aegis-target",
		"--dir /tmp/aegis-target/workspace",
		"--bind " + workdir + " " + workdir,
		"--chdir " + workdir,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in bwrap args: %q", want, joined)
		}
	}
	indices := []int{
		strings.Index(joined, "--tmpfs /tmp"),
		strings.Index(joined, "--dir /tmp/aegis-target"),
		strings.Index(joined, "--bind "+workdir+" "+workdir),
		strings.Index(joined, "--chdir "+workdir),
	}
	for index := 1; index < len(indices); index++ {
		if indices[index-1] < 0 || indices[index] <= indices[index-1] {
			t.Fatalf("unsafe bwrap mount ordering %v: %q", indices, joined)
		}
	}
	if strings.Contains(joined, "--bind /tmp /tmp") || strings.Contains(joined, "--bind /tmp/aegis-target /tmp/aegis-target") {
		t.Fatalf("bwrap exposed a workspace parent: %q", joined)
	}
}

func TestBwrapWorkspaceTargetArgsReuseReadonlySystemTarget(t *testing.T) {
	args, err := bwrapWorkspaceTargetArgs("/usr/local/aegis-agent")
	if err != nil || len(args) != 0 {
		t.Fatalf("system-mounted target should already exist: args=%#v err=%v", args, err)
	}
}

func TestBwrapWorkspaceTargetArgsRejectRootAndProc(t *testing.T) {
	for _, workdir := range []string{"/", "/proc/aegis-agent", "relative/workspace"} {
		if _, err := bwrapWorkspaceTargetArgs(workdir); err == nil {
			t.Fatalf("unsafe bwrap target %q was accepted", workdir)
		}
	}
}
