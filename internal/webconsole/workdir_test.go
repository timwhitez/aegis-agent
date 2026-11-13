package webconsole

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Darwin getcwd(3) keeps returning the stale path of a removed working
// directory; the server must fail like Linux instead of resolving (and
// MkdirAll-recreating) a default workspace below that deleted tree.
func TestServerWorkspaceRootRejectsRemovedWorkingDirectory(t *testing.T) {
	svc, err := New(testConfig(t, ""), Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	cwd := filepath.Join(t.TempDir(), "cwd")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Chdir(cwd)
	if err := os.Remove(cwd); err != nil {
		t.Fatalf("remove cwd: %v", err)
	}
	assertNotRecreated := func(step string) {
		t.Helper()
		if _, err := os.Lstat(cwd); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s recreated the removed working directory: %v", step, err)
		}
	}

	if root, err := currentServerWorkspaceRoot(); err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("currentServerWorkspaceRoot() = %q, %v; want not-exist error", root, err)
	}
	assertNotRecreated("currentServerWorkspaceRoot")
	if root, err := currentServerBrowseRoot(); err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("currentServerBrowseRoot() = %q, %v; want not-exist error", root, err)
	}
	if dir, err := resolveSkillDir("skills"); err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("resolveSkillDir() = %q, %v; want not-exist error", dir, err)
	}

	recorder := httptest.NewRecorder()
	svc.ServeHTTP(recorder, newLocalWebRequest(http.MethodGet, "/api/files", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/files with a removed cwd: status %d body=%s", recorder.Code, recorder.Body.String())
	}
	assertNotRecreated("GET /api/files")
}
