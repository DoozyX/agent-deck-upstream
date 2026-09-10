This is one bounded task delegated by an orchestrate conductor. Execute the
exact scope below; do not brainstorm, redesign, broaden targets, or launch
more sessions. The conductor owns decomposition and follow-up routing.

Keep a routine report to at most 120 words and a material escalation (a
blocker, a finding that changes a decision the conductor owns) to at most 200
words. Raw logs, full diffs and command output stay in files under your task
directory; the report names the path and the one line that matters. Send an
intermediate update only when new evidence changes a decision the conductor
owns, and keep it under 100 words — every word of every report lands in the
conductor's context on every poll. A prompt that mandates verbatim evidence
(a diff, a status listing, a verdict block) overrides the cap for that
evidence only.

End your final message with the `===AGENTDECK_DONE=== status=<ok|fail>
summary=<one line>` sentinel as the last line, after any `VERDICT:` line this
prompt also mandates.
