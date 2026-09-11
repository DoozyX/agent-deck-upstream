package session

import (
	"os"
	"path/filepath"
	"reflect"
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
