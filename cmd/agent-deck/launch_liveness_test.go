package main

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Unit coverage for the `launch --confirm-alive` verdict.
//
// The behaviour being pinned is the distinction the launch JSON could not make
// before it existed: "the session is up and working" vs "the session is up and
// already dead". These cases run without tmux; the end-to-end contract (flag
// off = byte-identical output, flag on + a dying tool = exit 1 with the tool's
// own error text) lives in launch_confirm_alive_cli_test.go.

// scriptedProbe answers the three questions confirmLaunchAlive asks, as a
// function of elapsed time, so a case can describe a session that dies at a
// chosen moment without a real pane.
type scriptedProbe struct {
	start    time.Time
	fastExit bool
	// gone reports whether the pane is missing at this elapsed time.
	gone func(elapsed time.Duration) bool
	// failure reports the spawn-failure sidecar visible at this elapsed time.
	failure func(elapsed time.Duration) *session.SpawnFailureRecord
}

func (p *scriptedProbe) elapsed() time.Duration { return time.Since(p.start) }

func (p *scriptedProbe) Exists() bool {
	if p.gone == nil {
		return true
	}
	return !p.gone(p.elapsed())
}

func (p *scriptedProbe) ExpectsFastExit() bool { return p.fastExit }

func (p *scriptedProbe) SpawnFailure() *session.SpawnFailureRecord {
	if p.failure == nil {
		return nil
	}
	return p.failure(p.elapsed())
}

func newScriptedProbe() *scriptedProbe { return &scriptedProbe{start: time.Now()} }

// shrinkSidecarGrace shortens the post-death wait for the fast-death watcher so
// the no-sidecar cases finish in milliseconds rather than a real second.
func shrinkSidecarGrace(t *testing.T, d time.Duration) {
	t.Helper()
	previous := launchAliveSidecarGrace
	launchAliveSidecarGrace = d
	t.Cleanup(func() { launchAliveSidecarGrace = previous })
}

// A session that stays up for the whole window is reported alive — and the
// window it survived rides along, because "survived 300ms" is all that was
// measured and the caller must be able to see that.
func TestConfirmLaunchAlive_SurvivorReportsAliveWithTheWindowItSurvived(t *testing.T) {
	got := confirmLaunchAlive(newScriptedProbe(), 300*time.Millisecond, 20*time.Millisecond)

	if !got.Alive {
		t.Fatalf("Alive = false for a session that never died: %+v", got)
	}
	if got.Reason != "" || got.Detail != "" {
		t.Errorf("a live session must carry no DOA reason/detail, got %q / %q", got.Reason, got.Detail)
	}
	if got.WindowMS != 300 {
		t.Errorf("WindowMS = %d, want 300", got.WindowMS)
	}
	if got.ObservedMS < 300 {
		t.Errorf("ObservedMS = %d: the check must actually watch the whole window before "+
			"claiming survival", got.ObservedMS)
	}
}

// The incident's shape: the pane is gone and the fast-death watcher has left a
// record explaining why. The verdict must carry the tool's own words — that
// text is the entire reason a caller can tell a usage limit from a crash — and
// must return as soon as it has them rather than idling out the budget.
func TestConfirmLaunchAlive_SpawnFailureRecordIsTheVerdictAndCarriesToolText(t *testing.T) {
	const toolText = "You've hit your usage limit. Try again at Sep 16th, 2026 4:41 PM."
	probe := newScriptedProbe()
	probe.gone = func(elapsed time.Duration) bool { return elapsed >= 30*time.Millisecond }
	probe.failure = func(elapsed time.Duration) *session.SpawnFailureRecord {
		if elapsed < 30*time.Millisecond {
			return nil
		}
		return &session.SpawnFailureRecord{Reason: "spawn_died_fast", DyingOutput: "  " + toolText + "\n"}
	}

	got := confirmLaunchAlive(probe, 3*time.Second, 10*time.Millisecond)

	if got.Alive {
		t.Fatalf("Alive = true for a session with a spawn-failure record: %+v", got)
	}
	if got.Reason != "spawn_died_fast" {
		t.Errorf("Reason = %q, want the record's own vocabulary %q", got.Reason, "spawn_died_fast")
	}
	if got.Detail != toolText {
		t.Errorf("Detail = %q, want the tool's dying output %q", got.Detail, toolText)
	}
	if got.ObservedMS >= 3000 {
		t.Errorf("ObservedMS = %d: a proven death must end the watch, not wait out the window",
			got.ObservedMS)
	}
	if msg := got.message("orchestrate-c2"); !strings.Contains(msg, toolText) ||
		!strings.Contains(msg, "orchestrate-c2") {
		t.Errorf("message() must name the session and quote the tool, got %q", msg)
	}
}

// A pane that vanishes with no record still has to be reported dead. The
// verdict waits out the sidecar grace first, because the record is the only
// thing that can explain the death to the caller.
func TestConfirmLaunchAlive_VanishedPaneWithoutRecordIsStillDOA(t *testing.T) {
	shrinkSidecarGrace(t, 60*time.Millisecond)
	probe := newScriptedProbe()
	probe.gone = func(elapsed time.Duration) bool { return elapsed >= 20*time.Millisecond }

	got := confirmLaunchAlive(probe, 3*time.Second, 10*time.Millisecond)

	if got.Alive {
		t.Fatalf("Alive = true for a vanished pane: %+v", got)
	}
	if got.Reason != launchDOAReasonSessionGone {
		t.Errorf("Reason = %q, want %q", got.Reason, launchDOAReasonSessionGone)
	}
	if got.Detail != "" {
		t.Errorf("Detail = %q, want empty: nothing explained this death", got.Detail)
	}
	if got.ObservedMS < 60 {
		t.Errorf("ObservedMS = %d: the grace must be spent giving the fast-death watcher a "+
			"chance to record the cause", got.ObservedMS)
	}
}

// The grace can never outlive the window. A caller that asked for 100ms gets an
// answer in about 100ms, not in a second.
func TestConfirmLaunchAlive_GraceNeverOutlivesTheWindow(t *testing.T) {
	shrinkSidecarGrace(t, time.Second)
	probe := newScriptedProbe()
	probe.gone = func(time.Duration) bool { return true }

	start := time.Now()
	got := confirmLaunchAlive(probe, 100*time.Millisecond, 10*time.Millisecond)
	elapsed := time.Since(start)

	if got.Alive || got.Reason != launchDOAReasonSessionGone {
		t.Fatalf("want a session_gone verdict, got %+v", got)
	}
	if elapsed > 600*time.Millisecond {
		t.Errorf("took %v for a 100ms window: the sidecar grace must be clamped to the budget",
			elapsed)
	}
}

// A one-shot invocation (a bounded `codex exec`, the DeepSeek headless profile)
// exits by contract. Reporting that as dead-on-arrival would make the flag
// unusable for exactly the automation most likely to set it. A one-shot that
// exits NON-zero still fails, because that arrives as a spawn-failure record.
func TestConfirmLaunchAlive_OneShotExitIsNotDeath(t *testing.T) {
	shrinkSidecarGrace(t, 30*time.Millisecond)
	probe := newScriptedProbe()
	probe.fastExit = true
	probe.gone = func(elapsed time.Duration) bool { return elapsed >= 20*time.Millisecond }

	got := confirmLaunchAlive(probe, 200*time.Millisecond, 10*time.Millisecond)
	if !got.Alive {
		t.Fatalf("a one-shot that completed was reported DOA: %+v", got)
	}

	probe = newScriptedProbe()
	probe.fastExit = true
	probe.gone = func(elapsed time.Duration) bool { return elapsed >= 20*time.Millisecond }
	probe.failure = func(elapsed time.Duration) *session.SpawnFailureRecord {
		if elapsed < 20*time.Millisecond {
			return nil
		}
		return &session.SpawnFailureRecord{Reason: "tmux_start_failed", DyingOutput: "exit status 7"}
	}

	got = confirmLaunchAlive(probe, 200*time.Millisecond, 10*time.Millisecond)
	if got.Alive {
		t.Fatalf("a one-shot that exited non-zero must still be DOA: %+v", got)
	}
	if got.Reason != "tmux_start_failed" || got.Detail != "exit status 7" {
		t.Errorf("verdict lost the recorded cause: %+v", got)
	}
}

// A single missed poll must not condemn a live session: the pane has to stay
// gone for the whole grace before the verdict flips.
func TestConfirmLaunchAlive_TransientMissDoesNotCondemnALiveSession(t *testing.T) {
	shrinkSidecarGrace(t, 100*time.Millisecond)
	probe := newScriptedProbe()
	probe.gone = func(elapsed time.Duration) bool {
		return elapsed >= 30*time.Millisecond && elapsed < 60*time.Millisecond
	}

	got := confirmLaunchAlive(probe, 400*time.Millisecond, 10*time.Millisecond)
	if !got.Alive {
		t.Fatalf("a session that came back inside the grace was reported dead: %+v", got)
	}
}

// No pane at all is the most complete death there is; it must not read as a
// survivor just because there was nothing to watch.
func TestConfirmLaunchAlive_NilProbeIsDOA(t *testing.T) {
	got := confirmLaunchAlive(nil, time.Second, 10*time.Millisecond)
	if got.Alive || got.Reason != launchDOAReasonNoSession {
		t.Fatalf("want a %q verdict for a session with no tmux session, got %+v",
			launchDOAReasonNoSession, got)
	}
}

// addTo is the JSON contract. The keys must appear exactly when the check ran,
// and doa_* only on a death — a caller keying off `alive` must never find it
// missing on an opted-in launch, nor present on one that did not opt in.
func TestLaunchLivenessAddTo_PublishesTheMeasurementAndOnlyTheMeasurement(t *testing.T) {
	alive := map[string]interface{}{}
	launchLiveness{Alive: true, WindowMS: 5000, ObservedMS: 5004}.addTo(alive)
	if alive["alive"] != true || alive["liveness_window_ms"] != int64(5000) ||
		alive["liveness_observed_ms"] != int64(5004) {
		t.Errorf("live payload = %v", alive)
	}
	if _, ok := alive["doa_reason"]; ok {
		t.Error("a live session must not carry doa_reason")
	}

	dead := map[string]interface{}{}
	launchLiveness{WindowMS: 5000, ObservedMS: 2363, Reason: "spawn_died_fast", Detail: "boom"}.addTo(dead)
	if dead["alive"] != false || dead["doa_reason"] != "spawn_died_fast" || dead["doa_detail"] != "boom" {
		t.Errorf("dead payload = %v", dead)
	}

	noDetail := map[string]interface{}{}
	launchLiveness{Reason: launchDOAReasonSessionGone}.addTo(noDetail)
	if _, ok := noDetail["doa_detail"]; ok {
		t.Error("doa_detail must be omitted when nothing explained the death, not empty-stringed")
	}
}

func TestResolveLaunchAliveWindow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		confirm bool
		window  string
		want    time.Duration
		wantErr string
	}{
		{name: "off by default", want: 0},
		{name: "flag alone takes the default", confirm: true, want: defaultLaunchAliveWindow},
		{name: "explicit window", confirm: true, window: "12s", want: 12 * time.Second},
		{name: "surrounding space is not a value", confirm: true, window: "  2s ", want: 2 * time.Second},
		// Ignoring a flag the caller set is how this class of bug starts, so an
		// orphaned --alive-window is refused rather than silently dropped.
		{name: "window without the flag is refused", window: "5s", wantErr: "requires --confirm-alive"},
		{name: "unparseable window", confirm: true, window: "soon", wantErr: "invalid --alive-window"},
		{name: "zero window", confirm: true, window: "0s", wantErr: "must be positive"},
		{name: "negative window", confirm: true, window: "-1s", wantErr: "must be positive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveLaunchAliveWindow(tc.confirm, tc.window)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("window = %v, want %v", got, tc.want)
			}
		})
	}
}
