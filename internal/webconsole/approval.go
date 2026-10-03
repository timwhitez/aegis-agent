package webconsole

import (
	"aegis-agent/internal/events"
	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
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
	snapshot, err := s.store.LoadApprovalSnapshot(sessionID)
	if err != nil {
		writeError(w, planModeActionStatus(err), err)
		return
	}
	plan := snapshot.PlanMode
	if snapshot.Goal != nil && plan.Enabled && plan.LinkedGoalID == snapshot.Goal.GoalID && (plan.Status == session.PlanModeStatusAwaitingApproval || plan.Status == session.PlanModeStatusApproved) {
		target := req.ApprovalTarget
		if err := s.launchPlanModeContinue(r.Context(), sessionID, runtime.ContinueRequest{SessionID: sessionID, ApprovePlan: true, ApprovalTarget: &target, OverrideGoalCoverage: req.OverrideCoverage, Source: session.PlanModeSourceWeb}); err != nil {
			writeError(w, planModeActionStatus(err), err)
			return
		}
		writeJSON(w, http.StatusAccepted, LaunchResponse{SessionID: sessionID, Status: "accepted"})
		return
	}
	s.withGoalMutation(w, sessionID, func(view *goalMutationService) {
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
