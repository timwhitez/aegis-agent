package extensions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverRequiresExplicitTrustAndSkipsSymlinkEscape(t *testing.T) {
	workdir := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".agent", "safe-tool"), 0o700); err != nil {
		t.Fatalf("mkdir safe tool: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "escaped-tool"), 0o700); err != nil {
		t.Fatalf("mkdir escaped tool: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "escaped-tool"), filepath.Join(workdir, ".agent", "escaped-tool")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	result, err := Discover(workdir, false)
	if err != nil {
		t.Fatalf("discover untrusted: %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("expected only safe in-workspace candidate, got %#v", result.Candidates)
	}
	if result.DiscoveryPath != filepath.Join(workdir, ".agent") || result.Trusted {
		t.Fatalf("unexpected discovery status: %#v", result)
	}
	if result.Candidates[0].QualifiedName != "workspace/safe-tool" || !result.Candidates[0].Disabled || result.Candidates[0].Trust != TrustUntrusted {
		t.Fatalf("unexpected untrusted candidate: %#v", result.Candidates[0])
	}
	if result.Candidates[0].DisabledReason == "" {
		t.Fatalf("expected disabled reason for untrusted candidate: %#v", result.Candidates[0])
	}

	result, err = Discover(workdir, true)
	if err != nil {
		t.Fatalf("discover trusted: %v", err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Disabled || result.Candidates[0].Trust != TrustExplicit {
		t.Fatalf("unexpected trusted candidate: %#v", result.Candidates)
	}
	if !result.Trusted || result.Candidates[0].DisabledReason != "" {
		t.Fatalf("unexpected trusted status: %#v", result)
	}
}

func TestUntrustedExtensionIsReportedButNotLoaded(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".agent", "reviewer"), 0o700); err != nil {
		t.Fatalf("mkdir extension: %v", err)
	}
	result, err := Discover(workdir, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("expected one candidate, got %#v", result.Candidates)
	}
	candidate := result.Candidates[0]
	if candidate.Trust != TrustUntrusted || !candidate.Disabled || candidate.DisabledReason == "" {
		t.Fatalf("expected untrusted disabled candidate, got %#v", candidate)
	}
	if candidate.QualifiedName != "workspace/reviewer" {
		t.Fatalf("expected qualified workspace name, got %#v", candidate)
	}
}

func TestValidateToolNameRequiresQualifiedNonReservedName(t *testing.T) {
	reserved := map[string]struct{}{"shell": {}}
	if err := ValidateToolName("workspace/tool", reserved); err != nil {
		t.Fatalf("expected qualified name to pass: %v", err)
	}
	if err := ValidateToolName("shell", reserved); err == nil {
		t.Fatal("expected reserved tool name to fail")
	}
	if err := ValidateToolName("custom_tool", nil); err == nil {
		t.Fatal("expected unqualified tool name to fail")
	}
}

func TestValidateNoAmbiguousShortNamesRejectsDuplicateShortName(t *testing.T) {
	err := ValidateNoAmbiguousShortNames([]Candidate{
		{Name: "reviewer", QualifiedName: "workspace/reviewer"},
		{Name: "reviewer", QualifiedName: "trusted/reviewer"},
	})
	if err == nil {
		t.Fatal("expected duplicate short name ambiguity to fail")
	}
}
