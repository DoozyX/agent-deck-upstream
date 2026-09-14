## Per-task pipeline

### 1. Implement

Derive a short `<task-slug>` and branch name. Render the implementer prompt to
`$RUN_DIR/<task-slug>/impl-prompt.md` and pass it with `--message-file` —
never inline via `-m "$(cat ...)"`: the shell mangles backticks and `$`, and
issue bodies are full of both.

Every task — spec-fed, plan-fed or freeform — takes the same explicit
`create-worktree.sh` path onto a fresh worktree off the base branch. The task file is not in that
worktree and does not need to be: the child reads it at its absolute path
in its task directory, so no base commit can hide it. Verify the file exists
before launch, and record the worktree path in the manifest:

```bash
test -f "$PLAN_ROOT/<task-slug>/tasks/task-NN-<name>.md"
bash "$RUN_DIR/prompts/render.sh" impl "$RUN_DIR/<task-slug>/impl-prompt.md" \
  TASK_TITLE="<title>" SPEC_BLOCK@="$RUN_DIR/<task-slug>/spec-block.md" \
  RUN_DIR="$RUN_DIR" TASK_SLUG="<task-slug>"
WT=$("<agent-deck-repo>/skills/orchestrate/references/create-worktree.sh" \
  --repo "$ROOT_WT" --run-dir "$RUN_DIR" --run-id "$RUN_ID" \
  --task "<task-slug>" --branch <branch> --base <base-branch>)
agent-deck launch "$WT" -c "$IMPLEMENTER_TOOL" -t "impl-<task-slug>" "${IMPLEMENTER_ARGS[@]}" \
  --message-file "$RUN_DIR/<task-slug>/impl-prompt.md"
```

**The spec block carries the binding rules distilled, with paths only as
backup — it is not a reading list.** An implementer pointed at the design doc,
the task file, a 28-item deferred list and two forward-constraint blocks, told
to read all of it before writing code, burned 208.9k of context and 13 minutes
reading 46.1k tokens and produced zero files. That brief was handed to a
mid-tier model, where a reading load is a strong-tier cost. The next two
tasks restated the binding rules inside the spec block itself and their
implementers wrote code on turn one. Every path you include should be
something the child consults to check a detail, never something it must read
to start.

**Confirm the brief actually arrived.** A launch's `success: true` reports
that the session started; since the delivery contract landed it also reports
`delivery` and `submitted`, and only `submitted: true` means the agent took
the brief up as a turn. Check the child's context is climbing ~20s after
launch — not the launch line. A child whose brief was never delivered sits at
`ctx=0` with an empty composer while `session list` reports `running`, and
that went unnoticed for four consecutive heartbeats. The rendered prompt stays
on disk, so redelivery is free:
`agent-deck session send <id> --message-file "$RUN_DIR/<task-slug>/impl-prompt.md"`.

Run the same launch verification used for planner worktrees: print and record
the worktree path, branch, HEAD, resolved base sha and merge base before the
child starts changing files. A mismatch is a launch failure, not a baseline
the implementer should work around.

If the task file is missing, stop before launching a child: a child handed a
path it cannot read improvises from an empty spec, and you find out one review
round later. Fix the path — the file is in the task directory, not in the
branch.

The rendered prompt tells the implementer to work strictly in this worktree
and, in order: install from the frozen lockfile, execute the manifest's shared
verification contract and record only task-specific baseline deltas *before*
touching anything, implement test-first, rerun the full suite plus
the repo's lint/format/build checks, verify end-to-end by driving the app
(isolated browser instance — siblings are driving browsers too), capture
before/after screenshots into `$RUN_DIR/<task-slug>/` **and describe in words
what each one shows**, and commit without pushing. It closes with the
keep-your-context-lean rules (delegate sweeps to subagents, read output tails).

### 2. Fresh review

When `session children` shows the implementer done (`done_status=ok`), launch
a **fresh** reviewer in the **same worktree path** (plain path, no `-w`).
Back the prompt's read-only rule with tool flags — but understand what they
do and don't buy you. They block the editing tools; **Bash stays available**
so the reviewer can run the suite, which means every destructive `git`
command is still one call away. That gap is not theoretical: a reviewer has
run `git stash` in a shared worktree and swept a sibling implementer's
in-flight work. The prompt rule below carries what the flags cannot.

```bash
bash "$RUN_DIR/prompts/render.sh" review-full "$RUN_DIR/<task-slug>/review-r1-prompt.md" \
  VERDICT_FILE="$RUN_DIR/<task-slug>/review-r1.md" \
  SPEC_BLOCK@="$RUN_DIR/<task-slug>/spec-block.md" \
  BASE_BRANCH=<base-branch> AGENT_DECK_REPO=<agent-deck-repo> \
  BASELINE="<shared manifest baseline plus task-specific delta, or none>"
agent-deck launch <worktree-path> -c "$REVIEWER_TOOL" -t "review-<task-slug>-r1" "${REVIEWER_ARGS[@]}" \
  --message-file "$RUN_DIR/<task-slug>/review-r1-prompt.md"
```

Record the worktree's current HEAD sha in the manifest when you launch each
reviewer — every later round is scoped against it as `REVIEWED_SHA`.

**Never grant a reviewer write authority without sole occupancy, in the same
sentence.** If a proof you want genuinely needs a mutation — flipping a guard,
adding a union member — the reviewer must be the only session in that worktree
at the time, and your brief must say both things together: "you have write
authority; you are the sole occupant of this worktree." Granting the first
without arranging the second produced this pipeline's two worst incidents in
one task: a reviewer left an uncommitted edit dropping an organization-scope
predicate from a query — a live cross-org leak in a tree an implementer was
committing from — and the round after, a fix report claimed a fix that had not
been made. Both were caught by a successor that checked, not by the process
that made the claim.

Prefer a static proof and grant nothing. An exhaustiveness claim is settled by
adding a union member, reading the exact `tsc` error and removing it; the
opposite direction by an unused `@ts-expect-error` throwing TS2578 under
`tsc --noEmit`. If you ask for a mutation proof while citing the read-only
stance, the reviewer is instructed to name the contradiction rather than pick
a side — that is correct behaviour, and the fix is your brief, not the child.

**A UI task's reviewer launches without `LEAN`.** The prompt makes it
reproduce every user-visible acceptance criterion with its own eyes — build,
start, drive the app in an isolated browser, look — and report a `Seen:` line
per criterion; the implementer's screenshot descriptions are claims it never
accepts as evidence. Stripping the browser MCPs from that reviewer turns each
`Seen:` into a `[decision-needed]` "could not exercise" finding, which is the
prompt working as designed against a launch mistake. Reviewers of non-UI work
keep `LEAN`.

**Blind A/B judge (UI tasks only).** Nobody in the pipeline except the
implementer has looked at its before/after captures, and you never will. So
launch a judge that sees nothing but the two images per pair, in shuffled
order, with no task context — the only reviewer that cannot flatter the home
team. Launch it alongside the round-1 reviewer, right after the implementer
reports done:

```bash
PAIRS=$(bash "<agent-deck-repo>/skills/orchestrate/references/ab-pair.sh" "$RUN_DIR/<task-slug>")
bash "$RUN_DIR/prompts/render.sh" ab-judge "$RUN_DIR/<task-slug>/ab-judge-prompt.md" \
  PAIRS_DIR="$PAIRS" VERDICT_FILE="$RUN_DIR/<task-slug>/ab-judge.md"
agent-deck launch "$PAIRS" -c "$JUDGE_TOOL" -t "ab-judge-<task-slug>" "${JUDGE_ARGS[@]}" \
  --message-file "$RUN_DIR/<task-slug>/ab-judge-prompt.md"
# when session children shows it done:
bash "<agent-deck-repo>/skills/orchestrate/references/ab-reveal.sh" \
  "$RUN_DIR/<task-slug>" "$RUN_DIR/<task-slug>/ab-judge.md" | tail -n 1
```

`ab-pair.sh` copies every `before-<what>.png`/`after-<what>.png` pair to
`ab/<what>/A.png` and `B.png` in coin-flip order and writes the mapping to
`ab/<what>/key`; it exits non-zero when a UI implementer captured no pair,
which is itself a finding for the fix round ("no before/after capture"). The
judge's cwd is the pairs directory, not the worktree, so it has no repository,
no `CLAUDE.md` and no task file to anchor on. `JUDGE_ARGS` is the reviewer's
read-only flags plus `LEAN` — it reads images with the file tool, not a
browser. Never open `key`, `A.png` or `B.png` yourself. `ab-reveal.sh` decodes
the judge's `A`/`B` back to `before`/`after` and its last line,
`AB_SUMMARY: pairs=<n> regressions=<n> unchanged=<n>`, is the only line you
read; record it in the manifest. A non-zero reveal means the judge died or
ignored the format — relaunch it, never read the round as clean. Delete the
judge once the reveal is read.

`regressions>0` (the judge preferred the *before* state at med or high
confidence) is a fix-round finding even when the reviewer's verdict is clean:
append it to the round's findings file before rendering the fix prompt, as
`N. <pair> — major — [decision-needed] — [AB-Judge] — blind judge preferred
the before state: <reason>`. `unchanged>0` on a task whose point was a
visible change is the same finding with "no visible difference". After any
fix round that touched the screen the implementer recaptures the pair and the
judge runs again; a UI task's loop ends only on a clean full-branch verdict
**and** a reveal with `regressions=0`.

The rendered prompt starts the full suite detached into
`<verdict-file>.suite.log` before the reviewer reads anything and runs the
layers as parallel subagents while it runs — measured over six rounds, the
suite (2–6 min) and the serial in-context layers (2.5–6 min) were the whole
cost of an 8-minute round, and overlapping them is what brings a round to
~4 min. It makes the reviewer read-only with exactly two permitted
writes (the verdict file and that log, both outside the repo), forbids every working-tree-rewriting
command in a worktree it may share with a live implementer, runs the review
layers with `adversarial` **first and spec-blind**, threads spec compliance
through the other layers, hands over the implementer's baseline as
not-a-finding, makes the reviewer reproduce user-visible criteria itself
(`Seen:` lines) and score any `## Quality bar` anchors (`Scored:` lines,
below-threshold = `major`), and demands the `## Merged findings` anchor plus
a machine-readable `VERDICT:` line.

**The verdict-file interface (the conductor owns the path).** `VERDICT_FILE` is
always `$RUN_DIR/<task-slug>/review-r<n>.md` — the same run
directory every other prompt file lives in, which is ignored and outside all
source worktrees by construction. The reviewer writing that file itself
replaces the old
`session output ... > $RUN_DIR/<slug>/review-r<n>.txt` capture: the raw layer
output lands there without ever passing through your context, and you read
only the merged findings, the `Checked:` lines and the `VERDICT:` line from
the child's response. Keep the file — the next round's reviewer is
handed the previous round's findings from it, and it is the evidence trail for
a needs-attention task. **Check the file before you build a fix round from
it:** if it is absent, or `grep -q '^## Merged findings'` fails, the reviewer
died or ignored the format — treat that round as failed and relaunch the
reviewer. Do not build a fix prompt from it, or you will mail the implementer
a fix round containing no findings and read its "nothing to do" as progress.
When you do build the prompt, extract
only the `## Merged findings` section (`sed -n '/^## Merged findings/,$p'`) —
the raw layer output above that anchor is deliberately hostile, ungraded and
un-deduped, and shipping it to an implementer undoes the merge step's whole
purpose.

**`<agent-deck-repo>` is a path you must resolve, not a placeholder to paste.**
A reviewer child cannot execute a single layer without it, and that child runs
inside the *target* repo's worktree, which is not the agent-deck checkout.
Resolve it once during run setup — the installed plugin root (e.g.
`~/.claude/plugins/marketplaces/agent-deck`) or a local checkout, whichever
actually holds `skills/review/references/` — confirm the layer files are
readable there, record it in the manifest, and substitute the real absolute
path into every reviewer prompt.

### 3. Fix loop

- The verdict is machine-readable and you branch on it directly. `VERDICT:
  clean` → proceed. `VERDICT: fix-needed` → look at the buckets, not the raw
  count:
  - `patch` items go back to the implementer as a fix round.
  - `decision-needed` items are **not** the implementer's to resolve —
    escalate them to the user exactly like a waiting child's question (see
    "Answering waiting children"), and hold that task while you wait.
  - `defer` items append to `$RUN_DIR/deferred-work.md` and **never extend
    the loop**; they are listed in the final report and go no further.
  A verdict whose only findings are `defer` items is emitted as
  `VERDICT: clean` by construction, so nothing extra is needed for that case.
- **The reviewer proposes a severity; you decide it.** That is why you read
  findings lists in full (see "Context budget"). One rule is not a judgment
  call: **a finding whose blast radius is existing data, introduced by this
  branch, is never a `minor`.** A gate review once graded "the edit form seeds
  a stored workload of 0 as 100" a `minor`; it was a regression that made
  previously-savable rows unsavable and destroyed the value on an untouched
  Save. Regrade upward and send it back. Regrading *downward* is a different
  act — it needs a reason you can write in one line, and it goes in the final
  report.
- On findings → first confirm `impl-<task-slug>` is still registered
  (`agent-deck session children "$AGENTDECK_INSTANCE_ID" --json` lists it).
  The implementer is deleted at task-done cleanup, never after round 1: it
  holds why the code took its shape, and a fresh fix session re-reads the
  branch before it can start (one run launched four of them). If it is gone,
  record `deviation: implementer deleted before task-done` in the manifest and
  launch the replacement in the same worktree with the fix prompt prefixed by
  the rotation instruction (read `git log`, the branch diff and any
  `handoff.md` first). Then render the fix-round prompt and `session send` it to
  `impl-<task-slug>` with `--message-file` (findings lists are full of
  backticks too). Pipe the findings **file to file**: they were written by the
  reviewer and never need to pass through you a second time.

```bash
sed -n '/^## Merged findings/,$p' "$RUN_DIR/<slug>/review-r<n>.md" > "$RUN_DIR/<slug>/findings-r<n>.md"
bash "$RUN_DIR/prompts/render.sh" fix "$RUN_DIR/<slug>/fix-r<n>.md" \
  ROUND=<n> FINDINGS@="$RUN_DIR/<slug>/findings-r<n>.md"
agent-deck session send "impl-<task-slug>" \
  --message-file "$RUN_DIR/<slug>/fix-r<n>.md" --defer-if-busy
```

  **`--defer-if-busy` on every send to a working child, without exception.**
  It holds delivery until the target's hook-driven status says the turn is
  over, instead of typing into a composer that is mid-generation. Without it a
  send into a busy child can be accepted by the CLI and still never arrive:
  one run sent three fix rounds in a batch, two landed, and the third
  (`impl-t3-enchash`, mid-turn) vanished with a zero exit — the pipeline sat
  waiting on a fix that had never been asked for, and only a manual retry
  recovered it. A dropped fan-out send fails a launch loudly; a dropped
  fix-round send stalls one pipeline in silence, which is the failure you will
  not notice.

  **A zero exit is not arrival.** After any fix-round send, confirm the child
  actually moved — `agent-deck session children <conductor-id>` showing that
  target transition to `running`, or a fresh response in `session output` —
  before you record the round as sent and move on. Record the confirmation
  signal in the manifest next to the round. A send you never confirmed is an
  open question, not a completed stage.

  **Two strikes on an evidence-free response.** A fix report without the
  diff, sha and `git show --stat` the prompt demands, a done sentinel with no
  commit on the branch, or a "fixed" with no hunk to show for it is an
  evidence-free response, not a completed round. On the first, `session
  send` one focused retry that names exactly the missing evidence and
  nothing else. On the second, stop sending: rotate the worker (a fresh
  session in the same worktree, fix prompt prefixed by the rotation
  instruction, `deviation: implementer replaced after two evidence-free
  responses` in the manifest) or, if the counted budget is spent, mark the
  task needs-attention. Never take over its production or verification work
  yourself — a conductor that fixes one finding by hand has stopped
  supervising, and the branch now has a change no reviewer session saw.

  A nonzero send result is not permission to send the same fix twice. If the
  child subsequently emits a response attributable to that message, or its
  state transitions from idle/waiting to running after the send, record the
  attempt in the manifest as `delivered, confirmation uncertain` and continue
  without resending. If neither signal exists, keep the CLI's failure verdict
  and retry only after confirming the composer is safe. This distinct status
  preserves transport uncertainty without manufacturing duplicate work.

- When the implementer is done, launch the next fresh reviewer
  (`review-<task-slug>-r2`, then `-r3`) with the same `--disallowedTools`
  flags. **Rounds 2+ are full-branch rounds with the prior findings
  attached** — there is no separate end gate, so every round after the first
  must be able to certify the whole branch. It reuses the same findings file
  the fix round was built from:

```bash
bash "$RUN_DIR/prompts/render.sh" review-round "$RUN_DIR/<slug>/review-r<n+1>-prompt.md" \
  VERDICT_FILE="$RUN_DIR/<slug>/review-r<n+1>.md" \
  SPEC_BLOCK@="$RUN_DIR/<slug>/spec-block.md" \
  BASE_REF=<base-branch, or the subtask start sha in relay mode> \
  REVIEWED_SHA=<reviewed-sha> PREVIOUS_FINDINGS@="$RUN_DIR/<slug>/findings-r<n>.md" \
  BASELINE="<shared manifest baseline plus task-specific delta, or none>" \
  FOCUSED_TESTS="<the contract's focused-test command for the paths in git diff <reviewed-sha>...HEAD>" \
  AGENT_DECK_REPO=<agent-deck-repo>
```

  It carries the same read-only contract and verdict format as the full
  review: the full suite starts detached first, the layers take `git diff
  <reviewed-sha>...HEAD` as their focus and `git diff <base-ref>...HEAD` as
  their scope, the focused tests run in the foreground, every unfixed prior
  finding is a new finding, and already-dispositioned items are not
  re-raised. That last clause is what replaced the old incremental-then-gate
  pair: incremental rounds passed code a later full gate flagged, and the
  gate then re-litigated deferred work, so tasks burned two extra sessions
  to end where one round could have ended them.

  Once you have read the previous round's findings, **delete the
  superseded reviewer** (see "Deleting finished sessions").
- **`VERDICT: clean` from **any** round is terminal for the task** — round 1
  or any later round — because every round reviews the whole branch with the
  full suite run fresh. Proceed to the PR. A repeated in-scope defect across
  rounds is reviewer oscillation and escalates the reviewer tier; preventive
  or adjacent scope is never smuggled into the branch merely because a late
  round mentioned it — such findings get a one-line disposition in the
  manifest (`fix`, `defer`, or `separate issue` with an issue identifier)
  and only `fix` enters the fix prompt.
- **A blind A/B regression is a blocking review finding** (see "Blind A/B
  judge" in stage 2), and a UI task's terminal clean review also needs a reveal
  with `regressions=0`.
- **The durable state is the budget authority.** Launch every review or fix
  with [`review-attempt.sh`](review-attempt.sh), which atomically reserves the
  attempt through [`review-state.sh`](review-state.sh) before rendering and
  launching. Use one stable `TASK_ID`, `BASE_HEAD`, `REVIEWED_HEAD`, `SPEC_ID`,
  and lineage across retries. Branch only on `allowed`, `already-recorded`, or
  `needs-attention`; do not infer budget from prose or session titles.
- **Default cap: three completed reviews and two completed automatic fixes per
  epoch.** Transport failures may use only the script's bounded startup retry.
  Quota outcomes park until a known reset; unknown reset is
  `needs-attention`. A fourth review is reserved for a material integration
  boundary and is allowed only when the state script enforces that identity.
  Further work requires an explicit recorded operator override; never reset
  the counters or mint a replacement task id.
- Budget exhaustion with blocking findings or a user decision still open makes
  the task **needs-attention**, with no PR. Preserve its sessions, worktree,
  branch, receipts, and findings for inspection.

### 4. PR

**When the repo prescribes its own endgame.** Stages 4 and 5 below are
written for GitHub and `gh`. Plenty of repos aren't: a GitLab project wants
`glab` and a merge request, and a repo's own `CLAUDE.md` / `CONTRIBUTING.md`
may prescribe something else entirely — *merge the feature branch into `dev`*
is a common one, with no pull request at any point. **The repo's stated
workflow wins over this skill.** Read it during run setup rather than
discovering it at the finish line, and when it diverges, confirm the endgame
with the user **once** — "this repo's CLAUDE.md says merge into `dev` rather
than open a PR; I'll follow that" — then follow it for every task in the run.
Improvising a whole endgame per task, at the end, when a run's context is
already at its largest, is how a finished task fails to land.

Everything upstream of the endgame is unchanged: dedicated worktree, implement,
review to clean, pre-merge sync, full suite plus build checks. What varies is
only the last hop and how "green" is observed, so translate rather than skip —
`gh pr checks` becomes the pipeline status that host offers, and a merge-based
endgame still requires the same clean review and the same green checks before
the merge, plus a note in the manifest of the sha you merged.

The branch was cut when the task started and the base may have moved since.
`session send` the implementer a pre-PR sync step: fetch and merge the
current <base-branch>, resolve any conflicts preserving both sides' intent,
then rerun the full test suite **and the build/vet checks** — auto-merges
can compile-and-be-wrong (duplicated route handlers, duplicate test
function names) — commit the merge, and push the branch (it knows the
remotes; on forks that means the fork remote). The implementer then creates
the PR from the worktree:

```bash
cd <worktree-path> && gh pr create --base <base-branch> --title "<title>" --body "<body>"
```

On a fork setup, be fully explicit or `gh` stops to ask questions
interactively — which hangs a non-interactive conductor: add
`--repo <upstream-owner>/<repo>` and `--head <fork-owner>:<branch>`.

The body covers what changed, why, and how it was verified, plus
`Fixes #<n>` for issue-sourced tasks. It must contain **no screenshot paths,
no run directory, no orchestrate/session details**.

### 5. CI babysit

On a repo with a prescribed non-GitHub endgame, translate the commands here
to that host's equivalents; the loop itself is unchanged.

Maintain a cheap `inspect` child for open PRs. On every heartbeat, have it run
`gh pr checks <pr-url>` and write a compact status artifact. On a failure,
have that child pull the failing details (`gh pr checks`,
`gh run view <run-id> --log-failed`) into the task directory, then
`session send` the artifact path to the still-alive implementer to fix and
push. The conductor reads only the deciding green/red summary and routes the
result.
A mechanical fix (lint, format, flaky rerun) pushes directly; a fix that
touches logic gets one `review-round` on the new commits
(`<reviewed-sha>` = the sha the last clean review saw) before the task can
count as done. When a sibling PR from this run merges, rerun stage 4's
pre-PR sync (fetch/merge base, full suite + build checks, push) for every
still-open PR — the base just moved under them. A task counts as **done**
only when the review verdict is `clean`, the PR exists (or the
prescribed endgame has landed), and all checks are green.
