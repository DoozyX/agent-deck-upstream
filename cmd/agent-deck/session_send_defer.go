package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// paneShowsIdleComposer reports whether the target's pane is showing a
// composer with nothing in it — the picture of an agent waiting for input.
//
// It is the corroborating witness for `--defer-if-busy`, not the primary
// signal: the hook-driven status stays authoritative, and this is consulted
// only when that status has been claiming "running" while nothing changes.
// An unreadable pane and an invisible composer both answer "cannot tell",
// which never counts as corroboration.
func paneShowsIdleComposer(ts *tmux.Session) func() (bool, error) {
	if ts == nil {
		return nil
	}
	return func() (bool, error) {
		raw, err := ts.CapturePaneFresh()
		if err != nil {
			return false, err
		}
		draft, composerVisible := send.ComposerDraft(raw, tmux.StripANSI)
		if !composerVisible {
			return false, nil
		}
		return draft == "", nil
	}
}

// deferOrQueue holds delivery until the target is free, and when the defer
// window runs out it QUEUES the message rather than dropping it.
//
// Dropping was the old behaviour and it is the wrong end of the trade. A
// defer wait against a target whose Stop hook had stopped firing burned
// thirty minutes reporting "target still busy (running)" — against a pane
// that was sitting at an idle, empty composer the whole time — and then threw
// the message away. Two separate faults: the status was believed over the
// pane, and half an hour of waiting produced nothing to show for it.
//
// Queueing on timeout is safe in a way that force-sending is not: the runtime
// queue delivers on the target's next turn boundary, so a target that really
// is mid-generation is still never interrupted.
func deferOrQueue(
	out *CLIOutput,
	profile, sessionRef string,
	inst *session.Instance,
	ts *tmux.Session,
	message string,
	timeout time.Duration,
) {
	outcome, err := send.WaitUntilFree(send.DeferOptions{
		FetchStatus: func() (string, error) { return fetchHookDrivenStatus(profile, sessionRef) },
		PaneIdle:    paneShowsIdleComposer(ts),
		Timeout:     timeout,
		Poll:        send.DeferPollInterval,
		Sleep:       time.Sleep,
	})

	if outcome.StaleStatusOverridden {
		// Surfaced, never silent. A hook that stops reporting turn ends is a
		// fault in its own right — the thing that makes `status` unreliable
		// for every other consumer too — and it is invisible unless the one
		// place that detects it says so.
		fmt.Fprintf(os.Stderr,
			"agent-deck: '%s' reported status=%s for %s while its pane sat at an idle, empty composer; "+
				"treating the status as stale and delivering. The turn-end hook for this session is not firing.\n",
			inst.Title, outcome.LastStatus, outcome.Waited.Round(time.Second))
	}

	if err == nil {
		return
	}

	var timeoutErr *send.DeferTimeoutError
	if !errors.As(err, &timeoutErr) {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if eligibilityErr := queueRuntimeEligibilityError(inst); eligibilityErr != "" {
		// Nothing can hold the message for this target, so the timeout is
		// terminal after all — but say why, so it does not read as the tool
		// choosing to discard it.
		out.Error(fmt.Sprintf("%s, and it cannot be queued: %s", timeoutErr.Error(), eligibilityErr), ErrCodeDeliveryFailed)
		os.Exit(1)
	}

	tx, beginErr := sessionSendQueueBegin(inst.ID)
	if beginErr != nil {
		out.Error(fmt.Sprintf("%s, and the runtime queue could not be locked to hold it: %v", timeoutErr.Error(), beginErr), ErrCodeDeliveryFailed)
		os.Exit(1)
	}
	// Released explicitly on every path below, not with defer: each of them
	// ends in os.Exit, which does not run deferred calls — the lock would
	// outlive the process and the next sender would block on it.
	depth, enqueueErr := sessionSendQueueTxEnqueue(tx, message)
	if enqueueErr != nil {
		tx.Release()
		if errors.Is(enqueueErr, session.ErrRuntimeQueueFull) {
			out.Error(fmt.Sprintf("%s, and its runtime message queue is full", timeoutErr.Error()), ErrCodeQueueFull)
		} else {
			out.Error(fmt.Sprintf("%s, and it could not be queued: %v", timeoutErr.Error(), enqueueErr), ErrCodeDeliveryFailed)
		}
		os.Exit(1)
	}
	tx.Release()

	out.Success(
		fmt.Sprintf("Queued message for '%s' after waiting %s for it to finish its turn", inst.Title, timeout),
		map[string]interface{}{
			"success":       true,
			"queued":        true,
			"deferred_out":  true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"queue_depth":   depth,
			"last_status":   timeoutErr.LastStatus,
		})
	os.Exit(0)
}
