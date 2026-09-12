package session

import "testing"

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
			if got != tt.want {
				t.Fatalf("ResolveOrchestrateLaunch() = %#v, want %#v", got, tt.want)
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
	if got.Model != "gpt-5.6-sol" || got.Effort != "high" || got.ResolutionSource != "role:routine" {
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
