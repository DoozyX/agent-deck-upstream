---
name: agent-deck
description: Terminal session manager for AI coding agents. Use when user mentions "agent-deck", "session", "sub-agent", "MCP attach", "git worktree", or needs to create, supervise, configure, share, or troubleshoot agent sessions and worktree sessions.
metadata:
  compatibility: "claude, opencode"
---

# Agent Deck

Manage interactive agent sessions through the `agent-deck` CLI. Resolve this
skill's directory from the path shown when it loads; scripts live under that
directory, not under the user's project.

## Permissions and operating boundary

Launching or supervising a session does not expand the user's authorization.
Keep child work and tools inside the requested scope, preserve configured
permission modes, and surface destructive or externally mutating choices to the
user. Attach MCPs or plugins only when the task needs them.

Agent Deck owns interactive sessions, not always-on daemons or network
listeners. Put persistent services under the OS supervisor and keep deck
sessions disposable. Read [safety and known gotchas](references/safety-and-gotchas.md)
before raw tmux intervention, daemon-like use, binary replacement, or recovery
from a known failure mode.

## Session lifecycle and stopping

Use `agent-deck session current --json`, `session show`, or `list --json` to
resolve a target before mutating it. `launch` creates and starts; `session
start`, `stop`, and `restart` control an existing session. `session remove
--force` kills the pane and removes the registry row, so use it only for an
exact, finished target whose retained transcript/worktree policy is known.

Use `session send` for a new instruction and `session nudge` for supervised
wake-up. Never add a raw `tmux send-keys ... Enter` after either command. Wake
only for a new actionable state or changed evidence; unchanged polling must not
repeatedly wake a model. For repeated supervision use the orchestrate
`supervisor.sh` flow rather than a model polling loop.

Read [session operations](references/session-operations.md) for creation,
parenting, consultation, conductors, MCPs, accounts, and supported command
forms. Read [workspaces and watchers](references/workspaces-and-watchers.md)
for TUI keys, worktrees, scratch sessions, and watcher operation.

## Canonical identity and supervision

Treat the immutable session id as canonical; titles are labels and may collide
or change. A child task also retains the stable task id assigned by its
workflow—do not infer task identity from the session title. Verify every
launched child's id, parent id, group, worktree, and connector from supported
JSON output before trusting routing.

Completion requires a fresh final assistant response with the terminal
`===AGENTDECK_DONE=== status=<ok|fail> summary=<one line>` sentinel. A waiting
row, old output, artifact path, or stale sentinel is not completion. Use
`session output --json --require-fresh`; use `session output --pane` only to
diagnose the live UI state.

## Role and tool selection

Explicit user/session/account/tool/model/effort settings win. Orchestrate role
defaults fill only unspecified fields and must be applied through the supported
`agent-deck launch --orchestrate-role <routing|routine|architecture>` path so the resolved
role, provider, model, effort, sources, and loadout are persisted. Unsupported
provider/model/effort combinations park with an error; do not silently upgrade
or switch providers.

`[usage.policy]` is a separate, availability-aware advisory surface. It may
recommend a tool or tier but never overrides an explicit choice or the role
resolution precedence. See the [orchestrate and usage configuration
sections](references/config-reference.md#orchestrate-section).

Connector flags stay with their connector. Claude's strict empty-MCP flags are
not Codex flags; browser work keeps its required tools. Deterministic status,
counting, and path checks use shell/process execution rather than a model.

## Verification

Treat a worker's report as a claim. Before landing or reporting tracked work,
inspect the VCS diff and HEAD, run the task's current verification commands,
confirm the exact remote ref when a push is required, and compare failures to a
recorded baseline. Verify supported entrypoints, not an uncalled helper.

Read [verification and support](references/verification-and-support.md) for
claim-to-evidence mappings, verifier sessions, file locations, configuration,
and support commands.

## Reference routing

Load only the reference for the current operation:

| Need | Read |
| --- | --- |
| Session/sub-agent/consult/conductor commands | [session operations](references/session-operations.md) |
| TUI, MCP, worktree, scratch, watcher workflows | [workspaces and watchers](references/workspaces-and-watchers.md) |
| Goal workers or transcript self-improvement | [goals and self-improvement](references/goals-and-self-improvement.md) |
| Evidence rules, paths, configuration, help | [verification and support](references/verification-and-support.md) |
| Contributions, session sharing, account switching | [contributing and sharing](references/contributing-and-sharing.md) |
| Lifecycle hazards and uncommon troubleshooting | [safety and gotchas](references/safety-and-gotchas.md) |
| Complete CLI catalog | [CLI reference](references/cli-reference.md) |
| Complete configuration catalog | [configuration reference](references/config-reference.md) |
| TUI feature catalog | [TUI reference](references/tui-reference.md) |
| Troubleshooting only | [troubleshooting reference](references/troubleshooting.md) |
| Goal architecture | [goal reference](references/goal.md) |
| Sandboxed sessions | [sandbox reference](references/sandbox.md) |

For multi-child fan-out read the [fleet skill](../fleet/SKILL.md). For
end-to-end delivery or deployed verification read the [orchestrate
skill](../orchestrate/SKILL.md).
