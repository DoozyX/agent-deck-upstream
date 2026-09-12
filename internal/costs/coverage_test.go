package costs_test

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestPricingCoverageKnownZeroUnknownLegacyAndSuperseded(t *testing.T) {
	store := testStore(t)
	now := time.Now()
	events := []costs.CostEvent{
		{ID: "known", SessionID: "session", Timestamp: now, Provider: "test", SourceKind: "test", SourceIdentity: "known", Model: "known", InputTokens: 1, OutputTokens: 1, CostMicrodollars: 100, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative},
		{ID: "zero", SessionID: "session", Timestamp: now, Provider: "test", SourceKind: "test", SourceIdentity: "zero", Model: "zero", InputTokens: 3, PricingStatus: costs.PricingKnownZero, ReconciliationStatus: costs.ReconciliationAuthoritative},
		{ID: "unknown", SessionID: "session", Timestamp: now, Provider: "test", SourceKind: "test", SourceIdentity: "unknown", Model: "unknown", CacheReadTokens: 4, PricingStatus: costs.PricingUnknown, ReconciliationStatus: costs.ReconciliationAuthoritative},
		{ID: "legacy", SessionID: "session", Timestamp: now, Model: "legacy", InputTokens: 5, CostMicrodollars: 999},
		{ID: "superseded", SessionID: "session", Timestamp: now, Provider: "test", SourceKind: "test", SourceIdentity: "superseded", Model: "old", InputTokens: 100, CostMicrodollars: 1000, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationLegacySuperseded},
	}
	for _, event := range events {
		if err := store.WriteCostEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := store.CoveredTotalBySession("session")
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalCostMicrodollars != 100 || summary.EventCount != 4 || summary.TotalInputTokens != 13 || summary.TotalOutputTokens != 1 {
		t.Fatalf("known subtotal summary=%+v", summary.CostSummary)
	}
	want := costs.Coverage{
		EventCount: 4, TotalTokens: 14, KnownPriceEventCount: 2, KnownPriceTokens: 5,
		KnownZeroEventCount: 1, UnknownPriceEventCount: 1, UnreconciledEventCount: 1,
		UnknownPriceTokens: 4, UnreconciledTokens: 5, CoverageKnown: true, Complete: false,
	}
	if summary.Coverage != want {
		t.Fatalf("coverage=%+v want=%+v", summary.Coverage, want)
	}
}
