package runtime

import (
	"strings"
	"testing"

	"go-cli-agent/internal/session"
)

func TestDoomLoopReminderUsesToolSignaturePatternWithoutWorkflowGuard(t *testing.T) {
	var messages []session.Message
	for i := 0; i < 3; i++ {
		messages = append(messages, session.NewToolMessage([]session.ToolResult{{
			Name:          "load_skill",
			LLMOutput:     "loaded",
			DisplayOutput: "loaded",
		}}))
	}
	text := toolLoopReminderText(messages)
	if !strings.Contains(text, "load_skill repeated 3 times") {
		t.Fatalf("expected repeated load_skill reminder, got %q", text)
	}
	for _, forbidden := range []string{"must spawn", "agent_spawn", "delegate", "read these files in order"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("reminder should not encode a fixed workflow, got %q", text)
		}
	}
}

func TestToolRepetitionSummaryCountsReadPathsAndTodoNoops(t *testing.T) {
	messages := []session.Message{
		session.NewToolMessage([]session.ToolResult{{
			Name: "read_file",
			Metadata: map[string]any{
				"path":   "/tmp/a.txt",
				"offset": 0,
				"end":    10,
			},
		}}),
		session.NewToolMessage([]session.ToolResult{{
			Name: "read_file",
			Metadata: map[string]any{
				"path":   "/tmp/a.txt",
				"offset": 0,
				"end":    10,
			},
		}}),
		session.NewToolMessage([]session.ToolResult{{
			Name:     "todo_write",
			Metadata: map[string]any{"noop": true},
		}}),
	}
	summary := summarizeToolRepetition(messages)
	if len(summary.TopReadPaths) == 0 || summary.TopReadPaths[0].Count != 2 || summary.TodoNoopCount != 1 {
		t.Fatalf("unexpected repetition summary: %#v", summary)
	}
}
