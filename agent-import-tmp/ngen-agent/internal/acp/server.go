package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any         `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *ErrorValue `json:"error,omitempty"`
}

type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type ErrorValue struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Server struct {
	Service *ngenrt.Service
}

var (
	errInvalidRequest = errors.New("invalid request")
	errMethodNotFound = errors.New("method not found")
)

type invalidParamsError struct {
	Err error
}

func (e invalidParamsError) Error() string {
	return e.Err.Error()
}

func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			if writeErr := writeResponse(w, Response{
				JSONRPC: "2.0",
				Error:   &ErrorValue{Code: -32700, Message: err.Error()},
			}); writeErr != nil {
				return writeErr
			}
			continue
		}
		resp, notifications := s.handle(ctx, req)
		if err := writeResponse(w, resp); err != nil {
			return err
		}
		for _, notification := range notifications {
			if err := writeNotification(w, notification); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func (s *Server) handle(ctx context.Context, req Request) (Response, []Notification) {
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		resp.Error = &ErrorValue{Code: -32600, Message: errInvalidRequest.Error()}
		return resp, nil
	}
	var err error
	var notifications []Notification
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"name":    "ngen",
			"version": "post-foundation-integrated-baseline",
			"capabilities": map[string]any{
				"session":       []string{"start", "list", "read", "prompt", "cancel", "snapshot"},
				"task":          []string{"create", "list", "get", "update", "patch", "run", "resume", "auto", "status", "review", "events"},
				"mission":       []string{"status"},
				"project":       []string{"get", "update", "patch"},
				"memory":        []string{"show", "promote"},
				"permission":    []string{"request", "list", "decide"},
				"input":         []string{"request", "list", "respond"},
				"worker":        []string{"spawn", "list", "sync", "continue"},
				"notifications": []string{"ngen.notification"},
				"objects":       []string{"status_snapshot", "session_snapshot", "worker_snapshot", "task_view", "task_list_entry", "project_view", "mission_view", "mission_status_snapshot", "acp_notification"},
			},
		}
	case "rpc.ping":
		resp.Result = map[string]any{"ok": true}
	case "task.list":
		resp.Result, err = s.Service.ListTasks(ctx)
	case "task.create":
		var params struct {
			Kind             task.Kind `json:"kind"`
			PresetID         string    `json:"preset_id"`
			Title            string    `json:"title"`
			Objective        string    `json:"objective"`
			Criteria         []string  `json:"criteria"`
			Constraints      []string  `json:"constraints"`
			PermissionModeID string    `json:"permission_mode_id"`
			ProjectStepID    string    `json:"project_step_id"`
			ProjectBranchID  string    `json:"project_branch_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			view, createErr := s.Service.CreateTask(ctx, task.TaskFile{
				Kind:             params.Kind,
				PresetID:         task.PresetID(params.PresetID),
				Title:            params.Title,
				Objective:        params.Objective,
				SuccessCriteria:  criteriaFromStrings(params.Criteria),
				Constraints:      append([]string(nil), params.Constraints...),
				PermissionModeID: params.PermissionModeID,
			}, task.StepSourceOperator, params.ProjectStepID, params.ProjectBranchID)
			if createErr == nil {
				resp.Result = view
				notifications = taskNotification(view.Status, "task.created", "Task created.", nil)
			}
			err = createErr
		}
	case "task.get":
		var params struct {
			TaskID string `json:"task_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			resp.Result, err = s.Service.GetTask(ctx, params.TaskID)
		}
	case "task.update":
		var params struct {
			TaskID      string                   `json:"task_id"`
			Explanation string                   `json:"explanation"`
			Steps       []task.ExecutionPlanStep `json:"steps"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			view, updateErr := s.Service.UpdateTaskPlan(ctx, params.TaskID, task.PlanUpdate{
				Explanation: params.Explanation,
				Steps:       params.Steps,
			}, task.StepSourceOperator)
			if updateErr == nil {
				resp.Result = view
				notifications = taskNotification(view.Status, "task.updated", "Mutable task plan updated.", nil)
			}
			err = updateErr
		}
	case "task.patch":
		var params struct {
			TaskID     string                    `json:"task_id"`
			Operations []task.PlanPatchOperation `json:"operations"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			view, patchErr := s.Service.PatchTaskPlan(ctx, params.TaskID, task.PlanPatch{
				Operations: params.Operations,
			}, task.StepSourceOperator)
			if patchErr == nil {
				resp.Result = view
				notifications = taskNotification(view.Status, "task.updated", "Mutable task plan patched.", nil)
			}
			err = patchErr
		}
	case "project.get":
		resp.Result, err = s.Service.GetProject(ctx)
	case "mission.status":
		var params struct {
			MissionID string `json:"mission_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.MissionID) == "" {
				err = invalidParamsError{Err: errors.New("mission_id is required")}
				break
			}
			resp.Result, err = s.Service.MissionStatus(ctx, params.MissionID)
		}
	case "project.update":
		var params struct {
			Explanation string                      `json:"explanation"`
			Steps       []task.ProjectExecutionStep `json:"steps"`
			Branches    []task.ProjectBranchSpec    `json:"branches"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			view, updateErr := s.Service.UpdateProject(ctx, task.ProjectUpdate{
				Explanation: params.Explanation,
				Steps:       params.Steps,
				Branches:    params.Branches,
			}, task.StepSourceOperator)
			if updateErr == nil {
				resp.Result = view
				notifications = projectNotification("project.updated", "Workspace project graph updated.")
			}
			err = updateErr
		}
	case "project.patch":
		var params struct {
			Operations []task.ProjectPatchOperation `json:"operations"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			view, patchErr := s.Service.PatchProject(ctx, task.ProjectPatch{
				Operations: params.Operations,
			}, task.StepSourceOperator)
			if patchErr == nil {
				resp.Result = view
				notifications = projectNotification("project.updated", "Workspace project graph patched.")
			}
			err = patchErr
		}
	case "memory.show":
		var data []byte
		data, err = s.Service.MemoryMarkdown(ctx)
		if err == nil {
			resp.Result = map[string]any{"markdown": string(data)}
		}
	case "memory.promote":
		var params struct {
			TaskID  string   `json:"task_id"`
			Summary string   `json:"summary"`
			Kind    string   `json:"kind"`
			Refs    []string `json:"refs"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" || strings.TrimSpace(params.Summary) == "" {
				err = invalidParamsError{Err: errors.New("task_id and summary are required")}
				break
			}
			var entry task.MemoryEntry
			entry, err = s.Service.PromoteMemory(ctx, params.TaskID, task.MemoryPromotion{
				Kind:    params.Kind,
				Summary: params.Summary,
				Refs:    params.Refs,
			}, task.MemorySourceOperator)
			if err == nil {
				resp.Result = entry
				notifications, err = s.taskNotification(ctx, params.TaskID, "task.updated", "Workspace memory promoted.", nil)
			}
		}
	case "session.start":
		var params struct {
			TaskID string `json:"task_id"`
			Mode   string `json:"mode"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			session, startErr := s.Service.StartSession(ctx, params.TaskID, params.Mode)
			if startErr == nil {
				resp.Result = session
				notifications, startErr = s.sessionNotification(ctx, session.SessionID, "Session started.", nil)
			}
			err = startErr
		}
	case "session.list":
		resp.Result, err = s.Service.ListSessions(ctx)
	case "session.read":
		var params struct {
			SessionID string `json:"session_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.SessionID) == "" {
				err = invalidParamsError{Err: errors.New("session_id is required")}
				break
			}
			session, messages, readErr := s.Service.ReadSession(ctx, params.SessionID)
			if readErr == nil {
				resp.Result = map[string]any{"session": session, "messages": messages}
			}
			err = readErr
		}
	case "session.snapshot":
		var params struct {
			SessionID string `json:"session_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.SessionID) == "" {
				err = invalidParamsError{Err: errors.New("session_id is required")}
				break
			}
			resp.Result, err = s.Service.SessionSnapshot(ctx, params.SessionID)
		}
	case "session.prompt":
		var params struct {
			SessionID string `json:"session_id"`
			Message   string `json:"message"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.SessionID) == "" {
				err = invalidParamsError{Err: errors.New("session_id is required")}
				break
			}
			if strings.TrimSpace(params.Message) == "" {
				err = invalidParamsError{Err: errors.New("message is required")}
				break
			}
			session, snapshot, events, promptErr := s.Service.PromptSession(ctx, params.SessionID, params.Message)
			if promptErr == nil {
				resp.Result = map[string]any{"session": session, "status": snapshot, "events": events}
				notifications, promptErr = s.sessionNotification(ctx, params.SessionID, "Session prompt applied.", events)
			}
			err = promptErr
		}
	case "session.cancel":
		var params struct {
			SessionID string `json:"session_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.SessionID) == "" {
				err = invalidParamsError{Err: errors.New("session_id is required")}
				break
			}
			session, cancelErr := s.Service.CancelSession(ctx, params.SessionID)
			if cancelErr == nil {
				resp.Result = session
				notifications, cancelErr = s.sessionNotification(ctx, params.SessionID, "Session cancelled.", nil)
			}
			err = cancelErr
		}
	case "task.status":
		var params struct {
			TaskID string `json:"task_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			resp.Result, err = s.Service.Status(ctx, params.TaskID)
		}
	case "task.run":
		var params struct {
			TaskID string `json:"task_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			snapshot, events, runErr := s.Service.Run(ctx, params.TaskID)
			if runErr == nil {
				resp.Result = map[string]any{"status": snapshot, "events": events}
				notifications = taskNotification(snapshot, "task.updated", "Task run completed.", events)
			}
			err = runErr
		}
	case "task.resume":
		var params struct {
			TaskID string `json:"task_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			snapshot, events, resumeErr := s.Service.Resume(ctx, params.TaskID)
			if resumeErr == nil {
				resp.Result = map[string]any{"status": snapshot, "events": events}
				notifications = taskNotification(snapshot, "task.updated", "Task resume completed.", events)
			}
			err = resumeErr
		}
	case "task.auto":
		var params struct {
			TaskID string `json:"task_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			snapshot, events, autoErr := s.Service.Auto(ctx, params.TaskID)
			if autoErr == nil {
				resp.Result = map[string]any{"status": snapshot, "events": events}
				notifications = taskNotification(snapshot, "task.updated", "Task auto pass completed.", events)
			}
			err = autoErr
		}
	case "task.review":
		var params struct {
			TaskID string `json:"task_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			report, reviewErr := s.Service.Review(ctx, params.TaskID)
			if reviewErr == nil {
				resp.Result = report
				notifications, reviewErr = s.taskNotification(ctx, params.TaskID, "task.updated", "Review refreshed.", nil)
			}
			err = reviewErr
		}
	case "task.events":
		var params struct {
			TaskID string `json:"task_id"`
			After  string `json:"after"`
			Limit  int    `json:"limit"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			if params.Limit == 0 {
				params.Limit = 20
			}
			resp.Result, err = s.Service.TailEventsAfter(params.TaskID, params.After, params.Limit)
		}
	case "permission.request":
		var params struct {
			TaskID string `json:"task_id"`
			Scope  string `json:"scope"`
			Reason string `json:"reason"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" || strings.TrimSpace(params.Scope) == "" {
				err = invalidParamsError{Err: errors.New("task_id and scope are required")}
				break
			}
			record, requestErr := s.Service.RequestApproval(ctx, params.TaskID, params.Scope, params.Reason)
			if requestErr == nil {
				resp.Result = record
				notifications, requestErr = s.approvalNotification(ctx, params.TaskID, "Approval state updated.", record)
			}
			err = requestErr
		}
	case "permission.list":
		var params struct {
			TaskID       string `json:"task_id"`
			IncludeOwned bool   `json:"include_owned"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			if params.IncludeOwned {
				resp.Result, err = s.Service.ListOwnedApprovals(ctx, params.TaskID)
			} else {
				resp.Result, err = s.Service.ListApprovals(ctx, params.TaskID)
			}
		}
	case "permission.decide":
		var params struct {
			TaskID     string `json:"task_id"`
			ApprovalID string `json:"approval_id"`
			Decision   string `json:"decision"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" || strings.TrimSpace(params.ApprovalID) == "" || strings.TrimSpace(params.Decision) == "" {
				err = invalidParamsError{Err: errors.New("task_id, approval_id, and decision are required")}
				break
			}
			record, decideErr := s.Service.DecideApproval(ctx, params.TaskID, params.ApprovalID, params.Decision)
			if decideErr == nil {
				resp.Result = record
				summary := fmt.Sprintf("Approval %s %s.", params.ApprovalID, params.Decision)
				notifications, decideErr = s.approvalNotification(ctx, params.TaskID, summary, record)
			}
			err = decideErr
		}
	case "input.request":
		var params struct {
			TaskID   string `json:"task_id"`
			Field    string `json:"field"`
			Prompt   string `json:"prompt"`
			Required *bool  `json:"required"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" || strings.TrimSpace(params.Prompt) == "" {
				err = invalidParamsError{Err: errors.New("task_id and prompt are required")}
				break
			}
			required := true
			if params.Required != nil {
				required = *params.Required
			}
			record, requestErr := s.Service.RequestInput(ctx, params.TaskID, params.Field, params.Prompt, required)
			if requestErr == nil {
				resp.Result = record
				notifications, requestErr = s.inputNotification(ctx, params.TaskID, "Input request state updated.", record)
			}
			err = requestErr
		}
	case "input.list":
		var params struct {
			TaskID string `json:"task_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" {
				err = invalidParamsError{Err: errors.New("task_id is required")}
				break
			}
			resp.Result, err = s.Service.ListInputRequests(ctx, params.TaskID)
		}
	case "input.respond":
		var params struct {
			TaskID   string `json:"task_id"`
			Request  string `json:"request_id"`
			Response string `json:"response"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.TaskID) == "" || strings.TrimSpace(params.Request) == "" {
				err = invalidParamsError{Err: errors.New("task_id and request_id are required")}
				break
			}
			record, respondErr := s.Service.RespondInput(ctx, params.TaskID, params.Request, params.Response)
			if respondErr == nil {
				resp.Result = record
				notifications, respondErr = s.inputNotification(ctx, params.TaskID, "Input response recorded.", record)
			}
			err = respondErr
		}
	case "worker.spawn":
		var params struct {
			ParentTaskID string `json:"parent_task_id"`
			Role         string `json:"role"`
			Objective    string `json:"objective"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.ParentTaskID) == "" || strings.TrimSpace(params.Role) == "" || strings.TrimSpace(params.Objective) == "" {
				err = invalidParamsError{Err: errors.New("parent_task_id, role, and objective are required")}
				break
			}
			contract, spawnErr := s.Service.SpawnWorker(ctx, params.ParentTaskID, params.Role, params.Objective)
			if spawnErr == nil {
				var snapshot task.WorkerSnapshot
				snapshot, spawnErr = s.Service.WorkerSnapshot(ctx, params.ParentTaskID, contract.WorkerID)
				if spawnErr == nil {
					resp.Result = snapshot
					notifications = workerNotification(snapshot, "Worker state updated.")
				}
			}
			err = spawnErr
		}
	case "worker.list":
		var params struct {
			ParentTaskID string `json:"parent_task_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.ParentTaskID) == "" {
				err = invalidParamsError{Err: errors.New("parent_task_id is required")}
				break
			}
			resp.Result, err = s.Service.ListWorkerSnapshots(ctx, params.ParentTaskID)
		}
	case "worker.sync":
		var params struct {
			ParentTaskID string `json:"parent_task_id"`
			WorkerID     string `json:"worker_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.ParentTaskID) == "" || strings.TrimSpace(params.WorkerID) == "" {
				err = invalidParamsError{Err: errors.New("parent_task_id and worker_id are required")}
				break
			}
			_, syncErr := s.Service.SyncWorker(ctx, params.ParentTaskID, params.WorkerID)
			if syncErr == nil {
				var snapshot task.WorkerSnapshot
				snapshot, syncErr = s.Service.WorkerSnapshot(ctx, params.ParentTaskID, params.WorkerID)
				if syncErr == nil {
					resp.Result = snapshot
					notifications = workerNotification(snapshot, "Worker state updated.")
				}
			}
			err = syncErr
		}
	case "worker.continue":
		var params struct {
			ParentTaskID string `json:"parent_task_id"`
			WorkerID     string `json:"worker_id"`
		}
		err = decodeParams(req.Params, &params)
		if err == nil {
			if strings.TrimSpace(params.ParentTaskID) == "" || strings.TrimSpace(params.WorkerID) == "" {
				err = invalidParamsError{Err: errors.New("parent_task_id and worker_id are required")}
				break
			}
			_, continueErr := s.Service.ContinueWorker(ctx, params.ParentTaskID, params.WorkerID)
			if continueErr == nil {
				var snapshot task.WorkerSnapshot
				snapshot, continueErr = s.Service.WorkerSnapshot(ctx, params.ParentTaskID, params.WorkerID)
				if continueErr == nil {
					resp.Result = snapshot
					notifications = workerNotification(snapshot, "Worker continuation applied.")
				}
			}
			err = continueErr
		}
	default:
		err = fmt.Errorf("%w: %s", errMethodNotFound, req.Method)
	}
	if err != nil {
		resp.Error = errorValueFor(err)
		resp.Result = nil
		notifications = nil
	}
	return resp, notifications
}

func (s *Server) taskNotification(ctx context.Context, taskID, kind, summary string, events []task.Event) ([]Notification, error) {
	snapshot, err := s.Service.Status(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return taskNotification(snapshot, kind, summary, events), nil
}

func taskNotification(snapshot task.StatusSnapshot, kind, summary string, events []task.Event) []Notification {
	status := snapshot
	note := newNotification(kind, snapshot.TaskID, "", "", summary)
	note.StatusSnapshot = &status
	note.Events = cloneEvents(events)
	return wrapNotification(note)
}

func projectNotification(kind, summary string) []Notification {
	note := newNotification(kind, "", "", "", summary)
	return wrapNotification(note)
}

func (s *Server) sessionNotification(ctx context.Context, sessionID, summary string, events []task.Event) ([]Notification, error) {
	snapshot, err := s.Service.SessionSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	status := snapshot.StatusSnapshot
	session := snapshot
	note := newNotification("session.updated", snapshot.TaskID, sessionID, "", summary)
	note.StatusSnapshot = &status
	note.SessionSnapshot = &session
	note.Events = cloneEvents(events)
	return wrapNotification(note), nil
}

func (s *Server) inputNotification(ctx context.Context, taskID, summary string, record task.InputRequestRecord) ([]Notification, error) {
	snapshot, err := s.Service.Status(ctx, taskID)
	if err != nil {
		return nil, err
	}
	status := snapshot
	input := record
	note := newNotification("input.updated", taskID, "", "", summary)
	note.StatusSnapshot = &status
	note.InputRequest = &input
	return wrapNotification(note), nil
}

func (s *Server) approvalNotification(ctx context.Context, taskID, summary string, record task.ApprovalRecord) ([]Notification, error) {
	statusTaskID := taskID
	var workerSnapshot *task.WorkerSnapshot
	workerID := ""
	if record.OwnerTaskID != "" && record.OwnerTaskID == taskID && record.OwnerWorkerID != "" {
		snapshot, err := s.Service.WorkerSnapshot(ctx, record.OwnerTaskID, record.OwnerWorkerID)
		if err != nil {
			return nil, err
		}
		workerSnapshot = &snapshot
		workerID = record.OwnerWorkerID
	}
	snapshot, err := s.Service.Status(ctx, statusTaskID)
	if err != nil {
		return nil, err
	}
	status := snapshot
	approval := record
	note := newNotification("approval.updated", statusTaskID, "", workerID, summary)
	note.StatusSnapshot = &status
	if workerSnapshot != nil {
		note.WorkerSnapshot = workerSnapshot
	}
	note.Approval = &approval
	return wrapNotification(note), nil
}

func workerNotification(snapshot task.WorkerSnapshot, summary string) []Notification {
	parent := snapshot.ParentStatus
	worker := snapshot
	note := newNotification("worker.updated", snapshot.Worker.ParentTaskID, "", snapshot.Worker.WorkerID, summary)
	note.StatusSnapshot = &parent
	note.WorkerSnapshot = &worker
	return wrapNotification(note)
}

func newNotification(kind, taskID, sessionID, workerID, summary string) task.ACPNotification {
	return task.ACPNotification{
		ObjectKind:     "acp_notification",
		SchemaVersion:  task.SchemaVersion,
		NotificationID: task.NewID("NTF"),
		Kind:           kind,
		TaskID:         taskID,
		SessionID:      sessionID,
		WorkerID:       workerID,
		Summary:        summary,
		TS:             task.Now(),
	}
}

func wrapNotification(note task.ACPNotification) []Notification {
	return []Notification{{
		JSONRPC: "2.0",
		Method:  "ngen.notification",
		Params:  note,
	}}
}

func cloneEvents(events []task.Event) []task.Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]task.Event, len(events))
	copy(out, events)
	return out
}

func decodeParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return invalidParamsError{Err: err}
	}
	return nil
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

func errorValueFor(err error) *ErrorValue {
	switch {
	case errors.Is(err, errMethodNotFound):
		return &ErrorValue{Code: -32601, Message: err.Error()}
	case errors.Is(err, errInvalidRequest):
		return &ErrorValue{Code: -32600, Message: err.Error()}
	}
	var invalid invalidParamsError
	if errors.As(err, &invalid) {
		return &ErrorValue{Code: -32602, Message: invalid.Error()}
	}
	return &ErrorValue{Code: -32000, Message: err.Error()}
}

func writeResponse(w io.Writer, resp Response) error {
	return writeMessage(w, resp)
}

func writeNotification(w io.Writer, notification Notification) error {
	return writeMessage(w, notification)
}

func writeMessage(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
