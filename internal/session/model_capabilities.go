package session

import "slices"

// codexOrchestrateEffortsByModel mirrors the bundled model capabilities in
// codex-cli 0.153.4. Keep validation model-specific: the connector does not
// support one common effort range across all models.
var codexOrchestrateEffortsByModel = map[string][]string{
	"gpt-6-astra":              {"low", "medium", "high", "xhigh", "max", "ultra"},
	"gpt-5.6-sol":              {"low", "medium", "high", "xhigh", "max", "ultra"},
	"gpt-5.6-terra":            {"low", "medium", "high", "xhigh", "max", "ultra"},
	"gpt-5.6-luna":             {"low", "medium", "high", "xhigh", "max"},
	"gpt-5.5":                  {"low", "medium", "high", "xhigh"},
	"gpt-5.4":                  {"low", "medium", "high", "xhigh"},
	"gpt-5.4-mini":             {"low", "medium", "high", "xhigh"},
	"gpt-5.2":                  {"low", "medium", "high", "xhigh"},
	"codex-auto-review":        {"low", "medium", "high", "xhigh", "max"},
	"gpt-daybreak-blue-latest": {"low", "medium", "high", "xhigh", "max", "ultra"},
	"gpt-daybreak-red-latest":  {"low", "medium", "high", "xhigh", "max", "ultra"},
}

// KnownModelIDsForTool is the visible connector model catalog used by launch
// UI suggestions. Orchestrated Codex validation also accepts installed hidden
// models through codexOrchestrateEffortsByModel.
func KnownModelIDsForTool(tool string) []string {
	var models []string
	switch {
	case IsClaudeCompatible(tool):
		models = []string{
			"claude-opus-5",
			"claude-sonnet-5",
			"claude-fable-5-1",
			"claude-fable-5",
			"claude-sonnet-4-6",
			"claude-opus-4-8",
			"claude-opus-4-7",
			"claude-haiku-4-5",
			"claude-haiku-4-5-20251001",
		}
	case tool == "gemini":
		models = []string{
			"gemini-3.1-pro-preview",
			"gemini-3.1-pro-preview-customtools",
			"gemini-3-flash-preview",
			"gemini-3.1-flash-lite",
			"gemini-3.1-flash-lite-preview",
			"gemini-2.5-pro",
			"gemini-2.5-flash",
			"gemini-2.5-flash-lite",
		}
	case tool == "opencode":
		models = []string{
			"openai/gpt-5.5",
			"openai/gpt-5.5-pro",
			"openai/gpt-5.4",
			"openai/gpt-5.4-pro",
			"openai/gpt-5.4-mini",
			"openai/gpt-5.3-codex",
			"openai/gpt-5",
			"openai/o3",
			"anthropic/claude-opus-5",
			"anthropic/claude-sonnet-5",
			"anthropic/claude-fable-5-1",
			"anthropic/claude-fable-5",
			"anthropic/claude-sonnet-4-6",
			"anthropic/claude-opus-4-8",
			"anthropic/claude-opus-4-7",
			"anthropic/claude-haiku-4-5",
		}
	case IsCodexCompatible(tool):
		models = []string{
			"gpt-5.6-sol",
			"gpt-5.6-terra",
			"gpt-5.6-luna",
			"gpt-6-astra",
			"gpt-5.5",
			"gpt-5.2",
		}
	}
	return append([]string(nil), models...)
}

func isKnownOrchestrateModel(provider, model string) bool {
	if provider == "codex" {
		_, ok := codexOrchestrateEffortsByModel[model]
		return ok
	}
	if provider == "claude" {
		if model == "opus" || model == "sonnet" || model == "haiku" || model == "fable" {
			return true
		}
		return slices.Contains(KnownModelIDsForTool(provider), model)
	}
	return false
}

func supportsOrchestrateModelEffort(provider, model, effort string) bool {
	if provider == "codex" {
		efforts, ok := codexOrchestrateEffortsByModel[model]
		return ok && slices.Contains(efforts, effort)
	}
	return isKnownOrchestrateModel(provider, model) && ValidateLaunchReasoningEffort(provider, effort) == nil
}
