package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"ngen/internal/provider"
	"ngen/internal/task"
)

type Pipeline struct {
	Config task.Config
}

type rootScope struct {
	ID   string
	Path string
}

func New(config task.Config) *Pipeline {
	return &Pipeline{Config: config}
}

func (p *Pipeline) CaptureBaseline(ctx context.Context, spec task.Spec) task.Baseline {
	verifiers := []string{"baseline"}
	switch spec.Kind {
	case task.KindCoding:
		verifiers = append(verifiers, "go_test")
	case task.KindReviewer:
		verifiers = append(verifiers, "reviewer_go_test", "reviewer_docs_review")
	case task.KindGeneral:
		verifiers = append(verifiers, "docs_structure")
	case task.KindSecurityReview:
		verifiers = append(verifiers, "security_inventory", "security_secret_indicators", "security_entrypoints")
	}
	var refs []string
	for _, root := range p.candidateRoots(spec.WorkspaceRoot) {
		for _, candidate := range []string{"README.md", "go.mod", "docs/00-repo-status.md", "ngen.json"} {
			full := filepath.Join(root.Path, filepath.FromSlash(candidate))
			if _, err := os.Stat(full); err == nil {
				if ref, ok := p.rootRef(root, full); ok {
					refs = append(refs, ref)
				}
			}
		}
	}
	sort.Strings(verifiers)
	return task.Baseline{
		SchemaVersion:     task.SchemaVersion,
		TaskID:            spec.TaskID,
		CapturedAt:        task.Now(),
		WorkspaceRoot:     spec.WorkspaceRoot,
		RepoTruthRefs:     uniqueStrings(refs),
		CommandHints:      p.baselineCommandHints(spec),
		WorkspaceSnapshot: CaptureWorkspaceSnapshot(ctx, spec.WorkspaceRoot),
		Environment: task.EnvInfo{
			OS:        goruntime.GOOS,
			GoVersion: goruntime.Version(),
		},
		AvailableVerifiers:   verifiers,
		MissingPrerequisites: p.missingPrerequisites(),
	}
}

func (p *Pipeline) Run(ctx context.Context, spec task.Spec) task.VerificationReport {
	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		ReportID:      task.NewID("VER"),
		Status:        "passed",
		Profile:       string(spec.Kind),
		RanAt:         task.Now(),
	}

	switch spec.Kind {
	case task.KindCoding:
		report.Checks = append(report.Checks, p.runCoding(ctx, spec)...)
	case task.KindReviewer:
		report.Checks = append(report.Checks, p.runReviewer(ctx, spec)...)
	case task.KindGeneral:
		report.Checks = append(report.Checks, p.runDocsLite(spec))
	case task.KindSecurityReview:
		report.Checks = append(report.Checks, p.runSecurityReview(spec)...)
	default:
		report.Status = "failed"
		report.FailureSummary = fmt.Sprintf("unsupported kind: %s", spec.Kind)
		report.Checks = append(report.Checks, task.VerificationCheck{
			Name:    "unsupported_kind",
			Status:  "failed",
			Summary: report.FailureSummary,
		})
	}

	for _, check := range report.Checks {
		if check.Status != "passed" {
			report.Status = "failed"
			if report.FailureSummary == "" {
				report.FailureSummary = check.Summary
			}
			break
		}
	}
	return report
}

func (p *Pipeline) runCoding(ctx context.Context, spec task.Spec) []task.VerificationCheck {
	commands := p.codingVerifierCommands(spec)
	defaultArgs := task.DefaultConfig().Verification.CodingGoTestCommand
	checks := make([]task.VerificationCheck, 0, len(commands))
	for i, cmdArgs := range commands {
		displayCommand := strings.Join(cmdArgs, " ")
		verifyCtx := ctx
		cancel := func() {}
		if timeout := p.codingTimeout(); timeout > 0 {
			verifyCtx, cancel = context.WithTimeout(ctx, timeout)
		}

		cmd := exec.CommandContext(verifyCtx, cmdArgs[0], cmdArgs[1:]...)
		cmd.Dir = spec.WorkspaceRoot
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		cancel()

		suppressDefaultPrefix := len(commands) == 1 && sameStrings(cmdArgs, defaultArgs)
		check := task.VerificationCheck{
			Name:    codingVerifierCheckName(cmdArgs, i, suppressDefaultPrefix),
			Command: append([]string(nil), cmdArgs...),
		}
		out := summarizeCommandOutput(stdout.String(), stderr.String())
		if errors.Is(verifyCtx.Err(), context.DeadlineExceeded) {
			if out == "" {
				out = fmt.Sprintf("%s timed out after %s", strings.Join(cmdArgs, " "), p.codingTimeout())
			} else {
				out = fmt.Sprintf("%s (timed out after %s)", out, p.codingTimeout())
			}
			check.Status = "failed"
			check.Summary = prefixVerifierCommand(out, displayCommand, suppressDefaultPrefix)
			checks = append(checks, check)
			return checks
		}
		if err != nil {
			if out == "" {
				out = err.Error()
			}
			check.Status = "failed"
			check.Summary = prefixVerifierCommand(out, displayCommand, suppressDefaultPrefix)
			checks = append(checks, check)
			return checks
		}
		if out == "" {
			out = displayCommand + " passed"
		} else {
			out = prefixVerifierCommand(out, displayCommand, suppressDefaultPrefix)
		}
		check.Status = "passed"
		check.Summary = out
		checks = append(checks, check)
	}
	return checks
}

func (p *Pipeline) codingVerifierCommands(spec task.Spec) [][]string {
	if len(p.Config.Verification.CodingCommands) > 0 {
		return cloneCommandMatrix(p.Config.Verification.CodingCommands)
	}
	cmdArgs := append([]string(nil), p.Config.Verification.CodingGoTestCommand...)
	defaultArgs := task.DefaultConfig().Verification.CodingGoTestCommand
	if len(cmdArgs) == 0 {
		cmdArgs = append([]string(nil), defaultArgs...)
	}
	if !sameStrings(cmdArgs, defaultArgs) {
		return [][]string{cmdArgs}
	}
	if derived := verifierCommandsFromCriteria(spec); len(derived) > 0 {
		return derived
	}
	return [][]string{cmdArgs}
}

func verifierCommandsFromCriteria(spec task.Spec) [][]string {
	var commands [][]string
	seen := make(map[string]struct{})
	for _, criterion := range spec.SuccessCriteria {
		for _, command := range VerifierCommandsFromStatement(spec.WorkspaceRoot, criterion.Statement) {
			key := strings.Join(command, "\x00")
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			commands = append(commands, command)
		}
	}
	return commands
}

func VerifierCommandsFromStatement(workspaceRoot, statement string) [][]string {
	lower := strings.ToLower(statement)
	if !strings.Contains(lower, "pass") && !strings.Contains(lower, "succeed") && !strings.Contains(lower, "exit code 0") && !strings.Contains(lower, "exits 0") {
		return nil
	}
	var commands [][]string
	for _, literal := range backtickedLiterals(statement) {
		args := strings.Fields(strings.TrimSpace(literal))
		if len(args) == 0 || !looksLikeVerifierCommand(workspaceRoot, args) {
			continue
		}
		commands = append(commands, args)
	}
	return commands
}

func backtickedLiterals(statement string) []string {
	var literals []string
	for {
		start := strings.IndexByte(statement, '`')
		if start < 0 {
			return literals
		}
		statement = statement[start+1:]
		end := strings.IndexByte(statement, '`')
		if end < 0 {
			return literals
		}
		literals = append(literals, statement[:end])
		statement = statement[end+1:]
	}
}

func looksLikeVerifierCommand(workspaceRoot string, args []string) bool {
	if len(args) == 0 {
		return false
	}
	token := args[0]
	if token == "" || filepath.IsAbs(token) {
		return false
	}
	if strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") {
		full := filepath.Clean(filepath.Join(workspaceRoot, filepath.FromSlash(token)))
		if !strings.HasPrefix(full, workspaceRoot+string(os.PathSeparator)) {
			return false
		}
		info, err := os.Stat(full)
		return err == nil && !info.IsDir()
	}
	if token == "multica" {
		return isReadOnlyMulticaIssueCommand(args)
	}
	switch token {
	case "go", "bash", "sh", "make", "just", "npm", "pnpm", "yarn", "bun", "pytest", "cargo", "python", "python3":
		return true
	default:
		return false
	}
}

func isReadOnlyMulticaIssueCommand(args []string) bool {
	if len(args) < 3 || args[0] != "multica" || args[1] != "issue" {
		return false
	}
	switch args[2] {
	case "get", "list":
		return true
	case "comment":
		return len(args) >= 4 && args[3] == "list"
	default:
		return false
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func cloneCommandMatrix(commands [][]string) [][]string {
	if len(commands) == 0 {
		return nil
	}
	cloned := make([][]string, 0, len(commands))
	for _, command := range commands {
		cloned = append(cloned, append([]string(nil), command...))
	}
	return cloned
}

func codingVerifierCheckName(command []string, index int, preserveLegacyGoTestName bool) string {
	if preserveLegacyGoTestName {
		return "go_test"
	}
	if len(command) >= 2 && command[0] == "go" && command[1] == "test" {
		return fmt.Sprintf("go_test_%02d", index+1)
	}
	return fmt.Sprintf("verifier_command_%02d", index+1)
}

func prefixVerifierCommand(summary, command string, defaultCommand bool) string {
	if summary == "" || command == "" || defaultCommand || strings.Contains(summary, command) {
		return summary
	}
	return command + "\n" + summary
}

func (p *Pipeline) codingTimeout() time.Duration {
	timeout := time.Duration(p.Config.Verification.CodingTimeoutSeconds) * time.Second
	if timeout <= 0 {
		return 0
	}
	return timeout
}

func (p *Pipeline) runReviewer(ctx context.Context, spec task.Spec) []task.VerificationCheck {
	var checks []task.VerificationCheck
	if _, err := os.Stat(filepath.Join(spec.WorkspaceRoot, "go.mod")); err == nil {
		codingChecks := p.runCoding(ctx, spec)
		for i := range codingChecks {
			if len(codingChecks) == 1 {
				codingChecks[i].Name = "reviewer_go_test"
			} else {
				codingChecks[i].Name = "reviewer_" + codingChecks[i].Name
			}
			checks = append(checks, codingChecks[i])
		}
	}
	docsCheck := p.runDocsLite(spec)
	if docsCheck.Status == "passed" {
		docsCheck.Name = "reviewer_docs_review"
		checks = append(checks, docsCheck)
	}
	if len(checks) == 0 {
		checks = append(checks, task.VerificationCheck{
			Name:    "reviewer_surface",
			Status:  "failed",
			Summary: "reviewer verifier found neither a Go module nor visible Markdown documentation",
		})
	}
	return checks
}

func (p *Pipeline) runDocsLite(spec task.Spec) task.VerificationCheck {
	var found string
	err := p.walkVisibleFiles(spec.WorkspaceRoot, func(root rootScope, _ string, rel string, entry fs.DirEntry) error {
		name := strings.ToLower(entry.Name())
		if name == "readme" || name == "readme.md" || strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".markdown") {
			if ref, ok := p.refForRel(root, rel); ok {
				found = ref
			}
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return task.VerificationCheck{
			Name:    "docs_structure",
			Status:  "failed",
			Summary: summarizeCommandOutput("", err.Error()),
		}
	}
	if found == "" {
		return task.VerificationCheck{
			Name:    "docs_structure",
			Status:  "failed",
			Summary: "docs_lite verifier found no visible Markdown documentation in configured roots",
		}
	}
	return task.VerificationCheck{
		Name:    "docs_structure",
		Status:  "passed",
		Summary: "docs_lite structural review found " + found,
	}
}

func (p *Pipeline) runSecurityReview(spec task.Spec) []task.VerificationCheck {
	var (
		fileCount      int
		secretHits     []string
		entrypointHits []string
		executableHits []string
	)
	err := p.walkVisibleFiles(spec.WorkspaceRoot, func(root rootScope, fullPath string, rel string, entry fs.DirEntry) error {
		fileCount++
		ref, _ := p.refForRel(root, rel)
		if looksSensitive(entry.Name()) || looksExecutable(entry.Name()) {
			if ref != "" && len(executableHits) < 5 && looksExecutable(entry.Name()) {
				executableHits = append(executableHits, ref)
			}
			if ref != "" && len(secretHits) < 5 && looksSensitive(entry.Name()) {
				secretHits = append(secretHits, ref)
			}
		}
		data, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			return nil
		}
		if len(data) > 65536 {
			data = data[:65536]
		}
		body := string(data)
		if ref != "" && len(secretHits) < 5 && containsSensitiveText(body) {
			secretHits = append(secretHits, ref)
		}
		if ref != "" && len(entrypointHits) < 5 && containsEntrypointSignals(entry.Name(), body) {
			entrypointHits = append(entrypointHits, ref)
		}
		return nil
	})
	if err != nil {
		return []task.VerificationCheck{{
			Name:    "security_inventory",
			Status:  "failed",
			Summary: summarizeCommandOutput("", err.Error()),
		}}
	}
	if fileCount == 0 {
		return []task.VerificationCheck{{
			Name:    "security_inventory",
			Status:  "failed",
			Summary: "security_review verifier found no visible workspace files",
		}}
	}
	return []task.VerificationCheck{
		{
			Name:    "security_inventory",
			Status:  "passed",
			Summary: fmt.Sprintf("security inventory scanned %d visible files", fileCount),
		},
		{
			Name:    "security_secret_indicators",
			Status:  "passed",
			Summary: summarizeCandidates("secret indicators", secretHits),
		},
		{
			Name:    "security_entrypoints",
			Status:  "passed",
			Summary: summarizeCandidates("entrypoint candidates", uniqueStrings(append(entrypointHits, executableHits...))),
		},
	}
}

func looksSensitive(name string) bool {
	lower := strings.ToLower(name)
	for _, token := range []string{"secret", "password", "token", "key", "credential"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func containsSensitiveText(body string) bool {
	lower := strings.ToLower(body)
	for _, token := range []string{"password", "api_key", "secret", "private key", "bearer ", "sk-"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func looksExecutable(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range []string{".sh", ".bash", ".zsh", ".ps1", ".bat", ".cmd", ".py"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func containsEntrypointSignals(name, body string) bool {
	if looksExecutable(name) {
		return true
	}
	lower := strings.ToLower(body)
	for _, token := range []string{"listenandserve", "http.handle", "app.listen(", "router.get(", "router.post(", "http.server", "#!/bin/"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func (p *Pipeline) candidateRoots(workspaceRoot string) []rootScope {
	var roots []rootScope
	seen := map[string]struct{}{}
	if resolved, ok := resolveRoot(workspaceRoot); ok {
		roots = append(roots, rootScope{ID: "workspace", Path: resolved})
		seen[resolved] = struct{}{}
	}
	for idx, extra := range p.Config.Visibility.AdditionalRoots {
		if strings.TrimSpace(extra) == "" {
			continue
		}
		resolved := extra
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(workspaceRoot, extra)
		}
		normalized, ok := resolveRoot(resolved)
		if !ok {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		roots = append(roots, rootScope{ID: fmt.Sprintf("extra_%d", idx+1), Path: normalized})
	}
	return roots
}

func resolveRoot(root string) (string, bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if _, err := os.Stat(abs); err != nil {
		return "", false
	}
	return abs, true
}

func (p *Pipeline) rootRef(root rootScope, fullPath string) (string, bool) {
	realFull := fullPath
	if resolved, err := filepath.EvalSymlinks(fullPath); err == nil {
		realFull = resolved
	}
	rel, err := filepath.Rel(root.Path, realFull)
	if err != nil {
		return "", false
	}
	if rel == "." {
		return "", false
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", false
	}
	return p.refForRel(root, rel)
}

func (p *Pipeline) refForRel(root rootScope, rel string) (string, bool) {
	slash := filepath.ToSlash(filepath.Clean(rel))
	slash = strings.TrimPrefix(slash, "./")
	if slash == "." || slash == "" || p.isDenied(slash) {
		return "", false
	}
	if root.ID == "workspace" {
		return "workspace:" + slash, true
	}
	return "root:" + root.ID + "/" + slash, true
}

func (p *Pipeline) isDenied(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(rel)), "./")
	for _, rawPattern := range p.Config.Visibility.DenyPatterns {
		pattern := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(rawPattern)), "./")
		if pattern == "" || pattern == "." {
			continue
		}
		if rel == pattern || strings.HasPrefix(rel, pattern+"/") {
			return true
		}
		if ok, _ := path.Match(pattern, rel); ok {
			return true
		}
		for _, segment := range strings.Split(rel, "/") {
			if segment == pattern {
				return true
			}
		}
	}
	return false
}

func (p *Pipeline) walkVisibleFiles(workspaceRoot string, fn func(root rootScope, fullPath string, rel string, entry fs.DirEntry) error) error {
	seen := map[string]struct{}{}
	for _, root := range p.candidateRoots(workspaceRoot) {
		err := filepath.WalkDir(root.Path, func(fullPath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(root.Path, fullPath)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(filepath.Clean(rel))
			if rel == "." {
				return nil
			}
			if p.isDenied(rel) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			realPath := fullPath
			if resolved, err := filepath.EvalSymlinks(fullPath); err == nil {
				realPath = resolved
			}
			if _, ok := seen[realPath]; ok {
				return nil
			}
			seen[realPath] = struct{}{}
			return fn(root, realPath, rel, entry)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *Pipeline) missingPrerequisites() []string {
	mode := provider.CanonicalMode(p.Config.Provider.Mode)
	if !provider.RequiresRemoteConfig(mode) {
		return nil
	}
	var missing []string
	if strings.TrimSpace(p.Config.Provider.BaseURL) == "" {
		missing = append(missing, fmt.Sprintf("provider.base_url missing for %s provider", mode))
	}
	if strings.TrimSpace(p.Config.Provider.Model) == "" {
		missing = append(missing, fmt.Sprintf("provider.model missing for %s provider", mode))
	}
	keyEnv := strings.TrimSpace(p.Config.Provider.APIKeyEnv)
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	if strings.TrimSpace(os.Getenv(keyEnv)) == "" {
		missing = append(missing, fmt.Sprintf("env %s not set", keyEnv))
	}
	return missing
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func summarizeCandidates(prefix string, refs []string) string {
	if len(refs) == 0 {
		return prefix + ": none found"
	}
	return prefix + ": " + strings.Join(refs, ", ")
}

func summarizeCommandOutput(stdout, stderr string) string {
	text := strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(stdout),
		strings.TrimSpace(stderr),
	}, "\n"))
	if text == "" {
		return ""
	}
	if len(text) > 240 {
		return text[:240]
	}
	return text
}
