package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Dead-on-arrival detection for `agent-deck launch --confirm-alive`.
//
// The gap this closes, measured on a real orchestrate conductor rotation
// (2026-09-11 11:49:58 CEST): `agent-deck launch -c codex -m "<brief>" --json`
// returned `{"success": true, "submitted": true, "delivery": "submitted"}` and
// the session was dead three seconds later, its codex rollout carrying
// `codex_error_info: usage_limit` and a zero credit balance. The caller acted on
// `success: true` and destroyed a live run.
//
// Nothing in that JSON was a lie about what it asserts — it is what it asserts
// that is thin. A fresh codex launch with `-m` takes the argv path:
// buildCodexCommandWithPrompt embeds the prompt in the command, so
// StartWithMessageDelivery returns send.DeliverySubmitted the instant tmux
// accepts `new-session`, WITHOUT entering sendMessageWhenReady — the only place
// send.WaitForAgentReady is ever called. So on that path there is no readiness
// wait at all: `delivery: "submitted"` means "the prompt bytes are in the
// spawned process's argv", and `success: true` adds "the row saved". Neither
// observes the agent, and launch returned ~1s BEFORE the rollout's first
// task_started.
//
// The second half is why every downstream surface was silent too.
// startFastDeathWatcher already watches 15s at 250ms ticks and writes the
// spawn-failure sidecar `session show --json` publishes — but it is a goroutine
// inside the `agent-deck launch` process, and nothing on the launch path waits
// for it. out.Success prints, the process exits, the watcher dies with it. The
// mechanism was not missing; it was unreached.
//
// Keeping the CLI alive for a bounded window therefore does two things at once:
// it lets this poll observe the death, and it gives that existing watcher the
// time it needs to record the diagnosis for later readers.
//
// What this deliberately does NOT do: claim the agent is working. The verdict is
// "did not die within the window" — nothing more. The window rides in the JSON
// next to the boolean so a caller can never read more into it than was measured.
// A tool that renders a terminal error and keeps its pane up is alive by this
// measure and would need the pane or transcript read to catch; the incident was
// a death, and a death is what this observes.
//
// It is also never consulted on the queued-at-cap path, which returns before any
// spawn: there is no session to watch, so that launch carries no liveness keys
// at all.
const (
	// defaultLaunchAliveWindow is the post-spawn observation budget. The
	// incident's death landed at ~+2.8s; 5s covers it with margin while keeping
	// an opt-in launch cheap. Nothing catches a death at +30s, and pretending
	// otherwise would be the same overclaim in a new field.
	defaultLaunchAliveWindow = 5 * time.Second

	// launchAlivePollInterval matches the fast-death watcher's own tick, so the
	// two observers cost one tmux round-trip each per interval and neither
	// starves the other.
	launchAlivePollInterval = 250 * time.Millisecond
)

// launchAliveSidecarGrace is how long a vanished pane is held before the
// verdict is published, to let the fast-death watcher land its record. The
// death is already decided at that point; this only decides whether the caller
// also gets the dying output that explains it.
//
// A var rather than a const so tests can shrink it and exercise the
// no-sidecar path without spending a real second per case — the same reason
// usageLimitScanChunkBytes is one.
var launchAliveSidecarGrace = time.Second

// DOA reasons published as `doa_reason`. A value copied from the spawn-failure
// record keeps that record's vocabulary (spawn_died_fast, tmux_start_failed,
// prepare_failed) rather than inventing a parallel one.
const (
	// launchDOAReasonSessionGone: the pane was observed gone and no
	// spawn-failure record explained it within the grace.
	launchDOAReasonSessionGone = "session_gone"
	// launchDOAReasonNoSession: there was no tmux session to observe at all.
	launchDOAReasonNoSession = "no_tmux_session"
)

// launchLivenessProbe is the slice of *session.Instance this check reads. An
// interface so the verdict logic is testable without a tmux server.
type launchLivenessProbe interface {
	Exists() bool
	ExpectsFastExit() bool
	SpawnFailure() *session.SpawnFailureRecord
}

// launchLiveness is the outcome of one bounded post-spawn observation.
type launchLiveness struct {
	// Alive is false ONLY on positive evidence of death. A window that
	// expires with the pane still up reports true — there is no third state,
	// because "unknown" and "still up" are the same observation here.
	Alive bool
	// WindowMS is the budget that was applied, published alongside Alive so a
	// caller reads "survived 5s", never "healthy".
	WindowMS int64
	// ObservedMS is how long the check actually watched before deciding. It is
	// watch-relative, NOT spawn-relative: PostStartSync and the session save run
	// between the spawn and the first poll, so it can trail the real age of the
	// process by seconds.
	ObservedMS int64
	// SpawnElapsedMS is the spawn-relative age of the death, taken from the
	// fast-death record that observed it. 0 when no record explained the death,
	// which is the only case where ObservedMS has to stand in for it.
	SpawnElapsedMS int64
	// Reason is the DOA classification; empty when Alive.
	Reason string
	// Detail is the tool's own dying output when the spawn-failure record
	// carried it. Best-effort: absent is normal, not an error.
	Detail string
}

// addTo merges the verdict into a launch JSON payload.
func (l launchLiveness) addTo(payload map[string]interface{}) {
	payload["alive"] = l.Alive
	payload["liveness_window_ms"] = l.WindowMS
	payload["liveness_observed_ms"] = l.ObservedMS
	if l.Alive {
		return
	}
	payload["doa_reason"] = l.Reason
	if l.SpawnElapsedMS > 0 {
		payload["doa_elapsed_ms"] = l.SpawnElapsedMS
	}
	if l.Detail != "" {
		payload["doa_detail"] = l.Detail
	}
}

// message renders the human/error text for a DOA verdict.
//
// It quotes the spawn-relative age when the record carried one and otherwise
// says plainly that the death happened somewhere inside the window. Printing
// ObservedMS as "after launch" would be the same overclaim this whole change
// exists to remove: it is measured from the first poll, not from the spawn.
func (l launchLiveness) message(title string) string {
	var sb strings.Builder
	if l.SpawnElapsedMS > 0 {
		fmt.Fprintf(&sb, "session %q died %dms after spawn (reason: %s); "+
			"the prompt was delivered but nothing ran it",
			title, l.SpawnElapsedMS, l.Reason)
	} else {
		fmt.Fprintf(&sb, "session %q died within the %dms liveness window (reason: %s); "+
			"the prompt was delivered but nothing ran it",
			title, l.WindowMS, l.Reason)
	}
	if l.Detail != "" {
		sb.WriteString(": ")
		sb.WriteString(firstLines(l.Detail, 3))
	}
	return sb.String()
}

// firstLines trims a dying-output block to its leading lines so an error
// message stays readable; the full text remains in `doa_detail` and in the
// spawn-failure record `session show --json` publishes.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	trimmed := lines
	if len(lines) > n {
		trimmed = lines[:n]
	}
	joined := strings.TrimSpace(strings.Join(trimmed, " | "))
	if len(lines) > n {
		joined += " …"
	}
	return joined
}

// confirmLaunchAlive watches a freshly spawned session for window, returning as
// soon as death is proven and otherwise when the budget runs out.
//
// A one-shot invocation (ExpectsFastExit) is never DOA for exiting: that IS its
// contract. A non-zero exit still arrives here, because
// acknowledgeInitialProcess's completion watch records it as a spawn failure,
// which the first branch reads.
func confirmLaunchAlive(probe launchLivenessProbe, window, poll time.Duration) launchLiveness {
	start := time.Now()
	if probe == nil {
		return launchLiveness{Alive: false, WindowMS: window.Milliseconds(), Reason: launchDOAReasonNoSession}
	}
	if poll <= 0 {
		poll = launchAlivePollInterval
	}
	grace := launchAliveSidecarGrace
	if grace > window {
		grace = window
	}

	var goneAt time.Time
	for {
		if rec := probe.SpawnFailure(); rec != nil {
			return launchLiveness{
				Alive:          false,
				WindowMS:       window.Milliseconds(),
				ObservedMS:     time.Since(start).Milliseconds(),
				SpawnElapsedMS: rec.ElapsedMs,
				Reason:         rec.Reason,
				Detail:         strings.TrimSpace(rec.DyingOutput),
			}
		}

		switch {
		case probe.Exists(), probe.ExpectsFastExit():
			// Up, or down in the one way that is not a failure. Either way a
			// previous disappearance is no longer being held against it.
			goneAt = time.Time{}
		case goneAt.IsZero():
			goneAt = time.Now()
		case time.Since(goneAt) >= grace:
			return launchLiveness{
				Alive:      false,
				WindowMS:   window.Milliseconds(),
				ObservedMS: time.Since(start).Milliseconds(),
				Reason:     launchDOAReasonSessionGone,
			}
		}

		if remaining := window - time.Since(start); remaining <= 0 {
			if !goneAt.IsZero() {
				return launchLiveness{
					Alive:      false,
					WindowMS:   window.Milliseconds(),
					ObservedMS: time.Since(start).Milliseconds(),
					Reason:     launchDOAReasonSessionGone,
				}
			}
			return launchLiveness{
				Alive:      true,
				WindowMS:   window.Milliseconds(),
				ObservedMS: time.Since(start).Milliseconds(),
			}
		} else if remaining < poll {
			time.Sleep(remaining)
		} else {
			time.Sleep(poll)
		}
	}
}
