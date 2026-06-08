package ngenrt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"ngen/internal/artifact"
	"ngen/internal/task"
)

func workerWorkspaceBaseDir(parentWorkspaceRoot, workerID string) string {
	return filepath.Join(filepath.Dir(parentWorkspaceRoot), ".ngen-worker-workspaces", filepath.Base(parentWorkspaceRoot), workerID)
}

func (s *Service) ensureTaskWorkspaceRoot(spec task.Spec) (task.Spec, error) {
	if _, err := os.Stat(spec.WorkspaceRoot); err == nil {
		return spec, nil
	} else if !os.IsNotExist(err) {
		return spec, err
	}
	if strings.TrimSpace(spec.ParentTaskID) == "" || strings.TrimSpace(spec.ParentWorkerID) == "" {
		return spec, fmt.Errorf("task workspace root is unavailable: %s", spec.WorkspaceRoot)
	}
	contract, err := s.Store.LoadWorkerContract(spec.ParentTaskID, spec.ParentWorkerID)
	if err != nil {
		return spec, err
	}
	if strings.TrimSpace(contract.WorkspaceRoot) != "" {
		if _, err := os.Stat(contract.WorkspaceRoot); err == nil {
			spec.WorkspaceRoot = contract.WorkspaceRoot
			if saveErr := s.Store.SaveTask(spec); saveErr != nil {
				return spec, saveErr
			}
			return spec, nil
		}
	}
	parentSpec, err := s.Store.LoadTask(spec.ParentTaskID)
	if err != nil {
		return spec, err
	}
	parentSpec = task.HydrateSpec(parentSpec, s.Config)
	if _, err := os.Stat(parentSpec.WorkspaceRoot); err != nil {
		return spec, err
	}
	spec.WorkspaceRoot = parentSpec.WorkspaceRoot
	if err := s.Store.SaveTask(spec); err != nil {
		return spec, err
	}
	return spec, nil
}

func (s *Service) rebindChildTaskWorkspaceRoot(childTaskID, workspaceRoot string) error {
	spec, err := s.Store.LoadTask(childTaskID)
	if err != nil {
		return err
	}
	if spec.WorkspaceRoot == workspaceRoot {
		return nil
	}
	spec.WorkspaceRoot = workspaceRoot
	return s.Store.SaveTask(spec)
}

func workerWorkspaceBaseDirFromRecord(workspace task.WorkerWorkspace) string {
	switch workspace.EffectiveMode {
	case "snapshot_copy":
		return filepath.Dir(workspace.WorkspaceRoot)
	case "git_worktree":
		if workspace.RepoRoot != "" {
			return filepath.Dir(workspace.RepoRoot)
		}
	}
	return ""
}

func (s *Service) prepareWorkerWorkspace(ctx context.Context, parent task.Spec, workerID, requestedMode string) (task.WorkerWorkspace, error) {
	requestedMode = task.NormalizeWorkspaceIsolationMode(requestedMode)
	if requestedMode == "" {
		requestedMode = "auto"
	}
	record := task.WorkerWorkspace{
		SchemaVersion: task.SchemaVersion,
		WorkerID:      workerID,
		ParentTaskID:  parent.TaskID,
		RequestedMode: requestedMode,
		EffectiveMode: "shared_workspace",
		Status:        "shared",
		WorkspaceRoot: parent.WorkspaceRoot,
		Reason:        "Worker uses the parent workspace directly.",
		CreatedAt:     task.Now(),
		UpdatedAt:     task.Now(),
	}
	switch requestedMode {
	case "shared_workspace":
		return record, nil
	case "snapshot_copy":
		return s.prepareSnapshotWorkerWorkspace(parent, record)
	case "git_worktree":
		return s.prepareGitWorkerWorkspace(ctx, parent, record)
	case "auto":
		gitRecord, err := s.prepareGitWorkerWorkspace(ctx, parent, record)
		if err == nil {
			gitRecord.RequestedMode = requestedMode
			return gitRecord, nil
		}
		snapshotRecord, snapshotErr := s.prepareSnapshotWorkerWorkspace(parent, record)
		if snapshotErr != nil {
			return task.WorkerWorkspace{}, fmt.Errorf("prepare isolated worker workspace: git_worktree=%v; snapshot_copy=%w", err, snapshotErr)
		}
		snapshotRecord.RequestedMode = requestedMode
		snapshotRecord.Reason = fmt.Sprintf("Fell back to snapshot_copy after git_worktree was unavailable: %v", err)
		return snapshotRecord, nil
	default:
		return task.WorkerWorkspace{}, fmt.Errorf("unsupported workspace isolation mode: %s", requestedMode)
	}
}

func (s *Service) prepareSnapshotWorkerWorkspace(parent task.Spec, record task.WorkerWorkspace) (task.WorkerWorkspace, error) {
	baseDir := workerWorkspaceBaseDir(parent.WorkspaceRoot, record.WorkerID)
	destRoot := filepath.Join(baseDir, "workspace")
	if err := os.RemoveAll(baseDir); err != nil && !os.IsNotExist(err) {
		return task.WorkerWorkspace{}, err
	}
	if err := mirrorWorkspaceTree(parent.WorkspaceRoot, destRoot); err != nil {
		_ = os.RemoveAll(baseDir)
		return task.WorkerWorkspace{}, err
	}
	record.EffectiveMode = "snapshot_copy"
	record.Status = "prepared"
	record.WorkspaceRoot = destRoot
	record.Reason = "Prepared an isolated snapshot copy for the child workspace."
	record.UpdatedAt = task.Now()
	return record, nil
}

func (s *Service) prepareGitWorkerWorkspace(ctx context.Context, parent task.Spec, record task.WorkerWorkspace) (task.WorkerWorkspace, error) {
	repoRoot, err := gitOutput(ctx, parent.WorkspaceRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return task.WorkerWorkspace{}, err
	}
	parentRel, err := filepath.Rel(repoRoot, parent.WorkspaceRoot)
	if err != nil {
		return task.WorkerWorkspace{}, err
	}
	baseDir := workerWorkspaceBaseDir(parent.WorkspaceRoot, record.WorkerID)
	repoDest := filepath.Join(baseDir, "repo")
	if err := os.RemoveAll(baseDir); err != nil && !os.IsNotExist(err) {
		return task.WorkerWorkspace{}, err
	}
	if _, err := runCommand(ctx, repoRoot, "git", "-C", repoRoot, "worktree", "add", "--detach", repoDest, "HEAD"); err != nil {
		return task.WorkerWorkspace{}, err
	}
	childRoot := repoDest
	if parentRel != "." {
		childRoot = filepath.Join(repoDest, parentRel)
	}
	if err := mirrorWorkspaceTree(parent.WorkspaceRoot, childRoot); err != nil {
		_ = s.removeWorkerWorkspace(context.Background(), parent.WorkspaceRoot, task.WorkerWorkspace{
			EffectiveMode: "git_worktree",
			RepoRoot:      repoDest,
		})
		return task.WorkerWorkspace{}, err
	}
	record.EffectiveMode = "git_worktree"
	record.Status = "prepared"
	record.WorkspaceRoot = childRoot
	record.RepoRoot = repoDest
	record.Reason = "Prepared a git worktree for the child workspace."
	record.UpdatedAt = task.Now()
	return record, nil
}

func mirrorWorkspaceTree(srcRoot, destRoot string) error {
	type relEntry struct {
		kind fs.FileMode
	}
	seen := map[string]relEntry{}
	if err := filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == srcRoot {
			return os.MkdirAll(destRoot, 0o755)
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.Clean(rel)
		if shouldSkipWorkerWorkspaceRel(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(destRoot, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		seen[rel] = relEntry{kind: info.Mode()}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			if err := os.RemoveAll(target); err != nil && !os.IsNotExist(err) {
				return err
			}
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		default:
			if err := copyFile(path, target, info.Mode().Perm()); err != nil {
				return err
			}
			return nil
		}
	}); err != nil {
		return err
	}
	var toDelete []string
	if err := filepath.WalkDir(destRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == destRoot {
			return nil
		}
		rel, err := filepath.Rel(destRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.Clean(rel)
		if shouldSkipWorkerWorkspaceRel(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if _, ok := seen[rel]; ok {
			return nil
		}
		toDelete = append(toDelete, path)
		if d.IsDir() {
			return fs.SkipDir
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(toDelete)))
	for _, target := range toDelete {
		if err := os.RemoveAll(target); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func shouldSkipWorkerWorkspaceRel(rel string) bool {
	if rel == "." {
		return false
	}
	head := strings.Split(filepath.ToSlash(rel), "/")[0]
	switch head {
	case ".ngen", ".git":
		return true
	default:
		return false
	}
}

func copyFile(src, dest string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	result, err := runCommand(ctx, dir, "git", args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.stdout.String()), nil
}

type workerManifestEntry struct {
	Exists bool
	Kind   string
	SHA256 string
}

type workerReconcileDecision struct {
	change task.WorkerReconcileFileChange
	apply  bool
}

type workerParentBackup struct {
	exists bool
	kind   string
	mode   fs.FileMode
	data   []byte
}

func workerRoleReconcileMode(role string) string {
	switch strings.TrimSpace(role) {
	case string(task.KindCoding), string(task.KindGeneral):
		return "apply_on_accept"
	default:
		return "artifact_only"
	}
}

func workerContractReconcileMode(contract task.WorkerContract) string {
	if contract.SubagentPolicy != nil && strings.TrimSpace(contract.SubagentPolicy.ReconcileMode) != "" {
		return task.NormalizeWorkerReconcileMode(contract.SubagentPolicy.ReconcileMode)
	}
	return workerRoleReconcileMode(contract.Role)
}

func workerContractAutoReleaseOnSuccess(cfg task.Config, contract task.WorkerContract) bool {
	if contract.SubagentPolicy != nil {
		return contract.SubagentPolicy.AutoReleaseOnSuccess
	}
	return cfg.Subagents.AutoReleaseOnSuccess
}

func collectWorkerManifest(root string) (map[string]workerManifestEntry, error) {
	manifest := make(map[string]workerManifestEntry)
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.Clean(rel)
		if shouldSkipWorkerWorkspaceRel(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entry := workerManifestEntry{Exists: true, Kind: "file"}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256([]byte(target))
			entry.Kind = "symlink"
			entry.SHA256 = hex.EncodeToString(sum[:])
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			entry.SHA256 = hex.EncodeToString(sum[:])
		}
		manifest[filepath.ToSlash(rel)] = entry
		return nil
	}); err != nil {
		return nil, err
	}
	return manifest, nil
}

func workerManifestToBaselineEntries(manifest map[string]workerManifestEntry) []task.WorkerWorkspaceBaselineEntry {
	if len(manifest) == 0 {
		return nil
	}
	paths := make([]string, 0, len(manifest))
	for rel := range manifest {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	entries := make([]task.WorkerWorkspaceBaselineEntry, 0, len(paths))
	for _, rel := range paths {
		entry := manifest[rel]
		entries = append(entries, task.WorkerWorkspaceBaselineEntry{
			Path:   rel,
			Exists: entry.Exists,
			Kind:   entry.Kind,
			SHA256: entry.SHA256,
		})
	}
	return entries
}

func baselineEntriesToManifest(entries []task.WorkerWorkspaceBaselineEntry) map[string]workerManifestEntry {
	manifest := make(map[string]workerManifestEntry, len(entries))
	for _, entry := range entries {
		manifest[entry.Path] = workerManifestEntry{
			Exists: entry.Exists,
			Kind:   entry.Kind,
			SHA256: entry.SHA256,
		}
	}
	return manifest
}

func captureWorkerBaseline(parent task.Spec, workerID, childTaskID string) (task.WorkerWorkspaceBaseline, error) {
	manifest, err := collectWorkerManifest(parent.WorkspaceRoot)
	if err != nil {
		return task.WorkerWorkspaceBaseline{}, err
	}
	entries := workerManifestToBaselineEntries(manifest)
	return task.WorkerWorkspaceBaseline{
		SchemaVersion: task.SchemaVersion,
		BaselineID:    task.NewID("WBL"),
		WorkerID:      workerID,
		ParentTaskID:  parent.TaskID,
		ChildTaskID:   childTaskID,
		FileCount:     len(entries),
		Entries:       entries,
		CreatedAt:     task.Now(),
		UpdatedAt:     task.Now(),
	}, nil
}

func manifestEntryEqual(a, b workerManifestEntry) bool {
	return a.Exists == b.Exists && a.Kind == b.Kind && a.SHA256 == b.SHA256
}

func manifestEntryForPath(manifest map[string]workerManifestEntry, rel string) workerManifestEntry {
	if entry, ok := manifest[rel]; ok {
		if entry.Kind == "" && entry.Exists {
			entry.Kind = "file"
		}
		return entry
	}
	return workerManifestEntry{}
}

func workerReconcileAction(baseline, child workerManifestEntry) string {
	switch {
	case !baseline.Exists && child.Exists:
		return "add"
	case baseline.Exists && !child.Exists:
		return "delete"
	default:
		return "update"
	}
}

func workerReconcileSummary(status string, changeCount, appliedCount, conflictCount int) string {
	switch status {
	case "pending":
		return "Worker reconcile is waiting for accepted settlement."
	case "shared_workspace":
		return "Worker used the parent workspace directly; no isolated reconcile step was needed."
	case "noop":
		return "Worker reconcile found no parent-visible isolated workspace changes to apply."
	case "recorded":
		return fmt.Sprintf("Recorded %d isolated child change(s) without applying them because the worker role is artifact-only.", changeCount)
	case "applied":
		return fmt.Sprintf("Applied %d isolated child change(s) back into the parent workspace.", appliedCount)
	case "conflict":
		return fmt.Sprintf("Worker reconcile detected %d conflict(s); no isolated child changes were applied.", conflictCount)
	case "failed":
		return "Worker reconcile failed before it could safely apply the isolated child changes."
	default:
		return "Worker reconcile has no durable result yet."
	}
}

type commandResult struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func runCommand(ctx context.Context, dir string, name string, args ...string) (commandResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var result commandResult
	cmd.Stdout = &result.stdout
	cmd.Stderr = &result.stderr
	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(result.stderr.String())
		if output == "" {
			output = strings.TrimSpace(result.stdout.String())
		}
		if output != "" {
			return result, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), output)
		}
		return result, err
	}
	return result, nil
}

func (s *Service) workerWorkspaceView(parentSpec task.Spec, contract task.WorkerContract) task.WorkerWorkspace {
	workspace, err := s.Store.LoadWorkerWorkspace(contract.ParentTaskID, contract.WorkerID)
	if err == nil {
		if workspace.ChildTaskID == "" {
			workspace.ChildTaskID = contract.ChildTaskID
		}
		return workspace
	}
	root := contract.WorkspaceRoot
	if root == "" {
		root = parentSpec.WorkspaceRoot
	}
	mode := contract.WorkspaceMode
	if mode == "" {
		mode = "shared_workspace"
	}
	status := contract.WorkspaceStatus
	if status == "" {
		status = "shared"
	}
	return task.WorkerWorkspace{
		SchemaVersion: task.SchemaVersion,
		WorkerID:      contract.WorkerID,
		ParentTaskID:  contract.ParentTaskID,
		ChildTaskID:   contract.ChildTaskID,
		RequestedMode: mode,
		EffectiveMode: mode,
		Status:        status,
		WorkspaceRoot: root,
		Reason:        "Worker runtime has not persisted a dedicated workspace record yet.",
		CreatedAt:     latestTimestamp(contract.CreatedAt, task.Now()),
		UpdatedAt:     latestTimestamp(contract.UpdatedAt, task.Now()),
	}
}

func (s *Service) workerSettlementView(contract task.WorkerContract, childStatus task.StatusSnapshot) task.WorkerSettlement {
	existing, err := s.Store.LoadWorkerSettlement(contract.ParentTaskID, contract.WorkerID)
	if err != nil {
		existing = task.WorkerSettlement{
			SchemaVersion: task.SchemaVersion,
			SettlementID:  task.NewID("SET"),
			WorkerID:      contract.WorkerID,
			ParentTaskID:  contract.ParentTaskID,
			ChildTaskID:   contract.ChildTaskID,
			CreatedAt:     task.Now(),
		}
	}
	existing.ChildTaskID = contract.ChildTaskID
	existing.ChildState = childStatus.State
	existing.UpdatedAt = task.Now()

	var (
		completion   task.CompletionReport
		reviewReport task.ReviewReport
		verification task.VerificationReport
	)
	if value, err := s.Store.LoadCompletion(contract.ChildTaskID); err == nil {
		completion = value
		existing.CompletionStatus = value.Status
	}
	if value, err := s.Store.LoadReview(contract.ChildTaskID); err == nil {
		reviewReport = value
		existing.ReviewStatus = value.Status
	}
	if value, err := s.Store.LoadVerification(contract.ChildTaskID); err == nil {
		verification = value
		existing.VerificationStatus = value.Status
	}

	existing.EvidenceRefs = workerSettlementRefs(contract, childStatus)
	switch childStatus.State {
	case task.StateDone:
		existing.Status = "accepted"
		existing.Summary = firstNonEmpty(completion.Summary, reviewReport.Summary, "Worker reached Done.")
		if existing.SettledAt == "" {
			existing.SettledAt = task.Now()
		}
	case task.StateFailed:
		existing.Status = "failed"
		existing.Summary = firstNonEmpty(completion.Summary, verification.FailureSummary, "Worker failed before meeting its completion gate.")
		if existing.SettledAt == "" {
			existing.SettledAt = task.Now()
		}
	case task.StateAborted:
		existing.Status = "aborted"
		existing.Summary = "Worker was aborted."
		if existing.SettledAt == "" {
			existing.SettledAt = task.Now()
		}
	case task.StateBlocked:
		existing.Status = "blocked"
		existing.Summary = fmt.Sprintf("Worker is blocked: %s.", childStatus.StatusReasonCode)
		existing.SettledAt = ""
	case task.StateWaiting:
		existing.Status = "waiting"
		existing.Summary = "Worker is waiting on a durable watch."
		existing.SettledAt = ""
	default:
		existing.Status = "pending"
		existing.Summary = "Worker has not reached a terminal settlement yet."
		existing.SettledAt = ""
	}
	return existing
}

func (s *Service) workerResultView(contract task.WorkerContract, childStatus task.StatusSnapshot, settlement task.WorkerSettlement) task.WorkerResult {
	existing, err := s.Store.LoadWorkerResult(contract.ParentTaskID, contract.WorkerID)
	if err != nil {
		existing = task.WorkerResult{
			SchemaVersion: task.SchemaVersion,
			ResultID:      task.NewID("WRES"),
			WorkerID:      contract.WorkerID,
			ParentTaskID:  contract.ParentTaskID,
			ChildTaskID:   contract.ChildTaskID,
			Role:          contract.Role,
			Objective:     contract.Objective,
			CreatedAt:     task.Now(),
		}
	}
	existing.ChildTaskID = contract.ChildTaskID
	existing.Role = contract.Role
	existing.Objective = contract.Objective
	existing.ChildState = childStatus.State
	existing.SettlementStatus = settlement.Status
	existing.UpdatedAt = task.Now()

	existing.HandoffRef = ""
	if childStatus.HandoffRef != "" {
		existing.HandoffRef = filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, childStatus.HandoffRef))
	}

	existing.CompletionStatus = ""
	existing.CompletionSummary = ""
	existing.CompletionRef = ""
	if value, err := s.Store.LoadCompletion(contract.ChildTaskID); err == nil {
		existing.CompletionStatus = value.Status
		existing.CompletionSummary = strings.TrimSpace(value.Summary)
		existing.CompletionRef = filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, "completion", "latest.json"))
	}

	existing.ReviewStatus = ""
	existing.ReviewSummary = ""
	existing.ReviewRef = ""
	if value, err := s.Store.LoadReview(contract.ChildTaskID); err == nil {
		existing.ReviewStatus = value.Status
		existing.ReviewSummary = strings.TrimSpace(value.Summary)
		existing.ReviewRef = filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, "reviews", "latest.json"))
	}

	existing.VerificationStatus = ""
	existing.VerificationSummary = ""
	existing.VerificationRef = ""
	if value, err := s.Store.LoadVerification(contract.ChildTaskID); err == nil {
		existing.VerificationStatus = value.Status
		existing.VerificationSummary = strings.TrimSpace(value.FailureSummary)
		if existing.VerificationSummary == "" && len(value.Checks) > 0 {
			existing.VerificationSummary = strings.TrimSpace(value.Checks[0].Summary)
		}
		existing.VerificationRef = filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, "verification", "latest.json"))
	}

	existing.CriteriaRef = ""
	if _, err := s.Store.LoadCriteria(contract.ChildTaskID); err == nil {
		existing.CriteriaRef = filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, "criteria", "latest.json"))
	}

	existing.BlockedReasonCode = childStatus.StatusReasonCode
	existing.BlockedDetailRef = childStatus.StatusDetailRef
	existing.ApprovalID = contract.ApprovalID
	existing.ApprovalRef = contract.ApprovalRef
	existing.ApprovalStatus = contract.ApprovalStatus
	existing.ApprovalScope = strings.TrimSpace(contract.ApprovalScope)
	existing.ApprovalReason = strings.TrimSpace(contract.ApprovalReason)
	existing.InputRequestID = contract.InputRequestID
	existing.InputRequestRef = contract.InputRequestRef
	existing.InputField = strings.TrimSpace(contract.InputField)
	existing.InputPrompt = strings.TrimSpace(contract.InputPrompt)
	existing.RequiresParentAction = contract.RequiresParentAction
	existing.ParentActionType = contract.ParentActionType
	existing.ParentActionOptions = append([]string(nil), contract.ParentActionOptions...)
	existing.ParentActionSummary = strings.TrimSpace(contract.ParentActionSummary)

	existing.EvidenceRefs = workerResultRefs(contract, existing)
	existing.Summary = workerResultSummary(childStatus, settlement, existing)
	return existing
}

func workerSettlementRefs(contract task.WorkerContract, childStatus task.StatusSnapshot) []string {
	refs := []string{
		filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, "task.json")),
		filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, "state.json")),
	}
	if childStatus.HandoffRef != "" {
		refs = append(refs, filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, childStatus.HandoffRef)))
	}
	if childStatus.CompletionRef != "" {
		refs = append(refs, filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, childStatus.CompletionRef)))
	}
	if childStatus.LastVerificationRef != "" {
		refs = append(refs, filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, childStatus.LastVerificationRef)))
	}
	if childStatus.LastReviewRef != "" {
		refs = append(refs, filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, childStatus.LastReviewRef)))
	}
	return uniqueStrings(refs)
}

func workerResultRefs(contract task.WorkerContract, result task.WorkerResult) []string {
	refs := []string{
		filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, "task.json")),
		filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, "state.json")),
		artifact.WorkerSettlementRef(contract.WorkerID),
		result.BlockedDetailRef,
		result.ApprovalRef,
		result.InputRequestRef,
		result.HandoffRef,
		result.CompletionRef,
		result.ReviewRef,
		result.VerificationRef,
		result.CriteriaRef,
	}
	return uniqueStrings(refs)
}

func scoreWorkerResultEvidence(result task.WorkerResult, settlement task.WorkerSettlement, reconcile task.WorkerReconcile) task.WorkerResult {
	result.HandoffPresent = strings.TrimSpace(result.HandoffRef) != ""
	result.Verified = result.VerificationStatus == "passed"
	result.ReviewClear = result.ReviewStatus == "clear"
	result.CriteriaClosed = strings.TrimSpace(result.CriteriaRef) != ""
	result.SettlementAccepted = settlement.Status == "accepted"
	result.ReconcileClean = workerReconcileTrusted(reconcile)
	result.ParentActionUnresolved = result.RequiresParentAction
	result.ConflictCount = reconcile.ConflictCount

	var missing []string
	score := 0
	if result.HandoffPresent {
		score += 10
	} else {
		missing = append(missing, "handoff")
	}
	if strings.TrimSpace(result.CompletionRef) != "" {
		score += 10
	} else {
		missing = append(missing, "completion")
	}
	if result.CompletionStatus == "accepted" {
		score += 10
	} else {
		missing = append(missing, "completion_accepted")
	}
	if result.Verified {
		score += 15
	} else {
		missing = append(missing, "verification_passed")
	}
	if result.ReviewClear {
		score += 15
	} else {
		missing = append(missing, "review_clear")
	}
	if result.CriteriaClosed {
		score += 10
	} else {
		missing = append(missing, "criteria")
	}
	if result.SettlementAccepted {
		score += 15
	} else {
		missing = append(missing, "settlement_accepted")
	}
	if result.ReconcileClean {
		score += 15
	} else {
		missing = append(missing, "reconcile_clean")
	}
	if result.ParentActionUnresolved {
		missing = append(missing, "parent_action")
	}
	result.MissingEvidence = uniqueStrings(missing)
	result.EvidenceScore = score
	switch {
	case score >= 90 && len(result.MissingEvidence) == 0:
		result.EvidenceGrade = "complete"
	case score >= 70:
		result.EvidenceGrade = "partial"
	case score > 0:
		result.EvidenceGrade = "weak"
	default:
		result.EvidenceGrade = "missing"
	}
	result.TrustedForParentCompletion = result.EvidenceGrade == "complete" &&
		result.ChildState == task.StateDone &&
		!result.ParentActionUnresolved &&
		result.ConflictCount == 0
	return result
}

func workerReconcileTrusted(reconcile task.WorkerReconcile) bool {
	switch reconcile.Status {
	case "applied", "recorded", "noop", "shared_workspace":
		return reconcile.ConflictCount == 0
	default:
		return false
	}
}

func uniqueStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func workerResultSummary(childStatus task.StatusSnapshot, settlement task.WorkerSettlement, result task.WorkerResult) string {
	statusParts := make([]string, 0, 3)
	if result.CompletionStatus != "" {
		statusParts = append(statusParts, "completion="+result.CompletionStatus)
	}
	if result.ReviewStatus != "" {
		statusParts = append(statusParts, "review="+result.ReviewStatus)
	}
	if result.VerificationStatus != "" {
		statusParts = append(statusParts, "verification="+result.VerificationStatus)
	}

	prefix := "Worker result is still pending."
	detail := firstNonEmpty(workerResultActionDetail(result), result.ReviewSummary, result.CompletionSummary, result.VerificationSummary, settlement.Summary)
	switch childStatus.State {
	case task.StateDone:
		prefix = "Worker reached Done."
		detail = firstNonEmpty(result.CompletionSummary, result.ReviewSummary, settlement.Summary)
	case task.StateFailed:
		prefix = "Worker failed."
		detail = firstNonEmpty(result.VerificationSummary, result.CompletionSummary, settlement.Summary)
	case task.StateBlocked:
		if strings.TrimSpace(childStatus.StatusReasonCode) != "" {
			prefix = fmt.Sprintf("Worker is blocked (%s).", childStatus.StatusReasonCode)
		} else {
			prefix = "Worker is blocked."
		}
		detail = firstNonEmpty(workerResultActionDetail(result), result.ReviewSummary, result.CompletionSummary, settlement.Summary)
	case task.StateWaiting:
		prefix = "Worker is waiting on a durable watch."
		detail = settlement.Summary
	case task.StateAborted:
		prefix = "Worker was aborted."
		detail = settlement.Summary
	case task.StateActive:
		if result.RequiresParentAction && result.ParentActionType == "continue_child" {
			prefix = "Worker is ready for parent continuation."
			detail = firstNonEmpty(workerResultActionDetail(result), settlement.Summary)
		}
	}

	parts := []string{prefix}
	if len(statusParts) > 0 {
		parts = append(parts, "Statuses: "+strings.Join(statusParts, ", ")+".")
	}
	if detail != "" && detail != prefix {
		parts = append(parts, detail)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func workerResultActionDetail(result task.WorkerResult) string {
	if summary := strings.TrimSpace(result.ParentActionSummary); summary != "" {
		switch result.ParentActionType {
		case "owned_approval_pending", "continue_child", "parent_takeover":
			return summary
		}
	}
	switch result.BlockedReasonCode {
	case "blocked_missing_input":
		if result.InputRequestID != "" || result.InputPrompt != "" {
			var b strings.Builder
			b.WriteString("Pending input request")
			if result.InputRequestID != "" {
				b.WriteString(" ")
				b.WriteString(result.InputRequestID)
			}
			if result.InputField != "" {
				b.WriteString(" for ")
				b.WriteString(result.InputField)
			}
			if result.InputPrompt != "" {
				b.WriteString(": ")
				b.WriteString(result.InputPrompt)
			}
			b.WriteString(".")
			if summary := strings.TrimSpace(result.ParentActionSummary); summary != "" {
				b.WriteString(" ")
				b.WriteString(summary)
			}
			return strings.TrimSpace(b.String())
		}
	case "blocked_policy":
		if result.ApprovalID != "" {
			var b strings.Builder
			b.WriteString("Approval ")
			b.WriteString(result.ApprovalID)
			if result.ApprovalScope != "" {
				b.WriteString(" (")
				b.WriteString(result.ApprovalScope)
				b.WriteString(")")
			}
			if result.ApprovalStatus != "" {
				b.WriteString(" is ")
				b.WriteString(result.ApprovalStatus)
			}
			b.WriteString(".")
			if result.ApprovalReason != "" {
				b.WriteString(" ")
				b.WriteString(result.ApprovalReason)
				if !strings.HasSuffix(result.ApprovalReason, ".") {
					b.WriteString(".")
				}
			}
			if summary := strings.TrimSpace(result.ParentActionSummary); summary != "" {
				b.WriteString(" ")
				b.WriteString(summary)
			}
			return strings.TrimSpace(b.String())
		}
	}
	if summary := strings.TrimSpace(result.ParentActionSummary); summary != "" {
		return summary
	}
	return ""
}

func workerReconcileRefs(contract task.WorkerContract, workspace task.WorkerWorkspace) []string {
	refs := []string{
		artifact.WorkerWorkspaceRef(contract.WorkerID),
		artifact.WorkerSettlementRef(contract.WorkerID),
	}
	if strings.TrimSpace(workspace.BaselineRef) != "" {
		refs = append(refs, workspace.BaselineRef)
	}
	return uniqueStrings(refs)
}

func markWorkerReconcileParentTakeover(reconcile *task.WorkerReconcile, contract task.WorkerContract, workspace task.WorkerWorkspace, summary string) {
	reconcile.ParentTakeoverRequired = true
	reconcile.ParentTakeoverSummary = summary
	reconcile.ParentTakeoverRefs = workerParentTakeoverRefs(contract, workspace)
	reconcile.EvidenceRefs = uniqueStrings(append(reconcile.EvidenceRefs, reconcile.ParentTakeoverRefs...))
}

func workerParentTakeoverRefs(contract task.WorkerContract, workspace task.WorkerWorkspace) []string {
	refs := []string{
		filepath.ToSlash(filepath.Join("workers", contract.WorkerID+".json")),
		artifact.WorkerWorkspaceRef(contract.WorkerID),
		artifact.WorkerBaselineRef(contract.WorkerID),
		artifact.WorkerReconcileRef(contract.WorkerID),
	}
	if workspace.WorkspaceRoot != "" {
		refs = append(refs, "workspace:"+filepath.ToSlash(workspace.WorkspaceRoot))
	}
	return uniqueStrings(refs)
}

func buildWorkerReconcileDecisions(mode string, baseline, parent, child map[string]workerManifestEntry) []workerReconcileDecision {
	pathSet := make(map[string]struct{}, len(baseline)+len(child))
	for rel := range baseline {
		pathSet[rel] = struct{}{}
	}
	for rel := range child {
		pathSet[rel] = struct{}{}
	}
	paths := make([]string, 0, len(pathSet))
	for rel := range pathSet {
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	decisions := make([]workerReconcileDecision, 0, len(paths))
	for _, rel := range paths {
		baselineEntry := manifestEntryForPath(baseline, rel)
		childEntry := manifestEntryForPath(child, rel)
		if manifestEntryEqual(baselineEntry, childEntry) {
			continue
		}
		parentEntry := manifestEntryForPath(parent, rel)
		change := task.WorkerReconcileFileChange{
			Path:           rel,
			Action:         workerReconcileAction(baselineEntry, childEntry),
			BaselineExists: baselineEntry.Exists,
			BaselineKind:   baselineEntry.Kind,
			BaselineSHA256: baselineEntry.SHA256,
			ParentExists:   parentEntry.Exists,
			ParentKind:     parentEntry.Kind,
			ParentSHA256:   parentEntry.SHA256,
			ChildExists:    childEntry.Exists,
			ChildKind:      childEntry.Kind,
			ChildSHA256:    childEntry.SHA256,
		}
		decision := workerReconcileDecision{change: change}
		switch mode {
		case "artifact_only":
			decision.change.Status = "recorded"
			decision.change.Summary = "Recorded isolated child change without applying it to the parent workspace."
		default:
			if (baselineEntry.Exists && baselineEntry.Kind != "file") || (parentEntry.Exists && parentEntry.Kind != "file") || (childEntry.Exists && childEntry.Kind != "file") {
				decision.change.Status = "conflict"
				decision.change.Summary = "Non-file entries are not auto-applied during worker reconcile."
			} else if manifestEntryEqual(parentEntry, childEntry) {
				decision.change.Status = "noop"
				decision.change.Summary = "Parent workspace already matches the child change."
			} else if !manifestEntryEqual(parentEntry, baselineEntry) {
				decision.change.Status = "conflict"
				decision.change.Summary = "Parent workspace drifted since the child baseline was captured."
			} else {
				decision.change.Status = "pending_apply"
				decision.change.Summary = "Change is ready to apply back into the parent workspace."
				decision.apply = true
			}
		}
		decisions = append(decisions, decision)
	}
	return decisions
}

func readWorkerParentBackup(path string) (workerParentBackup, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return workerParentBackup{}, nil
		}
		return workerParentBackup{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return workerParentBackup{exists: true, kind: "symlink", mode: info.Mode()}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return workerParentBackup{}, err
	}
	return workerParentBackup{
		exists: true,
		kind:   "file",
		mode:   info.Mode().Perm(),
		data:   data,
	}, nil
}

func restoreWorkerParentBackup(path string, backup workerParentBackup) error {
	if !backup.exists {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if backup.kind != "file" {
		return fmt.Errorf("restore unsupported backup kind %s for %s", backup.kind, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, backup.data, backup.mode)
}

func applyWorkerReconcileChanges(parentRoot, childRoot string, decisions []workerReconcileDecision) ([]task.WorkspaceFileChange, error) {
	backups := make(map[string]workerParentBackup)
	var applyOrder []string
	for _, decision := range decisions {
		if !decision.apply {
			continue
		}
		rel := filepath.Clean(filepath.FromSlash(decision.change.Path))
		parentPath, err := safeWorkspaceEditFullPath(parentRoot, rel)
		if err != nil {
			return nil, err
		}
		backup, err := readWorkerParentBackup(parentPath)
		if err != nil {
			return nil, err
		}
		backups[decision.change.Path] = backup
		applyOrder = append(applyOrder, decision.change.Path)
	}

	rollback := func(applied []string) {
		for i := len(applied) - 1; i >= 0; i-- {
			rel := filepath.Clean(filepath.FromSlash(applied[i]))
			if parentPath, err := safeWorkspaceEditFullPath(parentRoot, rel); err == nil {
				_ = restoreWorkerParentBackup(parentPath, backups[applied[i]])
			}
		}
	}

	changes := make([]task.WorkspaceFileChange, 0, len(applyOrder))
	var applied []string
	for _, relSlash := range applyOrder {
		decision := workerReconcileDecision{}
		for _, candidate := range decisions {
			if candidate.change.Path == relSlash {
				decision = candidate
				break
			}
		}
		rel := filepath.Clean(filepath.FromSlash(relSlash))
		parentPath, err := safeWorkspaceEditFullPath(parentRoot, rel)
		if err != nil {
			rollback(applied)
			return nil, err
		}
		childPath, err := safeWorkspaceEditFullPath(childRoot, rel)
		if err != nil {
			rollback(applied)
			return nil, err
		}
		switch decision.change.Action {
		case "delete":
			if err := os.Remove(parentPath); err != nil && !os.IsNotExist(err) {
				rollback(applied)
				return nil, err
			}
		case "add", "update":
			info, err := os.Lstat(childPath)
			if err != nil {
				rollback(applied)
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				rollback(applied)
				return nil, fmt.Errorf("worker reconcile cannot auto-apply symlink %s", relSlash)
			}
			if err := copyFile(childPath, parentPath, info.Mode().Perm()); err != nil {
				rollback(applied)
				return nil, err
			}
		default:
			rollback(applied)
			return nil, fmt.Errorf("unsupported worker reconcile action: %s", decision.change.Action)
		}
		afterExists, afterHash, err := fileHash(parentPath)
		if err != nil {
			rollback(applied)
			return nil, err
		}
		changes = append(changes, task.WorkspaceFileChange{
			Path:         relSlash,
			Action:       decision.change.Action,
			BeforeExists: decision.change.ParentExists,
			AfterExists:  afterExists,
			BeforeSHA256: decision.change.ParentSHA256,
			AfterSHA256:  afterHash,
		})
		applied = append(applied, relSlash)
	}
	return changes, nil
}

func markWorkerReconcileUnsafeParentPaths(parentRoot string, decisions []workerReconcileDecision) {
	for i := range decisions {
		if !decisions[i].apply {
			continue
		}
		if _, err := safeWorkspaceEditFullPath(parentRoot, decisions[i].change.Path); err != nil {
			decisions[i].change.Status = "conflict"
			decisions[i].change.Summary = fmt.Sprintf("Parent path is not safe for automatic worker reconcile: %v", err)
			decisions[i].apply = false
		}
	}
}

func (s *Service) workerReconcileView(parentSpec task.Spec, contract task.WorkerContract, workspace task.WorkerWorkspace, settlement task.WorkerSettlement) (task.WorkerReconcile, *task.WorkspaceEditRecord, error) {
	existing, err := s.Store.LoadWorkerReconcile(contract.ParentTaskID, contract.WorkerID)
	if err != nil {
		existing = task.WorkerReconcile{
			SchemaVersion: task.SchemaVersion,
			ReconcileID:   task.NewID("REC"),
			WorkerID:      contract.WorkerID,
			ParentTaskID:  contract.ParentTaskID,
			ChildTaskID:   contract.ChildTaskID,
			CreatedAt:     task.Now(),
		}
	}
	existing.ChildTaskID = contract.ChildTaskID
	existing.Role = contract.Role
	existing.Mode = workerContractReconcileMode(contract)
	existing.SettlementStatus = settlement.Status
	existing.SettlementSettledAt = settlement.SettledAt
	existing.UpdatedAt = task.Now()
	existing.EvidenceRefs = workerReconcileRefs(contract, workspace)
	if strings.TrimSpace(existing.WorkspaceEditRef) != "" {
		existing.EvidenceRefs = uniqueStrings(append(existing.EvidenceRefs, existing.WorkspaceEditRef))
	}
	existing.PartialApply = false
	existing.ParentTakeoverRequired = false
	existing.ParentTakeoverSummary = ""
	existing.ParentTakeoverRefs = nil

	if settlement.Status != "accepted" {
		existing.Status = "pending"
		existing.ChangeCount = 0
		existing.AppliedCount = 0
		existing.ConflictCount = 0
		existing.FileChanges = nil
		existing.WorkspaceEditRef = ""
		existing.Summary = workerReconcileSummary(existing.Status, 0, 0, 0)
		return existing, nil, nil
	}
	if workspace.EffectiveMode == "" || workspace.EffectiveMode == "shared_workspace" {
		existing.Status = "shared_workspace"
		existing.ChangeCount = 0
		existing.AppliedCount = 0
		existing.ConflictCount = 0
		existing.FileChanges = nil
		existing.WorkspaceEditRef = ""
		existing.Summary = workerReconcileSummary(existing.Status, 0, 0, 0)
		if existing.ReconciledAt == "" {
			existing.ReconciledAt = task.Now()
		}
		return existing, nil, nil
	}
	if settlement.SettledAt != "" && existing.Status != "" && existing.SettlementSettledAt == settlement.SettledAt && isTerminalWorkerReconcileStatus(existing.Status) {
		return existing, nil, nil
	}
	if strings.TrimSpace(workspace.BaselineRef) == "" {
		existing.Status = "failed"
		existing.ChangeCount = 0
		existing.AppliedCount = 0
		existing.ConflictCount = 0
		existing.FileChanges = nil
		existing.WorkspaceEditRef = ""
		existing.Summary = "Worker reconcile baseline is missing for an isolated child workspace."
		existing.ReconciledAt = task.Now()
		markWorkerReconcileParentTakeover(&existing, contract, workspace, "Parent must inspect the child workspace because isolated reconcile has no baseline to prove safe apply.")
		return existing, nil, nil
	}

	baseline, err := s.Store.LoadWorkerBaseline(contract.ParentTaskID, contract.WorkerID)
	if err != nil {
		return task.WorkerReconcile{}, nil, err
	}
	parentManifest, err := collectWorkerManifest(parentSpec.WorkspaceRoot)
	if err != nil {
		return task.WorkerReconcile{}, nil, err
	}
	childManifest, err := collectWorkerManifest(workspace.WorkspaceRoot)
	if err != nil {
		return task.WorkerReconcile{}, nil, err
	}

	decisions := buildWorkerReconcileDecisions(existing.Mode, baselineEntriesToManifest(baseline.Entries), parentManifest, childManifest)
	markWorkerReconcileUnsafeParentPaths(parentSpec.WorkspaceRoot, decisions)
	existing.ChangeCount = len(decisions)
	existing.FileChanges = nil
	existing.AppliedCount = 0
	existing.ConflictCount = 0
	existing.WorkspaceEditRef = ""
	existing.ReconciledAt = task.Now()

	if len(decisions) == 0 {
		existing.Status = "noop"
		existing.Summary = workerReconcileSummary(existing.Status, 0, 0, 0)
		return existing, nil, nil
	}

	for i := range decisions {
		if decisions[i].change.Status == "conflict" {
			existing.ConflictCount++
		}
	}
	if existing.Mode == "artifact_only" {
		existing.Status = "recorded"
		for _, decision := range decisions {
			existing.FileChanges = append(existing.FileChanges, decision.change)
		}
		existing.Summary = workerReconcileSummary(existing.Status, existing.ChangeCount, 0, 0)
		return existing, nil, nil
	}
	if existing.ConflictCount > 0 {
		for i := range decisions {
			if decisions[i].apply {
				decisions[i].change.Status = "blocked"
				decisions[i].change.Summary = "Not applied because another isolated child change conflicted with parent drift."
				decisions[i].apply = false
			}
			existing.FileChanges = append(existing.FileChanges, decisions[i].change)
		}
		existing.Status = "conflict"
		existing.Summary = workerReconcileSummary(existing.Status, existing.ChangeCount, 0, existing.ConflictCount)
		markWorkerReconcileParentTakeover(&existing, contract, workspace, "Parent must inspect or take over the child workspace before any conflicted isolated changes can be applied.")
		return existing, nil, nil
	}

	appliedFileChanges, err := applyWorkerReconcileChanges(parentSpec.WorkspaceRoot, workspace.WorkspaceRoot, decisions)
	if err != nil {
		existing.Status = "failed"
		existing.Summary = fmt.Sprintf("%s: %v", workerReconcileSummary(existing.Status, existing.ChangeCount, 0, 0), err)
		for i := range decisions {
			if decisions[i].apply {
				decisions[i].change.Status = "failed"
				decisions[i].change.Summary = fmt.Sprintf("Auto-apply failed: %v", err)
			}
			existing.FileChanges = append(existing.FileChanges, decisions[i].change)
		}
		markWorkerReconcileParentTakeover(&existing, contract, workspace, "Parent must inspect or take over the child workspace because automatic reconcile failed.")
		return existing, nil, nil
	}

	existing.Status = "applied"
	existing.AppliedCount = len(appliedFileChanges)
	existing.AppliedAt = task.Now()
	for i := range decisions {
		if decisions[i].apply {
			decisions[i].change.Status = "applied"
			decisions[i].change.Summary = "Applied isolated child change back into the parent workspace."
		}
		existing.FileChanges = append(existing.FileChanges, decisions[i].change)
	}
	record := &task.WorkspaceEditRecord{
		SchemaVersion: task.SchemaVersion,
		EditRecordID:  task.NewID("EDITREC"),
		EditID:        task.NewID("EDIT"),
		TaskID:        contract.ParentTaskID,
		TS:            task.Now(),
		Kind:          "worker_reconcile",
		Status:        "applied",
		ProviderMode:  "worker_runtime",
		Summary:       workerReconcileSummary(existing.Status, existing.ChangeCount, existing.AppliedCount, 0),
		FileChanges:   appliedFileChanges,
		ReplaySafety:  workspaceEditReplaySafety("worker_reconcile"),
	}
	existing.WorkspaceEditRef = artifact.WorkspaceEditRecordRef(record.EditRecordID)
	existing.EvidenceRefs = uniqueStrings(append(existing.EvidenceRefs, existing.WorkspaceEditRef))
	existing.Summary = workerReconcileSummary(existing.Status, existing.ChangeCount, existing.AppliedCount, 0)
	return existing, record, nil
}

func mergeWorkerRuntimeIntoContract(contract *task.WorkerContract, workspace task.WorkerWorkspace, settlement task.WorkerSettlement, result task.WorkerResult, reconcile task.WorkerReconcile) {
	contract.WorkspaceRoot = workspace.WorkspaceRoot
	contract.WorkspaceMode = workspace.EffectiveMode
	contract.WorkspaceStatus = workspace.Status
	contract.WorkspaceRef = artifact.WorkerWorkspaceRef(contract.WorkerID)
	contract.SettlementStatus = settlement.Status
	contract.SettlementSummary = settlement.Summary
	contract.SettlementRef = artifact.WorkerSettlementRef(contract.WorkerID)
	contract.ResultSummary = result.Summary
	contract.ResultRef = artifact.WorkerResultRef(contract.WorkerID)
	contract.CompletionStatus = result.CompletionStatus
	contract.ReviewStatus = result.ReviewStatus
	contract.VerificationStatus = result.VerificationStatus
	contract.ReconcileMode = reconcile.Mode
	contract.ReconcileStatus = reconcile.Status
	contract.ReconcileSummary = reconcile.Summary
	contract.ReconcileRef = artifact.WorkerReconcileRef(contract.WorkerID)
	contract.EvidenceScore = result.EvidenceScore
	contract.EvidenceGrade = result.EvidenceGrade
	contract.MissingEvidence = append([]string(nil), result.MissingEvidence...)
	contract.Verified = result.Verified
	contract.ReviewClear = result.ReviewClear
	contract.HandoffPresent = result.HandoffPresent
	contract.CriteriaClosed = result.CriteriaClosed
	contract.SettlementAccepted = result.SettlementAccepted
	contract.ReconcileClean = result.ReconcileClean
	contract.ParentActionUnresolved = result.ParentActionUnresolved
	contract.ConflictCount = result.ConflictCount
	contract.TrustedForParentCompletion = result.TrustedForParentCompletion
}

func (s *Service) ensureWorkerRuntimeArtifacts(ctx context.Context, parentSpec task.Spec, contract task.WorkerContract, childStatus task.StatusSnapshot) (task.WorkerContract, task.WorkerWorkspace, task.WorkerSettlement, task.WorkerReconcile, string, error) {
	workspace := s.workerWorkspaceView(parentSpec, contract)
	workspace.ChildTaskID = contract.ChildTaskID
	settlement := s.workerSettlementView(contract, childStatus)
	result := s.workerResultView(contract, childStatus, settlement)
	reconcile, editRecord, err := s.workerReconcileView(parentSpec, contract, workspace, settlement)
	if err != nil {
		return task.WorkerContract{}, task.WorkerWorkspace{}, task.WorkerSettlement{}, task.WorkerReconcile{}, "", err
	}
	if editRecord != nil {
		if err := s.Store.AppendWorkspaceEdit(*editRecord); err != nil {
			return task.WorkerContract{}, task.WorkerWorkspace{}, task.WorkerSettlement{}, task.WorkerReconcile{}, "", err
		}
	}

	releaseSummary := ""
	if workerContractAutoReleaseOnSuccess(s.Config, contract) && settlement.Status == "accepted" && workspace.Status == "prepared" && reconcile.Status != "conflict" && reconcile.Status != "failed" {
		if err := s.removeWorkerWorkspace(ctx, parentSpec.WorkspaceRoot, workspace); err != nil {
			workspace.Status = "release_failed"
			workspace.ReleaseSummary = fmt.Sprintf("Failed to release child workspace: %v", err)
			releaseSummary = workspace.ReleaseSummary
		} else {
			workspace.Status = "released"
			workspace.ReleasedAt = task.Now()
			workspace.ReleaseSummary = "Released isolated child workspace after accepted settlement."
			releaseSummary = workspace.ReleaseSummary
			if err := s.rebindChildTaskWorkspaceRoot(contract.ChildTaskID, parentSpec.WorkspaceRoot); err != nil {
				return task.WorkerContract{}, task.WorkerWorkspace{}, task.WorkerSettlement{}, task.WorkerReconcile{}, "", err
			}
		}
		workspace.UpdatedAt = task.Now()
	}

	result = scoreWorkerResultEvidence(result, settlement, reconcile)
	mergeWorkerRuntimeIntoContract(&contract, workspace, settlement, result, reconcile)
	contract.LastReconciledAt = task.Now()
	contract.UpdatedAt = latestTimestamp(contract.UpdatedAt, contract.LastReconciledAt)

	if err := s.Store.SaveWorkerWorkspace(workspace); err != nil {
		return task.WorkerContract{}, task.WorkerWorkspace{}, task.WorkerSettlement{}, task.WorkerReconcile{}, "", err
	}
	if err := s.Store.SaveWorkerSettlement(settlement); err != nil {
		return task.WorkerContract{}, task.WorkerWorkspace{}, task.WorkerSettlement{}, task.WorkerReconcile{}, "", err
	}
	if err := s.Store.SaveWorkerResult(result); err != nil {
		return task.WorkerContract{}, task.WorkerWorkspace{}, task.WorkerSettlement{}, task.WorkerReconcile{}, "", err
	}
	if err := s.Store.SaveWorkerReconcile(reconcile); err != nil {
		return task.WorkerContract{}, task.WorkerWorkspace{}, task.WorkerSettlement{}, task.WorkerReconcile{}, "", err
	}
	if err := s.Store.SaveWorkerContract(contract); err != nil {
		return task.WorkerContract{}, task.WorkerWorkspace{}, task.WorkerSettlement{}, task.WorkerReconcile{}, "", err
	}
	return contract, workspace, settlement, reconcile, releaseSummary, nil
}

func (s *Service) removeWorkerWorkspace(ctx context.Context, parentWorkspaceRoot string, workspace task.WorkerWorkspace) error {
	switch workspace.EffectiveMode {
	case "", "shared_workspace":
		return nil
	case "snapshot_copy":
		baseDir := workerWorkspaceBaseDirFromRecord(workspace)
		if baseDir == "" {
			return nil
		}
		return os.RemoveAll(baseDir)
	case "git_worktree":
		if workspace.RepoRoot == "" {
			return nil
		}
		if _, err := runCommand(ctx, parentWorkspaceRoot, "git", "-C", parentWorkspaceRoot, "worktree", "remove", "--force", workspace.RepoRoot); err != nil {
			return err
		}
		baseDir := workerWorkspaceBaseDirFromRecord(workspace)
		if baseDir != "" {
			_ = os.RemoveAll(baseDir)
		}
		return nil
	default:
		return fmt.Errorf("unsupported worker workspace mode: %s", workspace.EffectiveMode)
	}
}

func (s *Service) appendWorkerReconcileEvents(parentTaskID string, previous, current task.WorkerContract, settlement task.WorkerSettlement, reconcile task.WorkerReconcile, releaseSummary string) error {
	state, err := s.loadStateOrRecover(parentTaskID)
	if err != nil {
		return err
	}
	appendEvent := func(typ, summary string, refs []string) error {
		event := newEvent(parentTaskID, state, typ, summary, refs)
		if err := s.Store.AppendEvent(event); err != nil {
			return err
		}
		state.LastEventRef = artifact.EventRef(event.EventID)
		state.UpdatedAt = task.Now()
		return s.Store.SaveState(state)
	}
	if previous.SettlementStatus != current.SettlementStatus || previous.SettlementSummary != current.SettlementSummary {
		eventType := "worker_reconciled"
		if isTerminalWorkerSettlementStatus(current.SettlementStatus) {
			eventType = "worker_settled"
		}
		if err := appendEvent(eventType, current.SettlementSummary, []string{
			filepath.ToSlash(filepath.Join("workers", current.WorkerID+".json")),
			current.SettlementRef,
		}); err != nil {
			return err
		}
	}
	if previous.ReconcileStatus != current.ReconcileStatus || previous.ReconcileSummary != current.ReconcileSummary {
		eventType := "worker_reconciled"
		switch current.ReconcileStatus {
		case "applied":
			eventType = "worker_reconcile_applied"
		case "recorded":
			eventType = "worker_reconcile_recorded"
		case "conflict":
			eventType = "worker_reconcile_conflict"
		case "failed":
			eventType = "worker_reconcile_failed"
		}
		if err := appendEvent(eventType, current.ReconcileSummary, []string{
			filepath.ToSlash(filepath.Join("workers", current.WorkerID+".json")),
			current.ReconcileRef,
		}); err != nil {
			return err
		}
	}
	if previous.WorkspaceStatus != current.WorkspaceStatus && strings.TrimSpace(releaseSummary) != "" {
		eventType := "worker_workspace_released"
		if current.WorkspaceStatus == "release_failed" {
			eventType = "worker_workspace_release_failed"
		}
		if err := appendEvent(eventType, releaseSummary, []string{
			filepath.ToSlash(filepath.Join("workers", current.WorkerID+".json")),
			current.WorkspaceRef,
		}); err != nil {
			return err
		}
	}
	return nil
}

func isTerminalWorkerSettlementStatus(status string) bool {
	switch status {
	case "accepted", "failed", "aborted":
		return true
	default:
		return false
	}
}

func isTerminalWorkerReconcileStatus(status string) bool {
	switch status {
	case "shared_workspace", "noop", "recorded", "applied", "conflict", "failed":
		return true
	default:
		return false
	}
}
