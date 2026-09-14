## TUI Keyboard Shortcuts

### Navigation
| Key | Action |
|-----|--------|
| `j/k` or `↑/↓` | Move up/down |
| `h/l` or `←/→` | Collapse/expand groups |
| `Enter` | Attach to session |

### Session Actions
| Key | Action |
|-----|--------|
| `n` | New session |
| `r/R` | Restart (reloads MCPs) |
| `m` | MCP Manager |
| `s` | Skills Manager |
| `f/F` | Fork Claude/OpenCode/Pi/Codex session |
| `d` | Delete |
| `A` | Archive (stops tmux, hides from default list) |
| `Shift+U` | Unarchive (does not auto-start tmux) |
| `M` | Move to group |

### Search & Filter
| Key | Action |
|-----|--------|
| `/` | Local search |
| `G` | Global search (all Claude conversations) |
| `!@#&` | Filter by status (running/waiting/idle/error) |
| `^` | View archived sessions |

### Global
| Key | Action |
|-----|--------|
| `?` | Help overlay |
| `Ctrl+Q` | Detach (keep tmux running) |
| `Ctrl+E` | Open feedback dialog |
| `q` | Quit |

## MCP Management

**Default:** Do NOT attach MCPs unless user explicitly requests.

```bash
# List available
agent-deck mcp list

# Attach and restart
agent-deck mcp attach <session> <mcp-name>
agent-deck session restart <session>

# Or attach on create
agent-deck add -t "Task" -c claude --mcp exa /path
```

**Scopes:**
- **LOCAL** (default) - `.mcp.json` in project, affects only that session
- **GLOBAL** (`--global`) - Claude config, affects all projects

## Worktree Workflows

### Create Session in Git Worktree

When working on a feature that needs isolation from main branch:

```bash
# Create session with new worktree and branch
agent-deck add /path/to/repo -t "Feature Work" -c claude --worktree feature/my-feature --new-branch

# Create session in existing branch's worktree
agent-deck add . --worktree develop -c claude
```

### List and Manage Worktrees

```bash
# List all worktrees and their associated sessions
agent-deck worktree list

# Show detailed info for a session's worktree
agent-deck worktree info "My Session"

# Find orphaned worktrees/sessions (dry-run)
agent-deck worktree cleanup

# Actually clean up orphans
agent-deck worktree cleanup --force
```

### When to Use Worktrees

| Use Case | Benefit |
|----------|---------|
| **Parallel agent work** | Multiple agents on same repo, different branches |
| **Feature isolation** | Keep main branch clean while agent experiments |
| **Code review** | Agent reviews PR in worktree while main work continues |
| **Hotfix work** | Quick branch off main without disrupting feature work |

## Scratch Sessions (`agent-deck try`)

**Use when:** the user wants a throwaway playground, a quick experiment, or a scratch repo to dry-run something — "spin up a scratch session", "try this out somewhere disposable", "make a playground".

```bash
# Find-or-create a dated experiment folder and start a session in it
agent-deck try redis-cache            # → <experiments-dir>/2026-07-29-redis-cache/
agent-deck try rds                    # Fuzzy-matches an existing experiment (e.g. redis-cache)
agent-deck try myproject -c gemini    # Non-default tool
agent-deck try myproject --no-session # Create/find the folder only
agent-deck try scratch --sandbox      # Run the session in a Docker sandbox
agent-deck try --list [query]         # List (or fuzzy-search) existing experiments
```

The argument is an **experiment name**, not a prompt. `try` finds or creates `<experiments-dir>/<YYYY-MM-DD>-<name>/`, reuses an existing session for that path if one exists, and otherwise creates one in the `experiments` group and starts it.

**The base directory is configurable** — important when your machine only trusts certain roots for agent workspaces:

```toml
[experiments]
directory = "~/code/tries"    # Default: ~/src/tries
date_prefix = true            # YYYY-MM-DD- prefix on folder names
default_tool = "claude"       # Tool when -c is omitted
```

Note: `try` creates a plain folder, not a git repo — run `git init` in it first if the experiment needs one.

## Watchers

Watchers listen for inbound events (webhooks, push notifications, GitHub events, Slack messages) and route them into conductor sessions. Use them when the user says **"set up a watcher"**, **"listen for webhooks"**, **"route GitHub events to my conductor"**, **"forward ntfy notifications"**, or similar.

Four adapter types are supported:

| Type | Required flag | Typical use |
|------|---------------|-------------|
| `webhook` | `--port` | Generic HTTP listener |
| `github` | `--secret` | GitHub repo webhooks with HMAC verification |
| `ntfy` | `--topic` | ntfy.sh push notifications |
| `slack` | `--topic` | Slack (via Cloudflare Worker bridge) |

```bash
agent-deck watcher create <type> --name <name> <adapter-flags...>
agent-deck watcher start <name>
agent-deck watcher list                # health + events/hour
agent-deck watcher test <name>         # synthetic event (verify routing)
```

Full conversational setup flow is available as a separate skill:

```bash
agent-deck watcher install-skill watcher-creator
```

After running the install command, read the installed `watcher-creator/SKILL.md` to walk the user through adapter selection, required settings, and configuring the effective watcher data dir's `clients.json` routing (`${XDG_DATA_HOME:-$HOME/.local/share}/agent-deck/watcher/clients.json` for new users; legacy `~/.agent-deck/watcher/clients.json` when existing watcher state is present).

See `agent-deck watcher --help` for the full command surface and per-adapter examples.
