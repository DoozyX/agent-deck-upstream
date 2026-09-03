package send

import (
	"errors"
	"testing"
	"time"
)

// A defer wait against a target whose turn-end hook had stopped firing burned
// thirty minutes reporting "target still busy (running)" while the pane sat at
// an idle, empty composer, then dropped the message. "Check the pane, not the
// status field" was the operator's lesson in three consecutive retros; these
// pin it as the tool's job.
func TestStaleBusyStatusIsOverriddenByASustainedIdlePane(t *testing.T) {
	polls := 0
	outcome, err := WaitUntilFree(DeferOptions{
		FetchStatus: func() (string, error) { polls++; return "running", nil },
		PaneIdle:    func() (bool, error) { return true, nil },
		Timeout:     time.Hour,
		Poll:        time.Millisecond,
		Sleep:       func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("wait returned %v, want the stale status overridden", err)
	}
	if !outcome.StaleStatusOverridden {
		t.Error("outcome does not report the override; the caller cannot warn about a hook that stopped firing")
	}
	if outcome.StatusWentIdle {
		t.Error("StatusWentIdle is set: the status never went idle, the pane contradicted it")
	}
	if polls != StaleStatusIdleChecks {
		t.Errorf("overrode after %d polls, want %d consecutive corroborations", polls, StaleStatusIdleChecks)
	}
}

// Interrupting a live turn is the exact harm --defer-if-busy prevents, so a
// pane that merely blinks idle between tool calls must not trip the override.
func TestIntermittentIdleDoesNotOverrideABusyStatus(t *testing.T) {
	calls := 0
	_, err := WaitUntilFree(DeferOptions{
		FetchStatus: func() (string, error) { return "running", nil },
		// Idle every other look: never a sustained run.
		PaneIdle: func() (bool, error) { calls++; return calls%2 == 0, nil },
		Timeout:  50 * time.Millisecond,
		Poll:     time.Millisecond,
		Sleep:    func(time.Duration) {},
		Now:      fakeClock(time.Millisecond),
	})
	var timeoutErr *DeferTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want a defer timeout: a blinking composer must not be read as an ended turn", err)
	}
}

func TestUnreadablePaneNeverCorroborates(t *testing.T) {
	_, err := WaitUntilFree(DeferOptions{
		FetchStatus: func() (string, error) { return "running", nil },
		PaneIdle:    func() (bool, error) { return true, errors.New("capture failed") },
		Timeout:     50 * time.Millisecond,
		Poll:        time.Millisecond,
		Sleep:       func(time.Duration) {},
		Now:         fakeClock(time.Millisecond),
	})
	var timeoutErr *DeferTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want a defer timeout: a failed capture is not evidence of an idle pane", err)
	}
}

// The timeout must be distinguishable, because the caller's remedy is to queue
// the message rather than to fail — a body waited half an hour for should not
// be thrown away.
func TestDeferTimeoutIsATypedErrorCarryingTheLastStatus(t *testing.T) {
	_, err := WaitUntilFree(DeferOptions{
		FetchStatus: func() (string, error) { return "running", nil },
		Timeout:     50 * time.Millisecond,
		Poll:        time.Millisecond,
		Sleep:       func(time.Duration) {},
		Now:         fakeClock(time.Millisecond),
	})
	var timeoutErr *DeferTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %T (%v), want *DeferTimeoutError", err, err)
	}
	if timeoutErr.LastStatus != "running" {
		t.Errorf("LastStatus = %q, want running", timeoutErr.LastStatus)
	}
}

// A status that genuinely goes idle is still the ordinary path, and it must
// not need a pane probe to get there.
func TestStatusGoingIdleStillEndsTheWaitWithoutAPaneProbe(t *testing.T) {
	n := 0
	outcome, err := WaitUntilFree(DeferOptions{
		FetchStatus: func() (string, error) {
			n++
			if n < 3 {
				return "running", nil
			}
			return "waiting", nil
		},
		Timeout: time.Hour,
		Poll:    time.Millisecond,
		Sleep:   func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("wait returned %v, want success once the hook reported the turn ended", err)
	}
	if !outcome.StatusWentIdle || outcome.StaleStatusOverridden {
		t.Errorf("outcome = %+v, want StatusWentIdle with no override", outcome)
	}
}

// fakeClock advances a fixed step per call so timeout tests are deterministic
// and do not sleep.
func fakeClock(step time.Duration) func() time.Time {
	base := time.Unix(0, 0)
	n := 0
	return func() time.Time {
		n++
		return base.Add(time.Duration(n) * step)
	}
}
