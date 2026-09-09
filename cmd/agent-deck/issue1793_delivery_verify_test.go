package main

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ---------------------------------------------------------------------------
// Issue #1793: `session send` returned {"success":true,"delivery":"unverified"}
// for a 4095-byte payload that never reached the agent.
//
// The failure direction is the point of these tests: when delivery cannot be
// confirmed, the command must NOT report success. A test suite that only
// checks the happy path is how a fix that delivers nothing gets merged.
// ---------------------------------------------------------------------------

// bigMessage returns a payload at or above the size where an unconfirmed send
// is treated as a failure rather than as "we could not tell".
func bigMessage(n int) string {
	const marker = "ISSUE1793-DISTINCTIVE-PAYLOAD-MARKER "
	return marker + strings.Repeat("x", n-len(marker))
}

// TestIssue1793_LargePayloadNeverSeenInPane_IsAFailureNotAnUnverifiedSuccess
// is the exact reported scenario: a boundary-sized prompt to a non-Claude tool
// (Codex), the pane never shows it, the agent never goes active. The old code
// returned deliveryUnverified with a nil error and the CLI exited 0.
func TestIssue1793_LargePayloadNeverSeenInPane_IsAFailureNotAnUnverifiedSuccess(t *testing.T) {
	msg := bigMessage(4095)
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"codex composer, empty, nothing of ours in it\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})

	if err == nil {
		t.Fatal("issue #1793: an unconfirmed large send must not report success")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
	if delivery == deliveryUnverified {
		t.Fatal("issue #1793: transport-only success is exactly the phantom this fixes")
	}
}

// TestIssue1793_LargePayloadVisibleInPane_IsReportedTypedNotSubmitted pins the
// positive direction, and pins it through terminal line wrapping: a pane wraps long
// content at its width and capture-pane returns those wraps as newlines, so a
// byte-exact search for the body fails on any real wide message. Verification
// that cannot see a delivered message is worse than none — it turns working
// sends into failures.
func TestIssue1793_LargePayloadVisibleInPane_IsReportedTypedNotSubmitted(t *testing.T) {
	msg := bigMessage(4095)

	// Render the message the way a 80-column pane would: hard-wrapped.
	var wrapped strings.Builder
	for i := 0; i < len(msg); i += 80 {
		end := i + 80
		if end > len(msg) {
			end = len(msg)
		}
		wrapped.WriteString(msg[i:end])
		wrapped.WriteString("\n")
	}

	// Index 0 is the pre-send baseline capture, the rest are post-send.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"❯ \n", "❯ " + wrapped.String()},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	// Seen in the pane, but the agent never took it up: typed, NOT submitted,
	// and NOT a success. Text sitting unsent in a composer is the reported
	// bug; returning nil here would give the caller exit 0 and
	// "success": true right next to "submitted": false.
	if err == nil {
		t.Fatal("issue #1793: a message that reached the pane but was never submitted must not report success")
	}
	if delivery != deliveryTyped {
		t.Fatalf("delivery: want %q, got %q", deliveryTyped, delivery)
	}
	if fields := (sendDeliveryResult{delivery: delivery}).jsonFields(); fields["submitted"] != false {
		t.Fatalf("typed must report submitted=false in --json, got %v", fields["submitted"])
	}
}

// TestIssue1793_NonClaudeTypedPromptGetsAnAttributableRecoveryEnter reproduces
// the production Codex failure: the initial Enter is swallowed, while the body
// is visibly parked in the pane. Codex takes the non-Claude verification path;
// that path used to report DELIVERY_FAILED after its arrival checks without
// ever trying the one bare Enter that immediately starts the turn.
func TestIssue1793_NonClaudeTypedPromptGetsAnAttributableRecoveryEnter(t *testing.T) {
	const msg = "ISSUE1793 CODEX SUBMIT RECOVERY distinctive prompt body"
	mock := &mockSendRetryTarget{
		// The first read is the pre-send baseline. The first post-send read
		// remains waiting because the original Enter was swallowed; after the
		// recovery Enter the target starts work.
		statuses: []string{"waiting", "waiting", "active"},
		panes:    []string{"codex>\n", "codex> " + msg + "\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, tool: "codex",
	})

	if err != nil {
		t.Fatalf("a recovered Codex submission must succeed: %v", err)
	}
	if delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, delivery)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 1 {
		t.Fatalf("visible prompt with swallowed initial Enter: want one recovery Enter, got %d", got)
	}
}

func TestIssue1793_CodexRecoveryAttemptIsConsumedWhenEnterFails(t *testing.T) {
	const msg = "ISSUE1793 CODEX RECOVERY ATTEMPT distinctive prompt body"
	mock := &mockSendRetryTarget{
		statuses:     []string{"waiting"},
		panes:        []string{"codex>\n", "codex> " + msg + "\n"},
		sendEnterErr: errors.New("tmux send-keys Enter failed"),
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, tool: "codex",
	})

	if err == nil || delivery != deliveryTyped {
		t.Fatalf("failed recovery must stay an unconfirmed typed delivery: delivery=%q err=%v", delivery, err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 1 {
		t.Fatalf("a failed recovery Enter must still consume the one-attempt budget, got %d attempts", got)
	}
}

func TestIssue1793_CodexForeignDraftIsNeverSubmittedByRecovery(t *testing.T) {
	const msg = "ISSUE1793 CODEX FOREIGN DRAFT distinctive prompt body"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		// The body reached scrollback, but another actor replaced the current
		// Codex composer before recovery. The prompt marker is Codex's native
		// `codex>` form, not Claude's glyph.
		panes: []string{"codex>\n", "history: " + msg + "\ncodex> deploy production immediately\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, tool: "codex",
	})

	if err == nil || delivery != deliveryTyped {
		t.Fatalf("foreign-draft recovery must remain unconfirmed: delivery=%q err=%v", delivery, err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("recovery must not submit a foreign Codex draft, got %d Enter presses", got)
	}
}

// TestIssue1793_CodexForeignDraftConsumesRecoveryBudgetAcrossPaneChanges
// covers the race between arrival evidence and recovery: the body is first
// visible, but the composer belongs to another actor when recovery is
// considered. Even if a later capture looks like our draft again, that
// attribution refusal must consume the only recovery opportunity rather than
// risk submitting a concurrently replaced prompt.
func TestIssue1793_CodexForeignDraftConsumesRecoveryBudgetAcrossPaneChanges(t *testing.T) {
	const msg = "ISSUE1793 CODEX CHANGING PANE distinctive prompt body"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes: []string{
			"codex>\n", // pre-send baseline
			"history: " + msg + "\ncodex> deploy production immediately\n",
			"history: " + msg + "\ncodex> " + msg + "\n",
		},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})

	if err == nil || delivery != deliveryTyped {
		t.Fatalf("foreign-draft recovery must remain unconfirmed: delivery=%q err=%v", delivery, err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("an attribution refusal must consume recovery budget; got %d Enter presses after pane changed back", got)
	}
}

// TestIssue1793_CustomCodexCompatibleToolGetsBoundedRecoveryEnter proves the
// recovery capability follows compatible_with rather than the built-in tool
// name, so wrappers retain the same one-Enter safety boundary as native Codex.
func TestIssue1793_CustomCodexCompatibleToolGetsBoundedRecoveryEnter(t *testing.T) {
	const tool = "issue1793_codex_wrapper"
	const msg = "ISSUE1793 CUSTOM CODEX RECOVERY distinctive prompt body"

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	if err := session.SaveUserConfig(&session.UserConfig{Tools: map[string]session.ToolDef{
		tool: {Command: "company-codex-wrapper", CompatibleWith: "codex"},
	}}); err != nil {
		t.Fatalf("save custom Codex-compatible tool: %v", err)
	}
	if !session.IsCodexCompatible(tool) {
		t.Fatal("custom tool with compatible_with=codex must receive Codex recovery behavior")
	}

	mock := &mockSendRetryTarget{
		statuses: []string{"waiting", "waiting", "active"},
		panes:    []string{"codex>\n", "codex> " + msg + "\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, tool: tool,
	})

	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("custom Codex-compatible recovery must submit: delivery=%q err=%v", delivery, err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 1 {
		t.Fatalf("custom Codex-compatible tool: want one bounded recovery Enter, got %d", got)
	}
}

func TestIssue1793_CodexActiveTransitionNeedsNoRecoveryEnter(t *testing.T) {
	const msg = "ISSUE1793 CODEX NORMAL SUBMIT distinctive prompt body"
	mock := &mockSendRetryTarget{
		// Baseline is waiting; the first post-send sample is active, so this
		// normal submission must return before inspecting/nudging the pane.
		statuses: []string{"waiting", "active"},
		panes:    []string{"codex>\n", "history: " + msg + "\ncodex>\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, tool: "codex",
	})

	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("active transition must remain submitted without recovery: delivery=%q err=%v", delivery, err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("already-submitted Codex turn must not receive recovery Enter, got %d", got)
	}
}

func TestIssue1793_CodexWorkingPaneConfirmsSubmittedNoWaitSend(t *testing.T) {
	const msg = "ISSUE1793 BABA CODEX WORKING submitted prompt"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes: []string{
			"codex>\n",
			"• Working\n" + msg + "\n",
		},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("Codex Working pane after a submitted prompt = delivery %q, err %v; want submitted", delivery, err)
	}
}

func TestIssue1793_CodexTimedWorkingPaneConfirmsSubmitted(t *testing.T) {
	const msg = "ISSUE1793 TIMED CODEX WORKING submitted prompt"
	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{
		"codex>\n", "• Working (4s • esc to interrupt)\n" + msg + "\n",
	}}
	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{maxRetries: 3, checkDelay: 0, tool: "codex"})
	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("timed Codex Working pane = delivery %q, err %v; want submitted", delivery, err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("timed Working state must not receive recovery Enter, got %d", got)
	}
}

func TestIssue1793_CodexNormalWorkingPaneConfirmsSubmittedWithoutRecoveryEnter(t *testing.T) {
	const msg = "ISSUE1793 NORMAL CODEX WORKING submitted prompt"
	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{
		"codex>\n", "• Working\n" + msg + "\n",
	}}
	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{maxRetries: 3, checkDelay: 0, tool: "codex"})
	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("normal Codex Working pane = delivery %q, err %v; want submitted", delivery, err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("normal Working state must not receive recovery Enter, got %d", got)
	}
}

func TestIssue1793_CodexSubmittedBodyContainingPromptLiteralDoesNotNeedRecovery(t *testing.T) {
	const msg = "ISSUE1793 quote the literal codex> prompt in the report"
	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{
		"codex>\n", "• Working\n" + msg + "\n",
	}}
	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{maxRetries: 3, checkDelay: 0, tool: "codex"})
	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("submitted Codex body containing codex> = delivery %q, err %v; want submitted", delivery, err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("submitted body containing codex> must not receive duplicate recovery Enter, got %d", got)
	}
}

func TestIssue1793_CodexMultilineSubmittedBodyContainingPromptLiteralDoesNotNeedRecovery(t *testing.T) {
	const msg = "ISSUE1793 first line\ninclude the literal codex> prompt\nfinal line"
	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{
		"codex>\n", "• Working\n" + msg + "\n",
	}}
	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{maxRetries: 3, checkDelay: 0, tool: "codex"})
	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("multiline submitted Codex body containing codex> = delivery %q, err %v; want submitted", delivery, err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("multiline submitted body containing codex> must not receive duplicate recovery Enter, got %d", got)
	}
}

func TestIssue1793_ExecuteSendPublishesMultilineCodexSubmission(t *testing.T) {
	const msg = "ISSUE1793 first line\ninclude the literal codex> prompt\nfinal line"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes: []string{
			"codex>\n",
			"• Working\n" + msg + "\n",
		},
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 3, checkDelay: 0})
	result, err := executeSend(mock, "codex", msg, false, tun)
	if err != nil {
		t.Fatalf("command-facing multiline Codex send failed: %v", err)
	}
	fields := result.jsonFields()
	if fields["delivery"] != deliverySubmitted || fields["submitted"] != true {
		t.Fatalf("command-facing result = %#v, want submitted=true", fields)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("command-facing multiline submission must not receive recovery Enter, got %d", got)
	}
}

func TestIssue1793_CodexTimedWorkingDoesNotSubmitForeignDraftFromExistingTurn(t *testing.T) {
	const msg = "ISSUE1793 PREEXISTING TURN DRAFT"
	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{
		"codex>\n", "• Working (4s • esc to interrupt)\nprior request\n❯ " + msg + "\n",
	}}
	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{maxRetries: 3, checkDelay: 0, tool: "codex"})
	if err == nil || delivery == deliverySubmitted {
		t.Fatalf("pre-existing Working turn with an unsent draft must not submit: delivery=%q err=%v", delivery, err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("foreign draft must not receive recovery Enter, got %d", got)
	}
}

func TestIssue1793_CodexPayloadWorkingLineIsNotSubmissionEvidence(t *testing.T) {
	const msg = "working on the deployment plan"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes: []string{
			"codex>\n",
			msg + "\n",
		},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if err == nil || delivery == deliverySubmitted {
		t.Fatalf("payload text beginning with working must not submit: delivery=%q err=%v", delivery, err)
	}
}

func TestIssue1793_CodexUnrelatedWorkingLineNeedsThisBody(t *testing.T) {
	const msg = "ISSUE1793 ATTRIBUTABLE CODEX BODY"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes: []string{
			"codex>\n",
			"• Working\nunrelated request\n",
		},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if delivery == deliverySubmitted {
		t.Fatalf("unrelated Working text must not submit this request: delivery=%q err=%v", delivery, err)
	}
}

func TestIssue1793_CodexStaleWorkingBodyIsNotNewSubmission(t *testing.T) {
	const msg = "ISSUE1793 STALE CODEX BODY"
	pane := "• Working\n" + msg + "\n"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes:    []string{pane, pane},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if delivery == deliverySubmitted {
		t.Fatalf("stale body and Working text must not submit again: delivery=%q err=%v", delivery, err)
	}
}

func TestIssue1793_CodexBaselineWorkingIsNotNewSubmission(t *testing.T) {
	const msg = "ISSUE1793 BASELINE WORKING CODEX BODY"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes: []string{
			"• Working\ncodex>\n",
			"• Working\n" + msg + "\n",
		},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if delivery == deliverySubmitted {
		t.Fatalf("a pre-existing Codex Working state must not submit this body: delivery=%q err=%v", delivery, err)
	}
}

func TestIssue1793_CodexTimedBaselineWorkingIsNotNewSubmission(t *testing.T) {
	const msg = "ISSUE1793 TIMED BASELINE CODEX BODY"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes: []string{
			"• Working (4s • esc to interrupt)\nprior request\n",
			"• Working (5s • esc to interrupt)\n" + msg + "\n",
		},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if delivery == deliverySubmitted {
		t.Fatalf("a pre-existing timed Codex Working state must not submit this body: delivery=%q err=%v", delivery, err)
	}
}

func TestIssue1793_CodexMultilinePayloadStartingWorkingIsNotSubmissionEvidence(t *testing.T) {
	const msg = "working\nISSUE1793 MULTILINE CODEX BODY"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes: []string{
			"codex>\n",
			"working\nISSUE1793 MULTILINE CODEX BODY\n",
		},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if delivery == deliverySubmitted {
		t.Fatalf("a payload line beginning with working must not impersonate Codex Working: delivery=%q err=%v", delivery, err)
	}
}

func TestIssue1793_CodexAlternateWorkingTextIsNotSubmissionEvidence(t *testing.T) {
	const msg = "ISSUE1793 ALTERNATE CODEX BODY"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes: []string{
			"codex>\n",
			"Working on another request\n" + msg + "\n",
		},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if err == nil || delivery == deliverySubmitted {
		t.Fatalf("alternate UI text must not submit: delivery=%q err=%v", delivery, err)
	}
}

func TestIssue1793_CodexDelayedWorkingBeforeBodyIsNotSubmissionEvidence(t *testing.T) {
	const msg = "ISSUE1793 DELAYED CODEX BODY"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes: []string{
			"codex>\n",
			"• Working\n",
			"• Working\n" + msg + "\n",
		},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if delivery == deliverySubmitted {
		t.Fatalf("Working before this body must not submit: delivery=%q err=%v", delivery, err)
	}
}

func TestIssue1793_CodexWorkingRequiresAttributableEnter(t *testing.T) {
	const msg = "ISSUE1793 WORKING ENTER ATTRIBUTION BODY"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes:    []string{"codex>\n", "• Working\n" + msg + "\ncodex> foreign draft\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if delivery == deliverySubmitted {
		t.Fatalf("Working plus a foreign draft must not submit: delivery=%q err=%v", delivery, err)
	}
}

func TestIssue1793_CodexWorkingBodyMatchIsWhitespaceSafe(t *testing.T) {
	const msg = "request with\nmultiple words"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes: []string{
			"codex>\n",
			"• Working\nrequest with\nmultiple words\n",
		},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 3, checkDelay: 0, tool: "codex",
	})
	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("whitespace-wrapped body with attributable Working line must submit: delivery=%q err=%v", delivery, err)
	}
}

// TestIssue1793_ClaudePath_TypedButNeverSubmitted_IsNotSuccess is the Claude
// half of the same defect. The Claude verification loop treated "the body is
// visible in the pane" as delivery evidence and, at the end of its budget,
// returned deliverySubmitted on the strength of it — re-certifying the exact
// state #1793 is about, on the path most sessions actually use.
//
// Body visible, composer never shows an unsent marker, agent never goes
// active: that is arrival without submission and must fail.
func TestIssue1793_ClaudePath_TypedButNeverSubmitted_IsNotSuccess(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	// The body is on screen, but no composer marker and the status never
	// leaves "waiting" — nothing ever showed the agent taking it up.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"some prior output\n" + msg + "\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 5, checkDelay: 0, verifyDelivery: true,
	})
	if err == nil {
		t.Fatal("issue #1793: body text alone is arrival, not submission, and must not report success")
	}
	if delivery == deliverySubmitted {
		t.Fatal("issue #1793: the Claude path must not promote visible body text to submitted")
	}
	if delivery != deliveryTyped {
		t.Fatalf("delivery: want %q, got %q", deliveryTyped, delivery)
	}
}

// TestIssue1793_ClaudePath_ActiveTransitionStillReportsSubmitted guards the
// other direction: the fix above must not stop the Claude path recognising a
// genuinely accepted turn.
func TestIssue1793_ClaudePath_ActiveTransitionStillReportsSubmitted(t *testing.T) {
	const msg = "please re-run the integration suite against staging"
	mock := &mockSendRetryTarget{
		statuses: []string{"active", "active"},
		panes:    []string{""},
	}

	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		maxRetries: 5, checkDelay: 0, verifyDelivery: true,
	})
	if err != nil {
		t.Fatalf("an agent that went active accepted the turn: %v", err)
	}
	if delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, delivery)
	}
}

// TestIssue1793_TypedIsAFailingExitNotASuccessfulOne pins the contract the
// CLI exposes: JSON, exit code and human text must agree. `typed` carries
// submitted=false, so it must also carry success=false and a nonzero exit.
func TestIssue1793_TypedIsAFailingExitNotASuccessfulOne(t *testing.T) {
	fields := (sendDeliveryResult{delivery: deliveryTyped}).jsonFields()
	if fields["submitted"] != false {
		t.Fatalf("typed must report submitted=false, got %v", fields["submitted"])
	}
	// The command maps a non-nil sendErr to ErrorWithData + os.Exit(1); the
	// statuses that must travel that path are pinned here so a future edit
	// cannot quietly route `typed` into the success branch.
	for _, failing := range []string{deliveryTyped, deliveryNoEvidence, deliveryLineTooLong, deliveryTypedNotSubmitted} {
		if failing == deliverySubmitted {
			t.Fatalf("%q must never be treated as a successful delivery", failing)
		}
		if got := (sendDeliveryResult{delivery: failing}).jsonFields()["submitted"]; got != false {
			t.Errorf("%q must report submitted=false, got %v", failing, got)
		}
	}
}

// TestIssue1793_IdenticalMessageAlreadyOnScreen_IsNotEvidence guards the
// nastiest false positive available to a "is the body in the pane?" check.
// Automated senders repeat themselves — heartbeats, inbox nudges, retries of
// the same body. If the previous copy is still on screen, a naive containment
// check certifies the NEXT send even when that one vanished. Only an increase
// in occurrences counts.
func TestIssue1793_IdenticalMessageAlreadyOnScreen_IsNotEvidence(t *testing.T) {
	msg := bigMessage(4095)
	// The same body is already in the pane before the send, and the pane
	// never changes afterwards: the new send went nowhere.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"❯ " + msg + "\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err == nil {
		t.Fatal("a leftover copy of an identical message must not certify a send that vanished")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
}

// TestIssue1793_LargePayload_IdleAgentGoingActiveIsSubmitted: an agent that
// was idle and starts working necessarily received what it is working on,
// even if its TUI never echoes the body. That is the one signal here strong
// enough to claim submission.
func TestIssue1793_LargePayload_IdleAgentGoingActiveIsSubmitted(t *testing.T) {
	msg := bigMessage(4095)
	// Index 0 is the pre-send baseline: the agent was idle, so going active
	// afterwards is a transition attributable to this send.
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting", "active"},
		panes:    []string{"❯ \n", "thinking…\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delivery != deliverySubmitted {
		t.Fatalf("delivery: want %q, got %q", deliverySubmitted, delivery)
	}
}

// TestIssue1793_AlreadyActiveAgent_IsNotEvidenceOfArrival is the second half
// of the same trap as the leftover-copy test, and the more dangerous one: a
// pane that was ALREADY working is still working a moment later whether or not
// it received anything. Accepting "it is active now" as proof would hand back
// success for a message that vanished into a busy agent — the #1793 phantom,
// reintroduced through the status signal instead of the pane signal. Only a
// transition from not-active to active counts.
func TestIssue1793_AlreadyActiveAgent_IsNotEvidenceOfArrival(t *testing.T) {
	msg := bigMessage(4095)
	// Busy before the send, busy throughout, and the body never appears.
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes:    []string{"thinking…\n"},
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err == nil {
		t.Fatal("an agent that was already busy before the send does not prove the send arrived")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
}

// TestIssue1793_SmallPayloadKeepsTheBestEffortContract: below the size where
// canonical-buffer loss can happen, an unmatched pane is far more likely to be
// a rendering quirk than a lost message. Those keep reporting `unverified`
// rather than becoming a wall of new failures — the honesty fix must not turn
// into a false-alarm generator.
func TestIssue1793_SmallPayloadKeepsTheBestEffortContract(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"nothing of ours here\n"},
	}
	delivery, err := sendWithRetryTarget(mock, "please re-run the integration suite", true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err != nil {
		t.Fatalf("small unmatched sends must stay best-effort, got error: %v", err)
	}
	if delivery != deliveryUnverified {
		t.Fatalf("delivery: want %q, got %q", deliveryUnverified, delivery)
	}
}

// TestIssue1793_UnverifiableMessageSaysSoInsteadOfGuessing: a message with no
// distinctive token cannot be looked for at all. That is "verification
// impossible", which must be reported as unverified — not silently upgraded
// to success-with-evidence and not downgraded to a fabricated failure.
func TestIssue1793_UnverifiableMessageSaysSoInsteadOfGuessing(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"y\n"},
	}
	delivery, err := sendWithRetryTarget(mock, "y", true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delivery != deliveryUnverified {
		t.Fatalf("delivery: want %q, got %q", deliveryUnverified, delivery)
	}
}

// TestIssue1793_LargeMultilinePayloadIsNotFailedForItsTotalSize is the
// regression for a contradiction in an earlier cut of this fix: the transport
// refuses on the longest LINE (canonical buffering is per line), but the CLI
// gated its hard failure on the TOTAL payload size. A 20 KB body of 80-byte
// lines transports fine — the tmux suite proves it against a real pane — yet
// would have been reported as lost whenever the agent's TUI does not echo it.
func TestIssue1793_LargeMultilinePayloadIsNotFailedForItsTotalSize(t *testing.T) {
	// 20 KB total, longest line 80 bytes: far above any total-size threshold,
	// far below every canonical line buffer.
	body := strings.Repeat(strings.Repeat("m", 79)+"\n", 250)
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"a pane that never echoes anything\n"},
	}

	delivery, err := sendWithRetryTarget(mock, body, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err != nil {
		t.Fatalf("a %d-byte payload whose longest line is 79 bytes is deliverable and must not be "+
			"reported as lost: %v", len(body), err)
	}
	if delivery != deliveryUnverified {
		t.Fatalf("delivery: want %q, got %q", deliveryUnverified, delivery)
	}
}

// TestIssue1793_TokenlessOverLongLineIsNotAFreePass closes the door a
// verification check leaves open by construction: a payload with no content
// distinctive enough to search for. If such a message also carries a line long
// enough to be eaten whole, "we could not look" must not come back as exit 0 —
// that is the reported bug reached through the token-less path.
func TestIssue1793_TokenlessOverLongLineIsNotAFreePass(t *testing.T) {
	// Whitespace only: messageDeliveryToken yields nothing to search for.
	body := strings.Repeat(" ", 4095)
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"\n"},
	}

	delivery, err := sendWithRetryTarget(mock, body, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err == nil {
		t.Fatal("an unverifiable message with an over-long line must not report success")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
}

// TestIssue1793_FailedBaselineDisablesTheSignalItBelongsTo: a baseline that
// could not be read is not a baseline of zero. If the pre-send capture fails
// and the pane already holds a copy of a repeated message, treating the
// missing baseline as 0 would read that stale copy as a fresh arrival.
func TestIssue1793_FailedBaselineDisablesTheSignalItBelongsTo(t *testing.T) {
	msg := bigMessage(4095)
	mock := &mockSendRetryTarget{
		statuses: []string{"active"}, // busy before and after: no transition
		panes:    []string{"❯ " + msg + "\n"},
		paneErrs: []error{errors.New("capture failed")}, // baseline unreadable
	}

	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err == nil {
		t.Fatal("with no readable baseline, a pre-existing copy must not certify the send")
	}
	if delivery != deliveryNoEvidence {
		t.Fatalf("delivery: want %q, got %q", deliveryNoEvidence, delivery)
	}
}

// TestIssue1793_CanonicalOverflowIsItsOwnDeliveryStatus: when the transport
// refuses because the pane's canonical buffer cannot hold the line, nothing
// was typed. That is a distinct, non-retryable outcome and must not be
// flattened into the generic send_failed bucket, so callers can tell "the
// composer is untouched, change the message" from "the pipe broke, try again".
func TestIssue1793_CanonicalOverflowIsItsOwnDeliveryStatus(t *testing.T) {
	mock := &mockSendRetryTarget{
		sendKeysErr: fmt.Errorf("send to pane: %w", &tmux.CanonicalOverflowError{
			LineBytes:  4095,
			LimitBytes: 4095,
			TTY:        "/dev/pts/7",
		}),
	}

	delivery, err := sendWithRetryTarget(mock, bigMessage(4095), true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0,
	})
	if err == nil {
		t.Fatal("a refused over-long line must be an error")
	}
	if delivery != deliveryLineTooLong {
		t.Fatalf("delivery: want %q, got %q", deliveryLineTooLong, delivery)
	}
	if !strings.Contains(err.Error(), "canonical") {
		t.Errorf("error should explain the canonical-buffer cause, got: %v", err)
	}
}

// TestIssue1793_CanonicalOverflowSurvivesTheClaudeVerifiedPath: the same
// refusal must classify identically when the Claude verification loop is in
// play, not just on the skip-verify path.
func TestIssue1793_CanonicalOverflowSurvivesTheClaudeVerifiedPath(t *testing.T) {
	mock := &mockSendRetryTarget{
		sendKeysErr: &tmux.CanonicalOverflowError{LineBytes: 2000, LimitBytes: 1024, TTY: "/dev/ttys001"},
	}
	delivery, err := sendWithRetryTarget(mock, bigMessage(2000), false, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, verifyDelivery: true,
	})
	if err == nil {
		t.Fatal("a refused over-long line must be an error on the verified path too")
	}
	if delivery != deliveryLineTooLong {
		t.Fatalf("delivery: want %q, got %q", deliveryLineTooLong, delivery)
	}
}

// TestIssue1793_DeliveryStatusReachesTheJSONContract pins that the new
// statuses are actually machine-readable by the callers that key off them
// (watchers, conductors, bridges), rather than only existing in Go.
func TestIssue1793_DeliveryStatusReachesTheJSONContract(t *testing.T) {
	for _, status := range []string{deliveryLineTooLong, deliveryNoEvidence, deliveryTyped} {
		res := sendDeliveryResult{delivery: status}
		fields := res.jsonFields()
		if fields["delivery"] != status {
			t.Errorf("delivery %q missing from --json fields: %v", status, fields)
		}
	}
}
