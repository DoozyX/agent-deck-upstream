package session

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Removing auth provisioning from ApplyConfiguredLoadout must fail this test:
// isolated group homes would again launch without the main home's login.
func TestConfiguredLoadoutCodexSharedAuth(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := withIsolatedHomeAndConfig(t, `
[codex]
shared_auth_source = "~/.codex/auth.json"
[groups.team.codex]
config_dir = "~/.agent-deck/codex/team"
`)
	source := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"test":"initial"}`), 0600); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithGroupAndTool("shared-auth", t.TempDir(), "team/child", "codex")
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatal(warnings)
	}
	dest := filepath.Join(home, ".agent-deck", "codex", "team", "auth.json")
	if link, err := os.Readlink(dest); err != nil || link != source {
		t.Fatalf("group credentials must link to shared login: link=%q err=%v", link, err)
	}
	// An atomic refresh at the source must be visible without another sync.
	if err := os.WriteFile(source+".new", []byte(`{"test":"refreshed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source+".new", source); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != `{"test":"refreshed"}` {
		t.Fatalf("refresh not shared: %s %v", data, err)
	}
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatal(warnings)
	}
}

func TestCodexSharedAuthPreservesExistingCredentials(t *testing.T) {
	for _, kind := range []string{"file", "foreign-link", "broken-link", "directory"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			source := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(source, []byte("shared"), 0600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(home, "auth.json")
			switch kind {
			case "file":
				if err := os.WriteFile(dest, []byte("independent"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(dest, 0700); err != nil {
					t.Fatal(err)
				}
			default:
				foreign := filepath.Join(t.TempDir(), "auth.json")
				if kind == "foreign-link" {
					if err := os.WriteFile(foreign, []byte("independent"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(foreign, dest); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyCodexSharedAuth(home, source); err == nil {
				t.Fatal("must refuse existing credentials")
			}
			after, err := os.Lstat(dest)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("existing credentials changed: %v", err)
			}
		})
	}
}

func TestCodexSharedAuthChecksStorageEvenWithExistingLink(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(source, []byte("shared"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("cli_auth_credentials_store = 'keyring'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applyCodexSharedAuth(home, source); err == nil || !strings.Contains(err.Error(), "cli_auth_credentials_store") {
		t.Fatalf("must report ignored credential link: %v", err)
	}
}

func TestCodexSharedAuthConcurrentProvisioning(t *testing.T) {
	home := filepath.Join(t.TempDir(), "new-group")
	source := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(source, []byte("shared"), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := applyCodexSharedAuth(home, source); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if target, err := os.Readlink(filepath.Join(home, "auth.json")); err != nil || target != source {
		t.Fatalf("link=%q err=%v", target, err)
	}
}

func TestConfiguredLoadoutCodexSharedAuthSkipsAccountAndOptOut(t *testing.T) {
	for _, account := range []string{"", "work-account"} {
		t.Run(account, func(t *testing.T) {
			t.Setenv("CODEX_HOME", "")
			setting := ""
			if account != "" {
				setting = "[codex]\nshared_auth_source = '~/.codex/auth.json'\n"
			}
			home := withIsolatedHomeAndConfig(t, setting+"[groups.team.codex]\nconfig_dir = '~/.agent-deck/codex/team'\n")
			source := filepath.Join(home, ".codex", "auth.json")
			if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("shared"), 0600); err != nil {
				t.Fatal(err)
			}
			inst := NewInstanceWithGroupAndTool("isolated", t.TempDir(), "team", "codex")
			inst.Account = account
			if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
				t.Fatal(warnings)
			}
			if _, err := os.Lstat(filepath.Join(home, ".agent-deck/codex/team/auth.json")); !os.IsNotExist(err) {
				t.Fatalf("credentials unexpectedly shared: %v", err)
			}
		})
	}
}
