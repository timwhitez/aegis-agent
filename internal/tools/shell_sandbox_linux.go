//go:build linux

package tools

import (
	"os/exec"
	"strings"
)

func shellSandboxCommand(sandbox, workdir, shellPath, shellArg, command string) (string, []string, string) {
	if strings.ToLower(strings.TrimSpace(sandbox)) != "bwrap" {
		return shellPath, []string{shellArg, command}, "off"
	}
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return shellPath, []string{shellArg, command}, "bwrap_unavailable"
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
		shellPath, shellArg, command,
	}
	return bwrapPath, args, "bwrap"
}
