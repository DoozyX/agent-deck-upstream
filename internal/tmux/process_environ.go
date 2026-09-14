package tmux

import (
	"fmt"
	"strings"
)

func processEnvironLookup(pid int, entries []string) (func(string) string, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("empty environment for pid %d", pid)
	}
	env := make(map[string]string, len(entries))
	for _, entry := range entries {
		if key, value, found := strings.Cut(entry, "="); found {
			env[key] = value
		}
	}
	return func(key string) string { return env[key] }, nil
}
