package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// allowHeadlessTUIEnv is the escape hatch for the headless-TUI refusal below,
// mirroring AGENT_DECK_ALLOW_OUTER_TMUX for the nested-tmux guard.
const allowHeadlessTUIEnv = "AGENTDECK_ALLOW_HEADLESS_TUI"

// residualTUIArgs reports which arguments are left over once every flag the
// TUI-boot path actually consumes has been accounted for, plus whether help
// was requested. It runs after extractProfileFlag and
// extractAllowRepoScriptsFlag, so -p/--profile and --allow-repo-scripts are
// already gone; what remains that this does not recognize is, by definition,
// something main()'s dispatch switch did not handle.
//
// This exists because the switch has no default: an unrecognized first token
// silently fell through to the bubbletea boot. `agent-deck attach --help`
// (attach is not a subcommand — it is `session attach`) therefore started a
// full TUI instead of printing help, and because its stdout was a pipe nobody
// could see or quit it. It ran for 52 days at ~90% CPU, re-probing the
// configured remote over SSH the whole time.
//
// Flag values are consumed alongside their flag so that `-g list` reports
// nothing left over rather than accusing the group named "list" of being an
// unknown command.
func residualTUIArgs(args []string) (extra []string, help bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "--help" || arg == "-h":
			help = true
			continue
		case strings.HasPrefix(arg, "-g=") ||
			strings.HasPrefix(arg, "--group=") ||
			strings.HasPrefix(arg, "--select="):
			continue
		case arg == "-g" || arg == "--group" || arg == "--select":
			// Consume the value too, exactly as extractGroupFlag and
			// extractSelectFlag do. A dangling flag with no value keeps
			// falling through and is reported, which is correct: the
			// invocation is malformed either way.
			if i+1 < len(args) {
				i++
				continue
			}
		}

		extra = append(extra, arg)
	}

	return extra, help
}

// headlessTUIRefused reports whether the interactive TUI must refuse to boot.
//
// The bubbletea TUI takes raw-mode ownership of stdin/stdout and is only ever
// useful in front of a human. Booted against a pipe it renders into the void,
// has no reachable quit key, and keeps running its refresh loop (including
// remote SSH probes) until something kills it. Refusing costs nothing: there
// has never been a reason to start a TUI nobody can see.
//
// webHeadless (`web --no-tui`) never boots a TUI at all, so it is exempt --
// that is the supported way to run agent-deck from a script or a daemon.
func headlessTUIRefused(isTTY, webHeadless, allowOverride bool) bool {
	if webHeadless || allowOverride {
		return false
	}
	return !isTTY
}

// writeUnknownCommandError explains the leftover arguments and points at the
// subcommands that do exist, so a near-miss like `attach` (meaning `session
// attach`) or `lst` is corrected instead of silently starting a TUI.
func writeUnknownCommandError(w io.Writer, extra []string) {
	fmt.Fprintf(w, "Error: unrecognized argument %q\n", extra[0])
	if len(extra) > 1 {
		fmt.Fprintf(w, "Also unrecognized: %s\n", strings.Join(extra[1:], " "))
	}
	fmt.Fprintln(w)

	if subs := subcommandsContaining(extra[0]); len(subs) > 0 {
		fmt.Fprintf(w, "Did you mean one of these?\n")
		for _, s := range subs {
			fmt.Fprintf(w, "  agent-deck %s\n", s)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "Run 'agent-deck --help' for the full command list,")
	fmt.Fprintln(w, "or 'agent-deck' with no arguments to open the TUI.")
}

// subcommandsContaining finds registered commands whose own subcommands or
// names relate to tok. `attach` is not a top-level command but `session
// attach`, `mcp attach`, `skill attach` and `plugin attach` all exist, and
// that is exactly the mistake that produced the 52-day process.
func subcommandsContaining(tok string) []string {
	nested := map[string][]string{
		"attach": {"session attach", "mcp attach", "plugin attach", "skill attach"},
		"detach": {"mcp detach", "plugin detach", "skill detach"},
		"start":  {"session start"},
		"stop":   {"session stop"},
		"send":   {"session send"},
		"output": {"session output"},
		"show":   {"session show"},
		"fork":   {"session fork"},
	}
	if hits, ok := nested[tok]; ok {
		return hits
	}

	var near []string
	for cmd := range commandRegistry {
		if strings.HasPrefix(cmd, "-") {
			continue
		}
		if strings.HasPrefix(cmd, tok) || strings.HasPrefix(tok, cmd) {
			near = append(near, cmd)
		}
	}
	sort.Strings(near)
	if len(near) > 5 {
		near = near[:5]
	}
	return near
}

// writeHeadlessTUIError states why the TUI refused and names the supported
// non-interactive entry points.
func writeHeadlessTUIError(w io.Writer) {
	fmt.Fprintln(w, "Error: the agent-deck TUI needs an interactive terminal.")
	fmt.Fprintln(w, "stdin and stdout must both be a TTY; one of them is a pipe or redirect.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A TUI booted against a pipe renders where nobody can see it and has no")
	fmt.Fprintln(w, "reachable quit key, so it runs its refresh loop until it is killed.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "For non-interactive use:")
	fmt.Fprintln(w, "  agent-deck list                     # Read session state")
	fmt.Fprintln(w, "  agent-deck session send <id> <msg>  # Drive a session")
	fmt.Fprintln(w, "  agent-deck web --no-tui             # Headless HTTP server")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "To override anyway, set %s=1.\n", allowHeadlessTUIEnv)
}

// allowHeadlessTUI reports whether the escape hatch is set.
func allowHeadlessTUI() bool {
	v := os.Getenv(allowHeadlessTUIEnv)
	return v == "1" || strings.EqualFold(v, "true")
}
