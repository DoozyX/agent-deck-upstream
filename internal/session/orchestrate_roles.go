package session

import (
	"fmt"
	"strings"
)

// OrchestrateRole describes the amount of model judgment a child needs.
type OrchestrateRole string

const (
	OrchestrateRoleDeterministic OrchestrateRole = "deterministic"
	OrchestrateRoleRouting       OrchestrateRole = "routing"
	OrchestrateRoleRoutine       OrchestrateRole = "routine"
	OrchestrateRoleArchitecture  OrchestrateRole = "architecture"
)

// OrchestrateLaunchExplicit contains choices made by a user, group, or task.
// Empty fields intentionally permit a role default to fill the gap.
type OrchestrateLaunchExplicit struct {
	Provider            string
	Model               string
	Effort              string
	UserSelectedModel   bool
	GroupPath           string
	Browser             bool
	MCPs                []string
	MCPConfig           bool
	JustifiedEscalation bool
}

// OrchestrateToolLoadout is connector-neutral launch data. Claude's strict
// empty MCP mode is deliberately never emitted for Codex.
type OrchestrateToolLoadout struct {
	Kind           string   `json:"kind"`
	Browser        bool     `json:"browser,omitempty"`
	StrictEmptyMCP bool     `json:"strict_empty_mcp,omitempty"`
	MCPs           []string `json:"mcps,omitempty"`
}

// ResolvedLaunch is a sanitized, durable receipt of a role resolution.
type ResolvedLaunch struct {
	Role             OrchestrateRole        `json:"role"`
	Provider         string                 `json:"provider"`
	Model            string                 `json:"model,omitempty"`
	Effort           string                 `json:"effort,omitempty"`
	ToolLoadout      OrchestrateToolLoadout `json:"tool_loadout"`
	ResolutionSource string                 `json:"resolution_source"`
	ProviderSource   string                 `json:"provider_source"`
	ModelSource      string                 `json:"model_source"`
	EffortSource     string                 `json:"effort_source"`
	LoadoutSource    string                 `json:"loadout_source"`
	Parked           bool                   `json:"parked,omitempty"`
	ParkedReason     string                 `json:"parked_reason,omitempty"`
}

func (r ResolvedLaunch) parked(reason string) (ResolvedLaunch, error) {
	// Never persist arbitrary rejected settings: callers can mistakenly pass a
	// credential as a model/effort value and receipts are a status surface.
	r.Model, r.Effort = "", ""
	r.ModelSource, r.EffortSource = "", ""
	r.Parked = true
	r.ParkedReason = reason
	return r, fmt.Errorf("orchestrate launch parked: %s", reason)
}

// ResolveOrchestrateLaunch resolves a child launch without mutating a session.
// Explicit settings and configured group/global defaults always win; role
// defaults only fill the remaining empty fields.
func ResolveOrchestrateLaunch(role OrchestrateRole, tool string, explicit OrchestrateLaunchExplicit, cfg *UserConfig) (ResolvedLaunch, error) {
	role = OrchestrateRole(strings.TrimSpace(string(role)))
	provider := strings.TrimSpace(explicit.Provider)
	if provider == "" {
		provider = strings.TrimSpace(tool)
	}
	if role == OrchestrateRoleDeterministic {
		return ResolvedLaunch{Role: role, Provider: "process", ToolLoadout: OrchestrateToolLoadout{Kind: "process"}, ResolutionSource: "deterministic", ProviderSource: "role:deterministic", LoadoutSource: "role:deterministic"}, nil
	}
	if !validOrchestrateRole(role) {
		return (ResolvedLaunch{Role: role}).parked("unknown role")
	}
	if provider != "codex" && provider != "claude" {
		return (ResolvedLaunch{Role: role, Provider: provider}).parked("unsupported provider " + provider)
	}

	result := ResolvedLaunch{Role: role, Provider: provider, ToolLoadout: orchestrateLoadout(provider, explicit), ProviderSource: "tool", LoadoutSource: "role:" + string(role)}
	if explicit.Provider != "" {
		result.ProviderSource = "explicit"
	}
	if explicit.Browser || explicit.MCPConfig || len(explicit.MCPs) > 0 {
		result.LoadoutSource = "explicit"
	}
	if provider == "codex" && explicit.Browser {
		return result.parked("browser loadout unsupported by codex connector")
	}
	result.Model, result.Effort = strings.TrimSpace(explicit.Model), strings.TrimSpace(explicit.Effort)
	if result.Model != "" {
		result.ModelSource = "explicit"
	}
	if result.Effort != "" {
		result.EffortSource = "explicit"
	}
	if result.Model != "" || result.Effort != "" || strings.TrimSpace(explicit.Provider) != "" {
		result.ResolutionSource = "explicit"
	}

	if cfg != nil {
		if provider == "codex" {
			if model, group := cfg.findGroupCodexSetting(strings.TrimSpace(explicit.GroupPath), func(s GroupCodexSettings) string { return strings.TrimSpace(s.Model) }); result.Model == "" && model != "" {
				result.Model, result.ModelSource = model, "group:"+group
			}
			if effort, group := cfg.findGroupCodexSetting(strings.TrimSpace(explicit.GroupPath), func(s GroupCodexSettings) string { return strings.TrimSpace(s.ReasoningEffort) }); result.Effort == "" && effort != "" {
				result.Effort, result.EffortSource = effort, "group:"+group
			}
			if result.Model == "" && strings.TrimSpace(cfg.Codex.DefaultModel) != "" {
				result.Model, result.ModelSource = strings.TrimSpace(cfg.Codex.DefaultModel), "config:codex.default_model"
			}
			if result.Effort == "" && strings.TrimSpace(cfg.Codex.DefaultReasoningEffort) != "" {
				result.Effort, result.EffortSource = strings.TrimSpace(cfg.Codex.DefaultReasoningEffort), "config:codex.default_reasoning_effort"
			}
		} else {
			if result.Model == "" {
				if model, group := cfg.findGroupClaudeSetting(strings.TrimSpace(explicit.GroupPath), func(s GroupClaudeSettings) string { return strings.TrimSpace(s.Model) }); model != "" {
					result.Model, result.ModelSource = model, "group:"+group
				}
			}
			if result.Model == "" && strings.TrimSpace(cfg.Claude.DefaultModel) != "" {
				result.Model, result.ModelSource = strings.TrimSpace(cfg.Claude.DefaultModel), "config:claude.default_model"
			}
		}
	}

	defaults := roleDefaults(cfg, role)
	if provider == "codex" {
		if result.Model == "" {
			result.Model = defaults.CodexModel
			result.ModelSource = roleDefaultSource(cfg, role, provider, "model")
		}
		if result.Effort == "" {
			result.Effort = defaults.CodexEffort
			result.EffortSource = roleDefaultSource(cfg, role, provider, "effort")
		}
	} else {
		if result.Model == "" {
			result.Model = defaults.ClaudeModel
			result.ModelSource = roleDefaultSource(cfg, role, provider, "model")
		}
		if result.Effort == "" {
			result.Effort = defaults.ClaudeEffort
			result.EffortSource = roleDefaultSource(cfg, role, provider, "effort")
		}
	}
	result.ResolutionSource = result.ModelSource
	if result.ResolutionSource == "" {
		result.ResolutionSource = result.EffortSource
	}
	if !supportedOrchestrateChoice(role, provider, result.Model, result.Effort, result.ModelSource, explicit.UserSelectedModel, explicit.JustifiedEscalation) {
		return result.parked("unsupported " + provider + " model or effort")
	}
	return result, nil
}

func validOrchestrateRole(role OrchestrateRole) bool {
	return role == OrchestrateRoleRouting || role == OrchestrateRoleRoutine || role == OrchestrateRoleArchitecture
}

func orchestrateLoadout(provider string, explicit OrchestrateLaunchExplicit) OrchestrateToolLoadout {
	mcps := append([]string(nil), explicit.MCPs...)
	if provider == "claude" {
		if explicit.Browser {
			return OrchestrateToolLoadout{Kind: "claude-browser", Browser: true, MCPs: mcps}
		}
		if explicit.MCPConfig || len(mcps) > 0 {
			return OrchestrateToolLoadout{Kind: "claude-task-mcp", MCPs: mcps}
		}
		return OrchestrateToolLoadout{Kind: "claude-strict-empty-mcp", StrictEmptyMCP: true}
	}
	return OrchestrateToolLoadout{Kind: "codex", MCPs: mcps}
}

func roleDefaults(cfg *UserConfig, role OrchestrateRole) OrchestrateRoleDefault {
	base := builtInRoleDefaults(role)
	if cfg != nil {
		var override OrchestrateRoleDefault
		switch role {
		case OrchestrateRoleRouting:
			override = cfg.Orchestrate.Routing
		case OrchestrateRoleRoutine:
			override = cfg.Orchestrate.Routine
		case OrchestrateRoleArchitecture:
			override = cfg.Orchestrate.Architecture
		}
		if override.hasValues() {
			if override.CodexModel != "" {
				base.CodexModel = override.CodexModel
			}
			if override.CodexEffort != "" {
				base.CodexEffort = override.CodexEffort
			}
			if override.ClaudeModel != "" {
				base.ClaudeModel = override.ClaudeModel
			}
			if override.ClaudeEffort != "" {
				base.ClaudeEffort = override.ClaudeEffort
			}
		}
	}
	return base
}

func builtInRoleDefaults(role OrchestrateRole) OrchestrateRoleDefault {
	switch role {
	case OrchestrateRoleRouting:
		return OrchestrateRoleDefault{CodexModel: "gpt-5.6-luna", CodexEffort: "low", ClaudeModel: "haiku"}
	case OrchestrateRoleArchitecture:
		return OrchestrateRoleDefault{CodexModel: "gpt-5.6-sol", CodexEffort: "high", ClaudeModel: "opus", ClaudeEffort: "medium"}
	default:
		return OrchestrateRoleDefault{CodexModel: "gpt-5.6-terra", CodexEffort: "medium", ClaudeModel: "sonnet", ClaudeEffort: "medium"}
	}
}

func roleDefaultSource(cfg *UserConfig, role OrchestrateRole, provider, field string) string {
	if cfg != nil {
		var d OrchestrateRoleDefault
		switch role {
		case OrchestrateRoleRouting:
			d = cfg.Orchestrate.Routing
		case OrchestrateRoleRoutine:
			d = cfg.Orchestrate.Routine
		case OrchestrateRoleArchitecture:
			d = cfg.Orchestrate.Architecture
		}
		configured := false
		switch provider + ":" + field {
		case "codex:model":
			configured = strings.TrimSpace(d.CodexModel) != ""
		case "codex:effort":
			configured = strings.TrimSpace(d.CodexEffort) != ""
		case "claude:model":
			configured = strings.TrimSpace(d.ClaudeModel) != ""
		case "claude:effort":
			configured = strings.TrimSpace(d.ClaudeEffort) != ""
		}
		if configured {
			return "config:orchestrate." + string(role)
		}
	}
	return "builtin:role:" + string(role)
}

func (d OrchestrateRoleDefault) hasValues() bool {
	return d.CodexModel != "" || d.CodexEffort != "" || d.ClaudeModel != "" || d.ClaudeEffort != ""
}

func supportedOrchestrateChoice(role OrchestrateRole, provider, model, effort, modelSource string, userSelected, justified bool) bool {
	if !supportsOrchestrateModelEffort(provider, model, effort) {
		return false
	}
	expensive := (provider == "codex" && model == "gpt-6-astra") || (provider == "claude" && (model == "opus" || strings.HasPrefix(model, "claude-opus-")))
	if !expensive || justified || userSelected {
		return true
	}
	if strings.HasPrefix(modelSource, "group:") || strings.HasPrefix(modelSource, "config:") {
		return true
	}
	return role == OrchestrateRoleArchitecture && modelSource == "builtin:role:architecture"
}

func validateOrchestrateRoleDefaults(cfg *UserConfig) error {
	return nil
}

// ApplyResolvedOrchestrateLaunch persists a resolution at a child launch
// boundary. It never alters an existing session unless the caller explicitly
// invokes it, which keeps restarts and legacy sessions unchanged.
func (i *Instance) ApplyResolvedOrchestrateLaunch(role OrchestrateRole, explicit OrchestrateLaunchExplicit, cfg *UserConfig) (ResolvedLaunch, error) {
	if i == nil {
		return ResolvedLaunch{}, fmt.Errorf("cannot resolve a nil instance")
	}
	if hasMCPExtraArgs(i.ExtraArgs) {
		explicit.MCPConfig = true
	}
	resolved, err := ResolveOrchestrateLaunch(role, i.Tool, explicit, cfg)
	if err != nil || resolved.Parked {
		i.OrchestrateLaunch = &resolved
		return resolved, err
	}
	if resolved.Provider == "process" {
		i.OrchestrateLaunch = &resolved
		return resolved, fmt.Errorf("deterministic role must run as a process, not an agent session")
	}
	if (resolved.Provider == "codex" && !IsCodexCompatible(i.Tool)) || (resolved.Provider == "claude" && !IsClaudeCompatible(i.Tool)) {
		resolved, err = resolved.parked("explicit provider does not match session connector")
		i.OrchestrateLaunch = &resolved
		return resolved, err
	}
	switch resolved.Provider {
	case "codex":
		opts := i.GetCodexOptions()
		if opts == nil {
			opts = NewCodexOptions(cfg)
		}
		opts.Model, opts.ReasoningEffort = resolved.Model, resolved.Effort
		err = i.SetCodexOptions(opts)
	case "claude":
		opts := i.GetClaudeOptions()
		if opts == nil {
			opts = NewClaudeOptions(cfg)
		}
		opts.Model, opts.Effort = resolved.Model, resolved.Effort
		opts.UseChrome = resolved.ToolLoadout.Browser
		if resolved.ToolLoadout.StrictEmptyMCP && !hasMCPExtraArgs(i.ExtraArgs) {
			i.ExtraArgs = append(i.ExtraArgs, "--strict-mcp-config", "--mcp-config", "{}")
		}
		err = i.SetClaudeOptions(opts)
	}
	if err == nil {
		i.OrchestrateLaunch = &resolved
	}
	return resolved, err
}

func hasMCPExtraArgs(args []string) bool {
	for _, arg := range args {
		if arg == "--mcp-config" || strings.HasPrefix(arg, "--mcp-config=") || arg == "--strict-mcp-config" {
			return true
		}
	}
	return false
}
