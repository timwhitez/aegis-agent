package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestInitPersistentMetadataSelection(t *testing.T) {
	for _, tc := range []struct {
		name, profile, option string
		want                  *bool
		wantError             bool
	}{
		{name: "omitted", profile: "openai-compatible"},
		{name: "false", profile: "openai-compatible", option: "false", want: boolPointer(false)},
		{name: "true", profile: "openai-compatible", option: "true", want: boolPointer(true)},
		{name: "official_openai", profile: "openai", option: "false", want: boolPointer(false)},
		{name: "omitted_anthropic", profile: "anthropic"},
		{name: "omitted_google", profile: "google"},
		{name: "other_family", profile: "anthropic", option: "false", wantError: true},
		{name: "unknown_profile", profile: "unsupported-test-profile", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			guidanceWorkingDirectory(t, cwd)
			guidanceIsolatedEnvironment(t, t.TempDir())
			args := []string{"init", "--provider", tc.profile, "--example-hook=false"}
			const model, endpoint, keyEnv = "init-selected-model", "http://127.0.0.1:1/init-selected/v1", "INIT_SELECTION_TEST_API_KEY"
			args = append(args, "--model", model, "--base-url", endpoint, "--api-key-env", keyEnv)
			expected := config.Default().Providers[tc.profile]
			expected.Model, expected.BaseURL, expected.APIKeyEnv = model, endpoint, keyEnv
			expected.SendMetadata = tc.want
			if tc.profile == "openai" || tc.profile == "openai-compatible" {
				args = append(args, "--wire-api", "responses")
				expected.WireAPI = "responses"
			}
			if tc.option != "" {
				args = append(args, "--send-metadata="+tc.option)
			}
			err := Run(context.Background(), args, io.Discard, io.Discard)
			if tc.wantError {
				if err == nil {
					t.Fatal("unsupported selection succeeded")
				}
				for _, path := range []string{".aegis-agent", ".env.example", "workspace", "skills"} {
					if _, err := os.Lstat(filepath.Join(cwd, path)); !os.IsNotExist(err) {
						t.Fatalf("validation wrote %s: %v", path, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(filepath.Join(cwd, ".aegis-agent/config.yaml"), cwd)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DefaultProvider != tc.profile || !reflect.DeepEqual(cfg.Providers[tc.profile], expected) {
				t.Fatalf("generated complete profile: profile=%q value=%#v want=%#v", cfg.DefaultProvider, cfg.Providers[tc.profile], expected)
			}
			for name, before := range config.Default().Providers {
				if name == tc.profile {
					continue
				}
				if !reflect.DeepEqual(cfg.Providers[name], before) {
					t.Fatalf("changed unrelated profile %q", name)
				}
			}
			if _, err := os.Lstat(filepath.Join(cwd, ".aegis-agent/trusted")); !os.IsNotExist(err) {
				t.Fatalf("created trust marker: %v", err)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func TestExplicitMissingSelectionIsClassifiedConfigError(t *testing.T) {
	for _, kind := range []string{"cli", "env_after_home"} {
		t.Run(kind, func(t *testing.T) {
			cwd, home := t.TempDir(), t.TempDir()
			guidanceWorkingDirectory(t, cwd)
			guidanceIsolatedEnvironment(t, home)
			missing := filepath.Join(cwd, "missing-parent", "selected.yaml")
			args := []string{"doctor", "--skip-probe", "--json"}
			if kind == "cli" {
				args = append(args, "--config", missing)
			} else {
				guidanceWrite(t, filepath.Join(home, ".aegis-agent/config.yaml"), "providers:\n  openai:\n    model: home-model\n", 0o600)
				t.Setenv("AEGIS_AGENT_CONFIG", missing)
			}
			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), args, &stdout, &stderr)
			var classified ClassifiedError
			if !errors.Is(err, os.ErrNotExist) || !errors.As(err, &classified) || classified.Code != 2 {
				t.Fatalf("selected-missing classification: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("claimed active doctor config: %s", stdout.String())
			}
			if _, err := os.Lstat(filepath.Join(cwd, ".aegis-agent/sessions")); !os.IsNotExist(err) {
				t.Fatalf("selection failure created Store: %v", err)
			}
		})
	}
}

func TestApprovalReceiptReplayWithRealSelectedConfigLoader(t *testing.T) {
	// Preserve the actual loading boundary before the existing admission fixture
	// replaces it. Admission still uses a real runtime and local provider.
	actualRunner, actualStore := runnerLoader, storeRunnerLoader
	cwd := t.TempDir()
	guidanceWorkingDirectory(t, cwd)
	guidanceIsolatedEnvironment(t, t.TempDir())
	store, id, cfg, calls := cliReceiptFixture(t)
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshot.Target()
	const requestID = "real-loader-existing-receipt"
	result, err := runtime.NewCoreRunner(cfg).Continue(context.Background(), runtime.ContinueRequest{
		SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: requestID,
	})
	if err != nil || result.Status != session.StatusCompleted || calls.Load() != 1 {
		t.Fatalf("admission control: %#v %v calls=%d", result, err, calls.Load())
	}
	beforeState, err := store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	beforeMessages, err := store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents, err := store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(store.Root(), id, "approval-operations.json")
	beforeLedger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	controlReceipt, err := runtime.NewCoreRunner(cfg).ApprovalReceipt(id, requestID)
	if err != nil || !controlReceipt.Found {
		t.Fatalf("control receipt lookup: %#v %v", controlReceipt, err)
	}
	cfg.DefaultProvider = "unavailable-current-default"
	path := filepath.Join(cwd, "selected.yaml")
	data, err := config.MarshalYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	guidanceWrite(t, path, string(data), 0o600)
	runnerLoader, storeRunnerLoader = actualRunner, actualStore
	for _, args := range [][]string{
		cliReceiptArgs(id, requestID, target),
		{"continue", id, "--approval-receipt", "--approval-request-id", requestID, "--json"},
	} {
		var stdout, stderr bytes.Buffer
		args = append(args, "--config", path)
		if err := Run(context.Background(), args, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), `"stage":"admitted"`) {
			t.Fatalf("real loader lost receipt: args=%v err=%v stdout=%s stderr=%s", args, err, stdout.String(), stderr.String())
		}
		var wire struct {
			SessionID         string                  `json:"session_id"`
			Approval          *runtime.ApprovalResult `json:"approval"`
			CurrentState      *session.State          `json:"current_state"`
			CurrentStateError string                  `json:"current_state_error"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &wire); err != nil {
			t.Fatalf("receipt JSON: %v stdout=%s", err, stdout.String())
		}
		if wire.SessionID != id || wire.Approval == nil || !wire.Approval.Replay || !reflect.DeepEqual(wire.Approval.Lookup, controlReceipt) || wire.CurrentState == nil || wire.CurrentStateError != "" || !reflect.DeepEqual(*wire.CurrentState, beforeState) {
			t.Fatalf("wrong receipt/replay/current facts: %#v stdout=%s", wire, stdout.String())
		}
	}
	missingArgs := append(cliReceiptArgs(id, requestID, target), "--config", filepath.Join(cwd, "deleted.yaml"))
	if err := Run(context.Background(), missingArgs, io.Discard, io.Discard); !errors.Is(err, os.ErrNotExist) || exitCodeForError(err) != 2 {
		t.Fatalf("missing selected base config did not reject before lookup: %v", err)
	}
	afterState, err := store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	afterMessages, err := store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	afterEvents, err := store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	afterLedger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !bytes.Equal(beforeLedger, afterLedger) || !reflect.DeepEqual(beforeState, afterState) || !reflect.DeepEqual(beforeMessages, afterMessages) || !reflect.DeepEqual(beforeEvents, afterEvents) {
		t.Fatal("real-loader receipt query/replay/selection failure changed execution facts")
	}
}
