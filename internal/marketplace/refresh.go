package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const nativeRefreshTimeout = 60 * time.Second

var nativeSelectors = []string{"agent-deck@agent-deck", "agent-deck-mcp@agent-deck"}

// RefreshProfile uses the versioned native Codex install operation. It first
// inventories the profile, because plugin add would enable a disabled plugin.
func RefreshProfile(ctx context.Context, codexHome string, codexArgv []string, checkout Checkout) error {
	if !validRevision(checkout.Revision) || !supportedRuntime(codexArgv) {
		return nil
	}
	if len(codexArgv) == 0 || !filepath.IsAbs(codexHome) {
		return errors.New("marketplace: invalid native refresh request")
	}
	installed, err := nativeList(ctx, codexHome, codexArgv)
	if err != nil {
		return err
	}
	for _, selector := range nativeSelectors {
		if !installed[selector] {
			continue
		}
		if err := nativeCommand(ctx, codexHome, codexArgv, "plugin", "add", selector, "--json"); err != nil {
			return err
		}
	}
	return nil
}

func supportedRuntime(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	b := filepath.Base(argv[0])
	return b == "codex" || b == "codex.real"
}

func nativeList(ctx context.Context, home string, argv []string) (map[string]bool, error) {
	out, err := nativeOutput(ctx, home, argv, "plugin", "list", "--available", "--json")
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(out, &value); err != nil {
		return nil, errors.New("marketplace: invalid native plugin inventory")
	}
	result := map[string]bool{}
	collectEnabled(value, result)
	return result, nil
}

func collectEnabled(v any, result map[string]bool) {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			collectEnabled(item, result)
		}
	case map[string]any:
		id, _ := x["id"].(string)
		if id == "" {
			id, _ = x["selector"].(string)
		}
		enabled, ok := x["enabled"].(bool)
		if ok && enabled {
			for _, wanted := range nativeSelectors {
				if id == wanted {
					result[id] = true
				}
			}
		}
		for _, item := range x {
			collectEnabled(item, result)
		}
	}
}

func nativeCommand(ctx context.Context, home string, argv []string, args ...string) error {
	_, err := nativeOutput(ctx, home, argv, args...)
	return err
}

func nativeOutput(ctx context.Context, home string, argv []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, nativeRefreshTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], args...)...)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	out, err := cmd.Output()
	if len(out) > 64*1024 {
		return nil, errors.New("marketplace: native output exceeded limit")
	}
	if err != nil {
		return nil, errors.New("marketplace: native refresh failed")
	}
	return out, nil
}

func runtimeIdentity(argv []string) string { return strings.Join(argv, "\x00") }
