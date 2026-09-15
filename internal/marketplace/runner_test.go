package marketplace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarketplaceRunUnmanagedHomeIsNoop(t *testing.T) {
	if err := Run(context.Background(), Request{HostHome: t.TempDir(), CodexHome: t.TempDir(), CodexArgv: []string{"not-codex"}}); err != nil {
		t.Fatal(err)
	}
}

func TestMarketplaceRunNoEligibleReceipt(t *testing.T) {
	f := newFixture(t)
	profile := t.TempDir()
	writeTest(t, filepath.Join(profile, "config.toml"), "unknown = 'preserve'\n")
	if err := Run(context.Background(), Request{HostHome: f.home, CodexHome: profile, CodexArgv: approvedTestArgv}); err != nil {
		t.Fatal(err)
	}
	entries, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(entries) != 0 {
		t.Fatalf("ineligible profile has success receipts: %v", entries)
	}
}

func TestMarketplaceRunReceiptRejectsAliases(t *testing.T) {
	home, profile := t.TempDir(), t.TempDir()
	revision := strings.Repeat("a", 40)
	if err := saveProfileReceipt(home, profile, approvedTestArgv, revision); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(StateDir(home), "profiles", "*.json"))
	if len(paths) != 1 {
		t.Fatalf("receipt count %d", len(paths))
	}
	victim := filepath.Join(home, "victim")
	writeTest(t, victim, "preserve")
	if err := os.Remove(paths[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, paths[0]); err != nil {
		t.Fatal(err)
	}
	_ = saveProfileReceipt(home, profile, approvedTestArgv, revision)
	got, _ := os.ReadFile(victim)
	if string(got) != "preserve" {
		t.Fatal("receipt followed symlink")
	}
}

func managedNativeFixture(t *testing.T) (fixture, string) {
	t.Helper()
	f := newFixture(t)
	profile, checkout := nativeFixture(t)
	if err := os.CopyFS(f.source, os.DirFS(checkout.Path)); err != nil {
		t.Fatal(err)
	}
	gitTest(t, f.source, "add", ".")
	gitTest(t, f.source, "commit", "-m", "native fixture")
	cfg, _ := os.ReadFile(filepath.Join(profile, "config.toml"))
	writeTest(t, filepath.Join(profile, "config.toml"), strings.ReplaceAll(string(cfg), checkout.Path, f.path))
	return f, profile
}

func TestMarketplaceRunNativeFailureBacksOff(t *testing.T) {
	f, profile := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", profile)
	writeTest(t, filepath.Join(profile, "mode"), "fail-second")
	req := Request{HostHome: f.home, CodexHome: profile, CodexArgv: approvedTestArgv}
	_ = Run(context.Background(), req)
	before, _ := os.ReadFile(filepath.Join(profile, "calls"))
	if len(before) == 0 {
		t.Fatal("failure fixture did not run")
	}
	_ = Run(context.Background(), req)
	after, _ := os.ReadFile(filepath.Join(profile, "calls"))
	if string(before) != string(after) {
		t.Fatal("native failure was retried without backoff")
	}
	paths, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(paths) != 0 {
		t.Fatal("failed native refresh wrote receipt")
	}
	log, _ := os.ReadFile(filepath.Join(StateDir(f.home), "update.log"))
	if !strings.Contains(string(log), "callback_failure") || strings.Contains(string(log), "secret") {
		t.Fatalf("failure log: %s", log)
	}
}

func TestMarketplaceRunSecondProfileAndAlias(t *testing.T) {
	f, first := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", first)
	req := Request{HostHome: f.home, CodexHome: first, CodexArgv: approvedTestArgv}
	_ = Run(context.Background(), req)
	second := t.TempDir()
	if err := os.CopyFS(second, os.DirFS(first)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(second, "calls")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", second)
	req.CodexHome = second
	_ = Run(context.Background(), req)
	calls, _ := os.ReadFile(filepath.Join(second, "calls"))
	if string(calls) != "agent-deck\nagent-deck-mcp\n" {
		t.Fatalf("second profile skipped: %q", calls)
	}
	paths, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(paths) != 2 {
		t.Fatalf("profile receipt count: %d", len(paths))
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", alias)
	req.CodexHome = alias
	_ = Run(context.Background(), req)
	paths, _ = filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(paths) != 2 {
		t.Fatalf("alias receipt count: %d", len(paths))
	}
	fetches, _ := os.ReadFile(filepath.Join(f.home, "fetches"))
	if strings.Count(string(fetches), "fetch") != 1 {
		t.Fatalf("checkout not coalesced: %s", fetches)
	}
}

func TestMarketplaceRunGitFailurePreventsNative(t *testing.T) {
	f, profile := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", profile)
	writeTest(t, filepath.Join(f.home, "git-mode"), "fail")
	_ = Run(context.Background(), Request{HostHome: f.home, CodexHome: profile, CodexArgv: approvedTestArgv})
	if _, err := os.Stat(filepath.Join(profile, "calls")); !os.IsNotExist(err) {
		t.Fatal("native executed after Git failure")
	}
	paths, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(paths) != 0 {
		t.Fatal("Git failure receipt")
	}
}

func TestMarketplaceRunReceiptCoalescesVerifiedProfile(t *testing.T) {
	f, profile := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", profile)
	req := Request{HostHome: f.home, CodexHome: profile, CodexArgv: approvedTestArgv}
	_ = Run(context.Background(), req)
	before, _ := os.ReadFile(filepath.Join(profile, "calls"))
	if len(before) == 0 {
		t.Fatal("initial refresh missing")
	}
	_ = Run(context.Background(), req)
	after, _ := os.ReadFile(filepath.Join(profile, "calls"))
	if string(before) != string(after) {
		t.Fatal("verified same-profile receipt did not coalesce native writes")
	}
}
