package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type providerState struct {
	mu       sync.Mutex
	sessions map[string]*sessionScriptState
	logPath  string
}

type sessionScriptState struct {
	Calls                 int
	DirectChildID         string
	BackgroundJobID       string
	CommandArtifactPath   string
	HistoryMessageID      string
	HistoryNextByteOffset int64
}

type responseRequest struct {
	Metadata map[string]any `json:"metadata"`
	Input    []any          `json:"input"`
}

type requestFacts struct {
	SessionID       string
	ParentSessionID string
	QueueJobID      string
	AgentName       string
	InputText       string
}

type scriptedToolCall struct {
	Name      string
	Arguments any
}

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:0", "listen address")
	readyFile := flag.String("ready-file", "", "file that receives the provider base URL")
	logPath := flag.String("log", "", "optional JSONL decision log")
	flag.Parse()

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		fatalf("listen: %v", err)
	}
	defer listener.Close()

	state := &providerState{
		sessions: make(map[string]*sessionScriptState),
		logPath:  strings.TrimSpace(*logPath),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/responses", state.handleResponses)
	mux.HandleFunc("/responses", state.handleResponses)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	baseURL := "http://" + listener.Addr().String()
	if path := strings.TrimSpace(*readyFile); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			fatalf("create ready-file directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(baseURL+"\n"), 0o600); err != nil {
			fatalf("write ready file: %v", err)
		}
	}

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			fatalf("shutdown: %v", err)
		}
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatalf("serve: %v", err)
		}
	}
}

func (s *providerState) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	defer r.Body.Close()
	var request responseRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("decode request: %v", err)})
		return
	}
	facts := requestFacts{
		SessionID:       metadataString(request.Metadata, "session_id"),
		ParentSessionID: metadataString(request.Metadata, "parent_session_id"),
		QueueJobID:      metadataString(request.Metadata, "queue_job_id"),
		AgentName:       metadataString(request.Metadata, "agent_name"),
		InputText:       inputText(request.Input),
	}
	if facts.SessionID == "" {
		if strings.Contains(facts.InputText, "what is 2+2") {
			writeTextResponse(w, "probe", 1, "4")
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "metadata.session_id is required for budget smoke"})
		return
	}

	s.mu.Lock()
	state := s.sessions[facts.SessionID]
	if state == nil {
		state = &sessionScriptState{}
		s.sessions[facts.SessionID] = state
	}
	state.Calls++
	captureToolReferences(request.Input, state)
	callNumber := state.Calls
	if strings.Contains(facts.InputText, "E2E_UI_SLOW") && callNumber == 1 {
		s.mu.Unlock()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(4 * time.Second):
		}
		writeTextResponse(w, facts.SessionID, callNumber, "slow checkpoint")
		return
	}
	toolCall, err := scriptedCall(facts, state, callNumber)
	s.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.appendDecisionLog(facts, callNumber, toolCall.Name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("append decision log: %v", err)})
		return
	}

	arguments, err := json.Marshal(toolCall.Arguments)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("marshal tool arguments: %v", err)})
		return
	}
	callID := fmt.Sprintf("call_%s_%d", sanitizeID(facts.SessionID), callNumber)
	if toolCall.Name == "__text__" {
		writeTextResponse(w, facts.SessionID, callNumber, fmt.Sprint(toolCall.Arguments))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     fmt.Sprintf("resp_%s_%d", sanitizeID(facts.SessionID), callNumber),
		"status": "completed",
		"output": []map[string]any{{
			"type":      "function_call",
			"call_id":   callID,
			"name":      toolCall.Name,
			"arguments": string(arguments),
		}},
		"usage": map[string]any{
			"input_tokens":  1,
			"output_tokens": 1,
		},
	})
}

func scriptedCall(facts requestFacts, state *sessionScriptState, callNumber int) (scriptedToolCall, error) {
	if strings.Contains(facts.InputText, "E2E_UI_PLAN") {
		if callNumber == 1 {
			return scriptedToolCall{Name: "submit_plan", Arguments: map[string]any{
				"title":         "E2E browser plan",
				"summary":       "Exercise the real Plan Mode approval boundary.",
				"plan_markdown": "# E2E browser plan\n\n1. Approve the plan.\n2. Continue the durable session.\n3. Verify completion.",
				"verification":  []string{"Browser observes awaiting approval", "Browser approves and observes completion"},
			}}, nil
		}
		return scriptedToolCall{Name: "finish", Arguments: map[string]any{"message": "E2E plan completed"}}, nil
	}
	if strings.Contains(facts.InputText, "E2E_UI_GOAL") {
		if callNumber == 1 {
			return scriptedToolCall{Name: "update_goal", Arguments: map[string]any{
				"status":             "complete",
				"evidence":           []string{"deterministic browser provider completed the goal"},
				"completion_summary": "Goal completion exercised by browser E2E.",
			}}, nil
		}
		return scriptedToolCall{Name: "finish", Arguments: map[string]any{"message": "E2E goal completed"}}, nil
	}
	if strings.Contains(facts.InputText, "E2E_UI_AWAIT") {
		if callNumber == 1 {
			return scriptedToolCall{Name: "await_input", Arguments: map[string]any{
				"kind":             "needs_input",
				"reason":           "Browser E2E requires a continue message.",
				"resume_condition": "Operator provides the continuation.",
			}}, nil
		}
		return scriptedToolCall{Name: "finish", Arguments: map[string]any{"message": "E2E continue completed"}}, nil
	}
	if strings.Contains(facts.InputText, "E2E_UI_SLOW") {
		return scriptedToolCall{Name: "finish", Arguments: map[string]any{"message": "E2E slow session completed"}}, nil
	}
	if strings.Contains(facts.InputText, "E2E_UI_MAIN") {
		switch callNumber {
		case 1:
			return scriptedToolCall{Name: "todo_write", Arguments: map[string]any{"todos": []map[string]any{
				{"content": "Inspect bilingual Web Console", "status": "in_progress", "priority": "high"},
				{"content": "Verify task grouping", "status": "in_progress", "priority": "high"},
				{"content": "Capture screenshot evidence", "status": "pending", "priority": "medium"},
			}}}, nil
		case 2:
			return scriptedToolCall{Name: "task_create", Arguments: map[string]any{"subject": "Implement UI contract", "description": "Persistent E2E task", "priority": "high"}}, nil
		case 3:
			return scriptedToolCall{Name: "task_create", Arguments: map[string]any{"subject": "Verify UI contract", "description": "Blocked until implementation", "priority": "high", "blocked_by": []string{"task_0001"}}}, nil
		case 4:
			return scriptedToolCall{Name: "task_update", Arguments: map[string]any{"task_id": "task_0001", "status": "in_progress"}}, nil
		case 5:
			return scriptedToolCall{Name: "shell", Arguments: map[string]any{"command": "printf 'e2e workspace artifact\\n' > e2e-created.txt"}}, nil
		default:
			return scriptedToolCall{Name: "finish", Arguments: map[string]any{"message": "E2E main completed"}}, nil
		}
	}
	switch facts.AgentName {
	case "budget-resume-child":
		if callNumber == 1 {
			return scriptedToolCall{Name: "read_file", Arguments: map[string]any{"path": "missing-budget-resume-smoke.txt"}}, nil
		}
		return scriptedToolCall{Name: "finish", Arguments: map[string]any{"message": "budget resume child complete"}}, nil
	case "budget-cancel-child":
		return scriptedToolCall{Name: "read_file", Arguments: map[string]any{"path": "missing-budget-cancel-smoke.txt"}}, nil
	case "ui-smoke-queue":
		return scriptedToolCall{Name: "finish", Arguments: map[string]any{"message": "ui smoke queue ok"}}, nil
	}

	if facts.ParentSessionID != "" || facts.QueueJobID != "" {
		return scriptedToolCall{Name: "finish", Arguments: map[string]any{"message": "budget smoke auxiliary child complete"}}, nil
	}

	const todoContent = "Exercise budget lifecycle through WebConsole"
	switch callNumber {
	case 1:
		return scriptedToolCall{Name: "todo_write", Arguments: map[string]any{
			"todos": []map[string]any{{"content": todoContent, "status": "in_progress", "priority": "high"}},
		}}, nil
	case 2:
		return scriptedToolCall{Name: "shell", Arguments: map[string]any{
			"command": "head -c 70000 /dev/zero | tr '\\0' p",
		}}, nil
	case 3:
		if state.CommandArtifactPath == "" {
			return scriptedToolCall{}, errors.New("large shell result did not expose a complete artifact path")
		}
		return scriptedToolCall{Name: "read_file", Arguments: map[string]any{
			"path":        state.CommandArtifactPath,
			"byte_offset": 0,
			"byte_limit":  512,
		}}, nil
	case 4:
		return scriptedToolCall{Name: "read_session_history", Arguments: map[string]any{"limit": 4}}, nil
	case 5:
		if state.HistoryMessageID == "" {
			return scriptedToolCall{}, errors.New("history record page did not expose the large shell result message")
		}
		return scriptedToolCall{Name: "read_session_history", Arguments: map[string]any{
			"message_id":  state.HistoryMessageID,
			"byte_offset": 0,
			"byte_limit":  512,
		}}, nil
	case 6:
		if state.HistoryMessageID == "" || state.HistoryNextByteOffset <= 0 {
			return scriptedToolCall{}, errors.New("history content page did not expose a continuation offset")
		}
		return scriptedToolCall{Name: "read_session_history", Arguments: map[string]any{
			"message_id":  state.HistoryMessageID,
			"byte_offset": state.HistoryNextByteOffset,
			"byte_limit":  512,
		}}, nil
	case 7:
		return scriptedToolCall{Name: "agent_spawn", Arguments: map[string]any{
			"prompt":         "BUDGET_RESUME_CHILD: call read_file once, then finish with budget resume child complete after the parent extends the paused budget.",
			"agent_name":     "budget-resume-child",
			"agent_role":     "evaluator",
			"mode":           "exec",
			"background":     false,
			"isolation_mode": "off",
		}}, nil
	case 8:
		if state.DirectChildID == "" {
			return scriptedToolCall{}, errors.New("foreground agent_spawn result did not expose session_id")
		}
		return scriptedToolCall{Name: "agent_prompt", Arguments: map[string]any{
			"session_id": state.DirectChildID,
			"message":    "Finish within the extended budget attempt.",
			"budget_extension": map[string]any{
				"add_turns": 1,
				"reason":    "browser smoke foreground resume",
			},
		}}, nil
	case 9:
		if state.DirectChildID == "" {
			return scriptedToolCall{}, errors.New("foreground child id is unavailable for status")
		}
		return scriptedToolCall{Name: "agent_status", Arguments: map[string]any{"session_id": state.DirectChildID}}, nil
	case 10:
		return scriptedToolCall{Name: "agent_spawn", Arguments: map[string]any{
			"prompt":         "BUDGET_CANCEL_CHILD: call read_file once and remain budget-paused until the parent cancels and settles the job.",
			"agent_name":     "budget-cancel-child",
			"agent_role":     "generator",
			"mode":           "exec",
			"background":     true,
			"resume_parent":  true,
			"isolation_mode": "off",
		}}, nil
	case 11:
		if state.BackgroundJobID == "" {
			return scriptedToolCall{}, errors.New("background agent_spawn result did not expose queue_job_id")
		}
		return scriptedToolCall{Name: "agent_stop", Arguments: map[string]any{"queue_job_id": state.BackgroundJobID}}, nil
	case 12:
		return scriptedToolCall{Name: "agent_list", Arguments: map[string]any{}}, nil
	case 13:
		return scriptedToolCall{Name: "todo_write", Arguments: map[string]any{
			"todos": []map[string]any{{"content": todoContent, "status": "completed", "priority": "high"}},
		}}, nil
	default:
		return scriptedToolCall{Name: "finish", Arguments: map[string]any{"message": "budget browser parent complete"}}, nil
	}
}

func inputText(input []any) string {
	payload, _ := json.Marshal(input)
	return string(payload)
}

func writeTextResponse(w http.ResponseWriter, sessionID string, callNumber int, text string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     fmt.Sprintf("resp_%s_%d", sanitizeID(sessionID), callNumber),
		"status": "completed",
		"output": []map[string]any{{
			"type": "message",
			"content": []map[string]any{{
				"type": "output_text",
				"text": text,
			}},
		}},
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
}

func captureToolReferences(input []any, state *sessionScriptState) {
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || item["type"] != "function_call_output" {
			continue
		}
		output, _ := item["output"].(string)
		if strings.TrimSpace(output) == "" {
			continue
		}
		captureCommandArtifactPath(output, state)
		var value map[string]any
		if err := json.Unmarshal([]byte(output), &value); err != nil {
			continue
		}
		if sessionID := metadataString(value, "session_id"); sessionID != "" {
			state.DirectChildID = sessionID
		}
		if queueJobID := metadataString(value, "queue_job_id"); queueJobID != "" {
			state.BackgroundJobID = queueJobID
		}
		captureHistoryReferences(value, state)
	}
}

func captureCommandArtifactPath(output string, state *sessionScriptState) {
	const prefix = "[Complete artifact: "
	start := strings.Index(output, prefix)
	if start < 0 {
		return
	}
	remainder := output[start+len(prefix):]
	end := strings.IndexByte(remainder, ';')
	if end < 0 {
		return
	}
	if path := strings.TrimSpace(remainder[:end]); path != "" {
		state.CommandArtifactPath = path
	}
}

func captureHistoryReferences(value map[string]any, state *sessionScriptState) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	var envelope struct {
		Mode           string `json:"mode"`
		HasMore        bool   `json:"has_more"`
		NextByteOffset *int64 `json:"next_byte_offset"`
		Messages       []struct {
			MessageID   string `json:"message_id"`
			ToolResults []struct {
				Name        string `json:"name"`
				OutputBytes int    `json:"output_bytes"`
			} `json:"tool_results"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return
	}
	if envelope.Mode == "message_content" {
		if envelope.HasMore && envelope.NextByteOffset != nil && *envelope.NextByteOffset > 0 {
			state.HistoryNextByteOffset = *envelope.NextByteOffset
		}
		return
	}
	for _, message := range envelope.Messages {
		for _, result := range message.ToolResults {
			if result.Name == "shell" && result.OutputBytes > 512 && strings.TrimSpace(message.MessageID) != "" {
				state.HistoryMessageID = message.MessageID
				return
			}
		}
	}
}

func (s *providerState) appendDecisionLog(facts requestFacts, callNumber int, toolName string) error {
	if s.logPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.logPath), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(map[string]any{
		"time":              time.Now().UTC().Format(time.RFC3339Nano),
		"session_id":        facts.SessionID,
		"parent_session_id": facts.ParentSessionID,
		"queue_job_id":      facts.QueueJobID,
		"agent_name":        facts.AgentName,
		"call":              callNumber,
		"tool":              toolName,
	})
}

func metadataString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func sanitizeID(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('_')
	}
	return builder.String()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
