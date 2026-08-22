package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/skills"
)

func metadataInteger(t *testing.T, metadata map[string]any, key string) int64 {
	t.Helper()
	switch value := metadata[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			t.Fatalf("metadata[%q]=%#v is not an integer: %v", key, value, err)
		}
		return parsed
	default:
		t.Fatalf("metadata[%q]=%#v is not numeric", key, metadata[key])
		return 0
	}
}

func metadataText(t *testing.T, metadata map[string]any, key string) string {
	t.Helper()
	value, ok := metadata[key].(string)
	if !ok {
		t.Fatalf("metadata[%q]=%#v is not a string", key, metadata[key])
	}
	return value
}

func readFileBody(output string) string {
	_, body, found := strings.Cut(output, "\n")
	if !found {
		return ""
	}
	return body
}

func TestReadFileByteSchemaAndMutualExclusion(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	definition := registry.Get("read_file")
	if definition == nil {
		t.Fatal("read_file definition missing")
	}
	properties, _ := definition.InputSchema["properties"].(map[string]any)
	for _, field := range []string{"byte_offset", "byte_limit"} {
		if properties[field] == nil {
			t.Fatalf("read_file schema missing %s: %#v", field, definition.InputSchema)
		}
	}
	if definition.InputSchema["oneOf"] == nil {
		t.Fatalf("read_file schema does not express line/byte mode exclusivity: %#v", definition.InputSchema)
	}
	if err := os.WriteFile(filepath.Join(workdir, "notes.txt"), []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	for name, raw := range map[string]json.RawMessage{
		"mixed modes":   json.RawMessage(`{"path":"notes.txt","offset":1,"byte_offset":0,"byte_limit":4}`),
		"zero limit":    json.RawMessage(`{"path":"notes.txt","byte_offset":0,"byte_limit":0}`),
		"negative":      json.RawMessage(`{"path":"notes.txt","byte_offset":0,"byte_limit":-1}`),
		"missing limit": json.RawMessage(`{"path":"notes.txt","byte_offset":0}`),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), "read_file", execCtx, raw)
			if err != nil {
				t.Fatalf("execute read_file: %v", err)
			}
			if !result.IsError || result.Metadata["failure_class"] != FailureClassSchemaReject {
				t.Fatalf("expected stable invalid-argument result, got %#v", result)
			}
		})
	}
}

func TestReadFileByteUTF8PaginationAndBoundaryAdjustment(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	content := "A🙂中BéZ"
	if err := os.WriteFile(filepath.Join(workdir, "utf8.txt"), []byte(content), 0o600); err != nil {
		t.Fatalf("write UTF-8 fixture: %v", err)
	}

	var rebuilt strings.Builder
	offset := int64(0)
	for page := 0; page < 16; page++ {
		raw := json.RawMessage(fmt.Sprintf(`{"path":"utf8.txt","byte_offset":%d,"byte_limit":5}`, offset))
		result, err := registry.Execute(context.Background(), "read_file", execCtx, raw)
		if err != nil {
			t.Fatalf("read byte page %d: %v", page, err)
		}
		if result.IsError {
			t.Fatalf("read byte page %d returned error: %#v", page, result)
		}
		if result.Metadata["mode"] != "byte" || result.Metadata["encoding"] != "utf-8" {
			t.Fatalf("missing byte/encoding metadata: %#v", result.Metadata)
		}
		body := readFileBody(result.LLMOutput)
		if !json.Valid([]byte(fmt.Sprintf("%q", body))) {
			t.Fatalf("page body is not JSON-safe UTF-8: %q", body)
		}
		rebuilt.WriteString(body)
		if result.Metadata["has_more"] != true {
			break
		}
		next := metadataInteger(t, result.Metadata, "next_byte_offset")
		if next <= offset {
			t.Fatalf("byte cursor did not advance: current=%d next=%d metadata=%#v", offset, next, result.Metadata)
		}
		offset = next
	}
	if rebuilt.String() != content {
		t.Fatalf("UTF-8 pagination changed content: got %q want %q", rebuilt.String(), content)
	}

	midRune, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{"path":"utf8.txt","byte_offset":2,"byte_limit":8}`))
	if err != nil {
		t.Fatalf("read mid-rune page: %v", err)
	}
	if midRune.IsError || midRune.Metadata["start_adjusted"] != true || metadataInteger(t, midRune.Metadata, "effective_byte_start") != 5 {
		t.Fatalf("mid-rune offset was not advanced to the next boundary: %#v", midRune)
	}
}

func TestReadFileByteMinifiedSingleLineIsBounded(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	content := strings.Repeat("x", int(fileutil.MaxRegularFileReadBytes)-6) + "needle"
	if err := os.WriteFile(filepath.Join(workdir, "minified.js"), []byte(content), 0o600); err != nil {
		t.Fatalf("write minified fixture: %v", err)
	}
	result, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{"path":"minified.js","byte_offset":0,"byte_limit":1024}`))
	if err != nil {
		t.Fatalf("read minified byte range: %v", err)
	}
	if result.IsError {
		t.Fatalf("minified byte range failed: %#v", result)
	}
	if len(result.LLMOutput) > 2048 || metadataInteger(t, result.Metadata, "returned_bytes") != 1024 || metadataInteger(t, result.Metadata, "total_bytes") != fileutil.MaxRegularFileReadBytes || result.Metadata["has_more"] != true {
		t.Fatalf("minified result was not bounded/recoverable: output=%d metadata=%#v", len(result.LLMOutput), result.Metadata)
	}
	lineResult, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{"path":"minified.js"}`))
	if err != nil {
		t.Fatalf("read minified line mode: %v", err)
	}
	if !lineResult.IsError || lineResult.Metadata[MetadataFailureClass] != FailureClassOutputBudgetTooSmall || len(lineResult.LLMOutput) > 512 || !strings.Contains(lineResult.LLMOutput, "byte_offset") {
		t.Fatalf("line mode allowed a 16 MiB single line into model output: output=%d result=%#v", len(lineResult.LLMOutput), lineResult)
	}
}

func TestReadFileByteRejectsInvalidUTF8AndPreservesSourceGuard(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	if err := os.WriteFile(filepath.Join(workdir, "invalid.bin"), []byte{'a', 0xff, 'b'}, 0o600); err != nil {
		t.Fatalf("write invalid UTF-8 fixture: %v", err)
	}
	invalid, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{"path":"invalid.bin","byte_offset":0,"byte_limit":8}`))
	if err != nil {
		t.Fatalf("read invalid UTF-8: %v", err)
	}
	if !invalid.IsError || invalid.Metadata["failure_class"] != FailureClassUnsupportedEncoding || !strings.Contains(invalid.LLMOutput, "UTF-8") {
		t.Fatalf("invalid UTF-8 did not return a stable encoding error: %#v", invalid)
	}

	oversized := filepath.Join(workdir, "oversized.txt")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatalf("create oversized fixture: %v", err)
	}
	if err := file.Truncate(fileutil.MaxRegularFileReadBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("truncate oversized fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close oversized fixture: %v", err)
	}
	guarded, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{"path":"oversized.txt","byte_offset":0,"byte_limit":16}`))
	if err != nil {
		t.Fatalf("read oversized workspace source: %v", err)
	}
	if !guarded.IsError || !strings.Contains(guarded.LLMOutput, "maximum readable size") {
		t.Fatalf("byte mode weakened the 16 MiB source guard: %#v", guarded)
	}
}

func TestReadFileByteEOFSmallRuneAndConfiguredCap(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	if err := os.WriteFile(filepath.Join(workdir, "edge.txt"), []byte("🙂"+strings.Repeat("x", 40*1024)), 0o600); err != nil {
		t.Fatalf("write edge fixture: %v", err)
	}

	tooSmall, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{"path":"edge.txt","byte_offset":0,"byte_limit":1}`))
	if err != nil {
		t.Fatalf("read too-small rune window: %v", err)
	}
	if !tooSmall.IsError || tooSmall.Metadata[MetadataFailureClass] != FailureClassOutputBudgetTooSmall {
		t.Fatalf("partial rune did not return a typed error: %#v", tooSmall)
	}

	capped, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{"path":"edge.txt","byte_offset":0,"byte_limit":100000}`))
	if err != nil || capped.IsError {
		t.Fatalf("read capped byte window: result=%#v err=%v", capped, err)
	}
	if metadataInteger(t, capped.Metadata, "returned_bytes") > readFileMaxByteLimit || capped.Metadata["end_adjusted"] != true || len(capped.LLMOutput) > execCtx.Config.Runtime.ToolOutput.LLMOutputMaxBytes {
		t.Fatalf("read_file byte cap was not enforced: output=%d metadata=%#v", len(capped.LLMOutput), capped.Metadata)
	}

	total := metadataInteger(t, capped.Metadata, "total_bytes")
	for _, offset := range []int64{total, total + 100} {
		result, execErr := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(fmt.Sprintf(`{"path":"edge.txt","byte_offset":%d,"byte_limit":16}`, offset)))
		if execErr != nil || result.IsError {
			t.Fatalf("read EOF offset %d: result=%#v err=%v", offset, result, execErr)
		}
		if metadataInteger(t, result.Metadata, "returned_bytes") != 0 || result.Metadata["has_more"] != false || metadataInteger(t, result.Metadata, "next_byte_offset") != total {
			t.Fatalf("EOF byte window is not stable for offset %d: %#v", offset, result.Metadata)
		}
	}

	if err := os.WriteFile(filepath.Join(workdir, "empty.txt"), nil, 0o600); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}
	empty, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{"path":"empty.txt","byte_limit":16}`))
	if err != nil || empty.IsError || metadataInteger(t, empty.Metadata, "total_bytes") != 0 || readFileBody(empty.LLMOutput) != "" {
		t.Fatalf("empty byte window is not deterministic: result=%#v err=%v", empty, err)
	}
}

func TestReadFileBytePagesJSONLAndNoNewlineWithoutLoss(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	content := "{\"id\":1,\"name\":\"中\"}\n{\"id\":2,\"name\":\"🙂\"}" + strings.Repeat(" tail", 200)
	if err := os.WriteFile(filepath.Join(workdir, "records.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatalf("write JSONL fixture: %v", err)
	}
	var rebuilt strings.Builder
	offset := int64(0)
	for page := 0; page < 64; page++ {
		result, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(fmt.Sprintf(`{"path":"records.jsonl","byte_offset":%d,"byte_limit":31}`, offset)))
		if err != nil || result.IsError {
			t.Fatalf("read JSONL page %d: result=%#v err=%v", page, result, err)
		}
		rebuilt.WriteString(readFileBody(result.LLMOutput))
		if result.Metadata["has_more"] != true {
			break
		}
		next := metadataInteger(t, result.Metadata, "next_byte_offset")
		if next <= offset {
			t.Fatalf("JSONL cursor did not advance: current=%d next=%d", offset, next)
		}
		offset = next
	}
	if rebuilt.String() != content {
		t.Fatalf("JSONL/no-newline pagination changed content: got=%d bytes want=%d", rebuilt.Len(), len(content))
	}
}

func TestReadFileByteAllowsLargeExactSessionArtifact(t *testing.T) {
	registry, execCtx, _ := newSearchToolTestRegistry(t)
	artifactRoot := filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "artifacts", "tool-outputs")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatalf("mkdir artifact root: %v", err)
	}
	artifactPath := filepath.Join(artifactRoot, "large-output.txt")
	file, err := os.OpenFile(artifactPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	payload := []byte("recoverable-artifact-tail")
	payloadOffset := fileutil.MaxRegularFileReadBytes + 128
	if err := file.Truncate(payloadOffset + int64(len(payload)) + 32); err != nil {
		_ = file.Close()
		t.Fatalf("truncate artifact: %v", err)
	}
	if _, err := file.WriteAt(payload, payloadOffset); err != nil {
		_ = file.Close()
		t.Fatalf("write artifact tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close artifact: %v", err)
	}
	execCtx.EphemeralArtifactRoot = artifactRoot
	result, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(fmt.Sprintf(`{"path":%q,"byte_offset":%d,"byte_limit":%d}`, artifactPath, payloadOffset, len(payload))))
	if err != nil || result.IsError {
		t.Fatalf("read large session artifact: result=%#v err=%v", result, err)
	}
	if result.Metadata["path_source"] != "session_ephemeral_artifact" || metadataInteger(t, result.Metadata, "total_bytes") <= fileutil.MaxRegularFileReadBytes || readFileBody(result.LLMOutput) != string(payload) {
		t.Fatalf("large artifact range was not recovered exactly: %#v output=%q", result.Metadata, result.LLMOutput)
	}
}

func TestReadFileByteSourceAndSymlinkSafety(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(workdir, "file-link.txt")); err != nil {
		t.Fatalf("create file symlink: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(workdir, "dir-link")); err != nil {
		t.Fatalf("create directory symlink: %v", err)
	}
	for name, path := range map[string]string{
		"file symlink":     "file-link.txt",
		"parent symlink":   "dir-link/outside.txt",
		"workspace escape": "../outside.txt",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(fmt.Sprintf(`{"path":%q,"byte_offset":0,"byte_limit":16}`, path)))
			if err != nil {
				t.Fatalf("read unsafe path: %v", err)
			}
			if !result.IsError {
				t.Fatalf("byte mode accepted unsafe path %q: %#v", path, result)
			}
		})
	}

	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "range-skill")
	referenceDir := filepath.Join(skillDir, "references")
	if err := os.MkdirAll(referenceDir, 0o700); err != nil {
		t.Fatalf("mkdir skill references: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: range-skill\ndescription: range test\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write skill entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(referenceDir, "safe.md"), []byte("skill-byte-content"), 0o600); err != nil {
		t.Fatalf("write skill reference: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skill: %v", err)
	}
	skillRegistry, err := NewRegistry(execCtx.Config, catalog, execCtx.Store, nil)
	if err != nil {
		t.Fatalf("new skill registry: %v", err)
	}
	execCtx.Catalog = catalog
	skillResult, err := skillRegistry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{"path":"skills/range-skill/references/safe.md","byte_offset":0,"byte_limit":64}`))
	if err != nil || skillResult.IsError || skillResult.Metadata["path_source"] != "skill" || readFileBody(skillResult.LLMOutput) != "skill-byte-content" {
		t.Fatalf("safe skill byte range failed: result=%#v err=%v", skillResult, err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(referenceDir, "escape.md")); err != nil {
		t.Fatalf("create skill escape symlink: %v", err)
	}
	escaped, err := skillRegistry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{"path":"skills/range-skill/references/escape.md","byte_offset":0,"byte_limit":16}`))
	if err != nil || !escaped.IsError {
		t.Fatalf("skill symlink escape was accepted: result=%#v err=%v", escaped, err)
	}
}

func TestGrepByteBudgetAndSearchCursorPagination(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	var content strings.Builder
	for index := 0; index < 9; index++ {
		fmt.Fprintf(&content, "needle record %02d %s\n", index, strings.Repeat("x", 90))
	}
	if err := os.WriteFile(filepath.Join(workdir, "records.txt"), []byte(content.String()), 0o600); err != nil {
		t.Fatalf("write grep fixture: %v", err)
	}

	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 16; page++ {
		raw := json.RawMessage(fmt.Sprintf(`{"pattern":"needle","path":".","include":"*.txt","limit":2,"byte_limit":4096,"cursor":%q}`, cursor))
		result, err := registry.Execute(context.Background(), "grep", execCtx, raw)
		if err != nil {
			t.Fatalf("grep page %d: %v", page, err)
		}
		if result.IsError {
			t.Fatalf("grep page %d returned error: %#v", page, result)
		}
		if len(result.LLMOutput) > 4096 || result.Metadata["snapshot_semantics"] != "current_view" {
			t.Fatalf("grep page exceeded budget or omitted cursor semantics: output=%d metadata=%#v", len(result.LLMOutput), result.Metadata)
		}
		for _, line := range strings.Split(result.LLMOutput, "\n") {
			if !strings.Contains(line, ":needle record") {
				continue
			}
			if seen[line] {
				t.Fatalf("grep cursor repeated record %q", line)
			}
			seen[line] = true
		}
		if result.Metadata["has_more"] != true {
			if result.Metadata["stop_reason"] != "complete" {
				t.Fatalf("final grep page has wrong stop reason: %#v", result.Metadata)
			}
			break
		}
		if result.Metadata["stop_reason"] != "match_limit" {
			t.Fatalf("grep count page has wrong stop reason: %#v", result.Metadata)
		}
		cursor = metadataText(t, result.Metadata, "next_cursor")
		if cursor == "" || !strings.Contains(result.LLMOutput, cursor) {
			t.Fatalf("grep continuation cursor is not intact/model-visible: %#v output=%q", result.Metadata, result.LLMOutput)
		}
	}
	if len(seen) != 9 {
		t.Fatalf("grep cursor pagination returned %d unique records, want 9: %#v", len(seen), seen)
	}
}

func TestGrepByteLimitStopsAtWholeRecordAndReportsSpans(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	var content strings.Builder
	for index := 0; index < 20; index++ {
		fmt.Fprintf(&content, "%s needle-%02d %s\n", strings.Repeat("a", 180), index, strings.Repeat("b", 180))
	}
	if err := os.WriteFile(filepath.Join(workdir, "long-lines.txt"), []byte(content.String()), 0o600); err != nil {
		t.Fatalf("write long-line fixture: %v", err)
	}
	result, err := registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{"pattern":"needle","path":"long-lines.txt","limit":100,"byte_limit":1024}`))
	if err != nil {
		t.Fatalf("grep byte-limited page: %v", err)
	}
	if result.IsError {
		t.Fatalf("grep byte-limited page returned error: %#v", result)
	}
	if len(result.LLMOutput) > 1024 || result.Metadata["stop_reason"] != "byte_limit" || result.Metadata["byte_limit_reached"] != true || result.Metadata["has_more"] != true {
		t.Fatalf("grep did not stop at byte budget: output=%d metadata=%#v", len(result.LLMOutput), result.Metadata)
	}
	if metadataText(t, result.Metadata, "next_cursor") == "" {
		t.Fatalf("byte-limited grep result lacks continuation: %#v", result.Metadata)
	}
	records, ok := result.Metadata["match_records"].([]map[string]any)
	if !ok || len(records) == 0 {
		t.Fatalf("grep match byte spans missing: %#v", result.Metadata["match_records"])
	}
	for _, record := range records {
		if record["match_start_byte"] == nil || record["match_end_byte"] == nil || record["line_start_byte"] == nil || record["line_end_byte"] == nil {
			t.Fatalf("incomplete grep span record: %#v", record)
		}
	}
}

func TestGrepCountAndByteLimitTriggerAtSameBoundary(t *testing.T) {
	query := searchCursorQuery{Tool: "grep", RootPath: "/workspace", RootSource: "workspace", Pattern: "needle"}
	records := []searchPageRecord{
		plainSearchPageRecord("a.txt:1:"+strings.Repeat("a", 160), "a.txt", 1),
		plainSearchPageRecord("b.txt:1:"+strings.Repeat("b", 160), "b.txt", 1),
		plainSearchPageRecord("c.txt:1:"+strings.Repeat("c", 160), "c.txt", 1),
	}
	options := searchPageOptions{
		Tool:               "grep",
		Query:              query,
		Records:            records,
		ScanComplete:       false,
		RequestedLimit:     2,
		EffectiveLimit:     2,
		RequestedByteLimit: 4096,
		EffectiveByteLimit: 4096,
	}
	countOnly, err := buildSearchPage(options)
	if err != nil || countOnly.Metadata["stop_reason"] != "match_limit" {
		t.Fatalf("establish count-only boundary: page=%#v err=%v", countOnly, err)
	}
	options.RequestedByteLimit = len(countOnly.Output)
	options.EffectiveByteLimit = len(countOnly.Output)
	both, err := buildSearchPage(options)
	if err != nil {
		t.Fatalf("build same-boundary page: %v", err)
	}
	if both.Metadata["stop_reason"] != "byte_limit" || both.Metadata["match_limit_reached"] != true || both.Metadata["byte_limit_reached"] != true || both.Metadata["has_more"] != true {
		t.Fatalf("count+byte same boundary was not reported precisely: output=%d budget=%d metadata=%#v", len(both.Output), options.EffectiveByteLimit, both.Metadata)
	}
	if len(both.Output) > options.EffectiveByteLimit || !strings.Contains(both.Output, metadataText(t, both.Metadata, "next_cursor")) {
		t.Fatalf("same-boundary cursor exceeded/truncated the page: output=%d budget=%d page=%#v", len(both.Output), options.EffectiveByteLimit, both)
	}
}

func TestGrepFilesByteBudgetAndSearchCursor(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	for index := 0; index < 12; index++ {
		name := fmt.Sprintf("nested/%02d-%s.txt", index, strings.Repeat("p", 40))
		path := filepath.Join(workdir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir grep_files fixture: %v", err)
		}
		if err := os.WriteFile(path, []byte("needle\n"), 0o600); err != nil {
			t.Fatalf("write grep_files fixture: %v", err)
		}
	}
	first, err := registry.Execute(context.Background(), "grep_files", execCtx, json.RawMessage(`{"pattern":"needle","path":".","include":"*.txt","limit":100,"byte_limit":512}`))
	if err != nil {
		t.Fatalf("grep_files byte page: %v", err)
	}
	if first.IsError || len(first.LLMOutput) > 512 || first.Metadata["stop_reason"] != "byte_limit" || first.Metadata["has_more"] != true {
		t.Fatalf("grep_files byte contract failed: output=%d result=%#v", len(first.LLMOutput), first)
	}
	cursor := metadataText(t, first.Metadata, "next_cursor")
	secondRaw := json.RawMessage(fmt.Sprintf(`{"pattern":"needle","path":".","include":"*.txt","limit":100,"byte_limit":1024,"cursor":%q}`, cursor))
	second, err := registry.Execute(context.Background(), "grep_files", execCtx, secondRaw)
	if err != nil || second.IsError {
		t.Fatalf("grep_files continuation failed: result=%#v err=%v", second, err)
	}
	for _, firstLine := range strings.Split(first.LLMOutput, "\n") {
		if strings.HasSuffix(firstLine, ".txt") && strings.Contains(second.LLMOutput, firstLine) {
			t.Fatalf("grep_files continuation repeated %q", firstLine)
		}
	}
}

func TestSearchCursorRejectsQueryMismatchAndTampering(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	for index := 0; index < 3; index++ {
		if err := os.WriteFile(filepath.Join(workdir, fmt.Sprintf("match-%d.txt", index)), []byte("needle\n"), 0o600); err != nil {
			t.Fatalf("write cursor fixture: %v", err)
		}
	}
	first, err := registry.Execute(context.Background(), "grep_files", execCtx, json.RawMessage(`{"pattern":"needle","path":".","limit":1}`))
	if err != nil || first.IsError {
		t.Fatalf("create cursor: result=%#v err=%v", first, err)
	}
	cursor := metadataText(t, first.Metadata, "next_cursor")
	replacement := "A"
	if strings.HasSuffix(cursor, replacement) {
		replacement = "B"
	}
	mutated := cursor[:len(cursor)-1] + replacement
	for name, raw := range map[string]json.RawMessage{
		"different pattern": json.RawMessage(fmt.Sprintf(`{"pattern":"other","path":".","limit":1,"cursor":%q}`, cursor)),
		"different include": json.RawMessage(fmt.Sprintf(`{"pattern":"needle","path":".","include":"*.go","limit":1,"cursor":%q}`, cursor)),
		"different path":    json.RawMessage(fmt.Sprintf(`{"pattern":"needle","path":"match-0.txt","limit":1,"cursor":%q}`, cursor)),
		"tampered token":    json.RawMessage(fmt.Sprintf(`{"pattern":"needle","path":".","limit":1,"cursor":%q}`, mutated)),
	} {
		t.Run(name, func(t *testing.T) {
			result, execErr := registry.Execute(context.Background(), "grep_files", execCtx, raw)
			if execErr != nil {
				t.Fatalf("execute mismatched cursor: %v", execErr)
			}
			if !result.IsError || result.Metadata["cursor_error"] == nil {
				t.Fatalf("mismatched/tampered cursor was accepted: %#v", result)
			}
		})
	}
}

func TestSearchCursorCodecVersionChecksumAndBounds(t *testing.T) {
	query := searchCursorQuery{Tool: "grep", RootPath: "/workspace", RootSource: "workspace", Pattern: "needle", Include: "*.go"}
	token, err := encodeSearchCursor(query, 17, strings.Repeat("nested/", 40)+"file.go", 9)
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	if len(token) > maxSearchCursorEncodedBytes {
		t.Fatalf("cursor exceeded encoded bound: %d", len(token))
	}
	index, err := decodeSearchCursor(token, query)
	if err != nil || index != 17 {
		t.Fatalf("cursor round trip failed: index=%d err=%v", index, err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("decode cursor payload: %v", err)
	}
	var payload searchCursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal cursor payload: %v", err)
	}
	if !strings.HasPrefix(payload.LastPath, "h:") {
		t.Fatalf("long diagnostic path was not replaced by a bounded digest: %#v", payload)
	}

	t.Run("checksum", func(t *testing.T) {
		modified := payload
		modified.NextIndex++
		modifiedRaw, err := json.Marshal(modified)
		if err != nil {
			t.Fatalf("marshal modified payload: %v", err)
		}
		_, err = decodeSearchCursor(base64.RawURLEncoding.EncodeToString(modifiedRaw), query)
		var cursorErr *searchCursorError
		if !errors.As(err, &cursorErr) || cursorErr.Code != "checksum_mismatch" {
			t.Fatalf("checksum mutation was not rejected precisely: %v", err)
		}
	})

	t.Run("version", func(t *testing.T) {
		modified := payload
		modified.Version = searchCursorVersion + 1
		modified.Checksum, err = searchCursorChecksum(modified)
		if err != nil {
			t.Fatalf("checksum versioned payload: %v", err)
		}
		modifiedRaw, err := json.Marshal(modified)
		if err != nil {
			t.Fatalf("marshal versioned payload: %v", err)
		}
		_, err = decodeSearchCursor(base64.RawURLEncoding.EncodeToString(modifiedRaw), query)
		var cursorErr *searchCursorError
		if !errors.As(err, &cursorErr) || cursorErr.Code != "unsupported_version" {
			t.Fatalf("unsupported cursor version was not rejected precisely: %v", err)
		}
	})

	t.Run("oversize", func(t *testing.T) {
		_, err := decodeSearchCursor(strings.Repeat("A", maxSearchCursorEncodedBytes+1), query)
		var cursorErr *searchCursorError
		if !errors.As(err, &cursorErr) || cursorErr.Code != "token_too_large" {
			t.Fatalf("oversized cursor was not rejected precisely: %v", err)
		}
	})
}

func TestSearchLongPathReturnsBoundedTypedFailure(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	deep := workdir
	for index := 0; index < 7; index++ {
		deep = filepath.Join(deep, fmt.Sprintf("%02d-%s", index, strings.Repeat("p", 44)))
	}
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatalf("mkdir deep fixture: %v", err)
	}
	for index := 0; index < 2; index++ {
		if err := os.WriteFile(filepath.Join(deep, fmt.Sprintf("match-%d.txt", index)), []byte("needle\n"), 0o600); err != nil {
			t.Fatalf("write deep match: %v", err)
		}
	}
	result, err := registry.Execute(context.Background(), "grep_files", execCtx, json.RawMessage(`{"pattern":"needle","path":".","limit":100,"byte_limit":512}`))
	if err != nil {
		t.Fatalf("grep_files deep path: %v", err)
	}
	if !result.IsError || result.Metadata[MetadataFailureClass] != FailureClassSearchRecordTooLarge || result.Metadata["error_code"] != FailureClassSearchRecordTooLarge || len(result.LLMOutput) > 512 {
		t.Fatalf("long path did not return a bounded typed failure: output=%d result=%#v", len(result.LLMOutput), result)
	}
}

func TestGlobSearchCursorReturnsSourceContinuation(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	for index := 0; index < 5; index++ {
		if err := os.WriteFile(filepath.Join(workdir, fmt.Sprintf("glob-%02d.txt", index)), []byte("x"), 0o600); err != nil {
			t.Fatalf("write glob fixture: %v", err)
		}
	}
	first, err := registry.Execute(context.Background(), "glob", execCtx, json.RawMessage(`{"pattern":"*.txt","limit":2,"byte_limit":1024}`))
	if err != nil || first.IsError {
		t.Fatalf("glob first page: result=%#v err=%v", first, err)
	}
	if first.Metadata["has_more"] != true || metadataText(t, first.Metadata, "next_cursor") == "" || first.Metadata["artifact_path"] != nil {
		t.Fatalf("glob did not return a source cursor: %#v", first)
	}
	cursor := metadataText(t, first.Metadata, "next_cursor")
	second, err := registry.Execute(context.Background(), "glob", execCtx, json.RawMessage(fmt.Sprintf(`{"pattern":"*.txt","limit":2,"byte_limit":1024,"cursor":%q}`, cursor)))
	if err != nil || second.IsError {
		t.Fatalf("glob continuation: result=%#v err=%v", second, err)
	}
	if strings.Contains(second.LLMOutput, "glob-00.txt") || strings.Contains(second.LLMOutput, "glob-01.txt") {
		t.Fatalf("glob continuation repeated first page: %q", second.LLMOutput)
	}
}
