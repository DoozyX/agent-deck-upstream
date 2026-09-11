---
name: verify
description: Evidence gate before any completion claim — every "it works", "tests pass", "fixed", or "done" must be backed by a command run in this message with its output shown. Use before committing, opening a PR, reporting a task complete, or telling the user something is working.
metadata:
  compatibility: "claude, opencode"
---

# Verify

## Iron law

> No completion claim without fresh evidence **in this message**. Evidence
> from three messages ago is a memory, not a verification — the tree has
> changed since. Run the command now, show the output, then make the claim.

## Claim → evidence table

| Claim | Evidence required |
| --- | --- |
| "tests pass" | The suite run **now**, showing **0 failures**. A subset run proves the subset only — say which you ran. |
| "build works" | The build command exiting **0**. A passing linter is **not** a build; a type-check is not a build. |
| "the bug is fixed" | The **original symptom** re-tested by the original reproduction, now absent. |
| "I added a regression test" | A verified red-green cycle: revert the fix → the test **fails** → restore the fix → the test **passes**. Show both runs. |
| "the child/agent completed the work" | The **VCS diff** (`git log`, `git diff`) — never the agent's own success report. An agent reporting success is a claim, not evidence. |
| "nothing else broke" | The full suite, compared against a recorded baseline from before the change. |
| "it's deployed / running" | A request against the running thing, with its response. |
| "the type checker is happy" | The type-check command run now, exit 0 — distinct from tests and from a build. |
| "the migration is safe" | A run against a copy of real (or representative) data, not just the migration applying cleanly on an empty schema. |
| "the feature is done" / "this matches the design" | The design's acceptance criteria and `## Testing Decisions`, **re-read now** (not recalled), each criterion quoted and mapped to the command that proves it or named as a gap. Tests passing proves the code does what the tests say; only the criteria say what was asked for. |
| "the task/run is complete" | The same, against the run's stated goal — plus every criterion accounted for, including the ones no test covers. "All units landed" is a claim about your task list, not about the goal. |

## How to show evidence

Paste the command and the decisive lines of its output — the failure count,
the exit status, the assertion. Not the whole log. If the output is large,
show the tail and say what you filtered (e.g. "last 15 lines of 400; full log
had no other failures").

## Baselines

Before touching anything, record what already fails — run the suite, note
the failing names or count. Hold yourself accountable only for *new*
failures against that recorded baseline. A baseline claimed from memory
("I think that test was already broken") is not a baseline; if you didn't
run it before you changed anything, you don't have one, and every failure
is yours until proven otherwise.

## Check against what was asked, not only what you built

Evidence that a command passed is only half a completion claim. The other
half is *what was asked for*, re-read at the moment you claim it — because
the version in your head has been drifting since you started, and a long
session or a compaction is exactly when it drifts silently.

So before any "done" or "it works", read the source of truth again, in this
order, and use the first that exists:

- **An orchestrate run** — `$RUN_DIR/goal.md` (the run's frozen goal) and the
  approved design at the path it names, usually `$RUN_ROOT/design/design.md`.
  Quote the acceptance criteria and `## Testing Decisions` verbatim. These are
  written to survive rotation and compaction precisely so this check still
  works in a session that has lost the original conversation.
- **A design or spec** named anywhere in the task — read it at its path.
- **Neither** — the request as the user actually stated it. Most sessions are
  this case: there is no design, and inventing a requirement to check against
  is worse than checking against the ask. Do not go hunting for a design
  document that was never written.

Then say, per criterion, which command proves it or that nothing does. A
criterion no command covers is a gap to name, not a box to tick — see "When
evidence cannot be gathered".

If the goal or design **cannot be read** (missing file, unreadable path),
that is itself the finding. Say so and stop; a completion claim checked
against a remembered goal is the failure this section exists to prevent.

## Red flags

| Red flag | Why it's a tell |
| --- | --- |
| "should work" / "probably passes" / "seems fine" | Hedging words mean no command was run — a command either passed or it didn't. |
| "Done!" / "Perfect!" / "All set!" before any output appears in the message | The claim is written before the evidence exists. |
| Citing a run from earlier in the conversation as current | The tree has changed since; stale evidence isn't evidence. |
| Reporting a subagent's or child session's summary as the result | Its success report is a claim, not a diff you've checked. |
| "The test file exists, so it's covered." | A file existing proves nothing about whether it runs or passes. |
| "It worked when I ran it manually earlier" | "Earlier" precedes the latest edit; re-run against the current tree. |
| Silence about a step that failed, followed by a claim about the step after it | A skipped failure doesn't disappear — it invalidates everything downstream. |
| "All the tests pass, so it's done." | The suite proves the code matches the tests. Nobody has checked it against the acceptance criteria, and a test suite cannot notice a requirement that was never implemented. |
| Stating the goal or the acceptance criteria from memory after a long session or a compaction | That is the copy most likely to have drifted. Re-read the file; the drift is invisible from the inside. |

## When evidence cannot be gathered

Say so explicitly, and name both the reason and the gap — e.g. "no browser
available here, so the e2e path is unverified" or "no access to the
staging DB, so the migration is unverified against real data." An honest
unverified is fine; a claim dressed as verified is not.
