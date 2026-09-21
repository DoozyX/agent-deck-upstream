package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Lean child profile (startup-context budget).
//
// Every dispatched child pays the host agent's full startup context — system
// prompt, tool schemas, instruction files, memory index — before it reads its
// task. An orchestrate run with a conductor, a planner and half a dozen workers
// pays that cost once per session, and again on every rotation.
//
// --exclude-dynamic-system-prompt-sections moves the per-machine blocks (cwd,
// env, memory paths, git status) out of the cached system prompt and into the
// first user message. That shrinks what every child carries AND makes the
// remaining prompt identical across children, so they share prompt-cache
// entries instead of each paying a cold cache.
//
// It applies to leaf children only. A conductor is long-lived, is the session a
// human attaches to, and reads the dynamic blocks while supervising, so it
// keeps the full profile.

const leanFlag = "--exclude-dynamic-system-prompt-sections"

// writeLeanChildrenConfig writes [context] lean_children into the temp HOME's
// config.toml. channelsTestEnv must have run first.
func writeLeanChildrenConfig(t *testing.T, enabled bool) {
	t.Helper()
	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	value := "false"
	if enabled {
		value = "true"
	}
	body := "[context]\nlean_children = " + value + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
}

func TestBuildClaudeExtraFlags_LeanChildExcludesDynamicSystemPromptSections(t *testing.T) {
	channelsTestEnv(t)

	inst := NewInstanceWithTool("lean-child", t.TempDir(), "claude")
	inst.ParentSessionID = "parent-instance-id"

	flags := inst.buildClaudeExtraFlags(&ClaudeOptions{})

	if !strings.Contains(flags, leanFlag) {
		t.Fatalf("a dispatched child must launch with %s; got:\n%s", leanFlag, flags)
	}
}

func TestBuildClaudeExtraFlags_ConductorKeepsFullProfile(t *testing.T) {
	channelsTestEnv(t)

	inst := NewInstanceWithTool("lean-conductor", t.TempDir(), "claude")
	inst.ParentSessionID = "parent-instance-id"
	inst.IsConductor = true

	flags := inst.buildClaudeExtraFlags(&ClaudeOptions{})

	if strings.Contains(flags, leanFlag) {
		t.Fatalf("a conductor must keep the full profile; got:\n%s", flags)
	}
}

func TestBuildClaudeExtraFlags_UnparentedSessionKeepsFullProfile(t *testing.T) {
	channelsTestEnv(t)

	inst := NewInstanceWithTool("lean-standalone", t.TempDir(), "claude")

	flags := inst.buildClaudeExtraFlags(&ClaudeOptions{})

	if strings.Contains(flags, leanFlag) {
		t.Fatalf("an interactive session a human drives must keep the full "+
			"profile; got:\n%s", flags)
	}
}

func TestBuildClaudeExtraFlags_LeanChildDisabledByConfig(t *testing.T) {
	channelsTestEnv(t)
	writeLeanChildrenConfig(t, false)

	inst := NewInstanceWithTool("lean-optout", t.TempDir(), "claude")
	inst.ParentSessionID = "parent-instance-id"

	flags := inst.buildClaudeExtraFlags(&ClaudeOptions{})

	if strings.Contains(flags, leanFlag) {
		t.Fatalf("[context] lean_children = false must switch the lean profile "+
			"off; got:\n%s", flags)
	}
}

// The escape hatch must be an opt-out, not an opt-in: children are lean unless
// the config says otherwise, so a host with no [context] section still gets the
// budget. This pins the default against a later change that silently inverts it.
func TestBuildClaudeExtraFlags_LeanChildDefaultsOnWithConfigPresent(t *testing.T) {
	channelsTestEnv(t)
	writeLeanChildrenConfig(t, true)

	inst := NewInstanceWithTool("lean-optin", t.TempDir(), "claude")
	inst.ParentSessionID = "parent-instance-id"

	flags := inst.buildClaudeExtraFlags(&ClaudeOptions{})

	if !strings.Contains(flags, leanFlag) {
		t.Fatalf("[context] lean_children = true must keep the lean profile on; "+
			"got:\n%s", flags)
	}
}

// Non-Claude tools do not understand the flag; emitting it would break the
// launch command outright.
func TestBuildClaudeExtraFlags_LeanFlagIsClaudeOnly(t *testing.T) {
	channelsTestEnv(t)

	inst := NewInstanceWithTool("lean-codex", t.TempDir(), "codex")
	inst.ParentSessionID = "parent-instance-id"

	if cmd := inst.buildCodexCommand("codex"); strings.Contains(cmd, leanFlag) {
		t.Fatalf("a codex session must never carry %s; got:\n%s", leanFlag, cmd)
	}
}
