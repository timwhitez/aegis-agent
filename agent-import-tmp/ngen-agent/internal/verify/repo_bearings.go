package verify

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ngen/internal/task"
)

const repoProbeTimeout = 2 * time.Second

func CaptureWorkspaceSnapshot(ctx context.Context, workspaceRoot string) *task.WorkspaceSnapshot {
	git := captureGitSummary(ctx, workspaceRoot)
	if git == nil {
		return nil
	}
	return &task.WorkspaceSnapshot{Git: git}
}

func captureGitSummary(ctx context.Context, workspaceRoot string) *task.GitSummary {
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, repoProbeTimeout)
	defer cancel()
	if out, err := gitOutput(probeCtx, workspaceRoot, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return nil
	}
	summary := &task.GitSummary{IsRepository: true}
	if branch, err := gitOutput(probeCtx, workspaceRoot, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		summary.Branch = strings.TrimSpace(branch)
	}
	if head, err := gitOutput(probeCtx, workspaceRoot, "rev-parse", "--short", "HEAD"); err == nil {
		summary.Head = strings.TrimSpace(head)
	}
	if status, err := gitOutput(probeCtx, workspaceRoot, "status", "--short", "--", "."); err == nil {
		lines := nonEmptyRawLines(status)
		summary.Dirty = len(lines) > 0
		summary.ChangedPaths = gitChangedPaths(lines, 5)
		if summary.Dirty {
			summary.StatusSummary = "dirty working tree"
		} else {
			summary.StatusSummary = "clean working tree"
		}
	}
	if logText, err := gitOutput(probeCtx, workspaceRoot, "log", "--oneline", "-3", "--", "."); err == nil {
		summary.RecentCommits = gitRecentCommits(logText)
	}
	return summary
}

func gitOutput(ctx context.Context, workspaceRoot string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspaceRoot
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func compactLines(text string) []string {
	raw := strings.Split(text, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func nonEmptyRawLines(text string) []string {
	raw := strings.Split(text, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, strings.TrimRight(line, "\r"))
	}
	return lines
}

func gitChangedPaths(lines []string, limit int) []string {
	seen := make(map[string]struct{}, len(lines))
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		path := gitStatusPath(line)
		if path == "" {
			continue
		}
		path = filepath.ToSlash(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if limit > 0 && len(paths) > limit {
		return paths[:limit]
	}
	return paths
}

func gitStatusPath(line string) string {
	if len(line) < 4 {
		return ""
	}
	path := strings.TrimSpace(line[3:])
	if path == "" {
		return ""
	}
	if idx := strings.LastIndex(path, " -> "); idx >= 0 {
		path = strings.TrimSpace(path[idx+4:])
	}
	return path
}

func gitRecentCommits(text string) []task.GitCommit {
	lines := compactLines(text)
	commits := make([]task.GitCommit, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		sha := fields[0]
		subject := strings.TrimSpace(strings.TrimPrefix(line, sha))
		commits = append(commits, task.GitCommit{
			SHA:     sha,
			Subject: subject,
		})
	}
	return commits
}

func (p *Pipeline) baselineCommandHints(spec task.Spec) []task.CommandHint {
	var hints []task.CommandHint
	addHint := func(kind string, command []string, reason, sourceRef string) {
		command = compactStrings(command)
		reason = strings.TrimSpace(reason)
		if len(command) == 0 || reason == "" {
			return
		}
		hints = append(hints, task.CommandHint{
			Kind:      strings.TrimSpace(kind),
			Command:   append([]string(nil), command...),
			Reason:    reason,
			SourceRef: strings.TrimSpace(sourceRef),
		})
	}
	for _, candidate := range []struct {
		rel     string
		command []string
		reason  string
	}{
		{rel: "init.sh", command: []string{"bash", "./init.sh"}, reason: "Workspace exposes an init bootstrap command."},
		{rel: "scripts/init.sh", command: []string{"bash", "./scripts/init.sh"}, reason: "Workspace exposes an init bootstrap command under scripts/."},
	} {
		full := filepath.Join(spec.WorkspaceRoot, filepath.FromSlash(candidate.rel))
		if _, err := os.Stat(full); err == nil {
			addHint("setup", candidate.command, candidate.reason, "workspace:"+candidate.rel)
		}
	}
	if spec.Kind == task.KindCoding {
		sourceRef := verifierCommandHintSource(p.Config, spec)
		for _, command := range p.codingVerifierCommands(spec) {
			addHint("verify", command, "Repo-owned verifier command for this task.", sourceRef)
		}
	}
	return dedupeCommandHints(hints)
}

func verifierCommandHintSource(cfg task.Config, spec task.Spec) string {
	switch {
	case len(cfg.Verification.CodingCommands) > 0:
		return "workspace:ngen.json"
	case len(cfg.Verification.CodingGoTestCommand) > 0 && !sameStrings(cfg.Verification.CodingGoTestCommand, task.DefaultConfig().Verification.CodingGoTestCommand):
		return "workspace:ngen.json"
	case len(verifierCommandsFromCriteria(spec)) > 0:
		return "task.json"
	default:
		return "task.json"
	}
}

func compactStrings(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func dedupeCommandHints(items []task.CommandHint) []task.CommandHint {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]task.CommandHint, 0, len(items))
	for _, item := range items {
		key := item.Kind + "\x00" + strings.Join(item.Command, "\x00") + "\x00" + item.Reason + "\x00" + item.SourceRef
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}
