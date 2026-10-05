package app

import (
	"context"
	"io"
	"testing"

	"aegis-agent/internal/config"
	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

func TestRunCommandPreservesLiteralFlagPrompt(t *testing.T) {
	for _, args := range [][]string{
		{"exec", "--", "--send-metadata=false"},
		{"exec", "--json", "--", "--send-metadata=false", "--config", "literal.yaml"},
		{"run", "prefix", "--json", "--", "--send-metadata=false"},
		{"exec", "--resume", "saved", "--", "--send-metadata=false"},
	} {
		t.Run(args[0]+args[1], func(t *testing.T) {
			fake := newFakeRunner()
			fake.startResult = runtime.RunResult{SessionID: "fresh", Status: session.StatusCompleted}
			fake.continueResult = runtime.RunResult{SessionID: "saved", Status: session.StatusCompleted}
			original := runnerLoader
			runnerLoader = func(path, cwd string) (coreRunner, *config.Config, error) {
				if path != "" {
					t.Fatalf("literal config became an option: %q", path)
				}
				return fake, config.Default(), nil
			}
			t.Cleanup(func() { runnerLoader = original })
			if err := Run(context.Background(), args, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			want := "--send-metadata=false"
			if args[1] == "--json" {
				want += " --config literal.yaml"
			}
			if args[0] == "run" {
				want = "prefix " + want
			}
			if len(fake.continueCalls) == 1 {
				got := fake.continueCalls[0]
				if got.Message != want || got.ProviderOptions.SendMetadata != nil {
					t.Fatalf("literal resume input became an option: %#v", got)
				}
			} else if len(fake.startCalls) == 1 {
				got := fake.startCalls[0]
				if got.Prompt != want || got.ProviderOptions.SendMetadata != nil {
					t.Fatalf("literal prompt became an option: %#v", got)
				}
			} else {
				t.Fatalf("unexpected calls: start=%d continue=%d", len(fake.startCalls), len(fake.continueCalls))
			}
		})
	}
}
