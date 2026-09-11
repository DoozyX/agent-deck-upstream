package usage

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

var recFetchedAt = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

// recSnapshot builds a snapshot literal. A negative session5h or weekly means
// that window is absent from the snapshot; models maps a per-model window name
// to its remaining percent. Tests never call Parse and never spawn openusage.
func recSnapshot(provider Provider, account string, session5h, weekly int, models map[string]int) Snapshot {
	s := Snapshot{Available: true, Provider: provider, Account: account, FetchedAt: recFetchedAt}
	if session5h >= 0 {
		s.Windows.Session5H = &Window{RemainingPercent: session5h}
	}
	if weekly >= 0 {
		s.Windows.Weekly = &Window{RemainingPercent: weekly}
	}
	if len(models) > 0 {
		s.Windows.Models = make(map[string]*Window, len(models))
		for name, percent := range models {
			s.Windows.Models[name] = &Window{RemainingPercent: percent}
		}
	}
	return s
}

func autoTools() session.OrchestrateToolPolicy {
	return session.OrchestrateToolPolicy{
		Strategy:       "auto",
		FallbackTool:   "codex",
		AvailableTools: []string{"claude", "codex", "gemini"},
	}
}

// assertDecision checks the full Decision against a row's expectations.
func assertDecision(t *testing.T, got Decision, want Decision, wantReasons []string) {
	t.Helper()
	if got.Tool != want.Tool {
		t.Errorf("Tool = %q, want %q", got.Tool, want.Tool)
	}
	if got.Provider != want.Provider {
		t.Errorf("Provider = %q, want %q", got.Provider, want.Provider)
	}
	if got.Model != want.Model {
		t.Errorf("Model = %q, want %q", got.Model, want.Model)
	}
	if got.TierRequested != want.TierRequested {
		t.Errorf("TierRequested = %q, want %q", got.TierRequested, want.TierRequested)
	}
	if got.TierApplied != want.TierApplied {
		t.Errorf("TierApplied = %q, want %q", got.TierApplied, want.TierApplied)
	}
	if got.Account != want.Account {
		t.Errorf("Account = %q, want %q", got.Account, want.Account)
	}
	if got.State != want.State {
		t.Errorf("State = %q, want %q", got.State, want.State)
	}
	if !got.FetchedAt.Equal(want.FetchedAt) {
		t.Errorf("FetchedAt = %v, want %v", got.FetchedAt, want.FetchedAt)
	}
	if len(got.Alternatives) != len(want.Alternatives) {
		t.Errorf("Alternatives = %+v, want %+v", got.Alternatives, want.Alternatives)
	} else {
		for i := range want.Alternatives {
			if got.Alternatives[i] != want.Alternatives[i] {
				t.Errorf("Alternatives[%d] = %+v, want %+v", i, got.Alternatives[i], want.Alternatives[i])
			}
		}
	}
	assertReason(t, got.Reason, wantReasons)
}

// assertReason pins that Reason is a single line naming the deciding rule.
func assertReason(t *testing.T, reason string, wantReasons []string) {
	t.Helper()
	if strings.ContainsAny(reason, "\n\r") {
		t.Errorf("Reason must be a single line, got %q", reason)
	}
	for _, want := range wantReasons {
		if !strings.Contains(reason, want) {
			t.Errorf("Reason = %q, want it to contain %q", reason, want)
		}
	}
}

// The main table: provider states {healthy, constrained, exhausted, unknown} x
// tiers {cheap, mid, strong, frontier} under strategy=auto, asserting the full
// Decision per row.
func TestRecommend_StateAndTierTable(t *testing.T) {
	policy := DefaultPolicy("claude")
	codexHealthy := recSnapshot(Codex, "work", 90, 90, nil)

	claudeFor := func(state string) []Snapshot {
		switch state {
		case StateHealthy:
			return []Snapshot{recSnapshot(Claude, "personal", 90, 90, map[string]int{"fable": 80}), codexHealthy}
		case StateConstrained:
			return []Snapshot{recSnapshot(Claude, "personal", 20, 90, map[string]int{"fable": 80}), codexHealthy}
		case StateExhausted:
			return []Snapshot{recSnapshot(Claude, "personal", 5, 90, map[string]int{"fable": 80}), codexHealthy}
		default: // unknown: no claude snapshot at all
			return []Snapshot{codexHealthy}
		}
	}

	claudeAlt := map[string]Alternative{
		StateHealthy:     {Tool: "claude", State: StateHealthy, RemainingPercent: 90},
		StateConstrained: {Tool: "claude", State: StateConstrained, RemainingPercent: 20},
		StateExhausted:   {Tool: "claude", State: StateExhausted, RemainingPercent: 5},
		StateUnknown:     {Tool: "claude", State: StateUnknown, RemainingPercent: -1},
	}
	codexAlt := Alternative{Tool: "codex", State: StateHealthy, RemainingPercent: 90}

	claudePick := func(tier Tier, model, state string) Decision {
		return Decision{
			Tool: "claude", Provider: "claude", Model: model,
			TierRequested: tier, TierApplied: tier,
			Account: "personal", State: state, FetchedAt: recFetchedAt,
			Alternatives: []Alternative{codexAlt},
		}
	}
	codexPick := func(tier Tier, model, claudeState string) Decision {
		return Decision{
			Tool: "codex", Provider: "codex", Model: model,
			TierRequested: tier, TierApplied: tier,
			Account: "work", State: StateHealthy, FetchedAt: recFetchedAt,
			Alternatives: []Alternative{claudeAlt[claudeState]},
		}
	}

	tests := []struct {
		name        string
		claudeState string
		tier        Tier
		want        Decision
		wantReasons []string
	}{
		{"healthy/cheap", StateHealthy, TierCheap, claudePick(TierCheap, "haiku", StateHealthy), []string{reasonFirstHealthy}},
		{"healthy/mid", StateHealthy, TierMid, claudePick(TierMid, "sonnet", StateHealthy), []string{reasonFirstHealthy}},
		{"healthy/strong", StateHealthy, TierStrong, claudePick(TierStrong, "opus", StateHealthy), []string{reasonFirstHealthy}},
		{"healthy/frontier", StateHealthy, TierFrontier, claudePick(TierFrontier, "fable", StateHealthy), []string{reasonFirstHealthy}},

		{"constrained/cheap", StateConstrained, TierCheap, codexPick(TierCheap, "gpt-5.6-luna", StateConstrained), []string{reasonFirstHealthy}},
		{"constrained/mid", StateConstrained, TierMid, codexPick(TierMid, "gpt-5.6-terra", StateConstrained), []string{reasonFirstHealthy}},
		// The carve-out: a constrained PREFERRED tool beats a healthy failover
		// candidate at strong and frontier. Under a literal reading of the
		// design's rule 3 these two rows would pick codex.
		{"constrained_preferred_beats_healthy_failover_at_strong", StateConstrained, TierStrong,
			claudePick(TierStrong, "opus", StateConstrained), []string{reasonPreferredConstrained}},
		{"constrained_preferred_beats_healthy_failover_at_frontier", StateConstrained, TierFrontier,
			claudePick(TierFrontier, "fable", StateConstrained), []string{reasonPreferredConstrained}},

		{"exhausted/cheap", StateExhausted, TierCheap, codexPick(TierCheap, "gpt-5.6-luna", StateExhausted), []string{reasonFirstHealthy}},
		{"exhausted/mid", StateExhausted, TierMid, codexPick(TierMid, "gpt-5.6-terra", StateExhausted), []string{reasonFirstHealthy}},
		{"exhausted/strong", StateExhausted, TierStrong, codexPick(TierStrong, "gpt-5.6-sol", StateExhausted), []string{reasonFirstHealthy}},
		{"exhausted/frontier", StateExhausted, TierFrontier, codexPick(TierFrontier, "gpt-6-astra", StateExhausted), []string{reasonFirstHealthy}},

		{"unknown/cheap", StateUnknown, TierCheap, codexPick(TierCheap, "gpt-5.6-luna", StateUnknown), []string{reasonFirstHealthy}},
		{"unknown/mid", StateUnknown, TierMid, codexPick(TierMid, "gpt-5.6-terra", StateUnknown), []string{reasonFirstHealthy}},
		{"unknown/strong", StateUnknown, TierStrong, codexPick(TierStrong, "gpt-5.6-sol", StateUnknown), []string{reasonFirstHealthy}},
		{"unknown/frontier", StateUnknown, TierFrontier, codexPick(TierFrontier, "gpt-6-astra", StateUnknown), []string{reasonFirstHealthy}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Recommend(
				Request{Role: "planner", Tier: tc.tier, Prefer: "claude"},
				claudeFor(tc.claudeState), autoTools(), policy,
			)
			assertDecision(t, got, tc.want, tc.wantReasons)
			if strings.Contains(got.Reason, reasonPolicyLimited) {
				t.Errorf("Reason = %q, strategy=auto must not claim the policy limited failover", got.Reason)
			}
		})
	}
}

// Strategies {auto, default, empty} x states: the candidate list collapses to a
// single tool for default/empty and the reason says the policy limited failover.
func TestRecommend_StrategyCollapsesCandidates(t *testing.T) {
	policy := DefaultPolicy("claude")
	codexHealthy := recSnapshot(Codex, "work", 90, 90, nil)

	tests := []struct {
		name        string
		strategy    string
		prefer      string
		snapshots   []Snapshot
		want        Decision
		wantReasons []string
	}{
		{
			name: "auto/claude-exhausted-fails-over", strategy: "auto", prefer: "claude",
			snapshots: []Snapshot{recSnapshot(Claude, "personal", 5, 90, nil), codexHealthy},
			want: Decision{
				Tool: "codex", Provider: "codex", Model: "gpt-5.6-terra",
				TierRequested: TierMid, TierApplied: TierMid,
				Account: "work", State: StateHealthy, FetchedAt: recFetchedAt,
				Alternatives: []Alternative{{Tool: "claude", State: StateExhausted, RemainingPercent: 5}},
			},
			wantReasons: []string{reasonFirstHealthy},
		},
		{
			name: "default/claude-exhausted-cannot-fail-over", strategy: "default", prefer: "claude",
			snapshots: []Snapshot{recSnapshot(Claude, "personal", 5, 90, nil), codexHealthy},
			want: Decision{
				Tool: "claude", Provider: "claude", Model: "sonnet",
				TierRequested: TierMid, TierApplied: TierMid,
				Account: "personal", State: StateExhausted, FetchedAt: recFetchedAt,
				Alternatives: []Alternative{},
			},
			wantReasons: []string{reasonPolicyLimited, reasonExhaustedFallback},
		},
		{
			name: "empty/claude-healthy", strategy: "", prefer: "claude",
			snapshots: []Snapshot{recSnapshot(Claude, "personal", 90, 90, nil), codexHealthy},
			want: Decision{
				Tool: "claude", Provider: "claude", Model: "sonnet",
				TierRequested: TierMid, TierApplied: TierMid,
				Account: "personal", State: StateHealthy, FetchedAt: recFetchedAt,
				Alternatives: []Alternative{},
			},
			wantReasons: []string{reasonPolicyLimited, reasonFirstHealthy},
		},
		{
			name: "empty/no-prefer-uses-fallback-tool", strategy: "", prefer: "",
			snapshots: []Snapshot{recSnapshot(Claude, "personal", 90, 90, nil), codexHealthy},
			want: Decision{
				Tool: "codex", Provider: "codex", Model: "gpt-5.6-terra",
				TierRequested: TierMid, TierApplied: TierMid,
				Account: "work", State: StateHealthy, FetchedAt: recFetchedAt,
				Alternatives: []Alternative{},
			},
			wantReasons: []string{reasonPolicyLimited, reasonFirstHealthy},
		},
		{
			name: "default/unknown-tool-is-eligible-because-it-is-in-failover", strategy: "default", prefer: "claude",
			snapshots: []Snapshot{codexHealthy},
			want: Decision{
				Tool: "claude", Provider: "claude", Model: "sonnet",
				TierRequested: TierMid, TierApplied: TierMid,
				State: StateUnknown, Alternatives: []Alternative{},
			},
			wantReasons: []string{reasonPolicyLimited, reasonUnknownFailover},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tools := autoTools()
			tools.Strategy = tc.strategy
			got := Recommend(
				Request{Role: "planner", Tier: TierMid, Prefer: tc.prefer},
				tc.snapshots, tools, policy,
			)
			assertDecision(t, got, tc.want, tc.wantReasons)
		})
	}
}

// The collapsed strategies have exactly two sources for their single
// candidate, Request.Prefer then OrchestrateToolPolicy.FallbackTool. There is
// no third: with neither set they name nothing rather than reaching for
// Policy.Failover[0], which is the list this strategy exists to drop.
func TestRecommend_CollapsedStrategyHasNoThirdFallback(t *testing.T) {
	policy := DefaultPolicy("claude") // Failover[0] is "claude".

	for _, strategy := range []string{"default", ""} {
		t.Run(fmt.Sprintf("strategy=%q", strategy), func(t *testing.T) {
			tools := session.OrchestrateToolPolicy{
				Strategy: strategy, AvailableTools: []string{"claude", "codex"},
			}
			req := Request{Role: "planner", Tier: TierMid}
			got := Recommend(req, []Snapshot{recSnapshot(Claude, "personal", 90, 90, nil)}, tools, policy)

			if got.Tool != "" {
				t.Errorf("Tool = %q, want empty: neither Prefer nor FallbackTool names a tool", got.Tool)
			}
			if got.State != StateUnknown {
				t.Errorf("State = %q, want unknown", got.State)
			}
			assertReason(t, got.Reason, []string{reasonNoCandidates})
			if strings.Contains(got.Reason, reasonPolicyLimited) {
				t.Errorf("Reason = %q, want no policy-limited clause when there are no candidates at all", got.Reason)
			}

			candidates, prefer, limited := candidateTools(req, tools, policy)
			if len(candidates) != 0 || prefer != "" || limited {
				t.Errorf("candidateTools = %v/%q/%v, want []/\"\"/false", candidates, prefer, limited)
			}
		})
	}
}

// The rule 3 carve-out compares against the prefer candidateTools RESOLVED,
// not preferredTool's answer: on the collapsed path an empty Request.Prefer
// resolves to FallbackTool while Policy.Failover[0] names a different tool, so
// re-deriving one here would compare against a non-candidate and fall through
// to the weaker "first constrained candidate" rule.
func TestRecommend_CollapsedStrategyCarveOutUsesTheResolvedPrefer(t *testing.T) {
	policy := DefaultPolicy("claude") // Failover[0] is "claude", not "codex".
	tools := session.OrchestrateToolPolicy{
		Strategy: "default", FallbackTool: "codex", AvailableTools: []string{"claude", "codex"},
	}
	req := Request{Role: "planner", Tier: TierStrong}

	if _, prefer, _ := candidateTools(req, tools, policy); prefer != "codex" {
		t.Fatalf("resolved prefer = %q, want codex (the fallback tool)", prefer)
	}

	got := Recommend(req, []Snapshot{recSnapshot(Codex, "work", 20, 90, nil)}, tools, policy)
	if got.Tool != "codex" || got.State != StateConstrained {
		t.Fatalf("Tool/State = %q/%q, want codex/constrained", got.Tool, got.State)
	}
	assertReason(t, got.Reason, []string{reasonPreferredConstrained})
	if strings.Contains(got.Reason, reasonFirstConstrained) {
		t.Errorf("Reason = %q, want the preferred-tool carve-out, not the weaker first-constrained rule", got.Reason)
	}
}

// The two silent carve-outs out of rule 5, exactly as resolveModel documents
// them: neither adds a reason clause.
func TestRecommend_ModelResolutionCarveOuts(t *testing.T) {
	t.Run("a-tool-with-no-usage-provider-keeps-the-requested-tier", func(t *testing.T) {
		policy := DefaultPolicy("claude")
		policy.Failover = []string{"gemini"}
		got := Recommend(
			Request{Role: "planner", Tier: TierFrontier, Prefer: "gemini"},
			nil, autoTools(), policy,
		)
		if got.Tool != "gemini" || got.Provider != "" {
			t.Fatalf("Tool/Provider = %q/%q, want gemini/\"\"", got.Tool, got.Provider)
		}
		if got.Model != "" {
			t.Errorf("Model = %q, want empty: there is no ladder for a tool with no usage provider", got.Model)
		}
		if got.TierApplied != TierFrontier {
			t.Errorf("TierApplied = %q, want frontier: there is nothing to downgrade to", got.TierApplied)
		}
		if strings.Contains(got.Reason, "downgraded") {
			t.Errorf("Reason = %q, want no downgrade clause", got.Reason)
		}
	})

	t.Run("an-unrecognised-tier-echoes-into-tier-applied-with-no-model", func(t *testing.T) {
		got := Recommend(
			Request{Role: "planner", Tier: Tier("enormous"), Prefer: "claude"},
			[]Snapshot{recSnapshot(Claude, "personal", 90, 90, nil)},
			autoTools(), DefaultPolicy("claude"),
		)
		if got.Tool != "claude" {
			t.Fatalf("Tool = %q, want claude", got.Tool)
		}
		if got.Model != "" {
			t.Errorf("Model = %q, want empty: an unrecognised tier has no ladder rung", got.Model)
		}
		if got.TierApplied != Tier("enormous") {
			t.Errorf("TierApplied = %q, want the requested tier echoed back", got.TierApplied)
		}
	})
}

// Thresholds are strict: remaining == ExhaustedBelow is constrained, remaining
// == ConstrainedBelow is healthy. The boundaries 0, 100 and equal thresholds
// are pinned here.
func TestRecommend_StateThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name                        string
		exhaustedBelow, constrained int
		remaining                   int
		want                        string
	}{
		{"default/just-below-exhausted", 15, 35, 14, StateExhausted},
		{"default/exactly-exhausted-below-is-constrained", 15, 35, 15, StateConstrained},
		{"default/just-below-constrained", 15, 35, 34, StateConstrained},
		{"default/exactly-constrained-below-is-healthy", 15, 35, 35, StateHealthy},
		{"default/zero-remaining-is-exhausted", 15, 35, 0, StateExhausted},
		{"default/full-remaining-is-healthy", 15, 35, 100, StateHealthy},
		{"zero-thresholds/zero-remaining-is-healthy", 0, 0, 0, StateHealthy},
		{"zero-thresholds/full-remaining-is-healthy", 0, 0, 100, StateHealthy},
		{"hundred-thresholds/ninety-nine-is-exhausted", 100, 100, 99, StateExhausted},
		{"hundred-thresholds/full-remaining-is-healthy", 100, 100, 100, StateHealthy},
		{"equal-thresholds/below-is-exhausted", 20, 20, 19, StateExhausted},
		{"equal-thresholds/at-is-healthy", 20, 20, 20, StateHealthy},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := DefaultPolicy("claude")
			policy.ExhaustedBelow = tc.exhaustedBelow
			policy.ConstrainedBelow = tc.constrained
			tools := autoTools()
			tools.AvailableTools = []string{"claude"}
			got := Recommend(
				Request{Role: "planner", Tier: TierMid, Prefer: "claude"},
				[]Snapshot{recSnapshot(Claude, "personal", tc.remaining, 100, nil)},
				tools, policy,
			)
			if got.State != tc.want {
				t.Errorf("State = %q, want %q (remaining %d, thresholds %d/%d)",
					got.State, tc.want, tc.remaining, tc.exhaustedBelow, tc.constrained)
			}
		})
	}
}

// The state is the minimum remaining over the windows PRESENT; a snapshot with
// neither dedicated window, or one that is unavailable, is unknown.
func TestRecommend_StateUsesMinimumOfPresentWindows(t *testing.T) {
	policy := DefaultPolicy("claude")
	tools := autoTools()
	tools.AvailableTools = []string{"claude"}

	tests := []struct {
		name          string
		snapshot      Snapshot
		wantState     string
		wantRemaining int
	}{
		{"weekly-is-lower", recSnapshot(Claude, "a", 90, 10, nil), StateExhausted, 10},
		{"session-is-lower", recSnapshot(Claude, "a", 10, 90, nil), StateExhausted, 10},
		{"only-weekly-present", recSnapshot(Claude, "a", -1, 20, nil), StateConstrained, 20},
		{"only-session-present", recSnapshot(Claude, "a", 20, -1, nil), StateConstrained, 20},
		{"neither-window-present", recSnapshot(Claude, "a", -1, -1, map[string]int{"fable": 90}), StateUnknown, -1},
		{"unavailable-snapshot", Snapshot{Provider: Claude, Account: "a", Available: false}, StateUnknown, -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Recommend(
				Request{Role: "planner", Tier: TierMid, Prefer: "claude"},
				[]Snapshot{tc.snapshot}, tools, policy,
			)
			if got.State != tc.wantState {
				t.Errorf("State = %q, want %q", got.State, tc.wantState)
			}
			alts := Recommend(
				Request{Role: "planner", Tier: TierMid, Prefer: "codex"},
				[]Snapshot{tc.snapshot}, autoTools(), policy,
			).Alternatives
			for _, alt := range alts {
				if alt.Tool == "claude" && alt.RemainingPercent != tc.wantRemaining {
					t.Errorf("alternative remaining = %d, want %d", alt.RemainingPercent, tc.wantRemaining)
				}
			}
		})
	}
}

func TestRecommend_FrontierGate(t *testing.T) {
	basePolicy := func() Policy {
		p := DefaultPolicy("claude")
		return p
	}
	tools := autoTools()
	tools.AvailableTools = []string{"claude"}

	tests := []struct {
		name        string
		mutate      func(*Policy)
		models      map[string]int
		wantModel   string
		wantTier    Tier
		wantReasons []string
	}{
		{
			name:      "gate-window-below-constrained-downgrades",
			models:    map[string]int{"fable": 10},
			wantModel: "opus", wantTier: TierStrong,
			wantReasons: []string{reasonFrontierGated},
		},
		{
			name:      "gate-window-at-constrained-threshold-does-not-downgrade",
			models:    map[string]int{"fable": 35},
			wantModel: "fable", wantTier: TierFrontier,
		},
		{
			name:      "gate-window-just-below-threshold-downgrades",
			models:    map[string]int{"fable": 34},
			wantModel: "opus", wantTier: TierStrong,
			wantReasons: []string{reasonFrontierGated},
		},
		{
			name:      "gate-window-at-zero-downgrades",
			models:    map[string]int{"fable": 0},
			wantModel: "opus", wantTier: TierStrong,
			wantReasons: []string{reasonFrontierGated},
		},
		{
			name:      "configured-gate-window-absent-from-models-does-not-gate",
			models:    map[string]int{"spark": 1},
			wantModel: "fable", wantTier: TierFrontier,
			wantReasons: []string{reasonFrontierGateAbsent},
		},
		{
			name:      "no-model-windows-at-all-does-not-gate",
			models:    nil,
			wantModel: "fable", wantTier: TierFrontier,
			wantReasons: []string{reasonFrontierGateAbsent},
		},
		{
			name:   "empty-ladder-entry-downgrades",
			mutate: func(p *Policy) { p.Ladder[Claude] = Ladder{Cheap: "haiku", Mid: "sonnet", Strong: "opus"} },
			models: map[string]int{"fable": 90},
			// The ladder cause is reported, not the (unfired) gate.
			wantModel: "opus", wantTier: TierStrong,
			wantReasons: []string{reasonFrontierNoModel},
		},
		{
			name: "empty-ladder-entry-and-empty-strong-entry-yields-no-model",
			mutate: func(p *Policy) {
				p.Ladder[Claude] = Ladder{Cheap: "haiku", Mid: "sonnet"}
			},
			models:    map[string]int{"fable": 90},
			wantModel: "", wantTier: TierStrong,
			wantReasons: []string{reasonFrontierNoModel},
		},
		{
			name:      "no-gate-configured-keeps-frontier",
			mutate:    func(p *Policy) { delete(p.FrontierWindow, Claude) },
			models:    map[string]int{"fable": 1},
			wantModel: "fable", wantTier: TierFrontier,
		},
		{
			name:      "empty-gate-name-keeps-frontier",
			mutate:    func(p *Policy) { p.FrontierWindow[Claude] = "" },
			models:    map[string]int{"fable": 1},
			wantModel: "fable", wantTier: TierFrontier,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := basePolicy()
			if tc.mutate != nil {
				tc.mutate(&policy)
			}
			got := Recommend(
				Request{Role: "planner", Tier: TierFrontier, Prefer: "claude"},
				[]Snapshot{recSnapshot(Claude, "personal", 90, 90, tc.models)},
				tools, policy,
			)
			if got.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", got.Model, tc.wantModel)
			}
			if got.TierApplied != tc.wantTier {
				t.Errorf("TierApplied = %q, want %q", got.TierApplied, tc.wantTier)
			}
			if got.TierRequested != TierFrontier {
				t.Errorf("TierRequested = %q, want %q", got.TierRequested, TierFrontier)
			}
			assertReason(t, got.Reason, tc.wantReasons)
			if tc.wantTier == TierFrontier && strings.Contains(got.Reason, "downgraded") {
				t.Errorf("Reason = %q, want no downgrade clause", got.Reason)
			}
		})
	}
}

// Only a frontier request consults the gate; a strong request with an exhausted
// gate window keeps its strong model and says nothing about the gate.
func TestRecommend_FrontierGateOnlyAppliesToFrontier(t *testing.T) {
	got := Recommend(
		Request{Role: "planner", Tier: TierStrong, Prefer: "claude"},
		[]Snapshot{recSnapshot(Claude, "personal", 90, 90, map[string]int{"fable": 0})},
		autoTools(), DefaultPolicy("claude"),
	)
	if got.Model != "opus" || got.TierApplied != TierStrong {
		t.Fatalf("Model/TierApplied = %q/%q, want opus/strong", got.Model, got.TierApplied)
	}
	if strings.Contains(got.Reason, "frontier") {
		t.Errorf("Reason = %q, want no frontier clause for a strong request", got.Reason)
	}
}

func TestRecommend_TierFloor(t *testing.T) {
	policy := DefaultPolicy("claude")
	snapshots := []Snapshot{recSnapshot(Claude, "personal", 90, 90, nil)}
	tools := autoTools()
	tools.AvailableTools = []string{"claude"}

	tests := []struct {
		name        string
		role        string
		tier        Tier
		wantTier    Tier
		wantModel   string
		wantFloor   bool
		wantReasons []string
	}{
		{"implementer-cheap-is-raised", "implementer", TierCheap, TierMid, "sonnet", true, []string{reasonTierFloor}},
		{"reviewer-cheap-is-raised", "reviewer", TierCheap, TierMid, "sonnet", true, []string{reasonTierFloor}},
		{"planner-cheap-is-not-raised", "planner", TierCheap, TierCheap, "haiku", false, nil},
		{"empty-role-cheap-is-not-raised", "", TierCheap, TierCheap, "haiku", false, nil},
		{"implementer-mid-is-unchanged", "implementer", TierMid, TierMid, "sonnet", false, nil},
		{"implementer-strong-is-unchanged", "implementer", TierStrong, TierStrong, "opus", false, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Recommend(Request{Role: tc.role, Tier: tc.tier, Prefer: "claude"}, snapshots, tools, policy)
			if got.TierRequested != tc.tier {
				t.Errorf("TierRequested = %q, want %q", got.TierRequested, tc.tier)
			}
			if got.TierApplied != tc.wantTier {
				t.Errorf("TierApplied = %q, want %q", got.TierApplied, tc.wantTier)
			}
			if got.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", got.Model, tc.wantModel)
			}
			assertReason(t, got.Reason, tc.wantReasons)
			if !tc.wantFloor && strings.Contains(got.Reason, reasonTierFloor) {
				t.Errorf("Reason = %q, want no tier floor clause", got.Reason)
			}
		})
	}
}

// An unknown-state tool is eligible only when it appears in Failover verbatim,
// and only after every healthy candidate.
func TestRecommend_UnknownEligibility(t *testing.T) {
	claudeExhausted := recSnapshot(Claude, "personal", 5, 90, nil)
	claudeConstrained := recSnapshot(Claude, "personal", 20, 90, nil)
	claudeHealthy := recSnapshot(Claude, "personal", 90, 90, nil)

	policyWith := func(failover ...string) Policy {
		p := DefaultPolicy("claude")
		p.Failover = failover
		return p
	}

	t.Run("listed-in-failover-is-a-late-fallback", func(t *testing.T) {
		got := Recommend(
			Request{Role: "planner", Tier: TierMid, Prefer: "gemini"},
			[]Snapshot{claudeExhausted}, autoTools(), policyWith("gemini", "claude"),
		)
		if got.Tool != "gemini" || got.State != StateUnknown {
			t.Fatalf("Tool/State = %q/%q, want gemini/unknown", got.Tool, got.State)
		}
		if got.Provider != "" || got.Model != "" {
			t.Errorf("Provider/Model = %q/%q, want both empty for a tool with no usage provider", got.Provider, got.Model)
		}
		assertReason(t, got.Reason, []string{reasonUnknownFailover})
	})

	t.Run("listed-in-failover-still-loses-to-a-healthy-candidate", func(t *testing.T) {
		got := Recommend(
			Request{Role: "planner", Tier: TierMid, Prefer: "gemini"},
			[]Snapshot{claudeHealthy}, autoTools(), policyWith("gemini", "claude"),
		)
		if got.Tool != "claude" {
			t.Fatalf("Tool = %q, want claude", got.Tool)
		}
		assertReason(t, got.Reason, []string{reasonFirstHealthy})
	})

	// Rule 3's tail must respect the same eligibility rule the unknown step
	// applies: an EXHAUSTED candidate outranks an unknown one that Failover
	// does not list, even though the unknown one is the preferred tool and sits
	// first in the candidate list.
	t.Run("not-listed-in-failover-loses-even-to-an-exhausted-candidate", func(t *testing.T) {
		got := Recommend(
			Request{Role: "planner", Tier: TierMid, Prefer: "gemini"},
			[]Snapshot{claudeExhausted}, autoTools(), policyWith("claude"),
		)
		if got.Tool != "claude" || got.State != StateExhausted {
			t.Fatalf("Tool/State = %q/%q, want claude/exhausted", got.Tool, got.State)
		}
		assertReason(t, got.Reason, []string{reasonExhaustedFallback})
		if strings.Contains(got.Reason, reasonUnknownFailover) {
			t.Errorf("Reason = %q, want no unknown-failover clause for a tool absent from Failover", got.Reason)
		}
	})

	// The one case where the tail DOES name an ineligible unknown: it is the
	// resolved preferred tool and no candidate is eligible at all.
	t.Run("an-ineligible-unknown-preferred-tool-is-still-the-last-resort", func(t *testing.T) {
		got := Recommend(
			Request{Role: "planner", Tier: TierMid, Prefer: "gemini"},
			nil, autoTools(), policyWith(),
		)
		if got.Tool != "gemini" || got.State != StateUnknown {
			t.Fatalf("Tool/State = %q/%q, want gemini/unknown", got.Tool, got.State)
		}
		assertReason(t, got.Reason, []string{reasonExhaustedFallback})
	})

	t.Run("not-listed-in-failover-is-never-selected", func(t *testing.T) {
		got := Recommend(
			Request{Role: "planner", Tier: TierMid, Prefer: "gemini"},
			[]Snapshot{claudeConstrained}, autoTools(), policyWith("claude"),
		)
		if got.Tool != "claude" || got.State != StateConstrained {
			t.Fatalf("Tool/State = %q/%q, want claude/constrained", got.Tool, got.State)
		}
		assertReason(t, got.Reason, []string{reasonFirstConstrained})
		want := Alternative{Tool: "gemini", State: StateUnknown, RemainingPercent: -1}
		if len(got.Alternatives) != 1 || got.Alternatives[0] != want {
			t.Errorf("Alternatives = %+v, want [%+v]", got.Alternatives, want)
		}
	})
}

func TestRecommend_ProfileSelectsSnapshot(t *testing.T) {
	policy := DefaultPolicy("claude")
	tools := autoTools()
	tools.AvailableTools = []string{"claude"}
	// Deliberately out of sorted order in the input slice.
	snapshots := []Snapshot{
		recSnapshot(Claude, "beta", 90, 90, nil),
		recSnapshot(Claude, "alpha", 5, 90, nil),
	}

	t.Run("empty-profile-takes-the-first-account-in-sorted-order", func(t *testing.T) {
		got := Recommend(Request{Role: "planner", Tier: TierMid, Prefer: "claude"}, snapshots, tools, policy)
		if got.Account != "alpha" || got.State != StateExhausted {
			t.Fatalf("Account/State = %q/%q, want alpha/exhausted", got.Account, got.State)
		}
	})

	t.Run("named-profile-selects-that-account", func(t *testing.T) {
		got := Recommend(Request{Role: "planner", Tier: TierMid, Prefer: "claude", Profile: "beta"}, snapshots, tools, policy)
		if got.Account != "beta" || got.State != StateHealthy {
			t.Fatalf("Account/State = %q/%q, want beta/healthy", got.Account, got.State)
		}
	})

	t.Run("unmatched-profile-is-unknown-and-says-so", func(t *testing.T) {
		got := Recommend(Request{Role: "planner", Tier: TierMid, Prefer: "claude", Profile: "nope"}, snapshots, tools, policy)
		if got.State != StateUnknown {
			t.Errorf("State = %q, want unknown", got.State)
		}
		if got.Account != "" {
			t.Errorf("Account = %q, want empty", got.Account)
		}
		assertReason(t, got.Reason, []string{"nope"})
	})
}

func TestRecommend_DegenerateInputs(t *testing.T) {
	t.Run("zero-everything-does-not-panic", func(t *testing.T) {
		got := Recommend(Request{}, nil, session.OrchestrateToolPolicy{}, Policy{})
		if got.Tool != "" || got.Provider != "" || got.Model != "" {
			t.Errorf("Tool/Provider/Model = %q/%q/%q, want all empty", got.Tool, got.Provider, got.Model)
		}
		if got.State != StateUnknown {
			t.Errorf("State = %q, want unknown", got.State)
		}
		if got.Alternatives == nil {
			t.Errorf("Alternatives = nil, want an empty slice so the JSON carries []")
		}
		assertReason(t, got.Reason, []string{reasonNoCandidates})
		// "no candidate tools" and "policy limited failover" are contradictory
		// as a pair: there was no failover left to limit.
		if strings.Contains(got.Reason, reasonPolicyLimited) {
			t.Errorf("Reason = %q, want no policy-limited clause alongside %q", got.Reason, reasonNoCandidates)
		}
	})

	t.Run("empty-request-with-defaults-and-no-snapshots", func(t *testing.T) {
		got := Recommend(Request{}, nil, autoTools(), DefaultPolicy("claude"))
		if got.Tool != "claude" {
			t.Errorf("Tool = %q, want claude (the first failover entry)", got.Tool)
		}
		if got.State != StateUnknown {
			t.Errorf("State = %q, want unknown", got.State)
		}
		if got.Model != "" {
			t.Errorf("Model = %q, want empty for an unset tier", got.Model)
		}
		if !got.FetchedAt.IsZero() {
			t.Errorf("FetchedAt = %v, want the zero time", got.FetchedAt)
		}
		assertReason(t, got.Reason, []string{reasonUnknownFailover})
	})

	// The final fallback is NOT filtered against the orchestrate policy: it
	// names the preferred tool even when AvailableTools excludes it, and
	// ProvidersToQuery then returns nothing for the very same input.
	t.Run("preferred-tool-outside-available-tools-is-still-recommended", func(t *testing.T) {
		tools := session.OrchestrateToolPolicy{
			Strategy: "auto", FallbackTool: "codex", AvailableTools: []string{"gemini"},
		}
		policy := DefaultPolicy("claude")
		req := Request{Role: "planner", Tier: TierMid, Prefer: "claude"}
		got := Recommend(req, []Snapshot{recSnapshot(Claude, "personal", 90, 90, nil)}, tools, policy)
		if got.Tool != "claude" {
			t.Errorf("Tool = %q, want claude even though AvailableTools excludes it", got.Tool)
		}
		if got.State != StateHealthy {
			t.Errorf("State = %q, want healthy", got.State)
		}
		assertReason(t, got.Reason, []string{reasonNoCandidates})
		if providers := ProvidersToQuery(req, tools, policy); len(providers) != 0 {
			t.Errorf("ProvidersToQuery = %v, want none for the input whose decision names claude", providers)
		}
	})

	// When candidates DO exist, the tail takes the first eligible one, not the
	// preferred tool the strategy already filtered out.
	t.Run("preferred-tool-filtered-out-falls-back-to-the-first-candidate", func(t *testing.T) {
		tools := session.OrchestrateToolPolicy{
			Strategy: "auto", FallbackTool: "claude", AvailableTools: []string{"codex"},
		}
		got := Recommend(
			Request{Role: "planner", Tier: TierMid, Prefer: "claude"},
			[]Snapshot{recSnapshot(Codex, "work", 5, 90, nil)},
			tools, DefaultPolicy("claude"),
		)
		if got.Tool != "codex" || got.State != StateExhausted {
			t.Fatalf("Tool/State = %q/%q, want codex/exhausted (claude is not a candidate)", got.Tool, got.State)
		}
		assertReason(t, got.Reason, []string{reasonExhaustedFallback})
	})

	t.Run("auto-strategy-with-no-available-tools", func(t *testing.T) {
		tools := session.OrchestrateToolPolicy{Strategy: "auto"}
		got := Recommend(Request{Role: "planner", Tier: TierMid, Prefer: "claude"}, nil, tools, DefaultPolicy("claude"))
		if got.Tool != "claude" {
			t.Errorf("Tool = %q, want the preferred tool", got.Tool)
		}
		if got.State != StateUnknown {
			t.Errorf("State = %q, want unknown", got.State)
		}
		assertReason(t, got.Reason, []string{reasonNoCandidates})
	})
}

// ProvidersToQuery must agree with the candidate list Recommend considers: the
// two share one unexported helper so the CLI cannot drift from the recommender.
func TestProvidersToQuery_AgreesWithRecommendCandidates(t *testing.T) {
	tests := []struct {
		name      string
		strategy  string
		available []string
		prefer    string
		failover  []string
		want      []Provider
	}{
		{"auto/claude-first", "auto", []string{"claude", "codex"}, "claude", []string{"claude", "codex"}, []Provider{Claude, Codex}},
		{"auto/codex-first", "auto", []string{"claude", "codex"}, "codex", []string{"claude", "codex"}, []Provider{Codex, Claude}},
		{"auto/filters-unavailable", "auto", []string{"codex"}, "claude", []string{"claude", "codex"}, []Provider{Codex}},
		{"auto/omits-tools-without-a-provider", "auto", []string{"claude", "gemini"}, "gemini", []string{"gemini", "claude"}, []Provider{Claude}},
		{"auto/dedupes-prefer-already-in-failover", "auto", []string{"claude", "codex"}, "codex", []string{"codex", "claude"}, []Provider{Codex, Claude}},
		{"default/collapses-to-prefer", "default", []string{"claude", "codex"}, "codex", []string{"claude", "codex"}, []Provider{Codex}},
		{"empty/collapses-to-prefer", "", []string{"claude", "codex"}, "claude", []string{"claude", "codex"}, []Provider{Claude}},
		{"empty/no-prefer-collapses-to-fallback-tool", "", []string{"claude", "codex"}, "", []string{"claude", "codex"}, []Provider{Claude}},
		{"auto/no-available-tools-yields-nothing", "auto", nil, "claude", []string{"claude", "codex"}, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := DefaultPolicy("claude")
			policy.Failover = tc.failover
			tools := session.OrchestrateToolPolicy{
				Strategy:       tc.strategy,
				FallbackTool:   "claude",
				AvailableTools: tc.available,
			}
			req := Request{Role: "planner", Tier: TierMid, Prefer: tc.prefer}

			got := ProvidersToQuery(req, tools, policy)
			if len(got) != len(tc.want) {
				t.Fatalf("ProvidersToQuery = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("ProvidersToQuery = %v, want %v", got, tc.want)
				}
			}

			// The same helper backs Recommend's candidate list.
			candidates, _, _ := candidateTools(req, tools, policy)
			var viaCandidates []Provider
			seen := map[Provider]bool{}
			for _, tool := range candidates {
				if provider, ok := toolProvider(tool); ok && !seen[provider] {
					seen[provider] = true
					viaCandidates = append(viaCandidates, provider)
				}
			}
			if fmt.Sprint(viaCandidates) != fmt.Sprint(got) {
				t.Fatalf("candidate providers = %v, ProvidersToQuery = %v", viaCandidates, got)
			}

			// And Recommend really considers exactly those candidate tools.
			decision := Recommend(req, nil, tools, policy)
			considered := make([]string, 0, len(candidates))
			for _, alt := range decision.Alternatives {
				considered = append(considered, alt.Tool)
			}
			if len(candidates) > 0 {
				considered = append(considered, decision.Tool)
			}
			if len(considered) != len(candidates) {
				t.Fatalf("Recommend considered %v, candidates are %v", considered, candidates)
			}
		})
	}
}

func TestDecision_JSONKeys(t *testing.T) {
	decision := Recommend(
		Request{Role: "implementer", Tier: TierCheap, Prefer: "claude"},
		[]Snapshot{recSnapshot(Claude, "personal", 90, 90, nil), recSnapshot(Codex, "work", 90, 90, nil)},
		autoTools(), DefaultPolicy("claude"),
	)
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	for _, key := range []string{
		"tool", "provider", "model", "tier_requested", "tier_applied",
		"account", "state", "reason", "alternatives", "fetched_at",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("decision JSON is missing key %q: %s", key, raw)
		}
	}
	alts, ok := decoded["alternatives"].([]any)
	if !ok || len(alts) == 0 {
		t.Fatalf("alternatives = %v, want a non-empty array", decoded["alternatives"])
	}
	alt, ok := alts[0].(map[string]any)
	if !ok {
		t.Fatalf("alternatives[0] = %v, want an object", alts[0])
	}
	for _, key := range []string{"tool", "state", "remaining_percent"} {
		if _, ok := alt[key]; !ok {
			t.Errorf("alternative JSON is missing key %q: %s", key, raw)
		}
	}
}
