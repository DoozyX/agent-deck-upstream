## Run setup

Keep every run-owned artifact under the target repository's ignored
`.agent-deck/<run-id>/` directory. Keep source worktrees in the repository's
dedicated `.worktrees/` directory:

```bash
ROOT_WT=$(git -C <repo-root> worktree list --porcelain | awk '/^worktree /{print $2; exit}')
# For an approved design, preserve the run id established by brainstorming.
SPEC_PATH=<absolute-design-path-or-empty>
if [ -n "$SPEC_PATH" ]; then
  RUN_ROOT=$(cd "$(dirname "$SPEC_PATH")/.." && pwd)
  RUN_ID=$(basename "$RUN_ROOT")
else
  RUN_ID=<date>-<slug>
  RUN_ROOT="$ROOT_WT/.agent-deck/$RUN_ID"
fi
DESIGN_DIR="$RUN_ROOT/design"
PLAN_ROOT="$RUN_ROOT/plan"
RUN_DIR="$RUN_ROOT/orchestrate"
WORKTREES_DIR="$ROOT_WT/.worktrees"

case "$SPEC_PATH" in
  "") ;;
  "$DESIGN_DIR/design.md") ;;
  *) echo "design must be $DESIGN_DIR/design.md" >&2; exit 2 ;;
esac

# Keep run state local without changing the repository's tracked .gitignore.
EXCLUDE_FILE=$(git -C "$ROOT_WT" rev-parse --path-format=absolute --git-path info/exclude)
grep -qxF '/.agent-deck/' "$EXCLUDE_FILE" || printf '/.agent-deck/\n' >> "$EXCLUDE_FILE"
mkdir -p "$DESIGN_DIR" "$PLAN_ROOT" "$RUN_DIR"
git -C "$ROOT_WT" check-ignore -q "$RUN_ROOT/.probe"
```

Resolve the user's tool policy once, persist it with the run, and consult it
before every child launch:

```bash
agent-deck config orchestrate > "$RUN_DIR/tool-policy.json"
TOOL_STRATEGY=$(jq -r '.strategy' "$RUN_DIR/tool-policy.json")
DEFAULT_TOOL=$(jq -r '.fallback_tool' "$RUN_DIR/tool-policy.json")
AVAILABLE_TOOLS=$(jq -r '.available_tools | join(", ")' "$RUN_DIR/tool-policy.json")
```

- Empty strategy is the backwards-compatible legacy policy: keep the explicit
  connector shown by the workflow recipe.
- `default` means every non-explicit launch uses `$DEFAULT_TOOL`.
- `auto` means choose separately for each role from `.available_tools`, using
  the capability table in the `agent-deck` skill and the task's actual needs.
  Use `$DEFAULT_TOOL` when it is available and there is no concrete reason to
  prefer another connector. If it is unavailable, select an available tool and
  record that fallback.
- An explicit workflow choice (for example the cross-provider Codex reviewer)
  overrides the policy.
- Before launching, append `role=<role> tool=<tool> reason=<one line>` to
  `$RUN_DIR/manifest.md`. Automatic selection that is not recorded is not a
  selection; it is hidden drift.
- Connector flags move with the connector. `LEAN` is Claude-only. Build a
  role-specific argument array for another connector rather than passing
  Claude flags to it. Set the recipe's role variable (`PLANNER_TOOL`,
  `IMPLEMENTER_TOOL`, `REVIEWER_TOOL` or `JUDGE_TOOL`) and its matching `*_ARGS` array
  immediately before the launch. For a Claude reviewer, `REVIEWER_ARGS`
  includes the lean flags plus `--disallowedTools`; for a Codex reviewer it
  uses `--sandbox read-only` instead, as shown under connector tiering.

`git worktree list` prints the main worktree first — that first entry is the
root worktree even when you are running inside a worktree yourself.

The layout keeps run metadata in one ignored tree and source checkouts in the
repository's worktree tree:

```text
<repo-root>/.agent-deck/<run-id>/      = $RUN_ROOT
  design/
    design.md                           approved design
  plan/                                 = $PLAN_ROOT
    <task-slug>/
      plan.md  tasks/task-NN-<name>.md  planner output
  orchestrate/                          = $RUN_DIR
    goal.md  manifest.md  poll.sh  heartbeat.sh  prompts/  retro.md
    <task-slug>/                        spec blocks, prompts, reviews, screenshots, handoffs

<repo-root>/.worktrees/                = $WORKTREES_DIR
  <run-id>-<task-slug>/                run-owned source checkout
```

Every checkout path is exactly
`$WORKTREES_DIR/$RUN_ID-<task-slug>` and is recorded in
`$RUN_DIR/worktrees.tsv` by `references/create-worktree.sh`. Never create a
run artifact or retrospective outside `$RUN_DIR`. Never create a source
checkout outside `$WORKTREES_DIR`. Never create a design, plan, task file,
prompt, review, report, or retrospective outside `$RUN_ROOT`. The one
run-independent artifact is the cross-run diary at
`$ROOT_WT/.agent-deck/diary.md`, written only by the retrospective child.

**No child of this run works in a primary checkout — including the ones that
are not tasks.** Implementers get a worktree because the recipe hands them
one; deploy, build, merge and cleanup children are launched ad hoc and will
happily run wherever you point them, which is how a deploy child once
fast-forwarded a repository's primary checkout 15 commits. That one was 0
ahead so nothing was lost, but a primary checkout is the one place a person's
uncommitted work lives, and it is not yours to move. Give every such child its
own worktree from `create-worktree.sh` — a deploy child needs a tree to build
from, not *the* tree — and pin the primary around it:

```bash
GUARD="$RUN_DIR/primary-checkout-guard.sh"   # copied in with poll.sh below
sh "$GUARD" snapshot --repo <repo> --run-dir "$RUN_DIR" --label deploy-<repo>
# ... launch the deploy/build/merge child, wait for it to report done ...
sh "$GUARD" verify   --repo <repo> --run-dir "$RUN_DIR" --label deploy-<repo>
```

Verify **before deleting the child**, while its worktree and pane still exist
to diagnose from. A non-zero verify is a run deviation: record what moved in
the manifest, restore the primary, and find out which child did it before
launching another. Merge children push with
`git push HEAD:refs/heads/<branch>` from a detached merge worktree for the
same reason — nothing needs a primary checkout checked out to land work.

**Establish the environment's hazards once, here, before the first child.**
Write `$RUN_DIR/environment-hazards.md` and pass it **by path** into every
child prompt from then on. What belongs in it is anything a child could only
learn by hitting the wrong wall: which container/cluster context is
production and which one it may build on (named explicitly on every command,
because the *active* context has been production); tunnels and services
already up, so a child does not go looking for a daemon it does not need;
credentials that must not be echoed; hosts that are not this machine. Two
such facts cost real time on the run this rule came from, and both were
established mid-run by the conductor only *after* a child had already skipped
a mandatory test suite believing it needed a Docker daemon it did not have.
An hour of recon at run start is cheaper than one child discovering
production the hard way.

**Freeze the goal first, before anything else lands in the run directory.**
You received the run's goal in your launch message and nowhere else: the
brainstorm's `conductor-prompt.md`, the user's `/orchestrate ...` turn, an
issue list. That message does not survive you. A successor conductor is
launched on the manifest and the handoff, and neither carries the goal — a
rotated run has been observed carrying on with every task row intact and
nothing that said what the run was *for*. So write `$RUN_DIR/goal.md` now,
and `rotate-conductor.sh` refuses to rotate without it and pastes it verbatim
into every successor's prompt.

**If `goal.md` already exists, keep it.** The brainstorming skill's
orchestrated exit writes it before launching you, because that session is
the only one holding the user's ask in their own words; yours is the generic
launch message. Read it, fill any field it left blank by appending, and
never rewrite what is there. Write the file from scratch only when it is
missing:

```markdown
# Goal
<the user's request, verbatim — quote it, do not paraphrase it. For a
 design entrance without a brainstorm-written goal.md, quote the design's
 motivation and acceptance sections instead, by absolute path and verbatim>

entrance: <design | plan | issues | freeform | verification>
inputs: <SPEC_PATH, issue refs, or "none">
done means: <what has to be true for this run to end — merged PR(s) on
            which branch, a verification verdict, a report to whom>
report to: <where the final report goes and who reads it>
user constraints: <anything the user said that bounds the work; "none">
```

`goal.md` is append-only after this point. A scope change the user approves
goes under `## Approved changes` with the date and who approved it; the
original text above it is never edited, so every successor can see both what
was asked and what was later agreed.

Populate the run directory:

```bash
cp <agent-deck-repo>/skills/orchestrate/references/poll.sh "$RUN_DIR/"
cp <agent-deck-repo>/skills/orchestrate/references/rotate-conductor.sh "$RUN_DIR/"
cp <agent-deck-repo>/skills/orchestrate/references/heartbeat.sh "$RUN_DIR/"
cp <agent-deck-repo>/skills/orchestrate/references/command-timeout.sh "$RUN_DIR/"
cp <agent-deck-repo>/skills/orchestrate/references/supervisor-observe.py "$RUN_DIR/"
cp <agent-deck-repo>/skills/orchestrate/references/supervisor.sh "$RUN_DIR/"
cp <agent-deck-repo>/skills/orchestrate/references/review-state.sh "$RUN_DIR/"
cp <agent-deck-repo>/skills/orchestrate/references/review-attempt.sh "$RUN_DIR/"
cp <agent-deck-repo>/skills/orchestrate/references/primary-checkout-guard.sh "$RUN_DIR/"
cp <agent-deck-repo>/skills/orchestrate/references/teardown-gate.sh "$RUN_DIR/"
cp -R <agent-deck-repo>/skills/orchestrate/references/prompts "$RUN_DIR/"

# Bind supervision to this conductor, then start one deterministic local loop.
agent-deck session show --json | jq -r '(.data // .).id' > "$RUN_DIR/.conductor-id"
bash "$RUN_DIR/supervisor.sh" start "$RUN_DIR"

# Lean child launch flags — see "Child startup baseline" under Context budget.
# Drop them for a child that must drive a browser.
LEAN=(--extra-arg --strict-mcp-config --extra-arg --mcp-config
      --extra-arg '{"mcpServers":{}}')
```

The optional watchdog template is only for an explicitly approved bounded
safe-prompt/stall helper. If used, give it the narrow allowlist in
[`prompts/watchdog.md`](prompts/watchdog.md) and write its id to
`$RUN_DIR/.watchdog-id`. With no id, deterministic supervision remains fully
supported and escalates conductor choices to the operator without model wakes.

Everything any child captures goes under `$RUN_DIR/<task-slug>/`; planner
output goes under `$PLAN_ROOT/<task-slug>/`; and the approved design is under
`$DESIGN_DIR/`. These paths are not `/tmp`, where they collide across runs or
vanish on reboot, and are not child worktrees, where they are one `git add -A`
away from a PR. Nothing under `$RUN_ROOT` is ever committed, pushed, uploaded,
or mentioned in a PR or commit message.

The run directory is inside the repository root but excluded through Git's
local `info/exclude`, so it cannot enter a diff and does not require a tracked
`.gitignore` change. Children reach plans, task files and screenshots only
through the absolute paths handed to them. Never run `git clean -x` or another
ignored-file cleanup against the repository while a run exists.

`supervisor.sh` performs change-only observation and wake delivery;
`rotate-conductor.sh` replaces the conductor when context runs out. `poll.sh`
and `heartbeat.sh` remain available for legacy/manual diagnosis, not as a
second concurrent supervision loop. `prompts/` holds every child template plus
`render.sh`, which fills them; read only the template needed for the current
stage.

**Nothing in this run ever asks the user to type a slash command.** A
conductor that ends its turn asking for `/compact` stalls the entire run until
somebody notices, and nobody is watching. Every remedy in this skill is a
shell command you run yourself, unattended — including compaction, which is
`agent-deck session compact` (see "Thresholds"), not a `/compact` you ask for.

Launch an `inspect` child to read the target repo's `CLAUDE.md` and
`CONTRIBUTING.md` and write `$RUN_DIR/landing-policy.md`, for the one thing
this skill cannot know: how work is expected to *land* there. Read only its
deciding summary line. If the repository prescribes an endgame other than a
GitHub PR, that changes stages 4–5 — see "When the repo prescribes its own
endgame".

**Then settle the landing policy with the user, at triage, before a single
branch is cut, and record it in the manifest as a contract.** Two things, per
repo, both of them decided and neither of them defaulted:

- **Mechanism** — pull request, or direct merge into an integration branch?
- **Target** — *which* branch, by name, for each repo in the run?

Do not infer either from the inspect child's summary and proceed; that summary
is input to the question, not an answer to it. A landing policy chosen by
default is the cheapest decision in the run to get wrong and the most expensive
to reverse: one run opened six pull requests across three repos on the inspect
child's default before the user said "merge into the integration branch
instead", and all eight open PRs had to be declined and re-landed. Rewriting an
endgame costs a PR per task; asking one question costs one turn.

Write the answer into the manifest next to the verification contract, as
`## Landing policy`, with one line per repo: repo → mechanism → target branch.
Every task in the run reads it from there. A task that wants to deviate (a
finding that says a PR is the right endgame for this one change) is a conductor
decision recorded as a deviation, not a silent per-task improvisation.

Maintain a run manifest at `$RUN_DIR/manifest.md` and update it after every
stage transition. The manifest is state, not purpose: the goal lives in
`$RUN_DIR/goal.md` (frozen at run start, above) and the manifest never
restates it, so the two cannot drift. Start it with one shared
`## Verification contract` block:
the exact baseline, full-suite, lint/format, build/vet and E2E commands, plus
the focused-test command shape (how to test only the packages or paths a
diff touches — fix rounds and review rounds 2+ render it as `FOCUSED_TESTS=`); each
command's required services, credentials and fixtures; who owns that
infrastructure; the known environment-dependent failures; and the **known
test-generated drift files** — tracked files the suite itself rewrites
(`Package.resolved` under `swift test`, a regenerated lockfile, a snapshot
directory). Reviewers are read-only by design and must not restore them, so
the drift legitimately survives to the endgame: the merge child, as sole
occupant, restores exactly the listed files (`git checkout -- <file>`) before
it merges, and the cleanup executor still refuses any other dirt. Where the
runner can be told not to write (`swift test --disable-automatic-resolution`,
`npm ci`), put that in the contract's test command instead and leave the list
empty. Children cite that block instead of rediscovering or paraphrasing the
same constraints. A task may append a task-specific exception, but must not
silently replace the shared contract.

Then record per task: slug, base ref and resolved base sha, branch, worktree
path, verified launch HEAD and merge base, session ids with each session's
connector + model (and any escalation), current stage, review round, the HEAD
sha each review round saw, per review round `launched=<unix> done=<unix>
span=<s>` (from `session children --json`, so the next run can be compared
against this one's round times), the `AB_SUMMARY:` line of each blind A/B
judge run on a UI task, PR url. If
the conductor session dies, a fresh session can resume the run from the
manifest plus `session children <old-conductor-id>` — but the surviving
children are still parented to the dead session, so first re-parent them
(`agent-deck session set-parent <child> <new-conductor-id>`) so waiting/done
notifications and the turn-start snapshot route to the new conductor.
