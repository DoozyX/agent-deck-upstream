## Failure handling

A task that cannot pass its tests, exhausts its 3 review rounds with `patch`
or `decision-needed` findings
remaining, or cannot reach green CI after a few fix attempts is reported as
**needs-attention**: leave its session and worktree fully intact for
inspection, and never force-push, reset, or delete anything.

## Deleting finished sessions

The moment a child is no longer needed, **delete** it — don't archive it, and
don't leave it cluttering the active list:

```bash
agent-deck session remove <id> --force
```

`session remove --force` kills the pane and drops the session from the
registry outright, whatever state it is in; no separate `stop` and no
`archive` step. It is registry-only: the child's Claude transcript under
`~/.claude/projects/` and its git worktree both survive, so the work stays
inspectable — a run that archived every child instead accumulated 1845 dead
rows in a single month, which is why this is a delete and not an archive.
Delete:

- a **reviewer** once you've read its verdict and are moving on (launching the
  next round, or proceeding to the PR);
- a **planner** (and its plan-reviewer) once the plan review's findings have
  been applied — or, when the plan review is skipped as a single-implementer
  plan, as soon as the plan and its task files are in the task directory;
- the **implementer** at task-done cleanup (below);
- the **cleanup** and **verify-cleanup** children once their verdict is read;
- the **A/B judge** once its reveal line is read.

**Never delete a needs-attention task's sessions** — those stay live and fully
intact for inspection (see "Failure handling"). The rotating **conductor** is
the one exception that still archives itself (`rotate-conductor.sh`), and only
once its successor has passed the liveness gate — a rotation that cannot bring
up a live successor leaves the conductor unarchived. So the handoff chain stays
readable after the run.

## Cleanup (successful tasks only)

When a task reaches **done** (review clean, PR created, checks green), delete
its finished sessions as an orchestration action. Delegate repository and run
cleanup: render `cleanup-execute`, launch exactly one cleanup child from the
task's `$RUN_DIR` directory, and give it the exact candidate list from
`$RUN_DIR/worktrees.tsv`. The pushed remote branch backs the PR, so nothing
local is still needed. Cleanup stays serial because worktrees and branches
share repository-wide Git metadata.

`BASE_REF` is the ref the work actually landed on, and on a direct-merge
endgame that is **`origin/<target>` after a `git fetch`, never the primary
checkout's local branch**. Merge children push `HEAD:refs/heads/<target>` from
a detached worktree, so the primary's local `main` stays where the run found
it; a cleanup child handed `BASE_REF=main` finds no candidate merged into it
and refuses the whole list — correctly, and uselessly. A PR endgame has the
same shape: the base is `origin/<base-branch>` after the merge, not a local
name.

```bash
agent-deck session remove <id> --force
bash "$RUN_DIR/prompts/render.sh" cleanup-execute \
  "$RUN_DIR/cleanup-execute-prompt.md" \
  REPO_ROOT=<repo-root> BASE_REF=<base-ref> \
  CANDIDATE_FILE="$RUN_DIR/worktrees.tsv" \
  RESULT_FILE="$RUN_DIR/cleanup-result.tsv"
agent-deck launch "$RUN_DIR" -c claude -t "cleanup-<run-id>" \
  --inherit-group \
  --message-file "$RUN_DIR/cleanup-execute-prompt.md"
```

For pre-existing branches or worktrees not already recorded in the manifest,
first launch an `inspect` child to produce an exact candidate TSV. Do not let
the cleanup child discover or broaden its own targets.

After the cleanup child asserts completion, launch a fresh read-only child
with `cleanup-verify`. It independently checks the candidate list, cleanup
result, base ancestry, remaining registrations and branches, and the main
checkout's status. Cleanup is complete only on `VERDICT: clean`; preserve all
state and report `VERDICT: fix-needed` otherwise.

```bash
bash "$RUN_DIR/prompts/render.sh" cleanup-verify \
  "$RUN_DIR/cleanup-verify-prompt.md" \
  REPO_ROOT=<repo-root> BASE_REF=<base-ref> \
  CANDIDATE_FILE="$RUN_DIR/worktrees.tsv" \
  RESULT_FILE="$RUN_DIR/cleanup-result.tsv" \
  VERDICT_FILE="$RUN_DIR/cleanup-verdict.md"
agent-deck launch "$RUN_DIR" -c claude -t "verify-cleanup-<run-id>" \
  --inherit-group \
  --extra-arg --disallowedTools --extra-arg "Edit,Write,NotebookEdit" \
  --message-file "$RUN_DIR/cleanup-verify-prompt.md"
```

A separately scheduled host-maintenance job, outside the conductor, may sweep
old run-owned worktrees and build caches after all task sessions have been
deleted. Configure the repository-local root explicitly:

```bash
AGENTDECK_ORCHESTRATE_DIR="$ROOT_WT/.agent-deck" \
"<agent-deck-repo>/skills/orchestrate/references/cleanup-runs.sh" \
  --days 7 --apply
```

The collector reads `$RUN_DIR/worktrees.tsv`, refuses runs with live sessions,
preserves reports/screenshots, and skips worktrees with tracked or staged
edits. For any task that needs human attention, create
`$RUN_DIR/.needs-attention` before cleanup. Active and marked runs remain
protected. Run it without `--apply` to preview.

The cleanup executor takes `<worktree-path>` and the exact `<branch>` name from
`$RUN_DIR/worktrees.tsv` and verifies both against `git -C <repo-root>
worktree list` before mutation. The conductor never performs manual cleanup.

If review feedback arrives on the PR later, recreate a worktree from the
remote branch. **Needs-attention tasks are the exception**: leave their
session, worktree, and branch fully intact for inspection.

Once every task is done or parked and the final report is written, retire the
wall-clock watchdog — otherwise it keeps nudging you into pointless turns
until it times itself out:

```bash
touch "$RUN_DIR/.heartbeat-stop"
```

The same stop file retires the watchdog detector; delete the watchdog child
with the other finished children (its id is in `$RUN_DIR/.watchdog-id`).

Do this **last**, after the report. A run that stops its own heartbeat while
work is still live has removed the only thing that would have noticed it going
quiet.

## Teardown gate

Before the final report, prove the run left nothing behind. Per-task cleanup
covers the success path and deletes exactly the worktrees a task recorded, so
by itself it is not evidence that the host is clean — it is evidence that the
tasks that finished cleanly were cleaned up. Run the gate from the run
directory:

```bash
bash "$RUN_DIR/teardown-gate.sh" --repo <repo-root>
```

It is read-only and it never deletes: it prints `VERDICT: clean` and exits 0,
or lists every leftover and exits 1. Three things it looks for, each a leak
that nothing else on the host collects:

- **sessions** whose title ends in this run's id and are still unarchived —
  a round abandoned mid-flight leaves children the per-role deletions never
  reached;
- **worktrees** under `$WORKTREES_DIR` whose name starts with this run's id.
  It reads `git worktree list`, not `worktrees.tsv`, and says for each whether
  the tsv knew about it. A checkout made outside `create-worktree.sh` is in no
  tsv, so no cleanup child was ever given its path — that is how eight of nine
  stale worktrees survived in agent-deck itself;
- **session scratch** under `<repo>/.agent-deck/tmp/<session-id>` with no
  registry row. That directory is removed by the session's remove lifecycle
  intent; a killed pane or a pruned row skips it and the space is leaked for
  good. `cleanup-runs.sh` cannot help — it only walks `<run>/orchestrate/`
  layouts.

On residue, hand the printed list to a cleanup child exactly as you would a
`worktrees.tsv` — never delete anything yourself — then re-run the gate. Ship
the report only on `VERDICT: clean`, or state the leftovers in the report as
open items. For a run parked for a human, `touch
"$RUN_DIR/.needs-attention"` first: the gate then prints its sessions,
worktrees and scratch as `KEEP` and passes, because that residue is the point.
