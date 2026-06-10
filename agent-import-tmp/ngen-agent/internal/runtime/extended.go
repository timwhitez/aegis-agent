package ngenrt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"ngen/internal/artifact"
	"ngen/internal/provider"
	"ngen/internal/task"
)

func (s *Service) Run(ctx context.Context, taskID string) (task.StatusSnapshot, []task.Event, error) {
	return s.runWithSession(ctx, taskID, "run", nil)
}

func (s *Service) Resume(ctx context.Context, taskID string) (task.StatusSnapshot, []task.Event, error) {
	return s.runWithSession(ctx, taskID, "resume", nil)
}

func (s *Service) runWithSession(ctx context.Context, taskID, action string, session *task.Session) (task.StatusSnapshot, []task.Event, error) {
	return s.runWithHooks(ctx, taskID, action, func(inner context.Context) (task.StatusSnapshot, []task.Event, error) {
		return s.execute(inner, taskID, session)
	})
}

func (s *Service) runWithHooks(ctx context.Context, taskID, action string, fn func(context.Context) (task.StatusSnapshot, []task.Event, error)) (task.StatusSnapshot, []task.Event, error) {
	preEvents, err := s.executeHooks(ctx, "pre_run", taskID, action)
	if err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	snapshot, events, err := fn(ctx)
	if err != nil {
		return task.StatusSnapshot{}, nil, err
	}
	if len(preEvents) > 0 {
		events = append(append([]task.Event{}, preEvents...), events...)
	}
	postEvents, err := s.executeHooks(ctx, "post_run", taskID, action)
	if err != nil {
		return task.StatusSnapshot{}, events, err
	}
	if len(postEvents) > 0 {
		events = append(events, postEvents...)
	}
	if snapshot.State == task.StateDone {
		doneEvents, err := s.executeHooks(ctx, "on_done", taskID, action)
		if err != nil {
			return snapshot, events, err
		}
		if len(doneEvents) > 0 {
			events = append(events, doneEvents...)
		}
	}
	if _, err := s.captureHarnessEvaluation(ctx, taskID, action); err != nil {
		return task.StatusSnapshot{}, events, err
	}
	return snapshot, events, nil
}

func (s *Service) executeHooks(ctx context.Context, hookStage, taskID, action string) ([]task.Event, error) {
	definitions := s.hookDefinitions(hookStage, action)
	if len(definitions) == 0 {
		return nil, nil
	}
	if _, err := s.Store.LoadTask(taskID); err != nil {
		return nil, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return nil, err
	}
	var events []task.Event
	for _, definition := range definitions {
		event, execErr := s.executeHookDefinition(ctx, definition, taskID, action, state)
		if event.EventID != "" {
			events = append(events, event)
			state.LastEventRef = artifact.EventRef(event.EventID)
			state.UpdatedAt = task.Now()
			if err := s.Store.SaveState(state); err != nil {
				return events, err
			}
		}
		if execErr != nil {
			return events, execErr
		}
	}
	return events, nil
}

func (s *Service) executeHookDefinition(ctx context.Context, definition task.HookDefinition, taskID, action string, state task.State) (task.Event, error) {
	if len(definition.Command) == 0 {
		return task.Event{}, nil
	}
	cmdCtx := ctx
	cancel := func() {}
	if definition.TimeoutSeconds > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, time.Duration(definition.TimeoutSeconds)*time.Second)
	}
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, definition.Command[0], definition.Command[1:]...)
	payload := map[string]string{
		"hook":       definition.Stage,
		"hook_id":    definition.HookID,
		"task_id":    taskID,
		"action":     action,
		"state":      string(state.State),
		"phase":      string(state.Phase),
		"allow_fail": fmt.Sprintf("%t", definition.AllowFailure),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return task.Event{}, err
	}
	cmd.Stdin = bytes.NewReader(data)
	stdout := newCappedOutputBuffer(commandOutputMaxBytes)
	stderr := newCappedOutputBuffer(commandOutputMaxBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		summary := fmt.Sprintf("%s hook failed: %v", hookLabel(definition), err)
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			if stderr.Truncated() {
				detail = detail + fmt.Sprintf(" [stderr truncated after %d bytes]", commandOutputMaxBytes)
			}
			summary = summary + ": " + detail
		}
		event := newEvent(taskID, state, "hook_failed", summary, nil)
		if appendErr := s.Store.AppendEvent(event); appendErr != nil {
			return task.Event{}, appendErr
		}
		if definition.AllowFailure {
			return event, nil
		}
		return event, errors.New(summary)
	}
	summary := strings.TrimSpace(stdout.String())
	if stdout.Truncated() {
		summary = strings.TrimSpace(summary + fmt.Sprintf(" [stdout truncated after %d bytes]", commandOutputMaxBytes))
	}
	if summary == "" {
		summary = fmt.Sprintf("%s hook executed.", hookLabel(definition))
	} else {
		summary = fmt.Sprintf("%s: %s", hookLabel(definition), summary)
	}
	event := newEvent(taskID, state, "hook_executed", summary, nil)
	if err := s.Store.AppendEvent(event); err != nil {
		return task.Event{}, err
	}
	return event, nil
}

func (s *Service) Auto(ctx context.Context, taskID string) (task.StatusSnapshot, []task.Event, error) {
	return s.auto(ctx, taskID, nil)
}

type autoOptions struct {
	RuntimeAction        string
	DisableTaskCreate    bool
	TaskCreateDiagnostic string
	MissionID            string
}

func (s *Service) auto(ctx context.Context, taskID string, session *task.Session) (snapshot task.StatusSnapshot, all []task.Event, err error) {
	return s.autoWithOptions(ctx, taskID, session, autoOptions{RuntimeAction: "auto"})
}

func (s *Service) autoWithOptions(ctx context.Context, taskID string, session *task.Session, opts autoOptions) (snapshot task.StatusSnapshot, all []task.Event, err error) {
	runtimeAction := strings.TrimSpace(opts.RuntimeAction)
	if runtimeAction == "" {
		runtimeAction = "auto"
	}
	defer func() {
		if err != nil {
			return
		}
		if snapshot.TaskID == "" {
			snapshot, err = s.Status(ctx, taskID)
			if err != nil {
				return
			}
		}
		_, err = s.captureHarnessEvaluation(ctx, taskID, runtimeAction)
	}()
	driver := provider.New(s.Config.Provider)
	maxTurns := s.Config.Provider.AutoRunMaxTurns
	if maxTurns <= 0 {
		maxTurns = 1
	}
	taskCreationsRemaining := 2
	taskPlanMutationsRemaining := 2
	projectMutationsRemaining := 2
	memoryMutationsRemaining := 2
	multicaIssueNoTurnLimit := false
	for turn := 0; ; turn++ {
		if !multicaIssueNoTurnLimit && turn >= maxTurns {
			break
		}
		spec, state, input, err := s.buildProviderInput(ctx, taskID, session)
		if err != nil {
			return task.StatusSnapshot{}, all, err
		}
		if multicaIssueRunsWithoutAutoTurnLimit(spec) {
			multicaIssueNoTurnLimit = true
		}
		var decision provider.Decision
		if session != nil {
			if explicit, ok, explicitErr := provider.OperatorPromptDecision(session.LastPrompt); ok {
				if explicitErr != nil {
					return task.StatusSnapshot{}, all, explicitErr
				}
				decision = explicit
			}
		}
		providerDecisionCall := false
		if decision.Action == "" {
			decision, err = driver.Decide(ctx, input)
			if err != nil {
				return task.StatusSnapshot{}, all, err
			}
			providerDecisionCall = true
		}
		if providerDecisionCall {
			tokenUsage, promptCacheUsage := providerUsageFromDecision(decision)
			_, err = s.appendProviderUsage(taskID, providerUsageOperationDecision, s.Config.Provider, tokenUsage, promptCacheUsage, []string{
				"task.json",
				"state.json",
				"plan.json",
				"context/latest-pack.json",
				"continuity/latest.json",
				"sprint/latest.json",
			})
			if err != nil {
				return task.StatusSnapshot{}, all, err
			}
		}
		if input.RoleContract != nil {
			if err := authorizeProviderDecision(*input.RoleContract, decision); err != nil {
				return task.StatusSnapshot{}, all, err
			}
		}
		decisionEvent := newEvent(taskID, state, "provider_decided", decision.Summary, nil)
		if err := s.Store.AppendEvent(decisionEvent); err != nil {
			return task.StatusSnapshot{}, all, err
		}
		state.LastEventRef = artifact.EventRef(decisionEvent.EventID)
		state.UpdatedAt = task.Now()
		if err := s.Store.SaveState(state); err != nil {
			return task.StatusSnapshot{}, all, err
		}
		all = append(all, decisionEvent)
		if decision.Action == "run" || decision.Action == "resume" {
			bootstrapEvents, bootstrapErr := s.bootstrapExecutionPlan(ctx, taskID, spec, input)
			if bootstrapErr != nil {
				return task.StatusSnapshot{}, all, bootstrapErr
			}
			all = append(all, bootstrapEvents...)
		}
		var events []task.Event
		switch decision.Action {
		case "run":
			snapshot, events, err = s.runWithSession(ctx, taskID, "run", session)
		case "resume":
			snapshot, events, err = s.runWithSession(ctx, taskID, "resume", session)
		case "respond":
			replyEvent := newEvent(taskID, state, "provider_responded", decision.ResponseText, nil)
			if err = s.Store.AppendEvent(replyEvent); err != nil {
				break
			}
			state.LastEventRef = artifact.EventRef(replyEvent.EventID)
			state.UpdatedAt = task.Now()
			if err = s.Store.SaveState(state); err != nil {
				break
			}
			events = append(events, replyEvent)
			if session != nil {
				if err = s.appendSessionMessage(session.SessionID, session.TaskID, "assistant", decision.ResponseText); err != nil {
					break
				}
			}
			snapshot, err = s.Status(ctx, taskID)
		case "task_create":
			if opts.DisableTaskCreate {
				diagnostic := strings.TrimSpace(opts.TaskCreateDiagnostic)
				if diagnostic == "" {
					diagnostic = "task_create is disabled for this provider decision pass"
				}
				err = errors.New(diagnostic)
				break
			}
			if taskCreationsRemaining <= 0 {
				err = errors.New("provider emitted too many task_create decisions in one auto pass")
				break
			}
			taskCreationsRemaining--
			decision = normalizeProviderTaskCreateDecision(input, decision)
			taskFile := task.TaskFile{
				Kind:             task.Kind(decision.TaskKind),
				PresetID:         task.PresetID(decision.TaskPresetID),
				Title:            decision.TaskTitle,
				Objective:        decision.TaskObjective,
				SuccessCriteria:  criteriaFromStrings(decision.TaskCriteria),
				Constraints:      append([]string(nil), decision.TaskConstraints...),
				WorkspaceRoot:    spec.WorkspaceRoot,
				PermissionModeID: firstNonEmpty(decision.TaskPermissionModeID, spec.PermissionModeID),
			}
			if strings.TrimSpace(opts.MissionID) != "" {
				taskFile.ParentTaskID = spec.TaskID
				taskFile.RootTaskID = firstNonEmpty(spec.RootTaskID, spec.TaskID)
				taskFile.LineageDepth = spec.LineageDepth + 1
				taskFile.Constraints = uniqueNonEmptyStrings(append(taskFile.Constraints, fmt.Sprintf("Mission child task for %s: keep evidence linked to the mission feature and root task.", strings.TrimSpace(opts.MissionID))))
			}
			createdView, createErr := s.CreateTask(ctx, taskFile, task.StepSourceProvider, decision.ProjectStepID, decision.ProjectBranchID)
			if createErr != nil {
				err = createErr
				break
			}
			if strings.TrimSpace(opts.MissionID) != "" {
				if bindErr := s.bindMissionChildTask(ctx, opts.MissionID, createdView.Task.TaskID); bindErr != nil {
					err = bindErr
					break
				}
			}
			event := newEvent(taskID, state, "project_task_created", durableTaskCreatedSummary(createdView.Task, decision.ProjectStepID, decision.ProjectBranchID), []string{
				workspaceTaskRef(createdView.Task.TaskID, "task.json"),
				"workspace:.ngen/project/project.json",
			})
			if err = s.Store.AppendEvent(event); err != nil {
				break
			}
			state.LastEventRef = artifact.EventRef(event.EventID)
			state.UpdatedAt = task.Now()
			if err = s.Store.SaveState(state); err != nil {
				break
			}
			events = append(events, event)
			snapshot, err = s.Status(ctx, taskID)
		case "task_update":
			if taskPlanMutationsRemaining <= 0 {
				err = errors.New("provider emitted too many task plan mutation decisions in one auto pass")
				break
			}
			taskPlanMutationsRemaining--
			beforeCount := 0
			if existing, readErr := s.Store.ReadEvents(taskID); readErr == nil {
				beforeCount = len(existing)
			}
			_, err = s.UpdateTaskPlan(ctx, taskID, task.PlanUpdate{
				Explanation: decision.PlanExplanation,
				Steps:       decision.PlanSteps,
			}, task.StepSourceProvider)
			if err == nil {
				snapshot, err = s.Status(ctx, taskID)
			}
			if err == nil {
				if updated, readErr := s.Store.ReadEvents(taskID); readErr == nil && len(updated) > beforeCount {
					events = append(events, updated[beforeCount:]...)
				}
			}
		case "task_patch":
			if taskPlanMutationsRemaining <= 0 {
				err = errors.New("provider emitted too many task plan mutation decisions in one auto pass")
				break
			}
			taskPlanMutationsRemaining--
			beforeCount := 0
			if existing, readErr := s.Store.ReadEvents(taskID); readErr == nil {
				beforeCount = len(existing)
			}
			_, err = s.PatchTaskPlan(ctx, taskID, task.PlanPatch{
				Operations: decision.PlanPatchOperations,
			}, task.StepSourceProvider)
			if err == nil {
				snapshot, err = s.Status(ctx, taskID)
			}
			if err == nil {
				if updated, readErr := s.Store.ReadEvents(taskID); readErr == nil && len(updated) > beforeCount {
					events = append(events, updated[beforeCount:]...)
				}
			}
		case "project_update":
			if projectMutationsRemaining <= 0 {
				err = errors.New("provider emitted too many project graph mutation decisions in one auto pass")
				break
			}
			projectMutationsRemaining--
			_, err = s.UpdateProject(ctx, task.ProjectUpdate{
				Explanation: decision.ProjectExplanation,
				Steps:       decision.ProjectSteps,
				Branches:    decision.ProjectBranches,
			}, task.StepSourceProvider)
			if err == nil {
				snapshot, err = s.Status(ctx, taskID)
			}
		case "project_patch":
			if projectMutationsRemaining <= 0 {
				err = errors.New("provider emitted too many project graph mutation decisions in one auto pass")
				break
			}
			projectMutationsRemaining--
			_, err = s.PatchProject(ctx, task.ProjectPatch{
				Operations: decision.ProjectPatchOperations,
			}, task.StepSourceProvider)
			if err == nil {
				snapshot, err = s.Status(ctx, taskID)
			}
		case "memory_promote":
			if memoryMutationsRemaining <= 0 {
				err = errors.New("provider emitted too many memory promotion decisions in one auto pass")
				break
			}
			memoryMutationsRemaining--
			beforeCount := 0
			if existing, readErr := s.Store.ReadEvents(taskID); readErr == nil {
				beforeCount = len(existing)
			}
			_, err = s.PromoteMemory(ctx, taskID, task.MemoryPromotion{
				Kind:    decision.MemoryKind,
				Summary: decision.Summary,
				Refs:    decision.MemoryRefs,
			}, task.MemorySourceProvider)
			if err == nil {
				snapshot, err = s.Status(ctx, taskID)
			}
			if err == nil {
				if updated, readErr := s.Store.ReadEvents(taskID); readErr == nil && len(updated) > beforeCount {
					events = append(events, updated[beforeCount:]...)
				}
			}
		case "review":
			beforeCount := 0
			if existing, readErr := s.Store.ReadEvents(taskID); readErr == nil {
				beforeCount = len(existing)
			}
			_, err = s.Review(ctx, taskID)
			if err == nil {
				snapshot, err = s.Status(ctx, taskID)
			}
			if err == nil {
				if updated, readErr := s.Store.ReadEvents(taskID); readErr == nil && len(updated) > beforeCount {
					events = append(events, updated[beforeCount:]...)
				}
			}
		case "worker_spawn":
			beforeCount := 0
			if existing, readErr := s.Store.ReadEvents(taskID); readErr == nil {
				beforeCount = len(existing)
			}
			var worker task.WorkerContract
			worker, err = s.SpawnWorker(ctx, taskID, decision.WorkerRole, decision.WorkerObjective)
			if err == nil {
				_, err = s.ContinueWorker(ctx, taskID, worker.WorkerID)
			}
			if err == nil {
				snapshot, err = s.Status(ctx, taskID)
			}
			if err == nil {
				if updated, readErr := s.Store.ReadEvents(taskID); readErr == nil && len(updated) > beforeCount {
					events = append(events, updated[beforeCount:]...)
				}
			}
		case "worker_continue":
			beforeCount := 0
			if existing, readErr := s.Store.ReadEvents(taskID); readErr == nil {
				beforeCount = len(existing)
			}
			_, err = s.ContinueWorker(ctx, taskID, decision.WorkerID)
			if err == nil {
				snapshot, err = s.Status(ctx, taskID)
			}
			if err == nil {
				if updated, readErr := s.Store.ReadEvents(taskID); readErr == nil && len(updated) > beforeCount {
					events = append(events, updated[beforeCount:]...)
				}
			}
		case "wait":
			interval := time.Duration(s.Config.Watch.DefaultIntervalSeconds) * time.Second
			if decision.WatchInterval != "" {
				if parsed, parseErr := time.ParseDuration(decision.WatchInterval); parseErr == nil && parsed > 0 {
					interval = parsed
				}
			}
			_, err = s.SetWatch(ctx, taskID, interval, decision.WatchReason)
			if err == nil {
				snapshot, err = s.Status(ctx, taskID)
			}
		case "approval_request":
			_, err = s.RequestApproval(ctx, taskID, decision.ApprovalScope, decision.ApprovalReason)
			if err == nil {
				snapshot, err = s.Status(ctx, taskID)
			}
		case "block", "noop":
			snapshot, err = s.Status(ctx, taskID)
		default:
			err = fmt.Errorf("unsupported provider action: %s", decision.Action)
		}
		all = append(all, events...)
		if err != nil {
			return task.StatusSnapshot{}, all, err
		}
		if decision.Action == "memory_promote" && sessionRequestsStandaloneMemory(session) {
			return snapshot, all, nil
		}
		if decision.Action == "task_create" && sessionStopsAfterTaskCreate(session) {
			return snapshot, all, nil
		}
		if decision.Action == "task_create" && strings.TrimSpace(opts.MissionID) != "" {
			return snapshot, all, nil
		}
		if (decision.Action == "task_create" || decision.Action == "task_update" || decision.Action == "task_patch" || decision.Action == "project_update" || decision.Action == "project_patch" || decision.Action == "memory_promote") && snapshot.State == task.StateActive {
			turn--
			continue
		}
		if !shouldContinueAuto(decision, snapshot) {
			return snapshot, all, nil
		}
		_ = spec
	}
	if snapshot.TaskID == "" {
		status, err := s.Status(ctx, taskID)
		if err != nil {
			return task.StatusSnapshot{}, all, err
		}
		snapshot = status
	}
	if snapshot.State == task.StateActive {
		state, err := s.loadStateOrRecover(taskID)
		if err != nil {
			return task.StatusSnapshot{}, all, err
		}
		limitEvent := newEvent(taskID, state, "auto_turn_limit_reached", fmt.Sprintf("Auto loop stopped after %d turns.", maxTurns), nil)
		if err := s.Store.AppendEvent(limitEvent); err != nil {
			return task.StatusSnapshot{}, all, err
		}
		state.LastEventRef = artifact.EventRef(limitEvent.EventID)
		state.UpdatedAt = task.Now()
		if err := s.Store.SaveState(state); err != nil {
			return task.StatusSnapshot{}, all, err
		}
		all = append(all, limitEvent)
		refreshed, err := s.Status(ctx, taskID)
		if err != nil {
			return task.StatusSnapshot{}, all, err
		}
		snapshot = refreshed
	}
	return snapshot, all, nil
}

func multicaIssueRunsWithoutAutoTurnLimit(spec task.Spec) bool {
	return strings.HasPrefix(strings.TrimSpace(spec.Objective), multicaIssueExecutionObjectivePrefix) &&
		multicaIssueIDFromSpec(spec) != ""
}

func (s *Service) bootstrapExecutionPlan(ctx context.Context, taskID string, spec task.Spec, input provider.Input) ([]task.Event, error) {
	explanation, steps, ok := provider.SuggestExecutionPlan(input)
	if !ok {
		return nil, nil
	}
	beforeCount := 0
	if existing, readErr := s.Store.ReadEvents(taskID); readErr == nil {
		beforeCount = len(existing)
	}
	if _, err := s.UpdateTaskPlan(ctx, spec.TaskID, task.PlanUpdate{
		Explanation: explanation,
		Steps:       steps,
	}, task.StepSourceSystem); err != nil {
		return nil, err
	}
	if updated, readErr := s.Store.ReadEvents(taskID); readErr == nil && len(updated) > beforeCount {
		return updated[beforeCount:], nil
	}
	return nil, nil
}

func (s *Service) StartSession(ctx context.Context, taskID, mode string) (task.Session, error) {
	_ = ctx
	if _, err := s.Store.LoadTask(taskID); err != nil {
		return task.Session{}, err
	}
	if mode == "" {
		mode = "acp"
	}
	session := task.Session{
		SchemaVersion: task.SchemaVersion,
		SessionID:     task.NewID("SES"),
		TaskID:        taskID,
		Mode:          mode,
		Status:        "active",
		CreatedAt:     task.Now(),
		UpdatedAt:     task.Now(),
	}
	if err := s.Store.SaveSession(session); err != nil {
		return task.Session{}, err
	}
	return session, nil
}

func (s *Service) ReadSession(ctx context.Context, sessionID string) (task.Session, []task.SessionMessage, error) {
	_ = ctx
	session, err := s.Store.LoadSession(sessionID)
	if err != nil {
		return task.Session{}, nil, err
	}
	msgs, err := s.Store.ReadSessionMessages(sessionID)
	if err != nil {
		return task.Session{}, nil, err
	}
	return session, msgs, nil
}

func (s *Service) appendSessionMessage(sessionID, taskID, role, content string) error {
	msg := task.SessionMessage{
		SchemaVersion: task.SchemaVersion,
		MessageID:     task.NewID("MSG"),
		SessionID:     sessionID,
		TaskID:        taskID,
		Role:          strings.TrimSpace(role),
		Content:       strings.TrimSpace(content),
		TS:            task.Now(),
	}
	return s.Store.AppendSessionMessage(msg)
}

func (s *Service) SessionSnapshot(ctx context.Context, sessionID string) (task.SessionSnapshot, error) {
	session, messages, err := s.ReadSession(ctx, sessionID)
	if err != nil {
		return task.SessionSnapshot{}, err
	}
	status, err := s.Status(ctx, session.TaskID)
	if err != nil {
		return task.SessionSnapshot{}, err
	}
	managedWorkers, ownedPendingApprovals, err := s.managedWorkerContext(ctx, session.TaskID)
	if err != nil {
		return task.SessionSnapshot{}, err
	}
	return task.SessionSnapshot{
		ObjectKind:            "session_snapshot",
		SchemaVersion:         task.SchemaVersion,
		SessionID:             session.SessionID,
		TaskID:                session.TaskID,
		Mode:                  session.Mode,
		SessionStatus:         session.Status,
		LastPrompt:            session.LastPrompt,
		LastAction:            session.LastAction,
		SessionRef:            workspaceSessionRef(session.SessionID, ".json"),
		MessagesRef:           workspaceSessionRef(session.SessionID, ".messages.jsonl"),
		MessageCount:          len(messages),
		RecentMessages:        tailSessionMessages(messages, 5),
		ManagedWorkers:        managedWorkers,
		OwnedPendingApprovals: ownedPendingApprovals,
		StatusSnapshot:        status,
		UpdatedAt:             session.UpdatedAt,
	}, nil
}

func (s *Service) ListSessions(ctx context.Context) ([]task.Session, error) {
	_ = ctx
	return s.Store.ListSessions()
}

func (s *Service) GetTask(ctx context.Context, taskID string) (task.TaskView, error) {
	_ = ctx
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.TaskView{}, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.TaskView{}, err
	}
	plan, state := s.currentTaskPlan(spec, state)
	snapshot, err := s.Status(ctx, taskID)
	if err != nil {
		return task.TaskView{}, err
	}
	snapshot.CurrentStepID = state.CurrentStepID
	snapshot.PlanRevision = plan.Revision
	snapshot.CurrentSystemStepID = plan.CurrentSystemStepID
	snapshot.CurrentExecutionStepID = plan.CurrentExecutionStepID
	return task.TaskView{
		ObjectKind:    "task_view",
		SchemaVersion: task.SchemaVersion,
		Task:          spec,
		State:         state,
		Plan:          plan,
		Status:        snapshot,
	}, nil
}

func (s *Service) ListTasks(ctx context.Context) ([]task.TaskListEntry, error) {
	_ = ctx
	taskIDs, err := s.Store.ListTaskIDs()
	if err != nil {
		return nil, err
	}
	entries := make([]task.TaskListEntry, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		spec, err := s.Store.LoadTask(taskID)
		if err != nil {
			return nil, err
		}
		snapshot, err := s.Status(ctx, taskID)
		if err != nil {
			return nil, err
		}
		entries = append(entries, task.TaskListEntry{
			ObjectKind:             "task_list_entry",
			SchemaVersion:          task.SchemaVersion,
			TaskID:                 taskID,
			Title:                  spec.Title,
			Kind:                   spec.Kind,
			Phase:                  snapshot.Phase,
			State:                  snapshot.State,
			StatusReasonCode:       snapshot.StatusReasonCode,
			PlanRevision:           snapshot.PlanRevision,
			CurrentStepID:          snapshot.CurrentStepID,
			CurrentSystemStepID:    snapshot.CurrentSystemStepID,
			CurrentExecutionStepID: snapshot.CurrentExecutionStepID,
			UpdatedAt:              snapshot.UpdatedAt,
		})
	}
	return entries, nil
}

func (s *Service) CreateTask(ctx context.Context, file task.TaskFile, source, projectStepID, projectBranchID string) (task.TaskView, error) {
	if err := s.validateTaskCreateBindings(projectStepID, projectBranchID); err != nil {
		return task.TaskView{}, err
	}
	spec, err := s.Create(ctx, file)
	if err != nil {
		return task.TaskView{}, err
	}
	if strings.TrimSpace(projectStepID) != "" || strings.TrimSpace(projectBranchID) != "" {
		if err := s.bindCreatedTaskIntoProject(ctx, spec.TaskID, projectStepID, projectBranchID, source); err != nil {
			return task.TaskView{}, err
		}
		state, stateErr := s.loadStateOrRecover(spec.TaskID)
		if stateErr != nil {
			return task.TaskView{}, stateErr
		}
		if err := s.syncTaskNarrative(spec, state, "Task created. Run the task to capture baseline and execute verification."); err != nil {
			return task.TaskView{}, err
		}
	}
	return s.GetTask(ctx, spec.TaskID)
}

func (s *Service) refineTUITaskFromFirstPrompt(ctx context.Context, session task.Session, prompt string) error {
	if !strings.EqualFold(strings.TrimSpace(session.Mode), "tui") {
		return nil
	}
	objective, ok := tuiRefinementObjectiveFromPrompt(prompt)
	if !ok {
		return nil
	}
	messages, err := s.Store.ReadSessionMessages(session.SessionID)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "operator") && strings.TrimSpace(message.Content) != "" {
			return nil
		}
	}
	spec, err := s.Store.LoadTask(session.TaskID)
	if err != nil {
		return err
	}
	if !isRefinableTUITaskSpec(spec) {
		return nil
	}
	state, err := s.loadStateOrRecover(spec.TaskID)
	if err != nil {
		return err
	}
	title := tuiTaskTitleFromPrompt(objective)
	spec.Title = title
	spec.Objective = objective
	spec.SuccessCriteria = []task.SuccessCriterion{
		{
			ID:        "SC-001",
			Statement: fmt.Sprintf("TUI prompt is satisfied with durable verification or review evidence: %s", title),
		},
	}
	if err := s.Store.SaveTask(spec); err != nil {
		return err
	}
	if err := s.Store.SaveCriteria(task.NewInitialCriteria(spec)); err != nil {
		return err
	}
	state, err = s.refreshTaskPlan(spec, state)
	if err != nil {
		return err
	}
	event := newEvent(spec.TaskID, state, "task_refined", "Refined TUI session task from the first operator prompt.", []string{"task.json", "criteria/latest.json", "plan.json"})
	if err := s.Store.AppendEvent(event); err != nil {
		return err
	}
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return err
	}
	if err := s.TrackTaskInProject(ctx, spec); err != nil {
		return err
	}
	return s.syncTaskNarrative(spec, state, "Refined TUI session task from the first operator prompt.")
}

func tuiRefinementObjectiveFromPrompt(prompt string) (string, bool) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return "", false
	}
	if strings.HasPrefix(trimmed, "/") {
		missionPrompt, ok := parseMissionSlashPrompt(trimmed)
		if !ok || strings.TrimSpace(missionPrompt.Objective) == "" {
			return "", false
		}
		return strings.TrimSpace(missionPrompt.Objective), true
	}
	return trimmed, true
}

func isRefinableTUITaskSpec(spec task.Spec) bool {
	if spec.Kind != task.KindCoding {
		return false
	}
	if strings.TrimSpace(spec.Title) != "TUI Session" {
		return false
	}
	if strings.TrimSpace(spec.Objective) != "Capture the first TUI prompt and turn it into durable task context." {
		return false
	}
	if len(spec.SuccessCriteria) != 1 {
		return false
	}
	return strings.TrimSpace(spec.SuccessCriteria[0].Statement) == "First operator prompt is captured and used to drive the session"
}

func tuiTaskTitleFromPrompt(prompt string) string {
	compact := strings.Join(strings.Fields(strings.TrimSpace(prompt)), " ")
	if compact == "" {
		return "TUI Session"
	}
	runes := []rune(compact)
	const maxTitleRunes = 72
	if len(runes) > maxTitleRunes {
		compact = string(runes[:maxTitleRunes-1]) + "..."
	}
	return compact
}

func normalizeProviderTaskCreateDecision(input provider.Input, decision provider.Decision) provider.Decision {
	if decision.Action != "task_create" {
		return decision
	}
	decision.ProjectStepID = strings.TrimSpace(decision.ProjectStepID)
	decision.ProjectBranchID = strings.TrimSpace(decision.ProjectBranchID)
	if !taskCreateAllowsParentBindingReuse(input) {
		parentStepID, parentBranchID := currentTaskProjectBinding(input)
		if decision.ProjectStepID != "" && decision.ProjectStepID == parentStepID {
			decision.ProjectStepID = ""
		}
		if decision.ProjectBranchID != "" && decision.ProjectBranchID == parentBranchID {
			decision.ProjectBranchID = ""
		}
	}
	decision.TaskConstraints = sanitizeProviderTaskCreateConstraints(decision.TaskConstraints)
	return decision
}

func currentTaskProjectBinding(input provider.Input) (string, string) {
	focuses := []*task.ProjectTaskContext{}
	if input.ContextPack != nil && input.ContextPack.ProjectFocus != nil {
		focuses = append(focuses, input.ContextPack.ProjectFocus)
	}
	if input.Continuity != nil && input.Continuity.CurrentFocus.ProjectFocus != nil {
		focuses = append(focuses, input.Continuity.CurrentFocus.ProjectFocus)
	}
	if input.Sprint != nil && input.Sprint.ProjectFocus != nil {
		focuses = append(focuses, input.Sprint.ProjectFocus)
	}
	for _, focus := range focuses {
		if focus == nil {
			continue
		}
		stepID := firstNonEmpty(append([]string{strings.TrimSpace(focus.PrimaryStepID)}, focus.BoundStepIDs...)...)
		branchID := firstNonEmpty(append([]string{strings.TrimSpace(focus.PrimaryBranchID)}, focus.BoundBranchIDs...)...)
		if stepID != "" || branchID != "" {
			return strings.TrimSpace(stepID), strings.TrimSpace(branchID)
		}
	}
	return "", ""
}

func taskCreateAllowsParentBindingReuse(input provider.Input) bool {
	prompts := make([]string, 0, len(input.SessionRecentMessages)+1)
	if input.Session != nil {
		prompts = append(prompts, input.Session.LastPrompt)
	}
	for _, msg := range input.SessionRecentMessages {
		if strings.EqualFold(strings.TrimSpace(msg.Role), "operator") {
			prompts = append(prompts, msg.Content)
		}
	}
	for _, prompt := range prompts {
		if operatorRequestedBindingHandoff(prompt) {
			return true
		}
	}
	return false
}

func operatorRequestedBindingHandoff(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "handoff") || strings.Contains(lower, "hand off") || strings.Contains(lower, "take over") || strings.Contains(lower, "reassign") {
		return true
	}
	if strings.Contains(lower, "replace") && (strings.Contains(lower, "binding") || strings.Contains(lower, "current task") || strings.Contains(lower, "project step") || strings.Contains(lower, "project branch")) {
		return true
	}
	return false
}

func sanitizeProviderTaskCreateConstraints(constraints []string) []string {
	out := make([]string, 0, len(constraints))
	for _, raw := range constraints {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if providerTaskCreateConstraintIsOrchestration(lower) {
			continue
		}
		out = append(out, trimmed)
	}
	return uniqueStrings(out)
}

func providerTaskCreateConstraintIsOrchestration(lower string) bool {
	if strings.HasPrefix(lower, "create exactly one durable task") {
		return true
	}
	if strings.HasPrefix(lower, "create exactly one durable ") && strings.Contains(lower, " task") {
		return true
	}
	if strings.Contains(lower, "bind the new task to project step") {
		return true
	}
	if strings.Contains(lower, "bind it to project step") {
		return true
	}
	if strings.Contains(lower, "bind the task to project step") {
		return true
	}
	return false
}

func (s *Service) validateTaskCreateBindings(projectStepID, projectBranchID string) error {
	projectStepID = strings.TrimSpace(projectStepID)
	projectBranchID = strings.TrimSpace(projectBranchID)
	if projectStepID == "" && projectBranchID == "" {
		return nil
	}
	project, err := s.loadOrInitProject()
	if err != nil {
		return err
	}
	project, err = s.refreshProject(project)
	if err != nil {
		return err
	}
	if projectStepID != "" {
		idx := -1
		for i := range project.Steps {
			if strings.TrimSpace(project.Steps[i].ID) == projectStepID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("project_step_id references unknown step %s", projectStepID)
		}
		if existing := strings.TrimSpace(project.Steps[idx].TaskID); existing != "" {
			return fmt.Errorf("project_step_id %s is already bound to task %s", projectStepID, existing)
		}
	}
	if projectBranchID != "" {
		idx := -1
		for i := range project.Branches {
			if strings.TrimSpace(project.Branches[i].ID) == projectBranchID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("project_branch_id references unknown branch %s", projectBranchID)
		}
		if existing := strings.TrimSpace(project.Branches[idx].TaskID); existing != "" {
			return fmt.Errorf("project_branch_id %s is already bound to task %s", projectBranchID, existing)
		}
	}
	return nil
}

func (s *Service) bindCreatedTaskIntoProject(ctx context.Context, taskID, projectStepID, projectBranchID, source string) error {
	project, err := s.loadOrInitProject()
	if err != nil {
		return err
	}
	project, err = s.refreshProject(project)
	if err != nil {
		return err
	}
	ops := make([]task.ProjectPatchOperation, 0, 4)
	projectStepID = strings.TrimSpace(projectStepID)
	projectBranchID = strings.TrimSpace(projectBranchID)
	if projectStepID != "" {
		ops = append(ops, task.ProjectPatchOperation{
			Op:     task.ProjectPatchOpBindStepTask,
			StepID: projectStepID,
			TaskID: taskID,
		})
	}
	if projectBranchID != "" {
		ops = append(ops, task.ProjectPatchOperation{
			Op:       task.ProjectPatchOpBindBranchTask,
			BranchID: projectBranchID,
			TaskID:   taskID,
		})
	}
	autoStepID := projectTaskStepID(taskID)
	autoBranchID := projectTaskBranchID(taskID)
	if projectHasStep(project, autoStepID) && autoStepID != projectStepID {
		ops = append(ops, task.ProjectPatchOperation{
			Op:     task.ProjectPatchOpRemoveStep,
			StepID: autoStepID,
		})
	}
	if projectHasBranch(project, autoBranchID) && autoBranchID != projectBranchID {
		ops = append(ops, task.ProjectPatchOperation{
			Op:       task.ProjectPatchOpRemoveBranch,
			BranchID: autoBranchID,
		})
	}
	if len(ops) == 0 {
		return nil
	}
	_, err = s.PatchProject(ctx, task.ProjectPatch{Operations: ops}, source)
	return err
}

func projectHasStep(project task.Project, stepID string) bool {
	for _, step := range project.Steps {
		if strings.TrimSpace(step.ID) == strings.TrimSpace(stepID) {
			return true
		}
	}
	return false
}

func projectHasBranch(project task.Project, branchID string) bool {
	for _, branch := range project.Branches {
		if strings.TrimSpace(branch.ID) == strings.TrimSpace(branchID) {
			return true
		}
	}
	return false
}

func criteriaFromStrings(values []string) []task.SuccessCriterion {
	out := make([]task.SuccessCriterion, 0, len(values))
	for idx, value := range values {
		statement := strings.TrimSpace(value)
		if statement == "" {
			continue
		}
		out = append(out, task.SuccessCriterion{
			ID:        fmt.Sprintf("SC-%03d", idx+1),
			Statement: statement,
		})
	}
	return out
}

func workspaceTaskRef(taskID, rel string) string {
	return fmt.Sprintf("workspace:.ngen/tasks/%s/%s", strings.TrimSpace(taskID), filepath.ToSlash(strings.TrimSpace(rel)))
}

func durableTaskCreatedSummary(spec task.Spec, projectStepID, projectBranchID string) string {
	summary := fmt.Sprintf("Created durable %s task %s.", spec.Kind, spec.TaskID)
	bindings := make([]string, 0, 2)
	if strings.TrimSpace(projectStepID) != "" {
		bindings = append(bindings, fmt.Sprintf("step %s", strings.TrimSpace(projectStepID)))
	}
	if strings.TrimSpace(projectBranchID) != "" {
		bindings = append(bindings, fmt.Sprintf("branch %s", strings.TrimSpace(projectBranchID)))
	}
	if len(bindings) == 0 {
		return summary
	}
	return fmt.Sprintf("Created durable %s task %s and bound it to %s.", spec.Kind, spec.TaskID, strings.Join(bindings, " and "))
}

func (s *Service) UpdateTaskPlan(ctx context.Context, taskID string, update task.PlanUpdate, source string) (task.TaskView, error) {
	spec, state, existingPlan, err := s.prepareTaskPlanMutation(taskID)
	if err != nil {
		return task.TaskView{}, err
	}
	update, err = task.NormalizeExecutionPlanUpdate(spec, update)
	if err != nil {
		return task.TaskView{}, err
	}
	return s.persistTaskPlanMutation(ctx, spec, state, existingPlan, update, source, task.PlanMutationKindReplace, nil)
}

func (s *Service) PatchTaskPlan(ctx context.Context, taskID string, patch task.PlanPatch, source string) (task.TaskView, error) {
	spec, state, existingPlan, err := s.prepareTaskPlanMutation(taskID)
	if err != nil {
		return task.TaskView{}, err
	}
	patch, err = task.NormalizePlanPatch(patch)
	if err != nil {
		return task.TaskView{}, err
	}
	baseUpdate := executionPlanUpdateFromSteps(existingPlan.Explanation, normalizeExecutionPlanSteps(existingPlan.Steps, existingPlan.UpdatedAt))
	update, err := task.ApplyPlanPatch(baseUpdate, patch)
	if err != nil {
		return task.TaskView{}, err
	}
	update, err = task.NormalizeExecutionPlanUpdate(spec, update)
	if err != nil {
		return task.TaskView{}, err
	}
	return s.persistTaskPlanMutation(ctx, spec, state, existingPlan, update, source, task.PlanMutationKindPatch, patch.Operations)
}

func (s *Service) prepareTaskPlanMutation(taskID string) (task.Spec, task.State, task.Plan, error) {
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.Spec{}, task.State{}, task.Plan{}, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.Spec{}, task.State{}, task.Plan{}, err
	}
	if state.State == task.StateDone || state.State == task.StateAborted {
		return task.Spec{}, task.State{}, task.Plan{}, fmt.Errorf("cannot update plan for terminal task in state %s", state.State)
	}
	existingPlan, _ := s.Store.LoadPlan(taskID)
	return spec, state, existingPlan, nil
}

func (s *Service) persistTaskPlanMutation(
	ctx context.Context,
	spec task.Spec,
	state task.State,
	existingPlan task.Plan,
	update task.PlanUpdate,
	source string,
	mutationKind string,
	patchOps []task.PlanPatchOperation,
) (task.TaskView, error) {
	systemPlan, currentSystemStepID := s.deriveSystemPlan(spec)
	now := task.Now()
	executionSteps := make([]task.Step, 0, len(update.Steps))
	stepSource := strings.TrimSpace(strings.ToLower(source))
	if stepSource == "" {
		stepSource = task.StepSourceOperator
	}
	for i, step := range update.Steps {
		executionSteps = append(executionSteps, task.Step{
			ID:           step.ID,
			Kind:         task.StepKindExecution,
			Source:       stepSource,
			ParentStepID: step.ParentStepID,
			DependsOn:    append([]string(nil), step.DependsOn...),
			Priority:     step.Priority,
			Title:        step.Title,
			Status:       step.Status,
			Covers:       append([]string(nil), step.Covers...),
			Notes:        step.Notes,
			UpdatedAt:    now,
		})
		if executionSteps[i].ID == "" {
			executionSteps[i].ID = task.ExecutionPlanStepID(i)
		}
	}
	currentExecutionStepID, readyExecutionStepIDs, blockedExecutionStepIDs := executionPlanState(executionSteps)
	mutationID := task.NewID("PLN")
	plan := systemPlan
	plan.Revision = existingPlan.Revision + 1
	plan.Explanation = update.Explanation
	plan.CurrentSystemStepID = currentSystemStepID
	plan.CurrentExecutionStepID = currentExecutionStepID
	plan.ReadyExecutionStepIDs = readyExecutionStepIDs
	plan.BlockedExecutionStepIDs = blockedExecutionStepIDs
	plan.LastMutationRef = artifact.PlanMutationRef(mutationID)
	plan.Steps = mergePlanSteps(systemPlan.Steps, executionSteps)
	plan.UpdatedAt = now
	if err := s.Store.SavePlan(plan); err != nil {
		return task.TaskView{}, err
	}
	if err := s.Store.AppendPlanMutation(task.PlanMutationRecord{
		SchemaVersion:           task.SchemaVersion,
		MutationID:              mutationID,
		TaskID:                  spec.TaskID,
		Revision:                plan.Revision,
		MutationKind:            mutationKind,
		Source:                  stepSource,
		TS:                      now,
		Explanation:             plan.Explanation,
		CurrentExecutionStepID:  plan.CurrentExecutionStepID,
		ReadyExecutionStepIDs:   append([]string(nil), plan.ReadyExecutionStepIDs...),
		BlockedExecutionStepIDs: append([]string(nil), plan.BlockedExecutionStepIDs...),
		Steps:                   append([]task.Step(nil), executionSteps...),
		PatchOperations:         clonePlanPatchOperations(patchOps),
	}); err != nil {
		return task.TaskView{}, err
	}
	if plan.CurrentExecutionStepID != "" {
		state.CurrentStepID = plan.CurrentExecutionStepID
	} else if plan.CurrentSystemStepID != "" {
		state.CurrentStepID = plan.CurrentSystemStepID
	}
	state.UpdatedAt = now
	if err := s.Store.SaveState(state); err != nil {
		return task.TaskView{}, err
	}
	summary := "Mutable execution plan updated."
	if mutationKind == task.PlanMutationKindPatch {
		summary = "Mutable execution plan patched."
	}
	event := newEvent(spec.TaskID, state, "task_plan_updated", summary, []string{"plan.json", plan.LastMutationRef})
	if err := s.Store.AppendEvent(event); err != nil {
		return task.TaskView{}, err
	}
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return task.TaskView{}, err
	}
	if err := s.syncTaskNarrative(spec, state, summary); err != nil {
		return task.TaskView{}, err
	}
	return s.GetTask(ctx, spec.TaskID)
}

func executionPlanUpdateFromSteps(explanation string, steps []task.Step) task.PlanUpdate {
	update := task.PlanUpdate{
		Explanation: strings.TrimSpace(explanation),
		Steps:       make([]task.ExecutionPlanStep, 0, len(steps)),
	}
	for _, step := range steps {
		update.Steps = append(update.Steps, task.ExecutionPlanStep{
			ID:           step.ID,
			ParentStepID: step.ParentStepID,
			DependsOn:    append([]string(nil), step.DependsOn...),
			Priority:     step.Priority,
			Title:        step.Title,
			Status:       step.Status,
			Covers:       append([]string(nil), step.Covers...),
			Notes:        step.Notes,
		})
	}
	return update
}

func clonePlanPatchOperations(ops []task.PlanPatchOperation) []task.PlanPatchOperation {
	out := make([]task.PlanPatchOperation, 0, len(ops))
	for _, op := range ops {
		cloned := task.PlanPatchOperation{
			Op:          op.Op,
			Explanation: op.Explanation,
			StepID:      op.StepID,
			AfterStepID: op.AfterStepID,
		}
		if op.Step != nil {
			step := *op.Step
			step.DependsOn = append([]string(nil), step.DependsOn...)
			step.Covers = append([]string(nil), step.Covers...)
			cloned.Step = &step
		}
		out = append(out, cloned)
	}
	return out
}

func (s *Service) GetProject(ctx context.Context) (task.ProjectView, error) {
	_ = ctx
	project, err := s.loadOrInitProject()
	if err != nil {
		return task.ProjectView{}, err
	}
	project, err = s.refreshProject(project)
	if err != nil {
		return task.ProjectView{}, err
	}
	return task.ProjectView{
		ObjectKind:    "project_view",
		SchemaVersion: task.SchemaVersion,
		Project:       project,
	}, nil
}

func (s *Service) UpdateProject(ctx context.Context, update task.ProjectUpdate, source string) (task.ProjectView, error) {
	project, err := s.loadOrInitProject()
	if err != nil {
		return task.ProjectView{}, err
	}
	update, err = task.NormalizeProjectUpdate(update)
	if err != nil {
		return task.ProjectView{}, err
	}
	return s.persistProjectMutation(ctx, project, update, source, task.PlanMutationKindReplace, nil)
}

func (s *Service) PatchProject(ctx context.Context, patch task.ProjectPatch, source string) (task.ProjectView, error) {
	project, err := s.loadOrInitProject()
	if err != nil {
		return task.ProjectView{}, err
	}
	patch, err = task.NormalizeProjectPatch(patch)
	if err != nil {
		return task.ProjectView{}, err
	}
	baseUpdate := projectUpdateFromGraph(project)
	update, err := task.ApplyProjectPatch(baseUpdate, patch)
	if err != nil {
		return task.ProjectView{}, err
	}
	update, err = task.NormalizeProjectUpdate(update)
	if err != nil {
		return task.ProjectView{}, err
	}
	return s.persistProjectMutation(ctx, project, update, source, task.PlanMutationKindPatch, patch.Operations)
}

func (s *Service) persistProjectMutation(
	ctx context.Context,
	existing task.Project,
	update task.ProjectUpdate,
	source string,
	mutationKind string,
	patchOps []task.ProjectPatchOperation,
) (task.ProjectView, error) {
	if err := s.validateProjectTaskBindings(update); err != nil {
		return task.ProjectView{}, err
	}
	now := task.Now()
	steps := make([]task.ProjectStep, 0, len(update.Steps))
	for _, step := range update.Steps {
		steps = append(steps, task.ProjectStep{
			ID:           step.ID,
			ParentStepID: step.ParentStepID,
			DependsOn:    append([]string(nil), step.DependsOn...),
			Priority:     step.Priority,
			Title:        step.Title,
			Status:       step.Status,
			BranchID:     step.BranchID,
			TaskID:       step.TaskID,
			Notes:        step.Notes,
			UpdatedAt:    now,
		})
	}
	branches := make([]task.ProjectBranch, 0, len(update.Branches))
	for _, branch := range update.Branches {
		branches = append(branches, task.ProjectBranch{
			ID:        branch.ID,
			Title:     branch.Title,
			Status:    branch.Status,
			TaskID:    branch.TaskID,
			Notes:     branch.Notes,
			UpdatedAt: now,
		})
	}
	currentStepID, readyStepIDs, blockedStepIDs := projectGraphState(steps)
	activeBranchIDs := projectActiveBranchIDs(branches)
	project := task.Project{
		SchemaVersion:   task.SchemaVersion,
		WorkspaceRoot:   firstNonEmpty(existing.WorkspaceRoot, s.Store.WorkspaceRoot),
		UpdatedAt:       now,
		Revision:        existing.Revision + 1,
		Explanation:     update.Explanation,
		CurrentStepID:   currentStepID,
		ReadyStepIDs:    readyStepIDs,
		BlockedStepIDs:  blockedStepIDs,
		ActiveBranchIDs: activeBranchIDs,
		LastMutationRef: artifact.ProjectMutationRef(task.NewID("PRJUP")),
		Steps:           steps,
		Branches:        branches,
	}
	mutationID := strings.TrimPrefix(project.LastMutationRef, "project_updates.jsonl#mutation_id=")
	if err := s.Store.SaveProject(project); err != nil {
		return task.ProjectView{}, err
	}
	if err := s.Store.AppendProjectMutation(task.ProjectMutationRecord{
		SchemaVersion:   task.SchemaVersion,
		MutationID:      mutationID,
		Revision:        project.Revision,
		MutationKind:    mutationKind,
		Source:          normalizedMutationSource(source),
		TS:              now,
		Explanation:     project.Explanation,
		CurrentStepID:   project.CurrentStepID,
		ReadyStepIDs:    append([]string(nil), project.ReadyStepIDs...),
		BlockedStepIDs:  append([]string(nil), project.BlockedStepIDs...),
		ActiveBranchIDs: append([]string(nil), project.ActiveBranchIDs...),
		Steps:           append([]task.ProjectStep(nil), project.Steps...),
		Branches:        append([]task.ProjectBranch(nil), project.Branches...),
		PatchOperations: cloneProjectPatchOperations(patchOps),
	}); err != nil {
		return task.ProjectView{}, err
	}
	return s.GetProject(ctx)
}

func (s *Service) validateProjectTaskBindings(update task.ProjectUpdate) error {
	seen := make(map[string]struct{})
	for _, step := range update.Steps {
		taskID := strings.TrimSpace(step.TaskID)
		if taskID == "" {
			continue
		}
		seen[taskID] = struct{}{}
	}
	for _, branch := range update.Branches {
		taskID := strings.TrimSpace(branch.TaskID)
		if taskID == "" {
			continue
		}
		seen[taskID] = struct{}{}
	}
	for taskID := range seen {
		if _, err := s.Store.LoadTask(taskID); err != nil {
			return fmt.Errorf("project task binding references unknown task %s", taskID)
		}
	}
	return nil
}

func (s *Service) loadOrInitProject() (task.Project, error) {
	if err := s.Store.EnsureWorkspaceLayout(); err != nil {
		return task.Project{}, err
	}
	project, err := s.Store.LoadProject()
	if err == nil {
		if strings.TrimSpace(project.WorkspaceRoot) == "" {
			project.WorkspaceRoot = s.Store.WorkspaceRoot
		}
		return project, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return task.Project{}, err
	}
	project = task.NewProject(s.Store.WorkspaceRoot)
	if err := s.Store.SaveProject(project); err != nil {
		return task.Project{}, err
	}
	return project, nil
}

func (s *Service) refreshProject(project task.Project) (task.Project, error) {
	now := task.Now()
	changed := false
	for idx := range project.Steps {
		step := project.Steps[idx]
		if strings.TrimSpace(step.TaskID) == "" {
			continue
		}
		spec, state, hasHandoff, loadErr := s.projectTaskBinding(step.TaskID)
		nextStatus := task.ProjectStepStatusBlocked
		if loadErr == nil {
			nextStatus = projectStepStatusFromTaskState(state.State)
		}
		if step.Status != nextStatus {
			project.Steps[idx].Status = nextStatus
			project.Steps[idx].UpdatedAt = latestTimestamp(project.Steps[idx].UpdatedAt, state.UpdatedAt, now)
			changed = true
		}
		if loadErr == nil {
			if strings.TrimSpace(project.Steps[idx].UpdatedAt) == "" {
				project.Steps[idx].UpdatedAt = latestTimestamp(state.UpdatedAt, now)
				changed = true
			}
			if hasHandoff {
				_ = spec
			}
		}
	}
	for idx := range project.Branches {
		branch := project.Branches[idx]
		if strings.TrimSpace(branch.TaskID) == "" {
			continue
		}
		spec, state, hasHandoff, loadErr := s.projectTaskBinding(branch.TaskID)
		next := branch
		if loadErr != nil {
			next.Status = task.ProjectBranchStatusBlocked
			next.LastReasonCode = "missing_task"
			next.TaskRef = ""
			next.StatusRef = ""
			next.HandoffRef = ""
			next.WorkspaceRoot = ""
		} else {
			next.Status = projectBranchStatusFromTaskState(state.State)
			next.TaskRef = filepath.ToSlash(filepath.Join("tasks", branch.TaskID, "task.json"))
			next.StatusRef = filepath.ToSlash(filepath.Join("tasks", branch.TaskID, "state.json"))
			if hasHandoff {
				next.HandoffRef = filepath.ToSlash(filepath.Join("tasks", branch.TaskID, "handoff.md"))
			} else {
				next.HandoffRef = ""
			}
			next.WorkspaceRoot = spec.WorkspaceRoot
			next.LastReasonCode = state.StatusReasonCode
		}
		next.UpdatedAt = latestTimestamp(branch.UpdatedAt, state.UpdatedAt, now)
		if !projectBranchEqual(branch, next) {
			project.Branches[idx] = next
			changed = true
		}
	}
	currentStepID, readyStepIDs, blockedStepIDs := projectGraphState(project.Steps)
	activeBranchIDs := projectActiveBranchIDs(project.Branches)
	if project.CurrentStepID != currentStepID ||
		!equalStringSlices(project.ReadyStepIDs, readyStepIDs) ||
		!equalStringSlices(project.BlockedStepIDs, blockedStepIDs) ||
		!equalStringSlices(project.ActiveBranchIDs, activeBranchIDs) {
		project.CurrentStepID = currentStepID
		project.ReadyStepIDs = readyStepIDs
		project.BlockedStepIDs = blockedStepIDs
		project.ActiveBranchIDs = activeBranchIDs
		project.UpdatedAt = latestTimestamp(project.UpdatedAt, now)
		changed = true
	}
	if changed {
		if strings.TrimSpace(project.WorkspaceRoot) == "" {
			project.WorkspaceRoot = s.Store.WorkspaceRoot
		}
		if err := s.Store.SaveProject(project); err != nil {
			return task.Project{}, err
		}
	}
	return project, nil
}

func (s *Service) projectTaskBinding(taskID string) (task.Spec, task.State, bool, error) {
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.Spec{}, task.State{}, false, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.Spec{}, task.State{}, false, err
	}
	return spec, state, s.Store.HandoffExists(taskID), nil
}

func (s *Service) TrackTaskInProject(ctx context.Context, spec task.Spec) error {
	state, err := s.loadStateOrRecover(spec.TaskID)
	if err != nil {
		return err
	}
	project, err := s.loadOrInitProject()
	if err != nil {
		return err
	}
	update := projectUpdateFromGraph(project)
	stepID := projectTaskStepID(spec.TaskID)
	branchID := projectTaskBranchID(spec.TaskID)
	step := task.ProjectExecutionStep{
		ID:       stepID,
		Title:    projectTaskTitle(spec),
		Status:   projectStepStatusFromTaskState(state.State),
		Priority: task.StepPriorityHigh,
		BranchID: branchID,
		TaskID:   spec.TaskID,
		Notes:    projectTaskNotes(spec),
	}
	branch := task.ProjectBranchSpec{
		ID:     branchID,
		Title:  projectTaskTitle(spec),
		Status: projectBranchStatusFromTaskState(state.State),
		TaskID: spec.TaskID,
		Notes:  projectTaskBranchNotes(spec),
	}
	if idx := projectUpdateStepIndex(update.Steps, stepID); idx >= 0 {
		update.Steps[idx] = step
	} else {
		update.Steps = append(update.Steps, step)
	}
	if idx := projectUpdateBranchIndex(update.Branches, branchID); idx >= 0 {
		update.Branches[idx] = branch
	} else {
		update.Branches = append(update.Branches, branch)
	}
	update.Explanation = firstNonEmpty(update.Explanation, "Workspace-level project graph tracking all durable ngen tasks.")
	_, err = s.persistProjectMutation(ctx, project, update, task.StepSourceSystem, task.PlanMutationKindReplace, nil)
	return err
}

func projectUpdateFromGraph(project task.Project) task.ProjectUpdate {
	update := task.ProjectUpdate{
		Explanation: strings.TrimSpace(project.Explanation),
		Steps:       make([]task.ProjectExecutionStep, 0, len(project.Steps)),
		Branches:    make([]task.ProjectBranchSpec, 0, len(project.Branches)),
	}
	for _, step := range project.Steps {
		update.Steps = append(update.Steps, task.ProjectExecutionStep{
			ID:           step.ID,
			ParentStepID: step.ParentStepID,
			DependsOn:    append([]string(nil), step.DependsOn...),
			Priority:     step.Priority,
			Title:        step.Title,
			Status:       step.Status,
			BranchID:     step.BranchID,
			TaskID:       step.TaskID,
			Notes:        step.Notes,
		})
	}
	for _, branch := range project.Branches {
		update.Branches = append(update.Branches, task.ProjectBranchSpec{
			ID:     branch.ID,
			Title:  branch.Title,
			Status: branch.Status,
			TaskID: branch.TaskID,
			Notes:  branch.Notes,
		})
	}
	return update
}

func cloneProjectPatchOperations(ops []task.ProjectPatchOperation) []task.ProjectPatchOperation {
	out := make([]task.ProjectPatchOperation, 0, len(ops))
	for _, op := range ops {
		cloned := task.ProjectPatchOperation{
			Op:           op.Op,
			Explanation:  op.Explanation,
			StepID:       op.StepID,
			AfterStepID:  op.AfterStepID,
			BranchID:     op.BranchID,
			ParentStepID: op.ParentStepID,
			TaskID:       op.TaskID,
			DependsOn:    append([]string(nil), op.DependsOn...),
			Status:       op.Status,
		}
		if op.Step != nil {
			step := *op.Step
			step.DependsOn = append([]string(nil), step.DependsOn...)
			cloned.Step = &step
		}
		if op.Branch != nil {
			branch := *op.Branch
			cloned.Branch = &branch
		}
		out = append(out, cloned)
	}
	return out
}

func normalizedMutationSource(source string) string {
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		return task.StepSourceOperator
	}
	return source
}

func projectTaskStepID(taskID string) string {
	return "task:" + strings.TrimSpace(taskID)
}

func projectTaskBranchID(taskID string) string {
	return "branch:" + strings.TrimSpace(taskID)
}

func projectTaskTitle(spec task.Spec) string {
	return firstNonEmpty(strings.TrimSpace(spec.Title), strings.TrimSpace(spec.Objective), spec.TaskID)
}

func projectTaskNotes(spec task.Spec) string {
	if strings.TrimSpace(spec.ParentTaskID) != "" {
		return fmt.Sprintf("Auto-tracked child task for %s.", spec.ParentTaskID)
	}
	return "Auto-tracked durable root task."
}

func projectTaskBranchNotes(spec task.Spec) string {
	if strings.TrimSpace(spec.ParentTaskID) != "" {
		return fmt.Sprintf("Auto-tracked execution branch for child task %s.", spec.TaskID)
	}
	return fmt.Sprintf("Auto-tracked execution branch for task %s.", spec.TaskID)
}

func projectUpdateStepIndex(steps []task.ProjectExecutionStep, stepID string) int {
	for idx, step := range steps {
		if strings.TrimSpace(step.ID) == strings.TrimSpace(stepID) {
			return idx
		}
	}
	return -1
}

func projectUpdateBranchIndex(branches []task.ProjectBranchSpec, branchID string) int {
	for idx, branch := range branches {
		if strings.TrimSpace(branch.ID) == strings.TrimSpace(branchID) {
			return idx
		}
	}
	return -1
}

func projectStepStatusFromTaskState(state task.StateName) string {
	switch state {
	case task.StateDone:
		return task.ProjectStepStatusCompleted
	case task.StateFailed, task.StateAborted:
		return task.ProjectStepStatusCancelled
	case task.StateBlocked, task.StateWaiting:
		return task.ProjectStepStatusBlocked
	default:
		return task.ProjectStepStatusInProgress
	}
}

func projectBranchStatusFromTaskState(state task.StateName) string {
	switch state {
	case task.StateDone:
		return task.ProjectBranchStatusCompleted
	case task.StateFailed, task.StateAborted:
		return task.ProjectBranchStatusCancelled
	case task.StateBlocked, task.StateWaiting:
		return task.ProjectBranchStatusBlocked
	default:
		return task.ProjectBranchStatusActive
	}
}

func projectGraphState(steps []task.ProjectStep) (string, []string, []string) {
	statusByID := make(map[string]string, len(steps))
	for _, step := range steps {
		statusByID[step.ID] = strings.TrimSpace(strings.ToLower(step.Status))
	}
	var currentStepID string
	var readyStepIDs []string
	var blockedStepIDs []string
	for _, step := range steps {
		switch strings.TrimSpace(strings.ToLower(step.Status)) {
		case task.ProjectStepStatusCompleted, task.ProjectStepStatusCancelled:
			continue
		case task.ProjectStepStatusInProgress:
			if currentStepID == "" {
				currentStepID = step.ID
			}
			continue
		case task.ProjectStepStatusBlocked:
			blockedStepIDs = append(blockedStepIDs, step.ID)
			continue
		}
		if projectStepDepsSatisfied(step, statusByID) {
			readyStepIDs = append(readyStepIDs, step.ID)
			if currentStepID == "" {
				currentStepID = step.ID
			}
		} else {
			blockedStepIDs = append(blockedStepIDs, step.ID)
		}
	}
	return currentStepID, readyStepIDs, blockedStepIDs
}

func projectStepDepsSatisfied(step task.ProjectStep, statusByID map[string]string) bool {
	for _, dep := range step.DependsOn {
		status := statusByID[dep]
		if status != task.ProjectStepStatusCompleted && status != task.ProjectStepStatusCancelled {
			return false
		}
	}
	return true
}

func projectActiveBranchIDs(branches []task.ProjectBranch) []string {
	active := make([]string, 0, len(branches))
	for _, branch := range branches {
		if strings.EqualFold(strings.TrimSpace(branch.Status), task.ProjectBranchStatusActive) {
			active = append(active, branch.ID)
		}
	}
	return active
}

func projectBranchEqual(a, b task.ProjectBranch) bool {
	return a.ID == b.ID &&
		a.Title == b.Title &&
		a.Status == b.Status &&
		a.TaskID == b.TaskID &&
		a.TaskRef == b.TaskRef &&
		a.StatusRef == b.StatusRef &&
		a.HandoffRef == b.HandoffRef &&
		a.WorkspaceRoot == b.WorkspaceRoot &&
		a.LastReasonCode == b.LastReasonCode &&
		a.Notes == b.Notes &&
		a.UpdatedAt == b.UpdatedAt
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for idx := range a {
		if a[idx] != b[idx] {
			return false
		}
	}
	return true
}

func (s *Service) PromptSession(ctx context.Context, sessionID, message string) (task.Session, task.StatusSnapshot, []task.Event, error) {
	session, err := s.Store.LoadSession(sessionID)
	if err != nil {
		return task.Session{}, task.StatusSnapshot{}, nil, err
	}
	if err := s.refineTUITaskFromFirstPrompt(ctx, session, message); err != nil {
		return task.Session{}, task.StatusSnapshot{}, nil, err
	}
	msg := task.SessionMessage{
		SchemaVersion: task.SchemaVersion,
		MessageID:     task.NewID("MSG"),
		SessionID:     sessionID,
		TaskID:        session.TaskID,
		Role:          "operator",
		Content:       message,
		TS:            task.Now(),
	}
	if err := s.Store.AppendSessionMessage(msg); err != nil {
		return task.Session{}, task.StatusSnapshot{}, nil, err
	}
	session.LastPrompt = message
	session.UpdatedAt = task.Now()
	if err := s.Store.SaveSession(session); err != nil {
		return task.Session{}, task.StatusSnapshot{}, nil, err
	}
	if missionPrompt, ok := parseMissionSlashPrompt(message); ok {
		view, err := s.OpenOrSetMissionForTask(ctx, session.TaskID, task.MissionCreateRequest{
			Title:     missionTitleFromObjective(missionPrompt.Objective),
			Objective: missionPrompt.Objective,
			Criteria:  missionCriteriaFromSlashPrompt(missionPrompt),
		})
		if err != nil {
			return task.Session{}, task.StatusSnapshot{}, nil, err
		}
		state, err := s.loadStateOrRecover(session.TaskID)
		if err != nil {
			return task.Session{}, task.StatusSnapshot{}, nil, err
		}
		eventType := "mission_opened"
		eventSummary := fmt.Sprintf("Opened mission %s.", view.Mission.MissionID)
		if strings.TrimSpace(missionPrompt.Objective) != "" {
			eventType = "mission_updated"
			eventSummary = fmt.Sprintf("Updated mission %s from /%s prompt.", view.Mission.MissionID, missionPrompt.Command)
		}
		event := newEvent(session.TaskID, state, eventType, eventSummary, []string{missionWorkspaceRef(view.Mission.MissionID, "mission.json"), missionWorkspaceRef(view.Mission.MissionID, "validation_contract.json")})
		if err := s.Store.AppendEvent(event); err != nil {
			return task.Session{}, task.StatusSnapshot{}, nil, err
		}
		state.LastEventRef = artifact.EventRef(event.EventID)
		state.UpdatedAt = task.Now()
		if err := s.Store.SaveState(state); err != nil {
			return task.Session{}, task.StatusSnapshot{}, nil, err
		}
		snapshot, err := s.Status(ctx, session.TaskID)
		if err != nil {
			return task.Session{}, task.StatusSnapshot{}, nil, err
		}
		session.LastAction = event.Type
		session.UpdatedAt = task.Now()
		if err := s.Store.SaveSession(session); err != nil {
			return task.Session{}, task.StatusSnapshot{}, []task.Event{event}, err
		}
		runtimeMsg := task.SessionMessage{
			SchemaVersion: task.SchemaVersion,
			MessageID:     task.NewID("MSG"),
			SessionID:     sessionID,
			TaskID:        session.TaskID,
			Role:          "runtime",
			Content:       missionSlashRuntimeMessage(view, missionPrompt),
			TS:            task.Now(),
		}
		if err := s.Store.AppendSessionMessage(runtimeMsg); err != nil {
			return task.Session{}, task.StatusSnapshot{}, []task.Event{event}, err
		}
		return session, snapshot, []task.Event{event}, nil
	}
	snapshot, events, runErr := s.auto(ctx, session.TaskID, &session)
	if snapshot.TaskID == "" {
		if status, statusErr := s.Status(ctx, session.TaskID); statusErr == nil {
			snapshot = status
		}
	}
	if len(events) > 0 {
		session.LastAction = events[len(events)-1].Type
	}
	session.UpdatedAt = task.Now()
	if err := s.Store.SaveSession(session); err != nil {
		return task.Session{}, task.StatusSnapshot{}, events, err
	}
	runtimeMsg := task.SessionMessage{
		SchemaVersion: task.SchemaVersion,
		MessageID:     task.NewID("MSG"),
		SessionID:     sessionID,
		TaskID:        session.TaskID,
		Role:          "runtime",
		Content:       sessionRuntimeContent(snapshot, events, runErr),
		TS:            task.Now(),
	}
	if err := s.Store.AppendSessionMessage(runtimeMsg); err != nil {
		return task.Session{}, task.StatusSnapshot{}, events, err
	}
	if runErr != nil {
		return session, snapshot, events, runErr
	}
	return session, snapshot, events, nil
}

func (s *Service) CancelSession(ctx context.Context, sessionID string) (task.Session, error) {
	session, err := s.Store.LoadSession(sessionID)
	if err != nil {
		return task.Session{}, err
	}
	if _, err := s.Abort(ctx, session.TaskID, "session cancelled by operator"); err != nil {
		return task.Session{}, err
	}
	session.Status = "cancelled"
	session.LastAction = "aborted"
	session.UpdatedAt = task.Now()
	if err := s.Store.SaveSession(session); err != nil {
		return task.Session{}, err
	}
	runtimeMsg := task.SessionMessage{
		SchemaVersion: task.SchemaVersion,
		MessageID:     task.NewID("MSG"),
		SessionID:     sessionID,
		TaskID:        session.TaskID,
		Role:          "runtime",
		Content:       "Session cancelled by operator. Task was aborted.",
		TS:            task.Now(),
	}
	if err := s.Store.AppendSessionMessage(runtimeMsg); err != nil {
		return task.Session{}, err
	}
	return session, nil
}

func (s *Service) Abort(ctx context.Context, taskID, reason string) (task.StatusSnapshot, error) {
	_ = ctx
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.StatusSnapshot{}, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.StatusSnapshot{}, err
	}
	state.State = task.StateAborted
	state.StatusReasonCode = "aborted_user"
	state.StatusDetailRef = ""
	state.UpdatedAt = task.Now()
	event := newEvent(taskID, state, "aborted", reason, nil)
	if err := s.Store.AppendEvent(event); err != nil {
		return task.StatusSnapshot{}, err
	}
	state.LastEventRef = artifact.EventRef(event.EventID)
	if err := s.Store.SaveState(state); err != nil {
		return task.StatusSnapshot{}, err
	}
	if err := s.syncTaskNarrative(spec, state, reason); err != nil {
		return task.StatusSnapshot{}, err
	}
	return s.Status(ctx, taskID)
}

func (s *Service) SpawnWorker(ctx context.Context, parentTaskID, role, objective string) (task.WorkerContract, error) {
	parentSpec, err := s.Store.LoadTask(parentTaskID)
	if err != nil {
		return task.WorkerContract{}, err
	}
	parentSpec = task.HydrateSpec(parentSpec, s.Config)
	parentSpec, err = s.ensureTaskWorkspaceRoot(parentSpec)
	if err != nil {
		return task.WorkerContract{}, err
	}
	workers, err := s.Store.ListWorkerContracts(parentTaskID)
	if err != nil {
		return task.WorkerContract{}, err
	}
	maxWorkers := s.Config.Subagents.MaxWorkersPerTask
	if parentSpec.SubagentPolicy != nil && parentSpec.SubagentPolicy.MaxWorkersPerTask > 0 {
		maxWorkers = parentSpec.SubagentPolicy.MaxWorkersPerTask
	}
	if len(workers) >= maxWorkers {
		return task.WorkerContract{}, fmt.Errorf("max workers exceeded for task %s", parentTaskID)
	}
	role, childKind, childPreset, err := task.NormalizeWorkerRole(role)
	if err != nil {
		return task.WorkerContract{}, err
	}
	if parentSpec.SubagentPolicy != nil {
		if !parentSpec.SubagentPolicy.AllowChildWorkers {
			return task.WorkerContract{}, fmt.Errorf("task %s is not allowed to spawn child workers", parentTaskID)
		}
		if parentSpec.SubagentPolicy.MaxLineageDepth > 0 && parentSpec.LineageDepth >= parentSpec.SubagentPolicy.MaxLineageDepth {
			return task.WorkerContract{}, fmt.Errorf("task %s reached max child lineage depth %d", parentTaskID, parentSpec.SubagentPolicy.MaxLineageDepth)
		}
		if !workerRoleAllowedByPolicy(parentSpec.SubagentPolicy, role) {
			return task.WorkerContract{}, fmt.Errorf("task %s is not allowed to spawn worker role %s", parentTaskID, role)
		}
	}
	parentRoleContract, err := s.roleContractForSpec(parentSpec)
	if err != nil {
		return task.WorkerContract{}, err
	}
	if !task.RoleContractAllowsWorkerRole(parentRoleContract, role) {
		return task.WorkerContract{}, fmt.Errorf("role %s cannot spawn worker role %s", parentRoleContract.RoleID, role)
	}
	childPolicy, err := task.ResolveSubagentRolePolicy(s.Config, role, parentSpec.PermissionModeID)
	if err != nil {
		return task.WorkerContract{}, err
	}
	if parentSpec.SubagentPolicy != nil && parentSpec.SubagentPolicy.MaxLineageDepth > 0 &&
		(childPolicy.MaxLineageDepth == 0 || childPolicy.MaxLineageDepth > parentSpec.SubagentPolicy.MaxLineageDepth) {
		childPolicy.MaxLineageDepth = parentSpec.SubagentPolicy.MaxLineageDepth
	}
	workerID := task.NewID("WKR")
	workspace, err := s.prepareWorkerWorkspace(ctx, parentSpec, workerID, childPolicy.WorkspaceIsolation)
	if err != nil {
		return task.WorkerContract{}, err
	}
	childSpec, err := s.Create(ctx, task.TaskFile{
		Kind:      childKind,
		PresetID:  childPreset,
		Title:     fmt.Sprintf("%s worker for %s", role, parentTaskID),
		Objective: objective,
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "Produce a bounded worker handoff."},
		},
		WorkspaceRoot:    workspace.WorkspaceRoot,
		PermissionModeID: childPolicy.PermissionModeID,
		ParentTaskID:     parentSpec.TaskID,
		ParentWorkerID:   workerID,
		RootTaskID:       parentSpec.RootTaskID,
		LineageDepth:     parentSpec.LineageDepth + 1,
		SubagentPolicy:   &childPolicy,
	})
	if err != nil {
		_ = s.removeWorkerWorkspace(context.Background(), parentSpec.WorkspaceRoot, workspace)
		return task.WorkerContract{}, err
	}
	contract := task.WorkerContract{
		SchemaVersion:  task.SchemaVersion,
		WorkerID:       workerID,
		ParentTaskID:   parentTaskID,
		ChildTaskID:    childSpec.TaskID,
		RootTaskID:     childSpec.RootTaskID,
		LineageDepth:   childSpec.LineageDepth,
		Role:           role,
		Objective:      objective,
		Status:         "active",
		SubagentPolicy: childSpec.SubagentPolicy,
		CreatedAt:      task.Now(),
		UpdatedAt:      task.Now(),
	}
	workspace.ChildTaskID = childSpec.TaskID
	if workspace.EffectiveMode != "shared_workspace" {
		baseline, err := captureWorkerBaseline(parentSpec, workerID, childSpec.TaskID)
		if err != nil {
			_ = s.removeWorkerWorkspace(context.Background(), parentSpec.WorkspaceRoot, workspace)
			return task.WorkerContract{}, err
		}
		if err := s.Store.SaveWorkerBaseline(baseline); err != nil {
			_ = s.removeWorkerWorkspace(context.Background(), parentSpec.WorkspaceRoot, workspace)
			return task.WorkerContract{}, err
		}
		workspace.BaselineRef = artifact.WorkerBaselineRef(workerID)
	}
	workspace.UpdatedAt = task.Now()
	if err := s.Store.SaveWorkerWorkspace(workspace); err != nil {
		_ = s.removeWorkerWorkspace(context.Background(), parentSpec.WorkspaceRoot, workspace)
		return task.WorkerContract{}, err
	}
	childStatus, err := s.Status(ctx, childSpec.TaskID)
	if err != nil {
		_ = s.removeWorkerWorkspace(context.Background(), parentSpec.WorkspaceRoot, workspace)
		return task.WorkerContract{}, err
	}
	settlement := s.workerSettlementView(contract, childStatus)
	result := s.workerResultView(contract, childStatus, settlement)
	mergeWorkerRuntimeIntoContract(&contract, workspace, settlement, result, task.WorkerReconcile{
		Mode:    workerContractReconcileMode(contract),
		Status:  "pending",
		Summary: workerReconcileSummary("pending", 0, 0, 0),
	})
	contract, _, _, _, _, err = s.ensureWorkerRuntimeArtifacts(ctx, parentSpec, contract, childStatus)
	if err != nil {
		_ = s.removeWorkerWorkspace(context.Background(), parentSpec.WorkspaceRoot, workspace)
		return task.WorkerContract{}, err
	}
	parentState, err := s.loadStateOrRecover(parentTaskID)
	if err == nil {
		if workspace.EffectiveMode != "shared_workspace" {
			event := newEvent(parentTaskID, parentState, "worker_workspace_prepared", fmt.Sprintf("Prepared %s child workspace for worker %s.", workspace.EffectiveMode, contract.WorkerID), []string{contract.WorkspaceRef})
			if err := s.Store.AppendEvent(event); err == nil {
				parentState.LastEventRef = artifact.EventRef(event.EventID)
				parentState.UpdatedAt = task.Now()
				_ = s.Store.SaveState(parentState)
			}
		}
		event := newEvent(parentTaskID, parentState, "worker_spawned", fmt.Sprintf("Spawned %s worker %s.", role, contract.WorkerID), []string{filepath.ToSlash(filepath.Join("workers", contract.WorkerID+".json"))})
		if err := s.Store.AppendEvent(event); err == nil {
			parentState.LastEventRef = artifact.EventRef(event.EventID)
			parentState.UpdatedAt = task.Now()
			_ = s.Store.SaveState(parentState)
		}
	}
	return contract, nil
}

func (s *Service) ListWorkers(ctx context.Context, parentTaskID string) ([]task.WorkerContract, error) {
	workers, _, err := s.managedWorkerContext(ctx, parentTaskID)
	return workers, err
}

func (s *Service) WorkerSnapshot(ctx context.Context, parentTaskID, workerID string) (task.WorkerSnapshot, error) {
	contract, err := s.Store.LoadWorkerContract(parentTaskID, workerID)
	if err != nil {
		return task.WorkerSnapshot{}, err
	}
	return s.workerSnapshot(ctx, contract)
}

func (s *Service) ListWorkerSnapshots(ctx context.Context, parentTaskID string) ([]task.WorkerSnapshot, error) {
	contracts, err := s.Store.ListWorkerContracts(parentTaskID)
	if err != nil {
		return nil, err
	}
	snapshots := make([]task.WorkerSnapshot, 0, len(contracts))
	for _, contract := range contracts {
		snapshot, err := s.workerSnapshot(ctx, contract)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (s *Service) SyncWorker(ctx context.Context, parentTaskID, workerID string) (task.WorkerContract, error) {
	contract, err := s.Store.LoadWorkerContract(parentTaskID, workerID)
	if err != nil {
		return task.WorkerContract{}, err
	}
	parentSpec, err := s.Store.LoadTask(parentTaskID)
	if err != nil {
		return task.WorkerContract{}, err
	}
	updated, childStatus, _, err := s.resolveWorkerControl(ctx, contract)
	if err != nil {
		return task.WorkerContract{}, err
	}
	previous := contract
	updated, _, settlement, reconcile, releaseSummary, err := s.ensureWorkerRuntimeArtifacts(ctx, parentSpec, updated, childStatus)
	if err != nil {
		return task.WorkerContract{}, err
	}
	if err := s.appendWorkerReconcileEvents(parentTaskID, previous, updated, settlement, reconcile, releaseSummary); err != nil {
		return task.WorkerContract{}, err
	}
	return updated, nil
}

func (s *Service) ContinueWorker(ctx context.Context, parentTaskID, workerID string) (task.WorkerContract, error) {
	contract, err := s.Store.LoadWorkerContract(parentTaskID, workerID)
	if err != nil {
		return task.WorkerContract{}, err
	}
	childSpec, err := s.Store.LoadTask(contract.ChildTaskID)
	if err != nil {
		return task.WorkerContract{}, err
	}
	childSpec = task.HydrateSpec(childSpec, s.Config)
	updated, childStatus, _, err := s.resolveWorkerControl(ctx, contract)
	if err != nil {
		return task.WorkerContract{}, err
	}
	if updated.ParentActionType == "owned_approval_pending" {
		return updated, fmt.Errorf("worker %s is blocked on owned approval %s; resolve it before continuing", workerID, updated.ApprovalID)
	}
	if childStatus.State == task.StateBlocked {
		return updated, fmt.Errorf("worker %s is blocked: %s", workerID, childStatus.StatusReasonCode)
	}
	switch childStatus.State {
	case task.StateDone:
		return s.SyncWorker(ctx, parentTaskID, workerID)
	case task.StateWaiting:
		return updated, fmt.Errorf("worker %s is waiting: %s", workerID, childStatus.StatusReasonCode)
	case task.StateFailed, task.StateAborted:
		return updated, fmt.Errorf("worker %s is not continuable: %s", workerID, childStatus.State)
	case task.StateActive:
		runner := s
		if mission, ok, routeErr := s.missionForOwnedTaskSpec(childSpec); routeErr != nil {
			return task.WorkerContract{}, routeErr
		} else if ok {
			scoped, _, scopedErr := s.serviceForMissionRole(mission, task.MissionRoleWorkers)
			if scopedErr != nil {
				return task.WorkerContract{}, scopedErr
			}
			runner = scoped
		}
		if _, _, err := runner.Resume(ctx, contract.ChildTaskID); err != nil {
			return task.WorkerContract{}, err
		}
		updated.ContinuationCount++
		updated.LastContinuedAt = task.Now()
		updated.UpdatedAt = latestTimestamp(updated.UpdatedAt, updated.LastContinuedAt)
		if err := s.Store.SaveWorkerContract(updated); err != nil {
			return task.WorkerContract{}, err
		}
	default:
		return updated, fmt.Errorf("worker %s is not ready to continue: %s", workerID, childStatus.State)
	}
	if err := s.recordWorkerContinuation(parentTaskID, workerID, updated.ApprovalID); err != nil {
		return task.WorkerContract{}, err
	}
	return s.SyncWorker(ctx, parentTaskID, workerID)
}

func (s *Service) workerSnapshot(ctx context.Context, contract task.WorkerContract) (task.WorkerSnapshot, error) {
	parentStatus, err := s.Status(ctx, contract.ParentTaskID)
	if err != nil {
		return task.WorkerSnapshot{}, err
	}
	derived, childStatus, _, err := s.resolveWorkerControl(ctx, contract)
	if err != nil {
		return task.WorkerSnapshot{}, err
	}
	return task.WorkerSnapshot{
		ObjectKind:    "worker_snapshot",
		SchemaVersion: task.SchemaVersion,
		Worker:        derived,
		ParentStatus:  parentStatus,
		ChildStatus:   childStatus,
		UpdatedAt:     latestTimestamp(derived.UpdatedAt, parentStatus.UpdatedAt),
	}, nil
}

func (s *Service) managedWorkerContext(ctx context.Context, parentTaskID string) ([]task.WorkerContract, []task.OwnedApprovalSummary, error) {
	contracts, err := s.Store.ListWorkerContracts(parentTaskID)
	if err != nil {
		return nil, nil, err
	}
	managed := make([]task.WorkerContract, 0, len(contracts))
	var ownedPending []task.OwnedApprovalSummary
	for _, contract := range contracts {
		updated, _, pending, err := s.resolveWorkerControl(ctx, contract)
		if err != nil {
			return nil, nil, err
		}
		managed = append(managed, updated)
		ownedPending = append(ownedPending, pending...)
	}
	return managed, ownedPending, nil
}

func (s *Service) resolveWorkerControl(ctx context.Context, contract task.WorkerContract) (task.WorkerContract, task.StatusSnapshot, []task.OwnedApprovalSummary, error) {
	childStatus, err := s.Status(ctx, contract.ChildTaskID)
	if err != nil {
		return task.WorkerContract{}, task.StatusSnapshot{}, nil, err
	}
	childSpec, err := s.Store.LoadTask(contract.ChildTaskID)
	if err != nil {
		return task.WorkerContract{}, task.StatusSnapshot{}, nil, err
	}
	childSpec = task.HydrateSpec(childSpec, s.Config)
	parentSpec, err := s.Store.LoadTask(contract.ParentTaskID)
	if err != nil {
		return task.WorkerContract{}, task.StatusSnapshot{}, nil, err
	}
	parentSpec = task.HydrateSpec(parentSpec, s.Config)
	updated := contract
	updated.Status = strings.ToLower(string(childStatus.State))
	updated.RootTaskID = childSpec.RootTaskID
	updated.LineageDepth = childSpec.LineageDepth
	updated.SubagentPolicy = childSpec.SubagentPolicy
	if childStatus.HandoffRef != "" {
		updated.HandoffRef = filepath.ToSlash(filepath.Join("..", contract.ChildTaskID, childStatus.HandoffRef))
	}
	updated.UpdatedAt = latestTimestamp(contract.UpdatedAt, childStatus.UpdatedAt)
	updated.BlockedReasonCode = childStatus.StatusReasonCode
	updated.BlockedDetailRef = childStatus.StatusDetailRef
	updated.ApprovalID = ""
	updated.ApprovalRef = ""
	updated.ApprovalStatus = ""
	updated.ApprovalScope = ""
	updated.ApprovalReason = ""
	updated.InputRequestID = ""
	updated.InputRequestRef = ""
	updated.InputField = ""
	updated.InputPrompt = ""
	updated.RequiresParentAction = false
	updated.ParentActionType = ""
	updated.ParentActionOptions = nil
	updated.ParentActionSummary = ""

	records, err := s.Store.ReadApprovals(contract.ChildTaskID)
	if err != nil && !os.IsNotExist(err) {
		return task.WorkerContract{}, task.StatusSnapshot{}, nil, err
	}
	if record, ok := approvalRecordByRefOrID(records, childStatus.StatusDetailRef, ""); ok {
		applyWorkerApprovalRecord(&updated, record)
	}
	ownedApprovals := latestOwnedApprovals(records, contract.ParentTaskID, contract.WorkerID)
	pendingCount := 0
	var latestOwned task.ApprovalRecord
	var hasLatestOwned bool
	var latestPending task.ApprovalRecord
	var hasLatestPending bool
	for _, record := range ownedApprovals {
		latestOwned = record
		hasLatestOwned = true
		if record.Status == "pending" {
			pendingCount++
			latestPending = record
			hasLatestPending = true
		}
	}
	pendingSummaries := make([]task.OwnedApprovalSummary, 0, pendingCount)
	for _, record := range ownedApprovals {
		if record.Status != "pending" {
			continue
		}
		pendingSummaries = append(pendingSummaries, buildOwnedPendingApprovalSummary(contract, childStatus, record, pendingCount))
	}

	switch {
	case hasLatestPending:
		applyWorkerApprovalRecord(&updated, latestPending)
		updated.RequiresParentAction = true
		updated.ParentActionType = "owned_approval_pending"
		updated.ParentActionOptions = []string{"approve", "deny", "parent_takeover"}
		updated.ParentActionSummary = pendingApprovalActionSummary(contract, latestPending, pendingCount)
	case hasLatestOwned && latestOwned.Status == "approved" && childStatus.State == task.StateActive:
		applyWorkerApprovalRecord(&updated, latestOwned)
		updated.RequiresParentAction = true
		updated.ParentActionType = "continue_child"
		updated.ParentActionOptions = []string{"worker_continue"}
		updated.ParentActionSummary = continuationActionSummary(contract, latestOwned.ApprovalID)
	case hasLatestOwned && latestOwned.Status == "denied" && childStatus.State == task.StateBlocked && childStatus.StatusReasonCode == "blocked_policy":
		applyWorkerApprovalRecord(&updated, latestOwned)
		updated.RequiresParentAction = true
		updated.ParentActionType = "parent_takeover"
		updated.ParentActionOptions = []string{"parent_takeover", "inspect_child"}
		updated.ParentActionSummary = fmt.Sprintf("Worker %s approval %s was denied. Parent must take over or adjust the child plan directly.", contract.WorkerID, latestOwned.ApprovalID)
	case childStatus.State == task.StateBlocked:
		updated.RequiresParentAction = true
		updated.ParentActionType = "inspect_child"
		updated.ParentActionOptions = []string{"inspect_child"}
		updated.ParentActionSummary = genericBlockedWorkerSummary(contract, childStatus)
	}
	if childStatus.StatusReasonCode == "blocked_missing_input" {
		if inputRecord, ok := s.inputRequestRecordByRefOrPending(contract.ChildTaskID, childStatus.StatusDetailRef); ok {
			applyWorkerInputRequest(&updated, inputRecord)
		}
	}
	reconcile, err := s.Store.LoadWorkerReconcile(contract.ParentTaskID, contract.WorkerID)
	if err != nil {
		reconcile = task.WorkerReconcile{
			SchemaVersion: task.SchemaVersion,
			ReconcileID:   task.NewID("REC"),
			WorkerID:      contract.WorkerID,
			ParentTaskID:  contract.ParentTaskID,
			ChildTaskID:   contract.ChildTaskID,
			Role:          contract.Role,
			Mode:          workerContractReconcileMode(updated),
			Status:        "pending",
			Summary:       workerReconcileSummary("pending", 0, 0, 0),
			CreatedAt:     task.Now(),
			UpdatedAt:     task.Now(),
		}
	}
	settlement := s.workerSettlementView(updated, childStatus)
	result := s.workerResultView(updated, childStatus, settlement)
	mergeWorkerRuntimeIntoContract(&updated, s.workerWorkspaceView(parentSpec, updated), settlement, result, reconcile)
	return updated, childStatus, pendingSummaries, nil
}

func latestOwnedApprovals(records []task.ApprovalRecord, ownerTaskID, ownerWorkerID string) []task.ApprovalRecord {
	latest := make(map[string]task.ApprovalRecord)
	order := make([]string, 0, len(records))
	for _, record := range records {
		if record.OwnerTaskID != ownerTaskID || record.OwnerWorkerID != ownerWorkerID {
			continue
		}
		if _, ok := latest[record.ApprovalID]; !ok {
			order = append(order, record.ApprovalID)
		}
		latest[record.ApprovalID] = record
	}
	out := make([]task.ApprovalRecord, 0, len(order))
	for _, approvalID := range order {
		out = append(out, latest[approvalID])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TS == out[j].TS {
			return out[i].ApprovalRecordID < out[j].ApprovalRecordID
		}
		return out[i].TS < out[j].TS
	})
	return out
}

func applyWorkerApprovalRecord(contract *task.WorkerContract, record task.ApprovalRecord) {
	contract.ApprovalID = record.ApprovalID
	contract.ApprovalRef = artifact.ApprovalRecordRef(record.ApprovalRecordID)
	contract.ApprovalStatus = record.Status
	contract.ApprovalScope = strings.TrimSpace(record.Scope)
	contract.ApprovalReason = strings.TrimSpace(record.Reason)
}

func applyWorkerInputRequest(contract *task.WorkerContract, record task.InputRequestRecord) {
	contract.InputRequestID = record.RequestID
	contract.InputRequestRef = artifact.InputRequestRecordRef(record.InputRecordID)
	contract.InputField = strings.TrimSpace(record.Field)
	contract.InputPrompt = strings.TrimSpace(record.Prompt)
}

func approvalRecordByRefOrID(records []task.ApprovalRecord, approvalRef, approvalID string) (task.ApprovalRecord, bool) {
	recordID := recordIDFromArtifactRef(approvalRef, "approvals.jsonl#approval_record_id=")
	for i := len(records) - 1; i >= 0; i-- {
		if recordID != "" && records[i].ApprovalRecordID == recordID {
			return records[i], true
		}
		if approvalID != "" && records[i].ApprovalID == approvalID {
			return records[i], true
		}
	}
	return task.ApprovalRecord{}, false
}

func (s *Service) inputRequestRecordByRefOrPending(taskID, inputRef string) (task.InputRequestRecord, bool) {
	records, err := s.Store.ReadInputRequests(taskID)
	if err != nil {
		return task.InputRequestRecord{}, false
	}
	recordID := recordIDFromArtifactRef(inputRef, "input_requests.jsonl#input_record_id=")
	for i := len(records) - 1; i >= 0; i-- {
		if recordID != "" && records[i].InputRecordID == recordID {
			return records[i], true
		}
	}
	if pending, ok, err := s.pendingInputRequest(taskID); err == nil && ok {
		return pending, true
	}
	return task.InputRequestRecord{}, false
}

func recordIDFromArtifactRef(ref, prefix string) string {
	if prefix == "" {
		return ""
	}
	if !strings.HasPrefix(ref, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(ref, prefix))
}

func buildOwnedPendingApprovalSummary(contract task.WorkerContract, childStatus task.StatusSnapshot, record task.ApprovalRecord, pendingCount int) task.OwnedApprovalSummary {
	return task.OwnedApprovalSummary{
		SchemaVersion:        task.SchemaVersion,
		WorkerID:             contract.WorkerID,
		ChildTaskID:          contract.ChildTaskID,
		ApprovalID:           record.ApprovalID,
		ApprovalRef:          artifact.ApprovalRecordRef(record.ApprovalRecordID),
		Status:               record.Status,
		Scope:                record.Scope,
		Reason:               record.Reason,
		ChildState:           childStatus.State,
		BlockedReasonCode:    childStatus.StatusReasonCode,
		RequiresParentAction: true,
		ParentActionType:     "owned_approval_pending",
		ParentActionOptions:  []string{"approve", "deny", "parent_takeover"},
		ParentActionSummary:  pendingApprovalActionSummary(contract, record, pendingCount),
	}
}

func pendingApprovalActionSummary(contract task.WorkerContract, record task.ApprovalRecord, pendingCount int) string {
	summary := fmt.Sprintf(
		"Worker %s is blocked on owned approval %s (%s). Parent can approve, deny, or parent_takeover.",
		contract.WorkerID,
		record.ApprovalID,
		record.Scope,
	)
	if pendingCount > 1 {
		summary = fmt.Sprintf("%s %d approvals are pending for this worker.", summary, pendingCount)
	}
	return summary
}

func continuationActionSummary(contract task.WorkerContract, approvalID string) string {
	return fmt.Sprintf("Worker %s approval %s was approved. Parent should run worker continue to resume the child.", contract.WorkerID, approvalID)
}

func genericBlockedWorkerSummary(contract task.WorkerContract, childStatus task.StatusSnapshot) string {
	switch childStatus.StatusReasonCode {
	case "blocked_missing_input":
		return fmt.Sprintf("Worker %s is blocked on missing input. Parent should inspect the child task and answer the input request directly.", contract.WorkerID)
	case "blocked_review":
		return fmt.Sprintf("Worker %s is blocked on review/completion. Parent should inspect the child task directly.", contract.WorkerID)
	case "blocked_policy":
		return fmt.Sprintf("Worker %s is blocked on policy. Parent should inspect the child task directly.", contract.WorkerID)
	default:
		return fmt.Sprintf("Worker %s is blocked (%s). Parent should inspect the child task directly.", contract.WorkerID, childStatus.StatusReasonCode)
	}
}

func workerRoleAllowedByPolicy(policy *task.SubagentPolicy, role string) bool {
	if policy == nil {
		return true
	}
	if len(policy.AllowedWorkerRoles) == 0 {
		return policy.AllowChildWorkers
	}
	for _, allowedRole := range policy.AllowedWorkerRoles {
		if allowedRole == role {
			return true
		}
	}
	return false
}

func (s *Service) recordWorkerContinuation(parentTaskID, workerID, approvalID string) error {
	state, err := s.loadStateOrRecover(parentTaskID)
	if err != nil {
		return err
	}
	summary := fmt.Sprintf("Continued worker %s.", workerID)
	if strings.TrimSpace(approvalID) != "" {
		summary = fmt.Sprintf("Continued worker %s after approval %s.", workerID, approvalID)
	}
	event := newEvent(parentTaskID, state, "worker_continued", summary, []string{filepath.ToSlash(filepath.Join("workers", workerID+".json"))})
	if err := s.Store.AppendEvent(event); err != nil {
		return err
	}
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	return s.Store.SaveState(state)
}

func (s *Service) MemoryMarkdown(ctx context.Context) ([]byte, error) {
	_ = ctx
	return s.refreshMemoryMarkdown()
}

func (s *Service) PromoteMemory(ctx context.Context, taskID string, promote task.MemoryPromotion, source string) (task.MemoryEntry, error) {
	_ = ctx
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.MemoryEntry{}, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.MemoryEntry{}, err
	}
	entry, err := s.persistMemoryEntry(spec, state, promote, source)
	if err != nil {
		return task.MemoryEntry{}, err
	}
	summary := fmt.Sprintf("Promoted workspace memory (%s).", entry.Kind)
	event := newEvent(taskID, state, "memory_promoted", summary, []string{s.Store.MemoryEntryRef(entry.EntryID), s.Store.MemoryMarkdownRef()})
	if err := s.Store.AppendEvent(event); err != nil {
		return task.MemoryEntry{}, err
	}
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	if err := s.Store.SaveState(state); err != nil {
		return task.MemoryEntry{}, err
	}
	if state.State != task.StateDone {
		if err := s.syncTaskNarrative(spec, state, summary); err != nil {
			return task.MemoryEntry{}, err
		}
	}
	return entry, nil
}

func (s *Service) ensureRoleContracts() error {
	if err := s.Store.EnsureWorkspaceLayout(); err != nil {
		return err
	}
	for _, contract := range task.DefaultRoleContracts(s.Config) {
		if _, err := s.Store.LoadRoleContract(contract.RoleID); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := s.Store.SaveRoleContract(contract); err != nil {
			return err
		}
	}
	_, err := s.Store.ReadRoleContracts()
	return err
}

func (s *Service) roleContractForSpec(spec task.Spec) (task.RoleContract, error) {
	if err := s.ensureRoleContracts(); err != nil {
		return task.RoleContract{}, err
	}
	return s.Store.LoadRoleContract(string(spec.Kind))
}

func authorizeProviderDecision(contract task.RoleContract, decision provider.Decision) error {
	if !task.RoleContractAllowsProviderAction(contract, decision.Action) {
		return fmt.Errorf("role %s cannot select provider action %s", contract.RoleID, decision.Action)
	}
	if decision.Action == "worker_spawn" && !task.RoleContractAllowsWorkerRole(contract, decision.WorkerRole) {
		return fmt.Errorf("role %s cannot spawn worker role %s", contract.RoleID, decision.WorkerRole)
	}
	return nil
}

func (s *Service) buildProviderInput(ctx context.Context, taskID string, session *task.Session) (task.Spec, task.State, provider.Input, error) {
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.Spec{}, task.State{}, provider.Input{}, err
	}
	spec = task.HydrateSpec(spec, s.Config)
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.Spec{}, task.State{}, provider.Input{}, err
	}
	input := provider.Input{
		Task:    spec,
		State:   state,
		Session: session,
	}
	if roleContract, err := s.roleContractForSpec(spec); err != nil {
		return task.Spec{}, task.State{}, provider.Input{}, err
	} else {
		input.RoleContract = &roleContract
	}
	if session != nil {
		ref, messages, err := s.sessionContinuity(session, 6)
		if err != nil {
			return task.Spec{}, task.State{}, provider.Input{}, err
		}
		input.SessionMessagesRef = ref
		input.SessionRecentMessages = messages
	}
	if plan, err := s.Store.LoadPlan(taskID); err == nil {
		input.Plan = &plan
	}
	if baseline, err := s.Store.LoadBaseline(taskID); err == nil {
		input.Baseline = &baseline
	}
	if continuity, err := s.Store.LoadContinuity(taskID); err == nil {
		input.Continuity = &continuity
	}
	if sprint, err := s.Store.LoadSprint(taskID); err == nil {
		input.Sprint = &sprint
	}
	if verification, err := s.Store.LoadVerification(taskID); err == nil {
		input.Verification = &verification
	}
	if reviewReport, err := s.Store.LoadReview(taskID); err == nil {
		input.Review = &reviewReport
	}
	if completion, err := s.Store.LoadCompletion(taskID); err == nil {
		input.Completion = &completion
	}
	if criteria, err := s.Store.LoadCriteria(taskID); err == nil {
		input.Criteria = &criteria
	}
	if contextPack, err := s.Store.LoadContextSummary(taskID); err == nil {
		input.ContextPack = &contextPack
	}
	if guidance, err := s.Store.LoadWorkspaceGuidance(taskID); err == nil {
		input.WorkspaceGuidance = &guidance
	}
	if project, err := s.loadOrInitProject(); err == nil {
		if refreshed, refreshErr := s.refreshProject(project); refreshErr == nil {
			input.Project = &refreshed
		}
	}
	if mission, err := s.providerMissionViewForTask(taskID); err != nil {
		return task.Spec{}, task.State{}, provider.Input{}, err
	} else if mission != nil {
		input.Mission = mission
	}
	if s.Config.Memory.Enabled {
		if memory, err := s.refreshMemoryMarkdown(); err == nil {
			input.WorkspaceMemory = string(memory)
		}
	}
	if events, err := s.Store.ReadEvents(taskID); err == nil {
		if len(events) > 8 {
			events = events[len(events)-8:]
		}
		input.RecentEvents = events
	}
	managedWorkers, ownedPendingApprovals, err := s.managedWorkerContext(ctx, taskID)
	if err != nil {
		return task.Spec{}, task.State{}, provider.Input{}, err
	}
	input.ManagedWorkers = managedWorkers
	input.OwnedPendingApprovals = ownedPendingApprovals
	if approvals, err := s.Store.ReadApprovals(taskID); err == nil {
		input.PendingApprovals = pendingApprovals(approvals)
	}
	if watch, err := s.activeWatch(taskID); err == nil {
		input.ActiveWatch = watch
	}
	return spec, state, input, nil
}

func (s *Service) activeWatch(taskID string) (*task.Watch, error) {
	watches, err := s.Store.ListWatches()
	if err != nil {
		return nil, err
	}
	for _, watch := range watches {
		if watch.TaskID == taskID && watch.Status == "active" {
			copy := watch
			return &copy, nil
		}
	}
	return nil, nil
}

func pendingApprovals(records []task.ApprovalRecord) []task.ApprovalRecord {
	latest := make(map[string]task.ApprovalRecord, len(records))
	for _, record := range records {
		latest[record.ApprovalID] = record
	}
	out := make([]task.ApprovalRecord, 0, len(latest))
	for _, record := range latest {
		if record.Status == "pending" {
			out = append(out, record)
		}
	}
	return out
}

func sessionRuntimeContent(snapshot task.StatusSnapshot, events []task.Event, runErr error) string {
	detail := ""
	if len(events) > 0 {
		last := events[len(events)-1]
		if last.Type == "provider_responded" {
			detail = "Responded to operator prompt."
		} else {
			detail = strings.TrimSpace(last.Summary)
		}
	}
	if detail == "" && runErr != nil {
		detail = strings.TrimSpace(runErr.Error())
	}
	if snapshot.TaskID != "" {
		prefix := strings.TrimSpace(fmt.Sprintf("%s %s", snapshot.State, snapshot.Phase))
		switch {
		case prefix != "" && detail != "":
			if runErr != nil && !strings.Contains(strings.ToLower(detail), strings.ToLower(strings.TrimSpace(runErr.Error()))) {
				return fmt.Sprintf("%s: %s Error: %s", prefix, detail, strings.TrimSpace(runErr.Error()))
			}
			return fmt.Sprintf("%s: %s", prefix, detail)
		case prefix != "":
			return prefix
		}
	}
	if detail != "" {
		return detail
	}
	if runErr != nil {
		return fmt.Sprintf("runtime error: %s", strings.TrimSpace(runErr.Error()))
	}
	return "runtime update recorded."
}

func (s *Service) sessionContinuity(session *task.Session, limit int) (string, []task.SessionMessage, error) {
	if session == nil {
		return "", nil, nil
	}
	ref := workspaceSessionRef(session.SessionID, ".messages.jsonl")
	messages, err := s.Store.ReadSessionMessages(session.SessionID)
	if err != nil {
		if os.IsNotExist(err) {
			return ref, nil, nil
		}
		return "", nil, err
	}
	return ref, tailSessionMessages(messages, limit), nil
}

func shouldContinueAuto(decision provider.Decision, snapshot task.StatusSnapshot) bool {
	if snapshot.State != task.StateActive {
		return false
	}
	switch decision.Action {
	case "respond", "wait", "block", "noop":
		return false
	default:
		return true
	}
}

func sessionRequestsStandaloneMemory(session *task.Session) bool {
	if session == nil {
		return false
	}
	text := strings.TrimSpace(strings.ToLower(session.LastPrompt))
	return strings.HasPrefix(text, "/memory")
}

func sessionStopsAfterTaskCreate(session *task.Session) bool {
	return session != nil
}

func (s *Service) hookDefinitions(hookStage, action string) []task.HookDefinition {
	var defs []task.HookDefinition
	switch hookStage {
	case "pre_run":
		if len(s.Config.Hooks.PreRunCommand) > 0 {
			defs = append(defs, task.HookDefinition{
				HookID:  "legacy_pre_run",
				Stage:   hookStage,
				Command: append([]string(nil), s.Config.Hooks.PreRunCommand...),
			})
		}
	case "post_run":
		if len(s.Config.Hooks.PostRunCommand) > 0 {
			defs = append(defs, task.HookDefinition{
				HookID:  "legacy_post_run",
				Stage:   hookStage,
				Command: append([]string(nil), s.Config.Hooks.PostRunCommand...),
			})
		}
	case "on_done":
		if len(s.Config.Hooks.OnDoneCommand) > 0 {
			defs = append(defs, task.HookDefinition{
				HookID:  "legacy_on_done",
				Stage:   hookStage,
				Command: append([]string(nil), s.Config.Hooks.OnDoneCommand...),
			})
		}
	}
	for idx, definition := range s.Config.Hooks.Registry {
		if !strings.EqualFold(strings.TrimSpace(definition.Stage), hookStage) {
			continue
		}
		if !hookMatchesAction(definition.Actions, action) {
			continue
		}
		if len(definition.Command) == 0 {
			continue
		}
		if strings.TrimSpace(definition.HookID) == "" {
			definition.HookID = fmt.Sprintf("%s_%02d", hookStage, idx+1)
		}
		definition.Stage = hookStage
		definition.Command = append([]string(nil), definition.Command...)
		defs = append(defs, definition)
	}
	return defs
}

func hookMatchesAction(actions []string, action string) bool {
	if len(actions) == 0 {
		return true
	}
	for _, candidate := range actions {
		if strings.EqualFold(strings.TrimSpace(candidate), action) {
			return true
		}
	}
	return false
}

func hookLabel(definition task.HookDefinition) string {
	label := strings.TrimSpace(definition.HookID)
	if label == "" {
		label = strings.TrimSpace(definition.Stage)
	}
	if label == "" {
		label = "hook"
	}
	return label
}

func (s *Service) maybePromoteTaskMemory(spec task.Spec, state task.State, summary string) error {
	if !s.Config.Memory.Enabled || state.State != task.StateDone {
		return nil
	}
	_, err := s.persistMemoryEntry(spec, state, task.MemoryPromotion{
		Kind:    task.MemoryKindTaskCompletion,
		Summary: summary,
		Refs:    []string{"handoff.md", "completion/latest.json"},
	}, task.MemorySourceRuntime)
	return err
}

var sensitiveSummaryPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`),
	regexp.MustCompile(`(?i)(api[_-]?key\s*[:=]\s*)[^\s,;]+`),
	regexp.MustCompile(`(?i)(password\s*[:=]\s*)[^\s,;]+`),
	regexp.MustCompile(`sk-[A-Za-z0-9_-]+`),
}

func memoryEntrySummary(spec task.Spec, summary string) string {
	label := strings.TrimSpace(spec.Title)
	if label == "" {
		label = strings.TrimSpace(spec.Objective)
	}
	if label == "" {
		label = spec.TaskID
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "completed"
	}
	return fmt.Sprintf("[%s] %s: %s", spec.Kind, label, summary)
}

func (s *Service) persistMemoryEntry(spec task.Spec, state task.State, promote task.MemoryPromotion, source string) (task.MemoryEntry, error) {
	if !s.Config.Memory.Enabled {
		return task.MemoryEntry{}, errors.New("workspace memory is disabled")
	}
	kind := task.CanonicalMemoryKind(promote.Kind)
	if strings.TrimSpace(promote.Kind) == "" && state.State == task.StateDone {
		kind = task.MemoryKindTaskCompletion
	}
	if !task.IsSupportedMemoryKind(kind) {
		return task.MemoryEntry{}, fmt.Errorf("unsupported memory kind: %s", strings.TrimSpace(promote.Kind))
	}
	source = task.CanonicalMemorySource(source)
	switch source {
	case task.MemorySourceRuntime, task.MemorySourceOperator, task.MemorySourceProvider:
	default:
		return task.MemoryEntry{}, fmt.Errorf("unsupported memory source: %s", strings.TrimSpace(source))
	}
	rawSummary := strings.TrimSpace(promote.Summary)
	if rawSummary == "" && kind != task.MemoryKindTaskCompletion {
		return task.MemoryEntry{}, errors.New("memory summary is required")
	}
	summary := redactSensitiveSummary(memoryEntrySummary(spec, rawSummary))
	if strings.TrimSpace(summary) == "" {
		return task.MemoryEntry{}, errors.New("memory summary is required")
	}
	refs := normalizeMemoryRefs(promote.Refs)
	if len(refs) == 0 {
		refs = defaultMemoryRefs(state)
	}
	entries, err := s.Store.ReadMemoryEntries()
	if err != nil {
		return task.MemoryEntry{}, err
	}
	entry := task.MemoryEntry{
		SchemaVersion:    task.SchemaVersion,
		EntryID:          task.NewID("MEM"),
		TaskID:           spec.TaskID,
		Kind:             kind,
		Source:           source,
		Scope:            "task",
		Paths:            memoryPathsFromRefs(refs),
		Profiles:         []string{string(spec.Kind)},
		ProviderModes:    []string{provider.CanonicalMode(s.Config.Provider.Mode)},
		Confidence:       memoryConfidence(state),
		FreshnessStatus:  "fresh",
		LastValidatedRef: memoryLastValidatedRef(state),
		Summary:          summary,
		Refs:             refs,
		CreatedAt:        task.Now(),
	}
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		if sameMemoryEntry(last, entry) {
			return last, nil
		}
	}
	if err := s.Store.AppendMemoryEntry(entry); err != nil {
		return task.MemoryEntry{}, err
	}
	entries, err = s.Store.ReadMemoryEntries()
	if err != nil {
		return task.MemoryEntry{}, err
	}
	if max := s.Config.Memory.MaxEntries; max > 0 && len(entries) > max {
		entries = entries[len(entries)-max:]
	}
	if err := s.Store.SaveMemoryMarkdown([]byte(s.renderWorkspaceMemory(entries))); err != nil {
		return task.MemoryEntry{}, err
	}
	return entry, nil
}

func (s *Service) refreshMemoryMarkdown() ([]byte, error) {
	entries, err := s.Store.ReadMemoryEntries()
	if err != nil {
		return nil, err
	}
	if max := s.Config.Memory.MaxEntries; max > 0 && len(entries) > max {
		entries = entries[len(entries)-max:]
	}
	data := []byte(s.renderWorkspaceMemory(entries))
	if err := s.Store.SaveMemoryMarkdown(data); err != nil {
		return nil, err
	}
	return data, nil
}

func defaultMemoryRefs(state task.State) []string {
	if state.State == task.StateDone {
		return []string{"handoff.md", "completion/latest.json"}
	}
	refs := []string{"progress.md", "context/summary.md"}
	if detail := strings.TrimSpace(state.StatusDetailRef); detail != "" {
		refs = append(refs, detail)
	}
	return normalizeMemoryRefs(refs)
}

func sameMemoryEntry(a, b task.MemoryEntry) bool {
	if a.TaskID != b.TaskID || a.Kind != b.Kind || a.Source != b.Source || a.Summary != b.Summary {
		return false
	}
	if len(a.Refs) != len(b.Refs) {
		return false
	}
	for i := range a.Refs {
		if a.Refs[i] != b.Refs[i] {
			return false
		}
	}
	return true
}

func normalizeMemoryRefs(refs []string) []string {
	trimmed := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		trimmed = append(trimmed, ref)
	}
	return uniqueRefs(trimmed)
}

func redactSensitiveSummary(summary string) string {
	out := strings.TrimSpace(summary)
	for _, pattern := range sensitiveSummaryPatterns {
		switch pattern.String() {
		case `sk-[A-Za-z0-9_-]+`:
			out = pattern.ReplaceAllString(out, "[redacted-secret]")
		default:
			out = pattern.ReplaceAllString(out, `${1}[redacted-secret]`)
		}
	}
	return out
}

func memoryConfidence(state task.State) string {
	if state.State == task.StateDone {
		return "validated"
	}
	return "observed"
}

func memoryLastValidatedRef(state task.State) string {
	if state.State == task.StateDone {
		return "completion/latest.json"
	}
	return firstNonEmpty(state.StatusDetailRef, state.LastEventRef, "progress.md")
}

func memoryPathsFromRefs(refs []string) []string {
	var paths []string
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if strings.HasPrefix(ref, "workspace:") {
			path := strings.TrimPrefix(ref, "workspace:")
			path = strings.TrimPrefix(path, "./")
			if path != "" && !strings.HasPrefix(path, ".ngen/") {
				paths = append(paths, filepath.ToSlash(path))
			}
			continue
		}
		if strings.Contains(ref, "#") || strings.Contains(ref, ":") || strings.HasPrefix(ref, ".ngen/") {
			continue
		}
		switch {
		case strings.HasPrefix(ref, "context/"),
			strings.HasPrefix(ref, "completion/"),
			strings.HasPrefix(ref, "criteria/"),
			strings.HasPrefix(ref, "verification/"),
			strings.HasPrefix(ref, "reviews/"),
			strings.HasPrefix(ref, "diagnostics/"),
			ref == "progress.md",
			ref == "handoff.md":
			continue
		}
		if filepath.Ext(ref) != "" {
			paths = append(paths, filepath.ToSlash(ref))
		}
	}
	return uniqueRefs(paths)
}

func (s *Service) renderWorkspaceMemory(entries []task.MemoryEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Workspace Memory\n\n")
	fmt.Fprintf(&b, "## Recent Memory Entries\n")
	latest := latestEntriesByTask(entries)
	if len(latest) == 0 {
		fmt.Fprintf(&b, "- No promoted memory yet.\n")
	} else {
		for _, item := range latest {
			label := item.Kind
			if source := strings.TrimSpace(item.Source); source != "" {
				label = label + "/" + source
			}
			if freshness := s.memoryEntryFreshness(item); freshness != "" {
				label = label + "/" + freshness
			}
			fmt.Fprintf(&b, "- %s %s [%s]: %s\n", item.CreatedAt, item.TaskID, label, item.Summary)
		}
	}
	fmt.Fprintf(&b, "\n## Consolidated Topics\n")
	topics := summarizeMemoryTopics(entries)
	if len(topics) == 0 {
		fmt.Fprintf(&b, "- No recurring topics yet.\n")
	} else {
		for _, topic := range topics {
			fmt.Fprintf(&b, "- %s\n", topic)
		}
	}
	return b.String()
}

func (s *Service) memoryEntryFreshness(entry task.MemoryEntry) string {
	if len(entry.Paths) == 0 {
		return firstNonEmpty(entry.FreshnessStatus, "fresh")
	}
	for _, rel := range entry.Paths {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			return "stale"
		}
		if _, err := os.Stat(filepath.Join(s.Store.WorkspaceRoot, filepath.FromSlash(rel))); err != nil {
			if os.IsNotExist(err) {
				return "stale"
			}
			return "unknown"
		}
	}
	return "fresh"
}

func latestEntriesByTask(entries []task.MemoryEntry) []task.MemoryEntry {
	latest := make(map[string]task.MemoryEntry, len(entries))
	order := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, ok := latest[entry.TaskID]; !ok {
			order = append(order, entry.TaskID)
		}
		latest[entry.TaskID] = entry
	}
	sort.SliceStable(order, func(i, j int) bool {
		return latest[order[i]].CreatedAt > latest[order[j]].CreatedAt
	})
	out := make([]task.MemoryEntry, 0, len(order))
	for _, taskID := range order {
		out = append(out, latest[taskID])
	}
	return out
}

func summarizeMemoryTopics(entries []task.MemoryEntry) []string {
	stopWords := map[string]struct{}{
		"the": {}, "and": {}, "for": {}, "with": {}, "task": {}, "completed": {}, "done": {},
		"gate": {}, "passed": {}, "review": {}, "coding": {}, "general_execution": {}, "reviewer": {},
		"security_review": {}, "blocked": {}, "active": {}, "state": {}, "profile": {},
	}
	counts := map[string]int{}
	for _, entry := range entries {
		seenInEntry := map[string]struct{}{}
		for _, token := range tokenizeSummary(entry.Summary) {
			if _, skip := stopWords[token]; skip {
				continue
			}
			if strings.HasPrefix(token, "task") {
				continue
			}
			if _, seen := seenInEntry[token]; seen {
				continue
			}
			seenInEntry[token] = struct{}{}
			counts[token]++
		}
	}
	type topicCount struct {
		Token string
		Count int
	}
	var ranked []topicCount
	for token, count := range counts {
		if count < 2 {
			continue
		}
		ranked = append(ranked, topicCount{Token: token, Count: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Count == ranked[j].Count {
			return ranked[i].Token < ranked[j].Token
		}
		return ranked[i].Count > ranked[j].Count
	})
	if len(ranked) > 5 {
		ranked = ranked[:5]
	}
	topics := make([]string, 0, len(ranked))
	for _, item := range ranked {
		topics = append(topics, fmt.Sprintf("%s (%d)", item.Token, item.Count))
	}
	return topics
}

func workspaceSessionRef(sessionID, suffix string) string {
	return "workspace:" + filepath.ToSlash(filepath.Join(".ngen", "sessions", sessionID+suffix))
}

func tailSessionMessages(messages []task.SessionMessage, limit int) []task.SessionMessage {
	if len(messages) == 0 || limit <= 0 {
		return nil
	}
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	out := make([]task.SessionMessage, len(messages))
	copy(out, messages)
	return out
}

func latestTimestamp(values ...string) string {
	var latest string
	for _, value := range values {
		if value > latest {
			latest = value
		}
	}
	return latest
}

func tokenizeSummary(summary string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(summary) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte(' ')
	}
	return strings.Fields(b.String())
}
