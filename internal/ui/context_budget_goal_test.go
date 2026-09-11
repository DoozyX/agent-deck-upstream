package ui

import "strings"

import "testing"

// The wrap-up instruction is the only thing standing between a session at its
// context ceiling and a successor that inherits files but not purpose. A
// continuation prompt that records what was half-done, and not what the work
// was FOR, produces a successor that confidently finishes the wrong thing.
func TestWrapUpMessageDemandsTheGoalVerbatim(t *testing.T) {
	got := wrapUpMessage("/repo/.agent-deck/handoff/abc/PROMPT.md")

	for _, want := range []string{
		"/repo/.agent-deck/handoff/abc/PROMPT.md", // where to write
		"goal",     // what must survive
		"verbatim", // not paraphrased — a paraphrase is where drift enters
		"goal.md",  // the frozen copy an orchestrate run keeps
	} {
		if !strings.Contains(got, want) {
			t.Errorf("wrap-up message is missing %q:\n%s", want, got)
		}
	}

	// The message is typed into an agent's prompt through tmux. A shell sigil
	// in it has no business there and risks being mangled en route.
	if strings.Contains(got, "$") {
		t.Errorf("wrap-up message must not carry a shell sigil:\n%s", got)
	}
}

// The seed covers the degraded path: when the agent never wrote PROMPT.md the
// prompt is rebuilt from a transcript tail, and the original goal may simply
// not be in it. A successor that guesses there is worse than one that asks.
func TestContinuationSeedDemandsTheGoalBeforeResuming(t *testing.T) {
	got := continuationSeed("HANDOFF BODY MARKER")

	if !strings.Contains(got, "HANDOFF BODY MARKER") {
		t.Fatalf("seed dropped the handoff body:\n%s", got)
	}
	for _, want := range []string{
		"goal",    // state it before resuming
		"goal.md", // where an orchestrate run froze it
		"ask",     // when neither source states it
	} {
		if !strings.Contains(got, want) {
			t.Errorf("seed is missing %q:\n%s", want, got)
		}
	}

	// The goal clause has to reach the model before the transcript does.
	// Behind 32k characters of prior conversation it is an afterthought.
	if strings.Index(got, "goal") > strings.Index(got, "HANDOFF BODY MARKER") {
		t.Errorf("the goal clause must precede the handoff body:\n%s", got)
	}
}
