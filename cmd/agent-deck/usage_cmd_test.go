package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/usage"
)

func TestUsageAccountForSessionUsesEffectiveProviderHome(t *testing.T) {
	inst := &session.Instance{Title: "Work", Tool: "claude"}
	account, err := usageAccountForSession(inst)
	if err != nil {
		t.Fatal(err)
	}
	if account.Provider != usage.Claude || account.Home == "" || account.Label != "Work" {
		t.Fatalf("account=%#v", account)
	}
}

func TestUsageAccountForSessionRejectsUnsupportedTools(t *testing.T) {
	if _, err := usageAccountForSession(&session.Instance{Tool: "gemini"}); err == nil {
		t.Fatal("expected unsupported-tool error")
	}
}

func TestConfiguredUsageAccountsPrefersLexicographicallyFirstProfileLabel(t *testing.T) {
	home := t.TempDir()
	config := &session.UserConfig{Profiles: map[string]session.ProfileSettings{
		"zeta":  {Claude: session.ProfileClaudeSettings{ConfigDir: home}},
		"alpha": {Claude: session.ProfileClaudeSettings{ConfigDir: home}},
	}}
	accounts := configuredUsageAccounts(config)
	for _, account := range accounts {
		if account.Provider == usage.Claude && account.Home == usage.CanonicalHome(home) {
			if account.Label != "alpha" {
				t.Fatalf("label = %q, want lexicographically first profile", account.Label)
			}
			return
		}
	}
	t.Fatalf("Claude account for %q missing: %#v", home, accounts)
}

func TestUsageAccountsForInstancesIncludesEffectiveProviderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	accounts := usageAccountsForInstances([]*session.Instance{{Title: "session-only", Tool: "claude"}})
	if len(accounts) != 1 || accounts[0].Home != usage.CanonicalHome(home) || accounts[0].Label != "session-only" {
		t.Fatalf("accounts = %#v", accounts)
	}
}

func runUsageHelperProcess(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	fakeDir := t.TempDir()
	fakeOpenUsage := fakeDir + "/openusage"
	if err := os.WriteFile(fakeOpenUsage, []byte("#!/bin/sh\nprintf '%s\\n' '{\"limits\":{\"weekly\":{\"remaining\":77}}}'\n"), 0o755); err != nil {
		t.Fatalf("write fake openusage: %v", err)
	}
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestUsageHelperProcess", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "AGENT_DECK_USAGE_HELPER=1", "HOME="+t.TempDir(), "PATH="+fakeDir+":"+os.Getenv("PATH"))
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err == nil {
		return out.String(), errOut.String(), 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run helper: %v", err)
	}
	return out.String(), errOut.String(), exitErr.ExitCode()
}

func TestUsageHelperProcess(t *testing.T) {
	if os.Getenv("AGENT_DECK_USAGE_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	handleUsage("", args[1:])
	os.Exit(0)
}

func TestHandleUsageAcceptsJSONAfterSessionTarget(t *testing.T) {
	_, stderr, code := runUsageHelperProcess(t, "missing-session", "--json")
	if code != 2 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, "usage accepts one session target") {
		t.Fatalf("flag after target was parsed as a second target: %q", stderr)
	}
	if !strings.Contains(stderr, "provide a session target or use --all") {
		t.Fatalf("expected session lookup after parsing --json, stderr=%q", stderr)
	}
}

func TestHandleUsageAllJSON(t *testing.T) {
	stdout, stderr, code := runUsageHelperProcess(t, "--all", "--json")
	if code != 0 || !strings.Contains(stdout, "77") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
