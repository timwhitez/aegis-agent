package webconsole

import (
	"aegis-agent/internal/session"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserWebAssemblyAndReadOnlySettings(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			if !bytes.Contains(data, []byte(`"name":"browser_exec"`)) {
				t.Error("Web registry lacks browser tool")
			}
			io.WriteString(w, `{"id":"one","status":"completed","output":[{"type":"function_call","call_id":"web-browser","name":"browser_exec","arguments":"{\"code\":\"print('WEB-HEAD'+'x'*60000+'WEB-MIDDLE'+'y'*60000+'WEB-TAIL')\"}"}]}`)
			return
		}
		if !bytes.Contains(data, []byte("Complete artifact:")) || !bytes.Contains(data, []byte("process completed")) {
			t.Error("Web provider did not receive finalized browser artifact")
		}
		io.WriteString(w, `{"id":"two","status":"completed","output":[{"type":"function_call","call_id":"finish","name":"finish","arguments":"{\"message\":\"done\"}"}]}`)
	}))
	defer provider.Close()
	cfg := testConfig(t, provider.URL)
	cfg.Runtime.CommandTimeoutSec = 10
	cfg.Tools.Browser.Enabled = true
	cfg.Tools.Browser.InstallRoot = t.TempDir()
	fake, err := os.ReadFile("../tools/testdata/browser_fake.py")
	if err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(cfg.Tools.Browser.InstallRoot, "bin"), 0700)
	for _, p := range []string{filepath.Join(cfg.Tools.Browser.InstallRoot, "bin", "python"), filepath.Join(cfg.Tools.Browser.InstallRoot, "chrome")} {
		if err := os.WriteFile(p, fake, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Tools.Browser.BrowserExecutable = filepath.Join(cfg.Tools.Browser.InstallRoot, "chrome")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, word := range []string{`"browser"`, `"mode":"local"`, `"image_delivery":"ref-only"`, cfg.Tools.Browser.InstallRoot, "host_sandbox"} {
		if !bytes.Contains(body, []byte(word)) {
			t.Fatal(string(body), word)
		}
	}
	var launched LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/start", map[string]any{"prompt": "Use optional browser.", "mode": "exec", "workdir": t.TempDir()}, http.StatusAccepted, &launched)
	waitFor(t, 15*time.Second, func() bool {
		state, err := svc.store.LoadState(launched.SessionID)
		return err == nil && state.Status == session.StatusCompleted
	}, func() string { state, err := svc.store.LoadState(launched.SessionID); return fmt.Sprint(state, err) })
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	// Settings browser block is observational only, never a write/install entry.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/api/config", bytes.NewBufferString(`{"browser":{"enabled":false}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", ts.URL)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.StatusCode)
	}
}
