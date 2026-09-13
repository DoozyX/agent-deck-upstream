---
name: orchestrate
description: End-to-end delivery and deployed-system verification pipeline. Use it to verify a deployed system through independent evidence arms and a terminal pass, defect, or inconclusive report, or to take tasks/issues through dedicated implementation, tests, review, PR, and green CI. Also use it for approved designs/specs and implementation plans that should be executed in dedicated child sessions. For plain fan-out-and-supervise work, use the fleet skill instead.
metadata:
  compatibility: "claude, opencode"
---

# Orchestrate

Run deployed-system verification or take approved work through implementation,
independent review, landing, and green CI. Read `skills/fleet/SKILL.md` first;
this skill depends on its launch, parenting, group, and completion contracts.

## Permissions and stopping

The conductor performs control-plane work only: run-directory setup, prompt
rendering, session lifecycle/supervision, durable state, arbitration, and final
reporting. Delegate repository inspection, planning, implementation, testing,
review, merging, deployment, and cleanup to scoped children. Never edit or
switch the primary checkout; tracked work belongs in a dedicated isolated
worktree.

Authorization does not grow with the workflow. Stop and ask for destructive
actions, scope changes, credentials, externally visible mutations, or product
decisions not already approved. A deployed-verification `pass` or
`inconclusive` is terminal with no edit, PR, CI, or deployment. A `defect`
enters delivery only when it is inside the authorized scope.

When a task exhausts its durable budget, cannot produce trustworthy evidence,
or needs a user decision, mark it `needs-attention`; preserve its session,
worktree, branch, findings, and receipts. Do not force-push, reset, delete, or
silently continue. Read [principles and permissions](references/principles-and-permissions.md)
for the full conductor boundary.

## Lifecycle and run startup

Keep workflow artifacts under the repository's ignored
`.agent-deck/<run-id>/`; keep source checkouts under `.worktrees/`. Freeze the
user-approved goal in `goal.md` before launching children and never rewrite it;
append approved scope changes. Maintain `manifest.md` and
`conductor-handoff.md` as durable state.

Copy and use the supported run helpers described in [delivery
startup](references/delivery-startup.md), including
[`supervisor.sh`](references/supervisor.sh),
[`review-state.sh`](references/review-state.sh),
[`review-attempt.sh`](references/review-attempt.sh),
[`rotate-conductor.sh`](references/rotate-conductor.sh), and the
[`prompts/`](references/prompts/) directory. Start deterministic supervision
with `supervisor.sh start`; inspect `status`, acknowledge a delivered event with
`ack`, and call `stop` only after final reporting. An optional bounded watchdog
may handle approved safe prompts; it is never a permission bypass or a required
always-active model loop.

Use [modes and prompts](references/modes-and-prompts.md) to select the entrance,
run focused-first triage, and render children. Use the [prompt
catalog](references/prompt-catalog.md) to locate a specific template without
loading all prompt bodies.

## Canonical task identity and durable review limits

Assign one stable canonical task id from the approved task record; never derive
it from a mutable title, session id, branch display name, or retry number. The
same task id, canonical run path, base HEAD, reviewed HEAD, spec id, and attempt
lineage must flow through every review and fix receipt.

Launch reviews and fixes through `review-attempt.sh`; its atomic
`review-state.sh` reservation is the durable authority. Branch only on its
machine results: `allowed`, `already-recorded`, or `needs-attention`. The
default epoch permits at most three completed reviews and two completed
automatic fixes. A fourth review is only the explicit material integration
gate enforced by the script. Transport startup retries are bounded; quota waits
park until their recorded reset. Any operator-approved override must be
recorded explicitly—never reset counters or manufacture a new task identity.

Read [task delivery](references/task-delivery.md) for implementation, review,
fix, PR, and CI mechanics. Review verdicts are `clean` or `fix-needed`; a clean
verdict must cover the current full branch and fresh verification.

## Change-only supervision and liveness

Use `supervisor.sh` as the normal supervision entrypoint. It performs local
checks without model turns and wakes only on a new or materially changed
actionable event. Repeated unchanged status, acknowledged events, known quota
waits before reset, and ordinary health checks do not wake a model. Do not add a
periodic model polling loop.

A fresh terminal done sentinel must be attributable to the current turn before
completion is actionable. Delivery attempts are bounded; unreachable or
ambiguous delivery becomes durable operator attention instead of an infinite
retry. Waiting questions, permission choices, failures, stalls, context
thresholds, and quota changes retain distinct event identities across recovery
and conductor rotation.

Read [supervision](references/supervision.md) for answering children and
watchdog boundaries. Read [lifecycle and recovery](references/lifecycle-and-recovery.md)
for compact/rotation thresholds, goal recovery, queued-successor rejection,
settle-window liveness, and failure invariants. Never re-parent children,
advance generation, repoint supervision, or archive the predecessor unless the
successor passes the liveness gate. If startup fails, preserve the predecessor
and all routing and write the failure receipt.

## Role and tool selection

Use the supported `agent-deck launch --orchestrate-role
<routing|routine|architecture>` path. Explicit user/session,
account, provider, tool, model, effort, group, browser, and MCP choices win;
configured group/global values follow; role defaults fill only empty fields.
Persist the resolved role, provider, model, effort, source fields, and tool
loadout, and verify the launch receipt from the supported entrypoint.

Deterministic checks run directly through shell/process execution and do not
use model-role resolution. Built-in model defaults are Luna low or Haiku for
routing; Terra medium or Sonnet medium for routine work; Sol high or Opus
medium for architecture. Astra/frontier use requires an explicit model choice
or justified escalation. Unsupported choices park visibly; do not silently
fall back to a stronger model or another provider.

Claude non-browser children default to strict empty MCP configuration. Browser
work retains browser tools. Codex receives only Codex-supported loadout flags;
never pass Claude-only flags to it. Read [planning and role
selection](references/planning-and-role-selection.md) for planning and
connector mechanics.

`[usage.policy]` remains a separate advisory, availability-aware recommendation
surface. Query it only before a relevant launch wave or after reported quota
change; unavailable data does not block work. It never overrides explicit
choices or role precedence. Cross-provider quota rotation and installed replay
are integration obligations outside this skill edit; do not claim them here.

## Verification

Every implementation/fix must use the repository's required test discipline,
commit its scoped change, and report fresh command evidence. A conductor must
verify the VCS diff, immutable HEAD, supported launch/receipt path, and required
remote state before accepting a child's sentinel. Artifact existence or a
worker's success claim is not evidence.

For deployed verification, use independent arms, validate each producer's
completion/provenance/freshness/schema, adjudicate contradictions, and end with
exactly `pass`, `defect`, or `inconclusive`; read [deployed
verification](references/deployed-verification.md). For delivery, follow [task
delivery](references/task-delivery.md). Before reporting, follow [failure,
cleanup, and stopping](references/failure-cleanup-and-stopping.md), run the
read-only teardown gate, then use [reporting](references/reporting.md).

## Reference routing

| Stage | Read |
| --- | --- |
| Authority and conductor constraints | [principles and permissions](references/principles-and-permissions.md) |
| Run directories, goal freeze, helper copies, landing policy | [delivery startup](references/delivery-startup.md) |
| Entrance parsing, focused-first gate, prompt rendering | [modes and prompts](references/modes-and-prompts.md) |
| Plan/review and connector/role mechanics | [planning and role selection](references/planning-and-role-selection.md) |
| Independent evidence-arm flow | [deployed verification](references/deployed-verification.md) |
| Implement/review/fix/PR/CI pipeline | [task delivery](references/task-delivery.md) |
| Waiting children and watchdog boundaries | [supervision](references/supervision.md) |
| Context, compaction, rotation, handoff recovery | [lifecycle and recovery](references/lifecycle-and-recovery.md) |
| Needs-attention, cleanup, teardown, stop order | [failure, cleanup, and stopping](references/failure-cleanup-and-stopping.md) |
| Final report and retrospective | [reporting](references/reporting.md) |

Load only the row for the current stage. Operational scripts and all prompt
templates remain discoverable through the [prompt and helper
catalog](references/prompt-catalog.md).
