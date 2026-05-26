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
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/fileutil"
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
	runner    RunnerInterface
}

type RunnerInterface interface {
	AutoContinue(ctx context.Context, sessionID string) (RunResult, error)
}

func NewEngine(cfg *config.Config, store *session.Store, bus *events.Bus, control *runControl) *Engine {
	return &Engine{
		cfg:       cfg,
		store:     store,
		bus:       bus,
		control:   control,
		compactor: newCompactor(store),
		runner:    nil,
	}
}

func (e *Engine) SetRunner(runner RunnerInterface) {
	e.runner = runner
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
	hardTurnLimit := e.cfg.Runtime.MaxTurnsHard
	hardTurnLimitEnabled := hardTurnLimit > 0
	runStartTurn := state.Turn
	for turn := state.Turn; ; turn++ {
		usingResolutionTurn := false
		runTurn := turn - runStartTurn
		if hardTurnLimitEnabled && runTurn >= hardTurnLimit {
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
		messages, err = e.maybeAppendToolLoopReminder(meta, messages)
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
		goal, err := loadGoalOptional(e.store, meta.ID)
		if err != nil {
			return RunResult{}, err
		}
		budgetWrapUpTurn := false
		if goal != nil && goal.Status == session.GoalStatusBudgetLimited && goal.Control.StopOnBudget && goal.BudgetWrapUpRequestedAt != "" && !session.HasBudgetWrapUpRecord(*goal) {
			goalCopy := *goal
			if session.MarkBudgetWrapUpTurnStarted(&goalCopy) {
				if err := e.store.SaveGoal(meta.ID, goalCopy); err != nil {
					return RunResult{}, err
				}
				if err := e.store.AppendGoalHistory(meta.ID, session.GoalHistoryEntry{
					Type:   "goal.budget_wrapup_turn_started",
					Source: session.GoalSourceSystem,
					Status: goalCopy.Status,
					Data: map[string]any{
						"budget_wrapup_turn_started_at": goalCopy.BudgetWrapUpTurnStartedAt,
					},
				}); err != nil {
					if rollbackErr := e.store.SaveGoal(meta.ID, *goal); rollbackErr != nil {
						return RunResult{}, fmt.Errorf("restore goal after budget wrap-up turn history error %v: %w", err, rollbackErr)
					}
					return e.fail(ctx, meta, state, err, hookManager)
				}
				e.emit(meta.ID, "goal.budget_wrapup_turn_started", "prepare", goalEventData(goalCopy))
				goal = &goalCopy
				budgetWrapUpTurn = true
			} else {
				return e.awaitingBudgetWrapUp(ctx, meta, state, hookManager)
			}
		}
		planMode, err := loadPlanModeOptional(e.store, meta.ID)
		if err != nil {
			return RunResult{}, err
		}
		projectMemory := loadProjectMemoryStack(meta.Workdir)
		contextData := contextLoadedEventData(meta, state.Turn, projectMemory, todo, tasks)
		if goal != nil {
			contextData["goal_status"] = goal.Status
			contextData["goal_mode"] = goal.Mode
			contextData["goal_id"] = goal.GoalID
		}
		if planMode != nil {
			contextData["plan_mode_status"] = planMode.Status
			contextData["plan_mode_id"] = planMode.PlanModeID
			contextData["plan_version"] = planMode.PlanVersion
		}
		e.emit(meta.ID, "session.context.loaded", "prepare", contextData)

		systemPrompt := buildSystemPrompt(meta.Workdir, meta.Mode, systemOverride, catalog.Summaries(), catalog.TrustedCommandTools(meta.Workdir), state, messages, meta.AgentName, meta.AgentRole)
		if goal != nil {
			if contextText := goalPromptContext(*goal); contextText != "" {
				systemPrompt += "\n\n" + contextText
			}
		}
		if planMode != nil {
			if shouldUsePlanModeInstructions(planMode) {
				systemPrompt += "\n\n" + buildPlanModeInstructions(*planMode)
			} else if contextText := buildApprovedPlanContext(*planMode); contextText != "" {
				systemPrompt += "\n\n" + contextText
			}
		}
		if e.guardrailsYolo() {
			systemPrompt += "\n\n## Guardrails Mode\nYOLO mode is enabled. Runtime retrieval, project-memory, and review-artifact guardrails are disabled for this run. You still operate within tool-enforced workspace boundaries, shell timeouts, and explicit user instructions."
		}
		compactionProfile := compactionProfileFromConfig(meta, e.cfg.Runtime.Compact)
		view, compactionInputChars, didCompact, err := e.compactor.BuildWithProfile(meta.ID, meta.Workdir, state, messages, todo, tasks, compactionProfile, state.LastCompactionInputChars, func(evt events.Event) {
			_ = e.store.AppendEvent(meta.ID, evt)
			e.bus.Publish(evt)
		})
		if err != nil {
			return e.fail(ctx, meta, state, err, hookManager)
		}
		if didCompact {
			state.LastCompactionInputChars = compactionInputChars
			if err := e.store.SaveState(meta.ID, state); err != nil {
				return RunResult{}, err
			}
		}

		state.Phase = "provider_call"
		if err := e.store.SaveState(meta.ID, state); err != nil {
			return RunResult{}, err
		}
		e.emit(meta.ID, "provider.call", state.Phase, map[string]any{"provider": meta.Provider})
		requestMetadata := providerRequestMetadata(meta)
		if planMode != nil {
			requestMetadata["collaboration_mode"] = "plan"
			if !session.IsPlanModePending(planMode.Status) {
				requestMetadata["collaboration_mode"] = "default"
			}
			requestMetadata["plan_mode_id"] = planMode.PlanModeID
			requestMetadata["plan_status"] = planMode.Status
			requestMetadata["approved_plan_version"] = planMode.ApprovedVersion
		}
		if meta.ProviderOptions.SendMetadata != nil && !*meta.ProviderOptions.SendMetadata {
			requestMetadata = nil
		}
		e.emit(meta.ID, "provider.request.prepared", state.Phase, providerRequestPreparedEventData(meta, requestMetadata))
		callCtx, cancel := context.WithCancel(ctx)
		e.control.setCancel(cancel)
		providerStart := time.Now()
		var providerAttemptErr error
		result, err := adapter.RunTurn(callCtx, provider.TurnRequest{
			SessionID:        meta.ID,
			Model:            meta.Model,
			SystemPrompt:     systemPrompt,
			Messages:         view,
			Tools:            providerToolsForPlanMode(registry, planMode),
			Metadata:         requestMetadata,
			Temperature:      meta.ProviderOptions.Temperature,
			TopP:             meta.ProviderOptions.TopP,
			MaxOutputTokens:  meta.ProviderOptions.MaxOutputTokens,
			ProviderProfile:  meta.Provider,
			APIProvider:      meta.ProviderOptions.APIProvider,
			ReasoningEffort:  meta.ProviderOptions.ReasoningEffort,
			ReasoningSummary: meta.ProviderOptions.ReasoningSummary,
			TextVerbosity:    meta.ProviderOptions.TextVerbosity,
			ThinkingBudget:   meta.ProviderOptions.ThinkingBudget,
			IncludeThoughts:  meta.ProviderOptions.IncludeThoughts,
			PromptCache:      meta.ProviderOptions.PromptCache,
			Store:            meta.ProviderOptions.Store,
		}, func(eventType string, data map[string]any) {
			if eventType == "provider.retry" {
				if appendErr := e.appendProviderRetry(meta.ID, data); appendErr != nil && providerAttemptErr == nil {
					providerAttemptErr = appendErr
					if cancel != nil {
						cancel()
					}
					return
				}
				if appendErr := recordProviderRetry(e.store, meta, state.Turn, data); appendErr != nil && providerAttemptErr == nil {
					providerAttemptErr = appendErr
					if cancel != nil {
						cancel()
					}
					return
				}
				if providerAttemptErr == nil {
					_ = writeSessionSummary(e.store, meta.ID)
				}
				return
			}
			e.emit(meta.ID, eventType, "provider_call", data)
		})
		e.control.clearCancel(cancel)
		if providerAttemptErr != nil {
			return e.fail(ctx, meta, state, providerAttemptErr, hookManager)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				switch {
				case e.control.consumePause():
					reason := e.control.takePauseReason()
					if appendErr := e.appendProviderCancelled(meta.ID, reason); appendErr != nil {
						return RunResult{}, appendErr
					}
					return e.pause(ctx, meta, state, reason, hookManager)
				case e.control.consumeSteerInterrupt():
					if appendErr := e.appendProviderCancelled(meta.ID, "steer_interrupt"); appendErr != nil {
						return RunResult{}, appendErr
					}
					continue
				}
			}
			if e.shouldAutoResumeProviderError(err, state) {
				state.ProviderAutoResumeCount++
				state.LastError = err.Error()
				state.Phase = "provider_call"
				if err := e.store.SaveState(meta.ID, state); err != nil {
					return RunResult{}, err
				}
				if appendErr := recordProviderAutoResumeAttempt(e.store, meta, state.Turn, err, state.ProviderAutoResumeCount); appendErr != nil {
					return e.fail(ctx, meta, state, appendErr, hookManager)
				}
				_ = writeSessionSummary(e.store, meta.ID)
				_ = writeLongRunCheckpoint(e.store, meta.ID)
				if appendErr := e.appendProviderAutoResume(meta.ID, err, state.ProviderAutoResumeCount); appendErr != nil {
					return RunResult{}, appendErr
				}
				if _, appendErr := e.appendHarnessReminder(meta, "provider_call", providerAutoResumePrompt(err, state.ProviderAutoResumeCount, e.cfg.Runtime.ProviderAutoResume.MaxAttempts), "provider_auto_resume"); appendErr != nil {
					return RunResult{}, appendErr
				}
				continue
			}
			state.Status = session.StatusFailed
			state.LastError = err.Error()
			state.Phase = "provider_call"
			if saveErr := e.store.SaveState(meta.ID, state); saveErr != nil {
				return RunResult{}, fmt.Errorf("record provider failure state after %v: %w", err, saveErr)
			}
			if appendErr := e.appendEvent(meta.ID, "session.failed", state.Phase, map[string]any{"error": err.Error()}); appendErr != nil {
				return RunResult{}, fmt.Errorf("record provider failure event after %v: %w", err, appendErr)
			}
			if appendErr := recordProviderFailure(e.store, meta, state.Turn, err, false); appendErr != nil {
				return e.fail(ctx, meta, state, appendErr, hookManager)
			}
			_ = writeSessionSummary(e.store, meta.ID)
			_ = writeLongRunCheckpoint(e.store, meta.ID)
			return RunResult{SessionID: meta.ID, Status: state.Status, LastError: err.Error()}, WrapProviderError(err)
		}
		if providerRawSidecarEnabled(meta) {
			if err := e.store.SaveProviderRawSidecar(meta.ID, providerRawSidecarEnvelope(meta, state.Turn, result)); err != nil {
				return RunResult{}, err
			}
		}
		if err := recordProviderSuccess(e.store, meta, state.Turn, result); err != nil {
			return e.fail(ctx, meta, state, err, hookManager)
		}
		accountedGoal, budgetLimited, err := e.updateGoalAccounting(meta.ID, state.Turn, result.Usage, time.Since(providerStart))
		if err != nil {
			return e.fail(ctx, meta, state, err, hookManager)
		}
		budgetStopRequested := budgetLimited && accountedGoal.Control.StopOnBudget
		_ = writeSessionSummary(e.store, meta.ID)
		if state.ProviderAutoResumeCount != 0 {
			state.ProviderAutoResumeCount = 0
			if err := e.store.SaveState(meta.ID, state); err != nil {
				return RunResult{}, err
			}
		}

		e.emit(meta.ID, "turn.stopped", "provider_call", providerTurnEventData(result))

		result.ProviderContentBlocks = stampProviderContentBlocks(meta, result.ProviderContentBlocks)
		if shouldPersistAssistantResult(result) {
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
			msg := session.NewAssistantMessage(assistantText, result.Thinking, translateToolCalls(result.ToolCalls))
			msg.ProviderContentBlocks = result.ProviderContentBlocks
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

		if budgetStopRequested {
			if len(result.ToolCalls) > 0 {
				toolResults := syntheticToolResults(result.ToolCalls, "Error: goal budget limit reached and stop_on_budget is true; this tool call was not executed. Wrap up with record_goal_progress kind=\"budget_wrapup\" on the next turn.")
				if err := e.store.AppendMessage(meta.ID, session.NewToolMessage(toolResults)); err != nil {
					return RunResult{}, err
				}
			}
			if _, err := e.appendHarnessReminder(meta, "turn_decide", goalBudgetWrapUpPrompt(), "goal_budget_wrapup_required"); err != nil {
				return RunResult{}, err
			}
			continue
		}

		if len(result.ToolCalls) == 0 {
			if failureReason, failureText := providerStopFailure(result.StopReason); failureReason != "" {
				state.Status = session.StatusFailed
				state.Phase = "turn_decide"
				state.IncompleteReason = failureReason
				state.LastError = failureText
				if err := e.store.SaveState(meta.ID, state); err != nil {
					return RunResult{}, err
				}
				if err := e.appendEvent(meta.ID, "session.failed", state.Phase, map[string]any{"reason": failureReason, "stop_reason": result.StopReason}); err != nil {
					return RunResult{}, fmt.Errorf("record provider stop failure event for %s: %w", failureReason, err)
				}
				_ = writeSessionSummary(e.store, meta.ID)
				_ = writeLongRunCheckpoint(e.store, meta.ID)
				return RunResult{SessionID: meta.ID, Status: state.Status, LastError: state.LastError}, nil
			}
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
			planModeTerminal := ""
			for callIndex, call := range result.ToolCalls {
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
					toolResults = append(toolResults, syntheticToolResults(result.ToolCalls[callIndex:], "Error: tool.before hook failed: "+err.Error())...)
					if appendErr := e.store.AppendMessage(meta.ID, session.NewToolMessage(toolResults)); appendErr != nil {
						return RunResult{}, appendErr
					}
					return e.fail(ctx, meta, state, err, hookManager)
				}
				toolArgs := call.Arguments
				if value, ok := updatedBeforePayload["arguments"].(string); ok {
					trimmed := strings.TrimSpace(value)
					if trimmed != "" {
						if !json.Valid([]byte(trimmed)) {
							toolResults = append(toolResults, syntheticToolResults(result.ToolCalls[callIndex:], "Error: tool.before hook produced invalid arguments JSON")...)
							if appendErr := e.store.AppendMessage(meta.ID, session.NewToolMessage(toolResults)); appendErr != nil {
								return RunResult{}, appendErr
							}
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
				controller := NewCompletionController(e.store, meta.ID, meta.Workdir, e.guardrailsYolo(), func(eventType string, data map[string]any) {
					e.emit(meta.ID, eventType, "tool_execute", data)
				})
				decision := controller.EvaluateToolCall(currentMessages, call.Name, toolArgs)
				if decision.Status == GateAllow && call.Name == "finish" && meta.Mode == session.ModeInit && e.cfg.Runtime.PreCompletion.Enabled && e.cfg.Runtime.PreCompletion.CheckFeatures {
					decision = controller.EvaluatePreCompletionFeatures(true)
				}
				if decision.Status == GateAllow {
					controller.MarkAllowed(call.Name)
				}
				guardKind := decision.GateID
				guardText := decision.ModelMessage

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
					var planInputResponder tools.PlanInputResponder
					if responder, ok := e.runner.(tools.PlanInputResponder); ok {
						planInputResponder = responder
					}
					toolResult, toolErr = registry.Execute(toolCtx, call.Name, tools.ExecContext{
						SessionID:          meta.ID,
						ToolCallID:         call.ID,
						Workdir:            meta.Workdir,
						Store:              e.store,
						Config:             e.cfg,
						Catalog:            catalog,
						PlanInputResponder: planInputResponder,
						Emit: func(eventType string, data map[string]any) {
							e.emit(meta.ID, eventType, "tool_execute", data)
						},
					}, toolArgs)
					e.control.clearCancel(cancel)
					annotateExactArtifactTemplateResult(meta.Workdir, currentMessages, call.Name, toolArgs, &toolResult)
					annotateExactArtifactLiteralResult(meta.Workdir, currentMessages, call.Name, toolArgs, &toolResult)
					annotateTargetConsistencyResult(meta.Workdir, currentMessages, call.Name, toolArgs, &toolResult)
					annotateReviewArtifactResult(meta.Workdir, currentMessages, call.Name, toolArgs, &toolResult)
					toolResult.ToolCallID = call.ID
					toolResult.Name = call.Name
					if err := controller.TrackToolResult(call.Name, toolResult, state.Turn); err != nil {
						toolResult.IsError = true
						toolResult.LLMOutput = "Error: artifact tracker update failed after tool execution: " + err.Error()
						toolResult.DisplayOutput = toolResult.LLMOutput
						toolResults = append(toolResults, toolResult)
						toolResults = append(toolResults, syntheticToolResults(result.ToolCalls[callIndex+1:], "Error: artifact tracker update failed before this call ran: "+err.Error())...)
						if appendErr := e.store.AppendMessage(meta.ID, session.NewToolMessage(toolResults)); appendErr != nil {
							return RunResult{}, appendErr
						}
						return e.fail(ctx, meta, state, err, hookManager)
					}
					if call.Name == "record_goal_progress" && !toolResult.IsError {
						_ = writeSessionSummary(e.store, meta.ID)
						_ = writeLongRunCheckpoint(e.store, meta.ID)
					}
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
						toolResults = append(toolResults, syntheticToolResults(result.ToolCalls[callIndex+1:], "Error: tool execution was interrupted before this call ran")...)
						if err := e.store.AppendMessage(meta.ID, session.NewToolMessage(toolResults)); err != nil {
							return RunResult{}, err
						}
						if e.control.consumePause() {
							return e.pause(ctx, meta, state, e.control.takePauseReason(), hookManager)
						}
						if e.control.consumeSteerInterrupt() {
							goto nextTurn
						}
						return e.fail(ctx, meta, state, toolErr, hookManager)
					}
					if toolResult.LLMOutput != "" || toolResult.DisplayOutput != "" || len(toolResult.Metadata) > 0 {
						toolResult.IsError = true
						if toolResult.LLMOutput == "" {
							toolResult.LLMOutput = "Error: " + toolErr.Error()
						}
						if toolResult.DisplayOutput == "" {
							toolResult.DisplayOutput = toolResult.LLMOutput
						}
					} else {
						toolResult = session.ToolResult{
							ToolCallID:    call.ID,
							Name:          call.Name,
							LLMOutput:     "Error: " + toolErr.Error(),
							DisplayOutput: "Error: " + toolErr.Error(),
							IsError:       true,
						}
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
					toolResult.IsError = true
					if strings.TrimSpace(toolResult.LLMOutput) == "" {
						toolResult.LLMOutput = "Error: tool.after hook failed: " + err.Error()
					}
					if strings.TrimSpace(toolResult.DisplayOutput) == "" {
						toolResult.DisplayOutput = toolResult.LLMOutput
					}
					toolResults = append(toolResults, toolResult)
					toolResults = append(toolResults, syntheticToolResults(result.ToolCalls[callIndex+1:], "Error: tool.after hook failed before this call ran: "+err.Error())...)
					if appendErr := e.store.AppendMessage(meta.ID, session.NewToolMessage(toolResults)); appendErr != nil {
						return RunResult{}, appendErr
					}
					return e.fail(ctx, meta, state, err, hookManager)
				}
				if value, ok := updatedPayload["llm_output"].(string); ok {
					toolResult.LLMOutput = value
				}
				if value, ok := updatedPayload["display_output"].(string); ok {
					toolResult.DisplayOutput = value
				}

				// Ephemeral message handling
				if e.cfg.Runtime.Ephemeral.Enabled {
					toolDef := registry.Get(call.Name)
					if toolDef != nil && toolDef.Ephemeral {
						count := countToolCalls(messages, call.Name)
						if count > toolDef.EphemeralWindow {
							artifactPath := e.ephemeralArtifactPath(meta.ID, call.Name, turn)
							if err := fileutil.AtomicWriteFileNoSymlink(artifactPath, []byte(toolResult.LLMOutput), 0o600); err == nil {
								toolResult.LLMOutput = fmt.Sprintf(
									"[Output saved to %s; this internal artifact is not readable via read_file. If you need to inspect it later, rerun the command and redirect output to a normal workspace file such as reports/validation.txt.]",
									artifactPath,
								)
								if toolResult.Metadata == nil {
									toolResult.Metadata = make(map[string]any)
								}
								toolResult.Metadata["ephemeral_artifact"] = artifactPath
							}
						}
					}
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
				if action := terminalPlanModeAction(toolResult); action != "" {
					planModeTerminal = action
					if callIndex+1 < len(result.ToolCalls) {
						toolResults = append(toolResults, syntheticToolResults(result.ToolCalls[callIndex+1:], "Error: submit_plan ended the Plan Mode turn; this later tool call was not executed")...)
					}
					break
				}
				if toolResult.Final {
					if callIndex+1 < len(result.ToolCalls) {
						toolResults = append(toolResults, syntheticToolResults(result.ToolCalls[callIndex+1:], "Error: finish completed the session; this later tool call was not executed")...)
					}
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
			if planModeTerminal == planModeTerminalPlanSubmitted {
				return e.awaitingPlanApproval(ctx, meta, state, hookManager)
			}
			if planModeTerminal == planModeTerminalPlanCancelled {
				return e.awaitingPlanCancelled(ctx, meta, state, hookManager)
			}
			allowResolutionTurn = hardTurnLimitEnabled && !usingResolutionTurn && turn+1 >= hardTurnLimit
		nextTurn:
			if budgetWrapUpTurn {
				return e.awaitingBudgetWrapUp(ctx, meta, state, hookManager)
			}
			continue
		}
		allowResolutionTurn = false
		if budgetWrapUpTurn {
			return e.awaitingBudgetWrapUp(ctx, meta, state, hookManager)
		}

		switch meta.Mode {
		case session.ModeRun:
			return e.awaitingInput(ctx, meta, state, result.Text, hookManager)
		case session.ModeExec, session.ModeInit:
			if doneCandidates == 0 {
				doneCandidates++
				if _, err := e.appendHarnessReminder(meta, "turn_decide", "Harness reminder: if the task is complete, call the finish tool explicitly.", "finish_required"); err != nil {
					return RunResult{}, err
				}
				continue
			}
			if doneCandidates == 1 {
				doneCandidates++
				continue
			}
			state.Status = session.StatusFailed
			state.IncompleteReason = "incomplete_no_finish"
			state.LastError = "incomplete_no_finish: task ended without explicit finish"
			if err := e.store.SaveState(meta.ID, state); err != nil {
				return RunResult{}, err
			}
			e.emit(meta.ID, "session.failed", "turn_decide", map[string]any{"reason": state.IncompleteReason})
			result := RunResult{SessionID: meta.ID, Status: state.Status, LastError: state.LastError}
			if e.cfg.Runtime.RalphLoop.Enabled {
				return e.runner.AutoContinue(ctx, meta.ID)
			}
			return result, nil
		}
	}
}

func (e *Engine) ephemeralArtifactPath(sessionID, toolName string, turn int) string {
	base := strings.TrimSpace(e.cfg.Runtime.Ephemeral.ArtifactDir)
	if base == "" || filepath.Clean(base) == filepath.Clean(".artifacts/tool-outputs") {
		base = filepath.Join(e.store.SessionDir(sessionID), "artifacts", "tool-outputs")
	} else {
		base = filepath.Join(base, sessionID)
	}
	return filepath.Join(base, fmt.Sprintf("%s-turn%d.txt", toolName, turn))
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
	if err := e.appendEvent(meta.ID, "session.awaiting_input", state.Phase, map[string]any{}); err != nil {
		return RunResult{}, fmt.Errorf("record session.awaiting_input event: %w", err)
	}
	result := RunResult{SessionID: meta.ID, Status: state.Status, FinalText: text}
	if err := e.reconcileLinkedQueueJob(meta.ID); err != nil {
		return result, err
	}
	_ = writeSessionSummary(e.store, meta.ID)
	_ = writeLongRunCheckpoint(e.store, meta.ID)
	return result, nil
}

func (e *Engine) awaitingBudgetWrapUp(ctx context.Context, meta session.SessionMetadata, state session.State, hookManager *hooks.Manager) (RunResult, error) {
	text := "Goal budget limit reached. stop_on_budget is true, so execution is awaiting input after the budget wrap-up boundary."
	if goal, err := e.store.LoadGoal(meta.ID); err == nil && session.HasBudgetWrapUpRecord(goal) {
		text = "Goal budget limit reached and budget wrap-up was recorded. Execution is awaiting input because stop_on_budget is true."
	} else if err != nil {
		return RunResult{}, fmt.Errorf("load goal.json for budget wrap-up: %w", err)
	}
	if _, err := hookManager.Trigger(ctx, "session.awaiting_input", sessionHookPayload(meta, session.StatusAwaitingInput)); err != nil {
		return e.fail(ctx, meta, state, err, hookManager)
	}
	state.Status = session.StatusAwaitingInput
	state.Phase = "goal_budget_limited"
	state.LastAssistantExcerpt = truncateText(text, 500)
	if err := e.store.SaveState(meta.ID, state); err != nil {
		return RunResult{}, err
	}
	e.emit(meta.ID, "session.awaiting_input", state.Phase, map[string]any{"reason": "goal_budget_limited"})
	result := RunResult{SessionID: meta.ID, Status: state.Status, FinalText: text}
	if err := e.reconcileLinkedQueueJob(meta.ID); err != nil {
		return result, err
	}
	_ = writeSessionSummary(e.store, meta.ID)
	_ = writeLongRunCheckpoint(e.store, meta.ID)
	return result, nil
}

func (e *Engine) awaitingPlanApproval(ctx context.Context, meta session.SessionMetadata, state session.State, hookManager *hooks.Manager) (RunResult, error) {
	if _, err := hookManager.Trigger(ctx, "session.awaiting_input", sessionHookPayload(meta, session.StatusAwaitingInput)); err != nil {
		return e.fail(ctx, meta, state, err, hookManager)
	}
	state.Status = session.StatusAwaitingInput
	state.Phase = "plan_approval"
	state.LastAssistantExcerpt = "Plan Mode is awaiting approval."
	if err := e.store.SaveState(meta.ID, state); err != nil {
		return RunResult{}, err
	}
	e.emit(meta.ID, "session.awaiting_input", state.Phase, map[string]any{"reason": "plan_approval"})
	result := RunResult{SessionID: meta.ID, Status: state.Status, FinalText: "Plan Mode is awaiting approval."}
	if err := e.reconcileLinkedQueueJob(meta.ID); err != nil {
		return result, err
	}
	_ = writeSessionSummary(e.store, meta.ID)
	_ = writeLongRunCheckpoint(e.store, meta.ID)
	return result, nil
}

func (e *Engine) awaitingPlanCancelled(ctx context.Context, meta session.SessionMetadata, state session.State, hookManager *hooks.Manager) (RunResult, error) {
	if _, err := hookManager.Trigger(ctx, "session.awaiting_input", sessionHookPayload(meta, session.StatusAwaitingInput)); err != nil {
		return e.fail(ctx, meta, state, err, hookManager)
	}
	state.Status = session.StatusAwaitingInput
	state.Phase = "plan_cancelled"
	state.LastAssistantExcerpt = "Plan Mode cancelled."
	if err := e.store.SaveState(meta.ID, state); err != nil {
		return RunResult{}, err
	}
	e.emit(meta.ID, "session.awaiting_input", state.Phase, map[string]any{"reason": "plan_cancelled"})
	result := RunResult{SessionID: meta.ID, Status: state.Status, FinalText: "Plan Mode cancelled."}
	if err := e.reconcileLinkedQueueJob(meta.ID); err != nil {
		return result, err
	}
	_ = writeSessionSummary(e.store, meta.ID)
	_ = writeLongRunCheckpoint(e.store, meta.ID)
	return result, nil
}

func (e *Engine) complete(ctx context.Context, meta session.SessionMetadata, state session.State, text string, hookManager *hooks.Manager) (RunResult, error) {
	if _, err := hookManager.Trigger(ctx, "session.complete", sessionHookPayload(meta, session.StatusCompleted)); err != nil {
		return e.fail(ctx, meta, state, err, hookManager)
	}
	state.Status = session.StatusCompleted
	state.Phase = "turn_decide"
	state.LastError = ""
	state.IncompleteReason = ""
	state.ProviderAutoResumeCount = 0
	state.LastAssistantExcerpt = truncateText(text, 500)
	if err := e.store.SaveState(meta.ID, state); err != nil {
		return RunResult{}, err
	}
	if err := e.appendEvent(meta.ID, "session.completed", state.Phase, map[string]any{}); err != nil {
		return RunResult{}, fmt.Errorf("record session.completed event: %w", err)
	}
	result := RunResult{SessionID: meta.ID, Status: state.Status, FinalText: text}
	if err := e.reconcileLinkedQueueJob(meta.ID); err != nil {
		return result, err
	}
	_ = writeSessionSummary(e.store, meta.ID)
	_ = writeLongRunCheckpoint(e.store, meta.ID)
	return result, nil
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
	result := RunResult{SessionID: meta.ID, Status: state.Status}
	if err := e.reconcileLinkedQueueJob(meta.ID); err != nil {
		return result, err
	}
	_ = writeSessionSummary(e.store, meta.ID)
	_ = writeLongRunCheckpoint(e.store, meta.ID)
	return result, nil
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
	if appendErr := e.appendEvent(meta.ID, "session.failed", state.Phase, map[string]any{"error": state.LastError}); appendErr != nil {
		return RunResult{}, appendErr
	}
	result := RunResult{SessionID: meta.ID, Status: state.Status, LastError: state.LastError}
	if reconcileErr := e.reconcileLinkedQueueJob(meta.ID); reconcileErr != nil {
		return result, reconcileErr
	}
	_ = writeSessionSummary(e.store, meta.ID)
	_ = writeLongRunCheckpoint(e.store, meta.ID)
	return result, err
}

func (e *Engine) reconcileLinkedQueueJob(sessionID string) error {
	job, ok, err := e.store.ReconcileSessionQueueJob(sessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return &linkedQueueJobReconcileError{sessionID: sessionID, err: err}
	}
	if ok && strings.TrimSpace(job.ParentSessionID) != "" {
		_ = writeSessionSummary(e.store, job.ParentSessionID)
		_ = writeLongRunCheckpoint(e.store, job.ParentSessionID)
	}
	return nil
}

type linkedQueueJobReconcileError struct {
	sessionID string
	err       error
}

func (e *linkedQueueJobReconcileError) Error() string {
	return fmt.Sprintf("reconcile linked queue job for session %s: %v", e.sessionID, e.err)
}

func (e *linkedQueueJobReconcileError) Unwrap() error {
	return e.err
}

func isLinkedQueueJobReconcileError(err error) bool {
	var target *linkedQueueJobReconcileError
	return errors.As(err, &target)
}

func (e *Engine) emit(sessionID, eventType, phase string, data map[string]any) {
	evt := events.New(sessionID, eventType, phase, data)
	_ = e.store.AppendEvent(sessionID, evt)
	e.bus.Publish(evt)
}

func (e *Engine) appendEvent(sessionID, eventType, phase string, data map[string]any) error {
	evt := events.New(sessionID, eventType, phase, data)
	if err := e.store.AppendEvent(sessionID, evt); err != nil {
		return err
	}
	e.bus.Publish(evt)
	return nil
}

func (e *Engine) guardrailsYolo() bool {
	return strings.EqualFold(strings.TrimSpace(e.cfg.Runtime.GuardrailsMode), "yolo")
}

func (e *Engine) shouldAutoResumeProviderError(err error, state session.State) bool {
	if err == nil || !e.cfg.Runtime.ProviderAutoResume.Enabled {
		return false
	}
	maxAttempts := e.cfg.Runtime.ProviderAutoResume.MaxAttempts
	if maxAttempts <= 0 || state.ProviderAutoResumeCount >= maxAttempts {
		return false
	}
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.Class == "upstream_timeout"
}

func (e *Engine) appendProviderAutoResume(sessionID string, err error, attempt int) error {
	data := e.providerAutoResumeEventData(err, attempt)
	return e.appendEvent(sessionID, "provider.auto_resume", "provider_call", data)
}

func (e *Engine) providerAutoResumeEventData(err error, attempt int) map[string]any {
	data := map[string]any{
		"attempt":      attempt,
		"max_attempts": e.cfg.Runtime.ProviderAutoResume.MaxAttempts,
		"error":        err.Error(),
	}
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) {
		data["class"] = httpErr.Class
		if strings.TrimSpace(httpErr.TimeoutKind) != "" {
			data["timeout_kind"] = httpErr.TimeoutKind
		}
		if httpErr.StatusCode > 0 {
			data["status_code"] = httpErr.StatusCode
		}
	}
	return data
}

func (e *Engine) appendProviderRetry(sessionID string, data map[string]any) error {
	return e.appendEvent(sessionID, "provider.retry", "provider_call", data)
}

func (e *Engine) appendProviderCancelled(sessionID, reason string) error {
	return e.appendEvent(sessionID, "provider.cancelled", "provider_call", map[string]any{"reason": reason})
}

func providerAutoResumePrompt(err error, attempt, maxAttempts int) string {
	return fmt.Sprintf("Harness reminder: the provider request timed out while waiting for the model (%s). This is a provider/gateway timeout, not a shell or tool hang; the session is still running and the runtime is auto-resuming (%d/%d). Continue from existing durable evidence. Do not restart broad exploration because of this timeout. If the current task is report finalization, fix the specific finish guard or call finish with current evidence.", err.Error(), attempt, maxAttempts)
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

func (e *Engine) maybeAppendToolLoopReminder(meta session.SessionMetadata, messages []session.Message) ([]session.Message, error) {
	text := toolLoopReminderText(messages)
	if text == "" {
		return messages, nil
	}
	msg, err := e.appendHarnessReminder(meta, "prepare", text, "tool_loop_observation")
	if err != nil {
		return nil, err
	}
	return append(messages, msg), nil
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
	_, _ = e.store.RefreshPendingSteerCount(sessionID)
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
		if err := e.appendEvent(sessionID, "user.message", "control_drain", map[string]any{
			"text":      text,
			"mode":      meta.Mode,
			"source":    "steer",
			"interrupt": requests[i].Interrupt,
		}); err != nil {
			return accepted, err
		}
		if err := e.appendEvent(sessionID, "session.steer.accepted", "control_drain", map[string]any{
			"id":        requests[i].ID,
			"interrupt": requests[i].Interrupt,
		}); err != nil {
			return accepted, err
		}
		if err := appendGoalHistoryForSteer(e.store, sessionID, text, requests[i].Interrupt); err != nil {
			return accepted, err
		}
		if goal, goalErr := e.store.LoadGoal(sessionID); goalErr == nil && goal.GoalID != "" {
			e.emit(sessionID, "goal.updated", "control_drain", map[string]any{
				"goal_id":   goal.GoalID,
				"status":    goal.Status,
				"source":    "steer",
				"interrupt": requests[i].Interrupt,
			})
		}
	}
	if changed {
		if err := e.store.UpdateSteerRequests(sessionID, requests); err != nil {
			return accepted, err
		}
		_, _ = e.store.RefreshPendingSteerCount(sessionID)
		if err := refreshContractForSession(e.store, func(eventType string, data map[string]any) {
			e.emit(sessionID, eventType, "control_drain", data)
		}, meta); err != nil {
			return accepted, err
		}
		return accepted, nil
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
	if err := e.appendEvent(sessionID, "user.message", "control_drain", map[string]any{
		"text":   text,
		"mode":   meta.Mode,
		"source": "background_results",
		"count":  len(pending),
	}); err != nil {
		return 0, err
	}
	if err := e.appendEvent(sessionID, "session.background.accepted", "control_drain", map[string]any{
		"count": len(pending),
	}); err != nil {
		return 0, err
	}
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
	return session.CountOpenSteerRequests(requests)
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
	if strings.TrimSpace(meta.ProviderOptions.APIProvider) != "" {
		data["api_provider"] = meta.ProviderOptions.APIProvider
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
	if strings.TrimSpace(meta.ProviderOptions.APIProvider) != "" {
		data["api_provider"] = meta.ProviderOptions.APIProvider
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
	if strings.TrimSpace(meta.ProviderOptions.ReasoningSummary) != "" {
		data["reasoning_summary"] = meta.ProviderOptions.ReasoningSummary
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
	if meta.ProviderOptions.PromptCache != nil {
		data["prompt_cache"] = *meta.ProviderOptions.PromptCache
	}
	if meta.ProviderOptions.Store != nil {
		data["store"] = *meta.ProviderOptions.Store
	}
	if meta.ProviderOptions.SendMetadata != nil {
		data["send_metadata"] = *meta.ProviderOptions.SendMetadata
	}
	if meta.ProviderOptions.RawSidecar != nil {
		data["raw_sidecar"] = *meta.ProviderOptions.RawSidecar
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
	if meta.ProviderOptions.TimeoutPolicy != nil {
		data["timeout_policy"] = map[string]any{
			"timeout_sec":            meta.ProviderOptions.TimeoutPolicy.TimeoutSec,
			"request_timeout_sec":    meta.ProviderOptions.TimeoutPolicy.RequestTimeoutSec,
			"stream_idle_timeout_ms": meta.ProviderOptions.TimeoutPolicy.StreamIdleTimeoutMS,
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

func shouldPersistAssistantResult(result provider.TurnResult) bool {
	if strings.TrimSpace(result.Text) != "" || strings.TrimSpace(result.Thinking) != "" || len(result.ToolCalls) > 0 {
		return true
	}
	for _, block := range result.ProviderContentBlocks {
		if block.Provider == "openai" && block.Type == "reasoning" && strings.TrimSpace(block.ID) != "" && strings.TrimSpace(block.Data) != "" {
			return true
		}
	}
	return false
}

func stampProviderContentBlocks(meta session.SessionMetadata, blocks []session.ProviderContentBlock) []session.ProviderContentBlock {
	if len(blocks) == 0 {
		return blocks
	}
	out := make([]session.ProviderContentBlock, len(blocks))
	copy(out, blocks)
	for i := range out {
		if strings.TrimSpace(out[i].ProviderProfile) == "" {
			out[i].ProviderProfile = meta.Provider
		}
		if strings.TrimSpace(out[i].APIProvider) == "" {
			out[i].APIProvider = meta.ProviderOptions.APIProvider
		}
		if strings.TrimSpace(out[i].Model) == "" {
			out[i].Model = meta.Model
		}
	}
	return out
}

func providerTurnEventData(result provider.TurnResult) map[string]any {
	usage := map[string]any{
		"input_tokens":  result.Usage.InputTokens,
		"output_tokens": result.Usage.OutputTokens,
	}
	if result.Usage.CacheCreationInputTokens > 0 {
		usage["cache_creation_input_tokens"] = result.Usage.CacheCreationInputTokens
	}
	if result.Usage.CacheReadInputTokens > 0 {
		usage["cache_read_input_tokens"] = result.Usage.CacheReadInputTokens
	}
	data := map[string]any{
		"stop_reason": result.StopReason,
		"usage":       usage,
	}
	if strings.TrimSpace(result.ProviderResponseID) != "" {
		data["provider_response_id"] = result.ProviderResponseID
	}
	if len(result.RawProvider) > 0 {
		data["raw_provider"] = result.RawProvider
	}
	return data
}

func providerStopFailure(stopReason string) (string, string) {
	switch strings.TrimSpace(stopReason) {
	case "max_tokens":
		return "provider_max_tokens", "provider stopped because max output tokens were reached"
	case "blocked":
		return "provider_blocked", "provider stopped because the response was blocked"
	case "error":
		return "provider_stop_error", "provider stopped with an adapter/provider error stop reason"
	default:
		return "", ""
	}
}

func syntheticToolResults(calls []provider.ToolCall, output string) []session.ToolResult {
	if len(calls) == 0 {
		return nil
	}
	results := make([]session.ToolResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, session.ToolResult{
			ToolCallID:    call.ID,
			Name:          call.Name,
			LLMOutput:     output,
			DisplayOutput: output,
			IsError:       true,
		})
	}
	return results
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

func providerRawSidecarEnabled(meta session.SessionMetadata) bool {
	return meta.ProviderOptions.RawSidecar != nil && *meta.ProviderOptions.RawSidecar
}

func providerRawSidecarEnvelope(meta session.SessionMetadata, turn int, result provider.TurnResult) session.ProviderRawSidecar {
	selected := map[string]any{}
	for key, value := range result.RawProvider {
		selected[key] = value
	}
	if len(selected) == 0 {
		selected = nil
	}
	return session.ProviderRawSidecar{
		SchemaVersion:      1,
		Provider:           meta.Provider,
		Model:              meta.Model,
		Turn:               turn,
		Timestamp:          time.Now().UTC().Format(time.RFC3339Nano),
		ProviderResponseID: strings.TrimSpace(result.ProviderResponseID),
		StopReason:         strings.TrimSpace(result.StopReason),
		SelectedRawItems:   selected,
	}
}

func contextLoadedEventData(meta session.SessionMetadata, turn int, stack projectMemoryStack, todo []session.TodoItem, tasks []session.Task) map[string]any {
	taskSummary := taskCounts(tasks)
	data := map[string]any{
		"turn":                   turn,
		"project_memory_present": stack.PresentPaths(),
		"project_memory_missing": stack.MissingPaths(),
		"todo_count":             len(todo),
		"task_count":             len(tasks),
		"ready_task_count":       taskSummary.Ready,
		"blocked_task_count":     taskSummary.Blocked,
		"completed_task_count":   taskSummary.Completed,
		"cancelled_task_count":   taskSummary.Cancelled,
		"done_task_count":        taskSummary.Done,
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

type taskCountSummary struct {
	Ready     int
	Blocked   int
	Completed int
	Cancelled int
	Done      int
}

func taskCounts(tasks []session.Task) taskCountSummary {
	var summary taskCountSummary
	for _, task := range tasks {
		switch {
		case task.Status == "completed":
			summary.Completed++
			summary.Done++
		case task.Status == "cancelled":
			summary.Cancelled++
			summary.Done++
		case task.Status == "pending" && len(task.BlockedBy) == 0:
			summary.Ready++
		case task.Status == "pending" && len(task.BlockedBy) > 0:
			summary.Blocked++
		}
	}
	return summary
}

func prettyJSON(value any) string {
	data, _ := json.MarshalIndent(value, "", "  ")
	return string(data)
}

func countToolCalls(messages []session.Message, toolName string) int {
	count := 0
	for _, msg := range messages {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.Name == toolName {
					count++
				}
			}
		}
	}
	return count
}
