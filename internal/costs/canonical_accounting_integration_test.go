package costs_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestCanonicalAccountingReplayExportAndIdempotency(t *testing.T) {
	root := t.TempDir()
	claudePath := filepath.Join(root, "claude.jsonl")
	codexPath := filepath.Join(root, "codex.jsonl")
	claudeBase := copyFixture(t, filepath.Join("testdata", "canonical", "claude.jsonl"), claudePath)
	copyFixture(t, filepath.Join("testdata", "canonical", "codex.jsonl"), codexPath)
	sources := []costs.TranscriptSource{
		{Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeDirect, Identity: "claude:canonical", Path: claudePath, SessionID: "claude-session", ParentSessionID: "parent", RunID: "claude-run"},
		{Provider: costs.ProviderCodex, Kind: costs.SourceKindCodexRollout, Identity: "codex:canonical", Path: codexPath, SessionID: "codex-session", ParentSessionID: "parent", RunID: "codex-run"},
	}
	store := testStore(t)
	pricer := costs.NewPricer(costs.PricerConfig{})

	first := costs.Sync(context.Background(), store, pricer, sources)
	if len(first.Errors) != 0 || first.SourcesChanged != 2 || first.EventsImported != 3 {
		t.Fatalf("first sync=%+v", first)
	}
	assertCanonicalExportTotals(t, store, canonicalTotals{
		events: 3, input: 2_000_100, cacheRead: 2_000_000, cacheWrite: 4_000_000,
		cacheWrite5m: 1_000_000, cacheWrite1h: 1_000_000, cacheWriteUnknown: 2_000_000,
		output: 2_000_050, reasoning: 300_010, knownCost: 50_600_000,
		knownEvents: 2, knownTokens: 10_000_000, unknownEvents: 1, unknownTokens: 150,
		authoritativeEvents: 3,
	})

	claudeQuote := pricer.Quote("claude-sonnet-5", costs.TokenUsage{
		InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 3_000_000,
		CacheWrite5mTokens: 1_000_000, CacheWrite1hTokens: 1_000_000,
		OutputTokens: 1_000_000, ReasoningTokens: 200_000,
	})
	if !claudeQuote.Valid || claudeQuote.CostMicrodollars != 21_200_000 {
		t.Fatalf("duration-aware Claude quote=%+v, want 21,200,000", claudeQuote)
	}

	second := costs.Sync(context.Background(), store, pricer, sources)
	if len(second.Errors) != 0 || second.SourcesChanged != 0 || second.EventsImported != 0 {
		t.Fatalf("unchanged second sync=%+v", second)
	}
	appended := []byte(`{"type":"assistant","uuid":"canonical-claude-2","requestId":"canonical-request-2","timestamp":"2026-09-12T10:03:00Z","message":{"id":"canonical-message-2","model":"claude-sonnet-5","content":[{"type":"text","text":"synthetic append"}],"usage":{"input_tokens":100,"cache_read_input_tokens":200,"cache_creation_input_tokens":300,"output_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":100},"output_tokens_details":{"thinking_tokens":50}}}}` + "\n")
	if err := os.WriteFile(claudePath, append(claudeBase, appended...), 0o600); err != nil {
		t.Fatal(err)
	}
	third := costs.Sync(context.Background(), store, pricer, sources)
	if len(third.Errors) != 0 || third.SourcesChanged != 1 || third.EventsImported != 1 {
		t.Fatalf("append sync=%+v", third)
	}
	assertCanonicalExportTotals(t, store, canonicalTotals{
		events: 4, input: 2_000_200, cacheRead: 2_000_200, cacheWrite: 4_000_300,
		cacheWrite5m: 1_000_100, cacheWrite1h: 1_000_100, cacheWriteUnknown: 2_000_100,
		output: 2_000_450, reasoning: 300_060, knownCost: 50_605_140,
		knownEvents: 3, knownTokens: 10_001_000, unknownEvents: 1, unknownTokens: 150,
		authoritativeEvents: 4,
	})

	// This integration test is also the contraction guard: the canonical
	// replay above must not coexist with the removed Claude-only historian.
	source, err := os.ReadFile("sync.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(source, []byte("SyncFromTranscripts")) || bytes.Contains(source, []byte("uuid.NewString")) {
		t.Fatal("canonical replay still coexists with obsolete random-identity historical sync")
	}
}

type canonicalTotals struct {
	events, knownEvents, unknownEvents, authoritativeEvents, unreconciledEvents int
	input, cacheRead, cacheWrite, cacheWrite5m, cacheWrite1h, cacheWriteUnknown int64
	output, reasoning, knownCost, knownTokens, unknownTokens                    int64
}

func assertCanonicalExportTotals(t *testing.T, store *costs.Store, want canonicalTotals) {
	t.Helper()
	from := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	events, err := store.EventsByDateRange(from, to)
	if err != nil {
		t.Fatal(err)
	}
	var got canonicalTotals
	got.events = len(events)
	for _, event := range events {
		got.input += event.InputTokens
		got.cacheRead += event.CacheReadTokens
		got.cacheWrite += event.CacheWriteTokens
		got.cacheWrite5m += event.CacheWrite5mTokens
		got.cacheWrite1h += event.CacheWrite1hTokens
		got.cacheWriteUnknown += event.CacheWriteTokens - event.CacheWrite5mTokens - event.CacheWrite1hTokens
		got.output += event.OutputTokens
		got.reasoning += event.ReasoningTokens
		tokens := event.InputTokens + event.CacheReadTokens + event.CacheWriteTokens + event.OutputTokens
		switch event.PricingStatus {
		case costs.PricingKnown, costs.PricingKnownZero:
			got.knownEvents++
			got.knownTokens += tokens
			got.knownCost += event.CostMicrodollars
		case costs.PricingUnknown:
			got.unknownEvents++
			got.unknownTokens += tokens
		}
		switch event.ReconciliationStatus {
		case costs.ReconciliationAuthoritative:
			got.authoritativeEvents++
		case costs.ReconciliationLegacyUnreconciled:
			got.unreconciledEvents++
		}
		if event.ReasoningTokens > event.OutputTokens {
			t.Fatalf("reasoning is not an output subset: %+v", event)
		}
	}
	if got != want {
		t.Fatalf("canonical export totals=%+v want=%+v", got, want)
	}
	byDay, err := store.CoveredCostByDay()
	if err != nil {
		t.Fatal(err)
	}
	if len(byDay) != 1 {
		t.Fatalf("covered day breakdown=%+v, want one UTC day", byDay)
	}
	coverage := byDay[0].Coverage
	if coverage.KnownPriceEventCount != want.knownEvents || coverage.KnownPriceTokens != want.knownTokens ||
		coverage.UnknownPriceEventCount != want.unknownEvents || coverage.UnknownPriceTokens != want.unknownTokens ||
		coverage.UnreconciledEventCount != want.unreconciledEvents || coverage.Complete {
		t.Fatalf("covered export coverage=%+v, want known=%d/%d unknown=%d/%d unreconciled=%d incomplete",
			coverage, want.knownEvents, want.knownTokens, want.unknownEvents, want.unknownTokens, want.unreconciledEvents)
	}
}
