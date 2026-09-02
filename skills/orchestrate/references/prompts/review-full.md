{{include:reviewer-preamble.md}}

{{SPEC_BLOCK}}

Start the full suite FIRST, detached, before you read anything: the
full-suite command from the verification contract in the block above (or
the repo's documented suite command if the block has none), output to
{{VERDICT_FILE}}.suite.log, 600 s ceiling, last line `SUITE_EXIT=<code>`.
Claude: one Bash call with `run_in_background: true` and `timeout: 600000`
running `( <suite-cmd>; echo SUITE_EXIT=$? ) > {{VERDICT_FILE}}.suite.log 2>&1`.
Other connectors: `nohup sh -c '<suite-cmd>; echo SUITE_EXIT=$?' > {{VERDICT_FILE}}.suite.log 2>&1 &`.
Do not wait for it and do not poll it; the layers below fill that time.

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

After every layer has reported, read the suite result: `tail -n 40` of
{{VERDICT_FILE}}.suite.log plus its `SUITE_EXIT=` line. If that line is
missing, wait once with the harness's blocking wait on the background task;
if it is still missing, record `SUITE_EXIT=timeout` and report it as a
finding, never as clean. Judge whether the tests actually cover the change.

Known pre-existing test failures (the implementer's recorded baseline):
{{BASELINE}}. These are NOT findings — only failures new against this
baseline are.

Write your full output to {{VERDICT_FILE}}, in this order: every layer's
raw findings first, then a line containing exactly `## Merged findings`, then
the merged list, the "Checked:" lines and the verdict line. That heading is a
parsing anchor — emit it verbatim, exactly once. Then print ONLY the merged
findings list, the "Checked:" lines and the verdict line as your response. One
"Checked:" line must be exactly of the form
`Checked: tests full cmd=<cmd> exit=<code> duration=<s>s`. A verdict with no
evidence is not acceptable.
End with exactly one line, using real counts:
VERDICT: clean
VERDICT: fix-needed patch=<n> decision-needed=<n> defer=<n>
