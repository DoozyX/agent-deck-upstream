//go:build !windows

package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// newBareBridge builds a bridge around cmd WITHOUT starting streamOutput, so a
// test owns the reap/Close ordering completely. Every field Close touches is
// populated exactly as newPTYBridge populates it.
func newBareBridge(t *testing.T, cmd *exec.Cmd) *tmuxPTYBridge {
	t.Helper()
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	return &tmuxPTYBridge{
		sessionID: "sess-reaped",
		cmd:       cmd,
		ptmx:      ptmx,
		done:      make(chan struct{}),
		startedAt: time.Now(),
	}
}

// TestCloseDoesNotSignalAReapedPID is the round-8 (#1) regression guard.
//
// Round 7 moved the single cmd.Wait earlier: on a quick exit,
// attachFailed -> waitExitCode -> reap reaps the child right there. Close still
// ran syscall.Getpgid(b.cmd.Process.Pid) + Kill(-pgid, SIGTERM) afterwards, on
// a PID the kernel is already free to reuse. The window is not microseconds:
// on the remote-attach-failure path the server never closes the WebSocket (the
// client disables reconnect and leaves the socket open), so serveTerminalWS's
// deferred Close does not run until the user closes the tab. A recycled PID
// would have made that Kill terminate an unrelated process group on the web
// server host.
func TestCloseDoesNotSignalAReapedPID(t *testing.T) {
	t.Run("already reaped: Close must not signal", func(t *testing.T) {
		cmd := exec.Command("sh", "-c", "exit 7")
		b := newBareBridge(t, cmd)

		var signalled atomic.Int32
		b.terminateProcessGroup = func(*os.Process) { signalled.Add(1) }

		// Exactly what the quick-exit path does before the client eventually
		// goes away: classify the exit, which reaps the child.
		if got := b.waitExitCode(); got != 7 {
			t.Fatalf("waitExitCode() = %d, want 7 (the child must actually be reaped here)", got)
		}
		if !b.reaped.Load() {
			t.Fatal("reaped flag not set after waitExitCode reaped the child")
		}

		b.Close()
		// serveTerminalWS's deferred Close, a WriteBinary failure inside
		// streamOutput and an explicit teardown can all land; none of them may
		// re-Wait (cmd.Wait is not re-entrant) or signal.
		b.Close()
		b.reap()

		if n := signalled.Load(); n != 0 {
			t.Fatalf("Close signalled a reaped PID %d time(s); want 0 — Getpgid/Kill on a recycled PID can take down an unrelated process group", n)
		}
		if got := b.exitCode.Load(); got != 7 {
			t.Fatalf("exit code = %d after repeated Close/reap, want 7 — the child must be reaped exactly once", got)
		}
	})

	t.Run("still running: Close signals the live process", func(t *testing.T) {
		cmd := exec.Command("sh", "-c", "sleep 30")
		b := newBareBridge(t, cmd)
		wantPid := cmd.Process.Pid

		var gotPid atomic.Int32
		b.terminateProcessGroup = func(p *os.Process) {
			gotPid.Store(int32(p.Pid)) // #nosec G115 -- test pid fits in int32
			defaultTerminateProcessGroup(p)
		}

		if b.reaped.Load() {
			t.Fatal("reaped flag set before any Wait ran")
		}

		b.Close()

		if int(gotPid.Load()) != wantPid {
			t.Fatalf("Close signalled pid %d, want the live attach process %d", gotPid.Load(), wantPid)
		}
		if !b.reaped.Load() {
			t.Fatal("Close returned without reaping the process it killed")
		}
	})
}

// TestCloseAfterQuickExitReapIsSafeEndToEnd walks the real quick-exit sequence
// through streamOutput over a live WebSocket (remote shape, command exits with
// ssh's 255 inside the grace, so attachFailed reaps it) and then runs the
// deferred Close the way serveTerminalWS eventually does — long after the reap,
// because on this path the server never closes the socket itself. Nothing may
// be signalled at that point.
func TestCloseAfterQuickExitReapIsSafeEndToEnd(t *testing.T) {
	mapErr := func(error) (string, string, string) {
		return "REMOTE_ATTACH_FAILED", "failed to attach remote terminal", "Check ssh."
	}

	var signalled atomic.Int32
	bridgeCh := make(chan *tmuxPTYBridge, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		cmd := exec.Command("sh", "-c", `printf "ssh: Could not resolve hostname build\r\n"; exit 255`)
		ptmx, err := pty.Start(cmd)
		if err != nil {
			t.Errorf("pty.Start: %v", err)
			return
		}
		b := &tmuxPTYBridge{
			sessionID:             "sess-quickexit",
			writer:                newWSConnWriter(conn),
			cmd:                   cmd,
			ptmx:                  ptmx,
			done:                  make(chan struct{}),
			startedAt:             time.Now(),
			mapErr:                mapErr,
			terminateProcessGroup: func(*os.Process) { signalled.Add(1) },
		}
		bridgeCh <- b
		go b.streamOutput()

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws"), nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	// The client deliberately stays open, exactly as TerminalPanel does after a
	// fatal REMOTE_ATTACH_FAILED: reconnect disabled, socket left alone. That is
	// what stretches the gap between the reap and Close.
	defer conn.Close()

	b := <-bridgeCh
	select {
	case <-b.done:
	case <-time.After(10 * time.Second):
		t.Fatal("streamOutput did not finish after the child exited")
	}

	if !b.reaped.Load() {
		t.Fatal("quick-exit classification did not reap the child; the round-8 hazard depends on it having done so")
	}

	// streamOutput's own defer already ran Close once; the WS-teardown Close
	// that serveTerminalWS defers arrives later and must be equally inert.
	b.Close()

	if n := signalled.Load(); n != 0 {
		t.Fatalf("Close signalled after the quick-exit reap %d time(s); want 0", n)
	}
}
