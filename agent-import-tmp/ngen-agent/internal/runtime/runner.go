package ngenrt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ngen/internal/artifact"
	"ngen/internal/provider"
	"ngen/internal/review"
	"ngen/internal/task"
	"ngen/internal/verify"
)

type Service struct {
	Store  *artifact.Store
	Config task.Config
	verify *verify.Pipeline
}

var workspaceHintPathPattern = regexp.MustCompile(`(?m)([A-Za-z0-9_./*-]+\.[A-Za-z0-9_*]+)`)
var observationUndefinedNamePattern = regexp.MustCompile(`undefined:\s*([A-Za-z_][A-Za-z0-9_]*)`)
var observationNameTokenPattern = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\b`)
var criterionLiteralPattern = regexp.MustCompile("[`\"]([^`\"]+)[`\"]")
var criterionCodeTokenPattern = regexp.MustCompile(`(?m)(--[A-Za-z0-9_-]+|[A-Za-z_][A-Za-z0-9_-]{2,})`)
var backtickCommandPattern = regexp.MustCompile("`([^`\\n]+)`")
var shellLikeCommandPattern = regexp.MustCompile("(?im)^\\s*(?:[-*]\\s*)?(?:run|execute|invoke|call)\\s+([A-Za-z0-9_./-]+(?:\\s+[^\\n`]+)?)\\s*$")

var multicaIssueIDPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

const commandOutputMaxBytes = 1024 * 1024

type cappedOutputBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newCappedOutputBuffer(limit int) *cappedOutputBuffer {
	return &cappedOutputBuffer{limit: limit}
}

func (b *cappedOutputBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	switch {
	case remaining <= 0 && len(p) > 0:
		b.truncated = true
	case len(p) > remaining:
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
	default:
		_, _ = b.buf.Write(p)
	}
	return len(p), nil
}

func (b *cappedOutputBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *cappedOutputBuffer) String() string {
	return b.buf.String()
}

func (b *cappedOutputBuffer) Truncated() bool {
	return b.truncated
}

func New(workspaceRoot string, cfg task.Config) *Service {
	store := artifact.NewStore(workspaceRoot, cfg.StateDir)
	store.MemoryFile = cfg.Memory.File
	return &Service{
		Store:  store,
		Config: cfg,
		verify: verify.New(cfg),
	}
}

func (s *Service) Create(ctx context.Context, tf task.TaskFile) (task.Spec, error) {
	_ = ctx
	tf = task.NormalizeTaskFile(tf, s.Store.WorkspaceRoot, s.Config)
	if err := task.ValidateTaskFile(tf); err != nil {
		return task.Spec{}, err
	}
	if err := s.Store.EnsureWorkspaceLayout(); err != nil {
		return task.Spec{}, err
	}
	if err := s.ensureRoleContracts(); err != nil {
		return task.Spec{}, err
	}
	spec := task.NewSpec(tf)
	if err := s.Store.EnsureTaskLayout(spec.TaskID); err != nil {
		return task.Spec{}, err
	}
	plan := task.NewBootstrapPlan(spec)
	state := task.NewInitialState(spec)
	criteria := task.NewInitialCriteria(spec)
	checkpoint := task.NewInitialCheckpoint(spec)
	checkpoint.WorkspaceSnapshot = verify.CaptureWorkspaceSnapshot(ctx, spec.WorkspaceRoot)

	if err := s.Store.SaveTask(spec); err != nil {
		return task.Spec{}, err
	}
	if err := s.Store.SavePlan(plan); err != nil {
		return task.Spec{}, err
	}
	if err := s.Store.SaveState(state); err != nil {
		return task.Spec{}, err
	}
	if err := s.Store.SaveCriteria(criteria); err != nil {
		return task.Spec{}, err
	}
	if err := s.Store.SaveCheckpoint(checkpoint); err != nil {
		return task.Spec{}, err
	}
	event := task.Event{
		SchemaVersion: task.SchemaVersion,
		EventID:       task.NewID("EVT"),
		TaskID:        spec.TaskID,
		TS:            task.Now(),
		Phase:         state.Phase,
		State:         state.State,
		Type:          "task_created",
		Summary:       "Created task skeleton.",
		Refs:          []string{"task.json", "plan.json", "state.json"},
	}
	if err := s.Store.AppendEvent(event); err != nil {
		return task.Spec{}, err
	}
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return task.Spec{}, err
	}
	if err := s.TrackTaskInProject(ctx, spec); err != nil {
		return task.Spec{}, err
	}
	if err := s.syncTaskNarrative(spec, state, "Task created. Run the task to capture baseline and execute verification."); err != nil {
		return task.Spec{}, err
	}
	return spec, nil
}

func (s *Service) execute(ctx context.Context, taskID string, session *task.Session) (task.StatusSnapshot, []task.Event, error) {
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	if state.State == task.StateDone || state.State == task.StateAborted {
		snapshot, snapErr := s.Status(ctx, taskID)
		return snapshot, nil, snapErr
	}
	if state.State == task.StateBlocked && (state.StatusReasonCode == "blocked_policy" || state.StatusReasonCode == "blocked_missing_input") {
		snapshot, snapErr := s.Status(ctx, taskID)
		return snapshot, nil, snapErr
	}

	var emitted []task.Event
	plan, err := s.Store.LoadPlan(taskID)
	if err != nil {
		return task.StatusSnapshot{}, nil, err
	}

	if !s.Store.HasBaseline(taskID) {
		baseline := s.verify.CaptureBaseline(ctx, spec)
		if err := s.Store.SaveBaseline(baseline); err != nil {
			return task.StatusSnapshot{}, nil, err
		}
		plan = markStep(plan, "STEP-001", "done")
		plan = markStep(plan, "STEP-002", "in_progress")
		if err := s.Store.SavePlan(plan); err != nil {
			return task.StatusSnapshot{}, nil, err
		}
		state.Phase = task.PhasePlan
		state.CurrentStepID = "STEP-002"
		state.UpdatedAt = task.Now()
		event := newEvent(spec.TaskID, state, "baseline_captured", "Captured workspace baseline.", []string{"baseline.json"})
		if err := s.Store.AppendEvent(event); err != nil {
			return task.StatusSnapshot{}, nil, err
		}
		emitted = append(emitted, event)
		state.LastEventRef = artifact.EventRef(event.EventID)
		planEvent := newEvent(spec.TaskID, state, "plan_updated", "Plan advanced after baseline capture.", []string{"plan.json"})
		if err := s.Store.AppendEvent(planEvent); err != nil {
			return task.StatusSnapshot{}, nil, err
		}
		emitted = append(emitted, planEvent)
		state.LastEventRef = artifact.EventRef(planEvent.EventID)
	}

	state.Phase = task.PhaseExecute
	state.State = task.StateActive
	state.StatusReasonCode = ""
	state.StatusDetailRef = ""
	execEvent := newEvent(spec.TaskID, state, "state_changed", "Entered Execute phase.", nil)
	if err := s.Store.AppendEvent(execEvent); err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	emitted = append(emitted, execEvent)
	state.LastEventRef = artifact.EventRef(execEvent.EventID)

	report, verifyEvents, repairAttempts, err := s.runVerificationSequence(ctx, spec, &state, session)
	if err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	emitted = append(emitted, verifyEvents...)

	criteria := s.criteriaFromEvidence(spec, report)
	if report.Status == "passed" && spec.Kind == task.KindCoding && provider.SupportsWorkspaceEdit(s.Config.Provider) && !criteriaAllMet(criteria) {
		repairedReport, repairedCriteria, repairEvents, err := s.runCriteriaRepairSequence(ctx, spec, &state, report, criteria, session, repairAttempts)
		if err != nil {
			return task.StatusSnapshot{}, nil, err
		}
		report = repairedReport
		criteria = repairedCriteria
		emitted = append(emitted, repairEvents...)
	}
	if report.Status == "passed" && !criteriaAllMet(criteria) && explicitCommandTaskRequested(spec) {
		commandReport, commandCriteria, commandEvents, err := s.runExplicitCommandTask(ctx, spec, &state, report, criteria)
		if err != nil {
			return task.StatusSnapshot{}, nil, err
		}
		report = commandReport
		criteria = commandCriteria
		emitted = append(emitted, commandEvents...)
	}
	if err := s.Store.SaveCriteria(criteria); err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	provisionalReview := task.ReviewReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		Status:        "pending",
		Summary:       "Review has not completed yet.",
	}
	provisionalCompletion := task.CompletionReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		Status:        "rejected",
		Summary:       "Completion has not been evaluated yet.",
	}
	state, err = s.refreshTaskPlan(spec, state)
	if err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	// Reuse in-memory truth after a successful save instead of immediately
	// re-statting handoff.md on mounted filesystems where visibility can lag.
	if err := s.Store.SaveHandoff(spec.TaskID, []byte(s.renderHandoff(spec, state, report, criteria, provisionalReview, provisionalCompletion))); err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	handoffExists := true
	reviewInput, err := s.buildReviewInput(ctx, spec, report, handoffExists, criteria)
	if err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	reportReview, reviewFindings := review.EvaluateWithContext(reviewInput)
	if len(reviewFindings) > 0 {
		if err := s.appendReviewFindings(reviewFindings); err != nil {
			return task.StatusSnapshot{}, nil, err
		}
	}
	state.Phase = task.PhaseReview
	reviewEvent := newEvent(spec.TaskID, state, "review_completed", reportReview.Summary, append([]string{"reviews/latest.json"}, reportReview.BlockingFindingRefs...))
	if err := s.Store.SaveReview(reportReview); err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	if err := s.Store.AppendEvent(reviewEvent); err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	emitted = append(emitted, reviewEvent)
	state.LastEventRef = artifact.EventRef(reviewEvent.EventID)
	state.LastReviewRef = "reviews/latest.json"

	if report.Status == "passed" && reportReview.Status == "clear" {
		criteria = s.criteriaWithReviewEvidence(spec, criteria, reportReview.Summary)
		if err := s.Store.SaveCriteria(criteria); err != nil {
			return task.StatusSnapshot{}, nil, err
		}
	}

	completion := s.buildCompletion(spec, criteria, reportReview, handoffExists)
	if err := s.Store.SaveCompletion(completion); err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	state.LastCompletionRef = "completion/latest.json"

	switch {
	case completion.Status == "accepted":
		state.State = task.StateDone
		state.StatusReasonCode = ""
		state.StatusDetailRef = ""
		doneEvent := newEvent(spec.TaskID, state, "done", "Done gate passed.", []string{"completion/latest.json", "handoff.md"})
		if err := s.Store.AppendEvent(doneEvent); err != nil {
			return task.StatusSnapshot{}, nil, err
		}
		emitted = append(emitted, doneEvent)
		state.LastEventRef = artifact.EventRef(doneEvent.EventID)
		plan = markStep(plan, "STEP-002", "done")
		planEvent := newEvent(spec.TaskID, state, "plan_updated", "Plan completed after the done gate passed.", []string{"plan.json"})
		if err := s.Store.AppendEvent(planEvent); err != nil {
			return task.StatusSnapshot{}, nil, err
		}
		emitted = append(emitted, planEvent)
		state.LastEventRef = artifact.EventRef(planEvent.EventID)
	case report.Status != "passed":
		state.State = task.StateFailed
		state.StatusReasonCode = "failed_verification"
		state.StatusDetailRef = "verification/latest.json"
		failEvent := newEvent(spec.TaskID, state, "failed", report.FailureSummary, []string{"verification/latest.json"})
		if err := s.Store.AppendEvent(failEvent); err != nil {
			return task.StatusSnapshot{}, nil, err
		}
		emitted = append(emitted, failEvent)
		state.LastEventRef = artifact.EventRef(failEvent.EventID)
	default:
		state.State = task.StateBlocked
		state.StatusReasonCode = "blocked_review"
		state.StatusDetailRef = "reviews/latest.json"
		rejectEvent := newEvent(spec.TaskID, state, "completion_rejected", completion.Summary, []string{"completion/latest.json"})
		if err := s.Store.AppendEvent(rejectEvent); err != nil {
			return task.StatusSnapshot{}, nil, err
		}
		emitted = append(emitted, rejectEvent)
		state.LastEventRef = artifact.EventRef(rejectEvent.EventID)
	}
	state, err = s.refreshTaskPlan(spec, state)
	if err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	if err := s.Store.SaveHandoff(spec.TaskID, []byte(s.renderHandoff(spec, state, report, criteria, reportReview, completion))); err != nil {
		return task.StatusSnapshot{}, nil, err
	}

	cp := s.nextCheckpoint(ctx, spec, state)
	if err := s.Store.SaveCheckpoint(cp); err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	state.LastCheckpointRef = filepath.ToSlash(filepath.Join("checkpoints", cp.CheckpointID+".json"))
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	if err := s.Store.SaveHandoff(spec.TaskID, []byte(s.renderHandoff(spec, state, report, criteria, reportReview, completion))); err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	if err := s.Store.SavePlan(plan); err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	if err := s.syncTaskNarrative(spec, state, completion.Summary); err != nil {
		return task.StatusSnapshot{}, nil, err
	}

	snapshot, err := s.Status(ctx, taskID)
	return snapshot, emitted, err
}

func (s *Service) runVerificationSequence(ctx context.Context, spec task.Spec, state *task.State, session *task.Session) (task.VerificationReport, []task.Event, int, error) {
	var emitted []task.Event
	report := s.verify.Run(ctx, spec)
	verifyEvent, report, err := s.persistVerificationReport(state, report)
	if err != nil {
		return task.VerificationReport{}, nil, 0, err
	}
	emitted = append(emitted, verifyEvent)
	if report.Status == "passed" || spec.Kind != task.KindCoding || !provider.SupportsWorkspaceEdit(s.Config.Provider) {
		return report, emitted, 0, nil
	}
	repairBudget := s.codingRepairBudget()
	lastFingerprint := verificationFingerprint(report)
	repairFailures := s.recentRepairFailures(spec.TaskID, repairBudget)
	usedAttempts := 0
	for attempt := 1; attempt <= repairBudget; attempt++ {
		usedAttempts = attempt
		repaired, repairEvents, repairedOK, failure, err := s.repairCodingTask(ctx, spec, state, report, nil, session, repairFailures, attempt, repairBudget)
		if err != nil {
			return task.VerificationReport{}, emitted, usedAttempts, err
		}
		emitted = append(emitted, repairEvents...)
		repairFailures = appendRepairFailure(repairFailures, failure, repairBudget)
		if !repairedOK {
			continue
		}
		report = repaired
		if report.Status == "passed" {
			return report, emitted, usedAttempts, nil
		}
		nextFingerprint := verificationFingerprint(report)
		if nextFingerprint == lastFingerprint {
			stalledEvent := newEvent(
				spec.TaskID,
				*state,
				"workspace_edit_stalled",
				fmt.Sprintf("Verification failure repeated after bounded workspace edit attempt %d/%d.", attempt, repairBudget),
				[]string{"verification/latest.json"},
			)
			if err := s.Store.AppendEvent(stalledEvent); err != nil {
				return task.VerificationReport{}, emitted, usedAttempts, err
			}
			emitted = append(emitted, stalledEvent)
			state.LastEventRef = artifact.EventRef(stalledEvent.EventID)
			return report, emitted, usedAttempts, nil
		}
		lastFingerprint = nextFingerprint
	}
	budgetEvent := newEvent(
		spec.TaskID,
		*state,
		"workspace_edit_budget_exhausted",
		fmt.Sprintf("Reached bounded coding repair budget after %d attempts.", repairBudget),
		[]string{"verification/latest.json"},
	)
	if err := s.Store.AppendEvent(budgetEvent); err != nil {
		return task.VerificationReport{}, emitted, usedAttempts, err
	}
	emitted = append(emitted, budgetEvent)
	state.LastEventRef = artifact.EventRef(budgetEvent.EventID)
	return report, emitted, usedAttempts, nil
}

func (s *Service) runExplicitCommandTask(
	ctx context.Context,
	spec task.Spec,
	state *task.State,
	report task.VerificationReport,
	criteria task.CriteriaSnapshot,
) (task.VerificationReport, task.CriteriaSnapshot, []task.Event, error) {
	if !explicitCommandTaskRequested(spec) || s.explicitCommandTaskAttempted(spec) {
		return report, criteria, nil, nil
	}
	plan, ok := s.explicitCommandWorkspaceEditPlan(spec)
	if !ok {
		return report, criteria, nil, nil
	}
	editID := task.NewID("EDIT")
	startEvent := newEvent(spec.TaskID, *state, "workspace_edit_started", "Workspace edit started for explicit command task.", nil)
	if err := s.Store.AppendEvent(startEvent); err != nil {
		return task.VerificationReport{}, task.CriteriaSnapshot{}, nil, err
	}
	emitted := []task.Event{startEvent}
	state.LastEventRef = artifact.EventRef(startEvent.EventID)

	record, applyErr := s.applyWorkspaceEditPlan(spec, editID, plan)
	eventType := "workspace_edit_applied"
	switch {
	case applyErr != nil:
		record.Status = "failed"
		record.Summary = fmt.Sprintf("%s Apply failed: %v", record.Summary, applyErr)
		eventType = "workspace_edit_failed"
	case len(record.FileChanges) == 0:
		record.Status = "noop"
		eventType = "workspace_edit_noop"
	default:
		record.Status = "applied"
	}
	editEvent, persistErr := s.persistWorkspaceEditRecord(*state, record, eventType)
	if persistErr != nil {
		return task.VerificationReport{}, task.CriteriaSnapshot{}, emitted, persistErr
	}
	emitted = append(emitted, editEvent)
	state.LastEventRef = artifact.EventRef(editEvent.EventID)
	if applyErr != nil {
		return report, criteria, emitted, nil
	}

	commandEvents, _, err := s.runWorkspaceExecutionCommands(ctx, spec, state, plan.Commands, "post")
	if err != nil {
		return task.VerificationReport{}, task.CriteriaSnapshot{}, emitted, err
	}
	emitted = append(emitted, commandEvents...)
	if len(commandEvents) > 0 {
		state.LastEventRef = artifact.EventRef(commandEvents[len(commandEvents)-1].EventID)
	}
	refreshedReport := s.verify.Run(ctx, spec)
	verifyEvent, refreshedReport, err := s.persistVerificationReport(state, refreshedReport)
	if err != nil {
		return task.VerificationReport{}, task.CriteriaSnapshot{}, emitted, err
	}
	emitted = append(emitted, verifyEvent)
	refreshedCriteria := s.criteriaFromEvidence(spec, refreshedReport)
	return refreshedReport, refreshedCriteria, emitted, nil
}

func (s *Service) runCriteriaRepairSequence(
	ctx context.Context,
	spec task.Spec,
	state *task.State,
	report task.VerificationReport,
	criteria task.CriteriaSnapshot,
	session *task.Session,
	usedAttempts int,
) (task.VerificationReport, task.CriteriaSnapshot, []task.Event, error) {
	repairBudget := s.codingRepairBudget()
	if repairBudget <= 0 || usedAttempts >= repairBudget || criteriaAllMet(criteria) {
		return report, criteria, nil, nil
	}

	var emitted []task.Event
	lastFingerprint := criteriaFingerprint(spec, criteria)
	repairFailures := s.recentRepairFailures(spec.TaskID, repairBudget)
	for attempt := usedAttempts + 1; attempt <= repairBudget; attempt++ {
		repairContext := criteriaRepairSignal(spec, criteria)
		if report.Status != "passed" {
			repairContext = report
		}
		repaired, repairEvents, repairedOK, failure, err := s.repairCodingTask(ctx, spec, state, repairContext, &criteria, session, repairFailures, attempt, repairBudget)
		if err != nil {
			return task.VerificationReport{}, task.CriteriaSnapshot{}, emitted, err
		}
		emitted = append(emitted, repairEvents...)
		repairFailures = appendRepairFailure(repairFailures, failure, repairBudget)
		if !repairedOK {
			continue
		}
		report = repaired
		criteria = s.criteriaFromEvidence(spec, report)
		if report.Status == "passed" && criteriaAllMet(criteria) {
			return report, criteria, emitted, nil
		}
		nextFingerprint := verificationFingerprint(report)
		if report.Status == "passed" {
			nextFingerprint = criteriaFingerprint(spec, criteria)
		}
		if nextFingerprint == lastFingerprint {
			summary := fmt.Sprintf("Repair target repeated after bounded workspace edit attempt %d/%d.", attempt, repairBudget)
			if report.Status == "passed" {
				summary = fmt.Sprintf("Criteria gap repeated after bounded workspace edit attempt %d/%d.", attempt, repairBudget)
			}
			stalledEvent := newEvent(
				spec.TaskID,
				*state,
				"workspace_edit_stalled",
				summary,
				[]string{"verification/latest.json", "criteria/latest.json"},
			)
			if err := s.Store.AppendEvent(stalledEvent); err != nil {
				return task.VerificationReport{}, task.CriteriaSnapshot{}, emitted, err
			}
			emitted = append(emitted, stalledEvent)
			state.LastEventRef = artifact.EventRef(stalledEvent.EventID)
			return report, criteria, emitted, nil
		}
		lastFingerprint = nextFingerprint
	}

	budgetEvent := newEvent(
		spec.TaskID,
		*state,
		"workspace_edit_budget_exhausted",
		fmt.Sprintf("Reached bounded coding repair budget after %d attempts.", repairBudget),
		[]string{"verification/latest.json", "criteria/latest.json"},
	)
	if err := s.Store.AppendEvent(budgetEvent); err != nil {
		return task.VerificationReport{}, task.CriteriaSnapshot{}, emitted, err
	}
	emitted = append(emitted, budgetEvent)
	state.LastEventRef = artifact.EventRef(budgetEvent.EventID)
	return report, criteria, emitted, nil
}

func (s *Service) persistVerificationReport(state *task.State, report task.VerificationReport) (task.Event, task.VerificationReport, error) {
	state.Phase = task.PhaseVerify
	verifyType := "verification_passed"
	verifySummary := "Verification passed."
	if report.Status != "passed" {
		verifyType = "verification_failed"
		verifySummary = report.FailureSummary
		if verifySummary == "" {
			verifySummary = "Verification failed."
		}
	}
	verifyEvent := newEvent(report.TaskID, *state, verifyType, verifySummary, nil)
	for i := range report.Checks {
		report.Checks[i].EvidenceRefs = uniqueRefs(append(report.Checks[i].EvidenceRefs, artifact.EventRef(verifyEvent.EventID)))
	}
	if err := s.Store.SaveVerification(report); err != nil {
		return task.Event{}, task.VerificationReport{}, err
	}
	if err := s.Store.AppendEvent(verifyEvent); err != nil {
		return task.Event{}, task.VerificationReport{}, err
	}
	state.LastEventRef = artifact.EventRef(verifyEvent.EventID)
	state.LastVerificationRef = "verification/latest.json"
	return verifyEvent, report, nil
}

func (s *Service) repairCodingTask(
	ctx context.Context,
	spec task.Spec,
	state *task.State,
	failed task.VerificationReport,
	criteria *task.CriteriaSnapshot,
	session *task.Session,
	previousFailures []provider.RepairFailure,
	attempt, budget int,
) (task.VerificationReport, []task.Event, bool, *provider.RepairFailure, error) {
	var emitted []task.Event
	if criteria == nil {
		derived := s.criteriaFromEvidence(spec, failed)
		criteria = &derived
	}
	editID := task.NewID("EDIT")
	state.Phase = task.PhaseExecute
	startEvent := newEvent(
		spec.TaskID,
		*state,
		"workspace_edit_started",
		fmt.Sprintf("Workspace edit %s started for bounded repair attempt %d/%d.", editID, attempt, budget),
		nil,
	)
	if err := s.Store.AppendEvent(startEvent); err != nil {
		return task.VerificationReport{}, nil, false, nil, err
	}
	emitted = append(emitted, startEvent)
	state.LastEventRef = artifact.EventRef(startEvent.EventID)

	files, collection, err := s.collectWorkspaceEditFiles(spec, failed)
	if err != nil {
		record := task.WorkspaceEditRecord{
			SchemaVersion: task.SchemaVersion,
			EditRecordID:  task.NewID("EDITREC"),
			EditID:        editID,
			TaskID:        spec.TaskID,
			TS:            task.Now(),
			Kind:          "workspace_edit",
			Status:        "failed",
			ProviderMode:  provider.CanonicalMode(s.Config.Provider.Mode),
			Summary:       fmt.Sprintf("Workspace edit failed while collecting files: %v", err),
		}
		event, persistErr := s.persistWorkspaceEditRecord(*state, record, "workspace_edit_failed")
		if persistErr != nil {
			return task.VerificationReport{}, emitted, false, nil, persistErr
		}
		emitted = append(emitted, event)
		state.LastEventRef = artifact.EventRef(event.EventID)
		return task.VerificationReport{}, emitted, false, repairFailureFromRecord(attempt, record), nil
	}

	observations, observationEvents, err := s.runWorkspaceObservationCommands(ctx, spec, state, failed, criteria, session, previousFailures, attempt, budget, files, collection)
	if err != nil {
		return task.VerificationReport{}, emitted, false, nil, err
	}
	emitted = append(emitted, observationEvents...)
	contextPack := s.loadContextPack(spec.TaskID)
	baseline := s.loadBaseline(spec.TaskID)
	continuity := s.loadContinuity(spec.TaskID)
	sprint := s.loadSprint(spec.TaskID)
	sessionMessagesRef, sessionRecentMessages, err := s.sessionContinuity(session, 6)
	if err != nil {
		return task.VerificationReport{}, emitted, false, nil, err
	}
	plan, err := provider.GenerateWorkspaceEdit(ctx, s.Config.Provider, provider.WorkspaceEditInput{
		Task:                  spec,
		Baseline:              baseline,
		Continuity:            continuity,
		Sprint:                sprint,
		RecentVerification:    &failed,
		Criteria:              criteria,
		OpenCriteria:          openCriteria(spec, criteria),
		ContextPack:           contextPack,
		SessionMessagesRef:    sessionMessagesRef,
		SessionRecentMessages: sessionRecentMessages,
		PreviousFailures:      previousFailures,
		RepairAttempt:         attempt,
		RepairBudget:          budget,
		ExecutionBudget:       s.codingExecutionCommandBudget(),
		Collection:            collection,
		Observations:          observations,
		Files:                 files,
	})
	if err != nil {
		record := task.WorkspaceEditRecord{
			SchemaVersion: task.SchemaVersion,
			EditRecordID:  task.NewID("EDITREC"),
			EditID:        editID,
			TaskID:        spec.TaskID,
			TS:            task.Now(),
			Kind:          "workspace_edit",
			Status:        "failed",
			ProviderMode:  provider.CanonicalMode(s.Config.Provider.Mode),
			Summary:       fmt.Sprintf("Workspace edit planning failed: %v", err),
		}
		event, persistErr := s.persistWorkspaceEditRecord(*state, record, "workspace_edit_failed")
		if persistErr != nil {
			return task.VerificationReport{}, emitted, false, nil, persistErr
		}
		emitted = append(emitted, event)
		state.LastEventRef = artifact.EventRef(event.EventID)
		return task.VerificationReport{}, emitted, false, repairFailureFromRecord(attempt, record), nil
	}

	tokenUsage, promptCacheUsage := providerUsageFromWorkspaceEdit(plan)
	usageRecord, usageErr := s.appendProviderUsage(spec.TaskID, providerUsageOperationWorkspaceEdit, s.Config.Provider, tokenUsage, promptCacheUsage, []string{
		"verification/latest.json",
		"context/latest-pack.json",
		"continuity/latest.json",
		"sprint/latest.json",
	})
	if usageErr != nil {
		return task.VerificationReport{}, emitted, false, nil, usageErr
	}
	preCommands, postCommands := splitWorkspaceCommands(plan.Commands)
	executedCommands := false
	if failure := tooManyExecutionCommandsFailure(len(preCommands)+len(postCommands), s.codingExecutionCommandBudget()); failure != nil {
		return task.VerificationReport{}, emitted, false, failure, nil
	}
	if len(preCommands) > 0 {
		commandEvents, failure, err := s.runWorkspaceExecutionCommands(ctx, spec, state, preCommands, "pre")
		if err != nil {
			return task.VerificationReport{}, emitted, false, nil, err
		}
		emitted = append(emitted, commandEvents...)
		if len(commandEvents) > 0 {
			state.LastEventRef = artifact.EventRef(commandEvents[len(commandEvents)-1].EventID)
		}
		if failure != nil {
			return task.VerificationReport{}, emitted, false, failure, nil
		}
		executedCommands = true
	}

	record, applyErr := s.applyWorkspaceEditPlan(spec, editID, plan)
	record.ProviderUsageRef = providerUsageRef(usageRecord)
	record.TokenUsage = tokenUsage
	record.PromptCacheUsage = promptCacheUsage
	eventType := "workspace_edit_applied"
	switch {
	case applyErr != nil:
		record.Status = "failed"
		record.Summary = fmt.Sprintf("%s Apply failed: %v", record.Summary, applyErr)
		eventType = "workspace_edit_failed"
	case len(record.FileChanges) == 0 && len(preCommands)+len(postCommands) > 0:
		record.Status = "applied"
	case len(record.FileChanges) == 0:
		record.Status = "noop"
		eventType = "workspace_edit_noop"
	default:
		record.Status = "applied"
	}
	event, persistErr := s.persistWorkspaceEditRecord(*state, record, eventType)
	if persistErr != nil {
		return task.VerificationReport{}, emitted, false, nil, persistErr
	}
	emitted = append(emitted, event)
	state.LastEventRef = artifact.EventRef(event.EventID)
	if applyErr != nil {
		return task.VerificationReport{}, emitted, false, repairFailureFromRecord(attempt, record), nil
	}
	if len(postCommands) > 0 {
		commandEvents, failure, err := s.runWorkspaceExecutionCommands(ctx, spec, state, postCommands, "post")
		if err != nil {
			return task.VerificationReport{}, emitted, false, nil, err
		}
		emitted = append(emitted, commandEvents...)
		if len(commandEvents) > 0 {
			state.LastEventRef = artifact.EventRef(commandEvents[len(commandEvents)-1].EventID)
		}
		if failure != nil {
			return task.VerificationReport{}, emitted, false, failure, nil
		}
		executedCommands = true
	}
	if len(record.FileChanges) == 0 && !executedCommands {
		return task.VerificationReport{}, emitted, false, repairFailureFromRecord(attempt, record), nil
	}

	repaired := s.verify.Run(ctx, spec)
	verifyEvent, repaired, err := s.persistVerificationReport(state, repaired)
	if err != nil {
		return task.VerificationReport{}, emitted, false, nil, err
	}
	emitted = append(emitted, verifyEvent)
	return repaired, emitted, true, nil, nil
}

func (s *Service) persistWorkspaceEditRecord(state task.State, record task.WorkspaceEditRecord, eventType string) (task.Event, error) {
	if err := s.Store.AppendWorkspaceEdit(record); err != nil {
		return task.Event{}, err
	}
	event := newEvent(record.TaskID, state, eventType, record.Summary, []string{artifact.WorkspaceEditRecordRef(record.EditRecordID)})
	if err := s.Store.AppendEvent(event); err != nil {
		return task.Event{}, err
	}
	return event, nil
}

func (s *Service) applyWorkspaceEditPlan(spec task.Spec, editID string, plan provider.WorkspaceEditPlan) (task.WorkspaceEditRecord, error) {
	record := task.WorkspaceEditRecord{
		SchemaVersion: task.SchemaVersion,
		EditRecordID:  task.NewID("EDITREC"),
		EditID:        editID,
		TaskID:        spec.TaskID,
		TS:            task.Now(),
		Kind:          "workspace_edit",
		Status:        "applied",
		ProviderMode:  provider.CanonicalMode(s.Config.Provider.Mode),
		Summary:       strings.TrimSpace(plan.Summary),
		ReplaySafety:  workspaceEditReplaySafety("workspace_edit"),
	}
	if record.Summary == "" {
		record.Summary = "Workspace edit applied."
	}
	if strings.TrimSpace(plan.Patch) != "" {
		return s.applyWorkspacePatch(spec, record, plan.Patch)
	}

	writeMap := make(map[string]string, len(plan.Writes))
	for _, write := range plan.Writes {
		rel, err := s.normalizeWorkspaceEditPath(write.Path)
		if err != nil {
			return record, err
		}
		if err := validateWorkspaceEditConstraints(spec, rel); err != nil {
			return record, err
		}
		writeMap[rel] = write.Content
	}
	deleteSet := make(map[string]struct{}, len(plan.Deletes))
	for _, candidate := range plan.Deletes {
		rel, err := s.normalizeWorkspaceEditPath(candidate)
		if err != nil {
			return record, err
		}
		if err := validateWorkspaceEditConstraints(spec, rel); err != nil {
			return record, err
		}
		if _, exists := writeMap[rel]; exists {
			return record, fmt.Errorf("workspace edit path %s cannot be both deleted and written", rel)
		}
		deleteSet[rel] = struct{}{}
	}

	var deletePaths []string
	for rel := range deleteSet {
		deletePaths = append(deletePaths, rel)
	}
	sort.Strings(deletePaths)
	for _, rel := range deletePaths {
		full, err := safeWorkspaceEditFullPath(spec.WorkspaceRoot, rel)
		if err != nil {
			return record, err
		}
		beforeExists, beforeHash, err := fileHash(full)
		if err != nil {
			return record, err
		}
		if beforeExists {
			if err := os.Remove(full); err != nil {
				return record, err
			}
		}
		if beforeExists {
			record.FileChanges = append(record.FileChanges, task.WorkspaceFileChange{
				Path:         rel,
				Action:       "delete",
				BeforeExists: true,
				AfterExists:  false,
				BeforeSHA256: beforeHash,
			})
		}
	}

	var writePaths []string
	for rel := range writeMap {
		writePaths = append(writePaths, rel)
	}
	sort.Strings(writePaths)
	for _, rel := range writePaths {
		full, err := safeWorkspaceEditFullPath(spec.WorkspaceRoot, rel)
		if err != nil {
			return record, err
		}
		beforeExists, beforeHash, err := fileHash(full)
		if err != nil {
			return record, err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return record, err
		}
		if err := os.WriteFile(full, []byte(writeMap[rel]), 0o644); err != nil {
			return record, err
		}
		afterExists, afterHash, err := fileHash(full)
		if err != nil {
			return record, err
		}
		if beforeExists && beforeHash == afterHash {
			continue
		}
		record.FileChanges = append(record.FileChanges, task.WorkspaceFileChange{
			Path:         rel,
			Action:       "write",
			BeforeExists: beforeExists,
			AfterExists:  afterExists,
			BeforeSHA256: beforeHash,
			AfterSHA256:  afterHash,
		})
	}
	return record, nil
}

type workspacePatch struct {
	ops []patchOperation
}

type patchOperation struct {
	kind  string
	path  string
	hunks []patchHunk
	lines []string
}

type patchHunk struct {
	header string
	lines  []patchHunkLine
}

type patchHunkLine struct {
	kind byte
	text string
}

func (s *Service) applyWorkspacePatch(spec task.Spec, record task.WorkspaceEditRecord, rawPatch string) (task.WorkspaceEditRecord, error) {
	parsed, err := parseWorkspacePatch(rawPatch)
	if err != nil {
		return record, err
	}
	for _, op := range parsed.ops {
		rel, err := s.normalizeWorkspaceEditPath(op.path)
		if err != nil {
			return record, err
		}
		if err := validateWorkspaceEditConstraints(spec, rel); err != nil {
			return record, err
		}
		full, err := safeWorkspaceEditFullPath(spec.WorkspaceRoot, rel)
		if err != nil {
			return record, err
		}
		beforeExists, beforeHash, err := fileHash(full)
		if err != nil {
			return record, err
		}
		switch op.kind {
		case "add":
			if beforeExists {
				return record, fmt.Errorf("patch add file already exists: %s", rel)
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return record, err
			}
			if err := os.WriteFile(full, []byte(joinPatchLines(op.lines, true)), 0o644); err != nil {
				return record, err
			}
		case "delete":
			if !beforeExists {
				return record, fmt.Errorf("patch delete file not found: %s", rel)
			}
			if err := os.Remove(full); err != nil {
				return record, err
			}
		case "update":
			if !beforeExists {
				return record, fmt.Errorf("patch update file not found: %s", rel)
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return record, err
			}
			updated, err := applyPatchHunks(string(data), op.hunks)
			if err != nil {
				return record, fmt.Errorf("apply patch %s: %w", rel, err)
			}
			if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
				return record, err
			}
		default:
			return record, fmt.Errorf("unsupported patch operation: %s", op.kind)
		}
		afterExists, afterHash, err := fileHash(full)
		if err != nil {
			return record, err
		}
		action := "write"
		if !afterExists {
			action = "delete"
		}
		if !beforeExists && afterExists {
			action = "write"
		}
		if beforeExists && afterExists && beforeHash == afterHash {
			continue
		}
		record.FileChanges = append(record.FileChanges, task.WorkspaceFileChange{
			Path:         rel,
			Action:       action,
			BeforeExists: beforeExists,
			AfterExists:  afterExists,
			BeforeSHA256: beforeHash,
			AfterSHA256:  afterHash,
		})
	}
	return record, nil
}

func parseWorkspacePatch(rawPatch string) (workspacePatch, error) {
	lines := strings.Split(strings.ReplaceAll(rawPatch, "\r\n", "\n"), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "*** Begin Patch" {
		return workspacePatch{}, fmt.Errorf("patch must start with *** Begin Patch")
	}
	var parsed workspacePatch
	for i := 1; i < len(lines); {
		line := strings.TrimSpace(lines[i])
		switch {
		case line == "":
			i++
		case line == "*** End Patch":
			return parsed, nil
		case strings.HasPrefix(line, "*** Add File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
			i++
			var addLines []string
			for i < len(lines) {
				current := lines[i]
				trimmed := strings.TrimSpace(current)
				if trimmed == "*** End Patch" || strings.HasPrefix(trimmed, "*** ") {
					break
				}
				if !strings.HasPrefix(current, "+") {
					return workspacePatch{}, fmt.Errorf("add file %s contains non-add line: %s", path, current)
				}
				addLines = append(addLines, strings.TrimPrefix(current, "+"))
				i++
			}
			parsed.ops = append(parsed.ops, patchOperation{kind: "add", path: path, lines: addLines})
		case strings.HasPrefix(line, "*** Delete File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
			parsed.ops = append(parsed.ops, patchOperation{kind: "delete", path: path})
			i++
		case strings.HasPrefix(line, "*** Update File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
			i++
			var hunks []patchHunk
			for i < len(lines) {
				current := lines[i]
				trimmed := strings.TrimSpace(current)
				switch {
				case trimmed == "*** End Patch" || strings.HasPrefix(trimmed, "*** Add File: ") || strings.HasPrefix(trimmed, "*** Delete File: ") || strings.HasPrefix(trimmed, "*** Update File: "):
					goto doneUpdate
				case strings.HasPrefix(trimmed, "*** Move to: "):
					return workspacePatch{}, fmt.Errorf("patch moves are not supported")
				case strings.HasPrefix(current, "@@"):
					hunk := patchHunk{header: strings.TrimSpace(strings.TrimPrefix(current, "@@"))}
					i++
					for i < len(lines) {
						hunkLine := lines[i]
						trimmedHunkLine := strings.TrimSpace(hunkLine)
						if strings.HasPrefix(hunkLine, "@@") || trimmedHunkLine == "*** End Patch" || strings.HasPrefix(trimmedHunkLine, "*** Add File: ") || strings.HasPrefix(trimmedHunkLine, "*** Delete File: ") || strings.HasPrefix(trimmedHunkLine, "*** Update File: ") {
							break
						}
						if trimmedHunkLine == "*** End of File" {
							i++
							continue
						}
						if hunkLine == "" {
							return workspacePatch{}, fmt.Errorf("empty patch hunk line in %s", path)
						}
						switch hunkLine[0] {
						case ' ', '+', '-':
							hunk.lines = append(hunk.lines, patchHunkLine{kind: hunkLine[0], text: hunkLine[1:]})
						default:
							return workspacePatch{}, fmt.Errorf("invalid patch hunk line in %s: %s", path, hunkLine)
						}
						i++
					}
					hunks = append(hunks, hunk)
				default:
					return workspacePatch{}, fmt.Errorf("unexpected patch line in %s: %s", path, current)
				}
			}
		doneUpdate:
			if len(hunks) == 0 {
				return workspacePatch{}, fmt.Errorf("update file %s has no hunks", path)
			}
			parsed.ops = append(parsed.ops, patchOperation{kind: "update", path: path, hunks: hunks})
		default:
			return workspacePatch{}, fmt.Errorf("unexpected patch line: %s", line)
		}
	}
	return workspacePatch{}, fmt.Errorf("patch must end with *** End Patch")
}

func joinPatchLines(lines []string, finalNewline bool) string {
	text := strings.Join(lines, "\n")
	if finalNewline {
		return text + "\n"
	}
	return text
}

func applyPatchHunks(original string, hunks []patchHunk) (string, error) {
	newline := "\n"
	if strings.Contains(original, "\r\n") {
		newline = "\r\n"
		original = strings.ReplaceAll(original, "\r\n", "\n")
	}
	lines, hadTrailingNewline := splitPatchLines(original)
	cursor := 0
	for _, hunk := range hunks {
		oldLines := make([]string, 0, len(hunk.lines))
		newLines := make([]string, 0, len(hunk.lines))
		for _, line := range hunk.lines {
			switch line.kind {
			case ' ':
				oldLines = append(oldLines, line.text)
				newLines = append(newLines, line.text)
			case '-':
				oldLines = append(oldLines, line.text)
			case '+':
				newLines = append(newLines, line.text)
			}
		}
		if len(oldLines) == 0 {
			return "", fmt.Errorf("patch hunk has no matchable context")
		}
		start, err := findPatchMatch(lines, oldLines, cursor)
		if err != nil {
			return "", err
		}
		lines = append(append(append([]string{}, lines[:start]...), newLines...), lines[start+len(oldLines):]...)
		cursor = start + len(newLines)
	}
	updated := strings.Join(lines, newline)
	if hadTrailingNewline || updated == "" {
		return updated + newline, nil
	}
	return updated, nil
}

func splitPatchLines(text string) ([]string, bool) {
	if text == "" {
		return []string{}, false
	}
	hadTrailingNewline := strings.HasSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\n")
	return strings.Split(text, "\n"), hadTrailingNewline
}

func findPatchMatch(lines, target []string, cursor int) (int, error) {
	for _, start := range []int{cursor, 0} {
		for i := start; i+len(target) <= len(lines); i++ {
			match := true
			for j := range target {
				if lines[i+j] != target[j] {
					match = false
					break
				}
			}
			if match {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("patch hunk context not found")
}

type workspaceFileCandidate struct {
	Path string
	Size int64
}

type criterionWorkspaceKind string

const (
	criterionKindReadme criterionWorkspaceKind = "readme"
	criterionKindDocs   criterionWorkspaceKind = "docs"
	criterionKindConfig criterionWorkspaceKind = "config"
	criterionKindSource criterionWorkspaceKind = "source"
)

type criterionWorkspaceAnalysis struct {
	Paths  map[string]struct{}
	Globs  map[string]struct{}
	Kinds  map[criterionWorkspaceKind]struct{}
	Tokens []string
}

type criterionWorkerAnalysis struct {
	Active                   bool
	Roles                    map[string]struct{}
	RequireContract          bool
	RequireResult            bool
	ResultReviewStatus       string
	ResultCompletionStatus   string
	ResultVerificationStatus string
	RequireSettlement        bool
	SettlementStatus         string
	RequireReconcile         bool
	ReconcileStatus          string
	RequireWorkspace         bool
	WorkspaceStatus          string
	ParentActionType         string
}

type workspaceSnapshotHints struct {
	Paths map[string]struct{}
	Dirs  map[string]struct{}
	Kinds map[criterionWorkspaceKind]struct{}
}

func (s *Service) collectWorkspaceEditFiles(spec task.Spec, failed task.VerificationReport) ([]provider.WorkspaceFile, provider.WorkspaceCollection, error) {
	const (
		maxFiles                   = 128
		maxBytes                   = 512 * 1024
		focusedUnrelatedFileBudget = 24
	)
	var (
		candidates   []workspaceFileCandidate
		files        []provider.WorkspaceFile
		total        int
		omittedExtra int
		omitted      []string
		stopReason   string
		truncated    bool
	)
	err := filepath.WalkDir(spec.WorkspaceRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == spec.WorkspaceRoot {
			return nil
		}
		rel, err := filepath.Rel(spec.WorkspaceRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if s.isDeniedWorkspacePath(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			omittedExtra++
			truncated = true
			if stopReason == "" {
				stopReason = "skipped symlink paths"
			}
			if len(omitted) < 16 {
				omitted = append(omitted, rel)
			}
			return nil
		}
		if s.isDeniedWorkspacePath(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		candidates = append(candidates, workspaceFileCandidate{Path: rel, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, provider.WorkspaceCollection{}, err
	}

	hints := buildWorkspaceSnapshotHints(spec, failed)
	unrelatedIncluded := 0
	sort.Slice(candidates, func(i, j int) bool {
		left := workspaceSnapshotPriority(candidates[i].Path, hints)
		right := workspaceSnapshotPriority(candidates[j].Path, hints)
		if left != right {
			return left < right
		}
		return candidates[i].Path < candidates[j].Path
	})

	for _, candidate := range candidates {
		hintRelevant := workspaceSnapshotRelevant(candidate.Path, hints)
		if len(hints.Paths) > 0 && !hintRelevant && unrelatedIncluded >= focusedUnrelatedFileBudget {
			truncated = true
			if stopReason == "" {
				stopReason = "workspace snapshot focused on verifier-hinted files"
			}
			if len(omitted) < 16 {
				omitted = append(omitted, candidate.Path)
			}
			continue
		}
		if len(files) >= maxFiles {
			truncated = true
			if stopReason == "" {
				stopReason = "workspace snapshot file budget reached"
			}
			if len(omitted) < 16 {
				omitted = append(omitted, candidate.Path)
			}
			continue
		}
		if candidate.Size > 64*1024 {
			truncated = true
			if stopReason == "" {
				stopReason = "skipped large or non-text files"
			}
			if len(omitted) < 16 {
				omitted = append(omitted, candidate.Path)
			}
			continue
		}
		full := filepath.Join(spec.WorkspaceRoot, filepath.FromSlash(candidate.Path))
		if info, err := os.Lstat(full); err != nil {
			return nil, provider.WorkspaceCollection{}, err
		} else if info.Mode()&os.ModeSymlink != 0 {
			omittedExtra++
			truncated = true
			if stopReason == "" {
				stopReason = "skipped symlink paths"
			}
			if len(omitted) < 16 {
				omitted = append(omitted, candidate.Path)
			}
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, provider.WorkspaceCollection{}, err
		}
		if !utf8.Valid(data) || strings.ContainsRune(string(data), '\x00') {
			truncated = true
			if stopReason == "" {
				stopReason = "skipped large or non-text files"
			}
			if len(omitted) < 16 {
				omitted = append(omitted, candidate.Path)
			}
			continue
		}
		if total+len(data) > maxBytes {
			truncated = true
			if stopReason == "" {
				stopReason = "workspace snapshot byte budget reached"
			}
			if len(omitted) < 16 {
				omitted = append(omitted, candidate.Path)
			}
			continue
		}
		total += len(data)
		files = append(files, provider.WorkspaceFile{Path: candidate.Path, Content: string(data)})
		if len(hints.Paths) > 0 && !hintRelevant {
			unrelatedIncluded++
		}
	}

	collection := provider.WorkspaceCollection{
		IncludedFileCount: len(files),
		IncludedByteCount: total,
		OmittedFileCount:  len(candidates) - len(files) + omittedExtra,
		Truncated:         truncated,
		StopReason:        stopReason,
		OmittedPaths:      omitted,
	}
	return files, collection, nil
}

func buildWorkspaceSnapshotHints(spec task.Spec, failed task.VerificationReport) workspaceSnapshotHints {
	hints := workspaceSnapshotHints{
		Paths: make(map[string]struct{}),
		Dirs:  make(map[string]struct{}),
		Kinds: make(map[criterionWorkspaceKind]struct{}),
	}
	for _, text := range []string{spec.Objective, failed.FailureSummary} {
		addWorkspaceSnapshotHintText(hints, text)
	}
	for _, criterion := range spec.SuccessCriteria {
		addWorkspaceSnapshotHintText(hints, criterion.Statement)
	}
	for _, check := range failed.Checks {
		addWorkspaceSnapshotHintText(hints, check.Summary)
	}
	return hints
}

func addWorkspaceSnapshotHintText(hints workspaceSnapshotHints, text string) {
	analysis := analyzeCriterionWorkspace(text)
	for candidate := range analysis.Paths {
		addWorkspaceSnapshotHintPath(hints, candidate)
	}
	for pattern := range analysis.Globs {
		addWorkspaceSnapshotHintPath(hints, criterionGlobAnchor(pattern))
	}
	for kind := range analysis.Kinds {
		hints.Kinds[kind] = struct{}{}
	}
}

func addWorkspaceSnapshotHintPath(hints workspaceSnapshotHints, rel string) {
	rel = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(rel))))
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return
	}
	hints.Paths[rel] = struct{}{}
	dir := filepath.ToSlash(filepath.Dir(rel))
	for dir != "." && dir != "/" && dir != "" {
		hints.Dirs[dir] = struct{}{}
		next := filepath.ToSlash(filepath.Dir(dir))
		if next == dir {
			break
		}
		dir = next
	}
}

func criterionGlobAnchor(pattern string) string {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" {
		return ""
	}
	if slash := strings.LastIndex(pattern, "/"); slash >= 0 {
		return strings.TrimSuffix(pattern[:slash+1], "/")
	}
	return ""
}

func workspaceSnapshotPriority(rel string, hints workspaceSnapshotHints) int {
	priority := 100
	switch rel {
	case "go.mod", "go.work", "go.sum", "ngen.json":
		priority -= 60
	}
	if _, ok := hints.Paths[rel]; ok {
		priority -= 50
	}
	for dir := filepath.ToSlash(filepath.Dir(rel)); dir != "." && dir != "/" && dir != ""; {
		if _, ok := hints.Dirs[dir]; ok {
			priority -= 20
			break
		}
		next := filepath.ToSlash(filepath.Dir(dir))
		if next == dir {
			break
		}
		dir = next
	}
	for kind := range hints.Kinds {
		if !criterionPathMatchesKind(rel, kind) {
			continue
		}
		switch kind {
		case criterionKindReadme:
			priority -= 25
		case criterionKindDocs, criterionKindConfig:
			priority -= 18
		case criterionKindSource:
			priority -= 12
		}
	}
	switch filepath.Ext(rel) {
	case ".go":
		priority -= 25
		if strings.HasSuffix(rel, "_test.go") {
			priority += 5
		}
	case ".mod", ".sum", ".json":
		priority -= 15
	case ".md", ".txt":
		priority -= 5
	}
	if !strings.Contains(rel, "/") {
		priority -= 5
	}
	return priority
}

func workspaceSnapshotRelevant(rel string, hints workspaceSnapshotHints) bool {
	switch rel {
	case "go.mod", "go.work", "go.sum", "ngen.json":
		return true
	}
	if _, ok := hints.Paths[rel]; ok {
		return true
	}
	for dir := filepath.ToSlash(filepath.Dir(rel)); dir != "." && dir != "/" && dir != ""; {
		if _, ok := hints.Dirs[dir]; ok {
			return true
		}
		next := filepath.ToSlash(filepath.Dir(dir))
		if next == dir {
			break
		}
		dir = next
	}
	for kind := range hints.Kinds {
		if criterionPathMatchesKind(rel, kind) {
			return true
		}
	}
	return false
}

func (s *Service) runWorkspaceObservationCommands(
	ctx context.Context,
	spec task.Spec,
	state *task.State,
	failed task.VerificationReport,
	criteria *task.CriteriaSnapshot,
	session *task.Session,
	previousFailures []provider.RepairFailure,
	attempt, budget int,
	files []provider.WorkspaceFile,
	collection provider.WorkspaceCollection,
) ([]provider.ObservationResult, []task.Event, error) {
	commandBudget := s.codingObservationCommandBudget()
	if commandBudget == 0 {
		return nil, nil, nil
	}
	commands := heuristicObservationCommands(spec, failed, collection, commandBudget)
	remainingBudget := commandBudget - len(commands)
	if remainingBudget > 0 {
		contextPack := s.loadContextPack(spec.TaskID)
		baseline := s.loadBaseline(spec.TaskID)
		continuity := s.loadContinuity(spec.TaskID)
		sprint := s.loadSprint(spec.TaskID)
		sessionMessagesRef, sessionRecentMessages, err := s.sessionContinuity(session, 6)
		if err != nil {
			return nil, nil, err
		}
		plan, err := provider.GenerateWorkspaceObservations(ctx, s.Config.Provider, provider.WorkspaceObservationInput{
			Task:                  spec,
			Baseline:              baseline,
			Continuity:            continuity,
			Sprint:                sprint,
			RecentVerification:    &failed,
			Criteria:              criteria,
			OpenCriteria:          openCriteria(spec, criteria),
			ContextPack:           contextPack,
			SessionMessagesRef:    sessionMessagesRef,
			SessionRecentMessages: sessionRecentMessages,
			PreviousFailures:      previousFailures,
			RepairAttempt:         attempt,
			RepairBudget:          budget,
			CommandBudget:         remainingBudget,
			Collection:            collection,
			Files:                 files,
		})
		if err != nil {
			return nil, nil, err
		}
		tokenUsage, promptCacheUsage := providerUsageFromWorkspaceObservation(plan)
		if _, err := s.appendProviderUsage(spec.TaskID, providerUsageOperationWorkspaceObservation, s.Config.Provider, tokenUsage, promptCacheUsage, []string{
			"verification/latest.json",
			"context/latest-pack.json",
			"continuity/latest.json",
			"sprint/latest.json",
		}); err != nil {
			return nil, nil, err
		}
		commands = append(commands, plan.Commands...)
	}
	if len(commands) > commandBudget {
		commands = commands[:commandBudget]
	}
	results := make([]provider.ObservationResult, 0, len(commands))
	var emitted []task.Event
	for _, command := range commands {
		result, events, err := s.executeObservationCommand(ctx, spec, *state, command)
		if err != nil {
			return nil, emitted, err
		}
		emitted = append(emitted, events...)
		if len(events) > 0 {
			state.LastEventRef = artifact.EventRef(events[len(events)-1].EventID)
		}
		results = append(results, result)
	}
	return results, emitted, nil
}

func heuristicObservationCommands(
	spec task.Spec,
	failed task.VerificationReport,
	collection provider.WorkspaceCollection,
	budget int,
) []provider.ObservationCommand {
	if commands := explicitObservationCommands(spec, budget); len(commands) > 0 {
		return commands
	}
	if budget <= 0 || collection.StopReason != "skipped large or non-text files" {
		return nil
	}
	targetPath := ""
	for _, path := range collection.OmittedPaths {
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			targetPath = path
			break
		}
	}
	if targetPath == "" {
		return nil
	}
	terms := observationSearchTerms(spec.Objective, failed.FailureSummary)
	commands := make([]provider.ObservationCommand, 0, budget)
	if len(terms) > 0 {
		commands = append(commands, provider.ObservationCommand{
			Argv:   []string{"rg", "-n", terms[0], targetPath},
			Reason: fmt.Sprintf("Locate %s inside the omitted large source file before proposing an edit.", terms[0]),
		})
	}
	if len(commands) < budget {
		commands = append(commands, provider.ObservationCommand{
			Argv:   []string{"tail", "-n", "120", targetPath},
			Reason: "Inspect the end of the omitted large source file because the failing implementation often lives near the bottom.",
		})
	}
	return commands
}

func explicitObservationCommands(spec task.Spec, budget int) []provider.ObservationCommand {
	if budget <= 0 {
		return nil
	}
	values := []string{spec.Objective, spec.Title}
	values = append(values, spec.Constraints...)
	for _, criterion := range spec.SuccessCriteria {
		values = append(values, criterion.Statement)
	}
	commands := make([]provider.ObservationCommand, 0, budget)
	for _, text := range values {
		for _, argv := range explicitCommandArgvs(text) {
			if len(commands) >= budget {
				return commands
			}
			if err := validateObservationCommand(argv); err != nil {
				continue
			}
			if duplicateObservationCommand(commands, argv) {
				continue
			}
			commands = append(commands, provider.ObservationCommand{
				Argv:   argv,
				Reason: "Run the explicit read-only command requested by the task text.",
			})
		}
	}
	return commands
}

func explicitCommandArgvs(text string) [][]string {
	var commands [][]string
	for _, match := range backtickCommandPattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 || !explicitCommandContext(text, match[0]) {
			continue
		}
		if commandLiteralNegated(text, match[0]) {
			continue
		}
		if argv := splitSimpleCommand(match[1]); len(argv) > 0 {
			commands = append(commands, argv)
		}
	}
	for _, match := range shellLikeCommandPattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		if argv := splitSimpleCommand(match[1]); len(argv) > 0 {
			commands = append(commands, argv)
		}
	}
	return commands
}

func commandLiteralNegated(text, literal string) bool {
	idx := strings.Index(text, literal)
	if idx < 0 {
		return false
	}
	start := idx - 80
	if start < 0 {
		start = 0
	}
	context := strings.ToLower(text[start:idx])
	for _, phrase := range []string{"do not", "don't", "never", "不要", "别", "不得", "禁止"} {
		if strings.Contains(context, phrase) {
			return true
		}
	}
	return false
}

func explicitCommandContext(text, literal string) bool {
	idx := strings.Index(text, literal)
	if idx < 0 {
		return false
	}
	start := idx - 120
	if start < 0 {
		start = 0
	}
	context := strings.ToLower(text[start:idx])
	for _, word := range []string{"run", "running", "execute", "invoke", "call", "command", "运行", "执行", "调用", "命令"} {
		if strings.Contains(context, word) {
			return true
		}
	}
	return false
}

func splitSimpleCommand(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, "\n\r|;&<>") {
		return nil
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	for _, field := range fields {
		if strings.Contains(field, "$(") || strings.Contains(field, "`") {
			return nil
		}
	}
	return fields
}

func duplicateObservationCommand(commands []provider.ObservationCommand, argv []string) bool {
	for _, command := range commands {
		if equalStringSlices(command.Argv, argv) {
			return true
		}
	}
	return false
}

func explicitCommandTaskRequested(spec task.Spec) bool {
	return explicitCommandTaskCriterion(spec) && len(explicitExecutionCommands(spec)) > 0
}

func explicitCommandTaskCriterion(spec task.Spec) bool {
	for _, criterion := range spec.SuccessCriteria {
		if explicitCommandCriterionMode(criterion.Statement) {
			return true
		}
	}
	return false
}

func explicitExecutionCommands(spec task.Spec) [][]string {
	values := []string{spec.Objective, spec.Title}
	values = append(values, spec.Constraints...)
	var commands [][]string
	for _, value := range values {
		for _, argv := range explicitCommandArgvs(value) {
			if len(argv) == 0 {
				continue
			}
			if duplicateArgv(commands, argv) {
				continue
			}
			commands = append(commands, append([]string(nil), argv...))
		}
	}
	return commands
}

func duplicateArgv(commands [][]string, argv []string) bool {
	for _, command := range commands {
		if equalStringSlices(command, argv) {
			return true
		}
	}
	return false
}

func (s *Service) explicitCommandTaskAttempted(spec task.Spec) bool {
	commands := explicitExecutionCommands(spec)
	if len(commands) == 0 {
		return false
	}
	records, err := s.Store.ReadCommandRuns(spec.TaskID)
	if err != nil {
		return false
	}
	for _, record := range records {
		if record.Kind != "repair_command" {
			continue
		}
		for _, command := range commands {
			if equalStringSlices(record.Argv, command) {
				return true
			}
		}
	}
	return false
}

func (s *Service) explicitCommandWorkspaceEditPlan(spec task.Spec) (provider.WorkspaceEditPlan, bool) {
	if !explicitCommandTaskCriterion(spec) {
		return provider.WorkspaceEditPlan{}, false
	}
	commands := explicitExecutionCommands(spec)
	if len(commands) == 0 {
		return provider.WorkspaceEditPlan{}, false
	}
	if len(commands) > 1 {
		commands = commands[:1]
	}
	workspaceCommands := make([]provider.WorkspaceCommand, 0, len(commands))
	for _, argv := range commands {
		workspaceCommands = append(workspaceCommands, provider.WorkspaceCommand{
			Phase:  "post",
			Argv:   append([]string(nil), argv...),
			Reason: "Run the explicit user-requested command through the command policy lane.",
		})
	}
	return provider.WorkspaceEditPlan{
		Summary:  "Run the explicit command requested by the task text.",
		Commands: workspaceCommands,
	}, true
}

func observationSearchTerms(values ...string) []string {
	var terms []string
	stopwords := map[string]struct{}{
		"fix":       {},
		"implement": {},
		"normalize": {},
		"respect":   {},
		"update":    {},
		"find":      {},
		"cap":       {},
		"inspect":   {},
		"return":    {},
	}
	for _, value := range values {
		for _, match := range observationUndefinedNamePattern.FindAllStringSubmatch(value, -1) {
			if len(match) >= 2 {
				candidate := strings.TrimSpace(match[1])
				if _, blocked := stopwords[strings.ToLower(candidate)]; !blocked && !looksLikeAllCapsSignal(candidate) && !strings.HasPrefix(candidate, "Test") {
					terms = append(terms, candidate)
				}
			}
		}
		for _, match := range observationNameTokenPattern.FindAllStringSubmatch(value, -1) {
			if len(match) >= 2 {
				candidate := strings.TrimSpace(match[1])
				if _, blocked := stopwords[strings.ToLower(candidate)]; !blocked && !looksLikeAllCapsSignal(candidate) && !strings.HasPrefix(candidate, "Test") {
					terms = append(terms, candidate)
				}
			}
		}
	}
	return uniqueNonEmptyStrings(terms)
}

func looksLikeAllCapsSignal(value string) bool {
	if len(value) <= 1 {
		return false
	}
	hasLetter := false
	for _, r := range value {
		if !unicode.IsLetter(r) {
			continue
		}
		hasLetter = true
		if !unicode.IsUpper(r) {
			return false
		}
	}
	return hasLetter
}

func (s *Service) executeObservationCommand(ctx context.Context, spec task.Spec, state task.State, command provider.ObservationCommand) (provider.ObservationResult, []task.Event, error) {
	commandID := task.NewID("CMD")
	record := task.CommandRunRecord{
		SchemaVersion:   task.SchemaVersion,
		CommandRecordID: task.NewID("CMDREC"),
		CommandID:       commandID,
		TaskID:          spec.TaskID,
		TS:              task.Now(),
		Kind:            "observation_command",
		Status:          "completed",
		Summary:         strings.TrimSpace(command.Reason),
		Argv:            append([]string(nil), command.Argv...),
		ReplaySafety:    readOnlyCommandReplaySafety(),
	}
	if record.Summary == "" {
		record.Summary = "Executed bounded observation command."
	}
	startEvent := newEvent(
		spec.TaskID,
		state,
		"observation_command_started",
		fmt.Sprintf("Running bounded observation command: %s", strings.Join(command.Argv, " ")),
		nil,
	)
	if err := s.Store.AppendEvent(startEvent); err != nil {
		return provider.ObservationResult{}, nil, err
	}

	if err := validateObservationCommand(command.Argv); err != nil {
		record.Status = "failed"
		record.Summary = fmt.Sprintf("Rejected observation command: %v", err)
		event, updatedRecord, persistErr := s.persistCommandRunRecord(state, record, nil, nil, "observation_command_failed")
		if persistErr != nil {
			return provider.ObservationResult{}, []task.Event{startEvent}, persistErr
		}
		return observationResultFromRecord(updatedRecord), []task.Event{startEvent, event}, nil
	}
	if err := validateObservationCommandWorkspacePathsWithDeny(spec.WorkspaceRoot, command.Argv, s.Config.Visibility.DenyPatterns); err != nil {
		record.Status = "failed"
		record.Summary = fmt.Sprintf("Rejected observation command: %v", err)
		event, updatedRecord, persistErr := s.persistCommandRunRecord(state, record, nil, nil, "observation_command_failed")
		if persistErr != nil {
			return provider.ObservationResult{}, []task.Event{startEvent}, persistErr
		}
		return observationResultFromRecord(updatedRecord), []task.Event{startEvent, event}, nil
	}

	runCtx := ctx
	cancel := func() {}
	if timeout := s.codingObservationCommandTimeout(); timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, command.Argv[0], command.Argv[1:]...)
	cmd.Dir = spec.WorkspaceRoot
	stdout := newCappedOutputBuffer(commandOutputMaxBytes)
	stderr := newCappedOutputBuffer(commandOutputMaxBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	record.ExitCode = 0
	if runErr != nil {
		record.Status = "failed"
		record.ExitCode = exitCodeFromError(runErr)
		record.Summary = fmt.Sprintf("%s Command failed: %v", record.Summary, runErr)
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		record.Status = "failed"
		record.TimedOut = true
		record.Summary = fmt.Sprintf("%s Command timed out after %s", record.Summary, s.codingObservationCommandTimeout())
	}
	applyCommandOutputLimitToRecord(&record, stdout.Truncated(), stderr.Truncated())
	eventType := "observation_command_completed"
	if record.Status != "completed" {
		eventType = "observation_command_failed"
	}
	event, updatedRecord, err := s.persistCommandRunRecord(state, record, stdout.Bytes(), stderr.Bytes(), eventType)
	if err != nil {
		return provider.ObservationResult{}, []task.Event{startEvent}, err
	}
	return observationResultFromRecord(updatedRecord), []task.Event{startEvent, event}, nil
}

func (s *Service) persistCommandRunRecord(state task.State, record task.CommandRunRecord, stdout, stderr []byte, eventType string) (task.Event, task.CommandRunRecord, error) {
	stdoutExcerpt := excerptText(string(stdout), 2400)
	stderrExcerpt := excerptText(string(stderr), 2400)
	stdoutRef, stderrRef, err := s.Store.SaveCommandOutput(record.TaskID, record.CommandID, stdout, stderr)
	if err != nil {
		return task.Event{}, task.CommandRunRecord{}, err
	}
	record.StdoutRef = stdoutRef
	record.StderrRef = stderrRef
	record.StdoutExcerpt = stdoutExcerpt
	record.StderrExcerpt = stderrExcerpt
	if err := s.Store.AppendCommandRun(record); err != nil {
		return task.Event{}, task.CommandRunRecord{}, err
	}
	event := newEvent(record.TaskID, state, eventType, record.Summary, []string{artifact.CommandRunRecordRef(record.CommandRecordID)})
	if err := s.Store.AppendEvent(event); err != nil {
		return task.Event{}, task.CommandRunRecord{}, err
	}
	return event, record, nil
}

func applyCommandOutputLimitToRecord(record *task.CommandRunRecord, stdoutTruncated, stderrTruncated bool) {
	record.StdoutTruncated = stdoutTruncated
	record.StderrTruncated = stderrTruncated
	if !stdoutTruncated && !stderrTruncated {
		return
	}
	record.Status = "failed"
	var parts []string
	if stdoutTruncated {
		parts = append(parts, fmt.Sprintf("stdout exceeded max bytes (%d)", commandOutputMaxBytes))
	}
	if stderrTruncated {
		parts = append(parts, fmt.Sprintf("stderr exceeded max bytes (%d)", commandOutputMaxBytes))
	}
	record.Summary = strings.TrimSpace(record.Summary + " Command output truncated: " + strings.Join(parts, "; ") + ".")
}

func observationResultFromRecord(record task.CommandRunRecord) provider.ObservationResult {
	return provider.ObservationResult{
		CommandID:     record.CommandID,
		Status:        record.Status,
		Summary:       record.Summary,
		Argv:          append([]string(nil), record.Argv...),
		ExitCode:      record.ExitCode,
		TimedOut:      record.TimedOut,
		StdoutRef:     record.StdoutRef,
		StderrRef:     record.StderrRef,
		StdoutExcerpt: record.StdoutExcerpt,
		StderrExcerpt: record.StderrExcerpt,
	}
}

func splitWorkspaceCommands(commands []provider.WorkspaceCommand) ([]provider.WorkspaceCommand, []provider.WorkspaceCommand) {
	pre := make([]provider.WorkspaceCommand, 0, len(commands))
	post := make([]provider.WorkspaceCommand, 0, len(commands))
	for _, command := range commands {
		if strings.TrimSpace(strings.ToLower(command.Phase)) == "pre" {
			pre = append(pre, command)
			continue
		}
		post = append(post, command)
	}
	return pre, post
}

func tooManyExecutionCommandsFailure(count, budget int) *provider.RepairFailure {
	if count == 0 {
		return nil
	}
	if budget < 0 {
		budget = 0
	}
	if count <= budget {
		return nil
	}
	return &provider.RepairFailure{
		Stage:   "workspace_command_budget_exceeded",
		Summary: fmt.Sprintf("Workspace command plan requested %d commands but the execution budget is %d.", count, budget),
	}
}

func (s *Service) runWorkspaceExecutionCommands(
	ctx context.Context,
	spec task.Spec,
	state *task.State,
	commands []provider.WorkspaceCommand,
	phase string,
) ([]task.Event, *provider.RepairFailure, error) {
	if len(commands) == 0 {
		return nil, nil, nil
	}
	var emitted []task.Event
	for _, command := range commands {
		events, failure, err := s.executeWorkspaceExecutionCommand(ctx, spec, *state, command, phase)
		if err != nil {
			return emitted, nil, err
		}
		emitted = append(emitted, events...)
		if failure != nil {
			return emitted, failure, nil
		}
		if len(events) > 0 {
			state.LastEventRef = artifact.EventRef(events[len(events)-1].EventID)
		}
	}
	return emitted, nil, nil
}

func (s *Service) executeWorkspaceExecutionCommand(
	ctx context.Context,
	spec task.Spec,
	state task.State,
	command provider.WorkspaceCommand,
	phase string,
) ([]task.Event, *provider.RepairFailure, error) {
	commandID := task.NewID("CMD")
	record := task.CommandRunRecord{
		SchemaVersion:    task.SchemaVersion,
		CommandRecordID:  task.NewID("CMDREC"),
		CommandID:        commandID,
		TaskID:           spec.TaskID,
		TS:               task.Now(),
		Kind:             "repair_command",
		Status:           "completed",
		Summary:          strings.TrimSpace(command.Reason),
		Argv:             append([]string(nil), command.Argv...),
		PermissionModeID: task.EffectivePermissionModeID(firstNonEmpty(state.PermissionModeID, spec.PermissionModeID)),
	}
	record.PolicyDecision = s.executionCommandPolicyDecision(command.Argv, record.PermissionModeID)
	record.ReplaySafety = commandReplaySafety(command.Argv, record.PermissionModeID, record.PolicyDecision)
	if record.Summary == "" {
		record.Summary = fmt.Sprintf("Executed %s repair command.", strings.TrimSpace(phase))
	}
	startEvent := newEvent(
		spec.TaskID,
		state,
		"repair_command_started",
		fmt.Sprintf("Running %s repair command: %s", strings.TrimSpace(phase), strings.Join(command.Argv, " ")),
		nil,
	)
	if err := s.Store.AppendEvent(startEvent); err != nil {
		return nil, nil, err
	}

	if previous, blocked := s.previousUnsafeCommandReplay(spec.TaskID, record); blocked {
		record.Status = "failed"
		record.Summary = fmt.Sprintf("Rejected repair command replay: previous side-effect command %s has replay_policy=%s.", previous.CommandRecordID, replayPolicyLabel(previous.ReplaySafety))
		event, updatedRecord, persistErr := s.persistCommandRunRecord(state, record, nil, nil, "repair_command_failed")
		if persistErr != nil {
			return []task.Event{startEvent}, nil, persistErr
		}
		return []task.Event{startEvent, event}, repairFailureFromCommandRunRecord(updatedRecord, phase), nil
	}

	if err := s.validateExecutionCommand(command.Argv, record.PermissionModeID); err != nil {
		record.Status = "failed"
		record.Summary = fmt.Sprintf("Rejected repair command: %v", err)
		event, updatedRecord, persistErr := s.persistCommandRunRecord(state, record, nil, nil, "repair_command_failed")
		if persistErr != nil {
			return []task.Event{startEvent}, nil, persistErr
		}
		return []task.Event{startEvent, event}, repairFailureFromCommandRunRecord(updatedRecord, phase), nil
	}

	runCtx := ctx
	cancel := func() {}
	if timeout := s.codingExecutionCommandTimeout(); timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, command.Argv[0], command.Argv[1:]...)
	cmd.Dir = spec.WorkspaceRoot
	stdout := newCappedOutputBuffer(commandOutputMaxBytes)
	stderr := newCappedOutputBuffer(commandOutputMaxBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	record.ExitCode = 0
	if runErr != nil {
		record.Status = "failed"
		record.ExitCode = exitCodeFromError(runErr)
		record.Summary = fmt.Sprintf("%s Command failed: %v", record.Summary, runErr)
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		record.Status = "failed"
		record.TimedOut = true
		record.Summary = fmt.Sprintf("%s Command timed out after %s", record.Summary, s.codingExecutionCommandTimeout())
	}
	applyCommandOutputLimitToRecord(&record, stdout.Truncated(), stderr.Truncated())
	eventType := "repair_command_completed"
	if record.Status != "completed" {
		eventType = "repair_command_failed"
	}
	event, updatedRecord, err := s.persistCommandRunRecord(state, record, stdout.Bytes(), stderr.Bytes(), eventType)
	if err != nil {
		return []task.Event{startEvent}, nil, err
	}
	if updatedRecord.Status != "completed" {
		return []task.Event{startEvent, event}, repairFailureFromCommandRunRecord(updatedRecord, phase), nil
	}
	return []task.Event{startEvent, event}, nil, nil
}

func (s *Service) previousUnsafeCommandReplay(taskID string, record task.CommandRunRecord) (task.CommandRunRecord, bool) {
	if record.ReplaySafety == nil || record.ReplaySafety.ReplayPolicy == "safe_to_replay" {
		return task.CommandRunRecord{}, false
	}
	records, err := s.Store.ReadCommandRuns(taskID)
	if err != nil {
		return task.CommandRunRecord{}, false
	}
	for _, previous := range records {
		if previous.Kind != "repair_command" || !equalStringSlices(previous.Argv, record.Argv) {
			continue
		}
		if previous.ReplaySafety == nil || previous.ReplaySafety.ReplayPolicy != "safe_to_replay" {
			return previous, true
		}
	}
	return task.CommandRunRecord{}, false
}

func replayPolicyLabel(safety *task.ReplaySafety) string {
	if safety == nil || strings.TrimSpace(safety.ReplayPolicy) == "" {
		return "missing"
	}
	return safety.ReplayPolicy
}

func (s *Service) validateExecutionCommand(argv []string, permissionModeID string) error {
	if len(argv) == 0 {
		return fmt.Errorf("repair command argv is required")
	}
	for _, arg := range argv {
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("repair command contains empty argv entry")
		}
	}
	if s.Config.Permission.BenchmarkIntegrityMode && executionCommandBenchmarkIntegrityRisk(argv) {
		return fmt.Errorf("repair command %s is denied by benchmark integrity mode because it can access network or open-world execution", argv[0])
	}
	if task.EffectivePermissionModeID(permissionModeID) == task.PermissionModeYolo {
		return nil
	}
	switch decision := s.executionCommandPolicyDecision(argv, permissionModeID); decision {
	case "allow":
		return nil
	case "needs_approval":
		return fmt.Errorf("repair command %s requires approval in standard permission mode; use yolo permission mode or run the command manually", argv[0])
	default:
		return fmt.Errorf("repair command %s is denied in standard permission mode", argv[0])
	}
}

func (s *Service) executionCommandPolicyDecision(argv []string, permissionModeID string) string {
	if s.Config.Permission.BenchmarkIntegrityMode && executionCommandBenchmarkIntegrityRisk(argv) {
		return "denied_benchmark_integrity"
	}
	return executionCommandPolicyDecision(argv, permissionModeID)
}

func executionCommandPolicyDecision(argv []string, permissionModeID string) string {
	if len(argv) == 0 {
		return "denied"
	}
	if task.EffectivePermissionModeID(permissionModeID) == task.PermissionModeYolo {
		return "allow_yolo"
	}
	executable := strings.TrimSpace(argv[0])
	switch executable {
	case "gofmt":
		return "allow"
	case "go":
		if len(argv) < 2 {
			return "denied"
		}
		switch strings.TrimSpace(argv[1]) {
		case "fmt", "test", "build", "vet", "generate":
			return "allow"
		case "mod":
			if len(argv) >= 3 {
				switch strings.TrimSpace(argv[2]) {
				case "tidy", "download", "verify":
					return "allow"
				}
			}
			return "needs_approval"
		default:
			return "needs_approval"
		}
	case "cargo":
		if len(argv) >= 2 {
			switch strings.TrimSpace(argv[1]) {
			case "fmt", "test", "build", "check":
				return "allow"
			}
		}
		return "needs_approval"
	case "npm", "pnpm", "yarn", "make", "./build.sh":
		return "needs_approval"
	case "multica":
		if isAllowedMulticaIssueMutation(argv) {
			return "needs_approval"
		}
		return "denied"
	case "bash", "sh", "zsh", "fish", "python", "python3", "node", "perl", "ruby", "pwsh", "powershell", "cmd":
		return "needs_approval"
	default:
		if strings.Contains(executable, "/") || strings.Contains(executable, "\\") {
			return "needs_approval"
		}
		return "denied"
	}
}

func isAllowedMulticaIssueMutation(argv []string) bool {
	if len(argv) < 3 || argv[0] != "multica" {
		return false
	}
	if len(argv) >= 4 && argv[1] == "issue" && argv[2] == "create" {
		return true
	}
	if len(argv) < 5 {
		return false
	}
	if argv[1] == "issue" && argv[2] == "comment" && argv[3] == "add" {
		return multicaIssueIDPattern.MatchString(argv[4])
	}
	if argv[1] == "squad" && argv[2] == "delegate" {
		return multicaIssueIDPattern.MatchString(argv[3])
	}
	if argv[1] == "squad" && argv[2] == "activity" {
		return multicaIssueIDPattern.MatchString(argv[3])
	}
	if argv[1] == "mission" && (argv[2] == "complete" || argv[2] == "publish") {
		return multicaIssueIDPattern.MatchString(argv[3])
	}
	return false
}

func executionCommandBenchmarkIntegrityRisk(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	executable := strings.ToLower(filepath.Base(strings.TrimSpace(argv[0])))
	if executable == "" {
		return false
	}
	switch executable {
	case "curl", "wget", "aria2c", "http", "https", "nc", "ncat", "netcat", "ssh", "scp", "sftp", "rsync":
		return true
	case "gh", "hub":
		return true
	case "multica":
		return true
	case "pip", "pip3", "uv", "poetry", "pipenv":
		return true
	case "npm", "pnpm", "yarn", "bun":
		return true
	case "bash", "sh", "zsh", "fish", "python", "python3", "node", "perl", "ruby", "pwsh", "powershell", "cmd":
		return true
	}
	if strings.Contains(argv[0], "/") || strings.Contains(argv[0], "\\") {
		return true
	}
	if len(argv) < 2 {
		return false
	}
	subcommand := strings.ToLower(strings.TrimSpace(argv[1]))
	switch executable {
	case "git":
		switch subcommand {
		case "clone", "fetch", "pull", "push", "ls-remote", "submodule":
			return true
		}
	case "go":
		switch subcommand {
		case "get", "install", "generate":
			return true
		case "mod":
			if len(argv) >= 3 && strings.EqualFold(strings.TrimSpace(argv[2]), "download") {
				return true
			}
		}
	case "cargo":
		switch subcommand {
		case "fetch", "install", "update", "publish":
			return true
		}
	}
	return false
}

func readOnlyCommandReplaySafety() *task.ReplaySafety {
	return &task.ReplaySafety{
		SideEffectClass: "read_only_command",
		ReplayPolicy:    "safe_to_replay",
		ReadOnly:        true,
		Idempotent:      true,
		Summary:         "Bounded observation command is read-only; replay is allowed from the same workspace state.",
	}
}

func workspaceEditReplaySafety(kind string) *task.ReplaySafety {
	class := "workspace_file_edit"
	summary := "Workspace file edits are not automatically replay-safe; use file hashes and workspace_edits evidence before retrying."
	if strings.TrimSpace(kind) == "worker_reconcile" {
		class = "worker_reconcile"
		summary = "Worker reconcile changes are tied to parent/child baselines; do not replay without rechecking reconcile evidence."
	}
	return &task.ReplaySafety{
		SideEffectClass: class,
		ReplayPolicy:    "do_not_auto_replay",
		WritesWorkspace: true,
		Destructive:     true,
		Summary:         summary,
	}
}

func commandReplaySafety(argv []string, permissionModeID, policyDecision string) *task.ReplaySafety {
	safety := &task.ReplaySafety{
		SideEffectClass: "workspace_repair_command",
		ReplayPolicy:    "manual_review_required",
		WritesWorkspace: true,
		Summary:         "Repair command has workspace side effects; replay requires artifact review unless classified safe.",
	}
	if len(argv) == 0 {
		safety.Destructive = true
		safety.OpenWorld = true
		safety.Summary = "Empty repair command is invalid and not replay-safe."
		return safety
	}
	executable := strings.TrimSpace(argv[0])
	subcommand := ""
	if len(argv) > 1 {
		subcommand = strings.TrimSpace(argv[1])
	}
	switch executable {
	case "gofmt":
		safety.Idempotent = true
		safety.ReplayPolicy = "safe_to_replay"
		safety.Summary = "gofmt is an idempotent workspace formatter."
	case "go":
		classifyGoCommandReplaySafety(safety, argv)
	case "cargo":
		classifyCargoCommandReplaySafety(safety, argv)
	case "npm", "pnpm", "yarn":
		safety.Network = true
		safety.OpenWorld = true
		safety.Idempotent = false
	case "make", "./build.sh":
		safety.OpenWorld = true
	case "multica":
		safety.OpenWorld = true
		safety.Destructive = true
		safety.Summary = "Multica repair commands can mutate external issue state; replay requires manual review."
	case "bash", "sh", "zsh", "fish", "python", "python3", "node", "perl", "ruby", "pwsh", "powershell", "cmd":
		safety.OpenWorld = true
		safety.Destructive = true
	default:
		if strings.Contains(executable, "/") || strings.Contains(executable, "\\") {
			safety.OpenWorld = true
		} else {
			safety.Destructive = true
		}
	}
	if task.EffectivePermissionModeID(permissionModeID) == task.PermissionModeYolo || policyDecision == "allow_yolo" {
		safety.OpenWorld = true
		if safety.ReplayPolicy == "safe_to_replay" && !safety.ReadOnly {
			safety.ReplayPolicy = "manual_review_required"
		}
	}
	if policyDecision == "denied_benchmark_integrity" {
		safety.Network = true
		safety.OpenWorld = true
		safety.ReplayPolicy = "do_not_auto_replay"
		safety.Summary = "Repair command is blocked by benchmark integrity mode because it can access network or open-world execution."
	}
	if policyDecision == "denied" {
		safety.ReplayPolicy = "do_not_auto_replay"
	}
	if safety.ReplayPolicy == "safe_to_replay" {
		safety.Summary = strings.TrimSpace(safety.Summary)
		if safety.Summary == "" {
			safety.Summary = fmt.Sprintf("%s %s is idempotent under the current command policy.", executable, subcommand)
		}
	}
	return safety
}

func classifyGoCommandReplaySafety(safety *task.ReplaySafety, argv []string) {
	if len(argv) < 2 {
		safety.Destructive = true
		safety.ReplayPolicy = "do_not_auto_replay"
		return
	}
	switch strings.TrimSpace(argv[1]) {
	case "test", "build", "vet":
		safety.ReadOnly = true
		safety.WritesWorkspace = false
		safety.Idempotent = true
		safety.ReplayPolicy = "safe_to_replay"
		safety.Summary = "Go verification command is treated as read-only for workspace replay."
	case "fmt":
		safety.Idempotent = true
		safety.ReplayPolicy = "safe_to_replay"
		safety.Summary = "go fmt is an idempotent workspace formatter."
	case "generate":
		safety.Idempotent = false
		safety.OpenWorld = true
		safety.Summary = "go generate can run arbitrary generators; manual review is required before replay."
	case "mod":
		if len(argv) >= 3 {
			switch strings.TrimSpace(argv[2]) {
			case "verify":
				safety.ReadOnly = true
				safety.WritesWorkspace = false
				safety.Idempotent = true
				safety.ReplayPolicy = "safe_to_replay"
				safety.Summary = "go mod verify is read-only for workspace replay."
			case "tidy":
				safety.Idempotent = true
				safety.ReplayPolicy = "safe_to_replay"
				safety.Summary = "go mod tidy is treated as idempotent but still records workspace file hashes."
			case "download":
				safety.Network = true
				safety.OpenWorld = true
				safety.Idempotent = true
				safety.Summary = "go mod download can access the network; manual review is required before replay."
			}
		}
	}
}

func classifyCargoCommandReplaySafety(safety *task.ReplaySafety, argv []string) {
	if len(argv) < 2 {
		safety.Destructive = true
		safety.ReplayPolicy = "do_not_auto_replay"
		return
	}
	switch strings.TrimSpace(argv[1]) {
	case "test", "build", "check":
		safety.ReadOnly = true
		safety.WritesWorkspace = false
		safety.Idempotent = true
		safety.ReplayPolicy = "safe_to_replay"
		safety.Summary = "Cargo verification command is treated as read-only for workspace replay."
	case "fmt":
		safety.Idempotent = true
		safety.ReplayPolicy = "safe_to_replay"
		safety.Summary = "cargo fmt is an idempotent workspace formatter."
	}
}

func repairFailureFromCommandRunRecord(record task.CommandRunRecord, phase string) *provider.RepairFailure {
	summary := strings.TrimSpace(record.Summary)
	if summary == "" || record.Status == "completed" {
		return nil
	}
	stage := "workspace_command_failed"
	if trimmedPhase := strings.TrimSpace(strings.ToLower(phase)); trimmedPhase != "" {
		stage = fmt.Sprintf("workspace_command_%s_failed", trimmedPhase)
	}
	return &provider.RepairFailure{
		Stage:   stage,
		Summary: summary,
	}
}

func (s *Service) normalizeWorkspaceEditPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("workspace edit path is required")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("workspace edit path must be relative: %s", path)
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	rel := filepath.ToSlash(clean)
	if rel == "." || rel == "" {
		return "", fmt.Errorf("workspace edit path is invalid: %s", path)
	}
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return "", fmt.Errorf("workspace edit path escapes workspace: %s", path)
	}
	if s.isDeniedWorkspacePath(rel) {
		return "", fmt.Errorf("workspace edit path is denied by visibility rules: %s", path)
	}
	return rel, nil
}

func safeWorkspaceEditFullPath(workspaceRoot, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(rel)))
	if clean == "." || clean == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(filepath.ToSlash(clean), "../") {
		return "", fmt.Errorf("workspace path escapes workspace: %s", rel)
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", err
	}
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	parts := strings.Split(filepath.ToSlash(clean), "/")
	current := root
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) {
				current = next
				continue
			}
			return "", fmt.Errorf("inspect workspace path %s: %w", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("workspace path %s crosses symlink component %s", rel, filepath.ToSlash(filepath.Join(parts[:i+1]...)))
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("workspace path %s parent component is not a directory", rel)
		}
		current = next
	}
	full := filepath.Join(root, clean)
	relToRoot, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	relToRoot = filepath.ToSlash(relToRoot)
	if relToRoot == ".." || strings.HasPrefix(relToRoot, "../") {
		return "", fmt.Errorf("workspace path escapes workspace: %s", rel)
	}
	return full, nil
}

func (s *Service) isDeniedWorkspacePath(rel string) bool {
	return pathDeniedByPatterns(rel, s.Config.Visibility.DenyPatterns)
}

func validateObservationCommand(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("observation command argv is required")
	}
	for _, arg := range argv {
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("observation command contains empty argv entry")
		}
	}
	switch argv[0] {
	case "rg":
		for _, arg := range argv[1:] {
			switch {
			case arg == "-L" || arg == "--follow":
				return fmt.Errorf("rg symlink traversal is not allowed")
			case arg == "--hidden" || arg == "--no-ignore" || strings.HasPrefix(arg, "--no-ignore-") || arg == "--unrestricted":
				return fmt.Errorf("rg hidden/ignored path traversal is not allowed")
			case strings.HasPrefix(arg, "-u") && strings.TrimLeft(arg, "-u") == "":
				return fmt.Errorf("rg hidden/ignored path traversal is not allowed")
			case arg == "-f", arg == "--file":
				return fmt.Errorf("rg pattern files are not allowed")
			case strings.HasPrefix(arg, "--file="):
				return fmt.Errorf("rg pattern files are not allowed")
			case arg == "--ignore-file", strings.HasPrefix(arg, "--ignore-file="):
				return fmt.Errorf("rg ignore files are not allowed")
			case arg == "--pre", strings.HasPrefix(arg, "--pre="), arg == "--pre-glob", strings.HasPrefix(arg, "--pre-glob="):
				return fmt.Errorf("rg preprocessors are not allowed")
			case arg == "--config", strings.HasPrefix(arg, "--config="):
				return fmt.Errorf("rg external configs are not allowed")
			}
		}
		return nil
	case "cat", "head", "tail", "wc":
		return nil
	case "ls":
		for _, arg := range argv[1:] {
			switch {
			case arg == "--all" || arg == "--almost-all":
				return fmt.Errorf("ls hidden path listing is not allowed")
			case strings.HasPrefix(arg, "--"):
				continue
			case strings.HasPrefix(arg, "-") && strings.ContainsAny(strings.TrimLeft(arg, "-"), "aA"):
				return fmt.Errorf("ls hidden path listing is not allowed")
			}
		}
		return nil
	case "find":
		for _, arg := range argv[1:] {
			switch arg {
			case "-H", "-L", "-follow", "-exec", "-execdir", "-ok", "-okdir", "-delete":
				return fmt.Errorf("find expression %s is not allowed", arg)
			}
		}
		return nil
	case "sed":
		for _, arg := range argv[1:] {
			if arg == "-i" || strings.HasPrefix(arg, "-i") {
				return fmt.Errorf("sed in-place editing is not allowed")
			}
			if arg == "-f" || arg == "--file" || strings.HasPrefix(arg, "--file=") {
				return fmt.Errorf("sed script files are not allowed")
			}
		}
		return nil
	case "git":
		if len(argv) < 2 {
			return fmt.Errorf("git observation command requires a subcommand")
		}
		for _, arg := range argv[1:] {
			switch {
			case arg == "-C":
				return fmt.Errorf("git -C is not allowed")
			case strings.HasPrefix(arg, "--git-dir"), strings.HasPrefix(arg, "--work-tree"):
				return fmt.Errorf("git repository root overrides are not allowed")
			case arg == "--no-index":
				return fmt.Errorf("git --no-index is not allowed")
			case arg == "--output" || strings.HasPrefix(arg, "--output="):
				return fmt.Errorf("git output files are not allowed")
			case arg == "--ext-diff" || arg == "--textconv" || arg == "--external-diff":
				return fmt.Errorf("git external diff/textconv helpers are not allowed")
			case (arg == "--ignored" || strings.HasPrefix(arg, "--ignored=")) && len(argv) > 1 && argv[1] == "status":
				return fmt.Errorf("git status ignored listing is not allowed")
			case (arg == "-f" || arg == "--file" || strings.HasPrefix(arg, "--file=")) && len(argv) > 1 && argv[1] == "grep":
				return fmt.Errorf("git grep pattern files are not allowed")
			case !strings.HasPrefix(arg, "-") && strings.Contains(arg, ":"):
				ref, pathPart, ok := strings.Cut(arg, ":")
				if ok && strings.TrimSpace(ref) != "" && strings.TrimSpace(pathPart) != "" {
					return fmt.Errorf("git revision pathspecs are not allowed in observation: %s", arg)
				}
			}
		}
		switch argv[1] {
		case "diff", "status", "show", "grep", "ls-files", "rev-parse":
			return nil
		default:
			return fmt.Errorf("git subcommand %s is not allowed", argv[1])
		}
	case "go":
		if len(argv) < 2 {
			return fmt.Errorf("go observation command requires a subcommand")
		}
		switch argv[1] {
		case "test", "build", "fmt", "generate", "mod":
			return fmt.Errorf("go subcommand %s is not allowed for read-only observation; use verifier or repair command lanes", argv[1])
		case "env":
			for _, arg := range argv[2:] {
				if arg == "-w" || strings.HasPrefix(arg, "-w=") || arg == "-u" || strings.HasPrefix(arg, "-u=") {
					return fmt.Errorf("go command flag %s is not allowed", arg)
				}
			}
			return nil
		case "list":
			for i := 2; i < len(argv); i++ {
				arg := argv[i]
				switch {
				case arg == "-mod=mod" || strings.HasPrefix(arg, "-mod=mod"):
					return fmt.Errorf("go command flag %s is not allowed", arg)
				case arg == "-modfile" || strings.HasPrefix(arg, "-modfile="), arg == "-overlay" || strings.HasPrefix(arg, "-overlay="):
					return fmt.Errorf("go command flag %s is not allowed", arg)
				case arg == "-exec" || strings.HasPrefix(arg, "-exec="):
					return fmt.Errorf("go command flag %s is not allowed", arg)
				case arg == "-o" || strings.HasPrefix(arg, "-o="):
					return fmt.Errorf("go command flag %s is not allowed", arg)
				}
			}
			return nil
		case "version", "doc":
			return nil
		default:
			return fmt.Errorf("go subcommand %s is not allowed", argv[1])
		}
	case "multica":
		if !isReadOnlyMulticaIssueCommand(argv) {
			return fmt.Errorf("multica observation only allows read-only issue get/list commands")
		}
		return nil
	default:
		return fmt.Errorf("executable %s is not allowed", argv[0])
	}
}

func isReadOnlyMulticaIssueCommand(argv []string) bool {
	if len(argv) < 3 || argv[0] != "multica" || argv[1] != "issue" {
		return false
	}
	switch argv[2] {
	case "get", "list":
		return true
	case "runs":
		return len(argv) >= 4
	case "comment":
		return len(argv) >= 4 && argv[3] == "list"
	default:
		return false
	}
}

func validateObservationCommandWorkspacePaths(workspaceRoot string, argv []string) error {
	return validateObservationCommandWorkspacePathsWithDeny(workspaceRoot, argv, nil)
}

func validateObservationCommandWorkspacePathsWithDeny(workspaceRoot string, argv []string, denyPatterns []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("observation command argv is required")
	}
	switch argv[0] {
	case "rg":
		return validateRGObservationPaths(workspaceRoot, argv[1:], denyPatterns)
	case "sed":
		return validateSedObservationPaths(workspaceRoot, argv[1:], denyPatterns)
	case "find":
		return validateFindObservationPaths(workspaceRoot, argv[1:], denyPatterns)
	case "git":
		return validateGitObservationPaths(workspaceRoot, argv[1:], denyPatterns)
	case "ls":
		return validateLSObservationPaths(workspaceRoot, argv[1:], denyPatterns)
	case "multica":
		return nil
	default:
		for _, arg := range argv[1:] {
			if err := validateObservationPathOperand(workspaceRoot, arg, denyPatterns); err != nil {
				return err
			}
		}
		return nil
	}
}

func validateRGObservationPaths(workspaceRoot string, args []string, denyPatterns []string) error {
	patternSkipped := rgObservationHasNoPattern(args)
	skipNext := false
	skipNextIsPattern := false
	pathOperandSeen := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			if skipNextIsPattern {
				patternSkipped = true
			}
			skipNextIsPattern = false
			continue
		}
		switch {
		case arg == "-e" || arg == "--regexp":
			skipNext = true
			skipNextIsPattern = true
			continue
		case strings.HasPrefix(arg, "--regexp="):
			patternSkipped = true
			continue
		case rgOptionTakesOperand(arg):
			skipNext = true
			continue
		case rgOptionHasInlineOperand(arg):
			continue
		case strings.HasPrefix(arg, "-"):
			continue
		case !patternSkipped:
			patternSkipped = true
			continue
		default:
			if err := validateObservationPathOperand(workspaceRoot, arg, denyPatterns); err != nil {
				return err
			}
			if err := validateSearchPathDoesNotCoverDenied(workspaceRoot, arg, denyPatterns, "rg", false); err != nil {
				return err
			}
			pathOperandSeen = true
		}
	}
	if !pathOperandSeen {
		if err := validateSearchPathDoesNotCoverDenied(workspaceRoot, ".", denyPatterns, "rg", false); err != nil {
			return err
		}
	}
	return nil
}

func rgObservationHasNoPattern(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--files", "--type-list", "--help", "-h", "--version", "-V", "--pcre2-version":
			return true
		}
	}
	return false
}

func rgOptionTakesOperand(arg string) bool {
	switch arg {
	case "-g", "--glob", "--iglob", "-t", "--type", "-T", "--type-not", "-m", "--max-count", "--max-depth", "-A", "-B", "-C", "--context", "--after-context", "--before-context", "--sort", "--sortr", "--engine", "--encoding", "--path-separator":
		return true
	default:
		return false
	}
}

func rgOptionHasInlineOperand(arg string) bool {
	for _, prefix := range []string{
		"--glob=", "--iglob=", "--type=", "--type-not=", "--max-count=", "--max-depth=",
		"--context=", "--after-context=", "--before-context=", "--sort=", "--sortr=",
		"--engine=", "--encoding=", "--path-separator=",
	} {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

func validateSedObservationPaths(workspaceRoot string, args []string, denyPatterns []string) error {
	scriptSkipped := false
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		switch {
		case arg == "-e":
			skipNext = true
			scriptSkipped = true
			continue
		case strings.HasPrefix(arg, "-"):
			continue
		case !scriptSkipped:
			scriptSkipped = true
			continue
		default:
			if err := validateObservationPathOperand(workspaceRoot, arg, denyPatterns); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFindObservationPaths(workspaceRoot string, args []string, denyPatterns []string) error {
	expressionStarted := false
	skipNext := ""
	pathOperandSeen := false
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if skipNext != "" {
			if arg == "" {
				return fmt.Errorf("find predicate %s requires a non-empty operand", skipNext)
			}
			skipNext = ""
			continue
		}
		switch {
		case arg == "":
			continue
		case !expressionStarted && !isFindExpressionToken(arg):
			if err := validateObservationPathOperand(workspaceRoot, arg, denyPatterns); err != nil {
				return err
			}
			if err := validateSearchPathDoesNotCoverDenied(workspaceRoot, arg, denyPatterns, "find", true); err != nil {
				return err
			}
			pathOperandSeen = true
			continue
		case arg == "!" || arg == "(" || arg == ")" || arg == "-a" || arg == "-and" || arg == "-o" || arg == "-or" || arg == "-print" || arg == "-print0" || arg == "-prune" || arg == "-quit":
			expressionStarted = true
			continue
		case arg == "-H" || arg == "-L" || arg == "-follow":
			return fmt.Errorf("find symlink traversal option %s is not allowed", arg)
		case isFindExternalPathPredicate(arg):
			return fmt.Errorf("find predicate %s is not allowed because it reads an external path operand", arg)
		case findPredicateTakesOperand(arg):
			expressionStarted = true
			if i == len(args)-1 {
				return fmt.Errorf("find predicate %s requires an operand", arg)
			}
			skipNext = arg
			continue
		case strings.HasPrefix(arg, "-"):
			expressionStarted = true
			return fmt.Errorf("find predicate %s is not allowed", arg)
		default:
			return fmt.Errorf("find path operands must appear before expressions: %s", arg)
		}
	}
	if !pathOperandSeen {
		if err := validateObservationPathOperand(workspaceRoot, ".", denyPatterns); err != nil {
			return err
		}
		if err := validateSearchPathDoesNotCoverDenied(workspaceRoot, ".", denyPatterns, "find", true); err != nil {
			return err
		}
	}
	return nil
}

func isFindExpressionToken(arg string) bool {
	return strings.HasPrefix(arg, "-") || arg == "!" || arg == "(" || arg == ")"
}

func isFindExternalPathPredicate(arg string) bool {
	switch arg {
	case "-newer", "-anewer", "-cnewer", "-samefile":
		return true
	}
	return strings.HasPrefix(arg, "-newer")
}

func findPredicateTakesOperand(arg string) bool {
	switch arg {
	case "-maxdepth", "-mindepth", "-name", "-iname", "-path", "-ipath", "-regex", "-iregex",
		"-type", "-size", "-mtime", "-mmin", "-ctime", "-cmin", "-atime", "-amin",
		"-perm", "-user", "-group":
		return true
	default:
		return false
	}
}

func validateGitObservationPaths(workspaceRoot string, args []string, denyPatterns []string) error {
	if len(args) == 0 {
		return fmt.Errorf("git observation command requires a subcommand")
	}
	for _, arg := range args[1:] {
		switch {
		case arg == "--no-index":
			return fmt.Errorf("git --no-index is not allowed")
		case arg == "--output" || strings.HasPrefix(arg, "--output="):
			return fmt.Errorf("git output files are not allowed")
		case arg == "--ext-diff" || arg == "--textconv" || arg == "--external-diff":
			return fmt.Errorf("git external diff/textconv helpers are not allowed")
		case !strings.HasPrefix(arg, "-") && strings.Contains(arg, ":"):
			ref, pathPart, ok := strings.Cut(arg, ":")
			if ok && strings.TrimSpace(ref) != "" && strings.TrimSpace(pathPart) != "" {
				return fmt.Errorf("git revision pathspecs are not allowed in observation: %s", arg)
			}
		}
	}
	switch args[0] {
	case "grep":
		return validateGitGrepObservationPaths(workspaceRoot, args[1:], denyPatterns)
	case "diff", "show", "ls-files":
		return validateGitPathspecObservationPaths(workspaceRoot, args[0], args[1:], denyPatterns, true)
	case "status":
		return validateGitStatusObservationPaths(workspaceRoot, args[1:], denyPatterns)
	case "rev-parse":
		return validateGitRevParseObservationPaths(args[1:])
	default:
		return fmt.Errorf("git subcommand %s is not allowed", args[0])
	}
}

func validateGitGrepObservationPaths(workspaceRoot string, args []string, denyPatterns []string) error {
	separatorSeen := false
	patternSeen := false
	pathOperandSeen := false
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			patternSeen = true
			continue
		}
		if arg == "--" {
			separatorSeen = true
			continue
		}
		if !separatorSeen {
			switch {
			case arg == "-f" || arg == "--file" || strings.HasPrefix(arg, "--file="):
				return fmt.Errorf("git grep pattern files are not allowed")
			case arg == "-e" || arg == "--regexp":
				skipNext = true
				continue
			case strings.HasPrefix(arg, "--regexp="):
				patternSeen = true
				continue
			case strings.HasPrefix(arg, "-"):
				continue
			case !patternSeen:
				patternSeen = true
				continue
			}
		}
		if err := validateObservationPathOperand(workspaceRoot, arg, denyPatterns); err != nil {
			return err
		}
		if err := validateSearchPathDoesNotCoverDenied(workspaceRoot, arg, denyPatterns, "git grep", true); err != nil {
			return err
		}
		pathOperandSeen = true
	}
	if !pathOperandSeen && len(denyPatterns) > 0 {
		return fmt.Errorf("git grep requires an explicit non-denied pathspec when visibility deny rules are active")
	}
	return nil
}

func validateGitStatusObservationPaths(workspaceRoot string, args []string, denyPatterns []string) error {
	separatorSeen := false
	for _, arg := range args {
		if arg == "--" {
			separatorSeen = true
			continue
		}
		if !separatorSeen && strings.HasPrefix(arg, "-") {
			continue
		}
		if err := validateObservationPathOperand(workspaceRoot, arg, denyPatterns); err != nil {
			return err
		}
		if err := validateSearchPathDoesNotCoverDenied(workspaceRoot, arg, denyPatterns, "git status", true); err != nil {
			return err
		}
	}
	return nil
}

func validateGitPathspecObservationPaths(workspaceRoot, subcommand string, args []string, denyPatterns []string, requireExplicitPathspec bool) error {
	separatorSeen := false
	pathOperandSeen := false
	for _, arg := range args {
		if arg == "--" {
			separatorSeen = true
			continue
		}
		if !separatorSeen {
			if isLikelyGitPathspec(arg) {
				return fmt.Errorf("git %s path operands must follow --: %s", subcommand, arg)
			}
			continue
		}
		if err := validateObservationPathOperand(workspaceRoot, arg, denyPatterns); err != nil {
			return err
		}
		if err := validateSearchPathDoesNotCoverDenied(workspaceRoot, arg, denyPatterns, "git "+subcommand, true); err != nil {
			return err
		}
		pathOperandSeen = true
	}
	if requireExplicitPathspec && len(denyPatterns) > 0 && !pathOperandSeen {
		return fmt.Errorf("git %s requires an explicit non-denied pathspec when visibility deny rules are active", subcommand)
	}
	return nil
}

func validateGitRevParseObservationPaths(args []string) error {
	for _, arg := range args {
		if strings.TrimSpace(arg) == "--" {
			return fmt.Errorf("git rev-parse pathspec mode is not allowed")
		}
	}
	return nil
}

func isLikelyGitPathspec(arg string) bool {
	arg = strings.TrimSpace(arg)
	if arg == "" || strings.HasPrefix(arg, "-") {
		return false
	}
	return arg == "." || arg == ".." || strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") || strings.HasPrefix(arg, "/") || strings.Contains(arg, "/")
}

func validateObservationPathOperand(workspaceRoot, arg string, denyPatterns []string) error {
	rel, err := normalizedObservationPathRel(workspaceRoot, arg)
	if err != nil {
		return err
	}
	if rel == "" {
		return nil
	}
	if pathDeniedByPatterns(rel, denyPatterns) {
		return fmt.Errorf("observation command path is denied by visibility rules: %s", arg)
	}
	return nil
}

func normalizedObservationPathRel(workspaceRoot, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" || strings.HasPrefix(arg, "-") {
		return "", nil
	}
	workspaceEval := workspaceRoot
	if resolved, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
		workspaceEval = resolved
	}
	full := arg
	if !filepath.IsAbs(full) {
		full = filepath.Join(workspaceRoot, filepath.FromSlash(arg))
	}
	full = filepath.Clean(full)
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		full = resolved
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve observation command path %s: %w", arg, err)
	}
	rel, err := filepath.Rel(workspaceEval, full)
	if err != nil {
		return "", fmt.Errorf("resolve observation command path %s: %w", arg, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("observation command path escapes workspace: %s", arg)
	}
	return rel, nil
}

func validateLSObservationPaths(workspaceRoot string, args []string, denyPatterns []string) error {
	pathOperandSeen := false
	for _, arg := range args {
		if strings.TrimSpace(arg) == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		if err := validateObservationPathOperand(workspaceRoot, arg, denyPatterns); err != nil {
			return err
		}
		if err := validateSearchPathDoesNotCoverDenied(workspaceRoot, arg, denyPatterns, "ls", false); err != nil {
			return err
		}
		pathOperandSeen = true
	}
	if !pathOperandSeen {
		if err := validateSearchPathDoesNotCoverDenied(workspaceRoot, ".", denyPatterns, "ls", false); err != nil {
			return err
		}
	}
	return nil
}

func validateSearchPathDoesNotCoverDenied(workspaceRoot, arg string, denyPatterns []string, command string, includesHidden bool) error {
	rel, err := normalizedObservationPathRel(workspaceRoot, arg)
	if err != nil {
		return err
	}
	if rel == "" {
		return nil
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	for _, pattern := range denyPatterns {
		pattern = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(strings.TrimSpace(pattern))), "./")
		if pattern == "" || pattern == "." {
			continue
		}
		if !includesHidden && deniedPatternIsHidden(pattern) {
			continue
		}
		if rel == "." || rel == pattern || strings.HasPrefix(pattern, rel+"/") {
			return fmt.Errorf("%s path %s includes denied visibility path %s", command, arg, pattern)
		}
		if strings.ContainsAny(pattern, "*?[") {
			return fmt.Errorf("%s path %s may include denied visibility pattern %s", command, arg, pattern)
		}
	}
	return nil
}

func deniedPatternIsHidden(pattern string) bool {
	pattern = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(strings.TrimSpace(pattern))), "./")
	if pattern == "" || pattern == "." {
		return false
	}
	first, _, _ := strings.Cut(pattern, "/")
	return strings.HasPrefix(first, ".")
}

func pathDeniedByPatterns(rel string, denyPatterns []string) bool {
	rel = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(rel), "./"))
	if rel == "" || rel == "." {
		return false
	}
	segments := strings.Split(rel, "/")
	for _, pattern := range denyPatterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if rel == pattern || strings.HasPrefix(rel, pattern+"/") {
			return true
		}
		for _, segment := range segments {
			if segment == pattern {
				return true
			}
		}
	}
	return false
}

func exitCodeFromError(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func excerptText(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxChars <= 0 || len(text) <= maxChars {
		return text
	}
	head := maxChars / 2
	tail := maxChars - head
	return strings.TrimSpace(text[:head]) + "\n...[truncated]...\n" + strings.TrimSpace(text[len(text)-tail:])
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func fileHash(path string) (bool, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		return false, "", err
	}
	sum := sha256.Sum256(data)
	return true, hex.EncodeToString(sum[:]), nil
}

func validateWorkspaceEditConstraints(spec task.Spec, rel string) error {
	if constraint, ok := blockedTestFileConstraint(spec, rel); ok {
		return fmt.Errorf("workspace edit path %s violates task constraint: %s", rel, constraint)
	}
	return nil
}

func blockedTestFileConstraint(spec task.Spec, rel string) (string, bool) {
	if !strings.HasSuffix(rel, "_test.go") {
		return "", false
	}
	for _, constraint := range spec.Constraints {
		normalized := strings.ToLower(strings.TrimSpace(constraint))
		if normalized == "" {
			continue
		}
		mentionsTests := strings.Contains(normalized, "_test.go") ||
			strings.Contains(normalized, "test.go") ||
			strings.Contains(normalized, "test file") ||
			strings.Contains(normalized, "tests")
		forbidsMutation := strings.Contains(normalized, "do not modify") ||
			strings.Contains(normalized, "don't modify") ||
			strings.Contains(normalized, "must not modify") ||
			strings.Contains(normalized, "without modifying") ||
			strings.Contains(normalized, "keep") ||
			strings.Contains(normalized, "leave")
		if mentionsTests && forbidsMutation {
			return constraint, true
		}
	}
	return "", false
}

func verificationFingerprint(report task.VerificationReport) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(report.Status))
	b.WriteByte('\n')
	b.WriteString(strings.TrimSpace(report.FailureSummary))
	for _, check := range report.Checks {
		b.WriteByte('\n')
		b.WriteString(strings.TrimSpace(check.Name))
		b.WriteByte('|')
		b.WriteString(strings.TrimSpace(check.Status))
		b.WriteByte('|')
		b.WriteString(strings.TrimSpace(check.Summary))
	}
	return b.String()
}

func (s *Service) codingRepairBudget() int {
	budget := s.Config.Provider.CodingRepairBudget
	if budget <= 0 {
		return 1
	}
	return budget
}

func (s *Service) codingObservationCommandBudget() int {
	if s.Config.Provider.CodingObservationCommandBudget < 0 {
		return 0
	}
	return s.Config.Provider.CodingObservationCommandBudget
}

func (s *Service) codingObservationCommandTimeout() time.Duration {
	timeout := time.Duration(s.Config.Provider.CodingObservationCommandTimeoutSeconds) * time.Second
	if timeout <= 0 {
		return 0
	}
	return timeout
}

func (s *Service) codingExecutionCommandBudget() int {
	if s.Config.Provider.CodingExecutionCommandBudget < 0 {
		return 0
	}
	return s.Config.Provider.CodingExecutionCommandBudget
}

func (s *Service) codingExecutionCommandTimeout() time.Duration {
	timeout := time.Duration(s.Config.Provider.CodingExecutionCommandTimeoutSeconds) * time.Second
	if timeout <= 0 {
		return 0
	}
	return timeout
}

func (s *Service) Status(ctx context.Context, taskID string) (task.StatusSnapshot, error) {
	_ = ctx
	_, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.StatusSnapshot{}, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.StatusSnapshot{}, err
	}
	snapshot := task.StatusSnapshot{
		ObjectKind:          "status_snapshot",
		SchemaVersion:       task.SchemaVersion,
		TaskID:              taskID,
		Phase:               state.Phase,
		State:               state.State,
		StatusReasonCode:    state.StatusReasonCode,
		StatusDetailRef:     state.StatusDetailRef,
		PlanRef:             "plan.json",
		ProgressRef:         "progress.md",
		HandoffRef:          "",
		LastCheckpointRef:   state.LastCheckpointRef,
		RestoreClues:        s.statusRestoreClues(taskID, state.LastCheckpointRef),
		CurrentStepID:       state.CurrentStepID,
		LastEventRef:        state.LastEventRef,
		LastVerificationRef: state.LastVerificationRef,
		LastReviewRef:       state.LastReviewRef,
		CompletionRef:       state.LastCompletionRef,
		UpdatedAt:           state.UpdatedAt,
	}
	if plan, planErr := s.Store.LoadPlan(taskID); planErr == nil {
		snapshot.PlanRevision = plan.Revision
		snapshot.CurrentSystemStepID = plan.CurrentSystemStepID
		snapshot.CurrentExecutionStepID = plan.CurrentExecutionStepID
	}
	if mission, missionErr := s.statusMissionForTask(taskID); missionErr != nil {
		return task.StatusSnapshot{}, missionErr
	} else if mission != nil {
		snapshot.MissionID = mission.MissionID
		snapshot.MissionRef = missionWorkspaceRef(mission.MissionID, "mission.json")
		snapshot.MissionStatus = mission.Status
		snapshot.MissionStatusReasonCode = mission.StatusReasonCode
		snapshot.MissionCurrentMilestoneID = mission.CurrentMilestoneID
		if strings.TrimSpace(mission.LatestValidationRef) != "" {
			snapshot.MissionLatestValidationRef = missionWorkspaceRef(mission.MissionID, mission.LatestValidationRef)
		}
	}
	if s.Store.HandoffExists(taskID) {
		snapshot.HandoffRef = "handoff.md"
	}
	return snapshot, nil
}

func (s *Service) statusRestoreClues(taskID, checkpointRef string) []task.RestoreClue {
	checkpoint, err := s.Store.LoadLatestCheckpoint(taskID)
	if err != nil {
		return nil
	}
	ref := strings.TrimSpace(checkpointRef)
	if ref == "" {
		ref = filepath.ToSlash(filepath.Join("checkpoints", checkpoint.CheckpointID+".json"))
	}
	clue := task.RestoreClue{
		Ref:     ref,
		Summary: restoreClueSummary(checkpoint),
	}
	if checkpoint.WorkspaceSnapshot != nil && checkpoint.WorkspaceSnapshot.Git != nil {
		git := *checkpoint.WorkspaceSnapshot.Git
		clue.Git = &git
	}
	if baseline, baselineErr := s.Store.LoadBaseline(taskID); baselineErr == nil {
		clue.CommandHints = append([]task.CommandHint(nil), baseline.CommandHints...)
	}
	return []task.RestoreClue{clue}
}

func restoreClueSummary(checkpoint task.Checkpoint) string {
	var parts []string
	if checkpoint.Phase != "" || checkpoint.State != "" {
		parts = append(parts, fmt.Sprintf("checkpoint captured at phase=%s state=%s", checkpoint.Phase, checkpoint.State))
	}
	if checkpoint.WorkspaceSnapshot != nil && checkpoint.WorkspaceSnapshot.Git != nil {
		git := checkpoint.WorkspaceSnapshot.Git
		if git.IsRepository {
			parts = append(parts, fmt.Sprintf("git branch=%s head=%s dirty=%t", firstNonEmpty(git.Branch, "-"), firstNonEmpty(git.Head, "-"), git.Dirty))
			if len(git.ChangedPaths) > 0 {
				parts = append(parts, fmt.Sprintf("changed_paths=%s", strings.Join(git.ChangedPaths, ",")))
			}
		}
	}
	if len(parts) == 0 {
		return "checkpoint restore clue is available"
	}
	return strings.Join(parts, "; ")
}

func (s *Service) Review(ctx context.Context, taskID string) (task.ReviewReport, error) {
	_ = ctx
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.ReviewReport{}, err
	}
	criteria, err := s.Store.LoadCriteria(taskID)
	if err != nil {
		return task.ReviewReport{}, err
	}
	verification, verifyErr := s.Store.LoadVerification(taskID)
	hasVerification := verifyErr == nil
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.ReviewReport{}, err
	}
	handoffExists := s.Store.HandoffExists(taskID)
	if hasVerification {
		state, err = s.refreshTaskPlan(spec, state)
		if err != nil {
			return task.ReviewReport{}, err
		}
		provisionalReview := task.ReviewReport{
			SchemaVersion: task.SchemaVersion,
			TaskID:        spec.TaskID,
			Status:        "pending",
			Summary:       "Review has not completed yet.",
		}
		provisionalCompletion := task.CompletionReport{
			SchemaVersion: task.SchemaVersion,
			TaskID:        spec.TaskID,
			Status:        "rejected",
			Summary:       "Completion has not been evaluated yet.",
		}
		if err := s.Store.SaveHandoff(taskID, []byte(s.renderHandoff(spec, state, verification, criteria, provisionalReview, provisionalCompletion))); err != nil {
			return task.ReviewReport{}, err
		}
		handoffExists = true
	}
	var (
		report  task.ReviewReport
		finding *task.Finding
	)
	switch {
	case verifyErr == nil:
		reviewInput, err := s.buildReviewInput(ctx, spec, verification, handoffExists, criteria)
		if err != nil {
			return task.ReviewReport{}, err
		}
		var findings []task.Finding
		report, findings = review.EvaluateWithContext(reviewInput)
		if len(findings) > 0 {
			finding = &findings[0]
			if err := s.appendReviewFindings(findings); err != nil {
				return task.ReviewReport{}, err
			}
		}
	case os.IsNotExist(verifyErr):
		report, finding = reviewWithoutVerification(spec)
	default:
		return task.ReviewReport{}, verifyErr
	}
	if finding != nil && verifyErr != nil {
		if err := s.Store.AppendFinding(*finding); err != nil {
			return task.ReviewReport{}, err
		}
		report.BlockingFindingRefs = []string{"findings.jsonl#finding_id=" + finding.FindingID}
	}
	if err := s.Store.SaveReview(report); err != nil {
		return task.ReviewReport{}, err
	}
	state.Phase = task.PhaseReview
	state.LastReviewRef = "reviews/latest.json"
	if report.Status == "blocking" {
		state.State = task.StateBlocked
		state.StatusReasonCode = "blocked_review"
		state.StatusDetailRef = "reviews/latest.json"
	} else if state.State == task.StateBlocked && state.StatusReasonCode == "blocked_review" {
		state.State = task.StateActive
		state.StatusReasonCode = ""
		state.StatusDetailRef = ""
	}
	event := newEvent(taskID, state, "review_completed", report.Summary, append([]string{"reviews/latest.json"}, report.BlockingFindingRefs...))
	if err := s.Store.AppendEvent(event); err != nil {
		return task.ReviewReport{}, err
	}
	state.LastEventRef = artifact.EventRef(event.EventID)
	completion := s.buildCompletion(spec, criteria, report, handoffExists)
	if err := s.Store.SaveCompletion(completion); err != nil {
		return task.ReviewReport{}, err
	}
	state.LastCompletionRef = "completion/latest.json"
	switch completion.Status {
	case "accepted":
		state.State = task.StateDone
		state.StatusReasonCode = ""
		state.StatusDetailRef = ""
		doneEvent := newEvent(taskID, state, "done", "Done gate passed.", []string{"completion/latest.json", "handoff.md"})
		if err := s.Store.AppendEvent(doneEvent); err != nil {
			return task.ReviewReport{}, err
		}
		state.LastEventRef = artifact.EventRef(doneEvent.EventID)
	default:
		state.State = task.StateBlocked
		state.StatusReasonCode = "blocked_review"
		state.StatusDetailRef = "reviews/latest.json"
		rejectEvent := newEvent(taskID, state, "completion_rejected", completion.Summary, []string{"completion/latest.json"})
		if err := s.Store.AppendEvent(rejectEvent); err != nil {
			return task.ReviewReport{}, err
		}
		state.LastEventRef = artifact.EventRef(rejectEvent.EventID)
	}
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return task.ReviewReport{}, err
	}
	if hasVerification {
		state, err = s.refreshTaskPlan(spec, state)
		if err != nil {
			return task.ReviewReport{}, err
		}
		if err := s.Store.SaveHandoff(taskID, []byte(s.renderHandoff(spec, state, verification, criteria, report, completion))); err != nil {
			return task.ReviewReport{}, err
		}
	}
	if err := s.syncTaskNarrative(spec, state, completion.Summary); err != nil {
		return task.ReviewReport{}, err
	}
	if _, err := s.captureHarnessEvaluation(ctx, taskID, "review"); err != nil {
		return task.ReviewReport{}, err
	}
	return report, nil
}

func (s *Service) buildReviewInput(ctx context.Context, spec task.Spec, verification task.VerificationReport, handoffExists bool, criteria task.CriteriaSnapshot) (review.Input, error) {
	changedPaths := s.reviewChangedPaths(ctx, spec)
	workerEvidence := s.reviewWorkerEvidence(spec.TaskID, criteria)
	quality, qualityFindings, err := s.captureQualityDiagnostic(spec, changedPaths)
	if err != nil {
		return review.Input{}, err
	}
	contextRefs := s.reviewContextRefs(spec.TaskID, handoffExists, workerEvidence)
	if quality.DiagnosticID != "" {
		contextRefs = append(contextRefs, "diagnostics/quality-latest.json")
	}
	return review.Input{
		Spec:            spec,
		Verification:    verification,
		HandoffExists:   handoffExists,
		Criteria:        criteria,
		ContextRefs:     contextRefs,
		ChangedPaths:    changedPaths,
		ScopeDriftPaths: s.reviewScopeDriftPaths(spec, changedPaths),
		WorkerEvidence:  workerEvidence,
		QualityFindings: qualityFindings,
	}, nil
}

func (s *Service) appendReviewFindings(findings []task.Finding) error {
	for _, finding := range findings {
		if err := s.Store.AppendFinding(finding); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reviewContextRefs(taskID string, handoffExists bool, workers []review.WorkerEvidence) []string {
	refs := []string{"baseline.json", "verification/latest.json", "criteria/latest.json"}
	if handoffExists {
		refs = append(refs, "handoff.md")
	}
	if s.Store.CompletionExists(taskID) {
		refs = append(refs, "completion/latest.json")
	}
	if _, err := s.Store.LoadSprint(taskID); err == nil {
		refs = append(refs, "sprint/latest.json")
	}
	if _, err := s.Store.LoadContinuity(taskID); err == nil {
		refs = append(refs, "continuity/latest.json")
	}
	if _, err := s.Store.LoadContextSummary(taskID); err == nil {
		refs = append(refs, "context/latest-pack.json")
	}
	if _, err := s.Store.LoadProject(); err == nil {
		refs = append(refs, "workspace:.ngen/project/project.json")
	}
	for _, worker := range workers {
		refs = append(refs, worker.ContractRef, worker.ResultRef, worker.SettlementRef, worker.ReconcileRef)
	}
	return uniqueRefs(refs)
}

func (s *Service) reviewChangedPaths(ctx context.Context, spec task.Spec) []string {
	var paths []string
	snapshot := verify.CaptureWorkspaceSnapshot(ctx, spec.WorkspaceRoot)
	if snapshot != nil && snapshot.Git != nil {
		paths = append(paths, snapshot.Git.ChangedPaths...)
	}
	if edits, err := s.Store.ReadWorkspaceEdits(spec.TaskID); err == nil {
		for _, edit := range edits {
			for _, change := range edit.FileChanges {
				paths = append(paths, strings.TrimSpace(change.Path))
			}
		}
	}
	paths = uniqueStrings(paths)
	sort.Strings(paths)
	return paths
}

func (s *Service) reviewScopeDriftPaths(spec task.Spec, changedPaths []string) []string {
	if len(changedPaths) == 0 {
		return nil
	}
	sprint, err := s.Store.LoadSprint(spec.TaskID)
	if err != nil || len(sprint.WorkingSetPaths) == 0 {
		return nil
	}
	var drift []string
	for _, changed := range changedPaths {
		changed = filepath.ToSlash(strings.TrimSpace(changed))
		if changed == "" {
			continue
		}
		if strings.HasPrefix(changed, ".ngen/") || reviewPathAllowedByScope(changed, sprint.WorkingSetPaths) {
			continue
		}
		drift = append(drift, changed)
	}
	return uniqueStrings(drift)
}

func reviewPathAllowedByScope(path string, scopes []string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	for _, scope := range scopes {
		scope = filepath.ToSlash(strings.TrimSpace(scope))
		if scope == "" {
			continue
		}
		if strings.ContainsAny(scope, "*?[") {
			if ok, _ := filepath.Match(scope, path); ok {
				return true
			}
			continue
		}
		scope = strings.TrimSuffix(scope, "/")
		if path == scope || strings.HasPrefix(path, scope+"/") {
			return true
		}
	}
	return false
}

func (s *Service) reviewWorkerEvidence(parentTaskID string, criteria task.CriteriaSnapshot) []review.WorkerEvidence {
	contracts, err := s.Store.ListWorkerContracts(parentTaskID)
	if err != nil || len(contracts) == 0 {
		return nil
	}
	usedByCriteria := workerIDsFromCriteria(criteria)
	evidence := make([]review.WorkerEvidence, 0, len(contracts))
	for _, contract := range contracts {
		item := review.WorkerEvidence{
			WorkerID:                   contract.WorkerID,
			Role:                       contract.Role,
			ContractRef:                filepath.ToSlash(filepath.Join("workers", contract.WorkerID+".json")),
			ResultRef:                  contract.ResultRef,
			SettlementRef:              contract.SettlementRef,
			ReconcileRef:               contract.ReconcileRef,
			SettlementStatus:           contract.SettlementStatus,
			CompletionStatus:           contract.CompletionStatus,
			ReviewStatus:               contract.ReviewStatus,
			VerificationStatus:         contract.VerificationStatus,
			ReconcileStatus:            contract.ReconcileStatus,
			RequiresParentAction:       contract.RequiresParentAction,
			ParentActionType:           contract.ParentActionType,
			EvidenceScore:              contract.EvidenceScore,
			EvidenceGrade:              contract.EvidenceGrade,
			TrustedForParentCompletion: contract.TrustedForParentCompletion,
			UsedByCriteria:             usedByCriteria[contract.WorkerID],
		}
		if result, err := s.Store.LoadWorkerResult(parentTaskID, contract.WorkerID); err == nil {
			item.ChildState = result.ChildState
			item.ResultRef = firstNonEmpty(item.ResultRef, artifact.WorkerResultRef(contract.WorkerID))
			item.CompletionStatus = firstNonEmpty(result.CompletionStatus, item.CompletionStatus)
			item.ReviewStatus = firstNonEmpty(result.ReviewStatus, item.ReviewStatus)
			item.VerificationStatus = firstNonEmpty(result.VerificationStatus, item.VerificationStatus)
			item.RequiresParentAction = result.RequiresParentAction || item.RequiresParentAction
			item.ParentActionType = firstNonEmpty(result.ParentActionType, item.ParentActionType)
			if result.EvidenceScore > 0 {
				item.EvidenceScore = result.EvidenceScore
			}
			item.EvidenceGrade = firstNonEmpty(result.EvidenceGrade, item.EvidenceGrade)
			item.TrustedForParentCompletion = result.TrustedForParentCompletion || item.TrustedForParentCompletion
		}
		if settlement, err := s.Store.LoadWorkerSettlement(parentTaskID, contract.WorkerID); err == nil {
			item.SettlementRef = firstNonEmpty(item.SettlementRef, artifact.WorkerSettlementRef(contract.WorkerID))
			item.SettlementStatus = firstNonEmpty(settlement.Status, item.SettlementStatus)
		}
		if reconcile, err := s.Store.LoadWorkerReconcile(parentTaskID, contract.WorkerID); err == nil {
			item.ReconcileRef = firstNonEmpty(item.ReconcileRef, artifact.WorkerReconcileRef(contract.WorkerID))
			item.ReconcileStatus = firstNonEmpty(reconcile.Status, item.ReconcileStatus)
		}
		evidence = append(evidence, item)
	}
	return evidence
}

func workerIDsFromCriteria(criteria task.CriteriaSnapshot) map[string]bool {
	ids := make(map[string]bool)
	for _, criterion := range criteria.Criteria {
		for _, ref := range criterion.EvidenceRefs {
			ref = strings.TrimSpace(ref)
			if !strings.HasPrefix(ref, "workers/") || !strings.HasSuffix(ref, ".json") {
				continue
			}
			workerID := strings.TrimSuffix(strings.TrimPrefix(ref, "workers/"), ".json")
			if workerID != "" {
				ids[workerID] = true
			}
		}
	}
	return ids
}

func reviewWithoutVerification(spec task.Spec) (task.ReviewReport, *task.Finding) {
	finding := &task.Finding{
		SchemaVersion:     task.SchemaVersion,
		FindingID:         task.NewID("F"),
		TaskID:            spec.TaskID,
		TS:                task.Now(),
		Severity:          "high",
		Category:          review.CategoryMissingEvidence,
		Status:            "open",
		BlocksCompletion:  true,
		Claim:             "Review was requested before verification artifacts existed.",
		EvidenceRefs:      []string{"verification/latest.json"},
		RecommendedAction: "Run the verifier before requesting review.",
	}
	return task.ReviewReport{
		SchemaVersion:       task.SchemaVersion,
		TaskID:              spec.TaskID,
		ReviewID:            task.NewID("REV"),
		Status:              "blocking",
		Summary:             "review blocked because verification has not run yet.",
		ReviewerProfile:     runtimeReviewerProfile(spec),
		ReviewContextRefs:   []string{"criteria/latest.json"},
		RiskSummary:         task.ReviewRiskSummary{BlockingCount: 1, MissingEvidence: 1},
		BlockingCategories:  []string{review.CategoryMissingEvidence},
		BlockingFindingRefs: []string{"findings.jsonl#finding_id=" + finding.FindingID},
		ReviewedAt:          task.Now(),
	}, finding
}

func runtimeReviewerProfile(spec task.Spec) string {
	switch spec.Kind {
	case task.KindReviewer:
		return "reviewer"
	case task.KindSecurityReview:
		return "security_review"
	case task.KindCoding:
		return "coding_reviewer"
	case task.KindGeneral:
		return "general_execution_reviewer"
	default:
		if strings.TrimSpace(string(spec.Kind)) == "" {
			return "reviewer"
		}
		return string(spec.Kind) + "_reviewer"
	}
}

func (s *Service) TailEvents(taskID string, limit int) ([]task.Event, error) {
	return s.TailEventsAfter(taskID, "", limit)
}

func (s *Service) TailEventsAfter(taskID, afterEventID string, limit int) ([]task.Event, error) {
	events, err := s.Store.ReadEvents(taskID)
	if err != nil {
		return nil, err
	}
	afterEventID = strings.TrimSpace(afterEventID)
	if afterEventID != "" {
		found := false
		for i, event := range events {
			if event.EventID == afterEventID {
				events = events[i+1:]
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("event cursor not found: %s", afterEventID)
		}
	}
	if limit <= 0 || len(events) <= limit {
		return events, nil
	}
	return events[len(events)-limit:], nil
}

func (s *Service) ListApprovals(ctx context.Context, taskID string) ([]task.ApprovalRecord, error) {
	_ = ctx
	if _, err := s.Store.LoadTask(taskID); err != nil {
		return nil, err
	}
	records, err := s.Store.ReadApprovals(taskID)
	if err != nil {
		if os.IsNotExist(err) {
			return []task.ApprovalRecord{}, nil
		}
		return nil, err
	}
	return records, nil
}

func (s *Service) ListOwnedApprovals(ctx context.Context, taskID string) ([]task.ApprovalRecord, error) {
	workers, err := s.Store.ListWorkerContracts(taskID)
	if err != nil {
		return nil, err
	}
	owned := make([]task.ApprovalRecord, 0)
	for _, worker := range workers {
		records, err := s.Store.ReadApprovals(worker.ChildTaskID)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, record := range records {
			if record.OwnerTaskID != taskID || record.OwnerWorkerID != worker.WorkerID {
				continue
			}
			owned = append(owned, record)
		}
	}
	sort.SliceStable(owned, func(i, j int) bool {
		if owned[i].TS == owned[j].TS {
			return owned[i].ApprovalRecordID < owned[j].ApprovalRecordID
		}
		return owned[i].TS < owned[j].TS
	})
	return owned, nil
}

func (s *Service) approvalOwner(taskID string) (string, string, error) {
	contract, err := s.Store.LoadWorkerContractByChildTask(taskID)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}
	return contract.ParentTaskID, contract.WorkerID, nil
}

func (s *Service) RequestApproval(ctx context.Context, taskID, scope, reason string) (task.ApprovalRecord, error) {
	_ = ctx
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.ApprovalRecord{}, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.ApprovalRecord{}, err
	}
	ownerTaskID, ownerWorkerID, err := s.approvalOwner(taskID)
	if err != nil {
		return task.ApprovalRecord{}, err
	}
	record := task.ApprovalRecord{
		SchemaVersion:    task.SchemaVersion,
		ApprovalRecordID: task.NewID("APRREC"),
		ApprovalID:       task.NewID("APR"),
		TaskID:           taskID,
		OwnerTaskID:      ownerTaskID,
		OwnerWorkerID:    ownerWorkerID,
		TS:               task.Now(),
		Kind:             "approval_request",
		Status:           "pending",
		Scope:            scope,
		Reason:           reason,
	}
	if err := s.Store.AppendApproval(record); err != nil {
		return task.ApprovalRecord{}, err
	}
	event := newEvent(taskID, state, "approval_requested", fmt.Sprintf("Approval requested: %s", scope), []string{artifact.ApprovalRecordRef(record.ApprovalRecordID)})
	if err := s.Store.AppendEvent(event); err != nil {
		return task.ApprovalRecord{}, err
	}
	state.State = task.StateBlocked
	state.StatusReasonCode = "blocked_policy"
	state.StatusDetailRef = artifact.ApprovalRecordRef(record.ApprovalRecordID)
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return task.ApprovalRecord{}, err
	}
	if err := s.syncTaskNarrative(spec, state, "Waiting for approval."); err != nil {
		return task.ApprovalRecord{}, err
	}
	if state.PermissionModeID == task.PermissionModeYolo {
		return s.DecideApproval(ctx, taskID, record.ApprovalID, "approved")
	}
	return record, nil
}

func (s *Service) RequestInput(ctx context.Context, taskID, field, prompt string, required bool) (task.InputRequestRecord, error) {
	_ = ctx
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.InputRequestRecord{}, err
	}
	if strings.TrimSpace(prompt) == "" {
		return task.InputRequestRecord{}, fmt.Errorf("input prompt is required")
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.InputRequestRecord{}, err
	}
	if pending, ok, err := s.pendingInputRequest(taskID); err != nil {
		return task.InputRequestRecord{}, err
	} else if ok {
		return task.InputRequestRecord{}, fmt.Errorf("pending input request already exists: %s", pending.RequestID)
	}
	record := task.InputRequestRecord{
		SchemaVersion: task.SchemaVersion,
		InputRecordID: task.NewID("INPREC"),
		RequestID:     task.NewID("INP"),
		TaskID:        taskID,
		TS:            task.Now(),
		Kind:          "input_request",
		Status:        "pending",
		Field:         field,
		Prompt:        prompt,
		Required:      required,
	}
	if err := s.Store.AppendInputRequest(record); err != nil {
		return task.InputRequestRecord{}, err
	}
	summary := "Requested operator input."
	if strings.TrimSpace(field) != "" {
		summary = fmt.Sprintf("Requested operator input for %s.", strings.TrimSpace(field))
	}
	event := newEvent(taskID, state, "input_requested", summary, []string{artifact.InputRequestRecordRef(record.InputRecordID)})
	if err := s.Store.AppendEvent(event); err != nil {
		return task.InputRequestRecord{}, err
	}
	state.State = task.StateBlocked
	state.StatusReasonCode = "blocked_missing_input"
	state.StatusDetailRef = artifact.InputRequestRecordRef(record.InputRecordID)
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return task.InputRequestRecord{}, err
	}
	if err := s.syncTaskNarrative(spec, state, prompt); err != nil {
		return task.InputRequestRecord{}, err
	}
	return record, nil
}

func (s *Service) DecideApproval(ctx context.Context, taskID, approvalID, decision string) (task.ApprovalRecord, error) {
	_ = ctx
	targetTaskID, last, err := s.resolveApprovalTarget(taskID, approvalID)
	if err != nil {
		return task.ApprovalRecord{}, err
	}
	if decision != "approved" && decision != "denied" {
		return task.ApprovalRecord{}, fmt.Errorf("unsupported decision: %s", decision)
	}
	spec, err := s.Store.LoadTask(targetTaskID)
	if err != nil {
		return task.ApprovalRecord{}, err
	}
	state, err := s.loadStateOrRecover(targetTaskID)
	if err != nil {
		return task.ApprovalRecord{}, err
	}
	record := task.ApprovalRecord{
		SchemaVersion:    task.SchemaVersion,
		ApprovalRecordID: task.NewID("APRREC"),
		ApprovalID:       approvalID,
		TaskID:           targetTaskID,
		OwnerTaskID:      last.OwnerTaskID,
		OwnerWorkerID:    last.OwnerWorkerID,
		TS:               task.Now(),
		Kind:             "approval_decision",
		Status:           decision,
		Scope:            last.Scope,
		Reason:           last.Reason,
	}
	if err := s.Store.AppendApproval(record); err != nil {
		return task.ApprovalRecord{}, err
	}
	event := newEvent(targetTaskID, state, "approval_decided", fmt.Sprintf("Approval %s: %s", approvalID, decision), []string{artifact.ApprovalRecordRef(record.ApprovalRecordID)})
	if err := s.Store.AppendEvent(event); err != nil {
		return task.ApprovalRecord{}, err
	}
	if decision == "approved" {
		state.State = task.StateActive
		state.StatusReasonCode = ""
		state.StatusDetailRef = ""
	} else {
		state.State = task.StateBlocked
		state.StatusReasonCode = "blocked_policy"
		state.StatusDetailRef = artifact.ApprovalRecordRef(record.ApprovalRecordID)
	}
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return task.ApprovalRecord{}, err
	}
	if err := s.syncTaskNarrative(spec, state, fmt.Sprintf("Approval %s %s.", approvalID, decision)); err != nil {
		return task.ApprovalRecord{}, err
	}
	return record, nil
}

func (s *Service) resolveApprovalTarget(taskID, approvalID string) (string, task.ApprovalRecord, error) {
	if _, err := s.Store.LoadTask(taskID); err != nil {
		return "", task.ApprovalRecord{}, err
	}
	records, err := s.Store.ReadApprovals(taskID)
	if err != nil && !os.IsNotExist(err) {
		return "", task.ApprovalRecord{}, err
	}
	if last, ok := latestApprovalRecord(records, approvalID); ok {
		return taskID, last, nil
	}
	workers, err := s.Store.ListWorkerContracts(taskID)
	if err != nil {
		return "", task.ApprovalRecord{}, err
	}
	for _, worker := range workers {
		childRecords, err := s.Store.ReadApprovals(worker.ChildTaskID)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", task.ApprovalRecord{}, err
		}
		last, ok := latestApprovalRecord(childRecords, approvalID)
		if !ok {
			continue
		}
		if last.OwnerTaskID != taskID || last.OwnerWorkerID != worker.WorkerID {
			continue
		}
		return worker.ChildTaskID, last, nil
	}
	return "", task.ApprovalRecord{}, fmt.Errorf("approval not found: %s", approvalID)
}

func latestApprovalRecord(records []task.ApprovalRecord, approvalID string) (task.ApprovalRecord, bool) {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].ApprovalID == approvalID {
			return records[i], true
		}
	}
	return task.ApprovalRecord{}, false
}

func (s *Service) ListInputRequests(ctx context.Context, taskID string) ([]task.InputRequestRecord, error) {
	_ = ctx
	if _, err := s.Store.LoadTask(taskID); err != nil {
		return nil, err
	}
	return s.Store.ReadInputRequests(taskID)
}

func (s *Service) RespondInput(ctx context.Context, taskID, requestID, response string) (task.InputRequestRecord, error) {
	_ = ctx
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.InputRequestRecord{}, err
	}
	records, err := s.Store.ReadInputRequests(taskID)
	if err != nil {
		return task.InputRequestRecord{}, err
	}
	var latest *task.InputRequestRecord
	for i := range records {
		if records[i].RequestID == requestID {
			latest = &records[i]
		}
	}
	if latest == nil {
		return task.InputRequestRecord{}, fmt.Errorf("input request not found: %s", requestID)
	}
	if latest.Status != "pending" {
		return task.InputRequestRecord{}, fmt.Errorf("input request already resolved: %s", requestID)
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.InputRequestRecord{}, err
	}
	record := task.InputRequestRecord{
		SchemaVersion: task.SchemaVersion,
		InputRecordID: task.NewID("INPREC"),
		RequestID:     requestID,
		TaskID:        taskID,
		TS:            task.Now(),
		Kind:          "input_response",
		Status:        "answered",
		Field:         latest.Field,
		Prompt:        latest.Prompt,
		Response:      response,
		Required:      latest.Required,
	}
	if err := s.Store.AppendInputRequest(record); err != nil {
		return task.InputRequestRecord{}, err
	}
	summary := "Recorded operator input response."
	if strings.TrimSpace(latest.Field) != "" {
		summary = fmt.Sprintf("Recorded operator input response for %s.", strings.TrimSpace(latest.Field))
	}
	event := newEvent(taskID, state, "input_responded", summary, []string{artifact.InputRequestRecordRef(record.InputRecordID)})
	if err := s.Store.AppendEvent(event); err != nil {
		return task.InputRequestRecord{}, err
	}
	if state.State == task.StateBlocked && state.StatusReasonCode == "blocked_missing_input" {
		state.State = task.StateActive
		state.StatusReasonCode = ""
		state.StatusDetailRef = ""
	}
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return task.InputRequestRecord{}, err
	}
	if err := s.syncTaskNarrative(spec, state, summary); err != nil {
		return task.InputRequestRecord{}, err
	}
	return record, nil
}

func (s *Service) SetWatch(ctx context.Context, taskID string, interval time.Duration, reason string) (task.Watch, error) {
	_ = ctx
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.Watch{}, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.Watch{}, err
	}
	watches, err := s.Store.ListWatches()
	if err != nil {
		return task.Watch{}, err
	}
	for _, existing := range watches {
		if existing.TaskID != taskID || existing.Status != "active" {
			continue
		}
		existing.Status = "cancelled"
		existing.UpdatedAt = task.Now()
		if err := s.Store.SaveWatch(existing); err != nil {
			return task.Watch{}, err
		}
	}
	if interval <= 0 {
		interval = time.Duration(s.Config.Watch.DefaultIntervalSeconds) * time.Second
	}
	now := time.Now().UTC()
	watch := task.Watch{
		SchemaVersion:   task.SchemaVersion,
		WatchID:         task.NewID("WATCH"),
		TaskID:          taskID,
		Status:          "active",
		IntervalSeconds: int(interval.Seconds()),
		Reason:          reason,
		NextWakeAt:      now.Add(interval).Format(time.RFC3339),
		CreatedAt:       task.Now(),
		UpdatedAt:       task.Now(),
	}
	if err := s.Store.SaveWatch(watch); err != nil {
		return task.Watch{}, err
	}
	event := newEvent(taskID, state, "watch_registered", "Registered watch.", []string{"workspace:.ngen/watches/" + watch.WatchID + ".json"})
	if err := s.Store.AppendEvent(event); err != nil {
		return task.Watch{}, err
	}
	state.State = task.StateWaiting
	state.StatusReasonCode = "waiting_watch"
	state.StatusDetailRef = "workspace:.ngen/watches/" + watch.WatchID + ".json"
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return task.Watch{}, err
	}
	if err := s.syncTaskNarrative(spec, state, "Registered watch and moved task into waiting."); err != nil {
		return task.Watch{}, err
	}
	return watch, nil
}

func (s *Service) ListWatches(ctx context.Context) ([]task.Watch, error) {
	_ = ctx
	return s.Store.ListWatches()
}

func (s *Service) CancelWatch(ctx context.Context, watchID string) (task.Watch, error) {
	_ = ctx
	watch, err := s.Store.LoadWatch(watchID)
	if err != nil {
		return task.Watch{}, err
	}
	spec, err := s.Store.LoadTask(watch.TaskID)
	if err != nil {
		return task.Watch{}, err
	}
	watch.Status = "cancelled"
	watch.UpdatedAt = task.Now()
	if err := s.Store.SaveWatch(watch); err != nil {
		return task.Watch{}, err
	}
	state, err := s.loadStateOrRecover(watch.TaskID)
	if err == nil && state.State == task.StateWaiting {
		state.State = task.StateActive
		state.StatusReasonCode = ""
		state.StatusDetailRef = ""
		event := newEvent(watch.TaskID, state, "state_changed", "Cancelled active watch and returned task to Active.", []string{"workspace:.ngen/watches/" + watch.WatchID + ".json"})
		if appendErr := s.Store.AppendEvent(event); appendErr != nil {
			return task.Watch{}, appendErr
		}
		state.LastEventRef = artifact.EventRef(event.EventID)
		state.UpdatedAt = task.Now()
		if err := s.Store.SaveState(state); err != nil {
			return task.Watch{}, err
		}
		if err := s.syncTaskNarrative(spec, state, "Cancelled active watch."); err != nil {
			return task.Watch{}, err
		}
	}
	return watch, nil
}

func (s *Service) SchedulerTick(ctx context.Context, now time.Time) ([]string, error) {
	lockFile := filepath.Join(s.Store.WorkspaceRoot, s.Config.Scheduler.LeaseFile)
	if err := s.acquireLease(lockFile); err != nil {
		return nil, err
	}
	defer func() { _ = s.releaseLease(lockFile) }()

	watches, err := s.Store.ListWatches()
	if err != nil {
		return nil, err
	}
	var resumed []string
	for _, watch := range watches {
		if watch.Status != "active" {
			continue
		}
		nextWakeAt, err := time.Parse(time.RFC3339, watch.NextWakeAt)
		if err != nil {
			continue
		}
		if nextWakeAt.After(now) {
			continue
		}
		state, err := s.loadStateOrRecover(watch.TaskID)
		if err != nil {
			return resumed, err
		}
		wakeEvent := newEvent(watch.TaskID, state, "watch_woke", "Watch woke task.", []string{"workspace:.ngen/watches/" + watch.WatchID + ".json"})
		if err := s.Store.AppendEvent(wakeEvent); err != nil {
			return resumed, err
		}
		state.State = task.StateActive
		state.StatusReasonCode = ""
		state.StatusDetailRef = ""
		state.LastEventRef = artifact.EventRef(wakeEvent.EventID)
		state.UpdatedAt = task.Now()
		if err := s.Store.SaveState(state); err != nil {
			return resumed, err
		}
		_, _, err = s.execute(ctx, watch.TaskID, nil)
		if err != nil {
			return resumed, err
		}
		resumed = append(resumed, watch.TaskID)
		postState, postErr := s.loadStateOrRecover(watch.TaskID)
		if postErr == nil && postState.State != task.StateWaiting {
			watch.Status = "completed"
		} else {
			watch.NextWakeAt = now.Add(time.Duration(watch.IntervalSeconds) * time.Second).Format(time.RFC3339)
		}
		watch.UpdatedAt = task.Now()
		if err := s.Store.SaveWatch(watch); err != nil {
			return resumed, err
		}
	}
	sort.Strings(resumed)
	return resumed, nil
}

func (s *Service) loadStateOrRecover(taskID string) (task.State, error) {
	state, err := s.Store.LoadState(taskID)
	if err == nil {
		return state, nil
	}
	spec, specErr := s.Store.LoadTask(taskID)
	if specErr != nil {
		return task.State{}, err
	}
	diag := task.Diagnostic{
		SchemaVersion: task.SchemaVersion,
		DiagnosticID:  task.NewID("DIAG"),
		TaskID:        taskID,
		ReasonCode:    "failed_state",
		Summary:       fmt.Sprintf("state recovery failed: %v", err),
		BrokenRefs:    []string{"state.json"},
		EvidenceRefs:  []string{"task.json"},
		CreatedAt:     task.Now(),
		UpdatedAt:     task.Now(),
	}
	if saveErr := s.Store.SaveDiagnostic(diag); saveErr != nil {
		return task.State{}, saveErr
	}
	state = task.State{
		SchemaVersion:     task.SchemaVersion,
		TaskID:            taskID,
		Phase:             task.PhaseExplore,
		State:             task.StateFailed,
		StatusReasonCode:  "failed_state",
		StatusDetailRef:   filepath.ToSlash(filepath.Join("diagnostics", diag.DiagnosticID+".json")),
		CurrentStepID:     "STEP-001",
		PermissionModeID:  task.EffectivePermissionModeID(spec.PermissionModeID),
		LastCheckpointRef: "",
		UpdatedAt:         task.Now(),
	}
	if saveErr := s.Store.SaveState(state); saveErr != nil {
		return task.State{}, saveErr
	}
	failEvent := newEvent(taskID, state, "failed", diag.Summary, []string{state.StatusDetailRef})
	if saveErr := s.Store.AppendEvent(failEvent); saveErr != nil {
		return task.State{}, saveErr
	}
	state.LastEventRef = artifact.EventRef(failEvent.EventID)
	state.UpdatedAt = task.Now()
	if saveErr := s.Store.SaveState(state); saveErr != nil {
		return task.State{}, saveErr
	}
	if saveErr := s.syncTaskNarrative(spec, state, diag.Summary); saveErr != nil {
		return task.State{}, saveErr
	}
	return state, nil
}

func (s *Service) criteriaFromEvidence(spec task.Spec, report task.VerificationReport) task.CriteriaSnapshot {
	snapshot := task.NewInitialCriteria(spec)
	editRefsByPath := s.workspaceEditRefsByPath(spec.TaskID)
	for i, criterion := range spec.SuccessCriteria {
		snapshot.Criteria[i] = s.criterionStatus(spec, criterion, report, editRefsByPath)
	}
	return s.finalizeCriteriaSnapshot(spec, snapshot, report.RanAt, criteriaEvaluationSummary(report))
}

func (s *Service) criteriaWithReviewEvidence(spec task.Spec, snapshot task.CriteriaSnapshot, reviewSummary string) task.CriteriaSnapshot {
	for i := range snapshot.Criteria {
		if snapshot.Criteria[i].Status != "met" {
			continue
		}
		snapshot.Criteria[i].EvidenceRefs = uniqueRefs(append(snapshot.Criteria[i].EvidenceRefs, "reviews/latest.json", "handoff.md"))
	}
	return s.finalizeCriteriaSnapshot(spec, snapshot, task.Now(), strings.TrimSpace(reviewSummary))
}

func (s *Service) finalizeCriteriaSnapshot(spec task.Spec, snapshot task.CriteriaSnapshot, evaluatedAt, evaluationSummary string) task.CriteriaSnapshot {
	if strings.TrimSpace(evaluatedAt) == "" {
		evaluatedAt = task.Now()
	}
	previous, _ := s.Store.LoadCriteria(spec.TaskID)
	previousByID := make(map[string]task.CriterionStatus, len(previous.Criteria))
	for _, item := range previous.Criteria {
		previousByID[strings.TrimSpace(item.CriterionID)] = item
	}
	focus := s.criteriaCurrentFocus(spec, snapshot)
	currentCriterionID := ""
	currentCriterionStatement := ""
	if len(focus) > 0 {
		currentCriterionID = strings.TrimSpace(focus[0].ID)
		currentCriterionStatement = strings.TrimSpace(focus[0].Statement)
	}
	items := make([]task.CriterionStatus, 0, len(spec.SuccessCriteria))
	metCount := 0
	for i, criterion := range spec.SuccessCriteria {
		item := criterionStatusForID(snapshot, criterion.ID)
		item.CriterionID = criterion.ID
		item.Statement = strings.TrimSpace(criterion.Statement)
		item.Ordinal = i + 1
		item.EvidenceRefs = uniqueRefs(item.EvidenceRefs)
		item.Passes = item.Status == "met" && len(item.EvidenceRefs) > 0
		item.Selected = strings.TrimSpace(item.CriterionID) == currentCriterionID
		if item.Passes {
			metCount++
		}
		if strings.TrimSpace(item.LastSummary) == "" {
			item.LastSummary = criteriaItemSummary(item.Passes, item.Selected, evaluationSummary)
		}
		item.LastEvaluatedAt = evaluatedAt
		if prev, ok := previousByID[strings.TrimSpace(item.CriterionID)]; ok && prev.Status == item.Status && prev.Passes == item.Passes {
			item.LastTransitionAt = firstNonEmpty(strings.TrimSpace(prev.LastTransitionAt), evaluatedAt)
		} else {
			item.LastTransitionAt = evaluatedAt
		}
		items = append(items, item)
	}
	snapshot.SchemaVersion = task.SchemaVersion
	snapshot.SnapshotID = task.NewID("CRT")
	snapshot.TaskID = spec.TaskID
	snapshot.UpdatedAt = evaluatedAt
	snapshot.CurrentCriterionID = currentCriterionID
	snapshot.CurrentCriterionStatement = currentCriterionStatement
	snapshot.MetCount = metCount
	snapshot.OpenCount = len(spec.SuccessCriteria) - metCount
	snapshot.Summary = criteriaSnapshotSummary(snapshot)
	snapshot.Criteria = items
	return snapshot
}

func (s *Service) criteriaCurrentFocus(spec task.Spec, snapshot task.CriteriaSnapshot) []task.SuccessCriterion {
	plan, executionSteps := s.executionPlanSummary(spec.TaskID)
	open := openCriteria(spec, &snapshot)
	return focusCriteria(executionSteps, plan.CurrentExecutionStepID, open)
}

func criteriaEvaluationSummary(report task.VerificationReport) string {
	if strings.TrimSpace(report.FailureSummary) != "" {
		return strings.TrimSpace(report.FailureSummary)
	}
	if strings.TrimSpace(report.Status) == "passed" {
		return "Latest verifier pass is clean."
	}
	if strings.TrimSpace(report.Status) != "" {
		return fmt.Sprintf("Latest verification status is %s.", strings.TrimSpace(report.Status))
	}
	return "Acceptance criteria were re-evaluated."
}

func criteriaItemSummary(passes, selected bool, evaluationSummary string) string {
	if passes {
		return "Criterion is passing with durable evidence."
	}
	if selected {
		if strings.TrimSpace(evaluationSummary) != "" {
			return strings.TrimSpace(evaluationSummary)
		}
		return "Current acceptance focus remains open."
	}
	if strings.TrimSpace(evaluationSummary) != "" {
		return strings.TrimSpace(evaluationSummary)
	}
	return "Criterion remains open."
}

func criteriaSnapshotSummary(snapshot task.CriteriaSnapshot) string {
	switch {
	case len(snapshot.Criteria) == 0:
		return "Acceptance ledger has no explicit criteria."
	case snapshot.OpenCount == 0:
		return fmt.Sprintf("All %d acceptance criteria are passing.", snapshot.MetCount)
	case snapshot.CurrentCriterionID != "":
		return fmt.Sprintf("%d/%d acceptance criteria are passing; current focus is %s.", snapshot.MetCount, len(snapshot.Criteria), snapshot.CurrentCriterionID)
	default:
		return fmt.Sprintf("%d/%d acceptance criteria are passing.", snapshot.MetCount, len(snapshot.Criteria))
	}
}

func (s *Service) workspaceEditRefsByPath(taskID string) map[string][]string {
	refsByPath := make(map[string][]string)
	records, err := s.Store.ReadWorkspaceEdits(taskID)
	if err != nil {
		return refsByPath
	}
	for _, record := range records {
		if record.Status != "applied" {
			continue
		}
		ref := artifact.WorkspaceEditRecordRef(record.EditRecordID)
		for _, change := range record.FileChanges {
			refsByPath[change.Path] = uniqueRefs(append(refsByPath[change.Path], ref))
		}
	}
	return refsByPath
}

func (s *Service) recentRepairFailures(taskID string, limit int) []provider.RepairFailure {
	if limit <= 0 {
		return nil
	}
	type failureEntry struct {
		TS      string
		Failure provider.RepairFailure
	}
	var entries []failureEntry
	records, err := s.Store.ReadWorkspaceEdits(taskID)
	if err == nil {
		for _, record := range records {
			if record.Status == "applied" {
				continue
			}
			failure := repairFailureFromRecord(0, record)
			if failure == nil {
				continue
			}
			entries = append(entries, failureEntry{TS: record.TS, Failure: *failure})
		}
	}
	commandRuns, err := s.Store.ReadCommandRuns(taskID)
	if err == nil {
		for _, record := range commandRuns {
			if record.Kind != "repair_command" || record.Status == "completed" {
				continue
			}
			failure := repairFailureFromCommandRunRecord(record, "")
			if failure == nil {
				continue
			}
			entries = append(entries, failureEntry{TS: record.TS, Failure: *failure})
		}
	}
	if len(entries) == 0 {
		return nil
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].TS == entries[j].TS {
			return entries[i].Failure.Summary < entries[j].Failure.Summary
		}
		return entries[i].TS < entries[j].TS
	})
	failures := make([]provider.RepairFailure, 0, len(entries))
	for i, entry := range entries {
		entry.Failure.Attempt = i + 1
		failures = append(failures, entry.Failure)
	}
	if len(failures) <= limit {
		return failures
	}
	return append([]provider.RepairFailure(nil), failures[len(failures)-limit:]...)
}

func repairFailureFromRecord(attempt int, record task.WorkspaceEditRecord) *provider.RepairFailure {
	summary := strings.TrimSpace(record.Summary)
	if summary == "" || record.Status == "applied" {
		return nil
	}
	stage := "workspace_edit_failed"
	if record.Status == "noop" {
		stage = "workspace_edit_noop"
	}
	return &provider.RepairFailure{
		Attempt: attempt,
		Stage:   stage,
		Summary: summary,
	}
}

func appendRepairFailure(existing []provider.RepairFailure, failure *provider.RepairFailure, limit int) []provider.RepairFailure {
	if failure == nil {
		return existing
	}
	out := append(append([]provider.RepairFailure(nil), existing...), *failure)
	if limit > 0 && len(out) > limit {
		out = append([]provider.RepairFailure(nil), out[len(out)-limit:]...)
	}
	return out
}

func (s *Service) criterionStatus(spec task.Spec, criterion task.SuccessCriterion, report task.VerificationReport, editRefsByPath map[string][]string) task.CriterionStatus {
	status := task.CriterionStatus{
		CriterionID: criterion.ID,
		Status:      "open",
	}
	statement := strings.TrimSpace(criterion.Statement)
	if statement == "" {
		return status
	}
	if explicitCommandCriterionMode(statement) {
		if refs := s.explicitCommandEvidenceRefs(spec); len(refs) > 0 {
			status.Status = "met"
			status.EvidenceRefs = refs
		}
		return status
	}
	if genericActionCriterionMode(statement) {
		if refs := s.genericActionEvidenceRefs(spec.TaskID); len(refs) > 0 {
			status.Status = "met"
			status.EvidenceRefs = refs
		}
		return status
	}
	if workerAnalysis := analyzeCriterionWorker(statement); workerAnalysis.Active {
		if refs := s.workerCriterionRefs(spec.TaskID, workerAnalysis); len(refs) > 0 {
			status.Status = "met"
			status.EvidenceRefs = refs
		}
		return status
	}
	if criterionSatisfiedByVerification(spec.WorkspaceRoot, statement, report) {
		status.Status = "met"
		status.EvidenceRefs = []string{"verification/latest.json"}
		return status
	}

	analysis := analyzeCriterionWorkspace(statement)
	if !criterionRequiresWorkspaceEvidence(analysis) {
		if report.Status == "passed" {
			status.Status = "met"
			status.EvidenceRefs = []string{"verification/latest.json"}
		}
		return status
	}

	refs := s.workspaceCriterionRefs(spec.WorkspaceRoot, analysis, statement, editRefsByPath)
	if len(refs) == 0 {
		return status
	}
	if report.Status == "passed" {
		refs = append(refs, "verification/latest.json")
	}
	status.Status = "met"
	status.EvidenceRefs = uniqueRefs(refs)
	return status
}

func (s *Service) explicitCommandEvidenceRefs(spec task.Spec) []string {
	commands := explicitExecutionCommands(spec)
	if len(commands) == 0 {
		return nil
	}
	records, err := s.Store.ReadCommandRuns(spec.TaskID)
	if err != nil {
		return nil
	}
	var refs []string
	for _, command := range commands {
		found := false
		for _, record := range records {
			if record.Kind != "repair_command" || record.Status != "completed" || !equalStringSlices(record.Argv, command) {
				continue
			}
			refs = append(refs, artifact.CommandRunRecordRef(record.CommandRecordID))
			found = true
			break
		}
		if !found {
			continue
		}
	}
	return uniqueRefs(refs)
}

func explicitCommandCriterionMode(statement string) bool {
	lower := strings.ToLower(strings.TrimSpace(statement))
	return strings.Contains(lower, "completed repair command record") &&
		strings.Contains(lower, "explicit user-requested command") &&
		strings.Contains(lower, "result prose alone is not sufficient")
}

func (s *Service) genericActionEvidenceRefs(taskID string) []string {
	var refs []string
	if records, err := s.Store.ReadWorkspaceEdits(taskID); err == nil {
		for _, record := range records {
			if record.Status != "applied" || len(record.FileChanges) == 0 {
				continue
			}
			refs = append(refs, artifact.WorkspaceEditRecordRef(record.EditRecordID))
		}
	}
	if records, err := s.Store.ReadCommandRuns(taskID); err == nil {
		for _, record := range records {
			if record.Kind != "repair_command" || record.Status != "completed" {
				continue
			}
			refs = append(refs, artifact.CommandRunRecordRef(record.CommandRecordID))
		}
	}
	return uniqueRefs(refs)
}

func genericActionCriterionMode(statement string) bool {
	lower := strings.ToLower(strings.TrimSpace(statement))
	return strings.Contains(lower, "concrete execution progress is recorded") &&
		strings.Contains(lower, "durable workspace edit or completed repair command evidence") &&
		strings.Contains(lower, "result prose alone is not sufficient")
}

func multicaCommandMatches(argv, prefix []string) bool {
	if len(argv) < len(prefix) {
		return false
	}
	for i := range prefix {
		if argv[i] != prefix[i] {
			return false
		}
	}
	return true
}

func analyzeCriterionWorker(statement string) criterionWorkerAnalysis {
	lower := strings.ToLower(strings.TrimSpace(statement))
	if lower == "" {
		return criterionWorkerAnalysis{}
	}
	analysis := criterionWorkerAnalysis{
		Roles: make(map[string]struct{}),
	}

	roleAliases := map[string][]string{
		string(task.KindReviewer):       {"reviewer child", "reviewer worker"},
		string(task.KindSecurityReview): {"security_review child", "security review child", "security child", "security worker"},
		string(task.KindCoding):         {"coding child", "coding worker"},
		string(task.KindGeneral):        {"general_execution child", "general child", "docs child", "docs worker"},
	}
	for role, aliases := range roleAliases {
		for _, alias := range aliases {
			if strings.Contains(lower, alias) {
				analysis.Roles[role] = struct{}{}
				break
			}
		}
	}

	if strings.Contains(lower, "child exists") || strings.Contains(lower, "worker exists") || strings.Contains(lower, "spawned child") {
		analysis.RequireContract = true
	}
	if strings.Contains(lower, "compiled result") || strings.Contains(lower, "worker result") || strings.Contains(lower, "child result") {
		analysis.RequireResult = true
	}
	if strings.Contains(lower, "settlement") {
		analysis.RequireSettlement = true
		switch {
		case strings.Contains(lower, "accepted"), strings.Contains(lower, "done"):
			analysis.SettlementStatus = "accepted"
		case strings.Contains(lower, "blocked"):
			analysis.SettlementStatus = "blocked"
		case strings.Contains(lower, "failed"):
			analysis.SettlementStatus = "failed"
		case strings.Contains(lower, "aborted"):
			analysis.SettlementStatus = "aborted"
		case strings.Contains(lower, "waiting"):
			analysis.SettlementStatus = "waiting"
		}
	}
	if strings.Contains(lower, "reconcile") {
		analysis.RequireReconcile = true
		switch {
		case strings.Contains(lower, "applies"), strings.Contains(lower, "apply"), strings.Contains(lower, "applied"):
			analysis.ReconcileStatus = "applied"
		case strings.Contains(lower, "recorded"), strings.Contains(lower, "artifact_only"):
			analysis.ReconcileStatus = "recorded"
		case strings.Contains(lower, "conflict"):
			analysis.ReconcileStatus = "conflict"
		case strings.Contains(lower, "failed"):
			analysis.ReconcileStatus = "failed"
		case strings.Contains(lower, "noop"):
			analysis.ReconcileStatus = "noop"
		case strings.Contains(lower, "shared workspace"), strings.Contains(lower, "shared_workspace"):
			analysis.ReconcileStatus = "shared_workspace"
		}
	}
	switch {
	case strings.Contains(lower, "workspace remains prepared"), strings.Contains(lower, "workspace is prepared"), strings.Contains(lower, "workspace prepared"):
		analysis.RequireWorkspace = true
		analysis.WorkspaceStatus = "prepared"
	case strings.Contains(lower, "workspace is released"), strings.Contains(lower, "workspace released"), strings.Contains(lower, "workspace release"):
		analysis.RequireWorkspace = true
		analysis.WorkspaceStatus = "released"
	}
	switch {
	case strings.Contains(lower, "continue_child"), strings.Contains(lower, "worker continue"), strings.Contains(lower, "parent continuation"):
		analysis.RequireResult = true
		analysis.ParentActionType = "continue_child"
	case strings.Contains(lower, "owned approval pending"), strings.Contains(lower, "approval pending"):
		analysis.RequireResult = true
		analysis.ParentActionType = "owned_approval_pending"
	case strings.Contains(lower, "inspect child"):
		analysis.RequireResult = true
		analysis.ParentActionType = "inspect_child"
	}
	switch {
	case strings.Contains(lower, "review is clear"), strings.Contains(lower, "review clear"), strings.Contains(lower, "review status clear"):
		analysis.RequireResult = true
		analysis.ResultReviewStatus = "clear"
	case strings.Contains(lower, "review blocking"), strings.Contains(lower, "review is blocking"), strings.Contains(lower, "review status blocking"):
		analysis.RequireResult = true
		analysis.ResultReviewStatus = "blocking"
	}
	switch {
	case strings.Contains(lower, "completion accepted"), strings.Contains(lower, "completion is accepted"), strings.Contains(lower, "completion status accepted"):
		analysis.RequireResult = true
		analysis.ResultCompletionStatus = "accepted"
	case strings.Contains(lower, "completion rejected"), strings.Contains(lower, "completion status rejected"):
		analysis.RequireResult = true
		analysis.ResultCompletionStatus = "rejected"
	}
	switch {
	case strings.Contains(lower, "child verification passed"), strings.Contains(lower, "worker verification passed"), strings.Contains(lower, "verification status passed"):
		analysis.RequireResult = true
		analysis.ResultVerificationStatus = "passed"
	case strings.Contains(lower, "child verification failed"), strings.Contains(lower, "worker verification failed"), strings.Contains(lower, "verification status failed"):
		analysis.RequireResult = true
		analysis.ResultVerificationStatus = "failed"
	}
	if !analysis.RequireContract &&
		!analysis.RequireResult &&
		!analysis.RequireSettlement &&
		!analysis.RequireReconcile &&
		!analysis.RequireWorkspace {
		if len(analysis.Roles) == 0 {
			return criterionWorkerAnalysis{}
		}
		analysis.RequireContract = true
	}
	analysis.Active = len(analysis.Roles) > 0 ||
		analysis.RequireContract ||
		analysis.RequireResult ||
		analysis.RequireSettlement ||
		analysis.RequireReconcile ||
		analysis.RequireWorkspace ||
		analysis.ParentActionType != ""
	return analysis
}

func (s *Service) workerCriterionRefs(parentTaskID string, analysis criterionWorkerAnalysis) []string {
	contracts, err := s.Store.ListWorkerContracts(parentTaskID)
	if err != nil || len(contracts) == 0 {
		return nil
	}
	for _, contract := range contracts {
		if len(analysis.Roles) > 0 {
			if _, ok := analysis.Roles[contract.Role]; !ok {
				continue
			}
		}
		if refs := s.workerCriterionRefsForContract(parentTaskID, contract, analysis); len(refs) > 0 {
			return refs
		}
	}
	return nil
}

func (s *Service) workerCriterionRefsForContract(parentTaskID string, contract task.WorkerContract, analysis criterionWorkerAnalysis) []string {
	refs := []string{filepath.ToSlash(filepath.Join("workers", contract.WorkerID+".json"))}
	if analysis.RequireWorkspace {
		if strings.TrimSpace(contract.WorkspaceRef) == "" {
			return nil
		}
		workspace, err := s.Store.LoadWorkerWorkspace(parentTaskID, contract.WorkerID)
		if err != nil {
			return nil
		}
		if analysis.WorkspaceStatus != "" && workspace.Status != analysis.WorkspaceStatus {
			return nil
		}
		refs = append(refs, contract.WorkspaceRef)
	}
	if analysis.RequireSettlement {
		if strings.TrimSpace(contract.SettlementRef) == "" {
			return nil
		}
		settlement, err := s.Store.LoadWorkerSettlement(parentTaskID, contract.WorkerID)
		if err != nil {
			return nil
		}
		if analysis.SettlementStatus != "" && settlement.Status != analysis.SettlementStatus {
			return nil
		}
		refs = append(refs, contract.SettlementRef)
		refs = append(refs, settlement.EvidenceRefs...)
	}
	if analysis.RequireReconcile {
		if strings.TrimSpace(contract.ReconcileRef) == "" {
			return nil
		}
		reconcile, err := s.Store.LoadWorkerReconcile(parentTaskID, contract.WorkerID)
		if err != nil {
			return nil
		}
		if analysis.ReconcileStatus != "" && reconcile.Status != analysis.ReconcileStatus {
			return nil
		}
		refs = append(refs, contract.ReconcileRef)
		if strings.TrimSpace(reconcile.WorkspaceEditRef) != "" {
			refs = append(refs, reconcile.WorkspaceEditRef)
		}
		refs = append(refs, reconcile.EvidenceRefs...)
	}
	if analysis.RequireResult {
		if strings.TrimSpace(contract.ResultRef) == "" {
			return nil
		}
		result, err := s.Store.LoadWorkerResult(parentTaskID, contract.WorkerID)
		if err != nil {
			return nil
		}
		if analysis.ResultReviewStatus != "" && result.ReviewStatus != analysis.ResultReviewStatus {
			return nil
		}
		if analysis.ResultCompletionStatus != "" && result.CompletionStatus != analysis.ResultCompletionStatus {
			return nil
		}
		if analysis.ResultVerificationStatus != "" && result.VerificationStatus != analysis.ResultVerificationStatus {
			return nil
		}
		if analysis.ParentActionType != "" {
			if !result.RequiresParentAction || result.ParentActionType != analysis.ParentActionType {
				return nil
			}
		}
		refs = append(refs, contract.ResultRef)
		refs = append(refs, result.EvidenceRefs...)
		if strings.TrimSpace(result.ReviewRef) != "" {
			refs = append(refs, result.ReviewRef)
		}
		if strings.TrimSpace(result.CompletionRef) != "" {
			refs = append(refs, result.CompletionRef)
		}
		if strings.TrimSpace(result.VerificationRef) != "" {
			refs = append(refs, result.VerificationRef)
		}
		if strings.TrimSpace(result.ApprovalRef) != "" {
			refs = append(refs, result.ApprovalRef)
		}
		if strings.TrimSpace(result.InputRequestRef) != "" {
			refs = append(refs, result.InputRequestRef)
		}
	}
	return uniqueRefs(refs)
}

func analyzeCriterionWorkspace(statement string) criterionWorkspaceAnalysis {
	analysis := criterionWorkspaceAnalysis{
		Paths: make(map[string]struct{}),
		Globs: make(map[string]struct{}),
		Kinds: make(map[criterionWorkspaceKind]struct{}),
	}
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return analysis
	}

	for _, match := range workspaceHintPathPattern.FindAllString(statement, -1) {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(match))))
		if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			continue
		}
		if strings.ContainsAny(clean, "*?[") {
			analysis.Globs[clean] = struct{}{}
		} else {
			analysis.Paths[clean] = struct{}{}
		}
	}

	lower := strings.ToLower(statement)
	if strings.Contains(lower, "readme") {
		analysis.Kinds[criterionKindReadme] = struct{}{}
	}
	for _, keyword := range []string{"docs", "documentation", "guide", "runbook", "manual", "contract", "changelog", "notes"} {
		if strings.Contains(lower, keyword) {
			analysis.Kinds[criterionKindDocs] = struct{}{}
			break
		}
	}
	for _, keyword := range []string{"config", "configuration", "sample", "example", "setting", "settings", "schema", "openapi", "env"} {
		if strings.Contains(lower, keyword) {
			analysis.Kinds[criterionKindConfig] = struct{}{}
			break
		}
	}

	analysis.Tokens = criterionEvidenceTokens(statement, "")
	if len(analysis.Kinds) == 0 && len(analysis.Paths) == 0 && len(analysis.Globs) == 0 && len(analysis.Tokens) > 0 {
		analysis.Kinds[criterionKindSource] = struct{}{}
	}
	return analysis
}

func criterionRequiresWorkspaceEvidence(analysis criterionWorkspaceAnalysis) bool {
	return len(analysis.Paths) > 0 || len(analysis.Globs) > 0 || len(analysis.Kinds) > 0 || len(analysis.Tokens) > 0
}

func criterionAllowsRawWorkspaceEvidence(analysis criterionWorkspaceAnalysis) bool {
	if len(analysis.Paths) > 0 || len(analysis.Globs) > 0 {
		return true
	}
	for _, kind := range []criterionWorkspaceKind{criterionKindReadme, criterionKindDocs, criterionKindConfig} {
		if _, ok := analysis.Kinds[kind]; ok {
			return true
		}
	}
	return false
}

func criterionKindsForPath(rel string) map[criterionWorkspaceKind]struct{} {
	kinds := make(map[criterionWorkspaceKind]struct{})
	for _, kind := range []criterionWorkspaceKind{criterionKindReadme, criterionKindDocs, criterionKindConfig, criterionKindSource} {
		if criterionPathMatchesKind(rel, kind) {
			kinds[kind] = struct{}{}
		}
	}
	return kinds
}

func criterionPathMatchesKind(rel string, kind criterionWorkspaceKind) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	base := strings.ToLower(filepath.Base(rel))
	switch kind {
	case criterionKindReadme:
		return base == "readme.md"
	case criterionKindDocs:
		return strings.HasSuffix(strings.ToLower(rel), ".md") || base == "readme.md"
	case criterionKindConfig:
		lower := strings.ToLower(rel)
		switch filepath.Ext(lower) {
		case ".json", ".yaml", ".yml", ".toml":
			return true
		}
		if strings.Contains(lower, "config") || strings.Contains(lower, "sample") || strings.Contains(lower, "example") || strings.HasPrefix(base, ".env") {
			return true
		}
		return false
	case criterionKindSource:
		return strings.HasSuffix(strings.ToLower(rel), ".go") && !strings.HasSuffix(strings.ToLower(rel), "_test.go")
	default:
		return false
	}
}

func (s *Service) workspaceCriterionRefs(
	workspaceRoot string,
	analysis criterionWorkspaceAnalysis,
	statement string,
	editRefsByPath map[string][]string,
) []string {
	candidates := s.collectCriterionCandidatePaths(workspaceRoot, analysis)
	if len(candidates) == 0 {
		return nil
	}
	anyEdits := len(editRefsByPath) > 0
	var editRefs []string
	var rawRefs []string
	for _, rel := range candidates {
		content, ref, ok := s.workspaceCriterionContentEvidence(workspaceRoot, rel)
		if !ok || !criterionAnalysisMatchesContent(analysis, statement, rel, content) {
			continue
		}
		if refs := editRefsByPath[rel]; len(refs) > 0 {
			editRefs = append(editRefs, refs...)
			continue
		}
		rawRefs = append(rawRefs, ref)
	}
	if len(editRefs) > 0 {
		return uniqueRefs(append(editRefs, rawRefs...))
	}
	if !criterionAllowsRawWorkspaceEvidence(analysis) && anyEdits {
		return nil
	}
	return uniqueRefs(rawRefs)
}

func (s *Service) collectCriterionCandidatePaths(workspaceRoot string, analysis criterionWorkspaceAnalysis) []string {
	type candidate struct {
		Path     string
		Priority int
	}
	seen := make(map[string]int)
	addCandidate := func(rel string, priority int) {
		if rel == "" || s.isDeniedWorkspacePath(rel) {
			return
		}
		if current, ok := seen[rel]; ok && current <= priority {
			return
		}
		seen[rel] = priority
	}
	for rel := range analysis.Paths {
		addCandidate(rel, 0)
	}
	_ = filepath.WalkDir(workspaceRoot, func(pathname string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if pathname == workspaceRoot {
			return nil
		}
		rel, err := filepath.Rel(workspaceRoot, pathname)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if s.isDeniedWorkspacePath(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		priority, ok := criterionCandidatePriority(rel, analysis)
		if !ok {
			return nil
		}
		addCandidate(rel, priority)
		return nil
	})
	candidates := make([]candidate, 0, len(seen))
	for rel, priority := range seen {
		candidates = append(candidates, candidate{Path: rel, Priority: priority})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].Path < candidates[j].Path
	})
	out := make([]string, 0, len(candidates))
	for _, item := range candidates {
		out = append(out, item.Path)
	}
	return out
}

func criterionCandidatePriority(rel string, analysis criterionWorkspaceAnalysis) (int, bool) {
	if _, ok := analysis.Paths[rel]; ok {
		return 0, true
	}
	for pattern := range analysis.Globs {
		if matched, _ := path.Match(pattern, rel); matched {
			return 5, true
		}
	}
	priority := 0
	matched := false
	for _, kind := range []criterionWorkspaceKind{criterionKindReadme, criterionKindDocs, criterionKindConfig, criterionKindSource} {
		if _, ok := analysis.Kinds[kind]; !ok || !criterionPathMatchesKind(rel, kind) {
			continue
		}
		matched = true
		switch kind {
		case criterionKindReadme:
			priority += 10
		case criterionKindDocs:
			if strings.HasPrefix(rel, "docs/") {
				priority += 12
			} else {
				priority += 18
			}
		case criterionKindConfig:
			priority += 14
		case criterionKindSource:
			priority += 20
		}
	}
	return priority, matched
}

func criterionAnalysisMatchesContent(analysis criterionWorkspaceAnalysis, statement, rel, content string) bool {
	if len(analysis.Tokens) == 0 {
		if _, ok := analysis.Paths[rel]; ok {
			return true
		}
		for pattern := range analysis.Globs {
			if matched, _ := path.Match(pattern, rel); matched {
				return true
			}
		}
		for kind := range analysis.Kinds {
			if criterionPathMatchesKind(rel, kind) {
				return true
			}
		}
		return false
	}
	return criterionStatementMatchesContent(statement, rel, content)
}

func criterionSatisfiedByVerification(workspaceRoot, statement string, report task.VerificationReport) bool {
	if report.Status != "passed" {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(statement))
	if lower == "" {
		return false
	}
	if commands := verify.VerifierCommandsFromStatement(workspaceRoot, statement); len(commands) > 0 {
		return verificationReportIncludesCommands(report, commands)
	}

	switch {
	case strings.Contains(lower, "go test"), strings.Contains(lower, "tests pass"), strings.Contains(lower, "test passes"), strings.Contains(lower, "test pass"):
		return verificationReportHasMatchingCommand(report, verificationCommandLooksLikeTest)
	case strings.Contains(lower, "build passes"), strings.Contains(lower, "build pass"):
		return verificationReportHasMatchingCommand(report, verificationCommandLooksLikeBuild)
	case strings.Contains(lower, "lint passes"), strings.Contains(lower, "lint pass"):
		return verificationReportHasMatchingCommand(report, verificationCommandLooksLikeLint)
	case strings.Contains(lower, "verification passes"), strings.Contains(lower, "verification pass"):
		return len(report.Checks) > 0
	}
	return false
}

func verificationReportIncludesCommands(report task.VerificationReport, expected [][]string) bool {
	for _, command := range expected {
		if !verificationReportHasExactCommand(report, command) {
			return false
		}
	}
	return true
}

func verificationReportHasExactCommand(report task.VerificationReport, expected []string) bool {
	for _, check := range report.Checks {
		if sameCommandSlice(check.Command, expected) {
			return true
		}
	}
	return false
}

func verificationReportHasMatchingCommand(report task.VerificationReport, match func([]string) bool) bool {
	for _, check := range report.Checks {
		if match(check.Command) {
			return true
		}
	}
	return false
}

func verificationCommandLooksLikeTest(command []string) bool {
	return verificationCommandContainsToken(command, "test") || (len(command) > 0 && command[0] == "pytest")
}

func verificationCommandLooksLikeBuild(command []string) bool {
	return verificationCommandContainsToken(command, "build")
}

func verificationCommandLooksLikeLint(command []string) bool {
	return verificationCommandContainsToken(command, "lint") || verificationCommandContainsToken(command, "fmt") || verificationCommandContainsToken(command, "clippy")
}

func verificationCommandContainsToken(command []string, token string) bool {
	for _, part := range command {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func sameCommandSlice(left, right []string) bool {
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

func looksLikeVerificationCommandToken(token string) bool {
	if token == "" || filepath.IsAbs(token) {
		return false
	}
	if strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") {
		return true
	}
	switch token {
	case "go", "bash", "sh", "make", "just", "npm", "pnpm", "yarn", "bun", "pytest", "cargo", "python", "python3":
		return true
	default:
		return false
	}
}

func criterionWorkspacePaths(statement string) []string {
	var paths []string
	for _, match := range workspaceHintPathPattern.FindAllString(statement, -1) {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(match))))
		if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsAny(clean, "*?[") {
			continue
		}
		paths = append(paths, clean)
	}
	return uniqueNonEmptyStrings(paths)
}

func (s *Service) workspaceCriterionContentEvidence(workspaceRoot, rel string) (string, string, bool) {
	if rel == "" || s.isDeniedWorkspacePath(rel) {
		return "", "", false
	}
	full := filepath.Join(workspaceRoot, filepath.FromSlash(rel))
	data, err := os.ReadFile(full)
	if err != nil || !utf8.Valid(data) || strings.ContainsRune(string(data), '\x00') {
		return "", "", false
	}
	return string(data), "workspace:" + rel, true
}

func criterionStatementMatchesContent(statement, rel, content string) bool {
	tokens := criterionEvidenceTokens(statement, rel)
	if len(tokens) == 0 {
		analysis := analyzeCriterionWorkspace(statement)
		if _, ok := analysis.Paths[rel]; ok {
			return true
		}
		for pattern := range analysis.Globs {
			if matched, _ := path.Match(pattern, rel); matched {
				return true
			}
		}
		for kind := range analysis.Kinds {
			if criterionPathMatchesKind(rel, kind) {
				return true
			}
		}
		return false
	}
	lowerContent := strings.ToLower(content)
	for _, token := range tokens {
		if strings.Contains(lowerContent, strings.ToLower(token)) {
			return true
		}
	}
	return false
}

func criterionEvidenceTokens(statement, rel string) []string {
	var tokens []string
	for _, match := range criterionLiteralPattern.FindAllStringSubmatch(statement, -1) {
		if len(match) >= 2 {
			tokens = append(tokens, strings.TrimSpace(match[1]))
		}
	}
	for _, match := range criterionCodeTokenPattern.FindAllString(statement, -1) {
		token := strings.TrimSpace(match)
		if token == "" {
			continue
		}
		hasUpperBeyondFirst := false
		for idx, r := range token {
			if idx > 0 && unicode.IsUpper(r) {
				hasUpperBeyondFirst = true
				break
			}
		}
		if strings.HasPrefix(token, "--") ||
			strings.ContainsAny(token, "_-") ||
			strings.ContainsAny(token, "0123456789") ||
			strings.ToUpper(token) == token ||
			hasUpperBeyondFirst {
			tokens = append(tokens, token)
		}
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel)))
	filtered := make([]string, 0, len(tokens))
	for _, token := range uniqueNonEmptyStrings(tokens) {
		lower := strings.ToLower(strings.TrimSpace(token))
		if lower == "" || lower == base {
			continue
		}
		filtered = append(filtered, token)
	}
	return filtered
}

func criteriaAllMet(snapshot task.CriteriaSnapshot) bool {
	for _, item := range snapshot.Criteria {
		if item.Status != "met" || len(item.EvidenceRefs) == 0 {
			return false
		}
	}
	return true
}

func criteriaFingerprint(spec task.Spec, snapshot task.CriteriaSnapshot) string {
	var parts []string
	for _, criterion := range spec.SuccessCriteria {
		status := criterionStatusForID(snapshot, criterion.ID)
		if status.Status == "met" && len(status.EvidenceRefs) > 0 {
			continue
		}
		parts = append(parts, criterion.ID+":"+strings.TrimSpace(criterion.Statement))
	}
	return strings.Join(parts, "|")
}

func criteriaRepairSignal(spec task.Spec, snapshot task.CriteriaSnapshot) task.VerificationReport {
	var unmet []string
	for _, criterion := range spec.SuccessCriteria {
		status := criterionStatusForID(snapshot, criterion.ID)
		if status.Status == "met" && len(status.EvidenceRefs) > 0 {
			continue
		}
		unmet = append(unmet, fmt.Sprintf("%s: %s", criterion.ID, strings.TrimSpace(criterion.Statement)))
	}
	summary := "Open success criteria remain."
	if len(unmet) > 0 {
		summary = "Open success criteria remain: " + strings.Join(unmet, "; ")
	}
	return task.VerificationReport{
		SchemaVersion:  task.SchemaVersion,
		TaskID:         spec.TaskID,
		ReportID:       task.NewID("VER"),
		Status:         "failed",
		Profile:        string(spec.Kind),
		RanAt:          task.Now(),
		FailureSummary: summary,
		Checks: []task.VerificationCheck{
			{
				Name:    "criteria_gap",
				Status:  "failed",
				Summary: summary,
			},
		},
	}
}

func openCriteria(spec task.Spec, snapshot *task.CriteriaSnapshot) []task.SuccessCriterion {
	if snapshot == nil {
		return nil
	}
	var out []task.SuccessCriterion
	for _, criterion := range spec.SuccessCriteria {
		status := criterionStatusForID(*snapshot, criterion.ID)
		if status.Status == "met" && len(status.EvidenceRefs) > 0 {
			continue
		}
		out = append(out, criterion)
	}
	return out
}

func criterionStatusForID(snapshot task.CriteriaSnapshot, criterionID string) task.CriterionStatus {
	for _, item := range snapshot.Criteria {
		if item.CriterionID == criterionID {
			return item
		}
	}
	return task.CriterionStatus{CriterionID: criterionID, Status: "open"}
}

func (s *Service) syncTaskNarrative(spec task.Spec, state task.State, summary string) error {
	var err error
	state, err = s.refreshTaskPlan(spec, state)
	if err != nil {
		return err
	}
	refs := s.collectNarrativeRefs(spec.TaskID, state)
	missionRefs, err := s.missionRefsForTask(spec.TaskID)
	if err != nil {
		return err
	}
	refs = append(refs, missionRefs...)
	nextStep := s.deriveNextStep(spec, state)
	sprint := s.buildSprintSnapshot(spec, state)
	refs = uniqueRefs(append(refs, "sprint/latest.json"))
	progress := s.renderProgress(spec, state, summary, refs, nextStep, sprint)
	if err := s.Store.SaveProgress(spec.TaskID, []byte(progress)); err != nil {
		return err
	}
	compacted := s.renderContextCompaction(spec, state, summary, refs, nextStep, sprint)
	if err := s.Store.SaveContextCompactionSummary(spec.TaskID, []byte(compacted)); err != nil {
		return err
	}
	builtAt := task.Now()
	contextPack := s.buildContextPack(spec, state, builtAt, summary, nextStep, refs, sprint.ProjectFocus)
	if err := s.Store.SaveContextSummary(contextPack); err != nil {
		return err
	}
	if err := s.Store.SaveContinuity(s.buildContinuitySnapshot(spec, state, contextPack, summary, nextStep, refs, sprint.ProjectFocus)); err != nil {
		return err
	}
	if err := s.Store.SaveSprint(sprint); err != nil {
		return err
	}
	return s.maybePromoteTaskMemory(spec, state, summary)
}

func (s *Service) refreshTaskPlan(spec task.Spec, state task.State) (task.State, error) {
	previousStepID := state.CurrentStepID
	plan, state := s.currentTaskPlan(spec, state)
	if err := s.Store.SavePlan(plan); err != nil {
		return state, err
	}
	if state.CurrentStepID != previousStepID {
		if err := s.Store.SaveState(state); err != nil {
			return state, err
		}
	}
	return state, nil
}

func (s *Service) currentTaskPlan(spec task.Spec, state task.State) (task.Plan, task.State) {
	plan, currentStepID := s.deriveTaskPlan(spec, state)
	if currentStepID != "" && state.CurrentStepID != currentStepID {
		state.CurrentStepID = currentStepID
		state.UpdatedAt = task.Now()
	}
	return plan, state
}

func (s *Service) deriveTaskPlan(spec task.Spec, state task.State) (task.Plan, string) {
	systemPlan, currentSystemStepID := s.deriveSystemPlan(spec)
	if len(systemPlan.Steps) == 0 {
		return systemPlan, ""
	}
	plan := systemPlan
	now := plan.UpdatedAt
	existingPlan, executionSteps := s.loadExecutionPlan(spec.TaskID, now)
	if state.State == task.StateDone {
		executionSteps = cancelOpenExecutionSteps(executionSteps, now)
	}
	currentExecutionStepID, readyExecutionStepIDs, blockedExecutionStepIDs := executionPlanState(executionSteps)
	plan.Revision = existingPlan.Revision
	plan.Explanation = strings.TrimSpace(existingPlan.Explanation)
	plan.CurrentSystemStepID = currentSystemStepID
	plan.CurrentExecutionStepID = currentExecutionStepID
	plan.ReadyExecutionStepIDs = readyExecutionStepIDs
	plan.BlockedExecutionStepIDs = blockedExecutionStepIDs
	plan.LastMutationRef = strings.TrimSpace(existingPlan.LastMutationRef)
	plan.Steps = mergePlanSteps(systemPlan.Steps, executionSteps)
	currentStepID := currentSystemStepID
	if state.State != task.StateDone && plan.CurrentExecutionStepID != "" {
		currentStepID = plan.CurrentExecutionStepID
	}
	if currentStepID == "" {
		currentStepID = plan.CurrentExecutionStepID
	}
	return plan, currentStepID
}

func (s *Service) deriveSystemPlan(spec task.Spec) (task.Plan, string) {
	plan := task.NewBootstrapPlan(spec)
	plan.UpdatedAt = task.Now()
	if len(plan.Steps) == 0 {
		return plan, ""
	}
	baselineDone := s.Store.HasBaseline(spec.TaskID)
	criteria, err := s.Store.LoadCriteria(spec.TaskID)
	if err != nil {
		criteria = task.NewInitialCriteria(spec)
	}
	completion, err := s.Store.LoadCompletion(spec.TaskID)
	if err != nil {
		completion = task.CompletionReport{}
	}
	currentStepID := ""

	if baselineDone {
		plan.Steps[0].Status = task.StepStatusCompleted
	} else {
		plan.Steps[0].Status = task.StepStatusInProgress
		currentStepID = plan.Steps[0].ID
	}
	plan.Steps[0].UpdatedAt = plan.UpdatedAt

	for i, criterion := range spec.SuccessCriteria {
		stepIndex := i + 1
		if stepIndex >= len(plan.Steps)-1 {
			break
		}
		plan.Steps[stepIndex].Verifier = planVerifierHints(spec, criterion)
		criterionStatus := criterionStatusForID(criteria, criterion.ID)
		switch {
		case !baselineDone:
			plan.Steps[stepIndex].Status = task.StepStatusPending
		case criterionStatus.Status == "met":
			plan.Steps[stepIndex].Status = task.StepStatusCompleted
			plan.Steps[stepIndex].EvidenceRefs = uniqueRefs(criterionStatus.EvidenceRefs)
		case currentStepID == "":
			plan.Steps[stepIndex].Status = task.StepStatusInProgress
			currentStepID = plan.Steps[stepIndex].ID
		default:
			plan.Steps[stepIndex].Status = task.StepStatusPending
		}
		plan.Steps[stepIndex].UpdatedAt = plan.UpdatedAt
	}

	finalIndex := len(plan.Steps) - 1
	switch {
	case completion.Status == "accepted":
		plan.Steps[finalIndex].Status = task.StepStatusCompleted
		currentStepID = plan.Steps[finalIndex].ID
	case !baselineDone:
		plan.Steps[finalIndex].Status = task.StepStatusPending
	case currentStepID == "":
		plan.Steps[finalIndex].Status = task.StepStatusInProgress
		currentStepID = plan.Steps[finalIndex].ID
	default:
		plan.Steps[finalIndex].Status = task.StepStatusPending
	}
	plan.Steps[finalIndex].UpdatedAt = plan.UpdatedAt

	return plan, currentStepID
}

func (s *Service) loadExecutionPlan(taskID, fallbackUpdatedAt string) (task.Plan, []task.Step) {
	plan, err := s.Store.LoadPlan(taskID)
	if err != nil {
		return task.Plan{}, nil
	}
	return plan, normalizeExecutionPlanSteps(plan.Steps, fallbackUpdatedAt)
}

func normalizeExecutionPlanSteps(steps []task.Step, fallbackUpdatedAt string) []task.Step {
	execution := make([]task.Step, 0, len(steps))
	for _, step := range steps {
		if !task.IsExecutionStep(step) {
			continue
		}
		normalized := task.NormalizePlanStep(step, fallbackUpdatedAt)
		if strings.TrimSpace(normalized.Title) == "" {
			continue
		}
		execution = append(execution, normalized)
	}
	seenIDs := make(map[string]struct{}, len(execution))
	for i := range execution {
		if strings.TrimSpace(execution[i].ID) == "" {
			execution[i].ID = task.ExecutionPlanStepID(i)
		}
		if _, ok := seenIDs[execution[i].ID]; ok {
			execution[i].ID = task.ExecutionPlanStepID(i)
		}
		seenIDs[execution[i].ID] = struct{}{}
		if strings.TrimSpace(execution[i].Kind) == "" {
			execution[i].Kind = task.StepKindExecution
		}
	}
	return execution
}

func cancelOpenExecutionSteps(steps []task.Step, updatedAt string) []task.Step {
	if len(steps) == 0 {
		return nil
	}
	out := append([]task.Step(nil), steps...)
	for i := range out {
		switch out[i].Status {
		case task.StepStatusCompleted, task.StepStatusCancelled:
		default:
			out[i].Status = task.StepStatusCancelled
			out[i].UpdatedAt = updatedAt
		}
	}
	return out
}

func executionPlanState(steps []task.Step) (string, []string, []string) {
	if len(steps) == 0 {
		return "", nil, nil
	}
	type candidate struct {
		index int
		step  task.Step
	}
	byID := make(map[string]task.Step, len(steps))
	for _, step := range steps {
		byID[step.ID] = step
	}
	ready := make([]candidate, 0, len(steps))
	blocked := make([]string, 0, len(steps))
	current := ""
	for idx, step := range steps {
		switch step.Status {
		case task.StepStatusCompleted, task.StepStatusCancelled:
			continue
		}
		if step.Status == task.StepStatusInProgress && current == "" {
			current = step.ID
		}
		if executionStepReady(step, byID) {
			ready = append(ready, candidate{index: idx, step: step})
			continue
		}
		blocked = append(blocked, step.ID)
	}
	sort.SliceStable(ready, func(i, j int) bool {
		left := ready[i].step
		right := ready[j].step
		if left.Status != right.Status {
			return executionStatusRank(left.Status) < executionStatusRank(right.Status)
		}
		if left.Priority != right.Priority {
			return executionPriorityRank(left.Priority) < executionPriorityRank(right.Priority)
		}
		return ready[i].index < ready[j].index
	})
	readyIDs := make([]string, 0, len(ready))
	for _, item := range ready {
		readyIDs = append(readyIDs, item.step.ID)
	}
	if current == "" && len(readyIDs) > 0 {
		current = readyIDs[0]
	}
	return current, readyIDs, blocked
}

func executionStepReady(step task.Step, byID map[string]task.Step) bool {
	for _, dep := range step.DependsOn {
		dependency, ok := byID[dep]
		if !ok || dependency.Status != task.StepStatusCompleted {
			return false
		}
	}
	return true
}

func executionStatusRank(status string) int {
	switch status {
	case task.StepStatusInProgress:
		return 0
	case task.StepStatusPending:
		return 1
	default:
		return 2
	}
}

func executionPriorityRank(priority string) int {
	switch strings.TrimSpace(strings.ToLower(priority)) {
	case task.StepPriorityHigh:
		return 0
	case task.StepPriorityLow:
		return 2
	default:
		return 1
	}
}

func mergePlanSteps(systemSteps, executionSteps []task.Step) []task.Step {
	if len(systemSteps) == 0 {
		return executionSteps
	}
	if len(executionSteps) == 0 {
		return systemSteps
	}
	merged := make([]task.Step, 0, len(systemSteps)+len(executionSteps))
	merged = append(merged, systemSteps[0])
	merged = append(merged, executionSteps...)
	if len(systemSteps) > 1 {
		merged = append(merged, systemSteps[1:]...)
	}
	return merged
}

func planVerifierHints(spec task.Spec, criterion task.SuccessCriterion) []string {
	if commands := verify.VerifierCommandsFromStatement(spec.WorkspaceRoot, criterion.Statement); len(commands) > 0 {
		var hints []string
		for _, command := range commands {
			hints = append(hints, strings.Join(command, " "))
		}
		return uniqueNonEmptyStrings(hints)
	}
	if analysis := analyzeCriterionWorker(criterion.Statement); analysis.Active {
		hints := []string{"worker_runtime"}
		if analysis.RequireResult {
			hints = append(hints, "worker_result")
		}
		if analysis.RequireSettlement {
			hints = append(hints, "worker_settlement")
		}
		if analysis.RequireReconcile {
			hints = append(hints, "worker_reconcile")
		}
		if analysis.RequireWorkspace {
			hints = append(hints, "worker_workspace")
		}
		return uniqueNonEmptyStrings(hints)
	}
	if analysis := analyzeCriterionWorkspace(criterion.Statement); criterionRequiresWorkspaceEvidence(analysis) {
		return []string{"workspace_evidence", "profile_default"}
	}
	return []string{"profile_default"}
}

func (s *Service) currentStepLabel(taskID, stepID string) string {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return ""
	}
	plan, err := s.Store.LoadPlan(taskID)
	if err != nil {
		return stepID
	}
	for _, step := range plan.Steps {
		if step.ID != stepID {
			continue
		}
		title := strings.TrimSpace(step.Title)
		if title == "" {
			return stepID
		}
		return fmt.Sprintf("%s (%s)", stepID, title)
	}
	return stepID
}

func (s *Service) executionPlanSummary(taskID string) (task.Plan, []task.Step) {
	plan, err := s.Store.LoadPlan(taskID)
	if err != nil {
		return task.Plan{}, nil
	}
	execution := normalizeExecutionPlanSteps(plan.Steps, plan.UpdatedAt)
	return plan, execution
}

func stepStatusMarker(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case task.StepStatusCompleted:
		return "[x]"
	case task.StepStatusInProgress:
		return "[>]"
	case task.StepStatusCancelled:
		return "[-]"
	default:
		return "[ ]"
	}
}

func (s *Service) executionStepLabels(taskID string, stepIDs []string) []string {
	labels := make([]string, 0, len(stepIDs))
	for _, stepID := range stepIDs {
		label := s.currentStepLabel(taskID, stepID)
		if label == "" {
			continue
		}
		labels = append(labels, label)
	}
	return labels
}

func (s *Service) renderExecutionPlanMarkdown(b *strings.Builder, steps []task.Step) {
	if len(steps) == 0 {
		return
	}
	children := make(map[string][]task.Step, len(steps))
	roots := make([]task.Step, 0, len(steps))
	for _, step := range steps {
		parentID := strings.TrimSpace(step.ParentStepID)
		if parentID == "" {
			roots = append(roots, step)
			continue
		}
		children[parentID] = append(children[parentID], step)
	}
	var render func(task.Step, int)
	render = func(step task.Step, depth int) {
		indent := strings.Repeat("  ", depth)
		title := strings.TrimSpace(step.Title)
		if title == "" {
			title = step.ID
		}
		fmt.Fprintf(b, "%s- %s %s %s\n", indent, stepStatusMarker(step.Status), step.ID, title)
		metaIndent := indent + "  "
		if step.Priority != "" {
			fmt.Fprintf(b, "%s- priority: %s\n", metaIndent, step.Priority)
		}
		if len(step.DependsOn) > 0 {
			fmt.Fprintf(b, "%s- depends_on: %s\n", metaIndent, strings.Join(step.DependsOn, ", "))
		}
		if len(step.Covers) > 0 {
			fmt.Fprintf(b, "%s- covers: %s\n", metaIndent, strings.Join(step.Covers, ", "))
		}
		if step.Notes != "" {
			fmt.Fprintf(b, "%s- notes: %s\n", metaIndent, step.Notes)
		}
		for _, child := range children[step.ID] {
			render(child, depth+1)
		}
	}
	for _, root := range roots {
		render(root, 0)
	}
}

func (s *Service) collectNarrativeRefs(taskID string, state task.State) []string {
	refs := []string{"task.json", "plan.json", "state.json", "criteria/latest.json"}
	if history, err := s.Store.ReadCriteria(taskID); err == nil && len(history) > 0 {
		refs = append(refs, "criteria/history.jsonl")
	}
	if plan, err := s.Store.LoadPlan(taskID); err == nil && plan.LastMutationRef != "" {
		refs = append(refs, plan.LastMutationRef)
	}
	if s.Store.HasBaseline(taskID) {
		refs = append(refs, "baseline.json")
	}
	if state.LastVerificationRef != "" {
		refs = append(refs, state.LastVerificationRef)
	}
	if state.LastReviewRef != "" {
		refs = append(refs, state.LastReviewRef)
	}
	if state.LastCompletionRef != "" {
		refs = append(refs, state.LastCompletionRef)
	}
	if state.LastCheckpointRef != "" {
		refs = append(refs, state.LastCheckpointRef)
	}
	if s.Store.HandoffExists(taskID) {
		refs = append(refs, "handoff.md")
	}
	if s.Store.ContinuityExists(taskID) {
		refs = append(refs, "continuity/latest.json")
	}
	if s.Store.SprintExists(taskID) {
		refs = append(refs, "sprint/latest.json")
	}
	if state.StatusDetailRef != "" {
		refs = append(refs, state.StatusDetailRef)
	}
	if edits, err := s.Store.ReadWorkspaceEdits(taskID); err == nil && len(edits) > 0 {
		refs = append(refs, artifact.WorkspaceEditRecordRef(edits[len(edits)-1].EditRecordID))
	}
	if commands, err := s.Store.ReadCommandRuns(taskID); err == nil && len(commands) > 0 {
		refs = append(refs, artifact.CommandRunRecordRef(commands[len(commands)-1].CommandRecordID))
	}
	if projectFocus := s.projectTaskContext(taskID); projectFocus != nil {
		refs = append(refs, projectFocus.Refs...)
	}
	return uniqueRefs(refs)
}

func uniqueRefs(refs []string) []string {
	seen := make(map[string]struct{}, len(refs))
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

func (s *Service) loadContextPack(taskID string) *task.ContextSummary {
	pack, err := s.Store.LoadContextSummary(taskID)
	if err != nil {
		return nil
	}
	return &pack
}

func (s *Service) loadBaseline(taskID string) *task.Baseline {
	baseline, err := s.Store.LoadBaseline(taskID)
	if err != nil {
		return nil
	}
	return &baseline
}

func (s *Service) loadContinuity(taskID string) *task.ContinuitySnapshot {
	snapshot, err := s.Store.LoadContinuity(taskID)
	if err != nil {
		return nil
	}
	return &snapshot
}

func (s *Service) loadSprint(taskID string) *task.SprintSnapshot {
	snapshot, err := s.Store.LoadSprint(taskID)
	if err != nil {
		return nil
	}
	return &snapshot
}

func (s *Service) deriveNextStep(spec task.Spec, state task.State) string {
	plan, _ := s.executionPlanSummary(spec.TaskID)
	currentExecutionLabel := s.currentStepLabel(spec.TaskID, plan.CurrentExecutionStepID)
	switch state.State {
	case task.StateDone:
		return "Review the handoff and close the task, or create a follow-up task if more work remains."
	case task.StateWaiting:
		return "Wait for the active watch to wake and trigger `ngen scheduler tick --once`, or cancel the watch if conditions changed."
	case task.StateBlocked:
		switch state.StatusReasonCode {
		case "blocked_missing_input":
			return fmt.Sprintf("Provide the requested input, then rerun `ngen resume %s` if more execution is needed.", spec.TaskID)
		case "blocked_policy":
			return fmt.Sprintf("Resolve the pending approval for %s, then rerun `ngen resume %s` if more execution is needed.", spec.TaskID, spec.TaskID)
		case "blocked_review":
			return fmt.Sprintf("Address the review/completion blocker, then rerun `ngen review %s` or `ngen resume %s`.", spec.TaskID, spec.TaskID)
		default:
			return fmt.Sprintf("Resolve the current blocker, then rerun `ngen resume %s`.", spec.TaskID)
		}
	case task.StateFailed:
		switch state.StatusReasonCode {
		case "failed_state":
			return "Inspect the diagnostic artifact, repair the durable state if possible, and recreate the task if the state cannot be trusted."
		default:
			return fmt.Sprintf("Fix the verification failure, then rerun `ngen resume %s`.", spec.TaskID)
		}
	default:
		if !s.Store.HasBaseline(spec.TaskID) {
			return fmt.Sprintf("Run `ngen run %s` to capture baseline and execute the current verifier path.", spec.TaskID)
		}
		if currentExecutionLabel != "" {
			return fmt.Sprintf("Continue execution step %s with `ngen resume %s`.", currentExecutionLabel, spec.TaskID)
		}
		return fmt.Sprintf("Continue execution with `ngen resume %s`.", spec.TaskID)
	}
}

func (s *Service) pendingInputRequest(taskID string) (task.InputRequestRecord, bool, error) {
	records, err := s.Store.ReadInputRequests(taskID)
	if err != nil {
		return task.InputRequestRecord{}, false, err
	}
	latest := make(map[string]task.InputRequestRecord)
	order := make([]string, 0, len(records))
	for _, record := range records {
		if _, ok := latest[record.RequestID]; !ok {
			order = append(order, record.RequestID)
		}
		latest[record.RequestID] = record
	}
	for i := len(order) - 1; i >= 0; i-- {
		record := latest[order[i]]
		if record.Status == "pending" {
			return record, true, nil
		}
	}
	return task.InputRequestRecord{}, false, nil
}

func (s *Service) renderProgress(spec task.Spec, state task.State, summary string, refs []string, nextStep string, sprint task.SprintSnapshot) string {
	plan, executionSteps := s.executionPlanSummary(spec.TaskID)
	var b strings.Builder
	fmt.Fprintf(&b, "# Progress\n\n")
	fmt.Fprintf(&b, "## Objective\n")
	if spec.Title != "" {
		fmt.Fprintf(&b, "- Title: %s\n", spec.Title)
	}
	if spec.Objective != "" {
		fmt.Fprintf(&b, "- Objective: %s\n", spec.Objective)
	}
	if len(spec.SuccessCriteria) > 0 {
		fmt.Fprintf(&b, "- Success Criteria:\n")
		for _, criterion := range spec.SuccessCriteria {
			fmt.Fprintf(&b, "  - %s: %s\n", criterion.ID, criterion.Statement)
		}
	}
	if len(spec.Constraints) > 0 {
		fmt.Fprintf(&b, "- Constraints:\n")
		for _, constraint := range spec.Constraints {
			fmt.Fprintf(&b, "  - %s\n", constraint)
		}
	}
	fmt.Fprintf(&b, "\n## Repo Bearings\n")
	if !s.renderRepoBearingsMarkdown(&b, spec.TaskID) {
		fmt.Fprintf(&b, "- No repo bearings captured yet.\n")
	}
	fmt.Fprintf(&b, "\n## Current Status\n")
	fmt.Fprintf(&b, "- Phase: %s\n", state.Phase)
	fmt.Fprintf(&b, "- State: %s\n", state.State)
	if state.CurrentStepID != "" {
		fmt.Fprintf(&b, "- Current Step: %s\n", s.currentStepLabel(spec.TaskID, state.CurrentStepID))
	}
	if plan.Revision > 0 {
		fmt.Fprintf(&b, "- Plan Revision: %d\n", plan.Revision)
	}
	if plan.LastMutationRef != "" {
		fmt.Fprintf(&b, "- Plan Mutation Ref: %s\n", plan.LastMutationRef)
	}
	if plan.CurrentExecutionStepID != "" {
		fmt.Fprintf(&b, "- Current Execution Step: %s\n", s.currentStepLabel(spec.TaskID, plan.CurrentExecutionStepID))
	}
	if plan.CurrentSystemStepID != "" {
		fmt.Fprintf(&b, "- Current Gate: %s\n", s.currentStepLabel(spec.TaskID, plan.CurrentSystemStepID))
	}
	if len(plan.ReadyExecutionStepIDs) > 0 {
		fmt.Fprintf(&b, "- Ready Execution Steps: %s\n", strings.Join(s.executionStepLabels(spec.TaskID, plan.ReadyExecutionStepIDs), ", "))
	}
	if len(plan.BlockedExecutionStepIDs) > 0 {
		fmt.Fprintf(&b, "- Blocked Execution Steps: %s\n", strings.Join(s.executionStepLabels(spec.TaskID, plan.BlockedExecutionStepIDs), ", "))
	}
	if state.StatusReasonCode != "" {
		fmt.Fprintf(&b, "- Status Reason: %s\n", state.StatusReasonCode)
		if state.StatusDetailRef != "" {
			fmt.Fprintf(&b, "- Status Detail Ref: %s\n", state.StatusDetailRef)
		}
	}
	if summary != "" {
		fmt.Fprintf(&b, "- Summary: %s\n", summary)
	}
	if state.LastCheckpointRef != "" {
		fmt.Fprintf(&b, "- Last Checkpoint Ref: %s\n", state.LastCheckpointRef)
	}
	fmt.Fprintf(&b, "\n## Current Sprint\n")
	s.renderSprintMarkdown(&b, sprint)
	fmt.Fprintf(&b, "\n## Project Focus\n")
	s.renderProjectFocusMarkdown(&b, sprint.ProjectFocus)
	s.renderMissionMarkdown(&b, spec.TaskID)
	fmt.Fprintf(&b, "\n## Criteria Snapshot\n")
	if criteria, err := s.Store.LoadCriteria(spec.TaskID); err == nil && len(criteria.Criteria) > 0 {
		if strings.TrimSpace(criteria.Summary) != "" {
			fmt.Fprintf(&b, "- Summary: %s\n", strings.TrimSpace(criteria.Summary))
		}
		if criteria.CurrentCriterionID != "" {
			fmt.Fprintf(&b, "- Current Focus: %s %s\n", criteria.CurrentCriterionID, criteria.CurrentCriterionStatement)
		}
		fmt.Fprintf(&b, "- Passing: %d/%d\n", criteria.MetCount, len(spec.SuccessCriteria))
		for _, criterion := range spec.SuccessCriteria {
			status := criterionStatusForID(criteria, criterion.ID)
			if status.Passes {
				continue
			}
			fmt.Fprintf(&b, "- Open: %s %s\n", criterion.ID, criterion.Statement)
		}
	} else {
		fmt.Fprintf(&b, "- Criteria snapshot has not been materialized yet.\n")
	}
	fmt.Fprintf(&b, "\n## Latest Verification\n")
	if verification, err := s.Store.LoadVerification(spec.TaskID); err == nil {
		fmt.Fprintf(&b, "- Status: %s\n", verification.Status)
		if verification.FailureSummary != "" {
			fmt.Fprintf(&b, "- Summary: %s\n", verification.FailureSummary)
		} else {
			fmt.Fprintf(&b, "- Summary: Latest verifier pass is clean.\n")
		}
	} else {
		fmt.Fprintf(&b, "- No verifier report recorded yet.\n")
	}
	fmt.Fprintf(&b, "\n## Execution Plan\n")
	if len(executionSteps) == 0 {
		fmt.Fprintf(&b, "- No mutable execution plan has been recorded yet.\n")
	} else {
		s.renderExecutionPlanMarkdown(&b, executionSteps)
	}
	fmt.Fprintf(&b, "\n## Review And Completion\n")
	reviewStatus := "not_run"
	reviewSummary := "Review has not run yet."
	if report, err := s.Store.LoadReview(spec.TaskID); err == nil {
		reviewStatus = report.Status
		reviewSummary = strings.TrimSpace(report.Summary)
		if reviewSummary == "" {
			reviewSummary = "Review ran without an explicit summary."
		}
	}
	fmt.Fprintf(&b, "- Review Status: %s\n", reviewStatus)
	fmt.Fprintf(&b, "- Review Summary: %s\n", reviewSummary)
	completionStatus := "not_evaluated"
	completionSummary := "Completion gate has not been evaluated yet."
	if report, err := s.Store.LoadCompletion(spec.TaskID); err == nil {
		completionStatus = report.Status
		completionSummary = strings.TrimSpace(report.Summary)
		if completionSummary == "" {
			completionSummary = "Completion gate ran without an explicit summary."
		}
	}
	fmt.Fprintf(&b, "- Completion Status: %s\n", completionStatus)
	fmt.Fprintf(&b, "- Completion Summary: %s\n", completionSummary)
	fmt.Fprintf(&b, "\n## Recent Repairs\n")
	if !s.renderRepairSummary(&b, spec.TaskID, 3) {
		fmt.Fprintf(&b, "- No repair commands or workspace edits recorded yet.\n")
	}
	s.renderQualityDiagnosticsMarkdown(&b, spec.TaskID)
	fmt.Fprintf(&b, "\n## Latest Evidence\n")
	if len(refs) == 0 && state.LastEventRef == "" {
		fmt.Fprintf(&b, "- No runtime evidence has been recorded yet.\n")
	} else {
		for _, ref := range refs {
			fmt.Fprintf(&b, "- %s\n", ref)
		}
		if state.LastEventRef != "" {
			fmt.Fprintf(&b, "- %s\n", state.LastEventRef)
		}
	}
	fmt.Fprintf(&b, "\n## Next Step\n")
	if nextStep == "" {
		nextStep = "No next step recorded."
	}
	fmt.Fprintf(&b, "%s\n", nextStep)
	return b.String()
}

func (s *Service) buildContextPack(spec task.Spec, state task.State, builtAt, summary, nextStep string, refs []string, projectFocus *task.ProjectTaskContext) task.ContextSummary {
	refs = uniqueRefs(append(refs, state.LastEventRef))
	sections := s.buildContextSections(spec, state, summary, nextStep, refs)
	includedRefs := uniqueRefs(append(contextSectionRefs(sections), "context/summary.md"))
	return task.ContextSummary{
		SchemaVersion:    task.SchemaVersion,
		TaskID:           spec.TaskID,
		PackID:           task.NewID("PACK"),
		Phase:            state.Phase,
		State:            state.State,
		BuiltAt:          builtAt,
		UpdatedAt:        builtAt,
		Summary:          strings.TrimSpace(summary),
		NextStep:         strings.TrimSpace(nextStep),
		BasedOnRefs:      refs,
		IncludedRefs:     includedRefs,
		Sections:         sections,
		Compaction:       task.ContextCompaction{Performed: true, SummaryRef: "context/summary.md"},
		ProjectFocus:     projectFocus,
		StatusReasonCode: state.StatusReasonCode,
	}
}

func (s *Service) buildContinuitySnapshot(spec task.Spec, state task.State, contextPack task.ContextSummary, summary, nextStep string, refs []string, projectFocus *task.ProjectTaskContext) task.ContinuitySnapshot {
	plan, executionSteps := s.executionPlanSummary(spec.TaskID)
	criteriaSnapshot, err := s.Store.LoadCriteria(spec.TaskID)
	if err != nil {
		criteriaSnapshot = task.NewInitialCriteria(spec)
	}
	open := openCriteria(spec, &criteriaSnapshot)
	metCount := 0
	for _, criterion := range spec.SuccessCriteria {
		status := criterionStatusForID(criteriaSnapshot, criterion.ID)
		if status.Status == "met" && len(status.EvidenceRefs) > 0 {
			metCount++
		}
	}
	verificationStatus, verificationSummary := continuityVerificationStatusSummary(s.Store, spec.TaskID)
	reviewStatus, reviewSummary := continuityReviewStatusSummary(s.Store, spec.TaskID)
	completionStatus, completionSummary := continuityCompletionStatusSummary(s.Store, spec.TaskID)
	return task.ContinuitySnapshot{
		SchemaVersion:       task.SchemaVersion,
		SnapshotID:          task.NewID("CNT"),
		TaskID:              spec.TaskID,
		UpdatedAt:           contextPack.UpdatedAt,
		Phase:               state.Phase,
		State:               state.State,
		StatusReasonCode:    state.StatusReasonCode,
		Summary:             strings.TrimSpace(summary),
		NextStep:            strings.TrimSpace(nextStep),
		CurrentFocus:        s.buildContinuityFocus(spec, plan, executionSteps, open, projectFocus),
		StartupChecklist:    s.buildContinuityChecklist(spec.TaskID, projectFocus),
		CriteriaMetCount:    metCount,
		CriteriaTotalCount:  len(spec.SuccessCriteria),
		OpenCriteria:        open,
		VerificationStatus:  verificationStatus,
		VerificationSummary: verificationSummary,
		ReviewStatus:        reviewStatus,
		ReviewSummary:       reviewSummary,
		CompletionStatus:    completionStatus,
		CompletionSummary:   completionSummary,
		Refs: uniqueRefs(append(refs,
			"progress.md",
			"context/summary.md",
			"context/latest-pack.json",
			"sprint/latest.json",
		)),
	}
}

func (s *Service) buildSprintSnapshot(spec task.Spec, state task.State) task.SprintSnapshot {
	plan, executionSteps := s.executionPlanSummary(spec.TaskID)
	projectFocus := s.projectTaskContext(spec.TaskID)
	criteriaSnapshot, err := s.Store.LoadCriteria(spec.TaskID)
	if err != nil {
		criteriaSnapshot = task.NewInitialCriteria(spec)
	}
	open := openCriteria(spec, &criteriaSnapshot)
	active := focusCriteria(executionSteps, plan.CurrentExecutionStepID, open)
	activeIDs := make([]string, 0, len(active))
	activeSet := make(map[string]struct{}, len(active))
	for _, criterion := range active {
		id := strings.TrimSpace(criterion.ID)
		if id == "" {
			continue
		}
		activeIDs = append(activeIDs, id)
		activeSet[id] = struct{}{}
	}
	var deferred []string
	for _, criterion := range open {
		id := strings.TrimSpace(criterion.ID)
		if id == "" {
			continue
		}
		if _, ok := activeSet[id]; ok {
			continue
		}
		deferred = append(deferred, id)
	}

	currentExecutionStepID := strings.TrimSpace(plan.CurrentExecutionStepID)
	currentExecutionStepTitle := s.currentStepLabel(spec.TaskID, currentExecutionStepID)
	currentSystemStepID := strings.TrimSpace(plan.CurrentSystemStepID)
	currentSystemStepTitle := s.currentStepLabel(spec.TaskID, currentSystemStepID)
	currentStepID := currentExecutionStepID
	currentStepTitle := currentExecutionStepTitle
	if currentStepID == "" {
		currentStepID = currentSystemStepID
		currentStepTitle = currentSystemStepTitle
	}

	primaryCriterionID := strings.TrimSpace(criteriaSnapshot.CurrentCriterionID)
	primaryCriterionStatement := strings.TrimSpace(criteriaSnapshot.CurrentCriterionStatement)
	if primaryCriterionID == "" && len(active) > 0 {
		primaryCriterionID = strings.TrimSpace(active[0].ID)
		primaryCriterionStatement = strings.TrimSpace(active[0].Statement)
	}

	objective := strings.TrimSpace(currentExecutionStepTitle)
	switch {
	case objective != "":
	case primaryCriterionStatement != "":
		objective = primaryCriterionStatement
	case currentSystemStepTitle != "":
		objective = currentSystemStepTitle
	case state.State == task.StateDone:
		objective = "Task is already done."
	default:
		objective = "Hold the current task focus without expanding scope."
	}

	boundary := "Keep the sprint scoped to the current active criterion until durable evidence closes it."
	switch {
	case len(activeIDs) > 1:
		boundary = "Keep the sprint scoped to the current active criteria set and avoid expanding into deferred criteria."
	case len(deferred) > 0:
		boundary = fmt.Sprintf("Do not expand into deferred criteria yet: %s.", strings.Join(deferred, ", "))
	case len(activeIDs) == 0:
		boundary = "Do not reopen already passing criteria unless new verification or review evidence regresses them."
	}

	completionSignals := s.sprintCompletionSignals(spec, state, plan, executionSteps, active, primaryCriterionID, primaryCriterionStatement)
	summary := strings.TrimSpace(objective)
	switch {
	case primaryCriterionID != "" && len(deferred) > 0:
		summary = fmt.Sprintf("Current sprint closes %s before expanding into deferred criteria.", primaryCriterionID)
	case primaryCriterionID != "":
		summary = fmt.Sprintf("Current sprint closes %s.", primaryCriterionID)
	case state.State == task.StateDone:
		summary = "Current sprint is already complete."
	case currentSystemStepTitle != "":
		summary = fmt.Sprintf("Current sprint holds the runtime on %s.", currentSystemStepTitle)
	}

	refs := []string{"plan.json", "criteria/latest.json", "continuity/latest.json"}
	if projectFocus != nil {
		refs = append(refs, projectFocus.Refs...)
	}
	if state.LastVerificationRef != "" {
		refs = append(refs, state.LastVerificationRef)
	}
	if state.LastReviewRef != "" {
		refs = append(refs, state.LastReviewRef)
	}
	if state.LastCompletionRef != "" {
		refs = append(refs, state.LastCompletionRef)
	}

	return task.SprintSnapshot{
		SchemaVersion:             task.SchemaVersion,
		SnapshotID:                task.NewID("SPR"),
		TaskID:                    spec.TaskID,
		UpdatedAt:                 task.Now(),
		Summary:                   summary,
		Objective:                 objective,
		Boundary:                  boundary,
		CurrentStepID:             currentStepID,
		CurrentStepTitle:          currentStepTitle,
		CurrentExecutionStepID:    currentExecutionStepID,
		CurrentExecutionStepTitle: currentExecutionStepTitle,
		CurrentSystemStepID:       currentSystemStepID,
		CurrentSystemStepTitle:    currentSystemStepTitle,
		PrimaryCriterionID:        primaryCriterionID,
		PrimaryCriterionStatement: primaryCriterionStatement,
		ActiveCriterionIDs:        activeIDs,
		DeferredCriterionIDs:      deferred,
		CompletionSignals:         completionSignals,
		WorkingSetPaths:           s.continuityWorkingSetPaths(spec.TaskID),
		ProjectFocus:              projectFocus,
		Refs:                      uniqueRefs(refs),
	}
}

func (s *Service) sprintCompletionSignals(spec task.Spec, state task.State, plan task.Plan, executionSteps []task.Step, active []task.SuccessCriterion, primaryCriterionID, primaryCriterionStatement string) []string {
	var signals []string
	if primaryCriterionStatement != "" {
		signals = append(signals, primaryCriterionStatement)
	}
	for _, criterion := range active {
		statement := strings.TrimSpace(criterion.Statement)
		if statement == "" || statement == primaryCriterionStatement {
			continue
		}
		signals = append(signals, statement)
	}
	if stepID := strings.TrimSpace(plan.CurrentExecutionStepID); stepID != "" {
		for _, step := range executionSteps {
			if strings.TrimSpace(step.ID) != stepID {
				continue
			}
			for _, verifier := range step.Verifier {
				if trimmed := strings.TrimSpace(verifier); trimmed != "" {
					signals = append(signals, fmt.Sprintf("Verifier hint: %s", trimmed))
				}
			}
			break
		}
	}
	if verification, err := s.Store.LoadVerification(spec.TaskID); err == nil {
		switch verification.Status {
		case "passed":
			if spec.Kind == task.KindCoding {
				signals = append(signals, "Keep the latest verifier green after edits.")
			}
		case "failed":
			signals = append(signals, "Clear the latest verifier failure.")
		}
	}
	if len(active) == 0 {
		switch state.StatusReasonCode {
		case "blocked_review":
			signals = append(signals, "Clear the current review/completion blocker.")
		case "":
			if state.State != task.StateDone {
				signals = append(signals, "Keep the task moving on the current runtime gate.")
			}
		}
	}
	if primaryCriterionID != "" && len(signals) == 0 {
		signals = append(signals, fmt.Sprintf("Close %s with durable evidence.", primaryCriterionID))
	}
	return uniqueNonEmptyStrings(signals)
}

func continuityVerificationStatusSummary(store *artifact.Store, taskID string) (string, string) {
	report, err := store.LoadVerification(taskID)
	if err != nil {
		return "not_run", "Verification has not run yet."
	}
	if strings.TrimSpace(report.FailureSummary) != "" {
		return report.Status, strings.TrimSpace(report.FailureSummary)
	}
	return report.Status, "Latest verifier pass is clean."
}

func continuityReviewStatusSummary(store *artifact.Store, taskID string) (string, string) {
	report, err := store.LoadReview(taskID)
	if err != nil {
		return "not_run", "Review has not run yet."
	}
	summary := strings.TrimSpace(report.Summary)
	if summary == "" {
		summary = "Review ran without an explicit summary."
	}
	return report.Status, summary
}

func continuityCompletionStatusSummary(store *artifact.Store, taskID string) (string, string) {
	report, err := store.LoadCompletion(taskID)
	if err != nil {
		return "not_evaluated", "Completion gate has not been evaluated yet."
	}
	summary := strings.TrimSpace(report.Summary)
	if summary == "" {
		summary = "Completion gate ran without an explicit summary."
	}
	return report.Status, summary
}

func (s *Service) buildContinuityFocus(spec task.Spec, plan task.Plan, executionSteps []task.Step, open []task.SuccessCriterion, projectFocus *task.ProjectTaskContext) task.ContinuityFocus {
	focus := task.ContinuityFocus{
		CurrentExecutionStepID:    strings.TrimSpace(plan.CurrentExecutionStepID),
		CurrentExecutionStepTitle: s.currentStepLabel(spec.TaskID, plan.CurrentExecutionStepID),
		CurrentSystemStepID:       strings.TrimSpace(plan.CurrentSystemStepID),
		CurrentSystemStepTitle:    s.currentStepLabel(spec.TaskID, plan.CurrentSystemStepID),
		WorkingSetPaths:           s.continuityWorkingSetPaths(spec.TaskID),
		ProjectFocus:              projectFocus,
	}
	if stepID := strings.TrimSpace(plan.CurrentExecutionStepID); stepID != "" {
		focus.CurrentStepID = stepID
		focus.CurrentStepTitle = s.currentStepLabel(spec.TaskID, stepID)
	} else {
		focus.CurrentStepID = strings.TrimSpace(plan.CurrentSystemStepID)
		focus.CurrentStepTitle = s.currentStepLabel(spec.TaskID, plan.CurrentSystemStepID)
	}
	focus.Criteria = focusCriteria(executionSteps, plan.CurrentExecutionStepID, open)
	for _, criterion := range focus.Criteria {
		focus.CriterionIDs = append(focus.CriterionIDs, strings.TrimSpace(criterion.ID))
	}
	if len(focus.Criteria) > 0 {
		focus.PrimaryCriterionID = strings.TrimSpace(focus.Criteria[0].ID)
		focus.PrimaryCriterionStatement = strings.TrimSpace(focus.Criteria[0].Statement)
	}
	return focus
}

func focusCriteria(executionSteps []task.Step, currentExecutionStepID string, open []task.SuccessCriterion) []task.SuccessCriterion {
	if strings.TrimSpace(currentExecutionStepID) != "" {
		for _, step := range executionSteps {
			if strings.TrimSpace(step.ID) != strings.TrimSpace(currentExecutionStepID) {
				continue
			}
			if matched := filterCriteriaByIDs(open, step.Covers); len(matched) > 0 {
				return matched
			}
			break
		}
	}
	if len(open) == 0 {
		return nil
	}
	return []task.SuccessCriterion{open[0]}
}

func filterCriteriaByIDs(criteria []task.SuccessCriterion, ids []string) []task.SuccessCriterion {
	if len(criteria) == 0 || len(ids) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}
	var out []task.SuccessCriterion
	for _, criterion := range criteria {
		if _, ok := allowed[strings.TrimSpace(criterion.ID)]; ok {
			out = append(out, criterion)
		}
	}
	return out
}

func (s *Service) projectTaskContext(taskID string) *task.ProjectTaskContext {
	project, err := s.loadOrInitProject()
	if err != nil {
		return nil
	}
	project, err = s.refreshProject(project)
	if err != nil {
		return nil
	}
	taskID = strings.TrimSpace(taskID)
	stepByID := make(map[string]task.ProjectStep, len(project.Steps))
	statusByID := make(map[string]string, len(project.Steps))
	for _, step := range project.Steps {
		stepByID[step.ID] = step
		statusByID[step.ID] = strings.TrimSpace(strings.ToLower(step.Status))
	}
	branchByID := make(map[string]task.ProjectBranch, len(project.Branches))
	for _, branch := range project.Branches {
		branchByID[branch.ID] = branch
	}
	var boundSteps []task.ProjectStep
	var boundBranches []task.ProjectBranch
	for _, step := range project.Steps {
		if strings.TrimSpace(step.TaskID) == taskID {
			boundSteps = append(boundSteps, step)
		}
	}
	for _, branch := range project.Branches {
		if strings.TrimSpace(branch.TaskID) == taskID {
			boundBranches = append(boundBranches, branch)
		}
	}
	if len(boundSteps) == 0 && len(boundBranches) == 0 {
		return nil
	}
	focus := &task.ProjectTaskContext{
		ReadyProjectStepIDs:    append([]string(nil), project.ReadyStepIDs...),
		BlockedProjectStepIDs:  append([]string(nil), project.BlockedStepIDs...),
		ActiveProjectBranchIDs: append([]string(nil), project.ActiveBranchIDs...),
		Refs:                   []string{"workspace:.ngen/project/project.json"},
	}
	if project.LastMutationRef != "" {
		focus.Refs = append(focus.Refs, project.LastMutationRef)
	}
	if currentID := strings.TrimSpace(project.CurrentStepID); currentID != "" {
		focus.WorkspaceCurrentStepID = currentID
		if step, ok := stepByID[currentID]; ok {
			focus.WorkspaceCurrentStepTitle = strings.TrimSpace(step.Title)
		}
	}
	for _, step := range boundSteps {
		focus.BoundStepIDs = append(focus.BoundStepIDs, strings.TrimSpace(step.ID))
	}
	for _, branch := range boundBranches {
		focus.BoundBranchIDs = append(focus.BoundBranchIDs, strings.TrimSpace(branch.ID))
	}
	focus.BoundStepIDs = uniqueNonEmptyStrings(focus.BoundStepIDs)
	focus.BoundBranchIDs = uniqueNonEmptyStrings(focus.BoundBranchIDs)

	primaryStep := pickPrimaryProjectStep(boundSteps, project.CurrentStepID)
	if primaryStep != nil {
		focus.PrimaryStepID = strings.TrimSpace(primaryStep.ID)
		focus.PrimaryStepTitle = strings.TrimSpace(primaryStep.Title)
		focus.PrimaryStepStatus = strings.TrimSpace(primaryStep.Status)
		focus.ParentStepID = strings.TrimSpace(primaryStep.ParentStepID)
		focus.Priority = strings.TrimSpace(primaryStep.Priority)
		focus.Notes = strings.TrimSpace(primaryStep.Notes)
		focus.DependsOnStepIDs = append([]string(nil), primaryStep.DependsOn...)
		focus.DependenciesSatisfied = projectStepDepsSatisfied(*primaryStep, statusByID)
		for _, depID := range primaryStep.DependsOn {
			status := statusByID[depID]
			if status == task.ProjectStepStatusCompleted || status == task.ProjectStepStatusCancelled {
				continue
			}
			focus.UnmetDependencyStepIDs = append(focus.UnmetDependencyStepIDs, depID)
		}
		for _, depID := range primaryStep.DependsOn {
			if dep, ok := stepByID[depID]; ok {
				focus.DependencySteps = append(focus.DependencySteps, projectTaskLink(dep, branchByID))
			}
		}
		boundStepSet := make(map[string]struct{}, len(focus.BoundStepIDs))
		for _, id := range focus.BoundStepIDs {
			boundStepSet[id] = struct{}{}
		}
		for _, step := range project.Steps {
			if len(step.DependsOn) == 0 {
				continue
			}
			for _, depID := range step.DependsOn {
				if _, ok := boundStepSet[strings.TrimSpace(depID)]; !ok {
					continue
				}
				focus.DownstreamSteps = append(focus.DownstreamSteps, projectTaskLink(step, branchByID))
				break
			}
		}
	}
	if primaryBranch := pickPrimaryProjectBranch(boundBranches, primaryStep, branchByID); primaryBranch != nil {
		focus.PrimaryBranchID = strings.TrimSpace(primaryBranch.ID)
		focus.PrimaryBranchTitle = strings.TrimSpace(primaryBranch.Title)
		focus.PrimaryBranchStatus = strings.TrimSpace(primaryBranch.Status)
	}
	focus.DependsOnStepIDs = uniqueNonEmptyStrings(focus.DependsOnStepIDs)
	focus.UnmetDependencyStepIDs = uniqueNonEmptyStrings(focus.UnmetDependencyStepIDs)
	focus.ReadyProjectStepIDs = uniqueNonEmptyStrings(focus.ReadyProjectStepIDs)
	focus.BlockedProjectStepIDs = uniqueNonEmptyStrings(focus.BlockedProjectStepIDs)
	focus.ActiveProjectBranchIDs = uniqueNonEmptyStrings(focus.ActiveProjectBranchIDs)
	return focus
}

func pickPrimaryProjectStep(steps []task.ProjectStep, currentStepID string) *task.ProjectStep {
	currentStepID = strings.TrimSpace(currentStepID)
	for i := range steps {
		if strings.TrimSpace(steps[i].ID) == currentStepID {
			return &steps[i]
		}
	}
	for i := range steps {
		if strings.EqualFold(strings.TrimSpace(steps[i].Status), task.ProjectStepStatusInProgress) {
			return &steps[i]
		}
	}
	if len(steps) == 0 {
		return nil
	}
	return &steps[0]
}

func pickPrimaryProjectBranch(branches []task.ProjectBranch, primaryStep *task.ProjectStep, branchByID map[string]task.ProjectBranch) *task.ProjectBranch {
	if primaryStep != nil {
		if branch, ok := branchByID[strings.TrimSpace(primaryStep.BranchID)]; ok {
			copy := branch
			return &copy
		}
	}
	for i := range branches {
		if strings.EqualFold(strings.TrimSpace(branches[i].Status), task.ProjectBranchStatusActive) {
			return &branches[i]
		}
	}
	if len(branches) == 0 {
		return nil
	}
	return &branches[0]
}

func projectTaskLink(step task.ProjectStep, branches map[string]task.ProjectBranch) task.ProjectTaskLink {
	link := task.ProjectTaskLink{
		StepID:   strings.TrimSpace(step.ID),
		Title:    strings.TrimSpace(step.Title),
		Status:   strings.TrimSpace(step.Status),
		BranchID: strings.TrimSpace(step.BranchID),
		TaskID:   strings.TrimSpace(step.TaskID),
	}
	if branch, ok := branches[link.BranchID]; ok {
		link.BranchTitle = strings.TrimSpace(branch.Title)
		link.BranchStatus = strings.TrimSpace(branch.Status)
		if link.TaskID == "" {
			link.TaskID = strings.TrimSpace(branch.TaskID)
		}
		link.TaskRef = strings.TrimSpace(branch.TaskRef)
		link.StatusRef = strings.TrimSpace(branch.StatusRef)
		link.HandoffRef = strings.TrimSpace(branch.HandoffRef)
	}
	return link
}

func (s *Service) continuityWorkingSetPaths(taskID string) []string {
	var paths []string
	if checkpoint, err := s.Store.LoadLatestCheckpoint(taskID); err == nil && checkpoint.WorkspaceSnapshot != nil && checkpoint.WorkspaceSnapshot.Git != nil {
		paths = append(paths, checkpoint.WorkspaceSnapshot.Git.ChangedPaths...)
	}
	if edits, err := s.Store.ReadWorkspaceEdits(taskID); err == nil && len(edits) > 0 {
		for _, edit := range takeLastWorkspaceEdits(edits, 3) {
			for _, change := range edit.FileChanges {
				paths = append(paths, strings.TrimSpace(change.Path))
			}
		}
	}
	paths = uniqueNonEmptyStrings(paths)
	filtered := make([]string, 0, len(paths))
	for _, path := range paths {
		if !includeContinuityWorkingSetPath(path) {
			continue
		}
		filtered = append(filtered, path)
	}
	if len(filtered) > 8 {
		filtered = filtered[:8]
	}
	return filtered
}

func (s *Service) buildContinuityChecklist(taskID string, projectFocus *task.ProjectTaskContext) []task.ContinuityChecklistItem {
	items := []task.ContinuityChecklistItem{
		{
			ID:     "read_progress",
			Kind:   "read_ref",
			Title:  "Read progress.md",
			Ref:    "progress.md",
			Reason: "Load the live task status, blocker, and next step before taking new action.",
		},
		{
			ID:     "read_sprint_contract",
			Kind:   "read_ref",
			Title:  "Read sprint/latest.json",
			Ref:    "sprint/latest.json",
			Reason: "Load the durable current-scope contract before widening into adjacent criteria or files.",
		},
		{
			ID:     "read_context_summary",
			Kind:   "read_ref",
			Title:  "Read context/summary.md",
			Ref:    "context/summary.md",
			Reason: "Load the compacted task-local continuity summary before expanding scope again.",
		},
		{
			ID:     "read_acceptance_ledger",
			Kind:   "read_ref",
			Title:  "Read criteria/latest.json",
			Ref:    "criteria/latest.json",
			Reason: "Load the durable acceptance ledger and keep the next session on the current failing criterion.",
		},
	}
	if projectFocus != nil && len(projectFocus.Refs) > 0 {
		items = append(items, task.ContinuityChecklistItem{
			ID:     "read_project_focus",
			Kind:   "read_ref",
			Title:  "Read workspace project graph",
			Ref:    projectFocus.Refs[0],
			Reason: "Load the task-scoped project binding, dependency, and branch truth before widening into sibling work.",
		})
	}
	baseline, err := s.Store.LoadBaseline(taskID)
	if err != nil {
		return items
	}
	if baseline.WorkspaceSnapshot != nil && baseline.WorkspaceSnapshot.Git != nil && baseline.WorkspaceSnapshot.Git.IsRepository {
		items = append(items,
			task.ContinuityChecklistItem{
				ID:      "git_status",
				Kind:    "vcs_command",
				Title:   "Inspect git status",
				Command: []string{"git", "status", "--short"},
				Reason:  "Confirm the current dirty working set before extending the task.",
			},
			task.ContinuityChecklistItem{
				ID:      "git_log",
				Kind:    "vcs_command",
				Title:   "Inspect recent git log",
				Command: []string{"git", "log", "--oneline", "-5"},
				Reason:  "Get bearings on the most recent committed work before continuing.",
			},
		)
	}
	for i, hint := range baseline.CommandHints {
		if len(hint.Command) == 0 {
			continue
		}
		items = append(items, task.ContinuityChecklistItem{
			ID:      fmt.Sprintf("repo_%s_%d", strings.TrimSpace(hint.Kind), i+1),
			Kind:    "repo_command",
			Title:   continuityHintTitle(hint),
			Ref:     strings.TrimSpace(hint.SourceRef),
			Command: append([]string(nil), hint.Command...),
			Reason:  strings.TrimSpace(hint.Reason),
		})
	}
	return items
}

func continuityHintTitle(hint task.CommandHint) string {
	switch strings.TrimSpace(hint.Kind) {
	case "setup":
		return "Run repo-owned setup entrypoint"
	case "verify":
		return "Run repo-owned verifier entrypoint"
	default:
		return "Run repo-owned command hint"
	}
}

func includeContinuityWorkingSetPath(path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	switch path {
	case "", ".", "./":
		return false
	}
	if strings.HasPrefix(path, "../") {
		return false
	}
	if path == ".ngen" || strings.HasPrefix(path, ".ngen/") {
		return false
	}
	if path == ".git" || strings.HasPrefix(path, ".git/") {
		return false
	}
	return true
}

func (s *Service) buildContextSections(spec task.Spec, state task.State, summary, nextStep string, refs []string) []task.ContextSection {
	grouped := map[string][]string{}
	for _, ref := range uniqueRefs(refs) {
		grouped[classifyContextRef(ref)] = append(grouped[classifyContextRef(ref)], ref)
	}
	if s.Config.Memory.Enabled {
		grouped["workspace_memory"] = append(grouped["workspace_memory"], s.Store.MemoryMarkdownRef())
	}
	grouped["memory_summary"] = append(grouped["memory_summary"], "context/summary.md")
	order := []struct {
		name   string
		budget int
	}{
		{name: "task", budget: 900},
		{name: "plan", budget: 700},
		{name: "project", budget: 700},
		{name: "mission", budget: 900},
		{name: "observations", budget: 2200},
		{name: "blockers", budget: 900},
		{name: "workspace_memory", budget: 900},
		{name: "memory_summary", budget: 1000},
	}
	var sections []task.ContextSection
	for _, item := range order {
		refs := uniqueRefs(grouped[item.name])
		if len(refs) == 0 {
			continue
		}
		sections = append(sections, task.ContextSection{
			Name:         item.name,
			TokenBudget:  item.budget,
			ActualTokens: s.estimateContextTokens(spec, state, summary, nextStep, item.name, refs),
			Refs:         refs,
		})
	}
	return sections
}

func classifyContextRef(ref string) string {
	switch {
	case ref == "task.json", ref == "state.json", ref == "baseline.json", strings.HasPrefix(ref, "criteria/"), strings.HasPrefix(ref, "sprint/"):
		return "task"
	case ref == "plan.json":
		return "plan"
	case ref == "workspace:.ngen/project/project.json", strings.HasPrefix(ref, "project_updates.jsonl#"):
		return "project"
	case strings.HasPrefix(ref, "workspace:.ngen/missions/"):
		return "mission"
	case strings.HasPrefix(ref, "approvals.jsonl#"), strings.HasPrefix(ref, "input_requests.jsonl#"), strings.HasPrefix(ref, "workspace:.ngen/watches/"):
		return "blockers"
	default:
		return "observations"
	}
}

func (s *Service) estimateContextTokens(spec task.Spec, state task.State, summary, nextStep, section string, refs []string) int {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", section)
	for _, ref := range refs {
		fmt.Fprintf(&b, "%s\n", ref)
	}
	switch section {
	case "task":
		fmt.Fprintf(&b, "%s\n%s\n", spec.Title, spec.Objective)
		for _, criterion := range spec.SuccessCriteria {
			fmt.Fprintf(&b, "%s %s\n", criterion.ID, criterion.Statement)
		}
	case "project":
		if focus := s.projectTaskContext(spec.TaskID); focus != nil {
			fmt.Fprintf(&b, "%s\n%s\n", focus.PrimaryStepTitle, strings.Join(focus.BoundStepIDs, ","))
			for _, link := range focus.DependencySteps {
				fmt.Fprintf(&b, "%s %s %s\n", link.StepID, link.Status, link.TaskID)
			}
		}
	case "mission":
		if view, err := s.providerMissionViewForTask(spec.TaskID); err == nil && view != nil {
			fmt.Fprintf(&b, "%s %s %s\n", view.Mission.MissionID, view.Mission.Status, view.Mission.CurrentMilestoneID)
			fmt.Fprintf(&b, "%s\n", strings.Join(view.Contract.AcceptanceTests, "\n"))
			if view.LatestValidation != nil {
				fmt.Fprintf(&b, "%s %s\n", view.LatestValidation.Status, view.LatestValidation.Summary)
			}
		}
	case "observations":
		fmt.Fprintf(&b, "%s\n%s\n", summary, state.LastVerificationRef)
	case "blockers":
		fmt.Fprintf(&b, "%s\n%s\n%s\n", state.StatusReasonCode, state.StatusDetailRef, nextStep)
	case "workspace_memory":
		if memory, err := s.refreshMemoryMarkdown(); err == nil {
			b.Write(memory)
		}
	case "memory_summary":
		fmt.Fprintf(&b, "%s\n%s\n", summary, nextStep)
	}
	runes := utf8.RuneCountInString(strings.TrimSpace(b.String()))
	if runes == 0 {
		return 0
	}
	return (runes + 3) / 4
}

func contextSectionRefs(sections []task.ContextSection) []string {
	var refs []string
	for _, section := range sections {
		refs = append(refs, section.Refs...)
	}
	return uniqueRefs(refs)
}

func (s *Service) renderContextCompaction(spec task.Spec, state task.State, summary string, refs []string, nextStep string, sprint task.SprintSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Context Summary\n\n")
	fmt.Fprintf(&b, "## Task Focus\n")
	if spec.Title != "" {
		fmt.Fprintf(&b, "- Title: %s\n", spec.Title)
	}
	fmt.Fprintf(&b, "- Objective: %s\n", spec.Objective)
	fmt.Fprintf(&b, "- Phase: %s\n", state.Phase)
	fmt.Fprintf(&b, "- State: %s\n", state.State)
	if state.StatusReasonCode != "" {
		fmt.Fprintf(&b, "- Status Reason: %s\n", state.StatusReasonCode)
	}
	if strings.TrimSpace(summary) != "" {
		fmt.Fprintf(&b, "- Summary: %s\n", strings.TrimSpace(summary))
	}
	fmt.Fprintf(&b, "- Next Step: %s\n", nextStep)
	fmt.Fprintf(&b, "\n## Repo Bearings\n")
	if !s.renderRepoBearingsMarkdown(&b, spec.TaskID) {
		fmt.Fprintf(&b, "- No repo bearings captured yet.\n")
	}
	fmt.Fprintf(&b, "\n## Current Sprint\n")
	s.renderSprintMarkdown(&b, sprint)
	fmt.Fprintf(&b, "\n## Project Focus\n")
	s.renderProjectFocusMarkdown(&b, sprint.ProjectFocus)
	s.renderMissionMarkdown(&b, spec.TaskID)
	fmt.Fprintf(&b, "\n## Criteria Snapshot\n")
	if criteria, err := s.Store.LoadCriteria(spec.TaskID); err == nil && len(criteria.Criteria) > 0 {
		if strings.TrimSpace(criteria.Summary) != "" {
			fmt.Fprintf(&b, "- Summary: %s\n", strings.TrimSpace(criteria.Summary))
		}
		if criteria.CurrentCriterionID != "" {
			fmt.Fprintf(&b, "- Current Focus: %s %s\n", criteria.CurrentCriterionID, criteria.CurrentCriterionStatement)
		}
		fmt.Fprintf(&b, "- Passing: %d/%d\n", criteria.MetCount, len(spec.SuccessCriteria))
		for _, criterion := range spec.SuccessCriteria {
			status := criterionStatusForID(criteria, criterion.ID)
			if status.Passes {
				continue
			}
			fmt.Fprintf(&b, "- Open: %s %s\n", criterion.ID, criterion.Statement)
		}
	} else {
		fmt.Fprintf(&b, "- Criteria snapshot has not been materialized yet.\n")
	}
	fmt.Fprintf(&b, "\n## Latest Verification\n")
	if verification, err := s.Store.LoadVerification(spec.TaskID); err == nil {
		fmt.Fprintf(&b, "- Status: %s\n", verification.Status)
		if verification.FailureSummary != "" {
			fmt.Fprintf(&b, "- Summary: %s\n", verification.FailureSummary)
		} else {
			fmt.Fprintf(&b, "- Summary: Latest verifier pass is clean.\n")
		}
	} else {
		fmt.Fprintf(&b, "- No verifier report recorded yet.\n")
	}
	fmt.Fprintf(&b, "\n## Review And Completion\n")
	if reviewReport, err := s.Store.LoadReview(spec.TaskID); err == nil {
		fmt.Fprintf(&b, "- Review: %s - %s\n", reviewReport.Status, strings.TrimSpace(reviewReport.Summary))
	} else {
		fmt.Fprintf(&b, "- Review: not_run\n")
	}
	if completion, err := s.Store.LoadCompletion(spec.TaskID); err == nil {
		fmt.Fprintf(&b, "- Completion: %s - %s\n", completion.Status, strings.TrimSpace(completion.Summary))
	} else {
		fmt.Fprintf(&b, "- Completion: not_evaluated\n")
	}
	fmt.Fprintf(&b, "\n## Recent Repairs\n")
	if !s.renderRepairSummary(&b, spec.TaskID, 5) {
		fmt.Fprintf(&b, "- No repair commands or workspace edits recorded yet.\n")
	}
	fmt.Fprintf(&b, "\n## Continuity Refs\n")
	for _, ref := range refs {
		fmt.Fprintf(&b, "- %s\n", ref)
	}
	if state.LastEventRef != "" {
		fmt.Fprintf(&b, "- %s\n", state.LastEventRef)
	}
	return b.String()
}

func (s *Service) renderSprintMarkdown(b *strings.Builder, sprint task.SprintSnapshot) {
	if strings.TrimSpace(sprint.Summary) == "" && strings.TrimSpace(sprint.Objective) == "" && strings.TrimSpace(sprint.PrimaryCriterionID) == "" {
		fmt.Fprintf(b, "- Sprint contract has not been materialized yet.\n")
		return
	}
	if sprint.Summary != "" {
		fmt.Fprintf(b, "- Summary: %s\n", sprint.Summary)
	}
	if sprint.Objective != "" {
		fmt.Fprintf(b, "- Objective: %s\n", sprint.Objective)
	}
	if sprint.Boundary != "" {
		fmt.Fprintf(b, "- Boundary: %s\n", sprint.Boundary)
	}
	if sprint.CurrentStepID != "" {
		fmt.Fprintf(b, "- Current Step: %s\n", sprint.CurrentStepTitle)
	}
	if sprint.PrimaryCriterionID != "" {
		fmt.Fprintf(b, "- Primary Criterion: %s %s\n", sprint.PrimaryCriterionID, sprint.PrimaryCriterionStatement)
	}
	if len(sprint.ActiveCriterionIDs) > 0 {
		fmt.Fprintf(b, "- Active Criteria: %s\n", strings.Join(sprint.ActiveCriterionIDs, ", "))
	}
	if len(sprint.DeferredCriterionIDs) > 0 {
		fmt.Fprintf(b, "- Deferred Criteria: %s\n", strings.Join(sprint.DeferredCriterionIDs, ", "))
	}
	if len(sprint.CompletionSignals) > 0 {
		fmt.Fprintf(b, "- Completion Signals:\n")
		for _, signal := range sprint.CompletionSignals {
			fmt.Fprintf(b, "  - %s\n", signal)
		}
	}
	if len(sprint.WorkingSetPaths) > 0 {
		fmt.Fprintf(b, "- Working Set: %s\n", strings.Join(sprint.WorkingSetPaths, ", "))
	}
}

func (s *Service) renderProjectFocusMarkdown(b *strings.Builder, focus *task.ProjectTaskContext) {
	if focus == nil || (focus.PrimaryStepID == "" && len(focus.BoundStepIDs) == 0 && len(focus.BoundBranchIDs) == 0) {
		fmt.Fprintf(b, "- No task-scoped workspace project binding is active yet.\n")
		return
	}
	if focus.WorkspaceCurrentStepID != "" {
		if focus.WorkspaceCurrentStepTitle != "" {
			fmt.Fprintf(b, "- Workspace Current Step: %s %s\n", focus.WorkspaceCurrentStepID, focus.WorkspaceCurrentStepTitle)
		} else {
			fmt.Fprintf(b, "- Workspace Current Step: %s\n", focus.WorkspaceCurrentStepID)
		}
	}
	if focus.PrimaryStepID != "" {
		fmt.Fprintf(b, "- Primary Step: %s %s (%s)\n", focus.PrimaryStepID, focus.PrimaryStepTitle, focus.PrimaryStepStatus)
	}
	if focus.PrimaryBranchID != "" {
		fmt.Fprintf(b, "- Primary Branch: %s %s (%s)\n", focus.PrimaryBranchID, focus.PrimaryBranchTitle, focus.PrimaryBranchStatus)
	}
	if focus.ParentStepID != "" {
		fmt.Fprintf(b, "- Parent Step: %s\n", focus.ParentStepID)
	}
	if focus.Priority != "" {
		fmt.Fprintf(b, "- Priority: %s\n", focus.Priority)
	}
	if len(focus.BoundStepIDs) > 0 {
		fmt.Fprintf(b, "- Bound Steps: %s\n", strings.Join(focus.BoundStepIDs, ", "))
	}
	if len(focus.BoundBranchIDs) > 0 {
		fmt.Fprintf(b, "- Bound Branches: %s\n", strings.Join(focus.BoundBranchIDs, ", "))
	}
	if len(focus.DependsOnStepIDs) > 0 {
		fmt.Fprintf(b, "- Depends On: %s\n", strings.Join(focus.DependsOnStepIDs, ", "))
		fmt.Fprintf(b, "- Dependencies Satisfied: %t\n", focus.DependenciesSatisfied)
	}
	if len(focus.UnmetDependencyStepIDs) > 0 {
		fmt.Fprintf(b, "- Unmet Dependencies: %s\n", strings.Join(focus.UnmetDependencyStepIDs, ", "))
	}
	if len(focus.DependencySteps) > 0 {
		fmt.Fprintf(b, "- Upstream Task Links:\n")
		for _, link := range focus.DependencySteps {
			fmt.Fprintf(b, "  - %s %s (%s)", link.StepID, link.Title, link.Status)
			if link.TaskID != "" {
				fmt.Fprintf(b, " -> %s", link.TaskID)
			}
			fmt.Fprintln(b)
		}
	}
	if len(focus.DownstreamSteps) > 0 {
		fmt.Fprintf(b, "- Downstream Steps:\n")
		for _, link := range focus.DownstreamSteps {
			fmt.Fprintf(b, "  - %s %s (%s)\n", link.StepID, link.Title, link.Status)
		}
	}
	if len(focus.ReadyProjectStepIDs) > 0 {
		fmt.Fprintf(b, "- Workspace Ready Steps: %s\n", strings.Join(focus.ReadyProjectStepIDs, ", "))
	}
	if len(focus.BlockedProjectStepIDs) > 0 {
		fmt.Fprintf(b, "- Workspace Blocked Steps: %s\n", strings.Join(focus.BlockedProjectStepIDs, ", "))
	}
	if len(focus.ActiveProjectBranchIDs) > 0 {
		fmt.Fprintf(b, "- Active Branches: %s\n", strings.Join(focus.ActiveProjectBranchIDs, ", "))
	}
	if focus.Notes != "" {
		fmt.Fprintf(b, "- Notes: %s\n", focus.Notes)
	}
}

func (s *Service) renderRepoBearingsMarkdown(b *strings.Builder, taskID string) bool {
	rendered := false
	if baseline, err := s.Store.LoadBaseline(taskID); err == nil {
		if len(baseline.RepoTruthRefs) > 0 {
			rendered = true
			fmt.Fprintf(b, "- Repo Truth Refs: %s\n", strings.Join(baseline.RepoTruthRefs, ", "))
		}
		for _, hint := range baseline.CommandHints {
			if len(hint.Command) == 0 {
				continue
			}
			rendered = true
			fmt.Fprintf(b, "- Command Hint [%s]: `%s`", strings.TrimSpace(hint.Kind), strings.Join(hint.Command, " "))
			if strings.TrimSpace(hint.SourceRef) != "" {
				fmt.Fprintf(b, " (%s)", strings.TrimSpace(hint.SourceRef))
			}
			if strings.TrimSpace(hint.Reason) != "" {
				fmt.Fprintf(b, " - %s", strings.TrimSpace(hint.Reason))
			}
			fmt.Fprintln(b)
		}
		if baseline.WorkspaceSnapshot != nil && baseline.WorkspaceSnapshot.Git != nil {
			rendered = true
			s.renderGitSummaryMarkdown(b, "Baseline Git", *baseline.WorkspaceSnapshot.Git)
		}
	}
	if checkpoint, err := s.Store.LoadLatestCheckpoint(taskID); err == nil && checkpoint.WorkspaceSnapshot != nil && checkpoint.WorkspaceSnapshot.Git != nil {
		rendered = true
		s.renderGitSummaryMarkdown(b, "Checkpoint Git", *checkpoint.WorkspaceSnapshot.Git)
	}
	return rendered
}

func (s *Service) renderGitSummaryMarkdown(b *strings.Builder, label string, git task.GitSummary) {
	if !git.IsRepository {
		fmt.Fprintf(b, "- %s: not a git repository.\n", label)
		return
	}
	status := strings.TrimSpace(git.StatusSummary)
	if status == "" {
		if git.Dirty {
			status = "dirty working tree"
		} else {
			status = "clean working tree"
		}
	}
	branch := strings.TrimSpace(git.Branch)
	head := strings.TrimSpace(git.Head)
	switch {
	case branch != "" && head != "":
		fmt.Fprintf(b, "- %s: branch %s @ %s (%s)\n", label, branch, head, status)
	case head != "":
		fmt.Fprintf(b, "- %s: %s (%s)\n", label, head, status)
	default:
		fmt.Fprintf(b, "- %s: %s\n", label, status)
	}
	if len(git.ChangedPaths) > 0 {
		fmt.Fprintf(b, "- %s Changed Paths: %s\n", label, strings.Join(git.ChangedPaths, ", "))
	}
	for _, commit := range git.RecentCommits {
		if strings.TrimSpace(commit.SHA) == "" && strings.TrimSpace(commit.Subject) == "" {
			continue
		}
		fmt.Fprintf(b, "- %s Recent Commit: %s %s\n", label, strings.TrimSpace(commit.SHA), strings.TrimSpace(commit.Subject))
	}
}

func (s *Service) renderRepairSummary(b *strings.Builder, taskID string, limit int) bool {
	rendered := false
	if edits, err := s.Store.ReadWorkspaceEdits(taskID); err == nil && len(edits) > 0 {
		for _, record := range takeLastWorkspaceEdits(edits, limit) {
			rendered = true
			fmt.Fprintf(b, "- Workspace Edit [%s]: %s", record.Status, strings.TrimSpace(record.Summary))
			if len(record.FileChanges) > 0 {
				fmt.Fprintf(b, " (%s)", changedPathsSummary(record.FileChanges))
			}
			fmt.Fprintln(b)
		}
	}
	if commands, err := s.Store.ReadCommandRuns(taskID); err == nil && len(commands) > 0 {
		for _, record := range takeLastCommandRuns(commands, limit) {
			rendered = true
			fmt.Fprintf(b, "- Command [%s/%s]: %s", record.Kind, record.Status, strings.TrimSpace(record.Summary))
			if len(record.Argv) > 0 {
				fmt.Fprintf(b, " (`%s`)", strings.Join(record.Argv, " "))
			}
			fmt.Fprintln(b)
		}
	}
	return rendered
}

func takeLastWorkspaceEdits(items []task.WorkspaceEditRecord, limit int) []task.WorkspaceEditRecord {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func takeLastCommandRuns(items []task.CommandRunRecord, limit int) []task.CommandRunRecord {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func changedPathsSummary(changes []task.WorkspaceFileChange) string {
	var paths []string
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	paths = uniqueNonEmptyStrings(paths)
	if len(paths) == 0 {
		return "no file paths recorded"
	}
	if len(paths) > 3 {
		return strings.Join(paths[:3], ", ") + fmt.Sprintf(", +%d more", len(paths)-3)
	}
	return strings.Join(paths, ", ")
}

func (s *Service) renderHandoff(spec task.Spec, state task.State, verification task.VerificationReport, criteria task.CriteriaSnapshot, reviewReport task.ReviewReport, completion task.CompletionReport) string {
	plan, executionSteps := s.executionPlanSummary(spec.TaskID)
	sprint := s.buildSprintSnapshot(spec, state)
	var b strings.Builder
	fmt.Fprintf(&b, "# Handoff\n\n")
	fmt.Fprintf(&b, "## Task Summary\n")
	fmt.Fprintf(&b, "- Task ID: %s\n", spec.TaskID)
	if spec.Title != "" {
		fmt.Fprintf(&b, "- Title: %s\n", spec.Title)
	}
	fmt.Fprintf(&b, "- Objective: %s\n", spec.Objective)
	fmt.Fprintf(&b, "- Profile: %s\n", spec.Kind)
	if len(spec.SuccessCriteria) > 0 {
		fmt.Fprintf(&b, "- Success Criteria:\n")
		for _, criterion := range spec.SuccessCriteria {
			fmt.Fprintf(&b, "  - %s: %s\n", criterion.ID, criterion.Statement)
		}
	}
	if len(spec.Constraints) > 0 {
		fmt.Fprintf(&b, "- Constraints:\n")
		for _, constraint := range spec.Constraints {
			fmt.Fprintf(&b, "  - %s\n", constraint)
		}
	}
	fmt.Fprintf(&b, "\n## Repo Bearings\n")
	if !s.renderRepoBearingsMarkdown(&b, spec.TaskID) {
		fmt.Fprintf(&b, "- No repo bearings captured yet.\n")
	}
	fmt.Fprintf(&b, "\n## Status\n")
	fmt.Fprintf(&b, "- Phase: %s\n", state.Phase)
	fmt.Fprintf(&b, "- State: %s\n", state.State)
	if state.CurrentStepID != "" {
		fmt.Fprintf(&b, "- Current Step: %s\n", s.currentStepLabel(spec.TaskID, state.CurrentStepID))
	}
	if plan.Revision > 0 {
		fmt.Fprintf(&b, "- Plan Revision: %d\n", plan.Revision)
	}
	if plan.LastMutationRef != "" {
		fmt.Fprintf(&b, "- Plan Mutation Ref: %s\n", plan.LastMutationRef)
	}
	if plan.CurrentExecutionStepID != "" {
		fmt.Fprintf(&b, "- Current Execution Step: %s\n", s.currentStepLabel(spec.TaskID, plan.CurrentExecutionStepID))
	}
	if plan.CurrentSystemStepID != "" {
		fmt.Fprintf(&b, "- Current Gate: %s\n", s.currentStepLabel(spec.TaskID, plan.CurrentSystemStepID))
	}
	if len(plan.ReadyExecutionStepIDs) > 0 {
		fmt.Fprintf(&b, "- Ready Execution Steps: %s\n", strings.Join(s.executionStepLabels(spec.TaskID, plan.ReadyExecutionStepIDs), ", "))
	}
	if len(plan.BlockedExecutionStepIDs) > 0 {
		fmt.Fprintf(&b, "- Blocked Execution Steps: %s\n", strings.Join(s.executionStepLabels(spec.TaskID, plan.BlockedExecutionStepIDs), ", "))
	}
	if state.StatusReasonCode != "" {
		fmt.Fprintf(&b, "- Status Reason: %s\n", state.StatusReasonCode)
	}
	if state.StatusDetailRef != "" {
		fmt.Fprintf(&b, "- Status Detail Ref: %s\n", state.StatusDetailRef)
	}
	fmt.Fprintf(&b, "- Verification: %s\n", verification.Status)
	fmt.Fprintf(&b, "- Review: %s\n", reviewReport.Status)
	fmt.Fprintf(&b, "- Completion: %s\n", completion.Status)
	fmt.Fprintf(&b, "\n## Current Sprint\n")
	s.renderSprintMarkdown(&b, sprint)
	fmt.Fprintf(&b, "\n## Project Focus\n")
	s.renderProjectFocusMarkdown(&b, sprint.ProjectFocus)
	s.renderMissionMarkdown(&b, spec.TaskID)
	fmt.Fprintf(&b, "\n## Evidence\n")
	fmt.Fprintf(&b, "- baseline.json\n")
	fmt.Fprintf(&b, "- sprint/latest.json\n")
	if sprint.PrimaryCriterionID != "" {
		fmt.Fprintf(&b, "  - primary criterion: %s %s\n", sprint.PrimaryCriterionID, sprint.PrimaryCriterionStatement)
	}
	if sprint.ProjectFocus != nil {
		for _, ref := range sprint.ProjectFocus.Refs {
			fmt.Fprintf(&b, "- %s\n", ref)
		}
	}
	for _, signal := range sprint.CompletionSignals {
		fmt.Fprintf(&b, "  - completion signal: %s\n", signal)
	}
	fmt.Fprintf(&b, "- verification/latest.json\n")
	for _, check := range verification.Checks {
		fmt.Fprintf(&b, "  - %s: %s\n", check.Name, check.Summary)
	}
	fmt.Fprintf(&b, "- reviews/latest.json\n")
	fmt.Fprintf(&b, "  - %s\n", reviewReport.Summary)
	fmt.Fprintf(&b, "- criteria/latest.json\n")
	if criteria.CurrentCriterionID != "" {
		fmt.Fprintf(&b, "  - current focus: %s %s\n", criteria.CurrentCriterionID, criteria.CurrentCriterionStatement)
	}
	for _, criterion := range spec.SuccessCriteria {
		item := criterionStatusForID(criteria, criterion.ID)
		state := "open"
		if item.Passes {
			state = "passing"
		}
		fmt.Fprintf(&b, "  - %s: %s (%s)\n", criterion.ID, criterion.Statement, state)
		for _, ref := range item.EvidenceRefs {
			fmt.Fprintf(&b, "    - evidence: %s\n", ref)
		}
	}
	fmt.Fprintf(&b, "- completion/latest.json\n")
	fmt.Fprintf(&b, "  - %s\n", completion.Summary)
	fmt.Fprintf(&b, "\n## Execution Plan\n")
	if len(executionSteps) == 0 {
		fmt.Fprintf(&b, "- No mutable execution plan has been recorded yet.\n")
	} else {
		s.renderExecutionPlanMarkdown(&b, executionSteps)
	}
	fmt.Fprintf(&b, "\n## Changed Files Or Touched Areas\n")
	if edits, err := s.Store.ReadWorkspaceEdits(spec.TaskID); err == nil && len(edits) > 0 {
		seen := make(map[string]struct{})
		for _, edit := range edits {
			for _, change := range edit.FileChanges {
				if _, ok := seen[change.Path]; ok {
					continue
				}
				seen[change.Path] = struct{}{}
				fmt.Fprintf(&b, "- %s (%s)\n", change.Path, change.Action)
			}
		}
		if len(seen) == 0 {
			fmt.Fprintf(&b, "- Workspace edit artifacts exist, but no file mutation was applied.\n")
		}
	} else {
		baseline, err := s.Store.LoadBaseline(spec.TaskID)
		switch {
		case err == nil && len(baseline.RepoTruthRefs) > 0:
			for _, ref := range baseline.RepoTruthRefs {
				fmt.Fprintf(&b, "- Observed repo truth ref: %s\n", ref)
			}
		default:
			fmt.Fprintf(&b, "- No repo truth refs were captured beyond task-local artifacts.\n")
		}
		fmt.Fprintf(&b, "- No workspace edit records have been captured for this task yet.\n")
	}
	s.renderQualityDiagnosticsMarkdown(&b, spec.TaskID)
	fmt.Fprintf(&b, "\n## Open Risks\n")
	switch state.State {
	case task.StateDone:
		fmt.Fprintf(&b, "- No active runtime blocker remains. Manual inspection beyond the current verifier may still be required.\n")
	case task.StateFailed:
		fmt.Fprintf(&b, "- Runtime is failed: %s.\n", completion.Summary)
	case task.StateBlocked:
		fmt.Fprintf(&b, "- Task is blocked: %s.\n", completion.Summary)
	case task.StateWaiting:
		fmt.Fprintf(&b, "- Task is waiting on a watch and will require a future wake-up.\n")
	default:
		fmt.Fprintf(&b, "- Task is still active and may require another execution cycle.\n")
	}
	fmt.Fprintf(&b, "\n## Resume Instructions\n")
	s.renderRestoreCluesMarkdown(&b, s.statusRestoreClues(spec.TaskID, state.LastCheckpointRef))
	fmt.Fprintf(&b, "%s\n", s.deriveNextStep(spec, state))
	return b.String()
}

func (s *Service) renderRestoreCluesMarkdown(b *strings.Builder, clues []task.RestoreClue) {
	if len(clues) == 0 {
		return
	}
	fmt.Fprintf(b, "- Restore Clues:\n")
	for _, clue := range clues {
		fmt.Fprintf(b, "  - %s: %s\n", clue.Ref, clue.Summary)
		if clue.Git != nil && len(clue.Git.ChangedPaths) > 0 {
			fmt.Fprintf(b, "    - changed paths: %s\n", strings.Join(clue.Git.ChangedPaths, ", "))
		}
		for _, hint := range clue.CommandHints {
			if len(hint.Command) == 0 {
				continue
			}
			fmt.Fprintf(b, "    - command hint: %s (%s)\n", strings.Join(hint.Command, " "), hint.Reason)
		}
	}
}

func newEvent(taskID string, state task.State, typ, summary string, refs []string) task.Event {
	return task.Event{
		SchemaVersion: task.SchemaVersion,
		EventID:       task.NewID("EVT"),
		TaskID:        taskID,
		TS:            task.Now(),
		Phase:         state.Phase,
		State:         state.State,
		Type:          typ,
		Summary:       summary,
		Refs:          refs,
	}
}

func markStep(plan task.Plan, stepID, status string) task.Plan {
	for i := range plan.Steps {
		if plan.Steps[i].ID == stepID {
			plan.Steps[i].Status = status
		}
	}
	plan.UpdatedAt = task.Now()
	return plan
}

func (s *Service) nextCheckpoint(ctx context.Context, spec task.Spec, state task.State) task.Checkpoint {
	cp := task.Checkpoint{
		SchemaVersion:     task.SchemaVersion,
		TaskID:            spec.TaskID,
		CapturedAt:        task.Now(),
		Phase:             state.Phase,
		State:             state.State,
		LastEventRef:      state.LastEventRef,
		WorkspaceSnapshot: verify.CaptureWorkspaceSnapshot(ctx, spec.WorkspaceRoot),
	}
	latest, err := s.Store.LoadLatestCheckpoint(spec.TaskID)
	if err != nil {
		cp.CheckpointID = "0002"
		return cp
	}
	n := extractCheckpointNumber(latest.CheckpointID) + 1
	cp.CheckpointID = fmt.Sprintf("%04d", n)
	return cp
}

func extractCheckpointNumber(id string) int {
	id = strings.TrimSuffix(id, ".json")
	var n int
	_, _ = fmt.Sscanf(id, "%d", &n)
	if n == 0 {
		return 1
	}
	return n
}

func (s *Service) acquireLease(path string) error {
	if err := s.Store.EnsureWorkspaceLayout(); err != nil {
		return err
	}
	if err := ensureDir(path); err != nil {
		return err
	}
	f, err := filepath.Abs(path)
	if err == nil {
		path = f
	}
	file, err := createExclusive(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func (s *Service) releaseLease(path string) error {
	return removeIfExists(path)
}
