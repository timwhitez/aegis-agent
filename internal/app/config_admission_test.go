package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aegis-agent/internal/config"
	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

func TestProviderConfigAdmissionNoLoadedLayer(t *testing.T) {
	for _, cmd := range [][]string{{"run", "offline"}, {"exec", "offline"}, {"probe-provider"}, {"doctor", "--json"}, {"web", "--listen", "invalid-admission-address"}, {"experimental", "web", "--listen", "invalid-admission-address"}} {
		t.Run(strings.Join(cmd, "_"), func(t *testing.T) {
			f := newRunDiagnosticFixture(t)
			// A malformed candidate must remain uninspected during admission.
			guidanceWrite(t, filepath.Join(".aegis-agent", "config.yaml"), "unparseable: [\n", 0o600)
			var out bytes.Buffer
			err := Run(context.Background(), cmd, &out, io.Discard)
			if err == nil || !strings.Contains(err.Error()+out.String(), "--allow-builtin-config") {
				t.Fatalf("missing actionable rejection: %v %s", err, out.String())
			}
			var classified ClassifiedError
			var exit ExitError
			if !(errors.As(err, &classified) && classified.Code == 2) && !(errors.As(err, &exit) && exit.Code == 2) {
				t.Fatalf("expected config exit 2: %v", err)
			}
			if len(f.observations()) != 0 {
				t.Fatal("admission sent a provider request")
			}
			if _, err := os.Stat(filepath.Join(".aegis-agent", "sessions")); !os.IsNotExist(err) {
				t.Fatalf("admission initialized session store: %v", err)
			}
		})
	}
}

func TestProviderConfigAdmissionExplicitControls(t *testing.T) {
	for _, kind := range []string{"cli", "env", "home", "trusted_workspace"} {
		t.Run(kind, func(t *testing.T) {
			f := newRunDiagnosticFixture(t)
			p := f.cfg.Providers["gateway"]
			p.SendMetadata = boolPointer(false)
			f.cfg.Providers["gateway"] = p
			args := []string{"exec", "--json", "offline"}
			switch kind {
			case "cli":
				f.save(t, f.path)
				args = append(args, "--config", f.path)
			case "env":
				f.save(t, f.path)
				t.Setenv("AEGIS_AGENT_CONFIG", f.path)
			case "home":
				f.save(t, filepath.Join(f.home, ".aegis-agent", "config.yaml"))
			case "trusted_workspace":
				f.save(t, filepath.Join(".aegis-agent", "config.yaml"))
				t.Setenv("AEGIS_AGENT_TRUST_WORKSPACE_CONFIG", "1")
			}
			f.run(t, args...)
			if wire := f.observations(); len(wire) != 1 || wire[0].Metadata {
				t.Fatalf("selected config not used: %#v", wire)
			}
		})
	}
}

func TestProviderConfigAdmissionBuiltinAndOfflineDoctor(t *testing.T) {
	f := newRunDiagnosticFixture(t)
	// Admit deliberately chosen builtin defaults without making external requests.
	original := runnerLoader
	fake := newFakeRunner()
	fake.startResult = runtime.RunResult{SessionID: "admission-fixture", Status: session.StatusCompleted}
	runnerLoader = func(path, cwd string) (coreRunner, *config.Config, error) {
		cfg, err := loadConfig(path, cwd)
		return fake, cfg, err
	}
	t.Cleanup(func() { runnerLoader = original })
	for _, args := range [][]string{{"exec", "--allow-builtin-config", "offline"}, {"run", "offline", "--allow-builtin-config"}, {"probe-provider", "--allow-builtin-config"}, {"doctor", "--skip-probe", "--json"}} {
		if err := Run(context.Background(), args, io.Discard, io.Discard); err != nil {
			t.Fatalf("explicit/offline control %v: %v", args, err)
		}
	}
	if len(fake.startCalls) != 2 || len(fake.probeCalls) != 1 || len(f.observations()) != 0 {
		t.Fatalf("unexpected calls: start=%d probe=%d", len(fake.startCalls), len(fake.probeCalls))
	}
}

func TestMetadataCLIOverridePreservesResumeProvenance(t *testing.T) {
	for _, original := range []*bool{nil, boolPointer(true)} {
		for _, cmd := range []string{"exec", "continue"} {
			t.Run(fmt.Sprintf("%s_original_%v", cmd, original), func(t *testing.T) {
				f := newRunDiagnosticFixture(t)
				p := f.cfg.Providers["gateway"]
				p.SendMetadata, p.ReasoningEffort, p.RequestTimeoutSec = original, "high", 42
				f.cfg.Providers["gateway"] = p
				f.save(t, f.path)
				values := f.run(t, "exec", "--config", f.path, "--json", "offline")
				id := fmt.Sprint(values[len(values)-1]["session_id"])
				store := session.NewStore(f.cfg.Session.Dir)
				before, err := store.LoadMetadata(id)
				if err != nil {
					t.Fatal(err)
				}
				oldEvents, err := store.LoadEvents(id)
				if err != nil {
					t.Fatal(err)
				}
				originalEvents, _ := json.Marshal(oldEvents)
				// Today's config is deliberately incompatible; only the chosen option changes.
				p.Model, p.BaseURL, p.ReasoningEffort, p.RequestTimeoutSec = "changed", "http://127.0.0.1:1/never-contact", "low", 1
				p.SendMetadata = boolPointer(false)
				f.cfg.Providers["gateway"] = p
				f.save(t, f.path)
				args := []string{"continue", id, "--message", "offline next"}
				if cmd == "exec" {
					args = []string{"exec", "--resume", id, "offline next"}
				}
				args = append(args, "--config", f.path, "--json", "--send-metadata=false")
				f.run(t, args...)
				after, err := store.LoadMetadata(id)
				if err != nil {
					t.Fatal(err)
				}
				want := before
				want.ProviderOptions.SendMetadata = boolPointer(false)
				if !reflect.DeepEqual(after, want) {
					t.Fatalf("unrelated snapshot drift: %#v want %#v", after, want)
				}
				wire := f.observations()
				if len(wire) != 3 || wire[2].Metadata || wire[2].Model != before.Model {
					t.Fatalf("override first request: %#v", wire)
				}
				newEvents, err := store.LoadEvents(id)
				if err != nil {
					t.Fatal(err)
				}
				prefix, _ := json.Marshal(newEvents[:len(oldEvents)])
				if !bytes.Equal(prefix, originalEvents) {
					t.Fatal("rewrote original event provenance")
				}
				found := false
				for _, event := range newEvents[len(oldEvents):] {
					if event.Type != "session.provider_options.overridden" {
						continue
					}
					found = true
					if event.Data["effective"] != false || event.Data["option"] != "send_metadata" || event.Data["source"] != "cli" {
						t.Fatalf("override fact: %#v", event)
					}
					if original == nil && event.Data["previous"] != nil || original != nil && event.Data["previous"] != true {
						t.Fatalf("lost original option: %#v", event)
					}
				}
				if !found {
					t.Fatal("override was not recorded")
				}
				// Omission on the next resume keeps the explicit persisted false choice.
				f.run(t, "exec", "--config", f.path, "--resume", id, "--json", "offline again")
				if wire := f.observations(); len(wire) != 4 || wire[3].Metadata {
					t.Fatalf("persisted choice: %#v", wire)
				}
			})
		}
	}
}

func TestInitExplicitReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		profile, effort     string
		selected, wantError bool
	}{
		{"openai-compatible", "", false, false}, {"openai-compatible", "high", true, false}, {"openai", "provider-native-value", true, false}, {"anthropic", "high", true, true}, {"google", "high", true, true}, {"openai-compatible", "", true, true},
	} {
		t.Run(tc.profile+"_"+tc.effort+fmt.Sprint(tc.selected), func(t *testing.T) {
			newRunDiagnosticFixture(t)
			args := []string{"init", "--provider", tc.profile, "--example-hook=false"}
			if tc.selected {
				args = append(args, "--reasoning-effort", tc.effort)
			}
			err := Run(context.Background(), args, io.Discard, io.Discard)
			if tc.wantError {
				if err == nil {
					t.Fatal("invalid effort selection accepted")
				}
				if _, err := os.Stat(".aegis-agent"); !os.IsNotExist(err) {
					t.Fatalf("wrote assets before validation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(".aegis-agent/config.yaml", ".")
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Providers[tc.profile].ReasoningEffort != tc.effort {
				t.Fatal("effort not preserved")
			}
			if _, err := os.Stat(".aegis-agent/trusted"); !os.IsNotExist(err) {
				t.Fatal("created trust marker")
			}
		})
	}
}
