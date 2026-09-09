package session

import (
	"fmt"
	"strings"
)

// Claude Code buckets conversation history by the directory it was STARTED in:
// transcripts land in ~/.claude/projects/<slug-of-cwd>/, and `claude --resume`
// only lists the transcripts in the bucket for the current cwd. A session
// started inside .worktrees/<name> therefore gets a private resume history that
// is invisible from the repository root — one throwaway bucket per worktree,
// and the repo's "main" conversation history never grows.
//
// [worktree].session_cwd = "repo-root" starts worktree sessions in the base
// repository instead, so every session in the repo shares one resume history.
// The worktree is still created and still holds the work; the agent reaches it
// through --add-dir plus a system-prompt directive (see WorktreeCwdDirective).
const (
	// WorktreeSessionCwdWorktree starts the session inside its worktree
	// (historical behaviour, and the default).
	WorktreeSessionCwdWorktree = "worktree"
	// WorktreeSessionCwdRepoRoot starts the session in the base repository so
	// its conversation joins the root project's resume history.
	WorktreeSessionCwdRepoRoot = "repo-root"
)

// NormalizeWorktreeSessionCwd maps a configured [worktree].session_cwd value to
// one of the two canonical modes. Unset, unknown and malformed values fall back
// to WorktreeSessionCwdWorktree so a typo never silently relocates a session.
func NormalizeWorktreeSessionCwd(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "repo-root", "repo_root", "reporoot", "root":
		return WorktreeSessionCwdRepoRoot
	default:
		return WorktreeSessionCwdWorktree
	}
}

// GetWorktreeSessionCwd returns the configured worktree session cwd mode.
func GetWorktreeSessionCwd() string {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return WorktreeSessionCwdWorktree
	}
	return NormalizeWorktreeSessionCwd(config.Worktree.SessionCwd)
}

// ResolveWorktreeSessionCwd returns the directory a NEW worktree session should
// start in — the value that becomes its ProjectPath, and therefore both its
// tmux pane cwd and its Claude history bucket.
//
// It is deliberately a creation-time decision: the answer is baked into the
// stored ProjectPath, so flipping the config later never moves an existing
// session's transcripts out from under `claude --resume`.
//
// repoRoot is only honoured when it is actually a different directory from the
// worktree; a "worktree" that IS the repo root (the worktree_reuse case) has
// nothing to relocate.
func ResolveWorktreeSessionCwd(mode, worktreePath, repoRoot string) string {
	worktreePath = strings.TrimSpace(worktreePath)
	repoRoot = strings.TrimSpace(repoRoot)

	if NormalizeWorktreeSessionCwd(mode) != WorktreeSessionCwdRepoRoot {
		return worktreePath
	}
	if repoRoot == "" || repoRoot == worktreePath {
		return worktreePath
	}
	return repoRoot
}

// RunsOutsideItsWorktree reports whether the session's working directory is the
// repository root rather than its own worktree — i.e. it was created under
// [worktree].session_cwd = "repo-root". Such a session needs the worktree
// granted explicitly (--add-dir) and needs to be told to work there.
func (inst *Instance) RunsOutsideItsWorktree() bool {
	if inst.WorktreePath == "" {
		return false
	}
	return inst.EffectiveWorkingDir() != inst.WorktreePath
}

// WorktreeCwdDirective is the system-prompt text appended for a session that
// runs at the repo root but owns a worktree. Without it the agent would take
// the repo root as its work tree and commit to whatever branch the main
// checkout has out — the one real hazard of running outside the worktree.
func WorktreeCwdDirective(worktreePath, branch, repoRoot string) string {
	if strings.TrimSpace(worktreePath) == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("Working tree for this session: ")
	b.WriteString(worktreePath)
	if strings.TrimSpace(branch) != "" {
		b.WriteString(fmt.Sprintf(" (branch %s)", branch))
	}
	b.WriteString(". ")
	b.WriteString("This session starts at the repository root so its conversation ")
	b.WriteString("joins the root project's resume history, but ALL work belongs in ")
	b.WriteString("the working tree above: cd there before running any command, and ")
	b.WriteString("use absolute paths under it for file edits. ")
	if strings.TrimSpace(repoRoot) != "" {
		b.WriteString("Never edit, stage, commit or switch branches in ")
		b.WriteString(repoRoot)
		b.WriteString(" itself.")
	}
	return b.String()
}
