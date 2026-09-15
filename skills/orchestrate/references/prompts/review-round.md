{{include:reviewer-preamble.md}}

{{SPEC_BLOCK}}

Stable review identity: run={{RUN_DIR}} task={{TASK_ID}} attempt={{ATTEMPT_ID}} base={{BASE_HEAD}} reviewed={{REVIEWED_HEAD}} spec={{SPEC_ID}}. This is the same durable task across renamed rounds, gates, and model/context rotation.

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

A previous review at commit {{REVIEWED_SHA}} reported:
{{PREVIOUS_FINDINGS}}

This is a full-branch round: a clean verdict from you is terminal for the
task, so the whole branch is your responsibility, not only the new commits.
Do, in order:
1. Verify each finding above is actually fixed — an unfixed or half-fixed
   finding is a new finding.
2. Review the commits made since then closely: git diff {{REVIEWED_SHA}}...HEAD
   That diff is the FOCUS of every layer.
3. Review the whole branch: git diff {{BASE_REF}}...HEAD
   That diff is the SCOPE of every layer. Code outside the new commits was
   reviewed by an earlier round; flag it only when a new commit changed its
   behaviour, or when an earlier round plainly missed a defect there. Do not
   re-raise already-dispositioned items (a previous finding marked `defer`
   or fixed above) — a reviewer re-litigating them is the oscillation the
   conductor escalates on.
4. Run the focused tests for the changed paths, in the foreground with a
   600 s ceiling: {{FOCUSED_TESTS}}
   Known pre-existing failures (baseline): {{BASELINE}} — only NEW failures
   are findings.

If a previous finding came from a `Seen:` or `Scored:` line, re-exercise
that criterion yourself in an isolated browser (never a shared Chrome), save
the capture as {{VERDICT_FILE}}.seen-<what>.png, and print the fresh `Seen:`
or `Scored:` line with the real result. A fix you did not look at is
unverified and stays a finding; the implementer's description of it is a
claim, not evidence.

Execute the review layers per {{AGENT_DECK_REPO}}/skills/review/references/ —
run `adversarial.md`, `edge-cases.md` and `verification-gap.md`, plus
`deletion-check.md` if the diff removes meaningful code — then merge, dedup,
grade severity and triage exactly as {{AGENT_DECK_REPO}}/skills/review/SKILL.md
describes. Dispatch every layer in ONE message as parallel subagents where
the connector has them. Add spec compliance against the task file above as
an explicit concern threaded into `edge-cases`, `verification-gap` and
`deletion-check`. The adversarial layer stays spec-blind by design — it
receives the two diffs only.

After every layer has reported, collect the owner's terminal suite result and
read its log with `tail -n 40` plus failure lines. If your command is still
running, poll its retained handle until exit or the 600 s deadline. If another
child owns it, obtain its completed receipt through the conductor. Missing
exit evidence is unverified, never clean; a deadline is not proof the process
stopped. Do not fabricate a successful exit or relaunch a still-running suite.
Confirm the tested worktree, HEAD, clean-tree state and relevant environment
still match; changes invalidate reuse. Cite the owner and result/log path in
your verdict. Never run the suite twice in a round.

Report findings in the merged format from
{{AGENT_DECK_REPO}}/skills/review/SKILL.md: file:line — severity (critical |
major | minor) — [patch | decision-needed | defer] — provenance — one line
each. Then 2-3 "Checked:" evidence lines, two of them exactly of the form
`Checked: tests full cmd=<cmd> exit=<code> duration=<s>s` and
`Checked: tests focused cmd=<cmd> exit=<code> duration=<s>s`. A verdict with
no evidence is not acceptable.

Write your full output to {{VERDICT_FILE}}, in this order: every layer's
raw findings first, then a line containing exactly `## Merged findings`, then
the merged list, any "Seen:"/"Scored:" lines, the "Checked:" lines and the
verdict line. That heading is a parsing anchor — emit it verbatim, exactly
once. Then print ONLY the merged list, the "Seen:"/"Scored:" lines, the
"Checked:" lines and the verdict line as your response.
End with exactly one line, using real counts:
VERDICT: clean
VERDICT: fix-needed patch=<n> decision-needed=<n> defer=<n>
