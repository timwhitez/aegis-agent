package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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
		return snapshot, nil
	}
	index := 0
	if selectedID != "" {
		for i, item := range sessions {
			if item.ID == selectedID {
				index = i
				break
			}
		}
	}
	snapshot.SelectedIndex = index
	selected := sessions[index]
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
	messages, err := store.LoadMessages(selected.ID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load selected session messages.jsonl: %w", err)
	}
	snapshot.Messages = tailMessages(messages, 6)
	eventsList, err := store.LoadEvents(selected.ID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load selected session events.jsonl: %w", err)
	}
	snapshot.Events = tailEvents(eventsList, 8)
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
	go readKeys(stdin, keyCh)

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

func readKeys(stdin *os.File, out chan<- []byte) {
	buf := make([]byte, 3)
	for {
		n, err := stdin.Read(buf[:1])
		if err != nil || n == 0 {
			close(out)
			return
		}
		seq := []byte{buf[0]}
		if buf[0] == 27 {
			stdin.Read(buf[1:2])
			stdin.Read(buf[2:3])
			seq = []byte{buf[0], buf[1], buf[2]}
		}
		out <- seq
	}
}

func panel(title string, lines []string) string {
	if len(lines) == 0 {
		lines = []string{"(empty)"}
	}
	var b strings.Builder
	border := "+" + strings.Repeat("-", 78) + "+"
	b.WriteString(border)
	b.WriteByte('\n')
	b.WriteString(fmt.Sprintf("| %-76s |\n", title))
	b.WriteString(border)
	for _, line := range lines {
		for _, wrapped := range wrapLine(line, 76) {
			b.WriteByte('\n')
			b.WriteString(fmt.Sprintf("| %-76s |", wrapped))
		}
	}
	b.WriteByte('\n')
	b.WriteString(border)
	return b.String()
}

func wrapLine(line string, width int) []string {
	if width <= 0 || len(line) <= width {
		return []string{line}
	}
	var out []string
	for len(line) > width {
		out = append(out, line[:width])
		line = line[width:]
	}
	if line != "" {
		out = append(out, line)
	}
	return out
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
	return value[:limit-3] + "..."
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
