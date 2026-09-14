# Prompt and helper catalog

Read a template only when launching that role. Render templates with
[`prompts/render.sh`](prompts/render.sh); it resolves local includes before
substitution.

| Stage | Template |
| --- | --- |
| Shared executor contract | [`preamble.md`](prompts/preamble.md) |
| Delegated non-implementation contract | [`delegated-task-preamble.md`](prompts/delegated-task-preamble.md) |
| Reviewer contract | [`reviewer-preamble.md`](prompts/reviewer-preamble.md) |
| Verification-arm contract | [`verify-preamble.md`](prompts/verify-preamble.md) |
| Inspection | [`inspect.md`](prompts/inspect.md) |
| Planning | [`plan.md`](prompts/plan.md) |
| Implementation | [`impl.md`](prompts/impl.md) |
| Initial review | [`review-full.md`](prompts/review-full.md) |
| Later review | [`review-round.md`](prompts/review-round.md) |
| Fix | [`fix.md`](prompts/fix.md) |
| Verification recon | [`verify-recon.md`](prompts/verify-recon.md) |
| Verification arm | [`verify-arm.md`](prompts/verify-arm.md) |
| Verification report | [`verify-report.md`](prompts/verify-report.md) |
| Blind A/B judge | [`ab-judge.md`](prompts/ab-judge.md) |
| Cleanup executor | [`cleanup-execute.md`](prompts/cleanup-execute.md) |
| Cleanup verifier | [`cleanup-verify.md`](prompts/cleanup-verify.md) |
| Optional watchdog | [`watchdog.md`](prompts/watchdog.md) |
| Retrospective | [`retrospective.md`](prompts/retrospective.md) |

Run scaffolding copies the deterministic helpers needed by the chosen mode:
[`supervisor.sh`](supervisor.sh), [`supervisor-observe.py`](supervisor-observe.py),
[`command-timeout.sh`](command-timeout.sh), [`review-state.sh`](review-state.sh),
[`review-attempt.sh`](review-attempt.sh), [`poll.sh`](poll.sh),
[`heartbeat.sh`](heartbeat.sh), [`rotate-conductor.sh`](rotate-conductor.sh),
[`primary-checkout-guard.sh`](primary-checkout-guard.sh),
[`create-worktree.sh`](create-worktree.sh), and
[`teardown-gate.sh`](teardown-gate.sh).
