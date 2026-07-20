package webconsole

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

func TestServiceContextReportEndpointIsBoundedAndKeepsAggregate(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	const sessionID = "web-context-root"
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        "2026-07-20T08:00:00Z",
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fixture",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		RootSessionID:    sessionID,
	}
	if err := svc.store.Create(meta, session.State{Status: session.StatusCompleted, Phase: "complete", UpdatedAt: meta.CreatedAt}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	items := make([]events.Event, 0, 140)
	for i := 0; i < 70; i++ {
		requestID := fmt.Sprintf("%s:%d:main:0", sessionID, i+1)
		snapshot := session.RequestBudgetSnapshot{
			SchemaVersion:             session.RequestBudgetSnapshotSchemaVersion,
			RequestID:                 requestID,
			RequestKind:               "main",
			SessionID:                 sessionID,
			Turn:                      i + 1,
			Provider:                  "fake",
			Model:                     "fixture",
			WireEstimateSchemaVersion: 1,
			WireBodyBytes:             400 + i*4,
			EstimatedInputTokens:      100 + i,
			RequiredTokens:            120 + i,
			EffectiveWindowTokens:     1000,
			HeadroomTokens:            880 - i,
			CompactionAction:          "none",
			Fit:                       true,
		}
		at := time.Date(2026, 7, 20, 8, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		items = append(items,
			events.Event{SchemaVersion: events.SchemaVersion, ID: fmt.Sprintf("evt-web-prepared-%03d", i), SessionID: sessionID, Type: "provider.request.prepared", Time: at, Phase: "fixture", Data: map[string]any{
				"request_id": requestID, "request_kind": "main", "turn": i + 1, "request_sequence": 0,
				"request_budget": snapshot, "secret_metadata": "sk-WEB_CONTEXT_SENTINEL",
			}},
			events.Event{SchemaVersion: events.SchemaVersion, ID: fmt.Sprintf("evt-web-completed-%03d", i), SessionID: sessionID, Type: "provider.request.completed", Time: at, Phase: "fixture", Data: map[string]any{
				"request_id": requestID, "request_kind": "main", "turn": i + 1, "request_sequence": 0,
				"status": "completed", "usage": map[string]any{"reported": true, "input_tokens": i + 1, "output_tokens": 1},
			}},
		)
	}
	if err := svc.store.AppendEvents(sessionID, items); err != nil {
		t.Fatalf("append events: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/context", nil)
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("context endpoint status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var report session.ContextReport
	if err := json.Unmarshal(recorder.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v body=%s", err, recorder.Body.String())
	}
	if report.Aggregate.TotalRequestCount != 70 || report.Aggregate.TotalEstimatedInputTokens == 0 {
		t.Fatalf("aggregate must remain complete: %#v", report.Aggregate)
	}
	if len(report.Sessions) != 1 || len(report.Sessions[0].Requests) != webSafeMaxArrayItems {
		t.Fatalf("request detail must be bounded to %d: %#v", webSafeMaxArrayItems, report.Sessions)
	}
	if report.Truncation == nil || !report.Truncation.Truncated || report.Truncation.OmittedRequestCount != 6 {
		t.Fatalf("missing explicit truncation metadata: %#v", report.Truncation)
	}
	if strings.Contains(recorder.Body.String(), "WEB_CONTEXT_SENTINEL") {
		t.Fatalf("context endpoint leaked metadata value: %s", recorder.Body.String())
	}
}
