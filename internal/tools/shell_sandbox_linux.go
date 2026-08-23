//go:build linux

package tools

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxBwrapGitPointerBytes = 4096
	childBwrapWorkdirFD     = "/proc/self/fd/3"
)

func shellSandboxCommand(sandbox, workdir, bindSource, shellPath, shellArg, command string) (string, []string, string, error) {
	return sandboxCommand(sandbox, workdir, bindSource, []string{shellPath, shellArg, command})
}

func sandboxCommand(sandbox, workdir, bindSource string, argv []string) (string, []string, string, error) {
	normalized := strings.ToLower(strings.TrimSpace(sandbox))
	if normalized == "" {
		return argv[0], argv[1:], "off", nil
	}
	if normalized != "bwrap" {
		return "", nil, "unsupported", fmt.Errorf("unsupported shell sandbox: %s", sandbox)
	}
	if strings.TrimSpace(bindSource) == "" {
		bindSource = workdir
	}
	inspectionSource, err := bwrapInspectionSource(workdir, bindSource)
	if err != nil {
		return "", nil, "bwrap_external_git_metadata", err
	}
	if err := rejectBwrapExternalGitMetadata(inspectionSource); err != nil {
		return "", nil, "bwrap_external_git_metadata", err
	}
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return "", nil, "bwrap_unavailable", fmt.Errorf("runtime.shell.sandbox=bwrap requested but bwrap is unavailable")
	}
	args := []string{
		"--die-with-parent",
		"--bind", bindSource, workdir,
		"--dev", "/dev",
		"--proc", "/proc",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind-try", "/lib", "/lib",
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind-try", "/etc", "/etc",
		"--tmpfs", "/tmp",
		"--chdir", workdir,
		"--ro-bind-try", filepath.Join(bindSource, ".git"), filepath.Join(workdir, ".git"),
	}
	args = append(args, argv...)
	return bwrapPath, args, "bwrap", nil
}

// bwrapInspectionSource translates the child-only /proc/self/fd/3 bind source
// back to a parent-visible descriptor for the exact directory that will be
// inherited through cmd.ExtraFiles. Inspecting the literal child path in the
// parent can examine an unrelated fd 3; inspecting the pathname can race with a
// workspace replacement. Enumerating open descriptors and matching inode
// identity preserves the stable-directory guarantee used by the shell runner.
func bwrapInspectionSource(workdir, bindSource string) (string, error) {
	if filepath.Clean(bindSource) != childBwrapWorkdirFD {
		return bindSource, nil
	}
	targetInfo, err := os.Stat(workdir)
	if err != nil {
		return "", fmt.Errorf("inspect bwrap workdir %s: %w", workdir, err)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return "", fmt.Errorf("locate stable bwrap workdir descriptor: %w", err)
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd <= 2 {
			continue
		}
		candidate := filepath.Join("/proc/self/fd", entry.Name())
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		if os.SameFile(targetInfo, info) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("runtime.shell.sandbox=bwrap cannot locate the stable descriptor for workdir %s; refusing to inspect a different path", workdir)
}

func rejectBwrapExternalGitMetadata(bindSource string) error {
	gitPath := filepath.Join(bindSource, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect bwrap Git metadata %s: %w", gitPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime.shell.sandbox=bwrap refuses symlinked Git metadata at %s", gitPath)
	}
	if info.IsDir() {
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("runtime.shell.sandbox=bwrap refuses unsupported Git metadata at %s", gitPath)
	}

	file, err := os.Open(gitPath)
	if err != nil {
		return fmt.Errorf("read bwrap Git metadata %s: %w", gitPath, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBwrapGitPointerBytes+1))
	if err != nil {
		return fmt.Errorf("read bwrap Git metadata %s: %w", gitPath, err)
	}
	if len(data) > maxBwrapGitPointerBytes {
		return fmt.Errorf("runtime.shell.sandbox=bwrap refuses oversized Git metadata pointer at %s", gitPath)
	}
	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	if strings.HasPrefix(strings.ToLower(line), "gitdir:") {
		return fmt.Errorf("runtime.shell.sandbox=bwrap cannot safely expose linked-worktree or submodule Git metadata from %s; use copy isolation or disable bwrap", gitPath)
	}
	return fmt.Errorf("runtime.shell.sandbox=bwrap refuses unsupported regular Git metadata at %s", gitPath)
}
