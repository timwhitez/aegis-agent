package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
)

func TestTUIOpenAIResponseLiveAutoOpensApprovalModalPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, record := newLiveOpenAIResponseApprovalService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 104, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("a")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		records, err := svc.ListApprovals(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		return len(pendingApprovalRecords(records)) == 0, nil
	})
	pty.WaitForText(20*time.Second, "Approval approved.")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveDismissedApprovalStaysClosedUntilNewBlockerPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, first := newLiveOpenAIResponseApprovalService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 104, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", first.ApprovalID, "manual step")
	pty.Send("\x1b")
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	time.Sleep(400 * time.Millisecond)
	if strings.Contains(pty.TailOutput(3000), first.ApprovalID) {
		t.Fatalf("expected dismissed approval modal to stay closed until blocker changes\nterminal output:\n%s", pty.Output())
	}
	second, err := svc.RequestApproval(context.Background(), spec.TaskID, "follow-up step", "operator review")
	if err != nil {
		t.Fatalf("request second approval: %v", err)
	}
	pty.WaitForText(20*time.Second, "Approvals", second.ApprovalID, "follow-up step")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveAutoOpensInputModalPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, request := newLiveOpenAIResponseInputService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 104, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Input Request", request.RequestID, "Provide target path")
	pty.SendLine("./demo")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		records, err := svc.ListInputRequests(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		_, ok := pendingInputRecord(records)
		return !ok, nil
	})
	pty.WaitForText(20*time.Second, "Input response recorded.")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLivePickerFilterPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, specs := newLiveOpenAIResponseMultiTaskService(t, live, 3)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute)
	pty.WaitForText(20*time.Second, "Task Picker", specs[0].TaskID, specs[1].TaskID, specs[2].TaskID)
	filterToken := specs[2].TaskID[len(specs[2].TaskID)-4:]
	pty.Send(filterToken)
	pty.WaitForText(20*time.Second, "Filter: "+filterToken, specs[2].TaskID, "demo 3")
	pty.PressEnter()
	pty.WaitForText(20*time.Second, "Chat", specs[2].TaskID, "demo 3")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLivePickerNoMatchAndRecoverPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, specs := newLiveOpenAIResponseMultiTaskService(t, live, 2)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute)
	pty.WaitForText(20*time.Second, "Task Picker", specs[0].TaskID, specs[1].TaskID)
	pty.Send("zzzz")
	pty.WaitForText(20*time.Second, "No tasks match the current filter.")
	for i := 0; i < 4; i++ {
		pty.Send("\x7f")
	}
	pty.WaitForText(20*time.Second, "Filter: <all tasks>", specs[0].TaskID, specs[1].TaskID)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLivePickerAutoRefreshesLateCreatedTaskPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, specs := newLiveOpenAIResponseMultiTaskService(t, live, 1)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute)
	pty.WaitForText(20*time.Second, "Task Picker", specs[0].TaskID, "demo 1")
	late := createLateCodingTask(t, svc, "late live picker task", "surface a late-created durable task in the live picker")
	pty.WaitForText(20*time.Second, "Task Picker", late.TaskID, "late live picker task")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveComposerHistoryRecallPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "history recall live tui", "exercise composer history in a live-configured task")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/review")
	pty.WaitForText(20*time.Second, "Review refreshed.")
	pty.Send("\x10")
	pty.WaitForText(20*time.Second, "> /review")
	pty.Send("\x0e")
	pty.Send("x")
	pty.WaitForText(20*time.Second, "> x")
	if strings.Contains(pty.TailOutput(3000), "/reviewx") {
		t.Fatalf("expected history-next to restore the empty draft before typing\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLivePasteLikeBurstKeepsEnterAsNewlinePTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "paste-like burst live tui", "keep short rapid composer paste bursts inside the textarea instead of submitting")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendBurst("abc\rdef")
	pty.WaitForText(20*time.Second, "abc", "def")
	time.Sleep(400 * time.Millisecond)
	if outputShowsActivePrompt(pty.Output()) {
		t.Fatalf("expected paste-like burst to stay in composer instead of submitting\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveBurstSlashCommandStillSubmitsPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "burst slash command live tui", "keep slash commands submitting immediately even when they arrive as a rapid burst")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendBurst("/review\r")
	pty.WaitForText(20*time.Second, "Review refreshed.")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveCtrlDWithPendingBurstStaysOpenPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "ctrl+d pending burst live tui", "flush pending burst text into the composer before deciding whether ctrl+d may quit")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendBurst("a\x04")
	pty.WaitForText(20*time.Second, "> a")
	pty.Send("\x7f")
	pty.Send("\x04")
	pty.Wait()
}

func TestTUIOpenAIResponseLiveComposerQuestionMarkDoesNotOpenHelpPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "question mark text live tui", "keep question mark typed in the composer as text instead of opening the help modal")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.Send("?")
	pty.WaitForText(20*time.Second, "> ?")
	if strings.Contains(pty.TailOutput(4000), "Esc, Enter, or ? close help") {
		t.Fatalf("expected question mark typed in composer to stay as text instead of opening help\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveComposerApprovalKeyDoesNotOpenApprovalsPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, record := newLiveOpenAIResponseApprovalService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.Send("a")
	pty.WaitForText(20*time.Second, "> a")
	if strings.Contains(pty.TailOutput(4000), "Approvals") {
		t.Fatalf("expected approval hotkey character typed in composer to stay as text instead of reopening approvals\nterminal output:\n%s", pty.Output())
	}
	pty.Send("\x7f")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLivePromptAutoSettleAllowsImmediateFollowUpPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseRepairService(t, live)
	pty := startTUIHarnessSizedWithTimeoutAndEnv(t, svc, 110, 30, 6*time.Minute, []string{"NGEN_TUI_TEST_PROMPT_RETURN_DELAY_MS=1500"}, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 5*time.Minute)
	pty.WaitForText(30*time.Second, "Prompt completed.")
	pty.SendLine("/review")
	pty.WaitForText(20*time.Second, "Review refreshed.")
	time.Sleep(1700 * time.Millisecond)
	view := pty.Output()
	if lastIndexAny(view, []string{"Review refreshed."}) < lastIndexAny(view, []string{"Prompt completed."}) {
		t.Fatalf("expected stale prompt completion callback to avoid overwriting newer review status\nterminal output:\n%s", pty.Output())
	}
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveQueuesFollowUpPromptDuringActiveTurnPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseQueuedPromptService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone live queued follow-up memory note")
	pty.WaitForText(20*time.Second, "Queued follow-up prompt.")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 3*time.Minute)
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		entries, err := svc.Store.ReadMemoryEntries()
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if strings.Contains(entry.Summary, "live queued follow-up memory note") {
				return true, nil
			}
		}
		return false, nil
	})
	pty.Send("\t")
	pty.Send("6")
	pty.WaitForText(30*time.Second, "live queued follow-up memory note")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveQueuesMultipleFollowUpPromptsFIFOPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseQueuedPromptService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone first live queued memory note")
	pty.SendLine("/memory milestone second live queued memory note")
	pty.WaitForText(20*time.Second, "Queued 2 follow-up prompts.")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 3*time.Minute)
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		entries, err := svc.Store.ReadMemoryEntries()
		if err != nil {
			return false, err
		}
		if len(entries) < 2 {
			return false, nil
		}
		lastTwo := entries[len(entries)-2:]
		return strings.Contains(lastTwo[0].Summary, "first live queued memory note") && strings.Contains(lastTwo[1].Summary, "second live queued memory note"), nil
	})
	pty.Send("\t")
	pty.Send("6")
	pty.WaitForText(30*time.Second, "first live queued memory note", "second live queued memory note")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveReviewBeforeRunShowsBlockedPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "review blocked live tui", "show review blocker before verification runs")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/review")
	pty.WaitForText(20*time.Second, "Review refreshed.", "blocked_review")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveReviewRegeneratesMissingHandoffToDonePTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseReviewHandoffRecoveryService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/review")
	pty.WaitForText(20*time.Second, "Review refreshed.", "Done")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 20*time.Second)
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		_, err := os.Stat(filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md"))
		return err == nil, err
	})
	handoff, err := os.ReadFile(filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md"))
	if err != nil {
		t.Fatalf("read regenerated live handoff: %v", err)
	}
	if !strings.Contains(string(handoff), "## Evidence") || !strings.Contains(string(handoff), "## Resume Instructions") {
		t.Fatalf("expected regenerated live handoff to include evidence and resume instructions, got:\n%s", string(handoff))
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveReviewRegeneratesMissingHandoffButKeepsCriteriaBlockedPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseReviewHandoffCriteriaBlockedService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/review")
	pty.WaitForText(20*time.Second, "Review refreshed.", "blocked_review")
	waitForTaskState(t, svc, spec.TaskID, task.StateBlocked, 20*time.Second)
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		_, err := os.Stat(filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md"))
		return err == nil, err
	})
	handoff, err := os.ReadFile(filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md"))
	if err != nil {
		t.Fatalf("read regenerated blocked live handoff: %v", err)
	}
	if !strings.Contains(string(handoff), "README.md mentions `alpha`") || !strings.Contains(string(handoff), "blocked_review") {
		t.Fatalf("expected regenerated blocked live handoff to preserve the open criterion and blocker status, got:\n%s", string(handoff))
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveMemoryTabShowsPromotedEntryPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "memory preview live tui", "show promoted workspace memory in the inspector")
	if _, err := svc.PromoteMemory(context.Background(), spec.TaskID, task.MemoryPromotion{
		Kind:    task.MemoryKindTaskDecision,
		Summary: "Remember reviewer handoff and verification contract.",
	}, task.MemorySourceOperator); err != nil {
		t.Fatalf("promote memory: %v", err)
	}
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.Send("\t")
	pty.Send("6")
	pty.WaitForText(20*time.Second, "Remember reviewer handoff", "verification contract.")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveProjectTabShowsWorkspaceGraphPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "project graph live tui", "show the workspace project graph and task binding in the TUI")
	setProjectLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 104, 72, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.Send("\t")
	pty.Send("7")
	pty.WaitForText(20*time.Second, "Workspace Root:", "Task Binding", projectLongStepPrefix, "Long project review lane")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveBlockersTabShowsPendingApprovalAndInputPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, approval, input := newLiveOpenAIResponseBlockersService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", approval.ApprovalID, "manual step")
	pty.Send("\x1b")
	pty.WaitForText(20*time.Second, "Chat", "APPROVAL_REQUESTED", "INPUT_REQUESTED", "target_path")
	pty.SendLine("/input")
	pty.WaitForText(20*time.Second, "Input Request", input.RequestID, "Provide target path")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTaskPickerSwitchesTasksPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, specs := newLiveOpenAIResponseMultiTaskService(t, live, 2)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, specs[0].TaskID)
	pty.WaitForText(20*time.Second, "Chat", specs[0].TaskID, "demo 1")
	pty.SendLine("/picker")
	pty.WaitForText(20*time.Second, "Task Picker", specs[0].TaskID, specs[1].TaskID)
	filterToken := specs[1].TaskID[len(specs[1].TaskID)-4:]
	pty.Send(filterToken)
	pty.WaitForText(20*time.Second, "Filter: "+filterToken, specs[1].TaskID)
	pty.PressEnter()
	pty.WaitForText(20*time.Second, "Chat", specs[1].TaskID, "demo 2")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTaskPickerRestoresTaskLocalDraftPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, specs := newLiveOpenAIResponseMultiTaskService(t, live, 2)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, specs[0].TaskID)
	pty.WaitForText(20*time.Second, "Chat", specs[0].TaskID, "demo 1")
	pty.Send("parent draft")
	pty.WaitForText(20*time.Second, "> parent draft")
	pty.Send("\t")
	pty.Send("p")
	pty.WaitForText(20*time.Second, "Task Picker", specs[0].TaskID, specs[1].TaskID)
	pty.Send("j")
	pty.PressEnter()
	pty.WaitForText(20*time.Second, "Chat", specs[1].TaskID, "demo 2")
	pty.SendLine("/back")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		view := pty.TailOutput(12000)
		return strings.Contains(view, specs[0].TaskID) && strings.Contains(view, "> parent draft"), nil
	})
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTasksTabOpensWorkerChildAndBackPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, worker := newLiveOpenAIResponseWorkerContinueService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/tasks")
	pty.WaitForText(20*time.Second, "Related Tasks", worker.ChildTaskID, "reviewer")
	pty.PressEnter()
	pty.WaitForText(20*time.Second, "Chat", worker.ChildTaskID, "Parent Task:")
	pty.SendLine("/back")
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTasksTabOpensProjectSiblingAndBackPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, specs := newLiveOpenAIResponseProjectNavigationService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, specs[0].TaskID)
	pty.WaitForText(20*time.Second, "Chat", specs[0].TaskID)
	pty.SendLine("/tasks")
	pty.WaitForText(20*time.Second, "Related Tasks", specs[1].TaskID, projectNavigationSiblingStepID)
	pty.PressEnter()
	pty.WaitForText(20*time.Second, "Chat", specs[1].TaskID, "demo 2")
	pty.SendLine("/back")
	pty.WaitForText(20*time.Second, "Chat", specs[0].TaskID)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTasksTabAutoRefreshesLateCreatedTaskMetadataPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, specs := newLiveOpenAIResponseMultiTaskService(t, live, 1)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, specs[0].TaskID)
	pty.WaitForText(20*time.Second, "Chat", specs[0].TaskID)
	pty.SendLine("/tasks")
	pty.WaitForText(20*time.Second, "Related Tasks", "No related tasks discovered")
	late := createLateCodingTask(t, svc, "late live tasks tab sibling", "surface late-created task metadata in the live tasks tab")
	pty.WaitForText(20*time.Second, "Related Tasks", late.TaskID, "coding", "Explore / Active")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTasksTabRestoresTaskLocalDraftPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, specs := newLiveOpenAIResponseProjectNavigationService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, specs[0].TaskID)
	pty.WaitForText(20*time.Second, "Chat", specs[0].TaskID)
	pty.Send("parent draft")
	pty.WaitForText(20*time.Second, "> parent draft")
	pty.Send("\t")
	pty.Send("\t")
	pty.Send("8")
	pty.WaitForText(20*time.Second, "Related Tasks", specs[1].TaskID, projectNavigationSiblingStepID)
	pty.PressEnter()
	pty.WaitForText(20*time.Second, "Chat", specs[1].TaskID, "demo 2")
	pty.SendLine("/back")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		view := pty.TailOutput(12000)
		return strings.Contains(view, specs[0].TaskID) && strings.Contains(view, "> parent draft"), nil
	})
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTasksTabKeepsSelectedRelatedTaskAcrossLeadingInsertPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, specs := newLiveOpenAIResponseProjectNavigationService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, specs[0].TaskID)
	pty.WaitForText(20*time.Second, "Chat", specs[0].TaskID)
	pty.SendLine("/tasks")
	pty.WaitForText(20*time.Second, "Related Tasks", specs[1].TaskID, projectNavigationSiblingStepID)
	inserted := spawnTrailingWorker(t, svc, specs[0].TaskID, "review inserted before project sibling")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		workers, err := svc.ListWorkers(context.Background(), specs[0].TaskID)
		if err != nil {
			return false, err
		}
		for _, worker := range workers {
			if worker.WorkerID == inserted.WorkerID {
				return true, nil
			}
		}
		return false, nil
	})
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	pty.PressEnter()
	pty.WaitForText(20*time.Second, "Chat", specs[1].TaskID, "Opened related task")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTasksTabKeepsSelectedRelatedTaskAcrossLeadingInsertWithinFortyColsPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, specs := newLiveOpenAIResponseProjectNavigationService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 40, 72, 2*time.Minute, specs[0].TaskID)
	pty.WaitForText(20*time.Second, "Chat", specs[0].TaskID)
	pty.SendLine("/tasks")
	pty.WaitForText(20*time.Second, "Related Tasks", specs[1].TaskID, projectNavigationSiblingStepID)
	inserted := spawnTrailingWorker(t, svc, specs[0].TaskID, "review inserted before project sibling")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		workers, err := svc.ListWorkers(context.Background(), specs[0].TaskID)
		if err != nil {
			return false, err
		}
		for _, worker := range workers {
			if worker.WorkerID == inserted.WorkerID {
				return true, nil
			}
		}
		return false, nil
	})
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	pty.PressEnter()
	pty.WaitForText(20*time.Second, "Chat", specs[1].TaskID, "Opened related task")
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveSlashPickerDuringRunShowsBlockedPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseLongRunService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	pty.SendLine("/picker")
	pty.WaitForText(20*time.Second, taskConsoleDisabledMessage())
	if strings.Contains(pty.TailOutput(4000), "Task Picker") {
		t.Fatalf("expected /picker during active turn to stay on current task instead of opening picker\nterminal output:\n%s", pty.Output())
	}
	if outputShowsActivePrompt(pty.Output()) {
		interruptSimpleModeActiveTurn(t, pty, svc, spec.TaskID)
	}
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveBackCommandBlockedDuringRunPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseLongRunService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	pty.SendLine("/back")
	pty.WaitForText(20*time.Second, taskConsoleDisabledMessage())
	interruptSimpleModeActiveTurn(t, pty, svc, spec.TaskID)
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveInterruptPTYAbortsLongRun(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseLongRunService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	interruptSimpleModeActiveTurn(t, pty, svc, spec.TaskID)
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveSlashExitDuringRunPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseLongRunService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	pty.SendLine("/exit")
	pty.WaitForText(20*time.Second, "Confirm Interrupt", "Interrupt current turn")
	pty.PressEnter()
	waitForTaskState(t, svc, spec.TaskID, task.StateAborted, 2*time.Minute)
	pty.WaitForText(20*time.Second, "Session cancelled.")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveInterruptFromHelpModalPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseLongRunService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	pty.SendLine("/help")
	pty.WaitForText(20*time.Second, "Ctrl+C quit or interrupt active turn", "/exit")
	pty.Send("\x03")
	interruptSimpleModeActiveTurn(t, pty, svc, spec.TaskID)
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveNarrowResizeHelpPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "narrow resize live tui", "exercise narrow TUI layout in a live-configured task")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 60, 22, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "Ctrl+K actions")
	view := pty.TailOutput(3000)
	if strings.Contains(view, "a approvals") {
		t.Fatalf("expected live composer-focused footer to avoid single-key approval hint\nterminal output:\n%s", pty.Output())
	}
	pty.Send("\t")
	pty.WaitForText(20*time.Second, "Enter action", "a approvals")
	pty.Send("?")
	pty.WaitForText(20*time.Second, "Plain text submits to the coding agent", "Task switching and worker management stay in CLI", "or Web management surfaces.")
	pty.Send("?")
	pty.WaitForText(20*time.Second, "Enter action", "a approvals")
	pty.Resize(110, 30)
	pty.Send("\t")
	pty.Send("\t")
	pty.WaitForText(20*time.Second, "Chat", "Ctrl+K actions")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveLongHeaderWrapsWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "LONGTOKENABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ", "verify header wrapping on a narrow TUI")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 36, 22, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat")
	assertMaxLineWidth(t, pty.Output(), 36)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveInlineFlagDisablesAltScreenPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "inline alt-screen live tui", "keep --inline sessions in native scrollback mode without entering alt-screen")
	pty := startTUIHarnessCLIArgsSizedWithTimeoutAndEnv(
		t,
		svc,
		[]string{"tui", "--inline", spec.TaskID},
		96,
		24,
		2*time.Minute,
		nil,
	)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	time.Sleep(250 * time.Millisecond)
	if strings.Contains(pty.RawOutput(), altScreenEnterSeq) {
		t.Fatalf("expected --inline to keep the live TUI out of alt-screen mode\nraw terminal output:\n%q", pty.RawOutput())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveAutoAltScreenFallsBackToInlineInZellijPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "zellij alt-screen live tui", "fall back to inline mode when auto alternate-screen runs under ZELLIJ")
	pty := startTUIHarnessCLIArgsSizedWithTimeoutAndEnv(
		t,
		svc,
		[]string{"tui", spec.TaskID},
		96,
		24,
		2*time.Minute,
		[]string{"ZELLIJ=1"},
	)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	time.Sleep(250 * time.Millisecond)
	if strings.Contains(pty.RawOutput(), altScreenEnterSeq) {
		t.Fatalf("expected auto mode under ZELLIJ to stay inline instead of entering alt-screen\nraw terminal output:\n%q", pty.RawOutput())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveAlwaysAltScreenUsesAltScreenPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "always alt-screen live tui", "enter alternate-screen mode when the workspace config explicitly requires it")
	setTUIAltScreenMode(t, svc, "always")
	pty := startTUIHarnessCLIArgsSizedWithTimeoutAndEnv(
		t,
		svc,
		[]string{"tui", spec.TaskID},
		96,
		24,
		2*time.Minute,
		nil,
	)
	pty.WaitForRawText(20*time.Second, altScreenEnterSeq)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.ExitAndWait()
	if !strings.Contains(pty.RawOutput(), altScreenExitSeq) {
		t.Fatalf("expected live alt-screen session to restore the normal screen on exit\nraw terminal output:\n%q", pty.RawOutput())
	}
}

func TestTUIOpenAIResponseLiveTranscriptKeepsSessionMessagesWithBoundedEventTailPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "transcript bounded tail live tui", "keep session messages visible while bounding event tail entries")
	setTUIEventLimit(t, svc, 1)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	session := waitForTaskSession(t, svc, spec.TaskID, 20*time.Second)
	appendTranscriptFixture(t, svc, spec.TaskID, session.SessionID)

	pty.WaitForText(30*time.Second,
		transcriptMessageAlpha,
		transcriptMessageBeta,
		transcriptMessageGamma,
		transcriptEventNewest,
	)
	if strings.Contains(pty.TailOutput(5000), transcriptEventStale) {
		t.Fatalf("expected stale event to stay outside the bounded transcript tail\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTranscriptShowsLatestEventRefsPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "transcript event refs live tui", "show newest event refs while bounding stale event refs")
	setTUIEventLimit(t, svc, 1)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	session := waitForTaskSession(t, svc, spec.TaskID, 20*time.Second)
	appendTranscriptFixture(t, svc, spec.TaskID, session.SessionID)

	pty.WaitForText(30*time.Second, transcriptEventNewest, transcriptRefTrace, transcriptRefStatus)
	if strings.Contains(pty.TailOutput(5000), transcriptRefStale) {
		t.Fatalf("expected bounded-out event refs to stay outside the transcript tail\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTranscriptScrollUpStaysPinnedAcrossRefreshPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "transcript scroll live tui", "keep transcript browsing stable while refreshes append new lines")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 18, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	session := waitForTaskSession(t, svc, spec.TaskID, 20*time.Second)
	appendTranscriptScrollFixture(t, svc, spec.TaskID, session.SessionID, transcriptScrollFixtureCount)
	pty.WaitForText(20*time.Second, transcriptScrollBottomAnchor)

	pty.Send("\t")
	pty.Send("kkkkkkkkkkkkkkkkkk")
	pty.WaitForText(20*time.Second, transcriptScrollOlderAnchor)

	appendTranscriptScrollNewest(t, svc, spec.TaskID, session.SessionID)
	pty.Send("\x0c")
	time.Sleep(700 * time.Millisecond)
	if strings.Contains(pty.TailOutput(4000), transcriptScrollNewest) {
		t.Fatalf("expected scrolled-up transcript to stay pinned instead of auto-following new content\nterminal output:\n%s", pty.Output())
	}

	pty.Send(strings.Repeat("j", 80))
	pty.WaitForText(20*time.Second, transcriptScrollNewest)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveTranscriptAtBottomAutoFollowsRefreshPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "transcript auto-follow live tui", "keep transcript tail following new lines while the viewport stays at bottom")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 18, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	session := waitForTaskSession(t, svc, spec.TaskID, 20*time.Second)
	appendTranscriptScrollFixture(t, svc, spec.TaskID, session.SessionID, transcriptScrollFixtureCount)
	pty.WaitForText(20*time.Second, transcriptScrollBottomAnchor)

	appendTranscriptScrollNewest(t, svc, spec.TaskID, session.SessionID)
	pty.WaitForText(20*time.Second, transcriptScrollNewest)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveLongTranscriptSummaryWrapsWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "transcript long summary live tui", "wrap long transcript summary tokens within a narrow tui width")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 40, 28, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat")
	appendTranscriptLongSummaryEvent(t, svc, spec.TaskID)
	pty.WaitForText(20*time.Second, transcriptLongSummaryPrefix, transcriptLongSummaryTail)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveLongTranscriptRefWrapsWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "transcript long ref live tui", "wrap long transcript ref tokens within a narrow tui width")
	pty := startTUIHarnessSizedWithTimeout(t, svc, 40, 28, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat")
	appendTranscriptLongRefEvent(t, svc, spec.TaskID)
	pty.WaitForText(20*time.Second, transcriptLongRefPrefix, transcriptLongRefSuffix)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveOverviewInspectorWrapsLongRefsWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "overview inspector wrap live tui", "wrap long overview refs within a narrow inspector width")
	setOverviewLongRefs(t, svc, spec.TaskID)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 48, 64, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat")
	pty.WaitForText(20*time.Second, overviewLongVerificationRefPrefix, overviewLongVerificationRefSuffix, overviewLongStatusDetailSuffix)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveOverviewInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "overview scroll live tui", "keep the overview inspector viewport pinned while refreshing after new continuity lines arrive")
	setOverviewScrollFixture(t, svc, spec.TaskID, overviewScrollFixtureCount)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 96, 18, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat")
	pty.Send("\t")
	pty.Send("\t")
	pty.WaitForText(20*time.Second, "Task Summary")
	for i := 0; i < 20; i++ {
		if !strings.Contains(pty.TailOutput(2200), "Task Summary") {
			break
		}
		pty.Send("j")
	}
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(2200)
	if strings.Contains(view, "Task Summary") {
		t.Fatalf("expected overview inspector to scroll away from the top before refresh\nterminal output:\n%s", pty.Output())
	}
	appendOverviewScrollNewest(t, svc, spec.TaskID)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(2200)
	if strings.Contains(view, "Task Summary") {
		t.Fatalf("expected scrolled overview inspector to stay away from the top after refresh\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, overviewScrollNewest) {
		t.Fatalf("expected scrolled overview inspector to avoid jumping to the newest continuity line after refresh\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveWorkersInspectorWrapsLongObjectiveWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, worker := newLiveOpenAIResponseWorkerContinueService(t, live)
	setWorkerLongObjective(t, svc, spec.TaskID, worker.WorkerID)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 48, 52, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat")
	pty.Send("\t")
	pty.Send("\t")
	pty.WaitForText(20*time.Second, "Enter action")
	pty.Send("4")
	pty.WaitForText(20*time.Second, "Workers", workerLongObjectivePrefix, "findings/report", ".jsonl", "important blockers")
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLivePlanInspectorWrapsLongMetadataWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "plan wrap live tui", "wrap long plan inspector metadata inside a narrow inspector width")
	setPlanLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 48, 64, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.Send("\t")
	pty.Send("2")
	pty.WaitForText(20*time.Second, "Current Execution:", "Primary Criterion:", planLongCriterionPrefix, planLongCriterionSuffix)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveCriteriaInspectorWrapsLongMetadataWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "criteria wrap live tui", "wrap long criteria inspector metadata inside a narrow inspector width")
	setCriteriaLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 48, 88, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.Send("\t")
	pty.Send("3")
	pty.WaitForText(20*time.Second, "Summary:", criteriaLongSummaryPrefix, criteriaLongSummarySuffix, "Current:", criteriaLongCurrentPrefix, criteriaLongCurrentSuffix, criteriaLongStatementSuffix, "Latest criteria summary")
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLivePlanInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "plan scroll live tui", "keep the plan inspector viewport pinned while refreshing after new steps arrive")
	setPlanScrollFixture(t, svc, spec.TaskID, planScrollFixtureCount)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 96, 18, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat")
	pty.Send("\t")
	pty.Send("\t")
	pty.Send("2")
	pty.WaitForText(20*time.Second, "Current Execution:")
	for i := 0; i < 20; i++ {
		if !strings.Contains(pty.TailOutput(2200), "Current Execution:") {
			break
		}
		pty.Send("j")
	}
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(2200)
	if strings.Contains(view, "Current Execution:") {
		t.Fatalf("expected plan inspector to scroll away from the top metadata before refresh\nterminal output:\n%s", pty.Output())
	}
	appendPlanScrollNewest(t, svc, spec.TaskID)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(2200)
	if strings.Contains(view, "Revision: 0") {
		t.Fatalf("expected scrolled plan inspector to stay away from the top after refresh\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, planScrollNewest) {
		t.Fatalf("expected scrolled plan inspector to avoid jumping to the newest step after refresh\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveCriteriaInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "criteria scroll live tui", "keep the criteria inspector viewport pinned while refreshing after new criteria arrive")
	setCriteriaScrollFixture(t, svc, spec.TaskID, criteriaScrollFixtureCount)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 96, 18, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat")
	pty.Send("\t")
	pty.Send("\t")
	pty.Send("3")
	pty.WaitForText(20*time.Second, "Summary:")
	for i := 0; i < 24; i++ {
		if !strings.Contains(pty.TailOutput(2200), "Summary:") {
			break
		}
		pty.Send("j")
	}
	waitForCondition(t, 4*time.Second, func() (bool, error) {
		pty.Send("\x0c")
		time.Sleep(200 * time.Millisecond)
		return !strings.Contains(pty.TailOutput(1800), "Summary:"), nil
	})
	view := pty.TailOutput(1800)
	if strings.Contains(view, "Summary:") {
		t.Fatalf("expected criteria inspector to scroll away from the top metadata before refresh\nterminal output:\n%s", pty.Output())
	}
	appendCriteriaScrollNewest(t, svc, spec.TaskID)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(1800)
	if strings.Contains(view, "Summary:") {
		t.Fatalf("expected scrolled criteria inspector to stay away from the top after refresh\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, criteriaScrollNewest) {
		t.Fatalf("expected scrolled criteria inspector to avoid jumping to the newest criterion after refresh\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveBlockersInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "blockers scroll live tui", "keep the blockers inspector viewport pinned while refreshing after new watch detail arrives")
	setBlockersScrollFixture(t, svc, spec.TaskID, blockersScrollFixtureCount)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 96, 18, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Scope: blockers inspector line 01")
	pty.Send("\x1b")
	pty.WaitForText(20*time.Second, "Chat")
	pty.Send("\t")
	pty.Send("\t")
	pty.Send("5")
	pty.WaitForText(20*time.Second, "Task Approvals")
	for i := 0; i < 20; i++ {
		if !strings.Contains(pty.TailOutput(2200), "Task Approvals") {
			break
		}
		pty.Send("j")
	}
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(2200)
	if strings.Contains(view, "Task Approvals") {
		t.Fatalf("expected blockers inspector to scroll away from the top before refresh\nterminal output:\n%s", pty.Output())
	}
	appendBlockersScrollNewest(t, svc, spec.TaskID)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(2200)
	if strings.Contains(view, "Task Approvals") {
		t.Fatalf("expected scrolled blockers inspector to stay away from the top after refresh\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, blockersScrollNewest) {
		t.Fatalf("expected scrolled blockers inspector to avoid jumping to the newest watch detail after refresh\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveMemoryInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "memory scroll live tui", "keep the memory inspector viewport pinned while refreshing after new memory lines arrive")
	setMemoryScrollFixture(t, svc, memoryScrollFixtureCount)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 96, 18, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat")
	pty.Send("\t")
	pty.Send("\t")
	pty.WaitForText(20*time.Second, "Enter action")
	pty.Send("6")
	pty.WaitForText(20*time.Second, "Recent Memory Entries")
	for i := 0; i < 20; i++ {
		if !strings.Contains(pty.TailOutput(2200), "Recent Memory Entries") {
			break
		}
		pty.Send("j")
	}
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view := pty.TailOutput(1800)
	if strings.Contains(view, "Recent Memory Entries") {
		t.Fatalf("expected memory inspector to scroll away from the top before refresh\nterminal output:\n%s", pty.Output())
	}
	appendMemoryScrollNewest(t, svc)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(1800)
	if strings.Contains(view, "Recent Memory Entries") {
		t.Fatalf("expected scrolled memory inspector to stay away from the top after refresh\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, memoryScrollNewest) {
		t.Fatalf("expected scrolled memory inspector to avoid jumping to the newest memory line after refresh\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveProjectInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "project scroll live tui", "keep the project inspector viewport pinned while refreshing after new project steps arrive")
	setProjectScrollFixture(t, svc, projectScrollFixtureCount)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 96, 30, 2*time.Minute, spec.TaskID)
	waitForTUITaskReady(t, pty, 20*time.Second, spec.TaskID)
	pty.Send("\t")
	pty.Send("\t")
	pty.Send("7")
	pty.WaitForText(20*time.Second, projectScrollVisibleAnchor)
	for i := 0; i < 24; i++ {
		if !strings.Contains(pty.TailOutput(2400), projectScrollVisibleAnchor) {
			break
		}
		pty.Send("j")
	}
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(2400)
	if strings.Contains(view, projectScrollVisibleAnchor) {
		t.Fatalf("expected project inspector to scroll away from the older anchor before refresh\nterminal output:\n%s", pty.Output())
	}
	appendProjectScrollNewest(t, svc)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(2400)
	if strings.Contains(view, projectScrollVisibleAnchor) {
		t.Fatalf("expected scrolled project inspector to stay away from the older anchor after refresh\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, projectScrollNewest) {
		t.Fatalf("expected scrolled project inspector to avoid jumping to the newest step after refresh\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveBlockersInspectorWrapsLongEntriesWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "blockers wrap live tui", "wrap long blockers tab entries inside a narrow inspector width")
	approval, input := requestLongBlockers(t, svc, spec.TaskID)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 48, 52, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", approval.ApprovalID)
	pty.Send("\x1b")
	pty.WaitForText(20*time.Second, "Chat", "APPROVAL_REQUESTED", approvalLongScopePrefix, approvalLongScopeSuffix, "INPUT_REQUESTED")
	pty.SendLine("/input")
	pty.WaitForText(20*time.Second, "Input Request", input.RequestID, inputLongPromptPrefix, inputLongPromptSuffix)
	if strings.Contains(pty.TailOutput(5000), "Owned Child Approvals") {
		t.Fatalf("expected local approval to stay out of the owned approvals section\nterminal output:\n%s", pty.Output())
	}
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func interruptSimpleModeActiveTurn(t *testing.T, pty *ptyHarness, svc *ngenrt.Service, taskID string) {
	t.Helper()
	pty.Send("\x03")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		view := pty.Output()
		return strings.Contains(view, "Interrupt requested.") || strings.Contains(view, "Session cancelled."), nil
	})
	waitForTaskState(t, svc, taskID, task.StateAborted, 2*time.Minute)
	pty.WaitForText(20*time.Second, "Session cancelled.")
}

func TestTUIOpenAIResponseLiveMemoryInspectorWrapsLongEntryWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "memory wrap live tui", "wrap long workspace memory entries inside a narrow inspector width")
	promoteLongMemory(t, svc, spec.TaskID)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 48, 64, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.Send("\t")
	pty.Send("6")
	pty.WaitForText(20*time.Second, memoryLongSummaryPrefix, memoryLongSummarySuffix)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveProjectInspectorWrapsLongEntriesWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "project wrap live tui", "wrap long project graph entries inside a narrow inspector width")
	setProjectLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 48, 72, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.Send("\t")
	pty.Send("7")
	pty.WaitForText(20*time.Second, "Workspace Root:", projectLongStepPrefix, projectLongBranchPrefix)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveApprovalModalWrapsLongScopeAndReasonWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "approval wrap live tui", "wrap long approval scope and reason inside a narrow modal")
	record := requestLongApproval(t, svc, spec.TaskID)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 48, 36, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", record.ApprovalID, approvalLongScopePrefix, approvalLongScopeSuffix, approvalLongReasonSuffix)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveOwnedApprovalModalShowsChildStateAndBlockedReasonPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, worker, record := newLiveOpenAIResponseOwnedApprovalContinueService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 60, 36, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", record.ApprovalID, worker.WorkerID, "Child State: blocked", "Blocked: blocked_policy")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveInputModalWrapsLongPromptWithinNarrowPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "input wrap live tui", "wrap long input prompt text inside a narrow modal")
	request := requestLongInput(t, svc, spec.TaskID)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 48, 36, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Input Request", request.RequestID, inputLongPromptPrefix, inputLongPromptSuffix)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLivePlanTabAfterRunPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseRepairService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 3*time.Minute)
	pty.WaitForText(30*time.Second, "Done")
	pty.Send("\t")
	pty.Send("2")
	pty.WaitForText(20*time.Second, "Current Execution:", "Current System:")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveCriteriaTabAfterRunPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseRepairService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 3*time.Minute)
	pty.WaitForText(30*time.Second, "Done")
	pty.Send("\t")
	pty.Send("3")
	pty.WaitForText(20*time.Second, "Met/Open:", "go test passes")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveWorkerTabShowsReviewerAfterSpawnPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseWorkerReviewService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 3*time.Minute)
	pty.WaitForText(30*time.Second, "Done")

	pty.FocusComposer()
	pty.SendLine("/worker_spawn reviewer review the current parent output for correctness")
	var worker task.WorkerContract
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		workers, err := svc.ListWorkers(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		if len(workers) != 1 {
			return false, nil
		}
		worker = workers[0]
		return true, nil
	})
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(45*time.Second, "Workers", worker.WorkerID, "reviewer")
	waitForCondition(t, 45*time.Second, func() (bool, error) {
		return !outputShowsActivePrompt(pty.Output()), nil
	})
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveWorkerContinuePTYCompletesReviewerChild(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, worker := newLiveOpenAIResponseWorkerContinueService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(20*time.Second, "Workers", worker.WorkerID, "reviewer")
	pty.Send("\t")
	pty.PressEnter()
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		result, err := svc.Store.LoadWorkerResult(spec.TaskID, worker.WorkerID)
		if err != nil {
			return false, err
		}
		return result.CompletionStatus == "accepted" && result.ReviewStatus == "clear" && result.VerificationStatus == "passed", nil
	})
	pty.WaitForText(45*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveOwnedApprovalApproveThenContinueWorkerPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, worker, record := newLiveOpenAIResponseOwnedApprovalContinueService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("a")
	waitForCondition(t, 45*time.Second, func() (bool, error) {
		approvals, err := svc.ListOwnedApprovals(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		workers, err := svc.ListWorkers(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		if len(workers) != 1 {
			return false, nil
		}
		return len(pendingApprovalRecords(approvals)) == 0 && workers[0].ParentActionType == "continue_child", nil
	})
	pty.WaitForText(20*time.Second, "Approval approved.")
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(45*time.Second, "Workers", worker.WorkerID, "continue_child")
	pty.Send("\t")
	pty.PressEnter()
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		result, err := svc.Store.LoadWorkerResult(spec.TaskID, worker.WorkerID)
		if err != nil {
			return false, err
		}
		return result.CompletionStatus == "accepted" && result.ReviewStatus == "clear" && result.VerificationStatus == "passed", nil
	})
	pty.WaitForText(45*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveWorkersSelectionStaysOnActiveWorkerAcrossRefreshPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, first, second, record := newLiveOpenAIResponseWorkerSelectionActiveService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	waitForTUITaskReady(t, pty, 20*time.Second, spec.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(45*time.Second, "Workers", first.WorkerID, second.WorkerID)
	pty.Send("\t")
	pty.Send("j")
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected second worker to be selected before refresh\nterminal output:\n%s", pty.Output())
	}
	spawnTrailingWorker(t, svc, spec.TaskID, "review trailing parent docs")
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected refresh to keep second active worker selected\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(spec.TaskID, first.WorkerID)
		if err != nil {
			return false, err
		}
		secondResult, err := svc.Store.LoadWorkerResult(spec.TaskID, second.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			secondResult.CompletionStatus == "accepted" &&
			secondResult.ReviewStatus == "clear" &&
			secondResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(45*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveWorkersSelectionStaysOnActiveWorkerAcrossLeadingInsertPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, first, second, record := newLiveOpenAIResponseWorkerSelectionActiveService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	waitForTUITaskReady(t, pty, 20*time.Second, spec.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(45*time.Second, "Workers", first.WorkerID, second.WorkerID)
	pty.Send("\t")
	pty.Send("j")
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected second worker to be selected before refresh\nterminal output:\n%s", pty.Output())
	}
	inserted := spawnLeadingWorker(t, svc, spec.TaskID, "WORKER-0000-leading", "review inserted before selected worker")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		workers, err := svc.ListWorkers(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		for _, worker := range workers {
			if worker.WorkerID == inserted.WorkerID {
				return true, nil
			}
		}
		return false, nil
	})
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID, "> " + inserted.WorkerID}) {
		t.Fatalf("expected refresh to keep second active worker selected across leading insert\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		secondResult, err := svc.Store.LoadWorkerResult(spec.TaskID, second.WorkerID)
		if err != nil {
			return false, err
		}
		return secondResult.CompletionStatus == "accepted" &&
			secondResult.ReviewStatus == "clear" &&
			secondResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(45*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveWorkersSelectionStaysOnActiveWorkerAcrossLeadingInsertWithinFortyColsPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, first, second, record := newLiveOpenAIResponseWorkerSelectionActiveService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 40, 60, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	waitForTUITaskReady(t, pty, 20*time.Second, spec.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(45*time.Second, "Workers", first.WorkerID, second.WorkerID)
	pty.Send("\t")
	pty.Send("j")
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected second worker to be selected before refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	inserted := spawnLeadingWorker(t, svc, spec.TaskID, "WORKER-0000-leading", "review inserted before selected worker")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		workers, err := svc.ListWorkers(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		for _, worker := range workers {
			if worker.WorkerID == inserted.WorkerID {
				return true, nil
			}
		}
		return false, nil
	})
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID, "> " + inserted.WorkerID}) {
		t.Fatalf("expected refresh to keep second active worker selected across leading insert in 40 cols\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		secondResult, err := svc.Store.LoadWorkerResult(spec.TaskID, second.WorkerID)
		if err != nil {
			return false, err
		}
		return secondResult.CompletionStatus == "accepted" &&
			secondResult.ReviewStatus == "clear" &&
			secondResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(45*time.Second, "Worker continued.")
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveWorkersSelectionStaysOnContinueChildAcrossRefreshPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, first, second, record := newLiveOpenAIResponseWorkerSelectionContinueService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	waitForTUITaskReady(t, pty, 20*time.Second, spec.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(45*time.Second, "Workers", first.WorkerID, second.WorkerID)
	pty.Send("\t")
	pty.Send("j")
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected second continue_child worker to be selected before refresh\nterminal output:\n%s", pty.Output())
	}
	spawnTrailingWorker(t, svc, spec.TaskID, "review trailing parent docs")
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected refresh to keep second continue_child worker selected\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(spec.TaskID, first.WorkerID)
		if err != nil {
			return false, err
		}
		secondResult, err := svc.Store.LoadWorkerResult(spec.TaskID, second.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			secondResult.CompletionStatus == "accepted" &&
			secondResult.ReviewStatus == "clear" &&
			secondResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(45*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveWorkersLongListAutoScrollsActiveSelectionPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, workers := newLiveOpenAIResponseLongWorkerListService(t, live, workersLongListCount)
	target := workers[workersLongListTargetIndex]
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 18, 4*time.Minute, spec.TaskID)
	waitForTUITaskReady(t, pty, 20*time.Second, spec.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(45*time.Second, "Workers", workers[0].WorkerID)
	pty.Send("\t")
	pty.Send(strings.Repeat("j", workersLongListTargetIndex))
	time.Sleep(300 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + target.WorkerID}) < 0 {
		t.Fatalf("expected selected long-list worker to be visible after navigation\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(spec.TaskID, workers[0].WorkerID)
		if err != nil {
			return false, err
		}
		targetResult, err := svc.Store.LoadWorkerResult(spec.TaskID, target.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			targetResult.CompletionStatus == "accepted" &&
			targetResult.ReviewStatus == "clear" &&
			targetResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(45*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveWorkersLongListAutoScrollsActiveSelectionWithinFortyColsPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, workers := newLiveOpenAIResponseLongWorkerListService(t, live, workersLongListCount)
	target := workers[workersLongListTargetIndex]
	pty := startTUIHarnessSizedWithTimeout(t, svc, 40, 30, 4*time.Minute, spec.TaskID)
	waitForTUITaskReady(t, pty, 20*time.Second, spec.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(45*time.Second, "Workers", workers[0].WorkerID)
	pty.Send("\t")
	pty.Send(strings.Repeat("j", workersLongListTargetIndex))
	time.Sleep(300 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + target.WorkerID}) < 0 {
		t.Fatalf("expected selected long-list worker to be visible after navigation in 40 cols\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(spec.TaskID, workers[0].WorkerID)
		if err != nil {
			return false, err
		}
		targetResult, err := svc.Store.LoadWorkerResult(spec.TaskID, target.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			targetResult.CompletionStatus == "accepted" &&
			targetResult.ReviewStatus == "clear" &&
			targetResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(45*time.Second, "Worker continued.")
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveWorkersLongListAutoScrollsContinueChildSelectionPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, workers := newLiveOpenAIResponseLongWorkerListService(t, live, workersLongListCount)
	target := workers[workersLongListTargetIndex]
	setWorkerContinueChildReady(t, svc, spec.TaskID, target)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 18, 4*time.Minute, spec.TaskID)
	waitForTUITaskReady(t, pty, 20*time.Second, spec.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(45*time.Second, "Workers", workers[0].WorkerID)
	pty.Send("\t")
	pty.Send(strings.Repeat("j", workersLongListTargetIndex))
	time.Sleep(300 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + target.WorkerID}) < 0 {
		t.Fatalf("expected selected continue_child long-list worker to be visible after navigation\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(spec.TaskID, workers[0].WorkerID)
		if err != nil {
			return false, err
		}
		targetResult, err := svc.Store.LoadWorkerResult(spec.TaskID, target.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			targetResult.CompletionStatus == "accepted" &&
			targetResult.ReviewStatus == "clear" &&
			targetResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(45*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveApprovalApprovePTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, record := newLiveOpenAIResponseApprovalService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 104, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("a")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		records, err := svc.ListApprovals(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		return len(pendingApprovalRecords(records)) == 0, nil
	})
	pty.WaitForText(20*time.Second, "Approval approved.")
	pty.ExitAndWait()
}

func TestTUIOpenAIResponseLiveInputRespondPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec, request := newLiveOpenAIResponseInputService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 104, 30, 2*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Input Request", request.RequestID, "Provide target path")
	pty.SendLine("./demo")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		records, err := svc.ListInputRequests(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		_, ok := pendingInputRecord(records)
		return !ok, nil
	})
	pty.WaitForText(20*time.Second, "Input response recorded.")
	pty.ExitAndWait()
}

func newLiveOpenAIResponseApprovalService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec, task.ApprovalRecord) {
	t.Helper()
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "approval modal live tui", "show and resolve a pending approval in the TUI")
	record, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	return svc, spec, record
}

func newLiveOpenAIResponseInputService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec, task.InputRequestRecord) {
	t.Helper()
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "input modal live tui", "show and resolve a pending input request in the TUI")
	record, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true)
	if err != nil {
		t.Fatalf("request input: %v", err)
	}
	return svc, spec, record
}

func newLiveOpenAIResponseBlockersService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec, task.ApprovalRecord, task.InputRequestRecord) {
	t.Helper()
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "blockers tab live tui", "show pending approvals and input requests together in the blockers tab")
	approval, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	input, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true)
	if err != nil {
		t.Fatalf("request input: %v", err)
	}
	return svc, spec, approval, input
}

func newLiveOpenAIResponseWorkerContinueService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec, task.WorkerContract) {
	t.Helper()
	dir := liveScenarioWorkspace(t)
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")
	svc := newLiveConfiguredOpenAIResponseService(t, live, dir, nil)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "worker continue live tui",
		Objective: "continue a reviewer child from the workers tab",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "reviewer child produces a compiled result"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create worker continue task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), spec.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn reviewer worker: %v", err)
	}
	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# reviewer draft\n")
	return svc, spec, worker
}

func newLiveOpenAIResponseOwnedApprovalContinueService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec, task.WorkerContract, task.ApprovalRecord) {
	t.Helper()
	svc, spec, worker := newLiveOpenAIResponseWorkerContinueService(t, live)
	record, err := svc.RequestApproval(context.Background(), worker.ChildTaskID, "manual step", "worker asks parent")
	if err != nil {
		t.Fatalf("request worker approval: %v", err)
	}
	return svc, spec, worker, record
}

func newLiveOpenAIResponseWorkerSelectionActiveService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec, task.WorkerContract, task.WorkerContract, task.ApprovalRecord) {
	t.Helper()
	svc, spec, first := newLiveOpenAIResponseWorkerContinueService(t, live)
	record, err := svc.RequestApproval(context.Background(), first.ChildTaskID, "manual step", "first worker asks parent")
	if err != nil {
		t.Fatalf("request first worker approval: %v", err)
	}
	second := spawnTrailingWorker(t, svc, spec.TaskID, "review follow-up parent docs")
	return svc, spec, first, second, record
}

func newLiveOpenAIResponseWorkerSelectionContinueService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec, task.WorkerContract, task.WorkerContract, task.ApprovalRecord) {
	t.Helper()
	svc, spec, first, second, record := newLiveOpenAIResponseWorkerSelectionActiveService(t, live)
	secondApproval, err := svc.RequestApproval(context.Background(), second.ChildTaskID, "manual step", "second worker asks parent")
	if err != nil {
		t.Fatalf("request second worker approval: %v", err)
	}
	if _, err := svc.DecideApproval(context.Background(), spec.TaskID, secondApproval.ApprovalID, "approved"); err != nil {
		t.Fatalf("approve second worker owned approval: %v", err)
	}
	return svc, spec, first, second, record
}

func newLiveOpenAIResponseLongWorkerListService(t *testing.T, live liveProviderEnv, count int) (*ngenrt.Service, task.Spec, []task.WorkerContract) {
	t.Helper()
	if count < 1 {
		t.Fatalf("count must be >= 1")
	}
	dir := liveScenarioWorkspace(t)
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")
	svc := newLiveConfiguredOpenAIResponseService(t, live, dir, func(cfg *task.Config) {
		if cfg.Subagents.MaxWorkersPerTask < count {
			cfg.Subagents.MaxWorkersPerTask = count
		}
	})
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "live long worker list",
		Objective: "keep long worker selections visible while continuing the intended child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "selected worker can be continued from the long list"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create long worker list task: %v", err)
	}
	workers := make([]task.WorkerContract, 0, count)
	for i := 0; i < count; i++ {
		worker := spawnTrailingWorker(t, svc, spec.TaskID, fmt.Sprintf("review parent docs chunk %02d", i+1))
		workers = append(workers, worker)
	}
	return svc, spec, workers
}

func newLiveOpenAIResponsePassiveCodingService(t *testing.T, live liveProviderEnv, title, objective string) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := liveScenarioWorkspace(t)
	writeLiveCalcWorkspace(t, dir, "return a + b")
	return newLiveOpenAIResponseService(t, dir, live, func(cfg *task.Config) task.TaskFile {
		cfg.Verification.CodingCommands = [][]string{{"go", "test", "./..."}}
		return task.TaskFile{
			Kind:      task.KindCoding,
			Title:     title,
			Objective: objective,
			SuccessCriteria: []task.SuccessCriterion{
				{ID: "SC-001", Statement: "go test passes"},
			},
			WorkspaceRoot: dir,
		}
	})
}

func newLiveOpenAIResponseReviewHandoffRecoveryService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec) {
	t.Helper()
	svc, spec := newLiveOpenAIResponsePassiveCodingService(t, live, "handoff recovery live tui", "regenerate a missing handoff from existing verification and criteria truth")
	snapshot, _, err := svc.Run(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("run live handoff recovery task: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done snapshot before deleting live handoff, got %+v", snapshot)
	}
	handoffPath := filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md")
	if err := os.Remove(handoffPath); err != nil {
		t.Fatalf("remove live handoff: %v", err)
	}
	return svc, spec
}

func newLiveOpenAIResponseReviewHandoffCriteriaBlockedService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := liveScenarioWorkspace(t)
	writeFile(t, filepath.Join(dir, "README.md"), "# docs gate\n")
	svc := newLiveConfiguredOpenAIResponseService(t, live, dir, nil)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "blocked handoff live tui",
		Objective: "review docs",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "README.md mentions `alpha`"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create blocked live handoff task: %v", err)
	}
	snapshot, _, err := svc.Run(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("run blocked live handoff task: %v", err)
	}
	if snapshot.State != task.StateBlocked || snapshot.StatusReasonCode != "blocked_review" {
		t.Fatalf("expected blocked_review snapshot before deleting live handoff, got %+v", snapshot)
	}
	handoffPath := filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md")
	if err := os.Remove(handoffPath); err != nil {
		t.Fatalf("remove blocked live handoff: %v", err)
	}
	return svc, spec
}

func newLiveOpenAIResponseLongRunService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := liveScenarioWorkspace(t)
	writeLiveCalcWorkspace(t, dir, "return a + b")
	svc := newLiveConfiguredOpenAIResponseService(t, live, dir, func(cfg *task.Config) {
		cfg.Verification.CodingCommands = [][]string{{"bash", "-lc", "sleep 10"}}
		cfg.Verification.CodingTimeoutSeconds = 20
	})
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "interrupt live tui",
		Objective: "do not edit files; simply validate the workspace so the interrupt path can be exercised",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "verification passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create interrupt task: %v", err)
	}
	return svc, spec
}

func newLiveOpenAIResponseQueuedPromptService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := liveScenarioWorkspace(t)
	writeLiveCalcWorkspace(t, dir, "return a + b")
	writeFile(t, filepath.Join(dir, "verify.sh"), "#!/usr/bin/env bash\nset -euo pipefail\nsleep 2\ngo test ./...\n")
	if err := os.Chmod(filepath.Join(dir, "verify.sh"), 0o755); err != nil {
		t.Fatalf("chmod verify.sh: %v", err)
	}
	svc := newLiveConfiguredOpenAIResponseService(t, live, dir, func(cfg *task.Config) {
		cfg.Verification.CodingCommands = [][]string{{"./verify.sh"}}
		cfg.Verification.CodingTimeoutSeconds = 20
	})
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "queued live tui",
		Objective: "exercise queued follow-up prompts during an active TUI turn",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "`./verify.sh` passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create queued live task: %v", err)
	}
	return svc, spec
}

func newLiveOpenAIResponseMultiTaskService(t *testing.T, live liveProviderEnv, count int) (*ngenrt.Service, []task.Spec) {
	t.Helper()
	if count < 1 {
		t.Fatalf("count must be >= 1")
	}
	dir := liveScenarioWorkspace(t)
	writeLiveCalcWorkspace(t, dir, "return a + b")
	svc := newLiveConfiguredOpenAIResponseService(t, live, dir, func(cfg *task.Config) {
		cfg.Verification.CodingCommands = [][]string{{"go", "test", "./..."}}
	})
	specs := make([]task.Spec, 0, count)
	for i := 0; i < count; i++ {
		spec, err := svc.Create(context.Background(), task.TaskFile{
			Kind:      task.KindCoding,
			Title:     "demo " + string(rune('1'+i)),
			Objective: "exercise task picker flow",
			SuccessCriteria: []task.SuccessCriterion{
				{ID: "SC-001", Statement: "go test passes"},
			},
			WorkspaceRoot: dir,
		})
		if err != nil {
			t.Fatalf("create task %d: %v", i+1, err)
		}
		specs = append(specs, spec)
	}
	return svc, specs
}

func newLiveOpenAIResponseProjectNavigationService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, []task.Spec) {
	t.Helper()
	svc, specs := newLiveOpenAIResponseMultiTaskService(t, live, 2)
	project, err := svc.Store.LoadProject()
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	project.Revision = 2
	project.CurrentStepID = projectNavigationCurrentStepID
	project.ReadyStepIDs = []string{projectNavigationSiblingStepID}
	project.ActiveBranchIDs = []string{projectNavigationCurrentBranchID}
	project.Steps = []task.ProjectStep{
		{
			ID:       projectNavigationCurrentStepID,
			Title:    "Current repair lane",
			Status:   task.ProjectStepStatusInProgress,
			BranchID: projectNavigationCurrentBranchID,
			TaskID:   specs[0].TaskID,
			Priority: task.StepPriorityHigh,
		},
		{
			ID:       projectNavigationSiblingStepID,
			Title:    projectNavigationSiblingStepTitle,
			Status:   task.ProjectStepStatusPending,
			BranchID: projectNavigationSiblingBranchID,
			TaskID:   specs[1].TaskID,
			Priority: task.StepPriorityMedium,
		},
	}
	project.Branches = []task.ProjectBranch{
		{
			ID:            projectNavigationCurrentBranchID,
			Title:         "Current branch",
			Status:        task.ProjectBranchStatusActive,
			TaskID:        specs[0].TaskID,
			WorkspaceRoot: svc.Store.WorkspaceRoot,
		},
		{
			ID:            projectNavigationSiblingBranchID,
			Title:         projectNavigationSiblingBranchTitle,
			Status:        task.ProjectBranchStatusPending,
			TaskID:        specs[1].TaskID,
			WorkspaceRoot: svc.Store.WorkspaceRoot,
		},
	}
	if err := svc.Store.SaveProject(project); err != nil {
		t.Fatalf("save navigation project: %v", err)
	}
	return svc, specs
}

func newLiveConfiguredOpenAIResponseService(t *testing.T, live liveProviderEnv, dir string, customize func(cfg *task.Config)) *ngenrt.Service {
	t.Helper()
	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = live.BaseURL
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = live.Model
	cfg.Provider.DecisionTimeoutSeconds = 90
	cfg.Provider.AutoRunMaxTurns = 1
	cfg.Provider.CodingExecutionCommandBudget = 2
	cfg.Provider.CodingExecutionCommandTimeoutSeconds = 60
	if customize != nil {
		customize(&cfg)
	}
	writeConfigFile(t, dir, cfg)
	t.Setenv("OPENAI_API_KEY", live.APIKey)
	return ngenrt.New(dir, cfg)
}

func writeLiveCalcWorkspace(t *testing.T, dir, implementation string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/live\n\ngo 1.24.2\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package live\n\nfunc Add(a, b int) int { "+implementation+" }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package live\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2,3)=%d, want 5\", got)\n\t}\n}\n")
}
