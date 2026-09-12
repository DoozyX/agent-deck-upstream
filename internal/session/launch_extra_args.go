package session

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// LaunchExtraArgSelections is the normalized model/effort selection carried
// by persisted connector arguments. The Set fields remain true for malformed
// recognized flags so command generation never adds a competing default.
type LaunchExtraArgSelections struct {
	Model     string
	Effort    string
	ModelSet  bool
	EffortSet bool
}

// ParseLaunchExtraArgSelections recognizes the native model and effort forms
// accepted by the installed Claude and Codex connectors.
func ParseLaunchExtraArgSelections(tool string, args []string) (LaunchExtraArgSelections, error) {
	var selected LaunchExtraArgSelections
	for idx := 0; idx < len(args); idx++ {
		rawArg := args[idx]
		arg := strings.TrimSpace(rawArg)
		nextValue := func(flag string, rejectPadding bool) (string, error) {
			if idx+1 >= len(args) || strings.HasPrefix(strings.TrimSpace(args[idx+1]), "-") {
				return "", fmt.Errorf("%s in --extra-arg requires a following value for an orchestrated launch", flag)
			}
			idx++
			return requiredLaunchExtraArgValue(flag, args[idx], rejectPadding)
		}

		switch {
		case arg == "--model" || IsCodexCompatible(tool) && arg == "-m":
			selected.ModelSet = true
			value, err := nextValue(arg, true)
			if err != nil {
				return selected, err
			}
			selected.Model = value
		case strings.HasPrefix(arg, "--model=") || IsCodexCompatible(tool) && strings.HasPrefix(arg, "-m="):
			selected.ModelSet = true
			value, err := requiredLaunchExtraArgValue("--model", rawArg[strings.IndexByte(rawArg, '=')+1:], true)
			if err != nil {
				return selected, err
			}
			selected.Model = value
		case IsClaudeCompatible(tool) && arg == "--effort":
			selected.EffortSet = true
			value, err := nextValue(arg, true)
			if err != nil {
				return selected, err
			}
			selected.Effort = value
		case IsClaudeCompatible(tool) && strings.HasPrefix(arg, "--effort="):
			selected.EffortSet = true
			value, err := requiredLaunchExtraArgValue("--effort", rawArg[strings.IndexByte(rawArg, '=')+1:], true)
			if err != nil {
				return selected, err
			}
			selected.Effort = value
		case IsCodexCompatible(tool) && (arg == "--config" || arg == "-c"):
			value, err := nextValue(arg, false)
			if err != nil {
				return selected, err
			}
			if err := applyCodexLaunchConfigSelection(&selected, value); err != nil {
				return selected, err
			}
		case IsCodexCompatible(tool) && (strings.HasPrefix(arg, "--config=") || strings.HasPrefix(arg, "-c=")):
			value, err := requiredLaunchExtraArgValue("--config", rawArg[strings.IndexByte(rawArg, '=')+1:], false)
			if err != nil {
				return selected, err
			}
			if err := applyCodexLaunchConfigSelection(&selected, value); err != nil {
				return selected, err
			}
		}
	}
	return selected, nil
}

func requiredLaunchExtraArgValue(flag, value string, rejectPadding bool) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s in --extra-arg requires a value for an orchestrated launch", flag)
	}
	if rejectPadding && trimmed != value {
		return "", fmt.Errorf("%s value in --extra-arg must not have surrounding padding", flag)
	}
	return trimmed, nil
}

func applyCodexLaunchConfigSelection(selected *LaunchExtraArgSelections, assignment string) error {
	key, raw, ok := strings.Cut(assignment, "=")
	if !ok {
		return nil
	}
	key = strings.TrimSpace(key)
	if key != "model" && key != "model_reasoning_effort" {
		return nil
	}
	if key == "model" {
		selected.ModelSet = true
	} else {
		selected.EffortSet = true
	}
	value, err := normalizeCodexLaunchConfigString(key, raw)
	if err != nil {
		return err
	}
	if key == "model" {
		selected.Model = value
	} else {
		selected.Effort = value
	}
	return nil
}

func normalizeCodexLaunchConfigString(key, raw string) (string, error) {
	trimmedRaw := strings.TrimSpace(raw)
	if trimmedRaw == "" {
		return "", fmt.Errorf("%s in --extra-arg requires a value for an orchestrated launch", key)
	}
	if trimmedRaw[0] != '\'' && trimmedRaw[0] != '"' {
		if raw != trimmedRaw {
			return "", fmt.Errorf("%s value in --extra-arg must not have surrounding padding", key)
		}
		return raw, nil
	}
	var decoded map[string]any
	if _, err := toml.Decode("value = "+trimmedRaw, &decoded); err != nil {
		return "", fmt.Errorf("%s in --extra-arg must be a valid TOML string", key)
	}
	value, ok := decoded["value"].(string)
	trimmedValue := strings.TrimSpace(value)
	if !ok || trimmedValue == "" {
		return "", fmt.Errorf("%s in --extra-arg requires a non-empty string value for an orchestrated launch", key)
	}
	if value != trimmedValue {
		return "", fmt.Errorf("%s value in --extra-arg must not have surrounding padding", key)
	}
	return value, nil
}
