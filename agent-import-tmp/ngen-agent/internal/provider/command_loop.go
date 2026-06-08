package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"ngen/internal/task"
)

const (
	commandProviderModeEnv        = "NGEN_PROVIDER_MODE"
	commandProviderOperationEnv   = "NGEN_PROVIDER_OPERATION"
	commandProviderMaxOutputBytes = 1024 * 1024
)

type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			_, _ = b.buf.Write(p)
		} else {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *cappedBuffer) String() string {
	return b.buf.String()
}

func (b *cappedBuffer) Truncated() bool {
	return b.truncated
}

func runCommandProviderRaw(ctx context.Context, mode string, command []string, operation string, input any) ([]byte, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("provider command is required for %s", operation)
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = append(os.Environ(),
		commandProviderModeEnv+"="+CanonicalMode(mode),
		commandProviderOperationEnv+"="+strings.TrimSpace(operation),
	)
	stdout := newCappedBuffer(commandProviderMaxOutputBytes)
	stderr := newCappedBuffer(commandProviderMaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = bytes.NewReader(data)
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if stderr.Truncated() {
			detail = detail + fmt.Sprintf(" [stderr truncated after %d bytes]", commandProviderMaxOutputBytes)
		}
		return nil, fmt.Errorf("provider command %s failed: %v: %s", operation, err, detail)
	}
	if stdout.Truncated() {
		return nil, fmt.Errorf("provider command %s stdout exceeds max bytes (%d)", operation, commandProviderMaxOutputBytes)
	}
	payload := bytes.TrimSpace(stdout.Bytes())
	if len(payload) == 0 {
		return nil, fmt.Errorf("provider command %s returned empty output", operation)
	}
	return payload, nil
}

func generateWorkspaceEditWithCommand(ctx context.Context, cfg task.ProviderConfig, input WorkspaceEditInput) (WorkspaceEditPlan, error) {
	payload, err := runCommandProviderRaw(ctx, cfg.Mode, cfg.Command, "workspace_edit", input)
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	return decodeWorkspaceEditPayload("command workspace edit", payload)
}

func generateWorkspaceObservationsWithCommand(ctx context.Context, cfg task.ProviderConfig, input WorkspaceObservationInput) (WorkspaceObservationPlan, error) {
	payload, err := runCommandProviderRaw(ctx, cfg.Mode, cfg.Command, "workspace_observation", input)
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	return decodeWorkspaceObservationPayload("command workspace observation", payload)
}

func generateMissionValidationWithCommand(ctx context.Context, cfg task.ProviderConfig, input MissionValidationInput) (MissionValidationResult, error) {
	payload, err := runCommandProviderRaw(ctx, cfg.Mode, cfg.Command, "mission_validation", input)
	if err != nil {
		return MissionValidationResult{}, err
	}
	return decodeMissionValidationPayload("command mission validation", payload)
}
