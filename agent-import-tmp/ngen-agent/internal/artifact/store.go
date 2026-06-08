package artifact

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ngen/internal/task"
)

type Store struct {
	WorkspaceRoot string
	StateDir      string
	MemoryFile    string
}

const (
	stateLoadRetryAttempts = 25
	stateLoadRetryDelay    = 10 * time.Millisecond
)

func validateArtifactSegment(kind, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", kind)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be a single artifact path segment: %q", kind, value)
	}
	if value == "." || value == ".." || filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("%s must be a single artifact path segment: %q", kind, value)
	}
	if strings.ContainsAny(value, `/\:`) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must not contain path separators or drive syntax: %q", kind, value)
	}
	if strings.HasSuffix(value, ".json") || strings.HasSuffix(value, ".jsonl") {
		return fmt.Errorf("%s must not include artifact file suffixes: %q", kind, value)
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%s contains unsupported character %q: %q", kind, r, value)
	}
	return nil
}

func NewStore(workspaceRoot, stateDir string) *Store {
	return &Store{
		WorkspaceRoot: workspaceRoot,
		StateDir:      stateDir,
		MemoryFile:    filepath.ToSlash(filepath.Join(stateDir, "memory", "MEMORY.md")),
	}
}

func (s *Store) StateRoot() string {
	return filepath.Join(s.WorkspaceRoot, s.StateDir)
}

func (s *Store) ProjectRoot() string {
	return filepath.Join(s.StateRoot(), "project")
}

func (s *Store) MissionsRoot() string {
	return filepath.Join(s.StateRoot(), "missions")
}

func (s *Store) MissionRoot(missionID string) string {
	return filepath.Join(s.MissionsRoot(), missionID)
}

func (s *Store) TaskRoot(taskID string) string {
	return filepath.Join(s.StateRoot(), "tasks", taskID)
}

func (s *Store) WatchRoot() string {
	return filepath.Join(s.StateRoot(), "watches")
}

func (s *Store) RuntimeRoot() string {
	return filepath.Join(s.StateRoot(), "runtime")
}

func (s *Store) RoleRoot() string {
	return filepath.Join(s.StateRoot(), "roles")
}

func (s *Store) SessionRoot() string {
	return filepath.Join(s.StateRoot(), "sessions")
}

func (s *Store) MemoryRoot() string {
	return filepath.Join(s.StateRoot(), "memory")
}

func (s *Store) MemoryMarkdownPath() string {
	memoryFile := strings.TrimSpace(s.MemoryFile)
	if memoryFile == "" {
		memoryFile = filepath.ToSlash(filepath.Join(s.StateDir, "memory", "MEMORY.md"))
	}
	return filepath.Join(s.WorkspaceRoot, filepath.FromSlash(memoryFile))
}

func (s *Store) MemoryMarkdownRef() string {
	memoryFile := strings.TrimSpace(s.MemoryFile)
	if memoryFile == "" {
		memoryFile = filepath.ToSlash(filepath.Join(s.StateDir, "memory", "MEMORY.md"))
	}
	return "workspace:" + filepath.ToSlash(memoryFile)
}

func (s *Store) WorkerRoot(taskID string) string {
	return filepath.Join(s.TaskRoot(taskID), "workers")
}

func (s *Store) WorkerRuntimeRoot(taskID string) string {
	return filepath.Join(s.TaskRoot(taskID), "worker_runtime")
}

func (s *Store) EnsureWorkspaceLayout() error {
	dirs := []string{
		s.StateRoot(),
		s.ProjectRoot(),
		s.MissionsRoot(),
		filepath.Join(s.StateRoot(), "tasks"),
		s.WatchRoot(),
		s.RuntimeRoot(),
		s.RoleRoot(),
		s.SessionRoot(),
		s.MemoryRoot(),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) EnsureTaskLayout(taskID string) error {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return err
	}
	dirs := []string{
		s.TaskRoot(taskID),
		filepath.Join(s.TaskRoot(taskID), "criteria"),
		filepath.Join(s.TaskRoot(taskID), "completion"),
		filepath.Join(s.TaskRoot(taskID), "commands"),
		filepath.Join(s.TaskRoot(taskID), "continuity"),
		filepath.Join(s.TaskRoot(taskID), "sprint"),
		filepath.Join(s.TaskRoot(taskID), "verification"),
		filepath.Join(s.TaskRoot(taskID), "reviews"),
		filepath.Join(s.TaskRoot(taskID), "harness"),
		filepath.Join(s.TaskRoot(taskID), "context"),
		filepath.Join(s.TaskRoot(taskID), "diagnostics"),
		filepath.Join(s.TaskRoot(taskID), "checkpoints"),
		filepath.Join(s.TaskRoot(taskID), "multica"),
		s.WorkerRoot(taskID),
		s.WorkerRuntimeRoot(taskID),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) EnsureMissionLayout(missionID string) error {
	if err := validateArtifactSegment("mission_id", missionID); err != nil {
		return err
	}
	return os.MkdirAll(s.MissionRoot(missionID), 0o755)
}

func (s *Store) saveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

func (s *Store) loadJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (s *Store) appendJSONL(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (s *Store) readJSONLLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func decodeJSONLLines[T any](lines []string) ([]T, error) {
	items := make([]T, 0, len(lines))
	for _, line := range lines {
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) writeMarkdown(path string, data []byte) error {
	return writeFileAtomic(path, data, 0o644)
}

func (s *Store) readMarkdown(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (s *Store) SaveTask(spec task.Spec) error {
	if err := validateArtifactSegment("task_id", spec.TaskID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.TaskRoot(spec.TaskID), "task.json"), spec)
}

func (s *Store) LoadTask(taskID string) (task.Spec, error) {
	var spec task.Spec
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return spec, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "task.json"), &spec)
	return spec, err
}

func (s *Store) ListTaskIDs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.StateRoot(), "tasks"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if validateArtifactSegment("task_id", entry.Name()) != nil {
			continue
		}
		ready, err := s.taskReadyForDiscovery(entry.Name())
		if err != nil {
			return nil, err
		}
		if !ready {
			continue
		}
		ids = append(ids, entry.Name())
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) taskReadyForDiscovery(taskID string) (bool, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return false, err
	}
	for _, name := range []string{"task.json", "state.json"} {
		info, err := os.Stat(filepath.Join(s.TaskRoot(taskID), name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		if info.IsDir() {
			return false, fmt.Errorf("%s is a directory", filepath.Join(s.TaskRoot(taskID), name))
		}
	}
	return true, nil
}

func (s *Store) SavePlan(plan task.Plan) error {
	if err := validateArtifactSegment("task_id", plan.TaskID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.TaskRoot(plan.TaskID), "plan.json"), plan)
}

func (s *Store) LoadPlan(taskID string) (task.Plan, error) {
	var plan task.Plan
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return plan, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "plan.json"), &plan)
	return plan, err
}

func (s *Store) AppendPlanMutation(record task.PlanMutationRecord) error {
	if err := validateArtifactSegment("task_id", record.TaskID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.TaskRoot(record.TaskID), "plan_updates.jsonl"), record)
}

func (s *Store) ReadPlanMutations(taskID string) ([]task.PlanMutationRecord, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "plan_updates.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.PlanMutationRecord](lines)
}

func (s *Store) SaveProject(project task.Project) error {
	return s.saveJSON(filepath.Join(s.ProjectRoot(), "project.json"), project)
}

func (s *Store) LoadProject() (task.Project, error) {
	var project task.Project
	err := s.loadJSON(filepath.Join(s.ProjectRoot(), "project.json"), &project)
	return project, err
}

func (s *Store) SaveRoleContract(contract task.RoleContract) error {
	contract, err := task.ValidateRoleContract(contract)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.RoleRoot(), 0o755); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.RoleRoot(), contract.RoleID+".json"), contract)
}

func (s *Store) LoadRoleContract(roleID string) (task.RoleContract, error) {
	roleID, _, _, err := task.NormalizeWorkerRole(roleID)
	if err != nil {
		return task.RoleContract{}, err
	}
	var contract task.RoleContract
	if err := s.loadJSON(filepath.Join(s.RoleRoot(), roleID+".json"), &contract); err != nil {
		return task.RoleContract{}, err
	}
	return task.ValidateRoleContract(contract)
}

func (s *Store) ReadRoleContracts() (map[string]task.RoleContract, error) {
	entries, err := os.ReadDir(s.RoleRoot())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]task.RoleContract{}, nil
		}
		return nil, err
	}
	out := make(map[string]task.RoleContract, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var contract task.RoleContract
		path := filepath.Join(s.RoleRoot(), entry.Name())
		if err := s.loadJSON(path, &contract); err != nil {
			return nil, fmt.Errorf("load role contract %s: %w", entry.Name(), err)
		}
		contract, err := task.ValidateRoleContract(contract)
		if err != nil {
			return nil, fmt.Errorf("validate role contract %s: %w", entry.Name(), err)
		}
		out[contract.RoleID] = contract
	}
	return out, nil
}

func (s *Store) SaveMission(mission task.Mission) error {
	if err := validateArtifactSegment("mission_id", mission.MissionID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.MissionRoot(mission.MissionID), "mission.json"), mission)
}

func (s *Store) LoadMission(missionID string) (task.Mission, error) {
	var mission task.Mission
	if err := validateArtifactSegment("mission_id", missionID); err != nil {
		return mission, err
	}
	err := s.loadJSON(filepath.Join(s.MissionRoot(missionID), "mission.json"), &mission)
	return mission, err
}

func (s *Store) ListMissionIDs() ([]string, error) {
	entries, err := os.ReadDir(s.MissionsRoot())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if validateArtifactSegment("mission_id", entry.Name()) != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.MissionRoot(entry.Name()), "mission.json")); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		ids = append(ids, entry.Name())
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) SaveMissionValidationContract(contract task.MissionValidationContract) error {
	if err := validateArtifactSegment("mission_id", contract.MissionID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.MissionRoot(contract.MissionID), "validation_contract.json"), contract)
}

func (s *Store) LoadMissionValidationContract(missionID string) (task.MissionValidationContract, error) {
	var contract task.MissionValidationContract
	if err := validateArtifactSegment("mission_id", missionID); err != nil {
		return contract, err
	}
	err := s.loadJSON(filepath.Join(s.MissionRoot(missionID), "validation_contract.json"), &contract)
	return contract, err
}

func (s *Store) SaveMissionFeatures(features task.MissionFeatureSet) error {
	if err := validateArtifactSegment("mission_id", features.MissionID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.MissionRoot(features.MissionID), "features.json"), features)
}

func (s *Store) LoadMissionFeatures(missionID string) (task.MissionFeatureSet, error) {
	var features task.MissionFeatureSet
	if err := validateArtifactSegment("mission_id", missionID); err != nil {
		return features, err
	}
	err := s.loadJSON(filepath.Join(s.MissionRoot(missionID), "features.json"), &features)
	return features, err
}

func (s *Store) SaveMissionMilestones(milestones task.MissionMilestoneSet) error {
	if err := validateArtifactSegment("mission_id", milestones.MissionID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.MissionRoot(milestones.MissionID), "milestones.json"), milestones)
}

func (s *Store) LoadMissionMilestones(missionID string) (task.MissionMilestoneSet, error) {
	var milestones task.MissionMilestoneSet
	if err := validateArtifactSegment("mission_id", missionID); err != nil {
		return milestones, err
	}
	err := s.loadJSON(filepath.Join(s.MissionRoot(missionID), "milestones.json"), &milestones)
	return milestones, err
}

func (s *Store) SaveMissionNotes(missionID string, data []byte) error {
	if err := validateArtifactSegment("mission_id", missionID); err != nil {
		return err
	}
	return s.writeMarkdown(filepath.Join(s.MissionRoot(missionID), "notes.md"), data)
}

func (s *Store) AppendMissionValidationRun(run task.MissionValidationRun) error {
	if err := validateArtifactSegment("mission_id", run.MissionID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.MissionRoot(run.MissionID), "validation_runs.jsonl"), run)
}

func (s *Store) ReadMissionValidationRuns(missionID string) ([]task.MissionValidationRun, error) {
	if err := validateArtifactSegment("mission_id", missionID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.MissionRoot(missionID), "validation_runs.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.MissionValidationRun](lines)
}

func (s *Store) AppendMissionMetricsRecord(record task.MissionMetricsRecord) error {
	if err := validateArtifactSegment("mission_id", record.MissionID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.MissionRoot(record.MissionID), "metrics.jsonl"), record)
}

func (s *Store) ReadMissionMetrics(missionID string) ([]task.MissionMetricsRecord, error) {
	if err := validateArtifactSegment("mission_id", missionID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.MissionRoot(missionID), "metrics.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.MissionMetricsRecord](lines)
}

func (s *Store) AppendProjectMutation(record task.ProjectMutationRecord) error {
	return s.appendJSONL(filepath.Join(s.ProjectRoot(), "project_updates.jsonl"), record)
}

func (s *Store) ReadProjectMutations() ([]task.ProjectMutationRecord, error) {
	lines, err := s.readJSONLLines(filepath.Join(s.ProjectRoot(), "project_updates.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.ProjectMutationRecord](lines)
}

func (s *Store) SaveState(state task.State) error {
	if err := validateArtifactSegment("task_id", state.TaskID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.TaskRoot(state.TaskID), "state.json"), state)
}

func (s *Store) LoadState(taskID string) (task.State, error) {
	var state task.State
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return state, err
	}
	path := filepath.Join(s.TaskRoot(taskID), "state.json")
	var err error
	for attempt := 0; attempt < stateLoadRetryAttempts; attempt++ {
		err = s.loadJSON(path, &state)
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			return state, err
		}
		if attempt == stateLoadRetryAttempts-1 {
			break
		}
		time.Sleep(stateLoadRetryDelay)
	}
	return state, err
}

func (s *Store) SaveBaseline(baseline task.Baseline) error {
	if err := validateArtifactSegment("task_id", baseline.TaskID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.TaskRoot(baseline.TaskID), "baseline.json"), baseline)
}

func (s *Store) LoadBaseline(taskID string) (task.Baseline, error) {
	var baseline task.Baseline
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return baseline, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "baseline.json"), &baseline)
	return baseline, err
}

func (s *Store) HasBaseline(taskID string) bool {
	if validateArtifactSegment("task_id", taskID) != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.TaskRoot(taskID), "baseline.json"))
	return err == nil
}

func (s *Store) SaveProgress(taskID string, data []byte) error {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return err
	}
	return s.writeMarkdown(filepath.Join(s.TaskRoot(taskID), "progress.md"), data)
}

func (s *Store) LoadProgress(taskID string) ([]byte, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	return s.readMarkdown(filepath.Join(s.TaskRoot(taskID), "progress.md"))
}

func (s *Store) SaveHandoff(taskID string, data []byte) error {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return err
	}
	return s.writeMarkdown(filepath.Join(s.TaskRoot(taskID), "handoff.md"), data)
}

func (s *Store) LoadHandoff(taskID string) ([]byte, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	return s.readMarkdown(filepath.Join(s.TaskRoot(taskID), "handoff.md"))
}

func (s *Store) HandoffExists(taskID string) bool {
	if validateArtifactSegment("task_id", taskID) != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.TaskRoot(taskID), "handoff.md"))
	return err == nil
}

func (s *Store) SaveCriteria(snapshot task.CriteriaSnapshot) error {
	if err := validateArtifactSegment("task_id", snapshot.TaskID); err != nil {
		return err
	}
	latest := filepath.Join(s.TaskRoot(snapshot.TaskID), "criteria", "latest.json")
	history := filepath.Join(s.TaskRoot(snapshot.TaskID), "criteria", "history.jsonl")
	if err := s.saveJSON(latest, snapshot); err != nil {
		return err
	}
	return s.appendJSONL(history, snapshot)
}

func (s *Store) LoadCriteria(taskID string) (task.CriteriaSnapshot, error) {
	var snapshot task.CriteriaSnapshot
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return snapshot, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "criteria", "latest.json"), &snapshot)
	return snapshot, err
}

func (s *Store) ReadCriteria(taskID string) ([]task.CriteriaSnapshot, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "criteria", "history.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.CriteriaSnapshot](lines)
}

func (s *Store) SaveCompletion(report task.CompletionReport) error {
	if err := validateArtifactSegment("task_id", report.TaskID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.TaskRoot(report.TaskID), "completion", "latest.json"), report)
}

func (s *Store) LoadCompletion(taskID string) (task.CompletionReport, error) {
	var report task.CompletionReport
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return report, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "completion", "latest.json"), &report)
	return report, err
}

func (s *Store) CompletionExists(taskID string) bool {
	if validateArtifactSegment("task_id", taskID) != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.TaskRoot(taskID), "completion", "latest.json"))
	return err == nil
}

func (s *Store) SaveVerification(report task.VerificationReport) error {
	if err := validateArtifactSegment("task_id", report.TaskID); err != nil {
		return err
	}
	latest := filepath.Join(s.TaskRoot(report.TaskID), "verification", "latest.json")
	history := filepath.Join(s.TaskRoot(report.TaskID), "verification", "history.jsonl")
	if err := s.saveJSON(latest, report); err != nil {
		return err
	}
	return s.appendJSONL(history, report)
}

func (s *Store) LoadVerification(taskID string) (task.VerificationReport, error) {
	var report task.VerificationReport
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return report, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "verification", "latest.json"), &report)
	return report, err
}

func (s *Store) SaveReview(report task.ReviewReport) error {
	if err := validateArtifactSegment("task_id", report.TaskID); err != nil {
		return err
	}
	latest := filepath.Join(s.TaskRoot(report.TaskID), "reviews", "latest.json")
	history := filepath.Join(s.TaskRoot(report.TaskID), "reviews", "history.jsonl")
	if err := s.saveJSON(latest, report); err != nil {
		return err
	}
	return s.appendJSONL(history, report)
}

func (s *Store) LoadReview(taskID string) (task.ReviewReport, error) {
	var report task.ReviewReport
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return report, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "reviews", "latest.json"), &report)
	return report, err
}

func (s *Store) SaveHarnessEvaluation(eval task.HarnessEvaluation) error {
	if err := validateArtifactSegment("task_id", eval.TaskID); err != nil {
		return err
	}
	latest := filepath.Join(s.TaskRoot(eval.TaskID), "harness", "latest.json")
	history := filepath.Join(s.TaskRoot(eval.TaskID), "harness", "history.jsonl")
	if err := s.saveJSON(latest, eval); err != nil {
		return err
	}
	return s.appendJSONL(history, eval)
}

func (s *Store) LoadHarnessEvaluation(taskID string) (task.HarnessEvaluation, error) {
	var eval task.HarnessEvaluation
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return eval, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "harness", "latest.json"), &eval)
	return eval, err
}

func (s *Store) ReadHarnessEvaluations(taskID string) ([]task.HarnessEvaluation, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "harness", "history.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.HarnessEvaluation](lines)
}

func (s *Store) AppendProviderUsage(record task.ProviderUsageRecord) error {
	if err := validateArtifactSegment("task_id", record.TaskID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.TaskRoot(record.TaskID), "provider_usage.jsonl"), record)
}

func (s *Store) ReadProviderUsage(taskID string) ([]task.ProviderUsageRecord, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "provider_usage.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.ProviderUsageRecord](lines)
}

func (s *Store) SaveMulticaRunMetadata(metadata task.MulticaRunMetadata) error {
	if err := validateArtifactSegment("task_id", metadata.TaskID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.TaskRoot(metadata.TaskID), "multica", "run_metadata.json"), metadata)
}

func (s *Store) LoadMulticaRunMetadata(taskID string) (task.MulticaRunMetadata, error) {
	var metadata task.MulticaRunMetadata
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return metadata, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "multica", "run_metadata.json"), &metadata)
	return metadata, err
}

func (s *Store) SaveWorkspaceGuidance(guidance task.WorkspaceGuidanceArtifact) error {
	if err := validateArtifactSegment("task_id", guidance.TaskID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.TaskRoot(guidance.TaskID), "multica", "workspace_guidance.json"), guidance)
}

func (s *Store) LoadWorkspaceGuidance(taskID string) (task.WorkspaceGuidanceArtifact, error) {
	var guidance task.WorkspaceGuidanceArtifact
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return guidance, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "multica", "workspace_guidance.json"), &guidance)
	return guidance, err
}

func (s *Store) SaveContextSummary(summary task.ContextSummary) error {
	if err := validateArtifactSegment("task_id", summary.TaskID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.TaskRoot(summary.TaskID), "context", "latest-pack.json"), summary)
}

func (s *Store) LoadContextSummary(taskID string) (task.ContextSummary, error) {
	var summary task.ContextSummary
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return summary, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "context", "latest-pack.json"), &summary)
	return summary, err
}

func (s *Store) SaveContextCompactionSummary(taskID string, data []byte) error {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return err
	}
	return s.writeMarkdown(filepath.Join(s.TaskRoot(taskID), "context", "summary.md"), data)
}

func (s *Store) LoadContextCompactionSummary(taskID string) ([]byte, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	return s.readMarkdown(filepath.Join(s.TaskRoot(taskID), "context", "summary.md"))
}

func (s *Store) SaveContinuity(snapshot task.ContinuitySnapshot) error {
	if err := validateArtifactSegment("task_id", snapshot.TaskID); err != nil {
		return err
	}
	latest := filepath.Join(s.TaskRoot(snapshot.TaskID), "continuity", "latest.json")
	history := filepath.Join(s.TaskRoot(snapshot.TaskID), "continuity", "history.jsonl")
	if err := s.saveJSON(latest, snapshot); err != nil {
		return err
	}
	return s.appendJSONL(history, snapshot)
}

func (s *Store) LoadContinuity(taskID string) (task.ContinuitySnapshot, error) {
	var snapshot task.ContinuitySnapshot
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return snapshot, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "continuity", "latest.json"), &snapshot)
	return snapshot, err
}

func (s *Store) ReadContinuity(taskID string) ([]task.ContinuitySnapshot, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "continuity", "history.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.ContinuitySnapshot](lines)
}

func (s *Store) ContinuityExists(taskID string) bool {
	if validateArtifactSegment("task_id", taskID) != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.TaskRoot(taskID), "continuity", "latest.json"))
	return err == nil
}

func (s *Store) SaveSprint(snapshot task.SprintSnapshot) error {
	if err := validateArtifactSegment("task_id", snapshot.TaskID); err != nil {
		return err
	}
	latest := filepath.Join(s.TaskRoot(snapshot.TaskID), "sprint", "latest.json")
	history := filepath.Join(s.TaskRoot(snapshot.TaskID), "sprint", "history.jsonl")
	if err := s.saveJSON(latest, snapshot); err != nil {
		return err
	}
	return s.appendJSONL(history, snapshot)
}

func (s *Store) LoadSprint(taskID string) (task.SprintSnapshot, error) {
	var snapshot task.SprintSnapshot
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return snapshot, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "sprint", "latest.json"), &snapshot)
	return snapshot, err
}

func (s *Store) ReadSprint(taskID string) ([]task.SprintSnapshot, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "sprint", "history.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.SprintSnapshot](lines)
}

func (s *Store) SprintExists(taskID string) bool {
	if validateArtifactSegment("task_id", taskID) != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.TaskRoot(taskID), "sprint", "latest.json"))
	return err == nil
}

func (s *Store) SaveDiagnostic(diag task.Diagnostic) error {
	if err := validateArtifactSegment("task_id", diag.TaskID); err != nil {
		return err
	}
	if err := validateArtifactSegment("diagnostic_id", diag.DiagnosticID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.TaskRoot(diag.TaskID), "diagnostics", diag.DiagnosticID+".json"), diag)
}

func (s *Store) SaveQualityDiagnostic(diag task.QualityDiagnostic) error {
	if err := validateArtifactSegment("task_id", diag.TaskID); err != nil {
		return err
	}
	latest := filepath.Join(s.TaskRoot(diag.TaskID), "diagnostics", "quality-latest.json")
	history := filepath.Join(s.TaskRoot(diag.TaskID), "diagnostics", "quality-history.jsonl")
	if err := s.saveJSON(latest, diag); err != nil {
		return err
	}
	return s.appendJSONL(history, diag)
}

func (s *Store) LoadQualityDiagnostic(taskID string) (task.QualityDiagnostic, error) {
	var diag task.QualityDiagnostic
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return diag, err
	}
	err := s.loadJSON(filepath.Join(s.TaskRoot(taskID), "diagnostics", "quality-latest.json"), &diag)
	return diag, err
}

func (s *Store) ReadQualityDiagnostics(taskID string) ([]task.QualityDiagnostic, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "diagnostics", "quality-history.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.QualityDiagnostic](lines)
}

func (s *Store) AppendEvent(event task.Event) error {
	if err := validateArtifactSegment("task_id", event.TaskID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.TaskRoot(event.TaskID), "events.jsonl"), event)
}

func (s *Store) ReadEvents(taskID string) ([]task.Event, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "events.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.Event](lines)
}

func (s *Store) AppendFinding(finding task.Finding) error {
	if err := validateArtifactSegment("task_id", finding.TaskID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.TaskRoot(finding.TaskID), "findings.jsonl"), finding)
}

func (s *Store) ReadFindings(taskID string) ([]task.Finding, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "findings.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.Finding](lines)
}

func (s *Store) AppendApproval(record task.ApprovalRecord) error {
	if err := validateArtifactSegment("task_id", record.TaskID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.TaskRoot(record.TaskID), "approvals.jsonl"), record)
}

func (s *Store) ReadApprovals(taskID string) ([]task.ApprovalRecord, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "approvals.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.ApprovalRecord](lines)
}

func (s *Store) AppendInputRequest(record task.InputRequestRecord) error {
	if err := validateArtifactSegment("task_id", record.TaskID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.TaskRoot(record.TaskID), "input_requests.jsonl"), record)
}

func (s *Store) ReadInputRequests(taskID string) ([]task.InputRequestRecord, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "input_requests.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.InputRequestRecord](lines)
}

func (s *Store) AppendWorkspaceEdit(record task.WorkspaceEditRecord) error {
	if err := validateArtifactSegment("task_id", record.TaskID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.TaskRoot(record.TaskID), "workspace_edits.jsonl"), record)
}

func (s *Store) ReadWorkspaceEdits(taskID string) ([]task.WorkspaceEditRecord, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "workspace_edits.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.WorkspaceEditRecord](lines)
}

func (s *Store) AppendCommandRun(record task.CommandRunRecord) error {
	if err := validateArtifactSegment("task_id", record.TaskID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.TaskRoot(record.TaskID), "command_runs.jsonl"), record)
}

func (s *Store) ReadCommandRuns(taskID string) ([]task.CommandRunRecord, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.TaskRoot(taskID), "command_runs.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.CommandRunRecord](lines)
}

func (s *Store) SaveCommandOutput(taskID, commandID string, stdout, stderr []byte) (string, string, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return "", "", err
	}
	if err := validateArtifactSegment("command_id", commandID); err != nil {
		return "", "", err
	}
	dir := filepath.Join(s.TaskRoot(taskID), "commands", commandID)
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stderrPath := filepath.Join(dir, "stderr.txt")
	if err := s.writeMarkdown(stdoutPath, stdout); err != nil {
		return "", "", err
	}
	if err := s.writeMarkdown(stderrPath, stderr); err != nil {
		return "", "", err
	}
	return filepath.ToSlash(filepath.Join("commands", commandID, "stdout.txt")),
		filepath.ToSlash(filepath.Join("commands", commandID, "stderr.txt")),
		nil
}

func (s *Store) SaveCheckpoint(cp task.Checkpoint) error {
	if err := validateArtifactSegment("task_id", cp.TaskID); err != nil {
		return err
	}
	if err := validateArtifactSegment("checkpoint_id", cp.CheckpointID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.TaskRoot(cp.TaskID), "checkpoints", checkpointFileName(cp.CheckpointID)), cp)
}

func checkpointFileName(checkpointID string) string {
	if strings.HasSuffix(checkpointID, ".json") {
		return checkpointID
	}
	return checkpointID + ".json"
}

func (s *Store) LoadLatestCheckpoint(taskID string) (task.Checkpoint, error) {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return task.Checkpoint{}, err
	}
	dir := filepath.Join(s.TaskRoot(taskID), "checkpoints")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return task.Checkpoint{}, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if validateArtifactSegment("checkpoint_id", id) != nil {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return task.Checkpoint{}, os.ErrNotExist
	}
	sort.Strings(names)
	var cp task.Checkpoint
	err = s.loadJSON(filepath.Join(dir, names[len(names)-1]), &cp)
	return cp, err
}

func (s *Store) SaveWatch(watch task.Watch) error {
	if err := validateArtifactSegment("watch_id", watch.WatchID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.WatchRoot(), watch.WatchID+".json"), watch)
}

func (s *Store) LoadWatch(watchID string) (task.Watch, error) {
	var watch task.Watch
	if err := validateArtifactSegment("watch_id", watchID); err != nil {
		return watch, err
	}
	err := s.loadJSON(filepath.Join(s.WatchRoot(), watchID+".json"), &watch)
	return watch, err
}

func (s *Store) ListWatches() ([]task.Watch, error) {
	entries, err := os.ReadDir(s.WatchRoot())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	watches := make([]task.Watch, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if validateArtifactSegment("watch_id", id) != nil {
			continue
		}
		var watch task.Watch
		if err := s.loadJSON(filepath.Join(s.WatchRoot(), entry.Name()), &watch); err != nil {
			return nil, err
		}
		watches = append(watches, watch)
	}
	sort.Slice(watches, func(i, j int) bool {
		return watches[i].WatchID < watches[j].WatchID
	})
	return watches, nil
}

func (s *Store) SaveSession(session task.Session) error {
	if err := validateArtifactSegment("session_id", session.SessionID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.SessionRoot(), session.SessionID+".json"), session)
}

func (s *Store) LoadSession(sessionID string) (task.Session, error) {
	var session task.Session
	if err := validateArtifactSegment("session_id", sessionID); err != nil {
		return session, err
	}
	err := s.loadJSON(filepath.Join(s.SessionRoot(), sessionID+".json"), &session)
	return session, err
}

func (s *Store) ListSessions() ([]task.Session, error) {
	entries, err := os.ReadDir(s.SessionRoot())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	sessions := make([]task.Session, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if validateArtifactSegment("session_id", id) != nil {
			continue
		}
		var session task.Session
		if err := s.loadJSON(filepath.Join(s.SessionRoot(), entry.Name()), &session); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].SessionID < sessions[j].SessionID
	})
	return sessions, nil
}

func (s *Store) AppendSessionMessage(msg task.SessionMessage) error {
	if err := validateArtifactSegment("session_id", msg.SessionID); err != nil {
		return err
	}
	return s.appendJSONL(filepath.Join(s.SessionRoot(), msg.SessionID+".messages.jsonl"), msg)
}

func (s *Store) ReadSessionMessages(sessionID string) ([]task.SessionMessage, error) {
	if err := validateArtifactSegment("session_id", sessionID); err != nil {
		return nil, err
	}
	lines, err := s.readJSONLLines(filepath.Join(s.SessionRoot(), sessionID+".messages.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.SessionMessage](lines)
}

func (s *Store) SaveWorkerContract(contract task.WorkerContract) error {
	if err := validateArtifactSegment("parent_task_id", contract.ParentTaskID); err != nil {
		return err
	}
	if err := validateArtifactSegment("worker_id", contract.WorkerID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.WorkerRoot(contract.ParentTaskID), contract.WorkerID+".json"), contract)
}

func (s *Store) LoadWorkerContract(parentTaskID, workerID string) (task.WorkerContract, error) {
	var contract task.WorkerContract
	if err := validateArtifactSegment("parent_task_id", parentTaskID); err != nil {
		return contract, err
	}
	if err := validateArtifactSegment("worker_id", workerID); err != nil {
		return contract, err
	}
	err := s.loadJSON(filepath.Join(s.WorkerRoot(parentTaskID), workerID+".json"), &contract)
	return contract, err
}

func (s *Store) ListWorkerContracts(parentTaskID string) ([]task.WorkerContract, error) {
	if err := validateArtifactSegment("parent_task_id", parentTaskID); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.WorkerRoot(parentTaskID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	contracts := make([]task.WorkerContract, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if validateArtifactSegment("worker_id", id) != nil {
			continue
		}
		var contract task.WorkerContract
		if err := s.loadJSON(filepath.Join(s.WorkerRoot(parentTaskID), entry.Name()), &contract); err != nil {
			return nil, err
		}
		contracts = append(contracts, contract)
	}
	sort.Slice(contracts, func(i, j int) bool {
		return contracts[i].WorkerID < contracts[j].WorkerID
	})
	return contracts, nil
}

func (s *Store) SaveWorkerSettlement(settlement task.WorkerSettlement) error {
	if err := validateArtifactSegment("parent_task_id", settlement.ParentTaskID); err != nil {
		return err
	}
	if err := validateArtifactSegment("worker_id", settlement.WorkerID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.WorkerRuntimeRoot(settlement.ParentTaskID), settlement.WorkerID+".settlement.json"), settlement)
}

func (s *Store) LoadWorkerSettlement(parentTaskID, workerID string) (task.WorkerSettlement, error) {
	var settlement task.WorkerSettlement
	if err := validateArtifactSegment("parent_task_id", parentTaskID); err != nil {
		return settlement, err
	}
	if err := validateArtifactSegment("worker_id", workerID); err != nil {
		return settlement, err
	}
	err := s.loadJSON(filepath.Join(s.WorkerRuntimeRoot(parentTaskID), workerID+".settlement.json"), &settlement)
	return settlement, err
}

func (s *Store) SaveWorkerWorkspace(workspace task.WorkerWorkspace) error {
	if err := validateArtifactSegment("parent_task_id", workspace.ParentTaskID); err != nil {
		return err
	}
	if err := validateArtifactSegment("worker_id", workspace.WorkerID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.WorkerRuntimeRoot(workspace.ParentTaskID), workspace.WorkerID+".workspace.json"), workspace)
}

func (s *Store) LoadWorkerWorkspace(parentTaskID, workerID string) (task.WorkerWorkspace, error) {
	var workspace task.WorkerWorkspace
	if err := validateArtifactSegment("parent_task_id", parentTaskID); err != nil {
		return workspace, err
	}
	if err := validateArtifactSegment("worker_id", workerID); err != nil {
		return workspace, err
	}
	err := s.loadJSON(filepath.Join(s.WorkerRuntimeRoot(parentTaskID), workerID+".workspace.json"), &workspace)
	return workspace, err
}

func (s *Store) SaveWorkerBaseline(baseline task.WorkerWorkspaceBaseline) error {
	if err := validateArtifactSegment("parent_task_id", baseline.ParentTaskID); err != nil {
		return err
	}
	if err := validateArtifactSegment("worker_id", baseline.WorkerID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.WorkerRuntimeRoot(baseline.ParentTaskID), baseline.WorkerID+".baseline.json"), baseline)
}

func (s *Store) LoadWorkerBaseline(parentTaskID, workerID string) (task.WorkerWorkspaceBaseline, error) {
	var baseline task.WorkerWorkspaceBaseline
	if err := validateArtifactSegment("parent_task_id", parentTaskID); err != nil {
		return baseline, err
	}
	if err := validateArtifactSegment("worker_id", workerID); err != nil {
		return baseline, err
	}
	err := s.loadJSON(filepath.Join(s.WorkerRuntimeRoot(parentTaskID), workerID+".baseline.json"), &baseline)
	return baseline, err
}

func (s *Store) SaveWorkerReconcile(reconcile task.WorkerReconcile) error {
	if err := validateArtifactSegment("parent_task_id", reconcile.ParentTaskID); err != nil {
		return err
	}
	if err := validateArtifactSegment("worker_id", reconcile.WorkerID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.WorkerRuntimeRoot(reconcile.ParentTaskID), reconcile.WorkerID+".reconcile.json"), reconcile)
}

func (s *Store) LoadWorkerReconcile(parentTaskID, workerID string) (task.WorkerReconcile, error) {
	var reconcile task.WorkerReconcile
	if err := validateArtifactSegment("parent_task_id", parentTaskID); err != nil {
		return reconcile, err
	}
	if err := validateArtifactSegment("worker_id", workerID); err != nil {
		return reconcile, err
	}
	err := s.loadJSON(filepath.Join(s.WorkerRuntimeRoot(parentTaskID), workerID+".reconcile.json"), &reconcile)
	return reconcile, err
}

func (s *Store) SaveWorkerResult(result task.WorkerResult) error {
	if err := validateArtifactSegment("parent_task_id", result.ParentTaskID); err != nil {
		return err
	}
	if err := validateArtifactSegment("worker_id", result.WorkerID); err != nil {
		return err
	}
	return s.saveJSON(filepath.Join(s.WorkerRuntimeRoot(result.ParentTaskID), result.WorkerID+".result.json"), result)
}

func (s *Store) LoadWorkerResult(parentTaskID, workerID string) (task.WorkerResult, error) {
	var result task.WorkerResult
	if err := validateArtifactSegment("parent_task_id", parentTaskID); err != nil {
		return result, err
	}
	if err := validateArtifactSegment("worker_id", workerID); err != nil {
		return result, err
	}
	err := s.loadJSON(filepath.Join(s.WorkerRuntimeRoot(parentTaskID), workerID+".result.json"), &result)
	return result, err
}

func (s *Store) LoadWorkerContractByChildTask(childTaskID string) (task.WorkerContract, error) {
	if err := validateArtifactSegment("child_task_id", childTaskID); err != nil {
		return task.WorkerContract{}, err
	}
	taskEntries, err := os.ReadDir(filepath.Join(s.StateRoot(), "tasks"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return task.WorkerContract{}, os.ErrNotExist
		}
		return task.WorkerContract{}, err
	}
	for _, taskEntry := range taskEntries {
		if !taskEntry.IsDir() {
			continue
		}
		if validateArtifactSegment("task_id", taskEntry.Name()) != nil {
			continue
		}
		contracts, err := s.ListWorkerContracts(taskEntry.Name())
		if err != nil {
			return task.WorkerContract{}, err
		}
		for _, contract := range contracts {
			if contract.ChildTaskID == childTaskID {
				return contract, nil
			}
		}
	}
	return task.WorkerContract{}, os.ErrNotExist
}

func (s *Store) AppendMemoryEntry(entry task.MemoryEntry) error {
	return s.appendJSONL(filepath.Join(s.MemoryRoot(), "entries.jsonl"), entry)
}

func (s *Store) ReadMemoryEntries() ([]task.MemoryEntry, error) {
	lines, err := s.readJSONLLines(filepath.Join(s.MemoryRoot(), "entries.jsonl"))
	if err != nil {
		return nil, err
	}
	return decodeJSONLLines[task.MemoryEntry](lines)
}

func (s *Store) SaveMemoryMarkdown(data []byte) error {
	return s.writeMarkdown(s.MemoryMarkdownPath(), data)
}

func (s *Store) LoadMemoryMarkdown() ([]byte, error) {
	data, err := s.readMarkdown(s.MemoryMarkdownPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte("# Workspace Memory\n\n## Recent Memory Entries\n- No promoted memory yet.\n\n## Consolidated Topics\n- No recurring topics yet.\n"), nil
		}
		return nil, err
	}
	return data, nil
}

func (s *Store) ExportHandoff(taskID string, w io.Writer) error {
	if err := validateArtifactSegment("task_id", taskID); err != nil {
		return err
	}
	data, err := s.LoadHandoff(taskID)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func ApprovalRecordRef(id string) string {
	return fmt.Sprintf("approvals.jsonl#approval_record_id=%s", id)
}

func InputRequestRecordRef(id string) string {
	return fmt.Sprintf("input_requests.jsonl#input_record_id=%s", id)
}

func WorkspaceEditRecordRef(id string) string {
	return fmt.Sprintf("workspace_edits.jsonl#edit_record_id=%s", id)
}

func CommandRunRecordRef(id string) string {
	return fmt.Sprintf("command_runs.jsonl#command_record_id=%s", id)
}

func ProviderUsageRecordRef(id string) string {
	return fmt.Sprintf("provider_usage.jsonl#usage_record_id=%s", id)
}

func EventRef(id string) string {
	return fmt.Sprintf("events.jsonl#event_id=%s", id)
}

func MemoryEntryRef(id string) string {
	return fmt.Sprintf("workspace:.ngen/memory/entries.jsonl#entry_id=%s", id)
}

func (s *Store) MemoryEntryRef(id string) string {
	return fmt.Sprintf("workspace:%s#entry_id=%s", filepath.ToSlash(filepath.Join(s.StateDir, "memory", "entries.jsonl")), id)
}

func HarnessEvaluationRef(id string) string {
	return fmt.Sprintf("harness/history.jsonl#harness_eval_id=%s", id)
}

func MissionValidationRunRef(id string) string {
	return fmt.Sprintf("validation_runs.jsonl#validation_run_id=%s", id)
}

func PlanMutationRef(id string) string {
	return fmt.Sprintf("plan_updates.jsonl#mutation_id=%s", id)
}

func ProjectMutationRef(id string) string {
	return fmt.Sprintf("project_updates.jsonl#mutation_id=%s", id)
}

func WorkerSettlementRef(workerID string) string {
	return filepath.ToSlash(filepath.Join("worker_runtime", workerID+".settlement.json"))
}

func WorkerWorkspaceRef(workerID string) string {
	return filepath.ToSlash(filepath.Join("worker_runtime", workerID+".workspace.json"))
}

func WorkerBaselineRef(workerID string) string {
	return filepath.ToSlash(filepath.Join("worker_runtime", workerID+".baseline.json"))
}

func WorkerReconcileRef(workerID string) string {
	return filepath.ToSlash(filepath.Join("worker_runtime", workerID+".reconcile.json"))
}

func WorkerResultRef(workerID string) string {
	return filepath.ToSlash(filepath.Join("worker_runtime", workerID+".result.json"))
}
