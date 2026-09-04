package session

import "testing"

// #: quick-create copied a forked session's baked one-shot launch command
// verbatim into brand-new sessions, so every "new" session in the group
// re-entered the fork source's worktree and resumed its conversation.
func TestCommandIsInstanceBound(t *testing.T) {
	// The exact command observed in the field on the poisoned sessions.
	baked := `cd '/Users/x/repo/.worktrees/wt' && export AGENTDECK_INSTANCE_ID=5c9afbc2-1788428871; ` +
		`export AGENTDECK_PROFILE=default; exec claude --session-id "42828f9e-629e-4a1f-b14e-a96ab66920cd" ` +
		`--resume ef19d721-3f8b-4aae-8c21-6a8385ec01d0 --fork-session --name review-cont-5c9afbc2 ` +
		`--dangerously-skip-permissions --model opus`

	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"field baked fork command", baked, true},
		{"resume alone", "claude --resume ef19d721-3f8b-4aae-8c21-6a8385ec01d0", true},
		{"resume with equals", "claude --resume=ef19d721", true},
		{"session id alone", `claude --session-id "42828f9e"`, true},
		{"fork-session alone", "claude --fork-session", true},
		{"continue long", "claude --continue", true},
		{"continue short", "claude -c", true},
		{"instance id preamble", "export AGENTDECK_INSTANCE_ID=abc; exec claude", true},
		{"cd preamble", "cd '/some/worktree' && claude", true},

		{"plain claude", "claude", false},
		{"claude with reusable flags", "claude --dangerously-skip-permissions --model opus", false},
		{"codex", "codex", false},
		{"shell", "", false},
		{"resume as a substring only", "claude --model resume-preview", false},
		{"cd inside a later word", "claude --model cd", false},
		{"shell -c is not continue", "bash -c 'exec claude'", false},
		{"absolute shell -c is not continue", "/bin/zsh -c 'exec codex'", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CommandIsInstanceBound(tc.command); got != tc.want {
				t.Fatalf("CommandIsInstanceBound(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
