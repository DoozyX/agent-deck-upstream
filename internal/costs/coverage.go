package costs

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
