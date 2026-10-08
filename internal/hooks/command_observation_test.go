package hooks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"aegis-agent/internal/config"
	"aegis-agent/internal/procutil"
)

func TestManagerJoinedObservationFailurePreservesExitCode(t *testing.T) {
	const reason = "regression observer permission denied"
	observeErr := fmt.Errorf("%w: %s", procutil.ErrCommandExitObservation, reason)
	originalRun := runHookCommand
	runHookCommand = func(cmd *procutil.Command) error {
		return errors.Join(observeErr, originalRun(cmd))
	}
	t.Cleanup(func() { runHookCommand = originalRun })
	for _, exitCode := range []int{0, 3} {
		t.Run(fmt.Sprintf("exit_%d", exitCode), func(t *testing.T) {
			manager := New(config.HooksConfig{UserMessage: []config.HookDefinition{{
				Name: "observer", Command: []string{"sh", "-c", fmt.Sprintf("exit %d", exitCode)},
			}}}, t.TempDir())
			var failed map[string]any
			manager.SetEmitter(func(eventType string, data map[string]any) error {
				if eventType == "hook.failed" {
					failed = data
				}
				return nil
			})
			if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"}); err != nil {
				t.Fatalf("fail-open hook returned error: %v", err)
			}
			if failed["command_exit_code"] != exitCode {
				t.Errorf("hook.failed command_exit_code = %v, want %d", failed["command_exit_code"], exitCode)
			}
			if detail, _ := failed["error"].(string); !strings.Contains(detail, reason) {
				t.Errorf("hook.failed lost observer error: %#v", failed)
			}
		})
	}
}
