package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestCostDashboardRendersUnknownKnownZeroAndMixedCoverage(t *testing.T) {
	tests := []struct {
		name      string
		summary   costs.CoveredSummary
		want      []string
		forbidden string
	}{
		{name: "unknown", summary: costs.CoveredSummary{CostSummary: costs.CostSummary{EventCount: 1}, Coverage: costs.Coverage{EventCount: 1, UnknownPriceEventCount: 1, UnknownPriceTokens: 11, CoverageKnown: true}}, want: []string{"price unknown", "1 unpriced event / 11 tokens", "incomplete"}},
		{name: "known zero", summary: costs.CoveredSummary{CostSummary: costs.CostSummary{EventCount: 1}, Coverage: costs.Coverage{EventCount: 1, KnownPriceEventCount: 1, KnownZeroEventCount: 1, CoverageKnown: true, Complete: true}}, want: []string{"$0.00", "verified"}, forbidden: "price unknown"},
		{name: "mixed", summary: costs.CoveredSummary{CostSummary: costs.CostSummary{TotalCostMicrodollars: 500_000, EventCount: 2}, Coverage: costs.Coverage{EventCount: 2, KnownPriceEventCount: 1, UnknownPriceEventCount: 1, UnknownPriceTokens: 7, CoverageKnown: true}}, want: []string{"$0.50 known subtotal", "1 unpriced event / 7 tokens", "incomplete"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := costDashboard{today: tt.summary, week: tt.summary, month: tt.summary, projected: tt.summary.TotalCostMicrodollars}
			got := d.View()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("view missing %q:\n%s", want, got)
				}
			}
			if tt.forbidden != "" && strings.Contains(got, tt.forbidden) {
				t.Fatalf("view contains %q:\n%s", tt.forbidden, got)
			}
		})
	}
}
