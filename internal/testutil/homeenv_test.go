package testutil_test

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestIsolateHomeCodexHome(t *testing.T) {
	for _, state := range []string{"unset", "empty", "inherited"} {
		t.Run(state, func(t *testing.T) {
			value := ""
			if state == "inherited" {
				value = t.TempDir()
			}
			t.Setenv("CODEX_HOME", value)
			if state == "unset" {
				if err := os.Unsetenv("CODEX_HOME"); err != nil {
					t.Fatal(err)
				}
			}
			before, hadBefore := os.LookupEnv("CODEX_HOME")
			cleanup := testutil.IsolateHome()
			t.Cleanup(cleanup)
			if got := os.Getenv("CODEX_HOME"); got != "" {
				t.Errorf("inherited CODEX_HOME = %q, want cleared", got)
			}
			t.Run("explicit override", func(t *testing.T) {
				override := t.TempDir()
				t.Setenv("CODEX_HOME", override)
				if got := os.Getenv("CODEX_HOME"); got != override {
					t.Errorf("CODEX_HOME = %q, want %q", got, override)
				}
			})
			cleanup()
			if got, had := os.LookupEnv("CODEX_HOME"); got != before || had != hadBefore {
				t.Errorf("cleanup restored (%q, %v), want (%q, %v)", got, had, before, hadBefore)
			}
		})
	}
}
