//go:build !windows

package web

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestNewPTYBridge_RunsArbitraryCommand pins the terminal_bridge.go split:
// newPTYBridge must run any *exec.Cmd through a local PTY and stream its
// output over the writer, with no tmux involved at all — this is the seam
// handleRemoteSessionWS attaches an ssh command through (newTmuxPTYBridge is
// now a thin existence-check + tmuxAttachCommand wrapper on top of it).
func TestNewPTYBridge_RunsArbitraryCommand(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		writer := newWSConnWriter(conn)
		bridge, err := newPTYBridge(exec.Command("sh", "-c", "printf ARBITRARY_OK"), "sess-arb", "", "", writer, nil)
		if err != nil {
			t.Errorf("newPTYBridge: %v", err)
			return
		}
		defer bridge.Close()

		// Keep the connection open (and the bridge's streamOutput goroutine
		// alive) until the client disconnects.
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

	received, err := readBinaryUntilContains(conn, "ARBITRARY_OK", 4*time.Second)
	if err != nil {
		t.Fatalf("did not observe command output: %v (got %q)", err, trimForError(received, 200))
	}
}
