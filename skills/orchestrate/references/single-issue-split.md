# Single-issue split mode

Read this only when orchestrating **one** big task you have decided to split.
Everything here reuses the per-task pipeline from `SKILL.md`; "stages 1–3"
means implement → fresh review → fix loop, "stages 4–5" means PR → CI babysit.
The end state is always **one branch, one PR**.

## Decompose

**If a plan exists** — the planning stage ran (spec-fed task), or you were
handed one (plan-fed task) — decomposition is already done: subtasks = the
plan's tasks, in plan order, with the plan's parallel-safe markings deciding
the topology below. Each implementer is pointed at its own task file as its
spec and reads nothing else for it; do not re-decompose or reorder. A
plan-fed task's plan may lack `tier:` tags; tier those tasks yourself from
the tier table in `SKILL.md`.

**Otherwise** split the issue yourself into 2–5 subtasks, each independently
implementable and testable, ordered by dependency. For each subtask write a
mini-spec: goal, likely files/areas, done criteria. The reason to split is
**context hygiene** — each session holds one small coherent job — not raw
speed.

Your own split is subject to the same gate as a planner's plan: it feeds 2+
implementer sessions, so it gets **one** plan review before any
implementation — see "Reviewing the plan" in `SKILL.md`. Write the mini-specs
to `$RUN_DIR/<issue-slug>/subtasks.md` and point the reviewer at that plus the
issue/spec; it is checking coverage, cross-subtask interface mismatch and
ordering, not the code you propose. Being the author of the split is not a
reason to skip it — every downstream reviewer will be handed one mini-spec as
ground truth and can't see the ones around it.

## Choose the topology

- Subtasks touch **clearly disjoint areas** (different dirs/layers, no shared
  files — e.g. backend vs web UI vs docs) → **parallel worktrees +
  integration branch**.
- Subtasks overlap, build on each other, or you are unsure → **stacked
  relay**. **Default to the stacked relay when in doubt.**

## Stacked relay (default)

Subtasks build on each other, so they land on one branch in order — but
"in order" means the *branch* is ordered, not that task N+1 waits for task
N's review to be clean. It waits only for N's implementer to commit. While
N is reviewed and fixed, N+1 implements on top of it; that overlap is the
whole point of this topology (one run started task 03 at 09:26 the morning
after task 02's implementer finished at 20:05, with nothing running between).

1. Create subtask 1's worktree with `references/create-worktree.sh` using
   `--task <issue-slug>-1 --branch <issue-branch> --base <base-branch>` —
   subtask 1 owns the issue branch itself; later subtasks stack on it,
   then launch its implementer at the returned path:
   `agent-deck launch "$WT" -c "$IMPLEMENTER_TOOL" -t "impl-<issue-slug>-1" "${IMPLEMENTER_ARGS[@]}" --message-file ...`
   using the stage-1 prompt template with the subtask's mini-spec. The role's
   connector-specific argument array follows "Child startup baseline" in
   SKILL.md; drop lean MCP flags for a subtask that drives a browser.
2. **Stack the next subtask the moment N's implementer reports
   `done_status=ok`** (commits exist; review has not started). Record N's
   HEAD as N+1's start sha, create N+1's worktree branched off N's branch
   (`<issue-branch>` for N=1, `<issue-branch>-<n>` after):
   `references/create-worktree.sh --task <issue-slug>-<n+1> --branch <issue-branch>-<n+1> --base <N's branch>`
   and launch N+1's implementer there. Its prompt starts with: "You continue
   work on a stacked branch. Read `git log --oneline -20` and the diff since
   <start-sha> before starting; commits before it belong to an earlier
   subtask under review and are context, not yours to change." Then the
   normal stage-1 template with N+1's mini-spec. Write
   `stacked-on=<task-n> start-sha=<sha>` on N+1's manifest line.
3. Run stages 1–3 (review, fix loop) for N in N's worktree, concurrently.
4. **N+1's review starts only when both hold: N is clean, and N+1's
   implementer has merged N's final HEAD.** When N goes clean, `session send`
   N+1's implementer: "merge N's branch (its final HEAD <sha>),
   resolve conflicts preserving both sides' intent, rerun the focused tests
   and build checks, commit the merge." Then update N+1's `start-sha=` to
   N's final HEAD and launch N+1's round-1 reviewer. A fix on N that changed
   an interface N+1 built on costs N+1 that merge and its reviewer catches
   the mismatch — that is the accepted price of the overlap.
5. At most **2 tasks ahead** of the last clean task may be in flight: while
   N is dirty, N+1 and N+2 may implement; N+3 waits for N to go clean. A
   deeper stack accumulates unreviewed code that every later merge inherits.
6. When N is clean, remove N's worktree and its branch, then fast-forward
   `<issue-branch>` to N's final HEAD: `git -C "$ROOT_WT" branch -f
   <issue-branch> <sha>` (a metadata move; it refuses while any worktree has
   the branch checked out, which is why the worktree goes first). The stack
   above still points at those commits.
7. Repeat until the last subtask is clean. Its worktree then becomes the
   issue worktree: fast-forward `<issue-branch>` to its HEAD as in step 6 and
   `git -C <last-worktree> switch <issue-branch>` (same commit, no file
   changes). The final integration check and the PR run there.

**Review scope in relay mode.** From subtask 2 on, the branch already carries
earlier subtasks' reviewed work — a reviewer told to judge the full branch
diff against one mini-spec would flag that work as "extra" or "missing".
Scope every review round for subtask N to `git diff <start-sha>...HEAD` by
passing `BASE_REF=<start-sha>` to `review-round` (round 1 uses `review-full`
with `BASE_BRANCH=<previous subtask's branch>`, which is the same range), and add
to the reviewer prompt: "Commits before <start-sha> are earlier,
already-reviewed subtasks of the same issue — context, not review scope."
The one true full-issue review runs once, after the final integration check
(below).

## Parallel worktrees + integration branch

1. Create the integration branch and worktree with
   `references/create-worktree.sh --repo "$ROOT_WT" --run-dir "$RUN_DIR"
   --run-id "$RUN_ID" --task <issue-slug>-integration --branch <issue-branch>
   --base <resolved-base-sha>`
   then print and record its branch, HEAD and merge base as required by the
   main skill before launching any child.
2. For each subtask, create its worktree branched **off the integration
   branch**:
   `references/create-worktree.sh --repo "$ROOT_WT" --run-dir "$RUN_DIR"
   --run-id "$RUN_ID" --task <issue-slug>-<n> --branch <issue-branch>-<n>
   --base <issue-branch>`
   then launch its implementer at that worktree path (plain path — the
   worktree already exists) and run stages 1–3. Cap: 10 concurrent subtasks.
3. As each subtask's review comes back clean, merge it:
   `git -C "$ROOT_WT/.worktrees/$RUN_ID-<issue-slug>-integration" merge <issue-branch>-<n>`
4. On merge conflict, do not resolve it yourself — launch a session in the
   integration worktree: "Resolve the in-progress merge conflicts preserving
   the intent of both sides, run the full test suite, and commit the merge."
5. As soon as a subtask is merged, clean up its now-redundant worktree and
   branch (the integration branch holds the commits):
   `git -C "$ROOT_WT" worktree remove "$ROOT_WT/.worktrees/$RUN_ID-<issue-slug>-<n>" && git -C "$ROOT_WT" branch -d <issue-branch>-<n>`
6. Continue until every subtask is merged and only the integration worktree
   remains.

## Final integration check

Both topologies: launch one last session in the issue worktree to run the
build and the FULL test suite on the combined result, do a quick e2e sanity
pass of the issue's overall behavior, fix only trivial integration breakage,
and commit. If it finds non-trivial breakage, treat it as findings: route to
a fix session and re-check (it consumes a counted fix round like any other).

Then, in relay mode, run the one full-issue review: one fresh reviewer
with the stage-2 round-1 prompt, the **whole issue** (all mini-specs / plan
tasks) as its spec, and the full branch diff. `VERDICT: clean` → PR; any
finding → the normal fix loop under the same counted / in-place rules and
caps as a single task. A verdict carrying only `defer` findings is `clean`
by construction — that is the reviewer's call to make, not yours to infer
from severities. (Parallel mode already reviewed each subtask branch in full
against its own spec, so skip this extra review unless the merges were
conflict-heavy.)

## PR

Run stages 4–5 once, from the issue worktree, on `<issue-branch>`: one PR for
the whole issue, CI babysat to green. Screenshot policy is unchanged — all
subtask screenshots live under `$RUN_DIR/<issue-slug>/` and appear only in
the final report. Once the PR is green, the issue worktree and local branch
are cleaned up per the SKILL.md cleanup rules (subtask worktrees are already
gone by then).
