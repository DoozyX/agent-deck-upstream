{{include:reviewer-preamble.md}}

{{SPEC_BLOCK}}

Stable review identity: run={{RUN_DIR}} task={{TASK_ID}} attempt={{ATTEMPT_ID}} base={{BASE_HEAD}} reviewed={{REVIEWED_HEAD}} spec={{SPEC_ID}}. This identity and its reserved budget survive session title/model/context rotation; do not launch or rename another attempt to replace it.

Resolve suite ownership FIRST from the verification contract. If you are the
assigned owner and no matching run/result exists, start the documented suite
once with the connector's managed background execution, a 600 s deadline,
and output at {{VERDICT_FILE}}.suite.log. Retain the command handle and record
its terminal exit code, duration, worktree, HEAD and relevant environment in
the verdict. Review the code while it runs. Otherwise use the assigned owner's
log/result for the same revision and environment; do not launch another suite.
A missing assignment or unavailable result must be resolved with the conductor
before a clean verdict. Pass this ownership and command-tracking contract to
every review layer; layers may run focused checks but never duplicate the suite.

Review the full branch diff: git diff $(git merge-base {{BASE_BRANCH}} HEAD)...HEAD

Execute the review layers per {{AGENT_DECK_REPO}}/skills/review/references/ —
run `adversarial.md`, `edge-cases.md` and `verification-gap.md`, plus
`deletion-check.md` if the diff removes meaningful code — then merge, dedup,
grade severity and triage exactly as {{AGENT_DECK_REPO}}/skills/review/SKILL.md
describes. Add spec compliance against the task file above as an explicit
concern threaded into `edge-cases`, `verification-gap` and `deletion-check`:
anything missing, extra, or misunderstood is a finding. The adversarial layer
stays spec-blind by design — it receives the diff only. Not knowing the
author's intent is exactly what makes that layer catch what the others
rationalise; handing it the spec restores the anchoring bias it exists to
remove.

Own-eyes verification (tasks with user-visible or end-to-end acceptance
criteria). The implementer's "verified by driving the app" summary and its
screenshot descriptions are claims, not evidence: never treat them as proof of
anything. While the suite runs, reproduce each such criterion yourself: build
and start the app from this worktree, drive it in an isolated browser instance
(Playwright-style; never a shared Chrome — sibling sessions are driving
browsers too), and look at the result. Save what you looked at as
{{VERDICT_FILE}}.seen-<what>.png and record one line per criterion in the
verdict file, before the "Checked:" lines:
`Seen: <criterion> — <what the screen actually showed>`.
A criterion the screen contradicts is a finding at the severity the review
skill assigns. A criterion you could not exercise (no runnable app, missing
fixture, no browser tool) is a `[decision-needed]` finding that says why —
never a pass, and never a `Seen:` line. Write no `Seen:` line for anything
you did not look at with your own eyes.

Quality bar (only when the task file above carries a `## Quality bar`
block). Score each anchored criterion against what you saw and print one
line per criterion before the "Checked:" lines:
`Scored: <criterion> <n>/10 (threshold <t>) — <the written anchor it matches, and why>`.
A score below its threshold is a `major` finding: `[patch]` when the gap is
mechanical, `[decision-needed]` when closing it needs a product call. Report
the real number. Never round up to the threshold, and never let clean code
elsewhere lift a score above what the screen shows.

After every layer has reported, collect the owner's terminal suite result and
read its log with `tail -n 40` plus failure lines. If your command is still
running, poll its retained handle until exit or the 600 s deadline. If another
child owns it, obtain its completed receipt through the conductor. Missing
exit evidence is unverified, never clean; a deadline is not proof the process
stopped. Do not fabricate a successful exit or relaunch a still-running suite.
Confirm the tested worktree, HEAD, clean-tree state and relevant environment
still match; changes invalidate reuse. Cite the owner and result/log path in
your verdict. Never run the suite twice in a round. Judge whether the tests
actually cover the change.

Known pre-existing test failures (the implementer's recorded baseline):
{{BASELINE}}. These are NOT findings — only failures new against this
baseline are.

Write your full output to {{VERDICT_FILE}}, in this order: every layer's
raw findings first, then a line containing exactly `## Merged findings`, then
the merged list, any "Seen:" and "Scored:" lines, the "Checked:" lines and
the verdict line. That heading is a parsing anchor — emit it verbatim, exactly
once. Then print ONLY the merged findings list, the "Seen:"/"Scored:" lines,
the "Checked:" lines and the verdict line as your response. One
"Checked:" line must be exactly of the form
`Checked: tests full cmd=<cmd> exit=<code> duration=<s>s`. A verdict with no
evidence is not acceptable.
End with exactly one line, using real counts:
VERDICT: clean
VERDICT: fix-needed patch=<n> decision-needed=<n> defer=<n>
