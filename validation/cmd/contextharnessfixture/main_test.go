package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHarnessFixtureIsDeterministicAndSeparatesRootFromTotal(t *testing.T) {
	first, err := buildHarnessFixture()
	if err != nil {
		t.Fatalf("build first fixture: %v", err)
	}
	second, err := buildHarnessFixture()
	if err != nil {
		t.Fatalf("build second fixture: %v", err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first fixture: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second fixture: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("fixture output is not deterministic:\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}
	if first.SchemaVersion != harnessFixtureSchemaVersion || len(first.Scenarios) != 3 {
		t.Fatalf("unexpected fixture schema: %#v", first)
	}
	wantNames := []string{"single_root_broad", "single_root_narrowed", "delegated_explorer"}
	for index, want := range wantNames {
		if first.Scenarios[index].Name != want {
			t.Fatalf("scenario %d name=%q, want %q", index, first.Scenarios[index].Name, want)
		}
	}
	if !first.Comparison.NarrowedRootPeakLTEBroad || !first.Comparison.DelegatedRootPeakLTBroad || !first.Comparison.DelegatedChildAggregateNonZero || !first.Comparison.DelegatedTotalSeparatelyVisible {
		t.Fatalf("fixture relationships failed: %#v", first.Comparison)
	}
	if first.Comparison.DelegatedRootAggregate <= first.Comparison.DelegatedRootPeak {
		t.Fatalf("fixture must prove total uses root aggregate rather than root peak: %#v", first.Comparison)
	}
	if first.Comparison.DelegatedTotal != first.Comparison.DelegatedRootAggregate+first.Comparison.DelegatedChildAggregate {
		t.Fatalf("delegated total does not reconcile to root and child aggregates: %#v", first.Comparison)
	}
	encoded := string(firstJSON)
	for _, forbidden := range []string{"FIXTURE_PROMPT_SENTINEL", "FIXTURE_TOOL_SENTINEL", "sk-FIXTURE_SECRET"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("fixture report leaked %q: %s", forbidden, encoded)
		}
	}
}
