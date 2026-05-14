//go:build !linux

package tools

import (
	"fmt"
	"strings"
)

func shellSandboxCommand(sandbox, workdir, shellPath, shellArg, command string) (string, []string, string, error) {
	return sandboxCommand(sandbox, workdir, []string{shellPath, shellArg, command})
}

func sandboxCommand(sandbox, workdir string, argv []string) (string, []string, string, error) {
	normalized := strings.ToLower(strings.TrimSpace(sandbox))
	if normalized == "bwrap" {
		return "", nil, "bwrap_unsupported", fmt.Errorf("runtime.shell.sandbox=bwrap is only supported on linux")
	}
	if normalized != "" {
		return "", nil, "unsupported", fmt.Errorf("unsupported shell sandbox: %s", sandbox)
	}
	return argv[0], argv[1:], "off", nil
}
