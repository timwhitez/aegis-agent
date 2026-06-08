package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ngen/internal/task"
)

func TestCommandProviderHelperProcess(t *testing.T) {
	if len(os.Args) > 0 && os.Args[len(os.Args)-1] == "command-provider-huge-stderr" {
		fmt.Fprint(os.Stderr, strings.Repeat("x", commandProviderMaxOutputBytes+1024))
		os.Exit(9)
	}
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "command-provider-helper" {
		return
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read stdin: %v", err)
		os.Exit(2)
	}
	op := os.Getenv(commandProviderOperationEnv)
	mode := os.Getenv(commandProviderModeEnv)
	switch op {
	case "decision":
		if !strings.Contains(string(raw), `"task_id":"TASK-001"`) {
			fmt.Fprint(os.Stderr, "missing task id")
			os.Exit(3)
		}
		fmt.Fprintf(os.Stdout, `{"action":"run","summary":"mode=%s op=%s","watch_interval":"","watch_reason":"","approval_scope":"","approval_reason":""}`, mode, op)
	case "workspace_observation":
		if !strings.Contains(string(raw), `"command_budget":2`) {
			fmt.Fprint(os.Stderr, "missing command budget")
			os.Exit(4)
		}
		fmt.Fprintf(os.Stdout, `{"summary":"mode=%s op=%s","commands":[{"argv":["sed","-n","1,40p","calc.go"],"reason":"Read calc.go before editing"}]}`, mode, op)
	case "workspace_edit":
		if !strings.Contains(string(raw), `"repair_attempt":1`) {
			fmt.Fprint(os.Stderr, "missing repair attempt")
			os.Exit(5)
		}
		fmt.Fprintf(os.Stdout, `{"summary":"mode=%s op=%s","patch":"","writes":[{"path":"calc.go","content":"package main\n\nfunc Add(a, b int) int { return a + b }\n"}],"deletes":[],"commands":[]}`, mode, op)
	case "mission_validation":
		if !strings.Contains(string(raw), `"mission_id"`) {
			fmt.Fprint(os.Stderr, "missing mission id")
			os.Exit(7)
		}
		fmt.Fprintf(os.Stdout, `{"status":"passed","summary":"mode=%s op=%s","findings":[]}`, mode, op)
	default:
		fmt.Fprintf(os.Stderr, "unexpected operation: %s", op)
		os.Exit(6)
	}
	os.Exit(0)
}

func TestProviderResponseRejectsOversizedBody(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxProviderResponseBytes+1))
	}))
	defer server.Close()

	_, err := NewOpenAIResponsesDriver(task.ProviderConfig{
		Mode:    "openai-response",
		BaseURL: server.URL,
		Model:   "test-model",
	}).Decide(context.Background(), Input{
		Task:  task.Spec{TaskID: "TASK-001", Kind: task.KindCoding, Objective: "ship"},
		State: task.State{TaskID: "TASK-001", State: task.StateActive},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds max bytes") {
		t.Fatalf("expected oversized provider body to be rejected, got %v", err)
	}
}

func TestCommandProviderTruncatesOversizedStderr(t *testing.T) {
	_, err := runCommandProviderRaw(context.Background(), "command", []string{os.Args[0], "-test.run=TestCommandProviderHelperProcess", "--", "command-provider-huge-stderr"}, "decision", Input{
		Task: task.Spec{TaskID: "TASK-001"},
	})
	if err == nil {
		t.Fatal("expected command provider failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "stderr truncated") {
		t.Fatalf("expected truncated stderr diagnostic, got %v", err)
	}
	if strings.Count(msg, "x") > commandProviderMaxOutputBytes+16 {
		t.Fatalf("expected stderr to be capped, got %d x characters", strings.Count(msg, "x"))
	}
}

func TestDecodeMissionValidationRejectsDecisionActionFields(t *testing.T) {
	_, err := decodeMissionValidationPayload("test mission validator", []byte(`{
  "status": "blocking",
  "summary": "invalid action attempt",
  "action": "worker_spawn",
  "findings": []
}`))
	if err == nil {
		t.Fatal("expected mission validation payload with action field to fail")
	}
	if !strings.Contains(err.Error(), "invalid mission validation JSON") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOperatorPromptDecisionGreetingReturnsRespond(t *testing.T) {
	decision, ok, err := OperatorPromptDecision("hello")
	if err != nil {
		t.Fatalf("operator prompt decision: %v", err)
	}
	if !ok {
		t.Fatal("expected conversational greeting to be handled locally")
	}
	if decision.Action != "respond" {
		t.Fatalf("expected respond action, got %+v", decision)
	}
	if strings.TrimSpace(decision.ResponseText) == "" {
		t.Fatalf("expected response_text for greeting, got %+v", decision)
	}
}

func TestOperatorPromptDecisionGoalMissionHandledByBridge(t *testing.T) {
	for _, prompt := range []string{"/goal ship docs", "/mission ship docs", "/missions", "/goals release hardening"} {
		decision, ok, err := OperatorPromptDecision(prompt)
		if err != nil {
			t.Fatalf("operator prompt decision for %q: %v", prompt, err)
		}
		if !ok {
			t.Fatalf("expected %q to be handled locally", prompt)
		}
		if decision.Action != "noop" || !strings.Contains(decision.Summary, "mission bridge") {
			t.Fatalf("expected bridge noop for %q, got %+v", prompt, decision)
		}
	}
}

func TestPromptAndToolDescriptionsCarryArtifactFirstDiscipline(t *testing.T) {
	for name, text := range map[string]string{
		"decision":    defaultSystemPrompt(""),
		"edit":        workspaceEditSystemPrompt(""),
		"observation": workspaceObservationSystemPrompt(""),
	} {
		for _, want := range []string{
			"Return only",
			"schema",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s prompt missing %q:\n%s", name, want, text)
			}
		}
	}
	decisionPrompt := defaultSystemPrompt("")
	for _, want := range []string{
		"choose exactly one next runtime action",
		"Task artifacts are the system of record",
		"do not execute commands, edit files, call tools, or invent hidden state",
		"Surface blockers explicitly",
	} {
		if !strings.Contains(decisionPrompt, want) {
			t.Fatalf("decision prompt missing %q:\n%s", want, decisionPrompt)
		}
	}
	editPrompt := workspaceEditSystemPrompt("")
	for _, want := range []string{
		"bounded, schema-valid repair plan",
		"Prefer surgical root-cause fixes",
		"If context is insufficient for a safe edit",
	} {
		if !strings.Contains(editPrompt, want) {
			t.Fatalf("workspace edit prompt missing %q:\n%s", want, editPrompt)
		}
	}
	observationPrompt := workspaceObservationSystemPrompt("")
	for _, want := range []string{
		"read-only workspace inspection",
		"zero commands is preferred",
		"Prefer exact, narrow discovery",
	} {
		if !strings.Contains(observationPrompt, want) {
			t.Fatalf("workspace observation prompt missing %q:\n%s", want, observationPrompt)
		}
	}

	decisionInput := Input{
		Task:  task.Spec{TaskID: "TASK-001", Kind: task.KindCoding, Objective: "ship fix"},
		State: task.State{TaskID: "TASK-001", State: task.StateActive},
	}
	chatDecisionBody, err := (&OpenAIChatCompletionsDriver{Model: "test-model"}).requestBody(decisionInput)
	if err != nil {
		t.Fatalf("build chat decision body: %v", err)
	}
	if got := chatToolDescription(t, chatDecisionBody); !strings.Contains(got, "exactly one schema-valid NGEN runtime decision") || !strings.Contains(got, "do not execute commands") {
		t.Fatalf("unexpected chat decision tool description: %q", got)
	}
	anthropicDecisionBody, err := (&AnthropicMessagesDriver{Model: "test-model"}).requestBody(decisionInput)
	if err != nil {
		t.Fatalf("build anthropic decision body: %v", err)
	}
	if got := anthropicToolDescription(t, anthropicDecisionBody); !strings.Contains(got, "exactly one schema-valid NGEN runtime decision") || !strings.Contains(got, "do not execute commands") {
		t.Fatalf("unexpected anthropic decision tool description: %q", got)
	}

	cfg := task.ProviderConfig{Model: "test-model"}
	editInput := WorkspaceEditInput{
		Task:  task.Spec{TaskID: "TASK-001", Kind: task.KindCoding, Objective: "repair calc"},
		Files: []WorkspaceFile{{Path: "calc.go", Content: "package main\n"}},
	}
	chatEditBody, err := workspaceEditChatCompletionsRequestBody(cfg, editInput)
	if err != nil {
		t.Fatalf("build chat edit body: %v", err)
	}
	if got := chatToolDescription(t, chatEditBody); !strings.Contains(got, "minimal patch/write/delete operations") || !strings.Contains(got, "grounded only") {
		t.Fatalf("unexpected chat edit tool description: %q", got)
	}
	anthropicEditBody, err := workspaceEditAnthropicRequestBody(cfg, editInput)
	if err != nil {
		t.Fatalf("build anthropic edit body: %v", err)
	}
	if got := anthropicToolDescription(t, anthropicEditBody); !strings.Contains(got, "minimal patch/write/delete operations") || !strings.Contains(got, "grounded only") {
		t.Fatalf("unexpected anthropic edit tool description: %q", got)
	}

	observationInput := WorkspaceObservationInput{
		Task:          task.Spec{TaskID: "TASK-001", Kind: task.KindCoding, Objective: "inspect calc"},
		CommandBudget: 1,
		Files:         []WorkspaceFile{{Path: "calc.go", Content: "package main\n"}},
	}
	chatObservationBody, err := workspaceObservationChatCompletionsRequestBody(cfg, observationInput)
	if err != nil {
		t.Fatalf("build chat observation body: %v", err)
	}
	if got := chatToolDescription(t, chatObservationBody); !strings.Contains(got, "fewest read-only argv inspection commands") || !strings.Contains(got, "return zero commands") {
		t.Fatalf("unexpected chat observation tool description: %q", got)
	}
	anthropicObservationBody, err := workspaceObservationAnthropicRequestBody(cfg, observationInput)
	if err != nil {
		t.Fatalf("build anthropic observation body: %v", err)
	}
	if got := anthropicToolDescription(t, anthropicObservationBody); !strings.Contains(got, "fewest read-only argv inspection commands") || !strings.Contains(got, "return zero commands") {
		t.Fatalf("unexpected anthropic observation tool description: %q", got)
	}
}

func chatToolDescription(t *testing.T, body []byte) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal chat body: %v", err)
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected chat tools in body, got %#v", payload["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected chat tool object, got %#v", tools[0])
	}
	function, ok := tool["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected chat function object, got %#v", tool["function"])
	}
	description, ok := function["description"].(string)
	if !ok {
		t.Fatalf("expected chat tool description string, got %#v", function["description"])
	}
	return description
}

func anthropicToolDescription(t *testing.T, body []byte) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal anthropic body: %v", err)
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected anthropic tools in body, got %#v", payload["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected anthropic tool object, got %#v", tools[0])
	}
	description, ok := tool["description"].(string)
	if !ok {
		t.Fatalf("expected anthropic tool description string, got %#v", tool["description"])
	}
	return description
}

func TestAnthropicRequestBodiesAddCacheControlBreakpoints(t *testing.T) {
	cfg := task.ProviderConfig{Model: "test-model"}
	decisionBody, err := (&AnthropicMessagesDriver{Model: "test-model"}).requestBody(Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "ship cache-aware prompts",
		},
		State: task.State{TaskID: "TASK-001", State: task.StateActive},
	})
	if err != nil {
		t.Fatalf("build anthropic decision body: %v", err)
	}
	assertAnthropicCacheBreakpoints(t, decisionBody, "Task context JSON:", "\n  \"state\":")

	editBody, err := workspaceEditAnthropicRequestBody(cfg, WorkspaceEditInput{
		Task:  task.Spec{TaskID: "TASK-001", Kind: task.KindCoding, Objective: "repair calc"},
		Files: []WorkspaceFile{{Path: "calc.go", Content: "package main\n"}},
	})
	if err != nil {
		t.Fatalf("build anthropic edit body: %v", err)
	}
	assertAnthropicCacheBreakpoints(t, editBody, "Workspace edit context JSON:", "\n  \"collection\":")

	observationBody, err := workspaceObservationAnthropicRequestBody(cfg, WorkspaceObservationInput{
		Task:          task.Spec{TaskID: "TASK-001", Kind: task.KindCoding, Objective: "inspect calc"},
		CommandBudget: 1,
		Files:         []WorkspaceFile{{Path: "calc.go", Content: "package main\n"}},
	})
	if err != nil {
		t.Fatalf("build anthropic observation body: %v", err)
	}
	assertAnthropicCacheBreakpoints(t, observationBody, "Workspace observation context JSON:", "\n  \"collection\":")

	missionBody, err := missionValidationAnthropicRequestBody(cfg, MissionValidationInput{
		Mission: task.Mission{
			MissionID:             "MIS-001",
			Title:                 "Cache validation",
			Objective:             "validate cache-aware mission artifacts",
			RootTaskID:            "TASK-001",
			ValidationContractRef: "validation_contract.json",
		},
		Contract: task.MissionValidationContract{
			MissionID:  "MIS-001",
			ContractID: "MCON-001",
		},
		Features:   task.MissionFeatureSet{MissionID: "MIS-001"},
		Milestones: task.MissionMilestoneSet{MissionID: "MIS-001"},
		RootStatus: &task.StatusSnapshot{TaskID: "TASK-001", State: task.StateDone},
	})
	if err != nil {
		t.Fatalf("build anthropic mission body: %v", err)
	}
	assertAnthropicCacheBreakpoints(t, missionBody, "Mission validation context JSON:", "\n  \"root_status\":")
}

func TestAnthropicPromptVolatileSplitPreservesPromptText(t *testing.T) {
	prompt := strings.TrimSpace(`
Choose a step.

Task context JSON:
{
  "task": {
    "task_id": "TASK-001"
  },
  "plan": {
    "revision": 2
  },
  "state": {
    "state": "Active"
  },
  "recent_events": [{
    "type": "provider_decided"
  }]
}`)
	blocks := anthropicPromptTextBlocks(prompt)
	if len(blocks) != 3 {
		t.Fatalf("expected stable prelude, stable JSON prefix, and volatile tail blocks, got %+v", blocks)
	}
	joined := blocks[0].Text + blocks[1].Text + blocks[2].Text
	if joined != prompt {
		t.Fatalf("expected split blocks to preserve exact prompt text\nwant:\n%s\ngot:\n%s", prompt, joined)
	}
	if !strings.HasSuffix(blocks[0].Text, "Task context JSON:") {
		t.Fatalf("expected first block to end at context marker, got %q", blocks[0].Text)
	}
	if !strings.Contains(blocks[1].Text, "\"plan\"") || strings.Contains(blocks[1].Text, "\"state\"") {
		t.Fatalf("expected second block to contain stable JSON prefix only, got %q", blocks[1].Text)
	}
	if !strings.HasPrefix(blocks[2].Text, "\n  \"state\":") {
		t.Fatalf("expected volatile tail to start at state, got %q", blocks[2].Text)
	}
	for _, block := range blocks {
		if block.CacheControl == nil || block.CacheControl.Type != anthropicCacheControlTypeEphemeral {
			t.Fatalf("expected cacheable split block, got %+v", block)
		}
	}
}

func TestAnthropicResponsesParseUsageMetadata(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	type requestRecord struct {
		toolName string
		usage    string
	}
	requests := []requestRecord{
		{
			toolName: decisionToolName,
			usage:    `"usage":{"input_tokens":101,"output_tokens":17,"cache_creation_input_tokens":83,"cache_read_input_tokens":197},`,
		},
		{
			toolName: workspaceEditToolName,
			usage:    `"usage":{"input_tokens":102,"output_tokens":18,"cache_creation_input_tokens":84,"cache_read_input_tokens":198},`,
		},
		{
			toolName: workspaceObservationToolName,
			usage:    `"usage":{"input_tokens":103,"output_tokens":19,"cache_creation_input_tokens":85,"cache_read_input_tokens":199},`,
		},
		{
			toolName: "submit_mission_validation",
			usage:    `"usage":{"input_tokens":104,"output_tokens":20,"cache_creation_input_tokens":86,"cache_read_input_tokens":200},`,
		},
	}
	requestIndex := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestIndex >= len(requests) {
			t.Fatalf("unexpected request %d", requestIndex)
		}
		record := requests[requestIndex]
		requestIndex++
		w.Header().Set("Content-Type", "application/json")
		switch record.toolName {
		case decisionToolName:
			fmt.Fprintf(w, `{
				%s
				"content": [{
					"type": "tool_use",
					"name": "submit_decision",
					"input": {"action":"run","summary":"Run with usage"}
				}]
			}`, record.usage)
		case workspaceEditToolName:
			fmt.Fprintf(w, `{
				%s
				"content": [{
					"type": "tool_use",
					"name": "submit_workspace_edit",
					"input": {"summary":"Edit with usage","patch":"","writes":[],"deletes":[],"commands":[]}
				}]
			}`, record.usage)
		case workspaceObservationToolName:
			fmt.Fprintf(w, `{
				%s
				"content": [{
					"type": "tool_use",
					"name": "submit_workspace_observation",
					"input": {"summary":"Observe with usage","commands":[]}
				}]
			}`, record.usage)
		case "submit_mission_validation":
			fmt.Fprintf(w, `{
				%s
				"content": [{
					"type": "tool_use",
					"name": "submit_mission_validation",
					"input": {"status":"passed","summary":"Validate with usage","findings":[]}
				}]
			}`, record.usage)
		default:
			t.Fatalf("unexpected tool %s", record.toolName)
		}
	}))
	defer server.Close()

	cfg := task.ProviderConfig{Mode: "anthropic", BaseURL: server.URL + "/v1", Model: "claude-test", APIKeyEnv: "OPENAI_API_KEY"}
	decision, err := NewAnthropicMessagesDriver(cfg).Decide(context.Background(), Input{
		Task:  task.Spec{TaskID: "TASK-001", Kind: task.KindCoding, Objective: "decide"},
		State: task.State{TaskID: "TASK-001", State: task.StateActive},
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	assertProviderUsage(t, decision.TokenUsage, decision.PromptCacheUsage, "101", "17", "83", "197")

	edit, err := GenerateWorkspaceEdit(context.Background(), cfg, WorkspaceEditInput{
		Task: task.Spec{TaskID: "TASK-001", Kind: task.KindCoding, Objective: "edit"},
	})
	if err != nil {
		t.Fatalf("workspace edit: %v", err)
	}
	assertProviderUsage(t, edit.TokenUsage, edit.PromptCacheUsage, "102", "18", "84", "198")

	observation, err := GenerateWorkspaceObservations(context.Background(), cfg, WorkspaceObservationInput{
		Task:          task.Spec{TaskID: "TASK-001", Kind: task.KindCoding, Objective: "observe"},
		CommandBudget: 1,
	})
	if err != nil {
		t.Fatalf("workspace observation: %v", err)
	}
	assertProviderUsage(t, observation.TokenUsage, observation.PromptCacheUsage, "103", "19", "85", "199")

	validation, err := GenerateMissionValidation(context.Background(), cfg, MissionValidationInput{
		Mission:    task.Mission{MissionID: "MIS-001", RootTaskID: "TASK-001"},
		Contract:   task.MissionValidationContract{MissionID: "MIS-001", ContractID: "MCON-001"},
		Features:   task.MissionFeatureSet{MissionID: "MIS-001"},
		Milestones: task.MissionMilestoneSet{MissionID: "MIS-001"},
	})
	if err != nil {
		t.Fatalf("mission validation: %v", err)
	}
	assertProviderUsage(t, validation.TokenUsage, validation.PromptCacheUsage, "104", "20", "86", "200")
	if requestIndex != len(requests) {
		t.Fatalf("expected %d requests, got %d", len(requests), requestIndex)
	}
}

func TestAnthropicResponsesUseUnknownUsageWhenOmitted(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content": [{
				"type": "tool_use",
				"name": "submit_decision",
				"input": {"action":"run","summary":"Run without usage"}
			}]
		}`))
	}))
	defer server.Close()

	decision, err := NewAnthropicMessagesDriver(task.ProviderConfig{
		Mode:      "anthropic",
		BaseURL:   server.URL + "/v1",
		Model:     "claude-test",
		APIKeyEnv: "OPENAI_API_KEY",
	}).Decide(context.Background(), Input{
		Task:  task.Spec{TaskID: "TASK-001", Kind: task.KindCoding, Objective: "decide"},
		State: task.State{TaskID: "TASK-001", State: task.StateActive},
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decision.TokenUsage != "unknown" || decision.PromptCacheUsage != "unknown" {
		t.Fatalf("expected unknown usage when omitted, got token=%q cache=%q", decision.TokenUsage, decision.PromptCacheUsage)
	}
}

func assertProviderUsage(t *testing.T, tokenUsage, promptCacheUsage, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens string) {
	t.Helper()
	for _, want := range []string{
		"input_tokens=" + inputTokens,
		"output_tokens=" + outputTokens,
	} {
		if !strings.Contains(tokenUsage, want) {
			t.Fatalf("expected token usage %q to contain %q", tokenUsage, want)
		}
	}
	for _, want := range []string{
		"cache_creation_input_tokens=" + cacheCreationTokens,
		"cache_read_input_tokens=" + cacheReadTokens,
	} {
		if !strings.Contains(promptCacheUsage, want) {
			t.Fatalf("expected prompt cache usage %q to contain %q", promptCacheUsage, want)
		}
	}
}

func assertAnthropicCacheBreakpoints(t *testing.T, body []byte, contextMarker, volatileMarker string) {
	t.Helper()
	var payload struct {
		System []struct {
			Type         string `json:"type"`
			Text         string `json:"text"`
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control,omitempty"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type         string `json:"type"`
				Text         string `json:"text"`
				CacheControl *struct {
					Type string `json:"type"`
				} `json:"cache_control,omitempty"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal anthropic cache body: %v", err)
	}
	if len(payload.System) != 1 {
		t.Fatalf("expected one system text block, got %+v", payload.System)
	}
	if payload.System[0].Type != "text" || payload.System[0].CacheControl == nil || payload.System[0].CacheControl.Type != anthropicCacheControlTypeEphemeral {
		t.Fatalf("expected cacheable system text block, got %+v", payload.System[0])
	}
	if len(payload.Messages) != 1 || payload.Messages[0].Role != "user" {
		t.Fatalf("expected one user message, got %+v", payload.Messages)
	}
	content := payload.Messages[0].Content
	if len(content) != 3 {
		t.Fatalf("expected three cacheable user content blocks, got %+v", content)
	}
	if !strings.Contains(content[0].Text, contextMarker) {
		t.Fatalf("expected first content block to end at %q, got %q", contextMarker, content[0].Text)
	}
	if !strings.HasSuffix(content[0].Text, contextMarker) {
		t.Fatalf("expected first content block to end at %q, got %q", contextMarker, content[0].Text)
	}
	if strings.Contains(content[1].Text, volatileMarker) {
		t.Fatalf("expected second content block to stop before volatile marker %q, got %q", volatileMarker, content[1].Text)
	}
	if !strings.HasPrefix(content[2].Text, volatileMarker) {
		t.Fatalf("expected third content block to start at volatile marker %q, got %q", volatileMarker, content[2].Text)
	}
	joined := content[0].Text + content[1].Text + content[2].Text
	if !strings.Contains(joined, contextMarker) || !strings.Contains(joined, volatileMarker) {
		t.Fatalf("expected joined user content to preserve context and volatile markers, got %q", joined)
	}
	for _, block := range content {
		if block.Type != "text" || block.CacheControl == nil || block.CacheControl.Type != anthropicCacheControlTypeEphemeral {
			t.Fatalf("expected cacheable user text block, got %+v", block)
		}
	}
}

func helperProviderCommand(t *testing.T) []string {
	t.Helper()
	return []string{os.Args[0], "-test.run=TestCommandProviderHelperProcess", "--", "command-provider-helper"}
}

func TestBuiltinGenerateWorkspaceEditRepairsMissingAdd(t *testing.T) {
	plan, err := GenerateWorkspaceEdit(context.Background(), task.ProviderConfig{
		Mode: "builtin",
	}, WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		Files: []WorkspaceFile{
			{Path: "calc.go", Content: "package main\n\n// TODO: implement Add.\n"},
			{Path: "calc_test.go", Content: "package main\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n"},
		},
	})
	if err != nil {
		t.Fatalf("generate workspace edit: %v", err)
	}
	if len(plan.Writes) != 1 || plan.Writes[0].Path != "calc.go" {
		t.Fatalf("unexpected builtin writes: %+v", plan.Writes)
	}
	if !strings.Contains(plan.Writes[0].Content, "func Add(a, b int) int") || !strings.Contains(plan.Writes[0].Content, "return a + b") {
		t.Fatalf("expected builtin repair to implement Add, got %q", plan.Writes[0].Content)
	}
}

func TestBuiltinGenerateWorkspaceObservationRequestsSearchWhenSnapshotTruncated(t *testing.T) {
	plan, err := GenerateWorkspaceObservations(context.Background(), task.ProviderConfig{
		Mode: "builtin",
	}, WorkspaceObservationInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		CommandBudget: 2,
		Collection: WorkspaceCollection{
			IncludedFileCount: 1,
			OmittedFileCount:  10,
			Truncated:         true,
			StopReason:        "workspace snapshot file budget reached",
		},
		Files: []WorkspaceFile{
			{Path: "readme.txt", Content: "notes"},
		},
	})
	if err != nil {
		t.Fatalf("generate builtin observations: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected one builtin observation command, got %+v", plan.Commands)
	}
	if got := strings.Join(plan.Commands[0].Argv, " "); got != "rg -n Add ." {
		t.Fatalf("unexpected builtin observation argv: %s", got)
	}
}

func TestCommandDriverUsesDecisionOperationProtocol(t *testing.T) {
	driver := New(task.ProviderConfig{
		Mode:    "command",
		Command: helperProviderCommand(t),
	})
	decision, err := driver.Decide(context.Background(), Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "ship fix",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decision.Action != "run" || decision.Summary != "mode=command op=decision" {
		t.Fatalf("unexpected command provider decision: %+v", decision)
	}
}

func TestGenerateWorkspaceObservationUsesCommandProtocol(t *testing.T) {
	plan, err := GenerateWorkspaceObservations(context.Background(), task.ProviderConfig{
		Mode:    "command",
		Command: helperProviderCommand(t),
	}, WorkspaceObservationInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		CommandBudget: 2,
		Files: []WorkspaceFile{
			{Path: "calc.go", Content: "package main\n"},
		},
	})
	if err != nil {
		t.Fatalf("generate command observations: %v", err)
	}
	if plan.Summary != "mode=command op=workspace_observation" {
		t.Fatalf("unexpected command observation summary: %+v", plan)
	}
	if len(plan.Commands) != 1 || strings.Join(plan.Commands[0].Argv, " ") != "sed -n 1,40p calc.go" {
		t.Fatalf("unexpected command observation plan: %+v", plan.Commands)
	}
}

func TestGenerateWorkspaceEditUsesCommandProtocol(t *testing.T) {
	plan, err := GenerateWorkspaceEdit(context.Background(), task.ProviderConfig{
		Mode:    "command",
		Command: helperProviderCommand(t),
	}, WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		RepairAttempt: 1,
		RepairBudget:  3,
		Files: []WorkspaceFile{
			{Path: "calc.go", Content: "package main\n"},
		},
	})
	if err != nil {
		t.Fatalf("generate command workspace edit: %v", err)
	}
	if plan.Summary != "mode=command op=workspace_edit" {
		t.Fatalf("unexpected command edit summary: %+v", plan)
	}
	if len(plan.Writes) != 1 || plan.Writes[0].Path != "calc.go" || !strings.Contains(plan.Writes[0].Content, "return a + b") {
		t.Fatalf("unexpected command edit plan: %+v", plan)
	}
}

func TestOpenAIResponsesDriverUsesResponsesEndpointAndSchema(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	var seenPath string
	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_decision_ok",
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "{\"action\":\"wait\",\"summary\":\"Need a durable watch\",\"watch_interval\":\"5m\"}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	driver := New(task.ProviderConfig{
		Mode:          "responses",
		BaseURL:       server.URL + "/v1",
		Model:         "gpt-5.4",
		APIKeyEnv:     "OPENAI_API_KEY",
		ThinkingLevel: "xhigh",
	})
	decision, err := driver.Decide(context.Background(), Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindGeneral,
			Objective: "review docs",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if seenPath != "/v1/responses" {
		t.Fatalf("expected responses path, got %s", seenPath)
	}
	if got := seenBody["max_output_tokens"]; got != float64(2048) {
		t.Fatalf("expected default decision max output tokens 2048, got %#v", got)
	}
	reasoning, ok := seenBody["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "xhigh" {
		t.Fatalf("expected configured reasoning effort in request, got %#v", seenBody["reasoning"])
	}
	if decision.Action != "wait" || decision.WatchInterval != "5m" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	text, ok := seenBody["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in request, got %#v", seenBody["text"])
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		t.Fatalf("expected text.format in request, got %#v", text["format"])
	}
	if format["type"] != "json_schema" {
		t.Fatalf("expected json_schema format, got %#v", format["type"])
	}
	schema, ok := format["schema"].(map[string]any)
	if !ok {
		t.Fatalf("expected schema in request, got %#v", format["schema"])
	}
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("expected required list in schema, got %#v", schema["required"])
	}
	if len(required) != 28 {
		t.Fatalf("expected twenty-eight required keys for strict responses schema, got %#v", required)
	}
	foundResponseText := false
	foundTaskKind := false
	foundTaskPresetID := false
	foundTaskTitle := false
	foundTaskObjective := false
	foundTaskCriteria := false
	foundTaskConstraints := false
	foundTaskPermissionModeID := false
	foundProjectStepID := false
	foundProjectBranchID := false
	foundPlanExplanation := false
	foundPlanSteps := false
	foundPlanPatchOperations := false
	foundProjectExplanation := false
	foundProjectSteps := false
	foundProjectBranches := false
	foundProjectPatchOperations := false
	foundMemoryKind := false
	foundMemoryRefs := false
	foundWorkerID := false
	foundWorkerRole := false
	foundWorkerObjective := false
	for _, item := range required {
		value, ok := item.(string)
		if !ok {
			continue
		}
		switch value {
		case "response_text":
			foundResponseText = true
		case "task_kind":
			foundTaskKind = true
		case "task_preset_id":
			foundTaskPresetID = true
		case "task_title":
			foundTaskTitle = true
		case "task_objective":
			foundTaskObjective = true
		case "task_criteria":
			foundTaskCriteria = true
		case "task_constraints":
			foundTaskConstraints = true
		case "task_permission_mode_id":
			foundTaskPermissionModeID = true
		case "project_step_id":
			foundProjectStepID = true
		case "project_branch_id":
			foundProjectBranchID = true
		case "plan_explanation":
			foundPlanExplanation = true
		case "plan_steps":
			foundPlanSteps = true
		case "plan_patch_operations":
			foundPlanPatchOperations = true
		case "project_explanation":
			foundProjectExplanation = true
		case "project_steps":
			foundProjectSteps = true
		case "project_branches":
			foundProjectBranches = true
		case "project_patch_operations":
			foundProjectPatchOperations = true
		case "memory_kind":
			foundMemoryKind = true
		case "memory_refs":
			foundMemoryRefs = true
		case "worker_id":
			foundWorkerID = true
		case "worker_role":
			foundWorkerRole = true
		case "worker_objective":
			foundWorkerObjective = true
		}
	}
	if !foundResponseText || !foundTaskKind || !foundTaskPresetID || !foundTaskTitle || !foundTaskObjective || !foundTaskCriteria || !foundTaskConstraints || !foundTaskPermissionModeID || !foundProjectStepID || !foundProjectBranchID || !foundPlanExplanation || !foundPlanSteps || !foundPlanPatchOperations || !foundProjectExplanation || !foundProjectSteps || !foundProjectBranches || !foundProjectPatchOperations || !foundMemoryKind || !foundMemoryRefs || !foundWorkerID || !foundWorkerRole || !foundWorkerObjective {
		t.Fatalf("expected task-create, task/project mutation, and worker fields in strict responses schema, got %#v", required)
	}
	planSteps, ok := schema["properties"].(map[string]any)["plan_steps"].(map[string]any)
	if !ok {
		t.Fatalf("expected plan_steps schema in request, got %#v", schema["properties"])
	}
	items, ok := planSteps["items"].(map[string]any)
	if !ok {
		t.Fatalf("expected plan_steps item schema, got %#v", planSteps["items"])
	}
	properties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected plan_steps item properties, got %#v", items["properties"])
	}
	for _, key := range []string{"id", "parent_step_id", "depends_on", "priority"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("expected graph-capable plan_step property %q in schema, got %#v", key, properties)
		}
	}
	itemRequired, ok := items["required"].([]any)
	if !ok {
		t.Fatalf("expected required list on plan_steps items, got %#v", items["required"])
	}
	requiredSet := make(map[string]struct{}, len(itemRequired))
	for _, item := range itemRequired {
		text, ok := item.(string)
		if !ok {
			continue
		}
		requiredSet[text] = struct{}{}
	}
	for _, key := range []string{"id", "parent_step_id", "depends_on", "priority", "title", "status", "covers", "notes"} {
		if _, ok := requiredSet[key]; !ok {
			t.Fatalf("expected plan_steps item required list to include %q, got %#v", key, itemRequired)
		}
	}
	patchOps, ok := schema["properties"].(map[string]any)["plan_patch_operations"].(map[string]any)
	if !ok {
		t.Fatalf("expected plan_patch_operations schema in request, got %#v", schema["properties"])
	}
	patchItems, ok := patchOps["items"].(map[string]any)
	if !ok {
		t.Fatalf("expected plan_patch_operations item schema, got %#v", patchOps["items"])
	}
	patchProps, ok := patchItems["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected plan_patch_operations item properties, got %#v", patchItems["properties"])
	}
	for _, key := range []string{"op", "explanation", "step_id", "after_step_id", "step"} {
		if _, ok := patchProps[key]; !ok {
			t.Fatalf("expected plan_patch_operations item property %q, got %#v", key, patchProps)
		}
	}
	projectSteps, ok := schema["properties"].(map[string]any)["project_steps"].(map[string]any)
	if !ok {
		t.Fatalf("expected project_steps schema in request, got %#v", schema["properties"])
	}
	projectItems, ok := projectSteps["items"].(map[string]any)
	if !ok {
		t.Fatalf("expected project_steps item schema, got %#v", projectSteps["items"])
	}
	projectProps, ok := projectItems["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected project_steps item properties, got %#v", projectItems["properties"])
	}
	for _, key := range []string{"id", "parent_step_id", "depends_on", "priority", "branch_id", "task_id"} {
		if _, ok := projectProps[key]; !ok {
			t.Fatalf("expected project_steps item property %q, got %#v", key, projectProps)
		}
	}
	projectPatchOps, ok := schema["properties"].(map[string]any)["project_patch_operations"].(map[string]any)
	if !ok {
		t.Fatalf("expected project_patch_operations schema in request, got %#v", schema["properties"])
	}
	projectPatchItems, ok := projectPatchOps["items"].(map[string]any)
	if !ok {
		t.Fatalf("expected project_patch_operations item schema, got %#v", projectPatchOps["items"])
	}
	projectPatchProps, ok := projectPatchItems["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected project_patch_operations item properties, got %#v", projectPatchItems["properties"])
	}
	for _, key := range []string{"op", "step_id", "branch_id", "task_id", "depends_on", "status", "step", "branch"} {
		if _, ok := projectPatchProps[key]; !ok {
			t.Fatalf("expected project_patch_operations item property %q, got %#v", key, projectPatchProps)
		}
	}
	memoryRefs, ok := schema["properties"].(map[string]any)["memory_refs"].(map[string]any)
	if !ok {
		t.Fatalf("expected memory_refs schema in request, got %#v", schema["properties"])
	}
	if memoryRefs["type"] != "array" {
		t.Fatalf("expected memory_refs array schema, got %#v", memoryRefs)
	}
}

func TestOpenAIResponsesDriverUsesConfiguredDecisionTokenBudget(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_decision_budget",
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "{\"action\":\"respond\",\"summary\":\"Answer operator\",\"response_text\":\"done\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	driver := New(task.ProviderConfig{
		Mode:                    "responses",
		BaseURL:                 server.URL + "/v1",
		Model:                   "gpt-5.4",
		APIKeyEnv:               "OPENAI_API_KEY",
		DecisionMaxOutputTokens: 4096,
	})
	if _, err := driver.Decide(context.Background(), Input{
		Task:  task.Spec{TaskID: "TASK-001", Kind: task.KindGeneral, Objective: "answer"},
		State: task.State{TaskID: "TASK-001", State: task.StateActive},
	}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got := seenBody["max_output_tokens"]; got != float64(4096) {
		t.Fatalf("expected configured decision max output tokens 4096, got %#v", got)
	}
}

func TestOpenAIResponsesDriverRejectsInvalidDecision(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_bad_watch",
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "{\"action\":\"wait\",\"summary\":\"Bad watch\",\"watch_interval\":\"soon\"}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	driver := New(task.ProviderConfig{
		Mode:      "responses",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	})
	_, err := driver.Decide(context.Background(), Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindGeneral,
			Objective: "review docs",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
	})
	if err == nil {
		t.Fatal("expected invalid watch interval error")
	}
}

func TestOpenAIResponsesDriverSurfacesStringError(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"model gpt-5.5 is unavailable"}`))
	}))
	defer server.Close()

	driver := New(task.ProviderConfig{
		Mode:      "responses",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.5",
		APIKeyEnv: "OPENAI_API_KEY",
	})
	_, err := driver.Decide(context.Background(), Input{
		Task:  task.Spec{TaskID: "TASK-001", Kind: task.KindGeneral, Objective: "review docs"},
		State: task.State{TaskID: "TASK-001", State: task.StateActive},
	})
	if err == nil {
		t.Fatal("expected provider error")
	}
	text := err.Error()
	for _, want := range []string{"responses provider returned 400 Bad Request", "model gpt-5.5 is unavailable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected error to include %q, got %q", want, text)
		}
	}
	if strings.Contains(text, "invalid JSON") {
		t.Fatalf("expected provider error diagnostic instead of decode failure, got %q", text)
	}
}

func TestOpenAIResponsesDriverInvalidJSONIncludesResponseIDAndRawExcerpt(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_invalid_json",
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "not-json truncated decision payload"
				}]
			}]
		}`))
	}))
	defer server.Close()

	driver := New(task.ProviderConfig{
		Mode:      "responses",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	})
	_, err := driver.Decide(context.Background(), Input{
		Task:  task.Spec{TaskID: "TASK-001", Kind: task.KindGeneral, Objective: "review docs"},
		State: task.State{TaskID: "TASK-001", State: task.StateActive},
	})
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
	text := err.Error()
	for _, want := range []string{"response_id=resp_invalid_json", "raw_excerpt=", "not-json truncated decision payload"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected error to include %q, got %q", want, text)
		}
	}
}

func TestGenerateWorkspaceEditUsesResponsesEndpointAndSchema(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("expected responses path, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
						"text": "{\"summary\":\"Implement Add\",\"patch\":\"\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package main\\n\\nfunc Add(a, b int) int { return a + b }\\n\"}],\"deletes\":[],\"commands\":[]}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	plan, err := GenerateWorkspaceEdit(context.Background(), task.ProviderConfig{
		Mode:      "responses",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	}, WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		Files: []WorkspaceFile{
			{Path: "calc.go", Content: "package main\n"},
			{Path: "calc_test.go", Content: "package main\n"},
		},
	})
	if err != nil {
		t.Fatalf("generate workspace edit: %v", err)
	}
	if plan.Summary != "Implement Add" {
		t.Fatalf("unexpected plan summary: %+v", plan)
	}
	if len(plan.Writes) != 1 || plan.Writes[0].Path != "calc.go" {
		t.Fatalf("unexpected writes: %+v", plan.Writes)
	}
	text, ok := seenBody["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in request, got %#v", seenBody["text"])
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		t.Fatalf("expected text.format in request, got %#v", text["format"])
	}
	if format["name"] != "ngen_workspace_edit" {
		t.Fatalf("expected workspace edit schema name, got %#v", format["name"])
	}
	schema, ok := format["schema"].(map[string]any)
	if !ok {
		t.Fatalf("expected schema in request, got %#v", format["schema"])
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 5 {
		t.Fatalf("expected five required workspace edit keys, got %#v", schema["required"])
	}
}

func TestGenerateWorkspaceEditAcceptsResponsesTextContent(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "text",
					"text": "{\"summary\":\"Write Multica result\",\"patch\":\"\",\"writes\":[{\"path\":\"multica-result.md\",\"content\":\"multica issue comment add 0496acb9-ff48-4507-bb79-d122a68c3a98 --body ngen-multica-real-e2e-ok\\n\"}],\"deletes\":[],\"commands\":[]}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	plan, err := GenerateWorkspaceEdit(context.Background(), task.ProviderConfig{
		Mode:      "responses",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	}, WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "add Multica marker",
		},
		Files: []WorkspaceFile{{Path: "AGENTS.md", Content: "runtime brief\n"}},
	})
	if err != nil {
		t.Fatalf("generate workspace edit: %v", err)
	}
	if len(plan.Writes) != 1 || plan.Writes[0].Path != "multica-result.md" || !strings.Contains(plan.Writes[0].Content, "ngen-multica-real-e2e-ok") {
		t.Fatalf("unexpected plan writes: %+v", plan.Writes)
	}
}

func TestBuildWorkspaceEditPromptIncludesPreviousFailures(t *testing.T) {
	prompt, err := buildWorkspaceEditPrompt(WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		ContextPack: &task.ContextSummary{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			PackID:        "PACK-001",
			Summary:       "Repair Add without touching tests.",
			NextStep:      "Edit calc.go and rerun go test.",
		},
		PreviousFailures: []RepairFailure{
			{
				Attempt: 1,
				Stage:   "workspace_edit_failed",
				Summary: "Try a patch first Apply failed: patch hunk context not found",
			},
		},
	})
	if err != nil {
		t.Fatalf("build workspace edit prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"previous_failures\"") {
		t.Fatalf("expected previous_failures in prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "patch hunk context not found") {
		t.Fatalf("expected prior failure summary in prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Do not repeat the same bad repair path.") {
		t.Fatalf("expected prompt guidance about prior failures, got %q", prompt)
	}
	if !strings.Contains(prompt, "You may include bounded workspace commands") {
		t.Fatalf("expected prompt guidance about bounded workspace commands, got %q", prompt)
	}
	if !strings.Contains(prompt, "prefer updating the source inputs and using a bounded workspace command") {
		t.Fatalf("expected prompt guidance about generated artifacts and command-backed repair, got %q", prompt)
	}
	if !strings.Contains(prompt, "\"context_pack\"") || !strings.Contains(prompt, "Repair Add without touching tests.") {
		t.Fatalf("expected context_pack in prompt, got %q", prompt)
	}
}

func TestBuildWorkspaceEditPromptIncludesSessionTranscriptContext(t *testing.T) {
	prompt, err := buildWorkspaceEditPrompt(WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "repair calc.go without touching README.md",
		},
		SessionMessagesRef: "workspace:.ngen/sessions/SES-001.messages.jsonl",
		SessionRecentMessages: []task.SessionMessage{
			{
				SchemaVersion: task.SchemaVersion,
				MessageID:     "MSG-001",
				SessionID:     "SES-001",
				TaskID:        "TASK-001",
				Role:          "operator",
				Content:       "Keep README.md untouched while fixing calc.go.",
			},
			{
				SchemaVersion: task.SchemaVersion,
				MessageID:     "MSG-002",
				SessionID:     "SES-001",
				TaskID:        "TASK-001",
				Role:          "runtime",
				Content:       "Failed Verify: go test still fails in calc.go.",
			},
		},
	})
	if err != nil {
		t.Fatalf("build workspace edit prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"session_messages_ref\"") || !strings.Contains(prompt, "\"session_recent_messages\"") {
		t.Fatalf("expected session transcript fields in workspace edit prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Keep README.md untouched while fixing calc.go.") {
		t.Fatalf("expected operator transcript message in workspace edit prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Failed Verify: go test still fails in calc.go.") {
		t.Fatalf("expected runtime transcript message in workspace edit prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use session_recent_messages plus session_messages_ref") {
		t.Fatalf("expected explicit session continuity guidance in workspace edit prompt, got %q", prompt)
	}
}

func TestBuildWorkspaceEditPromptIncludesBaselineRepoBearings(t *testing.T) {
	prompt, err := buildWorkspaceEditPrompt(WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "use repo-owned setup and verifier commands",
		},
		Baseline: &task.Baseline{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			CommandHints: []task.CommandHint{
				{
					Kind:      "verify",
					Command:   []string{"./build.sh", "test"},
					Reason:    "Repo-owned verifier command for this task.",
					SourceRef: "workspace:ngen.json",
				},
			},
			WorkspaceSnapshot: &task.WorkspaceSnapshot{
				Git: &task.GitSummary{
					IsRepository:  true,
					Branch:        "main",
					Head:          "abc1234",
					StatusSummary: "clean working tree",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build workspace edit prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"baseline\"") || !strings.Contains(prompt, "\"command_hints\"") || !strings.Contains(prompt, "\"workspace_snapshot\"") {
		t.Fatalf("expected baseline repo bearings in workspace edit prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use baseline.command_hints and baseline.workspace_snapshot as durable repo bearings") {
		t.Fatalf("expected repo bearings guidance in workspace edit prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "./build.sh") || !strings.Contains(prompt, "clean working tree") {
		t.Fatalf("expected baseline verifier hint and git snapshot in workspace edit prompt, got %q", prompt)
	}
}

func TestBuildWorkspaceEditPromptIncludesContinuitySnapshot(t *testing.T) {
	prompt, err := buildWorkspaceEditPrompt(WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "repair the active sprint cleanly",
		},
		Continuity: &task.ContinuitySnapshot{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			SnapshotID:    "CNT-001",
			Summary:       "Continue the current sprint without reopening unrelated files.",
			NextStep:      "Fix calc.go and rerun the repo verifier.",
			CurrentFocus: task.ContinuityFocus{
				CurrentExecutionStepID:    "exec.calc",
				CurrentExecutionStepTitle: "Repair calc.go",
				CriterionIDs:              []string{"SC-001"},
				WorkingSetPaths:           []string{"calc.go", "README.md"},
			},
			StartupChecklist: []task.ContinuityChecklistItem{
				{
					ID:      "git_status",
					Kind:    "vcs_command",
					Title:   "Inspect git status",
					Command: []string{"git", "status", "--short"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build workspace edit prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"continuity\"") || !strings.Contains(prompt, "\"current_focus\"") || !strings.Contains(prompt, "\"startup_checklist\"") {
		t.Fatalf("expected continuity snapshot in workspace edit prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use continuity.current_focus as the current sprint contract") {
		t.Fatalf("expected continuity guidance in workspace edit prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Repair calc.go") || !strings.Contains(prompt, "git status") {
		t.Fatalf("expected continuity focus and checklist details in workspace edit prompt, got %q", prompt)
	}
}

func TestBuildWorkspaceEditPromptIncludesProjectFocus(t *testing.T) {
	prompt, err := buildWorkspaceEditPrompt(WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "repair the project-bound sprint cleanly",
		},
		ContextPack: &task.ContextSummary{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			PackID:        "PACK-001",
			ProjectFocus: &task.ProjectTaskContext{
				PrimaryStepID:          "phase.impl",
				PrimaryBranchID:        "branch.impl",
				UnmetDependencyStepIDs: []string{"phase.repo_truth"},
			},
		},
		Continuity: &task.ContinuitySnapshot{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			SnapshotID:    "CNT-001",
			CurrentFocus: task.ContinuityFocus{
				CurrentExecutionStepID:    "exec.impl",
				CurrentExecutionStepTitle: "Implement the bounded fix",
				ProjectFocus: &task.ProjectTaskContext{
					PrimaryStepID:          "phase.impl",
					PrimaryBranchID:        "branch.impl",
					UnmetDependencyStepIDs: []string{"phase.repo_truth"},
				},
			},
		},
		Sprint: &task.SprintSnapshot{
			SchemaVersion:      task.SchemaVersion,
			TaskID:             "TASK-001",
			SnapshotID:         "SPR-001",
			PrimaryCriterionID: "SC-001",
			ProjectFocus: &task.ProjectTaskContext{
				PrimaryStepID:          "phase.impl",
				PrimaryBranchID:        "branch.impl",
				UnmetDependencyStepIDs: []string{"phase.repo_truth"},
			},
		},
	})
	if err != nil {
		t.Fatalf("build workspace edit prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"project_focus\"") || !strings.Contains(prompt, "\"phase.impl\"") || !strings.Contains(prompt, "\"phase.repo_truth\"") {
		t.Fatalf("expected serialized project focus in workspace edit prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use context_pack.project_focus") || !strings.Contains(prompt, "If sprint.project_focus is present") {
		t.Fatalf("expected project-focus guidance in workspace edit prompt, got %q", prompt)
	}
}

func TestBuildWorkspaceEditPromptIncludesSprintSnapshot(t *testing.T) {
	prompt, err := buildWorkspaceEditPrompt(WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "repair the active sprint cleanly",
		},
		Sprint: &task.SprintSnapshot{
			SchemaVersion:             task.SchemaVersion,
			TaskID:                    "TASK-001",
			SnapshotID:                "SPR-001",
			Summary:                   "Current sprint closes SC-001 before widening scope.",
			Objective:                 "Repair calc.go",
			PrimaryCriterionID:        "SC-001",
			PrimaryCriterionStatement: "calc.go returns the correct sum",
			DeferredCriterionIDs:      []string{"SC-002"},
			CompletionSignals:         []string{"calc.go returns the correct sum", "Verifier hint: go test ./..."},
		},
	})
	if err != nil {
		t.Fatalf("build workspace edit prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"sprint\"") || !strings.Contains(prompt, "\"primary_criterion_id\"") || !strings.Contains(prompt, "\"completion_signals\"") {
		t.Fatalf("expected sprint snapshot in workspace edit prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use sprint as the durable current-scope contract") {
		t.Fatalf("expected sprint guidance in workspace edit prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "SC-001") || !strings.Contains(prompt, "go test ./...") {
		t.Fatalf("expected sprint criterion and completion signals in workspace edit prompt, got %q", prompt)
	}
}

func TestBuildDecisionPromptIncludesContextPackAndWorkspaceMemory(t *testing.T) {
	prompt, err := buildDecisionPrompt(Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "stabilize retries",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
		ContextPack: &task.ContextSummary{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			PackID:        "PACK-001",
			Summary:       "Retry logic still drifts from docs.",
			NextStep:      "Sync code, docs, and tests before finishing.",
		},
		WorkspaceMemory: "# Workspace Memory\n\n## Recent Memory Entries\n- previous retry hardening\n",
	})
	if err != nil {
		t.Fatalf("build decision prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"context_pack\"") || !strings.Contains(prompt, "\"workspace_memory\"") {
		t.Fatalf("expected context pack and workspace memory in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use context_pack for continuity") {
		t.Fatalf("expected continuity guidance in decision prompt, got %q", prompt)
	}
}

func TestBuildDecisionPromptIncludesContinuitySnapshot(t *testing.T) {
	prompt, err := buildDecisionPrompt(Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "continue the active sprint",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
		Continuity: &task.ContinuitySnapshot{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			SnapshotID:    "CNT-001",
			Summary:       "Stay inside the current calc sprint.",
			NextStep:      "Repair calc.go, then rerun the repo verifier.",
			CurrentFocus: task.ContinuityFocus{
				CurrentExecutionStepID:    "exec.calc",
				CurrentExecutionStepTitle: "Repair calc.go",
				CriterionIDs:              []string{"SC-001"},
			},
			StartupChecklist: []task.ContinuityChecklistItem{
				{
					ID:     "read_progress",
					Kind:   "read_ref",
					Title:  "Read progress.md",
					Ref:    "progress.md",
					Reason: "Load the live task status before choosing a new action.",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build decision prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"continuity\"") || !strings.Contains(prompt, "\"current_focus\"") || !strings.Contains(prompt, "\"startup_checklist\"") {
		t.Fatalf("expected continuity snapshot in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use continuity.current_focus and continuity.startup_checklist") {
		t.Fatalf("expected continuity guidance in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Repair calc.go") || !strings.Contains(prompt, "progress.md") {
		t.Fatalf("expected continuity focus and checklist details in decision prompt, got %q", prompt)
	}
}

func TestBuildDecisionPromptIncludesProjectFocusGuidance(t *testing.T) {
	prompt, err := buildDecisionPrompt(Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "continue the project-bound sprint",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
		ContextPack: &task.ContextSummary{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			PackID:        "PACK-001",
			ProjectFocus: &task.ProjectTaskContext{
				PrimaryStepID:          "phase.impl",
				PrimaryBranchID:        "branch.impl",
				UnmetDependencyStepIDs: []string{"phase.repo_truth"},
			},
		},
		Continuity: &task.ContinuitySnapshot{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			SnapshotID:    "CNT-001",
			CurrentFocus: task.ContinuityFocus{
				CurrentExecutionStepID: "exec.impl",
				ProjectFocus: &task.ProjectTaskContext{
					PrimaryStepID:          "phase.impl",
					PrimaryBranchID:        "branch.impl",
					UnmetDependencyStepIDs: []string{"phase.repo_truth"},
				},
			},
		},
		Sprint: &task.SprintSnapshot{
			SchemaVersion:      task.SchemaVersion,
			TaskID:             "TASK-001",
			SnapshotID:         "SPR-001",
			PrimaryCriterionID: "SC-001",
			ProjectFocus: &task.ProjectTaskContext{
				PrimaryStepID:          "phase.impl",
				PrimaryBranchID:        "branch.impl",
				UnmetDependencyStepIDs: []string{"phase.repo_truth"},
			},
		},
	})
	if err != nil {
		t.Fatalf("build decision prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"project_focus\"") || !strings.Contains(prompt, "\"phase.impl\"") || !strings.Contains(prompt, "\"phase.repo_truth\"") {
		t.Fatalf("expected serialized project focus in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use context_pack.project_focus") || !strings.Contains(prompt, "If sprint.project_focus is present") {
		t.Fatalf("expected project-focus guidance in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "do not set task_create.project_step_id or task_create.project_branch_id to the parent task's current binding") {
		t.Fatalf("expected anti-binding-steal guidance in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Write the child contract as a ready-to-run child task") || !strings.Contains(prompt, "Do not copy parent instructions like \"create exactly one durable task\"") {
		t.Fatalf("expected task_create child-contract guidance in decision prompt, got %q", prompt)
	}
}

func TestBuildDecisionPromptIncludesSprintSnapshot(t *testing.T) {
	prompt, err := buildDecisionPrompt(Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "continue the active sprint",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
		Sprint: &task.SprintSnapshot{
			SchemaVersion:             task.SchemaVersion,
			TaskID:                    "TASK-001",
			SnapshotID:                "SPR-001",
			Summary:                   "Current sprint closes SC-002 before widening scope.",
			Objective:                 "Repair calc.go",
			PrimaryCriterionID:        "SC-002",
			PrimaryCriterionStatement: "calc.go returns the correct sum",
			DeferredCriterionIDs:      []string{"SC-003"},
			CompletionSignals:         []string{"calc.go returns the correct sum", "Verifier hint: go test ./..."},
		},
	})
	if err != nil {
		t.Fatalf("build decision prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"sprint\"") || !strings.Contains(prompt, "\"primary_criterion_id\"") || !strings.Contains(prompt, "\"completion_signals\"") {
		t.Fatalf("expected sprint snapshot in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use sprint as the durable current-scope contract") {
		t.Fatalf("expected sprint guidance in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "SC-002") || !strings.Contains(prompt, "SC-003") {
		t.Fatalf("expected sprint criterion and deferred scope in decision prompt, got %q", prompt)
	}
}

func TestBuildDecisionPromptIncludesCriteriaAcceptanceLedger(t *testing.T) {
	prompt, err := buildDecisionPrompt(Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "advance the next acceptance item",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
		Criteria: &task.CriteriaSnapshot{
			SchemaVersion:             task.SchemaVersion,
			TaskID:                    "TASK-001",
			SnapshotID:                "CRT-001",
			Summary:                   "1/2 acceptance criteria are passing; current focus is SC-002.",
			CurrentCriterionID:        "SC-002",
			CurrentCriterionStatement: "docs/guide.md mentions `beta`",
			MetCount:                  1,
			OpenCount:                 1,
			Criteria: []task.CriterionStatus{
				{CriterionID: "SC-001", Statement: "README.md mentions `alpha`", Status: "met", Passes: true, EvidenceRefs: []string{"workspace:README.md"}},
				{CriterionID: "SC-002", Statement: "docs/guide.md mentions `beta`", Status: "open", Passes: false, Selected: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("build decision prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"criteria\"") || !strings.Contains(prompt, "\"current_criterion_id\"") || !strings.Contains(prompt, "\"passes\"") {
		t.Fatalf("expected criteria acceptance ledger in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use criteria as the durable acceptance ledger") {
		t.Fatalf("expected explicit acceptance-ledger guidance in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "SC-002") || !strings.Contains(prompt, "docs/guide.md mentions `beta`") {
		t.Fatalf("expected current criterion details in decision prompt, got %q", prompt)
	}
}

func TestBuildDecisionPromptIncludesBaselineRepoBearings(t *testing.T) {
	prompt, err := buildDecisionPrompt(Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "stabilize the repo bootstrap path",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
		Baseline: &task.Baseline{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			CommandHints: []task.CommandHint{
				{
					Kind:      "setup",
					Command:   []string{"bash", "./init.sh"},
					Reason:    "Workspace exposes an init bootstrap command.",
					SourceRef: "workspace:init.sh",
				},
			},
			WorkspaceSnapshot: &task.WorkspaceSnapshot{
				Git: &task.GitSummary{
					IsRepository:  true,
					Branch:        "main",
					Head:          "abc1234",
					Dirty:         true,
					StatusSummary: "dirty working tree",
					ChangedPaths:  []string{"README.md", "calc.go"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build decision prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"baseline\"") || !strings.Contains(prompt, "\"command_hints\"") || !strings.Contains(prompt, "\"workspace_snapshot\"") {
		t.Fatalf("expected baseline repo bearings in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use baseline.command_hints and baseline.workspace_snapshot as durable repo bearings") {
		t.Fatalf("expected repo bearings guidance in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "bash") || !strings.Contains(prompt, "./init.sh") || !strings.Contains(prompt, "dirty working tree") {
		t.Fatalf("expected baseline command hint and git snapshot in decision prompt, got %q", prompt)
	}
}

func TestBuildWorkspaceObservationPromptIncludesSprintSnapshot(t *testing.T) {
	prompt, err := buildWorkspaceObservationPrompt(WorkspaceObservationInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "inspect the active sprint cleanly",
		},
		Sprint: &task.SprintSnapshot{
			SchemaVersion:             task.SchemaVersion,
			TaskID:                    "TASK-001",
			SnapshotID:                "SPR-001",
			Summary:                   "Current sprint closes SC-001 before widening scope.",
			Objective:                 "Repair calc.go",
			PrimaryCriterionID:        "SC-001",
			PrimaryCriterionStatement: "calc.go returns the correct sum",
			DeferredCriterionIDs:      []string{"SC-002"},
			CompletionSignals:         []string{"calc.go returns the correct sum", "Verifier hint: go test ./..."},
		},
	})
	if err != nil {
		t.Fatalf("build workspace observation prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"sprint\"") || !strings.Contains(prompt, "\"primary_criterion_id\"") || !strings.Contains(prompt, "\"completion_signals\"") {
		t.Fatalf("expected sprint snapshot in workspace observation prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use sprint as the durable current-scope contract") {
		t.Fatalf("expected sprint guidance in workspace observation prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "SC-001") || !strings.Contains(prompt, "SC-002") {
		t.Fatalf("expected sprint criterion and deferred scope in workspace observation prompt, got %q", prompt)
	}
}

func TestBuildWorkspaceObservationPromptIncludesProjectFocus(t *testing.T) {
	prompt, err := buildWorkspaceObservationPrompt(WorkspaceObservationInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "inspect the project-bound sprint cleanly",
		},
		ContextPack: &task.ContextSummary{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			PackID:        "PACK-001",
			ProjectFocus: &task.ProjectTaskContext{
				PrimaryStepID:          "phase.impl",
				PrimaryBranchID:        "branch.impl",
				UnmetDependencyStepIDs: []string{"phase.repo_truth"},
			},
		},
		Continuity: &task.ContinuitySnapshot{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			SnapshotID:    "CNT-001",
			CurrentFocus: task.ContinuityFocus{
				ProjectFocus: &task.ProjectTaskContext{
					PrimaryStepID:          "phase.impl",
					PrimaryBranchID:        "branch.impl",
					UnmetDependencyStepIDs: []string{"phase.repo_truth"},
				},
			},
		},
		Sprint: &task.SprintSnapshot{
			SchemaVersion: task.SchemaVersion,
			TaskID:        "TASK-001",
			SnapshotID:    "SPR-001",
			ProjectFocus: &task.ProjectTaskContext{
				PrimaryStepID:          "phase.impl",
				PrimaryBranchID:        "branch.impl",
				UnmetDependencyStepIDs: []string{"phase.repo_truth"},
			},
		},
	})
	if err != nil {
		t.Fatalf("build workspace observation prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"project_focus\"") || !strings.Contains(prompt, "\"phase.impl\"") {
		t.Fatalf("expected serialized project focus in workspace observation prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use context_pack.project_focus") || !strings.Contains(prompt, "If sprint.project_focus is present") {
		t.Fatalf("expected project-focus guidance in workspace observation prompt, got %q", prompt)
	}
}

func TestSuggestExecutionPlanChainsCriteriaSequentially(t *testing.T) {
	explanation, steps, ok := SuggestExecutionPlan(Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "advance one criterion at a time",
			SuccessCriteria: []task.SuccessCriterion{
				{ID: "SC-001", Statement: "README.md mentions `alpha`"},
				{ID: "SC-002", Statement: "docs/guide.md mentions `beta`"},
				{ID: "SC-003", Statement: "config.sample.json mentions `timeout_seconds`"},
			},
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
	})
	if !ok {
		t.Fatal("expected bootstrap execution plan suggestion")
	}
	if !strings.Contains(explanation, "one-criterion-at-a-time") {
		t.Fatalf("expected sequential explanation, got %q", explanation)
	}
	if len(steps) != 3 {
		t.Fatalf("expected one execution step per open criterion, got %+v", steps)
	}
	if steps[0].Status != task.StepStatusInProgress || len(steps[0].DependsOn) != 0 {
		t.Fatalf("expected first step to start immediately, got %+v", steps[0])
	}
	if got := strings.Join(steps[1].DependsOn, ","); got != steps[0].ID {
		t.Fatalf("expected second step to depend on first, got %+v", steps[1])
	}
	if got := strings.Join(steps[2].DependsOn, ","); got != steps[1].ID {
		t.Fatalf("expected third step to depend on second, got %+v", steps[2])
	}
}

func TestBuildDecisionPromptIncludesSessionTranscriptContext(t *testing.T) {
	prompt, err := buildDecisionPrompt(Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindGeneral,
			Objective: "continue interactive review",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
		Session: &task.Session{
			SessionID:  "SES-001",
			TaskID:     "TASK-001",
			Mode:       "terminal",
			Status:     "active",
			LastPrompt: "continue from the last blocker",
		},
		SessionMessagesRef: "workspace:.ngen/sessions/SES-001.messages.jsonl",
		SessionRecentMessages: []task.SessionMessage{
			{
				SchemaVersion: task.SchemaVersion,
				MessageID:     "MSG-001",
				SessionID:     "SES-001",
				TaskID:        "TASK-001",
				Role:          "operator",
				Content:       "Remember that the next step is to inspect the failing review blocker.",
			},
			{
				SchemaVersion: task.SchemaVersion,
				MessageID:     "MSG-002",
				SessionID:     "SES-001",
				TaskID:        "TASK-001",
				Role:          "runtime",
				Content:       "Blocked Review: Review found missing handoff evidence.",
			},
		},
	})
	if err != nil {
		t.Fatalf("build decision prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"session_messages_ref\"") || !strings.Contains(prompt, "\"session_recent_messages\"") {
		t.Fatalf("expected session transcript fields in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Remember that the next step is to inspect the failing review blocker.") {
		t.Fatalf("expected operator transcript message in decision prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Blocked Review: Review found missing handoff evidence.") {
		t.Fatalf("expected runtime transcript message in decision prompt, got %q", prompt)
	}
}

func TestValidateWorkspaceEditPlanNormalizesCommands(t *testing.T) {
	plan, err := validateWorkspaceEditPlan(WorkspaceEditPlan{
		Summary: "Run formatter",
		Writes: []WorkspaceWrite{
			{Path: "calc.go", Content: "package main\n"},
		},
		Commands: []WorkspaceCommand{
			{
				Phase:  "",
				Argv:   []string{"gofmt", "-w", "calc.go"},
				Reason: "Format calc.go after writing it",
			},
		},
	})
	if err != nil {
		t.Fatalf("validate workspace edit plan: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected one command, got %+v", plan.Commands)
	}
	if plan.Commands[0].Phase != "post" {
		t.Fatalf("expected empty command phase to normalize to post, got %+v", plan.Commands[0])
	}
}

func TestGenerateWorkspaceObservationUsesResponsesEndpointAndSchema(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("expected responses path, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "{\"summary\":\"Need one focused grep\",\"commands\":[{\"argv\":[\"rg\",\"-n\",\"Add\",\".\"],\"reason\":\"Locate Add implementation\"}]}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	plan, err := GenerateWorkspaceObservations(context.Background(), task.ProviderConfig{
		Mode:      "responses",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	}, WorkspaceObservationInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		CommandBudget: 2,
		Collection: WorkspaceCollection{
			IncludedFileCount: 2,
			IncludedByteCount: 32,
		},
		Files: []WorkspaceFile{
			{Path: "calc.go", Content: "package main\n"},
			{Path: "calc_test.go", Content: "package main\n"},
		},
	})
	if err != nil {
		t.Fatalf("generate workspace observations: %v", err)
	}
	if len(plan.Commands) != 1 || len(plan.Commands[0].Argv) != 4 {
		t.Fatalf("unexpected observation commands: %+v", plan.Commands)
	}
	text, ok := seenBody["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in request, got %#v", seenBody["text"])
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		t.Fatalf("expected text.format in request, got %#v", text["format"])
	}
	if format["name"] != "ngen_workspace_observation" {
		t.Fatalf("expected workspace observation schema name, got %#v", format["name"])
	}
}

func TestBuildWorkspaceObservationPromptIncludesSessionTranscriptContext(t *testing.T) {
	prompt, err := buildWorkspaceObservationPrompt(WorkspaceObservationInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "repair calc.go without touching README.md",
		},
		SessionMessagesRef: "workspace:.ngen/sessions/SES-001.messages.jsonl",
		SessionRecentMessages: []task.SessionMessage{
			{
				SchemaVersion: task.SchemaVersion,
				MessageID:     "MSG-001",
				SessionID:     "SES-001",
				TaskID:        "TASK-001",
				Role:          "operator",
				Content:       "Keep README.md untouched while fixing calc.go.",
			},
			{
				SchemaVersion: task.SchemaVersion,
				MessageID:     "MSG-002",
				SessionID:     "SES-001",
				TaskID:        "TASK-001",
				Role:          "runtime",
				Content:       "Failed Verify: go test still fails in calc.go.",
			},
		},
	})
	if err != nil {
		t.Fatalf("build workspace observation prompt: %v", err)
	}
	if !strings.Contains(prompt, "\"session_messages_ref\"") || !strings.Contains(prompt, "\"session_recent_messages\"") {
		t.Fatalf("expected session transcript fields in workspace observation prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Keep README.md untouched while fixing calc.go.") {
		t.Fatalf("expected operator transcript message in workspace observation prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Failed Verify: go test still fails in calc.go.") {
		t.Fatalf("expected runtime transcript message in workspace observation prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "Use session_recent_messages plus session_messages_ref") {
		t.Fatalf("expected explicit session continuity guidance in workspace observation prompt, got %q", prompt)
	}
}

func TestGenerateWorkspaceEditUsesChatCompletionsEndpointAndToolCall(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("expected chat/completions path, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"tool_calls": [{
						"function": {
							"name": "submit_workspace_edit",
							"arguments": "{\"summary\":\"Implement Add\",\"patch\":\"\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package main\\n\\nfunc Add(a, b int) int { return a + b }\\n\"}],\"deletes\":[],\"commands\":[]}"
						}
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	plan, err := GenerateWorkspaceEdit(context.Background(), task.ProviderConfig{
		Mode:      "openai-comp",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	}, WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		Files: []WorkspaceFile{
			{Path: "calc.go", Content: "package main\n"},
		},
	})
	if err != nil {
		t.Fatalf("generate workspace edit: %v", err)
	}
	if plan.Summary != "Implement Add" {
		t.Fatalf("unexpected plan summary: %+v", plan)
	}
	if len(plan.Writes) != 1 || plan.Writes[0].Path != "calc.go" {
		t.Fatalf("unexpected writes: %+v", plan.Writes)
	}
	toolChoice, ok := seenBody["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("expected tool_choice map, got %#v", seenBody["tool_choice"])
	}
	function, ok := toolChoice["function"].(map[string]any)
	if !ok || function["name"] != workspaceEditToolName {
		t.Fatalf("expected workspace edit tool_choice, got %#v", toolChoice)
	}
}

func TestGenerateWorkspaceObservationUsesChatCompletionsEndpointAndToolCall(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("expected chat/completions path, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"tool_calls": [{
						"function": {
							"name": "submit_workspace_observation",
							"arguments": "{\"summary\":\"Need one focused grep\",\"commands\":[{\"argv\":[\"rg\",\"-n\",\"Add\",\".\"],\"reason\":\"Locate Add implementation\"}]}"
						}
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	plan, err := GenerateWorkspaceObservations(context.Background(), task.ProviderConfig{
		Mode:      "openai-comp",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	}, WorkspaceObservationInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		CommandBudget: 2,
		Files: []WorkspaceFile{
			{Path: "calc.go", Content: "package main\n"},
		},
	})
	if err != nil {
		t.Fatalf("generate workspace observations: %v", err)
	}
	if len(plan.Commands) != 1 || len(plan.Commands[0].Argv) != 4 {
		t.Fatalf("unexpected observation commands: %+v", plan.Commands)
	}
	toolChoice, ok := seenBody["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("expected tool_choice map, got %#v", seenBody["tool_choice"])
	}
	function, ok := toolChoice["function"].(map[string]any)
	if !ok || function["name"] != workspaceObservationToolName {
		t.Fatalf("expected workspace observation tool_choice, got %#v", toolChoice)
	}
}

func TestGenerateWorkspaceEditUsesAnthropicEndpointAndToolUse(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	var (
		seenBody    map[string]any
		seenVersion string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("expected messages path, got %s", r.URL.Path)
		}
		seenVersion = r.Header.Get("anthropic-version")
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content": [{
				"type": "tool_use",
				"name": "submit_workspace_edit",
				"input": {
					"summary": "Implement Add",
					"patch": "",
					"writes": [{
						"path": "calc.go",
						"content": "package main\n\nfunc Add(a, b int) int { return a + b }\n"
					}],
					"deletes": [],
					"commands": []
				}
			}]
		}`))
	}))
	defer server.Close()

	plan, err := GenerateWorkspaceEdit(context.Background(), task.ProviderConfig{
		Mode:      "anthropic",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	}, WorkspaceEditInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		Files: []WorkspaceFile{
			{Path: "calc.go", Content: "package main\n"},
		},
	})
	if err != nil {
		t.Fatalf("generate workspace edit: %v", err)
	}
	if plan.Summary != "Implement Add" {
		t.Fatalf("unexpected plan summary: %+v", plan)
	}
	if seenVersion != anthropicVersion {
		t.Fatalf("expected anthropic-version %s, got %q", anthropicVersion, seenVersion)
	}
	toolChoice, ok := seenBody["tool_choice"].(map[string]any)
	if !ok || toolChoice["name"] != workspaceEditToolName {
		t.Fatalf("expected workspace edit tool_choice, got %#v", seenBody["tool_choice"])
	}
}

func TestGenerateWorkspaceObservationUsesAnthropicEndpointAndToolUse(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("expected messages path, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content": [{
				"type": "tool_use",
				"name": "submit_workspace_observation",
				"input": {
					"summary": "Need one focused grep",
					"commands": [{
						"argv": ["rg", "-n", "Add", "."],
						"reason": "Locate Add implementation"
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	plan, err := GenerateWorkspaceObservations(context.Background(), task.ProviderConfig{
		Mode:      "anthropic",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	}, WorkspaceObservationInput{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "implement Add",
		},
		CommandBudget: 2,
		Files: []WorkspaceFile{
			{Path: "calc.go", Content: "package main\n"},
		},
	})
	if err != nil {
		t.Fatalf("generate workspace observations: %v", err)
	}
	if len(plan.Commands) != 1 || len(plan.Commands[0].Argv) != 4 {
		t.Fatalf("unexpected observation commands: %+v", plan.Commands)
	}
	toolChoice, ok := seenBody["tool_choice"].(map[string]any)
	if !ok || toolChoice["name"] != workspaceObservationToolName {
		t.Fatalf("expected workspace observation tool_choice, got %#v", seenBody["tool_choice"])
	}
}

func TestOpenAIChatCompletionsDriverUsesChatCompletionsEndpointAndToolCall(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	var (
		seenPath string
		seenAuth string
		seenBody map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"tool_calls": [{
						"function": {
							"name": "submit_decision",
							"arguments": "{\"action\":\"run\",\"summary\":\"Execute the task now\"}"
						}
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	driver := New(task.ProviderConfig{
		Mode:      "openai-comp",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	})
	decision, err := driver.Decide(context.Background(), Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindCoding,
			Objective: "ship fix",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if seenPath != "/v1/chat/completions" {
		t.Fatalf("expected chat/completions path, got %s", seenPath)
	}
	if seenAuth != "Bearer test-key" {
		t.Fatalf("expected bearer auth, got %q", seenAuth)
	}
	if decision.Action != "run" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	tools, ok := seenBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one tool definition, got %#v", seenBody["tools"])
	}
	toolChoice, ok := seenBody["tool_choice"].(map[string]any)
	if !ok || toolChoice["type"] != "function" {
		t.Fatalf("expected function tool_choice, got %#v", seenBody["tool_choice"])
	}
}

func TestAnthropicMessagesDriverUsesMessagesEndpointAndToolUse(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	var (
		seenPath    string
		seenKey     string
		seenVersion string
		seenBody    map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenKey = r.Header.Get("x-api-key")
		seenVersion = r.Header.Get("anthropic-version")
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content": [{
				"type": "tool_use",
				"name": "submit_decision",
				"input": {
					"action": "review",
					"summary": "Review the latest evidence"
				}
			}]
		}`))
	}))
	defer server.Close()

	driver := New(task.ProviderConfig{
		Mode:      "anthropic",
		BaseURL:   server.URL + "/v1",
		Model:     "gpt-5.4",
		APIKeyEnv: "OPENAI_API_KEY",
	})
	decision, err := driver.Decide(context.Background(), Input{
		Task: task.Spec{
			TaskID:    "TASK-001",
			Kind:      task.KindReviewer,
			Objective: "review workspace",
		},
		State: task.State{
			TaskID: "TASK-001",
			State:  task.StateActive,
		},
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if seenPath != "/v1/messages" {
		t.Fatalf("expected messages path, got %s", seenPath)
	}
	if seenKey != "test-key" {
		t.Fatalf("expected x-api-key header, got %q", seenKey)
	}
	if seenVersion != anthropicVersion {
		t.Fatalf("expected anthropic-version %s, got %q", anthropicVersion, seenVersion)
	}
	if decision.Action != "review" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	tools, ok := seenBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one tool definition, got %#v", seenBody["tools"])
	}
	toolChoice, ok := seenBody["tool_choice"].(map[string]any)
	if !ok || toolChoice["name"] != decisionToolName {
		t.Fatalf("expected submit_decision tool_choice, got %#v", seenBody["tool_choice"])
	}
}

func TestUnsupportedModeDoesNotSilentlyFallbackToCommand(t *testing.T) {
	driver := New(task.ProviderConfig{
		Mode:    "openai-typo",
		Command: []string{"echo", "ignored"},
	})
	_, err := driver.Decide(context.Background(), Input{})
	if err == nil {
		t.Fatal("expected unsupported provider mode error")
	}
	if !strings.Contains(err.Error(), "unsupported provider mode") {
		t.Fatalf("expected unsupported mode error, got %v", err)
	}
}

func TestBuiltinDriverHandlesManagedWorkerApprovalsAndContinuation(t *testing.T) {
	driver := BuiltinDriver{}

	decision, err := driver.Decide(context.Background(), Input{
		State: task.State{TaskID: "TASK-001", State: task.StateDone},
		OwnedPendingApprovals: []task.OwnedApprovalSummary{{
			SchemaVersion:        task.SchemaVersion,
			WorkerID:             "WKR-001",
			ChildTaskID:          "TASK-002",
			ApprovalID:           "APR-001",
			Scope:                "manual step",
			RequiresParentAction: true,
			ParentActionType:     "owned_approval_pending",
		}},
	})
	if err != nil {
		t.Fatalf("decide pending approval: %v", err)
	}
	if decision.Action != "block" || !strings.Contains(decision.Summary, "APR-001") || !strings.Contains(decision.Summary, "parent_takeover") {
		t.Fatalf("expected block summary for owned approval, got %+v", decision)
	}

	decision, err = driver.Decide(context.Background(), Input{
		State: task.State{TaskID: "TASK-001", State: task.StateDone},
		ManagedWorkers: []task.WorkerContract{{
			SchemaVersion:        task.SchemaVersion,
			WorkerID:             "WKR-001",
			ParentTaskID:         "TASK-001",
			ChildTaskID:          "TASK-002",
			Status:               "active",
			RequiresParentAction: true,
			ParentActionType:     "continue_child",
			ParentActionSummary:  "Worker WKR-001 approval APR-001 was approved. Parent should run worker continue to resume the child.",
			ParentActionOptions:  []string{"worker_continue"},
			ApprovalID:           "APR-001",
		}},
	})
	if err != nil {
		t.Fatalf("decide continuation: %v", err)
	}
	if decision.Action != "worker_continue" || decision.WorkerID != "WKR-001" {
		t.Fatalf("expected worker_continue for approved child continuation, got %+v", decision)
	}
}

func TestValidateDecisionNormalizesTaskPatchOperations(t *testing.T) {
	decision, err := validateDecision(Decision{
		Action:  "task_patch",
		Summary: "Patch the mutable plan.",
		PlanPatchOperations: []task.PlanPatchOperation{
			{
				Op:          "upsert_step",
				AfterStepID: " epic.repo_truth ",
				Step: &task.ExecutionPlanStep{
					ID:           " handoff.closeout ",
					ParentStepID: " epic.repo_truth ",
					DependsOn:    []string{" epic.repo_truth ", "epic.repo_truth"},
					Priority:     "HIGH",
					Title:        " Refresh handoff ",
					Status:       "IN_PROGRESS",
					Covers:       []string{"SC-002", "SC-002"},
					Notes:        "  keep it narrow  ",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("validate task_patch decision: %v", err)
	}
	if len(decision.PlanPatchOperations) != 1 || decision.PlanPatchOperations[0].Step == nil {
		t.Fatalf("expected normalized patch operations, got %+v", decision.PlanPatchOperations)
	}
	step := decision.PlanPatchOperations[0].Step
	if step.ID != "handoff.closeout" || decision.PlanPatchOperations[0].AfterStepID != "epic.repo_truth" {
		t.Fatalf("expected normalized patch ids, got %+v", decision.PlanPatchOperations[0])
	}
	if step.Priority != task.StepPriorityHigh || step.Status != task.StepStatusInProgress {
		t.Fatalf("expected normalized patch priority/status, got %+v", step)
	}
	if strings.Join(step.DependsOn, ",") != "epic.repo_truth" || strings.Join(step.Covers, ",") != "SC-002" {
		t.Fatalf("expected normalized patch refs, got %+v", step)
	}
}

func TestValidateDecisionNormalizesProjectPatchOperations(t *testing.T) {
	decision, err := validateDecision(Decision{
		Action:  "project_patch",
		Summary: "Patch the workspace project graph.",
		ProjectPatchOperations: []task.ProjectPatchOperation{
			{
				Op:     "set_step_dependencies",
				StepID: " task:TASK-002 ",
				DependsOn: []string{
					" task:TASK-001 ",
					"task:TASK-001",
				},
			},
			{
				Op:       "bind_branch_task",
				BranchID: " branch:TASK-002 ",
				TaskID:   " TASK-002 ",
			},
		},
	})
	if err != nil {
		t.Fatalf("validate project_patch decision: %v", err)
	}
	if len(decision.ProjectPatchOperations) != 2 {
		t.Fatalf("expected normalized project patch operations, got %+v", decision.ProjectPatchOperations)
	}
	if decision.ProjectPatchOperations[0].StepID != "task:TASK-002" || strings.Join(decision.ProjectPatchOperations[0].DependsOn, ",") != "task:TASK-001" {
		t.Fatalf("expected normalized dependency edge patch, got %+v", decision.ProjectPatchOperations[0])
	}
	if decision.ProjectPatchOperations[1].BranchID != "branch:TASK-002" || decision.ProjectPatchOperations[1].TaskID != "TASK-002" {
		t.Fatalf("expected normalized branch-task binding patch, got %+v", decision.ProjectPatchOperations[1])
	}
}

func TestValidateDecisionRequiresProjectGraphPayload(t *testing.T) {
	if _, err := validateDecision(Decision{
		Action:  "project_update",
		Summary: "Refresh workspace project graph.",
	}); err == nil {
		t.Fatal("expected project_update without project graph payload to fail validation")
	}

	decision, err := validateDecision(Decision{
		Action:             "project_update",
		Summary:            "Refresh workspace project graph.",
		ProjectExplanation: "Coordinate two durable tasks through a workspace graph.",
		ProjectSteps: []task.ProjectExecutionStep{
			{ID: "epic.repo", Title: "Inspect repo truth", Status: task.ProjectStepStatusInProgress, Priority: "high", BranchID: "branch.repo", TaskID: "TASK-001", Notes: "Start from README."},
			{ID: "epic.patch", ParentStepID: "epic.repo", DependsOn: []string{"epic.repo"}, Title: "Apply the remaining fix", Status: task.ProjectStepStatusPending, Priority: "medium", BranchID: "branch.patch", TaskID: "TASK-002", Notes: ""},
		},
		ProjectBranches: []task.ProjectBranchSpec{
			{ID: "branch.repo", Title: "Repo truth", Status: task.ProjectBranchStatusActive, TaskID: "TASK-001", Notes: "Primary branch."},
			{ID: "branch.patch", Title: "Patch branch", Status: task.ProjectBranchStatusPending, TaskID: "TASK-002", Notes: "Wait for repo truth."},
		},
	})
	if err != nil {
		t.Fatalf("validate project_update: %v", err)
	}
	if decision.Action != "project_update" || len(decision.ProjectSteps) != 2 || len(decision.ProjectBranches) != 2 {
		t.Fatalf("expected project_update graph to round-trip, got %+v", decision)
	}
	if decision.ProjectSteps[0].Priority != task.StepPriorityHigh || decision.ProjectSteps[1].ParentStepID != "epic.repo" {
		t.Fatalf("expected normalized project graph edges, got %+v", decision.ProjectSteps)
	}
}

func TestValidateDecisionNormalizesMemoryPromote(t *testing.T) {
	decision, err := validateDecision(Decision{
		Action:     "memory_promote",
		Summary:    "Record the durable milestone.",
		MemoryKind: "Milestone",
		MemoryRefs: []string{" progress.md ", "progress.md", " context/summary.md "},
	})
	if err != nil {
		t.Fatalf("validate memory_promote: %v", err)
	}
	if decision.MemoryKind != task.MemoryKindTaskMilestone {
		t.Fatalf("expected canonical memory kind, got %+v", decision)
	}
	if strings.Join(decision.MemoryRefs, ",") != "progress.md,context/summary.md" {
		t.Fatalf("expected normalized memory refs, got %+v", decision.MemoryRefs)
	}
}

func TestBuiltinDriverParsesWorkerSpawnPrompt(t *testing.T) {
	driver := BuiltinDriver{}

	decision, err := driver.Decide(context.Background(), Input{
		State: task.State{TaskID: "TASK-001", State: task.StateDone},
		Session: &task.Session{
			SessionID:  "SES-001",
			TaskID:     "TASK-001",
			LastPrompt: "/worker_spawn reviewer review the parent change list",
		},
	})
	if err != nil {
		t.Fatalf("decide worker spawn prompt: %v", err)
	}
	if decision.Action != "worker_spawn" || decision.WorkerRole != string(task.KindReviewer) || decision.WorkerObjective != "review the parent change list" {
		t.Fatalf("expected worker_spawn decision from prompt, got %+v", decision)
	}
}

func TestBuiltinDriverParsesMemoryPrompt(t *testing.T) {
	driver := BuiltinDriver{}

	decision, err := driver.Decide(context.Background(), Input{
		State: task.State{TaskID: "TASK-001", State: task.StateActive},
		Session: &task.Session{
			SessionID:  "SES-001",
			TaskID:     "TASK-001",
			LastPrompt: "/memory milestone repo truth confirmed and project graph refreshed",
		},
	})
	if err != nil {
		t.Fatalf("decide memory prompt: %v", err)
	}
	if decision.Action != "memory_promote" || decision.MemoryKind != task.MemoryKindTaskMilestone || !strings.Contains(decision.Summary, "repo truth confirmed") {
		t.Fatalf("expected memory_promote decision from prompt, got %+v", decision)
	}
}

func TestValidateDecisionRequiresWorkerIDForWorkerContinue(t *testing.T) {
	if _, err := validateDecision(Decision{
		Action:  "worker_continue",
		Summary: "Resume the child worker.",
	}); err == nil {
		t.Fatal("expected worker_continue without worker_id to fail validation")
	}

	decision, err := validateDecision(Decision{
		Action:   "worker_continue",
		Summary:  "Resume the child worker.",
		WorkerID: "WKR-123",
	})
	if err != nil {
		t.Fatalf("validate worker_continue: %v", err)
	}
	if decision.WorkerID != "WKR-123" {
		t.Fatalf("expected worker id to round-trip, got %+v", decision)
	}
}

func TestValidateDecisionRequiresWorkerRoleAndObjectiveForWorkerSpawn(t *testing.T) {
	if _, err := validateDecision(Decision{
		Action:          "worker_spawn",
		Summary:         "Spawn a worker.",
		WorkerObjective: "review the parent task",
	}); err == nil {
		t.Fatal("expected worker_spawn without worker_role to fail validation")
	}

	if _, err := validateDecision(Decision{
		Action:     "worker_spawn",
		Summary:    "Spawn a worker.",
		WorkerRole: "reviewer",
	}); err == nil {
		t.Fatal("expected worker_spawn without worker_objective to fail validation")
	}

	decision, err := validateDecision(Decision{
		Action:          "worker_spawn",
		Summary:         "Spawn a docs worker.",
		WorkerRole:      "docs_lite",
		WorkerObjective: "review documentation changes",
	})
	if err != nil {
		t.Fatalf("validate worker_spawn: %v", err)
	}
	if decision.WorkerRole != string(task.KindGeneral) {
		t.Fatalf("expected worker role to normalize to %s, got %+v", task.KindGeneral, decision)
	}
}

func TestValidateDecisionRequiresPlanStepsForTaskUpdate(t *testing.T) {
	if _, err := validateDecision(Decision{
		Action:  "task_update",
		Summary: "Refresh execution plan.",
	}); err == nil {
		t.Fatal("expected task_update without plan steps to fail validation")
	}

	decision, err := validateDecision(Decision{
		Action:          "task_update",
		Summary:         "Refresh execution plan.",
		PlanExplanation: "Track current work.",
		PlanSteps: []task.ExecutionPlanStep{
			{ID: "epic.repo", Title: "Inspect repo truth", Status: task.StepStatusInProgress, Priority: "high", Covers: []string{"SC-001"}, Notes: "Start from README."},
			{ID: "criterion.close", ParentStepID: "epic.repo", DependsOn: []string{"epic.repo"}, Title: "Close remaining criterion", Status: task.StepStatusPending, Covers: []string{"SC-002"}, Notes: ""},
		},
	})
	if err != nil {
		t.Fatalf("validate task_update: %v", err)
	}
	if decision.Action != "task_update" || len(decision.PlanSteps) != 2 {
		t.Fatalf("expected task_update plan to round-trip, got %+v", decision)
	}
	if decision.PlanSteps[0].Status != task.StepStatusInProgress || decision.PlanSteps[0].Title != "Inspect repo truth" {
		t.Fatalf("expected normalized in-progress task step, got %+v", decision.PlanSteps[0])
	}
	if decision.PlanSteps[0].Priority != task.StepPriorityHigh || decision.PlanSteps[1].ParentStepID != "epic.repo" {
		t.Fatalf("expected graph/priority fields to round-trip, got %+v", decision.PlanSteps)
	}
}

func TestValidateDecisionTaskCreate(t *testing.T) {
	if _, err := validateDecision(Decision{
		Action:  "task_create",
		Summary: "Create a durable task.",
	}); err == nil {
		t.Fatal("expected task_create without objective and criteria to fail validation")
	}

	decision, err := validateDecision(Decision{
		Action:               "task_create",
		Summary:              "Create a durable docs task.",
		TaskKind:             string(task.KindGeneral),
		TaskPresetID:         string(task.PresetDocsLite),
		TaskTitle:            "docs pass",
		TaskObjective:        "sync the operator guide",
		TaskCriteria:         []string{"docs reviewed", "handoff updated"},
		TaskConstraints:      []string{"do not edit generated files"},
		TaskPermissionModeID: task.PermissionModeYolo,
		ProjectStepID:        "phase.docs",
		ProjectBranchID:      "branch.docs",
	})
	if err != nil {
		t.Fatalf("validate task_create: %v", err)
	}
	if decision.Action != "task_create" || decision.TaskKind != string(task.KindGeneral) || decision.TaskPresetID != string(task.PresetDocsLite) {
		t.Fatalf("expected task_create kind/preset to round-trip, got %+v", decision)
	}
	if strings.Join(decision.TaskCriteria, ",") != "docs reviewed,handoff updated" {
		t.Fatalf("expected task_create criteria to normalize, got %+v", decision.TaskCriteria)
	}
	if decision.TaskPermissionModeID != task.PermissionModeYolo || decision.ProjectStepID != "phase.docs" || decision.ProjectBranchID != "branch.docs" {
		t.Fatalf("expected task_create binding fields to round-trip, got %+v", decision)
	}
}

func TestBuiltinDriverSynthesizesExecutionPlanForMultiCriterionTask(t *testing.T) {
	driver := BuiltinDriver{}

	decision, err := driver.Decide(context.Background(), Input{
		Task: task.Spec{
			TaskID: "TASK-001",
			Kind:   task.KindCoding,
			SuccessCriteria: []task.SuccessCriterion{
				{ID: "SC-001", Statement: "go test ./... passes"},
				{ID: "SC-002", Statement: "README.md mentions `Add`"},
			},
		},
		State: task.State{TaskID: "TASK-001", State: task.StateActive},
	})
	if err != nil {
		t.Fatalf("builtin decide multi-criterion task: %v", err)
	}
	if decision.Action != "task_update" {
		t.Fatalf("expected builtin driver to seed mutable execution plan, got %+v", decision)
	}
	if len(decision.PlanSteps) != 2 {
		t.Fatalf("expected two execution-plan steps, got %+v", decision.PlanSteps)
	}
	if decision.PlanSteps[0].Status != task.StepStatusInProgress || decision.PlanSteps[1].Status != task.StepStatusPending {
		t.Fatalf("expected first step in progress and second pending, got %+v", decision.PlanSteps)
	}
	if decision.PlanSteps[0].ID == "" || decision.PlanSteps[0].Priority != task.StepPriorityHigh {
		t.Fatalf("expected synthesized plan steps to carry stable ids and priority, got %+v", decision.PlanSteps)
	}
}
