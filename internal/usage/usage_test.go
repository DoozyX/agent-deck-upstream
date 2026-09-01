package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseNormalizesClaudeWindowsAndIgnoresFutureFields(t *testing.T) {
	s, err := Parse("claude", []byte(`{"plan":"Max","limits":{"five_hour":{"remaining_percent":82,"resets_at":"2026-08-31T15:45:00Z"},"weekly":{"remaining_percent":61,"resets_at":"2026-09-07T10:45:00Z"}},"future":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Available || s.Windows.Session5H == nil || s.Windows.Session5H.RemainingPercent != 82 || s.Windows.Weekly == nil || s.Windows.Weekly.RemainingPercent != 61 {
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
