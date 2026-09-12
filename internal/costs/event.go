package costs

import (
	"fmt"
	"time"
)

type PricingStatus string

const (
	PricingKnown            PricingStatus = "known"
	PricingKnownZero        PricingStatus = "known_zero"
	PricingUnknown          PricingStatus = "unknown"
	PricingLegacyUnresolved PricingStatus = "legacy_unresolved"
)

type ReconciliationStatus string

const (
	ReconciliationAuthoritative      ReconciliationStatus = "authoritative"
	ReconciliationLegacyUnreconciled ReconciliationStatus = "legacy_unreconciled"
	ReconciliationLegacySuperseded   ReconciliationStatus = "legacy_superseded"
)

// TokenUsage stores mutually exclusive input categories. ReasoningTokens is a
// subset of OutputTokens and is never added to TotalTokens.
type TokenUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheWriteTokens    int64
	CacheWrite5mTokens  int64
	CacheWrite1hTokens  int64
	ReasoningTokens     int64
	ProviderInputTokens *int64
}

// Validate checks the canonical exclusive-token invariants.
func (u TokenUsage) Validate() error {
	values := []struct {
		name  string
		value int64
	}{
		{"input_tokens", u.InputTokens},
		{"output_tokens", u.OutputTokens},
		{"cache_read_tokens", u.CacheReadTokens},
		{"cache_write_tokens", u.CacheWriteTokens},
		{"cache_write_5m_tokens", u.CacheWrite5mTokens},
		{"cache_write_1h_tokens", u.CacheWrite1hTokens},
		{"reasoning_tokens", u.ReasoningTokens},
	}
	for _, field := range values {
		if field.value < 0 {
			return fmt.Errorf("%s must not be negative", field.name)
		}
	}
	if u.ReasoningTokens > u.OutputTokens {
		return fmt.Errorf("reasoning_tokens must not exceed output_tokens")
	}
	if u.CacheWrite5mTokens+u.CacheWrite1hTokens > u.CacheWriteTokens {
		return fmt.Errorf("cache write duration subsets must not exceed cache_write_tokens")
	}
	if u.ProviderInputTokens != nil {
		cached := u.CacheReadTokens + u.CacheWriteTokens
		if *u.ProviderInputTokens < cached {
			return fmt.Errorf("provider_input_tokens is less than cached input subsets")
		}
	}
	return nil
}

// TotalInputTokens returns all exclusive input categories exactly once.
func (u TokenUsage) TotalInputTokens() int64 {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// TotalTokens returns all input plus output. Reasoning is already included in
// output and therefore is not added separately.
func (u TokenUsage) TotalTokens() int64 {
	return u.TotalInputTokens() + u.OutputTokens
}

// CacheWriteUnknownTokens returns the aggregate cache-write residual for which
// the provider did not report a 5-minute or 1-hour duration.
func (u TokenUsage) CacheWriteUnknownTokens() int64 {
	return u.CacheWriteTokens - u.CacheWrite5mTokens - u.CacheWrite1hTokens
}

// UsageEvent is the canonical, provider-aware ledger event. SourceIdentity is
// provider-native and deliberately independent of mutable Agent Deck session
// titles and instance IDs.
type UsageEvent struct {
	ID                   string
	Provider             string
	SourceKind           string
	SourceIdentity       string
	TranscriptIdentity   string
	SessionID            string
	ParentSessionID      string
	RunID                string
	Timestamp            time.Time
	Model                string
	Usage                TokenUsage
	CostMicrodollars     int64
	PricingStatus        PricingStatus
	ReconciliationStatus ReconciliationStatus
	SupersedesEventIDs   []string
}

// UsageEventFromCostEvent keeps legacy callers source-compatible while making
// every write use the canonical ledger shape. Empty legacy metadata is marked
// unresolved; it is never guessed from a mutable title or model alias.
func UsageEventFromCostEvent(event CostEvent) UsageEvent {
	provider := event.Provider
	if provider == "" {
		provider = "unknown"
	}
	sourceKind := event.SourceKind
	if sourceKind == "" {
		sourceKind = "legacy"
	}
	pricingStatus := event.PricingStatus
	if pricingStatus == "" {
		pricingStatus = PricingLegacyUnresolved
	}
	reconciliationStatus := event.ReconciliationStatus
	if reconciliationStatus == "" {
		reconciliationStatus = ReconciliationLegacyUnreconciled
	}
	return UsageEvent{
		ID: event.ID, Provider: provider, SourceKind: sourceKind,
		SourceIdentity: event.SourceIdentity, TranscriptIdentity: event.TranscriptIdentity,
		SessionID: event.SessionID, ParentSessionID: event.ParentSessionID, RunID: event.RunID,
		Timestamp: event.Timestamp, Model: event.Model,
		Usage: TokenUsage{
			InputTokens: event.InputTokens, OutputTokens: event.OutputTokens,
			CacheReadTokens: event.CacheReadTokens, CacheWriteTokens: event.CacheWriteTokens,
			CacheWrite5mTokens: event.CacheWrite5mTokens, CacheWrite1hTokens: event.CacheWrite1hTokens,
			ReasoningTokens: event.ReasoningTokens, ProviderInputTokens: event.ProviderInputTokens,
		},
		CostMicrodollars: event.CostMicrodollars, PricingStatus: pricingStatus,
		ReconciliationStatus: reconciliationStatus,
	}
}
