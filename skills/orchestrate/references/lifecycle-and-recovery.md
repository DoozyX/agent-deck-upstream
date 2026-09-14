## Context budget

### Child startup baseline

A child pays for its *configuration* on every turn, not once. Measured over
688 orchestrate children (Jul–Aug 2026): median context **before the child
does anything** was 51k, and multiplying each child's startup size by its turn
count accounts for **37% of every input token those 688 sessions billed**
(3.87B of 10.43B). It is cached, so the dollar cost is a tenth of the headline
— but it is not compressible at all, and it is a quarter of the window gone
before the task starts. That is why the median child peaked at 149k and 22%
crossed the soft threshold.

Three components, in the order worth attacking:

- **MCP tool listings — ~5.6k/session, measured.** Children inherit every
  globally-registered MCP server. In the sample, 106 of 120 children carried
  `finance-local`, 105 carried `claude-in-chrome`, and ~34 carried Gmail,
  Google Calendar and Google Drive — into backend implementer sessions that
  could never use them. The Claude `LEAN` array (see "Run setup") drops all of it:
  `--strict-mcp-config --mcp-config '{"mcpServers":{}}'` disables file-based
  *and* plugin-supplied MCPs. Verified: 34,820 → 29,238 tokens at startup.
- **The repo's `CLAUDE.md` — 1k to 8.4k/session.** One repo in the sample
  shipped a 33.5kB `CLAUDE.md`; its children started at 57k against 43k for
  the leanest repo. You cannot fix this mid-run, but it belongs in the retro:
  a `CLAUDE.md` is read by every child of every future run.
- **~29k harness floor** — system prompt, core tools, skill listings. Not
  yours to change.

**For every Claude child, initialize its role-specific argument array from
`"${LEAN[@]}"` except when it must drive a browser.** For other connectors,
use that connector's equivalent flags when supported or an empty array.
`--strict-mcp-config` takes playwright and chrome-devtools with it. A UI
implementer or a reviewer that reproduces UI behaviour launches without it;
planners, reviewers of non-UI work, the blind A/B judge (it reads images with
the file tool), fix children, merge and integration children never need the
browser MCPs at all.

### Children

Every Claude child row in `session children --json` carries `context_tokens`
— the child's current context size, read from its transcript. Check it on
every heartbeat and act on two thresholds:

- **Soft (~200k):** `session send` a wrap-up instruction — "your context is
  getting large: commit what's done, then write a handoff summary of what
  remains (decisions made, files touched, next steps) to
  `$RUN_DIR/<task-slug>/handoff.md`."
- **Hard (~250k):** stop feeding it work. Delete the child and launch a
  **fresh session in the same worktree** — same move as the round-2
  escalation in "Model & connector tiering" — told to read `git log`, the
  branch diff, and the handoff summary before continuing. Record the rotation
  in the manifest (it counts as the same role, not a new review round).

You do not have to remember either threshold. `poll.sh` renders both every
beat a child is over, with the id already substituted into the command, and
exits non-zero at hard the way it does for your own context. Send what it
prints. Knowing the rule was measured to be insufficient: a child was told to
commit at 242k instead of 200k with seven dirty files and zero commits, and
the very next task's child then reached 259.8k with ten dirty files, zero
commits and a stranded composer. **The only variable between a clean stop and
a lost one was whether the nudge reached the child before its turn got long.**

Never let a child run to auto-compaction mid-task: a lossy summary of its own
half-finished work is strictly worse than a deliberate handoff. Reviewers
rarely trip this (each round starts fresh); implementers on big tasks do —
and a task whose implementer needs rotating twice was mis-sized, which is
worth a line in the retro.

**A child's self-written handoff is a claim, not a record.** Before you act on
one — and always before you brief a successor from it — check it against
physical facts: `git log --oneline`, `git show --stat` for each commit it
mentions, `git status --porcelain`, and the presence of any file it says it
did or did not create. An auto-compacted handoff in this pipeline described
its round as still in progress when it was finished, claimed "there is no
remaining implementation" when there was, and claimed a spec file "was
deliberately NOT added" when it existed at HEAD — three claims, all confidently
wrong, all in exactly the places that decided what to do next. The diff was
the only thing that recovered the true state. A handoff written by a child
that committed cleanly, on the other hand, has been reliable; the tell is
whether the tree was clean when it wrote.

### The conductor

Your own context is the one that grows without a natural end. A child's
context is bounded by its task; yours is bounded by nothing — you outlive
every child, and the default supervision loop re-reads the same unchanged
rows for as long as the run lasts. On a five-hour run, heartbeat polling
alone outweighs every review, every prompt and every finding put together,
and roughly all of it is state that did not change.

**The invariant: your context grows with decisions taken, never with time
elapsed.** The manifest is the run's state; your context is a cache of it.
A conductor that has supervised four idle hours should have paid almost
nothing for them. Four rules follow.

**0. Watch your own number through deterministic supervision.**
`supervisor.sh status` reports context-threshold events sourced from the same
`parent_context_tokens` field without waking a model for unchanged readings.
Do not acknowledge a threshold until its required flush/compact/rotate action
has completed.

If it reads `self=n/a (upgrade agent-deck: no parent_context_tokens)`, the
binary predates this field and you are flying blind — say so to the user, and
fall back to a fixed schedule instead of guessing: flush to the manifest and
refresh `conductor-handoff.md` at every task completion, and rotate
(`bash "$RUN_DIR/rotate-conductor.sh"`) every fourth one. A missing signal is
not a low reading.

**1. Observe by change, never by dump.** `supervisor.sh` projects each child to
decision-driving fields and creates events only when a material state changes.
Use `supervisor.sh status` for pending events. `poll.sh` remains available for
an explicit one-shot diagnostic and projects the same compact fields:

```text
4 children · 3 running 1 waiting · no change · self=63k
```

```text
!! SELF-CONTEXT 214k >= soft 200k — flush now, this turn, without asking: write everything unwritten into $RUN_DIR/manifest.md, then bring $RUN_DIR/conductor-handoff.md up to date (live tasks + their stage, open questions, anything in flight). Do NOT stop and do NOT wait for a human. Rotation at hard is then one command.
CHANGED impl-vacancy: idle/ok
GONE    review-vacancy-r1
3 children · 1 idle 2 running · ctx impl-picker=soft · self=214k
```

You copied it during run setup; you do not need to read it. Its knobs are
env vars: `SOFT`/`HARD` for child thresholds, `SELF_SOFT`/`SELF_HARD` for
yours, `POLL_CMD` to feed it canned JSON in a test.

**Never load a raw `session children --json` dump into conductor context—use
the supervisor status or the two diagnostic lookups instead.**
Measured across August 2026 runs: 282 raw dumps against 87 heartbeats, and 213
of those 282 were asking for something the heartbeat withholds by design.
`poll.sh` answers both directly, one line each, without disturbing the diff
state the next heartbeat needs:

```bash
bash "$RUN_DIR/poll.sh" ctx                    # exact tokens, every child, largest first
bash "$RUN_DIR/poll.sh" ctx impl-<task-slug>   # exact tokens, one child
ID=$(bash "$RUN_DIR/poll.sh" id impl-<task-slug>)   # bare id for send/output/remove
```

Reach for `ctx` when a child buckets to `soft` and you need the real number to
decide between a wind-down and a rotation — that is the whole reason the raw
dump kept winning. `id` exits non-zero on no match rather than printing an
empty string, so a typo'd title fails the command instead of silently
retargeting `session send` at nothing.

Two details in there are load-bearing, so don't "simplify" them away. First,
**`context_tokens` churns on every single poll** for any live child — diff on
it raw and nothing is ever "unchanged", which defeats the entire mechanism;
it is bucketed to `ok`/`soft`/`HARD` and reported in the tail, outside the
diff key. Second, `done_at` and `last_sent_at` churn the same way and are
excluded in favour of the `done_stale` boolean the supervision rules already
turn on. What you lose is precision you weren't using; what you keep is every
transition that changes what you do next.

**2. Findings yes, transcripts never.** Every large payload lands in a
`$RUN_DIR` file by shell redirection, and you read only the line that carries
the decision. You do still read a findings list in full — findings lists are
short, and judging severity yourself is the point (a finding whose blast
radius is *existing data*, introduced by *this branch*, is never a `minor`, no
matter what the reviewer graded it). What must never enter your context is
the reasoning around them.

| Read | Instead of | Do |
| --- | --- | --- |
| Reviewer verdict | `session output <id>` | the reviewer already wrote `$RUN_DIR/<slug>/review-r<n>.md`; read only the merged findings plus the `VERDICT:` / `Checked:` lines from it (or from the child's response — they are the same lines) |
| Fix-round prompt | retyping the findings | `sed -n '/^## Merged findings/,$p'` into a findings file, then `render.sh fix ... FINDINGS@=<that file>` — file to file, never through you. Extract that section; never `cat` the whole verdict file, which still holds the raw hostile layer output |
| Child prompt of any kind | a `cat > prompt.md <<'EOF'` heredoc | `render.sh` (see "Rendering child prompts") — the template body never enters your transcript |
| CI failure | `gh run view --log-failed` | inspection child writes `$RUN_DIR/<slug>/ci-<run-id>.log` plus a green/red summary; route the artifact path to the implementer |
| Waiting child's question | `session output <id>` | `agent-deck session output <id> -q \| tail -40` — there is no `--tail` flag |
| A stuck child's actual screen | `tmux capture-pane` | `agent-deck session output <id> --pane \| tail -40` — same content, no raw tmux, works for isolated-socket sessions |
| Anything large or genuinely unclear | reading and reasoning yourself | dispatch a subagent — it burns its own context and hands you back a summary |

Reuse the task's cheap inspection child for repeated heartbeat checks. Reserve
an additional ad hoc child or subagent for a genuinely separate big read — a
five-thousand-line CI log, or "why has this child been stuck for twenty
minutes".

**Never open an image.** You do not `Read` a screenshot, ever — not to check a
child's work, not to settle a UI question, not "just this one". Measured over
533 image-only turns, a screenshot costs a median of **1.4k tokens** (p75 2.0k,
p95 5.6k, worst observed 30.4k) — so the cost of any *one* image is small and
the reason for the rule is not the single read. It is that images arrive in
streaks: you open one to settle a question, then five more for context, and a
conductor already at 300k has no room for a habit that compounds. Judge by the
p95, not the median, because a full-page retina capture is exactly the kind you
reach for when you are trying to settle something. Screenshots are the
implementer's evidence and the final report's payload: you handle their
*paths*. The implementer's prompt requires it to describe in words what each
screenshot shows — that description is what you read. If a visual judgment
genuinely has to be made, hand the path to the user, or send it back to the
child that produced it. Do **not** dispatch a subagent purely to look at one
image: a subagent launch costs more than the ~1.4k the image would have, so
that trade only pays for a batch of them or for a genuinely hard question.
The same rule covers any binary or generated blob: PDFs, `dist/` bundles,
minified JS, lockfiles — those have no comparable measured ceiling and a
single one can be far worse than any screenshot.

### Recovery after compaction or rotation

After either event, complete this recovery sequence before making new task,
scope, or landing decisions. A compaction summary is not a substitute for
reloading the workflow and its durable state.

0. Read `$RUN_DIR/goal.md` and, in the handoff, append
   `restored goal (c<gen>): <one sentence, your own words>`. After a rotation
   the goal is already inlined in the prompt you were launched on; write the
   line anyway — it is the checksum the next successor compares against
   `goal.md`, and a conductor that cannot state the goal in one sentence has
   not recovered it. If `goal.md` is missing, that is the first thing to
   surface to the user: nothing below can be checked against a goal you do
   not have, and reconstructing one from task rows is how a run drifts.
1. Re-read this orchestrate skill, then read `$RUN_DIR/manifest.md` and
   `$RUN_DIR/conductor-handoff.md`. Restore the verification and landing
   contracts, live task stages, open decisions, and recorded user approvals.
   Preserve the absolute skill path and run directory in the handoff.
2. Recover the approved design constraints from the manifest's
   `## Design constraints` summary: absolute source path, scope, non-goals,
   acceptance criteria, testing decisions (seam and prior-art tests), and
   approved deviations. Keep this bounded summary
   current when a design decision changes. Do not read the full design into
   conductor context or reopen approved design decisions.
3. Run `bash "$RUN_DIR/supervisor.sh" status "$RUN_DIR"` to reconcile durable
   events for surviving children. If the design summary
   is missing, its source is unclear, or it may be stale, use an `inspect`
   child to re-read the approved design at `SPEC_PATH` and return a bounded
   summary; record it in the manifest before making design-dependent
   decisions. Continue supervision while that child runs. If the source is
   unavailable, report the blocker rather than reconstructing it from memory.
   For runs without a design, restore the recorded task or verification
   contract instead; do not invent a design requirement.

Do not relaunch surviving children or redo completed stages. A missing or
empty handoff requires reconciliation with durable run state and live child
status before relying on any claimed progress.

**3. Thresholds, tighter than a child's.** Anything long-lived — findings
lists, baselines, pending questions, PR urls, HEAD shas — goes into
`$RUN_DIR` the moment you learn it, so the run survives you losing context at
any point. Then:

- **Soft (~200k):** flush, then compact yourself — both in the turn the banner
  appears, without asking anyone.

  First, everything not yet written down goes into `$RUN_DIR/manifest.md`, and
  `$RUN_DIR/conductor-handoff.md` gets brought up to date: live tasks and the
  stage each reached, open questions, anything in flight. Compaction is lossy
  and you are about to run one, so anything still only in your head is about
  to stop existing. That file is also the precondition `rotate-conductor.sh`
  checks, which is what makes the hard threshold a single command rather than
  a scramble.

  Then, as the last thing you do in the turn:

  ```bash
  agent-deck session compact \
    --instructions "Keep: the run goal exactly as written in $RUN_DIR/goal.md, the absolute orchestrate skill path, run dir path, design source path, every live task and its stage, open questions, PR urls, HEAD shas." \
    --resume "Run directory: $RUN_DIR. Read goal.md first. Then re-read the orchestrate skill and follow Recovery after compaction or rotation: read manifest.md and conductor-handoff.md, restore approved design constraints through the bounded summary (delegate a refresh if needed), then reconcile live children with the heartbeat before new task decisions."
  ```

  With no id it compacts *you*. It returns immediately — a self-compact runs
  once your turn ends, so it reports `queued`, never `verified`, and that is
  correct rather than a failure. `--resume` is delivered by a detached watcher
  after the compaction is recorded, which is why you must not send yourself
  follow-up work by hand: **a message arriving while a compaction starts
  cancels it**, and you would carry on at full context with nothing reclaimed.

  The supervisor retains the threshold event until it is acknowledged after
  action. **You do not pause here and you do not ask the user.**
- **Hard (~250k):** rotate, unattended:

  ```bash
  bash "$RUN_DIR/rotate-conductor.sh"
  ```

  It refuses to run while `conductor-handoff.md` or `goal.md` is missing or
  empty (an automatic rotation into an empty handoff has been observed in the
  field, and the successor inherits the manifest with nothing about what was
  in flight; a rotation without a frozen goal has been observed too, and the
  successor carried on with every task row intact and no idea what the run
  was for), then launches your successor on the manifest plus the handoff
  with `goal.md` pasted verbatim into its prompt, **waits for it to prove it
  is alive**, and only then re-parents every live child so waiting
  and done notifications route to it, repoints deterministic supervision (and
  an optional watchdog when configured), and archives you. It is one command because the five-step prose version it
  replaces was measured across 12 real conductors: median peak 348k, and half
  the runs sailed straight past this line.

  **If the successor is dead on arrival, the rotation fails and you stay.** A
  session id is not a live conductor: on 2026-09-11 a successor was launched,
  ran for three seconds and died on a usage limit, and the script — which then
  checked only that an id came back — re-parented five live children onto the
  corpse and archived the one session that knew what was in flight. The gate
  rejects a queued successor, a terminated/fatal pane, and a successor that
  never remains live through the settle window. On rejection it exits with the
  predecessor still active: no child moves, no generation is burned, and
  supervision still points at the predecessor. Evidence lands in
  `$RUN_DIR/rotate-failure-c<gen>-<tool>.log`; tell the user and quote the
  reason. A quota-limited successor parks the rotation until its reset or an
  explicit operator decision; it never triggers the automatic cross-provider
  retry. An ordinary non-quota startup or liveness failure retains exactly one
  alternate-tool retry when the predecessor's tool differs.

  **`poll.sh` exits 3 from here on, every beat, until you go** — your
  heartbeat is a failing command now, not a warning you can read past. A
  non-zero heartbeat is not a bug to work around and not a reason to stop
  polling; it is the rotation instruction. The diff baseline still advances on
  a failing beat, so nothing is re-reported while you wind down.

Both numbers match the child thresholds. Your loss is the worse one when it
lands — a child that compacts loses one task; you lose supervision state for
every task at once, and there is no reviewer downstream of you to catch it —
but your soft remedy is also the cheaper one (write two files, no rotation, no
re-parenting), so you are not made to stop earlier. If agent-deck's own budget
handler rotates you first, **check the handoff directory is actually non-empty
before trusting it** — an automatic rotation has been observed producing an
empty one. It writes `$ROOT_WT/.agent-deck/handoff/<session-id>/PROMPT.md`, the
same repository-local tree as `$RUN_ROOT`. Its wrap-up instruction asks for the
goal restated verbatim at the top of that file, so a handoff it produces should
carry the goal as well as the state — if the one you inherit does not, treat it
as the degraded transcript-rebuilt case and recover the goal from
`$RUN_DIR/goal.md` before acting on anything else in it. Note that handler only
runs while the TUI is open (`internal/ui/context_budget_ui.go`), and no hook can
stand in for it: Claude's `PreCompact` hook can only allow or deny a compaction,
not inject anything into what survives one. So on a headless run these
thresholds and `rotate-conductor.sh` are the only thing standing between you and
a million-token conductor.
