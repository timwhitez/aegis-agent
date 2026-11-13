package webconsole

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"aegis-agent/internal/session"
)

func newTitleTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := New(testConfig(t, ""), Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func createTitleTestSession(t *testing.T, svc *Service, id string, messages ...session.Message) {
	t.Helper()
	meta := testSessionMetadata(t, id)
	meta.RootSessionID = id
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create session %s: %v", id, err)
	}
	for _, message := range messages {
		if err := svc.store.AppendMessage(id, message); err != nil {
			t.Fatalf("append message to %s: %v", id, err)
		}
	}
}

func userMessageWithMeta(text string, meta map[string]any) session.Message {
	message := session.NewMessage("user", text)
	message.Meta = meta
	return message
}

// titlesFromEndpoint returns id -> title (and whether the key was present) for
// the summaries served by one of the list endpoints.
func titlesFromEndpoint(t *testing.T, svc *Service, path string) map[string]*string {
	t.Helper()
	recorder := httptest.NewRecorder()
	svc.ServeHTTP(recorder, newLocalWebRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d body=%s", path, recorder.Code, recorder.Body.String())
	}
	var items []map[string]any
	switch {
	case strings.HasPrefix(path, "/api/overview"):
		var payload struct {
			RecentSessions []map[string]any `json:"recent_sessions"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		items = payload.RecentSessions
	case strings.HasPrefix(path, "/api/history"):
		var payload struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if payload.Total != len(payload.Items) {
			t.Fatalf("GET %s: expected every session on one page, total=%d items=%d", path, payload.Total, len(payload.Items))
		}
		items = payload.Items
	default:
		if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	out := map[string]*string{}
	for _, item := range items {
		id, _ := item["id"].(string)
		if id == "" {
			t.Fatalf("GET %s: summary without id: %#v", path, item)
		}
		if _, ok := item["status"]; !ok {
			t.Fatalf("GET %s: summary lost embedded fields: %#v", path, item)
		}
		if raw, ok := item["title"]; ok {
			title, _ := raw.(string)
			out[id] = &title
		} else {
			out[id] = nil
		}
	}
	return out
}

var titleEndpoints = []string{"/api/overview", "/api/sessions?limit=50", "/api/history?page=1&page_size=50"}

func TestSessionTitleUsesFirstUserMessageOnListEndpoints(t *testing.T) {
	svc := newTitleTestService(t)
	createTitleTestSession(t, svc, "title_first_prompt",
		userMessageWithMeta("Harness reminder before the prompt", map[string]any{"source": "harness_reminder", "kind": "startup"}),
		session.NewMessage("user", "  Fix   the\n\tlogin bug  "),
		session.NewAssistantMessage("On it.", "", nil),
		session.NewMessage("user", "A later follow-up"),
	)
	for _, path := range titleEndpoints {
		titles := titlesFromEndpoint(t, svc, path)
		got := titles["title_first_prompt"]
		if got == nil || *got != "Fix the login bug" {
			t.Fatalf("GET %s: expected collapsed first prompt title, got %v", path, got)
		}
	}
}

func TestSessionTitleSkipsHarnessAndControlMessages(t *testing.T) {
	svc := newTitleTestService(t)
	createTitleTestSession(t, svc, "title_skips_kinds",
		session.NewAssistantMessage("assistant text is never a title", "", nil),
		userMessageWithMeta("steer text", map[string]any{"source": "steer", "interrupt": true}),
		userMessageWithMeta("background results", map[string]any{"source": "background_results"}),
		userMessageWithMeta("compaction summary", map[string]any{"source": "compaction_summary"}),
		userMessageWithMeta("plan approval", map[string]any{"source": "planmode_approval"}),
		userMessageWithMeta("kind-only reminder", map[string]any{"kind": "longrun_checkpoint"}),
		session.NewMessage("user", "Operator prompt"),
	)
	createTitleTestSession(t, svc, "title_only_harness",
		userMessageWithMeta("only a reminder", map[string]any{"source": "harness_reminder"}),
	)
	titles := titlesFromEndpoint(t, svc, "/api/overview")
	if got := titles["title_skips_kinds"]; got == nil || *got != "Operator prompt" {
		t.Fatalf("expected the operator prompt after skipped messages, got %v", got)
	}
	if got, ok := titles["title_only_harness"]; !ok || got != nil {
		t.Fatalf("expected title to be omitted when only harness messages exist, got %v (present=%v)", got, ok)
	}
}

func TestSessionTitleTruncatesLongPromptsByRunes(t *testing.T) {
	svc := newTitleTestService(t)
	long := strings.Repeat("修复登录页面", 40) // 240 runes, 3 bytes each
	createTitleTestSession(t, svc, "title_truncated", session.NewMessage("user", long))
	title := svc.sessionTitle("title_truncated")
	if got := utf8.RuneCountInString(title); got != sessionTitleMaxRunes {
		t.Fatalf("expected %d runes, got %d (%q)", sessionTitleMaxRunes, got, title)
	}
	if !strings.HasSuffix(title, "…") || !strings.HasPrefix(title, "修复登录页面") || !utf8.ValidString(title) {
		t.Fatalf("unexpected truncated title %q", title)
	}
	if got := normalizeSessionTitle("short title"); got != "short title" {
		t.Fatalf("short titles must stay intact, got %q", got)
	}
}

func TestSessionTitleOmittedWhenMessagesAreMissing(t *testing.T) {
	svc := newTitleTestService(t)
	createTitleTestSession(t, svc, "title_no_messages")
	if err := os.Remove(filepath.Join(svc.store.SessionDir("title_no_messages"), "messages.jsonl")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove messages.jsonl: %v", err)
	}
	for _, path := range titleEndpoints {
		titles := titlesFromEndpoint(t, svc, path)
		got, ok := titles["title_no_messages"]
		if !ok || got != nil {
			t.Fatalf("GET %s: expected the session without a title key, got %v (present=%v)", path, got, ok)
		}
	}
	if got := readSessionTitle(filepath.Join(t.TempDir(), "missing.jsonl"), sessionTitleScanMaxBytes); got != "" {
		t.Fatalf("missing file must yield an empty title, got %q", got)
	}
}

func TestSessionTitleCachesOnlyFoundTitles(t *testing.T) {
	svc := newTitleTestService(t)
	createTitleTestSession(t, svc, "title_cached")
	if got := svc.sessionTitle("title_cached"); got != "" {
		t.Fatalf("expected no title before the first message, got %q", got)
	}
	if _, ok := svc.titles.get("title_cached"); ok {
		t.Fatal("an empty title must not be cached")
	}
	if err := svc.store.AppendMessage("title_cached", session.NewMessage("user", "First prompt")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := svc.sessionTitle("title_cached"); got != "First prompt" {
		t.Fatalf("expected the title once the message is published, got %q", got)
	}
	// Replace the transcript on disk: the cached title must be served without re-reading.
	replacement, err := json.Marshal(session.NewMessage("user", "Rewritten prompt"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(svc.store.SessionDir("title_cached"), "messages.jsonl")
	if err := os.WriteFile(path, append(replacement, '\n'), 0o600); err != nil {
		t.Fatalf("rewrite messages: %v", err)
	}
	if got := svc.sessionTitle("title_cached"); got != "First prompt" {
		t.Fatalf("expected the cached title, got %q", got)
	}

	cache := newSessionTitleCache(2)
	cache.put("a", "A")
	cache.put("b", "B")
	cache.put("a", "A2")
	cache.put("c", "C")
	if _, ok := cache.get("a"); ok {
		t.Fatal("expected the oldest entry to be evicted at capacity")
	}
	if got, _ := cache.get("b"); got != "B" {
		t.Fatalf("expected b to survive, got %q", got)
	}
	if got, _ := cache.get("c"); got != "C" || cache.size() != 2 {
		t.Fatalf("expected a bounded cache of 2, size=%d c=%q", cache.size(), got)
	}
	cache.put("d", "")
	if _, ok := cache.get("d"); ok {
		t.Fatal("empty titles must not be cached")
	}
	var nilCache *sessionTitleCache
	nilCache.put("x", "X")
	if _, ok := nilCache.get("x"); ok || nilCache.size() != 0 {
		t.Fatal("a nil cache must be a no-op")
	}
}

func TestSessionTitleBoundedRead(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, records ...any) string {
		t.Helper()
		var builder strings.Builder
		for _, record := range records {
			switch value := record.(type) {
			case string:
				builder.WriteString(value)
			default:
				line, err := json.Marshal(value)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				builder.Write(line)
				builder.WriteByte('\n')
			}
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	// The first user message lies beyond the scan window: no title, no full read.
	big := session.NewAssistantMessage(strings.Repeat("x", 300<<10), "", nil)
	beyond := write("beyond.jsonl", big, session.NewMessage("user", "Too late"))
	if got := readSessionTitle(beyond, sessionTitleScanMaxBytes); got != "" {
		t.Fatalf("expected no title beyond the scan window, got %q", got)
	}
	// The same prompt is found when the window covers it.
	if got := readSessionTitle(beyond, 1<<20); got != "Too late" {
		t.Fatalf("expected the prompt inside a larger window, got %q", got)
	}

	// A record cut by the window is ignored rather than half-parsed.
	first := session.NewMessage("user", "Cut prompt")
	line, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cut := write("cut.jsonl", first)
	if got := readSessionTitle(cut, int64(len(line)-4)); got != "" {
		t.Fatalf("expected a cut record to be ignored, got %q", got)
	}
	if got := readSessionTitle(cut, int64(len(line)+1)); got != "Cut prompt" {
		t.Fatalf("expected the complete record to be read, got %q", got)
	}

	// Malformed and partial trailing records do not fail the scan.
	partial := write("partial.jsonl", "{not json}\n", session.NewMessage("user", "After garbage"), "{\"role\":\"user\",\"text\":\"half")
	if got := readSessionTitle(partial, sessionTitleScanMaxBytes); got != "After garbage" {
		t.Fatalf("expected the prompt after a malformed record, got %q", got)
	}
	blank := write("blank.jsonl", "{\"role\":\"user\",\"text\":\"  \\n \"}\n", session.NewMessage("user", "After blank"))
	if got := readSessionTitle(blank, sessionTitleScanMaxBytes); got != "After blank" {
		t.Fatalf("expected blank user text to be skipped, got %q", got)
	}
	trailing := write("trailing.jsonl", "{\"role\":\"user\",\"text\":\"half")
	if got := readSessionTitle(trailing, sessionTitleScanMaxBytes); got != "" {
		t.Fatalf("expected a partial trailing record to be ignored, got %q", got)
	}

	// Symlinked transcripts are refused.
	target := write("target.jsonl", session.NewMessage("user", "Linked"))
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if got := readSessionTitle(link, sessionTitleScanMaxBytes); got != "" {
		t.Fatalf("expected symlinked transcripts to be refused, got %q", got)
	}
}
