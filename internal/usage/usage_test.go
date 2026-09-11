package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	if claude.Plan != "Max" || claude.Stale || claude.Windows.Session5H == nil || claude.Windows.Session5H.RemainingPercent != 76 || !claude.Windows.Session5H.ResetsAt.Equal(time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)) || claude.Windows.Weekly == nil || claude.Windows.Weekly.RemainingPercent != 75 || !claude.Windows.Weekly.ResetsAt.Equal(time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("Claude snapshot = %#v", claude)
	}

	codex, err := Parse(Codex, []byte(`{"schema":"openusage.limits.v1","providers":{"codex":{"plan":"Pro","resources":{"weekly":{"kind":"consumption","remaining":83,"resetsAt":"2026-09-07T06:59:05Z"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if codex.Plan != "Pro" || codex.Windows.Session5H != nil || codex.Windows.Weekly == nil || codex.Windows.Weekly.RemainingPercent != 83 || !codex.Windows.Weekly.ResetsAt.Equal(time.Date(2026, 9, 7, 6, 59, 5, 0, time.UTC)) {
		t.Fatalf("Codex snapshot = %#v", codex)
	}
}

func TestParseClaudeSessionTakesPrecedenceOverFiveHour(t *testing.T) {
	s, err := Parse(Claude, []byte(`{"resources":{"session":{"remaining":91},"five_hour":{"remaining":37},"weekly":{"remaining":64}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Windows.Session5H == nil || s.Windows.Session5H.RemainingPercent != 91 {
		t.Fatalf("session window = %#v, want session remaining to win over five_hour", s.Windows.Session5H)
	}
}

func TestParseSelectsRequestedProvider(t *testing.T) {
	raw := []byte(`{"providers":{"claude":{"resources":{"weekly":{"remaining":21}}},"codex":{"resources":{"weekly":{"remaining":82}}}}}`)
	claude, err := Parse(Claude, raw)
	if err != nil {
		t.Fatal(err)
	}
	codex, err := Parse(Codex, raw)
	if err != nil {
		t.Fatal(err)
	}
	if claude.Windows.Weekly == nil || claude.Windows.Weekly.RemainingPercent != 21 || codex.Windows.Weekly == nil || codex.Windows.Weekly.RemainingPercent != 82 {
		t.Fatalf("provider snapshots = Claude %#v, Codex %#v", claude, codex)
	}
}

func TestParseMissingRequestedProviderFallsBackToTopLevelEnvelope(t *testing.T) {
	_, err := Parse(Claude, []byte(`{"providers":{"codex":{"resources":{"weekly":{"remaining":82}}}}}`))
	if err == nil || err.Error() != "openusage response has no limits" {
		t.Fatalf("error = %v, want missing limits error", err)
	}
}

func TestParseRejectsMalformedProviderResponse(t *testing.T) {
	_, err := Parse(Claude, []byte(`{"providers":{"claude":"malformed"}}`))
	if err == nil || !strings.HasPrefix(err.Error(), "malformed openusage provider response:") {
		t.Fatalf("error = %v, want malformed provider response error", err)
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

func TestParseClaudeFableLandsInModels(t *testing.T) {
	s, err := Parse(Claude, []byte(`{"schema":"openusage.limits.v1","providers":{"claude":{"plan":"Max","resources":{"session":{"kind":"consumption","remaining":76,"resetsAt":"2026-09-01T09:00:00Z"},"weekly":{"kind":"consumption","remaining":75,"resetsAt":"2026-09-03T08:00:00Z"},"fable":{"kind":"consumption","remaining":42,"resetsAt":"2026-09-02T10:00:00Z"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Windows.Models == nil || s.Windows.Models["fable"] == nil {
		t.Fatalf("Models = %#v, want fable window", s.Windows.Models)
	}
	fable := s.Windows.Models["fable"]
	if fable.RemainingPercent != 42 || !fable.ResetsAt.Equal(time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("fable window = %#v", fable)
	}
	if len(s.Windows.Models) != 1 {
		t.Fatalf("Models = %#v, want only fable", s.Windows.Models)
	}
}

func TestParseCodexSparkAndSparkWeeklyLandInModelsCreditsIgnored(t *testing.T) {
	s, err := Parse(Codex, []byte(`{"schema":"openusage.limits.v1","providers":{"codex":{"plan":"Pro","resources":{"weekly":{"kind":"consumption","remaining":83,"resetsAt":"2026-09-07T06:59:05Z"},"spark":{"kind":"consumption","remaining":60,"resetsAt":"2026-09-04T00:00:00Z"},"sparkWeekly":{"kind":"consumption","remaining":55,"resetsAt":"2026-09-08T00:00:00Z"},"credits":{"kind":"balance","amount":12.5}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Windows.Models == nil {
		t.Fatalf("Models = %#v, want spark and sparkWeekly", s.Windows.Models)
	}
	if len(s.Windows.Models) != 2 {
		t.Fatalf("Models = %#v, want exactly spark and sparkWeekly (credits ignored)", s.Windows.Models)
	}
	spark := s.Windows.Models["spark"]
	if spark == nil || spark.RemainingPercent != 60 || !spark.ResetsAt.Equal(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("spark window = %#v", spark)
	}
	sparkWeekly := s.Windows.Models["sparkWeekly"]
	if sparkWeekly == nil || sparkWeekly.RemainingPercent != 55 || !sparkWeekly.ResetsAt.Equal(time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("sparkWeekly window = %#v", sparkWeekly)
	}
	if _, ok := s.Windows.Models["credits"]; ok {
		t.Fatalf("Models = %#v, credits (a balance) must be ignored", s.Windows.Models)
	}
}

func TestParseModelsNilWhenNoExtraResource(t *testing.T) {
	s, err := Parse(Claude, []byte(`{"limits":{"five_hour":{"remaining_percent":82},"weekly":{"remaining_percent":61}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Windows.Models != nil {
		t.Fatalf("Models = %#v, want nil when no extra consumption resource is present", s.Windows.Models)
	}
}

func TestParseSessionAliasesAndWeeklyDoNotLeakIntoModels(t *testing.T) {
	s, err := Parse(Claude, []byte(`{"resources":{"session":{"remaining":91},"five_hour":{"remaining":37},"weekly":{"remaining":64}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Windows.Session5H == nil || s.Windows.Session5H.RemainingPercent != 91 {
		t.Fatalf("session window = %#v, want session to win over five_hour", s.Windows.Session5H)
	}
	if s.Windows.Models != nil {
		t.Fatalf("Models = %#v, want nil: session, five_hour and weekly must not leak into Models", s.Windows.Models)
	}
}

func TestParseModelOnlyResponseStillErrorsWithoutDedicatedWindow(t *testing.T) {
	_, err := Parse(Codex, []byte(`{"resources":{"spark":{"remaining":60}}}`))
	if err == nil || err.Error() != "openusage response has no supported windows" {
		t.Fatalf("error = %v, want no supported windows error", err)
	}
}

func TestParseSessionFiveHourAliasDoesNotLeakIntoModels(t *testing.T) {
	s, err := Parse(Claude, []byte(`{"resources":{"session_5h":{"remaining":48},"weekly":{"remaining":64}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Windows.Session5H == nil || s.Windows.Session5H.RemainingPercent != 48 {
		t.Fatalf("session window = %#v, want session_5h to populate Session5H", s.Windows.Session5H)
	}
	if s.Windows.Models != nil {
		t.Fatalf("Models = %#v, want nil: session_5h must not leak into Models", s.Windows.Models)
	}
}

// TestParseWeeklyValueExcludedFromModelsByDedicatedKeyGuard uses a
// well-formed weekly value on purpose. The guard (the dedicatedWindowKeys
// membership check in parseModelWindows, usage.go:216) runs BEFORE
// parseWindow is ever called for that key (usage.go:219): with the guard
// present, a dedicated key's raw value never reaches parseWindow at all. The
// round-2 predecessor of this test used a malformed "weekly":"n/a" value,
// which parseWindow rejects (returns nil) on its own regardless of the
// guard — so that fixture couldn't distinguish the guard's short-circuit
// from parseWindow's own rejection, and removing weeklyKey from
// dedicatedWindowKeys did not fail it (see
// TestParseMalformedWeeklyValueYieldsNilWeeklyAndNilModels below for that
// coverage, restored separately). A value parseWindow *accepts* is the only
// way to make the guard's exclusion observable: verified by deleting
// weeklyKey from dedicatedWindowKeys and confirming this test then fails
// (weekly leaks into Models), then restoring the key.
func TestParseWeeklyValueExcludedFromModelsByDedicatedKeyGuard(t *testing.T) {
	s, err := Parse(Claude, []byte(`{"resources":{"session":{"remaining":90},"weekly":{"remaining":64}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Windows.Session5H == nil || s.Windows.Session5H.RemainingPercent != 90 {
		t.Fatalf("session window = %#v, want session remaining=90", s.Windows.Session5H)
	}
	if s.Windows.Weekly == nil || s.Windows.Weekly.RemainingPercent != 64 {
		t.Fatalf("weekly window = %#v, want weekly remaining=64", s.Windows.Weekly)
	}
	if s.Windows.Models != nil {
		t.Fatalf("Models = %#v, want nil: a well-formed value under the dedicated weekly key must be excluded by the dedicatedWindowKeys guard, not leak into Models", s.Windows.Models)
	}
}

// TestParseMalformedWeeklyValueYieldsNilWeeklyAndNilModels restores the
// round-2 malformed-value coverage that round 3 dropped when it rewrote
// TestParseMalformedWeeklyValueExcludedFromModelsByDedicatedKeyGuard in
// place instead of adding this alongside it: a malformed value under a
// dedicated key must safely yield err == nil, Weekly == nil, Models == nil,
// rather than erroring. This is a different property than the guard test
// above (which needs a well-formed value to make the guard observable) —
// both fixtures are needed, not one or the other.
func TestParseMalformedWeeklyValueYieldsNilWeeklyAndNilModels(t *testing.T) {
	s, err := Parse(Claude, []byte(`{"resources":{"session":{"remaining":90},"weekly":"n/a"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Windows.Session5H == nil || s.Windows.Session5H.RemainingPercent != 90 {
		t.Fatalf("session window = %#v, want session remaining=90", s.Windows.Session5H)
	}
	if s.Windows.Weekly != nil {
		t.Fatalf("weekly window = %#v, want nil for a malformed weekly value", s.Windows.Weekly)
	}
	if s.Windows.Models != nil {
		t.Fatalf("Models = %#v, want nil for a malformed weekly value", s.Windows.Models)
	}
}

// TestParseCodexSessionKeyExcludedFromModelsThoughSession5HStaysNil pins the
// round-1 claim (usage.go:190-198) that dedicatedWindowKeys excludes
// session/five_hour/session_5h for every provider, not only Claude: a Codex
// resource literally named "session" must be dropped, not stored in Models.
// Prior art (TestParseCodexWeeklyOnlyAndStale) only incidentally asserts
// Session5H == nil, since its fixture carries no such key at all — it does
// not exercise the guard.
func TestParseCodexSessionKeyExcludedFromModelsThoughSession5HStaysNil(t *testing.T) {
	s, err := Parse(Codex, []byte(`{"resources":{"session":{"remaining":55},"weekly":{"remaining":64}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Windows.Session5H != nil {
		t.Fatalf("session window = %#v, want nil: Session5H is only populated for Claude", s.Windows.Session5H)
	}
	if s.Windows.Models != nil {
		t.Fatalf("Models = %#v, want nil: a Codex resource named \"session\" must be excluded by dedicatedWindowKeys, not stored in Models", s.Windows.Models)
	}
}

// TestParseZeroRemainingModelPreservesZeroInModels pins the *int-based
// absent-vs-zero distinction at the exact value task-03's frontier gate
// depends on: a remaining:0 resource must land in Models with
// RemainingPercent == 0 preserved, not be dropped as if absent.
func TestParseZeroRemainingModelPreservesZeroInModels(t *testing.T) {
	s, err := Parse(Claude, []byte(`{"resources":{"session":{"remaining":90},"fable":{"remaining":0}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Windows.Models == nil || s.Windows.Models["fable"] == nil {
		t.Fatalf("Models = %#v, want a fable entry", s.Windows.Models)
	}
	if s.Windows.Models["fable"].RemainingPercent != 0 {
		t.Fatalf("Models[\"fable\"].RemainingPercent = %d, want 0 preserved (not dropped as absent)", s.Windows.Models["fable"].RemainingPercent)
	}
}

func TestSnapshotJSONOmitsModelsFieldWhenNil(t *testing.T) {
	s, err := Parse(Claude, []byte(`{"limits":{"five_hour":{"remaining_percent":82},"weekly":{"remaining_percent":61}}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"models"`) {
		t.Fatalf("marshaled snapshot = %s, want no \"models\" key when Models is nil", out)
	}
	if !strings.Contains(string(out), `"session_5h":{"remaining_percent":82`) {
		t.Fatalf("marshaled snapshot = %s, want session_5h tag with remaining_percent=82", out)
	}
	if !strings.Contains(string(out), `"weekly":{"remaining_percent":61`) {
		t.Fatalf("marshaled snapshot = %s, want weekly tag with remaining_percent=61", out)
	}
	var roundTrip Snapshot
	if err := json.Unmarshal(out, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Windows.Models != nil {
		t.Fatalf("round-tripped Models = %#v, want nil", roundTrip.Windows.Models)
	}
}

func TestSnapshotJSONIncludesPopulatedModels(t *testing.T) {
	s, err := Parse(Claude, []byte(`{"schema":"openusage.limits.v1","providers":{"claude":{"plan":"Max","resources":{"session":{"kind":"consumption","remaining":76,"resetsAt":"2026-09-01T09:00:00Z"},"weekly":{"kind":"consumption","remaining":75,"resetsAt":"2026-09-03T08:00:00Z"},"fable":{"kind":"consumption","remaining":42,"resetsAt":"2026-09-02T10:00:00Z"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Snapshot
	if err := json.Unmarshal(out, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Windows.Models == nil || roundTrip.Windows.Models["fable"] == nil || roundTrip.Windows.Models["fable"].RemainingPercent != 42 {
		t.Fatalf("round-tripped Models = %#v, want fable=42 to survive marshal/unmarshal", roundTrip.Windows.Models)
	}
	if !strings.Contains(string(out), `"models":{"fable":{"remaining_percent":42`) {
		t.Fatalf("marshaled snapshot = %s, want models.fable serialized with remaining_percent", out)
	}
	if !strings.Contains(string(out), `"session_5h":{"remaining_percent":76`) {
		t.Fatalf("marshaled snapshot = %s, want session_5h tag with remaining_percent=76", out)
	}
	if !strings.Contains(string(out), `"weekly":{"remaining_percent":75`) {
		t.Fatalf("marshaled snapshot = %s, want weekly tag with remaining_percent=75", out)
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
