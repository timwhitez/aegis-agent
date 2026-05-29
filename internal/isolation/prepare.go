package isolation

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go-cli-agent/internal/fileutil"
)

type Request struct {
	SessionID     string
	ParentWorkdir string
	RequestedMode string
	RootDir       string
}

type Result struct {
	Mode          string
	RequestedMode string
	ParentWorkdir string
	Workdir       string
	RootDir       string
	GitRepoRoot   string
}

var (
	beforeCopyFileTempCreate func(parent string) error
	beforeCopyFileRename     func(tmpPath, dst string) error
	beforeCopyFileSourceOpen func(src string) error
)

func Prepare(req Request) (Result, error) {
	parentWorkdir, err := filepath.Abs(req.ParentWorkdir)
	if err != nil {
		return Result{}, err
	}
	parentWorkdir, err = filepath.EvalSymlinks(parentWorkdir)
	if err != nil {
		return Result{}, err
	}
	mode := strings.TrimSpace(req.RequestedMode)
	if mode == "" {
		mode = "off"
	}
	result := Result{
		RequestedMode: mode,
		ParentWorkdir: parentWorkdir,
	}
	if mode == "off" {
		result.Mode = "off"
		result.Workdir = parentWorkdir
		return result, nil
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return Result{}, fmt.Errorf("session id is required for isolation mode %s", mode)
	}
	rootDir, err := filepath.Abs(req.RootDir)
	if err != nil {
		return Result{}, err
	}
	target := filepath.Join(rootDir, req.SessionID)
	resolvedTarget, err := resolveWithExistingParent(target)
	if err != nil {
		return Result{}, err
	}
	if isWithin(parentWorkdir, resolvedTarget) {
		return Result{}, fmt.Errorf("isolation target must not be inside source workdir")
	}
	resolvedRoot, err := resolveWithExistingParent(rootDir)
	if err != nil {
		return Result{}, err
	}
	if !isWithin(resolvedRoot, resolvedTarget) {
		return Result{}, fmt.Errorf("isolation target escapes root: %s", target)
	}
	if isWithin(parentWorkdir, resolvedRoot) {
		return Result{}, fmt.Errorf("isolation root must not be inside source workdir")
	}
	result.RootDir = resolvedRoot
	switch mode {
	case "auto":
		if gitRoot, ok := detectGitRepo(parentWorkdir); ok && gitWorktreeCanRepresent(parentWorkdir, gitRoot) {
			return prepareGitWorktree(result, gitRoot, resolvedTarget)
		}
		return prepareCopy(result, resolvedTarget)
	case "git":
		gitRoot, ok := detectGitRepo(parentWorkdir)
		if !ok {
			return Result{}, fmt.Errorf("git isolation requested but %s is not inside a git repository", parentWorkdir)
		}
		return prepareGitWorktree(result, gitRoot, resolvedTarget)
	case "copy":
		return prepareCopy(result, resolvedTarget)
	default:
		return Result{}, fmt.Errorf("unsupported isolation mode: %s", mode)
	}
}

func resolveWithExistingParent(path string) (string, error) {
	path = filepath.Clean(path)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() || info.Mode().IsRegular() {
			return filepath.EvalSymlinks(path)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	var suffix []string
	current := path
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("unable to resolve path: %s", path)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func prepareGitWorktree(result Result, gitRoot, target string) (Result, error) {
	if err := mkdirAllNoSymlink(result.RootDir, 0o700); err != nil {
		return Result{}, err
	}
	cmd := exec.Command("git", "-C", gitRoot, "worktree", "add", "--detach", target, "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return Result{}, fmt.Errorf("git worktree add failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	workdir := target
	if rel, err := filepath.Rel(gitRoot, result.ParentWorkdir); err == nil && rel != "." {
		workdir = filepath.Join(target, rel)
	}
	if _, err := os.Stat(workdir); err != nil {
		return Result{}, fmt.Errorf("git worktree does not contain requested workdir %s: %w", result.ParentWorkdir, err)
	}
	result.Mode = "git"
	result.Workdir = workdir
	result.GitRepoRoot = gitRoot
	return result, nil
}

func prepareCopy(result Result, target string) (Result, error) {
	if err := copyTree(result.ParentWorkdir, target); err != nil {
		return Result{}, err
	}
	result.Mode = "copy"
	result.Workdir = target
	return result, nil
}

func detectGitRepo(workdir string) (string, bool) {
	cmd := exec.Command("git", "-C", workdir, "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(output)), true
}

func gitWorktreeCanRepresent(workdir, gitRoot string) bool {
	rel, err := filepath.Rel(gitRoot, workdir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	if rel == "." {
		return true
	}
	output, err := exec.Command("git", "-C", gitRoot, "ls-files", "--", rel).Output()
	return err == nil && strings.TrimSpace(string(output)) != ""
}

func isWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..")
}

func copyTree(src, dst string) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	if err := mkdirAllNoSymlink(dst, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := mkdirAllNoSymlink(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return mkdirAllNoSymlink(target, info.Mode().Perm())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	parent := filepath.Dir(dst)
	if err := mkdirAllNoSymlink(parent, 0o700); err != nil {
		return err
	}
	if err := rejectSymlinkOrDirectory(dst); err != nil {
		return err
	}
	if beforeCopyFileSourceOpen != nil {
		if err := beforeCopyFileSourceOpen(src); err != nil {
			return err
		}
	}
	in, err := fileutil.OpenFileNoSymlink(src, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	srcInfo, err := in.Stat()
	if err != nil {
		return err
	}
	if !srcInfo.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %s", src)
	}

	if beforeCopyFileTempCreate != nil {
		if err := beforeCopyFileTempCreate(parent); err != nil {
			return err
		}
	}
	tmp, err := fileutil.CreateTempNoSymlink(parent, "."+filepath.Base(dst)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer fileutil.RemoveFileNoSymlink(tmpPath)

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := rejectSymlinkOrDirectory(dst); err != nil {
		return err
	}
	if beforeCopyFileRename != nil {
		if err := beforeCopyFileRename(tmpPath, dst); err != nil {
			return err
		}
	}
	if err := fileutil.RenamePathNoSymlink(tmpPath, dst); err != nil {
		return err
	}
	return nil
}

func mkdirAllNoSymlink(path string, mode fs.FileMode) error {
	return fileutil.MkdirAllNoSymlink(path, os.FileMode(mode))
}

func rejectSymlinkOrDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to overwrite symlink target: %s", path)
	}
	if info.IsDir() {
		return fmt.Errorf("refusing to overwrite directory: %s", path)
	}
	return nil
}
