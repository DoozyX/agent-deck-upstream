package send

import (
	"fmt"
	"time"
)

// DeferPollInterval is the fixed cadence at which `session send --defer-if-busy`
// re-checks the target's hook-driven status while holding delivery.
const DeferPollInterval = 2 * time.Second

// maxConsecutiveFetchErrors bounds transient status-fetch failures before
// WaitUntilNotBusy gives up. Without this, a removed or renamed target would
// spin returning an error on every poll for the entire defer timeout.
const maxConsecutiveFetchErrors = 3

// StatusIsBusy reports whether a hook-driven status string represents a target
// that is mid-turn (actively generating) or still booting. These are the only
// states `--defer-if-busy` holds for; every other state
// (waiting/idle/error/stopped/queued/unknown) means the composer is free and
// delivery may proceed.
//
// This keys off the SAME hook-driven signal `agent-deck list --json` reports
// (Claude's UserPromptSubmit hook writes "running", the Stop hook writes
// "waiting"), which is a true turn-finished edge — unlike WaitForAgentReady's
// pane-content-diff heuristic that false-positives to idle during tool calls
// and thinking pauses (issue #1578).
func StatusIsBusy(status string) bool {
	switch status {
	case "running", "starting":
		return true
	default:
		return false
	}
}

// StaleStatusIdleChecks is how many CONSECUTIVE corroborating observations of
// an idle, empty composer are needed before a hook status of "running" is
// treated as stale rather than true.
//
// Five at the 2s poll cadence is ten seconds of an unchanged idle pane. It is
// deliberately not one or two: a target between tool calls can render an empty
// composer for a moment mid-turn, and interrupting a live turn is the exact
// harm --defer-if-busy exists to prevent. Ten seconds of stillness is not that.
const StaleStatusIdleChecks = 5

// DeferTimeoutError reports that the defer window elapsed with the target
// still busy. It is a distinct type because the remedy is not "fail": the
// caller can queue the message for the target's next turn instead of throwing
// away a body it waited half an hour to deliver.
type DeferTimeoutError struct {
	LastStatus string
	Waited     time.Duration
}

func (e *DeferTimeoutError) Error() string {
	return fmt.Sprintf("defer-if-busy: target still busy (%s) after %s", e.LastStatus, e.Waited)
}

// DeferOutcome describes how a defer wait ended, for callers that report it.
type DeferOutcome struct {
	// StatusWentIdle is the ordinary case: the hook reported a finished turn.
	StatusWentIdle bool
	// StaleStatusOverridden reports that the hook status never stopped saying
	// busy, but the pane was corroborated idle long enough to conclude the
	// status was lying. Callers should surface this — a hook that stops
	// reporting turn ends is a fault worth knowing about, not a detail.
	StaleStatusOverridden bool
	// LastStatus is the last status successfully fetched.
	LastStatus string
	// Waited is how long the wait took.
	Waited time.Duration
}

// DeferOptions bundles the wait's inputs. PaneIdle is optional; without it the
// wait is status-only and behaves exactly as it did before corroboration
// existed.
type DeferOptions struct {
	// FetchStatus returns the target's hook-driven status.
	FetchStatus func() (string, error)
	// PaneIdle reports whether the target's pane is showing an idle, empty
	// composer right now. An error (or a nil func) means "cannot tell", which
	// never counts as corroboration.
	PaneIdle func() (bool, error)
	Timeout  time.Duration
	Poll     time.Duration
	Sleep    func(time.Duration)
	// Now is injected for tests; nil means time.Now.
	Now func() time.Time
}

// WaitUntilNotBusy polls fetchStatus until the target is no longer busy (see
// StatusIsBusy), the timeout elapses, or the fetch fails persistently. It is
// the status-only face of WaitUntilFree, kept for callers that have no pane to
// corroborate against.
func WaitUntilNotBusy(fetchStatus func() (string, error), timeout, poll time.Duration, sleep func(time.Duration)) error {
	_, err := WaitUntilFree(DeferOptions{
		FetchStatus: fetchStatus,
		Timeout:     timeout,
		Poll:        poll,
		Sleep:       sleep,
	})
	return err
}

// WaitUntilFree is the core of `session send --defer-if-busy`: rather than
// interrupting a generating target with the composer-draft Ctrl+C guard
// (GuardComposerDraft, issue #1409), the sender holds until the target's turn
// is over, then lets the normal readiness/guard/send pipeline run.
//
// It keys off the hook-driven status first, because that is a true
// turn-finished edge where WaitForAgentReady's pane-content diff is only a
// heuristic. But the hook can stop firing — a wedged turn, a dead notifier, a
// turn that ended while background work kept the Stop hook from landing — and
// then the status says "running" forever. A defer wait against exactly that
// state burned thirty minutes and reported "target still busy (running)" while
// the pane showed an idle, empty composer, and the message was dropped. "Check
// the pane, not the status field" was recorded as the operator's lesson in
// three consecutive retros; PaneIdle makes it the tool's job.
//
// Corroboration is deliberately conservative: only a sustained run of idle
// observations (StaleStatusIdleChecks) overrides the status, and any
// observation that is busy, non-idle, or unreadable resets the run to zero.
func WaitUntilFree(opts DeferOptions) (DeferOutcome, error) {
	poll := opts.Poll
	if poll <= 0 {
		poll = DeferPollInterval
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	started := now()
	deadline := started.Add(opts.Timeout)
	consecutiveErrs := 0
	idleRun := 0
	lastStatus := ""

	for {
		status, err := opts.FetchStatus()
		if err != nil {
			consecutiveErrs++
			if consecutiveErrs >= maxConsecutiveFetchErrors {
				return DeferOutcome{LastStatus: lastStatus, Waited: now().Sub(started)},
					fmt.Errorf("defer-if-busy: giving up after %d consecutive status-fetch errors: %w", consecutiveErrs, err)
			}
		} else {
			consecutiveErrs = 0
			lastStatus = status
			if !StatusIsBusy(status) {
				return DeferOutcome{
					StatusWentIdle: true,
					LastStatus:     status,
					Waited:         now().Sub(started),
				}, nil
			}

			// The status says busy. Ask the pane whether that is true.
			if idle := paneIsIdle(opts.PaneIdle); idle {
				idleRun++
				if idleRun >= StaleStatusIdleChecks {
					return DeferOutcome{
						StaleStatusOverridden: true,
						LastStatus:            status,
						Waited:                now().Sub(started),
					}, nil
				}
			} else {
				idleRun = 0
			}
		}

		if opts.Timeout > 0 && !now().Before(deadline) {
			if lastStatus == "" {
				lastStatus = "unknown"
			}
			return DeferOutcome{LastStatus: lastStatus, Waited: now().Sub(started)},
				&DeferTimeoutError{LastStatus: lastStatus, Waited: opts.Timeout}
		}

		sleep(poll)
	}
}

// paneIsIdle collapses "no probe available", "probe failed" and "pane is
// busy" into the same answer: not corroborated. Only a positive, successful
// reading of an idle pane counts toward overriding a busy status.
func paneIsIdle(probe func() (bool, error)) bool {
	if probe == nil {
		return false
	}
	idle, err := probe()
	return err == nil && idle
}
