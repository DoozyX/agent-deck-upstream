## Usage-aware launch

Before each launch wave, run `agent-deck usage recommend` **once per distinct
role+tier** in that wave and save its JSON under `$RUN_DIR/usage/`:

```bash
mkdir -p "$RUN_DIR/usage"
agent-deck usage recommend --role <role> --tier <cheap|mid|strong|frontier> --json \
  > "$RUN_DIR/usage/<wave>-<role>-<tier>.json"
```

The command is read-only and advisory. It exits 0 for every decision — including
`state: exhausted` and `state: unknown` — and exit 2 is reserved for a bad flag:
a missing `--role`, an unknown `--tier`, an unknown `--prefer` tool, a stray
positional, and anything else `flag.Parse` rejects, which is wider than that
list — an undefined flag such as `--bogus`, or a defined flag given no value —
except the four help spellings `-h`, `--h`, `-help`, and `--help`, which print
the usage line on stderr and exit 0.
Exit 1 means the configuration could not be loaded or validated, or the JSON
could not be encoded — check the exit code before reading the saved file,
because the `>` redirect creates that file even when nothing was written to it.
A machine with no `openusage` binary gets `state: unknown` and the preferred
tool when one resolves, which is an answer, not a failure; continue unchanged
there.

**Check that the decision is launchable before you build the launch.** A
`reason` *beginning* `no candidate tools for tool strategy` means no candidate
tool resolved at all, and nothing in that decision may be launched. Match on the
prefix, not on the substring: that clause comes first, but a profile-miss
clause, a tier floor or a model clause can follow it after a `;`, and a
`--profile` name is interpolated into `reason` verbatim — so a substring match
can fire on a launchable decision whose profile is named `no candidate tools`.
Do not use `tool == ""` as the test — `tool` is empty only when the strategy
resolved no name either. When a name survives the empty candidate list, `tool`
is that name, `provider` comes back filled in whenever it maps to a usage
provider, `model` follows that provider's ladder and is empty when the applied
tier's rung is empty, `state` is `unknown`, and **stderr is empty**, so a
`tool == ""` guard passes that decision straight through and the launch below is
built for a tool the policy just filtered out. The exit code is 0 either way.
The remedy hint reaches stderr on the empty-`tool` branch only: capture stderr
for that one, but never read a silent stderr as a launchable decision.

On the empty-`tool` branch, re-run the call with `--prefer <tool>`, or set the
top-level `default_tool` in `config.toml`, before launching anything for that
wave. On the surviving-name branch, neither remedy makes the decision
launchable: `--prefer` picks which name survives, and when `failover` is omitted
`default_tool` supplies the default failover order and can change which name
survives; an explicit `failover` controls that order instead. The `reason` still
begins `no candidate tools for tool strategy`, so fix the environment instead:
un-hide the tool in `[ui] hidden_tools`, install it, or correct a misspelled or
miscased `failover` entry.

Launch with the decision's `tool`, and with its `model` when that field is
non-empty:

```bash
agent-deck launch <worktree-path> -c <tool> \
  -t "impl-<task-slug>" \
  --extra-arg --model --extra-arg <model> \
  --message-file "$RUN_DIR/<task-slug>/impl-prompt.md"
```

Omit the `--extra-arg --model --extra-arg <model>` flag entirely when `model` is
empty — an empty model is the decision telling you to run the connector's own
default.

**Re-run rule.** Reuse the saved decision for the rest of its wave. Call
`recommend` again only after a child reports the existing `usage-limit`
substate, or before a later wave when the saved decision's `fetched_at` is
older than five minutes. A saved decision whose `fetched_at` is `null` — what
the command emits whenever the *selected* tool has no available snapshot, which
happens even when snapshots were fetched for other candidates — counts as
stale. Do not query it during ordinary polling, focused single tasks, or
every-turn hooks.

**Record every launch** on one manifest line:

```text
role=<role> tool=<tool> model=<model> tier=<applied> state=<state> reason=<one line>
```

**A decision whose `state` is `exhausted` pauses the wave**: do not launch that
wave, record the decision, report it through the run's existing path, and wait
for the earliest `resets_at` from `agent-deck usage --all --json` before
retrying.

**Explicit workflow tool choices** — the cross-provider Codex reviewer in
"Model & connector tiering" is the standing one — yield to the recommendation
**only** when that provider's state is `exhausted`. Score that provider by
re-running the call with `--prefer <that provider>`, then read its state from
wherever the strategy in force puts it. Under the default `tool_strategy` that
provider is the only candidate, so the top-level `state` is its own and
`alternatives[]` is empty. Under `tool_strategy = "auto"` `--prefer` only puts
it FIRST among the candidates: a healthier tool can still be selected, so that
provider's state lands in one of three places — the top-level `state` when
`tool` names it, its entry in `alternatives[]` when a different tool was
selected, and neither when it was never a candidate at all. `alternatives[]`
lists the non-selected candidates, so a provider filtered out before it was
ever scored — hidden by `[ui] hidden_tools`, say, or dropped from the failover
order by a miscased entry — is absent from both, and that absence is the only
signal you get. The probe is diagnostic: do not save it over the wave's
`$RUN_DIR/usage/<wave>-<role>-<tier>.json`, which the re-run rule reuses for the
rest of the wave. Record the override on that launch's manifest line. The
format above has no override field, so it goes in `reason=`.

The recommendation chooses a connector and a model and nothing else: it never
switches an account automatically, and it never overrides an explicit account or
profile choice. Accounts stay exactly as configured.

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
