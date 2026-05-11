package isolation

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if isWithin(parentWorkdir, resolvedRoot) {
		return Result{}, fmt.Errorf("isolation root must not be inside source workdir")
	}
	result.RootDir = resolvedRoot
	switch mode {
	case "auto":
		if gitRoot, ok := detectGitRepo(parentWorkdir); ok {
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
	if err := os.MkdirAll(result.RootDir, 0o700); err != nil {
		return Result{}, err
	}
	cmd := exec.Command("git", "-C", gitRoot, "worktree", "add", "--detach", target, "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return Result{}, fmt.Errorf("git worktree add failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	result.Mode = "git"
	result.Workdir = target
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
	if err := os.MkdirAll(dst, 0o700); err != nil {
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
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(mode.Perm())
}
