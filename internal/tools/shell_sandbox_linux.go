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

func shellSandboxCommand(sandbox, workdir, bindSource, shellPath string, shellArgs []string, command string) (string, []string, string, error) {
	argv := append([]string{shellPath}, shellArgs...)
	argv = append(argv, command)
	return sandboxCommand(sandbox, workdir, bindSource, argv)
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

// bwrapInspectionSource handles the child-only /proc/self/fd/3 bind source.
// The parent cannot safely infer which numeric parent descriptor os/exec will
// expose as child fd 3 when other goroutines may open descriptors concurrently.
// Instead, conservatively inspect Git metadata reachable through every open
// directory descriptor in this process. That set includes the stable workdir
// descriptor placed in cmd.ExtraFiles. An unrelated unsafe directory can cause
// a fail-closed false positive, but a replaced workdir cannot hide an external
// .git pointer from the preflight.
func bwrapInspectionSource(workdir, bindSource string) (string, error) {
	if filepath.Clean(bindSource) != childBwrapWorkdirFD {
		return bindSource, nil
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return "", fmt.Errorf("inspect stable bwrap workdir descriptors: %w", err)
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
		if err := rejectBwrapExternalGitMetadata(candidate); err != nil {
			return "", fmt.Errorf("stable bwrap directory fd %d: %w", fd, err)
		}
	}
	// Inspect the pathname as well. This catches an unsafe replacement even
	// when no process has opened the replacement directory yet.
	if err := rejectBwrapExternalGitMetadata(workdir); err != nil {
		return "", err
	}
	return workdir, nil
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
