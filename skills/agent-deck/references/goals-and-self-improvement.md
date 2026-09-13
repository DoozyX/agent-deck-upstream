## Self-Improvement

**Use when:** user says "self-improve", "analyze my conductor", "what bugs are we hitting", "file issues from my usage", or asks the conductor to learn from past conversations.

A pipeline that analyzes a conductor's own Claude Code conversation transcripts and surfaces actionable signal:

- **Bugs** — tool errors, user-reported issues, recurring friction (with citations back to the source transcript)
- **Workflow patterns** — repeated multi-step sequences worth promoting to a skill or script
- **Capability discoveries** — undocumented commands / flags / recipes the conductor used in real work
- **User corrections** — meta-rules captured from "no, do it this way" exchanges, suitable to encode in the conductor's `CLAUDE.md`

Output lives at the conductor root and is regenerated on each run:

```
~/.agent-deck/conductor/<name>/
├── FINDINGS.md              # raw synthesis of the latest run
├── CAPABILITIES.md          # curated inventory (you edit; survives runs)
├── analysis-manifest.json   # tracking — sha + line counts + analyzer session IDs
└── analysis/                # scripts, prompts, distilled transcripts, per-transcript reports
```

### Quick start

```bash
SKILL_DIR="<base directory shown when this skill was loaded>"
SELFIMP="$SKILL_DIR/scripts/self-improvement"

# Phase 1 — distill all transcripts for one conductor (Python, no LLM, ~1 min)
mkdir -p ~/.agent-deck/conductor/<name>/analysis/distilled
for f in ~/.claude*/projects/-home-*-agent-deck-conductor-<name>/*.jsonl; do
  sid=$(basename "$f" .jsonl | cut -c1-8)
  python3 "$SELFIMP/distill.py" "$f" ~/.agent-deck/conductor/<name>/analysis/distilled/$sid.md
done

# Phase 2 — analyze + synthesize (spawns agent-deck sub-sessions, ~30 min, ~$5)
cp -r "$SELFIMP/prompts" ~/.agent-deck/conductor/<name>/analysis/
bash "$SELFIMP/run-analyzers.sh"   # paced, resumable via manifest

# Phase 3 — file issues from FINDINGS.md (interactive; never auto-files)
bash "$SELFIMP/file-issues.sh"
```

### Privacy

Three layers run before anything leaves the box: regex sanitize → AI sanitizer session → independent AI auditor session. The auditor must verdict `SAFE_TO_SHARE` before the filer will submit. Each layer covers the others' blind spots (regex catches tokens / IPs / paths; AI catches contextual names; auditor catches what the first two missed with fresh eyes). Human review is non-negotiable — the filer prints the exact `gh` command and waits for `[f]` before running it.

### Constraints

- All LLM work happens in spawned agent-deck sub-sessions — never via the Anthropic SDK directly. This dogfoods agent-deck and uses your Claude Max plan.
- Sequential with 30-60s pacing between launches — rate-limit safe.
- Manifest-based resume — re-runs only process new or grown transcripts.

### Deep dive

For the full architecture, output schemas, lessons learned from real runs, and per-script reference, see [self-improvement.md](self-improvement.md).

## Goal (goal-driven worker autonomy)

**Use when:** user says "pursue this", "set a goal", "make it work until done", "nudge the agent", "stop me having to message it again", or describes wanting an agent to keep working autonomously toward a specific goal without manual re-prompting.

A complementary layer on top of [Self-Improvement](#self-improvement). Self-improvement is *post-hoc* analysis. Goal is the *live* mechanism that prevents the kinds of stalls self-improvement keeps surfacing — specifically the FINDINGS pattern where a conductor's hourly cron fires 18 times with identical `[STATUS]` replies and no actual progress.

### The core idea

Three entities, never collapsed:

| Entity | Job | Restriction |
|---|---|---|
| **Worker** | Take one bounded step per cycle, write a progress receipt | May NOT decide it's done. May NOT escalate. |
| **Verifier** | An external shell command — runs the done-condition independently | NOT an LLM. NOT the worker's self-assessment. |
| **Manager** | Small Python daemon (cron'd) — runs the verifier, reads receipts, nudges the worker, escalates to user when stuck | NOT involved in doing the work |

Separating these three concerns is what prevents the "agent keeps reporting status but never finishes" failure mode the FINDINGS captured.

### Done-conditions must be shell commands

Examples that work:
- `gh release view v1.6.0 -R asheshgoplani/agent-deck --json publishedAt | jq -e '.publishedAt != null'`
- `gh pr view 890 -R asheshgoplani/agent-deck --json mergedAt | jq -e '.mergedAt != null'`
- `test -s /tmp/report.csv && [ "$(wc -l < /tmp/report.csv)" -gt 100 ]`

Examples that DON'T work:
- "Get this working" → not testable
- "Make the code better" → not measurable
- "Worker says it's done" → self-judgment (the bug we're avoiding)

### Quick start (Phase 1 — hand-wired proof)

The full spec is in [goal.md](goal.md). For early use, follow Phase 1:

1. Write a goal JSON at `~/.agent-deck/goals/<id>.json` (schema in the deep-dive doc).
2. Spawn the worker with the contract prompt (template in the deep-dive doc).
3. Run the manager script manually every few minutes to check + nudge.
4. After one real goal completes successfully, graduate to Phase 2 (CLI wrapper) and Phase 3 (cron'd daemon).

### Future CLI surface (Phase 2)

```bash
agent-deck goal \
    --goal "Ship agent-deck v1.6.0" \
    --done 'gh release view v1.6.0 --json publishedAt | jq -e ".publishedAt != null"' \
    --check-every 5m \
    --max-idle 1h \
    --escalate-after 3 \
    --max-cycles 24

agent-deck goal list           # active goals + state
agent-deck goal show <id>      # full JSON dump
agent-deck goal tail <id>      # tail the worker's task-log.md
agent-deck goal cancel <id>    # stop the worker
agent-deck goal resume <id> "<hint>"  # send context-rich hint, reset nudge counter
```

### Deep dive

For the full design — three-entity model, registry schema, worker contract prompt, manager loop pseudocode, nudge generator, escalation bundle, done-condition guidelines, failure modes, implementation phases, and the verification this closes the FINDINGS 18-hour stall — see [goal.md](goal.md).
