package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeUsagePolicyConfig writes content as the user config in an isolated
// XDG_CONFIG_HOME and clears the mtime-keyed loader cache around the test. The
// cache is shared process-wide, so a test that skips ClearUserConfigCache is
// flaky by construction — see TestLoadUserConfig_ParsesOrchestrateToolStrategy.
func writeUsagePolicyConfig(t *testing.T, content string) {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	path := filepath.Join(configDir, "agent-deck", UserConfigFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUserConfig_AbsentUsagePolicyLeavesEveryKeyUnset(t *testing.T) {
	writeUsagePolicyConfig(t, "default_tool = \"codex\"\n")

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.Usage, UsageSettings{}) {
		t.Fatalf("Usage = %#v, want the zero UsageSettings", cfg.Usage)
	}
}

func TestLoadUserConfig_ParsesUsagePolicy(t *testing.T) {
	writeUsagePolicyConfig(t, `default_tool = "claude"

[usage.policy]
exhausted_below = 10
constrained_below = 50
failover = ["codex", "claude"]

[usage.policy.ladder.claude]
cheap = "haiku"
mid = "sonnet"
strong = "opus"
frontier = ""

[usage.policy.ladder.codex]
cheap = "gpt-5.6-luna"
mid = "gpt-5.6-terra"
strong = "gpt-5.6-sol"
frontier = "gpt-6-astra"

[usage.policy.frontier_window]
claude = "fable"
`)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	got := cfg.Usage.Policy
	if got.ExhaustedBelow == nil || *got.ExhaustedBelow != 10 {
		t.Errorf("ExhaustedBelow = %v, want 10", got.ExhaustedBelow)
	}
	if got.ConstrainedBelow == nil || *got.ConstrainedBelow != 50 {
		t.Errorf("ConstrainedBelow = %v, want 50", got.ConstrainedBelow)
	}
	if want := []string{"codex", "claude"}; !reflect.DeepEqual(got.Failover, want) {
		t.Errorf("Failover = %v, want %v", got.Failover, want)
	}
	claude, ok := got.Ladder["claude"]
	if !ok {
		t.Fatalf("Ladder has no claude entry: %#v", got.Ladder)
	}
	if claude.Cheap == nil || *claude.Cheap != "haiku" {
		t.Errorf("claude ladder cheap = %v, want haiku", claude.Cheap)
	}
	if claude.Mid == nil || *claude.Mid != "sonnet" {
		t.Errorf("claude ladder mid = %v, want sonnet", claude.Mid)
	}
	if claude.Strong == nil || *claude.Strong != "opus" {
		t.Errorf("claude ladder strong = %v, want opus", claude.Strong)
	}
	// An explicitly empty entry is distinct from an omitted one: it means the
	// tier is unavailable on that provider.
	if claude.Frontier == nil || *claude.Frontier != "" {
		t.Errorf("claude ladder frontier = %v, want an explicit empty string", claude.Frontier)
	}
	codex, ok := got.Ladder["codex"]
	if !ok {
		t.Fatalf("Ladder has no codex entry: %#v", got.Ladder)
	}
	if codex.Frontier == nil || *codex.Frontier != "gpt-6-astra" {
		t.Errorf("codex ladder frontier = %v, want gpt-6-astra", codex.Frontier)
	}
	if want := (map[string]string{"claude": "fable"}); !reflect.DeepEqual(got.FrontierWindow, want) {
		t.Errorf("FrontierWindow = %#v, want %#v", got.FrontierWindow, want)
	}
}

func TestLoadUserConfig_ParsesPartialUsagePolicyLeavingOmittedKeysUnset(t *testing.T) {
	writeUsagePolicyConfig(t, `[usage.policy]
exhausted_below = 5

[usage.policy.ladder.claude]
frontier = ""
`)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	got := cfg.Usage.Policy
	if got.ExhaustedBelow == nil || *got.ExhaustedBelow != 5 {
		t.Errorf("ExhaustedBelow = %v, want 5", got.ExhaustedBelow)
	}
	if got.ConstrainedBelow != nil {
		t.Errorf("ConstrainedBelow = %v, want nil (omitted)", *got.ConstrainedBelow)
	}
	if got.Failover != nil {
		t.Errorf("Failover = %v, want nil (omitted)", got.Failover)
	}
	claude := got.Ladder["claude"]
	if claude.Cheap != nil {
		t.Errorf("claude ladder cheap = %v, want nil (omitted)", *claude.Cheap)
	}
	if claude.Frontier == nil || *claude.Frontier != "" {
		t.Errorf("claude ladder frontier = %v, want an explicit empty string", claude.Frontier)
	}
}

// The loader validates failover entries by SHAPE only and never consults the
// tool registry — config validity must not depend on what happens to be on
// PATH, and this mtime-cached loader must stay free of that I/O. The entry here
// is deliberately not a tool name at all, so inserting a registry lookup would
// break this test; "cursor" would not, because it IS a registry builtin.
//
// usage.PolicyFromConfig does not check the entry either: failover holds TOOL
// names, and the design's recommender rule 2 makes a tool in the "unknown"
// state eligible precisely when it appears in failover explicitly, so an entry
// with no usage provider must survive both layers untouched — see
// TestPolicyFromConfig_AcceptsFailoverEntriesThatAreNotUsageProviders.
func TestLoadUserConfig_AcceptsUsagePolicyFailoverEntryThatIsNotAToolName(t *testing.T) {
	writeUsagePolicyConfig(t, "[usage.policy]\nfailover = [\"totally-not-a-tool\"]\n")

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if want := []string{"totally-not-a-tool"}; !reflect.DeepEqual(cfg.Usage.Policy.Failover, want) {
		t.Fatalf("Failover = %v, want %v", cfg.Usage.Policy.Failover, want)
	}
}

// The loader never checks ladder or frontier_window TABLE KEYS against the
// usage provider set: that set lives in internal/usage, and internal/session
// must not import it (cross-task contract 4). A typo'd table name therefore
// loads with a nil error and lands in cfg.Usage.Policy verbatim;
// usage.PolicyFromConfig is the layer that rejects it — see
// TestPolicyFromConfig_RejectsInvalidValues, "unknown ladder table key".
// Adding key validation to validateUsagePolicySettings would break this test,
// which is the point: the two layers must disagree in exactly this direction.
func TestLoadUserConfig_AcceptsUsagePolicyLadderTableKeyThatIsNotAUsageProvider(t *testing.T) {
	writeUsagePolicyConfig(t, "[usage.policy.ladder.cluade]\ncheap = \"haiku\"\n")

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	got := cfg.Usage.Policy
	if len(got.Ladder) != 1 {
		t.Fatalf("Ladder = %#v, want only the cluade entry", got.Ladder)
	}
	cluade, ok := got.Ladder["cluade"]
	if !ok {
		t.Fatalf("Ladder has no cluade entry: %#v", got.Ladder)
	}
	if cluade.Cheap == nil || *cluade.Cheap != "haiku" {
		t.Errorf("cluade ladder cheap = %v, want haiku, unchanged by the loader", cluade.Cheap)
	}
}

// The VALUE mirror of the table-key case. A frontier_window value carrying
// whitespace never matches a window name, but only internal/usage knows what a
// window is, so the loader passes it through with a nil error and
// usage.PolicyFromConfig rejects it — see
// TestPolicyFromConfig_RejectsInvalidValues, "frontier_window value containing
// whitespace". Adding value validation here would break this test.
func TestLoadUserConfig_AcceptsUsagePolicyFrontierWindowValueWithWhitespace(t *testing.T) {
	writeUsagePolicyConfig(t, "[usage.policy.frontier_window]\nclaude = \" fable\"\n")

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	if want := (map[string]string{"claude": " fable"}); !reflect.DeepEqual(cfg.Usage.Policy.FrontierWindow, want) {
		t.Fatalf("FrontierWindow = %#v, want %#v, unchanged by the loader", cfg.Usage.Policy.FrontierWindow, want)
	}
}

func TestLoadUserConfig_AcceptsZeroUsagePolicyThresholds(t *testing.T) {
	writeUsagePolicyConfig(t, "[usage.policy]\nexhausted_below = 0\nconstrained_below = 0\n")

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	got := cfg.Usage.Policy
	if got.ExhaustedBelow == nil || *got.ExhaustedBelow != 0 {
		t.Errorf("ExhaustedBelow = %v, want an explicit 0", got.ExhaustedBelow)
	}
	if got.ConstrainedBelow == nil || *got.ConstrainedBelow != 0 {
		t.Errorf("ConstrainedBelow = %v, want an explicit 0", got.ConstrainedBelow)
	}
}

// The loader validates shape only, so a lone constrained_below that inverts
// against usage.DefaultPolicy's exhausted_below is well-formed here. Rejecting
// the merged pair is usage.PolicyFromConfig's job — the loader must not
// hardcode defaults that live in a package it cannot import.
func TestLoadUserConfig_AcceptsLoneConstrainedBelowBelowTheDefaultExhaustedBelow(t *testing.T) {
	writeUsagePolicyConfig(t, "[usage.policy]\nconstrained_below = 10\n")

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	got := cfg.Usage.Policy
	if got.ConstrainedBelow == nil || *got.ConstrainedBelow != 10 {
		t.Errorf("ConstrainedBelow = %v, want 10", got.ConstrainedBelow)
	}
	if got.ExhaustedBelow != nil {
		t.Errorf("ExhaustedBelow = %v, want nil (omitted)", *got.ExhaustedBelow)
	}
}

func TestLoadUserConfig_RejectsInvalidUsagePolicy(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "exhausted_below above 100",
			content: "[usage.policy]\nexhausted_below = 150\n",
			wantErr: `invalid [usage.policy].exhausted_below 150: must be between 0 and 100`,
		},
		{
			name:    "constrained_below below 0",
			content: "[usage.policy]\nconstrained_below = -1\n",
			wantErr: `invalid [usage.policy].constrained_below -1: must be between 0 and 100`,
		},
		{
			name:    "inverted thresholds",
			content: "[usage.policy]\nexhausted_below = 60\nconstrained_below = 40\n",
			wantErr: `invalid [usage.policy].exhausted_below 60: must be <= constrained_below 40`,
		},
		{
			name:    "empty failover entry",
			content: "[usage.policy]\nfailover = [\"claude\", \"\"]\n",
			wantErr: `invalid [usage.policy].failover[1] "": must be a tool name without whitespace`,
		},
		{
			name:    "whitespace-only failover entry",
			content: "[usage.policy]\nfailover = [\"   \"]\n",
			wantErr: `invalid [usage.policy].failover[0] "   ": must be a tool name without whitespace`,
		},
		{
			name:    "failover entry containing whitespace",
			content: "[usage.policy]\nfailover = [\"claude codex\"]\n",
			wantErr: `invalid [usage.policy].failover[0] "claude codex": must be a tool name without whitespace`,
		},
		{
			// The guard is unicode.IsSpace, not a literal-space check: a tab is
			// just as invalid in a tool name, and just as invisible in a config
			// file.
			name:    "failover entry containing a tab",
			content: "[usage.policy]\nfailover = [\"claude\tcodex\"]\n",
			wantErr: `invalid [usage.policy].failover[0] "claude\tcodex": must be a tool name without whitespace`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writeUsagePolicyConfig(t, "default_tool = \"codex\"\n\n"+tc.content)

			cfg, err := LoadUserConfig()
			if err == nil {
				t.Fatalf("LoadUserConfig() error = nil, want %q", tc.wantErr)
			}
			if got := err.Error(); got != tc.wantErr {
				t.Fatalf("LoadUserConfig() error = %q, want %q", got, tc.wantErr)
			}
			// Like the invalid tool_strategy path, the loader caches the
			// default config rather than a half-parsed one.
			if cfg == nil {
				t.Fatal("LoadUserConfig() returned a nil config alongside the error")
			}
			// Equality against the shipped default, not "!= codex": an
			// inequality assertion would quietly stop testing anything if the
			// default tool ever became "codex".
			if want := cloneDefaultUserConfig().DefaultTool; cfg.DefaultTool != want {
				t.Errorf("DefaultTool = %q, want the default %q (the half-parsed config leaked through)", cfg.DefaultTool, want)
			}
			if !reflect.DeepEqual(cfg.Usage, UsageSettings{}) {
				t.Errorf("Usage = %#v, want the zero UsageSettings from the default config", cfg.Usage)
			}
			// The error is cached alongside the default config, so a second
			// call (a cache hit) still surfaces it.
			if _, err := LoadUserConfig(); err == nil || err.Error() != tc.wantErr {
				t.Fatalf("second LoadUserConfig() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func usagePolicyIntPtr(i int) *int       { return &i }
func usagePolicyStrPtr(s string) *string { return &s }

// SaveUserConfig re-encodes the WHOLE config, so the [usage.policy] mirror sits
// on the ENCODE path too: a TUI action that saves for an unrelated reason must
// not rewrite or drop the user's block. That path is the encoder plus
// stripEmptyTOMLSections plus guardConfigSectionDrop, and nothing else
// exercises it in this direction — every other usage.policy test only loads.
//
// The two cases carry the values a plain int/string could not express: an
// explicit 0 threshold and an explicit empty ladder/frontier_window value on
// one side, and omitted keys that must stay nil on the other.
func TestSaveUserConfig_RoundTripsTheUsagePolicyBlock(t *testing.T) {
	tests := []struct {
		name   string
		policy UsagePolicySettings
	}{
		{
			name: "explicit zero and explicit empty values survive",
			policy: UsagePolicySettings{
				ExhaustedBelow:   usagePolicyIntPtr(0),
				ConstrainedBelow: usagePolicyIntPtr(35),
				Failover:         []string{"codex", "claude"},
				Ladder: map[string]UsageLadderSettings{
					"claude": {
						Cheap:  usagePolicyStrPtr("haiku"),
						Mid:    usagePolicyStrPtr("sonnet"),
						Strong: usagePolicyStrPtr("opus"),
						// An explicit empty value marks the tier unavailable;
						// dropping it would silently re-enable the tier.
						Frontier: usagePolicyStrPtr(""),
					},
					"codex": {Frontier: usagePolicyStrPtr("gpt-6-astra")},
				},
				// An empty window value means "no gate" and is equally load-bearing.
				FrontierWindow: map[string]string{"claude": "fable", "codex": ""},
			},
		},
		{
			name: "omitted keys stay omitted",
			policy: UsagePolicySettings{
				ConstrainedBelow: usagePolicyIntPtr(50),
				Ladder:           map[string]UsageLadderSettings{"claude": {Cheap: usagePolicyStrPtr("haiku")}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writeUsagePolicyConfig(t, "default_tool = \"claude\"\n")

			cfg, err := LoadUserConfig()
			if err != nil {
				t.Fatalf("LoadUserConfig() error = %v", err)
			}
			cfg.Usage.Policy = tc.policy

			if err := SaveUserConfig(cfg); err != nil {
				t.Fatalf("SaveUserConfig() error = %v", err)
			}
			ClearUserConfigCache()

			reloaded, err := LoadUserConfig()
			if err != nil {
				t.Fatalf("LoadUserConfig() after save error = %v", err)
			}
			if !reflect.DeepEqual(reloaded.Usage.Policy, tc.policy) {
				t.Fatalf("Usage.Policy after save+reload = %#v, want %#v", reloaded.Usage.Policy, tc.policy)
			}
		})
	}
}

// The loader's threshold range is INCLUSIVE at both ends: 0 and 100 are legal
// values, and only < 0 or > 100 is rejected. The 0 end is pinned by
// TestLoadUserConfig_AcceptsZeroUsagePolicyThresholds above; this pins the 100
// end, which is otherwise invisible — loosening validateUsagePolicyThreshold's
// `*value > 100` to `>= 100` leaves every other loader test green.
// usage.TestPolicyFromConfig_AcceptsThresholdBoundaries pins the same boundary
// on the merge side.
func TestLoadUserConfig_AcceptsUsagePolicyThresholdsAtOneHundred(t *testing.T) {
	writeUsagePolicyConfig(t, "[usage.policy]\nexhausted_below = 100\nconstrained_below = 100\n")

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	got := cfg.Usage.Policy
	if got.ExhaustedBelow == nil || *got.ExhaustedBelow != 100 {
		t.Errorf("ExhaustedBelow = %v, want an explicit 100", got.ExhaustedBelow)
	}
	if got.ConstrainedBelow == nil || *got.ConstrainedBelow != 100 {
		t.Errorf("ConstrainedBelow = %v, want an explicit 100", got.ConstrainedBelow)
	}
}

// One past the top end is still rejected, so the guard cannot be deleted
// outright.
func TestLoadUserConfig_RejectsUsagePolicyThresholdAtOneHundredAndOne(t *testing.T) {
	writeUsagePolicyConfig(t, "[usage.policy]\nconstrained_below = 101\n")

	_, err := LoadUserConfig()
	want := "invalid [usage.policy].constrained_below 101: must be between 0 and 100"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("LoadUserConfig() error = %v, want it to contain %q", err, want)
	}
}
