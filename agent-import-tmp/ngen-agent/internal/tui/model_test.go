package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestPickerViewShowsTasks(t *testing.T) {
	svc, spec := newTestService(t)
	m := newModel(NewBackend(svc), Options{
		PollInterval: time.Second,
		EventLimit:   20,
		ProviderMode: svc.Config.Provider.Mode,
	})
	m.ready = true
	m.width = 120
	m.height = 36
	m.resize()
	m.modal = modalPicker

	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks
	view := sanitizeView(m.View())
	if !strings.Contains(view, sanitizeView(spec.TaskID)) {
		t.Fatalf("picker view missing task id: %s", view)
	}
	if !strings.Contains(view, "Task Picker") {
		t.Fatalf("picker view missing title: %s", view)
	}
}

func TestNewModelWithoutTaskStartsInPicker(t *testing.T) {
	svc, _ := newTestService(t)
	m := newModel(NewBackend(svc), Options{
		PollInterval: time.Second,
		EventLimit:   20,
		ProviderMode: svc.Config.Provider.Mode,
	})
	if m.modal != modalPicker {
		t.Fatalf("expected picker modal on empty task selection, got %v", m.modal)
	}
}

func TestSimpleModeWithoutTaskStartsInComposerAndResumesRecentTask(t *testing.T) {
	svc, spec := newTestService(t)
	m := newModel(NewBackend(svc), Options{
		PollInterval: time.Second,
		EventLimit:   20,
		ProviderMode: svc.Config.Provider.Mode,
		SimpleMode:   true,
	})
	if m.modal != modalNone {
		t.Fatalf("expected simple mode to skip picker, got %v", m.modal)
	}
	if got := m.composer.Placeholder; got != "Type your message..." {
		t.Fatalf("unexpected simple composer placeholder: %q", got)
	}

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected simple mode init command")
	}
	next, _ := m.Update(cmd())
	opened := next.(model)
	if opened.taskID != spec.TaskID {
		t.Fatalf("expected simple mode to resume %s, got %s", spec.TaskID, opened.taskID)
	}
	if opened.modal != modalNone {
		t.Fatalf("expected opened simple model to stay in composer, got modal %v", opened.modal)
	}
}

func TestSimpleModeWithoutExistingTaskCreatesLightweightTask(t *testing.T) {
	svc := newEmptyTestService(t)
	m := newModel(NewBackend(svc), Options{
		PollInterval: time.Second,
		EventLimit:   20,
		ProviderMode: svc.Config.Provider.Mode,
		SimpleMode:   true,
	})

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected simple mode init command")
	}
	next, _ := m.Update(cmd())
	opened := next.(model)
	if strings.TrimSpace(opened.taskID) == "" {
		t.Fatal("expected simple mode to create and open a task")
	}
	spec, err := svc.Store.LoadTask(opened.taskID)
	if err != nil {
		t.Fatalf("load created task: %v", err)
	}
	if spec.Title != "TUI Session" || spec.Kind != task.KindCoding {
		t.Fatalf("unexpected created task: %+v", spec)
	}
}

func TestSimpleModeDoesNotExposeTaskConsoleShortcuts(t *testing.T) {
	svc, spec := newTestService(t)
	m := openSimpleModelForTask(t, svc, spec.TaskID)

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyCtrlO},
		{Type: tea.KeyCtrlT},
		{Type: tea.KeyCtrlB},
	} {
		next, cmd := m.updateMain(key)
		if cmd != nil {
			t.Fatalf("expected simple mode key %s not to open task-console command, got %v", key.String(), cmd)
		}
		m = next.(model)
		if !strings.Contains(m.errorLine, "chat-first") {
			t.Fatalf("expected chat-first diagnostic for %s, got %q", key.String(), m.errorLine)
		}
		if m.modal != modalNone || m.focus == focusInspector {
			t.Fatalf("expected no task-console modal/focus for %s, modal=%v focus=%v", key.String(), m.modal, m.focus)
		}
		m.errorLine = ""
	}

	m.composer.SetValue("/tasks")
	next, cmd := m.submitComposer()
	if cmd != nil {
		t.Fatalf("expected /tasks to be rejected locally in simple mode, got %v", cmd)
	}
	m = next.(model)
	if !strings.Contains(m.errorLine, "chat-first") {
		t.Fatalf("expected /tasks chat-first diagnostic, got %q", m.errorLine)
	}
}

func TestSimpleModeActionPaletteOmitsTaskConsoleActions(t *testing.T) {
	svc, spec := newTestService(t)
	m := openSimpleModelForTask(t, svc, spec.TaskID)
	var sawMission bool
	for _, item := range m.actionPaletteItems() {
		switch item.Command {
		case "/tasks", "/picker", "/back":
			t.Fatalf("simple mode action palette exposed task-console command: %+v", item)
		}
		if item.Command == "/mission" {
			sawMission = true
		}
	}
	if !sawMission {
		t.Fatal("simple mode action palette should expose the mission/goal shortcut")
	}
	view := renderHelpModalForMode(100, true)
	if strings.Contains(view, "Ctrl+O picker") || strings.Contains(view, "/tasks") || strings.Contains(view, "/picker") {
		t.Fatalf("simple mode help should not expose task-console controls:\n%s", view)
	}
	normalizedView := strings.Join(strings.Fields(view), " ")
	if !strings.Contains(normalizedView, "/goal PROMPT") || !strings.Contains(normalizedView, "/mission PROMPT") {
		t.Fatalf("simple mode help should document goal/mission prompt shortcuts:\n%s", view)
	}
	if !strings.Contains(view, "chat") && !strings.Contains(view, "Task switching") {
		t.Fatalf("simple mode help missing chat-first/task boundary:\n%s", view)
	}
}

func TestSimpleModeTranscriptFocusDoesNotOpenPicker(t *testing.T) {
	svc, spec := newTestService(t)
	m := openSimpleModelForTask(t, svc, spec.TaskID)

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatalf("expected no command while tabbing focus in simple mode, got %v", cmd)
	}
	m = next.(model)
	if m.focus != focusTranscript {
		t.Fatalf("expected transcript focus after tab, got %v", m.focus)
	}

	next, cmd = m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if cmd != nil {
		t.Fatalf("expected simple mode p key not to open picker command, got %v", cmd)
	}
	m = next.(model)
	if m.modal != modalNone {
		t.Fatalf("expected simple mode p key to keep modal closed, got %v", m.modal)
	}
	if !strings.Contains(m.errorLine, "chat-first") {
		t.Fatalf("expected chat-first diagnostic for simple mode p key, got %q", m.errorLine)
	}
}

func TestSimpleModeDetailsDrawerOpensFromTranscriptWithoutLeavingComposerPath(t *testing.T) {
	svc, spec := newTestService(t)
	setProjectLongFields(t, svc, spec.TaskID)
	m := openSimpleModelForTask(t, svc, spec.TaskID)
	m.width = 100
	m.height = 32
	m.resize()
	m.refreshViews(true)

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatalf("expected no command while focusing transcript, got %v", cmd)
	}
	m = next.(model)
	if m.focus != focusTranscript {
		t.Fatalf("expected transcript focus after first tab, got %v", m.focus)
	}

	next, cmd = m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
	if cmd != nil {
		t.Fatalf("expected no command while switching details tab, got %v", cmd)
	}
	m = next.(model)
	view := sanitizeView(m.View())
	if !strings.Contains(view, "Details") || !strings.Contains(view, "Project Summary") || !strings.Contains(view, projectLongStepPrefix) {
		t.Fatalf("expected project details drawer after numeric tab selection:\n%s", view)
	}

	next, cmd = m.updateMain(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatalf("expected no command while focusing details drawer, got %v", cmd)
	}
	m = next.(model)
	if m.focus != focusInspector {
		t.Fatalf("expected details focus after second tab, got %v", m.focus)
	}

	next, cmd = m.updateMain(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatalf("expected no command while returning to composer, got %v", cmd)
	}
	m = next.(model)
	if m.focus != focusComposer {
		t.Fatalf("expected composer focus after third tab, got %v", m.focus)
	}
	view = sanitizeView(m.View())
	if !strings.Contains(view, "Chat") || strings.Contains(view, "Details") {
		t.Fatalf("expected chat body after returning to composer:\n%s", view)
	}
}

func TestSimpleModeFocusedWorkerDetailsEnterContinuesWorker(t *testing.T) {
	svc, parent, worker := newWorkerContinueService(t)
	m := openSimpleModelForTask(t, svc, parent.TaskID)
	m.width = 100
	m.height = 32
	m.resize()
	m.refreshViews(true)

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatalf("expected no command while focusing transcript, got %v", cmd)
	}
	m = next.(model)
	next, cmd = m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	if cmd != nil {
		t.Fatalf("expected no command while selecting workers details, got %v", cmd)
	}
	m = next.(model)
	if view := sanitizeView(m.View()); !strings.Contains(view, "Workers") || !strings.Contains(view, worker.WorkerID) {
		t.Fatalf("expected worker details drawer before focused action:\n%s", view)
	}

	next, cmd = m.updateMain(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatalf("expected no command while focusing details drawer, got %v", cmd)
	}
	m = next.(model)
	if m.focus != focusInspector {
		t.Fatalf("expected details focus before worker action, got %v", m.focus)
	}
	_, cmd = m.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected focused worker Enter to continue worker")
	}
	if msg := cmd(); msg != (actionFinishedMsg{Status: "Worker continued."}) {
		t.Fatalf("unexpected worker continuation result: %#v", msg)
	}
	result, err := svc.Store.LoadWorkerResult(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker result: %v", err)
	}
	if result.CompletionStatus != "accepted" || result.ReviewStatus != "clear" || result.VerificationStatus != "passed" {
		t.Fatalf("expected accepted reviewer result, got %+v", result)
	}
}

func TestOpenTaskViewSnapshot(t *testing.T) {
	svc, spec := newTestService(t)
	m := newModel(NewBackend(svc), Options{
		TaskID:       spec.TaskID,
		PollInterval: time.Second,
		EventLimit:   20,
		ProviderMode: svc.Config.Provider.Mode,
	})
	m.ready = true
	m.width = 120
	m.height = 40
	m.resize()

	msg := openTaskCmd(m.backend, taskOpenRequest{TaskID: spec.TaskID}, m.opts)()
	next, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("expected polling cmd after open task")
	}
	opened := next.(model)
	view := sanitizeView(opened.View())
	wantParts := []string{
		"ngen tui",
		"Transcript",
		"Inspector",
		"Composer",
		"1 Overview",
		"Task Summary",
	}
	for _, part := range wantParts {
		if !strings.Contains(view, part) {
			t.Fatalf("view missing %q:\n%s", part, view)
		}
	}
}

func TestSubmitRunCompletesTaskAndRefreshesSnapshot(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/run")

	next, cmd := m.submitComposer()
	if cmd == nil {
		t.Fatal("expected prompt command")
	}
	running := next.(model)
	if !running.running {
		t.Fatalf("expected running model")
	}

	doneMsg := cmd()
	afterDone, refreshCmd := running.Update(doneMsg)
	if refreshCmd == nil {
		t.Fatal("expected refresh command after prompt finish")
	}

	refreshedMsg := refreshCmd()
	refreshed, _ := afterDone.Update(refreshedMsg)
	final := refreshed.(model)
	if final.snapshot.TaskView.Status.State != task.StateDone {
		t.Fatalf("expected done state, got %+v", final.snapshot.TaskView.Status)
	}
	if !strings.Contains(final.View(), "Done") {
		t.Fatalf("expected final view to mention Done, got:\n%s", sanitizeView(final.View()))
	}
}

func TestRefreshAutoSettlesCompletedPrompt(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.promptSeq = 1
	m.runningPromptSeq = 1
	m.activePrompt = "/run"
	m.activePromptBaseEvents = 0
	m.statusLine = "Running prompt..."

	settled := m.snapshot
	settled.Session.LastPrompt = "/run"
	settled.SessionSnapshot.LastPrompt = "/run"
	settled.SessionSnapshot.MessageCount = 2
	settled.Messages = []task.SessionMessage{
		{Role: "operator", Content: "/run"},
		{Role: "runtime", Content: "Done Review: Plan completed after the done gate passed."},
	}

	next, cmd := m.Update(refreshMsg{Snapshot: settled})
	if cmd != nil {
		t.Fatalf("expected no follow-up cmd from refresh settle, got %v", cmd)
	}
	updated := next.(model)
	if updated.running {
		t.Fatal("expected refresh to settle the completed prompt")
	}
	if updated.runningPromptSeq != 0 {
		t.Fatalf("expected running prompt seq cleared, got %d", updated.runningPromptSeq)
	}
	if updated.statusLine != "Prompt completed." {
		t.Fatalf("expected prompt completed status, got %q", updated.statusLine)
	}

	next, cmd = updated.Update(promptFinishedMsg{Seq: 1})
	if cmd != nil {
		t.Fatalf("expected stale prompt finished msg to be ignored, got %v", cmd)
	}
	still := next.(model)
	if still.statusLine != "Prompt completed." {
		t.Fatalf("expected stale completion to leave state unchanged, got %q", still.statusLine)
	}
}

func TestStalePromptFinishedDoesNotClearNewerRunningPrompt(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.promptSeq = 2
	m.runningPromptSeq = 2
	m.activePrompt = "/worker_spawn reviewer review the parent output"
	m.activePromptBaseEvents = 2
	m.statusLine = "Running prompt..."

	next, cmd := m.Update(promptFinishedMsg{Seq: 1})
	if cmd != nil {
		t.Fatalf("expected stale completion to return no cmd, got %v", cmd)
	}
	updated := next.(model)
	if !updated.running {
		t.Fatal("expected newer running prompt to stay active")
	}
	if updated.runningPromptSeq != 2 {
		t.Fatalf("expected running prompt seq to stay at 2, got %d", updated.runningPromptSeq)
	}
	if updated.statusLine != "Running prompt..." {
		t.Fatalf("expected status line to remain unchanged, got %q", updated.statusLine)
	}
}

func TestPromptFinishedProviderConfigErrorShowsCompactDiagnostic(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.promptSeq = 1
	m.runningPromptSeq = 1
	m.activePrompt = "use the configured remote provider"
	m.activePromptBaseEvents = 0

	next, _ := m.Update(promptFinishedMsg{
		Seq: 1,
		Err: errors.New("provider mode openai-response requires provider.base_url"),
	})
	updated := next.(model)
	for _, want := range []string{
		"Provider config missing",
		"provider mode openai-response requires provider.base_url",
		"ngen.json [provider]",
		"builtin/command modes do not need remote keys",
	} {
		if !strings.Contains(updated.errorLine, want) {
			t.Fatalf("expected compact provider diagnostic to contain %q, got %q", want, updated.errorLine)
		}
	}
	if updated.statusLine != "Provider configuration blocked the prompt." {
		t.Fatalf("unexpected provider status line %q", updated.statusLine)
	}
	view := sanitizeView(updated.View())
	if !strings.Contains(view, "provider mode openai-response requires provider.base_url") {
		t.Fatalf("expected view to preserve provider error:\n%s", view)
	}
}

func TestAutoSettledPromptAllowsImmediateFollowUpBeforeStaleCompletion(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.promptSeq = 1
	m.runningPromptSeq = 1
	m.activePrompt = "/run"
	m.activePromptBaseEvents = 0
	m.statusLine = "Running prompt..."

	settled := m.snapshot
	settled.Session.LastPrompt = "/run"
	settled.SessionSnapshot.LastPrompt = "/run"
	settled.SessionSnapshot.MessageCount = 2
	settled.Messages = []task.SessionMessage{
		{Role: "operator", Content: "/run"},
		{Role: "runtime", Content: "Done Review: Plan completed after the done gate passed."},
	}

	next, cmd := m.Update(refreshMsg{Snapshot: settled})
	if cmd != nil {
		t.Fatalf("expected no follow-up cmd from refresh settle, got %v", cmd)
	}
	updated := next.(model)
	updated.composer.SetValue("/review")

	next, cmd = updated.submitComposer()
	if cmd == nil {
		t.Fatal("expected local review command after auto-settle")
	}
	reviewed, refreshCmd := next.Update(cmd())
	if refreshCmd == nil {
		t.Fatal("expected local review action to schedule a refresh")
	}
	afterAction := reviewed.(model)
	if afterAction.statusLine != "Review refreshed." {
		t.Fatalf("expected review status after immediate follow-up, got %q", afterAction.statusLine)
	}

	next, cmd = afterAction.Update(refreshCmd())
	if cmd != nil {
		t.Fatalf("expected no extra cmd after review refresh, got %v", cmd)
	}
	afterReview := next.(model)
	if afterReview.statusLine != "Review refreshed." {
		t.Fatalf("expected review status to survive refresh, got %q", afterReview.statusLine)
	}

	next, cmd = afterReview.Update(promptFinishedMsg{Seq: 1})
	if cmd != nil {
		t.Fatalf("expected stale prompt finished msg to be ignored, got %v", cmd)
	}
	still := next.(model)
	if still.statusLine != "Review refreshed." {
		t.Fatalf("expected stale completion to preserve newer follow-up status, got %q", still.statusLine)
	}
}

func TestSubmitComposerQueuesPromptWhileRunning(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.promptSeq = 1
	m.runningPromptSeq = 1
	m.activePrompt = "/run"
	m.composer.SetValue("/memory milestone queued follow-up")

	next, cmd := m.submitComposer()
	if cmd != nil {
		t.Fatalf("expected queued prompt submit to avoid starting a second prompt immediately, got %v", cmd)
	}
	updated := next.(model)
	if !updated.running {
		t.Fatal("expected active prompt to keep running after queueing follow-up")
	}
	if len(updated.queuedPrompts) != 1 || updated.queuedPrompts[0] != "/memory milestone queued follow-up" {
		t.Fatalf("expected queued follow-up prompt, got %+v", updated.queuedPrompts)
	}
	if updated.composer.Value() != "" {
		t.Fatalf("expected composer cleared after queueing, got %q", updated.composer.Value())
	}
	if updated.statusLine != "Queued follow-up prompt." {
		t.Fatalf("unexpected queue status line: %q", updated.statusLine)
	}
}

func TestQueuedPromptPreviewRendersWhileRunning(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{"/memory milestone queued follow-up"}

	view := sanitizeView(m.View())
	for _, want := range []string{"Queued Prompts", "> 1. /memory milestone queued follow-up", "Enter edits selected. Backspace drops selected. Ctrl+P still pulls the newest queued prompt."} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected queued prompt preview to contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Prompt Overlay") {
		t.Fatalf("expected queued prompt preview to use contextual overlay title:\n%s", view)
	}
}

func TestQueuedPromptPreviewCapsVisibleLines(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{
		"/memory milestone first queued follow-up",
		"/memory milestone second queued follow-up",
		"/memory milestone third queued follow-up",
		"/memory milestone fourth queued follow-up",
	}

	view := sanitizeView(m.View())
	for _, want := range []string{
		"Queued Prompts",
		"> 1. /memory milestone first queued follow-up",
		"2. /memory milestone second queued follow-up",
		"3. /memory milestone third queued follow-up",
		"+1 more",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected queued prompt preview to contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "4. /memory milestone fourth queued follow-up") {
		t.Fatalf("expected queued prompt preview to cap visible lines:\n%s", view)
	}
}

func TestQueuedPromptPreviewScrollsSelectedWindow(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{
		"/memory milestone first queued follow-up",
		"/memory milestone second queued follow-up",
		"/memory milestone third queued follow-up",
		"/memory milestone fourth queued follow-up",
	}
	m.queuedPromptPreviewIndex = 3

	view := sanitizeView(m.View())
	for _, want := range []string{
		"Queued",
		"+1 earlier",
		"2. /memory milestone second queued follow-up",
		"3. /memory milestone third queued follow-up",
		"> 4. /memory milestone fourth queued follow-up",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected scrolled queued prompt preview to contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "1. /memory milestone first queued follow-up") {
		t.Fatalf("expected queued preview window to scroll past first item:\n%s", view)
	}
}

func TestSimpleModeComplexViewFitsTerminalHeight(t *testing.T) {
	svc, spec := newTestService(t)
	m := openSimpleModelForTask(t, svc, spec.TaskID)
	m.width = 40
	m.height = 18
	m.running = true
	m.queuedPrompts = []string{
		"/memory first queued follow-up with LONGTOKENABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
		"/memory second queued follow-up with LONGTOKENABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
		"/memory third queued follow-up with LONGTOKENABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
		"/memory fourth queued follow-up with LONGTOKENABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
	}
	m.statusLine = "Prompt is still running with queued follow-ups waiting for the current turn to settle."
	m.errorLine = "Provider reported a long actionable diagnostic that must wrap without pushing the composer away."
	m.resize()
	m.refreshViews(true)

	view := sanitizeView(m.View())
	for _, want := range []string{"Chat", "Queued 4", "Queued: Enter edit", "Ctrl+C interrupt"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected complex simple-mode view to keep %q visible:\n%s", want, view)
		}
	}
	assertMaxLineWidth(t, view, 40)
	assertMaxViewHeight(t, view, m.height)
}

func TestPromptFinishedStartsQueuedFollowUp(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.promptSeq = 1
	m.runningPromptSeq = 1
	m.activePrompt = "/run"
	m.activePromptBaseEvents = 0
	m.queuedPrompts = []string{"/memory milestone queued follow-up"}

	next, cmd := m.Update(promptFinishedMsg{Seq: 1})
	if cmd == nil {
		t.Fatal("expected queued follow-up prompt command after prompt finished")
	}
	updated := next.(model)
	if !updated.running {
		t.Fatal("expected queued follow-up to start running")
	}
	if updated.activePrompt != "/memory milestone queued follow-up" {
		t.Fatalf("expected queued follow-up to become active prompt, got %q", updated.activePrompt)
	}
	if updated.runningPromptSeq != 2 {
		t.Fatalf("expected queued follow-up to advance running prompt sequence, got %d", updated.runningPromptSeq)
	}
	if len(updated.queuedPrompts) != 0 {
		t.Fatalf("expected queued follow-up to be consumed, got %+v", updated.queuedPrompts)
	}
	if updated.statusLine != "Running queued prompt..." {
		t.Fatalf("unexpected queued running status: %q", updated.statusLine)
	}
}

func TestRefreshAutoSettleStartsQueuedFollowUp(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.promptSeq = 1
	m.runningPromptSeq = 1
	m.activePrompt = "/run"
	m.activePromptBaseEvents = 0
	m.queuedPrompts = []string{"/memory milestone queued follow-up"}

	settled := m.snapshot
	settled.Session.LastPrompt = "/run"
	settled.SessionSnapshot.LastPrompt = "/run"
	settled.SessionSnapshot.MessageCount = 2
	settled.Messages = []task.SessionMessage{
		{Role: "operator", Content: "/run"},
		{Role: "runtime", Content: "Done Review: Plan completed after the done gate passed."},
	}

	next, cmd := m.Update(refreshMsg{Snapshot: settled})
	if cmd == nil {
		t.Fatal("expected queued follow-up prompt command after durable settle")
	}
	updated := next.(model)
	if !updated.running {
		t.Fatal("expected queued follow-up to start running after settle")
	}
	if updated.activePrompt != "/memory milestone queued follow-up" {
		t.Fatalf("expected queued follow-up to become active prompt after settle, got %q", updated.activePrompt)
	}
	if len(updated.queuedPrompts) != 0 {
		t.Fatalf("expected queued follow-up to be consumed after settle, got %+v", updated.queuedPrompts)
	}
	if updated.statusLine != "Running queued prompt..." {
		t.Fatalf("unexpected queued settle status: %q", updated.statusLine)
	}
}

func TestRunningQueueRecallRestoresLatestQueuedPromptToComposer(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{"/memory milestone first queued note", "/memory milestone second queued note"}

	next, cmd := m.updateComposer(tea.KeyMsg{Type: tea.KeyCtrlP})
	if cmd != nil {
		t.Fatalf("expected no command when recalling queued prompt for editing, got %v", cmd)
	}
	updated := next.(model)
	if got := updated.composer.Value(); got != "/memory milestone second queued note" {
		t.Fatalf("expected latest queued prompt restored to composer, got %q", got)
	}
	if len(updated.queuedPrompts) != 1 || updated.queuedPrompts[0] != "/memory milestone first queued note" {
		t.Fatalf("expected earlier queued prompt to remain queued, got %+v", updated.queuedPrompts)
	}
	if got := updated.statusLine; got != "Queued prompt restored to composer for editing. 1 follow-up remains queued." {
		t.Fatalf("unexpected queue recall status %q", got)
	}
	if !updated.running {
		t.Fatal("expected active prompt to keep running while editing queued prompt")
	}
}

func TestQueuePreviewSelectionAndEnterEditVisiblePrompt(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{
		"/memory milestone first queued note",
		"/memory milestone second queued note",
	}

	next, cmd := m.updateComposer(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil {
		t.Fatalf("expected no command when moving queued preview selection, got %v", cmd)
	}
	selected := next.(model)
	if selected.queuedPromptPreviewIndex != 1 {
		t.Fatalf("expected queued preview selection to move to index 1, got %d", selected.queuedPromptPreviewIndex)
	}
	view := sanitizeView(selected.View())
	if !strings.Contains(view, "> 2. /memory milestone second queued note") {
		t.Fatalf("expected queued preview to show selected second item:\n%s", view)
	}

	next, cmd = selected.updateComposer(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected no submit when editing selected queued prompt, got %v", cmd)
	}
	updated := next.(model)
	if got := updated.composer.Value(); got != "/memory milestone second queued note" {
		t.Fatalf("expected selected queued prompt restored to composer, got %q", got)
	}
	if len(updated.queuedPrompts) != 1 || updated.queuedPrompts[0] != "/memory milestone first queued note" {
		t.Fatalf("expected non-selected queued prompt to remain queued, got %+v", updated.queuedPrompts)
	}
}

func TestQueuePreviewSelectionMovesBeyondInitialVisibleWindow(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{
		"/memory milestone first queued note",
		"/memory milestone second queued note",
		"/memory milestone third queued note",
		"/memory milestone fourth queued note",
	}

	for range 3 {
		next, cmd := m.updateComposer(tea.KeyMsg{Type: tea.KeyDown})
		if cmd != nil {
			t.Fatalf("expected no command when moving queued preview selection, got %v", cmd)
		}
		m = next.(model)
	}
	if m.queuedPromptPreviewIndex != 3 {
		t.Fatalf("expected queued preview selection to reach index 3, got %d", m.queuedPromptPreviewIndex)
	}
	view := sanitizeView(m.View())
	if !strings.Contains(view, "> 4. /memory milestone fourth queued note") || !strings.Contains(view, "+1 earlier") {
		t.Fatalf("expected scrolled queued selection view:\n%s", view)
	}
}

func TestQueuePreviewBackspaceDropsSelectedPrompt(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{
		"/memory milestone first queued note",
		"/memory milestone second queued note",
	}

	next, _ := m.updateComposer(tea.KeyMsg{Type: tea.KeyDown})
	selected := next.(model)
	next, cmd := selected.updateComposer(tea.KeyMsg{Type: tea.KeyBackspace})
	if cmd != nil {
		t.Fatalf("expected no command when dropping selected queued prompt, got %v", cmd)
	}
	updated := next.(model)
	if len(updated.queuedPrompts) != 1 || updated.queuedPrompts[0] != "/memory milestone first queued note" {
		t.Fatalf("expected unselected queued prompt to remain after drop, got %+v", updated.queuedPrompts)
	}
	if got := updated.statusLine; got != "Dropped selected queued prompt. 1 follow-up remains queued." {
		t.Fatalf("unexpected queued drop status %q", got)
	}
	if got := updated.composer.Value(); got != "" {
		t.Fatalf("expected composer to stay empty after dropping queued prompt, got %q", got)
	}
}

func TestInterruptDropsQueuedPromptsBeforeCancel(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.promptSeq = 1
	m.runningPromptSeq = 1
	m.activePrompt = "/run"
	m.pendingAbort = true
	m.queuedPrompts = []string{
		"/memory milestone first queued follow-up",
		"/memory milestone second queued follow-up",
	}

	next, cmd := m.Update(promptFinishedMsg{Seq: 1})
	if cmd == nil {
		t.Fatal("expected cancel session command after interrupt")
	}
	updated := next.(model)
	if len(updated.queuedPrompts) != 0 {
		t.Fatalf("expected queued prompts dropped on interrupt, got %+v", updated.queuedPrompts)
	}
	if !strings.Contains(updated.statusLine, "Dropping 2 queued prompt(s)") {
		t.Fatalf("expected interrupt status to mention dropped queued prompts, got %q", updated.statusLine)
	}

	doneMsg := cmd()
	done, ok := doneMsg.(actionFinishedMsg)
	if !ok {
		t.Fatalf("expected action finished message after cancel, got %T", doneMsg)
	}
	if done.Err != nil {
		t.Fatalf("cancel session command failed: %v", done.Err)
	}
	events, err := svc.Store.ReadEvents(spec.TaskID)
	if err != nil {
		t.Fatalf("read events after interrupt: %v", err)
	}
	foundAbort := false
	for _, event := range events {
		if event.Type == "aborted" && strings.Contains(event.Summary, "session cancelled by operator") {
			foundAbort = true
			break
		}
	}
	if !foundAbort {
		t.Fatalf("expected interrupt to persist aborted artifact event, got %+v", events)
	}
}

func TestApprovalModalApproveLifecycle(t *testing.T) {
	svc, spec := newTestService(t)
	if _, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review"); err != nil {
		t.Fatalf("request approval: %v", err)
	}
	m := openModelForTask(t, svc, spec.TaskID)
	m.modal = modalApprovals

	next, cmd := m.updateApprovalsModal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("expected approval command")
	}
	doneMsg := cmd()
	afterAction, refreshCmd := next.Update(doneMsg)
	if refreshCmd == nil {
		t.Fatal("expected refresh command")
	}
	refreshedMsg := refreshCmd()
	refreshed, _ := afterAction.Update(refreshedMsg)
	final := refreshed.(model)
	if final.modal != modalNone {
		t.Fatalf("expected approval modal to close after success, got %v", final.modal)
	}
	if final.snapshot.TaskView.Status.State != task.StateActive {
		t.Fatalf("expected active after approval, got %+v", final.snapshot.TaskView.Status)
	}
	if len(pendingApprovalRecords(final.snapshot.Approvals)) != 0 {
		t.Fatalf("expected no pending task approvals, got %+v", final.snapshot.Approvals)
	}
}

func TestInputModalRespondLifecycle(t *testing.T) {
	svc, spec := newTestService(t)
	if _, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true); err != nil {
		t.Fatalf("request input: %v", err)
	}
	m := openModelForTask(t, svc, spec.TaskID)
	m.modal = modalInput
	m.inputBox.SetValue("./demo")

	next, cmd := m.updateInputModal(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected input response command")
	}
	doneMsg := cmd()
	afterAction, refreshCmd := next.Update(doneMsg)
	if refreshCmd == nil {
		t.Fatal("expected refresh command")
	}
	refreshedMsg := refreshCmd()
	refreshed, _ := afterAction.Update(refreshedMsg)
	final := refreshed.(model)
	if final.modal != modalNone {
		t.Fatalf("expected input modal to close after successful response, got %v", final.modal)
	}
	if _, ok := pendingInputRecord(final.snapshot.Inputs); ok {
		t.Fatalf("expected input request to be resolved, got %+v", final.snapshot.Inputs)
	}
	if final.snapshot.TaskView.Status.StatusReasonCode != "" {
		t.Fatalf("expected cleared blocker after input response, got %+v", final.snapshot.TaskView.Status)
	}
}

func TestBlockersTabRendersPendingApprovalAndInput(t *testing.T) {
	svc, spec := newTestService(t)
	approval, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	input, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true)
	if err != nil {
		t.Fatalf("request input: %v", err)
	}
	m := openModelForTask(t, svc, spec.TaskID)
	next, _ := m.updateApprovalsModal(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	m.tab = tabBlockers
	m.refreshViews(false)

	view := sanitizeView(m.View())
	for _, part := range []string{"Task Approvals", approval.ApprovalID, "manual step", "pending", "reason operator review", "Pending Input", input.RequestID, "Provide target path", "field target_path"} {
		if !strings.Contains(view, part) {
			t.Fatalf("expected blockers tab to include %q:\n%s", part, view)
		}
	}
	if strings.Contains(view, "Owned Child Approvals") {
		t.Fatalf("expected local approval to stay out of the owned approvals section:\n%s", view)
	}
}

func TestShouldUseAltScreen(t *testing.T) {
	t.Setenv("ZELLIJ", "")
	if !shouldUseAltScreen("auto", false) {
		t.Fatal("expected auto alt screen outside zellij")
	}
	t.Setenv("ZELLIJ", "1")
	if shouldUseAltScreen("auto", false) {
		t.Fatal("expected auto alt screen disabled in zellij")
	}
	if shouldUseAltScreen("never", false) {
		t.Fatal("expected never to disable alt screen")
	}
	if !shouldUseAltScreen("always", false) {
		t.Fatal("expected always to enable alt screen")
	}
	if shouldUseAltScreen("always", true) {
		t.Fatal("expected inline flag to disable alt screen")
	}
}

func TestPickerAcceptsSlashExit(t *testing.T) {
	svc, _ := newTestService(t)
	m := newModel(NewBackend(svc), Options{
		PollInterval: time.Second,
		EventLimit:   20,
		ProviderMode: svc.Config.Provider.Mode,
	})
	m.modal = modalPicker

	for i, r := range []rune("/exit") {
		next, cmd := m.updatePicker(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
		if i < len("/exit")-1 {
			if cmd != nil {
				t.Fatalf("did not expect quit before command finished")
			}
			continue
		}
		if cmd == nil {
			t.Fatal("expected quit command after /exit")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("expected quit message, got %T", cmd())
		}
	}
}

func TestPickerFilterMatchesTaskTitle(t *testing.T) {
	svc, specs := newMultiTaskService(t, 3)
	m := newModel(NewBackend(svc), Options{
		PollInterval: time.Second,
		EventLimit:   20,
		ProviderMode: svc.Config.Provider.Mode,
	})
	m.ready = true
	m.width = 100
	m.height = 28
	m.resize()
	m.modal = modalPicker
	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks

	for _, r := range []rune("demo 2") {
		next, _ := m.updatePicker(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}

	filtered := m.filteredTasks()
	if len(filtered) != 1 {
		t.Fatalf("expected exactly one filtered task, got %d", len(filtered))
	}
	if filtered[0].TaskID != specs[1].TaskID {
		t.Fatalf("expected filtered task %s, got %s", specs[1].TaskID, filtered[0].TaskID)
	}
	view := sanitizeView(m.View())
	if !strings.Contains(view, "Filter: demo 2") {
		t.Fatalf("picker view missing filter text:\n%s", view)
	}
	if !strings.Contains(view, "demo 2") {
		t.Fatalf("picker view missing matching task title:\n%s", view)
	}
	if strings.Contains(view, "demo 1") || strings.Contains(view, "demo 3") {
		t.Fatalf("picker view should hide non-matching tasks:\n%s", view)
	}
}

func TestPickerFilterRetainsLongQueries(t *testing.T) {
	svc, _ := newTestService(t)
	longTitle := "feature branch observability rollout alpha"
	if _, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     longTitle,
		Objective: "exercise long picker filtering",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-002", Statement: "go test passes"},
		},
		WorkspaceRoot: svc.Store.WorkspaceRoot,
	}); err != nil {
		t.Fatalf("create long-title task: %v", err)
	}

	m := newModel(NewBackend(svc), Options{
		PollInterval: time.Second,
		EventLimit:   20,
		ProviderMode: svc.Config.Provider.Mode,
	})
	m.ready = true
	m.width = 100
	m.height = 28
	m.resize()
	m.modal = modalPicker
	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks

	query := "observability rollout alpha"
	for _, r := range []rune(query) {
		next, _ := m.updatePicker(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}

	if got := m.pickerFilter; got != query {
		t.Fatalf("expected picker filter to retain full query %q, got %q", query, got)
	}
	filtered := m.filteredTasks()
	if len(filtered) != 1 {
		t.Fatalf("expected exactly one filtered task, got %d", len(filtered))
	}
	if filtered[0].Title != longTitle {
		t.Fatalf("expected long-title task to match, got %+v", filtered[0])
	}
}

func TestPickerFilterNoMatchShowsRecoveryHint(t *testing.T) {
	svc, _ := newMultiTaskService(t, 2)
	m := newModel(NewBackend(svc), Options{
		PollInterval: time.Second,
		EventLimit:   20,
		ProviderMode: svc.Config.Provider.Mode,
	})
	m.ready = true
	m.width = 100
	m.height = 28
	m.resize()
	m.modal = modalPicker
	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks
	for _, r := range []rune("missing-task") {
		next, _ := m.updatePicker(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}
	view := sanitizeView(m.View())
	if !strings.Contains(view, "No tasks match the current filter.") {
		t.Fatalf("expected no-match picker hint:\n%s", view)
	}
	if !strings.Contains(view, "Press Backspace to broaden the search or clear the filter.") {
		t.Fatalf("expected recovery hint in picker:\n%s", view)
	}
}

func TestPickerViewWrapsCompactRowsWithinNarrowWidth(t *testing.T) {
	view := renderPickerView(48, 22, []task.TaskListEntry{
		{
			TaskID:           "TASK-PICKER-ALPHA",
			Title:            "observability rollout for workspace backed repair criteria",
			Kind:             task.KindCoding,
			Phase:            task.PhasePlan,
			State:            task.StateBlocked,
			StatusReasonCode: "waiting_input",
			CurrentStepID:    "EXEC-OBS-001",
			UpdatedAt:        "2099-01-01T00:00:01Z",
		},
		{
			TaskID:    "TASK-PICKER-BETA",
			Title:     "short follow-up",
			Kind:      task.KindReviewer,
			Phase:     task.PhaseReview,
			State:     task.StateActive,
			UpdatedAt: "2099-01-01T00:00:02Z",
		},
	}, 0, "", "", "")
	sanitized := sanitizeView(view)
	for _, want := range []string{
		"Task Picker",
		"TASK-PICKER-ALPHA",
		"observability",
		"coding",
		"Plan/Blocked",
		"updated",
		"step EXEC-OBS-001",
		"reason",
		"waiting_input",
		"TASK-PICKER-BETA",
		"short follow-up",
		"reviewer",
		"Review/Active",
	} {
		if !strings.Contains(sanitized, want) {
			t.Fatalf("expected picker view to contain %q:\n%s", want, sanitized)
		}
	}
	assertMaxLineWidth(t, view, 48)
}

func TestNarrowViewWrapsAndShowsComposerQuickActions(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 60
	m.height = 24
	m.resize()
	m.refreshViews(true)

	view := sanitizeView(m.View())
	for _, part := range []string{"Transcript", "Inspector", "Composer", "Quick:", "Ctrl+K actions", "Ctrl+O picker"} {
		if !strings.Contains(view, part) {
			t.Fatalf("narrow view missing %q:\n%s", part, view)
		}
	}
	contextLine := renderComposerContext(m, 60)
	for _, part := range []string{"Quick:", "run", "picker", "actions", "help"} {
		if !strings.Contains(contextLine, part) {
			t.Fatalf("fresh-task quick actions missing %q:\n%s", part, contextLine)
		}
	}
	for _, absent := range []string{"resume", "review", "tasks", "back"} {
		if strings.Contains(contextLine, absent) {
			t.Fatalf("expected fresh-task quick actions to avoid %q:\n%s", absent, contextLine)
		}
	}
	if strings.Contains(view, "Shortcuts:") {
		t.Fatalf("expected idle composer context to stay compact without shortcut inventory:\n%s", view)
	}
	if strings.Contains(view, "/approvals") {
		t.Fatalf("expected composer-focused surface to avoid slash-heavy hinting:\n%s", view)
	}
	for _, absent := range []string{"Ctrl+J newline", "Ctrl+C quit"} {
		if strings.Contains(view, absent) {
			t.Fatalf("expected narrow footer collapse to drop %q:\n%s", absent, view)
		}
	}
	assertMaxLineWidth(t, view, 60)
}

func TestComposerPlaceholderUsesCompactCopy(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)

	view := sanitizeView(m.View())
	if !strings.Contains(view, "Ask, steer, or type run/review/tasks/back") {
		t.Fatalf("expected compact composer placeholder:\n%s", view)
	}
	if strings.Contains(view, "Type a task request, question, or quick action like run, review, tasks, or back") {
		t.Fatalf("expected stale long composer placeholder to be removed:\n%s", view)
	}
}

func TestComposerTitleShowsCompactStateBadges(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{"/memory first queued note", "/memory second queued note"}

	rendered := sanitizeView(renderComposerTitle(m, 80))
	for _, want := range []string{"Composer", "Running", "Queued 2"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected composer title badges to contain %q:\n%s", want, rendered)
		}
	}
}

func TestComposerTitleShowsSwitchingBadge(t *testing.T) {
	svc, specs := newMultiTaskService(t, 2)
	m := openModelForTask(t, svc, specs[0].TaskID)

	next, cmd := m.beginTaskOpen(specs[1].TaskID, true, "Switched to child task.")
	if cmd == nil {
		t.Fatal("expected open-task command")
	}
	switching := next.(model)

	rendered := sanitizeView(renderComposerTitle(switching, 80))
	for _, want := range []string{"Composer", "Switching ->"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected pending-open composer title to contain %q:\n%s", want, rendered)
		}
	}
}

func TestComposerContextUsesCompactQueueCopy(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{"/memory first queued note", "/memory second queued note"}

	rendered := renderComposerContext(m, 80)
	if !strings.Contains(strings.Join(strings.Fields(rendered), " "), "Enter edits selected. Backspace drops. Ctrl+P newest.") {
		t.Fatalf("expected compact queued context copy:\n%s", rendered)
	}
	if strings.Contains(rendered, "They will run after the current turn settles.") {
		t.Fatalf("expected queued context to avoid redundant settle prose:\n%s", rendered)
	}
}

func TestComposerContextShowsCompactRunningCopy(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true

	rendered := renderComposerContext(m, 80)
	if !strings.Contains(rendered, "Type to queue the next prompt.") {
		t.Fatalf("expected compact running context copy:\n%s", rendered)
	}
	if strings.Contains(rendered, "Running now.") {
		t.Fatalf("expected stale running context copy to be removed:\n%s", rendered)
	}
}

func TestHeaderUsesCompactStatusBadges(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{"/memory first queued note", "/memory second queued note"}
	m.taskHistory = []string{"TASK-previous"}

	rendered := sanitizeView(renderHeader(
		m.snapshot,
		m.session,
		m.opts.ProviderMode,
		m.focus,
		m.running,
		len(m.queuedPrompts),
		len(m.taskHistory),
		m.pendingTaskOpen,
		96,
	))

	for _, want := range []string{
		fmt.Sprintf("%s / %s", blankDash(string(m.snapshot.TaskView.Status.Phase)), blankDash(string(m.snapshot.TaskView.Status.State))),
		"Running",
		"Queued 2",
		"Back 1",
		m.opts.ProviderMode,
		"Session ",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected compact header to contain %q:\n%s", want, rendered)
		}
	}
	for _, stale := range []string{"provider=", "session=", "queued=", "back=", "focus=", "run=active"} {
		if strings.Contains(rendered, stale) {
			t.Fatalf("expected compact header to remove raw token %q:\n%s", stale, rendered)
		}
	}
}

func TestHeaderCollapseDropsLowPriorityMetadataFirst(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{"/memory first queued note", "/memory second queued note"}
	m.taskHistory = []string{"TASK-previous"}

	rendered := sanitizeView(renderHeader(
		m.snapshot,
		m.session,
		m.opts.ProviderMode,
		m.focus,
		m.running,
		len(m.queuedPrompts),
		len(m.taskHistory),
		m.pendingTaskOpen,
		44,
	))

	for _, want := range []string{
		fmt.Sprintf("%s / %s", blankDash(string(m.snapshot.TaskView.Status.Phase)), blankDash(string(m.snapshot.TaskView.Status.State))),
		"Running",
		"Queued 2",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected narrow header to retain %q:\n%s", want, rendered)
		}
	}
	for _, dropped := range []string{"Back 1", m.opts.ProviderMode, "Session "} {
		if strings.Contains(rendered, dropped) {
			t.Fatalf("expected narrow header to drop low-priority metadata %q:\n%s", dropped, rendered)
		}
	}
	assertMaxLineWidth(t, rendered, 44)
}

func TestComposerQuickActionsPreferResumeAndReviewAfterDurableHistory(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.snapshot.Session.LastPrompt = "/run"
	m.snapshot.SessionSnapshot.LastPrompt = "/run"
	m.snapshot.SessionSnapshot.MessageCount = 2

	quick := m.composerQuickActions()
	want := []string{"resume", "review", "picker", "actions"}
	if strings.Join(quick, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected quick actions for durable-history task: got %v want %v", quick, want)
	}
}

func TestComposerQuickActionsPrioritizeTasksAndBackWhenAvailable(t *testing.T) {
	svc, specs := newProjectNavigationService(t)
	m := openModelForTask(t, svc, specs[0].TaskID)
	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks
	m.taskHistory = []string{specs[1].TaskID}

	quick := m.composerQuickActions()
	want := []string{"run", "tasks", "back", "picker"}
	if strings.Join(quick, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected quick actions for navigable task: got %v want %v", quick, want)
	}
}

func TestNarrowHeaderWrapsLongUnbrokenTitle(t *testing.T) {
	svc, spec := newLongHeaderService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 36
	m.height = 22
	m.resize()
	m.refreshViews(true)

	view := sanitizeView(m.View())
	if !strings.Contains(view, "LONGTOKEN") {
		t.Fatalf("expected wrapped header to retain long title token:\n%s", view)
	}
	assertMaxLineWidth(t, view, 36)
}

func TestRefreshKeepsSessionMessagesWhenEventTailIsBounded(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	appendTranscriptFixture(t, svc, spec.TaskID, m.session.SessionID)
	m.opts.EventLimit = 1

	msg := refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)()
	next, _ := m.Update(msg)
	updated := next.(model)

	if got := len(updated.snapshot.Messages); got != 3 {
		t.Fatalf("expected all session messages to remain visible, got %d", got)
	}
	if got := len(updated.snapshot.Events); got != 1 {
		t.Fatalf("expected bounded event tail of 1, got %d", got)
	}
	view := sanitizeView(updated.View())
	for _, want := range []string{
		transcriptMessageAlpha,
		transcriptMessageBeta,
		transcriptMessageGamma,
		transcriptEventNewest,
		transcriptRefTrace,
		transcriptRefStatus,
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected transcript view to contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, transcriptEventStale) {
		t.Fatalf("expected stale bounded-out event to stay out of the transcript:\n%s", view)
	}
}

func TestBuildTranscriptDedupesDuplicateMessageAndEventIDs(t *testing.T) {
	entries := buildTranscript(
		[]task.SessionMessage{
			newTranscriptMessage("MSG-DUP-001", "operator", "duplicate operator entry", transcriptTSMessage1, "SES-DEDUPE", "TASK-DEDUPE"),
			newTranscriptMessage("MSG-DUP-001", "operator", "duplicate operator entry", transcriptTSMessage1, "SES-DEDUPE", "TASK-DEDUPE"),
			newTranscriptMessage("MSG-DUP-002", "runtime", "unique runtime entry", transcriptTSMessage2, "SES-DEDUPE", "TASK-DEDUPE"),
		},
		[]task.Event{
			newTranscriptEvent("EVT-DUP-001", "task_progress", "duplicate event entry", transcriptTSEvent1, "TASK-DEDUPE", transcriptRefTrace),
			newTranscriptEvent("EVT-DUP-001", "task_progress", "duplicate event entry", transcriptTSEvent1, "TASK-DEDUPE", transcriptRefTrace),
			newTranscriptEvent("EVT-DUP-002", "task_done", "unique event entry", transcriptTSEvent2, "TASK-DEDUPE", transcriptRefStatus),
		},
	)

	if got := len(entries); got != 4 {
		t.Fatalf("expected duplicates to collapse to 4 transcript entries, got %d", got)
	}
	rendered := renderTranscript(entries, 120, false)
	if got := strings.Count(rendered, "duplicate operator entry"); got != 1 {
		t.Fatalf("expected duplicate session message to render once, got %d:\n%s", got, rendered)
	}
	if got := strings.Count(rendered, "duplicate event entry"); got != 1 {
		t.Fatalf("expected duplicate event to render once, got %d:\n%s", got, rendered)
	}
}

func TestTranscriptRendersCommandAndVerificationFailureEvents(t *testing.T) {
	entries := buildTranscript(nil, []task.Event{
		newTranscriptEvent(
			"EVT-COMMAND-FAIL",
			"command_failed",
			"Command failed: go test ./...",
			transcriptTSEvent1,
			"TASK-TRANSCRIPT",
			"command_runs/CMD-fail.json",
		),
		newTranscriptEvent(
			"EVT-VERIFY-FAIL",
			"verification_failed",
			"Verification failed: go test ./...",
			transcriptTSEvent2,
			"TASK-TRANSCRIPT",
			"verification/latest.json",
		),
	})

	rendered := renderTranscript(entries, 80, false)
	for _, want := range []string{
		"COMMAND_FAILED",
		"Command failed: go test ./...",
		"ref: command_runs/CMD-fail.json",
		"VERIFICATION_FAILED",
		"Verification failed: go test ./...",
		"ref: verification/latest.json",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected transcript failure event to contain %q:\n%s", want, rendered)
		}
	}
}

func TestRefreshKeepsTranscriptViewportOffsetWhenScrolledUp(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 96
	m.height = 18
	m.resize()
	m.refreshViews(true)
	appendTranscriptScrollFixture(t, svc, spec.TaskID, m.session.SessionID, transcriptScrollFixtureCount)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	if loaded.transcript.TotalLineCount() <= loaded.transcript.Height {
		t.Fatalf("expected transcript fixture to require scrolling, lines=%d height=%d", loaded.transcript.TotalLineCount(), loaded.transcript.Height)
	}
	loaded.transcript.GotoBottom()
	bottomOffset := loaded.transcript.YOffset
	loaded.transcript.LineUp(18)
	if loaded.transcript.YOffset >= bottomOffset {
		t.Fatalf("expected transcript to move above bottom, before=%d after=%d", bottomOffset, loaded.transcript.YOffset)
	}
	yBefore := loaded.transcript.YOffset

	appendTranscriptScrollNewest(t, svc, spec.TaskID, loaded.session.SessionID)
	next, _ = loaded.Update(refreshCmd(loaded.backend, spec.TaskID, loaded.session.SessionID, loaded.opts, false)())
	updated := next.(model)

	if updated.transcript.YOffset != yBefore {
		t.Fatalf("expected scrolled-up transcript offset to stay pinned across refresh, want %d got %d", yBefore, updated.transcript.YOffset)
	}
	if updated.transcript.AtBottom() {
		t.Fatal("expected scrolled-up transcript to remain away from bottom after refresh")
	}
	view := updated.transcript.View()
	if !strings.Contains(view, transcriptScrollOlderAnchor) {
		t.Fatalf("expected scrolled transcript to keep older anchor visible:\n%s", view)
	}
	if strings.Contains(view, transcriptScrollNewest) {
		t.Fatalf("expected scrolled transcript to avoid auto-following the newest line:\n%s", view)
	}
}

func TestRefreshAutoFollowsTranscriptWhenAtBottom(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 96
	m.height = 18
	m.resize()
	m.refreshViews(true)
	appendTranscriptScrollFixture(t, svc, spec.TaskID, m.session.SessionID, transcriptScrollFixtureCount)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	loaded.transcript.GotoBottom()
	if !loaded.transcript.AtBottom() {
		t.Fatal("expected transcript to start at bottom before auto-follow refresh")
	}

	appendTranscriptScrollNewest(t, svc, spec.TaskID, loaded.session.SessionID)
	next, _ = loaded.Update(refreshCmd(loaded.backend, spec.TaskID, loaded.session.SessionID, loaded.opts, false)())
	updated := next.(model)

	if !updated.transcript.AtBottom() {
		t.Fatal("expected transcript at bottom to keep auto-following newest content")
	}
	if !strings.Contains(updated.transcript.View(), transcriptScrollNewest) {
		t.Fatalf("expected bottom-following transcript to show newest line:\n%s", updated.transcript.View())
	}
}

func TestSimpleModeCompactFrameRendersTranscriptBottomContent(t *testing.T) {
	svc, spec := newTestService(t)
	m := openSimpleModelForTask(t, svc, spec.TaskID)
	m.width = 110
	m.height = 18
	m.resize()
	m.refreshViews(true)
	appendTranscriptScrollFixture(t, svc, spec.TaskID, m.session.SessionID, transcriptScrollFixtureCount)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)
	updated.transcript.GotoBottom()

	view := sanitizeView(updated.View())
	if !strings.Contains(view, transcriptScrollBottomAnchor) {
		t.Fatalf("expected compact simple frame to render bottom transcript content:\n%s", view)
	}
}

func TestTranscriptScrollToBottomRefreshesLatestMessages(t *testing.T) {
	svc, spec := newTestService(t)
	m := openSimpleModelForTask(t, svc, spec.TaskID)
	m.width = 96
	m.height = 18
	m.resize()
	m.refreshViews(true)
	appendTranscriptScrollFixture(t, svc, spec.TaskID, m.session.SessionID, transcriptScrollFixtureCount)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	loaded.focus = focusTranscript
	loaded.syncFocusStates()
	loaded.transcript.GotoBottom()
	loaded.transcript.LineUp(18)
	if loaded.transcript.AtBottom() {
		t.Fatal("expected transcript fixture to be scrolled away from bottom before latest append")
	}

	appendTranscriptScrollNewest(t, svc, spec.TaskID, loaded.session.SessionID)
	current := loaded
	var cmd tea.Cmd
	for i := 0; i < 80; i++ {
		next, nextCmd := current.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		current = next.(model)
		if nextCmd != nil {
			cmd = nextCmd
			break
		}
	}
	if cmd == nil {
		t.Fatal("expected scrolling back to transcript bottom to request a refresh")
	}

	next, _ = current.Update(cmd())
	updated := next.(model)
	if !updated.transcript.AtBottom() {
		t.Fatal("expected refreshed transcript to follow the latest bottom")
	}
	if !strings.Contains(updated.transcript.View(), transcriptScrollNewest) {
		t.Fatalf("expected refreshed transcript to show newest line:\n%s", updated.transcript.View())
	}
}

func TestRefreshKeepsPlanInspectorViewportOffsetWhenScrolled(t *testing.T) {
	svc, spec := newTestService(t)
	setPlanScrollFixture(t, svc, spec.TaskID, planScrollFixtureCount)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 96
	m.height = 18
	m.tab = tabPlan
	m.resize()
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	if loaded.inspector.TotalLineCount() <= loaded.inspector.Height {
		t.Fatalf("expected plan inspector fixture to require scrolling, lines=%d height=%d", loaded.inspector.TotalLineCount(), loaded.inspector.Height)
	}
	loaded.inspector.LineDown(6)
	if loaded.inspector.YOffset == 0 {
		t.Fatal("expected plan inspector viewport to move below the top before refresh")
	}
	yBefore := loaded.inspector.YOffset
	viewBefore := loaded.inspector.View()

	appendPlanScrollNewest(t, svc, spec.TaskID)
	next, _ = loaded.Update(refreshCmd(loaded.backend, spec.TaskID, loaded.session.SessionID, loaded.opts, false)())
	updated := next.(model)

	if updated.inspector.YOffset != yBefore {
		t.Fatalf("expected plan inspector offset to stay pinned across refresh, want %d got %d", yBefore, updated.inspector.YOffset)
	}
	view := updated.inspector.View()
	if view != viewBefore {
		t.Fatalf("expected plan inspector viewport to stay stable across refresh\nbefore:\n%s\nafter:\n%s", viewBefore, view)
	}
	if strings.Contains(view, planScrollNewest) {
		t.Fatalf("expected scrolled plan inspector to avoid auto-jumping to the newest step:\n%s", view)
	}
}

func TestRefreshKeepsCriteriaInspectorViewportOffsetWhenScrolled(t *testing.T) {
	svc, spec := newTestService(t)
	setCriteriaScrollFixture(t, svc, spec.TaskID, criteriaScrollFixtureCount)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 96
	m.height = 18
	m.tab = tabCriteria
	m.resize()
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	if loaded.inspector.TotalLineCount() <= loaded.inspector.Height {
		t.Fatalf("expected criteria inspector fixture to require scrolling, lines=%d height=%d", loaded.inspector.TotalLineCount(), loaded.inspector.Height)
	}
	loaded.inspector.LineDown(6)
	if loaded.inspector.YOffset == 0 {
		t.Fatal("expected criteria inspector viewport to move below the top before refresh")
	}
	yBefore := loaded.inspector.YOffset
	viewBefore := loaded.inspector.View()

	appendCriteriaScrollNewest(t, svc, spec.TaskID)
	next, _ = loaded.Update(refreshCmd(loaded.backend, spec.TaskID, loaded.session.SessionID, loaded.opts, false)())
	updated := next.(model)

	if updated.inspector.YOffset != yBefore {
		t.Fatalf("expected criteria inspector offset to stay pinned across refresh, want %d got %d", yBefore, updated.inspector.YOffset)
	}
	view := updated.inspector.View()
	if view != viewBefore {
		t.Fatalf("expected criteria inspector viewport to stay stable across refresh\nbefore:\n%s\nafter:\n%s", viewBefore, view)
	}
	if strings.Contains(view, criteriaScrollNewest) {
		t.Fatalf("expected scrolled criteria inspector to avoid auto-jumping to the newest criterion:\n%s", view)
	}
}

func TestRefreshKeepsBlockersInspectorViewportOffsetWhenScrolled(t *testing.T) {
	svc, spec := newTestService(t)
	setBlockersScrollFixture(t, svc, spec.TaskID, blockersScrollFixtureCount)
	m := openModelForTask(t, svc, spec.TaskID)
	next, _ := m.updateApprovalsModal(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	m.width = 96
	m.height = 18
	m.tab = tabBlockers
	m.resize()
	m.refreshViews(false)

	next, _ = m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	if loaded.modal != modalNone {
		t.Fatalf("expected blockers inspector refresh to keep dismissed approval modal closed, got %v", loaded.modal)
	}
	if loaded.inspector.TotalLineCount() <= loaded.inspector.Height {
		t.Fatalf("expected blockers inspector fixture to require scrolling, lines=%d height=%d", loaded.inspector.TotalLineCount(), loaded.inspector.Height)
	}
	loaded.inspector.LineDown(6)
	if loaded.inspector.YOffset == 0 {
		t.Fatal("expected blockers inspector viewport to move below the top before refresh")
	}
	yBefore := loaded.inspector.YOffset
	viewBefore := loaded.inspector.View()

	appendBlockersScrollNewest(t, svc, spec.TaskID)
	next, _ = loaded.Update(refreshCmd(loaded.backend, spec.TaskID, loaded.session.SessionID, loaded.opts, false)())
	updated := next.(model)

	if updated.modal != modalNone {
		t.Fatalf("expected watch-only refresh to keep blockers modal closed, got %v", updated.modal)
	}
	if updated.inspector.YOffset != yBefore {
		t.Fatalf("expected blockers inspector offset to stay pinned across refresh, want %d got %d", yBefore, updated.inspector.YOffset)
	}
	view := updated.inspector.View()
	if view != viewBefore {
		t.Fatalf("expected blockers inspector viewport to stay stable across refresh\nbefore:\n%s\nafter:\n%s", viewBefore, view)
	}
	if strings.Contains(view, blockersScrollNewest) {
		t.Fatalf("expected scrolled blockers inspector to avoid auto-jumping to the newest watch detail:\n%s", view)
	}
}

func TestRefreshKeepsMemoryInspectorViewportOffsetWhenScrolled(t *testing.T) {
	svc, spec := newTestService(t)
	setMemoryScrollFixture(t, svc, memoryScrollFixtureCount)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 96
	m.height = 18
	m.tab = tabMemory
	m.resize()
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, true)())
	loaded := next.(model)
	if loaded.inspector.TotalLineCount() <= loaded.inspector.Height {
		t.Fatalf("expected memory inspector fixture to require scrolling, lines=%d height=%d", loaded.inspector.TotalLineCount(), loaded.inspector.Height)
	}
	loaded.inspector.LineDown(6)
	if loaded.inspector.YOffset == 0 {
		t.Fatal("expected memory inspector viewport to move below the top before refresh")
	}
	yBefore := loaded.inspector.YOffset
	viewBefore := loaded.inspector.View()

	appendMemoryScrollNewest(t, svc)
	next, _ = loaded.Update(refreshCmd(loaded.backend, spec.TaskID, loaded.session.SessionID, loaded.opts, true)())
	updated := next.(model)

	if updated.inspector.YOffset != yBefore {
		t.Fatalf("expected memory inspector offset to stay pinned across refresh, want %d got %d", yBefore, updated.inspector.YOffset)
	}
	view := updated.inspector.View()
	if view != viewBefore {
		t.Fatalf("expected memory inspector viewport to stay stable across refresh\nbefore:\n%s\nafter:\n%s", viewBefore, view)
	}
	if strings.Contains(view, memoryScrollNewest) {
		t.Fatalf("expected scrolled memory inspector to avoid auto-jumping to the newest memory line:\n%s", view)
	}
}

func TestRefreshKeepsProjectInspectorViewportOffsetWhenScrolled(t *testing.T) {
	svc, spec := newTestService(t)
	setProjectScrollFixture(t, svc, projectScrollFixtureCount)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 96
	m.height = 18
	m.tab = tabProject
	m.resize()
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	if loaded.inspector.TotalLineCount() <= loaded.inspector.Height {
		t.Fatalf("expected project inspector fixture to require scrolling, lines=%d height=%d", loaded.inspector.TotalLineCount(), loaded.inspector.Height)
	}
	loaded.inspector.LineDown(6)
	if loaded.inspector.YOffset == 0 {
		t.Fatal("expected project inspector viewport to move below the top before refresh")
	}
	yBefore := loaded.inspector.YOffset
	viewBefore := loaded.inspector.View()

	appendProjectScrollNewest(t, svc)
	next, _ = loaded.Update(refreshCmd(loaded.backend, spec.TaskID, loaded.session.SessionID, loaded.opts, false)())
	updated := next.(model)

	if updated.inspector.YOffset != yBefore {
		t.Fatalf("expected project inspector offset to stay pinned across refresh, want %d got %d", yBefore, updated.inspector.YOffset)
	}
	view := updated.inspector.View()
	if view != viewBefore {
		t.Fatalf("expected project inspector viewport to stay stable across refresh\nbefore:\n%s\nafter:\n%s", viewBefore, view)
	}
	if strings.Contains(view, projectScrollNewest) {
		t.Fatalf("expected scrolled project inspector to avoid auto-jumping to the newest step:\n%s", view)
	}
}

func TestRefreshKeepsOverviewInspectorViewportOffsetWhenScrolled(t *testing.T) {
	svc, spec := newTestService(t)
	setOverviewScrollFixture(t, svc, spec.TaskID, overviewScrollFixtureCount)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 96
	m.height = 18
	m.tab = tabOverview
	m.resize()
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	if loaded.inspector.TotalLineCount() <= loaded.inspector.Height {
		t.Fatalf("expected overview inspector fixture to require scrolling, lines=%d height=%d", loaded.inspector.TotalLineCount(), loaded.inspector.Height)
	}
	loaded.inspector.LineDown(6)
	if loaded.inspector.YOffset == 0 {
		t.Fatal("expected overview inspector viewport to move below the top before refresh")
	}
	yBefore := loaded.inspector.YOffset
	viewBefore := loaded.inspector.View()

	appendOverviewScrollNewest(t, svc, spec.TaskID)
	next, _ = loaded.Update(refreshCmd(loaded.backend, spec.TaskID, loaded.session.SessionID, loaded.opts, false)())
	updated := next.(model)

	if updated.inspector.YOffset != yBefore {
		t.Fatalf("expected overview inspector offset to stay pinned across refresh, want %d got %d", yBefore, updated.inspector.YOffset)
	}
	view := updated.inspector.View()
	if view != viewBefore {
		t.Fatalf("expected overview inspector viewport to stay stable across refresh\nbefore:\n%s\nafter:\n%s", viewBefore, view)
	}
	if strings.Contains(view, overviewScrollNewest) {
		t.Fatalf("expected scrolled overview inspector to avoid auto-jumping to the newest continuity line:\n%s", view)
	}
}

func TestOverviewInspectorUsesCompactTaskSummary(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)

	view := sanitizeView(renderInspector(m.snapshot, tabOverview, 48, m.selectedWorker))
	for _, want := range []string{
		"Task Summary",
		"<id>",
		spec.Title,
		"coding",
		"Explore / Active",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected compact overview inspector to contain %q:\n%s", want, view)
		}
	}
	for _, stale := range []string{
		"Task:",
		"Kind:",
		"Title:",
		"Phase/State:",
		"Reason: -",
		"Status Detail Ref:",
		"Verification Ref:",
		"Review Ref:",
		"Completion Ref:",
		"Plan Revision: 0",
	} {
		if strings.Contains(view, stale) {
			t.Fatalf("expected compact overview inspector to remove %q:\n%s", stale, view)
		}
	}
}

func TestOverviewInspectorGroupsRefsUnderCompactSection(t *testing.T) {
	svc, spec := newTestService(t)
	setOverviewLongRefs(t, svc, spec.TaskID)
	m := openModelForTask(t, svc, spec.TaskID)

	view := sanitizeView(renderInspector(m.snapshot, tabOverview, 48, m.selectedWorker))
	for _, want := range []string{
		"Refs",
		"detail status/detail/",
		"verify " + overviewLongVerificationRefPrefix,
		overviewLongVerificationRefSuffix,
		overviewLongStatusDetailSuffix,
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected compact overview refs to contain %q:\n%s", want, view)
		}
	}
	for _, stale := range []string{"Status Detail Ref:", "Verification Ref:"} {
		if strings.Contains(view, stale) {
			t.Fatalf("expected compact overview refs to remove %q:\n%s", stale, view)
		}
	}
}

func TestRefreshKeepsWorkersSelectionOnActiveWorkerWhenNewWorkerAppends(t *testing.T) {
	svc, parent, first, second, _ := newWorkerSelectionActiveService(t)
	m := openModelForTask(t, svc, parent.TaskID)
	if m.modal == modalApprovals {
		next, _ := m.updateApprovalsModal(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(model)
	}
	m.width = 110
	m.height = 24
	m.tab = tabWorkers
	m.focus = focusInspector
	m.resize()
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, parent.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	if len(loaded.snapshot.Workers) != 2 {
		t.Fatalf("expected two workers before append, got %d", len(loaded.snapshot.Workers))
	}
	next, _ = loaded.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	selected := next.(model)
	if selected.selectedWorker != 1 {
		t.Fatalf("expected second worker to be selected before refresh, got %d", selected.selectedWorker)
	}
	viewBefore := sanitizeView(renderInspector(selected.snapshot, tabWorkers, max(selected.inspector.Width, 12), selected.selectedWorker))
	if !strings.Contains(viewBefore, "> "+second.WorkerID) {
		t.Fatalf("expected second worker to be selected before refresh:\n%s", viewBefore)
	}

	spawnTrailingWorker(t, svc, parent.TaskID, "review trailing parent docs")
	next, _ = selected.Update(refreshCmd(selected.backend, parent.TaskID, selected.session.SessionID, selected.opts, false)())
	updated := next.(model)

	if updated.selectedWorker != 1 {
		t.Fatalf("expected second worker selection to stay pinned across refresh, got %d", updated.selectedWorker)
	}
	view := sanitizeView(renderInspector(updated.snapshot, tabWorkers, max(updated.inspector.Width, 12), updated.selectedWorker))
	if !strings.Contains(view, "> "+second.WorkerID) {
		t.Fatalf("expected refresh to keep second worker selected:\n%s", view)
	}
	if strings.Contains(view, "> "+first.WorkerID) {
		t.Fatalf("expected refresh to avoid resetting selection to the first worker:\n%s", view)
	}

	afterEnter, cmd := updated.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	_ = afterEnter
	if cmd == nil {
		t.Fatal("expected selected active worker to remain continuable after refresh")
	}
	msgValue := cmd()
	msg, ok := msgValue.(actionFinishedMsg)
	if !ok {
		t.Fatalf("expected continue action message, got %T", msgValue)
	}
	if msg.Err != nil {
		t.Fatalf("expected continuing selected active worker to succeed, got %v", msg.Err)
	}
	firstContract, err := svc.Store.LoadWorkerContract(parent.TaskID, first.WorkerID)
	if err != nil {
		t.Fatalf("load first worker contract: %v", err)
	}
	if firstContract.ContinuationCount != 0 {
		t.Fatalf("expected first blocked worker to stay untouched, got continuation_count=%d", firstContract.ContinuationCount)
	}
	secondContract, err := svc.Store.LoadWorkerContract(parent.TaskID, second.WorkerID)
	if err != nil {
		t.Fatalf("load second worker contract: %v", err)
	}
	if secondContract.ContinuationCount == 0 {
		t.Fatal("expected selected second worker to record a continuation after refresh")
	}
}

func TestRefreshKeepsWorkersSelectionOnActiveWorkerAcrossLeadingInsert(t *testing.T) {
	svc, parent, first, second, _ := newWorkerSelectionActiveService(t)
	m := openModelForTask(t, svc, parent.TaskID)
	if m.modal == modalApprovals {
		next, _ := m.updateApprovalsModal(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(model)
	}
	m.width = 110
	m.height = 24
	m.tab = tabWorkers
	m.focus = focusInspector
	m.resize()
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, parent.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	if len(loaded.snapshot.Workers) != 2 {
		t.Fatalf("expected two workers before insert, got %d", len(loaded.snapshot.Workers))
	}
	next, _ = loaded.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	selected := next.(model)
	if selected.resolveSelectedWorker(selected.snapshot.Workers); selected.selectedWorkerID != second.WorkerID {
		t.Fatalf("expected second worker to be selected before refresh, got %q", selected.selectedWorkerID)
	}

	inserted := spawnLeadingWorker(t, svc, parent.TaskID, "WORKER-0000-leading", "review inserted before selected worker")
	next, _ = selected.Update(refreshCmd(selected.backend, parent.TaskID, selected.session.SessionID, selected.opts, false)())
	updated := next.(model)

	if updated.resolveSelectedWorker(updated.snapshot.Workers); updated.selectedWorkerID != second.WorkerID {
		t.Fatalf("expected second worker selection to stay pinned across leading insert, got %q", updated.selectedWorkerID)
	}
	view := sanitizeView(renderInspector(updated.snapshot, tabWorkers, max(updated.inspector.Width, 12), updated.resolveSelectedWorker(updated.snapshot.Workers)))
	if !strings.Contains(view, "> "+second.WorkerID) {
		t.Fatalf("expected refresh to keep second worker selected:\n%s", view)
	}
	if strings.Contains(view, "> "+first.WorkerID) || strings.Contains(view, "> "+inserted.WorkerID) {
		t.Fatalf("expected refresh to avoid drifting to another worker:\n%s", view)
	}

	afterEnter, cmd := updated.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	_ = afterEnter
	if cmd == nil {
		t.Fatal("expected selected active worker to remain continuable after leading insert")
	}
	msgValue := cmd()
	msg, ok := msgValue.(actionFinishedMsg)
	if !ok {
		t.Fatalf("expected continue action message, got %T", msgValue)
	}
	if msg.Err != nil {
		t.Fatalf("expected continuing selected active worker to succeed, got %v", msg.Err)
	}
	secondResult, err := svc.Store.LoadWorkerResult(parent.TaskID, second.WorkerID)
	if err != nil {
		t.Fatalf("load second worker result: %v", err)
	}
	if secondResult.CompletionStatus != "accepted" || secondResult.ReviewStatus != "clear" || secondResult.VerificationStatus != "passed" {
		t.Fatalf("expected second worker to remain the continued selection, got %+v", secondResult)
	}
}

func TestRefreshKeepsWorkersSelectionOnContinueChildWorkerWhenNewWorkerAppends(t *testing.T) {
	svc, parent, first, second, _ := newWorkerSelectionContinueService(t)
	m := openModelForTask(t, svc, parent.TaskID)
	if m.modal == modalApprovals {
		next, _ := m.updateApprovalsModal(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(model)
	}
	m.width = 110
	m.height = 24
	m.tab = tabWorkers
	m.focus = focusInspector
	m.resize()
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, parent.TaskID, m.session.SessionID, m.opts, false)())
	loaded := next.(model)
	if len(loaded.snapshot.Workers) != 2 {
		t.Fatalf("expected two workers before append, got %d", len(loaded.snapshot.Workers))
	}
	next, _ = loaded.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	selected := next.(model)
	if selected.selectedWorker != 1 {
		t.Fatalf("expected second worker to be selected before refresh, got %d", selected.selectedWorker)
	}
	viewBefore := sanitizeView(renderInspector(selected.snapshot, tabWorkers, max(selected.inspector.Width, 12), selected.selectedWorker))
	if !strings.Contains(viewBefore, "> "+second.WorkerID) || !strings.Contains(viewBefore, "continue_child") {
		t.Fatalf("expected second continue_child worker to be selected before refresh:\n%s", viewBefore)
	}

	spawnTrailingWorker(t, svc, parent.TaskID, "review trailing parent docs")
	next, _ = selected.Update(refreshCmd(selected.backend, parent.TaskID, selected.session.SessionID, selected.opts, false)())
	updated := next.(model)

	if updated.selectedWorker != 1 {
		t.Fatalf("expected second worker selection to stay pinned across refresh, got %d", updated.selectedWorker)
	}
	view := sanitizeView(renderInspector(updated.snapshot, tabWorkers, max(updated.inspector.Width, 12), updated.selectedWorker))
	if !strings.Contains(view, "> "+second.WorkerID) || !strings.Contains(view, "continue_child") {
		t.Fatalf("expected refresh to keep second continue_child worker selected:\n%s", view)
	}
	if strings.Contains(view, "> "+first.WorkerID) {
		t.Fatalf("expected refresh to avoid resetting selection to the first worker:\n%s", view)
	}

	afterEnter, cmd := updated.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	_ = afterEnter
	if cmd == nil {
		t.Fatal("expected selected continue_child worker to remain continuable after refresh")
	}
	msgValue := cmd()
	msg, ok := msgValue.(actionFinishedMsg)
	if !ok {
		t.Fatalf("expected continue action message, got %T", msgValue)
	}
	if msg.Err != nil {
		t.Fatalf("expected continuing selected continue_child worker to succeed, got %v", msg.Err)
	}
	firstContract, err := svc.Store.LoadWorkerContract(parent.TaskID, first.WorkerID)
	if err != nil {
		t.Fatalf("load first worker contract: %v", err)
	}
	if firstContract.ContinuationCount != 0 {
		t.Fatalf("expected first blocked worker to stay untouched, got continuation_count=%d", firstContract.ContinuationCount)
	}
	secondContract, err := svc.Store.LoadWorkerContract(parent.TaskID, second.WorkerID)
	if err != nil {
		t.Fatalf("load second worker contract: %v", err)
	}
	if secondContract.ContinuationCount == 0 {
		t.Fatal("expected selected second continue_child worker to record a continuation after refresh")
	}
}

func TestWorkersSelectionAutoScrollsActiveWorkerIntoView(t *testing.T) {
	svc, parent, workers := newLongWorkerListService(t, workersLongListCount)
	target := workers[workersLongListTargetIndex]
	m := openModelForTask(t, svc, parent.TaskID)
	m.width = 110
	m.height = 18
	m.tab = tabWorkers
	m.focus = focusInspector
	m.resize()
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, parent.TaskID, m.session.SessionID, m.opts, false)())
	current := next.(model)
	for i := 0; i < workersLongListTargetIndex; i++ {
		next, _ = current.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		current = next.(model)
	}
	if current.selectedWorker != workersLongListTargetIndex {
		t.Fatalf("expected target worker index %d, got %d", workersLongListTargetIndex, current.selectedWorker)
	}
	if current.inspector.YOffset == 0 {
		t.Fatal("expected long worker selection to scroll the inspector viewport")
	}
	view := sanitizeView(current.inspector.View())
	if !strings.Contains(view, "> "+target.WorkerID) {
		t.Fatalf("expected selected long-list worker to be visible after navigation:\n%s", view)
	}

	afterEnter, cmd := current.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	_ = afterEnter
	if cmd == nil {
		t.Fatal("expected selected active long-list worker to remain continuable")
	}
	msgValue := cmd()
	msg, ok := msgValue.(actionFinishedMsg)
	if !ok {
		t.Fatalf("expected continue action message, got %T", msgValue)
	}
	if msg.Err != nil {
		t.Fatalf("expected continuing selected active long-list worker to succeed, got %v", msg.Err)
	}
	firstContract, err := svc.Store.LoadWorkerContract(parent.TaskID, workers[0].WorkerID)
	if err != nil {
		t.Fatalf("load first worker contract: %v", err)
	}
	if firstContract.ContinuationCount != 0 {
		t.Fatalf("expected first worker to stay untouched, got continuation_count=%d", firstContract.ContinuationCount)
	}
	targetContract, err := svc.Store.LoadWorkerContract(parent.TaskID, target.WorkerID)
	if err != nil {
		t.Fatalf("load target worker contract: %v", err)
	}
	if targetContract.ContinuationCount == 0 {
		t.Fatal("expected selected active long-list worker to record a continuation")
	}
}

func TestWorkersSelectionAutoScrollsContinueChildWorkerIntoView(t *testing.T) {
	svc, parent, workers := newLongWorkerListService(t, workersLongListCount)
	target := workers[workersLongListTargetIndex]
	setWorkerContinueChildReady(t, svc, parent.TaskID, target)
	m := openModelForTask(t, svc, parent.TaskID)
	m.width = 110
	m.height = 18
	m.tab = tabWorkers
	m.focus = focusInspector
	m.resize()
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, parent.TaskID, m.session.SessionID, m.opts, false)())
	current := next.(model)
	for i := 0; i < workersLongListTargetIndex; i++ {
		next, _ = current.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		current = next.(model)
	}
	if current.selectedWorker != workersLongListTargetIndex {
		t.Fatalf("expected target worker index %d, got %d", workersLongListTargetIndex, current.selectedWorker)
	}
	if current.inspector.YOffset == 0 {
		t.Fatal("expected long continue_child selection to scroll the inspector viewport")
	}
	view := sanitizeView(current.inspector.View())
	if !strings.Contains(view, "> "+target.WorkerID) {
		t.Fatalf("expected selected continue_child long-list worker to be visible after navigation:\n%s", view)
	}

	afterEnter, cmd := current.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	_ = afterEnter
	if cmd == nil {
		t.Fatal("expected selected continue_child long-list worker to remain continuable")
	}
	msgValue := cmd()
	msg, ok := msgValue.(actionFinishedMsg)
	if !ok {
		t.Fatalf("expected continue action message, got %T", msgValue)
	}
	if msg.Err != nil {
		t.Fatalf("expected continuing selected continue_child long-list worker to succeed, got %v", msg.Err)
	}
	firstContract, err := svc.Store.LoadWorkerContract(parent.TaskID, workers[0].WorkerID)
	if err != nil {
		t.Fatalf("load first worker contract: %v", err)
	}
	if firstContract.ContinuationCount != 0 {
		t.Fatalf("expected first worker to stay untouched, got continuation_count=%d", firstContract.ContinuationCount)
	}
	targetContract, err := svc.Store.LoadWorkerContract(parent.TaskID, target.WorkerID)
	if err != nil {
		t.Fatalf("load target worker contract: %v", err)
	}
	if targetContract.ContinuationCount == 0 {
		t.Fatal("expected selected continue_child long-list worker to record a continuation")
	}
}

func TestNarrowTranscriptWrapsLongSummaryWithinWidth(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 40
	m.height = 22
	m.resize()
	m.refreshViews(true)
	appendTranscriptLongSummaryEvent(t, svc, spec.TaskID)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)

	view := sanitizeView(updated.View())
	for _, want := range []string{transcriptLongSummaryPrefix, transcriptLongSummaryTail} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected transcript long summary view to contain %q:\n%s", want, view)
		}
	}
	assertMaxLineWidth(t, view, 40)
}

func TestNarrowTranscriptWrapsLongRefWithinWidth(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 40
	m.height = 22
	m.resize()
	m.refreshViews(true)
	appendTranscriptLongRefEvent(t, svc, spec.TaskID)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)

	view := sanitizeView(updated.View())
	for _, want := range []string{transcriptLongRefPrefix, transcriptLongRefSuffix} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected transcript long ref view to contain %q:\n%s", want, view)
		}
	}
	assertMaxLineWidth(t, view, 40)
}

func TestNarrowOverviewInspectorWrapsLongRefsWithinWidth(t *testing.T) {
	svc, spec := newTestService(t)
	setOverviewLongRefs(t, svc, spec.TaskID)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 48
	m.height = 28
	m.resize()

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)

	view := sanitizeView(renderInspector(updated.snapshot, tabOverview, max(updated.inspector.Width, 12), updated.selectedWorker))
	for _, want := range []string{overviewLongVerificationRefPrefix, overviewLongVerificationRefSuffix, overviewLongStatusDetailSuffix} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected overview inspector view to contain %q:\n%s", want, view)
		}
	}
	assertMaxLineWidth(t, view, 48)
}

func TestNarrowPlanInspectorWrapsLongMetadataWithinWidth(t *testing.T) {
	svc, spec := newTestService(t)
	setPlanLongFields(t, svc, spec.TaskID)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 48
	m.height = 30
	m.resize()
	m.tab = tabPlan
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)

	view := sanitizeView(renderInspector(updated.snapshot, tabPlan, max(updated.inspector.Width, 12), updated.selectedWorker))
	for _, want := range []string{planLongCriterionPrefix, planLongCriterionSuffix} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected plan inspector view to contain %q:\n%s", want, view)
		}
	}
	assertMaxLineWidth(t, view, 48)
}

func TestNarrowCriteriaInspectorWrapsLongMetadataWithinWidth(t *testing.T) {
	svc, spec := newTestService(t)
	setCriteriaLongFields(t, svc, spec.TaskID)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 48
	m.height = 30
	m.resize()
	m.tab = tabCriteria
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)

	view := sanitizeView(renderInspector(updated.snapshot, tabCriteria, max(updated.inspector.Width, 12), updated.selectedWorker))
	for _, want := range []string{criteriaLongSummaryPrefix, criteriaLongSummarySuffix, criteriaLongCurrentPrefix, criteriaLongCurrentSuffix, criteriaLongStatementSuffix, criteriaLongLastSummaryTail} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected criteria inspector view to contain %q:\n%s", want, view)
		}
	}
	assertMaxLineWidth(t, view, 48)
}

func TestNarrowWorkersInspectorWrapsLongObjectiveWithinWidth(t *testing.T) {
	svc, parent, worker := newWorkerContinueService(t)
	setWorkerLongObjective(t, svc, parent.TaskID, worker.WorkerID)
	m := openModelForTask(t, svc, parent.TaskID)
	m.width = 48
	m.height = 30
	m.resize()
	m.tab = tabWorkers
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, parent.TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)

	view := renderInspector(updated.snapshot, tabWorkers, max(updated.inspector.Width, 12), updated.selectedWorker)
	normalized := strings.Join(strings.Fields(sanitizeView(view)), " ")
	for _, want := range []string{workerLongObjectivePrefix, "findings/report", ".jsonl", "important blockers"} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("expected workers inspector view to contain %q:\n%s", want, view)
		}
	}
	assertMaxLineWidth(t, view, 48)
}

func TestNarrowBlockersInspectorWrapsLongEntriesWithinWidth(t *testing.T) {
	svc, spec := newTestService(t)
	approval, input := requestLongBlockers(t, svc, spec.TaskID)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 44
	m.height = 30
	m.resize()
	if m.modal == modalApprovals {
		next, _ := m.updateApprovalsModal(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(model)
	}
	m.tab = tabBlockers
	m.refreshViews(false)

	view := renderInspector(m.snapshot, tabBlockers, max(m.inspector.Width, 12), m.selectedWorker)
	normalized := strings.Join(strings.Fields(sanitizeView(view)), " ")
	for _, want := range []string{
		approval.ApprovalID,
		approvalLongScopePrefix,
		approvalLongScopeSuffix,
		"reason operator review required",
		input.RequestID,
		"field target_path",
		"Provide",
		inputLongPromptSuffix,
	} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("expected blockers inspector view to contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(normalized, "Owned Child Approvals") {
		t.Fatalf("expected local approval to stay out of the owned approvals section:\n%s", view)
	}
	assertMaxLineWidth(t, view, 44)
}

func TestWatchOnlyBlockersInspectorUsesCompactSummary(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	appendBlockersScrollNewest(t, svc, spec.TaskID)
	m.width = 40
	m.height = 24
	m.resize()
	m.tab = tabBlockers
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)

	view := renderInspector(updated.snapshot, tabBlockers, max(updated.inspector.Width, 12), updated.selectedWorker)
	normalized := strings.Join(strings.Fields(sanitizeView(view)), " ")
	for _, want := range []string{"Watch", "Waiting for watch trigger", "reason waiting_watch", "detail", "watch/scroll-blockers-line-13"} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("expected watch-only blockers summary to contain %q:\n%s", want, view)
		}
	}
	assertMaxLineWidth(t, view, 40)
}

func TestNarrowMemoryInspectorWrapsLongEntryWithinWidth(t *testing.T) {
	svc, spec := newTestService(t)
	promoteLongMemory(t, svc, spec.TaskID)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 48
	m.height = 30
	m.resize()
	m.tab = tabMemory
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, true)())
	updated := next.(model)

	view := sanitizeView(renderInspector(updated.snapshot, tabMemory, max(updated.inspector.Width, 12), updated.selectedWorker))
	for _, want := range []string{memoryLongSummaryPrefix, memoryLongSummarySuffix} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected memory inspector view to contain %q:\n%s", want, view)
		}
	}
	assertMaxLineWidth(t, view, 48)
}

func TestNarrowProjectInspectorWrapsLongEntriesWithinWidth(t *testing.T) {
	svc, spec := newTestService(t)
	setProjectLongFields(t, svc, spec.TaskID)
	m := openModelForTask(t, svc, spec.TaskID)
	m.width = 48
	m.height = 36
	m.resize()
	m.tab = tabProject
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)

	view := sanitizeView(renderInspector(updated.snapshot, tabProject, max(updated.inspector.Width, 12), updated.selectedWorker))
	for _, want := range []string{projectLongExplanationPrefix, projectLongExplanationSuffix, projectLongStepPrefix, projectLongBranchPrefix, projectLongStatusRefSuffix, projectLongNoteSuffix} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected project inspector view to contain %q:\n%s", want, view)
		}
	}
	assertMaxLineWidth(t, view, 48)
}

func TestProjectInspectorUsesCompactTopMetadataAndOmitsEmptyPlaceholders(t *testing.T) {
	svc, specs := newProjectNavigationService(t)
	m := openModelForTask(t, svc, specs[0].TaskID)
	m.tab = tabProject
	m.refreshViews(false)

	view := sanitizeView(renderInspector(m.snapshot, tabProject, max(m.inspector.Width, 12), m.selectedWorker))
	for _, want := range []string{
		"Project Summary",
		"Workspace Root:",
		"rev 2",
		"current " + projectNavigationCurrentStepID,
		"active " + projectNavigationCurrentBranchID,
		"mutation",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected compact project inspector metadata to contain %q:\n%s", want, view)
		}
	}
	for _, stale := range []string{
		"Revision:",
		"Current Step:",
		"Ready Steps:",
		"Active Branches:",
		"Blocked Steps: -",
		"Last Mutation Ref: -",
		"Dependencies Satisfied: false",
		"Bound Steps: -",
		"Bound Branches: -",
		"Depends On: -",
		"Unmet Dependencies: -",
		"Blocked Project Steps: -",
		"Refs: -",
	} {
		if strings.Contains(view, stale) {
			t.Fatalf("expected compact project inspector metadata to remove %q:\n%s", stale, view)
		}
	}
}

func TestTasksInspectorShowsWorkerChildNavigation(t *testing.T) {
	svc, parent, worker := newWorkerContinueService(t)
	m := openModelForTask(t, svc, parent.TaskID)
	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks
	m.tab = tabTasks
	m.refreshViews(false)

	view, _ := renderTaskNavigationSummary(m.snapshot, m.tasks, m.taskHistory, m.selectedRelatedTask, max(m.inspector.Width, 12))
	normalized := strings.Join(strings.Fields(view), " ")
	for _, want := range []string{
		worker.ChildTaskID,
		"reviewer",
		"objective: review",
		"from worker " + worker.WorkerID,
		"Use /back",
	} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("expected tasks inspector view to contain %q:\n%s", want, view)
		}
	}
}

func TestTasksInspectorUsesCompactCurrentTaskSummary(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.tab = tabTasks
	m.refreshViews(false)

	view, _ := renderTaskNavigationSummary(m.snapshot, m.tasks, m.taskHistory, m.selectedRelatedTask, max(m.inspector.Width, 12))
	view = sanitizeView(view)
	for _, want := range []string{
		"Current Task",
		"<id>",
		spec.Title,
		"Explore / Active",
		"coding",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected compact current-task summary to contain %q:\n%s", want, view)
		}
	}
	for _, stale := range []string{"Task:", "Title:", "Kind:", "Phase/State:"} {
		if strings.Contains(view, stale) {
			t.Fatalf("expected compact current-task summary to remove %q:\n%s", stale, view)
		}
	}
}

func TestTasksInspectorShowsProjectBoundSiblingTask(t *testing.T) {
	svc, specs := newProjectNavigationService(t)
	m := openModelForTask(t, svc, specs[0].TaskID)
	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks
	m.tab = tabTasks
	m.refreshViews(false)

	view, _ := renderTaskNavigationSummary(m.snapshot, m.tasks, m.taskHistory, m.selectedRelatedTask, max(m.inspector.Width, 12))
	normalized := strings.Join(strings.Fields(view), " ")
	for _, want := range []string{
		specs[1].TaskID,
		projectNavigationSiblingStepTitle,
		"from project step " + projectNavigationSiblingStepID,
		"project branch " + projectNavigationSiblingBranchID,
	} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("expected tasks inspector view to contain %q:\n%s", want, view)
		}
	}
	for _, stale := range []string{"kind=", "phase/state=", "sources:"} {
		if strings.Contains(view, stale) {
			t.Fatalf("expected compact related-task summary to remove %q:\n%s", stale, view)
		}
	}
}

func TestNarrowTasksInspectorWrapsCompactRowsWithinWidth(t *testing.T) {
	svc, specs := newLongTaskNavigationService(t)
	m := openModelForTask(t, svc, specs[0].TaskID)
	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks
	m.width = 48
	m.height = 40
	m.resize()
	m.tab = tabTasks
	m.refreshViews(false)

	next, _ := m.Update(refreshCmd(m.backend, specs[0].TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)

	view, _ := renderTaskNavigationSummary(updated.snapshot, updated.tasks, updated.taskHistory, updated.selectedRelatedTask, max(updated.inspector.Width, 12))
	sanitized := sanitizeView(view)
	normalized := strings.Join(strings.Fields(sanitized), " ")
	for _, want := range []string{
		taskNavLongCurrentTitlePrefix,
		taskNavLongCurrentTitleSuffix,
		taskNavLongRelatedTitlePrefix,
		taskNavLongRelatedTitleSuffix,
		projectNavigationSiblingStepID,
	} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("expected tasks inspector view to contain %q:\n%s", want, sanitized)
		}
	}
	assertMaxLineWidth(t, view, 48)
}

func TestTasksInspectorRefreshReloadsLateCreatedTaskMetadata(t *testing.T) {
	svc, specs := newMultiTaskService(t, 1)
	m := openModelForTask(t, svc, specs[0].TaskID)
	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks
	m.tab = tabTasks
	m.refreshViews(false)

	late := createLateCodingTask(t, svc, "late project sibling", "surface late-created task metadata in the tasks inspector")

	refreshed, cmd := m.Update(refreshCmd(m.backend, specs[0].TaskID, m.session.SessionID, m.opts, false)())
	if cmd == nil {
		t.Fatal("expected refresh on the tasks tab to schedule a task-list reload")
	}
	afterRefresh := refreshed.(model)
	view, _ := renderTaskNavigationSummary(afterRefresh.snapshot, afterRefresh.tasks, afterRefresh.taskHistory, afterRefresh.selectedRelatedTask, max(afterRefresh.inspector.Width, 12))
	normalized := strings.Join(strings.Fields(view), " ")
	if !strings.Contains(normalized, late.TaskID) {
		t.Fatalf("expected project auto-track to surface the late task id before task-list reload:\n%s", view)
	}
	if strings.Contains(normalized, "coding · Explore / Active") {
		t.Fatalf("expected stale task list to omit late task metadata before reload:\n%s", view)
	}

	loaded, _ := afterRefresh.Update(cmd())
	loadedModel := loaded.(model)
	view, _ = renderTaskNavigationSummary(loadedModel.snapshot, loadedModel.tasks, loadedModel.taskHistory, loadedModel.selectedRelatedTask, max(loadedModel.inspector.Width, 12))
	normalized = strings.Join(strings.Fields(view), " ")
	for _, want := range []string{late.TaskID, "coding · Explore / Active"} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("expected tasks inspector view to contain %q after task-list reload:\n%s", want, view)
		}
	}
}

func TestTasksInspectorEnterOpensRelatedTaskAndBackReturns(t *testing.T) {
	svc, parent, worker := newWorkerContinueService(t)
	m := openModelForTask(t, svc, parent.TaskID)
	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks
	m.tab = tabTasks
	m.focus = focusInspector
	m.refreshViews(false)

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected open-related-task command")
	}
	openedMsg := cmd()
	opened, _ := next.Update(openedMsg)
	openedModel := opened.(model)
	if openedModel.taskID != worker.ChildTaskID {
		t.Fatalf("expected child task to open, got %q", openedModel.taskID)
	}
	if len(openedModel.taskHistory) != 1 || openedModel.taskHistory[0] != parent.TaskID {
		t.Fatalf("expected parent task to be pushed into history, got %#v", openedModel.taskHistory)
	}

	openedModel.composer.SetValue("/back")
	returnedNext, returnCmd := openedModel.submitComposer()
	if returnCmd == nil {
		t.Fatal("expected /back to open previous task")
	}
	returnedMsg := returnCmd()
	returned, _ := returnedNext.Update(returnedMsg)
	returnedModel := returned.(model)
	if returnedModel.taskID != parent.TaskID {
		t.Fatalf("expected /back to return to parent task, got %q", returnedModel.taskID)
	}
	if len(returnedModel.taskHistory) != 0 {
		t.Fatalf("expected history to be empty after returning, got %#v", returnedModel.taskHistory)
	}
}

func TestTaskSwitchRestoresTaskLocalDrafts(t *testing.T) {
	svc, specs := newMultiTaskService(t, 2)
	m := openModelForTask(t, svc, specs[0].TaskID)
	m.composer.SetValue("parent draft")

	next, _ := m.Update(openTaskCmd(m.backend, taskOpenRequest{
		TaskID:         specs[1].TaskID,
		PreviousTaskID: specs[0].TaskID,
		PushPrevious:   true,
		Status:         "Switched to child task.",
	}, m.opts)())
	childModel := next.(model)
	if childModel.taskID != specs[1].TaskID {
		t.Fatalf("expected child task to open, got %q", childModel.taskID)
	}
	if got := childModel.composer.Value(); got != "" {
		t.Fatalf("expected child task to start with an empty composer, got %q", got)
	}
	if got := childModel.taskDrafts[specs[0].TaskID].Draft; got != "parent draft" {
		t.Fatalf("expected parent draft saved by task id, got %q", got)
	}

	childModel.composer.SetValue("child draft")
	next, _ = childModel.Update(openTaskCmd(childModel.backend, taskOpenRequest{
		TaskID:         specs[0].TaskID,
		PreviousTaskID: specs[1].TaskID,
		PushPrevious:   true,
		Status:         "Returned to parent task.",
	}, childModel.opts)())
	returnedModel := next.(model)
	if returnedModel.taskID != specs[0].TaskID {
		t.Fatalf("expected parent task to reopen, got %q", returnedModel.taskID)
	}
	if got := returnedModel.composer.Value(); got != "parent draft" {
		t.Fatalf("expected parent draft restored after return, got %q", got)
	}
	if got := returnedModel.taskDrafts[specs[1].TaskID].Draft; got != "child draft" {
		t.Fatalf("expected child draft saved independently, got %q", got)
	}
}

func TestTaskSwitchCarriesImmediateTypingToOpenedTask(t *testing.T) {
	svc, specs := newMultiTaskService(t, 2)
	m := openModelForTask(t, svc, specs[0].TaskID)
	m.composer.SetValue("parent draft")

	next, cmd := m.beginTaskOpen(specs[1].TaskID, true, "Switched to child task.")
	if cmd == nil {
		t.Fatal("expected open-task command")
	}
	switching := next.(model)
	if switching.focus != focusComposer {
		t.Fatalf("expected composer focus while task open is pending, got %v", switching.focus)
	}
	if got := switching.composer.Value(); got != "" {
		t.Fatalf("expected pending open to clear composer for carryover typing, got %q", got)
	}
	for _, r := range "back" {
		next, _ = switching.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		switching = next.(model)
	}

	opened, _ := switching.Update(cmd())
	openedModel := opened.(model)
	if openedModel.taskID != specs[1].TaskID {
		t.Fatalf("expected child task to open, got %q", openedModel.taskID)
	}
	if got := openedModel.composer.Value(); got != "back" {
		t.Fatalf("expected typed carryover draft on opened task, got %q", got)
	}
	if got := openedModel.taskDrafts[specs[0].TaskID].Draft; got != "parent draft" {
		t.Fatalf("expected previous task draft preserved during pending open, got %q", got)
	}
}

func TestTaskSwitchCarryoverAppendsToRestoredDestinationDraft(t *testing.T) {
	svc, specs := newMultiTaskService(t, 2)
	m := openModelForTask(t, svc, specs[0].TaskID)
	m.composer.SetValue("parent draft")
	m.taskDrafts[specs[1].TaskID] = taskDraftState{Draft: "child draft"}

	next, cmd := m.beginTaskOpen(specs[1].TaskID, true, "Switched to child task.")
	if cmd == nil {
		t.Fatal("expected open-task command")
	}
	switching := next.(model)
	for _, r := range " ++" {
		next, _ = switching.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		switching = next.(model)
	}

	opened, _ := switching.Update(cmd())
	openedModel := opened.(model)
	if got := openedModel.composer.Value(); got != "child draft ++" {
		t.Fatalf("expected carryover appended to restored destination draft, got %q", got)
	}
}

func TestSubmitComposerWaitsWhileTaskOpenIsPending(t *testing.T) {
	svc, specs := newMultiTaskService(t, 2)
	m := openModelForTask(t, svc, specs[0].TaskID)

	next, cmd := m.beginTaskOpen(specs[1].TaskID, true, "Switched to child task.")
	if cmd == nil {
		t.Fatal("expected open-task command")
	}
	switching := next.(model)
	switching.composer.SetValue("run")

	next, submitCmd := switching.submitComposer()
	if submitCmd != nil {
		t.Fatalf("expected pending task-open submit to avoid starting a prompt, got %v", submitCmd)
	}
	waiting := next.(model)
	if waiting.running {
		t.Fatal("expected pending task-open submit to avoid starting a running turn")
	}
	if got := waiting.statusLine; got != "Opening task "+specs[1].TaskID+". Keep typing; Enter waits until the switch completes." {
		t.Fatalf("unexpected pending-open status line %q", got)
	}
	if got := waiting.composer.Value(); got != "run" {
		t.Fatalf("expected draft preserved while task open is pending, got %q", got)
	}
}

func TestPendingTaskOpenContextAndFooterStayExplicit(t *testing.T) {
	svc, specs := newMultiTaskService(t, 2)
	m := openModelForTask(t, svc, specs[0].TaskID)

	next, cmd := m.beginTaskOpen(specs[1].TaskID, true, "Switched to child task.")
	if cmd == nil {
		t.Fatal("expected open-task command")
	}
	switching := next.(model)

	if got := switching.statusLine; got != "Opening task "+specs[1].TaskID+". Keep typing; Enter waits until the switch completes." {
		t.Fatalf("unexpected pending-open status %q", got)
	}
	if got := renderComposerContext(switching, 80); !strings.Contains(strings.Join(strings.Fields(got), " "), "Enter waits until the switch completes.") {
		t.Fatalf("expected pending-open context copy, got:\n%s", got)
	}
	if got := switching.footerHints(); got != "Enter waits  Ctrl+C quit  Ctrl+J newline" {
		t.Fatalf("unexpected pending-open footer hints %q", got)
	}
}

func TestPendingTaskOpenViewShowsExplicitSwitchingState(t *testing.T) {
	svc, specs := newMultiTaskService(t, 2)
	m := openModelForTask(t, svc, specs[0].TaskID)

	next, cmd := m.beginTaskOpen(specs[1].TaskID, true, "Switched to child task.")
	if cmd == nil {
		t.Fatal("expected open-task command")
	}
	switching := next.(model)

	view := switching.View()
	for _, want := range []string{
		"Switching -> " + specs[1].TaskID,
		"Opening task " + specs[1].TaskID + ". Keep typing; Enter waits until the switch completes.",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected pending-open view to contain %q:\n%s", want, view)
		}
	}
	for _, stale := range []string{"switch=", "provider=", "session="} {
		if strings.Contains(view, stale) {
			t.Fatalf("expected pending-open view to avoid raw header token %q:\n%s", stale, view)
		}
	}
}

func TestPendingTaskOpenSubmitRunsAgainstOpenedTaskAfterSwitchCompletes(t *testing.T) {
	svc, specs := newMultiTaskService(t, 2)
	m := openModelForTask(t, svc, specs[0].TaskID)

	next, cmd := m.beginTaskOpen(specs[1].TaskID, true, "Switched to child task.")
	if cmd == nil {
		t.Fatal("expected open-task command")
	}
	switching := next.(model)
	switching.composer.SetValue("run")

	opened, _ := switching.Update(cmd())
	openedModel := opened.(model)
	if openedModel.taskID != specs[1].TaskID {
		t.Fatalf("expected second task to open, got %q", openedModel.taskID)
	}

	next, submitCmd := openedModel.submitComposer()
	if submitCmd == nil {
		t.Fatal("expected submit after task switch to start a prompt")
	}
	running := next.(model)
	if !running.running {
		t.Fatal("expected submit after task switch to run against destination task")
	}
	if running.taskID != specs[1].TaskID {
		t.Fatalf("expected running task to stay on destination task, got %q", running.taskID)
	}
	if running.activePrompt != "/run" {
		t.Fatalf("expected normalized /run prompt after switch, got %q", running.activePrompt)
	}
}

func TestTaskSwitchClearsTransientComposerAndInspectorState(t *testing.T) {
	svc, specs := newMultiTaskService(t, 2)
	m := openModelForTask(t, svc, specs[0].TaskID)
	m.tab = tabPlan
	m.inspector.SetYOffset(6)
	m.composer.SetValue("/review")
	m.historyIndex = 0
	m.historyDraft = "scratch"
	m.inputBox.SetValue("pending response")

	next, _ := m.Update(openTaskCmd(m.backend, taskOpenRequest{
		TaskID:         specs[1].TaskID,
		PreviousTaskID: specs[0].TaskID,
		PushPrevious:   true,
		Status:         "Switched to child task.",
	}, m.opts)())
	updated := next.(model)
	if updated.historyIndex != -1 {
		t.Fatalf("expected history recall reset on task switch, got %d", updated.historyIndex)
	}
	if updated.historyDraft != "" {
		t.Fatalf("expected history draft cleared on task switch, got %q", updated.historyDraft)
	}
	if got := updated.inputBox.Value(); got != "" {
		t.Fatalf("expected input modal draft cleared on task switch, got %q", got)
	}
	if updated.inspector.YOffset != 0 {
		t.Fatalf("expected inspector viewport reset on task switch, got %d", updated.inspector.YOffset)
	}
}

func TestTasksInspectorSelectionStaysOnProjectSiblingAcrossLeadingInsert(t *testing.T) {
	svc, specs := newProjectNavigationService(t)
	m := openModelForTask(t, svc, specs[0].TaskID)
	tasks, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	m.tasks = tasks
	m.width = 110
	m.height = 24
	m.tab = tabTasks
	m.focus = focusInspector
	m.resize()
	m.refreshViews(false)
	if targets := m.relatedTaskTargets(); len(targets) == 0 || m.resolveSelectedRelatedTask(targets) != 0 || m.selectedRelatedID != specs[1].TaskID {
		t.Fatalf("expected project sibling to be the initial related-task selection, got targets=%+v selected=%q", targets, m.selectedRelatedID)
	}

	inserted := spawnTrailingWorker(t, svc, specs[0].TaskID, "review inserted before project sibling")
	next, _ := m.Update(refreshCmd(m.backend, specs[0].TaskID, m.session.SessionID, m.opts, false)())
	updated := next.(model)

	targets := updated.relatedTaskTargets()
	updated.resolveSelectedRelatedTask(targets)
	if updated.selectedRelatedID != specs[1].TaskID {
		t.Fatalf("expected project sibling selection to stay pinned across leading insert, got %q", updated.selectedRelatedID)
	}
	view, _ := renderTaskNavigationSummary(updated.snapshot, updated.tasks, updated.taskHistory, updated.resolveSelectedRelatedTask(updated.relatedTaskTargets()), max(updated.inspector.Width, 12))
	if !strings.Contains(view, "> "+specs[1].TaskID) {
		t.Fatalf("expected project sibling to remain selected:\n%s", view)
	}
	if strings.Contains(view, "> "+inserted.ChildTaskID) {
		t.Fatalf("expected refresh to avoid drifting selection to the inserted worker child:\n%s", view)
	}

	afterEnter, cmd := updated.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected selected related task to remain openable after leading insert")
	}
	opened, _ := afterEnter.Update(cmd())
	openedModel := opened.(model)
	if openedModel.taskID != specs[1].TaskID {
		t.Fatalf("expected Enter to keep opening the project sibling task, got %q", openedModel.taskID)
	}
}

func TestNarrowApprovalModalWrapsLongScopeAndReasonWithinWidth(t *testing.T) {
	svc, spec := newTestService(t)
	record := requestLongApproval(t, svc, spec.TaskID)
	m := openModelForTask(t, svc, spec.TaskID)
	if m.modal != modalApprovals {
		t.Fatalf("expected approval modal to auto-open, got %v", m.modal)
	}
	rendered := sanitizeView(renderApprovalsModal(m, 36))
	for _, want := range []string{record.ApprovalID, approvalLongScopePrefix, approvalLongScopeSuffix, approvalLongReasonSuffix} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected approval modal to contain %q:\n%s", want, rendered)
		}
	}
	assertMaxLineWidth(t, rendered, 36)
}

func TestOwnedApprovalModalShowsChildStateAndBlockedReason(t *testing.T) {
	svc, parent, worker, record := newOwnedApprovalContinueService(t)
	m := openModelForTask(t, svc, parent.TaskID)
	if m.modal != modalApprovals {
		t.Fatalf("expected owned approval modal to auto-open, got %v", m.modal)
	}
	rendered := sanitizeView(renderApprovalsModal(m, 48))
	for _, want := range []string{record.ApprovalID, worker.WorkerID, "Child State: blocked", "Blocked: blocked_policy"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected owned approval modal to contain %q:\n%s", want, rendered)
		}
	}
}

func TestNarrowInputModalWrapsLongPromptWithinWidth(t *testing.T) {
	svc, spec := newTestService(t)
	request := requestLongInput(t, svc, spec.TaskID)
	m := openModelForTask(t, svc, spec.TaskID)
	if m.modal != modalInput {
		t.Fatalf("expected input modal to auto-open, got %v", m.modal)
	}
	rendered := sanitizeView(renderInputModal(m, 36))
	for _, want := range []string{request.RequestID, inputLongPromptPrefix, inputLongPromptSuffix} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected input modal to contain %q:\n%s", want, rendered)
		}
	}
	assertMaxLineWidth(t, rendered, 36)
}

func TestFocusCycleChangesFooterHintsAndTextareaFocus(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	if !m.composer.Focused() {
		t.Fatal("expected composer to start focused")
	}

	next, _ := m.updateMain(tea.KeyMsg{Type: tea.KeyTab})
	transcriptFocused := next.(model)
	if transcriptFocused.focus != focusTranscript {
		t.Fatalf("expected transcript focus, got %v", transcriptFocused.focus)
	}
	if transcriptFocused.composer.Focused() {
		t.Fatal("expected composer to blur when transcript is focused")
	}
	view := sanitizeView(transcriptFocused.View())
	if !strings.Contains(view, "a approvals") || strings.Contains(view, "/approvals") {
		t.Fatalf("expected non-composer footer hints after tab:\n%s", view)
	}

	next, _ = transcriptFocused.updateMain(tea.KeyMsg{Type: tea.KeyTab})
	inspectorFocused := next.(model)
	next, _ = inspectorFocused.updateMain(tea.KeyMsg{Type: tea.KeyTab})
	composerFocused := next.(model)
	if composerFocused.focus != focusComposer {
		t.Fatalf("expected composer focus after cycling tabs, got %v", composerFocused.focus)
	}
	if !composerFocused.composer.Focused() {
		t.Fatal("expected composer to refocus after cycling back")
	}
}

func TestFooterHintsStayCompactAndContextual(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)

	if got := m.footerHints(); got != "Enter send  Ctrl+K actions  Ctrl+O picker  Esc prev  Ctrl+J newline  Ctrl+C quit" {
		t.Fatalf("unexpected idle composer footer hints: %q", got)
	}
	if got := m.footerHintLine(60); got != "Enter send  Ctrl+K actions  Ctrl+O picker  Esc prev" {
		t.Fatalf("unexpected collapsed idle composer footer hints: %q", got)
	}

	m.running = true
	if got := m.footerHints(); got != "Enter queue  Ctrl+C interrupt  Esc interrupt  Ctrl+J newline" {
		t.Fatalf("unexpected running composer footer hints: %q", got)
	}
	if got := m.footerHintLine(35); got != "Enter queue  Ctrl+C interrupt" {
		t.Fatalf("unexpected collapsed running composer footer hints: %q", got)
	}

	m.focus = focusTranscript
	if got := m.footerHints(); got != "Tab focus  Enter action  a approvals  Ctrl+C interrupt  ? help  1-8 tabs" {
		t.Fatalf("unexpected running non-composer footer hints: %q", got)
	}
	if got := m.footerHintLine(38); got != "Tab focus  Ctrl+C interrupt" {
		t.Fatalf("unexpected collapsed running non-composer footer hints: %q", got)
	}

	m.focus = focusComposer
	m.running = false
	m.composer.SetValue("/he")
	if got := m.footerHints(); got != "Up/Down select  Tab complete  Esc hide  Ctrl+C quit" {
		t.Fatalf("unexpected slash overlay footer hints: %q", got)
	}

	m.running = true
	m.composer.SetValue("")
	m.queuedPrompts = []string{"/memory queued follow-up"}
	if got := m.footerHints(); got != "Up/Down select  Enter edit  Backspace drop  Esc hide  Ctrl+C interrupt" {
		t.Fatalf("unexpected queued overlay footer hints: %q", got)
	}
	if got := m.footerHintLine(38); got != "Enter edit  Ctrl+C interrupt" {
		t.Fatalf("unexpected collapsed queued overlay footer hints: %q", got)
	}

	m.dismissedPromptOverlaySig = m.currentPromptOverlaySignature()
	if got := m.footerHints(); got != "Enter queue  Ctrl+P newest  Ctrl+C interrupt  Esc interrupt  Ctrl+J newline" {
		t.Fatalf("unexpected hidden queued footer hints: %q", got)
	}

	m.focus = focusTranscript
	m.running = false
	m.queuedPrompts = nil
	m.dismissedPromptOverlaySig = ""
	if got := m.footerHintLine(38); got != "Tab focus  Enter action  Ctrl+C quit" {
		t.Fatalf("unexpected collapsed idle non-composer footer hints: %q", got)
	}
}

func TestCtrlCWhileRunAndHelpModalOpensInterruptConfirm(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.modal = modalHelp
	m.syncFocusStates()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatalf("expected no immediate command on ctrl+c interrupt confirm, got %v", cmd)
	}
	updated := next.(model)
	if updated.modal != modalConfirmInterrupt {
		t.Fatalf("expected confirm interrupt modal, got %v", updated.modal)
	}
	if updated.confirmIndex != 1 {
		t.Fatalf("expected interrupt option selected, got %d", updated.confirmIndex)
	}
}

func TestCtrlCWhileConfirmInterruptRequestsCancelImmediately(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	cancelled := false
	m.running = true
	m.modal = modalConfirmInterrupt
	m.confirmIndex = 1
	m.turnCancel = func() {
		cancelled = true
	}
	m.syncFocusStates()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatalf("expected no immediate command on second ctrl+c, got %v", cmd)
	}
	updated := next.(model)
	if updated.modal != modalNone {
		t.Fatalf("expected confirm modal to close after second ctrl+c, got %v", updated.modal)
	}
	if !updated.pendingAbort {
		t.Fatal("expected second ctrl+c to request interrupt")
	}
	if !cancelled {
		t.Fatal("expected second ctrl+c to call turnCancel")
	}
	if !strings.Contains(updated.statusLine, "Waiting for the current turn to stop") {
		t.Fatalf("unexpected interrupt status: %q", updated.statusLine)
	}
}

func TestSimpleModeCtrlCWhileRunningRequestsInterrupt(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	cancelled := false
	m.opts.SimpleMode = true
	m.running = true
	m.turnCancel = func() {
		cancelled = true
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatalf("expected simple mode interrupt to wait for prompt finish, got command %v", cmd)
	}
	updated := next.(model)
	if !updated.pendingAbort {
		t.Fatal("expected simple mode ctrl+c to request abort")
	}
	if !cancelled {
		t.Fatal("expected simple mode ctrl+c to cancel active turn context")
	}
	if updated.modal != modalNone {
		t.Fatalf("expected no confirmation modal in simple mode, got %v", updated.modal)
	}
	if !strings.Contains(updated.statusLine, "Waiting for the current turn to stop") {
		t.Fatalf("unexpected interrupt status: %q", updated.statusLine)
	}
}

func TestCtrlCQuitsWhenComposerEmptyAndIdle(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	_ = next
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected quit message, got %T", cmd())
	}
}

func TestCtrlDQuitsWhenComposerEmptyAndIdle(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	_ = next
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected quit message, got %T", cmd())
	}
}

func TestCtrlDDoesNotQuitWhenComposerHasDraft(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("draft")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	updated := next.(model)
	if cmd != nil {
		t.Fatalf("expected no quit command with non-empty draft, got %v", cmd)
	}
	if updated.composer.Value() != "draft" {
		t.Fatalf("expected draft to remain intact, got %q", updated.composer.Value())
	}
}

func TestCtrlDDoesNotQuitWhenComposerBurstIsPending(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	clock := newFakeComposerClock()
	m.now = clock.Now

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected flush tick after rapid ASCII input")
	}

	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	updated := next.(model)
	if cmd != nil {
		t.Fatalf("expected no quit command while burst text is pending, got %v", cmd)
	}
	if got := updated.composer.Value(); got != "a" {
		t.Fatalf("expected pending burst text to flush into composer before ctrl+d, got %q", got)
	}
}

func TestCtrlDDoesNotQuitDuringActiveTurn(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.composer.SetValue("")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	updated := next.(model)
	if cmd != nil {
		t.Fatalf("expected no quit command during active turn, got %v", cmd)
	}
	if !updated.running {
		t.Fatal("expected active turn to remain running")
	}
}

func TestComposerPasteLikeAsciiBurstTreatsEnterAsNewline(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	clock := newFakeComposerClock()
	m.now = clock.Now

	for _, text := range []string{"abc", " ", "def"} {
		next, _ := m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
		m = next.(model)
		clock.Advance(5 * time.Millisecond)
	}

	next, _ := m.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.running {
		t.Fatal("expected no prompt submission during paste-like burst")
	}
	if got := m.composer.Value(); got != "abc def\n" {
		t.Fatalf("expected newline inserted into composer, got %q", got)
	}
}

func TestComposerShortAsciiBurstTreatsEnterAsNewline(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	clock := newFakeComposerClock()
	m.now = clock.Now

	for _, r := range []rune("abc") {
		next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd == nil {
			t.Fatal("expected flush tick for buffered ASCII burst")
		}
		m = next.(model)
		clock.Advance(5 * time.Millisecond)
	}

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.running {
		t.Fatalf("expected short ASCII burst to avoid prompt submission, got cmd=%v", cmd)
	}
	if got := m.composer.Value(); got != "abc\n" {
		t.Fatalf("expected buffered short ASCII burst to flush with newline, got %q", got)
	}
}

func TestComposerSlashCommandEnterDoesNotUseBurstSuppression(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	clock := newFakeComposerClock()
	m.now = clock.Now

	for _, r := range []rune("/review") {
		next, _ := m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
		clock.Advance(5 * time.Millisecond)
	}

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected slash command enter to submit immediately")
	}
	m = next.(model)
	if got := m.composer.Value(); got != "" {
		t.Fatalf("expected submitted slash command to clear composer, got %q", got)
	}
}

func TestComposerShortNonASCIIBurstDoesNotSuppressSubmit(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	clock := newFakeComposerClock()
	m.now = clock.Now

	for _, text := range []string{"你好", "世界"} {
		next, _ := m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
		m = next.(model)
		clock.Advance(5 * time.Millisecond)
	}

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected short non-ASCII burst to keep normal submit semantics")
	}
	m = next.(model)
	if !m.running {
		t.Fatal("expected prompt submission to start running turn")
	}
	if got := m.composer.Value(); got != "" {
		t.Fatalf("expected submitted prompt to clear composer, got %q", got)
	}
}

func TestComposerPasteLikeNonASCIIBurstTreatsEnterAsNewline(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	clock := newFakeComposerClock()
	m.now = clock.Now

	next, _ := m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("你好 世界")})
	m = next.(model)
	clock.Advance(5 * time.Millisecond)

	next, _ = m.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if got := m.composer.Value(); got != "你好 世界\n" {
		t.Fatalf("expected newline inserted for non-ASCII paste-like burst, got %q", got)
	}
}

func TestComposerBurstFlushTickCommitsHeldASCII(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	clock := newFakeComposerClock()
	m.now = clock.Now

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("expected flush tick for held ASCII input")
	}
	m = next.(model)
	if got := m.composer.Value(); got != "" {
		t.Fatalf("expected rapid ASCII char to stay buffered before flush tick, got %q", got)
	}

	clock.Advance(composerBurstFlushDelay())
	next, followUp := m.Update(composerBurstFlushMsg(clock.Now()))
	if followUp != nil {
		t.Fatalf("expected no extra cmd after burst flush tick, got %v", followUp)
	}
	m = next.(model)
	if got := m.composer.Value(); got != "a" {
		t.Fatalf("expected flush tick to commit buffered ASCII char, got %q", got)
	}
}

func TestComposerHistoryRecallAndRestoreDraft(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/review")

	next, cmd := m.submitComposer()
	if cmd == nil {
		t.Fatal("expected local review command")
	}
	m = next.(model)
	if got := m.composer.Value(); got != "" {
		t.Fatalf("expected composer cleared after submit, got %q", got)
	}

	next, _ = m.updateMain(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(model)
	if got := m.composer.Value(); got != "/review" {
		t.Fatalf("expected history recall, got %q", got)
	}

	next, _ = m.updateMain(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	if got := m.composer.Value(); got != "" {
		t.Fatalf("expected draft restored after leaving history, got %q", got)
	}
}

func TestComposerHistoryDoesNotClobberNonEmptyDraft(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.history = []string{"/review"}
	m.composer.SetValue("draft")

	next, _ := m.updateMain(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(model)
	if got := updated.composer.Value(); got != "draft" {
		t.Fatalf("expected non-empty draft to stay intact, got %q", got)
	}
}

func TestComposerInputDoesNotTriggerInputShortcut(t *testing.T) {
	svc, spec := newTestService(t)
	if _, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true); err != nil {
		t.Fatalf("request input: %v", err)
	}
	m := openModelForTask(t, svc, spec.TaskID)
	if m.modal != modalInput {
		t.Fatalf("expected input modal to auto-open, got %v", m.modal)
	}
	next, _ := m.updateInputModal(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	_ = cmd
	updated := next.(model)
	if updated.modal != modalNone {
		t.Fatalf("expected composer input to avoid opening modal, got %v", updated.modal)
	}
	if updated.composerDraftValue() != "i" {
		t.Fatalf("expected composer to capture typed rune, got %q", updated.composerDraftValue())
	}
}

func TestComposerQuestionMarkDoesNotOpenHelpShortcut(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	_ = cmd
	updated := next.(model)
	if updated.modal != modalNone {
		t.Fatalf("expected question mark typed in composer to avoid opening help, got %v", updated.modal)
	}
	if updated.composerDraftValue() != "?" {
		t.Fatalf("expected composer to capture question mark, got %q", updated.composerDraftValue())
	}
}

func TestComposerInputDoesNotTriggerApprovalShortcut(t *testing.T) {
	svc, spec := newTestService(t)
	if _, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review"); err != nil {
		t.Fatalf("request approval: %v", err)
	}
	m := openModelForTask(t, svc, spec.TaskID)
	if m.modal != modalApprovals {
		t.Fatalf("expected approval modal to auto-open, got %v", m.modal)
	}
	next, _ := m.updateApprovalsModal(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	_ = cmd
	updated := next.(model)
	if updated.modal != modalNone {
		t.Fatalf("expected composer input to avoid reopening approvals, got %v", updated.modal)
	}
	if updated.composerDraftValue() != "a" {
		t.Fatalf("expected composer to capture typed rune, got %q", updated.composerDraftValue())
	}
}

func TestOpenTaskAutoOpensApprovalModal(t *testing.T) {
	svc, spec := newTestService(t)
	if _, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review"); err != nil {
		t.Fatalf("request approval: %v", err)
	}
	m := openModelForTask(t, svc, spec.TaskID)
	if m.modal != modalApprovals {
		t.Fatalf("expected approval modal to auto-open, got %v", m.modal)
	}
}

func TestOpenTaskAutoOpensInputModal(t *testing.T) {
	svc, spec := newTestService(t)
	if _, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true); err != nil {
		t.Fatalf("request input: %v", err)
	}
	m := openModelForTask(t, svc, spec.TaskID)
	if m.modal != modalInput {
		t.Fatalf("expected input modal to auto-open, got %v", m.modal)
	}
}

func TestDismissedApprovalDoesNotReopenUntilBlockerChanges(t *testing.T) {
	svc, spec := newTestService(t)
	first, err := svc.RequestApproval(context.Background(), spec.TaskID, "manual step", "operator review")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	m := openModelForTask(t, svc, spec.TaskID)
	if m.modal != modalApprovals {
		t.Fatalf("expected approval modal to auto-open, got %v", m.modal)
	}

	next, _ := m.updateApprovalsModal(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.modal != modalNone {
		t.Fatalf("expected approval modal to close on esc, got %v", m.modal)
	}
	if got := m.dismissedBlockerSig; !strings.Contains(got, first.ApprovalID) {
		t.Fatalf("expected dismissed blocker signature to record dismissed approval, got %q", got)
	}

	snapshot, err := svc.Status(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	_ = snapshot
	refresh := refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)()
	next, _ = m.Update(refresh)
	m = next.(model)
	if m.modal != modalNone {
		t.Fatalf("expected same dismissed approval to stay closed after refresh, got %v", m.modal)
	}

	second, err := svc.RequestApproval(context.Background(), spec.TaskID, "second step", "operator review")
	if err != nil {
		t.Fatalf("request second approval: %v", err)
	}
	refresh = refreshCmd(m.backend, spec.TaskID, m.session.SessionID, m.opts, false)()
	next, _ = m.Update(refresh)
	m = next.(model)
	if m.modal != modalApprovals {
		t.Fatalf("expected new blocker signature to reopen approvals, got %v", m.modal)
	}
	if got := currentBlockerSignature(m.snapshot); !strings.Contains(got, second.ApprovalID) {
		t.Fatalf("expected blocker signature to include new approval, got %q", got)
	}
}

func TestSubmitComposerExitQuits(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/exit")

	next, cmd := m.submitComposer()
	_ = next
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected quit message, got %T", cmd())
	}
}

func TestSubmitComposerExitDuringRunOpensInterruptConfirm(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.composer.SetValue("/exit")

	next, cmd := m.submitComposer()
	if cmd != nil {
		t.Fatalf("expected no immediate command while exit confirm is open, got %v", cmd)
	}
	updated := next.(model)
	if updated.modal != modalConfirmInterrupt {
		t.Fatalf("expected confirm interrupt modal, got %v", updated.modal)
	}
	if updated.confirmIndex != 1 {
		t.Fatalf("expected interrupt option selected by default, got %d", updated.confirmIndex)
	}
	if updated.composer.Value() != "" {
		t.Fatalf("expected /exit draft to clear once confirmation opens, got %q", updated.composer.Value())
	}
}

func TestSubmitComposerPlainTextHelpAliasOpensHelpModal(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("help")

	next, cmd := m.submitComposer()
	if cmd != nil {
		t.Fatalf("expected no immediate command for help alias, got %v", cmd)
	}
	updated := next.(model)
	if updated.modal != modalHelp {
		t.Fatalf("expected help modal from plain-text alias, got %v", updated.modal)
	}
	if updated.composer.Value() != "" {
		t.Fatalf("expected composer cleared after help alias, got %q", updated.composer.Value())
	}
}

func TestHelpModalDocumentsOverlayDismissBeforeInterrupt(t *testing.T) {
	view := renderHelpModal(80)
	normalized := strings.Join(strings.Fields(view), " ")
	if !strings.Contains(normalized, "Plain-text aliases: actions run resume review tasks back help approvals input refresh") {
		t.Fatalf("expected help modal to use compact alias cheat sheet:\n%s", view)
	}
	if !strings.Contains(normalized, "Esc hides the prompt overlay first; otherwise it opens interrupt confirmation while a turn is active") {
		t.Fatalf("expected help modal to document overlay-first esc semantics:\n%s", view)
	}
	if strings.Contains(normalized, "Ctrl+K opens the action palette for run, review, approvals, input, picker, tasks, back, refresh, help, and quit") {
		t.Fatalf("expected stale long action-palette copy to be removed:\n%s", view)
	}
	if strings.Contains(normalized, "Esc opens interrupt confirmation while a turn is active") {
		t.Fatalf("expected stale esc help copy to be removed:\n%s", view)
	}
}

func TestSubmitComposerPlainTextRunAliasStartsNormalizedPrompt(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("run")

	next, cmd := m.submitComposer()
	if cmd == nil {
		t.Fatal("expected prompt command for run alias")
	}
	updated := next.(model)
	if !updated.running {
		t.Fatal("expected run alias to start a prompt")
	}
	if updated.activePrompt != "/run" {
		t.Fatalf("expected run alias to normalize to /run, got %q", updated.activePrompt)
	}
}

func TestSubmitComposerMissionPromptStartsNormalizedPrompt(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/goal ship the docs refresh")

	next, cmd := m.submitComposer()
	if cmd == nil {
		t.Fatal("expected prompt command for goal slash prompt")
	}
	updated := next.(model)
	if !updated.running {
		t.Fatal("expected goal slash prompt to start a prompt")
	}
	if updated.activePrompt != "/goal ship the docs refresh" {
		t.Fatalf("expected goal slash prompt to pass through intact, got %q", updated.activePrompt)
	}
}

func TestSubmitComposerStatusAliasRefreshesLocally(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/status")

	next, cmd := m.submitComposer()
	if cmd == nil {
		t.Fatal("expected refresh command for /status")
	}
	updated := next.(model)
	if updated.running {
		t.Fatal("/status should not start a provider turn")
	}
}

func TestSubmitComposerUnknownSlashCommandReportsLocalDiagnostic(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/does-not-exist please guess")

	next, cmd := m.submitComposer()
	if cmd != nil {
		t.Fatalf("expected unknown slash command to stop locally, got %v", cmd)
	}
	updated := next.(model)
	if updated.running {
		t.Fatal("unknown slash command should not start a provider turn")
	}
	if !strings.Contains(updated.errorLine, "Unknown command") {
		t.Fatalf("expected unknown command diagnostic, got %q", updated.errorLine)
	}
}

func TestSubmitComposerPlainTextActionsAliasOpensActionPalette(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("actions")

	next, cmd := m.submitComposer()
	if cmd != nil {
		t.Fatalf("expected no immediate command for actions alias, got %v", cmd)
	}
	updated := next.(model)
	if updated.modal != modalActionPalette {
		t.Fatalf("expected action palette from plain-text alias, got %v", updated.modal)
	}
	if updated.composer.Value() != "" {
		t.Fatalf("expected composer cleared after actions alias, got %q", updated.composer.Value())
	}
}

func TestEditorFinishedMsgUpdatesComposerDraft(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)

	next, cmd := m.Update(editorFinishedMsg{Target: editorTargetComposer, Content: "review from editor"})
	if cmd != nil {
		t.Fatalf("expected no follow-up cmd for editor finish, got %v", cmd)
	}
	updated := next.(model)
	if updated.composer.Value() != "review from editor" {
		t.Fatalf("expected composer updated from editor, got %q", updated.composer.Value())
	}
	if updated.statusLine != "Draft updated from external editor." {
		t.Fatalf("unexpected status line %q", updated.statusLine)
	}
}

func TestEditorFinishedMsgUpdatesInputDraft(t *testing.T) {
	svc, spec := newTestService(t)
	if _, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true); err != nil {
		t.Fatalf("request input: %v", err)
	}
	m := openModelForTask(t, svc, spec.TaskID)
	if m.modal != modalInput {
		t.Fatalf("expected input modal, got %v", m.modal)
	}

	next, cmd := m.Update(editorFinishedMsg{Target: editorTargetInput, Content: "path/from/editor"})
	if cmd != nil {
		t.Fatalf("expected no follow-up cmd for editor finish, got %v", cmd)
	}
	updated := next.(model)
	if updated.inputBox.Value() != "path/from/editor" {
		t.Fatalf("expected input updated from editor, got %q", updated.inputBox.Value())
	}
	if updated.statusLine != "Input draft updated from external editor." {
		t.Fatalf("unexpected status line %q", updated.statusLine)
	}
}

func TestCtrlOShortcutOpensTaskPicker(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyCtrlO})
	if cmd == nil {
		t.Fatal("expected picker command from ctrl+o")
	}
	updated := next.(model)
	if updated.modal != modalPicker {
		t.Fatalf("expected picker modal from ctrl+o, got %v", updated.modal)
	}
}

func TestCtrlTShortcutOpensTaskNavigation(t *testing.T) {
	svc, specs := newProjectNavigationService(t)
	m := openModelForTask(t, svc, specs[0].TaskID)

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyCtrlT})
	if cmd == nil {
		t.Fatal("expected load-tasks command from ctrl+t")
	}
	updated := next.(model)
	if updated.tab != tabTasks {
		t.Fatalf("expected tasks tab from ctrl+t, got %v", updated.tab)
	}
	if updated.focus != focusInspector {
		t.Fatalf("expected inspector focus from ctrl+t, got %v", updated.focus)
	}
	if !strings.Contains(updated.statusLine, "Task navigation opened") {
		t.Fatalf("expected task navigation status, got %q", updated.statusLine)
	}
}

func TestCtrlBShortcutReturnsToPreviousTask(t *testing.T) {
	svc, specs := newProjectNavigationService(t)
	m := openModelForTask(t, svc, specs[0].TaskID)
	m.taskHistory = []string{specs[1].TaskID}

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyCtrlB})
	if cmd == nil {
		t.Fatal("expected open-task command from ctrl+b")
	}
	updated := next.(model)
	if got := len(updated.taskHistory); got != 0 {
		t.Fatalf("expected ctrl+b to consume task history, got %#v", updated.taskHistory)
	}
	msg := cmd()
	openMsg, ok := msg.(openTaskMsg)
	if !ok {
		t.Fatalf("expected openTaskMsg from ctrl+b, got %T", msg)
	}
	if openMsg.Session.TaskID != specs[1].TaskID {
		t.Fatalf("expected ctrl+b to target previous task %s, got %s", specs[1].TaskID, openMsg.Session.TaskID)
	}
}

func TestCtrlGShortcutOpensExternalEditorForComposer(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	t.Setenv("EDITOR", "/bin/true")
	m.composer.SetValue("draft")

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyCtrlG})
	if cmd == nil {
		t.Fatal("expected external editor cmd from ctrl+g")
	}
	updated := next.(model)
	if updated.statusLine != "Save and close external editor to continue." {
		t.Fatalf("unexpected status line %q", updated.statusLine)
	}
}

func TestCtrlKShortcutOpensActionPalette(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyCtrlK})
	if cmd != nil {
		t.Fatalf("expected no external cmd from ctrl+k, got %v", cmd)
	}
	updated := next.(model)
	if updated.modal != modalActionPalette {
		t.Fatalf("expected action palette modal from ctrl+k, got %v", updated.modal)
	}
}

func TestActionPaletteRunStartsPromptAndPreservesDraft(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("follow-up draft")

	next, _ := m.openActionPalette()
	palette := next.(model)
	item, ok := findActionPaletteItemByCommand(palette.actionPaletteItems(), "/run")
	if !ok {
		t.Fatal("expected run action in action palette")
	}

	next, cmd := palette.executeActionPaletteSelection(item)
	if cmd == nil {
		t.Fatal("expected prompt command from run action")
	}
	updated := next.(model)
	if !updated.running {
		t.Fatal("expected run action to start a prompt")
	}
	if updated.activePrompt != "/run" {
		t.Fatalf("expected /run prompt from action palette, got %q", updated.activePrompt)
	}
	if updated.composer.Value() != "follow-up draft" {
		t.Fatalf("expected action palette run to preserve existing draft, got %q", updated.composer.Value())
	}
}

func TestActionPaletteMissionStartsPromptAndPreservesDraft(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("draft objective")

	next, _ := m.openActionPalette()
	palette := next.(model)
	item, ok := findActionPaletteItemByCommand(palette.actionPaletteItems(), "/mission")
	if !ok {
		t.Fatal("expected mission action in action palette")
	}

	next, cmd := palette.executeActionPaletteSelection(item)
	if cmd == nil {
		t.Fatal("expected prompt command from mission action")
	}
	updated := next.(model)
	if !updated.running {
		t.Fatal("expected mission action to start a prompt")
	}
	if updated.activePrompt != "/mission" {
		t.Fatalf("expected /mission prompt from action palette, got %q", updated.activePrompt)
	}
	if updated.composer.Value() != "draft objective" {
		t.Fatalf("expected action palette mission to preserve existing draft, got %q", updated.composer.Value())
	}
}

func TestActionPaletteBackReportsUnavailableWithoutHistory(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)

	next, _ := m.openActionPalette()
	palette := next.(model)
	item, ok := findActionPaletteItemByCommand(palette.actionPaletteItems(), "/back")
	if !ok {
		t.Fatal("expected back action in action palette")
	}

	next, cmd := palette.executeActionPaletteSelection(item)
	if cmd != nil {
		t.Fatalf("expected no command for disabled back action, got %v", cmd)
	}
	updated := next.(model)
	if updated.modal != modalActionPalette {
		t.Fatalf("expected action palette to remain open on unavailable action, got %v", updated.modal)
	}
	if updated.errorLine != "No previous task is available in local navigation history." {
		t.Fatalf("unexpected unavailable reason %q", updated.errorLine)
	}
}

func TestCtrlKActionPaletteUsesSlashDraftAsFilter(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/he")

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyCtrlK})
	if cmd != nil {
		t.Fatalf("expected no external cmd from ctrl+k with slash draft, got %v", cmd)
	}
	updated := next.(model)
	if updated.modal != modalActionPalette {
		t.Fatalf("expected action palette modal from ctrl+k, got %v", updated.modal)
	}
	if updated.actionFilter != "he" {
		t.Fatalf("expected ctrl+k to seed action filter from slash draft, got %q", updated.actionFilter)
	}
	filtered := updated.filteredActionPaletteItems()
	if len(filtered) == 0 || filtered[0].Command != "/help" {
		t.Fatalf("expected /help to lead seeded action filter results, got %+v", filtered)
	}
}

func TestComposerSlashSuggestionsRender(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/mi")

	view := sanitizeView(m.View())
	for _, want := range []string{"Commands", "/mission [ready]", "Enter waits until suggestions are resolved"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected slash suggestions to render %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Prompt Overlay") {
		t.Fatalf("expected slash suggestions to use contextual overlay title:\n%s", view)
	}
}

func TestPromptOverlayPrefersSlashSuggestionsOverQueuedPreview(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{"/memory milestone queued follow-up"}
	m.composer.SetValue("/re")

	view := sanitizeView(m.View())
	if !strings.Contains(view, "Commands") {
		t.Fatalf("expected prompt overlay to render slash suggestions:\n%s", view)
	}
	if strings.Contains(view, "1. /memory milestone queued follow-up") {
		t.Fatalf("expected slash suggestions to suppress queued preview while active:\n%s", view)
	}
}

func TestComposerSlashSuggestionsUseTabCompletion(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/he")

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyTab})
	if cmd != nil {
		t.Fatalf("expected no submit on tab completion, got %v", cmd)
	}
	updated := next.(model)
	if updated.composer.Value() != "/help" {
		t.Fatalf("expected tab to complete to /help, got %q", updated.composer.Value())
	}
	if updated.focus != focusComposer {
		t.Fatalf("expected tab completion to keep composer focus, got %v", updated.focus)
	}
}

func TestComposerExactSlashCommandHidesSuggestions(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/help")

	view := sanitizeView(m.View())
	if strings.Contains(view, "Commands") {
		t.Fatalf("expected exact slash command to hide suggestions:\n%s", view)
	}
}

func TestComposerEnterDoesNotSubmitWhileSlashSuggestionsVisible(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/he")

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected no submit while slash suggestions are visible, got %v", cmd)
	}
	updated := next.(model)
	if updated.composer.Value() != "/he" {
		t.Fatalf("expected partial slash draft to remain intact, got %q", updated.composer.Value())
	}
	if updated.statusLine != "Command suggestions are open. Press Tab to complete or keep typing." {
		t.Fatalf("unexpected status line %q", updated.statusLine)
	}
}

func TestEscDismissesSlashPromptOverlayUntilNextInput(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.composer.SetValue("/he")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatalf("expected no cmd when dismissing prompt overlay, got %v", cmd)
	}
	dismissed := next.(model)
	if dismissed.promptOverlayVisible() {
		t.Fatal("expected prompt overlay to be hidden after esc")
	}
	if dismissed.statusLine != "Prompt overlay hidden. Keep typing to reopen." {
		t.Fatalf("unexpected dismiss status %q", dismissed.statusLine)
	}

	next, _ = dismissed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	reopened := next.(model)
	if !reopened.promptOverlayVisible() {
		t.Fatal("expected prompt overlay to reopen after next input")
	}
}

func TestEscDismissesQueuedPromptOverlayBeforeInterruptConfirm(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.queuedPrompts = []string{"/memory milestone queued follow-up"}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatalf("expected no cmd when hiding queued prompt overlay, got %v", cmd)
	}
	dismissed := next.(model)
	if dismissed.modal != modalNone {
		t.Fatalf("expected no modal on first esc, got %v", dismissed.modal)
	}
	if dismissed.promptOverlayVisible() {
		t.Fatal("expected queued prompt overlay hidden after esc")
	}
	if dismissed.statusLine != "Prompt overlay hidden. Press Esc again to interrupt or keep typing to reopen." {
		t.Fatalf("unexpected queued overlay dismiss status %q", dismissed.statusLine)
	}

	next, cmd = dismissed.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatalf("expected no immediate cmd on second esc before confirmation, got %v", cmd)
	}
	confirmed := next.(model)
	if confirmed.modal != modalConfirmInterrupt {
		t.Fatalf("expected second esc to open interrupt confirm, got %v", confirmed.modal)
	}
}

func TestComposerSlashSuggestionsConsumeArrowKeysBeforeHistory(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.history = []string{"/review"}
	m.composer.SetValue("/r")

	next, _ := m.updateMain(tea.KeyMsg{Type: tea.KeyDown})
	updated := next.(model)
	if updated.composer.Value() != "/r" {
		t.Fatalf("expected slash suggestion navigation to preserve draft, got %q", updated.composer.Value())
	}
	if updated.composerActionIndex != 1 {
		t.Fatalf("expected slash suggestion navigation to move selection, got %d", updated.composerActionIndex)
	}
}

func TestCtrlGInInputModalOpensExternalEditor(t *testing.T) {
	svc, spec := newTestService(t)
	if _, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true); err != nil {
		t.Fatalf("request input: %v", err)
	}
	t.Setenv("EDITOR", "/bin/true")
	m := openModelForTask(t, svc, spec.TaskID)
	if m.modal != modalInput {
		t.Fatalf("expected input modal, got %v", m.modal)
	}

	next, cmd := m.updateInputModal(tea.KeyMsg{Type: tea.KeyCtrlG})
	if cmd == nil {
		t.Fatal("expected external editor cmd from ctrl+g in input modal")
	}
	updated := next.(model)
	if updated.statusLine != "Save and close external editor to continue." {
		t.Fatalf("unexpected status line %q", updated.statusLine)
	}
}

func TestEscRestoresPreviousPromptWhenComposerEmpty(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.snapshot.Messages = []task.SessionMessage{
		{Role: "operator", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "operator", Content: "restore me"},
	}

	next, cmd := m.updateMain(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatalf("expected no cmd on first esc, got %v", cmd)
	}
	primed := next.(model)
	if !primed.escPrimed {
		t.Fatal("expected first esc to prime restore mode")
	}
	if primed.statusLine != "Press Esc again to edit the previous prompt." {
		t.Fatalf("unexpected prime status %q", primed.statusLine)
	}

	next, cmd = primed.updateMain(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatalf("expected no cmd on second esc, got %v", cmd)
	}
	restored := next.(model)
	if restored.composer.Value() != "restore me" {
		t.Fatalf("expected previous prompt restored, got %q", restored.composer.Value())
	}
	if restored.statusLine != "Previous prompt restored to composer." {
		t.Fatalf("unexpected restore status %q", restored.statusLine)
	}
	if restored.escPrimed {
		t.Fatal("expected restore mode cleared after second esc")
	}
}

func TestNonEscKeyClearsEscPriming(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.escPrimed = true
	m.escPrimedAt = time.Now()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	updated := next.(model)
	if updated.escPrimed {
		t.Fatal("expected non-esc key to clear esc priming")
	}
}

func findActionPaletteItemByCommand(items []actionPaletteItem, command string) (actionPaletteItem, bool) {
	for _, item := range items {
		if item.Command == command {
			return item, true
		}
	}
	return actionPaletteItem{}, false
}

func TestSubmitComposerRefreshAllowedDuringRun(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.composer.SetValue("/refresh")

	next, cmd := m.submitComposer()
	if cmd == nil {
		t.Fatal("expected refresh command while running")
	}
	updated := next.(model)
	if updated.errorLine != "" {
		t.Fatalf("expected no active-turn error for /refresh, got %q", updated.errorLine)
	}
	refreshed := cmd()
	msg, ok := refreshed.(refreshMsg)
	if !ok {
		t.Fatalf("expected refresh message, got %T", refreshed)
	}
	if msg.Err != nil {
		t.Fatalf("refresh returned error: %v", msg.Err)
	}
	if msg.Snapshot.TaskView.Task.TaskID != spec.TaskID {
		t.Fatalf("expected refresh snapshot for %s, got %+v", spec.TaskID, msg.Snapshot.TaskView.Task)
	}
}

func TestSubmitComposerPickerBlockedDuringRun(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.composer.SetValue("/picker")

	next, cmd := m.submitComposer()
	if cmd != nil {
		t.Fatalf("expected no picker command while active turn is running, got %v", cmd)
	}
	updated := next.(model)
	if updated.modal != modalNone {
		t.Fatalf("expected picker modal to stay closed during active turn, got %v", updated.modal)
	}
	if updated.errorLine != "Cannot switch tasks while a turn is active." {
		t.Fatalf("expected active-turn picker error, got %q", updated.errorLine)
	}
	if updated.taskID != spec.TaskID {
		t.Fatalf("expected task to remain unchanged, got %q", updated.taskID)
	}
}

func TestSubmitComposerBackBlockedDuringRun(t *testing.T) {
	svc, spec := newTestService(t)
	m := openModelForTask(t, svc, spec.TaskID)
	m.running = true
	m.taskHistory = []string{"TASK-previous"}
	m.composer.SetValue("/back")

	next, cmd := m.submitComposer()
	if cmd != nil {
		t.Fatalf("expected no /back command while active turn is running, got %v", cmd)
	}
	updated := next.(model)
	if updated.errorLine != "Cannot switch tasks while a turn is active." {
		t.Fatalf("expected active-turn back error, got %q", updated.errorLine)
	}
	if got := len(updated.taskHistory); got != 1 || updated.taskHistory[0] != "TASK-previous" {
		t.Fatalf("expected task history to remain unchanged, got %#v", updated.taskHistory)
	}
}

func TestWrapPrefixedLineSplitsLongToken(t *testing.T) {
	lines := wrapPrefixedLine(10, "  ", "TASK-20260327-abcdefghijklmnopqrstuvwxyz")
	if len(lines) < 2 {
		t.Fatalf("expected long token to wrap, got %v", lines)
	}
	for _, line := range lines {
		if lipgloss.Width(line) > 10 {
			t.Fatalf("wrapped line exceeds width 10: %q", line)
		}
	}
}

func newTestService(t *testing.T) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "demo",
		Objective: "verify coding flow",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return svc, spec
}

func newEmptyTestService(t *testing.T) *ngenrt.Service {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	return ngenrt.New(dir, task.DefaultConfig())
}

func newLongHeaderService(t *testing.T) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	svc := ngenrt.New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "LONGTOKENABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		Objective: "verify header wrapping",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create long-header task: %v", err)
	}
	return svc, spec
}

func openModelForTask(t *testing.T, svc *ngenrt.Service, taskID string) model {
	t.Helper()
	m := newModel(NewBackend(svc), Options{
		TaskID:       taskID,
		PollInterval: time.Second,
		EventLimit:   50,
		ProviderMode: svc.Config.Provider.Mode,
	})
	m.ready = true
	m.width = 120
	m.height = 40
	m.resize()
	msg := openTaskCmd(m.backend, taskOpenRequest{TaskID: taskID}, m.opts)()
	next, _ := m.Update(msg)
	return next.(model)
}

func openSimpleModelForTask(t *testing.T, svc *ngenrt.Service, taskID string) model {
	t.Helper()
	m := newModel(NewBackend(svc), Options{
		TaskID:       taskID,
		PollInterval: time.Second,
		EventLimit:   50,
		ProviderMode: svc.Config.Provider.Mode,
		SimpleMode:   true,
	})
	m.ready = true
	m.width = 120
	m.height = 40
	m.resize()
	msg := openTaskCmd(m.backend, taskOpenRequest{TaskID: taskID}, m.opts)()
	next, _ := m.Update(msg)
	return next.(model)
}

type fakeComposerClock struct {
	now time.Time
}

func newFakeComposerClock() *fakeComposerClock {
	return &fakeComposerClock{now: time.Unix(1711500000, 0)}
}

func (c *fakeComposerClock) Now() time.Time {
	return c.now
}

func (c *fakeComposerClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func sanitizeView(in string) string {
	replacements := []*regexp.Regexp{
		regexp.MustCompile(`TASK-[0-9TZ-]+-[0-9a-f]+`),
		regexp.MustCompile(`SES-[0-9TZ-]+-[0-9a-f]+`),
		regexp.MustCompile(`EVT-[0-9TZ-]+-[0-9a-f]+`),
		regexp.MustCompile(`MSG-[0-9TZ-]+-[0-9a-f]+`),
		regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z`),
	}
	out := in
	for _, re := range replacements {
		out = re.ReplaceAllString(out, "<id>")
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func createLateCodingTask(t *testing.T, svc *ngenrt.Service, title, objective string) task.Spec {
	t.Helper()
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     title,
		Objective: objective,
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test passes"},
		},
		WorkspaceRoot: svc.Store.WorkspaceRoot,
	})
	if err != nil {
		t.Fatalf("create late coding task: %v", err)
	}
	return spec
}

func assertMaxLineWidth(t *testing.T, view string, width int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimRight(line, " ")
		if lipgloss.Width(trimmed) > width {
			t.Fatalf("line exceeds width %d: %q\nview:\n%s", width, line, view)
		}
	}
}

func assertMaxViewHeight(t *testing.T, view string, height int) {
	t.Helper()
	if got := lipgloss.Height(view); got > height {
		t.Fatalf("view exceeds height %d: got %d\n%s", height, got, view)
	}
}
