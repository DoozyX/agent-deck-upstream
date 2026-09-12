package costs

import (
	"time"

	"github.com/google/uuid"
)

// Parser parses tool output into cost events.
type Parser interface {
	Name() string
	CanParse(toolType string) bool
	Parse(input string) ([]CostEvent, error)
}

// Collector routes tool output to the appropriate parser and applies pricing.
type Collector struct {
	parsers []Parser
	pricer  *Pricer
}

// NewCollector creates a Collector with all registered parsers.
func NewCollector(pricer *Pricer) *Collector {
	return &Collector{
		parsers: []Parser{
			&ClaudeHookParser{pricer: pricer},
			&GeminiOutputParser{pricer: pricer},
			&OpenAIOutputParser{pricer: pricer},
			&MiniMaxOutputParser{pricer: pricer},
		},
		pricer: pricer,
	}
}

// Collect parses input using the appropriate parser, sets session ID and computes cost.
func (c *Collector) Collect(toolType, sessionID, input string) ([]CostEvent, error) {
	for _, p := range c.parsers {
		if !p.CanParse(toolType) {
			continue
		}
		events, err := p.Parse(input)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		for i := range events {
			events[i].SessionID = sessionID
			events[i].Timestamp = now
			events[i].Provider = p.Name()
			events[i].SourceKind = "terminal"
			switch toolType {
			case "claude":
				events[i].SourceKind = "hook"
			case "codex":
				events[i].Provider = ProviderCodex
				events[i].SourceKind = SourceKindCodexTerminalLimited
			}
			events[i].ReconciliationStatus = ReconciliationLegacyUnreconciled
			if events[i].ID == "" {
				events[i].ID = uuid.New().String()
			}
			if price, ok := c.pricer.GetPrice(events[i].Model); !ok {
				events[i].PricingStatus = PricingUnknown
			} else if price.InputPerMtokMicro == 0 && price.OutputPerMtokMicro == 0 && price.CacheReadPerMtokMicro == 0 && price.CacheWritePerMtokMicro == 0 {
				events[i].PricingStatus = PricingKnownZero
			} else {
				events[i].PricingStatus = PricingKnown
			}
			if events[i].CostMicrodollars == 0 {
				events[i].CostMicrodollars = c.pricer.ComputeCost(
					events[i].Model,
					events[i].InputTokens,
					events[i].OutputTokens,
					events[i].CacheReadTokens,
					events[i].CacheWriteTokens,
				)
			}
		}
		return events, nil
	}
	return nil, nil
}
