package costs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func copyFixture(t *testing.T, source, target string) []byte {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAuthoritativeSyncTwiceThenAppendScansOnlyChangedSource(t *testing.T) {
	root := t.TempDir()
	codexPath := filepath.Join(root, "codex.jsonl")
	claudePath := filepath.Join(root, "claude.jsonl")
	copyFixture(t, filepath.Join("testdata", "codex", "cumulative_model_reset.jsonl"), codexPath)
	claudeData := copyFixture(t, filepath.Join("testdata", "claude", "native_child.jsonl"), claudePath)
	sources := []costs.TranscriptSource{
		{Provider: costs.ProviderCodex, Kind: costs.SourceKindCodexRollout, Identity: "codex:sync", Path: codexPath, SessionID: "codex-session"},
		{Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeNativeChild, Identity: "claude:sync", Path: claudePath, SessionID: "claude-session"},
	}
	store := testStore(t)
	pricer := costs.NewPricer(costs.PricerConfig{})

	first := costs.Sync(context.Background(), store, pricer, sources)
	if len(first.Errors) != 0 || first.SourcesScanned != 2 || first.SourcesChanged != 2 || first.EventsImported != 4 {
		t.Fatalf("first sync=%+v", first)
	}
	second := costs.Sync(context.Background(), store, pricer, sources)
	if len(second.Errors) != 0 || second.SourcesScanned != 2 || second.SourcesChanged != 0 || second.EventsImported != 0 {
		t.Fatalf("second sync=%+v", second)
	}

	appended := []byte(`{"type":"assistant","uuid":"sync-new","timestamp":"2026-09-01T16:00:05Z","message":{"id":"msg-sync-new","model":"claude-sonnet-4-6","usage":{"input_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4,"output_tokens":5}}}` + "\n")
	if err := os.WriteFile(claudePath, append(claudeData, appended...), 0o600); err != nil {
		t.Fatal(err)
	}
	third := costs.Sync(context.Background(), store, pricer, sources)
	if len(third.Errors) != 0 || third.SourcesChanged != 1 || third.EventsImported != 1 {
		t.Fatalf("third sync=%+v", third)
	}
}

func TestAuthoritativeSyncReconcilesCompleteRangeButNotPartialOrFailed(t *testing.T) {
	store := testStore(t)
	pricer := costs.NewPricer(costs.PricerConfig{})
	if err := store.WriteCostEvent(costs.CostEvent{ID: "legacy", SessionID: "session", Timestamp: time.Date(2026, 9, 1, 16, 0, 2, 0, time.UTC), Model: "legacy", InputTokens: 50}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "complete.jsonl")
	data := copyFixture(t, filepath.Join("testdata", "claude", "native_child.jsonl"), path)
	partialPath := filepath.Join(t.TempDir(), "partial.jsonl")
	if err := os.WriteFile(partialPath, append(data, []byte(`{"type":"assistant"`)...), 0o600); err != nil {
		t.Fatal(err)
	}

	partial := costs.Sync(context.Background(), store, pricer, []costs.TranscriptSource{{Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeNativeChild, Identity: "claude:partial-sync", Path: partialPath, SessionID: "session"}})
	if partial.EventsReconciled != 0 {
		t.Fatalf("partial sync reconciled=%+v", partial)
	}
	complete := costs.Sync(context.Background(), store, pricer, []costs.TranscriptSource{{Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeNativeChild, Identity: "claude:complete-sync", Path: path, SessionID: "session"}})
	if len(complete.Errors) != 0 || complete.EventsReconciled != 1 {
		t.Fatalf("complete sync=%+v", complete)
	}
	var status string
	if err := store.DB().QueryRow(`SELECT reconciliation_status FROM cost_events WHERE id = 'legacy'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(costs.ReconciliationLegacySuperseded) {
		t.Fatalf("legacy status=%q", status)
	}

	failed := costs.Sync(context.Background(), store, pricer, []costs.TranscriptSource{{Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeDirect, Identity: "claude:missing", Path: filepath.Join(t.TempDir(), "missing.jsonl")}})
	if len(failed.Errors) != 1 {
		t.Fatalf("read failure=%+v", failed)
	}
}

func TestAuthoritativeSyncSurfacesDatabaseFailure(t *testing.T) {
	store := testStore(t)
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_usage_sync BEFORE INSERT ON cost_events BEGIN SELECT RAISE(FAIL, 'forced sync write failure'); END`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	copyFixture(t, filepath.Join("testdata", "claude", "native_child.jsonl"), path)
	result := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{{Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeNativeChild, Identity: "claude:db-fail", Path: path}})
	if len(result.Errors) != 1 || result.EventsImported != 0 {
		t.Fatalf("database failure=%+v", result)
	}
	var checkpoints int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM usage_scan_checkpoints`).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 0 {
		t.Fatalf("failed sync advanced %d checkpoints", checkpoints)
	}
}

func TestAuthoritativeSyncDeduplicatesLiveHookOverlapByProviderIdentity(t *testing.T) {
	store := testStore(t)
	if err := store.WriteCostEvent(costs.CostEvent{
		ID: "live-hook-row", SessionID: "session", Timestamp: time.Date(2026, 9, 1, 16, 0, 2, 0, time.UTC),
		Provider: costs.ProviderClaude, SourceKind: "hook", SourceIdentity: "claude:msg:msg-child",
		Model: "claude-child", InputTokens: 1, CacheReadTokens: 4, OutputTokens: 3,
		PricingStatus: costs.PricingUnknown, ReconciliationStatus: costs.ReconciliationAuthoritative,
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "native.jsonl")
	copyFixture(t, filepath.Join("testdata", "claude", "native_child.jsonl"), path)
	result := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{{
		Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeNativeChild,
		Identity: "claude:overlap", Path: path, SessionID: "session",
	}})
	if len(result.Errors) != 0 || result.EventsImported != 0 || result.EventsSkipped != 1 {
		t.Fatalf("overlap sync=%+v", result)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM cost_events WHERE provider = ? AND source_identity = ?`, costs.ProviderClaude, "claude:msg:msg-child").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("overlap event count=%d", count)
	}
}
