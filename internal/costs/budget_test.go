package costs_test

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestBudgetCheck_NoBudget(t *testing.T) {
	s := testStore(t)
	b := costs.NewBudgetChecker(costs.BudgetConfig{}, s)
	result := b.Check("sess-1", "group-1")
	if result.Action != costs.BudgetActionNone {
		t.Errorf("action = %v, want None", result.Action)
	}
}

func TestBudgetCheck_Warning(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	if err := s.WriteCostEvent(costs.CostEvent{ID: "e1", SessionID: "s1", Timestamp: now, Model: "m", CostMicrodollars: 40_000_000, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative}); err != nil {
		t.Fatal(err)
	}

	b := costs.NewBudgetChecker(costs.BudgetConfig{DailyLimit: 50_000_000}, s)
	result := b.Check("s1", "group-1")
	if result.Action != costs.BudgetActionWarn {
		t.Errorf("action = %v, want Warn (80%%)", result.Action)
	}
}

func TestBudgetCheck_Stop(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	if err := s.WriteCostEvent(costs.CostEvent{ID: "e1", SessionID: "s1", Timestamp: now, Model: "m", CostMicrodollars: 51_000_000, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative}); err != nil {
		t.Fatal(err)
	}

	b := costs.NewBudgetChecker(costs.BudgetConfig{DailyLimit: 50_000_000}, s)
	result := b.Check("s1", "group-1")
	if result.Action != costs.BudgetActionStop {
		t.Errorf("action = %v, want Stop (100%%+)", result.Action)
	}
}

func TestBudgetCheckTx_SessionLimit(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	if err := s.WriteCostEvent(costs.CostEvent{ID: "e1", SessionID: "s1", Timestamp: now, Model: "m", CostMicrodollars: 100_000_000, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative}); err != nil {
		t.Fatal(err)
	}

	b := costs.NewBudgetChecker(costs.BudgetConfig{
		SessionLimits: map[string]int64{"s1": 90_000_000},
	}, s)

	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := b.CheckTx(tx, "s1", "group-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != costs.BudgetActionStop {
		t.Errorf("action = %v, want Stop", result.Action)
	}
	if result.Reason != "session lifetime limit exceeded" {
		t.Errorf("reason = %q", result.Reason)
	}
}

func TestBudgetCheck_UnderThreshold(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	if err := s.WriteCostEvent(costs.CostEvent{ID: "e1", SessionID: "s1", Timestamp: now, Model: "m", CostMicrodollars: 10_000_000, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative}); err != nil {
		t.Fatal(err)
	}

	b := costs.NewBudgetChecker(costs.BudgetConfig{DailyLimit: 50_000_000}, s)
	result := b.Check("s1", "group-1")
	if result.Action != costs.BudgetActionNone {
		t.Errorf("action = %v, want None (20%%)", result.Action)
	}
}

func TestBudgetCoverageUnknownPriceIsNotFreeHeadroom(t *testing.T) {
	s := testStore(t)
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "unknown", SessionID: "s1", Timestamp: time.Now(), Provider: "codex", SourceKind: "rollout", SourceIdentity: "unknown-request",
		Model: "gpt-5.5", InputTokens: 1_000_000, PricingStatus: costs.PricingUnknown, ReconciliationStatus: costs.ReconciliationAuthoritative,
	}); err != nil {
		t.Fatal(err)
	}
	b := costs.NewBudgetChecker(costs.BudgetConfig{DailyLimit: 50_000_000}, s)
	result := b.Check("s1", "group")
	if result.Action != costs.BudgetActionWarn || !result.CoverageIncomplete || result.UnknownPriceTokens != 1_000_000 {
		t.Fatalf("budget result=%+v", result)
	}
	if result.Reason != "budget price coverage incomplete" || result.UsedMicro != 0 || result.LimitMicro != 50_000_000 {
		t.Fatalf("budget metadata=%+v", result)
	}
}

func TestBudgetStopPreservesEarlierScopeIncompleteCoverage(t *testing.T) {
	s := testStore(t)
	old := time.Now().Add(-48 * time.Hour)
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "old-unknown", SessionID: "session", Timestamp: old,
		Provider: "test", SourceKind: "test", SourceIdentity: "old-unknown",
		Model: "unknown", InputTokens: 11, PricingStatus: costs.PricingUnknown,
		ReconciliationStatus: costs.ReconciliationAuthoritative,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "today-known", SessionID: "other", Timestamp: time.Now(),
		Provider: "test", SourceKind: "test", SourceIdentity: "today-known",
		Model: "known", CostMicrodollars: 100, PricingStatus: costs.PricingKnown,
		ReconciliationStatus: costs.ReconciliationAuthoritative,
	}); err != nil {
		t.Fatal(err)
	}
	checker := costs.NewBudgetChecker(costs.BudgetConfig{
		SessionLimits: map[string]int64{"session": 1000}, DailyLimit: 50, Timezone: time.UTC,
	}, s)
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := checker.CheckTx(tx, "session", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != costs.BudgetActionStop || !result.CoverageIncomplete || result.UnknownPriceTokens != 11 {
		t.Fatalf("budget result=%+v; stop must retain earlier incomplete coverage", result)
	}
}
