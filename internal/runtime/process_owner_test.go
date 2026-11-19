package runtime

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/events"
	"aegis-agent/internal/session"
)

func newRunOwnerTestRunner(t *testing.T, handler http.HandlerFunc) (*Runner, session.SessionMetadata, session.State) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider: "openai-compatible",
		APIKeyEnv:   "OPENAI_API_KEY",
		BaseURL:     server.URL + "/v1",
		Model:       "gpt-5.4",
		TimeoutSec:  30,
		WireAPI:     "responses",
		Retry:       config.Retry{MaxAttempts: 1},
	}
	cfg.Runtime.ProviderAutoResume.Enabled = false
	t.Setenv("OPENAI_API_KEY", "test-key")

	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeRun),
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return runner, meta, state
}

func runOwnerEventsOfType(items []events.Event, eventType string) []events.Event {
	out := make([]events.Event, 0, 1)
	for _, item := range items {
		if item.Type == eventType {
			out = append(out, item)
		}
	}
	return out
}

func eventIndexOfType(items []events.Event, eventType string) int {
	for i, item := range items {
		if item.Type == eventType {
			return i
		}
	}
	return -1
}

func assertRunOwnerFields(t *testing.T, evt events.Event) {
	t.Helper()
	owner := CurrentProcessOwner()
	if got := eventString(evt.Data, "source"); got != runOwnerEventSource {
		t.Fatalf("expected runtime owner source, got %q in %#v", got, evt.Data)
	}
	if got := eventInt(evt.Data, "pid"); got != os.Getpid() {
		t.Fatalf("expected owner pid %d, got %d", os.Getpid(), got)
	}
	if got := eventString(evt.Data, "process_start_id"); got != owner.ProcessStartID {
		t.Fatalf("expected owner process_start_id %q, got %q", owner.ProcessStartID, got)
	}
	startedAt := eventString(evt.Data, "started_at")
	if _, err := time.Parse(time.RFC3339Nano, startedAt); err != nil {
		t.Fatalf("owner started_at must be RFC3339Nano: %v", err)
	}
	if want := strconv.Itoa(os.Getpid()) + ":" + startedAt; eventString(evt.Data, "process_start_id") != want {
		t.Fatalf("expected process_start_id %q, got %q", want, eventString(evt.Data, "process_start_id"))
	}
}

func TestRunExistingRecordsDurableRunOwnerEvents(t *testing.T) {
	runner, meta, state := newRunOwnerTestRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"id\":\"resp_run_owner\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}"))
	})

	if _, err := runner.runExisting(context.Background(), meta, state, "", nil); err != nil {
		t.Fatalf("runExisting: %v", err)
	}

	eventsList, err := runner.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	acquired := runOwnerEventsOfType(eventsList, RunOwnerAcquiredEventType)
	released := runOwnerEventsOfType(eventsList, RunOwnerReleasedEventType)
	if len(acquired) != 1 || len(released) != 1 {
		t.Fatalf("expected one acquire and one release owner event, got acquired=%d released=%d", len(acquired), len(released))
	}
	assertRunOwnerFields(t, acquired[0])

	startedIndex := eventIndexOfType(eventsList, "session.started")
	acquiredIndex := eventIndexOfType(eventsList, RunOwnerAcquiredEventType)
	releasedIndex := eventIndexOfType(eventsList, RunOwnerReleasedEventType)
	if startedIndex < 0 || acquiredIndex <= startedIndex || releasedIndex <= acquiredIndex {
		t.Fatalf("expected session.started < run.owner.acquired < run.owner.released, got %d/%d/%d", startedIndex, acquiredIndex, releasedIndex)
	}
	if releasedIndex != len(eventsList)-1 {
		t.Fatalf("expected run.owner.released to be the final event, got index %d of %d", releasedIndex, len(eventsList))
	}

	assertRunOwnerFields(t, released[0])
	releasedAt := eventString(released[0].Data, "released_at")
	if releasedAt == "" {
		t.Fatalf("expected released_at on %s, got %#v", RunOwnerReleasedEventType, released[0].Data)
	}
	if _, err := time.Parse(time.RFC3339Nano, releasedAt); err != nil {
		t.Fatalf("owner released_at must be RFC3339Nano: %v", err)
	}
}

func TestRunExistingRecordsRunOwnerReleaseAfterEngineFailure(t *testing.T) {
	runner, meta, state := newRunOwnerTestRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("{\"error\":{\"message\":\"bad request\"}}"))
	})

	if _, err := runner.runExisting(context.Background(), meta, state, "", nil); err == nil {
		t.Fatal("expected engine failure for a rejected provider request")
	}

	eventsList, err := runner.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	acquired := runOwnerEventsOfType(eventsList, RunOwnerAcquiredEventType)
	released := runOwnerEventsOfType(eventsList, RunOwnerReleasedEventType)
	if len(acquired) != 1 || len(released) != 1 {
		t.Fatalf("failed run must still record both owner events, got acquired=%d released=%d", len(acquired), len(released))
	}
	assertRunOwnerFields(t, released[0])
	if eventString(released[0].Data, "released_at") == "" {
		t.Fatalf("expected released_at on the failure path, got %#v", released[0].Data)
	}
	if eventIndexOfType(eventsList, RunOwnerReleasedEventType) != len(eventsList)-1 {
		t.Fatalf("expected run.owner.released to be the final event after failure, got %#v", eventsList)
	}
}

// TestRunOwnerEventsCoverEveryEngineRunCallSite guards the funnel invariant:
// every run path (start, continue, approval resume, delegated child, queue
// worker) must reach the engine through runExisting, which is where the durable
// owner facts are written.
func TestRunOwnerEventsCoverEveryEngineRunCallSite(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	callSites := map[string]int{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Run" {
					return true
				}
				switch receiver := sel.X.(type) {
				case *ast.SelectorExpr:
					if receiver.Sel.Name == "engine" {
						callSites[fn.Name.Name]++
					}
				case *ast.Ident:
					if receiver.Name == "engine" {
						callSites[fn.Name.Name]++
					}
				}
				return true
			})
		}
	}
	if len(callSites) != 1 || callSites["runExisting"] != 1 {
		t.Fatalf("expected exactly one engine.Run call site inside runExisting, got %#v", callSites)
	}
}
