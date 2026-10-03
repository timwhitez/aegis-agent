package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/session"
	"aegis-agent/internal/webconsole"
	sdk "aegis-agent/pkg/agent"
)

func TestApprovalReceiptConcurrentCLIWebSDKAdmission(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprintf("different_request_ids=%v", alias), func(t *testing.T) {
			store, id, cfg, calls := cliReceiptFixture(t)
			snapshot, err := store.LoadApprovalSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			target := snapshot.Target()
			svc, err := webconsole.New(cfg, webconsole.Options{WorkerCount: 0})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Close)
			ids := []string{"shared-operation", "shared-operation", "shared-operation"}
			if alias {
				ids = []string{"cli-operation", "sdk-operation", "web-operation"}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			start := make(chan struct{})
			done := make(chan error, 3)
			go func() {
				<-start
				done <- Run(ctx, cliReceiptArgs(id, ids[0], target), io.Discard, io.Discard)
			}()
			go func() {
				<-start
				_, err := sdk.New(cfg).Continue(ctx, sdk.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: ids[1]})
				done <- err
			}()
			go func() {
				body, err := json.Marshal(map[string]any{"plan_mode_id": target.PlanModeID, "plan_version": target.PlanVersion, "expected_revision": target.ExpectedRevision, "approval_request_id": ids[2]})
				if err != nil {
					done <- err
					return
				}
				r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/sessions/"+id+"/planmode/approve", bytes.NewReader(body))
				r.Host = "127.0.0.1"
				r.Header.Set("X-Aegis-Agent-Web", "1")
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				<-start
				svc.ServeHTTP(w, r)
				if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
					done <- fmt.Errorf("Web admission/replay: %d %s", w.Code, w.Body)
					return
				}
				done <- nil
			}()
			close(start)
			// Drain every adapter before fixture cleanup restores process-wide loaders.
			for range 3 {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
			if t.Failed() {
				return
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				state, err := store.LoadState(id)
				if err != nil {
					t.Fatal(err)
				}
				if state.Status == session.StatusCompleted {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("admitted run did not complete: %#v", state)
				}
				time.Sleep(10 * time.Millisecond)
			}
			canonical, generation := "", ""
			for _, requestID := range ids {
				lookup, err := sdk.New(cfg).ApprovalReceipt(id, requestID)
				if err != nil || lookup.Receipt.Stage != session.ApprovalReceiptAdmitted {
					t.Fatalf("adapter has no admitted binding: %s %#v %v", requestID, lookup, err)
				}
				if canonical == "" {
					canonical, generation = lookup.Receipt.OperationID, lookup.Receipt.Recovery.RunGeneration
				} else if canonical != lookup.Receipt.OperationID || generation != lookup.Receipt.Recovery.RunGeneration {
					t.Fatal("adapters admitted different operations or generations")
				}
			}
			messages, err := store.LoadMessages(id)
			if err != nil {
				t.Fatal(err)
			}
			replays := 0
			for _, msg := range messages {
				if strings.TrimSpace(fmt.Sprint(msg.Meta["source"])) == "planmode_approval" {
					replays++
				}
			}
			if calls.Load() != 1 || replays != 1 || generation == "" {
				t.Fatalf("cross-surface duplicate execution: calls=%d replays=%d generation=%q", calls.Load(), replays, generation)
			}
			t.Logf("actual CLI/Web/SDK concurrent approval: canonical=%s generation=%s provider_calls=%d replay_messages=%d", canonical, generation, calls.Load(), replays)
		})
	}
}
