package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sync"
	"time"

	"aegis-agent/internal/session"
)

// PreparedApproval is an admitted, durable continuation whose provider has not
// started. It is single-use and must be run or aborted by the preparing Runner.
// Approval files, replay facts, and run claim are coordinated writes, not a
// multi-file atomic transaction: each failed write is reported synchronously.
type PreparedApproval struct {
	mu             sync.Mutex
	runner         *Runner
	meta           session.SessionMetadata
	state          session.State
	originalState  session.State
	req            ContinueRequest
	releaseRunSlot func()
	preparation    *approvalPreparationRecord
	consumed       bool
}

func (p *PreparedApproval) SessionID() string {
	if p == nil {
		return ""
	}
	return p.meta.ID
}

func (p *PreparedApproval) consume(r *Runner) error {
	if p == nil {
		return errors.New("prepared approval is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runner != r {
		return errors.New("prepared approval belongs to a different runner")
	}
	if p.consumed {
		return errors.New("prepared approval has already been run or aborted")
	}
	p.consumed = true
	return nil
}

// PrepareApprovalContinue compares the reviewed scope before claiming the run,
// and persists all approval/replay preparation before returning. No provider is
// invoked here. The Store callback owns the cross-process approval coordination
// lock and never holds Store.mu while invoking another public Store method.
func (r *Runner) PrepareApprovalContinue(ctx context.Context, req ContinueRequest) (*PreparedApproval, error) {
	if !req.ApprovePlan {
		return nil, errors.New("approval preparation requires approve_plan")
	}
	if req.ApprovalTarget != nil {
		captured := *req.ApprovalTarget
		req.ApprovalTarget = &captured
	}
	var prepared *PreparedApproval
	var releaseRunSlot func()
	err := r.store.WithApprovalLock(req.SessionID, func(scoped *session.Store) error {
		prep := r.newApprovalPreparationRunner(scoped)
		// Run request validation before even the in-memory run slot; stale/invalid
		// requests keep the original recoverable session and produce no run facts.
		if err := prep.preflightPlanModeControl(req.SessionID, req); err != nil {
			return err
		}
		if err := prep.recoverApprovalPreparation(req.SessionID); err != nil {
			return err
		}
		originalState, err := scoped.LoadState(req.SessionID)
		if err != nil {
			return err
		}
		meta, err := scoped.LoadMetadata(req.SessionID)
		if err != nil {
			return err
		}
		if err := ValidateContinueTarget(meta, originalState); err != nil {
			return err
		}
		releaseRunSlot, err = r.acquireRunSlot(req.SessionID)
		if err != nil {
			return err
		}
		snapshot, err := scoped.LoadApprovalSnapshot(req.SessionID)
		if err != nil {
			return err
		}
		identity, _ := session.ProcessIdentity(os.Getpid())
		prep.approvalPreparation = &approvalPreparationRecord{SchemaVersion: 1, SessionID: req.SessionID, Target: *req.ApprovalTarget, Snapshot: snapshot, OriginalState: originalState, ClaimUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), OwnerPID: os.Getpid(), OwnerIdentity: identity}
		if err := prep.recordApprovalPreparation("validated", originalState); err != nil {
			return err
		}
		_, err = prep.continueWithPreparation(ctx, req, &prepared)
		if err != nil {
			return err
		}
		if prepared == nil {
			return errors.New("approval preparation produced no continuation")
		}
		prepared.runner = r
		prepared.originalState = originalState
		prepared.releaseRunSlot = releaseRunSlot
		// Capture a value, not a caller-owned mutable pointer.
		target := *req.ApprovalTarget
		prepared.req.ApprovalTarget = &target
		return nil
	})
	if err != nil {
		if releaseRunSlot != nil {
			releaseRunSlot()
		}
		return nil, err
	}
	return prepared, nil
}

// newApprovalPreparationRunner deliberately constructs a new runner and engine;
// neither Runner nor Store mutexes are copied or temporarily replaced. The
// scoped Store is discarded at the callback boundary; execution uses r.store.
func (r *Runner) newApprovalPreparationRunner(scoped *session.Store) *Runner {
	prep := &Runner{
		cfg: r.cfg, store: scoped, bus: r.bus, control: r.control,
		planInputWaiters:  map[string]chan planInputResponse{},
		planInputHandlers: map[string]PlanInputHandler{},
	}
	prep.engine = NewEngine(r.cfg, scoped, r.bus, r.control)
	prep.engine.beforeAppendEvent = r.engine.beforeAppendEvent
	prep.engine.SetRunner(prep)
	prep.approvalRunClaim = r.approvalRunClaim
	return prep
}

// AbortPreparedApproval releases an admitted run when the adapter cannot create
// its handle. Approval facts remain durable and retryable; state is restored to
// its pre-claim recovery point, and the abort records which revision was prepared.
func (r *Runner) AbortPreparedApproval(p *PreparedApproval, cause error) error {
	if err := p.consume(r); err != nil {
		return err
	}
	defer p.releaseRunSlot()
	return r.store.WithApprovalLock(p.meta.ID, func(scoped *session.Store) error {
		if err := validateCurrentPreparedClaim(scoped, p); err != nil {
			return err
		}
		committed, err := swapPreparedApprovalState(scoped, p.meta.ID, p.state, func(session.State) session.State {
			return p.originalState
		})
		if err != nil {
			return fmt.Errorf("restore run claim after approval prepare abort: %w", err)
		}
		data := map[string]any{"plan_mode_id": p.req.ApprovalTarget.PlanModeID, "approved_revision": p.req.ApprovalTarget.ExpectedRevision, "resumed_from": p.originalState.Status}
		if cause != nil {
			data["error"] = cause.Error()
		}
		prep := r.newApprovalPreparationRunner(scoped)
		prep.approvalPreparation = p.preparation
		if err := prep.recordApprovalPreparation("aborted", committed); err != nil {
			return err
		}
		if err := prep.appendEvent(p.meta.ID, "planmode.approval_prepare_aborted", "prepare", data); err != nil {
			return fmt.Errorf("record approval prepare abort: %w", err)
		}
		return nil
	})
}

// RunPreparedApproval uses the root Store and rechecks the prepared scope before
// active-run registration. Every provider turn also consumes one coherent
// approval snapshot and refuses a changed scope without approving newer content.
func (r *Runner) RunPreparedApproval(ctx context.Context, p *PreparedApproval) (RunResult, error) {
	if err := p.consume(r); err != nil {
		return RunResult{}, err
	}
	defer p.releaseRunSlot()
	ctx = context.WithValue(ctx, approvalExecutionContextKey{}, approvalExecutionTarget{sessionID: p.meta.ID, target: *p.req.ApprovalTarget})
	var reviewResult RunResult
	var targetChanged bool
	err := r.store.WithApprovalLock(p.meta.ID, func(scoped *session.Store) error {
		if err := validateCurrentPreparedClaim(scoped, p); err != nil {
			return err
		}
		current, err := scoped.LoadState(p.meta.ID)
		if err != nil {
			return err
		}
		if !samePreparedRun(p.state, current) {
			return fmt.Errorf("%w: prepared run generation changed", session.ErrApprovalConflict)
		}
		if targetErr := r.engine.newApprovalScopedEngine(scoped).checkApprovalExecutionTarget(ctx, p.meta.ID); targetErr != nil {
			pending, err := swapPreparedApprovalState(scoped, p.meta.ID, p.state, func(current session.State) session.State {
				current.Status = session.StatusAwaitingInput
				current.Phase = "plan_approval"
				current.IdleReason = "approval_content_changed"
				current.LastError = targetErr.Error()
				return current
			})
			if err != nil {
				return err
			}
			targetChanged = true
			reviewResult = RunResult{SessionID: p.meta.ID, Status: pending.Status, LastError: targetErr.Error()}
			prep := r.newApprovalPreparationRunner(scoped)
			prep.approvalPreparation = p.preparation
			prep.approvalPreparation.LastError = targetErr.Error()
			var recoveryErrs []error
			if err := prep.recordApprovalPreparation("review_required", pending); err != nil {
				recoveryErrs = append(recoveryErrs, err)
			}
			if err := prep.appendEvent(p.meta.ID, "planmode.approval_review_required", pending.Phase, map[string]any{"reason": "approval_content_changed", "error": targetErr.Error()}); err != nil {
				recoveryErrs = append(recoveryErrs, err)
			}
			return errors.Join(append([]error{targetErr}, recoveryErrs...)...)
		}
		// CAS marks execution intent before registering an active provider.
		// An unrelated state writer cannot replace a newer run generation.
		committed, err := swapPreparedApprovalState(scoped, p.meta.ID, p.state, func(current session.State) session.State {
			return current
		})
		if err != nil {
			return err
		}
		p.state = committed
		prep := r.newApprovalPreparationRunner(scoped)
		prep.approvalPreparation = p.preparation
		return prep.recordApprovalPreparation("executing", p.state)
	})
	if err != nil {
		if targetChanged {
			return reviewResult, err
		}
		// Generation conflicts leave the currently owned state untouched.
		if errors.Is(err, session.ErrApprovalConflict) {
			return RunResult{SessionID: p.meta.ID}, err
		}
		return r.failBeforeRun(p.meta.ID, p.state, "prepare", err)
	}
	releaseActiveRegistration := registerActiveSessionRunner(r.store, p.meta.ID, r)
	defer releaseActiveRegistration()
	if err := r.notifySessionActive(p.meta); err != nil {
		return r.failBeforeRun(p.meta.ID, p.state, "prepare", err)
	}
	result, err := r.runExisting(ctx, p.meta, p.state, p.req.SystemOverride, p.req.PlanInputHandler)
	r.notifySessionInactive(p.meta, result, err)
	if err != nil && result.SessionID == "" {
		result, err = r.failBeforeRun(p.meta.ID, p.state, "prepare", err)
	}
	if journalErr := r.recordPreparedApprovalPhase(p, "settled", err); journalErr != nil {
		err = errors.Join(err, journalErr)
	}
	return result, err
}

func approvalRevisionFromData(data map[string]any) string {
	revision, _ := data["approved_revision"].(string)
	return revision
}

func (r *Runner) appendApprovalUserMessageOnce(ctx context.Context, meta session.SessionMetadata, text string, extra map[string]any) error {
	messages, err := r.store.LoadMessages(meta.ID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, msg := range messages {
		if msg.Role != "user" || msg.Meta["source"] != "planmode_approval" || msg.Meta["plan_mode_id"] != extra["plan_mode_id"] || intFromEventData(msg.Meta, "plan_version") != intFromEventData(extra, "plan_version") || approvalRevisionFromData(msg.Meta) != approvalRevisionFromData(extra) {
			continue
		}
		// The matching message is already durable; user.message append itself rolls
		// back on event failure, so this is a completed replay fact on retry.
		return nil
	}
	return r.appendUserMessage(ctx, meta, "prepare", text, extra)
}

type approvalExecutionContextKey struct{}
type approvalExecutionTarget struct {
	sessionID string
	target    session.ApprovalTarget
}

func approvalExecutionTargetFromContext(ctx context.Context, sessionID string) *session.ApprovalTarget {
	execution, ok := ctx.Value(approvalExecutionContextKey{}).(approvalExecutionTarget)
	if !ok || execution.sessionID != sessionID {
		return nil
	}
	return &execution.target
}

func (e *Engine) approvalExecutionSnapshot(ctx context.Context, sessionID string) (*session.ApprovalSnapshot, error) {
	target := approvalExecutionTargetFromContext(ctx, sessionID)
	if target == nil {
		return nil, nil
	}
	snapshot, err := e.store.LoadApprovalSnapshot(sessionID)
	if err != nil {
		return nil, err
	}
	if err := session.ValidateApprovalTarget(snapshot, *target); err != nil {
		return nil, err
	}
	if snapshot.PlanMode.ApprovedRevision != target.ExpectedRevision || snapshot.PlanMode.ApprovedVersion != target.PlanVersion || !session.IsPlanModeExecution(snapshot.PlanMode.Status) {
		return nil, fmt.Errorf("%w: prepared approval is no longer approved; reload and review the plan", session.ErrApprovalConflict)
	}
	return &snapshot, nil
}

func (e *Engine) checkApprovalExecutionTarget(ctx context.Context, sessionID string) error {
	_, err := e.approvalExecutionSnapshot(ctx, sessionID)
	return err
}

func (e *Engine) requireApprovalReview(meta session.SessionMetadata, state session.State, cause error) (RunResult, error) {
	state.Status = session.StatusAwaitingInput
	state.Phase = "plan_approval"
	state.IdleReason = "approval_content_changed"
	state.LastError = cause.Error()
	if err := e.store.SaveState(meta.ID, state); err != nil {
		return RunResult{}, fmt.Errorf("save approval review recovery after %v: %w", cause, err)
	}
	if err := e.appendEvent(meta.ID, "planmode.approval_review_required", state.Phase, map[string]any{"reason": "approval_content_changed", "error": cause.Error()}); err != nil {
		return RunResult{SessionID: meta.ID, Status: state.Status, LastError: cause.Error()}, fmt.Errorf("record approval review recovery after %v: %w", cause, err)
	}
	return RunResult{SessionID: meta.ID, Status: state.Status, LastError: cause.Error()}, cause
}

const approvalPreparationArtifact = "approval-preparation.json"

// The journal identifies the exact reviewed scope and last completed prepare
// phase. A rename makes each journal update atomic; other session files remain
// separate durable facts and may have advanced past this phase after a crash.
type approvalPreparationRecord struct {
	SchemaVersion  int                      `json:"schema_version"`
	SessionID      string                   `json:"session_id"`
	Target         session.ApprovalTarget   `json:"target"`
	Snapshot       session.ApprovalSnapshot `json:"snapshot"`
	OriginalState  session.State            `json:"original_state"`
	PreparedState  session.State            `json:"prepared_state"`
	Phase          string                   `json:"phase"`
	CompletedPhase string                   `json:"completed_phase,omitempty"`
	LastError      string                   `json:"last_error,omitempty"`
	ClaimUpdatedAt string                   `json:"claim_updated_at,omitempty"`
	OwnerPID       int                      `json:"owner_pid"`
	OwnerIdentity  string                   `json:"owner_identity,omitempty"`
	UpdatedAt      string                   `json:"updated_at"`
}

func (r *Runner) recordApprovalPreparation(phase string, state session.State) error {
	record := r.approvalPreparation
	if record == nil {
		return nil
	}
	if phase != "prepare_failed" {
		record.CompletedPhase = phase
	}
	record.Phase = phase
	record.PreparedState = state
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := r.store.WriteArtifact(record.SessionID, approvalPreparationArtifact, record); err != nil {
		return fmt.Errorf("record approval preparation %s: %w", phase, err)
	}
	return nil
}

func (r *Runner) recordPreparedApprovalPhase(p *PreparedApproval, phase string, cause error) error {
	return r.store.WithApprovalLock(p.meta.ID, func(scoped *session.Store) error {
		if err := validateCurrentPreparedClaim(scoped, p); err != nil {
			return err
		}
		prep := r.newApprovalPreparationRunner(scoped)
		prep.approvalPreparation = p.preparation
		state, err := scoped.LoadState(p.meta.ID)
		if err != nil {
			return err
		}
		if p.state.RunGeneration == "" || state.RunGeneration != p.state.RunGeneration {
			return fmt.Errorf("%w: approval settlement belongs to another run generation", session.ErrApprovalConflict)
		}
		if cause != nil {
			prep.approvalPreparation.LastError = cause.Error()
		}
		return prep.recordApprovalPreparation(phase, state)
	})
}

// A write error may occur after state.json was published. Compensate only a
// claim whose precommitted generation still owns the durable preparation state.
func (r *Runner) restoreApprovalClaimAfterError(sessionID string, cause error) error {
	record := r.approvalPreparation
	record.LastError = cause.Error()
	current, err := r.store.LoadState(sessionID)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("inspect failed approval run claim: %w", err))
	}
	if approvalPreparationOwnsState(*record, current) == nil {
		committed, saved, err := r.store.SwapStateIfCurrent(sessionID, current, record.OriginalState)
		if err != nil {
			return errors.Join(cause, fmt.Errorf("restore failed approval run claim: %w", err))
		}
		if !saved {
			return errors.Join(cause, fmt.Errorf("%w: failed approval claim generation changed", session.ErrApprovalConflict))
		}
		current = committed
	} else if !reflect.DeepEqual(current, record.OriginalState) {
		return errors.Join(cause, fmt.Errorf("%w: cannot restore failed approval claim over another state generation", session.ErrApprovalConflict))
	}
	return errors.Join(cause, r.recordApprovalPreparation("prepare_failed", current))
}

// Queue counts, their timestamp, and learned skill names may change while a
// prepared claim waits for its adapter. They do not replace the durable run
// identity or advance the engine. Every other prepared-state field must match.
func samePreparedRun(expected, current session.State) bool {
	if expected.RunGeneration == "" || current.RunGeneration != expected.RunGeneration || expected.Status != session.StatusRunning || current.Status != session.StatusRunning || expected.Phase != "prepare" || current.Phase != "prepare" || current.Turn != expected.Turn {
		return false
	}
	current.UpdatedAt = expected.UpdatedAt
	current.PendingSteerCount = expected.PendingSteerCount
	current.LoadedSkills = expected.LoadedSkills
	return reflect.DeepEqual(expected, current)
}

// The approval lock keeps the reviewed scope stable while state observations
// can still change independently. Retry a lost full-state CAS only after proving
// the same run is still in its unadvanced preparation state. Build each write
// from that fresh observation; never ignore fields in the Store's generic CAS.
func swapPreparedApprovalState(store *session.Store, sessionID string, expected session.State, next func(session.State) session.State) (session.State, error) {
	for {
		current, err := store.LoadState(sessionID)
		if err != nil {
			return session.State{}, err
		}
		if !samePreparedRun(expected, current) {
			return session.State{}, fmt.Errorf("%w: prepared run generation or state changed", session.ErrApprovalConflict)
		}
		committed, saved, err := store.SwapStateIfCurrent(sessionID, current, next(current))
		if err != nil {
			return session.State{}, err
		}
		if saved {
			return committed, nil
		}
	}
}

func approvalPreparationOwnsState(record approvalPreparationRecord, current session.State) error {
	expected := record.PreparedState
	if record.Phase == "claim_pending" {
		expected = record.OriginalState
		expected.Status = session.StatusRunning
		expected.Phase = "prepare"
		expected.PauseReason = ""
		expected.ProviderAutoResumeCount = 0
		expected.RunGeneration = record.ClaimUpdatedAt
	}
	if record.ClaimUpdatedAt == "" || expected.RunGeneration != record.ClaimUpdatedAt || !samePreparedRun(expected, current) {
		return fmt.Errorf("%w: approval preparation cannot prove ownership of the current run; explicitly stop the lost run before ordinary continue", session.ErrApprovalConflict)
	}
	return nil
}

// Recovery is allowed only for a dead process owner. A live process may be
// between synchronous admission and attaching its handle; reclaiming it here
// would let a second Store start the same session. Provider effects already in
// messages are retained, and normal dangling-call recovery runs on continuation.
func (r *Runner) recoverApprovalPreparation(sessionID string) error {
	state, err := r.store.LoadState(sessionID)
	if err != nil || state.Status != session.StatusRunning {
		return err
	}
	observedState := state
	var record approvalPreparationRecord
	if err := r.store.ReadArtifact(sessionID, approvalPreparationArtifact, &record); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read approval preparation recovery: %w", err)
	}
	if err := validateApprovalPreparationRecord(sessionID, record); err != nil {
		return err
	}
	if record.Phase == "aborted" || record.Phase == "settled" || record.Phase == "recovered" || record.Phase == "review_required" {
		return nil
	}
	if session.ProcessOwnerAlive(record.OwnerPID, record.OwnerIdentity) {
		return nil
	}
	// A dead owner is not evidence that the current running state belongs to
	// this journal: an ordinary continuation may already own a later claim.
	// claim_pending carries its generation before the claim write; subsequent
	// phases retain the same durable identity and unadvanced prepared state.
	if err := approvalPreparationOwnsState(record, observedState); err != nil {
		return err
	}
	if record.Phase == "executing" {
		state.Status = session.StatusAwaitingInput
		state.Phase = "approval_preparation_recovered"
		state.IdleReason = "approval_owner_lost"
	} else {
		state = record.OriginalState
	}
	saved, err := r.store.SaveStateIfCurrent(sessionID, observedState, state)
	if err != nil {
		return fmt.Errorf("restore orphan approval run claim: %w", err)
	}
	if !saved {
		return fmt.Errorf("%w: orphaned preparation run generation changed", session.ErrApprovalConflict)
	}
	previousPhase := record.CompletedPhase
	if previousPhase == "" {
		previousPhase = record.Phase
	}
	r.approvalPreparation = &record
	if err := r.recordApprovalPreparation("recovered", state); err != nil {
		return err
	}
	r.approvalPreparation = nil
	return r.appendEvent(sessionID, "planmode.approval_prepare_recovered", "prepare", map[string]any{"plan_mode_id": record.Target.PlanModeID, "approved_revision": record.Target.ExpectedRevision, "previous_phase": previousPhase, "owner_pid": record.OwnerPID})
}

// ApprovalPreparationOwnerAlive protects the interval between durable runtime
// preparation and attaching an adapter handle. A corrupt journal is an error,
// never evidence that its still-running claim can safely be reclaimed.
func ApprovalPreparationOwnerAlive(store *session.Store, sessionID string) (bool, error) {
	var record approvalPreparationRecord
	if err := store.ReadArtifact(sessionID, approvalPreparationArtifact, &record); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read approval preparation owner: %w", err)
	}
	if err := validateApprovalPreparationRecord(sessionID, record); err != nil {
		return false, err
	}
	switch record.Phase {
	case "settled", "aborted", "recovered", "review_required":
		return false, nil
	}
	return session.ProcessOwnerAlive(record.OwnerPID, record.OwnerIdentity), nil
}

// CanReconcileApprovalPreparation allows automatic reconciliation only when no
// current preparation owns the run, or its dead owner's journal proves the run
// generation. ErrApprovalConflict means ambiguous ownership; callers may retain
// an explicit stop path, but must not silently pause and resume that generation.
// A malformed journal is a different error and remains closed to reconciliation.
func CanReconcileApprovalPreparation(store *session.Store, sessionID string) (canReconcile bool, err error) {
	err = store.WithApprovalLock(sessionID, func(scoped *session.Store) error {
		var record approvalPreparationRecord
		if err := scoped.ReadArtifact(sessionID, approvalPreparationArtifact, &record); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				canReconcile = true
				return nil
			}
			return fmt.Errorf("read approval preparation reconciliation: %w", err)
		}
		if err := validateApprovalPreparationRecord(sessionID, record); err != nil {
			return err
		}
		switch record.Phase {
		case "settled", "aborted", "recovered", "review_required":
			canReconcile = true
			return nil
		}
		if session.ProcessOwnerAlive(record.OwnerPID, record.OwnerIdentity) {
			return nil
		}
		state, err := scoped.LoadState(sessionID)
		if err != nil {
			return err
		}
		if state.Status == session.StatusRunning {
			if err := approvalPreparationOwnsState(record, state); err != nil {
				return err
			}
		}
		canReconcile = true
		return nil
	})
	return canReconcile, err
}

func validateApprovalPreparationRecord(sessionID string, record approvalPreparationRecord) error {
	if record.SchemaVersion != 1 || record.SessionID != sessionID || record.OwnerPID <= 0 || record.Target.PlanModeID == "" || record.Target.PlanVersion <= 0 || record.Target.ExpectedRevision == "" || record.Snapshot.Target() != record.Target {
		return errors.New("invalid approval preparation recovery journal")
	}
	if record.ClaimUpdatedAt != "" {
		stamp, err := time.Parse(time.RFC3339Nano, record.ClaimUpdatedAt)
		if err != nil || stamp.IsZero() || stamp.UTC().Format(time.RFC3339Nano) != record.ClaimUpdatedAt {
			return errors.New("invalid approval preparation claim generation")
		}
	}
	switch record.Phase {
	case "validated", "claim_pending", "run_claimed", "plan_executing", "mission_approved", "replay_recorded", "prepared", "prepare_failed", "executing", "settled", "aborted", "recovered", "review_required":
		return nil
	default:
		return fmt.Errorf("invalid approval preparation phase: %s", record.Phase)
	}
}

func validateCurrentPreparedClaim(store *session.Store, p *PreparedApproval) error {
	var current approvalPreparationRecord
	if err := store.ReadArtifact(p.meta.ID, approvalPreparationArtifact, &current); err != nil {
		return fmt.Errorf("read prepared claim identity: %w", err)
	}
	if err := validateApprovalPreparationRecord(p.meta.ID, current); err != nil {
		return err
	}
	if p.preparation == nil || current.ClaimUpdatedAt == "" || current.ClaimUpdatedAt != p.preparation.ClaimUpdatedAt || current.Target != p.preparation.Target {
		return fmt.Errorf("%w: approval preparation belongs to another run claim", session.ErrApprovalConflict)
	}
	return nil
}
