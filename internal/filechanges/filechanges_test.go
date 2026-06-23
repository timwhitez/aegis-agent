package filechanges

import (
	"encoding/json"
	"testing"

	"go-cli-agent/internal/session"
)

func assistant(calls ...session.ToolCall) session.Message {
	return session.NewAssistantMessage("", "", calls)
}

func toolMsg(results ...session.ToolResult) session.Message {
	return session.NewToolMessage(results)
}

func call(id, name, args string) session.ToolCall {
	return session.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

func byPath(changes []FileChange) map[string]FileChange {
	out := map[string]FileChange{}
	for _, c := range changes {
		out[c.Path] = c
	}
	return out
}

func TestFromMessagesCountsOnlySuccessfulOperations(t *testing.T) {
	messages := []session.Message{
		assistant(
			call("c1", "write_file", `{"path":"reports/a.md","content":"one\ntwo"}`),
			call("c2", "edit_file", `{"path":"reports/b.md","old_text":"x","new_text":"missing"}`),
		),
		toolMsg(
			session.ToolResult{ToolCallID: "c1", Name: "write_file"},
			session.ToolResult{ToolCallID: "c2", Name: "edit_file", IsError: true, LLMOutput: "Error: old_text not found"},
		),
	}
	got := byPath(FromMessages("", messages))
	if a, ok := got["reports/a.md"]; !ok || a.Writes != 1 || a.LinesAdded != 2 {
		t.Fatalf("expected successful write counted, got %#v", got)
	}
	if _, ok := got["reports/b.md"]; ok {
		t.Fatalf("failed edit must not be counted, got %#v", got)
	}
}

func TestFromMessagesIgnoresUnresolvedAndPendingCalls(t *testing.T) {
	// A tool call with no matching result (interrupted/dangling) must not count.
	messages := []session.Message{
		assistant(call("c1", "write_file", `{"path":"reports/a.md","content":"one"}`)),
	}
	if got := FromMessages("", messages); len(got) != 0 {
		t.Fatalf("expected no changes for unresolved call, got %#v", got)
	}
}

func TestFromMessagesResolvesShellRedirectAgainstWorkdir(t *testing.T) {
	// A child workdir with a parent-relative redirect must normalize to the same
	// workspace-relative path the file actually lives at.
	messages := []session.Message{
		assistant(call("c1", "shell", `{"command":"echo hi > ../reports/out.txt","workdir":"rancher"}`)),
		toolMsg(session.ToolResult{ToolCallID: "c1", Name: "shell"}),
	}
	got := byPath(FromMessages("/work", messages))
	if _, ok := got["reports/out.txt"]; !ok {
		t.Fatalf("expected redirect resolved to reports/out.txt, got %#v", got)
	}
	if _, ok := got["../reports/out.txt"]; ok {
		t.Fatalf("raw relative path must not survive normalization, got %#v", got)
	}
}

func TestFromMessagesPrefersResolvedMetadataPath(t *testing.T) {
	// write_file/edit_file results carry the resolved absolute path in metadata;
	// it should normalize back to the workspace-relative path.
	messages := []session.Message{
		assistant(call("c1", "write_file", `{"path":"a.md","content":"x"}`)),
		toolMsg(session.ToolResult{
			ToolCallID: "c1",
			Name:       "write_file",
			Metadata:   map[string]any{"path": "/work/sub/a.md"},
		}),
	}
	got := byPath(FromMessages("/work", messages))
	if _, ok := got["sub/a.md"]; !ok {
		t.Fatalf("expected metadata path normalized to sub/a.md, got %#v", got)
	}
}

func TestFromCallReturnsNilForFailedOrNonMutating(t *testing.T) {
	failed := FromCall("/work", call("c1", "write_file", `{"path":"a.md","content":"x"}`),
		session.ToolResult{ToolCallID: "c1", Name: "write_file", IsError: true})
	if failed != nil {
		t.Fatalf("expected nil for failed call, got %#v", failed)
	}
	nonmut := FromCall("/work", call("c2", "read_file", `{"path":"a.md"}`),
		session.ToolResult{ToolCallID: "c2", Name: "read_file"})
	if nonmut != nil {
		t.Fatalf("expected nil for non-mutating call, got %#v", nonmut)
	}
}

func TestMergeSumsCountsAndPreservesOrder(t *testing.T) {
	base := []FileChange{{Path: "a", Writes: 1, LinesAdded: 2}}
	merged := Merge(base, []FileChange{
		{Path: "a", Edits: 1, LinesAdded: 3},
		{Path: "b", Writes: 1},
	})
	got := byPath(merged)
	if a := got["a"]; a.Writes != 1 || a.Edits != 1 || a.LinesAdded != 5 {
		t.Fatalf("expected summed counts for a, got %#v", a)
	}
	if b := got["b"]; b.Writes != 1 {
		t.Fatalf("expected new path b, got %#v", b)
	}
	if len(merged) != 2 || merged[0].Path != "a" || merged[1].Path != "b" {
		t.Fatalf("expected order [a b], got %#v", merged)
	}
	// Merge must not mutate base.
	if base[0].LinesAdded != 2 {
		t.Fatalf("merge mutated base: %#v", base)
	}
}
