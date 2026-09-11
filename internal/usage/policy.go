package usage

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Ladder maps the four request tiers to concrete model names for one provider.
// An empty entry means the tier is unavailable on that provider, and the
// recommender omits the model flag (or falls back a tier) rather than guessing.
type Ladder struct {
	Cheap    string
	Mid      string
	Strong   string
	Frontier string
}

// Policy is the resolved usage policy the recommender consumes. It is produced
// either from DefaultPolicy or from PolicyFromConfig, which merges the optional
// [usage.policy] block over those defaults key by key.
type Policy struct {
	// ExhaustedBelow and ConstrainedBelow are remaining-percent thresholds: a
	// provider is exhausted below the first and constrained below the second.
	ExhaustedBelow   int
	ConstrainedBelow int

	// Failover is the tool-name order tried after the preferred tool. Entries
	// need not have a usage provider; such a tool resolves to "unknown" state.
	Failover []string

	// Ladder holds the per-provider model ladder.
	Ladder map[Provider]Ladder

	// FrontierWindow names the per-model usage window that gates a provider's
	// frontier tier. An absent or empty value means no gate.
	FrontierWindow map[Provider]string
}

// Policy defaults, from the design's "Policy config" interface.
const (
	defaultExhaustedBelow   = 15
	defaultConstrainedBelow = 35
)

// DefaultPolicy returns the shipped policy, with the failover order derived
// from the user's default_tool: the default tool first, then the other usage
// provider. An empty or unrecognised defaultTool still yields a deterministic
// two-entry order covering both usage providers.
func DefaultPolicy(defaultTool string) Policy {
	return Policy{
		ExhaustedBelow:   defaultExhaustedBelow,
		ConstrainedBelow: defaultConstrainedBelow,
		Failover:         defaultFailover(defaultTool),
		Ladder: map[Provider]Ladder{
			Claude: {Cheap: "haiku", Mid: "sonnet", Strong: "opus", Frontier: "fable"},
			Codex:  {Cheap: "gpt-5.6-luna", Mid: "gpt-5.6-terra", Strong: "gpt-5.6-sol", Frontier: "gpt-6-astra"},
		},
		FrontierWindow: map[Provider]string{Claude: "fable"},
	}
}

func defaultFailover(defaultTool string) []string {
	if strings.TrimSpace(defaultTool) == string(Codex) {
		return []string{string(Codex), string(Claude)}
	}
	return []string{string(Claude), string(Codex)}
}

// PolicyFromConfig merges the optional [usage.policy] block over
// DefaultPolicy(cfg.DefaultTool). Every key is independent: an omitted key
// keeps its default, and an explicitly empty ladder entry marks that tier
// unavailable. A nil cfg yields DefaultPolicy("").
//
// It re-validates the merged thresholds, which the loader cannot do: a config
// setting only exhausted_below is well-formed on its own but may still invert
// against the default constrained_below, and the loader must not hardcode the
// defaults (they live here, and internal/session must not import this package).
func PolicyFromConfig(cfg *session.UserConfig) (Policy, error) {
	if cfg == nil {
		return DefaultPolicy(""), nil
	}
	policy := DefaultPolicy(cfg.DefaultTool)
	settings := cfg.Usage.Policy

	if settings.ExhaustedBelow != nil {
		policy.ExhaustedBelow = *settings.ExhaustedBelow
	}
	if settings.ConstrainedBelow != nil {
		policy.ConstrainedBelow = *settings.ConstrainedBelow
	}
	if err := validateThreshold("exhausted_below", policy.ExhaustedBelow); err != nil {
		return Policy{}, err
	}
	if err := validateThreshold("constrained_below", policy.ConstrainedBelow); err != nil {
		return Policy{}, err
	}
	if policy.ExhaustedBelow > policy.ConstrainedBelow {
		return Policy{}, fmt.Errorf("invalid [usage.policy].exhausted_below %d: must be <= constrained_below %d",
			policy.ExhaustedBelow, policy.ConstrainedBelow)
	}

	if len(settings.Failover) > 0 {
		failover := make([]string, 0, len(settings.Failover))
		for i, entry := range settings.Failover {
			if entry == "" || strings.ContainsFunc(entry, unicode.IsSpace) {
				return Policy{}, fmt.Errorf("invalid [usage.policy].failover[%d] %q: must be a tool name without whitespace", i, entry)
			}
			failover = append(failover, entry)
		}
		policy.Failover = failover
	}

	for name, ladder := range settings.Ladder {
		provider := Provider(name)
		merged := policy.Ladder[provider]
		if ladder.Cheap != nil {
			merged.Cheap = *ladder.Cheap
		}
		if ladder.Mid != nil {
			merged.Mid = *ladder.Mid
		}
		if ladder.Strong != nil {
			merged.Strong = *ladder.Strong
		}
		if ladder.Frontier != nil {
			merged.Frontier = *ladder.Frontier
		}
		policy.Ladder[provider] = merged
	}

	for name, window := range settings.FrontierWindow {
		policy.FrontierWindow[Provider(name)] = window
	}

	return policy, nil
}

func validateThreshold(key string, value int) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("invalid [usage.policy].%s %d: must be between 0 and 100", key, value)
	}
	return nil
}
