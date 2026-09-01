package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDedupeAndSortAccountsNormalizesAndUsesFirstLabel(t *testing.T) {
	accounts := DedupeAndSortAccounts([]Account{
		{Provider: Codex, Home: "/tmp/z/../work", Label: "Zeta"},
		{Provider: Claude, Home: "/tmp/claude", Label: "work"},
		{Provider: Codex, Home: "/tmp/work", Label: "alpha"},
	})
	if len(accounts) != 2 {
		t.Fatalf("accounts = %#v", accounts)
	}
	if accounts[0].Provider != Claude || accounts[1].Provider != Codex || accounts[1].Label != "alpha" || accounts[1].Home != "/tmp/work" {
		t.Fatalf("accounts = %#v, want normalized provider/label order", accounts)
	}
}

func TestParseNormalizesClaudeWindowsAndIgnoresFutureFields(t *testing.T) {
	s, err := Parse("claude", []byte(`{"plan":"Max","limits":{"five_hour":{"remaining_percent":82,"resets_at":"2026-08-31T15:45:00Z"},"weekly":{"remaining_percent":61,"resets_at":"2026-09-07T10:45:00Z"}},"future":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Available || s.Windows.Session5H == nil || s.Windows.Session5H.RemainingPercent != 82 || s.Windows.Weekly == nil || s.Windows.Weekly.RemainingPercent != 61 {
		t.Fatalf("snapshot = %#v", s)
	}
}

func TestParseOpenUsageLimitsV1ProviderResources(t *testing.T) {
	claude, err := Parse(Claude, []byte(`{"schema":"openusage.limits.v1","providers":{"claude":{"plan":"Max","stale":false,"resources":{"session":{"kind":"consumption","remaining":76,"resetsAt":"2026-09-01T09:00:00Z"},"weekly":{"kind":"consumption","remaining":75,"resetsAt":"2026-09-03T08:00:00Z"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if claude.Plan != "Max" || claude.Stale || claude.Windows.Session5H == nil || claude.Windows.Session5H.RemainingPercent != 76 || claude.Windows.Session5H.ResetsAt.IsZero() || claude.Windows.Weekly == nil || claude.Windows.Weekly.RemainingPercent != 75 || claude.Windows.Weekly.ResetsAt.IsZero() {
		t.Fatalf("Claude snapshot = %#v", claude)
	}

	codex, err := Parse(Codex, []byte(`{"schema":"openusage.limits.v1","providers":{"codex":{"plan":"Pro","resources":{"weekly":{"kind":"consumption","remaining":83,"resetsAt":"2026-09-07T06:59:05Z"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if codex.Plan != "Pro" || codex.Windows.Session5H != nil || codex.Windows.Weekly == nil || codex.Windows.Weekly.RemainingPercent != 83 {
		t.Fatalf("Codex snapshot = %#v", codex)
	}
}

func TestParseRejectsMalformedJSONAndResponsesWithoutWindows(t *testing.T) {
	if _, err := Parse(Claude, []byte("{")); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
	if _, err := Parse(Codex, []byte(`{"plan":"Pro"}`)); err == nil {
		t.Fatal("response without supported windows was accepted")
	}
}

func TestParseCodexWeeklyOnlyAndStale(t *testing.T) {
	s, err := Parse(Codex, []byte(`{"stale":true,"windows":{"weekly":{"remaining":44}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Stale || s.Windows.Weekly == nil || s.Windows.Weekly.RemainingPercent != 44 || s.Windows.Session5H != nil {
		t.Fatalf("snapshot = %#v", s)
	}
}

func TestRunUsesFixedProviderAndHome(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "openusage")
	log := filepath.Join(dir, "args")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s|%s' \"$1\" \"$CLAUDE_CONFIG_DIR\" > \"$OPENUSAGE_LOG\"\nprintf '{\\\"limits\\\":{\\\"weekly\\\":{\\\"remaining_percent\\\":55}}}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENUSAGE_LOG", log)
	r := Runner{Path: bin, Timeout: time.Second}
	s, err := r.Query(context.Background(), Account{Provider: Claude, Home: "/tmp/claude", Label: "Personal"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(log); string(got) != "claude|/tmp/claude" {
		t.Fatalf("arguments/environment = %q", got)
	}
	if s.Windows.Weekly == nil || s.Windows.Weekly.RemainingPercent != 55 {
		t.Fatalf("snapshot = %#v", s)
	}
}

func TestRunTimeoutIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "openusage")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := (Runner{Path: bin, Timeout: 10 * time.Millisecond}).Query(context.Background(), Account{Provider: Codex})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
