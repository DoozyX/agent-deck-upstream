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
	GroupPath           string
	Browser             bool
	JustifiedEscalation bool
}

// OrchestrateToolLoadout is connector-neutral launch data. Claude's strict
// empty MCP mode is deliberately never emitted for Codex.
type OrchestrateToolLoadout struct {
	Kind           string `json:"kind"`
	Browser        bool   `json:"browser,omitempty"`
	StrictEmptyMCP bool   `json:"strict_empty_mcp,omitempty"`
}

// ResolvedLaunch is a sanitized, durable receipt of a role resolution.
type ResolvedLaunch struct {
	Role             OrchestrateRole        `json:"role"`
	Provider         string                 `json:"provider"`
	Model            string                 `json:"model,omitempty"`
	Effort           string                 `json:"effort,omitempty"`
	ToolLoadout      OrchestrateToolLoadout `json:"tool_loadout"`
	ResolutionSource string                 `json:"resolution_source"`
	Parked           bool                   `json:"parked,omitempty"`
	ParkedReason     string                 `json:"parked_reason,omitempty"`
}

func (r ResolvedLaunch) parked(reason string) (ResolvedLaunch, error) {
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
		return ResolvedLaunch{Role: role, Provider: "process", ToolLoadout: OrchestrateToolLoadout{Kind: "process"}, ResolutionSource: "deterministic"}, nil
	}
	if !validOrchestrateRole(role) {
		return (ResolvedLaunch{Role: role}).parked("unknown role")
	}
	if provider != "codex" && provider != "claude" {
		return (ResolvedLaunch{Role: role, Provider: provider}).parked("unsupported provider " + provider)
	}

	result := ResolvedLaunch{Role: role, Provider: provider, ToolLoadout: orchestrateLoadout(provider, explicit.Browser)}
	result.Model, result.Effort = strings.TrimSpace(explicit.Model), strings.TrimSpace(explicit.Effort)
	if result.Model != "" || result.Effort != "" || strings.TrimSpace(explicit.Provider) != "" {
		result.ResolutionSource = "explicit"
	}

	if cfg != nil {
		if provider == "codex" {
			if model := cfg.GetGroupCodexModel(explicit.GroupPath); result.Model == "" && model != "" {
				result.Model, result.ResolutionSource = model, "group:"+explicit.GroupPath
			}
			if effort := cfg.GetGroupCodexReasoningEffort(explicit.GroupPath); result.Effort == "" && effort != "" {
				result.Effort, result.ResolutionSource = effort, "group:"+explicit.GroupPath
			}
			if result.Model == "" && strings.TrimSpace(cfg.Codex.DefaultModel) != "" {
				result.Model, result.ResolutionSource = strings.TrimSpace(cfg.Codex.DefaultModel), "config:codex.default_model"
			}
			if result.Effort == "" && strings.TrimSpace(cfg.Codex.DefaultReasoningEffort) != "" {
				result.Effort, result.ResolutionSource = strings.TrimSpace(cfg.Codex.DefaultReasoningEffort), "config:codex.default_reasoning_effort"
			}
		} else if result.Model == "" && strings.TrimSpace(cfg.Claude.DefaultModel) != "" {
			result.Model, result.ResolutionSource = strings.TrimSpace(cfg.Claude.DefaultModel), "config:claude.default_model"
		}
	}

	defaults := roleDefaults(cfg, role)
	if provider == "codex" {
		if result.Model == "" {
			result.Model = defaults.CodexModel
		}
		if result.Effort == "" {
			result.Effort = defaults.CodexEffort
		}
	} else {
		if result.Model == "" {
			result.Model = defaults.ClaudeModel
		}
		if result.Effort == "" {
			result.Effort = defaults.ClaudeEffort
		}
	}
	if result.ResolutionSource == "" {
		result.ResolutionSource = "role:" + string(role)
	}
	if !supportedOrchestrateChoice(provider, result.Model, result.Effort, explicit.JustifiedEscalation) {
		return result.parked("unsupported " + provider + " model or effort")
	}
	return result, nil
}

func validOrchestrateRole(role OrchestrateRole) bool {
	return role == OrchestrateRoleRouting || role == OrchestrateRoleRoutine || role == OrchestrateRoleArchitecture
}

func orchestrateLoadout(provider string, browser bool) OrchestrateToolLoadout {
	if provider == "claude" {
		if browser {
			return OrchestrateToolLoadout{Kind: "claude-browser", Browser: true}
		}
		return OrchestrateToolLoadout{Kind: "claude-strict-empty-mcp", StrictEmptyMCP: true}
	}
	return OrchestrateToolLoadout{Kind: "codex"}
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
		return OrchestrateRoleDefault{CodexModel: "gpt-5.6-sol", CodexEffort: "high", ClaudeModel: "sonnet", ClaudeEffort: "medium"}
	default:
		return OrchestrateRoleDefault{CodexModel: "gpt-5.6-terra", CodexEffort: "medium", ClaudeModel: "sonnet", ClaudeEffort: "medium"}
	}
}

func (d OrchestrateRoleDefault) hasValues() bool {
	return d.CodexModel != "" || d.CodexEffort != "" || d.ClaudeModel != "" || d.ClaudeEffort != ""
}

func supportedOrchestrateChoice(provider, model, effort string, justified bool) bool {
	if provider == "codex" {
		if model == "gpt-6-astra" && !justified {
			return false
		}
		if model != "gpt-5.6-luna" && model != "gpt-5.6-terra" && model != "gpt-5.6-sol" && model != "gpt-6-astra" {
			return false
		}
		return effort == "low" || effort == "medium" || effort == "high" || effort == "xhigh"
	}
	if model == "opus" && !justified {
		return false
	}
	return (model == "haiku" || model == "sonnet" || model == "opus") && (effort == "" || effort == "low" || effort == "medium" || effort == "high")
}

func validateOrchestrateRoleDefaults(cfg *UserConfig) error {
	if cfg == nil {
		return nil
	}
	for _, role := range []OrchestrateRole{OrchestrateRoleRouting, OrchestrateRoleRoutine, OrchestrateRoleArchitecture} {
		d := roleDefaults(cfg, role)
		if !supportedOrchestrateChoice("codex", d.CodexModel, d.CodexEffort, false) {
			return fmt.Errorf("invalid [orchestrate.%s] Codex model or effort", role)
		}
		if !supportedOrchestrateChoice("claude", d.ClaudeModel, d.ClaudeEffort, false) {
			return fmt.Errorf("invalid [orchestrate.%s] Claude model or effort", role)
		}
	}
	return nil
}

// ApplyResolvedOrchestrateLaunch persists a resolution at a child launch
// boundary. It never alters an existing session unless the caller explicitly
// invokes it, which keeps restarts and legacy sessions unchanged.
func (i *Instance) ApplyResolvedOrchestrateLaunch(role OrchestrateRole, explicit OrchestrateLaunchExplicit, cfg *UserConfig) (ResolvedLaunch, error) {
	if i == nil {
		return ResolvedLaunch{}, fmt.Errorf("cannot resolve a nil instance")
	}
	resolved, err := ResolveOrchestrateLaunch(role, i.Tool, explicit, cfg)
	i.OrchestrateLaunch = &resolved
	if err != nil || resolved.Parked || resolved.Provider == "process" {
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
		err = i.SetClaudeOptions(opts)
	}
	return resolved, err
}
