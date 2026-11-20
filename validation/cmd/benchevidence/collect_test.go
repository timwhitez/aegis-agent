package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/events"
	"aegis-agent/internal/session"
)

func TestProjectAndValidateAtBothBoundaries(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := session.NewSessionID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	const marker = "synthetic-private-marker"
	store := session.NewStore(root)
	meta := session.SessionMetadata{SchemaVersion: 1, ID: id, CreatedAt: now, Workdir: root, Mode: session.ModeExec, Provider: "fixture", Model: marker, CompletionPolicy: session.CompletionPolicyInteractive}
	if err = store.Create(meta, session.State{Status: session.StatusFailed, Phase: "provider_call", LastError: "provider stopped because max output tokens were reached", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	requestID := id + ":1:main:1"
	prepare := events.New(id, "provider.request.prepared", "provider_call", map[string]any{"request_id": requestID, "turn": 1, "request_sequence": 1, "request_budget": map[string]any{"effective_max_output_tokens": 4096, "max_output_tokens_source": "adapter_default", "output_config_effort": "max"}, "private": marker})
	stop := events.New(id, "turn.stopped", "provider_call", map[string]any{"request_id": requestID, "stop_reason": "max_tokens", "usage": map[string]any{"reported": true, "input_tokens": 8, "output_tokens": 2}, "raw_provider": marker})
	if err = store.AppendEvents(id, []events.Event{prepare, stop}); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendProviderAttempt(id, session.ProviderAttempt{Turn: 1, Attempt: 1, Provider: "p", Model: "m", Outcome: "max_tokens_resume", Error: marker, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	projected := collect(root, nil)
	buf, err := encode(projected)
	if err != nil || bytes.Contains(buf, []byte(marker)) {
		t.Fatalf("unsafe projection: %v", err)
	}
	checkedReport, err := checked(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	if !checkedReport.Started || checkedReport.Category != "agent_failed" || len(checkedReport.Sessions) != 1 || len(checkedReport.Sessions[0].Requests) != 1 {
		t.Fatalf("missing evidence: %s", buf)
	}
	q := checkedReport.Sessions[0].Requests[0]
	if q.ID != requestID || q.Source != "adapter_default" || q.MaxOutputTokens == nil || *q.MaxOutputTokens != 4096 || q.Stop != "max_tokens" || q.Usage == nil || q.Usage.Output == nil || *q.Usage.Output != 2 || q.Usage.Reasoning != nil || checkedReport.Sessions[0].Attempts[0].Outcome != "max_tokens_resume" {
		t.Fatalf("incorrect evidence: %s", buf)
	}
	// Compaction completes its provider request without generating a main-turn stop.
	summaryID := id + ":2:semantic_summary:0"
	if err = store.AppendEvents(id, []events.Event{
		events.New(id, "provider.request.prepared", "compact", map[string]any{"request_id": summaryID, "turn": 2, "request_budget": map[string]any{"effective_max_output_tokens": 2048, "max_output_tokens_source": "semantic_summary"}}),
		events.New(id, "provider.request.completed", "compact", map[string]any{"request_id": summaryID, "stop_reason": "end_turn", "usage": map[string]any{"reported": true, "output_tokens": 13}}),
	}); err != nil {
		t.Fatal(err)
	}
	withSummary := collect(root, nil)
	summary := withSummary.Sessions[0].Requests[1]
	if summary.Stop != "end_turn" || summary.Usage == nil || summary.Usage.Output == nil || *summary.Usage.Output != 13 || summary.Source != "semantic_summary" {
		t.Fatalf("lost completed summary response: %+v", summary)
	}
	if data, err := encode(withSummary); err != nil {
		t.Fatal(err)
	} else if _, err := checked(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	// Task-provided unknown string fields disappear on re-encoding. Free text in a typed field is rejected.
	injected := strings.Replace(string(buf), `"schema_version":1`, `"extra":"`+marker+`","schema_version":1`, 1)
	scrubbed, err := checked(strings.NewReader(injected))
	if err != nil {
		t.Fatal(err)
	}
	safe, _ := json.Marshal(scrubbed)
	if bytes.Contains(safe, []byte(marker)) {
		t.Fatal("unknown field persisted")
	}
	if _, err = checked(strings.NewReader(strings.Replace(string(buf), `"max_tokens"`, `"`+marker+`"`, 1))); err == nil {
		t.Fatal("accepted untrusted stop text")
	}
	if err = os.WriteFile(filepath.Join(root, id, "events.jsonl"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	incomplete := collect(root, nil)
	if !incomplete.Started || !incomplete.Partial || incomplete.Category != "incomplete" {
		t.Fatalf("scored an incomplete trace: %+v", incomplete)
	}
}

func TestMissingSessionDoesNotClaimStart(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result := collect(root, nil)
	if result.Started || result.Category != "not_started" {
		t.Fatalf("bad empty report: %+v", result)
	}
}

func TestRootSessionEvidence(t *testing.T) {
	for _, test := range []struct {
		name     string
		statuses []string
		children int
		category string
	}{
		{"old root with many newer children", []string{"completed"}, 34, "completed"},
		{"two old roots with many newer children", []string{"completed", "cancelled"}, 34, "incomplete"},
		{"two roots with no children", []string{"cancelled", "completed"}, 0, "incomplete"},
		{"child without a published root", nil, 1, "incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			roots := map[string]bool{}
			parent := "19700101-000000-ffffff"
			for i := 0; i < len(test.statuses)+test.children; i++ {
				ts := time.Unix(int64(i), 0).UTC().Format(time.RFC3339)
				id := fmt.Sprintf("19700101-000000-%06x", i)
				meta := session.SessionMetadata{SchemaVersion: 1, ID: id, CreatedAt: ts, Workdir: dir, Mode: session.ModeExec, Provider: "fixture", Model: "fixture", CompletionPolicy: session.CompletionPolicyInteractive}
				status := session.StatusCompleted
				if i < len(test.statuses) {
					status = test.statuses[i]
					parent = meta.ID
					roots[meta.ID] = true
				} else {
					meta.ParentSessionID, meta.RootSessionID, meta.Depth = parent, parent, 1
				}
				if err := os.Mkdir(filepath.Join(dir, id), 0700); err != nil {
					t.Fatal(err)
				}
				for file, value := range map[string]any{"session.json": meta, "state.json": session.State{Status: status, Phase: "complete", UpdatedAt: ts}} {
					data, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, id, file), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			projected := collect(dir, nil)
			data, err := encode(projected)
			if err != nil {
				t.Fatal(err)
			}
			result, err := checked(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if result.Category != test.category || result.Started != (len(test.statuses) > 0) || !result.Partial {
				t.Fatalf("category=%s started=%v partial=%v", result.Category, result.Started, result.Partial)
			}
			if len(result.Sessions) != min(32, len(test.statuses)+test.children) {
				t.Fatalf("retained %d sessions", len(result.Sessions))
			}
			for _, row := range result.Sessions {
				if !row.Parent {
					delete(roots, row.ID)
				}
			}
			if len(roots) != 0 {
				t.Fatalf("omitted %d known roots", len(roots))
			}
			if len(test.statuses) > 1 {
				projected.Category = "completed"
				data, err := encode(projected)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := checked(bytes.NewReader(data)); err == nil {
					t.Fatal("accepted a scoring category for multiple roots")
				}
			}
		})
	}
}
