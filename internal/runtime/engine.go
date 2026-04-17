package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/hooks"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
	"go-cli-agent/internal/tools"
)

type Engine struct {
	cfg       *config.Config
	store     *session.Store
	bus       *events.Bus
	control   *runControl
	compactor *compactor
}

func NewEngine(cfg *config.Config, store *session.Store, bus *events.Bus, control *runControl) *Engine {
	return &Engine{
		cfg:       cfg,
		store:     store,
		bus:       bus,
		control:   control,
		compactor: newCompactor(store),
	}
}

type runDeps struct {
	adapter  provider.Adapter
	catalog  *skills.Catalog
	registry *tools.Registry
	hooks    *hooks.Manager
}

type RunResult struct {
	SessionID string
	Status    string
	FinalText string
	LastError string
}

func (e *Engine) Run(ctx context.Context, meta session.SessionMetadata, state session.State, systemOverride string, adapter provider.Adapter, catalog *skills.Catalog, registry *tools.Registry, hookManager *hooks.Manager) (RunResult, error) {
	hookManager.SetEmitter(func(eventType string, data map[string]any) {
		e.emit(meta.ID, eventType, state.Phase, data)
	})
	if _, err := hookManager.Trigger(ctx, "session.start", sessionHookPayload(meta, session.StatusRunning)); err != nil {
		return e.fail(ctx, meta, state, err, hookManager)
	}
	doneCandidates := 0
	allowResolutionTurn := false
	for turn := state.Turn; ; turn++ {
		usingResolutionTurn := false
		if turn >= e.cfg.Runtime.MaxTurnsHard {
			if !allowResolutionTurn {
				state.Status = session.StatusFailed
				state.LastError = "max_turns_hard_exceeded"
				if err := e.store.SaveState(meta.ID, state); err != nil {
					return RunResult{}, err
				}
				return RunResult{SessionID: meta.ID, Status: state.Status, LastError: state.LastError}, nil
			}
			usingResolutionTurn = true
			allowResolutionTurn = false
		}
		state.Turn = turn + 1
		state.Status = session.StatusRunning
		state.Phase = "prepare"
		if err := e.store.SaveState(meta.ID, state); err != nil {
			return RunResult{}, err
		}

		if err := e.deferPendingInterrupts(meta.ID); err != nil {
			return RunResult{}, err
		}

		acceptedBackgroundAtTurnStart, err := e.drainBackground(ctx, meta, hookManager)
		if err != nil {
			return RunResult{}, err
		}
		if acceptedBackgroundAtTurnStart > 0 {
			doneCandidates = 0
		}

		acceptedAtTurnStart, err := e.drainSteer(ctx, meta, hookManager)
		if err != nil {
			return RunResult{}, err
		}
		if acceptedAtTurnStart > 0 {
			doneCandidates = 0
		}

		messages, err := e.store.LoadMessages(meta.ID)
		if err != nil {
			return RunResult{}, err
		}
		if !e.guardrailsYolo() {
			messages, err = e.maybeAppendHarnessReminder(meta, messages)
			if err != nil {
				return RunResult{}, err
			}
		}
		todo, err := e.store.LoadTodo(meta.ID)
		if err != nil {
			return RunResult{}, err
		}
		tasks, err := e.store.ListTasks(meta.ID)
		if err != nil {
			return RunResult{}, err
		}
		projectMemory := loadProjectMemoryStack(meta.Workdir)
		e.emit(meta.ID, "session.context.loaded", "prepare", contextLoadedEventData(meta, state.Turn, projectMemory, todo, tasks))

		systemPrompt := buildSystemPrompt(meta.Workdir, meta.Mode, systemOverride, catalog.Summaries(), catalog.CommandTools(), state, messages, meta.AgentName, meta.AgentRole)
		if e.guardrailsYolo() {
			systemPrompt += "\n\n## Guardrails Mode\nYOLO mode is enabled. Runtime retrieval, project-memory, and review-artifact guardrails are disabled for this run. You still operate within tool-enforced workspace boundaries, shell timeouts, and explicit user instructions."
		}
		view, err := e.compactor.Build(meta.ID, meta.Workdir, state, messages, todo, tasks, e.cfg.Runtime.Compact.InputCharThreshold, e.cfg.Runtime.Compact.KeepRecentToolResults, func(evt events.Event) {
			_ = e.store.AppendEvent(meta.ID, evt)
			e.bus.Publish(evt)
		})
		if err != nil {
			return RunResult{}, err
		}

		state.Phase = "provider_call"
		if err := e.store.SaveState(meta.ID, state); err != nil {
			return RunResult{}, err
		}
		e.emit(meta.ID, "provider.call", state.Phase, map[string]any{"provider": meta.Provider})
		requestMetadata := providerRequestMetadata(meta)
		if meta.ProviderOptions.SendMetadata != nil && !*meta.ProviderOptions.SendMetadata {
			requestMetadata = nil
		}
		e.emit(meta.ID, "provider.request.prepared", state.Phase, providerRequestPreparedEventData(meta, requestMetadata))
		callCtx, cancel := context.WithCancel(ctx)
		e.control.setCancel(cancel)
		result, err := adapter.RunTurn(callCtx, provider.TurnRequest{
			SessionID:       meta.ID,
			Model:           meta.Model,
			SystemPrompt:    systemPrompt,
			Messages:        view,
			Tools:           providerTools(registry),
			Metadata:        requestMetadata,
			Temperature:     meta.ProviderOptions.Temperature,
			TopP:            meta.ProviderOptions.TopP,
			MaxOutputTokens: meta.ProviderOptions.MaxOutputTokens,
			ReasoningEffort: meta.ProviderOptions.ReasoningEffort,
			TextVerbosity:   meta.ProviderOptions.TextVerbosity,
			ThinkingBudget:  meta.ProviderOptions.ThinkingBudget,
			IncludeThoughts: meta.ProviderOptions.IncludeThoughts,
			Store:           meta.ProviderOptions.Store,
		}, func(eventType string, data map[string]any) {
			e.emit(meta.ID, eventType, "provider_call", data)
		})
		e.control.clearCancel(cancel)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				switch {
				case e.control.consumePause():
					reason := e.control.takePauseReason()
					e.emit(meta.ID, "provider.cancelled", "provider_call", map[string]any{"reason": reason})
					return e.pause(ctx, meta, state, reason, hookManager)
				case e.control.consumeSteerInterrupt():
					e.emit(meta.ID, "provider.cancelled", "provider_call", map[string]any{"reason": "steer_interrupt"})
					continue
				}
			}
			state.Status = session.StatusFailed
			state.LastError = err.Error()
			state.Phase = "provider_call"
			_ = e.store.SaveState(meta.ID, state)
			e.emit(meta.ID, "session.failed", state.Phase, map[string]any{"error": err.Error()})
			return RunResult{SessionID: meta.ID, Status: state.Status, LastError: err.Error()}, WrapProviderError(err)
		}

		e.emit(meta.ID, "turn.stopped", "provider_call", providerTurnEventData(result))

		if strings.TrimSpace(result.Text) != "" || len(result.ToolCalls) > 0 {
			assistantPayload, err := hookManager.Trigger(ctx, "assistant.message", map[string]any{
				"session_id": meta.ID,
				"text":       result.Text,
				"status":     state.Status,
			})
			if err != nil {
				return e.fail(ctx, meta, state, err, hookManager)
			}
			assistantText := result.Text
			if value, ok := assistantPayload["text"].(string); ok {
				assistantText = value
			}
			msg := session.NewAssistantMessage(assistantText, translateToolCalls(result.ToolCalls))
			if meta := providerTurnMessageMeta(result); len(meta) > 0 {
				msg.Meta = meta
			}
			if err := e.store.AppendMessage(meta.ID, msg); err != nil {
				return RunResult{}, err
			}
			messages = append(messages, msg)
			state.LastAssistantExcerpt = truncateText(assistantText, 500)
			e.emit(meta.ID, "assistant.message", "assistant_output", map[string]any{
				"text":   assistantText,
				"status": state.Status,
			})
			result.Text = assistantText
		}

		if len(result.ToolCalls) == 0 {
			acceptedBackgroundAfterProvider, err := e.drainBackground(ctx, meta, hookManager)
			if err != nil {
				return RunResult{}, err
			}
			if acceptedBackgroundAfterProvider > 0 {
				doneCandidates = 0
				continue
			}
			acceptedAfterProvider, err := e.drainSteer(ctx, meta, hookManager)
			if err != nil {
				return RunResult{}, err
			}
			if acceptedAfterProvider > 0 {
				doneCandidates = 0
				continue
			}
		}

		if len(result.ToolCalls) > 0 {
			doneCandidates = 0
			toolResults := make([]session.ToolResult, 0, len(result.ToolCalls))
			for _, call := range result.ToolCalls {
				argumentsText := prettyJSON(call.Arguments)
				e.emit(meta.ID, "tool.before", "tool_execute", map[string]any{
					"tool_name": call.Name,
					"arguments": argumentsText,
				})
				beforePayload := map[string]any{
					"session_id": meta.ID,
					"tool_name":  call.Name,
					"mode":       meta.Mode,
					"arguments":  argumentsText,
				}
				updatedBeforePayload, err := hookManager.Trigger(ctx, "tool.before", beforePayload)
				if err != nil {
					return e.fail(ctx, meta, state, err, hookManager)
				}
				toolArgs := call.Arguments
				if value, ok := updatedBeforePayload["arguments"].(string); ok {
					trimmed := strings.TrimSpace(value)
					if trimmed != "" {
						if !json.Valid([]byte(trimmed)) {
							return e.fail(ctx, meta, state, errors.New("tool.before hook produced invalid arguments JSON"), hookManager)
						}
						toolArgs = json.RawMessage(trimmed)
					}
				}
				state.Phase = "tool_execute"
				if err := e.store.SaveState(meta.ID, state); err != nil {
					return RunResult{}, err
				}
				currentMessages := messages
				if len(toolResults) > 0 {
					currentMessages = append(currentMessages, session.NewToolMessage(toolResults))
				}
				guardKind := ""
				guardText := ""
				if !e.guardrailsYolo() {
					guardKind, guardText = toolGuard(meta.Workdir, currentMessages, call.Name, toolArgs)
				}

				if call.Name == "finish" && e.cfg.Runtime.PreCompletion.Enabled && e.cfg.Runtime.PreCompletion.CheckFeatures {
					featureListPath := filepath.Join(e.store.SessionDir(meta.ID), "feature_list.json")
					if data, err := os.ReadFile(featureListPath); err == nil {
						var featureList session.FeatureList
						if json.Unmarshal(data, &featureList) == nil {
							var incomplete []string
							for _, f := range featureList.Features {
								if f.Status != "completed" {
									incomplete = append(incomplete, fmt.Sprintf("- %s (status: %s)", f.ID, f.Status))
								}
							}
							if len(incomplete) > 0 {
								guardKind = "pre_completion_check"
								guardText = fmt.Sprintf("Pre-completion check failed: %d feature(s) not completed:\n%s\n\nPlease complete all features before calling finish.", len(incomplete), strings.Join(incomplete, "\n"))
							}
						}
					}
				}

				var toolResult session.ToolResult
				var toolErr error
				if guardText != "" {
					e.emit(meta.ID, "tool.blocked", "tool_execute", map[string]any{
						"tool_name": call.Name,
						"reason":    guardKind,
					})
					toolResult = session.ToolResult{
						ToolCallID:    call.ID,
						Name:          call.Name,
						LLMOutput:     "Error: " + guardText,
						DisplayOutput: "Error: " + guardText,
						IsError:       true,
						Metadata: map[string]any{
							"guard": guardKind,
						},
					}
				} else {
					toolCtx, cancel := context.WithCancel(ctx)
					e.control.setCancel(cancel)
					toolResult, toolErr = registry.Execute(toolCtx, call.Name, tools.ExecContext{
						SessionID: meta.ID,
						Workdir:   meta.Workdir,
						Store:     e.store,
						Config:    e.cfg,
						Catalog:   catalog,
						Emit: func(eventType string, data map[string]any) {
							e.emit(meta.ID, eventType, "tool_execute", data)
						},
					}, toolArgs)
					e.control.clearCancel(cancel)
					annotateExactArtifactTemplateResult(meta.Workdir, currentMessages, call.Name, toolArgs, &toolResult)
					annotateExactArtifactLiteralResult(meta.Workdir, currentMessages, call.Name, toolArgs, &toolResult)
					annotateReviewArtifactResult(meta.Workdir, currentMessages, call.Name, toolArgs, &toolResult)
				}
				toolResult.ToolCallID = call.ID
				toolResult.Name = call.Name
				if toolErr != nil {
					if errors.Is(toolErr, context.Canceled) {
						e.emit(meta.ID, "tool.interrupted", "tool_execute", map[string]any{
							"tool_name": call.Name,
						})
						toolResult = session.ToolResult{
							ToolCallID:    call.ID,
							Name:          call.Name,
							LLMOutput:     "[Tool execution was interrupted]",
							DisplayOutput: "[Tool execution was interrupted]",
							IsError:       true,
						}
						toolResults = append(toolResults, toolResult)
						if err := e.store.AppendMessage(meta.ID, session.NewToolMessage(toolResults)); err != nil {
							return RunResult{}, err
						}
						if e.control.consumePause() {
							return e.pause(ctx, meta, state, e.control.takePauseReason(), hookManager)
						}
						if e.control.consumeSteerInterrupt() {
							goto nextTurn
						}
					}
					toolResult = session.ToolResult{
						ToolCallID:    call.ID,
						Name:          call.Name,
						LLMOutput:     "Error: " + toolErr.Error(),
						DisplayOutput: "Error: " + toolErr.Error(),
						IsError:       true,
					}
				}
				afterPayload := map[string]any{
					"session_id":     meta.ID,
					"tool_name":      call.Name,
					"llm_output":     toolResult.LLMOutput,
					"display_output": toolResult.DisplayOutput,
				}
				updatedPayload, err := hookManager.Trigger(ctx, "tool.after", afterPayload)
				if err != nil {
					return e.fail(ctx, meta, state, err, hookManager)
				}
				if value, ok := updatedPayload["llm_output"].(string); ok {
					toolResult.LLMOutput = value
				}
				if value, ok := updatedPayload["display_output"].(string); ok {
					toolResult.DisplayOutput = value
				}
				toolResults = append(toolResults, toolResult)
				eventData := map[string]any{
					"tool_name":      call.Name,
					"display_output": toolResult.DisplayOutput,
					"is_error":       toolResult.IsError,
				}
				if len(toolResult.Metadata) > 0 {
					eventData["metadata"] = toolResult.Metadata
				}
				e.emit(meta.ID, "tool.after", "tool_execute", eventData)
				if toolResult.Final {
					if err := e.store.AppendMessage(meta.ID, session.NewToolMessage(toolResults)); err != nil {
						return RunResult{}, err
					}
					return e.complete(ctx, meta, state, toolResult.DisplayOutput, hookManager)
				}
			}
			if len(toolResults) > 0 {
				if err := e.store.AppendMessage(meta.ID, session.NewToolMessage(toolResults)); err != nil {
					return RunResult{}, err
				}
			}
			allowResolutionTurn = !usingResolutionTurn && turn+1 >= e.cfg.Runtime.MaxTurnsHard
		nextTurn:
			continue
		}
		allowResolutionTurn = false

		switch meta.Mode {
		case session.ModeRun:
			return e.awaitingInput(ctx, meta, state, result.Text, hookManager)
		case session.ModeExec:
			if doneCandidates == 0 {
				doneCandidates++
				if _, err := e.appendHarnessReminder(meta, "turn_decide", "Harness reminder: if the task is complete, call the finish tool explicitly.", "finish_required"); err != nil {
					return RunResult{}, err
				}
				continue
			}
			state.Status = session.StatusFailed
			state.IncompleteReason = "incomplete_no_finish"
			state.LastError = "incomplete_no_finish: task ended without explicit finish"
			if err := e.store.SaveState(meta.ID, state); err != nil {
				return RunResult{}, err
			}
			e.emit(meta.ID, "session.failed", "turn_decide", map[string]any{"reason": state.IncompleteReason})
			return RunResult{SessionID: meta.ID, Status: state.Status, LastError: state.LastError}, nil
		}
	}
}

func (e *Engine) awaitingInput(ctx context.Context, meta session.SessionMetadata, state session.State, text string, hookManager *hooks.Manager) (RunResult, error) {
	if _, err := hookManager.Trigger(ctx, "session.awaiting_input", sessionHookPayload(meta, session.StatusAwaitingInput)); err != nil {
		return e.fail(ctx, meta, state, err, hookManager)
	}
	state.Status = session.StatusAwaitingInput
	state.Phase = "turn_decide"
	state.LastAssistantExcerpt = truncateText(text, 500)
	if err := e.store.SaveState(meta.ID, state); err != nil {
		return RunResult{}, err
	}
	e.emit(meta.ID, "session.awaiting_input", state.Phase, map[string]any{})
	return RunResult{SessionID: meta.ID, Status: state.Status, FinalText: text}, nil
}

func (e *Engine) complete(ctx context.Context, meta session.SessionMetadata, state session.State, text string, hookManager *hooks.Manager) (RunResult, error) {
	if _, err := hookManager.Trigger(ctx, "session.complete", sessionHookPayload(meta, session.StatusCompleted)); err != nil {
		return e.fail(ctx, meta, state, err, hookManager)
	}
	state.Status = session.StatusCompleted
	state.Phase = "turn_decide"
	state.LastAssistantExcerpt = truncateText(text, 500)
	if err := e.store.SaveState(meta.ID, state); err != nil {
		return RunResult{}, err
	}
	e.emit(meta.ID, "session.completed", state.Phase, map[string]any{})
	return RunResult{SessionID: meta.ID, Status: state.Status, FinalText: text}, nil
}

func (e *Engine) pause(ctx context.Context, meta session.SessionMetadata, state session.State, reason string, hookManager *hooks.Manager) (RunResult, error) {
	if _, err := hookManager.Trigger(ctx, "session.pause", sessionHookPayload(meta, session.StatusPaused)); err != nil {
		return e.fail(ctx, meta, state, err, hookManager)
	}
	state.Status = session.StatusPaused
	state.PauseReason = reason
	state.Phase = "interrupt"
	if err := e.store.SaveState(meta.ID, state); err != nil {
		return RunResult{}, err
	}
	e.emit(meta.ID, "session.paused", state.Phase, map[string]any{"reason": reason})
	return RunResult{SessionID: meta.ID, Status: state.Status}, nil
}

func (e *Engine) fail(ctx context.Context, meta session.SessionMetadata, state session.State, err error, hookManager *hooks.Manager) (RunResult, error) {
	state.Status = session.StatusFailed
	state.Phase = "error"
	state.LastError = err.Error()
	if hookManager != nil {
		if _, hookErr := hookManager.Trigger(ctx, "session.fail", sessionHookPayload(meta, session.StatusFailed)); hookErr != nil {
			state.LastError = state.LastError + "; session.fail hook: " + hookErr.Error()
		}
	}
	if saveErr := e.store.SaveState(meta.ID, state); saveErr != nil {
		return RunResult{}, saveErr
	}
	e.emit(meta.ID, "session.failed", state.Phase, map[string]any{"error": state.LastError})
	return RunResult{SessionID: meta.ID, Status: state.Status, LastError: state.LastError}, err
}

func (e *Engine) emit(sessionID, eventType, phase string, data map[string]any) {
	evt := events.New(sessionID, eventType, phase, data)
	_ = e.store.AppendEvent(sessionID, evt)
	e.bus.Publish(evt)
}

func (e *Engine) guardrailsYolo() bool {
	return strings.EqualFold(strings.TrimSpace(e.cfg.Runtime.GuardrailsMode), "yolo")
}

func (e *Engine) maybeAppendHarnessReminder(meta session.SessionMetadata, messages []session.Message) ([]session.Message, error) {
	reminder := nextHarnessReminder(meta.Workdir, meta.Mode, messages)
	if reminder.Text == "" {
		return messages, nil
	}
	msg, err := e.appendHarnessReminder(meta, "prepare", reminder.Text, reminder.Kind)
	if err != nil {
		return nil, err
	}
	return append(messages, msg), nil
}

func (e *Engine) appendHarnessReminder(meta session.SessionMetadata, phase, text, kind string) (session.Message, error) {
	msg := session.NewMessage("user", text)
	msg.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   kind,
	}
	if err := e.store.AppendMessage(meta.ID, msg); err != nil {
		return session.Message{}, err
	}
	e.emit(meta.ID, "user.message", phase, map[string]any{
		"text":   text,
		"mode":   meta.Mode,
		"source": "harness_reminder",
		"kind":   kind,
	})
	return msg, nil
}

func (e *Engine) deferPendingInterrupts(sessionID string) error {
	if !e.control.consumeSteerInterrupt() {
		return nil
	}
	requests, err := e.store.LoadSteerRequests(sessionID)
	if err != nil {
		return err
	}
	changed := false
	for i := range requests {
		if requests[i].Status != session.SteerStatusPending || !requests[i].Interrupt {
			continue
		}
		requests[i].Status = session.SteerStatusDeferred
		changed = true
		e.emit(sessionID, "session.steer.deferred", "control_drain", map[string]any{
			"id":        requests[i].ID,
			"interrupt": true,
		})
	}
	if !changed {
		return nil
	}
	if err := e.store.UpdateSteerRequests(sessionID, requests); err != nil {
		return err
	}
	if state, err := e.store.LoadState(sessionID); err == nil {
		state.PendingSteerCount = countOpenSteerRequests(requests)
		_ = e.store.SaveState(sessionID, state)
	}
	return nil
}

func (e *Engine) drainSteer(ctx context.Context, meta session.SessionMetadata, hookManager *hooks.Manager) (int, error) {
	sessionID := meta.ID
	requests, err := e.store.LoadSteerRequests(sessionID)
	if err != nil {
		return 0, err
	}
	var changed bool
	accepted := 0
	for i := range requests {
		if requests[i].Status != session.SteerStatusPending && requests[i].Status != session.SteerStatusDeferred {
			continue
		}
		payload, err := hookManager.Trigger(ctx, "user.message", map[string]any{
			"session_id": sessionID,
			"text":       requests[i].Text,
			"mode":       meta.Mode,
		})
		if err != nil {
			return accepted, err
		}
		text := requests[i].Text
		if value, ok := payload["text"].(string); ok {
			text = value
		}
		msg := session.NewMessage("user", text)
		msg.Meta = map[string]any{
			"source":    "steer",
			"interrupt": requests[i].Interrupt,
		}
		if err := e.store.AppendMessage(sessionID, msg); err != nil {
			return accepted, err
		}
		requests[i].Status = session.SteerStatusAccepted
		changed = true
		accepted++
		e.emit(sessionID, "user.message", "control_drain", map[string]any{
			"text":      text,
			"mode":      meta.Mode,
			"source":    "steer",
			"interrupt": requests[i].Interrupt,
		})
		e.emit(sessionID, "session.steer.accepted", "control_drain", map[string]any{
			"id":        requests[i].ID,
			"interrupt": requests[i].Interrupt,
		})
	}
	if changed {
		if state, err := e.store.LoadState(sessionID); err == nil {
			state.PendingSteerCount = countOpenSteerRequests(requests)
			_ = e.store.SaveState(sessionID, state)
		}
		return accepted, e.store.UpdateSteerRequests(sessionID, requests)
	}
	return accepted, nil
}

func (e *Engine) drainBackground(ctx context.Context, meta session.SessionMetadata, hookManager *hooks.Manager) (int, error) {
	sessionID := meta.ID
	notifications, err := e.store.LoadBackgroundNotifications(sessionID)
	if err != nil {
		return 0, err
	}
	pending := make([]session.BackgroundNotification, 0, len(notifications))
	for _, notification := range notifications {
		if notification.DeliveryStatus == session.BackgroundNotificationPending {
			pending = append(pending, notification)
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}
	payload := map[string]any{
		"background_results": backgroundPayload(pending),
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	text := "<background-agent-results>\n" + string(data) + "\n</background-agent-results>"
	hookPayload, err := hookManager.Trigger(ctx, "user.message", map[string]any{
		"session_id": meta.ID,
		"text":       text,
		"mode":       meta.Mode,
	})
	if err != nil {
		return 0, err
	}
	if value, ok := hookPayload["text"].(string); ok {
		text = value
	}
	msg := session.NewMessage("user", text)
	msg.Meta = map[string]any{
		"source": "background_results",
		"count":  len(pending),
	}
	if err := e.store.AppendMessage(sessionID, msg); err != nil {
		return 0, err
	}
	for i := range notifications {
		if notifications[i].DeliveryStatus == session.BackgroundNotificationPending {
			notifications[i].DeliveryStatus = session.BackgroundNotificationAccepted
		}
	}
	if err := e.store.UpdateBackgroundNotifications(sessionID, notifications); err != nil {
		return 0, err
	}
	e.emit(sessionID, "user.message", "control_drain", map[string]any{
		"text":   text,
		"mode":   meta.Mode,
		"source": "background_results",
		"count":  len(pending),
	})
	e.emit(sessionID, "session.background.accepted", "control_drain", map[string]any{
		"count": len(pending),
	})
	return len(pending), nil
}

func backgroundPayload(notifications []session.BackgroundNotification) []map[string]any {
	out := make([]map[string]any, 0, len(notifications))
	for _, notification := range notifications {
		out = append(out, map[string]any{
			"queue_job_id":      notification.QueueJobID,
			"session_id":        notification.SessionID,
			"agent_name":        notification.AgentName,
			"agent_role":        notification.AgentRole,
			"status":            notification.Status,
			"session_status":    notification.SessionStatus,
			"requested_workdir": notification.RequestedWorkdir,
			"effective_workdir": notification.EffectiveWorkdir,
			"visible_paths":     append([]string(nil), notification.VisiblePaths...),
			"final_text":        notification.FinalText,
			"last_error":        notification.LastError,
		})
	}
	return out
}

func countOpenSteerRequests(requests []session.SteerRequest) int {
	pending := 0
	for _, request := range requests {
		if request.Status == session.SteerStatusPending || request.Status == session.SteerStatusDeferred {
			pending++
		}
	}
	return pending
}

func sessionHookPayload(meta session.SessionMetadata, status string) map[string]any {
	return map[string]any{
		"session_id": meta.ID,
		"status":     status,
		"workdir":    meta.Workdir,
		"provider":   meta.Provider,
		"model":      meta.Model,
	}
}

func providerTools(registry *tools.Registry) []provider.ToolSchema {
	defs := registry.Definitions()
	out := make([]provider.ToolSchema, 0, len(defs))
	for _, def := range defs {
		out = append(out, provider.ToolSchema{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.InputSchema,
		})
	}
	return out
}

func providerRequestMetadata(meta session.SessionMetadata) map[string]any {
	data := map[string]any{
		"session_id": meta.ID,
		"mode":       meta.Mode,
	}
	for key, value := range sessionIdentityEventData(meta) {
		data[key] = value
	}
	if strings.TrimSpace(meta.QueueJobID) != "" {
		data["queue_job_id"] = meta.QueueJobID
	}
	return data
}

func providerRequestPreparedEventData(meta session.SessionMetadata, requestMetadata map[string]any) map[string]any {
	data := map[string]any{
		"provider":         meta.Provider,
		"model":            meta.Model,
		"metadata_enabled": len(requestMetadata) > 0,
	}
	if len(requestMetadata) > 0 {
		keys := make([]string, 0, len(requestMetadata))
		for key := range requestMetadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		data["metadata_keys"] = keys
	}
	if meta.ProviderOptions.Temperature != nil {
		data["temperature"] = *meta.ProviderOptions.Temperature
	}
	if meta.ProviderOptions.TopP != nil {
		data["top_p"] = *meta.ProviderOptions.TopP
	}
	if meta.ProviderOptions.MaxOutputTokens > 0 {
		data["max_output_tokens"] = meta.ProviderOptions.MaxOutputTokens
	}
	if strings.TrimSpace(meta.ProviderOptions.ReasoningEffort) != "" {
		data["reasoning_effort"] = meta.ProviderOptions.ReasoningEffort
	}
	if strings.TrimSpace(meta.ProviderOptions.TextVerbosity) != "" {
		data["text_verbosity"] = meta.ProviderOptions.TextVerbosity
	}
	if meta.ProviderOptions.ThinkingBudget > 0 {
		data["thinking_budget"] = meta.ProviderOptions.ThinkingBudget
	}
	if meta.ProviderOptions.IncludeThoughts != nil {
		data["include_thoughts"] = *meta.ProviderOptions.IncludeThoughts
	}
	if meta.ProviderOptions.Store != nil {
		data["store"] = *meta.ProviderOptions.Store
	}
	if meta.ProviderOptions.SendMetadata != nil {
		data["send_metadata"] = *meta.ProviderOptions.SendMetadata
	}
	if meta.ProviderOptions.RetryPolicy != nil {
		data["retry_policy"] = map[string]any{
			"max_attempts":    meta.ProviderOptions.RetryPolicy.MaxAttempts,
			"base_delay_ms":   meta.ProviderOptions.RetryPolicy.BaseDelayMS,
			"retry_429":       meta.ProviderOptions.RetryPolicy.Retry429,
			"retry_5xx":       meta.ProviderOptions.RetryPolicy.Retry5xx,
			"retry_transport": meta.ProviderOptions.RetryPolicy.RetryTransport,
		}
	}
	return data
}

func translateToolCalls(calls []provider.ToolCall) []session.ToolCall {
	out := make([]session.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, session.ToolCall{
			ID:             call.ID,
			Name:           call.Name,
			Arguments:      call.Arguments,
			ProviderCallID: call.ProviderCallID,
		})
	}
	return out
}

func providerTurnEventData(result provider.TurnResult) map[string]any {
	data := map[string]any{
		"stop_reason": result.StopReason,
		"usage": map[string]any{
			"input_tokens":  result.Usage.InputTokens,
			"output_tokens": result.Usage.OutputTokens,
		},
	}
	if strings.TrimSpace(result.ProviderResponseID) != "" {
		data["provider_response_id"] = result.ProviderResponseID
	}
	if len(result.RawProvider) > 0 {
		data["raw_provider"] = result.RawProvider
	}
	return data
}

func providerTurnMessageMeta(result provider.TurnResult) map[string]any {
	meta := map[string]any{}
	if strings.TrimSpace(result.StopReason) != "" {
		meta["provider_stop_reason"] = result.StopReason
	}
	if strings.TrimSpace(result.ProviderResponseID) != "" {
		meta["provider_response_id"] = result.ProviderResponseID
	}
	if len(result.RawProvider) > 0 {
		meta["raw_provider"] = result.RawProvider
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func contextLoadedEventData(meta session.SessionMetadata, turn int, stack projectMemoryStack, todo []session.TodoItem, tasks []session.Task) map[string]any {
	readyCount, blockedCount, completedCount := taskCounts(tasks)
	data := map[string]any{
		"turn":                   turn,
		"project_memory_present": stack.PresentPaths(),
		"project_memory_missing": stack.MissingPaths(),
		"todo_count":             len(todo),
		"task_count":             len(tasks),
		"ready_task_count":       readyCount,
		"blocked_task_count":     blockedCount,
		"completed_task_count":   completedCount,
	}
	if item := currentInProgressTodo(todo); item != nil {
		data["current_in_progress_todo"] = item
	}
	if task := currentInProgressTask(tasks); task != nil {
		data["current_in_progress_task"] = task
	}
	for key, value := range sessionIdentityEventData(meta) {
		data[key] = value
	}
	return data
}

func sessionIdentityEventData(meta session.SessionMetadata) map[string]any {
	data := make(map[string]any)
	if strings.TrimSpace(meta.RootSessionID) != "" {
		data["root_session_id"] = meta.RootSessionID
	}
	if strings.TrimSpace(meta.ParentSessionID) != "" {
		data["parent_session_id"] = meta.ParentSessionID
	}
	if strings.TrimSpace(meta.AgentName) != "" {
		data["agent_name"] = meta.AgentName
	}
	if strings.TrimSpace(meta.AgentRole) != "" {
		data["agent_role"] = meta.AgentRole
	}
	if meta.Depth > 0 {
		data["depth"] = meta.Depth
	}
	if meta.Isolation != nil && strings.TrimSpace(meta.Isolation.Mode) != "" {
		data["isolation_mode"] = meta.Isolation.Mode
	}
	return data
}

func taskCounts(tasks []session.Task) (ready, blocked, completed int) {
	for _, task := range tasks {
		switch {
		case task.Status == "completed" || task.Status == "cancelled":
			completed++
		case task.Status == "pending" && len(task.BlockedBy) == 0:
			ready++
		case task.Status == "pending" && len(task.BlockedBy) > 0:
			blocked++
		}
	}
	return ready, blocked, completed
}

func prettyJSON(value any) string {
	data, _ := json.MarshalIndent(value, "", "  ")
	return string(data)
}
