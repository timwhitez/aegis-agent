package quota

import (
	"strings"
	"testing"
)

func TestResolveUsesDefaultWhenQuotaIsOmitted(t *testing.T) {
	got, err := Resolve(0, 250)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != 250 {
		t.Fatalf("expected default quota 250, got %d", got)
	}
}

func TestResolveRejectsNegativeQuota(t *testing.T) {
	_, err := Resolve(-1, 250)
	if err == nil {
		t.Fatal("expected negative quota to fail")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Fatalf("expected positive quota error, got %v", err)
	}
}
