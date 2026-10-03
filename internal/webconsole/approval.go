package webconsole

import (
	"aegis-agent/internal/events"
	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
	"context"
	"errors"
	"net/http"
)

// A scoped view shares the real Service for configuration/handles but owns only
// the Store valid within this approval boundary. No mutex or Service is copied.
type goalMutationService struct {
	*Service
	store *session.Store
}

func (s *Service) withGoalMutation(w http.ResponseWriter, sessionID string, fn func(*goalMutationService)) {
	if err := s.store.WithApprovalLock(sessionID, func(store *session.Store) error {
		fn(&goalMutationService{Service: s, store: store})
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
	}
}

func (s *Service) handleGoalCreate(w http.ResponseWriter, r *http.Request, sessionID string) {
	s.withGoalMutation(w, sessionID, func(view *goalMutationService) { view.handleGoalCreate(w, r, sessionID) })
}

func (s *Service) handleGoalPatch(w http.ResponseWriter, r *http.Request, sessionID string) {
	s.withGoalMutation(w, sessionID, func(view *goalMutationService) { view.handleGoalPatch(w, r, sessionID) })
}

func (s *Service) handleGoalClear(w http.ResponseWriter, r *http.Request, sessionID string) {
	s.withGoalMutation(w, sessionID, func(view *goalMutationService) { view.handleGoalClear(w, r, sessionID) })
}

func (s *Service) handleGoalStatus(w http.ResponseWriter, r *http.Request, sessionID, status, eventType string) {
	s.withGoalMutation(w, sessionID, func(view *goalMutationService) { view.handleGoalStatus(w, r, sessionID, status, eventType) })
}

func (s *Service) handleMissionPlanPatch(w http.ResponseWriter, r *http.Request, sessionID string) {
	s.withGoalMutation(w, sessionID, func(view *goalMutationService) { view.handleMissionPlanPatch(w, r, sessionID) })
}

func (s *Service) handleMissionValidationPatch(w http.ResponseWriter, r *http.Request, sessionID string) {
	s.withGoalMutation(w, sessionID, func(view *goalMutationService) { view.handleMissionValidationPatch(w, r, sessionID) })
}

func (s *Service) handleMissionPlanApprove(w http.ResponseWriter, r *http.Request, sessionID string) {
	req, ok := decodeOptionalMissionPlanApproveRequest(w, r)
	if !ok {
		return
	}
	if req.ApprovalRequestID != "" {
		cfg, err := s.configSnapshot()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		lookup, err := runtime.NewCoreRunner(cfg).LookupApprovalContinue(missionApprovalContinue(sessionID, req))
		if err != nil {
			writeError(w, planModeActionStatus(err), err)
			return
		}
		if lookup.Found {
			s.respondApproval(w, r, missionApprovalContinue(sessionID, req))
			return
		}
	}
	snapshot, err := s.store.LoadApprovalSnapshot(sessionID)
	if err != nil {
		writeError(w, planModeActionStatus(err), err)
		return
	}
	plan := snapshot.PlanMode
	if snapshot.Goal != nil && plan.Enabled && plan.LinkedGoalID == snapshot.Goal.GoalID && (plan.Status == session.PlanModeStatusAwaitingApproval || plan.Status == session.PlanModeStatusApproved) {
		s.respondApproval(w, r, missionApprovalContinue(sessionID, req))
		return
	}
	var receiptAppeared bool
	s.withGoalMutation(w, sessionID, func(view *goalMutationService) {
		if req.ApprovalRequestID != "" {
			// This scoped read only detects a peer binding under the mutation
			// lock. A complete core validation supplies every final response.
			lookup, err := view.store.LookupApprovalReceipt(sessionID, req.ApprovalRequestID, session.ApprovalParameters{Target: req.ApprovalTarget, OverrideCoverage: req.OverrideCoverage, Message: req.Message, Provider: req.Provider, Model: req.Model, ProviderOptions: req.ProviderOptions, SystemOverride: req.SystemOverride})
			if err != nil {
				writeError(w, planModeActionStatus(err), err)
				return
			}
			if lookup.Found {
				receiptAppeared = true
				return
			}
		}
		current, err := view.store.LoadApprovalSnapshot(sessionID)
		if err != nil {
			writeError(w, planModeActionStatus(err), err)
			return
		}
		// Executing is a fact-repair alias, never an execution admission.
		currentPlan := current.PlanMode
		if current.Goal != nil && currentPlan.Enabled && currentPlan.LinkedGoalID == current.Goal.GoalID && currentPlan.Status == session.PlanModeStatusExecuting {
			if err := session.ValidateApprovalTarget(current, req.ApprovalTarget); err != nil {
				writeError(w, planModeActionStatus(err), err)
				return
			}
		}
		view.handleMissionPlanApprove(w, r, sessionID, req)
	})
	if receiptAppeared {
		s.respondApproval(w, r, missionApprovalContinue(sessionID, req))
	}
}

func missionApprovalContinue(id string, req MissionPlanApproveRequest) runtime.ContinueRequest {
	return runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &req.ApprovalTarget, ApprovalRequestID: req.ApprovalRequestID, OverrideGoalCoverage: req.OverrideCoverage, Message: req.Message, Provider: req.Provider, Model: req.Model, ProviderOptions: req.ProviderOptions, SystemOverride: req.SystemOverride, Source: session.PlanModeSourceWeb}
}

func (s *Service) approvalResponse(id string, result runtime.ApprovalPreparationResult) ApprovalResponse {
	resp := ApprovalResponse{SessionID: id}
	if result.Lookup.Found {
		resp.Approval = &runtime.ApprovalResult{Lookup: result.Lookup, Replay: result.Replay, RecoveryRequired: result.RecoveryRequired, RecoveryReason: result.RecoveryReason}
		resp.Status = string(result.Lookup.Receipt.Stage)
		if result.Lookup.Receipt.Stage == session.ApprovalReceiptRejected {
			resp.Error = result.Lookup.Receipt.Rejection
			resp.Code = "APPROVAL_REJECTED"
		}
	}
	if result.RecoveryRequired {
		resp.Status = "recovery_required"
		resp.Code = "APPROVAL_RECOVERY_REQUIRED"
		resp.Action = "Review the receipt, explicitly stop any uncertain run, and use ordinary continue for recovery."
	}
	state, err := s.store.LoadState(id)
	if err == nil {
		resp.CurrentState = &state
	} else {
		resp.CurrentStateError = err.Error()
	}
	return resp
}

func (s *Service) handleApprovalReceipt(w http.ResponseWriter, r *http.Request, id, requestID string) {
	cfg, err := s.configSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	lookup, err := runtime.NewCoreRunner(cfg).ApprovalReceipt(id, requestID)
	if err != nil {
		writeError(w, planModeActionStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, s.approvalResponse(id, runtime.ApprovalPreparationResult{Lookup: lookup, Replay: true}))
}

func (s *Service) respondApproval(w http.ResponseWriter, r *http.Request, req runtime.ContinueRequest) {
	resp, status, err := s.launchApproval(r.Context(), req)
	if err != nil {
		if resp.Approval == nil {
			writeError(w, status, err)
			return
		}
		resp.Error = err.Error()
		if resp.Code == "" {
			resp.Code = "APPROVAL_PREPARATION_FAILED"
		}
	}
	writeJSON(w, status, resp)
}

// Only a new durable admission creates a Web handle. Receipt lookup deliberately
// precedes current metadata, state, handle and provider-default checks.
func (s *Service) launchApproval(ctx context.Context, req runtime.ContinueRequest) (ApprovalResponse, int, error) {
	cfg, err := s.configSnapshot()
	if err != nil {
		return ApprovalResponse{}, http.StatusInternalServerError, err
	}
	runner := runtime.NewRunner(cfg)
	lookup, err := runner.LookupApprovalContinue(req)
	if err != nil {
		return ApprovalResponse{}, planModeActionStatus(err), err
	}
	if !lookup.Found {
		if s.beforeApprovalPreflight != nil {
			s.beforeApprovalPreflight(req.SessionID)
		}
		preflight := s.approvalLaunchPreflight(ctx, req.SessionID)
		if preflight != nil {
			// A peer may have admitted the operation while this preflight observed
			// its run claim. Re-read before turning that claim into a rejection.
			lookup, err = runner.LookupApprovalContinue(req)
			if err != nil {
				return ApprovalResponse{}, planModeActionStatus(err), err
			}
			if !lookup.Found {
				return ApprovalResponse{}, planModeActionStatus(preflight), preflight
			}
		}
	}
	if s.beforeApprovalPrepare != nil {
		s.beforeApprovalPrepare(req.SessionID)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	prepared, err := runner.PrepareApprovalOperation(runCtx, req)
	resp := s.approvalResponse(req.SessionID, prepared)
	if err != nil {
		cancel()
		if errors.Is(err, session.ErrApprovalReceiptUnverifiable) || (prepared.Lookup.Found && prepared.Lookup.Receipt.Stage == session.ApprovalReceiptAdmitted) {
			prepared.RecoveryRequired, prepared.RecoveryReason = true, err.Error()
			resp = s.approvalResponse(req.SessionID, prepared)
		}
		return resp, planModeActionStatus(err), err
	}
	if prepared.Prepared == nil {
		cancel()
		return resp, http.StatusOK, nil
	}
	runner.SetRunLifecycleHooks(s.runLifecycleHooks())
	handle := newLaunchHandle(req.SessionID, runner, cancel)
	if err := s.addHandle(handle); err != nil {
		cancel()
		cause := errors.Join(err, runner.AbortPreparedApproval(prepared.Prepared, err))
		lookup, lookupErr := runner.ApprovalReceipt(req.SessionID, req.ApprovalRequestID)
		if lookupErr == nil {
			prepared.Lookup = lookup
		} else {
			cause = errors.Join(cause, lookupErr)
		}
		prepared.RecoveryRequired, prepared.RecoveryReason = true, cause.Error()
		return s.approvalResponse(req.SessionID, prepared), planModeActionStatus(cause), cause
	}
	resp.Status = "accepted"
	s.trackLaunch(func() {
		result, err := runner.RunPreparedApproval(runCtx, prepared.Prepared)
		s.finishHandle(handle, launchOutcome{result: result, err: err})
	})
	return resp, http.StatusAccepted, nil
}

func (s *Service) approvalLaunchPreflight(ctx context.Context, id string) error {
	meta, err := s.store.LoadMetadata(id)
	if err != nil {
		return err
	}
	state, err := s.store.LoadState(id)
	if err != nil {
		return err
	}
	if err := runtime.ValidateContinueTarget(meta, state); err != nil {
		return webContinueError(err)
	}
	if s.hasActiveHandle(id) {
		if !s.waitForSettlingActiveHandle(ctx, id, state.UpdatedAt) {
			return errSessionAlreadyActive
		}
		state, err = s.store.LoadState(id)
		if err != nil {
			return err
		}
		if err := runtime.ValidateContinueTarget(meta, state); err != nil {
			return webContinueError(err)
		}
	}
	if ctx.Err() != nil {
		return errSessionAlreadyActive
	}
	return nil
}

func decodeOptionalPlanModeApproveRequest(w http.ResponseWriter, r *http.Request) (PlanModeApproveRequest, bool) {
	var req PlanModeApproveRequest
	if _, err := decodeOptionalJSON(r, &req); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errJSONMutationContentType) {
			status = http.StatusForbidden
		}
		writeError(w, status, err)
		return req, false
	}
	if req.PlanModeID == "" || req.PlanVersion <= 0 || req.ExpectedRevision == "" {
		writeError(w, http.StatusBadRequest, session.ErrMissingApprovalTarget)
		return req, false
	}
	return req, true
}

// Reconciliation shares the approval boundary so a run claim between prepare
// and handle acquisition cannot be mistaken for a prior settled generation.
func (s *Service) reconcileStaleRunningSession(sessionID, pauseReason string) (changed bool, err error) {
	err = s.store.WithApprovalLock(sessionID, func(store *session.Store) error {
		changed, err = (&goalMutationService{Service: s, store: store}).reconcileStaleRunningSession(sessionID, pauseReason)
		return err
	})
	return
}

func (s *Service) restoreStaleRunningSessionReconcile(sessionID string, previousState session.State, previousEvents []events.Event, previousRequests []session.SteerRequest, cause error, expected ...session.State) (err error) {
	err = s.store.WithApprovalLock(sessionID, func(store *session.Store) error {
		return (&goalMutationService{Service: s, store: store}).restoreStaleRunningSessionReconcile(sessionID, previousState, previousEvents, previousRequests, cause, expected...)
	})
	return
}
