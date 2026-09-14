## Advisory usage context

Before a multi-child launch wave, optionally run `agent-deck usage --all --json`
once when OpenUsage is available. Re-check only after a child reports the
existing `usage-limit` substate or before a later wave after that snapshot has
expired. This information may inform a recommendation, but never blocks a
launch, switches an account automatically, or overrides an explicit tool or
account choice. Do not query it during ordinary polling, focused single tasks,
or every-turn hooks; continue unchanged when it is unavailable.

**Requires:** everything `fleet` requires. Delivery/PR entrances additionally
require an authenticated `gh` for the target repo; verification-only work does
not.

**Read `skills/fleet/SKILL.md` first.** This skill builds on fleet and does
not restate its mechanics: launch flags, the `--parent`-not-`-p` pitfall,
group inheritance, deps-install-first for worktree children, `session
children` polling, `session send` / `session approve`, the done sentinel, and
long-prompts-via-file all come from there.

## When to use

The user wants deployed-system verification with a terminal `pass`, `defect`,
or `inconclusive` report, or wants tasks/issues taken end-to-end to green PRs.
A verification pass stops with no edits, PR, CI, or deployment. If they only
want to fan out children and supervise them, use `fleet`. If they want one
child for one job, use the sub-agent pattern in the `agent-deck` skill.

## Conductor rules

You (the session running this skill) are the **conductor**. Hard rules:

- **Delegate all task execution.** The conductor delegates all task execution
  to child sessions. It only decomposes and sequences work, launches and
  supervises children, routes decisions and results, maintains orchestration
  state, and reports outcomes. Task execution includes audits, research,
  planning, cleanup, implementation, testing, verification, review, merges,
  release work, and CI investigation. The conductor may execute only
  orchestration control-plane actions: `$RUN_DIR` setup and prompt rendering,
  agent-deck lifecycle and supervision commands, manifest bookkeeping,
  heartbeat/rotation, and concise result routing.
- **You never `Read` or `Edit` a repo file either — not just never write one.**
  Your `Read`/`Edit`/`Write` tools exist for exactly one thing: `$RUN_DIR`
  bookkeeping (`manifest.md`, `conductor-handoff.md`, `deferred-work.md`).
  Every question about repo *content* — what a design doc says, whether an
  approach fits, why a test fails, what a diff changed — is delegated to a
  child or a subagent that burns its own context and hands you back a
  conclusion. Anything you must see with your own eyes goes through a shell
  redirect and you read only the deciding line (see "Findings yes,
  transcripts never"). This is the rule that keeps the invariant true: your
  context grows with decisions taken, never with material inspected.
- **Delegate down the ladder, not just outward.** You run on the strong model
  because arbitration is your job; nothing else you do needs it. A file to
  read, a log to search, a spec to summarise, a lockfile to diff — that is
  cheap-tier work, and running it in your own strong-model context pays the
  highest per-token rate for the lowest-value tokens *and* permanently
  occupies the one context nobody can rotate. Push it to a child on the cheap
  or mid tier, or to a subagent. See "Model & connector tiering" for the
  ladder per connector and the table of jobs that belong on cheap.
- **You never work in the main checkout.** Every task that needs a source
  checkout gets a dedicated repository-local worktree created by
  `references/create-worktree.sh`, including single-task relay mode.
  Metadata-only inspection and cleanup children launch from `$RUN_DIR` and
  receive explicit repository paths; they never edit tracked files.
- **You never block.** Supervise via the `poll.sh` heartbeat (never a raw
  `session children --json` dump — see "Context budget"); answer `waiting`
  children and route repository or external-status checks to inspection
  children on the same heartbeat.
- **You never open an image and you never type a prompt body.** Both are pure
  context burn with a cheaper substitute — see "Context budget" and "Rendering
  child prompts". These are the two things that took a real conductor to 839k.
- **You never pass `-g` to a child launch.** Worktree children auto-inherit your
  group; an explicit `-g` overrides that inheritance, and a group name guessed
  from the repo folder (`-g baba` when the group is really `doozyx/baba`) strands
  the child away from its siblings. Omit it — see the group trap in `fleet`.
- **Children auto-parent to you — verify it, don't hand-wire it.** A `launch`
  issued from this session attaches the child to you automatically: agent-deck
  reads your instance id from the tmux session environment, so it resolves even
  from a shell that lost `$AGENTDECK_INSTANCE_ID` (a subagent shell, a scrubbed
  env). Pass no parent flag in the normal case. **Still confirm** each child
  parented and landed in your group (the `fleet` "verify the group" check) —
  a `launch` from *outside* your tmux session, or against an agent-deck older
  than the tmux-env auto-parent fix, can still orphan the child, and a worktree
  child then strays into its branch-leaf group. Repair a stray with
  `agent-deck session set-parent <id> "$AGENTDECK_INSTANCE_ID"` and
  `agent-deck group move <id> "$AGENTDECK_RESOLVED_GROUP"`. Use `--parent <id>`
  only to deliberately re-home a child to a *different* conductor (long form —
  never `-p`, which the global `--profile` extractor eats).
