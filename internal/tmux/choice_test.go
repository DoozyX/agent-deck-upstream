package tmux

import "testing"

// The pane an orchestrate conductor was showing when its own 15-minute
// heartbeat destroyed the question: an AskUserQuestion decision menu.
const askUserQuestionPane = `  Tell me how you want each handled and I'll run it.

✻ Sautéed for 1m 44s
                                                                      176238 tokens
────────────────────────────────────────────────────────────── bronze-crow-081ded03 ─
❯ 1. Reclaim now + fix the defect (Recommended)
  2. Fix the defect first, let the sweeper reclaim
  3. Reclaim now, file the defect, don't fix it
  4. Hold — I'll look at the sweeper myself
─────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 5  Ctx: 175.5k  ⎇ USS-3617-linear-writer  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on (shift+tab to cycle)`

// A tool permission dialog, box-drawn gutter and all.
const permissionDialogPane = `  Bash command

  rm -rf /media/cdn/public/hls/stream/hrt1enc
  Reclaim disk

╭──────────────────────────────────────────────────╮
│ Do you want to proceed?                          │
│ ❯ 1. Yes                                         │
│   2. Yes, and don't ask again                    │
│   3. No, and tell Claude what to do differently  │
╰──────────────────────────────────────────────────╯
  Esc to cancel · Tab to amend · ctrl+e to explain`

// The SAME conductor one keystroke later, question dismissed, composer empty.
// Its last assistant message still contains a numbered list — the case a naive
// "numbers in the tail" detector would misread as a live menu forever.
const idleAfterNumberedMessagePane = `  You interrupted the decision prompt, so I've held off on both. The two things I
  need from you:

  1. The disk. It isn't recordings — it's /media/cdn/public/hls/stream, ~140G/node.

  2. feature/USS-3617-linear-streams-c1-auth-pin. 4 new commits, never pushed.

✻ Sautéed for 1m 44s
                                                                      176238 tokens
────────────────────────────────────────────────────────────── bronze-crow-081ded03 ─
❯
─────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 5  Ctx: 175.5k  ⎇ USS-3617-linear-writer  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on (shift+tab to cycle)`

// A genuine operator draft. Must stay a draft: the guard's save-clear-restore
// is what protects it from an automated send merging into it (#1409).
const operatorDraftPane = `  Done — commit 532cdd95.

────────────────────────────────────────────────────────────── impl-disp-break1 ─
❯ push the branch and open the PR
─────────────────────────────────────────────────────────────────────────────────
   Model: Sonnet 5  Ctx: 166.5k  ⎇ main  (+0,-0)  𖠰 main`

// A watchdog that QUOTED another session's decision menu into its own
// transcript to escalate it, then finished and went back to its composer.
// Observed 2026-09-04 on watchdog-2026-09-04-dooday-home-assistant-connection:
// the quoted menu never scrolls away (the watchdog is done, nothing else
// prints), so the pane stayed classified awaiting-choice — a permanent phantom
// "a human must answer" on a session whose own prompt is empty, and one that
// `session nudge` then refuses with SESSION_AWAITING_CHOICE, so no supervisor
// can wake it.
//
// The tell is structural: a live modal REPLACES the composer and nothing renders
// below it, so an assistant turn AND a composer line below the menu prove the
// menu is history.
const quotedMenuInScrollbackPane = `     │ without a PR, and a push to main runs CI then auto-deploys Dooday to
     prod.

     ❯ 1. Direct merge to main (Recommended)
          Repo policy. After a clean review and green checks the branch is
     merged
          into main, CI deploys Dooday to prod.
       2. Open a PR, you merge
          Branch pushed and a PR opened with CI green; you merge when ready.
       3. Type something.
       4. Chat about this

     Enter to select · ↑/↓ to navigate · Esc to cancel

⏺ Escalating to user. Conductor is asking a decision prompt (how to land work):
  direct merge vs PR. This is a user choice, not approvable by watchdog.

⏺ Conductor awaiting your decision on landing method (merge vs PR). Menu is on
  screen.

  ===AGENTDECK_DONE=== status=ok summary=escalated user decision prompt

✻ Cooked for 15s · done 1:23 PM
                                                                  48620 tokens
──────── watchdog-2026-09-04-dooday-home-assistant-connection-866a94ad ─
❯

─────────────────────────────
   Model: Haiku 4.5  Ctx: 48.5k  ⎇ main  (+1,-1)
  ⏵⏵ bypass permissions on (shift+tab to cycle)`

// The same quoted menu WITHOUT the conversation resuming below it. A live modal
// can render a free-text field ("Type something.") that also draws a "❯"
// prompt, so a composer line alone must NOT be enough to call the menu stale:
// this one stays true, honouring the file's asymmetry (a false positive only
// escalates to a human; a false negative destroys the question).
const quotedMenuNoTurnBelowPane = `     ❯ 1. Direct merge to main (Recommended)
       2. Open a PR, you merge
       3. Type something.

     Enter to select · ↑/↓ to navigate · Esc to cancel
──────── some-session-866a94ad ─
❯
   Model: Haiku 4.5  Ctx: 48.5k`

func TestPaneAwaitsChoice(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"AskUserQuestion decision menu", askUserQuestionPane, true},
		{"tool permission dialog", permissionDialogPane, true},
		{"idle after a numbered message", idleAfterNumberedMessagePane, false},
		{"operator draft in composer", operatorDraftPane, false},
		{"empty pane", "", false},
		{
			// "esc to interrupt" is the BUSY hint and must never read as a
			// choice; a running session is not waiting on anyone.
			name:    "running session",
			content: "✻ Churning… (12s · ↓ 1.2k tokens)\n  esc to interrupt",
			want:    false,
		},
		{
			// One numbered line with a marker is a list item, not a menu.
			name:    "single numbered option",
			content: "─────────────\n❯ 1. do the thing\n─────────────",
			want:    false,
		},
		{
			// A menu another agent quoted into its transcript, with its own
			// turn and composer rendered below it: history, not a live modal.
			name:    "quoted menu in scrollback",
			content: quotedMenuInScrollbackPane,
			want:    false,
		},
		{
			// No turn below the menu: stay conservative and keep escalating.
			name:    "quoted menu with no turn below",
			content: quotedMenuNoTurnBelowPane,
			want:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PaneAwaitsChoice(tc.content); got != tc.want {
				t.Errorf("PaneAwaitsChoice(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// A modal selection must outrank idle-at-empty-prompt. hasClaudePrompt matches
// permission-dialog text, so without the explicit ordering the idle verdict
// absorbs it and "a human must answer this" becomes unobservable — which is
// exactly how the conductor's stall went unnoticed for over an hour.
func TestClassifySubstate_AwaitingChoiceBeatsIdle(t *testing.T) {
	d := NewPromptDetector("claude")

	for _, tc := range []struct {
		name    string
		content string
	}{
		{"decision menu", askUserQuestionPane},
		{"permission dialog", permissionDialogPane},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.ClassifySubstate(tc.content); got != SubstateAwaitingChoice {
				t.Errorf("ClassifySubstate = %q, want %q", got, SubstateAwaitingChoice)
			}
		})
	}

	if got := d.ClassifySubstate(idleAfterNumberedMessagePane); got != SubstateIdleAtEmptyPrompt {
		t.Errorf("dismissed prompt: ClassifySubstate = %q, want %q", got, SubstateIdleAtEmptyPrompt)
	}
}

// The stuck-❓ regression end to end: a watchdog quoting a menu must classify as
// idle-at-empty-prompt, not awaiting-choice.
func TestClassifySubstate_QuotedMenuIsIdleNotAwaitingChoice(t *testing.T) {
	d := NewPromptDetector("claude")
	if got := d.ClassifySubstate(quotedMenuInScrollbackPane); got != SubstateIdleAtEmptyPrompt {
		t.Errorf("ClassifySubstate = %q, want %q", got, SubstateIdleAtEmptyPrompt)
	}
}
