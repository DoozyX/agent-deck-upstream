{{include:delegated-task-preamble.md}}

# Run retrospective

Run directory: `{{RUN_DIR}}`
Retrospective path: `{{RETRO_PATH}}`

Read the run manifest and concise artifacts needed to record what actually
happened. Read the cross-run diary at `{{DIARY_PATH}}` first (it may not
exist yet) and skim existing retrospective filenames for repeated issues.
Write a short retrospective to the exact path above with these sections:
agent-deck issues, skill friction, tiering outcomes, automated checks (a
lint, test or CI check that would have caught a fix-round finding before a
reviewer did), tool economy (which children spent context on what, and what
they could have been handed instead), and suggested changes. Use `none` or
`n/a` instead of padding an empty section. Reference an earlier retrospective
when the same issue recurred and add only the new evidence.

Then consolidate into the diary at `{{DIARY_PATH}}`. The diary is the
memory the next brainstorm reads before its first question, so it records
only what changes future work: distinct decisions and why, discarded
approaches and why, mistakes, and reusable lessons about this repository.
Never session chronology. Give each fact one canonical home: rewrite or
delete the entry an outcome supersedes rather than appending a second one,
merge repeats into one entry with a count, and keep the file under about
150 lines. Create it with a one-line header if it does not exist. A run that
taught nothing leaves the diary untouched.

Do not edit any other file, commit, push, or alter run state. End with the
completed retrospective path and `diary: updated|unchanged`.
