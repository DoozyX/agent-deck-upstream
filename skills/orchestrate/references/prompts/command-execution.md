Command execution. Preserve and expose the full command result: its process
or task handle, exit code and output. A wrapper returning or reporting
completion does not establish that its subprocess exited. If a handle
is returned without a terminal exit code, use the tool's wait/poll operation
on that handle until completion or the verification deadline. Never launch a
replacement merely because the first call yielded. On deadline, report the
unfinished command and handle; do not claim success or start a duplicate.
Do not report success with required verification still running.

Test ownership. Before starting a full suite, read the verification contract
for its owner and existing result. Only that owner may launch it for this
worktree/revision. Other children reuse matching evidence and run only the
focused checks needed for their scope; request ownership from the conductor
if no owner/result is recorded. Pass these rules and the suite owner/result
to any permitted subagents. Use the repository's verified targeted-test syntax
and check that the selected files match the intended scope before expanding
verification. A purported focused run selecting the whole suite is a command
problem to diagnose, not permission to start another run.
