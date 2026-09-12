package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveOrchestrateLaunch_PrecedenceAndRoleDefaults(t *testing.T) {
	tests := []struct {
		name     string
		role     OrchestrateRole
		tool     string
		explicit OrchestrateLaunchExplicit
		want     ResolvedLaunch
	}{
		{
			name:     "explicit session settings win",
			role:     OrchestrateRoleRoutine,
			tool:     "codex",
			explicit: OrchestrateLaunchExplicit{Model: "gpt-5.6-sol", Effort: "high"},
			want:     ResolvedLaunch{Role: OrchestrateRoleRoutine, Provider: "codex", Model: "gpt-5.6-sol", Effort: "high", ResolutionSource: "explicit", ToolLoadout: OrchestrateToolLoadout{Kind: "codex"}},
		},
		{
			name: "routine codex is economical",
			role: OrchestrateRoleRoutine,
			tool: "codex",
			want: ResolvedLaunch{Role: OrchestrateRoleRoutine, Provider: "codex", Model: "gpt-5.6-terra", Effort: "medium", ResolutionSource: "role:routine", ToolLoadout: OrchestrateToolLoadout{Kind: "codex"}},
		},
		{
			name: "routing claude is haiku",
			role: OrchestrateRoleRouting,
			tool: "claude",
			want: ResolvedLaunch{Role: OrchestrateRoleRouting, Provider: "claude", Model: "haiku", ResolutionSource: "role:routing", ToolLoadout: OrchestrateToolLoadout{Kind: "claude-strict-empty-mcp", StrictEmptyMCP: true}},
		},
		{
			name: "architecture uses sol not astra",
			role: OrchestrateRoleArchitecture,
			tool: "codex",
			want: ResolvedLaunch{Role: OrchestrateRoleArchitecture, Provider: "codex", Model: "gpt-5.6-sol", Effort: "high", ResolutionSource: "role:architecture", ToolLoadout: OrchestrateToolLoadout{Kind: "codex"}},
		},
		{
			name:     "browser claude retains browser tools",
			role:     OrchestrateRoleRoutine,
			tool:     "claude",
			explicit: OrchestrateLaunchExplicit{Browser: true},
			want:     ResolvedLaunch{Role: OrchestrateRoleRoutine, Provider: "claude", Model: "sonnet", Effort: "medium", ResolutionSource: "role:routine", ToolLoadout: OrchestrateToolLoadout{Kind: "claude-browser", Browser: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveOrchestrateLaunch(tt.role, tt.tool, tt.explicit, &UserConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Role != tt.want.Role || got.Provider != tt.want.Provider || got.Model != tt.want.Model || got.Effort != tt.want.Effort || !reflect.DeepEqual(got.ToolLoadout, tt.want.ToolLoadout) {
				t.Fatalf("ResolveOrchestrateLaunch() = %#v, want core %#v", got, tt.want)
			}
		})
	}
}

func TestResolveOrchestrateLaunch_DeterministicAndUnsupportedPark(t *testing.T) {
	got, err := ResolveOrchestrateLaunch(OrchestrateRoleDeterministic, "shell", OrchestrateLaunchExplicit{}, &UserConfig{})
	if err != nil || got.Provider != "process" || got.Model != "" || got.Parked {
		t.Fatalf("deterministic = %#v, %v; want process without model", got, err)
	}

	got, err = ResolveOrchestrateLaunch(OrchestrateRoleRoutine, "codex", OrchestrateLaunchExplicit{Model: "not-a-model"}, &UserConfig{})
	if err == nil || !got.Parked || got.ParkedReason == "" {
		t.Fatalf("unsupported model = %#v, %v; want visible parked error", got, err)
	}
}

func TestResolveOrchestrateLaunch_UsesConfiguredRoleDefaults(t *testing.T) {
	cfg := &UserConfig{Orchestrate: OrchestrateSettings{Routine: OrchestrateRoleDefault{CodexModel: "gpt-5.6-sol", CodexEffort: "high"}}}
	got, err := ResolveOrchestrateLaunch(OrchestrateRoleRoutine, "codex", OrchestrateLaunchExplicit{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-5.6-sol" || got.Effort != "high" || got.ResolutionSource != "config:orchestrate.routine" {
		t.Fatalf("configured role default = %#v", got)
	}
}

func TestInstanceApplyResolvedOrchestrateLaunch_PersistsSanitizedReceipt(t *testing.T) {
	inst := NewInstanceWithTool("role-receipt", t.TempDir(), "codex")
	resolved, err := inst.ApplyResolvedOrchestrateLaunch(OrchestrateRoleRoutine, OrchestrateLaunchExplicit{}, &UserConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if inst.OrchestrateLaunch == nil || inst.OrchestrateLaunch.Model != "gpt-5.6-terra" || inst.LaunchModelID() != "gpt-5.6-terra" {
		t.Fatalf("instance did not retain resolved receipt: %#v, model=%q", inst.OrchestrateLaunch, inst.LaunchModelID())
	}
	if resolved.Parked || resolved.Provider != "codex" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestResolveOrchestrateLaunch_UsesConnectorModelAndEffortCapabilities(t *testing.T) {
	accepted := []struct {
		name      string
		provider  string
		model     string
		effort    string
		justified bool
	}{
		{name: "codex sol upper boundary", provider: "codex", model: "gpt-5.6-sol", effort: "ultra"},
		{name: "codex luna upper boundary", provider: "codex", model: "gpt-5.6-luna", effort: "max"},
		{name: "codex 5.5 upper boundary", provider: "codex", model: "gpt-5.5", effort: "xhigh"},
		{name: "codex justified astra", provider: "codex", model: "gpt-6-astra", effort: "ultra", justified: true},
		{name: "claude current catalog id", provider: "claude", model: "claude-fable-5-1", effort: "max"},
		{name: "claude documented alias", provider: "claude", model: "fable", effort: "medium"},
		{name: "claude alias", provider: "claude", model: "sonnet", effort: "low"},
		{name: "claude justified opus", provider: "claude", model: "opus", effort: "medium", justified: true},
	}
	for _, tt := range accepted {
		t.Run("accepts "+tt.name, func(t *testing.T) {
			got, err := ResolveOrchestrateLaunch(OrchestrateRoleRoutine, tt.provider, OrchestrateLaunchExplicit{
				Model: tt.model, Effort: tt.effort, JustifiedEscalation: tt.justified,
			}, &UserConfig{})
			if err != nil || got.Parked {
				t.Fatalf("supported choice parked: got=%#v err=%v", got, err)
			}
		})
	}

	rejected := []struct {
		name      string
		provider  string
		model     string
		effort    string
		justified bool
	}{
		{name: "codex model outside catalog", provider: "codex", model: "gpt-5.6-nebula", effort: "medium"},
		{name: "codex 5.5 effort below boundary", provider: "codex", model: "gpt-5.5", effort: "minimal"},
		{name: "codex 5.5 effort above boundary", provider: "codex", model: "gpt-5.5", effort: "max"},
		{name: "codex luna effort above boundary", provider: "codex", model: "gpt-5.6-luna", effort: "ultra"},
		{name: "codex identifier absent from bundled catalog", provider: "codex", model: "gpt-5.5-pro", effort: "xhigh"},
		{name: "claude invented prefixed model", provider: "claude", model: "claude-sonnet-9", effort: "medium"},
		{name: "claude garbage effort", provider: "claude", model: "sonnet", effort: "garbage"},
		{name: "unjustified astra", provider: "codex", model: "gpt-6-astra", effort: "high"},
		{name: "unjustified opus id", provider: "claude", model: "claude-opus-5", effort: "medium"},
	}
	for _, tt := range rejected {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			got, err := ResolveOrchestrateLaunch(OrchestrateRoleRoutine, tt.provider, OrchestrateLaunchExplicit{
				Model: tt.model, Effort: tt.effort, JustifiedEscalation: tt.justified,
			}, &UserConfig{})
			if err == nil || !got.Parked {
				t.Fatalf("unsupported choice accepted: got=%#v err=%v", got, err)
			}
		})
	}
}

func TestResolveOrchestrateLaunch_ExpensiveModelSelectionOrigins(t *testing.T) {
	tests := []struct {
		name       string
		role       OrchestrateRole
		tool       string
		explicit   OrchestrateLaunchExplicit
		cfg        *UserConfig
		wantErr    bool
		wantModel  string
		wantSource string
	}{
		{name: "approved architecture builtin opus", role: OrchestrateRoleArchitecture, tool: "claude", cfg: &UserConfig{}, wantModel: "opus", wantSource: "builtin:role:architecture"},
		{name: "group opus is a user choice", role: OrchestrateRoleRoutine, tool: "claude", explicit: OrchestrateLaunchExplicit{GroupPath: "work/child"}, cfg: &UserConfig{Groups: map[string]GroupSettings{"work": {Claude: GroupClaudeSettings{Model: "opus"}}}}, wantModel: "opus", wantSource: "group:work"},
		{name: "root opus is a user choice", role: OrchestrateRoleRoutine, tool: "claude", cfg: &UserConfig{Claude: ClaudeSettings{DefaultModel: "opus"}}, wantModel: "opus", wantSource: "config:claude.default_model"},
		{name: "unjustified automatic astra", role: OrchestrateRoleRoutine, tool: "codex", explicit: OrchestrateLaunchExplicit{Model: "gpt-6-astra", Effort: "high"}, cfg: &UserConfig{}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveOrchestrateLaunch(tt.role, tt.tool, tt.explicit, tt.cfg)
			if tt.wantErr {
				if err == nil || !got.Parked {
					t.Fatalf("automatic expensive selection = %#v, %v; want parked", got, err)
				}
				return
			}
			if err != nil || got.Parked || got.Model != tt.wantModel || got.ModelSource != tt.wantSource {
				t.Fatalf("preserved expensive selection = %#v, %v; want model=%q source=%q", got, err, tt.wantModel, tt.wantSource)
			}
		})
	}
}

func TestResolveOrchestrateLaunch_ReportsPerFieldRoleDefaultProvenance(t *testing.T) {
	cfg := &UserConfig{Orchestrate: OrchestrateSettings{Routine: OrchestrateRoleDefault{
		CodexModel:   "gpt-5.5",
		ClaudeEffort: "high",
	}}}

	codex, err := ResolveOrchestrateLaunch(OrchestrateRoleRoutine, "codex", OrchestrateLaunchExplicit{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if codex.ModelSource != "config:orchestrate.routine" || codex.EffortSource != "builtin:role:routine" {
		t.Fatalf("codex provenance = model:%q effort:%q", codex.ModelSource, codex.EffortSource)
	}

	claude, err := ResolveOrchestrateLaunch(OrchestrateRoleRoutine, "claude", OrchestrateLaunchExplicit{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if claude.ModelSource != "builtin:role:routine" || claude.EffortSource != "config:orchestrate.routine" {
		t.Fatalf("claude provenance = model:%q effort:%q", claude.ModelSource, claude.EffortSource)
	}
}

func TestInstanceApplyResolvedOrchestrateLaunch_PreservesExplicitMCPBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		extra    []string
		explicit OrchestrateLaunchExplicit
	}{
		{name: "named task MCP", explicit: OrchestrateLaunchExplicit{MCPs: []string{"memory"}}},
		{name: "split MCP config", extra: []string{"--mcp-config", "/tmp/task.json"}},
		{name: "equals MCP config", extra: []string{"--mcp-config=/tmp/task.json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := NewInstanceWithTool("mcp-boundary", t.TempDir(), "claude")
			inst.ExtraArgs = append([]string(nil), tt.extra...)
			got, err := inst.ApplyResolvedOrchestrateLaunch(OrchestrateRoleRoutine, tt.explicit, &UserConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if got.ToolLoadout.StrictEmptyMCP || strings.Contains(strings.Join(inst.ExtraArgs, " "), "--strict-mcp-config") {
				t.Fatalf("explicit MCP boundary was disabled: receipt=%#v args=%v", got.ToolLoadout, inst.ExtraArgs)
			}
			if len(tt.explicit.MCPs) > 0 && (len(got.ToolLoadout.MCPs) != 1 || got.ToolLoadout.MCPs[0] != "memory") {
				t.Fatalf("named MCP missing from receipt: %#v", got.ToolLoadout)
			}
		})
	}
}

func TestInstanceApplyResolvedOrchestrateLaunch_ExplicitExtraArgsStayTruthfulOnFreshAndRestartCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	tests := []struct {
		name   string
		extra  []string
		model  string
		effort string
	}{
		{name: "split", extra: []string{"--model", "claude-sonnet-4-6", "--effort", "high"}, model: "claude-sonnet-4-6", effort: "high"},
		{name: "equals", extra: []string{"--model=claude-sonnet-4-6", "--effort=high"}, model: "claude-sonnet-4-6", effort: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := NewInstanceWithTool("explicit-command", t.TempDir(), "claude")
			inst.Command = "claude"
			inst.ExtraArgs = append([]string(nil), tt.extra...)
			got, err := inst.ApplyResolvedOrchestrateLaunch(OrchestrateRoleRoutine, OrchestrateLaunchExplicit{Model: tt.model, Effort: tt.effort}, &UserConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Model != tt.model || got.Effort != tt.effort || got.ModelSource != "explicit" || got.EffortSource != "explicit" {
				t.Fatalf("receipt does not describe explicit command args: %#v", got)
			}

			fresh := inst.buildClaudeCommand("claude")
			inst.ClaudeSessionID = "11111111-1111-4111-8111-111111111111"
			MarkClaudeSessionIDVerified(inst)
			restarted := inst.buildClaudeCommand("claude")
			for _, command := range []string{fresh, restarted} {
				if strings.Count(command, "--model") != 1 || strings.Count(command, "--effort") != 1 {
					t.Fatalf("command duplicated explicit model/effort flags:\n%s", command)
				}
				if !strings.Contains(command, tt.model) || !strings.Contains(command, tt.effort) {
					t.Fatalf("command lost explicit model/effort:\n%s", command)
				}
			}
		})
	}
}

func TestInstanceApplyResolvedOrchestrateLaunch_CodexExtraArgsStayTruthfulInCommandGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	tests := []struct {
		name  string
		extra []string
	}{
		{name: "split", extra: []string{"--model", "gpt-5.5", "--config", "model_reasoning_effort=minimal"}},
		{name: "equals", extra: []string{"--model=gpt-5.5", "--config=model_reasoning_effort=minimal"}},
		{name: "short and quoted TOML effort", extra: []string{"-m", "gpt-5.5", "-c", `model_reasoning_effort="xhigh"`}},
		{name: "TOML model and effort", extra: []string{"-c", `model="gpt-5.5"`, "-c", `model_reasoning_effort="xhigh"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := NewInstanceWithTool("codex-explicit-command", t.TempDir(), "codex")
			inst.Command = "codex"
			inst.ExtraArgs = append([]string(nil), tt.extra...)
			effort := "minimal"
			if strings.Contains(strings.Join(tt.extra, " "), "xhigh") {
				effort = "xhigh"
			}
			got, err := inst.ApplyResolvedOrchestrateLaunch(OrchestrateRoleRoutine, OrchestrateLaunchExplicit{Model: "gpt-5.5", Effort: effort}, &UserConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Model != "gpt-5.5" || got.Effort != effort || got.ModelSource != "explicit" || got.EffortSource != "explicit" {
				t.Fatalf("receipt does not describe explicit Codex args: %#v", got)
			}

			command := inst.buildCodexCommand("codex")
			if strings.Count(command, "gpt-5.5") != 1 || strings.Count(command, "model_reasoning_effort") != 1 {
				t.Fatalf("Codex command duplicated explicit model/effort:\n%s", command)
			}
			if !strings.Contains(command, "gpt-5.5") || !strings.Contains(command, effort) {
				t.Fatalf("Codex command lost explicit model/effort:\n%s", command)
			}
		})
	}
}

func TestInstanceApplyResolvedOrchestrateLaunch_ProviderMismatchAndDeterministicStopBeforeCommand(t *testing.T) {
	inst := NewInstanceWithTool("mismatch", t.TempDir(), "claude")
	got, err := inst.ApplyResolvedOrchestrateLaunch(OrchestrateRoleRoutine, OrchestrateLaunchExplicit{Provider: "codex"}, &UserConfig{})
	if err == nil || !got.Parked || !strings.Contains(got.ParkedReason, "does not match") {
		t.Fatalf("provider mismatch = %#v, %v", got, err)
	}

	got, err = inst.ApplyResolvedOrchestrateLaunch(OrchestrateRoleDeterministic, OrchestrateLaunchExplicit{}, &UserConfig{})
	if err == nil || got.Provider != "process" || inst.OrchestrateLaunch == nil {
		t.Fatalf("deterministic agent boundary = %#v, %v", got, err)
	}
}

func TestInstanceApplyResolvedOrchestrateLaunch_ClaudeLoadoutSurvivesFreshAndRestartCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	tests := []struct {
		name        string
		explicit    OrchestrateLaunchExplicit
		wantFlags   []string
		rejectFlags []string
	}{
		{name: "browser", explicit: OrchestrateLaunchExplicit{Browser: true}, wantFlags: []string{"--chrome"}, rejectFlags: []string{"--strict-mcp-config"}},
		{name: "strict empty MCP", wantFlags: []string{"--strict-mcp-config", "--mcp-config", "{}"}, rejectFlags: []string{"--chrome"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := NewInstanceWithTool("loadout-command", t.TempDir(), "claude")
			inst.Command = "claude"
			if _, err := inst.ApplyResolvedOrchestrateLaunch(OrchestrateRoleRoutine, tt.explicit, &UserConfig{}); err != nil {
				t.Fatal(err)
			}
			fresh := inst.buildClaudeCommand("claude")
			inst.ClaudeSessionID = "22222222-2222-4222-8222-222222222222"
			MarkClaudeSessionIDVerified(inst)
			restarted := inst.buildClaudeCommand("claude")
			for _, command := range []string{fresh, restarted} {
				for _, flag := range tt.wantFlags {
					if !strings.Contains(command, flag) {
						t.Fatalf("%s command lost %q:\n%s", tt.name, flag, command)
					}
				}
				for _, flag := range tt.rejectFlags {
					if strings.Contains(command, flag) {
						t.Fatalf("%s command unexpectedly contains %q:\n%s", tt.name, flag, command)
					}
				}
			}
		})
	}
}

func TestOrchestrateLaunchReceipt_SurvivesSQLiteLoadWithGroups(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	storage := newTestStorage(t)

	inst := NewInstanceWithTool("receipt-roundtrip", t.TempDir(), "claude")
	if _, err := inst.ApplyResolvedOrchestrateLaunch(OrchestrateRoleRoutine, OrchestrateLaunchExplicit{MCPs: []string{"memory"}}, &UserConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(loaded) != 1 || loaded[0].OrchestrateLaunch == nil {
		t.Fatalf("persisted receipt missing after LoadWithGroups: %#v", loaded)
	}
	if got := loaded[0].OrchestrateLaunch; got.Model != "sonnet" || len(got.ToolLoadout.MCPs) != 1 || got.ToolLoadout.MCPs[0] != "memory" {
		t.Fatalf("loaded receipt = %#v", got)
	}
}
