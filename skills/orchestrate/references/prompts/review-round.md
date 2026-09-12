{{include:reviewer-preamble.md}}

{{SPEC_BLOCK}}

Stable review identity: run={{RUN_DIR}} task={{TASK_ID}} attempt={{ATTEMPT_ID}} base={{BASE_HEAD}} reviewed={{REVIEWED_HEAD}} spec={{SPEC_ID}}. This is the same durable task across renamed rounds, gates, and model/context rotation.

Start the full suite FIRST, detached, before you read anything: the
full-suite command from the verification contract in the block above (or
the repo's documented suite command if the block has none), output to
{{VERDICT_FILE}}.suite.log, 600 s ceiling, last line `SUITE_EXIT=<code>`.
Claude: one Bash call with `run_in_background: true` and `timeout: 600000`
running `( <suite-cmd>; echo SUITE_EXIT=$? ) > {{VERDICT_FILE}}.suite.log 2>&1`.
Other connectors: `nohup sh -c '<suite-cmd>; echo SUITE_EXIT=$?' > {{VERDICT_FILE}}.suite.log 2>&1 &`.
Do not wait for it and do not poll it; the work below fills that time.

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

After every layer has reported, read the suite result: `tail -n 40` of
{{VERDICT_FILE}}.suite.log plus its `SUITE_EXIT=` line. If that line is
missing, wait once with the harness's blocking wait on the background task;
if it is still missing, record `SUITE_EXIT=timeout` and report it as a
finding, never as clean. Never run the suite twice in a round.

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
