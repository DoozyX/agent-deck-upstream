package costs_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
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

func TestAuthoritativeSyncMonotonicallyCorrectsPersistedClaudeObservation(t *testing.T) {
	store := testStore(t)
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	firstLine := []byte(`{"type":"assistant","uuid":"stream-a","requestId":"req-correction","timestamp":"2026-09-01T16:00:00Z","message":{"id":"msg-correction","model":"claude-sonnet-4-6","usage":{"input_tokens":2,"output_tokens":5}}}` + "\n")
	if err := os.WriteFile(path, firstLine, 0o600); err != nil {
		t.Fatal(err)
	}
	source := costs.TranscriptSource{Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeDirect, Identity: "claude:correction", Path: path, SessionID: "session"}
	first := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{source})
	if len(first.Errors) != 0 || first.EventsImported != 1 {
		t.Fatalf("first sync=%+v", first)
	}
	corrected := []byte(`{"type":"assistant","uuid":"stream-b","requestId":"req-correction","timestamp":"2026-09-01T16:00:01Z","message":{"id":"msg-correction","model":"claude-sonnet-4-6","usage":{"input_tokens":2,"output_tokens":9}}}` + "\n")
	if err := os.WriteFile(path, append(firstLine, corrected...), 0o600); err != nil {
		t.Fatal(err)
	}
	second := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{source})
	if len(second.Errors) != 0 {
		t.Fatalf("second sync=%+v", second)
	}
	var count int
	var output int64
	if err := store.DB().QueryRow(`SELECT COUNT(*), MAX(output_tokens) FROM cost_events WHERE provider = ? AND source_identity = ?`, costs.ProviderClaude, "claude:msg:msg-correction").Scan(&count, &output); err != nil {
		t.Fatal(err)
	}
	if count != 1 || output != 9 {
		t.Fatalf("persisted correction count=%d output=%d; sync=%+v", count, output, second)
	}
}

func TestAuthoritativeSyncAliasesRequestOnlyObservationToLaterMessageID(t *testing.T) {
	store := testStore(t)
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	requestOnly := []byte(`{"type":"assistant","uuid":"stream-a","requestId":"req-alias","timestamp":"2026-09-01T16:00:00Z","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":2,"output_tokens":5}}}` + "\n")
	if err := os.WriteFile(path, requestOnly, 0o600); err != nil {
		t.Fatal(err)
	}
	source := costs.TranscriptSource{Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeDirect, Identity: "claude:alias", Path: path, SessionID: "session"}
	first := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{source})
	if len(first.Errors) != 0 || first.EventsImported != 1 {
		t.Fatalf("first sync=%+v", first)
	}
	withMessageID := []byte(`{"type":"assistant","uuid":"stream-b","requestId":"req-alias","timestamp":"2026-09-01T16:00:01Z","message":{"id":"msg-alias","model":"claude-sonnet-4-6","usage":{"input_tokens":2,"output_tokens":9}}}` + "\n")
	if err := os.WriteFile(path, append(requestOnly, withMessageID...), 0o600); err != nil {
		t.Fatal(err)
	}
	second := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{source})
	if len(second.Errors) != 0 {
		t.Fatalf("second sync=%+v", second)
	}
	var count int
	var output int64
	if err := store.DB().QueryRow(`SELECT COUNT(*), MAX(output_tokens) FROM cost_events WHERE provider = ? AND transcript_identity = ?`, costs.ProviderClaude, source.Identity).Scan(&count, &output); err != nil {
		t.Fatal(err)
	}
	if count != 1 || output != 9 {
		t.Fatalf("aliased correction count=%d output=%d; sync=%+v", count, output, second)
	}
}

func TestAuthoritativeSyncMalformedCodexNeverSuppressesLegacyEstimate(t *testing.T) {
	store := testStore(t)
	legacyTime := time.Date(2026, 9, 1, 16, 0, 2, 0, time.UTC)
	if err := store.WriteCostEvent(costs.CostEvent{ID: "legacy-gap", SessionID: "session", Timestamp: legacyTime, Model: "legacy", InputTokens: 99}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := []byte(
		`{"timestamp":"2026-09-01T16:00:01Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}` + "\n" +
			`{"timestamp":"2026-09-01T16:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":3}}}}` + "\n" +
			`{malformed}` + "\n" +
			`{"timestamp":"2026-09-01T16:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"cached_input_tokens":4,"output_tokens":6}}}}` + "\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{{
		Provider: costs.ProviderCodex, Kind: costs.SourceKindCodexRollout,
		Identity: "codex:malformed", Path: path, SessionID: "session",
	}})
	var status string
	if err := store.DB().QueryRow(`SELECT reconciliation_status FROM cost_events WHERE id = 'legacy-gap'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(costs.ReconciliationLegacyUnreconciled) || result.EventsReconciled != 0 {
		t.Fatalf("malformed coverage suppressed legacy: status=%q result=%+v", status, result)
	}
}

func TestAuthoritativeSyncCompletesAccumulatedPartialCoverageRange(t *testing.T) {
	store := testStore(t)
	when := time.Date(2026, 9, 1, 16, 0, 2, 0, time.UTC)
	if err := store.WriteCostEvent(costs.CostEvent{ID: "legacy-partial", SessionID: "session", Timestamp: when, Model: "legacy", InputTokens: 99}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	completeUsage := []byte(`{"type":"assistant","uuid":"partial-a","timestamp":"2026-09-01T16:00:02Z","message":{"id":"msg-partial","model":"claude-sonnet-5","usage":{"input_tokens":2,"output_tokens":3}}}` + "\n")
	trailing := []byte(`{"type":"user"`)
	if err := os.WriteFile(path, append(completeUsage, trailing...), 0o600); err != nil {
		t.Fatal(err)
	}
	source := costs.TranscriptSource{Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeDirect, Identity: "claude:partial-range", Path: path, SessionID: "session"}
	first := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{source})
	if first.EventsImported != 1 || first.EventsReconciled != 0 {
		t.Fatalf("partial sync=%+v", first)
	}
	if err := os.WriteFile(path, append(completeUsage, []byte(`{"type":"user"}`+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	second := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{source})
	var status string
	if err := store.DB().QueryRow(`SELECT reconciliation_status FROM cost_events WHERE id = 'legacy-partial'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if len(second.Errors) != 0 || second.EventsReconciled != 1 || status != string(costs.ReconciliationLegacySuperseded) {
		t.Fatalf("completed partial range status=%q result=%+v", status, second)
	}
}

func TestAuthoritativeSyncDetectsSameSizeSameMtimeSourceReplacementAfterRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	path := filepath.Join(dir, "rollout.jsonl")
	original := []byte(
		`{"timestamp":"2026-09-01T12:00:00Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}` + "\n" +
			`{"timestamp":"2026-09-01T12:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":3}}}}` + "\n")
	replacement := []byte(
		`{"timestamp":"2026-09-02T12:00:00Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}` + "\n" +
			`{"timestamp":"2026-09-02T12:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":30,"cached_input_tokens":4,"output_tokens":7}}}}` + "\n")
	if len(original) != len(replacement) {
		t.Fatalf("fixture sizes differ: %d != %d", len(original), len(replacement))
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	openStore := func() (*statedb.StateDB, *costs.Store) {
		db, err := statedb.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(); err != nil {
			db.Close()
			t.Fatal(err)
		}
		return db, costs.NewStore(db.DB())
	}
	source := costs.TranscriptSource{Provider: costs.ProviderCodex, Kind: costs.SourceKindCodexRollout, Identity: "codex:replace", Path: path, SessionID: "session"}
	db, store := openStore()
	first := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{source})
	if len(first.Errors) != 0 || first.EventsImported != 1 {
		t.Fatalf("first sync=%+v", first)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	db, store = openStore()
	t.Cleanup(func() { _ = db.Close() })
	second := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{source})
	if len(second.Errors) != 0 || second.SourcesChanged != 1 || second.EventsImported != 1 {
		t.Fatalf("replacement sync=%+v", second)
	}
	var input, output int64
	if err := store.DB().QueryRow(`SELECT input_tokens, output_tokens FROM cost_events WHERE timestamp LIKE '2026-09-02%'`).Scan(&input, &output); err != nil {
		t.Fatal(err)
	}
	if input != 26 || output != 7 {
		t.Fatalf("replacement usage input=%d output=%d", input, output)
	}
}

func TestAuthoritativeSyncPersistsCodexParserStateAcrossRestartedAppend(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	path := filepath.Join(dir, "rollout.jsonl")
	original := []byte(
		`{"timestamp":"2026-09-01T12:00:00Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}` + "\n" +
			`{"timestamp":"2026-09-01T12:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":3}}}}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	openStore := func() (*statedb.StateDB, *costs.Store) {
		db, err := statedb.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(); err != nil {
			db.Close()
			t.Fatal(err)
		}
		return db, costs.NewStore(db.DB())
	}
	source := costs.TranscriptSource{Provider: costs.ProviderCodex, Kind: costs.SourceKindCodexRollout, Identity: "codex:restart-append", Path: path, SessionID: "session"}
	db, store := openStore()
	first := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{source})
	if len(first.Errors) != 0 || first.EventsImported != 1 {
		t.Fatalf("first sync=%+v", first)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	appended := []byte(`{"timestamp":"2026-09-01T12:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":17,"cached_input_tokens":4,"output_tokens":6}}}}` + "\n")
	if err := os.WriteFile(path, append(original, appended...), 0o600); err != nil {
		t.Fatal(err)
	}
	db, store = openStore()
	t.Cleanup(func() { _ = db.Close() })
	second := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{source})
	if len(second.Errors) != 0 || second.EventsImported != 1 {
		t.Fatalf("restarted append sync=%+v", second)
	}
	var input, cacheRead, output int64
	if err := store.DB().QueryRow(`SELECT input_tokens, cache_read_tokens, output_tokens FROM cost_events WHERE timestamp = '2026-09-01T12:00:02Z'`).Scan(&input, &cacheRead, &output); err != nil {
		t.Fatal(err)
	}
	if input != 5 || cacheRead != 2 || output != 3 {
		t.Fatalf("restarted append delta input=%d cache_read=%d output=%d", input, cacheRead, output)
	}
}

func TestAuthoritativeSyncPersistsAndSurfacesBlockedResetReceipt(t *testing.T) {
	store := testStore(t)
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	resetAt := int64(1788278400)
	line := []byte(`{"timestamp":"2026-09-01T15:00:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"rate_limit_reached_type":"primary","primary":{"resets_at":1788278400}}}}` + "\n")
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatal(err)
	}
	result := costs.Sync(context.Background(), store, costs.NewPricer(costs.PricerConfig{}), []costs.TranscriptSource{{
		Provider: costs.ProviderCodex, Kind: costs.SourceKindCodexRollout,
		Identity: "codex:blocked", Path: path, Account: "work", SessionID: "session",
	}})
	if len(result.Errors) != 0 {
		t.Fatalf("sync=%+v", result)
	}
	foundWarning := false
	for _, warning := range result.Warnings {
		if warning.Kind == "blocked" && strings.Contains(warning.Message, "primary") && strings.Contains(warning.Message, time.Unix(resetAt, 0).UTC().Format(time.RFC3339)) {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("blocked reset missing from sync result: %+v", result.Warnings)
	}
	var status, account, blockedUntil string
	var resetKnown int
	if err := store.DB().QueryRow(`
		SELECT blocked_status, account, blocked_until, reset_known
		FROM usage_sync_receipts
		WHERE provider = ? AND source_identity = ?`, costs.ProviderCodex, "codex:blocked").Scan(
		&status, &account, &blockedUntil, &resetKnown); err != nil {
		t.Fatal(err)
	}
	if status != "primary" || account != "work" || blockedUntil != time.Unix(resetAt, 0).UTC().Format(time.RFC3339Nano) || resetKnown != 1 {
		t.Fatalf("receipt status=%q account=%q until=%q known=%d", status, account, blockedUntil, resetKnown)
	}
}
