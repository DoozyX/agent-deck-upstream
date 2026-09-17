# ADR 0001: Default worktree session cwd is repo-root

## Context

Claude Code buckets conversation history by the directory a session is started in. Starting worktree sessions inside `.worktrees/<name>` creates a private resume history per worktree that is invisible from the repository root.

## Decision

Unset `[worktree].session_cwd` defaults to `"repo-root"`: new worktree sessions start at the base repository so they share one resume history. The worktree is still created and reached via `--add-dir` plus a system-prompt directive. Operators who want isolated per-worktree history set `session_cwd = "worktree"` explicitly.

## Alternative rejected

Keeping `"worktree"` as the shipped default preserves historical cwd behaviour but continues to fragment resume history for every new install that never touches the setting — the failure mode that prompted this change.
