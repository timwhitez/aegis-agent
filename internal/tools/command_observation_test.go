package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"aegis-agent/internal/config"
	"aegis-agent/internal/procutil"
	"aegis-agent/internal/session"
	"aegis-agent/internal/skills"
)

func TestCommandToolsSurfaceExitObservationFailure(t *testing.T) {
	const reason = "regression observer permission denied"
	observeErr := fmt.Errorf("%w: %s", procutil.ErrCommandExitObservation, reason)
	originalRun := runToolCommand
	runToolCommand = func(cmd *procutil.Command) error {
		return errors.Join(observeErr, originalRun(cmd))
	}
	t.Cleanup(func() { runToolCommand = originalRun })
	for _, toolName := range []string{"shell", "observer_skill"} {
		for _, exitCode := range []int{0, 7} {
			t.Run(fmt.Sprintf("%s/exit_%d", toolName, exitCode), func(t *testing.T) {
				cfg := config.Default()
				ec := newOutputCollectorExecContext(t, cfg)
				registry, err := NewRegistry(cfg, nil, ec.Store, nil)
				if err != nil {
					t.Fatal(err)
				}
				command := fmt.Sprintf("printf ordinary-output; exit %d", exitCode)
				if toolName != "shell" {
					registry.Register(commandToolDefinition(cfg, skills.CommandTool{
						Name: toolName, SkillName: "observer", SkillPath: filepath.Join(ec.Workdir, "SKILL.md"),
						Command: []string{"sh", "-c", command}, InputSchema: map[string]any{"type": "object"},
					}))
				}
				input, _ := json.Marshal(map[string]any{"command": command})
				if toolName != "shell" {
					input = []byte(`{}`)
				}
				result, err := registry.Execute(context.Background(), toolName, ec, input)
				if err != nil {
					t.Fatalf("recoverable observer failure returned control error: %v", err)
				}
				if !result.IsError || result.Metadata[MetadataFailureClass] != FailureClassHarnessError {
					t.Errorf("want harness error, got %#v", result)
				}
				if result.Metadata["exit_code"] != exitCode {
					t.Errorf("exit_code = %v, want %d", result.Metadata["exit_code"], exitCode)
				}
				for _, output := range []string{result.LLMOutput, result.DisplayOutput} {
					if !strings.Contains(output, reason) || !strings.Contains(output, "ordinary-output") {
						t.Errorf("output lost observer failure or command output: %q", output)
					}
				}
				if detail, _ := result.Metadata["exit_observation_error"].(string); !strings.Contains(detail, reason) {
					t.Errorf("observer failure missing from metadata: %#v", result.Metadata)
				}
				message := session.NewMessage("tool", "")
				message.ToolResults = []session.ToolResult{result}
				if err := ec.Store.AppendMessage(ec.SessionID, message); err != nil {
					t.Fatal(err)
				}
				messages, err := ec.Store.LoadMessages(ec.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				persisted := messages[len(messages)-1].ToolResults[0]
				if detail, _ := persisted.Metadata["exit_observation_error"].(string); !strings.Contains(detail, reason) || !strings.Contains(persisted.LLMOutput, reason) {
					t.Errorf("persisted result lost observer failure: %#v", persisted)
				}
			})
		}
	}
}
