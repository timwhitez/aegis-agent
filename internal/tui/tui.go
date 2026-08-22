package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

type Snapshot struct {
	Sessions      []session.SessionSummary
	SelectedIndex int
	Meta          session.SessionMetadata
	State         session.State
	Messages      []session.Message
	Events        []events.Event
	Children      []session.SessionSummary
	Jobs          []session.QueueJob
}

func BuildSnapshot(store *session.Store, selectedID string, limit int) (Snapshot, error) {
	sessions, err := store.List(limit)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Sessions: sessions}
	if len(sessions) == 0 {
		if selectedID != "" {
			selected, err := loadSelectedSummary(store, selectedID)
			if err != nil {
				return Snapshot{}, err
			}
			snapshot.Sessions = append(snapshot.Sessions, selected)
		}
		if len(snapshot.Sessions) == 0 {
			return snapshot, nil
		}
	}
	index := 0
	if selectedID != "" {
		found := false
		for i, item := range snapshot.Sessions {
			if item.ID == selectedID {
				index = i
				found = true
				break
			}
		}
		if !found {
			selected, err := loadSelectedSummary(store, selectedID)
			if err != nil {
				return Snapshot{}, err
			}
			snapshot.Sessions = append(snapshot.Sessions, selected)
			index = len(snapshot.Sessions) - 1
		}
	}
	snapshot.SelectedIndex = index
	selected := snapshot.Sessions[index]
	meta, err := store.LoadMetadata(selected.ID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load selected session session.json: %w", err)
	}
	snapshot.Meta = meta
	state, err := store.LoadState(selected.ID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load selected session state.json: %w", err)
	}
	snapshot.State = state
	messages, _, err := store.LoadMessagesTail(selected.ID, 6)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load selected session messages.jsonl: %w", err)
	}
	snapshot.Messages = messages
	eventsList, _, err := store.LoadEventsTail(selected.ID, 8)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load selected session events.jsonl: %w", err)
	}
	snapshot.Events = eventsList
	children, err := store.ListChildren(selected.ID, limit)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load selected session children: %w", err)
	}
	snapshot.Children = children
	jobs, err := store.ListJobsByParent(selected.ID, limit)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load selected session queue jobs: %w", err)
	}
	snapshot.Jobs = jobs
	return snapshot, nil
}

func loadSelectedSummary(store *session.Store, sessionID string) (session.SessionSummary, error) {
	meta, err := store.LoadMetadata(sessionID)
	if err != nil {
		return session.SessionSummary{}, fmt.Errorf("load selected session session.json: %w", err)
	}
	state, err := store.LoadState(sessionID)
	if err != nil {
		return session.SessionSummary{}, fmt.Errorf("load selected session state.json: %w", err)
	}
	return session.SessionSummary{
		ID:              meta.ID,
		Status:          state.Status,
		Provider:        meta.Provider,
		Model:           meta.Model,
		CreatedAt:       meta.CreatedAt,
		UpdatedAt:       state.UpdatedAt,
		Phase:           state.Phase,
		LastError:       state.LastError,
		Workdir:         meta.Workdir,
		ParentSessionID: meta.ParentSessionID,
		RootSessionID:   meta.RootSessionID,
		AgentName:       meta.AgentName,
		AgentRole:       meta.AgentRole,
		ToolProfile:     meta.ToolProfile,
		Depth:           meta.Depth,
		QueueJobID:      meta.QueueJobID,
	}, nil
}

func Render(snapshot Snapshot) string {
	if len(snapshot.Sessions) == 0 {
		return panel("Sessions", []string{"No sessions found."})
	}
	selected := snapshot.Sessions[snapshot.SelectedIndex]
	sessionLines := make([]string, 0, len(snapshot.Sessions))
	for i, item := range snapshot.Sessions {
		prefix := "  "
		if i == snapshot.SelectedIndex {
			prefix = "> "
		}
		sessionLines = append(sessionLines, fmt.Sprintf("%s%s  %s  %s", prefix, item.ID, item.Status, item.AgentName))
	}
	detailLines := []string{
		"id: " + selected.ID,
		"status: " + snapshot.State.Status,
		"phase: " + snapshot.State.Phase,
		"provider: " + selected.Provider,
		"model: " + selected.Model,
		"workdir: " + selected.Workdir,
	}
	if snapshot.Meta.ParentSessionID != "" {
		detailLines = append(detailLines, "parent: "+snapshot.Meta.ParentSessionID)
	}
	if snapshot.Meta.RootSessionID != "" {
		detailLines = append(detailLines, "root: "+snapshot.Meta.RootSessionID)
	}
	if snapshot.Meta.Isolation != nil {
		detailLines = append(detailLines, "isolation: "+snapshot.Meta.Isolation.Mode+" -> "+snapshot.Meta.Isolation.Workdir)
	}
	if snapshot.State.LastError != "" {
		detailLines = append(detailLines, "last_error: "+snapshot.State.LastError)
	}
	messageLines := []string{}
	for _, msg := range snapshot.Messages {
		messageLines = append(messageLines, fmt.Sprintf("%s: %s", msg.Role, summarizeMessage(msg)))
	}
	childLines := []string{}
	for _, child := range snapshot.Children {
		childLines = append(childLines, fmt.Sprintf("session %s  %s  %s", child.ID, child.Status, child.AgentName))
	}
	for _, job := range snapshot.Jobs {
		childLines = append(childLines, fmt.Sprintf("job %s  %s  %s", job.ID, job.Status, job.AgentName))
	}
	if len(childLines) == 0 {
		childLines = append(childLines, "No child sessions or jobs.")
	}
	eventLines := []string{}
	for _, evt := range snapshot.Events {
		eventLines = append(eventLines, fmt.Sprintf("%s  %s", evt.Type, summarizeEvent(evt)))
	}
	if len(eventLines) == 0 {
		eventLines = append(eventLines, "No recent events.")
	}
	footerLines := []string{
		"j/k or arrows: move   g/G: top/bottom   r: refresh   q: quit",
	}
	sections := []string{
		panel("Sessions", sessionLines),
		panel("Details", detailLines),
		panel("Recent Messages", messageLines),
		panel("Children And Queue", childLines),
		panel("Recent Events", eventLines),
		panel("Footer", footerLines),
	}
	return strings.Join(sections, "\n\n") + "\n"
}

func Run(ctx context.Context, store *session.Store, selectedID string, limit, refreshMS int, stdout *os.File, stdin *os.File) error {
	if !term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) {
		return fmt.Errorf("tui requires a TTY; use --once for snapshot mode")
	}
	if refreshMS <= 0 {
		refreshMS = 1000
	}
	oldState, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(stdin.Fd()), oldState)
	if _, err := io.WriteString(stdout, "\x1b[?1049h\x1b[H"); err != nil {
		return err
	}
	defer io.WriteString(stdout, "\x1b[?1049l")

	keyCh := make(chan []byte, 8)
	done := make(chan struct{})
	defer close(done)
	go readKeys(stdin, keyCh, done)

	currentID := selectedID
	refresh := time.NewTicker(time.Duration(refreshMS) * time.Millisecond)
	defer refresh.Stop()
	for {
		snapshot, err := BuildSnapshot(store, currentID, limit)
		if err != nil {
			return err
		}
		if len(snapshot.Sessions) > 0 {
			currentID = snapshot.Sessions[snapshot.SelectedIndex].ID
		}
		if _, err := io.WriteString(stdout, "\x1b[2J\x1b[H"+Render(snapshot)); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case seq, ok := <-keyCh:
			if !ok {
				return nil
			}
			nextID, quit := handleKey(seq, snapshot)
			if quit {
				return nil
			}
			if nextID != "" {
				currentID = nextID
			}
		case <-refresh.C:
		}
	}
}

func handleKey(seq []byte, snapshot Snapshot) (string, bool) {
	if len(seq) == 0 {
		return "", false
	}
	current := snapshot.SelectedIndex
	switch {
	case len(seq) == 1 && seq[0] == 'q':
		return "", true
	case len(seq) == 1 && seq[0] == 'r':
		if len(snapshot.Sessions) > 0 {
			return snapshot.Sessions[current].ID, false
		}
	case len(seq) == 1 && seq[0] == 'g':
		if len(snapshot.Sessions) > 0 {
			return snapshot.Sessions[0].ID, false
		}
	case len(seq) == 1 && seq[0] == 'G':
		if len(snapshot.Sessions) > 0 {
			return snapshot.Sessions[len(snapshot.Sessions)-1].ID, false
		}
	case len(seq) == 1 && seq[0] == 'j':
		if current+1 < len(snapshot.Sessions) {
			return snapshot.Sessions[current+1].ID, false
		}
	case len(seq) == 1 && seq[0] == 'k':
		if current-1 >= 0 {
			return snapshot.Sessions[current-1].ID, false
		}
	case len(seq) == 3 && seq[0] == 27 && seq[1] == '[' && seq[2] == 'B':
		if current+1 < len(snapshot.Sessions) {
			return snapshot.Sessions[current+1].ID, false
		}
	case len(seq) == 3 && seq[0] == 27 && seq[1] == '[' && seq[2] == 'A':
		if current-1 >= 0 {
			return snapshot.Sessions[current-1].ID, false
		}
	}
	return "", false
}

func readKeys(stdin *os.File, out chan<- []byte, done <-chan struct{}) {
	defer close(out)
	buf := make([]byte, 16)
	decoder := keySequenceDecoder{}
	for {
		n, err := stdin.Read(buf)
		for _, seq := range decoder.Push(buf[:n], err != nil || n == 0) {
			select {
			case out <- seq:
			case <-done:
				return
			}
		}
		if err != nil || n == 0 {
			return
		}
	}
}

type keySequenceDecoder struct {
	pending []byte
}

// Push preserves an incomplete CSI prefix across raw-mode reads. A lone ESC is
// emitted before the next non-CSI key, so it cannot swallow a following `q`.
func (d *keySequenceDecoder) Push(data []byte, final bool) [][]byte {
	combined := make([]byte, 0, len(d.pending)+len(data))
	combined = append(combined, d.pending...)
	combined = append(combined, data...)
	d.pending = d.pending[:0]
	var out [][]byte
	for i := 0; i < len(combined); {
		if combined[i] != 27 {
			out = append(out, []byte{combined[i]})
			i++
			continue
		}
		if i+1 == len(combined) && !final {
			d.pending = append(d.pending, combined[i])
			break
		}
		if i+1 < len(combined) && combined[i+1] == '[' {
			if i+2 == len(combined) && !final {
				d.pending = append(d.pending, combined[i:]...)
				break
			}
			if i+2 < len(combined) {
				out = append(out, []byte{combined[i], combined[i+1], combined[i+2]})
				i += 3
				continue
			}
		}
		out = append(out, []byte{combined[i]})
		i++
	}
	return out
}

func splitKeySequences(data []byte) [][]byte {
	decoder := keySequenceDecoder{}
	return decoder.Push(data, true)
}

func panel(title string, lines []string) string {
	if len(lines) == 0 {
		lines = []string{"(empty)"}
	}
	var b strings.Builder
	border := "+" + strings.Repeat("-", 78) + "+"
	b.WriteString(border)
	b.WriteByte('\n')
	b.WriteString("| " + padToWidth(title, 76) + " |\n")
	b.WriteString(border)
	for _, line := range lines {
		for _, wrapped := range wrapLine(line, 76) {
			b.WriteByte('\n')
			b.WriteString("| " + padToWidth(wrapped, 76) + " |")
		}
	}
	b.WriteByte('\n')
	b.WriteString(border)
	return b.String()
}

func wrapLine(line string, width int) []string {
	if width <= 0 || displayWidth(line) <= width {
		return []string{line}
	}
	var out []string
	start := 0
	current := 0
	for i, r := range line {
		w := runeDisplayWidth(r)
		if current+w > width && i > start {
			out = append(out, line[start:i])
			start = i
			current = 0
		}
		current += w
	}
	if start < len(line) {
		out = append(out, line[start:])
	}
	return out
}

// padToWidth right-pads text with spaces up to width terminal columns. It is
// used instead of fmt's %-Ns because fmt pads by rune count, which disagrees
// with the display width used when wrapping.
func padToWidth(text string, width int) string {
	pad := width - displayWidth(text)
	if pad <= 0 {
		return text
	}
	return text + strings.Repeat(" ", pad)
}

func displayWidth(text string) int {
	width := 0
	for _, r := range text {
		width += runeDisplayWidth(r)
	}
	return width
}

// runeDisplayWidth reports how many terminal columns r occupies. It covers the
// common cases only: combining marks and format characters are zero width,
// East Asian wide/fullwidth ranges and the main emoji blocks are two columns,
// everything else is one column.
func runeDisplayWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r), unicode.Is(unicode.Cf, r):
		return 0
	case r == 0x2329, r == 0x232A,
		r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0x303E,
		r >= 0x3041 && r <= 0x33FF,
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x4E00 && r <= 0x9FFF,
		r >= 0xA000 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F,
		r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	default:
		return 1
	}
}

func summarizeMessage(msg session.Message) string {
	if strings.TrimSpace(msg.Text) != "" {
		return truncate(msg.Text, 120)
	}
	if len(msg.ToolCalls) > 0 {
		names := make([]string, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			names = append(names, call.Name)
		}
		return "tool_calls: " + strings.Join(names, ", ")
	}
	if len(msg.ToolResults) > 0 {
		names := make([]string, 0, len(msg.ToolResults))
		for _, result := range msg.ToolResults {
			names = append(names, result.Name)
		}
		return "tool_results: " + strings.Join(names, ", ")
	}
	return "(empty)"
}

func summarizeEvent(evt events.Event) string {
	if len(evt.Data) == 0 {
		return evt.Phase
	}
	if value, ok := evt.Data["status"]; ok {
		return fmt.Sprintf("%s=%v", evt.Phase, value)
	}
	if value, ok := evt.Data["error"]; ok {
		return fmt.Sprintf("%s=%v", evt.Phase, value)
	}
	if value, ok := evt.Data["job_id"]; ok {
		return fmt.Sprintf("%s=%v", evt.Phase, value)
	}
	return evt.Phase
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len(value) <= limit {
		return value
	}
	return prefixAtRuneBoundary(value, limit-3) + "..."
}

func prefixAtRuneBoundary(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(text) {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func tailMessages(messages []session.Message, limit int) []session.Message {
	if len(messages) <= limit {
		return messages
	}
	return messages[len(messages)-limit:]
}

func tailEvents(items []events.Event, limit int) []events.Event {
	if len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}
