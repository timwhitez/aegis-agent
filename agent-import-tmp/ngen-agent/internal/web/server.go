package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
)

type Server struct {
	Service *ngenrt.Service
	Token   string
}

func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/tasks", s.withAuth(s.handleTasks))
	mux.HandleFunc("/api/tasks/", s.withAuth(s.handleTaskByID))
	mux.HandleFunc("/api/missions/", s.withAuth(s.handleMissionByID))
	mux.HandleFunc("/api/sessions", s.withAuth(s.handleSessions))
	mux.HandleFunc("/api/sessions/", s.withAuth(s.handleSessionByID))
	return mux
}

func (s Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	if s.Service == nil || s.Service.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "runtime service is not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"service":        "ngen",
		"workspace_root": s.Service.Store.WorkspaceRoot,
		"state_dir":      s.Service.Store.StateDir,
	})
}

func (s Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if s.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "runtime service is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		entries, err := s.Service.ListTasks(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "task_list_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, entries)
	case http.MethodPost:
		var req createTaskRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		view, err := s.Service.CreateTask(r.Context(), task.TaskFile{
			Kind:             req.Kind,
			PresetID:         task.PresetID(req.PresetID),
			Title:            req.Title,
			Objective:        req.Objective,
			SuccessCriteria:  criteriaFromStrings(req.Criteria),
			Constraints:      append([]string(nil), req.Constraints...),
			PermissionModeID: req.PermissionModeID,
		}, task.StepSourceOperator, req.ProjectStepID, req.ProjectBranchID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "task_create_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, view)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

func (s Server) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	if s.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "runtime service is not configured")
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/tasks/"))
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not_found", "task id is required")
		return
	}
	taskID := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		view, err := s.Service.GetTask(r.Context(), taskID)
		if err != nil {
			writeError(w, http.StatusNotFound, "task_get_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	if len(parts) == 2 && parts[1] == "events" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		if _, err := s.Service.GetTask(r.Context(), taskID); err != nil {
			writeError(w, http.StatusNotFound, "task_get_failed", err.Error())
			return
		}
		limit := 20
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be an integer")
				return
			}
			limit = parsed
		}
		events, err := s.Service.TailEventsAfter(taskID, strings.TrimSpace(r.URL.Query().Get("after")), limit)
		if err != nil {
			writeError(w, http.StatusNotFound, "task_events_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, events)
		return
	}
	if len(parts) == 3 && parts[1] == "events" && parts[2] == "stream" {
		s.handleTaskEventsStream(w, r, taskID)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "unknown task route")
}

func (s Server) handleTaskEventsStream(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "http streaming is not supported")
		return
	}
	if _, err := s.Service.GetTask(r.Context(), taskID); err != nil {
		writeError(w, http.StatusNotFound, "task_get_failed", err.Error())
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be an integer")
			return
		}
		limit = parsed
	}
	follow, err := parseBoolQuery(r, "follow", true)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_follow", "follow must be true or false")
		return
	}
	interval := time.Second
	if raw := strings.TrimSpace(r.URL.Query().Get("interval_ms")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 100 {
			writeError(w, http.StatusBadRequest, "invalid_interval", "interval_ms must be an integer >= 100")
			return
		}
		if parsed > 10000 {
			parsed = 10000
		}
		interval = time.Duration(parsed) * time.Millisecond
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sent := make(map[string]struct{})
	lastEventID := strings.TrimSpace(r.URL.Query().Get("after"))
	if lastEventID == "" {
		lastEventID = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	for {
		events, err := s.Service.TailEventsAfter(taskID, lastEventID, limit)
		if err != nil {
			_ = writeSSEError(w, "task_events_failed", err.Error())
			flusher.Flush()
			return
		}
		wrote := false
		for _, event := range events {
			if event.EventID != "" {
				if _, ok := sent[event.EventID]; ok {
					continue
				}
				sent[event.EventID] = struct{}{}
				lastEventID = event.EventID
			}
			if err := writeSSETaskEvent(w, event); err != nil {
				return
			}
			wrote = true
		}
		if !follow {
			flusher.Flush()
			return
		}
		if !wrote {
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
		}
		flusher.Flush()

		timer := time.NewTimer(interval)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s Server) handleMissionByID(w http.ResponseWriter, r *http.Request) {
	if s.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "runtime service is not configured")
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/missions/"))
	if len(parts) != 1 || strings.TrimSpace(parts[0]) == "" {
		writeError(w, http.StatusNotFound, "not_found", "mission id is required")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	view, err := s.Service.MissionStatus(r.Context(), parts[0])
	if err != nil {
		writeError(w, http.StatusNotFound, "mission_get_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "runtime service is not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var req startSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.Mode) == "" {
		req.Mode = "web"
	}
	session, err := s.Service.StartSession(r.Context(), req.TaskID, req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "session_start_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (s Server) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	if s.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "runtime service is not configured")
		return
	}
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/sessions/"))
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not_found", "session id is required")
		return
	}
	sessionID := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		session, messages, err := s.Service.ReadSession(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusNotFound, "session_read_failed", err.Error())
			return
		}
		snapshot, err := s.Service.SessionSnapshot(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "session_snapshot_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, sessionReadResponse{
			ObjectKind: "web_session",
			Session:    session,
			Messages:   messages,
			Snapshot:   snapshot,
		})
		return
	}
	if len(parts) == 2 && parts[1] == "prompt" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}
		var req promptSessionRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		session, snapshot, events, err := s.Service.PromptSession(r.Context(), sessionID, req.Prompt)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "session_prompt_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, promptSessionResponse{
			ObjectKind: "web_prompt_result",
			Session:    session,
			Status:     snapshot,
			Events:     events,
		})
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}
		session, err := s.Service.CancelSession(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "session_cancel_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, session)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "unknown session route")
}

func (s Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(s.Token)
		if token == "" {
			next(w, r)
			return
		}
		got := strings.TrimSpace(r.Header.Get("Authorization"))
		if got != "Bearer "+token {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		next(w, r)
	}
}

type createTaskRequest struct {
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

type startSessionRequest struct {
	TaskID string `json:"task_id"`
	Mode   string `json:"mode"`
}

type promptSessionRequest struct {
	Prompt string `json:"prompt"`
}

type sessionReadResponse struct {
	ObjectKind string                `json:"object_kind"`
	Session    task.Session          `json:"session"`
	Messages   []task.SessionMessage `json:"messages"`
	Snapshot   task.SessionSnapshot  `json:"snapshot"`
}

type promptSessionResponse struct {
	ObjectKind string              `json:"object_kind"`
	Session    task.Session        `json:"session"`
	Status     task.StatusSnapshot `json:"status"`
	Events     []task.Event        `json:"events"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("request body is required")
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorBody{Code: code, Message: message}})
}

func writeSSETaskEvent(w io.Writer, event task.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.EventID != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", sanitizeSSEField(event.EventID)); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, "event: task_event\n"); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}

func writeSSEError(w io.Writer, code, message string) error {
	payload, err := json.Marshal(errorResponse{Error: errorBody{Code: code, Message: message}})
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "event: error\n"); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}

func sanitizeSSEField(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return value
}

func parseBoolQuery(r *http.Request, name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(strings.ToLower(r.URL.Query().Get(name)))
	if raw == "" {
		return fallback, nil
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool query %s=%q", name, raw)
	}
}

func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func criteriaFromStrings(values []string) []task.SuccessCriterion {
	criteria := make([]task.SuccessCriterion, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		criteria = append(criteria, task.SuccessCriterion{
			ID:        fmt.Sprintf("SC-%03d", len(criteria)+1),
			Statement: value,
		})
	}
	return criteria
}
