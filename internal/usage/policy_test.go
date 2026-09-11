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

func TestPolicyFromConfig_AcceptsFailoverToolWithoutUsageProvider(t *testing.T) {
	cfg := &session.UserConfig{DefaultTool: "claude"}
	cfg.Usage.Policy.Failover = []string{"cursor", "claude"}

	got, err := PolicyFromConfig(cfg)
	if err != nil {
		t.Fatalf("PolicyFromConfig() error = %v", err)
	}
	if want := []string{"cursor", "claude"}; !reflect.DeepEqual(got.Failover, want) {
		t.Fatalf("Failover = %v, want %v", got.Failover, want)
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
