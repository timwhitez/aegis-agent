package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"

	"github.com/creack/pty"
)

func TestTUIInlinePTYSmoke(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.ExitAndWait()

	view := pty.Output()
	if strings.TrimSpace(view) == "" {
		t.Fatalf("expected non-empty PTY output")
	}
	status, err := svc.Status(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("status after smoke: %v", err)
	}
	if status.TaskID != spec.TaskID {
		t.Fatalf("unexpected status after smoke: %+v", status)
	}
}

func TestTUISimpleModeNoTaskPromptRefinesTaskPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc := ngenrt.New(t.TempDir(), task.DefaultConfig())
	pty := startTUIHarness(t, svc)
	pty.WaitForText(12*time.Second, "Type your message", "Created TUI session task.")
	pty.SendLine("hello")
	pty.WaitForText(12*time.Second, "ASSISTANT", "Prompt completed.")
	pty.ExitAndWait()

	taskIDs, err := svc.Store.ListTaskIDs()
	if err != nil {
		t.Fatalf("list task ids: %v", err)
	}
	if len(taskIDs) != 1 {
		t.Fatalf("expected one auto-created TUI task, got %+v", taskIDs)
	}
	spec, err := svc.Store.LoadTask(taskIDs[0])
	if err != nil {
		t.Fatalf("load refined task: %v", err)
	}
	if spec.Title != "hello" || spec.Objective != "hello" {
		t.Fatalf("expected first prompt to refine task title/objective, got %+v", spec)
	}
	events, err := svc.Store.ReadEvents(taskIDs[0])
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	foundRefinedEvent := false
	for _, event := range events {
		if event.Type == "task_refined" {
			foundRefinedEvent = true
			break
		}
	}
	if !foundRefinedEvent {
		t.Fatalf("expected task_refined event, got %+v", events)
	}
}

func TestTUISimpleModeRejectsTaskConsolePTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Type your message", "TUI session started.")
	pty.SendLine("/tasks")
	pty.WaitForText(12*time.Second, "Task navigation is available from CLI, ACP, or Web management surfaces", "chat-first")
	if strings.Contains(pty.TailOutput(4000), "Related Tasks") {
		t.Fatalf("expected simple mode to reject /tasks instead of opening task console\nterminal output:\n%s", pty.Output())
	}
	pty.Send("\x0f")
	pty.WaitForText(12*time.Second, "Task navigation is available from CLI, ACP, or Web management surfaces", "chat-first")
	if strings.Contains(pty.TailOutput(4000), "Task Picker") {
		t.Fatalf("expected simple mode to reject Ctrl+O instead of opening picker\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIPickerPTYSelectsSecondTask(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newMultiTaskService(t, 2)
	pty := startTUIHarness(t, svc)
	pty.WaitForText(12*time.Second, "Task Picker", specs[0].TaskID, specs[1].TaskID)
	pty.Send("j")
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Composer", specs[1].TaskID, "demo 2")
	pty.ExitAndWait()
}

func TestTUIPickerPTYFiltersTasks(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newMultiTaskService(t, 3)
	pty := startTUIHarness(t, svc)
	pty.WaitForText(12*time.Second, "Task Picker", specs[0].TaskID, specs[1].TaskID, specs[2].TaskID)
	filterToken := specs[2].TaskID[len(specs[2].TaskID)-4:]
	pty.Send(filterToken)
	pty.WaitForText(12*time.Second, "Filter: "+filterToken, specs[2].TaskID, "demo 3")
	view := pty.Output()
	filterIdx := strings.LastIndex(view, "Filter: "+filterToken)
	if filterIdx == -1 {
		t.Fatalf("expected picker view to include the active filter token\nterminal output:\n%s", pty.Output())
	}
	filteredView := view[filterIdx:]
	if strings.Contains(filteredView, specs[0].TaskID) || strings.Contains(filteredView, specs[1].TaskID) {
		t.Fatalf("expected picker filter to hide non-matching tasks\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Composer", specs[2].TaskID, "demo 3")
	pty.ExitAndWait()
}

func TestTUIPickerPTYAutoRefreshesLateCreatedTask(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newMultiTaskService(t, 1)
	pty := startTUIHarness(t, svc)
	pty.WaitForText(12*time.Second, "Task Picker", specs[0].TaskID, "demo 1")
	late := createLateCodingTask(t, svc, "late picker task", "surface a late-created durable task in the picker")
	pty.WaitForText(12*time.Second, "Task Picker", late.TaskID, "late picker task")
	pty.ExitAndWait()
}

func TestTUITasksTabOpensSelectedWorkerChildAndBackPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, worker := newWorkerContinueService(t)
	pty := startTUIHarness(t, svc, parent.TaskID)
	pty.WaitForText(12*time.Second, "Composer", parent.TaskID)
	pty.SendLine("/tasks")
	pty.WaitForText(12*time.Second, "Related Tasks", worker.ChildTaskID, "objective: review", "parent docs")
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Composer", worker.ChildTaskID, "Parent Task:")
	pty.SendLine("/back")
	pty.WaitForText(12*time.Second, "Composer", parent.TaskID)
	pty.ExitAndWait()
}

func TestTUITasksTabOpensProjectSiblingAndBackPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newProjectNavigationService(t)
	pty := startTUIHarness(t, svc, specs[0].TaskID)
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID)
	pty.SendLine("/tasks")
	pty.WaitForText(12*time.Second, "Related Tasks", specs[1].TaskID, projectNavigationSiblingStepID)
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Composer", specs[1].TaskID, "demo 2")
	pty.SendLine("/back")
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID)
	pty.ExitAndWait()
}

func TestTUITaskSwitchShowsExplicitSwitchingStatePTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newProjectNavigationService(t)
	pty := startTUIHarnessWithEnv(t, svc, []string{"NGEN_TUI_TEST_TASK_OPEN_DELAY_MS=900"}, specs[0].TaskID)
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID)
	pty.SendLine("/tasks")
	pty.WaitForText(12*time.Second, "Related Tasks", specs[1].TaskID, projectNavigationSiblingStepID)
	pty.PressEnter()
	pty.WaitForText(12*time.Second,
		"Switching -> "+specs[1].TaskID,
		"Opening task "+specs[1].TaskID+". Keep typing; Enter waits until the switch completes.",
	)
	view := pty.TailOutput(12000)
	for _, stale := range []string{"switch=", "provider=", "session="} {
		if strings.Contains(view, stale) {
			t.Fatalf("expected switching header to avoid raw token %q\nterminal output:\n%s", stale, pty.Output())
		}
	}
	pty.WaitForText(12*time.Second, "Composer", specs[1].TaskID, "demo 2")
	pty.ExitAndWait()
}

func TestTUITasksTabAutoRefreshesLateCreatedTaskMetadataPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newMultiTaskService(t, 1)
	pty := startTUIHarness(t, svc, specs[0].TaskID)
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID)
	pty.SendLine("/tasks")
	pty.WaitForText(12*time.Second, "Related Tasks", "No related tasks discovered")
	late := createLateCodingTask(t, svc, "late tasks tab sibling", "surface late-created task metadata in the tasks tab")
	pty.WaitForText(12*time.Second, "Related Tasks", late.TaskID, "coding", "Explore / Active")
	pty.ExitAndWait()
}

func TestTUITaskPickerRestoresTaskLocalDraftPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newMultiTaskService(t, 2)
	pty := startTUIHarness(t, svc, specs[0].TaskID)
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID, "demo 1")
	pty.Send("parent draft")
	pty.WaitForText(12*time.Second, "> parent draft")
	pty.Send("\t")
	pty.Send("p")
	pty.WaitForText(12*time.Second, "Task Picker", specs[0].TaskID, specs[1].TaskID)
	pty.Send("j")
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Composer", specs[1].TaskID, "demo 2")
	pty.SendLine("/back")
	waitForCondition(t, 12*time.Second, func() (bool, error) {
		view := pty.TailOutput(12000)
		return strings.Contains(view, specs[0].TaskID) && strings.Contains(view, "> parent draft"), nil
	})
	pty.ExitAndWait()
}

func TestTUITasksTabRestoresTaskLocalDraftPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newProjectNavigationService(t)
	pty := startTUIHarness(t, svc, specs[0].TaskID)
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID)
	pty.Send("parent draft")
	pty.WaitForText(12*time.Second, "> parent draft")
	pty.Send("\t")
	pty.Send("\t")
	pty.Send("8")
	pty.WaitForText(12*time.Second, "Related Tasks", specs[1].TaskID, projectNavigationSiblingStepID)
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Composer", specs[1].TaskID, "demo 2")
	pty.SendLine("/back")
	waitForCondition(t, 12*time.Second, func() (bool, error) {
		view := pty.TailOutput(12000)
		return strings.Contains(view, specs[0].TaskID) && strings.Contains(view, "> parent draft"), nil
	})
	pty.ExitAndWait()
}

func TestTUITasksTabKeepsSelectedRelatedTaskAcrossLeadingInsertPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newProjectNavigationService(t)
	pty := startTUIHarness(t, svc, specs[0].TaskID)
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID)
	pty.SendLine("/tasks")
	pty.WaitForText(12*time.Second, "Related Tasks", specs[1].TaskID, projectNavigationSiblingStepID)
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
	pty.WaitForText(12*time.Second, "Composer", specs[1].TaskID, "Opened related task")
	pty.ExitAndWait()
}

func TestTUITasksTabKeepsSelectedRelatedTaskAcrossLeadingInsertWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newProjectNavigationService(t)
	pty := startTUIHarnessSized(t, svc, 40, 72, specs[0].TaskID)
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID)
	pty.SendLine("/tasks")
	pty.WaitForText(12*time.Second, "Related Tasks", specs[1].TaskID, projectNavigationSiblingStepID)
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
	pty.WaitForText(12*time.Second, "Composer", specs[1].TaskID, "Opened related task")
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIBackCommandBlockedDuringActiveTurnPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newLongRunService(t)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 120, 40, 30*time.Second, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/back")
	waitForCondition(t, 12*time.Second, func() (bool, error) {
		view := pty.TailOutput(12000)
		return strings.Contains(view, "Cannot switch tasks while a turn is active.") || strings.Contains(view, "/back [blocked]"), nil
	})
	pty.Send("\x03")
	pty.WaitForText(12*time.Second, "Confirm Interrupt", "Interrupt current turn")
	pty.PressEnter()
	waitForTaskState(t, svc, spec.TaskID, task.StateAborted, 20*time.Second)
	pty.WaitForText(12*time.Second, "Session cancelled.")
	pty.cleanup()
}

func TestTUIRunPTYCompletesTask(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 20*time.Second)
	pty.WaitForText(12*time.Second, "Prompt completed.")
	pty.ExitAndWait()
}

func TestTUIAutoOpensApprovalModalPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	record, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}

	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	time.Sleep(150 * time.Millisecond)
	view := pty.TailOutput(4000)
	if strings.Contains(view, "Approvals") {
		t.Fatalf("expected approval modal to close after esc\nterminal output:\n%s", pty.Output())
	}
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.ExitAndWait()
}

func TestTUIApprovalPTYApprove(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	record, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}

	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("a")
	waitForCondition(t, 8*time.Second, func() (bool, error) {
		records, err := svc.ListApprovals(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		return len(pendingApprovalRecords(records)) == 0, nil
	})
	pty.WaitForText(12*time.Second, "Approval approved.")
	pty.Send("\t")
	pty.WaitForText(12*time.Second, "Enter action", "a approvals")
	pty.ExitAndWait()
}

func TestTUIAutoOpensInputModalPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	request, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true)
	if err != nil {
		t.Fatalf("request input: %v", err)
	}

	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Input Request", request.RequestID, "Provide target path")
	pty.Send("\x1b")
	time.Sleep(150 * time.Millisecond)
	view := pty.TailOutput(4000)
	if strings.Contains(view, "Input Request") {
		t.Fatalf("expected input modal to close after esc\nterminal output:\n%s", pty.Output())
	}
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.ExitAndWait()
}

func TestTUIInputPTYResponds(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	request, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true)
	if err != nil {
		t.Fatalf("request input: %v", err)
	}

	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Input Request", request.RequestID, "Provide target path")
	pty.SendLine("./demo")
	waitForCondition(t, 8*time.Second, func() (bool, error) {
		records, err := svc.ListInputRequests(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		_, ok := pendingInputRecord(records)
		return !ok, nil
	})
	pty.WaitForText(12*time.Second, "Input response recorded.")
	pty.Send("\t")
	pty.WaitForText(12*time.Second, "Enter action", "a approvals")
	pty.ExitAndWait()
}

func TestTUIComposerHistoryRecallPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/review")
	pty.WaitForText(12*time.Second, "Review refreshed.")
	pty.Send("\x10")
	pty.WaitForText(12*time.Second, "> /review")
	pty.Send("\x0e")
	pty.Send("x")
	pty.WaitForText(12*time.Second, "> x")
	if strings.Contains(pty.TailOutput(3000), "/reviewx") {
		t.Fatalf("expected history-next to restore the empty draft before typing\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIReviewRegeneratesMissingHandoffToDonePTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newReviewHandoffRecoveryService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/review")
	pty.WaitForText(12*time.Second, "Review refreshed.", "Done")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 12*time.Second)
	waitForCondition(t, 12*time.Second, func() (bool, error) {
		_, err := os.Stat(filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md"))
		return err == nil, err
	})
	handoff, err := os.ReadFile(filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md"))
	if err != nil {
		t.Fatalf("read regenerated handoff: %v", err)
	}
	if !strings.Contains(string(handoff), "## Evidence") || !strings.Contains(string(handoff), "## Resume Instructions") {
		t.Fatalf("expected regenerated handoff to include evidence and resume instructions, got:\n%s", string(handoff))
	}
	pty.ExitAndWait()
}

func TestTUIReviewRegeneratesMissingHandoffButKeepsCriteriaBlockedPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newReviewHandoffCriteriaBlockedService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/review")
	pty.WaitForText(12*time.Second, "Review refreshed.", "blocked_review")
	waitForTaskState(t, svc, spec.TaskID, task.StateBlocked, 12*time.Second)
	waitForCondition(t, 12*time.Second, func() (bool, error) {
		_, err := os.Stat(filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md"))
		return err == nil, err
	})
	handoff, err := os.ReadFile(filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md"))
	if err != nil {
		t.Fatalf("read regenerated blocked handoff: %v", err)
	}
	if !strings.Contains(string(handoff), "README.md mentions `alpha`") || !strings.Contains(string(handoff), "blocked_review") {
		t.Fatalf("expected regenerated blocked handoff to preserve the open criterion and blocker status, got:\n%s", string(handoff))
	}
	pty.ExitAndWait()
}

func TestTUIPasteLikeBurstKeepsEnterAsNewlinePTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendBurst("abc\rdef")
	pty.WaitForText(12*time.Second, "abc", "def")
	time.Sleep(250 * time.Millisecond)
	if outputShowsActivePrompt(pty.Output()) {
		t.Fatalf("expected paste-like burst to stay in composer instead of submitting\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIBurstSlashCommandStillSubmitsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendBurst("/review\r")
	pty.WaitForText(12*time.Second, "Review refreshed.")
	pty.ExitAndWait()
}

func TestTUICtrlDWithPendingBurstStaysOpenPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendBurst("a\x04")
	pty.WaitForText(12*time.Second, "> a")
	pty.Send("\x7f")
	pty.Send("\x04")
	pty.Wait()
}

func TestTUIComposerQuestionMarkDoesNotOpenHelpPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.Send("?")
	pty.WaitForText(12*time.Second, "> ?")
	if strings.Contains(pty.TailOutput(4000), "Esc, Enter, or ? close help") {
		t.Fatalf("expected question mark typed in composer to stay as text instead of opening help\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIComposerApprovalKeyDoesNotOpenApprovalsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	record, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.Send("a")
	pty.WaitForText(12*time.Second, "> a")
	if strings.Contains(pty.TailOutput(4000), "Approvals") {
		t.Fatalf("expected approval hotkey character typed in composer to stay as text instead of reopening approvals\nterminal output:\n%s", pty.Output())
	}
	pty.Send("\x7f")
	pty.ExitAndWait()
}

func TestTUIPromptAutoSettleAllowsImmediateFollowUpPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarnessWithEnv(t, svc, []string{"NGEN_TUI_TEST_PROMPT_RETURN_DELAY_MS=1200"}, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.WaitForText(12*time.Second, "Prompt completed.")
	pty.SendLine("/review")
	pty.WaitForText(12*time.Second, "Review refreshed.")
	time.Sleep(1400 * time.Millisecond)
	view := pty.Output()
	if lastIndexAny(view, []string{"Review refreshed."}) < lastIndexAny(view, []string{"Prompt completed."}) {
		t.Fatalf("expected stale prompt completion callback to avoid overwriting newer review status\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUIQueuesFollowUpPromptDuringActiveTurnPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newQueuedPromptService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone queued follow-up memory note")
	pty.WaitForText(12*time.Second, "Queued follow-up prompt.", "Queued Prompts", "1. /memory milestone queued follow-up memory note")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 20*time.Second)
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		entries, err := svc.Store.ReadMemoryEntries()
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if strings.Contains(entry.Summary, "queued follow-up memory note") {
				return true, nil
			}
		}
		return false, nil
	})
	pty.Send("\t")
	pty.Send("6")
	pty.WaitForText(12*time.Second, "queued follow-up memory note")
	pty.WaitForText(12*time.Second, "Prompt completed.")
	pty.ExitAndWait()
}

func TestTUIComposerShowsCompactQueuedStatePTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newQueuedPromptServiceWithVerifyDelay(t, 5*time.Second)
	pty := startTUIHarnessWithEnv(t, svc, []string{"NGEN_TUI_TEST_PROMPT_RETURN_DELAY_MS=1500"}, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone compact queued badge one")
	pty.SendLine("/memory milestone compact queued badge two")
	pty.WaitForText(12*time.Second, "Queued 2", "Enter edits selected. Backspace drops. Ctrl+P newest.")
	pty.cleanup()
}

func TestTUINarrowQueuedFooterKeepsInterruptVisiblePTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newQueuedPromptServiceWithVerifyDelay(t, 5*time.Second)
	pty := startTUIHarnessSizedWithTimeoutAndEnv(t, svc, 40, 24, 45*time.Second, []string{"NGEN_TUI_TEST_PROMPT_RETURN_DELAY_MS=1500"}, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone narrow queued footer")
	pty.WaitForText(12*time.Second, "Queued follow-up prompt.", "Enter edit", "Ctrl+C interrupt")
	view := pty.TailOutput(4000)
	if strings.Contains(view, "Backspace drop") {
		t.Fatalf("expected 40-col queued footer to prioritize edit plus interrupt over lower-priority drop hint\nterminal output:\n%s", pty.Output())
	}
	pty.Send("\x03")
	pty.WaitForText(12*time.Second, "Confirm Interrupt", "Interrupt current turn")
	pty.PressEnter()
	waitForTaskState(t, svc, spec.TaskID, task.StateAborted, 20*time.Second)
	pty.WaitForText(12*time.Second, "Session cancelled.")
	pty.cleanup()
}

func TestTUIRecallQueuedPromptForEditingPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newQueuedPromptService(t)
	pty := startTUIHarnessWithEnv(t, svc, []string{"NGEN_TUI_TEST_PROMPT_RETURN_DELAY_MS=1500"}, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone queued recall note one")
	pty.WaitForText(12*time.Second, "Queued follow-up prompt.", "1. /memory milestone queued recall note one")
	pty.Send("\x10")
	pty.WaitForText(12*time.Second, "Queued prompt restored to composer for editing.", "> /memory milestone queued recall note one")
	pty.Send("\x7f\x7f\x7f")
	pty.SendLine("two")
	pty.WaitForText(12*time.Second, "Queued follow-up prompt.", "queued recall note two")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 20*time.Second)
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		entries, err := svc.Store.ReadMemoryEntries()
		if err != nil {
			return false, err
		}
		var sawEdited bool
		for _, entry := range entries {
			if strings.Contains(entry.Summary, "queued recall note one") {
				return false, nil
			}
			if strings.Contains(entry.Summary, "queued recall note two") {
				sawEdited = true
			}
		}
		return sawEdited, nil
	})
	pty.Send("\t")
	pty.Send("6")
	pty.WaitForText(12*time.Second, "queued recall note two")
	pty.WaitForText(12*time.Second, "Prompt completed.")
	pty.ExitAndWait()
}

func TestTUISelectsVisibleQueuedPromptForEditingPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newQueuedPromptService(t)
	pty := startTUIHarnessWithEnv(t, svc, []string{"NGEN_TUI_TEST_PROMPT_RETURN_DELAY_MS=1500"}, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone queued select note one")
	pty.SendLine("/memory milestone queued select note two")
	pty.WaitForText(12*time.Second, "Queued 2 follow-up prompts.", "Queued Prompts", "> 1. /memory milestone queued select note one", "2. /memory milestone queued select note two")
	pty.SendBurst("\x1b[B")
	pty.WaitForText(12*time.Second, "> 2. /memory milestone queued select note two")
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Queued prompt restored to composer for editing. 1 follow-up remains queued.", "> /memory milestone queued select note two")
	pty.Send("\x7f\x7f\x7f")
	pty.SendLine("three")
	pty.WaitForText(12*time.Second, "Queued 2 follow-up prompts.", "queued select note three")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 20*time.Second)
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		entries, err := svc.Store.ReadMemoryEntries()
		if err != nil {
			return false, err
		}
		var sawOne bool
		var sawThree bool
		for _, entry := range entries {
			if strings.Contains(entry.Summary, "queued select note two") {
				return false, nil
			}
			if strings.Contains(entry.Summary, "queued select note one") {
				sawOne = true
			}
			if strings.Contains(entry.Summary, "queued select note three") {
				sawThree = true
			}
		}
		return sawOne && sawThree, nil
	})
	pty.Send("\t")
	pty.Send("6")
	pty.WaitForText(12*time.Second, "queued select note one", "queued select note three")
	pty.WaitForText(12*time.Second, "Prompt completed.")
	pty.ExitAndWait()
}

func TestTUIScrollsQueuedPromptSelectionBeyondVisibleWindowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newQueuedPromptServiceWithVerifyDelay(t, 5*time.Second)
	pty := startTUIHarnessWithEnv(t, svc, []string{"NGEN_TUI_TEST_PROMPT_RETURN_DELAY_MS=1500"}, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone queued scroll slot one")
	pty.SendLine("/memory milestone queued scroll slot two")
	pty.SendLine("/memory milestone queued scroll slot three")
	pty.SendLine("/memory milestone queued scroll slot dddd")
	pty.WaitForText(12*time.Second,
		"Queued Prompts",
		"Queued",
		"> 1. /memory milestone queued scroll slot one",
		"3. /memory milestone queued scroll slot three",
	)
	pty.SendBurst("\x1b[B\x1b[B\x1b[B")
	pty.WaitForText(12*time.Second, "+1 earlier", "> 4. /memory milestone queued scroll slot dddd")
	pty.Send("\x7f")
	pty.WaitForText(12*time.Second,
		"Dropped selected queued prompt. 3 follow-ups remain queued.",
		"> 3. /memory milestone queued scroll slot three",
	)
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 25*time.Second)
	waitForCondition(t, 25*time.Second, func() (bool, error) {
		entries, err := svc.Store.ReadMemoryEntries()
		if err != nil {
			return false, err
		}
		var sawOne bool
		var sawTwo bool
		var sawThree bool
		for _, entry := range entries {
			if strings.Contains(entry.Summary, "queued scroll slot dddd") {
				return false, nil
			}
			if strings.Contains(entry.Summary, "queued scroll slot one") {
				sawOne = true
			}
			if strings.Contains(entry.Summary, "queued scroll slot two") {
				sawTwo = true
			}
			if strings.Contains(entry.Summary, "queued scroll slot three") {
				sawThree = true
			}
		}
		return sawOne && sawTwo && sawThree, nil
	})
	pty.WaitForText(12*time.Second, "Prompt completed.")
	pty.ExitAndWait()
}

func TestTUIDropsSelectedVisibleQueuedPromptPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newQueuedPromptService(t)
	pty := startTUIHarnessWithEnv(t, svc, []string{"NGEN_TUI_TEST_PROMPT_RETURN_DELAY_MS=1500"}, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone queued drop note one")
	pty.SendLine("/memory milestone queued drop note two")
	pty.WaitForText(12*time.Second, "Queued 2 follow-up prompts.", "Queued Prompts", "> 1. /memory milestone queued drop note one", "2. /memory milestone queued drop note two")
	pty.SendBurst("\x1b[B")
	pty.WaitForText(12*time.Second, "> 2. /memory milestone queued drop note two")
	pty.Send("\x7f")
	pty.WaitForText(12*time.Second, "Dropped selected queued prompt. 1 follow-up remains queued.", "> 1. /memory milestone queued drop note one")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 20*time.Second)
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		entries, err := svc.Store.ReadMemoryEntries()
		if err != nil {
			return false, err
		}
		var sawOne bool
		for _, entry := range entries {
			if strings.Contains(entry.Summary, "queued drop note two") {
				return false, nil
			}
			if strings.Contains(entry.Summary, "queued drop note one") {
				sawOne = true
			}
		}
		return sawOne, nil
	})
	pty.Send("\t")
	pty.Send("6")
	pty.WaitForText(12*time.Second, "queued drop note one")
	pty.WaitForText(12*time.Second, "Prompt completed.")
	pty.ExitAndWait()
}

func TestTUIQueuesMultipleFollowUpPromptsFIFOActiveTurnPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newQueuedPromptService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone first queued memory note")
	pty.SendLine("/memory milestone second queued memory note")
	pty.WaitForText(12*time.Second, "Queued 2 follow-up prompts.", "Queued Prompts", "1. /memory milestone first queued memory note", "2. /memory milestone second queued memory note")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 20*time.Second)
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		entries, err := svc.Store.ReadMemoryEntries()
		if err != nil {
			return false, err
		}
		if len(entries) < 2 {
			return false, nil
		}
		lastTwo := entries[len(entries)-2:]
		return strings.Contains(lastTwo[0].Summary, "first queued memory note") && strings.Contains(lastTwo[1].Summary, "second queued memory note"), nil
	})
	pty.Send("\t")
	pty.Send("6")
	pty.WaitForText(12*time.Second, "first queued memory note", "second queued memory note")
	pty.WaitForText(12*time.Second, "Prompt completed.")
	pty.ExitAndWait()
}

func TestTUIInterruptPTYAbortsLongRun(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newLongRunService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.Send("\x03")
	pty.WaitForText(12*time.Second, "Confirm Interrupt", "Interrupt current turn")
	pty.PressEnter()
	waitForTaskState(t, svc, spec.TaskID, task.StateAborted, 20*time.Second)
	pty.WaitForText(12*time.Second, "Session cancelled.")
	pty.cleanup()
}

func TestTUISlashExitDuringRunOpensConfirmPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newLongRunService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/exit")
	pty.WaitForText(12*time.Second, "Confirm Interrupt", "Interrupt current turn")
	pty.PressEnter()
	waitForTaskState(t, svc, spec.TaskID, task.StateAborted, 20*time.Second)
	pty.WaitForText(12*time.Second, "Session cancelled.")
	pty.cleanup()
}

func TestTUIInterruptFromHelpModalPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newLongRunService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/help")
	pty.WaitForText(12*time.Second, "Fast path", "Ctrl+C quits when idle or interrupts the active turn")
	pty.Send("\x03")
	pty.WaitForText(12*time.Second, "Confirm Interrupt", "Interrupt current turn")
	pty.PressEnter()
	waitForTaskState(t, svc, spec.TaskID, task.StateAborted, 20*time.Second)
	pty.WaitForText(12*time.Second, "Session cancelled.")
	pty.cleanup()
}

func TestTUISecondCtrlCConfirmsInterruptPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newLongRunService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.Send("\x03")
	pty.WaitForText(12*time.Second, "Confirm Interrupt", "Interrupt current turn")
	pty.Send("\x03")
	waitForTaskState(t, svc, spec.TaskID, task.StateAborted, 20*time.Second)
	pty.WaitForText(12*time.Second, "Session cancelled.")
	pty.cleanup()
}

func TestTUIGreetingShowsAssistantReplyPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("hello")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.WaitForText(12*time.Second, "ASSISTANT", "task-oriented", "Prompt completed.")
	if svc.Store.HasBaseline(spec.TaskID) {
		t.Fatalf("expected greeting prompt to avoid baseline capture\nterminal output:\n%s", pty.Output())
	}
	pty.cleanup()
}

func TestTUISlashPickerDuringRunShowsBlockedPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newLongRunService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	before := len(pty.Output())
	pty.SendLine("/picker")
	waitForCondition(t, 12*time.Second, func() (bool, error) {
		view := pty.Output()
		delta := view
		if before < len(view) {
			delta = view[before:]
		}
		if strings.Contains(delta, "Cannot switch tasks while a turn is active.") {
			return true, nil
		}
		return strings.Contains(delta, "Prompt completed.") &&
			!strings.Contains(delta, "Task Picker") &&
			strings.Contains(view, spec.TaskID), nil
	})
	if strings.Contains(pty.TailOutput(4000), "Task Picker") {
		t.Fatalf("expected /picker during active turn to stay on current task instead of opening picker\nterminal output:\n%s", pty.Output())
	}
	if outputShowsActivePrompt(pty.Output()) {
		pty.Send("\x03")
		pty.WaitForText(12*time.Second, "Confirm Interrupt", "Interrupt current turn")
		pty.PressEnter()
		waitForTaskState(t, svc, spec.TaskID, task.StateAborted, 20*time.Second)
		pty.WaitForText(12*time.Second, "Session cancelled.")
	}
	pty.cleanup()
}

func TestTUIBlockersTabShowsPendingApprovalAndInputPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	approval, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	input, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true)
	if err != nil {
		t.Fatalf("request input: %v", err)
	}
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", approval.ApprovalID, "manual step")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.Send("\t")
	pty.Send("5")
	pty.WaitForText(12*time.Second, "Task Approvals", approval.ApprovalID, "manual step", "Pending Input", input.RequestID, "Provide target path")
	pty.ExitAndWait()
}

func TestTUIWorkerContinuePTYCompletesReviewerChild(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, worker := newWorkerContinueService(t)
	pty := startTUIHarness(t, svc, parent.TaskID)
	pty.WaitForText(12*time.Second, "Composer", parent.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(12*time.Second, "Workers", worker.WorkerID, "reviewer")
	pty.Send("\t")
	pty.PressEnter()
	waitForCondition(t, 12*time.Second, func() (bool, error) {
		result, err := svc.Store.LoadWorkerResult(parent.TaskID, worker.WorkerID)
		if err != nil {
			return false, err
		}
		return result.CompletionStatus == "accepted" && result.ReviewStatus == "clear" && result.VerificationStatus == "passed", nil
	})
	pty.WaitForText(12*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIOwnedApprovalApproveThenContinueWorkerPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, worker, record := newOwnedApprovalContinueService(t)
	pty := startTUIHarness(t, svc, parent.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("a")
	waitForCondition(t, 12*time.Second, func() (bool, error) {
		approvals, err := svc.ListOwnedApprovals(context.Background(), parent.TaskID)
		if err != nil {
			return false, err
		}
		workers, err := svc.ListWorkers(context.Background(), parent.TaskID)
		if err != nil {
			return false, err
		}
		if len(workers) != 1 {
			return false, nil
		}
		return len(pendingApprovalRecords(approvals)) == 0 && workers[0].ParentActionType == "continue_child", nil
	})
	pty.WaitForText(12*time.Second, "Approval approved.")
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(12*time.Second, "Workers", worker.WorkerID, "continue_child")
	pty.Send("\t")
	pty.PressEnter()
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		result, err := svc.Store.LoadWorkerResult(parent.TaskID, worker.WorkerID)
		if err != nil {
			return false, err
		}
		return result.CompletionStatus == "accepted" && result.ReviewStatus == "clear" && result.VerificationStatus == "passed", nil
	})
	pty.WaitForText(12*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIWorkersSelectionStaysOnActiveWorkerAcrossRefreshPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, first, second, record := newWorkerSelectionActiveService(t)
	pty := startTUIHarnessSized(t, svc, 110, 30, parent.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Composer")
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(12*time.Second, "Workers", first.WorkerID, second.WorkerID)
	pty.Send("\t")
	pty.Send("j")
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected second worker to be selected before refresh\nterminal output:\n%s", pty.Output())
	}
	spawnTrailingWorker(t, svc, parent.TaskID, "review trailing parent docs")
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected refresh to keep second active worker selected\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(parent.TaskID, first.WorkerID)
		if err != nil {
			return false, err
		}
		secondResult, err := svc.Store.LoadWorkerResult(parent.TaskID, second.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			secondResult.CompletionStatus == "accepted" &&
			secondResult.ReviewStatus == "clear" &&
			secondResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(12*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIWorkersSelectionStaysOnContinueChildAcrossRefreshPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, first, second, record := newWorkerSelectionContinueService(t)
	pty := startTUIHarnessSized(t, svc, 110, 30, parent.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Composer")
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(12*time.Second, "Workers", first.WorkerID, second.WorkerID)
	pty.Send("\t")
	pty.Send("j")
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected second continue_child worker to be selected before refresh\nterminal output:\n%s", pty.Output())
	}
	spawnTrailingWorker(t, svc, parent.TaskID, "review trailing parent docs")
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected refresh to keep second continue_child worker selected\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(parent.TaskID, first.WorkerID)
		if err != nil {
			return false, err
		}
		secondResult, err := svc.Store.LoadWorkerResult(parent.TaskID, second.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			secondResult.CompletionStatus == "accepted" &&
			secondResult.ReviewStatus == "clear" &&
			secondResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(12*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIWorkersSelectionStaysOnActiveWorkerAcrossLeadingInsertPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, first, second, record := newWorkerSelectionActiveService(t)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 45*time.Second, parent.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Composer")
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(12*time.Second, "Workers", first.WorkerID, second.WorkerID)
	pty.Send("\t")
	pty.Send("j")
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected second worker to be selected before refresh\nterminal output:\n%s", pty.Output())
	}
	inserted := spawnLeadingWorker(t, svc, parent.TaskID, "WORKER-0000-leading", "review inserted before selected worker")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		workers, err := svc.ListWorkers(context.Background(), parent.TaskID)
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
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		secondResult, err := svc.Store.LoadWorkerResult(parent.TaskID, second.WorkerID)
		if err != nil {
			return false, err
		}
		return secondResult.CompletionStatus == "accepted" &&
			secondResult.ReviewStatus == "clear" &&
			secondResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(12*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIWorkersSelectionStaysOnActiveWorkerAcrossLeadingInsertWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, first, second, record := newWorkerSelectionActiveService(t)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 40, 60, 45*time.Second, parent.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, "manual step")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Composer")
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(12*time.Second, "Workers", first.WorkerID, second.WorkerID)
	pty.Send("\t")
	pty.Send("j")
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + second.WorkerID}) < lastIndexAny(view, []string{"> " + first.WorkerID}) {
		t.Fatalf("expected second worker to be selected before refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	inserted := spawnLeadingWorker(t, svc, parent.TaskID, "WORKER-0000-leading", "review inserted before selected worker")
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		workers, err := svc.ListWorkers(context.Background(), parent.TaskID)
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
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		secondResult, err := svc.Store.LoadWorkerResult(parent.TaskID, second.WorkerID)
		if err != nil {
			return false, err
		}
		return secondResult.CompletionStatus == "accepted" &&
			secondResult.ReviewStatus == "clear" &&
			secondResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(12*time.Second, "Worker continued.")
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.cleanup()
}

func TestTUIWorkersLongListAutoScrollsActiveSelectionPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, workers := newLongWorkerListService(t, workersLongListCount)
	target := workers[workersLongListTargetIndex]
	pty := startTUIHarnessSized(t, svc, 110, 18, parent.TaskID)
	pty.WaitForText(12*time.Second, "Composer", parent.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(12*time.Second, "Workers", workers[0].WorkerID)
	pty.Send("\t")
	pty.Send(strings.Repeat("j", workersLongListTargetIndex))
	time.Sleep(300 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + target.WorkerID}) < 0 {
		t.Fatalf("expected selected long-list worker to be visible after navigation\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(parent.TaskID, workers[0].WorkerID)
		if err != nil {
			return false, err
		}
		targetResult, err := svc.Store.LoadWorkerResult(parent.TaskID, target.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			targetResult.CompletionStatus == "accepted" &&
			targetResult.ReviewStatus == "clear" &&
			targetResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(12*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIWorkersLongListAutoScrollsActiveSelectionWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, workers := newLongWorkerListService(t, workersLongListCount)
	target := workers[workersLongListTargetIndex]
	pty := startTUIHarnessSized(t, svc, 40, 30, parent.TaskID)
	pty.WaitForText(12*time.Second, "Composer", parent.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(12*time.Second, "Workers", workers[0].WorkerID)
	pty.Send("\t")
	pty.Send(strings.Repeat("j", workersLongListTargetIndex))
	time.Sleep(300 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + target.WorkerID}) < 0 {
		t.Fatalf("expected selected long-list worker to be visible after navigation in 40 cols\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(parent.TaskID, workers[0].WorkerID)
		if err != nil {
			return false, err
		}
		targetResult, err := svc.Store.LoadWorkerResult(parent.TaskID, target.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			targetResult.CompletionStatus == "accepted" &&
			targetResult.ReviewStatus == "clear" &&
			targetResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(12*time.Second, "Worker continued.")
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.cleanup()
}

func TestTUIWorkersLongListAutoScrollsContinueChildSelectionPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, workers := newLongWorkerListService(t, workersLongListCount)
	target := workers[workersLongListTargetIndex]
	setWorkerContinueChildReady(t, svc, parent.TaskID, target)
	pty := startTUIHarnessSized(t, svc, 110, 18, parent.TaskID)
	pty.WaitForText(12*time.Second, "Composer", parent.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(12*time.Second, "Workers", workers[0].WorkerID)
	pty.Send("\t")
	pty.Send(strings.Repeat("j", workersLongListTargetIndex))
	time.Sleep(300 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + target.WorkerID}) < 0 {
		t.Fatalf("expected selected continue_child long-list worker to be visible after navigation\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(parent.TaskID, workers[0].WorkerID)
		if err != nil {
			return false, err
		}
		targetResult, err := svc.Store.LoadWorkerResult(parent.TaskID, target.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			targetResult.CompletionStatus == "accepted" &&
			targetResult.ReviewStatus == "clear" &&
			targetResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(12*time.Second, "Worker continued.")
	pty.cleanup()
}

func TestTUIWorkersLongListAutoScrollsContinueChildSelectionWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, workers := newLongWorkerListService(t, workersLongListCount)
	target := workers[workersLongListTargetIndex]
	setWorkerContinueChildReady(t, svc, parent.TaskID, target)
	pty := startTUIHarnessSized(t, svc, 40, 30, parent.TaskID)
	pty.WaitForText(12*time.Second, "Composer", parent.TaskID)
	pty.Send("\t")
	pty.Send("4")
	pty.WaitForText(12*time.Second, "Workers", workers[0].WorkerID)
	pty.Send("\t")
	pty.Send(strings.Repeat("j", workersLongListTargetIndex))
	time.Sleep(300 * time.Millisecond)
	view := pty.TailOutput(5000)
	if lastIndexAny(view, []string{"> " + target.WorkerID}) < 0 {
		t.Fatalf("expected selected continue_child long-list worker to be visible after navigation in 40 cols\nterminal output:\n%s", pty.Output())
	}
	pty.PressEnter()
	waitForCondition(t, 20*time.Second, func() (bool, error) {
		firstContract, err := svc.Store.LoadWorkerContract(parent.TaskID, workers[0].WorkerID)
		if err != nil {
			return false, err
		}
		targetResult, err := svc.Store.LoadWorkerResult(parent.TaskID, target.WorkerID)
		if err != nil {
			return false, err
		}
		return firstContract.ContinuationCount == 0 &&
			targetResult.CompletionStatus == "accepted" &&
			targetResult.ReviewStatus == "clear" &&
			targetResult.VerificationStatus == "passed", nil
	})
	pty.WaitForText(12*time.Second, "Worker continued.")
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.cleanup()
}

func TestTUINarrowPTYResizeAndHelpInteractions(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarnessSized(t, svc, 60, 22, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", "Quick:", "Ctrl+K actions", "Ctrl+O picker")
	view := pty.TailOutput(3000)
	if strings.Contains(view, "a approvals") {
		t.Fatalf("expected composer-focused footer to avoid single-key approval hint\nterminal output:\n%s", pty.Output())
	}
	for _, absent := range []string{"Ctrl+J newline", "Ctrl+C quit"} {
		if strings.Contains(view, absent) {
			t.Fatalf("expected narrow composer footer collapse to hide %q\nterminal output:\n%s", absent, pty.Output())
		}
	}
	pty.Send("\t")
	pty.WaitForText(12*time.Second, "Enter action", "a approvals")
	pty.Send("?")
	pty.WaitForText(12*time.Second, "Slash forms still work", "/review /run /resume /quit")
	pty.Send("?")
	pty.WaitForText(12*time.Second, "Enter action", "a approvals")
	pty.Resize(110, 30)
	pty.Send("\t")
	pty.Send("\t")
	pty.WaitForText(12*time.Second, "Quick:", "Ctrl+K actions", "Ctrl+O picker")
	pty.ExitAndWait()
}

func TestTUINarrowRunningNonComposerFooterKeepsInterruptVisiblePTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newLongRunService(t)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 40, 24, 30*time.Second, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.Send("\t")
	pty.WaitForText(12*time.Second, "Tab focus", "Ctrl+C interrupt")
	view := pty.TailOutput(4000)
	if strings.Contains(view, "a approvals") {
		t.Fatalf("expected 40-col running non-composer footer to drop lower-priority approval hint before interrupt\nterminal output:\n%s", pty.Output())
	}
	pty.Send("\x03")
	pty.WaitForText(12*time.Second, "Confirm Interrupt", "Interrupt current turn")
	pty.PressEnter()
	waitForTaskState(t, svc, spec.TaskID, task.StateAborted, 20*time.Second)
	pty.WaitForText(12*time.Second, "Session cancelled.")
	pty.cleanup()
}

func TestTUIPlainTextAliasesAndShortcutsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newProjectNavigationService(t)
	pty := startTUIHarness(t, svc, specs[0].TaskID)
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID, "Quick:")
	pty.SendLine("tasks")
	pty.WaitForText(12*time.Second, "Related Tasks", specs[1].TaskID, projectNavigationSiblingStepID)
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Composer", specs[1].TaskID, "demo 2", "Quick:", "Ctrl+K actions")
	pty.SendLine("back")
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID, "Returned to task")
	pty.Send("\x0f")
	pty.WaitForText(12*time.Second, "Task Picker", specs[0].TaskID, specs[1].TaskID)
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID)
	pty.ExitAndWait()
}

func TestTUIActionPaletteShortcutAndAliasPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID, "Ctrl+K actions")
	pty.Send("\x0b")
	pty.WaitForText(12*time.Second, "Action Palette", "run [ready]", "help [ready]")
	pty.Send("help")
	pty.WaitForText(12*time.Second, "Filter: help", "help [ready]")
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Fast path", "/actions", "/review /run /resume /quit")
	pty.Send("?")
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID, "Ctrl+K actions")
	pty.SendLine("actions")
	pty.WaitForText(12*time.Second, "Action Palette", "run [ready]", "help [ready]")
	pty.ExitAndWait()
}

func TestTUISlashSuggestionsAndTabCompletePTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.Send("/he")
	pty.WaitForText(12*time.Second, "Commands", "/help [ready]", "Enter waits until suggestions are resolved")
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Command suggestions are open. Press Tab to complete or keep typing.", "> /he")
	pty.Send("\t")
	pty.WaitForText(12*time.Second, "> /help")
	pty.PressEnter()
	pty.WaitForText(12*time.Second, "Fast path", "Slash input: type / for local commands", "/actions")
	pty.ExitAndWait()
}

func TestTUIPromptOverlayEscDismissesBeforeInterruptPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newQueuedPromptService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.SendLine("/run")
	pty.WaitForText(12*time.Second, "Running prompt...")
	pty.SendLine("/memory milestone queued follow-up memory note")
	pty.WaitForText(12*time.Second, "Queued Prompts", "1. /memory milestone queued follow-up memory note")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Prompt overlay hidden. Press Esc again to interrupt or keep typing to reopen.")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Confirm Interrupt", "Interrupt current turn")
	pty.PressEnter()
	waitForTaskState(t, svc, spec.TaskID, task.StateAborted, 20*time.Second)
	pty.WaitForText(12*time.Second, "Session cancelled.")
	pty.cleanup()
}

func TestTUIExternalEditorUpdatesComposerPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	editor := createTestEditorScript(t, "edited from editor")
	pty := startTUIHarnessWithEnv(t, svc, []string{"EDITOR=" + editor}, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	pty.Send("\x07")
	pty.WaitForText(12*time.Second, "Draft updated from external editor.", "> edited from editor")
	pty.ExitAndWait()
}

func TestTUIExternalEditorUpdatesInputModalPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	if _, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true); err != nil {
		t.Fatalf("request input: %v", err)
	}
	editor := createTestEditorScript(t, "input/from/editor")
	pty := startTUIHarnessWithEnv(t, svc, []string{"EDITOR=" + editor}, spec.TaskID)
	pty.WaitForText(12*time.Second, "Input Request", "Ctrl+G editor")
	pty.Send("\x07")
	pty.WaitForText(12*time.Second, "Input draft updated from external editor.", "> input/from/editor")
	pty.ExitAndWait()
}

func TestTUIEscRestoresPreviousPromptPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID, "Esc prev")
	pty.SendLine("hello")
	pty.WaitForText(12*time.Second, "ASSISTANT", "Prompt completed.")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Press Esc again to edit the previous prompt.")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Previous prompt restored to composer.", "> hello")
	pty.ExitAndWait()
}

func TestTUILongHeaderWrapsWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newLongHeaderService(t)
	pty := startTUIHarnessSized(t, svc, 36, 22, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	assertMaxLineWidth(t, pty.Output(), 36)
	pty.ExitAndWait()
}

func TestTUIPickerWrapsCompactRowsWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newLongPickerService(t)
	pty := startTUIHarnessSized(t, svc, 44, 24)
	pty.WaitForText(12*time.Second, "Task Picker", specs[0].TaskID, "observability rollout", specs[1].TaskID, "workspace handoff")
	assertMaxLineWidth(t, pty.Output(), 44)
	pty.ExitAndWait()
}

func TestTUITasksInspectorWrapsCompactRowsWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newLongTaskNavigationService(t)
	pty := startTUIHarnessSized(t, svc, 48, 64, specs[0].TaskID)
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID)
	pty.Send("\t")
	pty.Send("8")
	pty.WaitForText(12*time.Second, "Related Tasks", taskNavLongCurrentTitleSuffix, taskNavLongRelatedTitleSuffix, projectNavigationSiblingStepID)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUITasksInspectorWrapsCompactRowsWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, specs := newLongTaskNavigationService(t)
	pty := startTUIHarnessSized(t, svc, 40, 88, specs[0].TaskID)
	pty.WaitForText(12*time.Second, "Composer", specs[0].TaskID)
	pty.Send("\t")
	pty.Send("8")
	pty.WaitForText(12*time.Second, "Related Tasks", taskNavLongCurrentTitleSuffix, taskNavLongRelatedTitleSuffix, projectNavigationSiblingStepID)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIInlineFlagDisablesAltScreenPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarnessCLIArgsSizedWithTimeoutAndEnv(
		t,
		svc,
		[]string{"tui", "--inline", spec.TaskID},
		96,
		24,
		45*time.Second,
		nil,
	)
	pty.WaitForText(12*time.Second, "Composer", "TUI session started.")
	time.Sleep(250 * time.Millisecond)
	if strings.Contains(pty.RawOutput(), altScreenEnterSeq) {
		t.Fatalf("expected --inline to keep the TUI out of alt-screen mode\nraw terminal output:\n%q", pty.RawOutput())
	}
	pty.ExitAndWait()
}

func TestTUIAutoAltScreenFallsBackToInlineInZellijPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarnessCLIArgsSizedWithTimeoutAndEnv(
		t,
		svc,
		[]string{"tui", spec.TaskID},
		96,
		24,
		45*time.Second,
		[]string{"ZELLIJ=1"},
	)
	pty.WaitForText(12*time.Second, "Composer", "TUI session started.")
	time.Sleep(250 * time.Millisecond)
	if strings.Contains(pty.RawOutput(), altScreenEnterSeq) {
		t.Fatalf("expected auto mode under ZELLIJ to stay inline instead of entering alt-screen\nraw terminal output:\n%q", pty.RawOutput())
	}
	pty.ExitAndWait()
}

func TestTUIAlwaysAltScreenUsesAltScreenPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setTUIAltScreenMode(t, svc, "always")
	pty := startTUIHarnessCLIArgsSizedWithTimeoutAndEnv(
		t,
		svc,
		[]string{"tui", spec.TaskID},
		96,
		24,
		45*time.Second,
		nil,
	)
	pty.WaitForRawText(12*time.Second, altScreenEnterSeq)
	pty.WaitForText(12*time.Second, "Composer", "TUI session started.")
	pty.ExitAndWait()
	if !strings.Contains(pty.RawOutput(), altScreenExitSeq) {
		t.Fatalf("expected alt-screen session to restore the normal screen on exit\nraw terminal output:\n%q", pty.RawOutput())
	}
}

func TestTUITranscriptKeepsSessionMessagesWithBoundedEventTailPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setTUIEventLimit(t, svc, 1)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	session := waitForTaskSession(t, svc, spec.TaskID, 8*time.Second)
	appendTranscriptFixture(t, svc, spec.TaskID, session.SessionID)

	pty.WaitForText(12*time.Second,
		transcriptMessageAlpha,
		transcriptMessageBeta,
		transcriptMessageGamma,
		transcriptEventNewest,
	)
	if strings.Contains(pty.TailOutput(4000), transcriptEventStale) {
		t.Fatalf("expected stale event to stay outside the bounded transcript tail\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUITranscriptShowsLatestEventRefsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setTUIEventLimit(t, svc, 1)
	pty := startTUIHarness(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	session := waitForTaskSession(t, svc, spec.TaskID, 8*time.Second)
	appendTranscriptFixture(t, svc, spec.TaskID, session.SessionID)

	pty.WaitForText(12*time.Second, transcriptEventNewest, transcriptRefTrace, transcriptRefStatus)
	if strings.Contains(pty.TailOutput(4000), transcriptRefStale) {
		t.Fatalf("expected bounded-out event refs to stay outside the transcript tail\nterminal output:\n%s", pty.Output())
	}
	pty.ExitAndWait()
}

func TestTUITranscriptScrollUpStaysPinnedAcrossRefreshPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarnessSized(t, svc, 110, 18, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	session := waitForTaskSession(t, svc, spec.TaskID, 8*time.Second)
	appendTranscriptScrollFixture(t, svc, spec.TaskID, session.SessionID, transcriptScrollFixtureCount)
	pty.WaitForText(12*time.Second, transcriptScrollBottomAnchor)

	pty.Send("\t")
	pty.Send("kkkkkkkkkkkkkkkkkk")
	pty.WaitForText(12*time.Second, transcriptScrollOlderAnchor)

	appendTranscriptScrollNewest(t, svc, spec.TaskID, session.SessionID)
	time.Sleep(600 * time.Millisecond)
	if strings.Contains(pty.TailOutput(3500), transcriptScrollNewest) {
		t.Fatalf("expected scrolled-up transcript to stay pinned instead of auto-following new content\nterminal output:\n%s", pty.Output())
	}

	pty.Send(strings.Repeat("j", 32))
	pty.WaitForText(12*time.Second, transcriptScrollNewest)
	pty.ExitAndWait()
}

func TestTUITranscriptScrollUpStaysPinnedAcrossRefreshWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarnessSized(t, svc, 40, 22, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	session := waitForTaskSession(t, svc, spec.TaskID, 8*time.Second)
	appendTranscriptScrollFixture(t, svc, spec.TaskID, session.SessionID, transcriptScrollFixtureCount)
	pty.WaitForText(12*time.Second, transcriptScrollBottomAnchor)

	pty.Send("\t")
	pty.Send("kkkkkkkkkkkkkkkkkkkkkkkk")
	pty.WaitForText(12*time.Second, transcriptScrollOlderAnchor)

	appendTranscriptScrollNewest(t, svc, spec.TaskID, session.SessionID)
	time.Sleep(600 * time.Millisecond)
	if strings.Contains(pty.TailOutput(3500), transcriptScrollNewest) {
		t.Fatalf("expected scrolled-up transcript to stay pinned in 40 cols instead of auto-following new content\nterminal output:\n%s", pty.Output())
	}

	pty.Send(strings.Repeat("j", 48))
	pty.WaitForText(12*time.Second, transcriptScrollNewest)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUITranscriptAtBottomAutoFollowsRefreshPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarnessSized(t, svc, 110, 18, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	session := waitForTaskSession(t, svc, spec.TaskID, 8*time.Second)
	appendTranscriptScrollFixture(t, svc, spec.TaskID, session.SessionID, transcriptScrollFixtureCount)
	pty.WaitForText(12*time.Second, transcriptScrollBottomAnchor)

	appendTranscriptScrollNewest(t, svc, spec.TaskID, session.SessionID)
	pty.WaitForText(12*time.Second, transcriptScrollNewest)
	pty.ExitAndWait()
}

func TestTUITranscriptAtBottomAutoFollowsRefreshWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarnessSized(t, svc, 40, 22, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	session := waitForTaskSession(t, svc, spec.TaskID, 8*time.Second)
	appendTranscriptScrollFixture(t, svc, spec.TaskID, session.SessionID, transcriptScrollFixtureCount)
	pty.WaitForText(12*time.Second, transcriptScrollBottomAnchor)

	appendTranscriptScrollNewest(t, svc, spec.TaskID, session.SessionID)
	pty.WaitForText(12*time.Second, transcriptScrollNewest)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUILongTranscriptSummaryWrapsWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarnessSized(t, svc, 40, 28, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	appendTranscriptLongSummaryEvent(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, transcriptLongSummaryPrefix, transcriptLongSummaryTail)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUILongTranscriptRefWrapsWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	pty := startTUIHarnessSized(t, svc, 40, 28, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	appendTranscriptLongRefEvent(t, svc, spec.TaskID)
	pty.WaitForText(12*time.Second, transcriptLongRefPrefix, transcriptLongRefSuffix)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIOverviewInspectorWrapsLongRefsWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setOverviewLongRefs(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 48, 64, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	pty.WaitForText(12*time.Second, overviewLongVerificationRefPrefix, overviewLongVerificationRefSuffix, overviewLongStatusDetailSuffix)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIOverviewInspectorWrapsLongRefsWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setOverviewLongRefs(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 40, 72, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	pty.Send("\t")
	pty.Send("1")
	pty.WaitForText(12*time.Second, "Refs", "detail status/detail/VERY_LONG_STA", "verify verification/reports/VERY_L", overviewLongVerificationRefSuffix)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIOverviewInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setOverviewScrollFixture(t, svc, spec.TaskID, overviewScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 96, 18, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", "TUI session started.")
	pty.Send("\t")
	pty.Send("\t")
	pty.WaitForText(12*time.Second, "Task Summary")
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

func TestTUIOverviewInspectorScrollStaysPinnedAcrossRefreshWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setOverviewScrollFixture(t, svc, spec.TaskID, overviewScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 40, 30, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", "TUI session started.")
	pty.Send("\t")
	pty.Send("\t")
	pty.WaitForText(12*time.Second, "Task Summary")
	for i := 0; i < 120; i++ {
		if !strings.Contains(pty.TailOutput(2200), "Task Summary") {
			break
		}
		pty.Send("j")
	}
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(2200)
	if strings.Contains(view, "Task Summary") {
		t.Fatalf("expected overview inspector to scroll away from the top before refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	appendOverviewScrollNewest(t, svc, spec.TaskID)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(2200)
	if strings.Contains(view, "Task Summary") {
		t.Fatalf("expected scrolled overview inspector to stay away from the top after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, overviewScrollNewest) {
		t.Fatalf("expected scrolled overview inspector to avoid jumping to the newest continuity line after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIWorkersInspectorWrapsLongObjectiveWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, worker := newWorkerContinueService(t)
	setWorkerLongObjective(t, svc, parent.TaskID, worker.WorkerID)
	pty := startTUIHarnessSized(t, svc, 48, 52, parent.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	focusInspectorTab(t, pty, 12*time.Second, "4")
	pty.WaitForText(12*time.Second, "Workers", workerLongObjectivePrefix, "findings/report", ".jsonl", "important blockers")
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIWorkersInspectorWrapsLongObjectiveWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, worker := newWorkerContinueService(t)
	setWorkerLongObjective(t, svc, parent.TaskID, worker.WorkerID)
	pty := startTUIHarnessSized(t, svc, 40, 72, parent.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	focusInspectorTab(t, pty, 12*time.Second, "4")
	pty.WaitForText(12*time.Second, "Workers", worker.WorkerID, "reviewer", "report.jsonl", "important", "blockers")
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIPlanInspectorWrapsLongMetadataWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setPlanLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 48, 64, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	focusInspectorTab(t, pty, 12*time.Second, "2")
	pty.WaitForText(12*time.Second, "Current Execution:", "Primary Criterion:", planLongCriterionPrefix, planLongCriterionSuffix)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIPlanInspectorWrapsLongMetadataWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setPlanLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 40, 84, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	focusInspectorTab(t, pty, 12*time.Second, "2")
	pty.WaitForText(12*time.Second, "Revision: 0", "Primary Criterion:", "Deliver plan", planLongCriterionSuffix)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUICriteriaInspectorWrapsLongMetadataWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setCriteriaLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 48, 88, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	focusInspectorTab(t, pty, 12*time.Second, "3")
	pty.WaitForText(12*time.Second, "Summary:", criteriaLongSummaryPrefix, criteriaLongSummarySuffix, "Current:", criteriaLongCurrentPrefix, criteriaLongCurrentSuffix, criteriaLongStatementSuffix, "Latest criteria summary")
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUICriteriaInspectorWrapsLongMetadataWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setCriteriaLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 40, 96, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	focusInspectorTab(t, pty, 12*time.Second, "3")
	pty.WaitForText(12*time.Second, "Summary:", criteriaLongSummaryPrefix, criteriaLongSummarySuffix, criteriaLongCurrentPrefix, criteriaLongCurrentSuffix, criteriaLongStatementSuffix)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIPlanInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setPlanScrollFixture(t, svc, spec.TaskID, planScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 96, 18, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	focusInspectorTab(t, pty, 12*time.Second, "2")
	pty.WaitForText(12*time.Second, "Current Execution:")
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

func TestTUIPlanInspectorScrollStaysPinnedAcrossRefreshWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setPlanScrollFixture(t, svc, spec.TaskID, planScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 40, 30, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	focusInspectorTab(t, pty, 12*time.Second, "2")
	pty.WaitForText(12*time.Second, "Revision: 0")
	for i := 0; i < 120; i++ {
		if !strings.Contains(pty.TailOutput(2200), "Revision: 0") {
			break
		}
		pty.Send("j")
	}
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(2200)
	if strings.Contains(view, "Revision: 0") {
		t.Fatalf("expected plan inspector to scroll away from the top metadata before refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	appendPlanScrollNewest(t, svc, spec.TaskID)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(2200)
	if strings.Contains(view, "Revision: 0") {
		t.Fatalf("expected scrolled plan inspector to stay away from the top after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, planScrollNewest) {
		t.Fatalf("expected scrolled plan inspector to avoid jumping to the newest step after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUICriteriaInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setCriteriaScrollFixture(t, svc, spec.TaskID, criteriaScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 96, 18, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	focusInspectorTab(t, pty, 12*time.Second, "3")
	pty.WaitForText(12*time.Second, "Summary:")
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

func TestTUICriteriaInspectorScrollStaysPinnedAcrossRefreshWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setCriteriaScrollFixture(t, svc, spec.TaskID, criteriaScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 40, 30, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	focusInspectorTab(t, pty, 12*time.Second, "3")
	pty.WaitForText(12*time.Second, "Summary:")
	for i := 0; i < 160; i++ {
		if !strings.Contains(pty.TailOutput(2200), "Summary:") {
			break
		}
		pty.Send("j")
	}
	waitForCondition(t, 4*time.Second, func() (bool, error) {
		pty.Send("\x0c")
		time.Sleep(200 * time.Millisecond)
		return !strings.Contains(pty.TailOutput(1400), "Summary:"), nil
	})
	view := pty.TailOutput(1400)
	if strings.Contains(view, "Summary:") {
		t.Fatalf("expected criteria inspector to scroll away from the top metadata before refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	appendCriteriaScrollNewest(t, svc, spec.TaskID)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(1400)
	if strings.Contains(view, "Summary:") {
		t.Fatalf("expected scrolled criteria inspector to stay away from the top after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, criteriaScrollNewest) {
		t.Fatalf("expected scrolled criteria inspector to avoid jumping to the newest criterion after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIBlockersInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setBlockersScrollFixture(t, svc, spec.TaskID, blockersScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 96, 18, spec.TaskID)
	pty.WaitForText(12*time.Second, "Scope: blockers inspector line 01")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Composer")
	pty.Send("\t")
	pty.Send("\t")
	pty.Send("5")
	pty.WaitForText(12*time.Second, "Task Approvals")
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

func TestTUIBlockersInspectorScrollStaysPinnedAcrossRefreshWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setBlockersScrollFixture(t, svc, spec.TaskID, blockersScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 40, 30, spec.TaskID)
	pty.WaitForText(12*time.Second, "Approvals")
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Composer")
	pty.Send("\t")
	pty.Send("\t")
	pty.Send("5")
	pty.WaitForText(12*time.Second, "Task Approvals")
	for i := 0; i < 120; i++ {
		if !strings.Contains(pty.TailOutput(2200), "Task Approvals") {
			break
		}
		pty.Send("j")
	}
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(2200)
	if strings.Contains(view, "Task Approvals") {
		t.Fatalf("expected blockers inspector to scroll away from the top before refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	appendBlockersScrollNewest(t, svc, spec.TaskID)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(2200)
	if strings.Contains(view, "Task Approvals") {
		t.Fatalf("expected scrolled blockers inspector to stay away from the top after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, blockersScrollNewest) {
		t.Fatalf("expected scrolled blockers inspector to avoid jumping to the newest watch detail after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIMemoryInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setMemoryScrollFixture(t, svc, memoryScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 96, 18, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	focusInspectorTab(t, pty, 12*time.Second, "6")
	pty.WaitForText(12*time.Second, "Recent Memory Entries")
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

func TestTUIMemoryInspectorScrollStaysPinnedAcrossRefreshWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setMemoryScrollFixture(t, svc, memoryScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 40, 30, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	focusInspectorTab(t, pty, 12*time.Second, "6")
	pty.WaitForText(12*time.Second, "Recent Memory Entries")
	for i := 0; i < 120; i++ {
		if !strings.Contains(pty.TailOutput(2200), "Recent Memory Entries") {
			break
		}
		pty.Send("j")
	}
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view := pty.TailOutput(1400)
	if strings.Contains(view, "Recent Memory Entries") {
		t.Fatalf("expected memory inspector to scroll away from the top before refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	appendMemoryScrollNewest(t, svc)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(1400)
	if strings.Contains(view, "Recent Memory Entries") {
		t.Fatalf("expected scrolled memory inspector to stay away from the top after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, memoryScrollNewest) {
		t.Fatalf("expected scrolled memory inspector to avoid jumping to the newest memory line after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIBlockersInspectorWrapsLongEntriesWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	approval, input := requestLongBlockers(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 44, 72, spec.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", approval.ApprovalID)
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	focusInspectorTab(t, pty, 12*time.Second, "5")
	pty.WaitForText(12*time.Second, "Task Approvals", approvalLongScopePrefix, approvalLongScopeSuffix, "Pending Input", input.RequestID, inputLongPromptPrefix)
	if strings.Contains(pty.TailOutput(5000), "Owned Child Approvals") {
		t.Fatalf("expected local approval to stay out of the owned approvals section\nterminal output:\n%s", pty.Output())
	}
	assertMaxLineWidth(t, pty.Output(), 44)
	pty.ExitAndWait()
}

func TestTUIBlockersInspectorWrapsLongEntriesWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	approval, input := requestLongBlockers(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 40, 88, spec.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", approval.ApprovalID)
	pty.Send("\x1b")
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	focusInspectorTab(t, pty, 12*time.Second, "5")
	pty.WaitForText(12*time.Second, "Task Approvals", approval.ApprovalID, "policy.txt", "Pending Input", input.RequestID, "Provide path", "field target_path")
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIWatchOnlyBlockersInspectorWrapsWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	appendBlockersScrollNewest(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 40, 52, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	focusInspectorTab(t, pty, 12*time.Second, "5")
	pty.WaitForText(12*time.Second, "Watch", "Waiting for watch trigger", "reason waiting_watch", "watch/scroll-blockers")
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIMemoryInspectorWrapsLongEntryWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	promoteLongMemory(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 48, 64, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	focusInspectorTab(t, pty, 12*time.Second, "6")
	pty.WaitForText(12*time.Second, memoryLongSummaryPrefix, memoryLongSummarySuffix)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIMemoryInspectorWrapsLongEntryWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	promoteLongMemory(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 40, 80, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", spec.TaskID)
	focusInspectorTab(t, pty, 12*time.Second, "6")
	pty.WaitForText(12*time.Second, memoryLongSummaryPrefix, memoryLongSummarySuffix)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIProjectTabShowsWorkspaceGraphPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setProjectLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 96, 72, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", "TUI session started.")
	focusInspectorTab(t, pty, 12*time.Second, "7")
	pty.WaitForText(12*time.Second, "Workspace Root:", "Task Binding", projectLongStepPrefix, "Long project review lane")
	pty.ExitAndWait()
}

func TestTUIProjectInspectorScrollStaysPinnedAcrossRefreshPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setProjectScrollFixture(t, svc, projectScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 96, 30, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	focusInspectorTab(t, pty, 12*time.Second, "7")
	pty.WaitForText(12*time.Second, projectScrollVisibleAnchor)
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

func TestTUIProjectInspectorScrollStaysPinnedAcrossRefreshWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setProjectScrollFixture(t, svc, projectScrollFixtureCount)
	pty := startTUIHarnessSized(t, svc, 40, 36, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer")
	focusInspectorTab(t, pty, 12*time.Second, "7")
	pty.WaitForText(12*time.Second, projectScrollVisibleAnchor)
	for i := 0; i < 160; i++ {
		if !strings.Contains(pty.TailOutput(2400), projectScrollVisibleAnchor) {
			break
		}
		pty.Send("j")
	}
	time.Sleep(250 * time.Millisecond)
	view := pty.TailOutput(2400)
	if strings.Contains(view, projectScrollVisibleAnchor) {
		t.Fatalf("expected project inspector to scroll away from the older anchor before refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	appendProjectScrollNewest(t, svc)
	pty.Send("\x0c")
	time.Sleep(400 * time.Millisecond)
	view = pty.TailOutput(2400)
	if strings.Contains(view, projectScrollVisibleAnchor) {
		t.Fatalf("expected scrolled project inspector to stay away from the older anchor after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	if strings.Contains(view, projectScrollNewest) {
		t.Fatalf("expected scrolled project inspector to avoid jumping to the newest step after refresh in 40 cols\nterminal output:\n%s", pty.Output())
	}
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIProjectInspectorWrapsLongEntriesWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setProjectLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 48, 72, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", "TUI session started.")
	focusInspectorTab(t, pty, 12*time.Second, "7")
	pty.WaitForText(12*time.Second, "Workspace Root:", projectLongStepPrefix, projectLongBranchPrefix)
	assertMaxLineWidth(t, pty.Output(), 48)
	pty.ExitAndWait()
}

func TestTUIProjectInspectorWrapsLongEntriesWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	setProjectLongFields(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 40, 88, spec.TaskID)
	pty.WaitForText(12*time.Second, "Composer", "TUI session started.")
	focusInspectorTab(t, pty, 12*time.Second, "7")
	pty.WaitForText(12*time.Second, "Workspace Root:", projectLongStepPrefix, projectLongBranchPrefix)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIApprovalModalWrapsLongScopeAndReasonWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	record := requestLongApproval(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 44, 36, spec.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, approvalLongScopePrefix, approvalLongScopeSuffix, approvalLongReasonSuffix)
	assertMaxLineWidth(t, pty.Output(), 44)
	pty.ExitAndWait()
}

func TestTUIApprovalModalWrapsLongScopeAndReasonWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	record := requestLongApproval(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 40, 44, spec.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, "scope/VERY_LONG_APPROVAL_SCOPE_TOK", approvalLongReasonSuffix)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

func TestTUIOwnedApprovalModalShowsChildStateAndBlockedReasonPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, parent, worker, record := newOwnedApprovalContinueService(t)
	pty := startTUIHarnessSized(t, svc, 60, 36, parent.TaskID)
	pty.WaitForText(12*time.Second, "Approvals", record.ApprovalID, worker.WorkerID, "Child State: blocked", "Blocked: blocked_policy")
	pty.ExitAndWait()
}

func TestTUIInputModalWrapsLongPromptWithinNarrowPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	request := requestLongInput(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 44, 36, spec.TaskID)
	pty.WaitForText(12*time.Second, "Input Request", request.RequestID, inputLongPromptPrefix, inputLongPromptSuffix)
	assertMaxLineWidth(t, pty.Output(), 44)
	pty.ExitAndWait()
}

func TestTUIInputModalWrapsLongPromptWithinFortyColsPTY(t *testing.T) {
	requireRealPTYSmoke(t)

	svc, spec := newTestService(t)
	request := requestLongInput(t, svc, spec.TaskID)
	pty := startTUIHarnessSized(t, svc, 40, 44, spec.TaskID)
	pty.WaitForText(12*time.Second, "Input Request", request.RequestID, inputLongPromptPrefix, inputLongPromptSuffix)
	assertMaxLineWidth(t, pty.Output(), 40)
	pty.ExitAndWait()
}

type ptyHarness struct {
	t      *testing.T
	cmd    *exec.Cmd
	ptmx   *os.File
	cancel context.CancelFunc
	output bytes.Buffer
	done   chan struct{}
	waited bool
}

func startTUIHarness(t *testing.T, svc *ngenrt.Service, taskID ...string) *ptyHarness {
	return startTUIHarnessSizedWithTimeoutAndEnv(t, svc, 120, 40, 45*time.Second, nil, taskID...)
}

func createTestEditorScript(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "editor.sh")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %q > \"$1\"\n", content)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write editor script: %v", err)
	}
	return path
}

func startTUIHarnessSized(t *testing.T, svc *ngenrt.Service, cols, rows int, taskID ...string) *ptyHarness {
	return startTUIHarnessSizedWithTimeoutAndEnv(t, svc, cols, rows, 45*time.Second, nil, taskID...)
}

func startTUIHarnessSizedWithTimeout(t *testing.T, svc *ngenrt.Service, cols, rows int, timeout time.Duration, taskID ...string) *ptyHarness {
	return startTUIHarnessSizedWithTimeoutAndEnv(t, svc, cols, rows, timeout, nil, taskID...)
}

func startTUIHarnessWithEnv(t *testing.T, svc *ngenrt.Service, extraEnv []string, taskID ...string) *ptyHarness {
	return startTUIHarnessSizedWithTimeoutAndEnv(t, svc, 120, 40, 45*time.Second, extraEnv, taskID...)
}

func startTUIHarnessSizedWithTimeoutAndEnv(t *testing.T, svc *ngenrt.Service, cols, rows int, timeout time.Duration, extraEnv []string, taskID ...string) *ptyHarness {
	args := []string{"tui", "--inline"}
	args = append(args, taskID...)
	return startTUIHarnessCLIArgsSizedWithTimeoutAndEnv(t, svc, args, cols, rows, timeout, extraEnv)
}

func startTUIHarnessCLIArgsSizedWithTimeoutAndEnv(t *testing.T, svc *ngenrt.Service, args []string, cols, rows int, timeout time.Duration, extraEnv []string) *ptyHarness {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, buildTestBinary(t), args...)
	cmd.Dir = svc.Store.WorkspaceRoot
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.Env = append(cmd.Env, extraEnv...)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		cancel()
		t.Fatalf("start pty: %v", err)
	}

	h := &ptyHarness{
		t:      t,
		cmd:    cmd,
		ptmx:   ptmx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go func() {
		_, _ = io.Copy(&h.output, ptmx)
		close(h.done)
	}()
	t.Cleanup(func() {
		h.cleanup()
	})
	return h
}

func (h *ptyHarness) Send(chars string) {
	h.t.Helper()
	for _, b := range []byte(chars) {
		if _, err := h.ptmx.Write([]byte{b}); err != nil {
			h.t.Fatalf("write PTY input %q: %v\nterminal output:\n%s", chars, err, h.Output())
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func (h *ptyHarness) SendBurst(chars string) {
	h.t.Helper()
	if _, err := h.ptmx.Write([]byte(chars)); err != nil {
		h.t.Fatalf("write PTY burst %q: %v\nterminal output:\n%s", chars, err, h.Output())
	}
}

func (h *ptyHarness) SendLine(text string) {
	h.t.Helper()
	h.Send(text)
	time.Sleep(100 * time.Millisecond)
	h.PressEnter()
}

func (h *ptyHarness) PressEnter() {
	h.t.Helper()
	time.Sleep(75 * time.Millisecond)
	h.Send("\r")
}

func (h *ptyHarness) Resize(cols, rows int) {
	h.t.Helper()
	if err := pty.Setsize(h.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		h.t.Fatalf("resize PTY to %dx%d: %v\nterminal output:\n%s", cols, rows, err, h.Output())
	}
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Signal(syscall.SIGWINCH)
	}
	time.Sleep(150 * time.Millisecond)
}

func (h *ptyHarness) WaitForText(timeout time.Duration, needles ...string) {
	h.t.Helper()
	waitForTerminalStrings(h.t, &h.output, timeout, needles...)
}

func (h *ptyHarness) WaitForRawText(timeout time.Duration, needles ...string) {
	h.t.Helper()
	waitForRawTerminalStrings(h.t, &h.output, timeout, needles...)
}

func (h *ptyHarness) Wait() {
	h.t.Helper()
	if h.waited {
		return
	}
	h.waited = true
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- h.cmd.Wait()
	}()
	var err error
	select {
	case err = <-waitDone:
	case <-time.After(5 * time.Second):
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
		err = <-waitDone
		h.t.Fatalf("timed out waiting for tui helper to exit\nterminal output:\n%s", h.Output())
	}
	if err != nil {
		h.t.Fatalf("wait tui helper: %v\nterminal output:\n%s", err, h.Output())
	}
	_ = h.ptmx.Close()
	<-h.done
	h.cancel()
}

func (h *ptyHarness) ExitAndWait() {
	h.t.Helper()
	view := h.Output()
	if outputShowsActivePrompt(view) {
		h.t.Fatalf("refusing to quit while a turn is still active\nterminal output:\n%s", h.Output())
	}
	h.Send("\x1b")
	time.Sleep(150 * time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	_, _ = h.ptmx.Write([]byte{0x03})
	h.Wait()
}

func waitForTUISessionStarted(t *testing.T, pty *ptyHarness, timeout time.Duration) {
	t.Helper()
	pty.WaitForText(timeout, "TUI session started.")
}

func waitForTUITaskReady(t *testing.T, pty *ptyHarness, timeout time.Duration, taskID string) {
	t.Helper()
	pty.WaitForText(timeout, "TUI session started.", taskID)
}

func focusInspectorTab(t *testing.T, pty *ptyHarness, timeout time.Duration, key string) {
	t.Helper()
	waitForTUISessionStarted(t, pty, timeout)
	pty.Send("\t")
	pty.Send("\t")
	pty.WaitForText(timeout, "Enter action")
	if strings.TrimSpace(key) != "" {
		pty.Send(key)
	}
}

func (h *ptyHarness) FocusComposer() {
	h.t.Helper()
	if outputShowsComposerFocus(h.Output()) {
		return
	}
	for i := 0; i < 3; i++ {
		h.Send("\t")
		view := h.Output()
		if outputShowsComposerFocus(view) {
			return
		}
		time.Sleep(75 * time.Millisecond)
	}
	h.t.Fatalf("failed to focus composer before exit\nterminal output:\n%s", h.Output())
}

func outputShowsComposerFocus(view string) bool {
	view = strings.TrimRight(view, "\n ")
	if idx := strings.LastIndex(view, "focus=composer"); idx >= 0 {
		return true
	}
	composerIdx := lastIndexAny(view, []string{"Enter send", "/approvals", "/picker"})
	actionIdx := lastIndexAny(view, []string{"Enter action", "a approvals", "Esc, Enter, or ? close help"})
	if composerIdx == -1 {
		return false
	}
	return composerIdx > actionIdx
}

func outputShowsActivePrompt(view string) bool {
	view = strings.TrimRight(view, "\n ")
	activeIdx := lastIndexAny(view, []string{"Running prompt...", "Working...", "run=active"})
	if activeIdx == -1 {
		return false
	}
	idleIdx := lastIndexAny(view, []string{
		"Prompt completed.",
		"Session cancelled.",
		"Approval approved.",
		"Input response recorded.",
		"Review refreshed.",
		"Worker continued.",
	})
	return activeIdx > idleIdx
}

func lastIndexAny(view string, needles []string) int {
	best := -1
	for _, needle := range needles {
		if idx := strings.LastIndex(view, needle); idx > best {
			best = idx
		}
	}
	return best
}

func (h *ptyHarness) Output() string {
	return sanitizeTerminalOutput(h.output.String())
}

func (h *ptyHarness) RawOutput() string {
	return h.output.String()
}

func (h *ptyHarness) TailOutput(limit int) string {
	view := h.Output()
	if limit <= 0 || len(view) <= limit {
		return view
	}
	return view[len(view)-limit:]
}

func (h *ptyHarness) cleanup() {
	if h.waited {
		return
	}
	h.cancel()
	if h.ptmx != nil {
		_ = h.ptmx.Close()
	}
	if h.cmd != nil && h.cmd.Process != nil && h.cmd.ProcessState == nil {
		_ = h.cmd.Process.Kill()
		_, _ = h.cmd.Process.Wait()
	}
	if h.done != nil {
		<-h.done
	}
}

func requireRealPTYSmoke(t *testing.T) {
	t.Helper()
	if os.Getenv("NGEN_RUN_TUI_PTY_SMOKE") != "1" {
		t.Skip("set NGEN_RUN_TUI_PTY_SMOKE=1 to run the real PTY smoke tests")
	}
}

var (
	testBinaryOnce sync.Once
	testBinaryPath string
	testBinaryErr  error
)

func buildTestBinary(t *testing.T) string {
	t.Helper()
	testBinaryOnce.Do(func() {
		cwd, err := os.Getwd()
		if err != nil {
			testBinaryErr = fmt.Errorf("getwd: %w", err)
			return
		}
		repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))
		dir, err := os.MkdirTemp("", "ngen-tui-bin-*")
		if err != nil {
			testBinaryErr = fmt.Errorf("mkdir temp bin dir: %w", err)
			return
		}
		testBinaryPath = filepath.Join(dir, "ngen")
		cmd := exec.Command("go", "build", "-o", testBinaryPath, "./cmd/ngen")
		cmd.Dir = repoRoot
		if output, err := cmd.CombinedOutput(); err != nil {
			testBinaryErr = fmt.Errorf("build binary: %w\n%s", err, string(output))
			return
		}
	})
	if testBinaryErr != nil {
		t.Fatal(testBinaryErr)
	}
	return testBinaryPath
}

func sanitizeTerminalOutput(raw string) string {
	csi := regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	osc := regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	raw = osc.ReplaceAllString(raw, "")
	raw = csi.ReplaceAllString(raw, "")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	raw = strings.ReplaceAll(raw, "\x00", "")
	return raw
}

func waitForTerminalStrings(t *testing.T, output *bytes.Buffer, timeout time.Duration, needles ...string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		view := sanitizeTerminalOutput(output.String())
		for _, needle := range needles {
			if !strings.Contains(view, needle) {
				goto next
			}
		}
		return
	next:
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for terminal text %q\nterminal output:\n%s", strings.Join(needles, ", "), sanitizeTerminalOutput(output.String()))
}

func waitForRawTerminalStrings(t *testing.T, output *bytes.Buffer, timeout time.Duration, needles ...string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		view := output.String()
		for _, needle := range needles {
			if !strings.Contains(view, needle) {
				goto next
			}
		}
		return
	next:
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for raw terminal text %q\nraw terminal output:\n%q", strings.Join(needles, ", "), output.String())
}

func waitForTaskState(t *testing.T, svc *ngenrt.Service, taskID string, want task.StateName, timeout time.Duration) {
	t.Helper()
	waitForCondition(t, timeout, func() (bool, error) {
		status, err := svc.Status(context.Background(), taskID)
		if err != nil {
			return false, err
		}
		return status.State == want, nil
	})
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		done, err := fn()
		if err != nil {
			lastErr = err
		}
		if done {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("timed out waiting for condition: %v", lastErr)
	}
	t.Fatal("timed out waiting for condition")
}

func newMultiTaskService(t *testing.T, count int) (*ngenrt.Service, []task.Spec) {
	t.Helper()
	if count < 1 {
		t.Fatalf("count must be >= 1")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	specs := make([]task.Spec, 0, count)
	for i := 0; i < count; i++ {
		spec, err := svc.Create(context.Background(), task.TaskFile{
			Kind:      task.KindCoding,
			Title:     fmt.Sprintf("demo %d", i+1),
			Objective: fmt.Sprintf("verify coding flow %d", i+1),
			SuccessCriteria: []task.SuccessCriterion{
				{ID: fmt.Sprintf("SC-%03d", i+1), Statement: "go test passes"},
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

func newLongPickerService(t *testing.T) (*ngenrt.Service, []task.Spec) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	svc := ngenrt.New(dir, task.DefaultConfig())
	titles := []string{
		"observability rollout for workspace backed repair criteria",
		"review child workspace handoff and reconcile evidence",
	}
	specs := make([]task.Spec, 0, len(titles))
	for i, title := range titles {
		spec, err := svc.Create(context.Background(), task.TaskFile{
			Kind:      task.KindCoding,
			Title:     title,
			Objective: fmt.Sprintf("exercise narrow picker layout %d", i+1),
			SuccessCriteria: []task.SuccessCriterion{
				{ID: fmt.Sprintf("SC-%03d", i+1), Statement: "go test passes"},
			},
			WorkspaceRoot: dir,
		})
		if err != nil {
			t.Fatalf("create long picker task %d: %v", i+1, err)
		}
		specs = append(specs, spec)
	}
	return svc, specs
}

func newProjectNavigationService(t *testing.T) (*ngenrt.Service, []task.Spec) {
	t.Helper()
	svc, specs := newMultiTaskService(t, 2)
	return bindProjectNavigation(t, svc, specs)
}

func newLongTaskNavigationService(t *testing.T) (*ngenrt.Service, []task.Spec) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	svc := ngenrt.New(dir, task.DefaultConfig())
	titles := []string{taskNavLongCurrentTitleValue, taskNavLongRelatedTitleValue}
	specs := make([]task.Spec, 0, len(titles))
	for i, title := range titles {
		spec, err := svc.Create(context.Background(), task.TaskFile{
			Kind:      task.KindCoding,
			Title:     title,
			Objective: fmt.Sprintf("exercise narrow tasks layout %d", i+1),
			SuccessCriteria: []task.SuccessCriterion{
				{ID: fmt.Sprintf("SC-%03d", i+1), Statement: "go test passes"},
			},
			WorkspaceRoot: dir,
		})
		if err != nil {
			t.Fatalf("create long task navigation task %d: %v", i+1, err)
		}
		specs = append(specs, spec)
	}
	return bindProjectNavigation(t, svc, specs)
}

func bindProjectNavigation(t *testing.T, svc *ngenrt.Service, specs []task.Spec) (*ngenrt.Service, []task.Spec) {
	t.Helper()
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

func newLongRunService(t *testing.T) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")

	cfg := task.DefaultConfig()
	cfg.Verification.CodingCommands = [][]string{{"bash", "-lc", "sleep 10"}}
	writeConfigFile(t, dir, cfg)

	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "slow verify",
		Objective: "exercise interrupt flow",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "verification passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create long-run task: %v", err)
	}
	return svc, spec
}

func newQueuedPromptService(t *testing.T) (*ngenrt.Service, task.Spec) {
	t.Helper()
	return newQueuedPromptServiceWithVerifyDelay(t, 2*time.Second)
}

func newQueuedPromptServiceWithVerifyDelay(t *testing.T, verifyDelay time.Duration) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "demo_test.go"), "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2,3)=%d, want 5\", got)\n\t}\n}\n")
	sleepSeconds := int(verifyDelay / time.Second)
	if sleepSeconds <= 0 {
		sleepSeconds = 1
	}
	writeFile(t, filepath.Join(dir, "verify.sh"), fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\nsleep %d\ngo test ./...\n", sleepSeconds))
	if err := os.Chmod(filepath.Join(dir, "verify.sh"), 0o755); err != nil {
		t.Fatalf("chmod verify.sh: %v", err)
	}

	cfg := task.DefaultConfig()
	cfg.Verification.CodingCommands = [][]string{{"./verify.sh"}}
	writeConfigFile(t, dir, cfg)

	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "queued follow-up",
		Objective: "exercise queued follow-up prompts in the TUI",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "`./verify.sh` passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create queued-follow-up task: %v", err)
	}
	return svc, spec
}

func newReviewHandoffRecoveryService(t *testing.T) (*ngenrt.Service, task.Spec) {
	t.Helper()
	svc, spec := newTestService(t)
	snapshot, _, err := svc.Run(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("run review handoff recovery task: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done snapshot before deleting handoff, got %+v", snapshot)
	}
	handoffPath := filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md")
	if err := os.Remove(handoffPath); err != nil {
		t.Fatalf("remove handoff: %v", err)
	}
	return svc, spec
}

func newReviewHandoffCriteriaBlockedService(t *testing.T) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs gate\n")
	svc := ngenrt.New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "blocked handoff review",
		Objective: "review docs",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "README.md mentions `alpha`"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create blocked handoff review task: %v", err)
	}
	snapshot, _, err := svc.Run(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("run blocked handoff review task: %v", err)
	}
	if snapshot.State != task.StateBlocked || snapshot.StatusReasonCode != "blocked_review" {
		t.Fatalf("expected blocked review snapshot before deleting handoff, got %+v", snapshot)
	}
	handoffPath := filepath.Join(svc.Store.TaskRoot(spec.TaskID), "handoff.md")
	if err := os.Remove(handoffPath); err != nil {
		t.Fatalf("remove blocked handoff: %v", err)
	}
	return svc, spec
}

func newWorkerContinueService(t *testing.T) (*ngenrt.Service, task.Spec, task.WorkerContract) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")
	svc := ngenrt.New(dir, task.DefaultConfig())
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "reviewer child produces a compiled result"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn reviewer worker: %v", err)
	}
	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# reviewer draft\n")
	return svc, parent, worker
}

func newOwnedApprovalContinueService(t *testing.T) (*ngenrt.Service, task.Spec, task.WorkerContract, task.ApprovalRecord) {
	t.Helper()
	svc, parent, worker := newWorkerContinueService(t)
	record, err := svc.RequestApproval(context.Background(), worker.ChildTaskID, "manual step", "worker asks parent")
	if err != nil {
		t.Fatalf("request worker approval: %v", err)
	}
	return svc, parent, worker, record
}

func newWorkerSelectionActiveService(t *testing.T) (*ngenrt.Service, task.Spec, task.WorkerContract, task.WorkerContract, task.ApprovalRecord) {
	t.Helper()
	svc, parent, first := newWorkerContinueService(t)
	record, err := svc.RequestApproval(context.Background(), first.ChildTaskID, "manual step", "first worker asks parent")
	if err != nil {
		t.Fatalf("request first worker approval: %v", err)
	}
	second := spawnTrailingWorker(t, svc, parent.TaskID, "review follow-up parent docs")
	return svc, parent, first, second, record
}

func newWorkerSelectionContinueService(t *testing.T) (*ngenrt.Service, task.Spec, task.WorkerContract, task.WorkerContract, task.ApprovalRecord) {
	t.Helper()
	svc, parent, first, second, record := newWorkerSelectionActiveService(t)
	secondApproval, err := svc.RequestApproval(context.Background(), second.ChildTaskID, "manual step", "second worker asks parent")
	if err != nil {
		t.Fatalf("request second worker approval: %v", err)
	}
	if _, err := svc.DecideApproval(context.Background(), parent.TaskID, secondApproval.ApprovalID, "approved"); err != nil {
		t.Fatalf("approve second worker owned approval: %v", err)
	}
	return svc, parent, first, second, record
}

func spawnTrailingWorker(t *testing.T, svc *ngenrt.Service, parentTaskID, objective string) task.WorkerContract {
	t.Helper()
	worker, err := svc.SpawnWorker(context.Background(), parentTaskID, "reviewer", objective)
	if err != nil {
		t.Fatalf("spawn trailing worker: %v", err)
	}
	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# reviewer draft\n")
	return worker
}

func spawnLeadingWorker(t *testing.T, svc *ngenrt.Service, parentTaskID, workerID, objective string) task.WorkerContract {
	t.Helper()
	worker := spawnTrailingWorker(t, svc, parentTaskID, objective)
	originalPath := filepath.Join(svc.Store.WorkerRoot(parentTaskID), worker.WorkerID+".json")
	if err := os.Remove(originalPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove original worker contract: %v", err)
	}
	worker.WorkerID = workerID
	worker.UpdatedAt = task.Now()
	if err := svc.Store.SaveWorkerContract(worker); err != nil {
		t.Fatalf("save leading worker contract: %v", err)
	}
	return worker
}

func newLongWorkerListService(t *testing.T, count int) (*ngenrt.Service, task.Spec, []task.WorkerContract) {
	t.Helper()
	if count < 1 {
		t.Fatalf("count must be >= 1")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")
	cfg := task.DefaultConfig()
	if cfg.Subagents.MaxWorkersPerTask < count {
		cfg.Subagents.MaxWorkersPerTask = count
	}
	svc := ngenrt.New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent long worker list",
		Objective: "manage a long reviewer worker list",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "selected worker can be continued from the long list"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	workers := make([]task.WorkerContract, 0, count)
	for i := 0; i < count; i++ {
		worker := spawnTrailingWorker(t, svc, parent.TaskID, fmt.Sprintf("review parent docs chunk %02d", i+1))
		workers = append(workers, worker)
	}
	return svc, parent, workers
}

func setWorkerContinueChildReady(t *testing.T, svc *ngenrt.Service, parentTaskID string, worker task.WorkerContract) task.ApprovalRecord {
	t.Helper()
	record, err := svc.RequestApproval(context.Background(), worker.ChildTaskID, "manual step", "worker asks parent")
	if err != nil {
		t.Fatalf("request worker approval for continue_child: %v", err)
	}
	if _, err := svc.DecideApproval(context.Background(), parentTaskID, record.ApprovalID, "approved"); err != nil {
		t.Fatalf("approve worker approval for continue_child: %v", err)
	}
	return record
}

const (
	workersLongListCount       = 6
	workersLongListTargetIndex = 4
	altScreenEnterSeq          = "\x1b[?1049h"
	altScreenExitSeq           = "\x1b[?1049l"
)

const (
	transcriptTSMessage1 = "2099-01-01T00:00:01Z"
	transcriptTSMessage2 = "2099-01-01T00:00:02Z"
	transcriptTSMessage3 = "2099-01-01T00:00:03Z"
	transcriptTSEvent1   = "2099-01-01T00:00:04Z"
	transcriptTSEvent2   = "2099-01-01T00:00:05Z"

	transcriptMessageAlpha = "operator prompt alpha"
	transcriptMessageBeta  = "runtime note beta"
	transcriptMessageGamma = "operator follow-up gamma"
	transcriptEventStale   = "event stale delta"
	transcriptEventNewest  = "event newest epsilon"
	transcriptRefStale     = "stale.log"
	transcriptRefTrace     = "trace.log"
	transcriptRefStatus    = "status.json"

	transcriptScrollFixtureCount = 12
	transcriptScrollOlderAnchor  = "scroll transcript line 06"
	transcriptScrollBottomAnchor = "scroll transcript line 12"
	transcriptScrollNewest       = "scroll transcript line 13 newest"

	transcriptLongSummaryTS     = "2099-01-03T00:00:01Z"
	transcriptLongRefTS         = "2099-01-03T00:00:02Z"
	transcriptLongSummaryPrefix = `{"payload":"LONGSUMMARYTOKEN`
	transcriptLongSummaryTail   = `summary-tail-marker"}`
	transcriptLongSummaryValue  = `{"payload":"LONGSUMMARYTOKENABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz","tail":"summary-tail-marker"}`
	transcriptLongRefPrefix     = "reports/"
	transcriptLongRefSuffix     = "final_trace.jsonl"
	transcriptLongRefValue      = "workspace/output/VERY_LONG_REF_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/reports/final_trace.jsonl"

	overviewLongVerificationRefPrefix = "verification/reports/"
	overviewLongVerificationRefSuffix = "sum.md"
	overviewLongVerificationRefValue  = "verification/reports/VERY_LONG_VERIFICATION_REF_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/sum.md"
	overviewLongStatusDetailSuffix    = "detail.md"
	overviewLongStatusDetailValue     = "status/detail/VERY_LONG_STATUS_DETAIL_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/detail.md"
	overviewScrollFixtureCount        = 12
	overviewScrollOlderAnchor         = "overview inspector line 06"
	overviewScrollNewest              = "overview inspector line 13 newest"

	workerLongObjectivePrefix = "workspace/output/"
	workerLongObjectiveSuffix = "report.jsonl"
	workerLongObjectiveValue  = "review workspace/output/VERY_LONG_WORKSPACE_PATH_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/findings/report.jsonl and summarize the important blockers"

	planLongExecutionPrefix = "EXEC-VERY-LONG-PLAN-STEP"
	planLongExecutionSuffix = "tail-step"
	planLongExecutionValue  = "EXEC-VERY-LONG-PLAN-STEP-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz-tail-step"
	planLongCriterionPrefix = "Deliver plan criterion"
	planLongCriterionSuffix = "tail-criterion."
	planLongCriterionValue  = "Deliver plan criterion workspace/output/VERY_LONG_PLAN_CRITERION_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/checklist.md tail-criterion."
	planScrollFixtureCount  = 12
	planScrollOlderAnchor   = "plan inspector line 06"
	planScrollNewest        = "plan inspector line 13 newest"

	criteriaLongSummaryPrefix    = "Criteria summary"
	criteriaLongSummarySuffix    = "tail-summary."
	criteriaLongSummaryValue     = "Criteria summary workspace/output/VERY_LONG_CRITERIA_SUMMARY_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/report.md tail-summary."
	criteriaLongCurrentPrefix    = "Current criterion"
	criteriaLongCurrentSuffix    = "tail-current."
	criteriaLongCurrentValue     = "Current criterion workspace/output/VERY_LONG_CURRENT_CRITERION_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/spec.md tail-current."
	criteriaLongStatementSuffix  = "tail-statement."
	criteriaLongStatementValue   = "Confirm criteria statement workspace/output/VERY_LONG_CRITERIA_STATEMENT_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/check.md tail-statement."
	criteriaLongLastSummaryTail  = "tail-last-summary."
	criteriaLongLastSummaryValue = "Latest criteria summary workspace/output/VERY_LONG_CRITERIA_LAST_SUMMARY_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/log.txt tail-last-summary."
	criteriaScrollFixtureCount   = 12
	criteriaScrollOlderAnchor    = "criteria inspector line 06"
	criteriaScrollNewest         = "criteria inspector line 13 newest"
	blockersScrollFixtureCount   = 12
	blockersScrollOlderAnchor    = "blockers inspector line 06"
	blockersScrollNewest         = "watch/scroll-blockers-line-13-newest"
	memoryScrollFixtureCount     = 12
	memoryScrollOlderAnchor      = "memory inspector line 06"
	memoryScrollNewest           = "memory inspector line 13 newest"
	projectScrollFixtureCount    = 12
	projectScrollOlderAnchor     = "project inspector line 06"
	projectScrollNewest          = "project inspector line 13 newest"
	projectScrollVisibleAnchor   = "PRJ-SCROLL-001"

	projectLongExplanationPrefix = "Project explanation"
	projectLongExplanationSuffix = "tail-project-explanation."
	projectLongExplanationValue  = "Project explanation keeps workspace/output/VERY_LONG_PROJECT_EXPLANATION_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/plan.md aligned tail-project-explanation."
	projectLongStepPrefix        = "PRJ-VERY-LONG-STEP"
	projectLongStepSuffix        = "tail-project-step"
	projectLongStepValue         = "PRJ-VERY-LONG-STEP-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz-tail-project-step"
	projectLongBranchPrefix      = "branch-VERY-LONG-PROJECT-BRANCH"
	projectLongBranchSuffix      = "tail-project-branch"
	projectLongBranchValue       = "branch-VERY-LONG-PROJECT-BRANCH-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz-tail-project-branch"
	projectLongStatusRefPrefix   = "status/project/"
	projectLongStatusRefSuffix   = "latest.json"
	projectLongStatusRefValue    = "status/project/VERY_LONG_PROJECT_STATUS_REF_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/latest.json"
	projectLongNoteSuffix        = "tail-project-note."
	projectLongNoteValue         = "Project branch note keeps workspace/output/VERY_LONG_PROJECT_NOTE_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/handoff.md visible tail-project-note."

	projectNavigationCurrentStepID      = "PRJ-NAV-CURRENT"
	projectNavigationCurrentBranchID    = "branch.nav.current"
	projectNavigationSiblingStepID      = "PRJ-NAV-SIBLING"
	projectNavigationSiblingBranchID    = "branch.nav.sibling"
	projectNavigationSiblingStepTitle   = "Project sibling repair lane"
	projectNavigationSiblingBranchTitle = "Project sibling branch"
	taskNavLongCurrentTitlePrefix       = "current task workspace lane"
	taskNavLongCurrentTitleSuffix       = "tail-current-task"
	taskNavLongCurrentTitleValue        = "current task workspace lane VERY_LONG_TASK_NAVIGATION_TITLE_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz tail-current-task"
	taskNavLongRelatedTitlePrefix       = "related task workspace lane"
	taskNavLongRelatedTitleSuffix       = "tail-related-task"
	taskNavLongRelatedTitleValue        = "related task workspace lane VERY_LONG_RELATED_TASK_TITLE_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz tail-related-task"

	approvalLongScopePrefix  = "scope/VERY_LONG_APPROVAL_SCOPE_TOKEN"
	approvalLongScopeSuffix  = "policy"
	approvalLongScopeValue   = "scope/VERY_LONG_APPROVAL_SCOPE_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/policy.txt"
	approvalLongReasonSuffix = "tail-reason-marker."
	approvalLongReasonValue  = "operator review required for workspace/output/VERY_LONG_APPROVAL_REASON_PATH_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/report.json tail-reason-marker."

	inputLongPromptPrefix = "Provide path"
	inputLongPromptSuffix = "tail-prompt-marker."
	inputLongPromptValue  = "Provide path workspace/output/VERY_LONG_INPUT_PROMPT_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/review.txt tail-prompt-marker."

	memoryLongSummaryPrefix = "Decision memory"
	memoryLongSummarySuffix = "memory-tail-marker."
	memoryLongSummaryValue  = "Decision memory keeps workspace/output/VERY_LONG_MEMORY_SUMMARY_TOKEN_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz/report.jsonl in scope memory-tail-marker."
)

func setTUIEventLimit(t *testing.T, svc *ngenrt.Service, limit int) {
	t.Helper()
	cfg := svc.Config
	cfg.TUI.EventLimit = limit
	svc.Config = cfg
	writeConfigFile(t, svc.Store.WorkspaceRoot, cfg)
}

func setTUIAltScreenMode(t *testing.T, svc *ngenrt.Service, mode string) {
	t.Helper()
	cfg := svc.Config
	cfg.TUI.AlternateScreen = mode
	svc.Config = cfg
	writeConfigFile(t, svc.Store.WorkspaceRoot, cfg)
}

func waitForTaskSession(t *testing.T, svc *ngenrt.Service, taskID string, timeout time.Duration) task.Session {
	t.Helper()
	var session task.Session
	waitForCondition(t, timeout, func() (bool, error) {
		sessions, err := svc.ListSessions(context.Background())
		if err != nil {
			return false, err
		}
		for _, candidate := range sessions {
			if candidate.TaskID == taskID {
				session = candidate
				return true, nil
			}
		}
		return false, nil
	})
	return session
}

func appendTranscriptFixture(t *testing.T, svc *ngenrt.Service, taskID, sessionID string) {
	t.Helper()
	records := []task.SessionMessage{
		newTranscriptMessage("MSG-TRANSCRIPT-001", "operator", transcriptMessageAlpha, transcriptTSMessage1, sessionID, taskID),
		newTranscriptMessage("MSG-TRANSCRIPT-002", "runtime", transcriptMessageBeta, transcriptTSMessage2, sessionID, taskID),
		newTranscriptMessage("MSG-TRANSCRIPT-003", "operator", transcriptMessageGamma, transcriptTSMessage3, sessionID, taskID),
	}
	for _, record := range records {
		if err := svc.Store.AppendSessionMessage(record); err != nil {
			t.Fatalf("append session message %s: %v", record.MessageID, err)
		}
	}

	events := []task.Event{
		newTranscriptEvent("EVT-TRANSCRIPT-001", "task_progress", transcriptEventStale, transcriptTSEvent1, taskID, transcriptRefStale),
		newTranscriptEvent("EVT-TRANSCRIPT-002", "task_done", transcriptEventNewest, transcriptTSEvent2, taskID, transcriptRefTrace, transcriptRefStatus),
	}
	for _, record := range events {
		if err := svc.Store.AppendEvent(record); err != nil {
			t.Fatalf("append event %s: %v", record.EventID, err)
		}
	}
}

func appendTranscriptScrollFixture(t *testing.T, svc *ngenrt.Service, taskID, sessionID string, count int) {
	t.Helper()
	for i := 1; i <= count; i++ {
		record := newTranscriptMessage(
			fmt.Sprintf("MSG-TRANSCRIPT-SCROLL-%03d", i),
			"runtime",
			fmt.Sprintf("scroll transcript line %02d", i),
			fmt.Sprintf("2099-01-02T00:00:%02dZ", i),
			sessionID,
			taskID,
		)
		if err := svc.Store.AppendSessionMessage(record); err != nil {
			t.Fatalf("append transcript scroll message %s: %v", record.MessageID, err)
		}
	}
}

func appendTranscriptScrollNewest(t *testing.T, svc *ngenrt.Service, taskID, sessionID string) {
	t.Helper()
	record := newTranscriptMessage(
		"MSG-TRANSCRIPT-SCROLL-NEWEST",
		"runtime",
		transcriptScrollNewest,
		"2099-01-02T00:00:59Z",
		sessionID,
		taskID,
	)
	if err := svc.Store.AppendSessionMessage(record); err != nil {
		t.Fatalf("append newest transcript scroll message: %v", err)
	}
}

func appendTranscriptLongSummaryEvent(t *testing.T, svc *ngenrt.Service, taskID string) {
	t.Helper()
	record := newTranscriptEvent(
		"EVT-TRANSCRIPT-LONG-SUMMARY",
		"task_progress",
		transcriptLongSummaryValue,
		transcriptLongSummaryTS,
		taskID,
	)
	if err := svc.Store.AppendEvent(record); err != nil {
		t.Fatalf("append long transcript summary event: %v", err)
	}
}

func appendTranscriptLongRefEvent(t *testing.T, svc *ngenrt.Service, taskID string) {
	t.Helper()
	record := newTranscriptEvent(
		"EVT-TRANSCRIPT-LONG-REF",
		"task_progress",
		"long ref wrap event",
		transcriptLongRefTS,
		taskID,
		transcriptLongRefValue,
	)
	if err := svc.Store.AppendEvent(record); err != nil {
		t.Fatalf("append long transcript ref event: %v", err)
	}
}

func setOverviewLongRefs(t *testing.T, svc *ngenrt.Service, taskID string) {
	t.Helper()
	state, err := svc.Store.LoadState(taskID)
	if err != nil {
		t.Fatalf("load state for overview refs: %v", err)
	}
	state.StatusDetailRef = overviewLongStatusDetailValue
	state.LastVerificationRef = overviewLongVerificationRefValue
	if err := svc.Store.SaveState(state); err != nil {
		t.Fatalf("save state for overview refs: %v", err)
	}
}

func setOverviewScrollFixture(t *testing.T, svc *ngenrt.Service, taskID string, count int) {
	t.Helper()
	continuity, err := svc.Store.LoadContinuity(taskID)
	if err != nil {
		t.Fatalf("load continuity for overview scroll fixture: %v", err)
	}
	lines := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		lines = append(lines, fmt.Sprintf("overview inspector line %02d", i))
	}
	continuity.Summary = strings.Join(lines, "\n")
	continuity.UpdatedAt = task.Now()
	if err := svc.Store.SaveContinuity(continuity); err != nil {
		t.Fatalf("save overview scroll fixture: %v", err)
	}
}

func appendOverviewScrollNewest(t *testing.T, svc *ngenrt.Service, taskID string) {
	t.Helper()
	continuity, err := svc.Store.LoadContinuity(taskID)
	if err != nil {
		t.Fatalf("load continuity for newest overview line: %v", err)
	}
	summary := strings.TrimRight(continuity.Summary, "\n")
	if summary != "" {
		summary += "\n"
	}
	continuity.Summary = summary + overviewScrollNewest
	continuity.UpdatedAt = task.Now()
	if err := svc.Store.SaveContinuity(continuity); err != nil {
		t.Fatalf("save newest overview line: %v", err)
	}
}

func setWorkerLongObjective(t *testing.T, svc *ngenrt.Service, parentTaskID, workerID string) {
	t.Helper()
	contract, err := svc.Store.LoadWorkerContract(parentTaskID, workerID)
	if err != nil {
		t.Fatalf("load worker contract: %v", err)
	}
	contract.Objective = workerLongObjectiveValue
	if err := svc.Store.SaveWorkerContract(contract); err != nil {
		t.Fatalf("save worker contract: %v", err)
	}
}

func setPlanLongFields(t *testing.T, svc *ngenrt.Service, taskID string) {
	t.Helper()
	plan, err := svc.Store.LoadPlan(taskID)
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}
	plan.CurrentExecutionStepID = planLongExecutionValue
	plan.CurrentSystemStepID = planLongExecutionValue
	if len(plan.Steps) > 0 {
		plan.Steps[0].Notes = planLongCriterionValue
	}
	if err := svc.Store.SavePlan(plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	sprint, err := svc.Store.LoadSprint(taskID)
	if err != nil {
		t.Fatalf("load sprint: %v", err)
	}
	sprint.PrimaryCriterionStatement = planLongCriterionValue
	if err := svc.Store.SaveSprint(sprint); err != nil {
		t.Fatalf("save sprint: %v", err)
	}
}

func setCriteriaLongFields(t *testing.T, svc *ngenrt.Service, taskID string) {
	t.Helper()
	criteria, err := svc.Store.LoadCriteria(taskID)
	if err != nil {
		t.Fatalf("load criteria: %v", err)
	}
	criteria.Summary = criteriaLongSummaryValue
	criteria.CurrentCriterionID = planLongExecutionValue
	criteria.CurrentCriterionStatement = criteriaLongCurrentValue
	if len(criteria.Criteria) > 0 {
		criteria.Criteria[0].Statement = criteriaLongStatementValue
		criteria.Criteria[0].LastSummary = criteriaLongLastSummaryValue
	}
	if err := svc.Store.SaveCriteria(criteria); err != nil {
		t.Fatalf("save criteria: %v", err)
	}
}

func setPlanScrollFixture(t *testing.T, svc *ngenrt.Service, taskID string, count int) {
	t.Helper()
	plan, err := svc.Store.LoadPlan(taskID)
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}
	execution := make([]task.Step, 0, count)
	for i := 1; i <= count; i++ {
		status := task.StepStatusPending
		if i == 1 {
			status = task.StepStatusInProgress
		}
		execution = append(execution, task.Step{
			ID:        fmt.Sprintf("EXEC-SCROLL-%03d", i),
			Kind:      task.StepKindExecution,
			Title:     fmt.Sprintf("plan inspector line %02d", i),
			Status:    status,
			UpdatedAt: fmt.Sprintf("2099-01-04T00:00:%02dZ", i),
		})
	}
	plan.Steps = append(plan.Steps, execution...)
	plan.CurrentExecutionStepID = execution[0].ID
	if err := svc.Store.SavePlan(plan); err != nil {
		t.Fatalf("save plan scroll fixture: %v", err)
	}
}

func appendPlanScrollNewest(t *testing.T, svc *ngenrt.Service, taskID string) {
	t.Helper()
	plan, err := svc.Store.LoadPlan(taskID)
	if err != nil {
		t.Fatalf("load plan for newest step: %v", err)
	}
	plan.Steps = append(plan.Steps, task.Step{
		ID:        "EXEC-SCROLL-NEWEST",
		Kind:      task.StepKindExecution,
		Title:     planScrollNewest,
		Status:    task.StepStatusPending,
		UpdatedAt: "2099-01-04T00:00:59Z",
	})
	if err := svc.Store.SavePlan(plan); err != nil {
		t.Fatalf("save newest plan step: %v", err)
	}
}

func setCriteriaScrollFixture(t *testing.T, svc *ngenrt.Service, taskID string, count int) {
	t.Helper()
	criteria, err := svc.Store.LoadCriteria(taskID)
	if err != nil {
		t.Fatalf("load criteria: %v", err)
	}
	items := make([]task.CriterionStatus, 0, count)
	for i := 1; i <= count; i++ {
		items = append(items, task.CriterionStatus{
			CriterionID: fmt.Sprintf("SCROLL-%03d", i),
			Statement:   fmt.Sprintf("criteria inspector line %02d", i),
			Ordinal:     i,
			Status:      "open",
			Passes:      false,
		})
	}
	criteria.Criteria = items
	criteria.MetCount = 0
	criteria.OpenCount = len(items)
	criteria.CurrentCriterionID = items[0].CriterionID
	criteria.CurrentCriterionStatement = items[0].Statement
	if err := svc.Store.SaveCriteria(criteria); err != nil {
		t.Fatalf("save criteria scroll fixture: %v", err)
	}
}

func appendCriteriaScrollNewest(t *testing.T, svc *ngenrt.Service, taskID string) {
	t.Helper()
	criteria, err := svc.Store.LoadCriteria(taskID)
	if err != nil {
		t.Fatalf("load criteria for newest entry: %v", err)
	}
	nextOrdinal := len(criteria.Criteria) + 1
	criteria.Criteria = append(criteria.Criteria, task.CriterionStatus{
		CriterionID: fmt.Sprintf("SCROLL-%03d", nextOrdinal),
		Statement:   criteriaScrollNewest,
		Ordinal:     nextOrdinal,
		Status:      "open",
		Passes:      false,
	})
	criteria.OpenCount = len(criteria.Criteria)
	if err := svc.Store.SaveCriteria(criteria); err != nil {
		t.Fatalf("save newest criteria entry: %v", err)
	}
}

func setBlockersScrollFixture(t *testing.T, svc *ngenrt.Service, taskID string, count int) {
	t.Helper()
	for i := 1; i <= count; i++ {
		scope := fmt.Sprintf("blockers inspector line %02d", i)
		reason := fmt.Sprintf("blockers inspector reason %02d", i)
		if _, err := svc.RequestApproval(context.Background(), taskID, scope, reason); err != nil {
			t.Fatalf("request blockers scroll approval %02d: %v", i, err)
		}
	}
	if _, err := svc.RequestInput(context.Background(), taskID, "target_path", "Provide blockers scroll input", true); err != nil {
		t.Fatalf("request blockers scroll input: %v", err)
	}
}

func appendBlockersScrollNewest(t *testing.T, svc *ngenrt.Service, taskID string) {
	t.Helper()
	state, err := svc.Store.LoadState(taskID)
	if err != nil {
		t.Fatalf("load state for blockers scroll newest: %v", err)
	}
	state.State = task.StateWaiting
	state.StatusReasonCode = "waiting_watch"
	state.StatusDetailRef = blockersScrollNewest
	state.UpdatedAt = task.Now()
	if err := svc.Store.SaveState(state); err != nil {
		t.Fatalf("save blockers scroll newest state: %v", err)
	}
}

func requestLongApproval(t *testing.T, svc *ngenrt.Service, taskID string) task.ApprovalRecord {
	t.Helper()
	record, err := svc.RequestApproval(context.Background(), taskID, approvalLongScopeValue, approvalLongReasonValue)
	if err != nil {
		t.Fatalf("request long approval: %v", err)
	}
	return record
}

func requestLongInput(t *testing.T, svc *ngenrt.Service, taskID string) task.InputRequestRecord {
	t.Helper()
	record, err := svc.RequestInput(context.Background(), taskID, "target_path", inputLongPromptValue, true)
	if err != nil {
		t.Fatalf("request long input: %v", err)
	}
	return record
}

func requestLongBlockers(t *testing.T, svc *ngenrt.Service, taskID string) (task.ApprovalRecord, task.InputRequestRecord) {
	t.Helper()
	return requestLongApproval(t, svc, taskID), requestLongInput(t, svc, taskID)
}

func promoteLongMemory(t *testing.T, svc *ngenrt.Service, taskID string) task.MemoryEntry {
	t.Helper()
	entry, err := svc.PromoteMemory(context.Background(), taskID, task.MemoryPromotion{
		Kind:    task.MemoryKindTaskDecision,
		Summary: memoryLongSummaryValue,
	}, task.MemorySourceOperator)
	if err != nil {
		t.Fatalf("promote long memory: %v", err)
	}
	return entry
}

func setMemoryScrollFixture(t *testing.T, svc *ngenrt.Service, count int) {
	t.Helper()
	for i := 1; i <= count; i++ {
		if err := svc.Store.AppendMemoryEntry(task.MemoryEntry{
			SchemaVersion:   task.SchemaVersion,
			EntryID:         task.NewID("MEM"),
			TaskID:          fmt.Sprintf("TASK-SCROLL-%02d", i),
			Kind:            task.MemoryKindTaskDecision,
			Source:          task.MemorySourceOperator,
			Scope:           "task",
			Confidence:      "observed",
			FreshnessStatus: "fresh",
			Summary:         fmt.Sprintf("memory inspector line %02d", i),
			Refs:            []string{"progress.md"},
			CreatedAt:       fmt.Sprintf("2099-01-03T00:00:%02dZ", i),
		}); err != nil {
			t.Fatalf("append memory scroll fixture: %v", err)
		}
	}
}

func appendMemoryScrollNewest(t *testing.T, svc *ngenrt.Service) {
	t.Helper()
	if err := svc.Store.AppendMemoryEntry(task.MemoryEntry{
		SchemaVersion:   task.SchemaVersion,
		EntryID:         task.NewID("MEM"),
		TaskID:          "TASK-SCROLL-NEW",
		Kind:            task.MemoryKindTaskDecision,
		Source:          task.MemorySourceOperator,
		Scope:           "task",
		Confidence:      "observed",
		FreshnessStatus: "fresh",
		Summary:         memoryScrollNewest,
		Refs:            []string{"progress.md"},
		CreatedAt:       "2099-01-02T23:59:59Z",
	}); err != nil {
		t.Fatalf("append newest memory line: %v", err)
	}
}

func setProjectLongFields(t *testing.T, svc *ngenrt.Service, taskID string) {
	t.Helper()
	project, err := svc.Store.LoadProject()
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	project.Revision = 7
	project.Explanation = projectLongExplanationValue
	project.CurrentStepID = projectLongStepValue
	project.ReadyStepIDs = []string{projectLongStepValue}
	project.ActiveBranchIDs = []string{projectLongBranchValue}
	project.LastMutationRef = projectLongStatusRefValue
	project.Steps = []task.ProjectStep{
		{
			ID:       projectLongStepValue,
			Title:    "Drive the long-horizon review and repair lane",
			Status:   task.ProjectStepStatusInProgress,
			BranchID: projectLongBranchValue,
			TaskID:   taskID,
			Priority: task.StepPriorityHigh,
			Notes:    projectLongNoteValue,
		},
	}
	project.Branches = []task.ProjectBranch{
		{
			ID:            projectLongBranchValue,
			Title:         "Long project review lane",
			Status:        task.ProjectBranchStatusActive,
			TaskID:        taskID,
			StatusRef:     projectLongStatusRefValue,
			WorkspaceRoot: svc.Store.WorkspaceRoot,
			Notes:         projectLongNoteValue,
		},
	}
	if err := svc.Store.SaveProject(project); err != nil {
		t.Fatalf("save project long fields: %v", err)
	}

	sprint, err := svc.Store.LoadSprint(taskID)
	if err != nil {
		t.Fatalf("load sprint for project focus: %v", err)
	}
	sprint.ProjectFocus = &task.ProjectTaskContext{
		PrimaryStepID:          projectLongStepValue,
		PrimaryStepTitle:       "Drive the long-horizon review and repair lane",
		PrimaryStepStatus:      task.ProjectStepStatusInProgress,
		PrimaryBranchID:        projectLongBranchValue,
		PrimaryBranchTitle:     "Long project review lane",
		PrimaryBranchStatus:    task.ProjectBranchStatusActive,
		BoundStepIDs:           []string{projectLongStepValue},
		BoundBranchIDs:         []string{projectLongBranchValue},
		ReadyProjectStepIDs:    []string{projectLongStepValue},
		ActiveProjectBranchIDs: []string{projectLongBranchValue},
		Refs:                   []string{"workspace:.ngen/project/project.json", projectLongStatusRefValue},
		Notes:                  projectLongNoteValue,
	}
	if err := svc.Store.SaveSprint(sprint); err != nil {
		t.Fatalf("save sprint project focus: %v", err)
	}
}

func setProjectScrollFixture(t *testing.T, svc *ngenrt.Service, count int) {
	t.Helper()
	project, err := svc.Store.LoadProject()
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	project.Revision = count
	project.Explanation = ""
	project.CurrentStepID = "PRJ-SCROLL-001"
	project.ReadyStepIDs = nil
	project.BlockedStepIDs = nil
	project.ActiveBranchIDs = []string{"branch.scroll"}
	project.LastMutationRef = ""
	project.Steps = make([]task.ProjectStep, 0, count)
	project.Branches = []task.ProjectBranch{
		{
			ID:            "branch.scroll",
			Title:         "Project scroll lane",
			Status:        task.ProjectBranchStatusActive,
			WorkspaceRoot: svc.Store.WorkspaceRoot,
		},
	}
	for i := 1; i <= count; i++ {
		status := task.ProjectStepStatusPending
		if i == 1 {
			status = task.ProjectStepStatusInProgress
		}
		project.Steps = append(project.Steps, task.ProjectStep{
			ID:       fmt.Sprintf("PRJ-SCROLL-%03d", i),
			Title:    fmt.Sprintf("project inspector line %02d", i),
			Status:   status,
			BranchID: "branch.scroll",
			Priority: task.StepPriorityMedium,
		})
	}
	if err := svc.Store.SaveProject(project); err != nil {
		t.Fatalf("save project scroll fixture: %v", err)
	}
}

func appendProjectScrollNewest(t *testing.T, svc *ngenrt.Service) {
	t.Helper()
	project, err := svc.Store.LoadProject()
	if err != nil {
		t.Fatalf("load project for newest step: %v", err)
	}
	project.Steps = append(project.Steps, task.ProjectStep{
		ID:       "PRJ-SCROLL-NEWEST",
		Title:    projectScrollNewest,
		Status:   task.ProjectStepStatusPending,
		BranchID: "branch.scroll",
		Priority: task.StepPriorityMedium,
	})
	project.Revision++
	if err := svc.Store.SaveProject(project); err != nil {
		t.Fatalf("save newest project step: %v", err)
	}
}

func newTranscriptMessage(messageID, role, content, ts, sessionID, taskID string) task.SessionMessage {
	return task.SessionMessage{
		SchemaVersion: task.SchemaVersion,
		MessageID:     messageID,
		SessionID:     sessionID,
		TaskID:        taskID,
		Role:          role,
		Content:       content,
		TS:            ts,
	}
}

func newTranscriptEvent(eventID, eventType, summary, ts, taskID string, refs ...string) task.Event {
	return task.Event{
		SchemaVersion: task.SchemaVersion,
		EventID:       eventID,
		TaskID:        taskID,
		TS:            ts,
		Phase:         task.PhaseExecute,
		State:         task.StateActive,
		Type:          eventType,
		Summary:       summary,
		Refs:          append([]string(nil), refs...),
	}
}

func writeConfigFile(t *testing.T, dir string, cfg task.Config) {
	t.Helper()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	writeFile(t, filepath.Join(dir, "ngen.json"), string(data)+"\n")
}
