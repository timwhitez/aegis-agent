package quota

import "testing"

func TestRemainingUsesLimitMinusUsed(t *testing.T) {
	window := Window{Limit: 20, Used: 7}
	if got := Remaining(window); got != 13 {
		t.Fatalf("expected remaining 13, got %d", got)
	}
}

func TestUtilizationPercentPreservesPartialUsage(t *testing.T) {
	window := Window{Limit: 20, Used: 7}
	if got := UtilizationPercent(window); got != 35 {
		t.Fatalf("expected utilization 35, got %d", got)
	}
}

func TestSummaryMatchesDerivedValues(t *testing.T) {
	window := Window{Limit: 16, Used: 4}
	got := Summary(window)
	want := map[string]int{
		"remaining":           12,
		"utilization_percent": 25,
	}
	if got["remaining"] != want["remaining"] || got["utilization_percent"] != want["utilization_percent"] {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
}
