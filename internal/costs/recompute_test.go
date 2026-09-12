package costs_test

import (
	"context"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

// seedEvent inserts one cost_event row directly via WriteCostEvent.
func seedEvent(t *testing.T, s *costs.Store, id, sessionID, model string, input, output, cacheRead, cacheWrite, cost int64) {
	t.Helper()
	if err := s.WriteCostEvent(costs.CostEvent{
		ID:               id,
		SessionID:        sessionID,
		Timestamp:        time.Now(),
		Model:            model,
		InputTokens:      input,
		OutputTokens:     output,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		CostMicrodollars: cost,
	}); err != nil {
		t.Fatalf("seed event %s: %v", id, err)
	}
}

func TestRecompute_EmptyStore(t *testing.T) {
	s := testStore(t)
	updated, skipped, err := costs.Recompute(context.Background(), s, costs.NewPricer(costs.PricerConfig{}), false)
	if err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	if updated != 0 || skipped != 0 {
		t.Errorf("empty store: got updated=%d skipped=%d, want 0/0", updated, skipped)
	}
}

func TestRecompute_BackfillsZeroCostRows(t *testing.T) {
	s := testStore(t)
	// 1M input + 1M output on Opus 5 costs $5 + $25 = $30.
	seedEvent(t, s, "evt-1", "sess-1", "claude-opus-5", 1_000_000, 1_000_000, 0, 0, 0)

	updated, skipped, err := costs.Recompute(context.Background(), s, costs.NewPricer(costs.PricerConfig{}), false)
	if err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	if updated != 1 || skipped != 0 {
		t.Errorf("got updated=%d skipped=%d, want 1/0", updated, skipped)
	}

	got, err := s.TotalBySession("sess-1")
	if err != nil {
		t.Fatalf("TotalBySession: %v", err)
	}
	if got.TotalCostMicrodollars != 30_000_000 {
		t.Errorf("cost after recompute = %d, want 30000000", got.TotalCostMicrodollars)
	}
}

func TestRecompute_SkipsAlreadyCorrectRows(t *testing.T) {
	s := testStore(t)
	// 1M input + 1M output on Sonnet 5 = $2 + $10 = $12.
	seedEvent(t, s, "evt-1", "sess-1", "claude-sonnet-5", 1_000_000, 1_000_000, 0, 0, 12_000_000)
	if _, err := s.DB().Exec(`UPDATE cost_events SET pricing_status = ? WHERE id = 'evt-1'`, costs.PricingKnown); err != nil {
		t.Fatal(err)
	}

	updated, skipped, err := costs.Recompute(context.Background(), s, costs.NewPricer(costs.PricerConfig{}), false)
	if err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	if updated != 0 || skipped != 1 {
		t.Errorf("got updated=%d skipped=%d, want 0/1", updated, skipped)
	}
}

func TestRecompute_LeavesUnknownModelRowsUntouched(t *testing.T) {
	s := testStore(t)
	// Unknown model with a positive cost: pricer cannot recompute, so we must
	// leave the row alone rather than zero out the existing value.
	seedEvent(t, s, "evt-unknown", "sess-1", "totally-made-up-model", 1_000_000, 0, 0, 0, 42_000)

	updated, skipped, err := costs.Recompute(context.Background(), s, costs.NewPricer(costs.PricerConfig{}), false)
	if err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	if updated != 0 || skipped != 1 {
		t.Errorf("got updated=%d skipped=%d, want 0/1", updated, skipped)
	}

	got, err := s.TotalBySession("sess-1")
	if err != nil {
		t.Fatalf("TotalBySession: %v", err)
	}
	if got.TotalCostMicrodollars != 42_000 {
		t.Errorf("cost after recompute = %d, want 42000 (unchanged)", got.TotalCostMicrodollars)
	}
}

func TestRecomputeDemotesStaleKnownUnknownModelWithoutErasingCost(t *testing.T) {
	s := testStore(t)
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "stale-known", SessionID: "session", Timestamp: time.Now(),
		Provider: "test", SourceKind: "test", SourceIdentity: "stale-known",
		Model: "not-in-catalog", InputTokens: 1, CostMicrodollars: 42,
		PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative,
	}); err != nil {
		t.Fatal(err)
	}
	updated, skipped, err := costs.Recompute(context.Background(), s, costs.NewPricer(costs.PricerConfig{}), false)
	if err != nil {
		t.Fatal(err)
	}
	var cost int64
	var status string
	if err := s.DB().QueryRow(`SELECT cost_microdollars, pricing_status FROM cost_events WHERE id = 'stale-known'`).Scan(&cost, &status); err != nil {
		t.Fatal(err)
	}
	if updated != 1 || skipped != 0 || cost != 42 || status != string(costs.PricingUnknown) {
		t.Fatalf("updated=%d skipped=%d cost=%d status=%q", updated, skipped, cost, status)
	}
}

func TestRecompute_DryRunDoesNotMutate(t *testing.T) {
	s := testStore(t)
	seedEvent(t, s, "evt-1", "sess-1", "claude-opus-5", 1_000_000, 1_000_000, 0, 0, 0)

	updated, skipped, err := costs.Recompute(context.Background(), s, costs.NewPricer(costs.PricerConfig{}), true)
	if err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	if updated != 1 || skipped != 0 {
		t.Errorf("dry-run counts: got updated=%d skipped=%d, want 1/0", updated, skipped)
	}

	got, err := s.TotalBySession("sess-1")
	if err != nil {
		t.Fatalf("TotalBySession: %v", err)
	}
	if got.TotalCostMicrodollars != 0 {
		t.Errorf("dry-run mutated DB: cost = %d, want 0", got.TotalCostMicrodollars)
	}
}

func TestRecompute_Idempotent(t *testing.T) {
	s := testStore(t)
	seedEvent(t, s, "evt-1", "sess-1", "claude-opus-5", 1_000_000, 1_000_000, 0, 0, 0)
	pricer := costs.NewPricer(costs.PricerConfig{})

	// First run: backfills 1 row.
	if u, _, err := costs.Recompute(context.Background(), s, pricer, false); err != nil || u != 1 {
		t.Fatalf("first run: updated=%d err=%v, want 1/nil", u, err)
	}
	// Second run: no further changes.
	updated, skipped, err := costs.Recompute(context.Background(), s, pricer, false)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if updated != 0 || skipped != 1 {
		t.Errorf("second run: got updated=%d skipped=%d, want 0/1", updated, skipped)
	}
}

func TestRecompute_MixedRows(t *testing.T) {
	s := testStore(t)
	// Zero-cost Opus 5 row: needs backfill to $30M.
	seedEvent(t, s, "evt-1", "sess-1", "claude-opus-5", 1_000_000, 1_000_000, 0, 0, 0)
	// Already-correct Sonnet 5 row.
	seedEvent(t, s, "evt-2", "sess-1", "claude-sonnet-5", 1_000_000, 1_000_000, 0, 0, 12_000_000)
	if _, err := s.DB().Exec(`UPDATE cost_events SET pricing_status = ? WHERE id = 'evt-2'`, costs.PricingKnown); err != nil {
		t.Fatal(err)
	}
	// Stale Opus 5 row at $90M should be corrected to $30M.
	seedEvent(t, s, "evt-3", "sess-1", "claude-opus-5", 1_000_000, 1_000_000, 0, 0, 90_000_000)
	// Unknown model with non-zero cost: leave alone.
	seedEvent(t, s, "evt-4", "sess-1", "totally-made-up-model", 1_000, 0, 0, 0, 1_234)

	updated, skipped, err := costs.Recompute(context.Background(), s, costs.NewPricer(costs.PricerConfig{}), false)
	if err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	if updated != 2 || skipped != 2 {
		t.Errorf("got updated=%d skipped=%d, want 2/2", updated, skipped)
	}

	got, err := s.TotalBySession("sess-1")
	if err != nil {
		t.Fatalf("TotalBySession: %v", err)
	}
	// $30M (Opus backfilled) + $12M (Sonnet unchanged) + $30M (Opus corrected) + $1234 (unknown, untouched).
	want := int64(30_000_000 + 12_000_000 + 30_000_000 + 1_234)
	if got.TotalCostMicrodollars != want {
		t.Errorf("total cost = %d, want %d", got.TotalCostMicrodollars, want)
	}
}

func TestRecompute_NilStore(t *testing.T) {
	_, _, err := costs.Recompute(context.Background(), nil, costs.NewPricer(costs.PricerConfig{}), false)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
}

func TestRecompute_NilPricer(t *testing.T) {
	s := testStore(t)
	_, _, err := costs.Recompute(context.Background(), s, nil, false)
	if err == nil {
		t.Fatal("expected error for nil pricer, got nil")
	}
}

func TestRecompute_CancelledContext(t *testing.T) {
	s := testStore(t)
	seedEvent(t, s, "evt-1", "sess-1", "claude-opus-5", 1_000_000, 1_000_000, 0, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := costs.Recompute(ctx, s, costs.NewPricer(costs.PricerConfig{}), false)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestRecomputeUpdatesPricingStatusWithoutChangingReconciliationOrRowCount(t *testing.T) {
	s := testStore(t)
	providerInput := int64(1_000_000)
	events := []costs.CostEvent{
		{ID: "known", SessionID: "s", Timestamp: time.Now(), Provider: "claude", SourceKind: "transcript", SourceIdentity: "known", Model: "claude-opus-5", InputTokens: 1_000_000, OutputTokens: 1_000_000, ProviderInputTokens: &providerInput, PricingStatus: costs.PricingUnknown, ReconciliationStatus: costs.ReconciliationAuthoritative},
		{ID: "zero", SessionID: "s", Timestamp: time.Now(), Provider: "test", SourceKind: "transcript", SourceIdentity: "zero", Model: "configured-free", InputTokens: 10, PricingStatus: costs.PricingUnknown, ReconciliationStatus: costs.ReconciliationAuthoritative},
		{ID: "legacy", SessionID: "s", Timestamp: time.Now(), Model: "gpt-5.5", InputTokens: 10, CostMicrodollars: 42},
	}
	for _, event := range events {
		if err := s.WriteCostEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	pricer := costs.NewPricer(costs.PricerConfig{Overrides: map[string]costs.PriceOverride{"configured-free": {}}})
	updated, skipped, err := costs.Recompute(context.Background(), s, pricer, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 || skipped != 1 {
		t.Fatalf("first recompute updated=%d skipped=%d", updated, skipped)
	}
	rows, err := s.DB().Query(`SELECT id, cost_microdollars, pricing_status, reconciliation_status FROM cost_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[string]struct {
		cost                    int64
		pricing, reconciliation string
	})
	for rows.Next() {
		var id, pricing, reconciliation string
		var cost int64
		if err := rows.Scan(&id, &cost, &pricing, &reconciliation); err != nil {
			t.Fatal(err)
		}
		got[id] = struct {
			cost                    int64
			pricing, reconciliation string
		}{cost, pricing, reconciliation}
	}
	if len(got) != 3 || got["known"].cost != 30_000_000 || got["known"].pricing != string(costs.PricingKnown) || got["known"].reconciliation != string(costs.ReconciliationAuthoritative) ||
		got["zero"].pricing != string(costs.PricingKnownZero) || got["legacy"].cost != 42 || got["legacy"].pricing != string(costs.PricingLegacyUnresolved) || got["legacy"].reconciliation != string(costs.ReconciliationLegacyUnreconciled) {
		t.Fatalf("recomputed rows=%+v", got)
	}
	updated, skipped, err = costs.Recompute(context.Background(), s, pricer, false)
	if err != nil || updated != 0 || skipped != 3 {
		t.Fatalf("second recompute updated=%d skipped=%d err=%v", updated, skipped, err)
	}
}
