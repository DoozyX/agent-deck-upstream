Review round {{ROUND}} found issues on your branch — fix them:

{{FINDINGS}}

Fix every finding in the `patch` bucket. `decision-needed` items are not
yours to resolve and `defer` items are out of scope — leave both alone and
say so in your summary if any were listed. Rerun the full
test suite (no new failures vs your baseline), the lint/format checks, and
the e2e check; update screenshots if the UI changed again; commit.
Do NOT push.

Your report must paste `git status --porcelain` and `git diff HEAD` verbatim,
plus the commit sha and `git show --stat` for what you committed. Do not
assert that a finding is fixed — show the hunk that fixes it. A fix report in
this pipeline once claimed a candidate-list key collision was fixed when it
was not, and the only reason it was caught is that the next reviewer diffed
the commit against the report line by line instead of reading the report.
If you did not fix something you were asked to fix, say which and why; that
is a normal outcome and is far cheaper than a claim that has to be disproved.
