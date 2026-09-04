//go:build !windows

package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// quickExitFrame starts a bridge over an attach command that exits
// immediately — well inside quickExitGrace — and returns the first text
// (JSON) frame the bridge writes, or the zero value if it writes none before
// the timeout. tmuxSession is what distinguishes a local tmux attach bridge
// from a remote ssh one (only handleRemoteSessionWS passes "").
func quickExitFrame(t *testing.T, tmuxSession string, mapErr wsAttachErrorFunc, wait time.Duration) wsServerMessage {
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
		bridge, err := newPTYBridge(exec.Command("sh", "-c", "exit 0"), "sess-quick", tmuxSession, "", writer, mapErr)
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
		msg := quickExitFrame(t, "web-test-local", mapLocalAttachError, 1500*time.Millisecond)
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
		msg := quickExitFrame(t, "", mapErr, 4*time.Second)
		if msg.Type != "error" {
			t.Fatalf("remote quick exit sent type=%q event=%q; want a fatal error frame", msg.Type, msg.Event)
		}
		if msg.Code != "REMOTE_ATTACH_FAILED" {
			t.Fatalf("remote quick exit code = %q, want REMOTE_ATTACH_FAILED", msg.Code)
		}
	})
}
