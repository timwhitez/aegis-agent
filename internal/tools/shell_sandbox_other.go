//go:build !linux

package tools

func shellSandboxCommand(sandbox, workdir, shellPath, shellArg, command string) (string, []string, string) {
	if sandbox == "bwrap" {
		return shellPath, []string{shellArg, command}, "bwrap_unsupported"
	}
	return shellPath, []string{shellArg, command}, "off"
}
