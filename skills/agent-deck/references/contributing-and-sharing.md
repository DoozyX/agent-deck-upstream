## Contributing to agent-deck

Going beyond a bug report to a fix? agent-deck ships a dedicated contributor skill that mirrors the repo's PR intake gate and the maintainer's review machine, so an agent that follows it passes intake on the first try and scores well on all four review lenses (correctness, security, fit, intent).

Load it from the repo checkout:

```bash
# In an agent-deck clone
cat .github/skills/agent-deck-contributor/SKILL.md
```

It walks the full loop and enforces the bar: understand and reproduce the issue first, capture the human's actual ask verbatim (it goes in the PR body), one scoped problem per PR, a test that FAILS without your change, self-check locally before opening (`.github/skills/agent-deck-contributor/scripts/self-check.sh`), disclose the AI model that wrote the change, and respond directly to review verdicts. Run tests sandboxed — never against a real home directory:

```bash
HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...
```

## Session Sharing

Share Claude sessions between developers for collaboration or handoff.

**Use when:** User says "share session", "export session", "send to colleague", "import session"

```bash
# Export current session to file (session-share is a sibling skill)
$SKILL_DIR/../session-share/scripts/export.sh
# Output: ~/session-shares/session-<date>-<title>.json

# Import received session
$SKILL_DIR/../session-share/scripts/import.sh ~/Downloads/session-file.json
```

**See:** [session-share skill](../../session-share/SKILL.md) for full documentation.

## Switch a Session to Another Claude Account

Move a session — conversation included — to a different Claude account (work/personal/client) and continue exactly where it left off.

**Use when:** User says "switch account", "move this conversation to my other account", "continue this session on account X", "this session should use the <name> account".

**One-time setup** — name each account in `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`; the target profile must already be logged in: `CLAUDE_CONFIG_DIR=<dir> claude` → `/login`):

```toml
[profiles.personal.claude]
  config_dir = "~/.claude"
[profiles.work.claude]
  config_dir = "~/.claude-team"
```

**In the TUI:** the New Session dialog's Claude options carry an `Account` row
(`←`/`→` or `Space` to cycle; `inherit` keeps the conductor/group/env chain), and
the Edit Session dialog (`e`) carries a `Claude account` row that runs the full
switch — conversation migration and `--resume` restart included — on save. Both
rows are hidden when no accounts are configured.

**Commands:**

```bash
# Inspect the account names available to add/launch/switch-account
agent-deck accounts

# Create and start a new session directly under one named account
agent-deck launch . -c claude --account <account>

# Full flow: stop → copy conversation into the target account → set account → restart with --resume
agent-deck session switch-account <session> <account>

# Skip the restart (e.g. switch several sessions, restart later)
agent-deck session switch-account <session> <account> --no-restart

# Equivalent low-level form — also migrates the conversation; restart required
agent-deck session set <session> account <account>
```

**How it works / guarantees:**

- The conversation `.jsonl` is **copied** into `<target-config-dir>/projects/<encoded-path>/` — the old account keeps its copy; a conflicting file in the target is backed up as `.bak-<timestamp>` first. The session id does not change.
- `claude --resume` is a pure file lookup, so the restarted session continues with full history under the new account's auth.
- Tools/MCPs/plugins and usage limits follow the **new** account; enable any needed plugins in the target profile (e.g. `CLAUDE_CONFIG_DIR=<dir> claude plugin enable telegram@claude-plugins-official` for channel owners).
- A fresh session with no conversation yet switches cleanly (nothing to migrate).
- Unknown account names error and list the configured ones.

**Make a whole group/conductor use the account going forward:** set `[groups.<name>.claude].config_dir` / `[conductors.<name>.claude].config_dir` to the same dir in config.toml — new sessions there spawn on that account; `switch-account` is what carries existing conversations over.
