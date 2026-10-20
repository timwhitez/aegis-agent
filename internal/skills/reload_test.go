package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func reloadCatalogFixture(t *testing.T) (*Catalog, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "helpers", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: helpers\ndescription: stable\n---\nV1"), 0600); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(filepath.Dir(path), "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "check.yaml"), []byte("name: stable_check\ncommand: [echo, original]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Scan([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	return c, path
}

func TestReloadBodyPreservesCacheOnFailureAndRecovers(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "symlink", "parser", "name", "description", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			c, path := reloadCatalogFixture(t)
			if got, err := c.LoadBody("helpers"); err != nil || got != "V1" {
				t.Fatalf("initial body %q %v", got, err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(filepath.Dir(path), "other"), path); err != nil {
					t.Fatal(err)
				}
			default:
				text := "---\nname: helpers\ndescription: stable\n---\nV2"
				switch kind {
				case "parser":
					text = "---\nother: [invalid\n---\nV2"
				case "name":
					text = strings.Replace(text, "name: helpers", "name: changed", 1)
				case "description":
					text = strings.Replace(text, "description: stable", "description: changed", 1)
				}
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "unreadable" {
					if os.Geteuid() == 0 {
						t.Skip("root can read mode-000 files; permission-denied control unavailable")
					}
					if err := os.Chmod(path, 0); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := c.ReloadBody("helpers"); err == nil || !strings.Contains(err.Error(), "cached_body_retained=true") {
				t.Fatalf("failed refresh did not report retained cache: %v", err)
			}
			if body, err := c.LoadBody("helpers"); err != nil || body != "V1" {
				t.Fatalf("failure replaced cache: %q %v", body, err)
			}
			if kind == "name" || kind == "description" {
				if _, err := c.ReloadBody("helpers"); err == nil || !strings.Contains(err.Error(), "rescan catalog") {
					t.Fatalf("metadata drift not explained: %v", err)
				}
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("---\nname: helpers\ndescription: stable\n---\nV2"), 0600); err != nil {
				t.Fatal(err)
			}
			if body, err := c.ReloadBody("helpers"); err != nil || body != "V2" {
				t.Fatalf("restored refresh %q %v", body, err)
			}
			if body, err := c.ReloadBody("helpers"); err != nil || body != "V2" {
				t.Fatalf("unchanged refresh %q %v", body, err)
			}
			if got := c.CommandTools(); len(got) != 1 || got[0].Name != "stable_check" {
				t.Fatalf("tool registration changed: %+v", got)
			}
		})
	}
}

func TestReloadBodyCachesValidEmptyVersion(t *testing.T) {
	c, path := reloadCatalogFixture(t)
	if _, err := c.LoadBody("helpers"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: helpers\ndescription: stable\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := c.ReloadBody("helpers"); err != nil || got != "" {
		t.Fatalf("valid empty refresh %q %v", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := c.LoadBody("helpers"); err != nil || got != "" {
		t.Fatalf("empty cached version reread missing file: %q %v", got, err)
	}
	if _, err := c.ReloadBody("helpers"); err == nil || !strings.Contains(err.Error(), "cached_body_retained=true") {
		t.Fatalf("empty cache retention not reported: %v", err)
	}
}

func TestCatalogBodyLoadAndReloadShareSynchronizationWithAllReaders(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%t", force), func(t *testing.T) {
			c, path := reloadCatalogFixture(t)
			outside := t.TempDir()
			var wg sync.WaitGroup
			for reader := 0; reader < 6; reader++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < 100; i++ {
						s, err := c.Load("helpers")
						if err != nil || s.Name != "helpers" || s.Description != "stable" || (s.Body != "" && s.Body != "V1" && s.Body != "V2") {
							t.Errorf("partial skill %+v %v", s, err)
							return
						}
						if len(c.Summaries()) != 1 || len(c.CommandTools()) != 1 || len(c.Names()) != 1 {
							t.Error("catalog read changed identity")
							return
						}
						// This public method calls CommandTools; an outer lock would deadlock.
						_ = c.TrustedCommandTools(outside)
						body, err := c.LoadBody("helpers")
						if err != nil || (body != "V1" && body != "V2") {
							t.Errorf("partial body %q %v", body, err)
							return
						}
					}
				}()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 30; i++ {
					if !force {
						if _, err := c.LoadBody("helpers"); err != nil {
							t.Error(err)
						}
						continue
					}
					text := fmt.Sprintf("---\nname: helpers\ndescription: stable\n---\nV%d", i%2+1)
					temp := path + ".next"
					if err := os.WriteFile(temp, []byte(text), 0600); err != nil {
						t.Error(err)
						return
					}
					if err := os.Rename(temp, path); err != nil {
						t.Error(err)
						return
					}
					if _, err := c.ReloadBody("helpers"); err != nil {
						t.Error(err)
						return
					}
				}
			}()
			wg.Wait()
		})
	}
}
