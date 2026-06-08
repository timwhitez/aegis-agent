package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type actionPaletteItem struct {
	ID             string
	Label          string
	Command        string
	Tag            string
	Description    string
	Aliases        []string
	Enabled        bool
	DisabledReason string
	Priority       int
}

func (m model) openActionPalette() (tea.Model, tea.Cmd) {
	m.clearEscPriming()
	query, ok := m.composerActionQuery()
	m.modal = modalActionPalette
	if ok {
		m.actionFilter = query
	} else {
		m.actionFilter = ""
	}
	m.actionIndex = 0
	m.clampActionIndex()
	m.syncFocusStates()
	return m, nil
}

func (m model) updateActionPalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	filtered := m.filteredActionPaletteItems()
	switch msg.String() {
	case "esc", "ctrl+k":
		m.modal = modalNone
		m.syncFocusStates()
		return m, nil
	case "up", "k":
		if m.actionIndex > 0 {
			m.actionIndex--
		}
		return m, nil
	case "down", "j":
		if m.actionIndex < len(filtered)-1 {
			m.actionIndex++
		}
		return m, nil
	case "enter":
		if len(filtered) == 0 {
			return m, nil
		}
		return m.executeActionPaletteSelection(filtered[m.actionIndex])
	case "backspace":
		if len(m.actionFilter) > 0 {
			runes := []rune(m.actionFilter)
			m.actionFilter = string(runes[:len(runes)-1])
			m.clampActionIndex()
		}
		return m, nil
	}
	if runes, ok := composerInputRunes(msg); ok {
		m.actionFilter += string(runes)
		m.actionIndex = 0
		m.clampActionIndex()
	}
	return m, nil
}

func (m model) executeActionPaletteSelection(item actionPaletteItem) (tea.Model, tea.Cmd) {
	if !item.Enabled {
		m.errorLine = item.DisabledReason
		return m, nil
	}
	m.modal = modalNone
	m.actionFilter = ""
	m.actionIndex = 0
	m.syncFocusStates()
	switch item.Command {
	case "/quit", "/exit":
		if m.running {
			m.modal = modalConfirmInterrupt
			m.confirmIndex = 1
			m.syncFocusStates()
			return m, nil
		}
		return m, tea.Quit
	case "/help":
		return m.openHelp()
	case "/approvals":
		return m.openApprovals()
	case "/input":
		return m.openInputRequest()
	case "/picker":
		return m.openTaskPicker()
	case "/tasks":
		return m.openTaskNavigation()
	case "/back":
		return m.navigateBack()
	case "/refresh":
		return m.refreshSnapshot()
	case "/clear":
		m.errorLine = ""
		m.statusLine = "Cleared transient TUI status."
		return m, nil
	case "/review":
		return m.refreshReview()
	case "/run", "/resume", "/mission":
		if m.running {
			m.queuedPrompts = append(m.queuedPrompts, item.Command)
			m.statusLine = queuedPromptStatus(len(m.queuedPrompts))
			m.errorLine = ""
			return m, nil
		}
		draft := m.composerDraftValue()
		cmd := m.startPrompt(item.Command, false)
		if strings.TrimSpace(draft) != "" {
			m.composer.SetValue(draft)
			m.composer.CursorEnd()
		}
		return m, cmd
	default:
		return m, nil
	}
}

func (m model) filteredActionPaletteItems() []actionPaletteItem {
	return filterActionPaletteItems(m.actionPaletteItems(), m.actionFilter)
}

func filterActionPaletteItems(items []actionPaletteItem, rawQuery string) []actionPaletteItem {
	query := normalizeActionQuery(rawQuery)
	if query == "" {
		return items
	}
	filtered := make([]actionPaletteItem, 0, len(items))
	var fallback []actionPaletteItem
	for _, item := range items {
		if actionPalettePrefixMatch(item, query) {
			filtered = append(filtered, item)
			continue
		}
		if actionPaletteContainsMatch(item, query) {
			fallback = append(fallback, item)
		}
	}
	filtered = append(filtered, fallback...)
	return filtered
}

func normalizeActionQuery(raw string) string {
	query := strings.ToLower(strings.TrimSpace(raw))
	query = strings.TrimPrefix(query, "/")
	return strings.Join(strings.Fields(query), " ")
}

func actionPalettePrefixMatch(item actionPaletteItem, query string) bool {
	for _, candidate := range actionPaletteCandidates(item) {
		if strings.HasPrefix(candidate, query) {
			return true
		}
	}
	return false
}

func actionPaletteContainsMatch(item actionPaletteItem, query string) bool {
	for _, candidate := range actionPaletteCandidates(item) {
		if strings.Contains(candidate, query) {
			return true
		}
	}
	return false
}

func actionPaletteCandidates(item actionPaletteItem) []string {
	values := make([]string, 0, 4+len(item.Aliases))
	values = append(values, normalizeActionQuery(item.Label))
	values = append(values, normalizeActionQuery(item.Command))
	values = append(values, normalizeActionQuery(item.Tag))
	values = append(values, normalizeActionQuery(item.Description))
	for _, alias := range item.Aliases {
		values = append(values, normalizeActionQuery(alias))
	}
	return values
}

func (m model) composerActionQuery() (string, bool) {
	if m.focus != focusComposer || m.modal != modalNone {
		return "", false
	}
	line := m.composerDraftValue()
	if idx := strings.Index(line, "\n"); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return "", false
	}
	if strings.Contains(line, " ") {
		return "", false
	}
	return canonicalComposerActionQuery(strings.TrimPrefix(line, "/")), true
}

func canonicalComposerActionQuery(query string) string {
	switch normalizeActionQuery(query) {
	case "exit":
		return "quit"
	default:
		return query
	}
}

func (m model) composerActionSuggestions() []actionPaletteItem {
	query, ok := m.composerActionQuery()
	if !ok {
		return nil
	}
	items := filterActionPaletteItems(m.actionPaletteItems(), query)
	if len(items) == 1 && actionQueryMatchesCommand(query, items[0]) {
		return nil
	}
	if len(items) > 5 {
		items = items[:5]
	}
	return items
}

func (m model) composerActionSuggestionsActive() bool {
	return len(m.composerActionSuggestions()) > 0
}

func actionQueryMatchesCommand(query string, item actionPaletteItem) bool {
	return normalizeActionQuery(item.Command) == normalizeActionQuery(query)
}

func (m *model) clampComposerActionIndex() {
	items := m.composerActionSuggestions()
	if len(items) == 0 {
		m.composerActionIndex = 0
		return
	}
	if m.composerActionIndex < 0 {
		m.composerActionIndex = 0
	}
	if m.composerActionIndex >= len(items) {
		m.composerActionIndex = len(items) - 1
	}
}

func (m *model) acceptComposerActionSuggestion() bool {
	items := m.composerActionSuggestions()
	if len(items) == 0 {
		return false
	}
	m.clampComposerActionIndex()
	selected := items[m.composerActionIndex]
	m.composer.SetValue(selected.Command)
	m.composer.CursorEnd()
	m.statusLine = fmt.Sprintf("Completed %s. Press Enter to apply it.", selected.Command)
	m.errorLine = ""
	m.clampComposerActionIndex()
	return true
}

func (m *model) clampActionIndex() {
	filtered := m.filteredActionPaletteItems()
	if len(filtered) == 0 {
		m.actionIndex = 0
		return
	}
	if m.actionIndex < 0 {
		m.actionIndex = 0
	}
	if m.actionIndex >= len(filtered) {
		m.actionIndex = len(filtered) - 1
	}
}

func (m model) actionPaletteItems() []actionPaletteItem {
	approvalsPending := len(pendingApprovalRecords(m.snapshot.Approvals))+len(pendingApprovalRecords(m.snapshot.OwnedApprovals)) > 0
	inputPending := pendingInputRecordExists(m.snapshot)
	hasTaskHistory := len(m.taskHistory) > 0
	hasRelatedTasks := len(m.relatedTaskTargets()) > 0

	queueOrReadyTag := func() string {
		if m.running {
			return "queue"
		}
		return "ready"
	}
	appendAction := func(items []actionPaletteItem, item actionPaletteItem) []actionPaletteItem {
		return append(items, item)
	}

	items := make([]actionPaletteItem, 0, 10)
	items = appendAction(items, actionPaletteItem{
		ID:          "approvals",
		Label:       "approvals",
		Command:     "/approvals",
		Tag:         actionTag("next", "ready", approvalsPending),
		Description: "Inspect and resolve pending approval requests.",
		Aliases:     []string{"approval", "审批"},
		Enabled:     true,
		Priority:    actionPriority(115, 70, approvalsPending),
	})
	items = appendAction(items, actionPaletteItem{
		ID:          "input",
		Label:       "input",
		Command:     "/input",
		Tag:         actionTag("next", "ready", inputPending),
		Description: "Answer the current input request.",
		Aliases:     []string{"reply", "输入"},
		Enabled:     true,
		Priority:    actionPriority(110, 68, inputPending),
	})
	items = appendAction(items, actionPaletteItem{
		ID:          "run",
		Label:       "run",
		Command:     "/run",
		Tag:         queueOrReadyTag(),
		Description: "Start a full runtime pass for the current task.",
		Aliases:     []string{"运行"},
		Enabled:     true,
		Priority:    actionPriority(100, 62, !m.running),
	})
	items = appendAction(items, actionPaletteItem{
		ID:          "resume",
		Label:       "resume",
		Command:     "/resume",
		Tag:         queueOrReadyTag(),
		Description: "Continue from the latest durable task state.",
		Aliases:     []string{"继续"},
		Enabled:     true,
		Priority:    actionPriority(96, 60, !m.running),
	})
	items = appendAction(items, actionPaletteItem{
		ID:          "mission",
		Label:       "mission",
		Command:     "/mission",
		Tag:         queueOrReadyTag(),
		Description: "Open mission status, or type /mission PROMPT to set the current goal.",
		Aliases:     []string{"missions", "goal", "goals", "目标"},
		Enabled:     true,
		Priority:    actionPriority(94, 59, !m.running),
	})
	items = appendAction(items, actionPaletteItem{
		ID:             "review",
		Label:          "review",
		Command:        "/review",
		Tag:            actionEnabledTag(!m.running),
		Description:    "Refresh review and completion truth.",
		Aliases:        []string{"审查"},
		Enabled:        !m.running,
		DisabledReason: "Wait for the current turn to settle before reviewing.",
		Priority:       actionPriority(90, 22, !m.running),
	})
	items = appendAction(items, actionPaletteItem{
		ID:          "tasks",
		Label:       "tasks",
		Command:     "/tasks",
		Tag:         actionTag("next", "ready", hasRelatedTasks),
		Description: "Open related-task navigation in the inspector.",
		Aliases:     []string{"related", "related tasks", "相关任务"},
		Enabled:     true,
		Priority:    actionPriority(88, 58, hasRelatedTasks),
	})
	items = appendAction(items, actionPaletteItem{
		ID:             "picker",
		Label:          "picker",
		Command:        "/picker",
		Tag:            actionEnabledTag(!m.running),
		Description:    "Switch to another workspace task.",
		Aliases:        []string{"task list", "tasklist", "任务列表", "切换任务"},
		Enabled:        !m.running,
		DisabledReason: "Wait for the current turn to settle before switching tasks.",
		Priority:       actionPriority(84, 24, !m.running),
	})
	items = appendAction(items, actionPaletteItem{
		ID:             "back",
		Label:          "back",
		Command:        "/back",
		Tag:            actionBackTag(hasTaskHistory, m.running),
		Description:    "Return to the previous task in local navigation history.",
		Aliases:        []string{"返回"},
		Enabled:        !m.running && hasTaskHistory,
		DisabledReason: actionBackDisabledReason(hasTaskHistory, m.running),
		Priority:       actionPriority(86, 16, hasTaskHistory && !m.running),
	})
	items = appendAction(items, actionPaletteItem{
		ID:          "refresh",
		Label:       "refresh",
		Command:     "/refresh",
		Tag:         "ready",
		Description: "Reload the current TUI snapshot.",
		Aliases:     []string{"status", "reload", "刷新"},
		Enabled:     true,
		Priority:    72,
	})
	items = appendAction(items, actionPaletteItem{
		ID:          "clear",
		Label:       "clear",
		Command:     "/clear",
		Tag:         "ready",
		Description: "Clear transient TUI status without deleting artifacts.",
		Aliases:     []string{"清空"},
		Enabled:     true,
		Priority:    70,
	})
	items = appendAction(items, actionPaletteItem{
		ID:          "help",
		Label:       "help",
		Command:     "/help",
		Tag:         "ready",
		Description: "Show the local interaction help.",
		Aliases:     []string{"?", "帮助"},
		Enabled:     true,
		Priority:    66,
	})
	items = appendAction(items, actionPaletteItem{
		ID:          "quit",
		Label:       "quit",
		Command:     "/quit",
		Tag:         "ready",
		Description: "Leave the TUI, or confirm an interrupt first if a turn is active.",
		Aliases:     []string{"exit", "退出"},
		Enabled:     true,
		Priority:    64,
	})

	if m.opts.SimpleMode {
		items = filterSimpleModeActionPaletteItems(items)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Priority > items[j].Priority
	})
	return items
}

func filterSimpleModeActionPaletteItems(items []actionPaletteItem) []actionPaletteItem {
	out := make([]actionPaletteItem, 0, len(items))
	for _, item := range items {
		switch item.ID {
		case "tasks", "picker", "back":
			continue
		default:
			out = append(out, item)
		}
	}
	return out
}

func actionPriority(preferred, fallback int, preferredState bool) int {
	if preferredState {
		return preferred
	}
	return fallback
}

func actionTag(preferred, fallback string, preferredState bool) string {
	if preferredState {
		return preferred
	}
	return fallback
}

func actionEnabledTag(enabled bool) string {
	if enabled {
		return "ready"
	}
	return "blocked"
}

func actionBackTag(hasHistory, running bool) string {
	switch {
	case running:
		return "blocked"
	case hasHistory:
		return "ready"
	default:
		return "blocked"
	}
}

func actionBackDisabledReason(hasHistory, running bool) string {
	if running {
		return "Wait for the current turn to settle before going back."
	}
	if !hasHistory {
		return "No previous task is available in local navigation history."
	}
	return fmt.Sprintf("Action %q is currently unavailable.", "back")
}
