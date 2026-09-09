package session

import "strings"

// shellsTakingDashC are the interpreters whose own `-c` introduces a script
// body and has nothing to do with resuming a conversation.
var shellsTakingDashC = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true,
}

// CommandIsInstanceBound reports whether a persisted launch command is bound to
// one specific instance or conversation rather than being a reusable base
// command.
//
// Fork and continuation flows bake a whole one-shot launch line into
// Instance.Command: a `cd <source worktree>` preamble, an
// AGENTDECK_INSTANCE_ID export naming the SOURCE instance, and
// `--session-id/--resume/--fork-session` naming the source's conversation.
// That string is correct for the instance it was built for and wrong for any
// other. Callers that copy a command from a template session (quick-create's
// cursor/most-recent inheritance) must not inherit one of these, or the "new"
// session silently re-enters the source's directory and conversation.
//
// This is deliberately a syntactic check over whitespace-delimited tokens, for
// the same reason commandHasToken is: the launch surface is a shell string we
// cannot evaluate without spawning a shell.
func CommandIsInstanceBound(command string) bool {
	if strings.TrimSpace(command) == "" {
		return false
	}

	// Conversation-selecting flags: the argument names a specific
	// conversation, which by definition belongs to another session.
	for _, flag := range []string{"--resume", "--session-id", "--fork-session", "--continue"} {
		if commandHasToken(command, flag) {
			return true
		}
	}

	// The AGENTDECK_* preamble pins the command to the instance it was built
	// for; inheriting it makes a new session impersonate the old one.
	if strings.Contains(command, "AGENTDECK_INSTANCE_ID=") {
		return true
	}

	fields := strings.Fields(command)

	// A `cd <dir> && ...` preamble pins the working directory, overriding
	// whatever project path the new session was given.
	if fields[0] == "cd" {
		return true
	}

	// `-c` is claude/opencode's short --continue, but it is also every
	// shell's "run this script" flag. Only the former is instance-bound.
	for idx, f := range fields {
		if f != "-c" {
			continue
		}
		if idx > 0 && shellsTakingDashC[baseName(fields[idx-1])] {
			continue
		}
		return true
	}

	return false
}

// baseName returns the last path element of a command token, so `/bin/bash`
// and `bash` are recognised alike.
func baseName(token string) string {
	if idx := strings.LastIndex(token, "/"); idx >= 0 {
		return token[idx+1:]
	}
	return token
}
