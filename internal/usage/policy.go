package usage

import (
	"fmt"
	"maps"
	"slices"
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

	// Failover is the TOOL-name order tried after the preferred tool. Entries
	// are validated by shape only, at both layers, and need not name a usage
	// provider: the design's recommender rule 2 makes a tool in the "unknown"
	// state eligible precisely when it appears here explicitly, so restricting
	// this list to the two providers would delete a behaviour the design
	// requires. A consequence is that a miscased entry such as "Codex" stays
	// inert — that is designed, not a defect.
	//
	// It is never empty, so callers may read Failover[0] unguarded: an explicit
	// `failover = []` in the config is treated as an omitted key and keeps the
	// default order, exactly like leaving the key out.
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
//
// It also rejects what only this package can judge: a ladder or frontier_window
// table key that is not a known usage provider, and a ladder or frontier_window
// VALUE carrying whitespace. A value this catches would otherwise be a silent
// no-op — the override discarded or the window never matched — surfacing as
// wrong behaviour units later instead of as a config error. "Whitespace" here
// is the Unicode White_Space set, as Go's unicode.IsSpace defines it. It does
// NOT cover the zero-width format characters U+200B, U+FEFF and U+2060, so a
// value carrying one of those is still accepted and reaches the launch flag
// verbatim. An empty ladder value stays legal: it is how the config marks a
// tier unavailable on a provider, and an empty frontier_window value means no
// gate.
//
// Failover entries are deliberately NOT checked against the provider set; see
// the Policy.Failover doc comment.
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

	// An explicit `failover = []` is treated as an omitted key, so the defaults
	// apply; see the Policy.Failover doc comment.
	if len(settings.Failover) > 0 {
		failover := make([]string, 0, len(settings.Failover))
		for i, entry := range settings.Failover {
			if entry == "" || strings.ContainsFunc(entry, unicode.IsSpace) {
				return Policy{}, fmt.Errorf("invalid [usage.policy].failover[%d] %q: must be a tool name without whitespace", i, entry)
			}
			failover = append(failover, entry)
		}
		// The copy is load-bearing: LoadUserConfig hands every caller the same
		// process-cached *UserConfig, so aliasing the slice would let one
		// caller's mutation of Policy.Failover write through into every other
		// caller's config.
		policy.Failover = failover
	}

	// Table keys are sorted so an error message is deterministic when more than
	// one key is wrong.
	for _, name := range sortedKeys(settings.Ladder) {
		provider, ok := knownProvider(name)
		if !ok {
			return Policy{}, fmt.Errorf("invalid [usage.policy].ladder.%s: unknown usage provider", name)
		}
		ladder := settings.Ladder[name]
		merged := policy.Ladder[provider]
		for _, rung := range []struct {
			key   string
			value *string
			dst   *string
		}{
			{"cheap", ladder.Cheap, &merged.Cheap},
			{"mid", ladder.Mid, &merged.Mid},
			{"strong", ladder.Strong, &merged.Strong},
			{"frontier", ladder.Frontier, &merged.Frontier},
		} {
			if rung.value == nil {
				continue
			}
			if strings.ContainsFunc(*rung.value, unicode.IsSpace) {
				return Policy{}, fmt.Errorf("invalid [usage.policy].ladder.%s.%s %q: must be a model name without whitespace",
					name, rung.key, *rung.value)
			}
			*rung.dst = *rung.value
		}
		policy.Ladder[provider] = merged
	}

	for _, name := range sortedKeys(settings.FrontierWindow) {
		provider, ok := knownProvider(name)
		if !ok {
			return Policy{}, fmt.Errorf("invalid [usage.policy].frontier_window.%s: unknown usage provider", name)
		}
		window := settings.FrontierWindow[name]
		if strings.ContainsFunc(window, unicode.IsSpace) {
			return Policy{}, fmt.Errorf("invalid [usage.policy].frontier_window.%s %q: must be a window name without whitespace",
				name, window)
		}
		policy.FrontierWindow[provider] = window
	}

	return policy, nil
}

// knownProvider reports whether name is one of the closed set of usage
// providers. It guards the ladder and frontier_window table KEYS only, which
// are keyed by Provider and so cannot hold anything else: a typo'd or miscased
// `[usage.policy.ladder.cluade]` would otherwise discard the user's override,
// leave the defaults in place, and add a dead Provider entry nothing reads.
//
// It deliberately does NOT guard failover entries, which are tool names.
func knownProvider(name string) (Provider, bool) {
	switch p := Provider(name); p {
	case Claude, Codex:
		return p, true
	default:
		return "", false
	}
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

func validateThreshold(key string, value int) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("invalid [usage.policy].%s %d: must be between 0 and 100", key, value)
	}
	return nil
}
