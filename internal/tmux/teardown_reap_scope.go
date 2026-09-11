// Package tmux — teardown reap SCOPE (2026-09-11 orphan-leak incident).
//
// Every reap set in this package used to be built by walking parent links:
// read `#{pane_pid}`, then `pgrep -P` recursively. That walk can only see
// processes still reachable from the pane. A descendant whose intermediate
// parent has already exited is reparented to PID 1, drops out of the walk, and
// is therefore never captured, never signalled, and outlives the session.
//
// Observed 2026-09-11: a session started a dev stack, ended, and left
//
//	PID 54087  PPID 1   18:19  node .../concurrently/dist/bin/concurrently.js
//	PID 54124  PPID 54087      node .../pnpm/...
//	PID 54275  PPID 54124      node .../apps/core-service/...
//
// alive for 18 minutes on a box that was already OOM-killing processes. The
// dying session reported "ports free at handoff" and it was TRUE — the
// listeners had gone, the process tree had not. Port checks are not evidence
// of a reaped tree.
//
// The surviving handle is the CONTROLLING TERMINAL. A reparented descendant
// keeps the pane's pty until the pane itself dies, so enumerating every
// process on the session's pane ttys finds exactly the processes the parent
// walk misses — and nothing else, because a pty is private to its pane. The
// enumeration must happen BEFORE `kill-session`: once the pane is gone the
// pty is gone and ps reports the orphans with tty `??`, at which point no
// kernel-side handle ties them to the session any more.
//
// Process groups are the signalling half of the same idea. POSIX guarantees a
// process group lies wholly inside one session, so any pgid seen on a pane pty
// is contained in that pane's terminal session: `kill(-pgid, sig)` cannot
// reach a process the session did not start, and unlike a PID list it also
// covers members spawned in the window between capture and kill.
//
// Not covered, deliberately: a descendant that calls setsid() AND drops the
// controlling terminal is invisible to both the parent walk and the tty scope.
// Nothing short of a path/cgroup-scoped supervisor can see it — see the
// orphan-listing recommendation in the incident report.

package tmux

import (
	"errors"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// teardownReapScope is the set of processes that must die with a tmux session:
// the PIDs to verify individually (identity-pinned, see CaptureProcessIdentities)
// plus the process groups to signal wholesale.
type teardownReapScope struct {
	// PanePID is the pane leader, 0 when the probe was indeterminate.
	PanePID int
	// PIDs is the union of the parent-link walk and the pane-tty sweep.
	PIDs []int
	// PGIDs are the process groups observed on the session's pane ttys.
	PGIDs []int
}

// teardownReapScopeFor computes the reap scope for the named tmux session.
// It is teardown-only on purpose: the respawn path needs the narrow
// parent-walk semantics of paneProcessTreeFor, whose result feeds the
// `pid == newPanePID` guard that spares the process the user just restarted.
// Widening that shared helper would let the respawn escalation reach processes
// it deliberately does not own.
func (s *Session) teardownReapScopeFor(target string) teardownReapScope {
	panePID, walked := s.getPaneProcessTreeFor(target)

	scope := teardownReapScope{PanePID: panePID}
	seenPID := make(map[int]struct{}, len(walked)+8)
	addPID := func(pid int) {
		if pid <= 1 {
			return
		}
		if _, dup := seenPID[pid]; dup {
			return
		}
		seenPID[pid] = struct{}{}
		scope.PIDs = append(scope.PIDs, pid)
	}
	for _, pid := range walked {
		addPID(pid)
	}

	ttys := s.paneTTYsFor(target)
	if len(ttys) == 0 {
		return scope
	}

	ownPGID := syscall.Getpgrp()
	seenPGID := make(map[int]struct{}, 4)
	for _, proc := range processesOnTTYs(ttys) {
		addPID(proc.pid)
		// A pgid of 0 or 1 is not a group we may signal, and our own group
		// would take this process down with the session it is tearing down.
		if proc.pgid <= 1 || proc.pgid == ownPGID {
			continue
		}
		if _, dup := seenPGID[proc.pgid]; dup {
			continue
		}
		seenPGID[proc.pgid] = struct{}{}
		scope.PGIDs = append(scope.PGIDs, proc.pgid)
	}

	if extra := len(scope.PIDs) - len(walked); extra > 0 {
		respawnLog.Info("teardown_scope_tty_orphans",
			slog.String("session", logging.SanitizeValue(s.Name)),
			slog.Int("walked", len(walked)),
			slog.Int("tty_only", extra),
			slog.Any("pgids", scope.PGIDs))
	}
	return scope
}

// paneTTYsFor returns the short tty names (as ps prints them, e.g. "ttys043")
// of every pane in the session. `-s` widens list-panes from the current window
// to the whole session, so a multi-window session does not leave panes 2..n
// out of scope.
func (s *Session) paneTTYsFor(target string) map[string]struct{} {
	// Bounded — see tmuxPollTimeout. Runs on the teardown path; a hang here
	// would stall the stop the user asked for.
	out, err := s.runBoundedOutput("list-panes", "-s", "-t", target+":", "-F", "#{pane_tty}")
	// Same WaitDelay contract as parsePanePID: bytes already written are
	// authoritative even when cmd.Wait abandoned the reader.
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		respawnLog.Warn("pane_tty_probe_failed",
			slog.String("session", logging.SanitizeValue(s.Name)),
			slog.String("error", err.Error()))
		return nil
	}
	ttys := make(map[string]struct{}, 2)
	for _, line := range strings.Split(string(out), "\n") {
		name := normalizeTTYName(line)
		if name == "" {
			continue
		}
		ttys[name] = struct{}{}
	}
	return ttys
}

// normalizeTTYName reduces a tmux `#{pane_tty}` value or a ps tty column to a
// comparable short name: "/dev/ttys043" and "ttys043" both yield "ttys043".
// "??" and "-" (ps for "no controlling terminal") yield "" — a process with no
// tty is never in a pane's scope.
func normalizeTTYName(raw string) string {
	name := strings.TrimSpace(raw)
	name = strings.TrimPrefix(name, "/dev/")
	if name == "" || name == "??" || name == "-" {
		return ""
	}
	return name
}

type ttyProcess struct {
	pid  int
	pgid int
}

// processesOnTTYs returns every process whose controlling terminal is one of
// ttys, from a single ps snapshot. A single snapshot matters for the same
// reason it does in discoverMCPChildrenFromPaneTree (#1086): two snapshots can
// disagree under load and drop a process between them.
func processesOnTTYs(ttys map[string]struct{}) []ttyProcess {
	if len(ttys) == 0 {
		return nil
	}
	// #nosec G204 -- fixed binary, fixed argv, no external input.
	out, err := exec.Command("ps", "-Ao", "pid=,pgid=,tty=").Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	var found []ttyProcess
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		tty := normalizeTTYName(fields[2])
		if tty == "" {
			continue
		}
		if _, ok := ttys[tty]; !ok {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 1 {
			continue
		}
		pgid, err := strconv.Atoi(fields[1])
		if err != nil {
			pgid = 0
		}
		found = append(found, ttyProcess{pid: pid, pgid: pgid})
	}
	return found
}

// ensureTeardownScopeDead is EnsureProcessIdentitiesDead plus process-group
// signalling for the groups the scope observed on the session's pane ttys.
//
// A group is only signalled while it still holds a live process from
// `identities` (pinned by start time). That gate is what keeps the group
// signal honest: a pgid whose members are all gone may have been recycled by
// the kernel onto an unrelated new group leader, and the whole point of the
// identity pinning elsewhere in this package is never to signal a stranger.
// While a captured member is alive the pgid cannot have been recycled, and the
// signal reaches siblings spawned after the capture snapshot.
func ensureTeardownScopeDead(scope teardownReapScope, identities []ProcessIdentity, timeout time.Duration) {
	if len(identities) == 0 {
		return
	}
	signalOwnedProcessGroups(scope.PGIDs, identities, syscall.SIGTERM)
	EnsureProcessIdentitiesDead(identities, timeout)
	// Reachable only when the escalation ran out its deadline with captured
	// members still alive — the SIGHUP/SIGTERM-immune class of issue #59. In
	// the common case every member is dead by now, the gate below finds no
	// live identity, and this is a no-op.
	signalOwnedProcessGroups(scope.PGIDs, identities, syscall.SIGKILL)
}

// signalOwnedProcessGroups sends sig to every pgid that still contains a live
// process from identities. See ensureTeardownScopeDead for why the gate exists.
func signalOwnedProcessGroups(pgids []int, identities []ProcessIdentity, sig syscall.Signal) {
	if len(pgids) == 0 {
		return
	}
	alive := filterAliveProcessIdentities(identities)
	if len(alive) == 0 {
		return
	}
	livePGIDs := pgidsOf(alive)
	ownPGID := syscall.Getpgrp()
	for _, pgid := range pgids {
		if pgid <= 1 || pgid == ownPGID {
			continue
		}
		if _, ok := livePGIDs[pgid]; !ok {
			continue
		}
		respawnLog.Info("teardown_scope_group_signal",
			slog.Int("pgid", pgid),
			slog.String("signal", sig.String()))
		// Negative pid addresses the process group. ESRCH (group already
		// empty) is not actionable.
		_ = syscall.Kill(-pgid, sig)
	}
}

// pgidsOf returns the set of process groups the given live processes sit in.
func pgidsOf(identities []ProcessIdentity) map[int]struct{} {
	groups := make(map[int]struct{}, len(identities))
	for _, process := range identities {
		// #nosec G204 -- fixed binary, the only varying arg is an int.
		out, err := exec.Command("ps", "-p", strconv.Itoa(process.PID), "-o", "pgid=").Output()
		if err != nil {
			continue
		}
		if pgid, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && pgid > 1 {
			groups[pgid] = struct{}{}
		}
	}
	return groups
}
