//go:build linux

package tools

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func shellSandboxCommand(sandbox, workdir, shellPath, shellArg, command string) (string, []string, string, error) {
	return sandboxCommand(sandbox, workdir, []string{shellPath, shellArg, command})
}

func sandboxCommand(sandbox, workdir string, argv []string) (string, []string, string, error) {
	normalized := strings.ToLower(strings.TrimSpace(sandbox))
	if normalized == "" {
		return argv[0], argv[1:], "off", nil
	}
	if normalized != "bwrap" {
		return "", nil, "unsupported", fmt.Errorf("unsupported shell sandbox: %s", sandbox)
	}
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return "", nil, "bwrap_unavailable", fmt.Errorf("runtime.shell.sandbox=bwrap requested but bwrap is unavailable")
	}
	args := []string{
		"--die-with-parent",
		"--bind", workdir, workdir,
		"--dev", "/dev",
		"--proc", "/proc",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind-try", "/lib", "/lib",
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind-try", "/etc", "/etc",
		"--tmpfs", "/tmp",
		"--chdir", workdir,
		"--ro-bind-try", filepath.Join(workdir, ".git"), filepath.Join(workdir, ".git"),
	}
	args = append(args, argv...)
	return bwrapPath, args, "bwrap", nil
}
