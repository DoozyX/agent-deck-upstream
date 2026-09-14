## Final report

Deliver to the user only — this is the single place screenshots are ever
referenced. Per task:

```text
## <task title>
- PR: <url> — checks: green | failing | none
- Review: <N> round(s), <M> counted — clean | open items: <list>
- Models: impl <connector/model>, review <connector/model> — escalations:
  <none | what and why>
- Screenshots: <run-dir>/<task-slug>/ (UI tasks only)
  Nominated pair worth attaching to the PR manually, if you like:
  before-<what>.png + after-<what>.png
- Blind A/B: <final AB_SUMMARY line> — <per pair: prefer=<before|after|neither>, reason> (UI tasks only)
- Needs attention: <anything left, or omit>
```

Close by listing what was cleaned up (deleted sessions, removed worktrees
and branches of successful tasks) and what was deliberately left in place for
needs-attention tasks.

## Retrospective (self-learning)

Before delivering the final report, render `retrospective` and launch a child
to write the run retrospective so agent-deck and this skill improve from every
run. The retrospective stays with every other artifact:

```bash
RETRO_PATH="$RUN_DIR/retro.md"
DIARY_PATH="$ROOT_WT/.agent-deck/diary.md"
```

The child writes the retrospective, consolidates what changes future work
into the diary, and does **not** commit, push, or copy either elsewhere.

Keep it short and only record what actually happened — an empty section is
better than a padded one:

```text
# Retro: <run-id> (<date>)

## agent-deck issues
<bugs/friction hit in agent-deck itself, each with the exact command,
what happened vs. expected, and enough detail to file an issue. "none">

## Skill friction
<places this SKILL.md was wrong, ambiguous, or forced a workaround —
quote the rule that misled you. "none">

## Tiering outcomes
<per task: tiers used, rounds needed, escalations and their trigger —
the data that validates or refutes the tier table. "n/a">

## Automated checks
<a lint, test or CI check that would have caught a fix-round finding
before a reviewer did — the cheapest review round is the one a check
replaces. "none">

## Tool economy
<which children spent context on what — an implementer that read half the
repo, a reviewer that re-ran a suite it was handed — and what they could
have been handed instead; feeds "Context budget". "none">

## Suggested changes
<concrete edits to SKILL.md / fleet / agent-deck worth making, one line
each. "none">
```

The retrospective child reads `$DIARY_PATH` and skims prior
`$ROOT_WT/.agent-deck/*/retro.md` files for a repeat issue before writing. If
a prior retro already reports it, it references that file and adds only what
is new (a recurring issue is a stronger signal than a new one).

**The diary is the run's only cross-run memory.** Retros are per-run and
nobody re-reads them; `brainstorming` reads the diary before its first
question, so a decision, a discarded approach, a mistake or a lesson about
this repository that would change the next design belongs there — rewritten
into its one canonical entry, never appended as chronology. Everything else
stays in the retro. Mention the completed retro path and the diary verdict
(`updated` or `unchanged`) as the last line of the final report.
