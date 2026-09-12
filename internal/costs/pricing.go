package costs

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// ModelPrice holds per-model pricing in microdollars per million tokens.
type ModelPrice struct {
	InputPerMtokMicro        int64
	OutputPerMtokMicro       int64
	CacheReadPerMtokMicro    int64
	CacheWritePerMtokMicro   int64
	CacheWrite5mPerMtokMicro int64
	CacheWrite1hPerMtokMicro int64
	SourceURL                string
	VerifiedAt               string
}

type PriceQuote struct {
	CostMicrodollars int64
	Status           PricingStatus
	NormalizedModel  string
	Source           string
	VerifiedAt       string
	Valid            bool
	Error            string
}

// PriceOverride holds user-configured pricing in USD per million tokens.
type PriceOverride struct {
	InputPerMtok        float64 `toml:"input_per_mtok"`
	OutputPerMtok       float64 `toml:"output_per_mtok"`
	CacheReadPerMtok    float64 `toml:"cache_read_per_mtok"`
	CacheWritePerMtok   float64 `toml:"cache_write_per_mtok"`
	CacheWrite5mPerMtok float64 `toml:"cache_write_5m_per_mtok"`
	CacheWrite1hPerMtok float64 `toml:"cache_write_1h_per_mtok"`
}

// PricerConfig configures the Pricer.
type PricerConfig struct {
	CachePath string
	Overrides map[string]PriceOverride
}

// Pricer resolves model pricing with fallback: override > cache > hardcoded.
type Pricer struct {
	defaults        map[string]ModelPrice
	cached          map[string]ModelPrice
	overrides       map[string]ModelPrice
	cachePath       string
	cacheTime       time.Time
	cacheProvenance string
}

type pricingCacheFile struct {
	FetchedAt   time.Time                    `json:"fetched_at,omitempty"`
	RefreshedAt time.Time                    `json:"refreshed_at,omitempty"`
	Provenance  string                       `json:"provenance,omitempty"`
	Models      map[string]pricingCacheModel `json:"models"`
}

type pricingCacheModel struct {
	InputPerMtok        float64 `json:"input_per_mtok"`
	OutputPerMtok       float64 `json:"output_per_mtok"`
	CacheReadPerMtok    float64 `json:"cache_read_per_mtok"`
	CacheWritePerMtok   float64 `json:"cache_write_per_mtok"`
	CacheWrite5mPerMtok float64 `json:"cache_write_5m_per_mtok,omitempty"`
	CacheWrite1hPerMtok float64 `json:"cache_write_1h_per_mtok,omitempty"`
	SourceURL           string  `json:"source_url,omitempty"`
	VerifiedAt          string  `json:"verified_at,omitempty"`
}

func (p *Pricer) Quote(model string, usage TokenUsage) PriceQuote {
	normalized := normalizeModel(model)
	if err := usage.Validate(); err != nil {
		return PriceQuote{Status: PricingUnknown, NormalizedModel: normalized, Valid: false, Error: err.Error()}
	}
	price, source, ok := p.lookupPrice(normalized)
	if !ok {
		return PriceQuote{Status: PricingUnknown, NormalizedModel: normalized, Valid: true}
	}
	status := PricingKnown
	if priceIsZero(price) {
		status = PricingKnownZero
	}
	writeResidual := usage.CacheWriteUnknownTokens()
	cost := tokenCost(usage.InputTokens, price.InputPerMtokMicro) +
		tokenCost(usage.OutputTokens, price.OutputPerMtokMicro) +
		tokenCost(usage.CacheReadTokens, price.CacheReadPerMtokMicro) +
		tokenCost(writeResidual, price.CacheWritePerMtokMicro) +
		tokenCost(usage.CacheWrite5mTokens, price.CacheWrite5mPerMtokMicro) +
		tokenCost(usage.CacheWrite1hTokens, price.CacheWrite1hPerMtokMicro)
	return PriceQuote{
		CostMicrodollars: cost, Status: status, NormalizedModel: normalized,
		Source: source, VerifiedAt: price.VerifiedAt, Valid: true,
	}
}

func usdToMicro(usd float64) int64 {
	return int64(math.Round(usd * 1_000_000))
}

func priceFromUSD(input, output, cacheRead, cacheWrite float64) ModelPrice {
	return ModelPrice{
		InputPerMtokMicro:        usdToMicro(input),
		OutputPerMtokMicro:       usdToMicro(output),
		CacheReadPerMtokMicro:    usdToMicro(cacheRead),
		CacheWritePerMtokMicro:   usdToMicro(cacheWrite),
		CacheWrite5mPerMtokMicro: usdToMicro(cacheWrite),
		CacheWrite1hPerMtokMicro: usdToMicro(cacheWrite),
	}
}

func verifiedPrice(input, output, cacheRead, cacheWrite, cacheWrite5m, cacheWrite1h float64, source string) ModelPrice {
	price := priceFromUSD(input, output, cacheRead, cacheWrite)
	price.CacheWrite5mPerMtokMicro = usdToMicro(cacheWrite5m)
	price.CacheWrite1hPerMtokMicro = usdToMicro(cacheWrite1h)
	price.SourceURL = source
	price.VerifiedAt = "2026-09-12"
	return price
}

const (
	openAIPriceSource    = "https://developers.openai.com/api/docs/pricing"
	anthropicPriceSource = "https://platform.claude.com/docs/en/about-claude/pricing"
)

func builtInPricingCatalog() map[string]ModelPrice {
	return map[string]ModelPrice{
		"gpt-5.6-luna":     verifiedPrice(0.20, 1.20, 0.02, 0.25, 0.25, 0.25, openAIPriceSource),
		"gpt-5.6-terra":    verifiedPrice(2.00, 12.00, 0.20, 2.50, 2.50, 2.50, openAIPriceSource),
		"gpt-5.6-sol":      verifiedPrice(4.00, 20.00, 0.40, 5.00, 5.00, 5.00, openAIPriceSource),
		"gpt-6-astra":      verifiedPrice(10.00, 50.00, 1.00, 12.50, 12.50, 12.50, openAIPriceSource),
		"claude-opus-5":    verifiedPrice(5.00, 25.00, 0.50, 6.25, 6.25, 10.00, anthropicPriceSource),
		"claude-sonnet-5":  verifiedPrice(2.00, 10.00, 0.20, 2.50, 2.50, 4.00, anthropicPriceSource),
		"claude-haiku-4-5": verifiedPrice(1.00, 5.00, 0.10, 1.25, 1.25, 2.00, anthropicPriceSource),

		// Retained supported catalog entries used by existing non-transcript
		// providers. They share this single catalog rather than a fetcher copy.
		"claude-opus-4-7":        verifiedPrice(5.0, 25.0, 0.50, 6.25, 6.25, 10.0, anthropicPriceSource),
		"claude-opus-4-6":        verifiedPrice(5.0, 25.0, 0.50, 6.25, 6.25, 10.0, anthropicPriceSource),
		"claude-sonnet-4-6":      verifiedPrice(3.0, 15.0, 0.30, 3.75, 3.75, 6.0, anthropicPriceSource),
		"gemini-2.5-pro":         priceFromUSD(1.25, 10.0, 0, 0),
		"gemini-2.5-flash":       priceFromUSD(0.15, 0.60, 0, 0),
		"gpt-4o":                 priceFromUSD(2.50, 10.0, 0, 0),
		"gpt-4.1":                priceFromUSD(2.0, 8.0, 0, 0),
		"o3":                     priceFromUSD(2.0, 8.0, 0, 0),
		"o4-mini":                priceFromUSD(1.10, 4.40, 0, 0),
		"MiniMax-M3":             priceFromUSD(0.60, 2.40, 0.12, 0),
		"MiniMax-M2.7":           priceFromUSD(0.30, 1.20, 0.06, 0.375),
		"MiniMax-M2.7-highspeed": priceFromUSD(0.35, 1.40, 0, 0),
		"MiniMax-M2.5":           priceFromUSD(0.50, 2.00, 0, 0),
		"MiniMax-M2.5-highspeed": priceFromUSD(0.15, 0.60, 0, 0),
	}
}

// NewPricer creates a Pricer with hardcoded defaults and optional overrides.
func NewPricer(cfg PricerConfig) *Pricer {
	p := &Pricer{
		defaults:  builtInPricingCatalog(),
		cached:    make(map[string]ModelPrice),
		overrides: make(map[string]ModelPrice),
		cachePath: cfg.CachePath,
	}

	for model, ov := range cfg.Overrides {
		p.overrides[model] = ModelPrice{
			InputPerMtokMicro:        usdToMicro(ov.InputPerMtok),
			OutputPerMtokMicro:       usdToMicro(ov.OutputPerMtok),
			CacheReadPerMtokMicro:    usdToMicro(ov.CacheReadPerMtok),
			CacheWritePerMtokMicro:   usdToMicro(ov.CacheWritePerMtok),
			CacheWrite5mPerMtokMicro: usdToMicro(ov.CacheWrite5mPerMtok),
			CacheWrite1hPerMtokMicro: usdToMicro(ov.CacheWrite1hPerMtok),
		}
		if ov.CacheWrite5mPerMtok == 0 {
			price := p.overrides[model]
			price.CacheWrite5mPerMtokMicro = price.CacheWritePerMtokMicro
			p.overrides[model] = price
		}
		if ov.CacheWrite1hPerMtok == 0 {
			price := p.overrides[model]
			price.CacheWrite1hPerMtokMicro = price.CacheWritePerMtokMicro
			p.overrides[model] = price
		}
	}

	return p
}

// LoadCache reads pricing.json from CachePath.
func (p *Pricer) LoadCache() error {
	if p.cachePath == "" {
		return nil
	}
	path := filepath.Join(p.cachePath, "pricing.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cf pricingCacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return err
	}
	p.cacheTime = cf.RefreshedAt
	if p.cacheTime.IsZero() {
		p.cacheTime = cf.FetchedAt
	}
	p.cacheProvenance = cf.Provenance
	p.cached = make(map[string]ModelPrice, len(cf.Models))
	for model, m := range cf.Models {
		p.cached[model] = ModelPrice{
			InputPerMtokMicro:        usdToMicro(m.InputPerMtok),
			OutputPerMtokMicro:       usdToMicro(m.OutputPerMtok),
			CacheReadPerMtokMicro:    usdToMicro(m.CacheReadPerMtok),
			CacheWritePerMtokMicro:   usdToMicro(m.CacheWritePerMtok),
			CacheWrite5mPerMtokMicro: usdToMicro(m.CacheWrite5mPerMtok),
			CacheWrite1hPerMtokMicro: usdToMicro(m.CacheWrite1hPerMtok),
			SourceURL:                m.SourceURL,
			VerifiedAt:               m.VerifiedAt,
		}
		price := p.cached[model]
		if m.CacheWrite5mPerMtok == 0 {
			price.CacheWrite5mPerMtokMicro = price.CacheWritePerMtokMicro
		}
		if m.CacheWrite1hPerMtok == 0 {
			price.CacheWrite1hPerMtokMicro = price.CacheWritePerMtokMicro
		}
		p.cached[model] = price
	}
	return nil
}

// SaveCache writes pricing.json to CachePath.
func (p *Pricer) SaveCache(models map[string]pricingCacheModel) error {
	return p.saveCache(models, "configured")
}

func (p *Pricer) saveCache(models map[string]pricingCacheModel, provenance string) error {
	if p.cachePath == "" {
		return nil
	}
	cf := pricingCacheFile{
		RefreshedAt: time.Now().UTC(),
		Provenance:  provenance,
		Models:      models,
	}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.cachePath, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.cachePath, "pricing.json"), data, 0o644)
}

func (p *Pricer) saveBundledCache() error {
	models := make(map[string]pricingCacheModel, len(p.defaults))
	for model, price := range p.defaults {
		models[model] = pricingCacheModel{
			InputPerMtok:        float64(price.InputPerMtokMicro) / 1_000_000,
			OutputPerMtok:       float64(price.OutputPerMtokMicro) / 1_000_000,
			CacheReadPerMtok:    float64(price.CacheReadPerMtokMicro) / 1_000_000,
			CacheWritePerMtok:   float64(price.CacheWritePerMtokMicro) / 1_000_000,
			CacheWrite5mPerMtok: float64(price.CacheWrite5mPerMtokMicro) / 1_000_000,
			CacheWrite1hPerMtok: float64(price.CacheWrite1hPerMtokMicro) / 1_000_000,
			SourceURL:           price.SourceURL,
			VerifiedAt:          price.VerifiedAt,
		}
	}
	return p.saveCache(models, "bundled")
}

func (p *Pricer) CacheProvenance() string { return p.cacheProvenance }

// CacheAge returns the age of the cache, or -1 if no cache is loaded.
func (p *Pricer) CacheAge() time.Duration {
	if p.cacheTime.IsZero() {
		return -1
	}
	return time.Since(p.cacheTime)
}

// GetPrice returns the price for a model with fallback: override > cache > hardcoded.
func (p *Pricer) GetPrice(model string) (ModelPrice, bool) {
	mp, _, ok := p.lookupPrice(normalizeModel(model))
	return mp, ok
}

func (p *Pricer) lookupPrice(normalized string) (ModelPrice, string, bool) {
	if mp, ok := p.overrides[normalized]; ok {
		return mp, "override", true
	}
	if mp, ok := p.cached[normalized]; ok {
		source := mp.SourceURL
		if source == "" {
			source = "cache"
		}
		return mp, source, true
	}
	if mp, ok := p.defaults[normalized]; ok {
		source := mp.SourceURL
		if source == "" {
			source = "builtin"
		}
		return mp, source, true
	}
	return ModelPrice{}, "", false
}

// ComputeCost calculates cost in microdollars for token usage on a model.
func (p *Pricer) ComputeCost(model string, input, output, cacheRead, cacheWrite int64) int64 {
	quote := p.Quote(model, TokenUsage{
		InputTokens: input, OutputTokens: output,
		CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
	})
	return quote.CostMicrodollars
}

func priceIsZero(price ModelPrice) bool {
	return price.InputPerMtokMicro == 0 && price.OutputPerMtokMicro == 0 &&
		price.CacheReadPerMtokMicro == 0 && price.CacheWritePerMtokMicro == 0 &&
		price.CacheWrite5mPerMtokMicro == 0 && price.CacheWrite1hPerMtokMicro == 0
}

func tokenCost(tokens, rate int64) int64 {
	return int64(math.Round(float64(tokens) * float64(rate) / 1_000_000))
}

var dateSuffixRe = regexp.MustCompile(`-\d{8}$`)

// normalizeModel strips date suffixes from model names.
func normalizeModel(model string) string {
	return dateSuffixRe.ReplaceAllString(model, "")
}
