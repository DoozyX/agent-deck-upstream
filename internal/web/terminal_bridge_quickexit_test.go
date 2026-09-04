//go:build !windows

package web

// Remote-attach scenario matrix (review round 8). Rounds 6 and 7 each fixed one
// row and broke the next one in the same code, so every row below is pinned by
// a named test and they are listed together to make a gap visible:
//
//	(a) healthy remote attach, user re-clicks the same tile -> terminal
//	    untouched, no teardown, no new WebSocket.
//	      tests/web/unit/state.test.js
//	        "leaves attempt alone when re-selecting a HEALTHY remote tile"
//	      tests/web/e2e/fleet-pane.spec.js
//	        "re-clicking a HEALTHY remote tile leaves the live terminal attached"
//	(b) attach failed with REMOTE_ATTACH_FAILED, user re-clicks the tile -> a
//	    new attach attempt is made.
//	      tests/web/unit/state.test.js
//	        "bumps attempt when re-selecting a FAILED remote tile"
//	      tests/web/e2e/fleet-pane.spec.js
//	        "re-clicking a failed remote tile retries the attach"
//	(c) ssh cannot connect (exit 255 inside the grace) -> fatal
//	    REMOTE_ATTACH_FAILED frame, no session_closed.
//	      TestQuickExitIsFatalOnlyForRemote/remote…,
//	      TestRemoteAttachFailureStaysFatalWhenSSHPrints (this file)
//	(d) attach succeeds and the remote session ends inside the grace (output was
//	    written) -> session_closed, no fatal frame.
//	      TestRemoteSessionThatRanAndExitedIsNotFatal (this file)
//	(e) the client closes the WS during the grace -> no fatal frame attempted,
//	    the child is reaped exactly once, and Close never signals a reaped PID.
//	      TestRemoteQuickExitSkippedWhenWeClosed (this file),
//	      TestCloseDoesNotSignalAReapedPID,
//	      TestCloseAfterQuickExitReapIsSafeEndToEnd
//	      (terminal_bridge_close_reaped_test.go)
//	(f) the local tmux attach path is unchanged for all of the above.
//	      TestQuickExitIsFatalOnlyForRemote/local…  (this file),
//	      TestCloseDoesNotSignalAReapedPID/"still running"  (a live child is
//	      still signalled, which is every local detach),
//	      tests/web/unit/state.test.js "selectLocalSession is idempotent…",
//	      tests/web/e2e/fleet-pane.spec.js "local fatal banner keeps its Restart
//	      action"

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// quickExitFrame starts a bridge over cmd — an attach command that exits well
// inside quickExitGrace — and returns the first text (JSON) frame the bridge
// writes, or the zero value if it writes none before the timeout. tmuxSession
// is what distinguishes a local tmux attach bridge from a remote ssh one (only
// handleRemoteSessionWS passes "").
func quickExitFrame(t *testing.T, cmd *exec.Cmd, tmuxSession string, mapErr wsAttachErrorFunc, wait time.Duration) wsServerMessage {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		writer := newWSConnWriter(conn)
		bridge, err := newPTYBridge(cmd, "sess-quick", tmuxSession, "", writer, mapErr)
		if err != nil {
			t.Errorf("newPTYBridge: %v", err)
			return
		}
		defer bridge.Close()

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
	defer conn.Close()

	deadline := time.Now().Add(wait)
	_ = conn.SetReadDeadline(deadline)
	for time.Now().Before(deadline) {
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			return wsServerMessage{}
		}
		if msgType != websocket.TextMessage {
			continue
		}
		var msg wsServerMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			t.Fatalf("decode frame %q: %v", string(payload), err)
		}
		return msg
	}
	return wsServerMessage{}
}

// TestQuickExitIsFatalOnlyForRemote pins the gate on streamOutput's
// quick-exit branch. The branch exists so a remote ssh attach that dies right
// after connecting (bad host, rejected auth, missing remote binary) surfaces a
// fatal error frame instead of looking attached-but-dead. A LOCAL tmux attach
// that exits fast is an ordinary detach and must keep its pre-existing
// handling — silence on a clean io.EOF, a session_closed status otherwise —
// because TerminalPanel.js does not disable reconnect on
// TERMINAL_ATTACH_FAILED, so a fatal frame there is nothing but a stray
// `[error:TERMINAL_ATTACH_FAILED]` line on a benign exit.
func TestQuickExitIsFatalOnlyForRemote(t *testing.T) {
	t.Run("local tmux attach keeps the non-fatal exit path", func(t *testing.T) {
		// Which of the two non-fatal outcomes lands is a PTY-teardown detail
		// that differs by platform (darwin surfaces io.EOF here, so no frame
		// at all; an EIO-style error yields session_closed). Both are the
		// pre-existing behavior; a fatal error frame is not.
		msg := quickExitFrame(t, exec.Command("sh", "-c", "exit 0"), "web-test-local", mapLocalAttachError, 1500*time.Millisecond)
		if msg.Type == "error" {
			t.Fatalf("local quick exit sent a fatal error frame (code=%s message=%s); want the non-fatal exit path",
				msg.Code, msg.Message)
		}
		if msg.Type != "" && (msg.Type != "status" || msg.Event != "session_closed") {
			t.Fatalf("local quick exit sent type=%q event=%q; want no frame or status/session_closed", msg.Type, msg.Event)
		}
	})

	t.Run("remote ssh attach reports a fatal error frame", func(t *testing.T) {
		mapErr := func(error) (string, string, string) {
			return "REMOTE_ATTACH_FAILED", "failed to attach remote terminal", "Check ssh."
		}
		msg := quickExitFrame(t, exec.Command("sh", "-c", "exit 0"), "", mapErr, 4*time.Second)
		if msg.Type != "error" {
			t.Fatalf("remote quick exit sent type=%q event=%q; want a fatal error frame", msg.Type, msg.Event)
		}
		if msg.Code != "REMOTE_ATTACH_FAILED" {
			t.Fatalf("remote quick exit code = %q, want REMOTE_ATTACH_FAILED", msg.Code)
		}
	})
}

// TestRemoteQuickExitSkippedWhenWeClosed pins the "closed by us" guard. The
// attach process is long-lived and healthy; the bridge is torn down from our
// side inside quickExitGrace (what happens when the WS client goes away — the
// user closes the tab — and serveTerminalWS's deferred Close runs). The PTY
// read then fails because WE closed the fd, which is not an attach failure, so
// no fatal frame may be produced.
//
// TestRemoteWSCloseKillsCommand covers the same timing end-to-end but cannot
// observe this: there the client has already closed its side, so any frame the
// server wrote would be discarded by the failing WriteJSON. Here the client
// stays connected and would see a stray REMOTE_ATTACH_FAILED.
func TestRemoteQuickExitSkippedWhenWeClosed(t *testing.T) {
	mapErr := func(error) (string, string, string) {
		return "REMOTE_ATTACH_FAILED", "failed to attach remote terminal", "Check ssh."
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		writer := newWSConnWriter(conn)
		// tmuxSession "" == the remote shape, the only one the quick-exit
		// branch applies to.
		bridge, err := newPTYBridge(exec.Command("sh", "-c", "sleep 30"), "sess-closed", "", "", writer, mapErr)
		if err != nil {
			t.Errorf("newPTYBridge: %v", err)
			return
		}
		// Well inside quickExitGrace, and the command is still alive.
		time.Sleep(200 * time.Millisecond)
		bridge.Close()

		// Keep serving so the client can observe anything the bridge wrote on
		// its way out.
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
	defer conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	_ = conn.SetReadDeadline(deadline)
	for time.Now().Before(deadline) {
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			break // read deadline or peer close: nothing more is coming.
		}
		if msgType != websocket.TextMessage {
			continue
		}
		var msg wsServerMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			t.Fatalf("decode frame %q: %v", string(payload), err)
		}
		if msg.Type == "error" {
			t.Fatalf("bridge closed by us produced a fatal frame (code=%s message=%s); want none",
				msg.Code, msg.Message)
		}
	}
}

// TestRemoteSessionThatRanAndExitedIsNotFatal is the round-7 (#2) regression
// guard. Round 6 widened quickExitGrace from 3s to
// session.SSHConnectTimeout+5s = 15s to cover the ssh dial, but the quick-exit
// case was still gated on elapsed time alone. That made a remote session which
// simply ENDED inside the window — the agent finished, the user typed `exit`,
// the remote pane died — report a fatal REMOTE_ATTACH_FAILED whose hint reads
// "Check that ssh can reach <host> from the web server host." The diagnosis is
// false, and TerminalPanel.js disables reconnect on that code, so for the one
// feature whose whole purpose is watching a remote agent work, finishing the
// work froze the pane.
//
// The command here does what a real attached session does: emits output, then
// exits cleanly, all well inside the grace. That must land on the ordinary
// exit path (silence on a clean io.EOF, session_closed otherwise), never a
// fatal frame.
func TestRemoteSessionThatRanAndExitedIsNotFatal(t *testing.T) {
	mapErr := func(error) (string, string, string) {
		return "REMOTE_ATTACH_FAILED", "failed to attach remote terminal", "Check ssh."
	}

	// tmuxSession "" is the remote shape — the only one the quick-exit branch
	// applies to at all.
	cmd := exec.Command("sh", "-c", `printf "remote-shell\r\n"; exit 0`)
	msg := quickExitFrame(t, cmd, "", mapErr, 4*time.Second)

	if msg.Type == "error" {
		t.Fatalf("a remote session that ran and exited sent a fatal error frame (code=%s message=%s hint has the ssh diagnosis); want the ordinary exit path",
			msg.Code, msg.Message)
	}
	if msg.Type != "" && (msg.Type != "status" || msg.Event != "session_closed") {
		t.Fatalf("remote run-then-exit sent type=%q event=%q; want no frame or status/session_closed", msg.Type, msg.Event)
	}
}

// TestRemoteAttachFailureStaysFatalWhenSSHPrints is the other half of the
// round-7 (#2) fix, and the reason attachFailed does not key on "wrote no
// bytes" alone.
//
// A real failed ssh dial is NOT silent: pty.Start points the child's stderr at
// the same pts, so ssh's own "Could not resolve hostname …" / "Operation timed
// out" line is read by streamOutput as ordinary output. A no-output-only test
// would therefore classify every real unreachable host as a session that ran
// and ended, silently undoing round 6. This stands in for that shape: output
// on the way out, then ssh's 255.
func TestRemoteAttachFailureStaysFatalWhenSSHPrints(t *testing.T) {
	mapErr := func(error) (string, string, string) {
		return "REMOTE_ATTACH_FAILED", "failed to attach remote terminal", "Check ssh."
	}

	cmd := exec.Command("sh", "-c",
		`printf "ssh: connect to host build port 22: Operation timed out\r\n"; exit 255`)
	msg := quickExitFrame(t, cmd, "", mapErr, 4*time.Second)

	if msg.Type != "error" {
		t.Fatalf("ssh dial failure that printed a diagnostic sent type=%q event=%q; want a fatal error frame", msg.Type, msg.Event)
	}
	if msg.Code != "REMOTE_ATTACH_FAILED" {
		t.Fatalf("code = %q, want REMOTE_ATTACH_FAILED", msg.Code)
	}
}
