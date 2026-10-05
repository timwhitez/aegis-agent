package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/events"
	"aegis-agent/internal/session"
)

func blockMetadataOverrideEvents(t *testing.T, r *Runner, id string) func() {
	t.Helper()
	path := filepath.Join(r.store.SessionDir(id), "events.jsonl")
	saved := path + ".saved"
	if err := os.Rename(path, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(saved, path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMetadataOverrideEventFailureRestoresOrdinarySnapshot(t *testing.T) {
	on, off := true, false
	for _, original := range []*bool{&on, nil} {
		name := "true"
		if original == nil {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if _, ok := body["metadata"]; !ok {
					t.Error("failed override persisted into an omitted-flag resume")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"override-retry","status":"completed","output":[{"type":"function_call","call_id":"finish-retry","name":"finish","arguments":"{\"message\":\"done\"}"}]}`))
			}))
			defer server.Close()
			cfg := config.Default()
			cfg.Session.Dir = t.TempDir()
			pc := cfg.Providers[cfg.DefaultProvider]
			pc.BaseURL, pc.APIKeyEnv, pc.SendMetadata = server.URL+"/v1", "AEGIS_METADATA_FAILURE_TEST_KEY", &off
			cfg.Providers[cfg.DefaultProvider] = pc
			t.Setenv(pc.APIKeyEnv, "dummy-test-key")
			r := NewRunner(cfg)
			oldConfig := pc
			oldConfig.SendMetadata = original
			meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeExec, CompletionPolicy: session.CompletionPolicyAutonomous, Provider: cfg.DefaultProvider, Model: pc.Model, ProviderOptions: providerOptionsFromConfig(cfg.DefaultProvider, oldConfig)}
			if err := r.store.Create(meta, session.State{Status: session.StatusPaused, Phase: "paused"}); err != nil {
				t.Fatal(err)
			}
			if err := r.store.AppendEvent(meta.ID, events.New(meta.ID, "session.started", "prepare", map[string]any{"provider_options": meta.ProviderOptions})); err != nil {
				t.Fatal(err)
			}
			beforeEvents, err := r.store.LoadEvents(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			unblock := blockMetadataOverrideEvents(t, r, meta.ID)
			_, err = r.Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Message: "retry", ProviderOptions: session.ProviderOptions{SendMetadata: &off}})
			if err == nil || calls.Load() != 0 {
				t.Fatalf("event failure must stop before provider: %v, calls=%d", err, calls.Load())
			}
			after, err := r.store.LoadMetadata(meta.ID)
			if err != nil || !reflect.DeepEqual(after, meta) {
				t.Fatalf("failed override changed snapshot: %#v, %v", after, err)
			}
			state, err := r.store.LoadState(meta.ID)
			if err != nil || state.Status != session.StatusFailed {
				t.Fatalf("missing pre-run failure state: %#v, %v", state, err)
			}
			unblock()
			afterEvents, err := r.store.LoadEvents(meta.ID)
			if err != nil || !reflect.DeepEqual(afterEvents, beforeEvents) {
				t.Fatalf("changed original provenance: %#v, %v", afterEvents, err)
			}
			// A new process and current false config cannot adopt the failed override.
			if _, err := NewRunner(cfg).Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Message: "retry without override"}); err != nil || calls.Load() != 1 {
				t.Fatalf("omitted-flag recovery failed: %v, calls=%d", err, calls.Load())
			}
		})
	}
}

func TestMetadataOverrideEventFailureRetainsApprovalRecovery(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	meta, err := r.store.LoadMetadata(id)
	if err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	pc, _ := r.cfg.ProviderConfig(meta.Provider)
	meta.ProviderOptions = providerOptionsFromConfig(meta.Provider, pc)
	meta.ProviderOptions.SendMetadata = &on
	if err := r.store.SaveMetadata(id, meta); err != nil {
		t.Fatal(err)
	}
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "metadata-event-recovery", ProviderOptions: session.ProviderOptions{SendMetadata: &off}}
	unblock := blockMetadataOverrideEvents(t, r, id)
	failed, err := r.PrepareApprovalOperation(context.Background(), req)
	if err == nil || failed.Prepared != nil || calls.Load() != 0 {
		t.Fatalf("unexpected failed preparation: %#v, %v, calls=%d", failed, err, calls.Load())
	}
	unblock()
	recovered, err := r.PrepareApprovalOperation(context.Background(), req)
	if err != nil || recovered.Prepared == nil || calls.Load() != 0 {
		t.Fatalf("receipt recovery failed: %#v, %v, calls=%d", recovered, err, calls.Load())
	}
	facts, err := r.store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	originalFacts := 0
	for _, fact := range facts {
		if fact.Type == "session.provider_options.overridden" && fact.Data["previous"] == true && fact.Data["effective"] == false {
			originalFacts++
		}
	}
	if originalFacts != 1 {
		t.Fatalf("lost or duplicated original override fact: %#v", facts)
	}
}
