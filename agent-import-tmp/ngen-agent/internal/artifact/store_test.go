package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ngen/internal/task"
)

func TestListTaskIDsSkipsUnmaterializedTaskDirectories(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, ".ngen")
	if err := store.EnsureWorkspaceLayout(); err != nil {
		t.Fatalf("ensure workspace layout: %v", err)
	}

	ready := task.Spec{TaskID: "TASK-ready", Title: "ready"}
	if err := store.SaveTask(ready); err != nil {
		t.Fatalf("save ready task: %v", err)
	}
	if err := store.SaveState(task.NewInitialState(ready)); err != nil {
		t.Fatalf("save ready state: %v", err)
	}
	if err := os.MkdirAll(store.TaskRoot("TASK-pending"), 0o755); err != nil {
		t.Fatalf("mkdir pending task root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.TaskRoot("TASK-pending"), "plan.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write pending plan: %v", err)
	}
	taskOnly := task.Spec{TaskID: "TASK-task-only", Title: "task only"}
	if err := store.SaveTask(taskOnly); err != nil {
		t.Fatalf("save task-only task: %v", err)
	}

	ids, err := store.ListTaskIDs()
	if err != nil {
		t.Fatalf("list task ids: %v", err)
	}
	if len(ids) != 1 || ids[0] != ready.TaskID {
		t.Fatalf("expected only materialized task ids, got %+v", ids)
	}
}

func TestStoreUsesConfiguredMemoryMarkdownFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, ".ngen")
	store.MemoryFile = "docs/MEMORY.md"
	if err := store.EnsureWorkspaceLayout(); err != nil {
		t.Fatalf("ensure workspace layout: %v", err)
	}
	if err := store.SaveMemoryMarkdown([]byte("# Memory\n")); err != nil {
		t.Fatalf("save memory markdown: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "docs", "MEMORY.md"))
	if err != nil {
		t.Fatalf("read configured memory file: %v", err)
	}
	if string(got) != "# Memory\n" {
		t.Fatalf("unexpected memory markdown: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ngen", "memory", "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("default memory file should not be written when memory.file is configured, stat err=%v", err)
	}
	if ref := store.MemoryMarkdownRef(); ref != "workspace:docs/MEMORY.md" {
		t.Fatalf("unexpected memory ref: %s", ref)
	}
}

func TestLoadStateRetriesTransientNotExist(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, ".ngen")
	if err := store.EnsureWorkspaceLayout(); err != nil {
		t.Fatalf("ensure workspace layout: %v", err)
	}

	spec := task.Spec{TaskID: "TASK-retry", Title: "retry"}
	if err := store.EnsureTaskLayout(spec.TaskID); err != nil {
		t.Fatalf("ensure task layout: %v", err)
	}
	want := task.NewInitialState(spec)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(20 * time.Millisecond)
		if err := store.SaveState(want); err != nil {
			t.Errorf("save state after delay: %v", err)
		}
	}()

	got, err := store.LoadState(spec.TaskID)
	<-done
	if err != nil {
		t.Fatalf("load state with retry: %v", err)
	}
	if got.TaskID != want.TaskID || got.Phase != want.Phase || got.State != want.State {
		t.Fatalf("unexpected loaded state: got %+v want %+v", got, want)
	}
}

func TestStoreRoundTripsMissionAssertionArtifacts(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, ".ngen")
	if err := store.EnsureWorkspaceLayout(); err != nil {
		t.Fatalf("ensure workspace layout: %v", err)
	}
	if err := store.EnsureMissionLayout("MIS-roundtrip"); err != nil {
		t.Fatalf("ensure mission layout: %v", err)
	}

	contract := task.MissionValidationContract{
		ObjectKind:    "mission_validation_contract",
		SchemaVersion: task.SchemaVersion,
		MissionID:     "MIS-roundtrip",
		ContractID:    "MCON-roundtrip",
		Assertions: []task.MissionContractAssertion{
			{
				AssertionID:      "ASSERT-001",
				Kind:             "acceptance",
				Statement:        "round trip assertion survives",
				EvidenceRequired: []string{"verification/latest.json"},
				Validator:        "deterministic_artifact",
				NegativeCase:     "do not close without evidence",
				ManualCheck:      "inspect evidence refs",
			},
		},
		AcceptanceTests: []string{"round trip assertion survives"},
		CreatedAt:       task.Now(),
		UpdatedAt:       task.Now(),
	}
	if err := store.SaveMissionValidationContract(contract); err != nil {
		t.Fatalf("save contract: %v", err)
	}
	features := task.MissionFeatureSet{
		ObjectKind:    "mission_features",
		SchemaVersion: task.SchemaVersion,
		MissionID:     "MIS-roundtrip",
		Features: []task.MissionFeature{
			{
				FeatureID:        "FEAT-001",
				Title:            "Round trip",
				ContractCoverage: []string{"ASSERT-001"},
				EvidenceRefs:     []string{"workspace:.ngen/tasks/TASK-roundtrip/verification/latest.json"},
				UpdatedAt:        task.Now(),
			},
		},
		UpdatedAt: task.Now(),
	}
	if err := store.SaveMissionFeatures(features); err != nil {
		t.Fatalf("save features: %v", err)
	}
	milestones := task.MissionMilestoneSet{
		ObjectKind:       "mission_milestones",
		SchemaVersion:    task.SchemaVersion,
		MissionID:        "MIS-roundtrip",
		CurrentFeatureID: "FEAT-001",
		ReadyFeatureIDs:  []string{"FEAT-001"},
		Milestones: []task.MissionMilestone{
			{
				MilestoneID:      "MS-001",
				FeatureIDs:       []string{"FEAT-001"},
				ContractCoverage: []string{"ASSERT-001"},
				EvidenceRefs:     []string{"workspace:.ngen/tasks/TASK-roundtrip/reviews/latest.json"},
				UpdatedAt:        task.Now(),
			},
		},
		UpdatedAt: task.Now(),
	}
	if err := store.SaveMissionMilestones(milestones); err != nil {
		t.Fatalf("save milestones: %v", err)
	}

	gotContract, err := store.LoadMissionValidationContract("MIS-roundtrip")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	if len(gotContract.Assertions) != 1 || gotContract.Assertions[0].AssertionID != "ASSERT-001" || gotContract.Assertions[0].NegativeCase == "" || gotContract.Assertions[0].ManualCheck == "" {
		t.Fatalf("assertion fields did not round trip: %+v", gotContract.Assertions)
	}
	gotFeatures, err := store.LoadMissionFeatures("MIS-roundtrip")
	if err != nil {
		t.Fatalf("load features: %v", err)
	}
	if len(gotFeatures.Features) != 1 || strings.Join(gotFeatures.Features[0].ContractCoverage, ",") != "ASSERT-001" {
		t.Fatalf("feature assertion coverage did not round trip: %+v", gotFeatures.Features)
	}
	gotMilestones, err := store.LoadMissionMilestones("MIS-roundtrip")
	if err != nil {
		t.Fatalf("load milestones: %v", err)
	}
	if gotMilestones.CurrentFeatureID != "FEAT-001" || strings.Join(gotMilestones.Milestones[0].ContractCoverage, ",") != "ASSERT-001" {
		t.Fatalf("milestone scheduler or coverage did not round trip: %+v", gotMilestones)
	}
}

func TestStoreRejectsUnsafeArtifactIDs(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, ".ngen")
	if err := store.EnsureWorkspaceLayout(); err != nil {
		t.Fatalf("ensure workspace layout: %v", err)
	}

	checks := map[string]func() error{
		"load task traversal": func() error {
			_, err := store.LoadTask("../outside")
			return err
		},
		"load task leading space": func() error {
			_, err := store.LoadTask(" TASK-001")
			return err
		},
		"save session traversal": func() error {
			return store.SaveSession(task.Session{SessionID: "../outside"})
		},
		"save session trailing space": func() error {
			return store.SaveSession(task.Session{SessionID: "SESSION-001 "})
		},
		"append session message traversal": func() error {
			return store.AppendSessionMessage(task.SessionMessage{SessionID: "../outside"})
		},
		"save worker traversal": func() error {
			return store.SaveWorkerContract(task.WorkerContract{ParentTaskID: "TASK-001", WorkerID: "../outside"})
		},
		"save checkpoint traversal": func() error {
			return store.SaveCheckpoint(task.Checkpoint{TaskID: "TASK-001", CheckpointID: "../outside"})
		},
		"load watch suffix injection": func() error {
			_, err := store.LoadWatch("WATCH-001.json")
			return err
		},
	}
	for name, run := range checks {
		err := run()
		if err == nil {
			t.Fatalf("%s: expected unsafe artifact id to be rejected", name)
		}
		if !strings.Contains(err.Error(), "path segment") && !strings.Contains(err.Error(), "separator") && !strings.Contains(err.Error(), "suffix") {
			t.Fatalf("%s: expected artifact segment diagnostic, got %v", name, err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "outside")); !os.IsNotExist(err) {
		t.Fatalf("unsafe IDs should not create or read outside state root, stat err=%v", err)
	}
}
