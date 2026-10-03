package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"time"

	"aegis-agent/internal/events"
	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/session"
)

// ApprovalPreparationResult separates an executable admission from a durable
// receipt replay. A receipt is never an instruction to start another run.
type ApprovalPreparationResult struct {
	Lookup           session.ApprovalReceiptLookup `json:"lookup"`
	Prepared         *PreparedApproval             `json:"-"`
	Replay           bool                          `json:"replay"`
	RecoveryRequired bool                          `json:"recovery_required"`
	RecoveryReason   string                        `json:"recovery_reason,omitempty"`
}

// ApprovalResult accompanies Continue without confusing a prior receipt with
// the session's current state, which may belong to a later ordinary run.
type ApprovalResult struct {
	Lookup           session.ApprovalReceiptLookup `json:"lookup"`
	Replay           bool                          `json:"replay"`
	RecoveryRequired bool                          `json:"recovery_required"`
	RecoveryReason   string                        `json:"recovery_reason,omitempty"`
}

func (result ApprovalPreparationResult) sidecar() *ApprovalResult {
	return &ApprovalResult{Lookup: result.Lookup, Replay: result.Replay, RecoveryRequired: result.RecoveryRequired, RecoveryReason: result.RecoveryReason}
}

// ApprovalPreparationOutcomeError lets old adapters inspect an explicit replay
// or recovery result. The compatibility wrapper never returns a nil executable.
type ApprovalPreparationOutcomeError struct{ Result ApprovalPreparationResult }

func (e *ApprovalPreparationOutcomeError) Error() string {
	if e.Result.RecoveryRequired {
		return "approval receipt requires explicit recovery: " + e.Result.RecoveryReason
	}
	return "approval request returned its prior receipt; no new execution was admitted"
}

func approvalRequest(req ContinueRequest) (session.ApprovalOperationRequest, ContinueRequest, error) {
	if err := validatePlanModeControlCombination(req); err != nil {
		return session.ApprovalOperationRequest{}, req, err
	}
	if !req.ApprovePlan {
		return session.ApprovalOperationRequest{}, req, errors.New("approval preparation requires approve_plan")
	}
	if req.ApprovalRequestID == "" {
		return session.ApprovalOperationRequest{}, req, session.ErrMissingApprovalRequestID
	}
	if req.ApprovalTarget == nil {
		return session.ApprovalOperationRequest{}, req, session.ErrMissingApprovalTarget
	}
	params, err := session.NormalizeApprovalParameters(session.ApprovalParameters{Target: *req.ApprovalTarget, OverrideCoverage: req.OverrideGoalCoverage, Message: req.Message, Provider: req.Provider, Model: req.Model, ProviderOptions: req.ProviderOptions, SystemOverride: req.SystemOverride})
	if err != nil {
		return session.ApprovalOperationRequest{}, req, err
	}
	target := params.Target
	req.ApprovalTarget = &target
	req.Message = params.Message
	req.Provider = params.Provider
	req.Model = params.Model
	req.ProviderOptions = params.ProviderOptions
	req.SystemOverride = params.SystemOverride
	return session.ApprovalOperationRequest{RequestID: req.ApprovalRequestID, Parameters: params}, req, nil
}

func validateApprovalOperationPayloads(store *session.Store, sessionID string) error {
	receipts, err := store.ListApprovalOperationReceipts(sessionID)
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		if _, err := decodeApprovalRecovery(receipt); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runner) LookupApprovalContinue(req ContinueRequest) (lookup session.ApprovalReceiptLookup, err error) {
	request, _, err := approvalRequest(req)
	if err != nil {
		return lookup, err
	}
	err = r.store.WithApprovalLock(req.SessionID, func(scoped *session.Store) error {
		if err := validateApprovalOperationPayloads(scoped, req.SessionID); err != nil {
			return err
		}
		var err error
		lookup, err = scoped.LookupApprovalReceipt(req.SessionID, request.RequestID, request.Parameters)
		return err
	})
	return lookup, err
}
func (r *Runner) ApprovalReceipt(sessionID, requestID string) (lookup session.ApprovalReceiptLookup, err error) {
	err = r.store.WithApprovalLock(sessionID, func(scoped *session.Store) error {
		if err := validateApprovalOperationPayloads(scoped, sessionID); err != nil {
			return err
		}
		var err error
		lookup, err = scoped.GetApprovalReceipt(sessionID, requestID)
		return err
	})
	return lookup, err
}

// This is the versioned, self-contained recovery portion of a receipt. Actual
// control facts and their stable identities survive loss of a JSONL tail.
type approvalOperationPayload struct {
	SchemaVersion      int                            `json:"schema_version"`
	Preparation        approvalPreparationRecord      `json:"preparation"`
	Metadata           *session.SessionMetadata       `json:"metadata,omitempty"`
	OriginalMessageIDs []string                       `json:"original_message_ids"`
	HookPending        bool                           `json:"hook_pending"`
	ReplayDecision     *approvalReplayDecision        `json:"replay_decision,omitempty"`
	Events             []events.Event                 `json:"events"`
	Messages           []session.Message              `json:"messages"`
	PlanHistory        []session.PlanModeHistoryEntry `json:"plan_history"`
	GoalHistory        []session.GoalHistoryEntry     `json:"goal_history"`
	ApprovedPlan       *session.PlanModeState         `json:"approved_plan,omitempty"`
	ApprovedGoal       *session.SessionGoal           `json:"approved_goal,omitempty"`
	Complete           bool                           `json:"complete"`
}
type approvalReplayDecision struct {
	Suppressed bool             `json:"replay_suppressed"`
	Message    *session.Message `json:"message,omitempty"`
	Event      *events.Event    `json:"event,omitempty"`
}
type approvalOperationContext struct {
	Receipt     session.ApprovalReceipt
	Payload     approvalOperationPayload
	ResumeClaim bool
}

func newApprovalOperationPayload(record approvalPreparationRecord) approvalOperationPayload {
	return approvalOperationPayload{SchemaVersion: 1, Preparation: record, OriginalMessageIDs: []string{}, Events: []events.Event{}, Messages: []session.Message{}, PlanHistory: []session.PlanModeHistoryEntry{}, GoalHistory: []session.GoalHistoryEntry{}}
}
func (op *approvalOperationContext) recovery() (session.ApprovalRecoveryPayload, error) {
	data, err := json.Marshal(op.Payload)
	return session.ApprovalRecoveryPayload{SchemaVersion: 1, RunGeneration: op.Receipt.Recovery.RunGeneration, OwnerPID: op.Payload.Preparation.OwnerPID, OwnerIdentity: op.Payload.Preparation.OwnerIdentity, Data: data}, err
}

func decodeApprovalRecovery(receipt session.ApprovalReceipt) (approvalOperationPayload, error) {
	var payload approvalOperationPayload
	if receipt.Stage == session.ApprovalReceiptRejected {
		return payload, nil
	}
	bad := func(reason string) (approvalOperationPayload, error) {
		return payload, fmt.Errorf("%w: %s", session.ErrApprovalReceiptUnverifiable, reason)
	}
	decoder := json.NewDecoder(bytes.NewReader(receipt.Recovery.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return bad("decode runtime recovery: " + err.Error())
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return bad("trailing runtime recovery data")
	}
	// Require exact field spelling and all mandatory fields, rather than letting
	// encoding/json accept case-folded or absent facts as zero values.
	if err := validateRecoveryFields(receipt.Recovery.Data, reflect.TypeFor[approvalOperationPayload]()); err != nil {
		return bad(err.Error())
	}
	if payload.SchemaVersion != 1 || payload.Preparation.SessionID != receipt.SessionID || payload.Preparation.Target != receipt.Target || payload.Preparation.OwnerPID != receipt.Recovery.OwnerPID || payload.Preparation.OwnerIdentity != receipt.Recovery.OwnerIdentity {
		return bad("contradictory runtime recovery identity")
	}
	if err := validateApprovalPreparationRecord(receipt.SessionID, payload.Preparation); err != nil {
		return bad(err.Error())
	}
	switch payload.Preparation.CompletedPhase {
	case "validated", "claim_pending", "run_claimed", "plan_executing", "mission_approved", "replay_recorded":
		if payload.Complete {
			return bad("complete recovery facts precede completed preparation")
		}
	case "prepared", "executing", "settled", "aborted", "recovered", "review_required":
		if !payload.Complete {
			return bad("completed preparation lacks complete recovery facts")
		}
	default:
		return bad("invalid completed preparation phase")
	}
	if err := session.ValidateApprovalSnapshotRevision(payload.Preparation.Snapshot); err != nil {
		return bad("reviewed recovery snapshot revision: " + err.Error())
	}
	if payload.ApprovedPlan != nil {
		approved := session.ApprovalSnapshot{PlanMode: *payload.ApprovedPlan, Goal: payload.ApprovedGoal, Revision: receipt.Target.ExpectedRevision}
		if err := session.ValidateApprovalSnapshotRevision(approved); err != nil {
			return bad("approved recovery snapshot revision: " + err.Error())
		}
	}
	if receipt.Phase == "admitted" {
		if receipt.Stage != session.ApprovalReceiptAdmitted || payload.Preparation.Phase != "prepared" || payload.Preparation.CompletedPhase != "prepared" || !payload.Complete {
			return bad("contradictory admission marker")
		}
	} else if receipt.Phase != payload.Preparation.Phase {
		return bad("contradictory preparation phase")
	}

	if receipt.Recovery.RunGeneration != "" && payload.Preparation.ClaimUpdatedAt != receipt.Recovery.RunGeneration {
		return bad("contradictory recovery run generation")
	}
	if err := validateApprovalRecoveryStates(receipt, payload.Preparation); err != nil {
		return bad(err.Error())
	}
	if payload.OriginalMessageIDs == nil || payload.Events == nil || payload.Messages == nil || payload.PlanHistory == nil || payload.GoalHistory == nil {
		return bad("missing recovery fact arrays")
	}
	ids := map[string]bool{}
	for _, evt := range payload.Events {
		if evt.SchemaVersion != events.SchemaVersion || evt.SessionID != receipt.SessionID || evt.ID == "" || evt.Type == "" || !validApprovalFactTime(evt.Time) || ids[evt.ID] {
			return bad("invalid recovery event")
		}
		ids[evt.ID] = true
	}
	ids = map[string]bool{}
	for _, msg := range payload.Messages {
		if msg.ID == "" || !validApprovalFactTime(msg.CreatedAt) || ids[msg.ID] {
			return bad("invalid recovery message")
		}
		ids[msg.ID] = true
	}
	if payload.ReplayDecision != nil {
		replay := payload.ReplayDecision
		if payload.HookPending || (replay.Suppressed && (replay.Message != nil || replay.Event != nil)) || (!replay.Suppressed && (replay.Message == nil || replay.Event == nil || replay.Message.Role != "user" || replay.Event.Type != "user.message" || replay.Event.SessionID != receipt.SessionID || approvalRevisionFromData(replay.Message.Meta) != receipt.Target.ExpectedRevision)) {
			return bad("contradictory replay hook decision")
		}
	}
	if replay := payload.ReplayDecision; replay != nil && !replay.Suppressed {
		msg, evt := replay.Message, replay.Event
		if msg.ID == "" || evt.ID == "" || msg.Meta["source"] != "planmode_approval" || msg.Meta["plan_mode_id"] != receipt.Target.PlanModeID || intFromEventData(msg.Meta, "plan_version") != receipt.Target.PlanVersion || evt.Data["text"] != msg.Text || evt.Data["source"] != "planmode_approval" || evt.Data["plan_mode_id"] != receipt.Target.PlanModeID || intFromEventData(evt.Data, "plan_version") != receipt.Target.PlanVersion || approvalRevisionFromData(evt.Data) != receipt.Target.ExpectedRevision {
			return bad("contradictory stable replay identity")
		}
		foundMessage, foundEvent := false, false
		for _, fact := range payload.Messages {
			foundMessage = foundMessage || (fact.ID == msg.ID && equalApprovalFact(fact, *msg))
		}
		for _, fact := range payload.Events {
			foundEvent = foundEvent || (fact.ID == evt.ID && equalApprovalFact(fact, *evt))
		}
		if !foundMessage || !foundEvent {
			return bad("missing stable replay facts")
		}
	}
	if receipt.Stage == session.ApprovalReceiptAdmitted || payload.Complete {
		if !payload.Complete || payload.Metadata == nil || payload.Metadata.ID != receipt.SessionID || payload.ReplayDecision == nil || payload.ApprovedPlan == nil || payload.ApprovedPlan.ApprovedRevision != receipt.Target.ExpectedRevision || payload.ApprovedPlan.ApprovedVersion != receipt.Target.PlanVersion || payload.ApprovedPlan.PlanModeID != receipt.Target.PlanModeID || !session.IsPlanModeExecution(payload.ApprovedPlan.Status) || receipt.Recovery.RunGeneration == "" {
			return bad("incomplete admitted control facts")
		}
		approved, executing, resumed, missionEvent := false, false, false, false
		for _, evt := range payload.Events {
			switch evt.Type {
			case "planmode.plan_approved", "planmode.execution_started", "mission.plan.approved":
				if evt.Data["plan_mode_id"] != receipt.Target.PlanModeID || intFromEventData(evt.Data, "approved_version") != receipt.Target.PlanVersion || approvalRevisionFromData(evt.Data) != receipt.Target.ExpectedRevision {
					return bad("contradictory approval event scope")
				}
				approved = approved || evt.Type == "planmode.plan_approved"
				executing = executing || evt.Type == "planmode.execution_started"
				missionEvent = missionEvent || evt.Type == "mission.plan.approved"
			case "session.resumed":
				resumed = true
			}
		}
		if !approved || !executing || !resumed {
			return bad("missing admitted approval events")
		}
		approvedHistory, executionHistory := false, false
		ids := map[string]bool{}
		for _, row := range payload.PlanHistory {
			if row.SchemaVersion != 1 || row.ID == "" || !validApprovalFactTime(row.CreatedAt) || ids[row.ID] || row.SessionID != receipt.SessionID || row.PlanModeID != receipt.Target.PlanModeID || row.PlanVersion != receipt.Target.PlanVersion || approvalRevisionFromData(row.Data) != receipt.Target.ExpectedRevision {
				return bad("contradictory approval history")
			}
			ids[row.ID] = true
			approvedHistory = approvedHistory || row.Type == "planmode.plan_approved"
			executionHistory = executionHistory || row.Type == "planmode.execution_started"
		}
		if !approvedHistory || !executionHistory {
			return bad("missing admitted approval history")
		}
		if payload.ApprovedPlan.LinkedGoalID != "" && payload.Preparation.Snapshot.Goal != nil {
			if payload.ApprovedGoal == nil || payload.ApprovedGoal.GoalID != payload.ApprovedPlan.LinkedGoalID {
				return bad("missing linked goal snapshot")
			}
		}
		if payload.ApprovedPlan.LinkedGoalID != "" && payload.Preparation.Snapshot.Goal != nil && payload.Preparation.Snapshot.Goal.Mission != nil {
			goal := payload.ApprovedGoal
			if goal == nil || goal.GoalID != payload.ApprovedPlan.LinkedGoalID || goal.Mission == nil || goal.Mission.ApprovedRevision != receipt.Target.ExpectedRevision || session.NormalizeMissionPlanStatus(goal.Mission.PlanStatus) != session.MissionPlanStatusApproved || !missionEvent {
				return bad("missing linked mission approval facts")
			}
			missionHistory := false
			ids = map[string]bool{}
			for _, row := range payload.GoalHistory {
				if row.SchemaVersion != 1 || row.ID == "" || !validApprovalFactTime(row.CreatedAt) || ids[row.ID] || row.SessionID != receipt.SessionID || row.GoalID != goal.GoalID || row.Data["plan_mode_id"] != receipt.Target.PlanModeID || intFromEventData(row.Data, "approved_version") != receipt.Target.PlanVersion || approvalRevisionFromData(row.Data) != receipt.Target.ExpectedRevision {
					return bad("contradictory mission approval history")
				}
				ids[row.ID] = true
				missionHistory = missionHistory || row.Type == "mission.plan.approved"
			}
			if !missionHistory {
				return bad("missing linked mission approval history")
			}
		}
	}
	return payload, nil
}

func validateApprovalRecoveryStates(receipt session.ApprovalReceipt, record approvalPreparationRecord) error {
	for _, state := range []session.State{record.OriginalState, record.PreparedState} {
		if err := session.ValidateApprovalRecoveryState(state); err != nil {
			return fmt.Errorf("invalid captured recovery state: %w", err)
		}
	}
	// New operation records are created only after continue preflight. A known
	// state.json status is insufficient if it could not pass that entrypoint.
	switch record.OriginalState.Status {
	case session.StatusPaused, session.StatusAwaitingInput, session.StatusFailed, session.StatusCompleted:
	default:
		return errors.New("captured original recovery state is not resumable")
	}
	if record.Phase != "prepare_failed" && record.Phase != record.CompletedPhase {
		return errors.New("contradictory completed recovery phase")
	}
	// CAS compensation refreshes these observations without changing the
	// original semantic state. Validate them above, then compare core facts.
	observedOriginal := record.PreparedState
	observedOriginal.UpdatedAt = record.OriginalState.UpdatedAt
	observedOriginal.PendingSteerCount = record.OriginalState.PendingSteerCount
	observedOriginal.LoadedSkills = record.OriginalState.LoadedSkills
	original := reflect.DeepEqual(record.OriginalState, observedOriginal)
	claimed := receipt.Recovery.RunGeneration != "" && record.PreparedState.RunGeneration == receipt.Recovery.RunGeneration
	expectedClaim := record.OriginalState
	expectedClaim.Status, expectedClaim.Phase = session.StatusRunning, "prepare"
	expectedClaim.PauseReason, expectedClaim.ProviderAutoResumeCount = "", 0
	expectedClaim.RunGeneration = record.ClaimUpdatedAt
	prepared := record.PreparedState
	failedOwnClaim := prepared.Status == session.StatusFailed && prepared.LastError != "" && prepared.LastError == record.LastError
	if failedOwnClaim && (record.Phase == "prepare_failed" || record.Phase == "aborted") {
		prepared.Status = session.StatusRunning
		prepared.LastError = record.OriginalState.LastError
	}
	unadvanced := claimed && samePreparedRun(expectedClaim, prepared) && (record.Phase != "prepare_failed" || failedOwnClaim)
	switch record.CompletedPhase {
	case "validated":
		if original && receipt.Recovery.RunGeneration == "" {
			return nil
		}
	case "claim_pending":
		// A resumed preparation checkpoints claim_pending again before reusing
		// its existing claim; its snapshot can be original or already claimed.
		if receipt.Recovery.RunGeneration != "" && (original || unadvanced) {
			return nil
		}
	case "run_claimed", "plan_executing", "mission_approved", "replay_recorded", "prepared", "executing":
		if unadvanced {
			return nil
		}
	case "aborted":
		// Abort after addHandle failure restores original state; an uncertain
		// admission write leaves a failed own claim. Both retain the receipt.
		if receipt.Recovery.RunGeneration != "" && (original || (failedOwnClaim && unadvanced)) {
			return nil
		}
	case "review_required":
		state := record.PreparedState
		if claimed && state.Status == session.StatusAwaitingInput && state.Phase == "plan_approval" && state.Turn == record.OriginalState.Turn && state.IdleReason == "approval_content_changed" && state.LastError != "" && state.LastError == record.LastError {
			return nil
		}
	case "settled", "recovered":
		// Executed/settled runs can advance status, phase and turn. Recovered
		// snapshots may preserve that claim or the original compensation state.
		if record.CompletedPhase == "recovered" && original && receipt.Recovery.RunGeneration != "" {
			return nil
		}
		if claimed {
			return nil
		}
	}
	return errors.New("contradictory captured recovery state or generation")
}

func validateRecoveryFields(data []byte, typ reflect.Type) error {
	if typ == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	if typ.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return nil
		}
		return validateRecoveryFields(data, typ.Elem())
	}
	if typ.Kind() == reflect.Slice {
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, raw := range values {
			if err := validateRecoveryFields(raw, typ.Elem()); err != nil {
				return err
			}
		}
		return nil
	}
	if typ.Kind() != reflect.Struct {
		switch typ.Kind() {
		case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
			if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
				return errors.New("null recovery scalar")
			}
		}
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("recovery object required")
	}
	if typ == reflect.TypeFor[session.SessionGoal]() {
		// SessionGoal's established wire contract emits both provider-time names and
		// historical names. Require them to agree; retain strict checking elsewhere.
		canonical, ok := fields["provider_time_used_seconds"]
		if !ok {
			return errors.New("missing canonical provider time accounting")
		}
		if !bytes.Equal(canonical, fields["time_used_seconds"]) {
			return errors.New("contradictory provider time accounting")
		}
		delete(fields, "provider_time_used_seconds")
		if canonical, ok := fields["provider_time_budget_seconds"]; ok {
			if !bytes.Equal(canonical, fields["time_budget_seconds"]) {
				return errors.New("contradictory provider time budget")
			}
			delete(fields, "provider_time_budget_seconds")
		}
	}
	known := map[string]reflect.StructField{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")
		name := tag[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		known[name] = field
		if _, ok := fields[name]; !ok && !strings.Contains(field.Tag.Get("json"), ",omitempty") {
			return fmt.Errorf("missing recovery field %s", name)
		}
	}
	for name, raw := range fields {
		field, ok := known[name]
		if !ok {
			return fmt.Errorf("unknown recovery field %s", name)
		}
		if err := validateRecoveryFields(raw, field.Type); err != nil {
			return err
		}
	}
	return nil
}

// PrepareApprovalOperation performs receipt lookup before any current-state,
// target or default-configuration decision. Only a newly fsynced admission can
// produce Prepared; existing admitted receipts always return without execution.
func (r *Runner) PrepareApprovalOperation(ctx context.Context, req ContinueRequest) (result ApprovalPreparationResult, err error) {
	request, captured, err := approvalRequest(req)
	if err != nil {
		return result, err
	}
	req = captured
	var release func()
	err = r.store.WithApprovalLock(req.SessionID, func(scoped *session.Store) error {
		if err := validateApprovalOperationPayloads(scoped, req.SessionID); err != nil {
			return err
		}
		lookup, err := scoped.LookupApprovalReceipt(req.SessionID, request.RequestID, request.Parameters)
		if err != nil {
			return err
		}
		result.Lookup = lookup
		var op *approvalOperationContext
		if lookup.Found {
			payload, err := decodeApprovalRecovery(lookup.Receipt)
			if err != nil {
				return err
			}
			if lookup.NeedsAlias {
				if _, err := scoped.BindApprovalRequest(req.SessionID, request, lookup.Receipt.OperationID); err != nil {
					return err
				}
				lookup, err = scoped.GetApprovalReceipt(req.SessionID, request.RequestID)
				if err != nil {
					return err
				}
				result.Lookup = lookup
			}
			result.Replay = true
			if lookup.Receipt.Stage != session.ApprovalReceiptPrepared {
				return nil
			}
			op = &approvalOperationContext{Receipt: lookup.Receipt, Payload: payload}
			if reason := approvalOperationRecoveryReason(scoped, op); reason != "" {
				result.RecoveryRequired = true
				result.RecoveryReason = reason
				return nil
			}
		}
		prep := r.newApprovalPreparationRunner(scoped)
		if op == nil {
			if err := prep.preflightPlanModeControl(req.SessionID, req); err != nil {
				// A valid reviewed target with a policy rejection gets an immutable receipt.
				// Stale target failures cannot be fabricated into a current-target receipt.
				rejected, rejectErr := scoped.RejectApprovalOperation(req.SessionID, request, err.Error())
				if rejectErr == nil {
					result.Lookup, _ = scoped.GetApprovalReceipt(req.SessionID, rejected.OperationID)
				}
				if rejectErr != nil && !errors.Is(rejectErr, session.ErrApprovalConflict) {
					return errors.Join(err, rejectErr)
				}
				return err
			}
			snapshot, err := scoped.LoadApprovalSnapshot(req.SessionID)
			if err != nil {
				return err
			}
			if session.IsPlanModeExecution(snapshot.PlanMode.Status) {
				previous := session.ApprovalTarget{PlanModeID: snapshot.PlanMode.PlanModeID, PlanVersion: snapshot.PlanMode.ApprovedVersion, ExpectedRevision: snapshot.PlanMode.ApprovedRevision}
				receipts, err := scoped.ListApprovalOperationReceipts(req.SessionID)
				if err != nil {
					return err
				}
				known := false
				for _, receipt := range receipts {
					if receipt.Target == previous && (receipt.Stage == session.ApprovalReceiptPrepared || receipt.Stage == session.ApprovalReceiptAdmitted) {
						known = true
						break
					}
				}
				if !known {
					return fmt.Errorf("%w: executing approval has no operation receipt; explicitly stop and use ordinary continue for recovery", session.ErrApprovalReceiptUnverifiable)
				}
			}
			if err := prep.recoverApprovalPreparation(req.SessionID); err != nil {
				return err
			}
			original, err := scoped.LoadState(req.SessionID)
			if err != nil {
				return err
			}
			meta, err := scoped.LoadMetadata(req.SessionID)
			if err != nil {
				return err
			}
			if err := ValidateContinueTarget(meta, original); err != nil {
				return err
			}
			identity, _ := session.ProcessIdentity(os.Getpid())
			record := approvalPreparationRecord{SchemaVersion: 1, SessionID: req.SessionID, Target: *req.ApprovalTarget, Snapshot: snapshot, OriginalState: original, PreparedState: original, Phase: "validated", CompletedPhase: "validated", ClaimUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), OwnerPID: os.Getpid(), OwnerIdentity: identity, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			op = &approvalOperationContext{Payload: newApprovalOperationPayload(record)}
			messages, err := scoped.LoadMessages(req.SessionID)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			for _, msg := range messages {
				op.Payload.OriginalMessageIDs = append(op.Payload.OriginalMessageIDs, msg.ID)
			}
			recovery, err := op.recovery()
			if err != nil {
				return err
			}
			receipt, err := scoped.PrepareApprovalOperation(req.SessionID, request, recovery)
			if err != nil {
				return err
			}
			op.Receipt = receipt
		}
		release, err = r.acquireRunSlot(req.SessionID)
		if err != nil {
			if result.Lookup.Found {
				result.RecoveryRequired = true
				result.RecoveryReason = err.Error()
				return nil
			}
			return err
		}
		prep.approvalOperation = op
		prep.approvalPreparation = &op.Payload.Preparation
		if result.Lookup.Found {
			identity, _ := session.ProcessIdentity(os.Getpid())
			prep.approvalPreparation.OwnerPID = os.Getpid()
			prep.approvalPreparation.OwnerIdentity = identity
			if err := prep.recordApprovalPreparation(op.Payload.Preparation.Phase, op.Payload.Preparation.PreparedState); err != nil {
				return err
			}
			if err := prep.resumeApprovalOperationClaim(req.SessionID); err != nil {
				result.RecoveryRequired = true
				result.RecoveryReason = err.Error()
				return nil
			}
		}
		var prepared *PreparedApproval
		if op.Payload.Complete {
			if err := prep.restoreApprovalOperationFacts(req.SessionID); err != nil {
				return err
			}
			meta := *op.Payload.Metadata
			state, err := scoped.LoadState(req.SessionID)
			if err != nil {
				return err
			}
			if err := prep.recordApprovalPreparation("prepared", state); err != nil {
				return err
			}
			prepared = &PreparedApproval{meta: meta, state: state, req: req, preparation: prep.approvalPreparation, operation: op}
		} else {
			if _, err := prep.continueWithPreparation(ctx, req, &prepared); err != nil {
				result.Lookup, _ = scoped.GetApprovalReceipt(req.SessionID, request.RequestID)
				return err
			}
		}
		if prepared == nil {
			return errors.New("approval preparation produced no executable")
		}
		var admitted session.ApprovalReceipt
		if r.approvalAdmit != nil {
			admitted, err = r.approvalAdmit(scoped, req.SessionID, op.Receipt.OperationID, op.Receipt.Recovery.RunGeneration)
		} else {
			admitted, err = scoped.AdmitApprovalOperation(req.SessionID, op.Receipt.OperationID, op.Receipt.Recovery.RunGeneration)
		}
		// An error, including committed-but-reported errors, cannot authorize provider
		// effects. Never compensate by downgrading the irreversible admitted stage.
		if err != nil {
			admitErr := err
			var commitErr *session.ApprovalReceiptCommitError
			uncertain := errors.As(admitErr, &commitErr) && commitErr.Outcome != fileutil.AtomicCommitNotPublished
			released, releaseErr := swapPreparedApprovalState(scoped, req.SessionID, prepared.state, func(current session.State) session.State {
				current.Status = session.StatusFailed
				current.Phase = "prepare"
				current.LastError = admitErr.Error()
				return current
			})
			if releaseErr == nil {
				phase := "prepare_failed"
				if uncertain {
					phase = "aborted"
				}
				prep.approvalPreparation.LastError = admitErr.Error()
				releaseErr = prep.recordApprovalPreparation(phase, released)
			}
			result.Lookup, _ = scoped.GetApprovalReceipt(req.SessionID, request.RequestID)
			return errors.Join(admitErr, releaseErr)
		}
		op.Receipt = admitted
		result.Lookup, err = scoped.GetApprovalReceipt(req.SessionID, request.RequestID)
		if err != nil {
			return err
		}
		prepared.runner = r
		prepared.originalState = op.Payload.Preparation.OriginalState
		prepared.releaseRunSlot = release
		prepared.operation = op
		result.Prepared = prepared
		result.Replay = false
		return nil
	})
	if result.Prepared == nil && release != nil {
		release()
	}
	return result, err
}

func approvalOperationRecoveryReason(store *session.Store, op *approvalOperationContext) string {
	record := op.Payload.Preparation
	snapshot, err := store.LoadApprovalSnapshot(record.SessionID)
	if err != nil {
		return err.Error()
	}
	if err := session.ValidateApprovalTarget(snapshot, op.Receipt.Target); err != nil {
		return err.Error()
	}
	if op.Payload.HookPending {
		return "user-message hook outcome was not durably captured; use explicit ordinary continuation"
	}
	switch op.Receipt.Phase {
	case "executing", "settled", "aborted", "review_required", "recovered":
		return "operation is beyond a provably unexecuted preparation"
	}
	if session.ProcessOwnerAlive(record.OwnerPID, record.OwnerIdentity) && record.OwnerPID != os.Getpid() {
		return "approval preparation owner is still alive"
	}
	state, err := store.LoadState(record.SessionID)
	if err != nil {
		return err.Error()
	}
	if op.Receipt.Recovery.RunGeneration == "" {
		if !reflect.DeepEqual(state, record.OriginalState) {
			return "pre-claim state changed; explicit recovery is required"
		}
		return ""
	}
	if record.ClaimUpdatedAt != state.RunGeneration {
		return "approval receipt belongs to an older run generation"
	}
	expected := record.PreparedState
	if op.Receipt.Phase == "prepare_failed" && expected.Status == session.StatusFailed {
		if state.Status != session.StatusFailed {
			return "failed preparation state changed"
		}
		expected.Status = session.StatusRunning
		state.Status = session.StatusRunning
		if !samePreparedRun(expected, state) {
			return "failed preparation has advanced or changed generation"
		}
		return ""
	}
	if err := approvalPreparationOwnsState(record, state); err != nil {
		return err.Error()
	}
	return ""
}
func (r *Runner) resumeApprovalOperationClaim(sessionID string) error {
	op := r.approvalOperation
	record := r.approvalPreparation
	state, err := r.store.LoadState(sessionID)
	if err != nil {
		return err
	}
	if state.RunGeneration != record.ClaimUpdatedAt {
		return nil
	} // pre-claim retry, already checked against original
	if err := r.restoreApprovalOperationFacts(sessionID); err != nil {
		return err
	}
	if state.Status == session.StatusFailed {
		next := state
		next.Status = session.StatusRunning
		next.Phase = "prepare"
		next.LastError = record.OriginalState.LastError
		committed, saved, err := r.store.SwapStateIfCurrent(sessionID, state, next)
		if err != nil {
			return err
		}
		if !saved {
			return session.ErrApprovalConflict
		}
		state = committed
	}
	expected := record.PreparedState
	if expected.Status == session.StatusFailed {
		expected.Status = session.StatusRunning
		expected.LastError = record.OriginalState.LastError
	}
	if record.Phase == "claim_pending" {
		expected = state
	}
	if !samePreparedRun(expected, state) {
		return session.ErrApprovalConflict
	}
	record.PreparedState = state
	op.ResumeClaim = true
	return nil
}

func (r *Runner) checkpointApprovalOperation(phase string, state session.State) error {
	op := r.approvalOperation
	record := r.approvalPreparation
	if phase != "prepare_failed" {
		record.CompletedPhase = phase
	}
	record.Phase = phase
	record.PreparedState = state
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if phase == "prepared" {
		if err := r.captureApprovalOperationFacts(record.SessionID); err != nil {
			return err
		}
		op.Payload.Complete = true
	}
	recovery, err := op.recovery()
	if err != nil {
		return err
	}
	if phase == "prepared" {
		candidate := op.Receipt
		candidate.Phase = phase
		candidate.Recovery = recovery
		if _, err := decodeApprovalRecovery(candidate); err != nil {
			return err
		}
	}
	if phase == "claim_pending" && recovery.RunGeneration == "" {
		recovery.RunGeneration = record.ClaimUpdatedAt
	}
	receipt, err := r.store.CheckpointApprovalOperation(record.SessionID, op.Receipt.OperationID, op.Receipt.Recovery.RunGeneration, phase, recovery)
	if err != nil {
		return fmt.Errorf("checkpoint approval operation %s: %w", phase, err)
	}
	op.Receipt = receipt
	return nil
}
func (r *Runner) captureApprovalOperationFacts(sessionID string) error {
	op := r.approvalOperation
	snapshot, err := r.store.LoadApprovalSnapshot(sessionID)
	if err != nil {
		return err
	}
	if err := session.ValidateApprovalTarget(snapshot, op.Receipt.Target); err != nil {
		return err
	}
	op.Payload.ApprovedPlan = &snapshot.PlanMode
	op.Payload.ApprovedGoal = snapshot.Goal
	history, err := r.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		return err
	}
	for _, row := range history {
		if row.PlanModeID == op.Receipt.Target.PlanModeID && row.PlanVersion == op.Receipt.Target.PlanVersion && (approvalRevisionFromData(row.Data) == op.Receipt.Target.ExpectedRevision || row.Type == "execution_started") {
			op.Payload.PlanHistory = appendFactByID(op.Payload.PlanHistory, row, func(row session.PlanModeHistoryEntry) string { return row.ID })
		}
	}
	if snapshot.Goal != nil {
		history, err := r.store.LoadGoalHistory(sessionID)
		if err != nil {
			return err
		}
		for _, row := range history {
			if row.GoalID == snapshot.Goal.GoalID && approvalRevisionFromData(row.Data) == op.Receipt.Target.ExpectedRevision {
				op.Payload.GoalHistory = appendFactByID(op.Payload.GoalHistory, row, func(row session.GoalHistoryEntry) string { return row.ID })
			}
		}
	}
	actualEvents, err := r.store.LoadEvents(sessionID)
	if err != nil {
		return err
	}
	for _, evt := range actualEvents {
		if approvalRevisionFromData(evt.Data) == op.Receipt.Target.ExpectedRevision && (evt.Type == "planmode.plan_approved" || evt.Type == "planmode.execution_started" || evt.Type == "mission.plan.approved") {
			op.Payload.Events = appendFactByID(op.Payload.Events, evt, func(evt events.Event) string { return evt.ID })
		}
	}
	actualMessages, err := r.store.LoadMessages(sessionID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	initialIDs := map[string]bool{}
	for _, id := range op.Payload.OriginalMessageIDs {
		initialIDs[id] = true
	}
	// Keep this operation's newly written preparation messages. Existing tool
	// history is already a separate session fact and is not copied into each receipt.
	for _, msg := range actualMessages {
		if !initialIDs[msg.ID] {
			op.Payload.Messages = appendFactByID(op.Payload.Messages, msg, func(msg session.Message) string { return msg.ID })
		}
	}
	return nil
}
func appendFactByID[T any](facts []T, fact T, id func(T) string) []T {
	for _, existing := range facts {
		if id(existing) == id(fact) {
			return facts
		}
	}
	return append(facts, fact)
}
func equalApprovalFact(a, b any) bool {
	first, err := json.Marshal(a)
	if err != nil {
		return false
	}
	second, err := json.Marshal(b)
	return err == nil && bytes.Equal(first, second)
}

func (r *Runner) appendApprovalOperationEvent(sessionID, eventType, phase string, data map[string]any) error {
	op := r.approvalOperation
	evt := events.New(sessionID, eventType, phase, data)
	for _, existing := range op.Payload.Events {
		if existing.Type == eventType && existing.Phase == phase && equalApprovalFact(existing.Data, data) {
			evt = existing
			break
		}
	}
	op.Payload.Events = appendFactByID(op.Payload.Events, evt, func(evt events.Event) string { return evt.ID })
	if eventType == "planmode.plan_approved" || eventType == "planmode.execution_started" || eventType == "mission.plan.approved" {
		if err := r.captureApprovalOperationFacts(sessionID); err != nil {
			return err
		}
	}
	if err := r.checkpointApprovalOperation(op.Payload.Preparation.Phase, op.Payload.Preparation.PreparedState); err != nil {
		return err
	}
	return r.restoreApprovalEvent(evt)
}
func (r *Runner) restoreApprovalEvent(evt events.Event) error {
	existing, err := r.store.LoadEvents(evt.SessionID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, row := range existing {
		if row.ID == evt.ID {
			if !equalApprovalFact(row, evt) {
				return session.ErrApprovalReceiptUnverifiable
			}
			return nil
		}
	}
	if err := r.store.AppendEvent(evt.SessionID, evt); err != nil {
		return err
	}
	r.bus.Publish(evt)
	return nil
}
func (r *Runner) restoreApprovalMessage(sessionID string, msg session.Message) error {
	existing, err := r.store.LoadMessages(sessionID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, row := range existing {
		if row.ID == msg.ID {
			if !equalApprovalFact(row, msg) {
				return session.ErrApprovalReceiptUnverifiable
			}
			return nil
		}
	}
	return r.store.AppendMessage(sessionID, msg)
}
func (r *Runner) restoreApprovalOperationFacts(sessionID string) error {
	op := r.approvalOperation
	// Caller proved the exact semantic target before repairing any fact. These
	// appends never restore an older goal/plan snapshot over current content.
	for _, msg := range op.Payload.Messages {
		if err := r.restoreApprovalMessage(sessionID, msg); err != nil {
			return err
		}
	}
	for _, evt := range op.Payload.Events {
		if err := r.restoreApprovalEvent(evt); err != nil {
			return err
		}
	}
	planHistory, err := r.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		return err
	}
	for _, fact := range op.Payload.PlanHistory {
		found := false
		for _, row := range planHistory {
			if row.ID == fact.ID {
				if !equalApprovalFact(row, fact) {
					return session.ErrApprovalReceiptUnverifiable
				}
				found = true
				break
			}
		}
		if !found {
			if err := r.store.AppendPlanModeHistory(sessionID, fact); err != nil {
				return err
			}
		}
	}
	if len(op.Payload.GoalHistory) > 0 {
		history, err := r.store.LoadGoalHistory(sessionID)
		if err != nil {
			return err
		}
		for _, fact := range op.Payload.GoalHistory {
			found := false
			for _, row := range history {
				if row.ID == fact.ID {
					if !equalApprovalFact(row, fact) {
						return session.ErrApprovalReceiptUnverifiable
					}
					found = true
					break
				}
			}
			if !found {
				if err := r.store.AppendGoalHistory(sessionID, fact); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (r *Runner) appendApprovalOperationReplay(ctx context.Context, meta session.SessionMetadata, text string, extra map[string]any) error {
	op := r.approvalOperation
	if op.Payload.ReplayDecision == nil {
		if op.Payload.HookPending {
			return session.ErrApprovalReceiptUnverifiable
		}
		op.Payload.HookPending = true
		if err := r.checkpointApprovalOperation(op.Payload.Preparation.Phase, op.Payload.Preparation.PreparedState); err != nil {
			return err
		}
		transformed, err := r.transformUserMessage(ctx, meta, "prepare", text)
		if err != nil {
			return err
		}
		decision := &approvalReplayDecision{Suppressed: strings.TrimSpace(transformed) == ""}
		if !decision.Suppressed {
			msg := session.NewMessage("user", transformed)
			msg.Meta = extra
			data := map[string]any{"text": transformed, "mode": meta.Mode}
			for key, value := range extra {
				data[key] = value
			}
			evt := events.New(meta.ID, "user.message", "prepare", data)
			decision.Message = &msg
			decision.Event = &evt
			op.Payload.Messages = appendFactByID(op.Payload.Messages, msg, func(msg session.Message) string { return msg.ID })
			op.Payload.Events = appendFactByID(op.Payload.Events, evt, func(evt events.Event) string { return evt.ID })
		}
		op.Payload.HookPending = false
		op.Payload.ReplayDecision = decision
		if err := r.checkpointApprovalOperation(op.Payload.Preparation.Phase, op.Payload.Preparation.PreparedState); err != nil {
			return err
		}
	}
	if op.Payload.ReplayDecision.Suppressed {
		return nil
	}
	if err := r.restoreApprovalMessage(meta.ID, *op.Payload.ReplayDecision.Message); err != nil {
		return err
	}
	return r.restoreApprovalEvent(*op.Payload.ReplayDecision.Event)
}

func approvalReceiptRunResult(result ApprovalPreparationResult) RunResult {
	receipt := result.Lookup.Receipt
	status := string(receipt.Stage)
	if receipt.Stage == session.ApprovalReceiptRejected {
		status = "rejected"
	}
	return RunResult{SessionID: receipt.SessionID, Status: status, Approval: result.sidecar()}
}

func validApprovalFactTime(value string) bool {
	stamp, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !stamp.IsZero() && stamp.UTC().Format(time.RFC3339Nano) == value
}
