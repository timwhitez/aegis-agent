package runtime

import (
	"reflect"
	"testing"

	"aegis-agent/internal/config"
)

func TestRuntimeFacadesUseDistinctConcreteTypes(t *testing.T) {
	cfg := config.Default()
	core := NewCoreRunner(cfg)
	experimental := NewExperimentalRunner(cfg)
	store := NewStoreView(cfg)
	if reflect.TypeOf(core) == reflect.TypeOf(experimental) {
		t.Fatalf("expected distinct core and experimental facade types, got %T", core)
	}
	if reflect.TypeOf(core) == reflect.TypeOf(store) {
		t.Fatalf("expected distinct core and store facade types, got %T", store)
	}
	if reflect.TypeOf(experimental) == reflect.TypeOf(store) {
		t.Fatalf("expected distinct experimental and store facade types, got %T", store)
	}
}

func TestStoreViewUsesConfiguredSessionRoot(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	view := NewStoreView(cfg)
	if got := view.Store().Root(); got != cfg.Session.Dir {
		t.Fatalf("expected store root %q, got %q", cfg.Session.Dir, got)
	}
}
