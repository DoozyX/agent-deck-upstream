package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A quick-created session must never inherit a fork/continuation command: the
// field bug had every Shift+N session in a group re-entering the fork source's
// worktree and resuming its conversation.
func TestInheritableQuickCreateCommand(t *testing.T) {
	baked := `cd '/repo/.worktrees/wt' && export AGENTDECK_INSTANCE_ID=5c9afbc2-1788428871; ` +
		`exec claude --session-id "42828f9e" --resume ef19d721 --fork-session --model opus`

	if got := inheritableQuickCreateCommand(baked); got != "" {
		t.Fatalf("baked fork command was inherited: %q", got)
	}
	if got := inheritableQuickCreateCommand("claude --model opus"); got != "claude --model opus" {
		t.Fatalf("reusable command was dropped: %q", got)
	}
	if got := inheritableQuickCreateCommand(""); got != "" {
		t.Fatalf("empty command changed: %q", got)
	}
}

func TestQuickCreateTemplate(t *testing.T) {
	at := func(sec int64) time.Time { return time.Unix(sec, 0) }

	older := &session.Instance{Title: "older", GroupPath: "g", CreatedAt: at(100)}
	newer := &session.Instance{Title: "newer", GroupPath: "g", CreatedAt: at(200)}
	archived := &session.Instance{Title: "archived", GroupPath: "g", CreatedAt: at(300), ArchivedAt: at(301)}
	otherGroup := &session.Instance{Title: "other", GroupPath: "h", CreatedAt: at(400)}

	t.Run("newest live session in the group wins", func(t *testing.T) {
		got := quickCreateTemplate([]*session.Instance{older, newer, otherGroup}, "g")
		if got != newer {
			t.Fatalf("got %v, want newer", got)
		}
	})

	// The field source was archived; archived sessions are hidden from the
	// list, so inheriting from one is invisible to the user.
	t.Run("archived sessions are skipped", func(t *testing.T) {
		got := quickCreateTemplate([]*session.Instance{older, newer, archived}, "g")
		if got != newer {
			t.Fatalf("got %v, want newer", got)
		}
	})

	t.Run("no live session leaves no template", func(t *testing.T) {
		if got := quickCreateTemplate([]*session.Instance{archived, otherGroup}, "g"); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
}
