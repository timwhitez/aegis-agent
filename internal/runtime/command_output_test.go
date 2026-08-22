package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/provider"
	"aegis-agent/internal/session"
	"aegis-agent/internal/tools"
)

func TestEngineInterruptedShellPreservesCurrentOutputArtifactMetadata(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 768
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 8192
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16384
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 8
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "run and pause the output command")); err != nil {
		t.Fatalf("append prompt: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{
				{ID: "call_shell_output", Name: "shell", Arguments: json.RawMessage(`{"command":"head -c 3000 /dev/zero | tr '\\0' x; sleep 10"}`)},
				{ID: "call_after_interrupt", Name: "shell", Arguments: json.RawMessage(`{"command":"printf should-not-run"}`)},
			},
			StopReason: "tool_use",
		}, nil
	})

	artifactRoot := engine.ephemeralArtifactRoot(meta.ID)
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			entries, _ := os.ReadDir(artifactRoot)
			for _, entry := range entries {
				if strings.Contains(entry.Name(), "inflight") {
					engine.control.requestPause()
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		engine.control.requestPause()
	}()

	runResult, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run interrupted shell: %v", err)
	}
	if runResult.Status != session.StatusPaused {
		t.Fatalf("interrupted shell status=%s, want paused", runResult.Status)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	last := messages[len(messages)-1]
	if last.Role != "tool" || len(last.ToolResults) != 2 {
		t.Fatalf("expected interrupted + synthetic tool results, got %#v", last)
	}
	interrupted := last.ToolResults[0]
	if !interrupted.IsError || interrupted.Metadata[tools.MetadataFailureClass] != tools.FailureClassInterrupted {
		t.Fatalf("interrupted result classification missing: %#v", interrupted)
	}
	if interrupted.Metadata["artifact_complete"] != true || interrupted.Metadata["recoverable"] != true {
		t.Fatalf("runtime discarded or changed the collector artifact facts: %#v", interrupted.Metadata)
	}
	rawBytes, ok := toolMetadataInt(interrupted.Metadata, "raw_bytes")
	if !ok || rawBytes != 3000 {
		t.Fatalf("interrupted command raw byte count=%#v, want 3000", interrupted.Metadata["raw_bytes"])
	}
	artifactPath, _ := interrupted.Metadata["artifact_path"].(string)
	if artifactPath == "" {
		t.Fatalf("interrupted command artifact path missing: %#v", interrupted.Metadata)
	}
	absolute := artifactPath
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(engine.store.SessionDir(meta.ID), filepath.FromSlash(artifactPath))
	}
	data, err := os.ReadFile(absolute)
	if err != nil || len(data) != 3000 || strings.Trim(string(data), "x") != "" {
		t.Fatalf("interrupted command artifact mismatch: bytes=%d err=%v", len(data), err)
	}
	if !last.ToolResults[1].IsError || last.ToolResults[1].ToolCallID != "call_after_interrupt" || !strings.Contains(last.ToolResults[1].LLMOutput, "interrupted") {
		t.Fatalf("later call did not receive replay-safe synthetic interruption: %#v", last.ToolResults[1])
	}
}

func TestEphemeralProviderViewReusesCurrentCommandArtifactWithoutCopy(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 768
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 8192
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16384
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 8
	engine, meta, _, registry, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	result, err := registry.Execute(context.Background(), "shell", tools.ExecContext{
		SessionID:             meta.ID,
		ToolCallID:            "call_old_shell",
		Workdir:               meta.Workdir,
		EphemeralArtifactRoot: engine.ephemeralArtifactRoot(meta.ID),
		Store:                 engine.store,
		Config:                cfg,
	}, json.RawMessage(`{"command":"head -c 3000 /dev/zero | tr '\\0' x"}`))
	if err != nil || result.IsError || result.Metadata["artifact_complete"] != true {
		t.Fatalf("execute artifact-producing shell: result=%#v err=%v", result, err)
	}
	result.ToolCallID = "call_old_shell"
	result.Name = "shell"
	messages := []session.Message{
		session.NewToolMessage([]session.ToolResult{result}),
		session.NewToolMessage([]session.ToolResult{{ToolCallID: "call_new_1", Name: "shell", LLMOutput: "new-1", DisplayOutput: "new-1"}}),
		session.NewToolMessage([]session.ToolResult{{ToolCallID: "call_new_2", Name: "shell", LLMOutput: "new-2", DisplayOutput: "new-2"}}),
	}
	before, err := commandArtifactFiles(engine.ephemeralArtifactRoot(meta.ID))
	if err != nil {
		t.Fatalf("list artifacts before provider view: %v", err)
	}
	view := engine.applyEphemeralProviderView(meta.ID, messages, messages, registry)
	after, err := commandArtifactFiles(engine.ephemeralArtifactRoot(meta.ID))
	if err != nil {
		t.Fatalf("list artifacts after provider view: %v", err)
	}
	if len(before) != 1 || len(after) != len(before) {
		t.Fatalf("ephemeral provider view copied current artifact: before=%v after=%v", before, after)
	}
	older := view[0].ToolResults[0]
	if !strings.Contains(older.LLMOutput, "Complete artifact:") || older.Metadata["ephemeral_provider_view"] != true {
		t.Fatalf("older command result did not reuse current pointer: %#v", older)
	}
}

func commandArtifactFiles(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".txt") {
			out = append(out, entry.Name())
		}
	}
	return out, nil
}
