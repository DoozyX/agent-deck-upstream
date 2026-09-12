package costs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPricingQuoteStatusesDurationsAndVerifiedSources(t *testing.T) {
	p := NewPricer(PricerConfig{
		Overrides: map[string]PriceOverride{
			"configured-free": {},
		},
	})
	tests := []struct {
		name, model, status, source string
		usage                       TokenUsage
		cost                        int64
		valid                       bool
	}{
		{
			name: "verified OpenAI standard", model: "gpt-5.6-sol", status: string(PricingKnown), source: "https://developers.openai.com/api/docs/pricing",
			usage: TokenUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000, OutputTokens: 1_000_000},
			cost:  29_400_000, valid: true,
		},
		{
			name: "verified OpenAI Luna", model: "gpt-5.6-luna", status: string(PricingKnown), source: "https://developers.openai.com/api/docs/pricing",
			usage: TokenUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000, OutputTokens: 1_000_000},
			cost:  1_670_000, valid: true,
		},
		{
			name: "verified OpenAI Terra", model: "gpt-5.6-terra", status: string(PricingKnown), source: "https://developers.openai.com/api/docs/pricing",
			usage: TokenUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000, OutputTokens: 1_000_000},
			cost:  16_700_000, valid: true,
		},
		{
			name: "verified OpenAI Astra", model: "gpt-6-astra", status: string(PricingKnown), source: "https://developers.openai.com/api/docs/pricing",
			usage: TokenUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000, OutputTokens: 1_000_000},
			cost:  73_500_000, valid: true,
		},
		{
			name: "Claude duration subsets and residual", model: "claude-opus-5", status: string(PricingKnown), source: "https://platform.claude.com/docs/en/about-claude/pricing",
			usage: TokenUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 3_000_000, CacheWrite5mTokens: 1_000_000, CacheWrite1hTokens: 1_000_000, OutputTokens: 1_000_000},
			cost:  53_000_000, valid: true,
		},
		{name: "configured known zero", model: "configured-free", status: string(PricingKnownZero), source: "override", usage: TokenUsage{InputTokens: 1_000_000}, cost: 0, valid: true},
		{name: "unknown alias", model: "gpt-5.5", status: string(PricingUnknown), usage: TokenUsage{InputTokens: 1_000_000}, cost: 0, valid: true},
		{name: "unverified exact alias", model: "claude-fable-5-1", status: string(PricingUnknown), usage: TokenUsage{InputTokens: 1_000_000}, cost: 0, valid: true},
		{name: "invalid cache duration subsets", model: "claude-opus-5", status: string(PricingUnknown), usage: TokenUsage{CacheWriteTokens: 1, CacheWrite5mTokens: 1, CacheWrite1hTokens: 1}, valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quote := p.Quote(tt.model, tt.usage)
			if string(quote.Status) != tt.status || quote.CostMicrodollars != tt.cost || quote.Valid != tt.valid {
				t.Fatalf("quote=%+v", quote)
			}
			if tt.source != "" && quote.Source != tt.source {
				t.Fatalf("source=%q want %q", quote.Source, tt.source)
			}
			if tt.source != "" && tt.source != "override" && quote.VerifiedAt != "2026-09-12" {
				t.Fatalf("verified_at=%q", quote.VerifiedAt)
			}
		})
	}
}

func TestPricerExactDatedOverridePrecedesNormalizedBuiltIn(t *testing.T) {
	p := NewPricer(PricerConfig{Overrides: map[string]PriceOverride{
		"claude-sonnet-5-20260301": {InputPerMtok: 99},
	}})
	quote := p.Quote("claude-sonnet-5-20260301", TokenUsage{InputTokens: 1_000_000})
	if quote.Source != "override" || quote.CostMicrodollars != 99_000_000 {
		t.Fatalf("quote=%+v, want exact dated override", quote)
	}
}

func TestPricingCatalogOnlyMarksEvidenceBackedEntriesKnown(t *testing.T) {
	p := NewPricer(PricerConfig{})
	verified := map[string]string{
		"gpt-5.6-luna": openAIPriceSource, "gpt-5.6-terra": openAIPriceSource,
		"gpt-5.6-sol": openAIPriceSource, "gpt-6-astra": openAIPriceSource,
		"claude-opus-5": anthropicPriceSource, "claude-sonnet-5": anthropicPriceSource,
		"claude-haiku-4-5": anthropicPriceSource,
	}
	for model, source := range verified {
		quote := p.Quote(model, TokenUsage{InputTokens: 1})
		if quote.Status != PricingKnown || quote.Source != source || quote.VerifiedAt != "2026-09-12" {
			t.Fatalf("verified model %q quote=%+v", model, quote)
		}
	}
	for _, model := range []string{
		"claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6",
		"gemini-2.5-pro", "gemini-2.5-flash", "gpt-4o", "gpt-4.1", "o3", "o4-mini",
		"MiniMax-M3", "MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed",
	} {
		quote := p.Quote(model, TokenUsage{InputTokens: 1})
		if quote.Status != PricingUnknown || quote.Source != "" || quote.VerifiedAt != "" {
			t.Fatalf("unverified model %q quote=%+v", model, quote)
		}
	}
}

func TestVerifiedPricingRatesIncludeExactCacheDurations(t *testing.T) {
	p := NewPricer(PricerConfig{})
	tests := []struct {
		model                                        string
		input, output, read, write, write5m, write1h int64
	}{
		{"gpt-5.6-luna", 200_000, 1_200_000, 20_000, 250_000, 250_000, 250_000},
		{"gpt-5.6-terra", 2_000_000, 12_000_000, 200_000, 2_500_000, 2_500_000, 2_500_000},
		{"gpt-5.6-sol", 4_000_000, 20_000_000, 400_000, 5_000_000, 5_000_000, 5_000_000},
		{"gpt-6-astra", 10_000_000, 50_000_000, 1_000_000, 12_500_000, 12_500_000, 12_500_000},
		{"claude-opus-5", 5_000_000, 25_000_000, 500_000, 6_250_000, 6_250_000, 10_000_000},
		{"claude-sonnet-5", 2_000_000, 10_000_000, 200_000, 2_500_000, 2_500_000, 4_000_000},
		{"claude-haiku-4-5", 1_000_000, 5_000_000, 100_000, 1_250_000, 1_250_000, 2_000_000},
	}
	for _, tt := range tests {
		price, ok := p.GetPrice(tt.model)
		if !ok || price.InputPerMtokMicro != tt.input || price.OutputPerMtokMicro != tt.output ||
			price.CacheReadPerMtokMicro != tt.read || price.CacheWritePerMtokMicro != tt.write ||
			price.CacheWrite5mPerMtokMicro != tt.write5m || price.CacheWrite1hPerMtokMicro != tt.write1h {
			t.Fatalf("model %q price=%+v ok=%v", tt.model, price, ok)
		}
	}
}

func TestHardcodedPricing(t *testing.T) {
	p := NewPricer(PricerConfig{})
	mp, ok := p.GetPrice("claude-sonnet-5")
	assert.True(t, ok)
	assert.Greater(t, mp.InputPerMtokMicro, int64(0))
	assert.Greater(t, mp.OutputPerMtokMicro, int64(0))
	assert.Greater(t, mp.CacheReadPerMtokMicro, int64(0))
	assert.Greater(t, mp.CacheWritePerMtokMicro, int64(0))
}

func TestPricerComputeCost(t *testing.T) {
	p := NewPricer(PricerConfig{})
	// claude-sonnet-5: input=$2/Mtok, output=$10/Mtok.
	cost := p.ComputeCost("claude-sonnet-5", 1_000_000, 1_000_000, 0, 0)
	assert.Equal(t, int64(12_000_000), cost)
}

func TestPricerCacheFile(t *testing.T) {
	dir := t.TempDir()
	p := NewPricer(PricerConfig{CachePath: dir})

	// Save custom pricing
	err := p.SaveCache(map[string]pricingCacheModel{
		"custom-model": {
			InputPerMtok:        5.0,
			OutputPerMtok:       20.0,
			CacheWritePerMtok:   6.25,
			CacheWrite5mPerMtok: 6.25,
			CacheWrite1hPerMtok: 10.0,
		},
	})
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(filepath.Join(dir, "pricing.json"))
	require.NoError(t, err)

	// Load and verify
	p2 := NewPricer(PricerConfig{CachePath: dir})
	err = p2.LoadCache()
	require.NoError(t, err)

	mp, ok := p2.GetPrice("custom-model")
	assert.True(t, ok)
	assert.Equal(t, int64(5_000_000), mp.InputPerMtokMicro)
	assert.Equal(t, int64(20_000_000), mp.OutputPerMtokMicro)
	assert.Equal(t, int64(6_250_000), mp.CacheWritePerMtokMicro)
	assert.Equal(t, int64(6_250_000), mp.CacheWrite5mPerMtokMicro)
	assert.Equal(t, int64(10_000_000), mp.CacheWrite1hPerMtokMicro)
}

func TestPricerUserOverride(t *testing.T) {
	p := NewPricer(PricerConfig{
		Overrides: map[string]PriceOverride{
			"claude-sonnet-4-6": {
				InputPerMtok:  99.0,
				OutputPerMtok: 99.0,
			},
		},
	})
	mp, ok := p.GetPrice("claude-sonnet-4-6")
	assert.True(t, ok)
	// Override should take precedence over hardcoded
	assert.Equal(t, int64(99_000_000), mp.InputPerMtokMicro)
	assert.Equal(t, int64(99_000_000), mp.OutputPerMtokMicro)
}

func TestUnverifiedMiniMaxPricingIsUnknown(t *testing.T) {
	p := NewPricer(PricerConfig{})
	for _, model := range []string{"MiniMax-M3", "MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed"} {
		if _, ok := p.GetPrice(model); ok {
			t.Fatalf("unverified model %s unexpectedly has known pricing", model)
		}
	}
}

func TestUnverifiedMiniMaxComputeCostIsZeroSubtotal(t *testing.T) {
	p := NewPricer(PricerConfig{})
	cost := p.ComputeCost("MiniMax-M2.7", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	assert.Equal(t, int64(0), cost)
}

func TestPricerModelNormalization(t *testing.T) {
	p := NewPricer(PricerConfig{})
	mp, ok := p.GetPrice("claude-haiku-4-5-20251001")
	assert.True(t, ok)
	assert.Equal(t, int64(1_000_000), mp.InputPerMtokMicro)
}

// TestAnthropicPricing pins exact rates for every Anthropic model in defaults
// against Anthropic's published rates. Source:
// https://docs.anthropic.com/en/docs/about-claude/pricing
// Cache-write column is the 5-minute TTL rate (the cache write rate the
// pricing.json schema represents).
func TestAnthropicPricing(t *testing.T) {
	p := NewPricer(PricerConfig{})

	tests := []struct {
		model      string
		input      int64
		output     int64
		cacheRead  int64
		cacheWrite int64
	}{
		{"claude-opus-5", 5_000_000, 25_000_000, 500_000, 6_250_000},
		{"claude-sonnet-5", 2_000_000, 10_000_000, 200_000, 2_500_000},
		{"claude-haiku-4-5", 1_000_000, 5_000_000, 100_000, 1_250_000},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			mp, ok := p.GetPrice(tt.model)
			require.True(t, ok, "model %s should have pricing", tt.model)
			assert.Equal(t, tt.input, mp.InputPerMtokMicro, "input")
			assert.Equal(t, tt.output, mp.OutputPerMtokMicro, "output")
			assert.Equal(t, tt.cacheRead, mp.CacheReadPerMtokMicro, "cache_read")
			assert.Equal(t, tt.cacheWrite, mp.CacheWritePerMtokMicro, "cache_write")
		})
	}
}
