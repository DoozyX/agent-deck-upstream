package costs

import "fmt"

// Coverage reports which token events contribute to the known-price subtotal.
// Unknown-priced and unreconciled token volume stays visible rather than being
// represented as confidently free.
type Coverage struct {
	EventCount             int   `json:"event_count"`
	TotalTokens            int64 `json:"total_tokens"`
	KnownPriceEventCount   int   `json:"known_price_event_count"`
	KnownPriceTokens       int64 `json:"known_price_tokens"`
	KnownZeroEventCount    int   `json:"known_zero_event_count"`
	UnknownPriceEventCount int   `json:"unknown_price_event_count"`
	UnreconciledEventCount int   `json:"unreconciled_event_count"`
	UnknownPriceTokens     int64 `json:"unknown_price_tokens"`
	UnreconciledTokens     int64 `json:"unreconciled_tokens"`
	CoverageKnown          bool  `json:"coverage_known"`
	Complete               bool  `json:"complete"`
}

// CoveredSummary preserves CostSummary's numeric fields as known-price
// subtotals and adds explicit completeness metadata.
type CoveredSummary struct {
	CostSummary
	Coverage Coverage `json:"coverage"`
}

// CostBreakdown is an auditable covered subtotal for one report dimension.
// Cache-write duration fields are subsets of CacheWriteInputTokens.
type CostBreakdown struct {
	Key                     string   `json:"key"`
	KnownCostMicrodollars   int64    `json:"known_cost_microdollars"`
	UncachedInputTokens     int64    `json:"uncached_input_tokens"`
	CacheReadInputTokens    int64    `json:"cache_read_input_tokens"`
	CacheWriteInputTokens   int64    `json:"cache_write_input_tokens"`
	CacheWrite5mInputTokens int64    `json:"cache_write_5m_input_tokens"`
	CacheWrite1hInputTokens int64    `json:"cache_write_1h_input_tokens"`
	OutputTokens            int64    `json:"output_tokens"`
	ReasoningOutputTokens   int64    `json:"reasoning_output_tokens"`
	Coverage                Coverage `json:"coverage"`
}

// MergeCoverage combines disjoint coverage windows. Coverage is known only
// when every input explicitly says so; an omitted/legacy input stays unknown.
func MergeCoverage(items ...Coverage) Coverage {
	if len(items) == 0 {
		return Coverage{}
	}
	out := Coverage{CoverageKnown: true, Complete: true}
	for _, item := range items {
		out.EventCount += item.EventCount
		out.TotalTokens += item.TotalTokens
		out.KnownPriceEventCount += item.KnownPriceEventCount
		out.KnownPriceTokens += item.KnownPriceTokens
		out.KnownZeroEventCount += item.KnownZeroEventCount
		out.UnknownPriceEventCount += item.UnknownPriceEventCount
		out.UnreconciledEventCount += item.UnreconciledEventCount
		out.UnknownPriceTokens += item.UnknownPriceTokens
		out.UnreconciledTokens += item.UnreconciledTokens
		out.CoverageKnown = out.CoverageKnown && item.CoverageKnown
		out.Complete = out.Complete && item.Complete
	}
	return out
}

// CostCoverageStatus is the stable human-readable status used by terminal
// consumers. A known zero is deliberately distinct from missing pricing.
func CostCoverageStatus(summary CoveredSummary) string {
	c := summary.Coverage
	if !c.CoverageKnown {
		return "coverage unknown"
	}
	if c.EventCount > 0 && c.KnownPriceEventCount == 0 && (c.UnknownPriceEventCount > 0 || c.UnreconciledEventCount > 0) {
		return "price unknown"
	}
	if c.Complete {
		if summary.TotalCostMicrodollars == 0 && c.EventCount > 0 {
			return "verified"
		}
		return "complete"
	}
	return "known subtotal"
}

func CoverageDetail(c Coverage) string {
	parts := ""
	if c.UnknownPriceEventCount > 0 {
		parts = fmt.Sprintf("%d unpriced event%s / %d tokens", c.UnknownPriceEventCount, plural(c.UnknownPriceEventCount), c.UnknownPriceTokens)
	}
	if c.UnreconciledEventCount > 0 {
		if parts != "" {
			parts += "; "
		}
		parts += fmt.Sprintf("%d unreconciled event%s / %d tokens", c.UnreconciledEventCount, plural(c.UnreconciledEventCount), c.UnreconciledTokens)
	}
	return parts
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
