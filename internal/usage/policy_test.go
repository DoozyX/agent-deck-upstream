package usage

import (
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func policyStrPtr(s string) *string { return &s }
func policyIntPtr(i int) *int       { return &i }

func TestDefaultPolicy_ThresholdsLaddersAndFrontierWindow(t *testing.T) {
	p := DefaultPolicy("claude")

	if p.ExhaustedBelow != 15 {
		t.Errorf("ExhaustedBelow = %d, want 15", p.ExhaustedBelow)
	}
	if p.ConstrainedBelow != 35 {
		t.Errorf("ConstrainedBelow = %d, want 35", p.ConstrainedBelow)
	}
	wantLadder := map[Provider]Ladder{
		Claude: {Cheap: "haiku", Mid: "sonnet", Strong: "opus", Frontier: "fable"},
		Codex:  {Cheap: "gpt-5.6-luna", Mid: "gpt-5.6-terra", Strong: "gpt-5.6-sol", Frontier: "gpt-6-astra"},
	}
	if !reflect.DeepEqual(p.Ladder, wantLadder) {
		t.Errorf("Ladder = %#v, want %#v", p.Ladder, wantLadder)
	}
	wantWindow := map[Provider]string{Claude: "fable"}
	if !reflect.DeepEqual(p.FrontierWindow, wantWindow) {
		t.Errorf("FrontierWindow = %#v, want %#v", p.FrontierWindow, wantWindow)
	}
}

func TestDefaultPolicy_FailoverOrderFollowsDefaultTool(t *testing.T) {
	tests := []struct {
		name        string
		defaultTool string
		want        []string
	}{
		{name: "claude first", defaultTool: "claude", want: []string{"claude", "codex"}},
		{name: "codex first", defaultTool: "codex", want: []string{"codex", "claude"}},
		{name: "whitespace is trimmed", defaultTool: "  codex  ", want: []string{"codex", "claude"}},
		{name: "empty is deterministic", defaultTool: "", want: []string{"claude", "codex"}},
		{name: "unrecognised is deterministic", defaultTool: "cursor", want: []string{"claude", "codex"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultPolicy(tc.defaultTool).Failover; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DefaultPolicy(%q).Failover = %v, want %v", tc.defaultTool, got, tc.want)
			}
		})
	}
}

func TestDefaultPolicy_ReturnsIndependentMaps(t *testing.T) {
	a := DefaultPolicy("claude")
	a.Ladder[Claude] = Ladder{Cheap: "mutated"}
	a.FrontierWindow[Claude] = "mutated"
	a.Failover[0] = "mutated"

	b := DefaultPolicy("claude")
	if b.Ladder[Claude].Cheap != "haiku" {
		t.Errorf("Ladder leaked between calls: Cheap = %q", b.Ladder[Claude].Cheap)
	}
	if b.FrontierWindow[Claude] != "fable" {
		t.Errorf("FrontierWindow leaked between calls: %q", b.FrontierWindow[Claude])
	}
	if b.Failover[0] != "claude" {
		t.Errorf("Failover leaked between calls: %q", b.Failover[0])
	}
}

func TestPolicyFromConfig_NilConfigYieldsDefaults(t *testing.T) {
	got, err := PolicyFromConfig(nil)
	if err != nil {
		t.Fatalf("PolicyFromConfig(nil) error = %v", err)
	}
	if !reflect.DeepEqual(got, DefaultPolicy("")) {
		t.Fatalf("PolicyFromConfig(nil) = %#v, want DefaultPolicy(\"\")", got)
	}
}

func TestPolicyFromConfig_AbsentBlockYieldsDefaultsForDefaultTool(t *testing.T) {
	cfg := &session.UserConfig{DefaultTool: "codex"}

	got, err := PolicyFromConfig(cfg)
	if err != nil {
		t.Fatalf("PolicyFromConfig() error = %v", err)
	}
	if want := DefaultPolicy("codex"); !reflect.DeepEqual(got, want) {
		t.Fatalf("PolicyFromConfig() = %#v, want %#v", got, want)
	}
}

func TestPolicyFromConfig_FullyPopulatedBlockRoundTrips(t *testing.T) {
	cfg := &session.UserConfig{DefaultTool: "claude"}
	cfg.Usage.Policy = session.UsagePolicySettings{
		ExhaustedBelow:   policyIntPtr(10),
		ConstrainedBelow: policyIntPtr(50),
		Failover:         []string{"codex", "claude"},
		Ladder: map[string]session.UsageLadderSettings{
			"claude": {
				Cheap:    policyStrPtr("haiku-next"),
				Mid:      policyStrPtr("sonnet-next"),
				Strong:   policyStrPtr("opus-next"),
				Frontier: policyStrPtr(""), // intentionally empty: tier unavailable
			},
			"codex": {
				Cheap:    policyStrPtr("luna"),
				Mid:      policyStrPtr("terra"),
				Strong:   policyStrPtr("sol"),
				Frontier: policyStrPtr("astra"),
			},
		},
		FrontierWindow: map[string]string{"claude": "spark", "codex": "weekly"},
	}

	got, err := PolicyFromConfig(cfg)
	if err != nil {
		t.Fatalf("PolicyFromConfig() error = %v", err)
	}
	want := Policy{
		ExhaustedBelow:   10,
		ConstrainedBelow: 50,
		Failover:         []string{"codex", "claude"},
		Ladder: map[Provider]Ladder{
			Claude: {Cheap: "haiku-next", Mid: "sonnet-next", Strong: "opus-next", Frontier: ""},
			Codex:  {Cheap: "luna", Mid: "terra", Strong: "sol", Frontier: "astra"},
		},
		FrontierWindow: map[Provider]string{Claude: "spark", Codex: "weekly"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PolicyFromConfig() = %#v, want %#v", got, want)
	}
}

func TestPolicyFromConfig_PartialBlockKeepsDefaultsPerKey(t *testing.T) {
	cfg := &session.UserConfig{DefaultTool: "codex"}
	cfg.Usage.Policy = session.UsagePolicySettings{
		ExhaustedBelow: policyIntPtr(5),
		Ladder: map[string]session.UsageLadderSettings{
			"claude": {Frontier: policyStrPtr("")},
		},
		FrontierWindow: map[string]string{"codex": "weekly"},
	}

	got, err := PolicyFromConfig(cfg)
	if err != nil {
		t.Fatalf("PolicyFromConfig() error = %v", err)
	}
	if got.ExhaustedBelow != 5 {
		t.Errorf("ExhaustedBelow = %d, want 5", got.ExhaustedBelow)
	}
	if got.ConstrainedBelow != 35 {
		t.Errorf("ConstrainedBelow = %d, want the default 35", got.ConstrainedBelow)
	}
	if want := []string{"codex", "claude"}; !reflect.DeepEqual(got.Failover, want) {
		t.Errorf("Failover = %v, want the default %v", got.Failover, want)
	}
	wantLadder := map[Provider]Ladder{
		// Only the claude frontier entry was overridden; every other rung keeps
		// its default.
		Claude: {Cheap: "haiku", Mid: "sonnet", Strong: "opus", Frontier: ""},
		Codex:  {Cheap: "gpt-5.6-luna", Mid: "gpt-5.6-terra", Strong: "gpt-5.6-sol", Frontier: "gpt-6-astra"},
	}
	if !reflect.DeepEqual(got.Ladder, wantLadder) {
		t.Errorf("Ladder = %#v, want %#v", got.Ladder, wantLadder)
	}
	wantWindow := map[Provider]string{Claude: "fable", Codex: "weekly"}
	if !reflect.DeepEqual(got.FrontierWindow, wantWindow) {
		t.Errorf("FrontierWindow = %#v, want %#v", got.FrontierWindow, wantWindow)
	}
}

func TestPolicyFromConfig_ExplicitEmptyFrontierWindowRemovesTheGate(t *testing.T) {
	cfg := &session.UserConfig{DefaultTool: "claude"}
	cfg.Usage.Policy.FrontierWindow = map[string]string{"claude": ""}

	got, err := PolicyFromConfig(cfg)
	if err != nil {
		t.Fatalf("PolicyFromConfig() error = %v", err)
	}
	if want := (map[Provider]string{Claude: ""}); !reflect.DeepEqual(got.FrontierWindow, want) {
		t.Fatalf("FrontierWindow = %#v, want %#v", got.FrontierWindow, want)
	}
}

// Failover entries are TOOL names, not provider names, and are validated by
// shape only at BOTH layers. The design's recommender rule 2 makes a tool in the
// "unknown" state eligible precisely when it appears in Failover explicitly, so
// an entry with no usage provider must survive the merge untouched — including a
// miscased "Codex", which stays inert by design rather than being folded or
// rejected. The loader agrees; see
// TestLoadUserConfig_AcceptsUsagePolicyFailoverEntryThatIsNotAToolName.
func TestPolicyFromConfig_AcceptsFailoverEntriesThatAreNotUsageProviders(t *testing.T) {
	cfg := &session.UserConfig{DefaultTool: "claude"}
	cfg.Usage.Policy.Failover = []string{"totally-not-a-tool", "Codex", "claude"}

	got, err := PolicyFromConfig(cfg)
	if err != nil {
		t.Fatalf("PolicyFromConfig() error = %v", err)
	}
	if want := []string{"totally-not-a-tool", "Codex", "claude"}; !reflect.DeepEqual(got.Failover, want) {
		t.Fatalf("Failover = %v, want %v", got.Failover, want)
	}
}

// An explicit `failover = []` is treated as an omitted key. The whole pipeline
// relies on Policy.Failover being non-empty — the design has the recommender
// read policy.Failover[0] unguarded — so an empty list keeps the default order
// rather than disabling failover.
func TestPolicyFromConfig_ExplicitEmptyFailoverKeepsTheDefaultOrder(t *testing.T) {
	cfg := &session.UserConfig{DefaultTool: "codex"}
	cfg.Usage.Policy.Failover = []string{}

	got, err := PolicyFromConfig(cfg)
	if err != nil {
		t.Fatalf("PolicyFromConfig() error = %v", err)
	}
	if want := []string{"codex", "claude"}; !reflect.DeepEqual(got.Failover, want) {
		t.Fatalf("Failover = %v, want the default %v", got.Failover, want)
	}
}

// The returned Failover must not alias the config's slice: LoadUserConfig hands
// every caller the same process-cached *UserConfig, so an aliasing assignment
// would let one caller's mutation write through into every other caller's
// config.
func TestPolicyFromConfig_FailoverDoesNotAliasTheConfigSlice(t *testing.T) {
	cfg := &session.UserConfig{DefaultTool: "claude"}
	cfg.Usage.Policy.Failover = []string{"codex", "claude"}

	got, err := PolicyFromConfig(cfg)
	if err != nil {
		t.Fatalf("PolicyFromConfig() error = %v", err)
	}
	got.Failover[0] = "mutated"
	if cfg.Usage.Policy.Failover[0] != "codex" {
		t.Fatalf("mutating Policy.Failover wrote through to the config: %q", cfg.Usage.Policy.Failover[0])
	}
}

func TestPolicyFromConfig_RejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name     string
		settings session.UsagePolicySettings
		wantErr  string
	}{
		{
			name:     "exhausted_below above 100",
			settings: session.UsagePolicySettings{ExhaustedBelow: policyIntPtr(150)},
			wantErr:  `invalid [usage.policy].exhausted_below 150: must be between 0 and 100`,
		},
		{
			name:     "constrained_below below 0",
			settings: session.UsagePolicySettings{ConstrainedBelow: policyIntPtr(-1)},
			wantErr:  `invalid [usage.policy].constrained_below -1: must be between 0 and 100`,
		},
		{
			name:     "both present and inverted",
			settings: session.UsagePolicySettings{ExhaustedBelow: policyIntPtr(60), ConstrainedBelow: policyIntPtr(40)},
			wantErr:  `invalid [usage.policy].exhausted_below 60: must be <= constrained_below 40`,
		},
		{
			// Only exhausted_below is set, so the inversion only exists against
			// the default constrained_below. The merge must still reject it.
			name:     "inverted against the default constrained_below",
			settings: session.UsagePolicySettings{ExhaustedBelow: policyIntPtr(50)},
			wantErr:  `invalid [usage.policy].exhausted_below 50: must be <= constrained_below 35`,
		},
		{
			// The mirror case: only constrained_below is set, and it inverts
			// against the default exhausted_below. The message intentionally
			// names the effective values, so it reports the defaulted 15 the
			// user never wrote — that is what the merge actually rejects.
			name:     "inverted against the default exhausted_below",
			settings: session.UsagePolicySettings{ConstrainedBelow: policyIntPtr(10)},
			wantErr:  `invalid [usage.policy].exhausted_below 15: must be <= constrained_below 10`,
		},
		{
			name:     "empty failover entry",
			settings: session.UsagePolicySettings{Failover: []string{"claude", ""}},
			wantErr:  `invalid [usage.policy].failover[1] "": must be a tool name without whitespace`,
		},
		{
			name:     "whitespace-only failover entry",
			settings: session.UsagePolicySettings{Failover: []string{"   "}},
			wantErr:  `invalid [usage.policy].failover[0] "   ": must be a tool name without whitespace`,
		},
		{
			name:     "failover entry containing whitespace",
			settings: session.UsagePolicySettings{Failover: []string{"claude codex"}},
			wantErr:  `invalid [usage.policy].failover[0] "claude codex": must be a tool name without whitespace`,
		},
		{
			// A typo'd table name would otherwise discard the whole override
			// and leave the defaults silently in place.
			name: "unknown ladder table key",
			settings: session.UsagePolicySettings{
				Ladder: map[string]session.UsageLadderSettings{"cluade": {Cheap: policyStrPtr("haiku")}},
			},
			wantErr: `invalid [usage.policy].ladder.cluade: unknown usage provider`,
		},
		{
			name: "ladder table key with the wrong case",
			settings: session.UsagePolicySettings{
				Ladder: map[string]session.UsageLadderSettings{"Claude": {Cheap: policyStrPtr("haiku")}},
			},
			wantErr: `invalid [usage.policy].ladder.Claude: unknown usage provider`,
		},
		{
			name:     "unknown frontier_window table key",
			settings: session.UsagePolicySettings{FrontierWindow: map[string]string{"cluade": "fable"}},
			wantErr:  `invalid [usage.policy].frontier_window.cluade: unknown usage provider`,
		},
		{
			name:     "frontier_window table key with the wrong case",
			settings: session.UsagePolicySettings{FrontierWindow: map[string]string{"Claude": "fable"}},
			wantErr:  `invalid [usage.policy].frontier_window.Claude: unknown usage provider`,
		},
		{
			// A ladder value carrying whitespace reaches the launch flag
			// unchanged, so it is rejected here rather than three units later.
			name: "ladder value containing whitespace",
			settings: session.UsagePolicySettings{
				Ladder: map[string]session.UsageLadderSettings{"claude": {Strong: policyStrPtr("opus 4")}},
			},
			wantErr: `invalid [usage.policy].ladder.claude.strong "opus 4": must be a model name without whitespace`,
		},
		{
			name: "whitespace-only ladder value",
			settings: session.UsagePolicySettings{
				Ladder: map[string]session.UsageLadderSettings{"codex": {Frontier: policyStrPtr("  ")}},
			},
			wantErr: `invalid [usage.policy].ladder.codex.frontier "  ": must be a model name without whitespace`,
		},
		{
			// A frontier_window value with a stray space never matches a window
			// in Models, so the frontier gate silently stops gating — the
			// opposite of what the user configured.
			name:     "frontier_window value containing whitespace",
			settings: session.UsagePolicySettings{FrontierWindow: map[string]string{"claude": " fable"}},
			wantErr:  `invalid [usage.policy].frontier_window.claude " fable": must be a window name without whitespace`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &session.UserConfig{DefaultTool: "claude"}
			cfg.Usage.Policy = tc.settings

			got, err := PolicyFromConfig(cfg)
			if err == nil {
				t.Fatalf("PolicyFromConfig() error = nil, want %q", tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("PolicyFromConfig() error = %q, want %q", err.Error(), tc.wantErr)
			}
			if !reflect.DeepEqual(got, Policy{}) {
				t.Fatalf("PolicyFromConfig() = %#v on error, want the zero Policy", got)
			}
		})
	}
}
