package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"ngen/internal/task"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type focusTarget int

const (
	focusComposer focusTarget = iota
	focusTranscript
	focusInspector
)

func (f focusTarget) String() string {
	switch f {
	case focusTranscript:
		return "transcript"
	case focusInspector:
		return "inspector"
	default:
		return "composer"
	}
}

type inspectorTab int

const (
	tabOverview inspectorTab = iota
	tabPlan
	tabCriteria
	tabWorkers
	tabBlockers
	tabMemory
	tabProject
	tabTasks
)

var allInspectorTabs = []inspectorTab{tabOverview, tabPlan, tabCriteria, tabWorkers, tabBlockers, tabMemory, tabProject, tabTasks}

func (t inspectorTab) String() string {
	switch t {
	case tabPlan:
		return "2 Plan"
	case tabCriteria:
		return "3 Criteria"
	case tabWorkers:
		return "4 Workers"
	case tabBlockers:
		return "5 Blockers"
	case tabMemory:
		return "6 Memory"
	case tabProject:
		return "7 Project"
	case tabTasks:
		return "8 Tasks"
	default:
		return "1 Overview"
	}
}

type modalKind int

const (
	modalNone modalKind = iota
	modalPicker
	modalApprovals
	modalInput
	modalHelp
	modalActionPalette
	modalConfirmInterrupt
)

type Options struct {
	TaskID       string
	Inline       bool
	PollInterval time.Duration
	EventLimit   int
	ProviderMode string
	SimpleMode   bool
}

type taskListMsg struct {
	Tasks []task.TaskListEntry
	Err   error
}

type openTaskMsg struct {
	Session        task.Session
	Snapshot       Snapshot
	Err            error
	PreviousTaskID string
	PushPrevious   bool
	Status         string
}

type refreshMsg struct {
	Snapshot Snapshot
	Err      error
}

type promptFinishedMsg struct {
	Seq int
	Err error
}

type actionFinishedMsg struct {
	Status string
	Err    error
}

type pollTickMsg time.Time
type composerBurstFlushMsg time.Time

type taskDraftState struct {
	Draft string
}

type pendingTaskOpenState struct {
	TargetTaskID      string
	PreviousTaskID    string
	PreviousTaskDraft string
}

const escRestoreWindow = time.Second

type model struct {
	backend *Backend
	opts    Options

	width  int
	height int

	taskID  string
	session task.Session

	snapshot Snapshot
	tasks    []task.TaskListEntry

	transcript viewport.Model
	inspector  viewport.Model
	composer   textarea.Model
	inputBox   textarea.Model

	focus                     focusTarget
	tab                       inspectorTab
	modal                     modalKind
	pickerIndex               int
	pickerFilter              string
	actionIndex               int
	actionFilter              string
	composerActionIndex       int
	queuedPromptPreviewIndex  int
	approvalIndex             int
	confirmIndex              int
	selectedWorker            int
	selectedWorkerID          string
	selectedRelatedTask       int
	selectedRelatedID         string
	queuedPrompts             []string
	taskHistory               []string
	taskDrafts                map[string]taskDraftState
	pendingTaskOpen           pendingTaskOpenState
	history                   []string
	historyIndex              int
	historyDraft              string
	dismissedBlockerSig       string
	dismissedPromptOverlaySig string
	composerBurst             composerBurst
	escPrimed                 bool
	escPrimedAt               time.Time

	running      bool
	turnCancel   context.CancelFunc
	pendingAbort bool
	promptSeq    int

	runningPromptSeq       int
	activePrompt           string
	activePromptBaseEvents int

	statusLine string
	errorLine  string
	ready      bool
	now        func() time.Time
}

func newModel(backend *Backend, opts Options) model {
	composer := textarea.New()
	if opts.SimpleMode {
		composer.Placeholder = "Type your message..."
	} else {
		composer.Placeholder = "Ask, steer, or type run/review/tasks/back"
	}
	composer.Prompt = "> "
	composer.ShowLineNumbers = false
	composer.SetHeight(3)
	composer.Focus()

	inputBox := textarea.New()
	inputBox.Placeholder = "Answer the pending input request"
	inputBox.Prompt = "> "
	inputBox.ShowLineNumbers = false
	inputBox.SetHeight(3)

	m := model{
		backend:      backend,
		opts:         opts,
		taskID:       opts.TaskID,
		focus:        focusComposer,
		tab:          tabOverview,
		modal:        initialModal(opts.TaskID, opts.SimpleMode),
		transcript:   viewport.New(0, 0),
		inspector:    viewport.New(0, 0),
		composer:     composer,
		inputBox:     inputBox,
		pickerIndex:  0,
		taskDrafts:   make(map[string]taskDraftState),
		historyIndex: -1,
		now:          time.Now,
	}
	m.syncFocusStates()
	return m
}

func initialModal(taskID string, simpleMode bool) modalKind {
	if strings.TrimSpace(taskID) == "" && !simpleMode {
		return modalPicker
	}
	return modalNone
}

func (m model) Init() tea.Cmd {
	if strings.TrimSpace(m.taskID) != "" {
		return openTaskCmd(m.backend, taskOpenRequest{TaskID: m.taskID}, m.opts)
	}
	if m.opts.SimpleMode {
		return openRecentOrCreateTaskCmd(m.backend, m.opts)
	}
	return tea.Batch(loadTasksCmd(m.backend), pollTickCmd(m.opts.PollInterval))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.resize()
		m.refreshViews(true)
		return m, nil
	case taskListMsg:
		if msg.Err != nil {
			m.errorLine = msg.Err.Error()
			return m, nil
		}
		m.tasks = msg.Tasks
		m.clampPickerIndex()
		if len(m.tasks) == 0 && m.modal == modalPicker {
			m.statusLine = "No tasks found in this workspace. Use `ngen task create ...` first."
		}
		if m.tab == tabTasks {
			m.refreshViews(false)
		}
		return m, nil
	case openTaskMsg:
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		m.clearEscPriming()
		if msg.Err != nil {
			m.pendingTaskOpen = pendingTaskOpenState{}
			m.errorLine = msg.Err.Error()
			return m, nil
		}
		pendingOpenMatch := strings.TrimSpace(m.pendingTaskOpen.TargetTaskID) != "" &&
			strings.TrimSpace(m.pendingTaskOpen.TargetTaskID) == strings.TrimSpace(msg.Session.TaskID)
		carryDraft := ""
		if pendingOpenMatch {
			carryDraft = m.composerDraftValue()
			m.setTaskDraft(m.pendingTaskOpen.PreviousTaskID, m.pendingTaskOpen.PreviousTaskDraft)
		} else {
			m.saveTaskDraft(m.taskID)
		}
		if msg.PushPrevious && msg.PreviousTaskID != "" && msg.PreviousTaskID != msg.Session.TaskID {
			m.pushTaskHistory(msg.PreviousTaskID)
		}
		m.session = msg.Session
		m.taskID = msg.Session.TaskID
		m.snapshot = msg.Snapshot
		m.modal = modalNone
		m.queuedPrompts = nil
		m.selectedWorker = 0
		m.selectedWorkerID = ""
		m.selectedRelatedTask = 0
		m.selectedRelatedID = ""
		m.composerActionIndex = 0
		m.focus = focusComposer
		m.dismissedBlockerSig = ""
		m.dismissedPromptOverlaySig = ""
		m.restoreTaskDraft(msg.Session.TaskID)
		if carryDraft != "" {
			m.composer.SetValue(m.composer.Value() + carryDraft)
			m.composer.CursorEnd()
		}
		m.pendingTaskOpen = pendingTaskOpenState{}
		m.inspector.GotoTop()
		m.autoOpenBlockerModal()
		m.syncFocusStates()
		if strings.TrimSpace(msg.Status) != "" {
			m.statusLine = msg.Status
		} else {
			m.statusLine = "TUI session started."
		}
		m.errorLine = ""
		m.refreshViews(true)
		return m, tea.Batch(pollTickCmd(m.opts.PollInterval), loadTasksCmd(m.backend))
	case refreshMsg:
		if msg.Err != nil {
			m.errorLine = msg.Err.Error()
			return m, nil
		}
		m.snapshot = msg.Snapshot
		settled := false
		if m.shouldAutoSettleRunningPrompt(msg.Snapshot) {
			m.running = false
			m.turnCancel = nil
			m.runningPromptSeq = 0
			m.activePrompt = ""
			m.activePromptBaseEvents = 0
			m.statusLine = "Prompt completed."
			m.errorLine = ""
			settled = true
		}
		m.errorLine = ""
		if currentBlockerSignature(m.snapshot) == "" {
			m.dismissedBlockerSig = ""
		}
		m.autoOpenBlockerModal()
		m.refreshViews(false)
		var cmds []tea.Cmd
		if settled {
			if cmd := m.maybeStartQueuedPrompt(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.shouldLoadTaskList() {
			cmds = append(cmds, loadTasksCmd(m.backend))
		}
		return m, tea.Batch(cmds...)
	case promptFinishedMsg:
		if msg.Seq != 0 && msg.Seq != m.runningPromptSeq {
			return m, nil
		}
		m.running = false
		m.turnCancel = nil
		m.runningPromptSeq = 0
		m.activePrompt = ""
		m.activePromptBaseEvents = 0
		if m.pendingAbort {
			m.pendingAbort = false
			dropped := m.dropQueuedPrompts()
			m.statusLine = "Interrupt requested. Cancelling session."
			if dropped > 0 {
				m.statusLine = fmt.Sprintf("Interrupt requested. Cancelling session. Dropping %d queued prompt(s).", dropped)
			}
			return m, cancelSessionCmd(m.backend, m.session.SessionID)
		}
		if msg.Err != nil {
			m.errorLine = tuiPromptErrorLine(msg.Err)
			if isProviderConfigError(msg.Err) {
				m.statusLine = "Provider configuration blocked the prompt."
			}
		} else {
			m.statusLine = "Prompt completed."
			m.errorLine = ""
		}
		if cmd := m.maybeStartQueuedPrompt(); cmd != nil {
			return m, cmd
		}
		return m, refreshCmd(m.backend, m.taskID, m.session.SessionID, m.opts, m.tab == tabMemory)
	case actionFinishedMsg:
		if msg.Err != nil {
			m.errorLine = msg.Err.Error()
		} else {
			switch m.modal {
			case modalApprovals, modalInput:
				m.modal = modalNone
				m.dismissedBlockerSig = ""
			}
			m.syncFocusStates()
			m.statusLine = msg.Status
			m.errorLine = ""
		}
		return m, refreshCmd(m.backend, m.taskID, m.session.SessionID, m.opts, m.tab == tabMemory)
	case editorFinishedMsg:
		if msg.Err != nil {
			m.errorLine = msg.Err.Error()
			return m, nil
		}
		switch msg.Target {
		case editorTargetInput:
			m.inputBox.SetValue(msg.Content)
			m.inputBox.CursorEnd()
			m.statusLine = "Input draft updated from external editor."
		default:
			m.composer.SetValue(msg.Content)
			m.composer.CursorEnd()
			m.statusLine = "Draft updated from external editor."
		}
		m.errorLine = ""
		return m, nil
	case pollTickMsg:
		if m.taskID == "" || m.session.SessionID == "" {
			if !m.shouldLoadTaskList() {
				return m, nil
			}
			return m, tea.Batch(
				loadTasksCmd(m.backend),
				pollTickCmd(m.opts.PollInterval),
			)
		}
		cmds := []tea.Cmd{
			refreshCmd(m.backend, m.taskID, m.session.SessionID, m.opts, m.tab == tabMemory),
			pollTickCmd(m.opts.PollInterval),
		}
		if m.shouldLoadTaskList() {
			cmds = append(cmds, loadTasksCmd(m.backend))
		}
		return m, tea.Batch(cmds...)
	case composerBurstFlushMsg:
		m.flushComposerBurstIfDue(time.Time(msg))
		return m, nil
	case tea.KeyMsg:
		if msg.String() != "esc" {
			m.clearEscPriming()
			m.clearPromptOverlayDismissal()
		}
		if handled, next, cmd := m.handleGlobalKey(msg); handled {
			return next, cmd
		}
		if m.modal != modalNone {
			return m.updateModal(msg)
		}
		return m.updateMain(msg)
	}
	return m, nil
}

func (m model) handleGlobalKey(msg tea.KeyMsg) (bool, tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.opts.SimpleMode && m.running {
			next, cmd := m.requestInterrupt()
			return true, next, cmd
		}
		if m.running && m.modal != modalConfirmInterrupt {
			m.openInterruptConfirm()
			return true, m, nil
		}
	case "ctrl+d":
		if !m.running && m.modal == modalNone && m.focus == focusComposer {
			m.flushComposerBurstNow()
			m.composerBurst.Clear()
			if strings.TrimSpace(m.composerDraftValue()) == "" {
				return true, m, tea.Quit
			}
			return true, m, nil
		}
	}
	return false, m, nil
}

func (m model) updateModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.modal {
	case modalPicker:
		return m.updatePicker(msg)
	case modalApprovals:
		return m.updateApprovalsModal(msg)
	case modalInput:
		return m.updateInputModal(msg)
	case modalHelp:
		if msg.String() == "esc" || msg.String() == "enter" || msg.String() == "?" {
			m.modal = modalNone
			m.syncFocusStates()
			return m, nil
		}
		return m, nil
	case modalActionPalette:
		return m.updateActionPalette(msg)
	case modalConfirmInterrupt:
		return m.updateConfirmModal(msg)
	default:
		m.modal = modalNone
		m.syncFocusStates()
		return m, nil
	}
}

func (m model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	filtered := m.filteredTasks()
	switch msg.String() {
	case "ctrl+c", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.pickerIndex > 0 {
			m.pickerIndex--
		}
		return m, nil
	case "down", "j":
		if m.pickerIndex < len(filtered)-1 {
			m.pickerIndex++
		}
		return m, nil
	case "enter":
		if len(filtered) == 0 {
			return m, nil
		}
		m.pickerFilter = ""
		return m.beginTaskOpen(filtered[m.pickerIndex].TaskID, true, fmt.Sprintf("Switched to task %s.", filtered[m.pickerIndex].TaskID))
	case "backspace":
		if len(m.pickerFilter) > 0 {
			runes := []rune(m.pickerFilter)
			m.pickerFilter = string(runes[:len(runes)-1])
			m.clampPickerIndex()
		}
		return m, nil
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		m.pickerFilter = appendPickerCommand(m.pickerFilter, msg.Runes)
		m.pickerIndex = 0
		switch strings.ToLower(strings.TrimSpace(m.pickerFilter)) {
		case "/exit", "/quit":
			return m, tea.Quit
		}
	}
	m.clampPickerIndex()
	return m, nil
}

func appendPickerCommand(current string, incoming []rune) string {
	if len(incoming) == 0 {
		return current
	}
	return current + string(incoming)
}

func normalizeComposerAlias(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" || strings.HasPrefix(trimmed, "/") {
		return trimmed
	}
	if trimmed == "?" {
		return "/help"
	}
	normalized := strings.ToLower(trimmed)
	normalized = strings.Trim(normalized, "!?.,，。！？")
	normalized = strings.Join(strings.Fields(normalized), " ")
	switch normalized {
	case "help", "帮助":
		return "/help"
	case "approvals", "approval", "审批":
		return "/approvals"
	case "input", "reply", "输入":
		return "/input"
	case "picker", "task list", "tasklist", "任务列表", "切换任务":
		return "/picker"
	case "tasks", "related", "related tasks", "相关任务":
		return "/tasks"
	case "back", "返回":
		return "/back"
	case "status", "refresh", "reload", "刷新":
		return "/refresh"
	case "clear", "清空":
		return "/clear"
	case "review", "审查":
		return "/review"
	case "mission", "missions", "goal", "goals", "目标", "任务目标":
		return "/mission"
	case "quit", "exit", "退出":
		return "/quit"
	case "run", "运行":
		return "/run"
	case "resume", "继续":
		return "/resume"
	case "actions", "menu", "palette", "命令", "菜单":
		return "/actions"
	default:
		return trimmed
	}
}

func (m model) updateApprovalsModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	pending := append([]task.ApprovalRecord{}, pendingApprovalRecords(m.snapshot.Approvals)...)
	pending = append(pending, pendingApprovalRecords(m.snapshot.OwnedApprovals)...)
	if len(pending) == 0 {
		m.modal = modalNone
		m.syncFocusStates()
		return m, nil
	}
	if m.approvalIndex >= len(pending) {
		m.approvalIndex = len(pending) - 1
	}
	switch msg.String() {
	case "esc":
		m.dismissCurrentBlocker()
		m.modal = modalNone
		m.syncFocusStates()
		return m, nil
	case "up", "k":
		if m.approvalIndex > 0 {
			m.approvalIndex--
		}
	case "down", "j":
		if m.approvalIndex < len(pending)-1 {
			m.approvalIndex++
		}
	case "a":
		return m, approveCmd(m.backend, m.taskID, pending[m.approvalIndex].ApprovalID)
	case "d":
		return m, denyCmd(m.backend, m.taskID, pending[m.approvalIndex].ApprovalID)
	}
	return m, nil
}

func (m model) updateInputModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dismissCurrentBlocker()
		m.modal = modalNone
		m.syncFocusStates()
		return m, nil
	case "ctrl+g":
		return m.openInputInEditor()
	case "enter":
		record, ok := pendingInputRecord(m.snapshot.Inputs)
		if !ok {
			m.modal = modalNone
			m.syncFocusStates()
			return m, nil
		}
		value := strings.TrimSpace(m.inputBox.Value())
		if value == "" {
			m.errorLine = "Input response cannot be empty."
			return m, nil
		}
		return m, respondInputCmd(m.backend, m.taskID, record.RequestID, value)
	}
	var cmd tea.Cmd
	m.inputBox, cmd = m.inputBox.Update(msg)
	return m, cmd
}

func (m model) updateConfirmModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.modal = modalNone
		m.syncFocusStates()
		return m, nil
	case "ctrl+c":
		return m.confirmInterrupt()
	case "up", "k":
		if m.confirmIndex > 0 {
			m.confirmIndex--
		}
	case "down", "j":
		if m.confirmIndex < 1 {
			m.confirmIndex++
		}
	case "enter":
		if m.confirmIndex == 0 {
			m.modal = modalNone
			m.syncFocusStates()
			return m, nil
		}
		return m.confirmInterrupt()
	}
	return m, nil
}

func (m model) confirmInterrupt() (tea.Model, tea.Cmd) {
	m.modal = modalNone
	m.syncFocusStates()
	return m.requestInterrupt()
}

func (m model) requestInterrupt() (tea.Model, tea.Cmd) {
	if m.turnCancel != nil {
		m.pendingAbort = true
		m.turnCancel()
		m.statusLine = "Interrupt requested. Waiting for the current turn to stop."
	}
	return m, nil
}

func (m model) updateMain(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.running {
			m.openInterruptConfirm()
			return m, nil
		}
		return m, tea.Quit
	case "ctrl+o":
		if m.opts.SimpleMode {
			m.errorLine = taskConsoleDisabledMessage()
			return m, nil
		}
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		return m.openTaskPicker()
	case "ctrl+t":
		if m.opts.SimpleMode {
			m.errorLine = taskConsoleDisabledMessage()
			return m, nil
		}
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		return m.openTaskNavigation()
	case "ctrl+b":
		if m.opts.SimpleMode {
			m.errorLine = taskConsoleDisabledMessage()
			return m, nil
		}
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		return m.navigateBack()
	case "ctrl+g":
		if m.focus == focusComposer {
			m.flushComposerBurstNow()
			m.composerBurst.Clear()
			return m.openComposerInEditor()
		}
		return m, nil
	case "ctrl+k":
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		return m.openActionPalette()
	case "ctrl+d":
		return m, nil
	case "esc":
		if m.dismissPromptOverlay() {
			return m, nil
		}
		if m.running {
			m.openInterruptConfirm()
			return m, nil
		}
		if m.focus == focusComposer && strings.TrimSpace(m.composerDraftValue()) == "" {
			if restored := m.restorePreviousPrompt(); restored {
				return m, nil
			}
		}
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		return m, nil
	case "tab":
		if m.focus == focusComposer && m.composerActionSuggestionsActive() {
			return m.updateComposer(msg)
		}
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		if m.opts.SimpleMode {
			switch m.focus {
			case focusComposer:
				m.focus = focusTranscript
			case focusTranscript:
				m.focus = focusInspector
			default:
				m.focus = focusComposer
			}
		} else {
			m.focus = (m.focus + 1) % 3
		}
		m.syncFocusStates()
		m.refreshViews(false)
		return m, nil
	case "ctrl+r":
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		return m.refreshReview()
	case "ctrl+l":
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		return m.refreshSnapshot()
	}

	if m.focus == focusComposer {
		return m.updateComposer(msg)
	}

	switch msg.String() {
	case "?":
		return m.openHelp()
	case "a":
		return m.openApprovals()
	case "i":
		return m.openInputRequest()
	case "p":
		if m.opts.SimpleMode {
			m.errorLine = taskConsoleDisabledMessage()
			return m, nil
		}
		return m.openTaskPicker()
	case "1":
		m.tab = tabOverview
		m.refreshViews(false)
		return m, nil
	case "2":
		m.tab = tabPlan
		m.refreshViews(false)
		return m, nil
	case "3":
		m.tab = tabCriteria
		m.refreshViews(false)
		return m, nil
	case "4":
		m.tab = tabWorkers
		m.refreshViews(false)
		return m, nil
	case "5":
		m.tab = tabBlockers
		m.refreshViews(false)
		return m, nil
	case "6":
		m.tab = tabMemory
		return m, refreshCmd(m.backend, m.taskID, m.session.SessionID, m.opts, true)
	case "7":
		m.tab = tabProject
		m.refreshViews(false)
		return m, nil
	case "8":
		m.tab = tabTasks
		m.refreshViews(false)
		return m, loadTasksCmd(m.backend)
	}

	switch m.focus {
	case focusTranscript:
		switch msg.String() {
		case "up", "k":
			m.transcript.LineUp(1)
		case "down", "j":
			wasAtBottom := m.transcript.AtBottom()
			m.transcript.LineDown(1)
			if cmd := m.refreshTranscriptWhenScrolledToBottom(wasAtBottom); cmd != nil {
				return m, cmd
			}
		case "pgup", "b":
			m.transcript.HalfViewUp()
		case "pgdown", "f":
			wasAtBottom := m.transcript.AtBottom()
			m.transcript.HalfViewDown()
			if cmd := m.refreshTranscriptWhenScrolledToBottom(wasAtBottom); cmd != nil {
				return m, cmd
			}
		}
		return m, nil
	case focusInspector:
		if m.tab == tabWorkers && len(m.snapshot.Workers) > 0 {
			selected := m.resolveSelectedWorker(m.snapshot.Workers)
			switch msg.String() {
			case "up", "k":
				if selected > 0 {
					m.setSelectedWorkerIndex(m.snapshot.Workers, selected-1)
					m.refreshViews(false)
				}
				return m, nil
			case "down", "j":
				if selected < len(m.snapshot.Workers)-1 {
					m.setSelectedWorkerIndex(m.snapshot.Workers, selected+1)
					m.refreshViews(false)
				}
				return m, nil
			case "enter":
				worker := m.snapshot.Workers[selected]
				if worker.ParentActionType == "continue_child" || worker.RequiresParentAction == false && worker.Status == "active" {
					return m, continueWorkerCmd(m.backend, m.taskID, worker.WorkerID)
				}
			}
		}
		if m.tab == tabTasks {
			targets := m.relatedTaskTargets()
			selected := m.resolveSelectedRelatedTask(targets)
			switch msg.String() {
			case "up", "k":
				if selected > 0 {
					m.setSelectedRelatedTaskIndex(targets, selected-1)
					m.refreshViews(false)
				}
				return m, nil
			case "down", "j":
				if selected < len(targets)-1 {
					m.setSelectedRelatedTaskIndex(targets, selected+1)
					m.refreshViews(false)
				}
				return m, nil
			case "enter":
				if len(targets) == 0 {
					return m, nil
				}
				if m.running {
					m.errorLine = "Cannot switch tasks while a turn is active."
					return m, nil
				}
				target := targets[selected]
				return m.beginTaskOpen(target.TaskID, true, fmt.Sprintf("Opened related task %s. Use /back to return.", target.TaskID))
			}
		}
		switch msg.String() {
		case "up", "k":
			m.inspector.LineUp(1)
		case "down", "j":
			m.inspector.LineDown(1)
		case "pgup":
			m.inspector.HalfViewUp()
		case "pgdown":
			m.inspector.HalfViewDown()
		}
		return m, nil
	}
	return m, nil
}

func (m model) refreshTranscriptWhenScrolledToBottom(wasAtBottom bool) tea.Cmd {
	if wasAtBottom || !m.transcript.AtBottom() || strings.TrimSpace(m.taskID) == "" || strings.TrimSpace(m.session.SessionID) == "" {
		return nil
	}
	return refreshCmd(m.backend, m.taskID, m.session.SessionID, m.opts, m.tab == tabMemory)
}

func (m model) submitComposer() (tea.Model, tea.Cmd) {
	m.flushComposerBurstNow()
	m.composerBurst.Clear()
	prompt := strings.TrimSpace(m.composer.Value())
	if prompt == "" {
		return m, nil
	}
	if m.pendingTaskOpenActive() {
		m.statusLine = m.pendingTaskOpenStatus()
		m.errorLine = ""
		return m, nil
	}
	m.composerBurst.Clear()
	m.rememberPrompt(prompt)
	normalizedPrompt := normalizeComposerAlias(prompt)
	switch normalizedPrompt {
	case "/quit", "/exit":
		m.composer.SetValue("")
		if m.running {
			m.modal = modalConfirmInterrupt
			m.confirmIndex = 1
			m.syncFocusStates()
			return m, nil
		}
		return m, tea.Quit
	case "/help":
		m.composer.SetValue("")
		return m.openHelp()
	case "/approvals":
		m.composer.SetValue("")
		return m.openApprovals()
	case "/input":
		m.composer.SetValue("")
		return m.openInputRequest()
	case "/picker":
		if m.opts.SimpleMode {
			m.composer.SetValue("")
			m.errorLine = taskConsoleDisabledMessage()
			return m, nil
		}
		m.composer.SetValue("")
		return m.openTaskPicker()
	case "/status", "/refresh":
		m.composer.SetValue("")
		return m.refreshSnapshot()
	case "/clear":
		m.composer.SetValue("")
		m.errorLine = ""
		m.statusLine = "Cleared transient TUI status."
		return m, nil
	case "/actions":
		m.composer.SetValue("")
		return m.openActionPalette()
	case "/tasks":
		if m.opts.SimpleMode {
			m.composer.SetValue("")
			m.errorLine = taskConsoleDisabledMessage()
			return m, nil
		}
		m.composer.SetValue("")
		return m.openTaskNavigation()
	case "/back":
		if m.opts.SimpleMode {
			m.composer.SetValue("")
			m.errorLine = taskConsoleDisabledMessage()
			return m, nil
		}
		m.composer.SetValue("")
		return m.navigateBack()
	case "/review":
		m.composer.SetValue("")
		return m.refreshReview()
	}
	if isKnownSlashCommand(normalizedPrompt) == false && strings.HasPrefix(normalizedPrompt, "/") {
		m.errorLine = fmt.Sprintf("Unknown command %q. Type /help for available commands.", strings.Fields(normalizedPrompt)[0])
		m.composer.SetValue("")
		return m, nil
	}
	if m.running {
		m.queuedPrompts = append(m.queuedPrompts, normalizedPrompt)
		m.statusLine = queuedPromptStatus(len(m.queuedPrompts))
		m.errorLine = ""
		m.composer.SetValue("")
		return m, nil
	}
	return m, m.startPrompt(normalizedPrompt, false)
}

func taskConsoleDisabledMessage() string {
	return "Task navigation is available from CLI, ACP, or Web management surfaces; the default TUI stays chat-first."
}

func isKnownSlashCommand(prompt string) bool {
	fields := strings.Fields(strings.TrimSpace(prompt))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return true
	}
	switch strings.ToLower(strings.TrimPrefix(fields[0], "/")) {
	case "actions", "approvals", "input", "picker", "status", "refresh", "clear", "tasks", "back", "review", "run", "resume", "quit", "exit", "help", "mission", "missions", "goal", "goals", "memory", "worker_spawn", "worker", "worker_continue", "watch", "approve", "deny":
		return true
	default:
		return false
	}
}

func tuiPromptErrorLine(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if isProviderConfigError(err) {
		return "Provider config missing: " + message + ". Fix ngen.json [provider] or export the configured key; builtin/command modes do not need remote keys."
	}
	return message
}

func isProviderConfigError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.TrimSpace(err.Error())
	return strings.HasPrefix(message, "provider mode ") &&
		(strings.Contains(message, " requires provider.") || strings.Contains(message, " requires env "))
}

func (m *model) startPrompt(prompt string, fromQueue bool) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.turnCancel = cancel
	m.running = true
	m.promptSeq++
	m.runningPromptSeq = m.promptSeq
	m.activePrompt = prompt
	m.activePromptBaseEvents = m.snapshot.SessionSnapshot.MessageCount
	m.statusLine = runningPromptStatus(len(m.queuedPrompts), fromQueue)
	m.errorLine = ""
	m.composer.SetValue("")
	return promptCmd(m.backend, m.session.SessionID, prompt, ctx, m.runningPromptSeq)
}

func (m *model) resize() {
	if !m.ready {
		return
	}
	contentWidth := frameContentWidth(m.width)
	bodyHeight := frameBodyHeight(m.height)
	if m.opts.SimpleMode {
		panelWidth := max(contentWidth-4, 1)
		m.transcript.Width = panelWidth
		m.transcript.Height = max(bodyHeight-2, 1)
		m.inspector.Width = panelWidth
		m.inspector.Height = max(bodyHeight-3, 1)
		m.composer.SetWidth(panelWidth)
		m.composer.SetHeight(3)
		m.inputBox.SetWidth(panelWidth)
		m.inputBox.SetHeight(3)
		return
	}
	if useStackedLayout(contentWidth) {
		transcriptHeight, inspectorHeight := splitStackedPaneHeights(bodyHeight)
		panelWidth := max(contentWidth-4, 1)
		m.transcript.Width = panelWidth
		m.transcript.Height = max(transcriptHeight-2, 1)
		m.inspector.Width = panelWidth
		m.inspector.Height = max(inspectorHeight-3, 1)
	} else {
		transcriptWidth, inspectorWidth := splitWidePaneWidths(contentWidth)
		m.transcript.Width = max(transcriptWidth-4, 1)
		m.transcript.Height = max(bodyHeight-2, 1)
		m.inspector.Width = max(inspectorWidth-4, 1)
		m.inspector.Height = max(bodyHeight-3, 1)
	}
	m.composer.SetWidth(max(contentWidth-4, 1))
	m.composer.SetHeight(3)
	m.inputBox.SetWidth(max(contentWidth-4, 1))
	m.inputBox.SetHeight(3)
}

func (m *model) refreshViews(forceBottom bool) {
	if !m.ready {
		return
	}
	transcriptWidth := max(m.transcript.Width, 12)
	inspectorWidth := max(m.inspector.Width, 12)
	entries := buildTranscript(m.snapshot.Messages, m.snapshot.Events)
	atBottom := forceBottom || m.transcript.AtBottom()
	m.transcript.SetContent(renderTranscript(entries, transcriptWidth, m.running))
	if atBottom {
		m.transcript.GotoBottom()
	}
	if m.tab == tabWorkers {
		selected := m.resolveSelectedWorker(m.snapshot.Workers)
		content, starts := renderWorkersSummaryWithStarts(m.snapshot.Workers, selected, inspectorWidth)
		m.inspector.SetContent(content)
		m.ensureSelectedWorkerVisible(starts)
		return
	}
	if m.tab == tabTasks {
		targets := m.relatedTaskTargets()
		selected := m.resolveSelectedRelatedTask(targets)
		content, starts := renderTaskNavigationSummary(m.snapshot, m.tasks, m.taskHistory, selected, inspectorWidth)
		m.inspector.SetContent(content)
		m.ensureSelectedRelatedTaskVisible(starts)
		return
	}
	m.inspector.SetContent(renderInspector(m.snapshot, m.tab, inspectorWidth, m.selectedWorker))
}

func (m *model) ensureSelectedWorkerVisible(lineStarts []int) {
	if len(lineStarts) == 0 || m.inspector.Height <= 0 {
		return
	}
	selected := m.resolveSelectedWorker(m.snapshot.Workers)
	if selected >= len(lineStarts) {
		selected = len(lineStarts) - 1
	}
	line := lineStarts[selected]
	top := m.inspector.YOffset
	bottom := top + max(m.inspector.Height, 1)
	switch {
	case line < top:
		m.inspector.SetYOffset(line)
	case line >= bottom:
		m.inspector.SetYOffset(line - m.inspector.Height + 1)
	}
}

func (m *model) ensureSelectedRelatedTaskVisible(lineStarts []int) {
	if len(lineStarts) == 0 || m.inspector.Height <= 0 {
		return
	}
	selected := m.resolveSelectedRelatedTask(m.relatedTaskTargets())
	if selected >= len(lineStarts) {
		selected = len(lineStarts) - 1
	}
	line := lineStarts[selected]
	top := m.inspector.YOffset
	bottom := top + max(m.inspector.Height, 1)
	switch {
	case line < top:
		m.inspector.SetYOffset(line)
	case line >= bottom:
		m.inspector.SetYOffset(line - m.inspector.Height + 1)
	}
}

func (m model) View() string {
	if !m.ready {
		return "Loading..."
	}
	if m.modal == modalPicker {
		return appStyle.Render(renderPickerView(m.width, m.height, m.filteredTasks(), m.pickerIndex, m.pickerFilter, m.statusLine, m.errorLine))
	}
	contentWidth := frameContentWidth(m.width)
	header := renderHeader(m.snapshot, m.session, m.opts.ProviderMode, m.focus, m.running, len(m.queuedPrompts), len(m.taskHistory), m.pendingTaskOpen, contentWidth)
	promptOverlay := m.renderPromptOverlay()
	footer := renderFooter(contentWidth, m.footerHintLine(contentWidth), m.statusLine, m.errorLine)
	if !m.opts.SimpleMode {
		body := m.renderBody()
		composerPanel := m.renderComposer()
		return appStyle.Render(joinFrameSections(header, body, promptOverlay, composerPanel, footer))
	}
	return appStyle.Render(m.renderFrame(header, promptOverlay, footer))
}

func (m model) renderBody() string {
	return m.renderBodyWithHeight(frameBodyHeight(m.height))
}

func (m model) renderFrame(header, promptOverlay, footer string) string {
	bodyHeight := frameBodyHeight(m.height)
	composerPanel := m.renderComposer()
	for candidateBodyHeight := bodyHeight; candidateBodyHeight >= 1; candidateBodyHeight-- {
		if rendered, ok := m.renderFrameWithinHeight(header, promptOverlay, composerPanel, footer, candidateBodyHeight); ok {
			return rendered
		}
	}

	// In very short terminals the queued/suggestion overlay is the lowest-priority
	// surface. Header, chat pane, composer, and footer must remain usable.
	promptOverlay = ""
	footer = renderFooterMaxLines(frameContentWidth(m.width), m.footerHintLine(frameContentWidth(m.width)), m.statusLine, m.errorLine, 2)
	for composerInputHeight := 3; composerInputHeight >= 1; composerInputHeight-- {
		composerPanel = m.renderComposerWithInputHeight(composerInputHeight)
		for candidateBodyHeight := bodyHeight; candidateBodyHeight >= 1; candidateBodyHeight-- {
			if rendered, ok := m.renderFrameWithinHeight(header, promptOverlay, composerPanel, footer, candidateBodyHeight); ok {
				return rendered
			}
		}
	}

	footer = renderFooterMaxLines(frameContentWidth(m.width), m.footerHintLine(frameContentWidth(m.width)), m.statusLine, m.errorLine, 1)
	composerPanel = m.renderComposerWithInputHeight(1)
	body := m.renderBodyWithHeight(1)
	return joinFrameSections(header, body, "", composerPanel, footer)
}

func (m model) renderFrameWithinHeight(header, promptOverlay, composerPanel, footer string, bodyHeight int) (string, bool) {
	body := m.renderBodyWithHeight(bodyHeight)
	rendered := joinFrameSections(header, body, promptOverlay, composerPanel, footer)
	return rendered, lipgloss.Height(rendered) <= m.height
}

func joinFrameSections(header, body, promptOverlay, composerPanel, footer string) string {
	sections := []string{header, body}
	if promptOverlay != "" {
		sections = append(sections, promptOverlay)
	}
	sections = append(sections, composerPanel, footer)
	return strings.Join(sections, "\n")
}

func (m model) renderBodyWithHeight(bodyHeight int) string {
	contentWidth := frameContentWidth(m.width)
	if bodyHeight <= 0 {
		bodyHeight = 1
	}
	if m.modal != modalNone {
		return renderModalView(contentWidth, bodyHeight, m)
	}

	if m.opts.SimpleMode {
		if m.simpleModeShowsDetails() {
			return m.renderSimpleDetailsBody(contentWidth, bodyHeight)
		}
		return m.renderSimpleChatBody(contentWidth, bodyHeight)
	}

	leftStyle := borderStyle
	rightStyle := borderStyle
	if m.focus == focusTranscript {
		leftStyle = focusedBorderStyle
	}
	if m.focus == focusInspector {
		rightStyle = focusedBorderStyle
	}
	if useStackedLayout(contentWidth) {
		transcriptHeight, inspectorHeight := splitStackedPaneHeights(bodyHeight)
		transcriptViewport := m.transcript
		transcriptViewport.Height = max(transcriptHeight-2, 0)
		transcriptLines := []string{titleStyle.Render("Transcript")}
		if transcriptViewport.Height > 0 {
			transcriptLines = append(transcriptLines, transcriptViewport.View())
		}
		transcript := leftStyle.Width(max(contentWidth-2, 1)).Height(transcriptHeight).Render(strings.Join(transcriptLines, "\n"))
		inspectorViewport := m.inspector
		inspectorViewport.Height = max(inspectorHeight-3, 0)
		rightLines := []string{renderTabs(m.tab, max(m.inspector.Width, 1))}
		if inspectorViewport.Height > 0 {
			rightLines = append(rightLines, inspectorViewport.View())
		}
		rightContent := strings.Join(rightLines, "\n")
		inspector := rightStyle.Width(max(contentWidth-2, 1)).Height(inspectorHeight).Render(titleStyle.Render("Inspector") + "\n" + rightContent)
		return lipgloss.JoinVertical(lipgloss.Left, transcript, inspector)
	}
	transcriptWidth, inspectorWidth := splitWidePaneWidths(contentWidth)
	transcriptViewport := m.transcript
	transcriptViewport.Height = max(bodyHeight-2, 0)
	leftLines := []string{titleStyle.Render("Transcript")}
	if transcriptViewport.Height > 0 {
		leftLines = append(leftLines, transcriptViewport.View())
	}
	left := leftStyle.Width(max(transcriptWidth-2, 1)).Height(bodyHeight).Render(strings.Join(leftLines, "\n"))
	inspectorViewport := m.inspector
	inspectorViewport.Height = max(bodyHeight-3, 0)
	rightLines := []string{renderTabs(m.tab, max(m.inspector.Width, 1))}
	if inspectorViewport.Height > 0 {
		rightLines = append(rightLines, inspectorViewport.View())
	}
	rightContent := strings.Join(rightLines, "\n")
	right := rightStyle.Width(max(inspectorWidth-2, 1)).Height(bodyHeight).Render(titleStyle.Render("Inspector") + "\n" + rightContent)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func (m model) simpleModeShowsDetails() bool {
	if !m.opts.SimpleMode || m.modal != modalNone || m.focus == focusComposer {
		return false
	}
	return m.focus == focusInspector || m.tab != tabOverview
}

func (m model) renderSimpleChatBody(contentWidth, bodyHeight int) string {
	style := borderStyle
	if m.focus == focusTranscript {
		style = focusedBorderStyle
	}
	transcript := m.transcript
	wasAtBottom := transcript.AtBottom()
	transcript.Height = max(bodyHeight-2, 0)
	if wasAtBottom {
		transcript.GotoBottom()
	}
	lines := []string{titleStyle.Render("Chat")}
	if transcript.Height > 0 {
		lines = append(lines, transcript.View())
	}
	return style.Width(max(contentWidth-2, 1)).Height(bodyHeight).Render(strings.Join(lines, "\n"))
}

func (m model) renderSimpleDetailsBody(contentWidth, bodyHeight int) string {
	style := borderStyle
	if m.focus == focusInspector {
		style = focusedBorderStyle
	}
	inspectorWidth := max(contentWidth-4, 1)
	inspectorHeight := max(bodyHeight-3, 0)
	inspector := m.inspector
	inspector.Width = inspectorWidth
	inspector.Height = inspectorHeight
	if inspectorHeight > 0 {
		switch m.tab {
		case tabWorkers:
			selected := m.resolveSelectedWorker(m.snapshot.Workers)
			content, starts := renderWorkersSummaryWithStarts(m.snapshot.Workers, selected, inspectorWidth)
			inspector.SetContent(content)
			inspector.SetYOffset(selectedLineYOffset(inspector.YOffset, inspectorHeight, starts, selected))
		case tabTasks:
			targets := m.relatedTaskTargets()
			selected := m.resolveSelectedRelatedTask(targets)
			content, starts := renderTaskNavigationSummary(m.snapshot, m.tasks, m.taskHistory, selected, inspectorWidth)
			inspector.SetContent(content)
			inspector.SetYOffset(selectedLineYOffset(inspector.YOffset, inspectorHeight, starts, selected))
		}
	}
	lines := []string{
		titleStyle.Render("Details"),
		renderTabs(m.tab, inspectorWidth),
	}
	if inspector.Height > 0 {
		lines = append(lines, inspector.View())
	}
	return style.Width(max(contentWidth-2, 1)).Height(bodyHeight).Render(strings.Join(lines, "\n"))
}

func selectedLineYOffset(currentOffset, viewportHeight int, lineStarts []int, selected int) int {
	if len(lineStarts) == 0 || viewportHeight <= 0 {
		return currentOffset
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= len(lineStarts) {
		selected = len(lineStarts) - 1
	}
	line := lineStarts[selected]
	top := currentOffset
	bottom := top + max(viewportHeight, 1)
	switch {
	case line < top:
		return line
	case line >= bottom:
		return line - viewportHeight + 1
	default:
		return currentOffset
	}
}

func (m model) renderComposer() string {
	return m.renderComposerWithInputHeight(3)
}

func (m model) renderComposerWithInputHeight(inputHeight int) string {
	contentWidth := frameContentWidth(m.width)
	style := borderStyle
	if m.focus == focusComposer {
		style = focusedBorderStyle
	}
	sections := []string{renderComposerTitle(m, max(contentWidth-4, 1))}
	if context := renderComposerContext(m, max(contentWidth-4, 1)); context != "" {
		sections = append(sections, context)
	}
	composer := m.composer
	composer.SetHeight(max(inputHeight, 1))
	sections = append(sections, composer.View())
	return style.Width(max(contentWidth-2, 1)).Render(strings.Join(sections, "\n"))
}

func (m model) renderPromptOverlay() string {
	if !m.promptOverlayVisible() {
		return ""
	}
	contentWidth := frameContentWidth(m.width)
	innerWidth := max(contentWidth-4, 1)
	title := ""
	var content string
	if suggestions := renderComposerActionSuggestions(m, innerWidth); suggestions != "" {
		title = "Commands"
		content = suggestions
	} else if queued := renderQueuedPromptPreview(m, innerWidth); queued != "" {
		title = "Queued Prompts"
		content = queued
	} else {
		return ""
	}
	style := borderStyle
	if m.focus == focusComposer {
		style = focusedBorderStyle
	}
	return style.Width(max(contentWidth-2, 1)).Render(titleStyle.Render(title) + "\n" + content)
}

func (m *model) syncFocusStates() {
	if m.modal == modalInput {
		m.inputBox.Focus()
	} else {
		m.inputBox.Blur()
	}
	if m.modal == modalNone && m.focus == focusComposer {
		m.composer.Focus()
	} else {
		m.composer.Blur()
	}
}

func (m model) footerHints() string {
	return joinFooterHintParts(m.footerHintParts())
}

func (m model) footerHintParts() []footerHintPart {
	orderedHints := func(parts ...string) []footerHintPart {
		hints := make([]footerHintPart, 0, len(parts))
		for i, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			hints = append(hints, footerHintPart{
				text:     part,
				priority: 100 - i,
			})
		}
		return hints
	}
	switch m.modal {
	case modalApprovals:
		return orderedHints("Up/Down select", "a approve", "d deny", "Esc close")
	case modalInput:
		return orderedHints("Enter submit", "Ctrl+G editor", "Esc close")
	case modalHelp:
		return orderedHints("Esc, Enter, or ? close help")
	case modalActionPalette:
		return orderedHints("Type filter", "Up/Down select", "Enter apply", "Esc close")
	case modalConfirmInterrupt:
		return orderedHints("Up/Down select", "Enter confirm", "Esc close", "Ctrl+C confirm")
	}
	if m.focus == focusComposer {
		if m.pendingTaskOpenActive() {
			return []footerHintPart{
				{text: "Enter waits", priority: 100},
				{text: "Ctrl+C quit", priority: 90},
				{text: "Ctrl+J newline", priority: 40},
			}
		}
		if m.promptOverlayVisible() {
			if len(m.composerActionSuggestions()) > 0 {
				if m.running {
					return []footerHintPart{
						{text: "Up/Down select", priority: 70},
						{text: "Tab complete", priority: 100},
						{text: "Esc hide", priority: 40},
						{text: "Ctrl+C interrupt", priority: 95},
					}
				}
				return []footerHintPart{
					{text: "Up/Down select", priority: 80},
					{text: "Tab complete", priority: 100},
					{text: "Esc hide", priority: 40},
					{text: "Ctrl+C quit", priority: 75},
				}
			}
			if m.running && len(m.queuedPrompts) > 0 {
				return []footerHintPart{
					{text: "Up/Down select", priority: 70},
					{text: "Enter edit", priority: 100},
					{text: "Backspace drop", priority: 80},
					{text: "Esc hide", priority: 40},
					{text: "Ctrl+C interrupt", priority: 95},
				}
			}
			if m.running {
				return []footerHintPart{
					{text: "Esc hide", priority: 40},
					{text: "Keep typing to queue", priority: 100},
					{text: "Ctrl+C interrupt", priority: 95},
				}
			}
			return []footerHintPart{
				{text: "Esc hide", priority: 70},
				{text: "Ctrl+C quit", priority: 100},
			}
		}
		if m.running && len(m.queuedPrompts) > 0 {
			return []footerHintPart{
				{text: "Enter queue", priority: 100},
				{text: "Ctrl+P newest", priority: 80},
				{text: "Ctrl+C interrupt", priority: 95},
				{text: "Esc interrupt", priority: 70},
				{text: "Ctrl+J newline", priority: 40},
			}
		}
		if m.running {
			return []footerHintPart{
				{text: "Enter queue", priority: 100},
				{text: "Ctrl+C interrupt", priority: 95},
				{text: "Esc interrupt", priority: 70},
				{text: "Ctrl+J newline", priority: 40},
			}
		}
		hints := []footerHintPart{
			{text: "Enter send", priority: 100},
			{text: "Ctrl+K actions", priority: 95},
		}
		if !m.opts.SimpleMode {
			hints = append(hints, footerHintPart{text: "Ctrl+O picker", priority: 90})
		}
		if !m.opts.SimpleMode && len(m.taskHistory) > 0 {
			hints = append(hints, footerHintPart{text: "Ctrl+B back", priority: 85})
		}
		if strings.TrimSpace(m.composerDraftValue()) == "" {
			hints = append(hints, footerHintPart{text: "Esc prev", priority: 80})
		}
		hints = append(hints,
			footerHintPart{text: "Ctrl+J newline", priority: 40},
			footerHintPart{text: "Ctrl+C quit", priority: 50},
		)
		return hints
	}
	if m.running {
		return []footerHintPart{
			{text: "Tab focus", priority: 90},
			{text: "Enter action", priority: 85},
			{text: "a approvals", priority: 60},
			{text: "Ctrl+C interrupt", priority: 100},
			{text: "? help", priority: 50},
			{text: "1-8 tabs", priority: 40},
		}
	}
	return []footerHintPart{
		{text: "Tab focus", priority: 90},
		{text: "Enter action", priority: 85},
		{text: "a approvals", priority: 60},
		{text: "Ctrl+C quit", priority: 80},
		{text: "? help", priority: 50},
		{text: "1-8 tabs", priority: 40},
	}
}

func (m model) footerHintLine(width int) string {
	return collapseFooterHintPartsToWidth(m.footerHintParts(), width)
}

func (m model) openHelp() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	m.modal = modalHelp
	m.syncFocusStates()
	return m, nil
}

func (m model) openApprovals() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	if len(pendingApprovalRecords(m.snapshot.Approvals))+len(pendingApprovalRecords(m.snapshot.OwnedApprovals)) > 0 {
		m.modal = modalApprovals
		m.approvalIndex = 0
		m.syncFocusStates()
		return m, nil
	}
	m.statusLine = "No pending approvals."
	m.errorLine = ""
	return m, nil
}

func (m model) openComposerInEditor() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	m.statusLine = "Save and close external editor to continue."
	m.errorLine = ""
	return m, externalEditorCmd(editorTargetComposer, m.composerDraftValue())
}

func (m model) openInputInEditor() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	m.statusLine = "Save and close external editor to continue."
	m.errorLine = ""
	return m, externalEditorCmd(editorTargetInput, m.inputBox.Value())
}

func (m model) openInputRequest() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	if _, ok := pendingInputRecord(m.snapshot.Inputs); ok {
		m.modal = modalInput
		m.inputBox.SetValue("")
		m.syncFocusStates()
		return m, nil
	}
	m.statusLine = "No pending input request."
	m.errorLine = ""
	return m, nil
}

func (m model) openTaskPicker() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	if m.running {
		m.errorLine = "Cannot switch tasks while a turn is active."
		return m, nil
	}
	m.modal = modalPicker
	m.syncFocusStates()
	return m, loadTasksCmd(m.backend)
}

func (m model) openTaskNavigation() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	m.tab = tabTasks
	m.focus = focusInspector
	m.syncFocusStates()
	m.refreshViews(false)
	m.statusLine = "Task navigation opened. Enter opens the selected related task."
	m.errorLine = ""
	return m, loadTasksCmd(m.backend)
}

func (m model) navigateBack() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	if m.running {
		m.errorLine = "Cannot switch tasks while a turn is active."
		return m, nil
	}
	taskID, ok := m.popTaskHistory()
	if !ok {
		m.statusLine = "No previous task in navigation history."
		m.errorLine = ""
		return m, nil
	}
	return m.beginTaskOpen(taskID, false, fmt.Sprintf("Returned to task %s.", taskID))
}

func (m model) refreshReview() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	if m.running {
		m.errorLine = "Cannot review while a turn is active."
		return m, nil
	}
	return m, reviewCmd(m.backend, m.taskID)
}

func (m model) refreshSnapshot() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	return m, refreshCmd(m.backend, m.taskID, m.session.SessionID, m.opts, m.tab == tabMemory)
}

func (m *model) clearEscPriming() {
	m.escPrimed = false
	m.escPrimedAt = time.Time{}
}

func (m *model) clearPromptOverlayDismissal() {
	m.dismissedPromptOverlaySig = ""
}

func (m model) currentPromptOverlaySignature() string {
	if m.modal != modalNone {
		return ""
	}
	if suggestions := m.composerActionSuggestions(); len(suggestions) > 0 {
		query, _ := m.composerActionQuery()
		return "commands:" + normalizeActionQuery(query)
	}
	if len(m.queuedPrompts) == 0 {
		return ""
	}
	trimmed := make([]string, 0, len(m.queuedPrompts))
	for _, prompt := range m.queuedPrompts {
		trimmed = append(trimmed, strings.TrimSpace(prompt))
	}
	return "queue:" + strings.Join(trimmed, "\x1f")
}

func (m model) hasDurablePromptHistory() bool {
	return strings.TrimSpace(m.snapshot.Session.LastPrompt) != "" || m.snapshot.SessionSnapshot.MessageCount > 0
}

func (m model) pendingTaskOpenActive() bool {
	return strings.TrimSpace(m.pendingTaskOpen.TargetTaskID) != ""
}

func (m model) composerQuickActions() []string {
	if m.opts.SimpleMode {
		return nil
	}
	actions := make([]string, 0, 4)
	add := func(label string) {
		label = strings.TrimSpace(label)
		if label == "" || len(actions) >= 4 {
			return
		}
		for _, existing := range actions {
			if existing == label {
				return
			}
		}
		actions = append(actions, label)
	}

	if m.hasDurablePromptHistory() {
		add("resume")
	} else {
		add("run")
	}
	if m.hasDurablePromptHistory() || m.snapshot.TaskView.Status.State == task.StateDone || m.snapshot.TaskView.Status.State == task.StateBlocked || m.snapshot.TaskView.Status.State == task.StateWaiting {
		add("review")
	}
	if len(m.relatedTaskTargets()) > 0 {
		add("tasks")
	}
	if len(m.taskHistory) > 0 {
		add("back")
	}
	add("picker")
	add("actions")
	add("help")
	return actions
}

func (m model) pendingTaskOpenStatus() string {
	taskID := strings.TrimSpace(m.pendingTaskOpen.TargetTaskID)
	if taskID == "" {
		return ""
	}
	return fmt.Sprintf("Opening task %s. Keep typing; Enter waits until the switch completes.", taskID)
}

func (m model) promptOverlayVisible() bool {
	sig := m.currentPromptOverlaySignature()
	return sig != "" && sig != m.dismissedPromptOverlaySig
}

func (m *model) dismissPromptOverlay() bool {
	sig := m.currentPromptOverlaySignature()
	if sig == "" || sig == m.dismissedPromptOverlaySig {
		return false
	}
	m.dismissedPromptOverlaySig = sig
	if m.running {
		m.statusLine = "Prompt overlay hidden. Press Esc again to interrupt or keep typing to reopen."
	} else {
		m.statusLine = "Prompt overlay hidden. Keep typing to reopen."
	}
	m.errorLine = ""
	return true
}

func (m *model) restorePreviousPrompt() bool {
	now := m.currentTime()
	if !m.escPrimed || now.Sub(m.escPrimedAt) > escRestoreWindow {
		m.escPrimed = true
		m.escPrimedAt = now
		m.statusLine = "Press Esc again to edit the previous prompt."
		m.errorLine = ""
		return true
	}
	m.clearEscPriming()
	previous, ok := latestOperatorPrompt(m.snapshot.Messages)
	if !ok {
		m.statusLine = "No previous operator prompt available."
		m.errorLine = ""
		return true
	}
	m.composer.SetValue(previous)
	m.composer.CursorEnd()
	m.statusLine = "Previous prompt restored to composer."
	m.errorLine = ""
	return true
}

func latestOperatorPrompt(messages []task.SessionMessage) (string, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if !strings.EqualFold(strings.TrimSpace(message.Role), "operator") {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		return content, true
	}
	return "", false
}

func (m model) filteredTasks() []task.TaskListEntry {
	query := strings.ToLower(strings.TrimSpace(m.pickerFilter))
	if query == "" || strings.HasPrefix(query, "/") {
		return append([]task.TaskListEntry(nil), m.tasks...)
	}
	filtered := make([]task.TaskListEntry, 0, len(m.tasks))
	for _, entry := range m.tasks {
		haystack := strings.ToLower(strings.Join([]string{
			entry.TaskID,
			entry.Title,
			string(entry.Kind),
			string(entry.Phase),
			string(entry.State),
			entry.StatusReasonCode,
			entry.CurrentStepID,
			entry.CurrentSystemStepID,
			entry.CurrentExecutionStepID,
		}, "\n"))
		if strings.Contains(haystack, query) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (m *model) clampPickerIndex() {
	filtered := m.filteredTasks()
	if len(filtered) == 0 {
		m.pickerIndex = 0
		return
	}
	if m.pickerIndex < 0 {
		m.pickerIndex = 0
	}
	if m.pickerIndex >= len(filtered) {
		m.pickerIndex = len(filtered) - 1
	}
}

func (m *model) rememberPrompt(prompt string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}
	if n := len(m.history); n > 0 && m.history[n-1] == prompt {
		m.historyIndex = -1
		m.historyDraft = ""
		return
	}
	m.history = append(m.history, prompt)
	m.historyIndex = -1
	m.historyDraft = ""
}

func (m *model) popQueuedPromptForEditing() (string, bool) {
	for i := len(m.queuedPrompts) - 1; i >= 0; i-- {
		prompt := strings.TrimSpace(m.queuedPrompts[i])
		if prompt == "" {
			m.queuedPrompts = append(m.queuedPrompts[:i], m.queuedPrompts[i+1:]...)
			continue
		}
		m.queuedPrompts = append(m.queuedPrompts[:i], m.queuedPrompts[i+1:]...)
		return prompt, true
	}
	return "", false
}

func (m *model) clampQueuedPromptPreviewIndex() int {
	count := len(m.queuedPrompts)
	if count <= 0 {
		m.queuedPromptPreviewIndex = 0
		return 0
	}
	if m.queuedPromptPreviewIndex < 0 {
		m.queuedPromptPreviewIndex = 0
	}
	if m.queuedPromptPreviewIndex >= count {
		m.queuedPromptPreviewIndex = count - 1
	}
	return m.queuedPromptPreviewIndex
}

func (m model) queuedPromptPreviewSelectable() bool {
	return m.running &&
		m.promptOverlayVisible() &&
		len(m.queuedPrompts) > 0 &&
		len(m.composerActionSuggestions()) == 0 &&
		strings.TrimSpace(m.composerDraftValue()) == ""
}

func (m *model) popVisibleQueuedPromptForEditing() (string, bool) {
	if len(m.queuedPrompts) == 0 {
		return "", false
	}
	index := m.clampQueuedPromptPreviewIndex()
	if index < 0 || index >= len(m.queuedPrompts) {
		return "", false
	}
	prompt := strings.TrimSpace(m.queuedPrompts[index])
	m.queuedPrompts = append(m.queuedPrompts[:index], m.queuedPrompts[index+1:]...)
	m.clampQueuedPromptPreviewIndex()
	if prompt == "" {
		return "", false
	}
	return prompt, true
}

func (m *model) dropVisibleQueuedPrompt() bool {
	if len(m.queuedPrompts) == 0 {
		return false
	}
	index := m.clampQueuedPromptPreviewIndex()
	if index < 0 || index >= len(m.queuedPrompts) {
		return false
	}
	m.queuedPrompts = append(m.queuedPrompts[:index], m.queuedPrompts[index+1:]...)
	m.clampQueuedPromptPreviewIndex()
	return true
}

func queuedPromptEditStatus(remaining int) string {
	if remaining <= 0 {
		return "Queued prompt restored to composer for editing."
	}
	if remaining == 1 {
		return "Queued prompt restored to composer for editing. 1 follow-up remains queued."
	}
	return fmt.Sprintf("Queued prompt restored to composer for editing. %d follow-ups remain queued.", remaining)
}

func droppedQueuedPromptStatus(remaining int) string {
	if remaining <= 0 {
		return "Dropped selected queued prompt."
	}
	if remaining == 1 {
		return "Dropped selected queued prompt. 1 follow-up remains queued."
	}
	return fmt.Sprintf("Dropped selected queued prompt. %d follow-ups remain queued.", remaining)
}

func (m *model) dropQueuedPrompts() int {
	count := len(m.queuedPrompts)
	m.queuedPrompts = nil
	return count
}

func (m *model) setTaskDraft(taskID, draft string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	if draft == "" {
		delete(m.taskDrafts, taskID)
		return
	}
	m.taskDrafts[taskID] = taskDraftState{Draft: draft}
}

func (m *model) saveTaskDraft(taskID string) {
	m.setTaskDraft(taskID, m.composerDraftValue())
}

func (m *model) restoreTaskDraft(taskID string) {
	m.inputBox.SetValue("")
	m.historyIndex = -1
	m.historyDraft = ""
	m.composer.SetValue("")
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	draft, ok := m.taskDrafts[taskID]
	if !ok {
		return
	}
	m.composer.SetValue(draft.Draft)
	m.composer.CursorEnd()
}

func (m *model) pushTaskHistory(taskID string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	if n := len(m.taskHistory); n > 0 && m.taskHistory[n-1] == taskID {
		return
	}
	m.taskHistory = append(m.taskHistory, taskID)
}

func (m *model) popTaskHistory() (string, bool) {
	if len(m.taskHistory) == 0 {
		return "", false
	}
	last := strings.TrimSpace(m.taskHistory[len(m.taskHistory)-1])
	m.taskHistory = m.taskHistory[:len(m.taskHistory)-1]
	if last == "" {
		return "", false
	}
	return last, true
}

func queuedPromptStatus(count int) string {
	if count <= 1 {
		return "Queued follow-up prompt."
	}
	return fmt.Sprintf("Queued %d follow-up prompts.", count)
}

func runningPromptStatus(queuedRemaining int, fromQueue bool) string {
	label := "Running prompt..."
	if fromQueue {
		label = "Running queued prompt..."
	}
	if queuedRemaining <= 0 {
		return label
	}
	return fmt.Sprintf("%s %d more queued.", label, queuedRemaining)
}

func (m *model) dequeueNextPrompt() (string, bool) {
	for len(m.queuedPrompts) > 0 {
		next := strings.TrimSpace(m.queuedPrompts[0])
		m.queuedPrompts = m.queuedPrompts[1:]
		if next != "" {
			return next, true
		}
	}
	return "", false
}

func (m *model) maybeStartQueuedPrompt() tea.Cmd {
	next, ok := m.dequeueNextPrompt()
	if !ok {
		return nil
	}
	return m.startPrompt(next, true)
}

func (m model) relatedTaskTargets() []taskNavigationTarget {
	return buildTaskNavigationTargets(m.snapshot, m.tasks)
}

func (m model) beginTaskOpen(taskID string, pushPrevious bool, status string) (tea.Model, tea.Cmd) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return m, nil
	}
	m.flushComposerBurstNow()
	m.composerBurst.Clear()
	m.clearEscPriming()
	m.saveTaskDraft(m.taskID)
	m.pendingTaskOpen = pendingTaskOpenState{
		TargetTaskID:      taskID,
		PreviousTaskID:    m.taskID,
		PreviousTaskDraft: m.taskDrafts[m.taskID].Draft,
	}
	m.historyIndex = -1
	m.historyDraft = ""
	m.inputBox.SetValue("")
	m.composer.SetValue("")
	if m.modal == modalPicker {
		m.modal = modalNone
	}
	m.focus = focusComposer
	m.syncFocusStates()
	m.statusLine = m.pendingTaskOpenStatus()
	m.errorLine = ""
	m.refreshViews(false)
	return m, m.openTask(taskID, pushPrevious, status)
}

func (m model) openTask(taskID string, pushPrevious bool, status string) tea.Cmd {
	return openTaskCmd(m.backend, taskOpenRequest{
		TaskID:         taskID,
		PreviousTaskID: m.taskID,
		PushPrevious:   pushPrevious,
		Status:         status,
	}, m.opts)
}

func (m model) shouldLoadTaskList() bool {
	return m.modal == modalPicker || m.tab == tabTasks
}

func (m *model) navigateHistory(direction int) bool {
	if direction == 0 || len(m.history) == 0 {
		return false
	}
	current := m.composer.Value()
	if direction < 0 {
		if m.historyIndex == -1 {
			if strings.TrimSpace(current) != "" {
				return false
			}
			m.historyDraft = current
			m.historyIndex = len(m.history) - 1
		} else if m.historyIndex > 0 {
			m.historyIndex--
		}
		m.composer.SetValue(m.history[m.historyIndex])
		m.composer.CursorEnd()
		return true
	}
	if m.historyIndex == -1 {
		return false
	}
	if m.historyIndex < len(m.history)-1 {
		m.historyIndex++
		m.composer.SetValue(m.history[m.historyIndex])
		m.composer.CursorEnd()
		return true
	}
	m.historyIndex = -1
	m.composer.SetValue(m.historyDraft)
	m.composer.CursorEnd()
	return true
}

func (m *model) dismissCurrentBlocker() {
	m.dismissedBlockerSig = currentBlockerSignature(m.snapshot)
}

func (m *model) openInterruptConfirm() {
	m.composerBurst.Clear()
	m.modal = modalConfirmInterrupt
	m.confirmIndex = 1
	m.syncFocusStates()
}

func (m *model) resolveSelectedWorker(workers []task.WorkerContract) int {
	if len(workers) == 0 {
		m.selectedWorker = 0
		m.selectedWorkerID = ""
		return 0
	}
	if selectedID := strings.TrimSpace(m.selectedWorkerID); selectedID != "" {
		for i, worker := range workers {
			if worker.WorkerID == selectedID {
				m.selectedWorker = i
				return i
			}
		}
	}
	return m.setSelectedWorkerIndex(workers, m.selectedWorker)
}

func (m *model) setSelectedWorkerIndex(workers []task.WorkerContract, index int) int {
	if len(workers) == 0 {
		m.selectedWorker = 0
		m.selectedWorkerID = ""
		return 0
	}
	if index < 0 {
		index = 0
	}
	if index >= len(workers) {
		index = len(workers) - 1
	}
	m.selectedWorker = index
	m.selectedWorkerID = workers[index].WorkerID
	return index
}

func (m *model) resolveSelectedRelatedTask(targets []taskNavigationTarget) int {
	if len(targets) == 0 {
		m.selectedRelatedTask = 0
		m.selectedRelatedID = ""
		return 0
	}
	if selectedID := strings.TrimSpace(m.selectedRelatedID); selectedID != "" {
		for i, target := range targets {
			if target.TaskID == selectedID {
				m.selectedRelatedTask = i
				return i
			}
		}
	}
	return m.setSelectedRelatedTaskIndex(targets, m.selectedRelatedTask)
}

func (m *model) setSelectedRelatedTaskIndex(targets []taskNavigationTarget, index int) int {
	if len(targets) == 0 {
		m.selectedRelatedTask = 0
		m.selectedRelatedID = ""
		return 0
	}
	if index < 0 {
		index = 0
	}
	if index >= len(targets) {
		index = len(targets) - 1
	}
	m.selectedRelatedTask = index
	m.selectedRelatedID = targets[index].TaskID
	return index
}

func (m model) shouldAutoSettleRunningPrompt(snapshot Snapshot) bool {
	if !m.running || m.pendingAbort {
		return false
	}
	if strings.TrimSpace(m.activePrompt) == "" {
		return false
	}
	if strings.TrimSpace(snapshot.Session.LastPrompt) != strings.TrimSpace(m.activePrompt) {
		return false
	}
	if snapshot.SessionSnapshot.MessageCount < m.activePromptBaseEvents+2 {
		return false
	}
	if len(snapshot.Messages) == 0 {
		return false
	}
	last := snapshot.Messages[len(snapshot.Messages)-1]
	return strings.EqualFold(strings.TrimSpace(last.Role), "runtime")
}

func (m *model) autoOpenBlockerModal() {
	if m.modal != modalNone {
		return
	}
	kind, sig := preferredBlockerModal(m.snapshot)
	if kind == modalNone {
		return
	}
	if sig == m.dismissedBlockerSig {
		return
	}
	m.modal = kind
	if kind == modalApprovals {
		m.approvalIndex = 0
	}
	if kind == modalInput {
		m.inputBox.SetValue("")
	}
	m.flushComposerBurstNow()
	m.composerBurst.Clear()
	m.syncFocusStates()
}

func (m model) updateComposer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	now := m.currentTime()
	m.flushComposerBurstIfDue(now)
	suggestions := m.composerActionSuggestions()
	suggestionsActive := len(suggestions) > 1

	switch msg.String() {
	case "up", "ctrl+p":
		if suggestionsActive {
			if m.composerActionIndex > 0 {
				m.composerActionIndex--
			}
			return m, nil
		}
		if msg.String() == "up" && m.queuedPromptPreviewSelectable() {
			if m.clampQueuedPromptPreviewIndex() > 0 {
				m.queuedPromptPreviewIndex--
			}
			return m, nil
		}
		m.flushComposerBurstNow()
		if m.running && strings.TrimSpace(m.composer.Value()) == "" {
			if prompt, ok := m.popQueuedPromptForEditing(); ok {
				m.composer.SetValue(prompt)
				m.composer.CursorEnd()
				m.statusLine = queuedPromptEditStatus(len(m.queuedPrompts))
				m.errorLine = ""
				m.composerBurst.Clear()
				m.clampComposerActionIndex()
				return m, nil
			}
		}
		if m.navigateHistory(-1) {
			m.composerBurst.Clear()
			return m, nil
		}
		m.composerBurst.Clear()
	case "down", "ctrl+n":
		if suggestionsActive {
			if m.composerActionIndex < len(suggestions)-1 {
				m.composerActionIndex++
			}
			return m, nil
		}
		if m.queuedPromptPreviewSelectable() {
			if m.clampQueuedPromptPreviewIndex() < len(m.queuedPrompts)-1 {
				m.queuedPromptPreviewIndex++
			}
			return m, nil
		}
		m.flushComposerBurstNow()
		if m.navigateHistory(1) {
			m.composerBurst.Clear()
			return m, nil
		}
		m.composerBurst.Clear()
	case "tab":
		m.flushComposerBurstNow()
		if m.acceptComposerActionSuggestion() {
			m.composerBurst.Clear()
			return m, nil
		}
		m.composerBurst.Clear()
		return m, nil
	}

	switch msg.String() {
	case "enter":
		if m.queuedPromptPreviewSelectable() {
			if prompt, ok := m.popVisibleQueuedPromptForEditing(); ok {
				m.composer.SetValue(prompt)
				m.composer.CursorEnd()
				m.statusLine = queuedPromptEditStatus(len(m.queuedPrompts))
				m.errorLine = ""
				m.composerBurst.Clear()
				m.clampComposerActionIndex()
				return m, nil
			}
		}
		if len(suggestions) > 0 {
			m.flushComposerBurstNow()
			m.composerBurst.Clear()
			m.statusLine = "Command suggestions are open. Press Tab to complete or keep typing."
			m.errorLine = ""
			return m, nil
		}
		if m.composerBurst.ShouldInsertNewline(now, m.composerInSlashContext()) {
			m.flushComposerBurstNow()
			m.composerBurst.Extend(now)
			var cmd tea.Cmd
			m.composer, cmd = m.composer.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m.clampComposerActionIndex()
			return m, cmd
		}
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		return m.submitComposer()
	case "backspace", "delete":
		if m.queuedPromptPreviewSelectable() {
			if m.dropVisibleQueuedPrompt() {
				m.statusLine = droppedQueuedPromptStatus(len(m.queuedPrompts))
				m.errorLine = ""
				m.composerBurst.Clear()
				m.clampComposerActionIndex()
				return m, nil
			}
		}
	case "ctrl+j":
		m.flushComposerBurstNow()
		m.composerBurst.Clear()
		var cmd tea.Cmd
		m.composer, cmd = m.composer.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.clampComposerActionIndex()
		return m, cmd
	}

	if runes, ok := composerInputRunes(msg); ok {
		if msg.Paste {
			m.flushComposerBurstNow()
			m.composerBurst.ObserveDirectRunes(now, runes)
			m.composer.InsertString(string(runes))
			m.clampComposerActionIndex()
			return m, nil
		}
		if len(runes) == 1 && runes[0] <= unicode.MaxASCII {
			m.composerBurst.ObserveASCII(now, runes[0])
			m.clampComposerActionIndex()
			return m, composerBurstFlushCmd()
		}
		m.flushComposerBurstNow()
		m.composerBurst.ObserveDirectRunes(now, runes)
		m.composer.InsertString(string(runes))
		m.clampComposerActionIndex()
		return m, nil
	}

	m.flushComposerBurstNow()
	m.composerBurst.Clear()
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	m.clampComposerActionIndex()
	return m, cmd
}

func composerInputRunes(msg tea.KeyMsg) ([]rune, bool) {
	if msg.Alt {
		return nil, false
	}
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		if len(msg.Runes) == 0 {
			return nil, false
		}
		return msg.Runes, true
	default:
		return nil, false
	}
}

func (m model) composerInSlashContext() bool {
	firstLine := m.composerDraftValue()
	if idx := strings.Index(firstLine, "\n"); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	return strings.HasPrefix(strings.TrimSpace(firstLine), "/")
}

func (m *model) flushComposerBurstIfDue(now time.Time) {
	if text := m.composerBurst.FlushDue(now); text != "" {
		m.composer.InsertString(text)
		m.clampComposerActionIndex()
	}
}

func (m *model) flushComposerBurstNow() {
	if text := m.composerBurst.FlushNow(); text != "" {
		m.composer.InsertString(text)
		m.clampComposerActionIndex()
	}
}

func (m model) composerDraftValue() string {
	if pending := m.composerBurst.PendingText(); pending != "" {
		return m.composer.Value() + pending
	}
	return m.composer.Value()
}

func (m model) currentTime() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func composerBurstFlushCmd() tea.Cmd {
	return tea.Tick(composerBurstFlushDelay(), func(t time.Time) tea.Msg {
		return composerBurstFlushMsg(t)
	})
}

func preferredBlockerModal(snapshot Snapshot) (modalKind, string) {
	if approvals := pendingApprovalRecords(snapshot.Approvals); len(approvals) > 0 {
		return modalApprovals, currentBlockerSignature(snapshot)
	}
	if approvals := pendingApprovalRecords(snapshot.OwnedApprovals); len(approvals) > 0 {
		return modalApprovals, currentBlockerSignature(snapshot)
	}
	if _, ok := pendingInputRecord(snapshot.Inputs); ok {
		return modalInput, currentBlockerSignature(snapshot)
	}
	return modalNone, ""
}

func currentBlockerSignature(snapshot Snapshot) string {
	var parts []string
	for _, record := range pendingApprovalRecords(snapshot.Approvals) {
		parts = append(parts, "approval:"+record.ApprovalID)
	}
	for _, record := range pendingApprovalRecords(snapshot.OwnedApprovals) {
		parts = append(parts, "owned_approval:"+record.ApprovalID)
	}
	if input, ok := pendingInputRecord(snapshot.Inputs); ok {
		parts = append(parts, "input:"+input.RequestID)
	}
	return strings.Join(parts, "|")
}

func loadTasksCmd(backend *Backend) tea.Cmd {
	return func() tea.Msg {
		tasks, err := backend.ListTasks(context.Background())
		return taskListMsg{Tasks: tasks, Err: err}
	}
}

type taskOpenRequest struct {
	TaskID         string
	PreviousTaskID string
	PushPrevious   bool
	Status         string
}

func openTaskCmd(backend *Backend, request taskOpenRequest, opts Options) tea.Cmd {
	return func() tea.Msg {
		if request.PreviousTaskID != "" && backend.TaskOpenDelay > 0 {
			timer := time.NewTimer(backend.TaskOpenDelay)
			defer timer.Stop()
			<-timer.C
		}
		session, err := backend.StartSession(context.Background(), request.TaskID)
		if err != nil {
			return openTaskMsg{Err: err}
		}
		snapshot, err := backend.Refresh(context.Background(), request.TaskID, session.SessionID, RefreshOptions{
			EventLimit: opts.EventLimit,
			LoadMemory: false,
		})
		return openTaskMsg{
			Session:        session,
			Snapshot:       snapshot,
			Err:            err,
			PreviousTaskID: request.PreviousTaskID,
			PushPrevious:   request.PushPrevious,
			Status:         request.Status,
		}
	}
}

func refreshCmd(backend *Backend, taskID, sessionID string, opts Options, loadMemory bool) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := backend.Refresh(context.Background(), taskID, sessionID, RefreshOptions{
			EventLimit: opts.EventLimit,
			LoadMemory: loadMemory,
		})
		return refreshMsg{Snapshot: snapshot, Err: err}
	}
}

func promptCmd(backend *Backend, sessionID, prompt string, ctx context.Context, seq int) tea.Cmd {
	return func() tea.Msg {
		err := backend.PromptSession(ctx, sessionID, prompt)
		return promptFinishedMsg{Seq: seq, Err: err}
	}
}

func reviewCmd(backend *Backend, taskID string) tea.Cmd {
	return func() tea.Msg {
		err := backend.Review(context.Background(), taskID)
		return actionFinishedMsg{Status: "Review refreshed.", Err: err}
	}
}

func approveCmd(backend *Backend, taskID, approvalID string) tea.Cmd {
	return func() tea.Msg {
		err := backend.Approve(context.Background(), taskID, approvalID)
		return actionFinishedMsg{Status: "Approval approved.", Err: err}
	}
}

func denyCmd(backend *Backend, taskID, approvalID string) tea.Cmd {
	return func() tea.Msg {
		err := backend.Deny(context.Background(), taskID, approvalID)
		return actionFinishedMsg{Status: "Approval denied.", Err: err}
	}
}

func respondInputCmd(backend *Backend, taskID, requestID, value string) tea.Cmd {
	return func() tea.Msg {
		err := backend.RespondInput(context.Background(), taskID, requestID, value)
		return actionFinishedMsg{Status: "Input response recorded.", Err: err}
	}
}

func continueWorkerCmd(backend *Backend, taskID, workerID string) tea.Cmd {
	return func() tea.Msg {
		err := backend.ContinueWorker(context.Background(), taskID, workerID)
		return actionFinishedMsg{Status: "Worker continued.", Err: err}
	}
}

func cancelSessionCmd(backend *Backend, sessionID string) tea.Cmd {
	return func() tea.Msg {
		err := backend.CancelSession(context.Background(), sessionID)
		return actionFinishedMsg{Status: "Session cancelled.", Err: err}
	}
}

func pollTickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return pollTickMsg(t)
	})
}

func frameContentWidth(total int) int {
	if total <= 2 {
		return 1
	}
	return total - 2
}

func frameBodyHeight(total int) int {
	if total <= 11 {
		return 3
	}
	return total - 11
}

func useStackedLayout(contentWidth int) bool {
	return contentWidth < 88
}

func splitWidePaneWidths(contentWidth int) (int, int) {
	if contentWidth <= 1 {
		return contentWidth, 1
	}
	inspectorWidth := max(contentWidth/3, 24)
	if inspectorWidth > contentWidth-24 {
		inspectorWidth = max(contentWidth/2, 12)
	}
	transcriptWidth := contentWidth - inspectorWidth - 1
	if transcriptWidth < 12 {
		transcriptWidth = 12
		inspectorWidth = max(contentWidth-transcriptWidth-1, 1)
	}
	return transcriptWidth, inspectorWidth
}

func splitStackedPaneHeights(bodyHeight int) (int, int) {
	if bodyHeight <= 3 {
		return bodyHeight, 1
	}
	transcriptHeight := max((bodyHeight*3)/5, 2)
	if transcriptHeight > bodyHeight-2 {
		transcriptHeight = bodyHeight - 2
	}
	inspectorHeight := bodyHeight - transcriptHeight - 1
	if inspectorHeight < 1 {
		inspectorHeight = 1
		transcriptHeight = max(bodyHeight-inspectorHeight-1, 1)
	}
	return transcriptHeight, inspectorHeight
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func openRecentOrCreateTaskCmd(backend *Backend, opts Options) tea.Cmd {
	return func() tea.Msg {
		tasks, err := backend.ListTasks(context.Background())
		if err != nil {
			return openTaskMsg{Err: err}
		}
		if taskID := mostRecentActiveCodingTask(tasks); taskID != "" {
			return openTaskCmd(backend, taskOpenRequest{
				TaskID: taskID,
				Status: "Resumed recent coding task.",
			}, opts)()
		}
		taskFile := task.TaskFile{
			Title:     "TUI Session",
			Kind:      task.KindCoding,
			Objective: "Capture the first TUI prompt and turn it into durable task context.",
			SuccessCriteria: []task.SuccessCriterion{
				{ID: "SC-001", Statement: "First operator prompt is captured and used to drive the session"},
			},
		}
		view, err := backend.Service.CreateTask(context.Background(), taskFile, "tui", "", "")
		if err != nil {
			return openTaskMsg{Err: err}
		}
		return openTaskCmd(backend, taskOpenRequest{
			TaskID: view.Task.TaskID,
			Status: "Created TUI session task.",
		}, opts)()
	}
}

func mostRecentActiveCodingTask(tasks []task.TaskListEntry) string {
	bestID := ""
	bestUpdated := ""
	for _, entry := range tasks {
		if entry.Kind != task.KindCoding || terminalTaskState(entry.State) {
			continue
		}
		if bestID == "" || entry.UpdatedAt > bestUpdated {
			bestID = entry.TaskID
			bestUpdated = entry.UpdatedAt
		}
	}
	return bestID
}

func terminalTaskState(state task.StateName) bool {
	switch state {
	case task.StateDone, task.StateFailed, task.StateAborted:
		return true
	default:
		return false
	}
}
