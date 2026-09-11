---
name: brainstorming
description: Collaborative design and brainstorming before any code is written — explores project context, asks clarifying questions one frontier round at a time through the connector's native question tool, offers 2–3 approaches with trade-offs, and writes an approved design document to its repository-local `.agent-deck/DATE-SLUG/design/` directory (git-ignored, never committed). Use before building a feature, adding functionality, or changing behavior, and whenever the user says "let's build", "I want to add", "how should we do X", or asks for a design or spec. Hard-gates implementation until the design is approved.
metadata:
  compatibility: "claude, opencode"
---

# Brainstorming

**If this session was dispatched as an executor, stop reading here.** Two
tells: the session prompt says the work is already designed and approved,
or `tmux show-environment AGENTDECK_ROLE` prints `AGENTDECK_ROLE=child`.
(Check tmux, not `env` — the marker lives in the tmux *session*
environment, which a process that was already running does not inherit.)
An executor does not brainstorm: its task prompt is the contract. Follow
the task prompt. Do not write a spec, do not propose alternatives, do not
wait for an approval that no one in this session can give. If you believe
the task is genuinely wrong, say so in one line and stop.

## The hard gate

No implementation — and no implementation skill — until the design has
been presented and the user has approved it. This holds regardless of how
simple the change looks. "It's a one-liner" is the most common way this
gate gets skipped, and a one-liner with the wrong requirement is still the
wrong one-liner.

## 1. Explore project context first

Before the first question: read the repo's `README`, `CLAUDE.md`/`AGENTS.md`,
and `CONTRIBUTING.md`; look at how the nearest analogous feature is built.
Then read the repo's run diary, `$ROOT_WT/.agent-deck/diary.md`, when it
exists (`$ROOT_WT` is resolved in step 6): it holds the decisions, discarded
approaches and lessons that earlier runs consolidated, so a trade-off settled
last month is not re-litigated this month. Skim `docs/adr/` for standing
decisions the same way. State what you found in a few lines.

Questions asked without context waste the user's turns on things the repo
already answers.

## 2. Clarifying questions — one frontier round at a time

Map the design as a tree of decisions. The **frontier** is every decision
whose prerequisites are already settled; a question whose answer depends on
another question still open belongs to a later round. Ask the whole frontier
in one round, wait for the answers, then ask the next frontier.

Deliver each round through the connector's native question tool — Claude:
`AskUserQuestion` (up to four questions per call, each with 2–4 options, the
recommended option first and labelled `(Recommended)`); Codex:
`request_user_input`. The tool gives every question its own answer slot, so
one merged reply can no longer swallow half the round. A frontier larger
than one call is two calls, never a prose list. Without a native tool,
number the questions in one message, put a recommended answer under each,
and separate them with a rule, so the user can answer "1 yes, 2 as
recommended, 3 the second option":

```text
❓ Q1 — <title>: <question>
➡️ Recommended: <answer, one-line reason>
---
```

Every question carries a recommendation. A user answering cold re-derives
what you already know; a user confirming or overriding a recommendation
spends one word.

**Facts are your job; decisions are the user's.** A question whose answer
lives in the environment — does the API already paginate, which table holds
the flag, does CI run the e2e suite — is never asked. Dispatch an Explore
agent (or read it yourself when it is one file) and put only the decision to
the user. Questions downstream of the fact wait for it; the rest of the
round does not.

Stop when the frontier is empty: every branch of the tree visited, nothing
left silently assumed. There is no cap on rounds — some designs need three
questions, some need thirty — and there is no credit for finishing early
with an assumption the user never saw.

Usually on the frontier:

- Who uses this, and how?
- What breaks today that this needs to fix?
- What must not change (behavior, interfaces, data)?
- How is this expected to be verified — tests, manual check, both — and
  through which seam (step 4)? What must a good test for this assert?
- For feature work: what is the data model, and what are the contracts that
  cross a component boundary (types, signatures, schemas, events, CLI flags,
  API shapes)? Settle these here, in the design. A planner that later has to
  ask a data-model question is a design gap, not a planning task — one run
  spent two hours of planner, inspect and amend sessions re-deciding a
  membership model the design had left open.

## 3. Approaches — 2–3, with trade-offs and a recommendation

Each approach gets: what it does, what it costs, what it forecloses. Then a
named recommendation with a one-line reason.

**YAGNI ruthlessly**: cut anything that serves a requirement the user did
not state, and say what you cut.

## 4. Principles pass

Before presenting the chosen architecture, check it against
`${CLAUDE_PLUGIN_ROOT}/skills/review/references/principles.md` (that path is
relative to the installed plugin, not to the repo you are working in). The
question to answer out loud:
*does any component here exist for a requirement nobody stated?* Cut or
justify each one.

Then the **seam check**: name the seam the tests drive the feature through —
a CLI command, an HTTP handler, a package's exported function, a rendered
component. Prefer an existing seam to a new one, the highest seam that still
localises a failure, and ideally exactly one. A seam that exists only so a
test can reach it is hypothetical: one adapter behind an interface means the
interface was invented for the test; two real adapters mean the seam is real.
Name the prior-art tests the new ones will be modelled on. Both answers go
into `## Testing Decisions`.

## 5. Present the complete design, approve once

Present motivation, decisions, architecture, interfaces, testing decisions,
and out-of-scope together in a skimmable document. Ask once: `Approve this
design?` A single explicit approval covers all of those sections and the
written document.

For feature work the same approval turn carries the decomposition sketch's
three checks, in the same native-tool call as the approval (or under it in
prose): does the granularity feel right; does each unit depend only on units
that genuinely gate it; should any unit be merged or split. An approval with
no note on the checks approves the sketch as drawn.

Every design, bug fix included, carries one section:

- `## Testing Decisions` — the seam(s) from step 4, the prior-art tests new
  tests are modelled on (by test name and package, not path), what a good
  test for this feature asserts, and what is verified manually instead and
  why. Reviewer and verify sessions are held to this section; without it
  each one derives a different bar. For a bug fix it is the regression
  test's seam, in two lines.

For **feature work** (not a bug fix, not a one-file change) the design is a
mini architecture plan, and three sections are mandatory, after Decisions:

- `## Architecture` — components, their responsibilities, the data flow
  between them, and which existing code each one touches.
- `## Interfaces` — the concrete shared contracts, written out: short
  signatures, schemas, message or event shapes, CLI/API surfaces, and the
  settled data model. This is what a planner elaborates and an implementer
  is held to; "similar to X" is not an interface. Write decisions, not
  locations: names, signatures and schemas, never file paths or speculative
  code — a path goes stale before the planner reads it, and the planner
  finds the real one in the worktree. The one snippet that belongs here is
  one that states a decision more precisely than prose can (a schema, a wire
  format).
- `## Decomposition sketch` — ordered work units, one line each:
  `- U<n> <title> — implements: <iface,...> depends: <U..> parallel-safe: yes|no`.
  One unit means orchestrate launches one implementer and no planner; two or
  more units, with every named interface defined above, means the planner
  only elaborates them into task files and never re-decomposes. Units are
  vertical slices: each lands a thin end-to-end path (schema, logic, surface)
  that is green on its own, not a horizontal layer that only works once every
  other layer exists. A wide mechanical change — a rename, a retype, a call
  pattern migrated across many files — is sequenced expand → migrate →
  contract: one unit adds the new shape beside the old, parallel-safe units
  migrate call sites in batches, and one unit removes the old shape and
  depends on every migrate unit. A unit that commits an ADR (step 8) says so.

Bug fixes and one-file changes skip these three; say so in one line.

Do not ask for per-section approvals. Ask again only if later self-review
materially changes the approved scope: user-visible behavior, public
interfaces, data handling, or an explicitly excluded item. State the change
and why it needs confirmation; editorial fixes and clarifications do not need
another approval.

## 6. Write the spec

**Location: `.agent-deck/<date>-<slug>/design/design.md` in the root worktree,
git-ignored, never committed.** A design doc is scaffolding for the work, not a
deliverable — it must not land in a branch, a diff, or a PR. Its run root
groups the design with the later `plan/` and `orchestrate/` artifacts for the
same work. It goes in the repo's **main checkout** (not a worktree you may be
sitting in). Resolve the path and make the directory ignored *before* writing:

```bash
ROOT_WT=$(git worktree list --porcelain | awk '/^worktree /{print $2; exit}')
RUN_ID="YYYY-MM-DD-<topic>"
RUN_ROOT="$ROOT_WT/.agent-deck/$RUN_ID"
SPEC_PATH="$RUN_ROOT/design/design.md"
git -C "$ROOT_WT" check-ignore -q "$ROOT_WT/.agent-deck/.probe" || \
  printf '.agent-deck/\n' >> "$(git -C "$ROOT_WT" rev-parse --git-common-dir)/info/exclude"
git -C "$ROOT_WT" check-ignore -q "$ROOT_WT/.agent-deck/.probe"  # exit 0 before you write
mkdir -p "$(dirname "$SPEC_PATH")"
```

Probe with a path *inside* the directory, not the directory: asked about a
directory that has tracked files under it, `check-ignore` answers "not
ignored" even when the pattern matches, and you would add a duplicate rule and
still fail the gate. `.probe` need not exist.

`.git/info/exclude` is untracked and applies to every worktree of the repo, so
this costs the user no commit.

Never create a design, plan, task file, prompt, review, report, or retrospective
outside `$RUN_ROOT`. Two exceptions: source code checkout material, which
belongs in the repository's `.worktrees/` directory, and the cross-run diary
at `$ROOT_WT/.agent-deck/diary.md`, which only orchestrate's retrospective
child writes.

**Never `git add` the spec, and never verify it by looking for a commit.** Its
absolute path is what makes it findable — downstream sessions in other
worktrees read `$SPEC_PATH` directly, which works regardless of what any
branch contains. Verify that instead:

```bash
test -f "$SPEC_PATH" && git -C "$ROOT_WT" status --porcelain "$SPEC_PATH"
```

The file must exist and `status` must print nothing (ignored ⇒ invisible).

## 7. Spec self-review

Once written, read the document for placeholders (`TBD`, "etc.",
"handle errors"), internal contradictions, scope creep past what was
approved, and ambiguity a fresh reader would resolve differently than you
meant. Check that `## Testing Decisions` names a seam and a prior-art test rather
than "add tests". For feature work, check that `## Architecture`,
`## Interfaces` and `## Decomposition sketch` exist, that every interface a
sketch unit names is defined in `## Interfaces` — a unit consuming an
undefined contract is the gap the planner will have to invent around — and
that no unit depends on a unit sequenced after it. For contradictions, do a
mechanical pass rather than a read-through:
list every named region, state, mode or component the spec defines, grep the
document for each name, and confirm every mention agrees on its behavior. A
design once said in prose that a region does *not* repeat a countdown while
its own table two paragraphs down defined that region's hero *as* the
countdown; two full terminal review gates were spent re-deriving that from
scratch before anyone could implement it. Make non-material fixes in place.
The prior design approval covers
that document; do not ask for a second document-review approval.

If self-review makes a material change to scope, user-visible behavior,
interfaces, data handling, or an explicitly excluded item, stop and obtain
one approval for that change before handing the spec on.

## 8. Durable decisions (ADR)

The design is scaffolding and dies with the run. Test each decision in it
against three conditions; a decision that meets **all three** outlives the
run as an ADR:

- hard to reverse once code is built on it;
- surprising to a reader who lacks this brainstorm's context;
- the result of a real trade-off, not the only sensible option.

An ADR is one paragraph — context, decision, the alternative rejected and
why — destined for `docs/adr/NNNN-<slug>.md` in the repository, committed
with the code. Write it to `$RUN_ROOT/design/adr/NNNN-<slug>.md` (this
session sits in the primary checkout, which never takes tracked changes) and
name it in the design: in `## Decomposition sketch` as
`commits: adr/NNNN-<slug>.md → docs/adr/` on the first unit, or, for a design
without a sketch, in one line under `## Decisions`. The implementer copies
and commits it in its branch. Most designs produce zero ADRs; write none
rather than one that fails a condition.

## 9. Tiered exit

After approval, size the work and take exactly one exit:

- **Orchestrated** — several independent tasks, non-obvious decomposition, a
  dedicated PR pipeline, or separate executor/reviewer sessions are needed →
  launch a **detached conductor** on `$SPEC_PATH` and hand this session back
  to the user. Do not run `orchestrate` in this session: a conductor lives
  for hours and would hold the user's session hostage for the whole run —
  the user's next feature waits on this one's review rounds. Do not write
  the plan yourself either: orchestrate's planner child writes it against
  the codebase.

  **Freeze the run's goal before launching anything.** The conductor gets its
  goal from the launch message and nowhere else, and that message does not
  survive a conductor rotation: orchestrate's `rotate-conductor.sh` refuses
  to rotate without `$RUN_ROOT/orchestrate/goal.md` and pastes it verbatim
  into every successor's prompt. This session is the only one that still
  holds the user's original ask in their own words, so it writes the file —
  the conductor keeps it as-is and never rewrites it.

  ```bash
  mkdir -p "$RUN_ROOT/orchestrate"
  cat > "$RUN_ROOT/orchestrate/goal.md" <<EOF
  # Goal
  <the user's opening request to this brainstorm, verbatim — quoted, not
   paraphrased; then the design's one-paragraph motivation, verbatim>

  entrance: design
  inputs: $SPEC_PATH
  done means: every unit in the design's Decomposition sketch is landed per
              the landing policy orchestrate records in its manifest, and the
              design's Testing Decisions hold on the landed code
  report to: <the user, by the channel this brainstorm ran in>
  user constraints: <the design's out-of-scope section, verbatim; "none">
  EOF
  test -s "$RUN_ROOT/orchestrate/goal.md"   # a conductor launched on an empty goal cannot rotate

  TOOL=$(agent-deck session show --json | jq -r '(.data // .).tool')   # same connector as this session
  cat > "$RUN_ROOT/design/conductor-prompt.md" <<EOF
  The approved design for this feature is at $SPEC_PATH — read it there by
  absolute path; it is git-ignored on purpose. Run the \`orchestrate\` skill
  on that path. You are the conductor and the root of your own session tree;
  the design is approved, so never re-open it, and never brainstorm. The
  run's goal is already frozen at $RUN_ROOT/orchestrate/goal.md — read it,
  keep it, and append to it only for scope changes the user approves.
  EOF
  agent-deck launch "$ROOT_WT" -c "$TOOL" -t "conductor-$RUN_ID" --no-parent \
    --message-file "$RUN_ROOT/design/conductor-prompt.md" --json \
    | jq -r '(.data // .) | (.id // .session_id)' > "$RUN_ROOT/design/.conductor-id"
  ```

  `--no-parent` is the same mechanism `rotate-conductor.sh` uses for
  successor conductors: the conductor is nobody's child, so the SessionStart
  hook gives it the interactive preamble rather than the executor one, and
  its questions never land in this session. Then print exactly one line and
  end the turn — `conductor-<run-id> <id> launched; this session is free.
  The run's questions surface as that session going \`waiting\` plus a
  banner; attach to answer.` — and do not poll it. The user can start the
  next brainstorm here immediately; each feature gets its own conductor.
- **Focused** — an obvious, low-risk change, even across a few closely related
  files → implement in-session under `tdd`, then `verify` before claiming
  done; any ADR from step 8 lands in the same commit under `docs/adr/`.
  Multi-file alone does not require orchestration.
- **Borderline →** ask **one** final question with your recommendation, and
  take the answer.

## Red flags

| Rationalization | Reality |
|---|---|
| "This is obviously what they want" | Confirm it anyway — the gate exists for the 20% of cases where it isn't. |
| "I'll design as I code" | That's implementation wearing a design costume. Stop and present first. |
| "The spec dir is gitignored, I'll just keep it in the chat" | Chat isn't discoverable by the next session. Ignored is the point — write the file and hand on its absolute path. |
| "Downstream sessions need it committed to see it" | They read it by absolute path from the root worktree. Committing it only puts scaffolding in someone's PR. |
| "I'll ask all my questions at once to save time" | Ask the frontier, not the tree: a prose batch gets one merged answer that covers two of five; a native-tool round gives each question its own slot. |
| "I'll just ask the user whether the API paginates" | That is a fact, not a decision. Look it up; ask only what the codebase cannot answer. |
| "This trade-off is obvious, no ADR needed" | If it is obvious it fails the "surprising" condition and needs none. If a fresh reader would ask "why not the other way?", it passes, and the next brainstorm re-litigates it without one. |
| "They said build it, so approval is implied" | "Build it" approved the idea, not the design. Present the complete design and get one explicit sign-off. |
