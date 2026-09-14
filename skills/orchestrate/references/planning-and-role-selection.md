## Planning stage (only after the focused-first gate records a trigger)

Design and plan are separate artifacts produced by separate roles. Do not
enter this stage merely because a design/spec was supplied. First apply the
focused-first gate above and record its concrete trigger. The
**design/spec** (what and why) is user-approved and arrives as input — if it
doesn't exist yet, brainstorm it with the user *before* orchestrating; that
part is interactive and never delegated. The **plan** (how, task by task) is
written by a dedicated **planner child** in the task's worktree — it needs
deep codebase reading, which is neither your job (supervision only) nor the
user's session's:

```bash
bash "$RUN_DIR/prompts/render.sh" plan "$RUN_DIR/<task-slug>/plan-prompt.md" \
  SPEC_PATH="$SPEC_PATH" TASK_DIR="$PLAN_ROOT/<task-slug>"
WT=$("<agent-deck-repo>/skills/orchestrate/references/create-worktree.sh" \
  --repo "$ROOT_WT" --run-dir "$RUN_DIR" --run-id "$RUN_ID" \
  --task "<task-slug>-plan" --branch <branch> --base <base-branch>)
agent-deck launch "$WT" -c "$PLANNER_TOOL" -t "plan-<task-slug>" "${PLANNER_ARGS[@]}" \
  --message-file "$RUN_DIR/<task-slug>/plan-prompt.md"
```

Immediately verify and record the launch before trusting the child:

```bash
git -C "$WT" status --short --branch
git -C "$WT" rev-parse HEAD
git -C "$WT" merge-base <base-branch> HEAD
```

The printed HEAD must equal the resolved base sha for a newly created branch;
otherwise delete the child and repair the worktree before any task work.

The planner writes `$PLAN_ROOT/<task-slug>/plan.md` plus one concise task-boundary
file per task under `$PLAN_ROOT/<task-slug>/tasks/`. The plan coordinates scope,
paths, dependencies, ordering, interfaces, acceptance criteria, verification,
and any safety/rollback steps. It does not embed production code, duplicate
the approved design, or predict unobserved output. Short signatures, schemas,
and pseudocode are allowed only when they are the shared interface the plan
exists to settle. Each task file carries an `## Interfaces` block and an empty
`## Record (append-only)` section. It tags every task
`tier: mid | strong | frontier` and sizes it to fit one fresh session. It
implements nothing, and it commits
nothing — the plan is scaffolding under `$PLAN_ROOT`, not a change to the branch.
Verify that after it finishes:

```bash
ls "$PLAN_ROOT/<task-slug>/tasks/"                # task files exist
git -C <planner-worktree> status --porcelain      # must be empty
```

A non-empty planner worktree means the planner wrote into the branch instead
of its task directory. Move the files there, reset the worktree, and check the
paths you rendered before relaunching anything.

Skip this stage for small tasks — a single focused change with an obvious
approach (most issues) goes straight into the per-task pipeline.

### Reviewing the plan

**Review the plan only when it will feed 2+ implementer sessions or settle a
recorded high-risk shared contract** — whether a planner child wrote it or you
decomposed the task yourself. One session implementing ordinary work needs no
plan review: that task's stage-2 reviewer holds the whole spec and the whole
diff, so plan-vs-spec and code-vs-spec are the same check, done once on real
code.

Past a fan-out of 2 the plan becomes the coordination contract shared by every
implementer and reviewer, while the approved design remains the requirements
source of truth. Without a plan review, both sides can inherit the same wrong
boundary or interface and a missing requirement can fall between tasks. That
is the whole reason for this gate; review the plan for exactly the failures
that have nowhere else to be caught:

- **coverage** — every spec requirement maps to at least one task;
- **placeholders** — TBD, "add error handling", "similar to task N";
- **contradictions** between tasks, and cross-task **interface mismatch**
  (task 3 calls what task 1 was never told to build);
- **ordering** — a task depending on work scheduled after it, or marked
  parallel-safe while sharing files with its sibling;
- **tier tags** that are obviously wrong for the work described.

Explicitly *not* in scope: code-quality opinions on hypothetical code, exact
test-output predictions, or implementation details that do not change a
cross-task contract. Stage 2 reviews the real diff — cheaper and more accurate
there.

Launch a fresh read-only reviewer in the same worktree (same
`--disallowedTools` flags as stage 2 — it edits nothing), using the same
findings format and verdict line as a code review.

**There is one review and at most one amendment, never a loop.** Findings →
`session send` them to the planner to apply once → proceed. On a **plan-fed** task there is no planner child: launch one
in the worktree scoped to *applying these findings to the plan document* (not
re-planning), or — if the findings are design-level, or the user is still at
the keyboard from handing you the plan — put them to the user instead. Never
edit the plan yourself; it is the spec every child will be held to, and the
conductor doesn't author specs. No re-review and no fix-round budget: a plan is a document
that gets rewritten in place, not a diff that can regress under you, so the
loop-until-clean machinery belongs to code (stages 2–3) where it pays for
itself. Then **delete the planner and plan-reviewer sessions** (see
"Deleting finished sessions").

Two exceptions to proceeding after one amendment: findings that invalidate the
**design** rather than the plan (the approved spec itself is unbuildable or
self-contradictory) are the user's call — stop and surface them, don't have
the planner improvise. And if planning exhausts its context, emits an
implementation-sized artifact, or receives findings across most tasks,
do not launch or rotate to another planner. Collapse the work to one strong
focused implementer. If the recorded coordination or safety trigger makes that
unsafe, stop and ask the user to resolve the blocking architectural decision
instead.

The plan's task list now supplies your decomposition: subtasks = plan tasks
(see `references/single-issue-split.md`). Each implementer and reviewer is
pointed at both the approved design (the source of truth) and its concise task
boundary (the ownership and coordination contract).

**Implementers read the approved design and their own task boundary, not the
full coordination plan or sibling task files.** This prevents a planning error
from silently replacing the approved requirement while keeping sibling detail
out of each worker's context.
The `## Record (append-only)` section at the end of each task file is the
child's audit trail: it appends its commits, the files it touched, and any
concern it hit. It appends **in place**, to the file at its absolute path
under `$PLAN_ROOT/<task-slug>/tasks/` — never to a copy inside the worktree. Siblings each own a
different task file, so concurrent appends do not collide. That record costs
you no context — you read it only when a task goes needs-attention.

## Model & connector tiering

Apply role defaults through `agent-deck launch --orchestrate-role
<routing|routine|architecture>`; do not reproduce the resolution by hand from
the examples below. Run deterministic checks directly through shell/process
execution. The supported launch path preserves
explicit session/provider/model/effort values, then group and provider config,
then fills only missing values from `[orchestrate.<role>]` or built-in role
defaults. It persists the resolved role, provider, model, effort, source fields,
and loadout. `[usage.policy]` remains advisory and cannot overwrite this
precedence.

You (the conductor) run on the strong model; children don't have to. A tier
is a **connector + model** choice, and every child session gets its own — so
one run may mix providers per role: plan on opus, implement on sonnet,
review with codex. A cross-provider reviewer is a feature, not a hack:
fresh eyes from a different model family bring a different failure profile
than the one that wrote the code.

Passing a model is per-connector: `-c claude` and `-c codex` both accept
`--extra-arg --model --extra-arg <model>`; a connector with no known model
flag runs its default (tier by connector choice alone). Omit the flag to
use the user's default. Each provider maps its own ladder onto
cheap/mid/strong/frontier — Claude: `haiku` / `sonnet` / `opus` / `fable`
(the first three are aliases that self-update to the latest release, so pass
them bare); Codex (GPT-5.6): `gpt-5.6-luna` / `gpt-5.6-terra` / `gpt-5.6-sol` /
`gpt-6-astra` (generation-prefixed, so these do drift). The top rung is
special: frontier is never a baseline the conductor picks on its own. It is
reached only by a planner `tier: frontier` tag — which is why the baseline
table's frontier row names that tag — or by an escalation, which reaches it
through strong whatever tier the role started at.
Trust the user's config/defaults over any example here. Connector-specific mechanics move with the role:
read-only enforcement for a **Codex** reviewer is
`--extra-arg --sandbox --extra-arg read-only` (not `--disallowedTools`,
which is Claude-only), and permission menus follow the per-connector rules
in "Answering waiting children".

Launch a Codex reviewer with its native read-only flags:

```bash
agent-deck launch <worktree-path> -c codex \
  -t "review-<task-slug>-r<n>" \
  --extra-arg --sandbox --extra-arg read-only \
  --message-file "$RUN_DIR/<task-slug>/review-r<n>-prompt.md"
```

`LEAN` contains Claude CLI flags, so do not append `"${LEAN[@]}"` to a Codex
launch. Keep the rendered reviewer's read-only rules as defense in depth.

Baseline tier per session:

| Session | Tier |
| --- | --- |
| Planner, plan reviewer, merge-conflict, integration check | strong (e.g. opus) |
| Implementer of a reviewed plan task | the plan task's `tier:` tag — never below mid |
| Implementer of a plan task tagged `tier: frontier` | frontier |
| Implementer, clear spec but no plan | mid (e.g. sonnet) |
| Implementer, freeform — designs its own approach | strong |
| Reviewer, default | mid (e.g. sonnet) |
| Reviewer, freeform or design-heavy task | strong |
| Blind A/B judge (UI tasks) | mid — it must read images; cheap only when the provider's cheap model does |

For planned tasks the planner's `tier:` tags (see the planner prompt) are
authoritative — the planner read the codebase; you'd be guessing from
titles. Mid is the floor for any implementer, though, and the planner tags
`mid`, `strong` or `frontier` for that reason — never below mid: an
implementer never merely
transcribes the plan. It also edits real files, runs the verification
commands, diagnoses a failure the plan did not predict, commits, and emits
the sentinel — and a cheap-tier session that drops one of those does not
fail cheaply. The miss lands in the reviewer's findings, costs a fix round,
and by the round-2 rule below relaunches the whole task strong anyway.
The reviewer default is mid regardless of the implementer's tier:
review is verification work (diff vs. spec, run the suite) and the
Checked/VERDICT format keeps it honest. Freeform or design-heavy tasks get
a strong reviewer because spec compliance there is a judgment call, not a
checklist. Run the round that spends the **last** counted fix round strong,
for a reason distinct from difficulty: a mid-tier reviewer re-raises items
already dispositioned in earlier rounds, and the final round cannot afford
that noise. One run's strong final round matched 9 of 11 findings back to
prior dispositions and dropped them, leaving 2 genuinely new items; its
mid-tier predecessors had re-litigated deferred work every round.

Cheap keeps one home: work you would otherwise do yourself. Three properties
make a job safe to hand down — bounded input, extraction rather than
judgment, and a result you can check at a glance without opening the source.
Reach for it by default on:

| Job | Hand back |
| --- | --- |
| A red CI run | the failing job, the first real error line, the file:line |
| A fetched issue or PR body | the ask in three lines, plus any acceptance criteria stated |
| `gh pr checks` after a push | green / red, and which check if red |
| A long child transcript or verdict file | the VERDICT line and any `decision-needed` finding |
| A lockfile or generated-file diff | which dependencies moved, and whether anything else did |

Every row is something a conductor reads directly by reflex, and reading it
directly is the expensive mistake twice over: the highest per-token rate for
the lowest-value tokens, spent in the one context nobody can rotate. A child
that gets it wrong costs you one re-read; doing it yourself costs you the
context permanently. Cheap does not extend to implementing or reviewing —
those have their own floor above.

Escalations are one-way — once a role escalates, it stays at the escalated
tier for the rest of that task:

- **Reviewer oscillates** — a round reports new findings in code an earlier
  round already passed, meaning the reviewer is missing things → escalate
  the reviewer to strong → frontier.
- **Downgraded implementer fails round 2** — round 2 still reports `patch`
  or `decision-needed` findings → don't send a third round to the same
  session; launch the fix
  as a NEW session in the same worktree, escalated strong → frontier (tell it
  to read `git log` and the diff first). Caps the worst case at roughly
  one frontier-model session.

Record every session's connector + model in the manifest, escalations
included, on the launch line from "Usage-aware launch" in [principles and
permissions](principles-and-permissions.md)
(`role=<role> tool=<tool> model=<model> tier=<applied> state=<state> reason=<one line>`)
— the final report surfaces them, and that record is the only way
to tell whether tiering saved cost or just bought extra rounds.
