package costs_test

import (
	"context"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func canonicalEvent(id, sourceIdentity string) costs.UsageEvent {
	return costs.UsageEvent{
		ID: id, Provider: "codex", SourceKind: "rollout", SourceIdentity: sourceIdentity, TranscriptIdentity: "transcript-1",
		SessionID: "agent-deck-session", ParentSessionID: "parent", RunID: "run-1",
		Timestamp: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Model: "gpt-test",
		Usage:            costs.TokenUsage{InputTokens: 2, CacheReadTokens: 7, CacheWriteTokens: 3, OutputTokens: 5, ReasoningTokens: 4},
		CostMicrodollars: 123, PricingStatus: costs.PricingKnown,
		ReconciliationStatus: costs.ReconciliationAuthoritative,
	}
}

func completeCheckpoint(sourceIdentity string) costs.ScanCheckpoint {
	return costs.ScanCheckpoint{
		Provider: "codex", SourceKind: "rollout", SourceIdentity: sourceIdentity,
		Offset: 42, Fingerprint: "size:42", UpdatedAt: time.Date(2026, 9, 1, 12, 1, 0, 0, time.UTC), Complete: true,
	}
}

func TestStoreIngestIsIdempotentByProviderSourceIdentity(t *testing.T) {
	store := testStore(t)
	event := canonicalEvent("event-1", "request-1")

	first, err := store.Ingest(context.Background(), []costs.UsageEvent{event}, []costs.ScanCheckpoint{completeCheckpoint("transcript-1")})
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	secondEvent := event
	secondEvent.ID = "different-ledger-id"
	second, err := store.Ingest(context.Background(), []costs.UsageEvent{secondEvent}, []costs.ScanCheckpoint{completeCheckpoint("transcript-1")})
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if first.Inserted != 1 || second.Inserted != 0 || second.Duplicates != 1 {
		t.Fatalf("ingest results first=%+v second=%+v", first, second)
	}

	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM cost_events WHERE provider = 'codex' AND source_identity = 'request-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("canonical event count = %d, want 1", count)
	}
}

func TestStoreIngestIncompleteCheckpointDoesNotSupersedeLegacy(t *testing.T) {
	store := testStore(t)
	if err := store.WriteCostEvent(costs.CostEvent{ID: "legacy-1", SessionID: "session", Timestamp: time.Now(), Model: "legacy", InputTokens: 9}); err != nil {
		t.Fatal(err)
	}
	event := canonicalEvent("event-1", "request-1")
	event.SessionID = "session"
	event.SupersedesEventIDs = []string{"legacy-1"}
	checkpoint := completeCheckpoint("transcript-1")
	checkpoint.Complete = false
	result, err := store.Ingest(context.Background(), []costs.UsageEvent{event}, []costs.ScanCheckpoint{checkpoint})
	if err != nil {
		t.Fatal(err)
	}
	if result.Superseded != 0 || result.CheckpointsAdvanced != 0 {
		t.Fatalf("incomplete ingest result = %+v", result)
	}
	var status string
	if err := store.DB().QueryRow(`SELECT reconciliation_status FROM cost_events WHERE id = 'legacy-1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(costs.ReconciliationLegacyUnreconciled) {
		t.Fatalf("legacy reconciliation status = %q", status)
	}

	complete := completeCheckpoint("transcript-1")
	result, err = store.Ingest(context.Background(), []costs.UsageEvent{event}, []costs.ScanCheckpoint{complete})
	if err != nil {
		t.Fatal(err)
	}
	if result.Inserted != 0 || result.Duplicates != 1 || result.Superseded != 1 || result.CheckpointsAdvanced != 1 {
		t.Fatalf("completed replay result = %+v", result)
	}
	if err := store.DB().QueryRow(`SELECT reconciliation_status FROM cost_events WHERE id = 'legacy-1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(costs.ReconciliationLegacySuperseded) {
		t.Fatalf("completed legacy reconciliation status = %q", status)
	}
	summary, err := store.TotalBySession("session")
	if err != nil {
		t.Fatal(err)
	}
	if summary.EventCount != 1 || summary.TotalInputTokens != 12 || summary.TotalOutputTokens != 5 {
		t.Fatalf("reconciled summary = %+v", summary)
	}
}

func TestStoreIngestRollsBackEventSupersessionAndCheckpointOnWriteFailure(t *testing.T) {
	store := testStore(t)
	if err := store.WriteCostEvent(costs.CostEvent{ID: "legacy-1", SessionID: "session", Timestamp: time.Now(), Model: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_checkpoint BEFORE INSERT ON usage_scan_checkpoints BEGIN SELECT RAISE(FAIL, 'forced checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	event := canonicalEvent("event-1", "request-1")
	event.SupersedesEventIDs = []string{"legacy-1"}
	if _, err := store.Ingest(context.Background(), []costs.UsageEvent{event}, []costs.ScanCheckpoint{completeCheckpoint("transcript-1")}); err == nil {
		t.Fatal("Ingest succeeded, want forced checkpoint failure")
	}

	var inserted int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM cost_events WHERE id = 'event-1'`).Scan(&inserted); err != nil {
		t.Fatal(err)
	}
	var legacyStatus string
	if err := store.DB().QueryRow(`SELECT reconciliation_status FROM cost_events WHERE id = 'legacy-1'`).Scan(&legacyStatus); err != nil {
		t.Fatal(err)
	}
	if inserted != 0 || legacyStatus != string(costs.ReconciliationLegacyUnreconciled) {
		t.Fatalf("rollback event count=%d legacy status=%q", inserted, legacyStatus)
	}
}

func TestWriteCostEventUsesCanonicalCompatibilityAdapter(t *testing.T) {
	store := testStore(t)
	providerInput := int64(12)
	err := store.WriteCostEvent(costs.CostEvent{
		ID: "hook-1", SessionID: "session", ParentSessionID: "parent", RunID: "run",
		Timestamp: time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC),
		Provider:  "claude", SourceKind: "hook", SourceIdentity: "message-1", TranscriptIdentity: "transcript-1",
		Model: "claude-test", InputTokens: 2, CacheReadTokens: 7, CacheWriteTokens: 3,
		CacheWrite5mTokens: 2, CacheWrite1hTokens: 1, OutputTokens: 5, ReasoningTokens: 4,
		ProviderInputTokens: &providerInput, CostMicrodollars: 321,
		PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative,
	})
	if err != nil {
		t.Fatal(err)
	}

	var provider, sourceKind, sourceIdentity, parent, run, pricing, reconciliation string
	var reasoning, write5m, write1h, storedProviderInput int64
	err = store.DB().QueryRow(`
		SELECT provider, source_kind, source_identity, parent_session_id, run_id,
			pricing_status, reconciliation_status, reasoning_tokens,
			cache_write_5m_tokens, cache_write_1h_tokens, provider_input_tokens
		FROM cost_events WHERE id = 'hook-1'`).Scan(
		&provider, &sourceKind, &sourceIdentity, &parent, &run, &pricing,
		&reconciliation, &reasoning, &write5m, &write1h, &storedProviderInput)
	if err != nil {
		t.Fatal(err)
	}
	if provider != "claude" || sourceKind != "hook" || sourceIdentity != "message-1" || parent != "parent" || run != "run" ||
		pricing != string(costs.PricingKnown) || reconciliation != string(costs.ReconciliationAuthoritative) ||
		reasoning != 4 || write5m != 2 || write1h != 1 || storedProviderInput != 12 {
		t.Fatalf("canonical compatibility row = provider=%q source=%q/%q parent=%q run=%q pricing=%q reconciliation=%q reasoning=%d write=%d/%d provider_input=%d",
			provider, sourceKind, sourceIdentity, parent, run, pricing, reconciliation, reasoning, write5m, write1h, storedProviderInput)
	}
}
