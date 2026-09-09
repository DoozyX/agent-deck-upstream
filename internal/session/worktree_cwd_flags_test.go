package session

import (
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
)

// A session created under [worktree].session_cwd = "repo-root" runs at the repo
// root, so Claude can neither see nor be trusted to stay out of the worktree
// unless the launch command says so: --add-dir grants it, and the appended
// system prompt keeps the agent from committing in the main checkout.
func TestClaudeFlagsGrantWorktreeWhenRunningAtRepoRoot(t *testing.T) {
	inst := &Instance{
		Title:            "root-cwd",
		ProjectPath:      "/repo",
		WorktreePath:     "/repo/.worktrees/feature-x",
		WorktreeRepoRoot: "/repo",
		WorktreeBranch:   "feature/x",
	}

	flags := inst.buildClaudeExtraFlagsWithName(nil, "")

	if !strings.Contains(flags, "--add-dir "+shellescape.Quote("/repo/.worktrees/feature-x")) {
		t.Errorf("worktree not granted via --add-dir: %q", flags)
	}
	if !strings.Contains(flags, "--append-system-prompt ") {
		t.Errorf("no worktree directive appended: %q", flags)
	}
	if !strings.Contains(flags, "/repo/.worktrees/feature-x") {
		t.Errorf("directive does not name the worktree: %q", flags)
	}
}

// The default (session starts inside its own worktree) must be untouched: no
// redundant --add-dir for the cwd, no system-prompt injection.
func TestClaudeFlagsUnchangedForWorktreeCwdSessions(t *testing.T) {
	inst := &Instance{
		Title:            "classic",
		ProjectPath:      "/repo/.worktrees/feature-x",
		WorktreePath:     "/repo/.worktrees/feature-x",
		WorktreeRepoRoot: "/repo",
		WorktreeBranch:   "feature/x",
	}

	flags := inst.buildClaudeExtraFlagsWithName(nil, "")

	if strings.Contains(flags, "--append-system-prompt") {
		t.Errorf("directive leaked into a normal worktree session: %q", flags)
	}
	if strings.Contains(flags, "--add-dir") {
		t.Errorf("redundant --add-dir for a session already in its worktree: %q", flags)
	}
}

// A plain (non-worktree) session must be unaffected.
func TestClaudeFlagsUnchangedForNonWorktreeSessions(t *testing.T) {
	inst := &Instance{Title: "plain", ProjectPath: "/repo"}

	flags := inst.buildClaudeExtraFlagsWithName(nil, "")

	if strings.Contains(flags, "--append-system-prompt") || strings.Contains(flags, "--add-dir") {
		t.Errorf("non-worktree session gained worktree flags: %q", flags)
	}
}
