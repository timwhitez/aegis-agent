package app

import (
	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
	"aegis-agent/internal/tools"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBrowserCLIAssemblyAndDoctor(t *testing.T) {
	fixturePath, _ := filepath.Abs("../tools/testdata/browser_fake.py")
	guidanceIsolatedEnvironment(t, t.TempDir())
	work := t.TempDir()
	guidanceWorkingDirectory(t, work)
	cfg := config.Default()
	cfg.Tools.Browser.Enabled = true
	cfg.Tools.Browser.InstallRoot = t.TempDir()
	cfg.Runtime.Queue.AutoWorker = false
	cfg.Skills.Dirs = nil
	cfg.Session.Dir = filepath.Join(t.TempDir(), "sessions")
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(cfg.Tools.Browser.InstallRoot, "bin"), 0700)
	for _, p := range []string{filepath.Join(cfg.Tools.Browser.InstallRoot, "bin", "python"), filepath.Join(cfg.Tools.Browser.InstallRoot, "chrome")} {
		os.WriteFile(p, fixture, 0700)
	}
	cfg.Tools.Browser.BrowserExecutable = filepath.Join(cfg.Tools.Browser.InstallRoot, "chrome")
	// Outside Linux x86_64 (spec 19) the adapter is still assembled, but every
	// call and the doctor must report the unsupported platform explicitly.
	platformErr := tools.BrowserPlatformError()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			if !bytes.Contains(data, []byte(`"name":"browser_exec"`)) {
				t.Error("CLI registry assembly lacks browser dispatch")
			}
			args, _ := json.Marshal(map[string]string{"code": "print('CLI-HEAD'+'x'*60000+'CLI-MIDDLE'+'y'*60000+'CLI-TAIL')"})
			fmt.Fprintf(w, `{"id":"one","status":"completed","output":[{"type":"function_call","call_id":"browser-call","name":"browser_exec","arguments":%q}]}`, args)
			return
		}
		if platformErr != nil {
			if !bytes.Contains(data, []byte("browser platform unsupported")) {
				t.Errorf("CLI next wire request lacks unsupported-platform diagnostic on %s", runtime.GOOS)
			}
		} else if !bytes.Contains(data, []byte("Complete artifact:")) || !bytes.Contains(data, []byte("process completed")) {
			t.Error("CLI next wire request lacks bounded result/artifact")
		}
		io.WriteString(w, `{"id":"two","status":"completed","output":[{"type":"function_call","call_id":"finish","name":"finish","arguments":"{\"message\":\"done\"}"}]}`)
	}))
	defer server.Close()
	p := cfg.Providers["openai"]
	p.BaseURL = server.URL
	p.APIKeyEnv = ""
	cfg.Providers["openai"] = p
	raw, _ := yaml.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, raw, 0600)
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"exec", "--config", path, "--json", "Use optional browser."}, &stdout, io.Discard); err != nil {
		t.Fatal(err, stdout.String())
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	stdout.Reset()
	doctorErr := Run(context.Background(), []string{"doctor", "--config", path, "--skip-probe", "--json"}, &stdout, io.Discard)
	if platformErr != nil {
		// The failed browser check fails doctor before any browser process runs.
		if doctorErr == nil || !strings.Contains(stdout.String(), `"name":"browser"`) || !strings.Contains(stdout.String(), `"status":"unsupported_platform"`) || strings.Contains(stdout.String(), "Fake Chrome 136") {
			t.Fatalf("doctor on %s: err=%v output=%s", runtime.GOOS, doctorErr, stdout.String())
		}
	} else {
		if doctorErr != nil {
			t.Fatal(doctorErr)
		}
		if !strings.Contains(stdout.String(), `"name":"browser"`) || !strings.Contains(stdout.String(), "Fake Chrome 136") {
			t.Fatal(stdout.String())
		}
	}
	records, err := session.NewStore(cfg.Session.Dir).List(10)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
}
