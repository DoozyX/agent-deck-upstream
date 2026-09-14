## Trust-but-Verify

**Use when:** A conductor or worker reports any non-trivial completion claim — "PR is merge-ready", "release shipped", "tap updated", "comment posted", "goal done", "bulk drain complete". Treat all such claims as unverified until an independent verifier session has hit ground truth.

### The rule

For ANY non-trivial done-claim, spawn a **separate** Claude session (not the same conductor, not the same worker) whose job is to re-derive the claim from primary sources. The verifier MUST hit:

- Live state (test runs, GitHub API, release artifacts, file contents on disk)
- Not transcripts. Not the original worker's self-report. Not a same-session review.

Same-session reviewers carry the same blind spots that produced the claim. A separate session re-reads the world from scratch.

### Claim → verifier mapping

| Done-claim | Independent verifier MUST run |
|---|---|
| "PR is merge-ready" | `gh pr checkout <N> && go test -race ./...` AND `gh pr view <N> --json mergeable,statusCheckRollup` |
| "release shipped" | `gh release view <tag> --json assets` AND probe each download URL (HTTP 200, non-empty body, correct content-type) |
| "brew tap updated" | `gh api repos/<owner>/homebrew-tap/contents/Formula/agent-deck.rb` — confirm the SHA + URL match the new release |
| "comment posted" | `gh issue view <N> --comments` (or `gh pr view <N> --comments`) and string-match the expected body |
| "goal worker done" | The manager's `done_cmd` must return `rc=0` AS PART OF the verify cycle — the worker's self-report alone never closes a goal |
| "bulk drain complete" | Spawn a read-only AUDIT worker that enumerates the residual open items (open issues, open PRs, pending releases) and diffs them against the claimed-closed set |

### How to spawn the verifier

Use `launch-subagent.sh` from a different session than the one making the claim (peer session, not child of the claimant). Block with `--wait` so the conductor gets the verifier's answer before reporting up:

```bash
$SKILL_DIR/scripts/launch-subagent.sh "Verify PR #885" \
  "Independently verify PR #885 is merge-ready. Run \`gh pr checkout 885 && go test -race ./...\` and \`gh pr view 885 --json mergeable,statusCheckRollup\`. Report PASS only if BOTH succeed; otherwise report the exact failing tests + the mergeable state." \
  --wait --timeout 600
```

Key points:
- **Verifier prompt asks for primary-source commands, not opinions.** The verifier reports what the commands returned, not what it thinks.
- **`--wait` is mandatory.** Fire-and-forget defeats the purpose — the claim ships before the verifier answers.
- **One verifier per claim.** Don't fold three claims into one verifier; that re-introduces the blind-spot pattern at a smaller scale.
- **Verifier MUST be a peer (or root) session**, not a child of the claimant. A child of the claimant inherits the claimant's working state and is structurally closer to "same blind spots".

### Concrete examples from 2026-05-18 (the day this section was baked in)

1. **PR #885 over-claim.** A worker review reported "merge-ready; clean diff". Independent local re-test (`gh pr checkout 885 && go test -race ./...`) caught 2 failing tests. The reviewer had grepped the diff for obvious issues but never ran the suite. Lesson: code-review confidence ≠ test pass.

2. **ux-rethink-path verdict false-positive.** The ux-rethink-path worker reported "merge as-is; clean diff" for #885. User asked for a second look; a fresh peer verifier caught the same 2 failing tests the original worker missed. Lesson: reviewer + author in the same session-tree share priors. A peer session re-derives.

3. **Goal framework "metronome wakes".** The conductor's hourly wake cycles were firing `[STATUS]` replies with no actual work — the wake-loop had degenerated into a heartbeat instead of a do-work loop. User had to flag it manually. Lesson: a worker that hasn't run a ground-truth check since the last wake is not a working worker, it's a metronome. Bake the priority-0 check (below) into the wake template so this can't recur.

### Wake-loop priority 0 (mandatory)

The goal worker contract prompt at [scripts/goal/prompts/worker.md](../scripts/goal/prompts/worker.md) now begins each wake with **PRIORITY 0**: before reporting status or taking the next bounded step, run **one ground-truth verifier check** against any non-trivial claim made in the previous wake's receipt. If no claim was made last wake, skip priority 0 and go to step 1.

This converts a metronome wake into a do-work wake. See the worker template for the exact wording the cycle uses.

### Anti-patterns

- ❌ "I reviewed my own diff and it looks clean" — same-session review.
- ❌ "Codex agreed with me" — consult is brainstorming, not verification. Codex did not run your tests.
- ❌ "The PR's CI is green" alone — CI lag, flaky tests, or test-skip can hide real failures. Pair CI with a local re-run.
- ❌ "Bulk close all 200 — done." — without an audit pass, you don't know how many silently failed.
- ❌ Fire-and-forget verifier — by the time it answers, the conductor has already shipped the claim upstream.

### When you don't need a verifier

For trivial mechanical actions where the action IS its own verification (and the claim is "I did X"):
- File edit + git diff visible → diff IS verification
- `agent-deck list` reporting current sessions → list IS verification
- Reading a file and quoting its contents → quote IS verification

The verifier requirement attaches to claims about external mutable state: PRs, releases, comments, deployments, bulk operations.

## Where files live

Two locations, split by one question: would this file still mean anything if the
project were deleted tomorrow?

- **Project artifacts** → `<project-root>/.agent-deck/` — handoff prompts
  (`handoff/<session-id>/PROMPT.md`), designs, orchestrate runs, `skills.toml`,
  per-session `tmp/`. `<project-root>` is the repository's **main worktree**, so
  every worktree of a repo shares one tree. Kept out of `git status` via the
  user's global git excludes; agent-deck never edits a tracked `.gitignore`.
- **Machine state** → `$XDG_DATA_HOME/agent-deck/` — `state.db`, config, logs,
  hooks, inboxes, locks, agent homes, conductor/watcher state. Keyed by session
  id and read without a project checkout necessarily existing.

Full table and the lint test that enforces it: `docs/data-locations.md`.

## Configuration

**File:** `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`; legacy `~/.agent-deck/config.toml` still honored)

```toml
[claude]
config_dir = "~/.claude-team"    # Custom Claude profile
dangerous_mode = true            # --dangerously-skip-permissions
use_chrome = false               # --chrome
use_teammate_mode = false        # --teammate-mode tmux
extra_args = ["--agent", "reviewer"]

[logs]
max_size_mb = 10                 # Max before truncation
max_lines = 10000                # Lines to keep

[mcps.exa]
command = "npx"
args = ["-y", "exa-mcp-server"]
env = { EXA_API_KEY = "key" }
description = "Web search"
```

See [config-reference.md](config-reference.md) for all options.

## Troubleshooting

| Issue | Solution |
|-------|----------|
| Session shows error | `agent-deck session start <name>` |
| MCPs not loading | `agent-deck session restart <name>` |
| Flag not working | Put flags BEFORE arguments: `-m "msg" name` not `name -m "msg"` |

### Get Help

- **Discord:** [discord.gg/e4xSs6NBN8](https://discord.gg/e4xSs6NBN8) for quick questions and community support
- **GitHub Issues:** For bug reports and feature requests

### Report a Bug

If something isn't working, create a GitHub issue with context:

```bash
# Gather debug info
agent-deck version
agent-deck status --json
cat ~/.config/agent-deck/config.toml | grep -v "KEY\|TOKEN\|SECRET"  # Sanitized config (legacy: ~/.agent-deck/config.toml)

# Create issue at:
# https://github.com/asheshgoplani/agent-deck/issues/new
```

**Include:**
1. What you tried (command/action)
2. What happened vs expected
3. Output of commands above
4. Relevant log: `tail -100 ~/.agent-deck/logs/agentdeck_<session>_*.log`

See [troubleshooting.md](troubleshooting.md) for detailed diagnostics.
