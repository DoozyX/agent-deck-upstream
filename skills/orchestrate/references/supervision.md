# Supervision and waiting children

Use the run-local [`supervisor.sh`](supervisor.sh) for ordinary supervision:

```bash
bash "$RUN_DIR/supervisor.sh" start "$RUN_DIR"
bash "$RUN_DIR/supervisor.sh" status "$RUN_DIR"
bash "$RUN_DIR/supervisor.sh" ack "$RUN_DIR" <event-id>
bash "$RUN_DIR/supervisor.sh" stop "$RUN_DIR"
```

`start` owns one process-identity lock and runs deterministic observation.
Repeated unchanged observations do not deliver a wake. `status` returns durable
pending/delivered events without dumping raw child transcripts into conductor
context. Acknowledge an event only after the conductor has acted on it; a
duplicate acknowledgement returns `already-recorded`. Call `stop` only after
the final report.

Completion is actionable only when the current child output is fresh and its
last nonblank line is the done sentinel. A stale, quoted, or nonterminal
sentinel remains non-completion. Quota, failure, waiting, stall, and context
events have separate durable identities; a recovered then recurring state is a
new event, while an unchanged incident is not.

Delivery uses bounded `session nudge` attempts. A busy target is skipped without
manufacturing an error. An unreachable target becomes blocked operator
attention after the configured bound and receives no repeated model wake. A
conductor rotation re-arms delivery-derived attention but preserves quota
blocks and acknowledged event history.

## Answering a waiting child

Read only the fresh question, then decide who owns the answer:

- The conductor answers facts already fixed by the task/spec, repository policy,
  or a recorded decision. Send one decisive response with `--defer-if-busy` and
  verify the child transitions or emits fresh output.
- The user answers scope changes, destructive/irreversible actions,
  credentials, or product decisions absent from the approved contract. Keep
  that child waiting, record the question, and continue other pipelines.
- For a Codex permission menu use `session approve <id> once`; do not send the
  digit as text. Follow the connector's supported approval mechanism for other
  tools.

Never append a raw tmux Enter after `session send` or `session nudge`. Use
`session output <id> --json --require-fresh` for response freshness and
`session output <id> --pane` only to diagnose a visible prompt or stalled UI.

## Optional watchdog

No watchdog child is required. When the user has approved a bounded helper for
safe prompts or concrete stalls, render [`prompts/watchdog.md`](prompts/watchdog.md),
launch it with the documented narrow allowlist, and store its id in
`$RUN_DIR/.watchdog-id`. The supervisor may wake it once for a materially new
conductor stall. Unchanged stalls, observation failures, and choices already
escalated do not keep waking it.

The watchdog is never a permission bypass: it may approve only pre-authorized
safe prompts, must preserve destructive/user choices on screen, and escalates
those choices to the operator. Remove the optional child during successful
cleanup; preserve it with a needs-attention run when it remains diagnostic.
