package session

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func terminalReaderFixture(t *testing.T) (*Store, *Store, QueueJob, QueueJob) {
	t.Helper()
	reader := NewStore(t.TempDir())
	owner := NewStore(reader.root)
	parent := reaperParentMeta(t, owner)
	job := QueueJob{SchemaVersion: 1, ID: "job_reader_terminal", Status: QueueStatusRunning, ParentSessionID: parent.ID, RootSessionID: parent.ID, SessionID: "child_reader_terminal", SessionStatus: StatusRunning, Prompt: "offline", Mode: ModeExec, Background: true}
	seedParentJob(t, owner, parent.ID, job)
	old, err := owner.LoadJobCoordinationSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	job.Status, job.SessionStatus, job.FinalText = QueueStatusCompleted, StatusCompleted, "new canonical result"
	if err := owner.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	latest, err := owner.LoadJobCoordinationSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return reader, owner, old, latest
}

func saveTerminalReaderJob(t *testing.T, store *Store, job QueueJob) QueueJob {
	t.Helper()
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	current, err := store.LoadJobCoordinationSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func TestQueueReaderPublicConflictKeepsLatestSnapshot(t *testing.T) {
	for _, method := range []string{"LoadJob", "ListJobs"} {
		t.Run(method, func(t *testing.T) {
			reader, owner, old, latest := terminalReaderFixture(t)
			saveTerminalReaderJob(t, owner, old)
			checks := 0
			reader.beforeQueueJobRepairCheck = func(_ QueueJob, repaired *QueueJob) {
				if repaired == nil {
					checks++
					if checks == 1 {
						latest = saveTerminalReaderJob(t, owner, latest)
					}
				}
			}
			var got QueueJob
			var err error
			if method == "LoadJob" {
				got, err = reader.LoadJob(old.ID)
			} else {
				var jobs []QueueJob
				jobs, err = reader.ListJobs(-1)
				if len(jobs) != 1 {
					t.Fatalf("jobs=%#v err=%v", jobs, err)
				}
				got = jobs[0]
			}
			if err != nil || checks != 2 || !reflect.DeepEqual(got, latest) {
				t.Fatalf("full reader discarded refreshed snapshot: got=%#v latest=%#v checks=%d err=%v", got, latest, checks, err)
			}
			notifications, err := owner.LoadBackgroundNotifications(latest.ParentSessionID)
			if err != nil || len(notifications) != 1 || notifications[0].FinalText != latest.FinalText {
				t.Fatalf("terminal full read missing latest notification: %#v err=%v", notifications, err)
			}
			notifications[0].DeliveryStatus = BackgroundNotificationAccepted
			if err := owner.UpdateBackgroundNotifications(latest.ParentSessionID, notifications); err != nil {
				t.Fatal(err)
			}
			before, err := owner.LoadBackgroundNotifications(latest.ParentSessionID)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, _, err := reader.reconcileQueueJobSession(old); err != nil {
					t.Fatal(err)
				}
			}
			after, err := owner.LoadBackgroundNotifications(latest.ParentSessionID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("unchanged delivered facts were redelivered: before=%#v after=%#v err=%v", before, after, err)
			}
			events, err := owner.LoadEvents(latest.ParentSessionID)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, event := range events {
				if event.Data["job_id"] == latest.ID {
					counts[event.Type]++
				}
			}
			if counts["queue.job.notified"] != 1 || counts["queue.job.completed"] != 1 {
				t.Fatalf("terminal retry duplicated lifecycle facts: %#v", counts)
			}
		})
	}
}

func TestQueueReaderLatePublicationConflictRepairsNewTerminal(t *testing.T) {
	for _, branch := range []string{"stale-no-child", "reclaimed-running-child", "failed-queue-error", "completed-child"} {
		t.Run(branch, func(t *testing.T) {
			reader, owner, old, latest := terminalReaderFixture(t)
			var childMeta SessionMetadata
			if branch != "stale-no-child" {
				childMeta = SessionMetadata{SchemaVersion: 1, ID: old.SessionID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: ModeExec, Provider: "fake", Model: "fake", ParentSessionID: old.ParentSessionID, RootSessionID: old.ParentSessionID, QueueJobID: old.ID, Depth: 1, CompletionPolicy: CompletionPolicyAutonomous}
				state := State{Status: StatusCompleted, UpdatedAt: childMeta.CreatedAt, LastAssistantExcerpt: "previous child result"}
				if branch == "reclaimed-running-child" {
					state.Status = StatusRunning
					old.Status, old.LastError = QueueStatusBlocked, "queue lease reclaimed: owner process exited"
				}
				if branch == "failed-queue-error" {
					old.Status, old.LastError = QueueStatusFailed, "failed output handoff"
				}
				if err := owner.Create(childMeta, state); err != nil {
					t.Fatal(err)
				}
			}
			old = saveTerminalReaderJob(t, owner, old)
			if branch == "stale-no-child" {
				old.UpdatedAt = time.Now().UTC().Add(-2 * queueRunningStaleAfter).Format(time.RFC3339Nano)
				if err := owner.writeJSONFile(owner.queueJobPath(old.Status, old.ID), old); err != nil {
					t.Fatal(err)
				}
			}
			published := false
			reader.beforeQueueJobRepairCheck = func(_ QueueJob, repaired *QueueJob) {
				if repaired == nil || published {
					return
				}
				published = true
				if branch != "stale-no-child" {
					state := State{Status: StatusCompleted, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), LastAssistantExcerpt: latest.FinalText}
					if branch == "completed-child" {
						childMeta.ID = "child_new_reader_terminal"
						if err := owner.Create(childMeta, state); err != nil {
							t.Fatal(err)
						}
					} else if err := owner.SaveState(childMeta.ID, state); err != nil {
						t.Fatal(err)
					}
					syncRunningQueueJobSession(&latest, childMeta, state)
				}
				latest = saveTerminalReaderJob(t, owner, latest)
			}
			got, err := reader.LoadJob(old.ID)
			if err != nil || !published || !reflect.DeepEqual(got, latest) {
				t.Fatalf("late conflict did not reconcile winning result: got=%#v latest=%#v published=%t err=%v", got, latest, published, err)
			}
			notifications, err := owner.LoadBackgroundNotifications(latest.ParentSessionID)
			if err != nil || len(notifications) != 1 || notifications[0].QueueJobID != latest.ID || notifications[0].SessionID != latest.SessionID || notifications[0].Status != QueueStatusCompleted || notifications[0].FinalText != latest.FinalText || notifications[0].LastError != "" {
				t.Fatalf("late conflict notified stale attempt: %#v err=%v", notifications, err)
			}
		})
	}
}

func TestQueueReaderConflictPreservesNewRunningOwner(t *testing.T) {
	for _, ownerID := range []string{queueProcessStartID, "foreign-process"} {
		t.Run(ownerID, func(t *testing.T) {
			reader, owner, _, terminal := terminalReaderFixture(t)
			current := terminal
			current.Status, current.SessionStatus, current.FinalText = QueueStatusRunning, StatusRunning, ""
			meta := SessionMetadata{SchemaVersion: 1, ID: current.SessionID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: ModeExec, Provider: "fake", Model: "fake", ParentSessionID: current.ParentSessionID, RootSessionID: current.ParentSessionID, QueueJobID: current.ID, Depth: 1, CompletionPolicy: CompletionPolicyAutonomous}
			if err := owner.Create(meta, State{Status: StatusRunning, UpdatedAt: meta.CreatedAt}); err != nil {
				t.Fatal(err)
			}
			current.EffectiveWorkdir = meta.Workdir
			applyQueueLease(&current, time.Now().UTC().Format(time.RFC3339Nano))
			current.ProcessStartID = ownerID
			current = saveTerminalReaderJob(t, owner, current)
			got, changed, err := reader.reconcileQueueJobSession(terminal)
			after, loadErr := owner.LoadJobCoordinationSnapshot(current.ID)
			notifications, notificationErr := owner.LoadBackgroundNotifications(current.ParentSessionID)
			if err != nil || !changed || !reflect.DeepEqual(got, current) || loadErr != nil || !reflect.DeepEqual(after, current) || notificationErr != nil || len(notifications) != 0 {
				t.Fatalf("stale terminal reader affected latest owner: got=%#v current=%#v after=%#v changed=%t errors=%v/%v/%v notifications=%#v", got, current, after, changed, err, loadErr, notificationErr, notifications)
			}
		})
	}
}

func TestQueueReaderConflictPreservesPendingResumeClaim(t *testing.T) {
	reader, owner, _, latest := terminalReaderFixture(t)
	meta := SessionMetadata{SchemaVersion: 1, ID: latest.SessionID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: ModeExec, Provider: "fake", Model: "fake", ParentSessionID: latest.ParentSessionID, RootSessionID: latest.ParentSessionID, QueueJobID: latest.ID, Depth: 1, CompletionPolicy: CompletionPolicyAutonomous}
	state := State{Status: StatusPaused, UpdatedAt: meta.CreatedAt, PauseReason: "child_budget_turns_exceeded"}
	if err := owner.Create(meta, state); err != nil {
		t.Fatal(err)
	}
	latest.Status, latest.SessionStatus, latest.FinalText = QueueStatusBlocked, StatusPaused, ""
	old := saveTerminalReaderJob(t, owner, latest)
	if _, acquired, err := owner.AcquireQueueChildResumeSlot(meta.ParentSessionID, old.ID, meta.ID, 0); err != nil || !acquired {
		t.Fatalf("actual resume claim: acquired=%t err=%v", acquired, err)
	}
	claimed, err := owner.LoadJobCoordinationSnapshot(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, changed, err := reader.reconcileQueueJobSession(old)
	after, loadErr := owner.LoadJobCoordinationSnapshot(old.ID)
	childState, stateErr := owner.LoadState(meta.ID)
	notifications, notificationErr := owner.LoadBackgroundNotifications(meta.ParentSessionID)
	if err != nil || !changed || !reflect.DeepEqual(got, claimed) || loadErr != nil || !reflect.DeepEqual(after, claimed) || stateErr != nil || !reflect.DeepEqual(childState, state) || notificationErr != nil || len(notifications) != 0 {
		t.Fatalf("fresh retry settled reserved resume: got=%#v claimed=%#v after=%#v child=%#v changed=%t errors=%v/%v/%v/%v notifications=%#v", got, claimed, after, childState, changed, err, loadErr, stateErr, notificationErr, notifications)
	}
}

func TestQueueReaderConflictTracksRealHeartbeatVersion(t *testing.T) {
	reader, owner, _, latest := terminalReaderFixture(t)
	latest.Status, latest.SessionStatus, latest.FinalText = QueueStatusRunning, StatusRunning, ""
	applyQueueLease(&latest, time.Now().UTC().Format(time.RFC3339Nano))
	latest = saveTerminalReaderJob(t, owner, latest)
	checks := 0
	reader.beforeQueueJobRepairCheck = func(_ QueueJob, repaired *QueueJob) {
		if repaired != nil {
			t.Fatal("recent no-child running lease should not be repaired")
		}
		checks++
		if checks == 1 {
			var active bool
			var err error
			latest, active, err = owner.RefreshQueueJobLease(latest.ID)
			if err != nil || !active {
				t.Fatalf("actual heartbeat: active=%t err=%v", active, err)
			}
		}
	}
	got, err := reader.LoadJob(latest.ID)
	if err != nil || checks != 2 || !reflect.DeepEqual(got, latest) {
		t.Fatalf("reader discarded heartbeat version: got=%#v latest=%#v checks=%d err=%v", got, latest, checks, err)
	}
}

func TestQueueReaderConflictRetriesAreBounded(t *testing.T) {
	reader, owner, _, latest := terminalReaderFixture(t)
	checks := 0
	reader.beforeQueueJobRepairCheck = func(expected QueueJob, repaired *QueueJob) {
		if repaired != nil {
			t.Fatal("continuously superseded attempt reached repair publication")
		}
		checks++
		expected.FinalText = "new version " + strings.Repeat("x", checks)
		latest = saveTerminalReaderJob(t, owner, expected)
	}
	_, err := reader.LoadJob(latest.ID)
	if !errors.Is(err, errQueueJobRepairConflict) || checks != queueJobReconcileAttempts || !strings.Contains(err.Error(), "retry the read") {
		t.Fatalf("continuous updates were hidden or unbounded: checks=%d err=%v", checks, err)
	}
	notifications, notificationErr := owner.LoadBackgroundNotifications(latest.ParentSessionID)
	if notificationErr != nil || len(notifications) != 0 {
		t.Fatalf("failed attempts performed stale parent effects: %#v err=%v", notifications, notificationErr)
	}
	got, err := owner.LoadJob(latest.ID)
	if err != nil || got.FinalText != latest.FinalText {
		t.Fatalf("fresh read could not recover: %#v err=%v", got, err)
	}
}

func TestQueueReaderConflictRepairsLatestTerminalBeforeSuccess(t *testing.T) {
	reader, owner, old, latest := terminalReaderFixture(t)
	got, changed, err := reader.reconcileQueueJobSession(old)
	if err != nil || !changed || got.Status != latest.Status || got.FinalText != latest.FinalText || got.UpdatedAt != latest.UpdatedAt {
		t.Fatalf("latest terminal reconciliation: got=%#v changed=%t err=%v", got, changed, err)
	}
	notifications, err := owner.LoadBackgroundNotifications(latest.ParentSessionID)
	if err != nil || len(notifications) != 1 || notifications[0].QueueJobID != latest.ID || notifications[0].FinalText != latest.FinalText {
		t.Fatalf("successful terminal reader returned before notification repair: %#v err=%v", notifications, err)
	}
	coordination, err := owner.LoadParentCoordination(latest.ParentSessionID)
	if err != nil || len(coordination.UnresolvedQueueJobs) != 0 {
		t.Fatalf("successful terminal reader did not settle parent: %#v err=%v", coordination, err)
	}
}

func TestQueueReaderConflictPropagatesLatestTerminalPersistenceFailure(t *testing.T) {
	for _, name := range []string{"background.jsonl", "events.jsonl"} {
		t.Run(name, func(t *testing.T) {
			reader, owner, old, latest := terminalReaderFixture(t)
			var broken string
			if name == "background.jsonl" {
				broken = filepath.Join(owner.SessionDir(latest.ParentSessionID), "control", name)
			} else {
				broken = filepath.Join(owner.SessionDir(latest.ParentSessionID), name)
			}
			if err := os.Remove(broken); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(broken, 0o700); err != nil {
				t.Fatal(err)
			}
			_, _, err := reader.reconcileQueueJobSession(old)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("reader concealed latest terminal persistence failure: %v", err)
			}
			canonical, loadErr := owner.LoadJobCoordinationSnapshot(latest.ID)
			if loadErr != nil || canonical.Status != latest.Status || canonical.FinalText != latest.FinalText || canonical.UpdatedAt != latest.UpdatedAt {
				t.Fatalf("failed repair changed canonical result: %#v err=%v", canonical, loadErr)
			}
		})
	}
}
