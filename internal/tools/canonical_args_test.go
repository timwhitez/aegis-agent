package tools

import (
	"encoding/json"
	"testing"

	"go-cli-agent/internal/config"
)

func TestCanonicalArgsReadFileNormalizesEffectiveDefaultsAndCaps(t *testing.T) {
	cfg := config.Default()

	baseline := mustCanonicalReadOnlyArgs(t, "read_file", `{"path":"src/file.go"}`, cfg)
	for _, raw := range []string{
		`{"path":"./src/../src/file.go","offset":0,"limit":120}`,
		`{"path":"src/file.go","offset":1}`,
		`{"path":"src/file.go","limit":0}`,
		`{"path":"src/file.go","limit":9999}`,
	} {
		if got := mustCanonicalReadOnlyArgs(t, "read_file", raw, cfg); got != baseline {
			t.Fatalf("read_file canonical args differ for %s:\n got %s\nwant %s", raw, got, baseline)
		}
	}

	line := baseline
	byteMode := mustCanonicalReadOnlyArgs(t, "read_file", `{"path":"src/file.go","byte_offset":0,"byte_limit":24576}`, cfg)
	if byteMode == line {
		t.Fatal("line and byte mode canonical args must differ")
	}
	byteCapped := mustCanonicalReadOnlyArgs(t, "read_file", `{"path":"src/file.go","byte_limit":999999}`, cfg)
	if byteCapped != byteMode {
		t.Fatalf("oversized byte_limit did not normalize to the execution cap:\n got %s\nwant %s", byteCapped, byteMode)
	}

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "path", raw: `{"path":"src/other.go"}`},
		{name: "line offset", raw: `{"path":"src/file.go","offset":2}`},
		{name: "line limit", raw: `{"path":"src/file.go","limit":80}`},
		{name: "byte offset", raw: `{"path":"src/file.go","byte_offset":1,"byte_limit":24576}`},
		{name: "byte limit", raw: `{"path":"src/file.go","byte_limit":4096}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustCanonicalReadOnlyArgs(t, "read_file", tc.raw, cfg); got == baseline {
				t.Fatalf("%s unexpectedly canonicalized to an unchanged request: %s", tc.name, got)
			}
		})
	}
}

func TestCanonicalArgsSearchToolsNormalizeEffectiveDefaultsAndCaps(t *testing.T) {
	cfg := config.Default()
	tests := []struct {
		tool         string
		defaultLimit int
		cappedLimit  int
	}{
		{tool: "grep", defaultLimit: 200, cappedLimit: 200},
		{tool: "grep_files", defaultLimit: 100, cappedLimit: 200},
		{tool: "glob", defaultLimit: 100, cappedLimit: 200},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			baseline := mustCanonicalReadOnlyArgs(t, tc.tool, `{"pattern":"needle"}`, cfg)
			explicitDefault := mustCanonicalReadOnlyArgs(t, tc.tool, `{"pattern":"needle","limit":`+jsonInt(tc.defaultLimit)+`,"byte_limit":24576}`, cfg)
			if baseline != explicitDefault {
				t.Fatalf("omitted defaults differ from explicit defaults:\n got %s\nwant %s", baseline, explicitDefault)
			}
			capped := mustCanonicalReadOnlyArgs(t, tc.tool, `{"pattern":"needle","limit":9999,"byte_limit":999999}`, cfg)
			explicitCap := mustCanonicalReadOnlyArgs(t, tc.tool, `{"pattern":"needle","limit":`+jsonInt(tc.cappedLimit)+`,"byte_limit":32768}`, cfg)
			if capped != explicitCap {
				t.Fatalf("oversized limits differ from execution caps:\n got %s\nwant %s", capped, explicitCap)
			}

			variations := []string{
				`{"pattern":"other"}`,
				`{"pattern":"needle","path":"internal/runtime"}`,
				`{"pattern":"needle","include":"**/*.go"}`,
				`{"pattern":"needle","cursor":"cursor-v1"}`,
				`{"pattern":"needle","byte_limit":4096}`,
			}
			for _, raw := range variations {
				if got := mustCanonicalReadOnlyArgs(t, tc.tool, raw, cfg); got == baseline {
					t.Fatalf("query-affecting variation canonicalized to baseline: %s", raw)
				}
			}
		})
	}
	grepFiles := mustCanonicalReadOnlyArgs(t, "grep_files", `{"pattern":"needle"}`, cfg)
	glob := mustCanonicalReadOnlyArgs(t, "glob", `{"pattern":"needle"}`, cfg)
	if grepFiles == glob {
		t.Fatal("canonical search arguments must include the tool identity")
	}
}

func TestCanonicalArgsUseConfiguredToolOutputByteCap(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 4096

	for _, tool := range []string{"grep", "grep_files", "glob"} {
		capped := mustCanonicalReadOnlyArgs(t, tool, `{"pattern":"needle","byte_limit":999999}`, cfg)
		explicit := mustCanonicalReadOnlyArgs(t, tool, `{"pattern":"needle","byte_limit":4096}`, cfg)
		if capped != explicit {
			t.Fatalf("%s ignored configured tool-output cap:\n got %s\nwant %s", tool, capped, explicit)
		}
	}
	readCapped := mustCanonicalReadOnlyArgs(t, "read_file", `{"path":"src/file.go","byte_limit":999999}`, cfg)
	readExplicit := mustCanonicalReadOnlyArgs(t, "read_file", `{"path":"src/file.go","byte_limit":4096}`, cfg)
	if readCapped != readExplicit {
		t.Fatalf("read_file ignored configured tool-output cap:\n got %s\nwant %s", readCapped, readExplicit)
	}
}

func TestCanonicalArgsFailClosedOutsideAllowlistOrOnMalformedInput(t *testing.T) {
	for _, tool := range []string{"shell", "write_file", "edit_file", "agent_status"} {
		canonical, eligible, err := CanonicalReadOnlyToolArguments(tool, json.RawMessage(`{}`), config.Default())
		if err != nil || eligible || canonical != nil {
			t.Fatalf("%s should be ineligible without an error, got eligible=%t canonical=%s err=%v", tool, eligible, canonical, err)
		}
	}

	for _, tc := range []struct {
		tool string
		raw  string
	}{
		{tool: "read_file", raw: `{"path":"src/file.go","unknown":true}`},
		{tool: "read_file", raw: `{"path":"src/file.go","offset":1,"byte_limit":20}`},
		{tool: "grep", raw: `{"pattern":"needle","unknown":true}`},
		{tool: "glob", raw: `{"pattern":""}`},
	} {
		canonical, eligible, err := CanonicalReadOnlyToolArguments(tc.tool, json.RawMessage(tc.raw), config.Default())
		if !eligible || err == nil || canonical != nil {
			t.Fatalf("%s malformed input did not fail closed: eligible=%t canonical=%s err=%v", tc.tool, eligible, canonical, err)
		}
	}
}

func mustCanonicalReadOnlyArgs(t *testing.T, tool, raw string, cfg *config.Config) string {
	t.Helper()
	canonical, eligible, err := CanonicalReadOnlyToolArguments(tool, json.RawMessage(raw), cfg)
	if err != nil {
		t.Fatalf("canonicalize %s %s: %v", tool, raw, err)
	}
	if !eligible {
		t.Fatalf("%s unexpectedly ineligible", tool)
	}
	if !json.Valid(canonical) {
		t.Fatalf("%s canonical args are not JSON: %q", tool, canonical)
	}
	return string(canonical)
}

func jsonInt(value int) string {
	data, _ := json.Marshal(value)
	return string(data)
}
