//go:build !windows

package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/gorilla/websocket"
)

// setupRemoteWSConfig isolates session.LoadUserConfig() to a temp HOME for
// the duration of the test, optionally seeding $HOME/.agent-deck/config.toml
// with body. Mirrors internal/session's setupConductorTest pattern — the
// real ~/.agent-deck/config.toml on the machine running the suite must never
// leak into these tests (handleRemoteSessionWS calls the real
// session.LoadUserConfig(), same as TUI code, per the design's step 2).
func setupRemoteWSConfig(t *testing.T, body string) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")

	if body != "" {
		dir := filepath.Join(tmpHome, ".agent-deck")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir agent-deck dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
}

const remoteWSConfigWithBuildHost = "[remotes.build]\nhost = \"test-host\"\n"

func remoteFleetSnapshotWithSession(remoteName, sessionID string) session.RemoteFleetSnapshot {
	return session.RemoteFleetSnapshot{
		Remotes: []session.RemoteFleetRemote{
			{Name: remoteName, Online: true, Sessions: []session.RemoteSessionInfo{{ID: sessionID, Title: "release"}}},
		},
	}
}

// decodeAPIErrorCode reads and JSON-decodes the api error body from a failed
// websocket dial's HTTP response, returning the "code" field.
func decodeAPIErrorCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var body apiErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error.Code
}

func TestRemoteWSUnauthorized(t *testing.T) {
	setupRemoteWSConfig(t, remoteWSConfigWithBuildHost)
	srv := NewServer(Config{
		ListenAddr:  "127.0.0.1:0",
		Token:       "secret-token",
		RemoteFleet: &fakeRemoteFleetLoader{snapshot: remoteFleetSnapshotWithSession("build", "remote-1")},
	})
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/remote/build/session/remote-1"), nil)
	if err == nil {
		t.Fatal("expected websocket dial error for unauthorized request")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got resp=%v", http.StatusUnauthorized, resp)
	}
}

func TestRemoteWSUnknownRemote404(t *testing.T) {
	// No config.toml at all — cfg.Remotes has no "build" entry.
	setupRemoteWSConfig(t, "")
	srv := NewServer(Config{
		ListenAddr:  "127.0.0.1:0",
		RemoteFleet: &fakeRemoteFleetLoader{snapshot: remoteFleetSnapshotWithSession("build", "remote-1")},
	})
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/remote/build/session/remote-1"), nil)
	if err == nil {
		t.Fatal("expected websocket dial error for unknown remote")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status %d, got resp=%v", http.StatusNotFound, resp)
	}
	if code := decodeAPIErrorCode(t, resp); code != ErrCodeRemoteNotFound {
		t.Fatalf("expected error code %q, got %q", ErrCodeRemoteNotFound, code)
	}
}

func TestRemoteWSUnknownSession404(t *testing.T) {
	setupRemoteWSConfig(t, remoteWSConfigWithBuildHost)
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		// "build" is configured and known to the fleet, but never saw a
		// session called "remote-1" — the scanner snapshot is the source of
		// truth for attachable ids (design step 3).
		RemoteFleet: &fakeRemoteFleetLoader{snapshot: remoteFleetSnapshotWithSession("build", "remote-2")},
	})
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/remote/build/session/remote-1"), nil)
	if err == nil {
		t.Fatal("expected websocket dial error for unknown session")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status %d, got resp=%v", http.StatusNotFound, resp)
	}
	if code := decodeAPIErrorCode(t, resp); code != ErrCodeNotFound {
		t.Fatalf("expected error code %q, got %q", ErrCodeNotFound, code)
	}
}

// TestRemoteWSInvalidHost400 covers handleRemoteSessionWS's
// session.ValidateSSHHost branch (handlers_ws.go): a `[remotes.*]` entry
// whose host would be parsed by ssh as an option (leading "-") must 400
// rather than reach exec.Command with attacker-controlled argv (#1206).
func TestRemoteWSInvalidHost400(t *testing.T) {
	setupRemoteWSConfig(t, "[remotes.build]\nhost = \"-oProxyCommand=evil\"\n")
	srv := NewServer(Config{
		ListenAddr:  "127.0.0.1:0",
		RemoteFleet: &fakeRemoteFleetLoader{snapshot: remoteFleetSnapshotWithSession("build", "remote-1")},
	})
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/remote/build/session/remote-1"), nil)
	if err == nil {
		t.Fatal("expected websocket dial error for invalid ssh host")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status %d, got resp=%v", http.StatusBadRequest, resp)
	}
	if code := decodeAPIErrorCode(t, resp); code != ErrCodeBadRequest {
		t.Fatalf("expected error code %q, got %q", ErrCodeBadRequest, code)
	}
}

func TestRemoteWSNoFleet503(t *testing.T) {
	setupRemoteWSConfig(t, remoteWSConfigWithBuildHost)
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.remoteFleet = nil // same degraded state /api/remotes reports as 503
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/remote/build/session/remote-1"), nil)
	if err == nil {
		t.Fatal("expected websocket dial error when the remote fleet is unavailable")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got resp=%v", http.StatusServiceUnavailable, resp)
	}
}

func TestRemoteWSConnectAttachesAndEchoes(t *testing.T) {
	setupRemoteWSConfig(t, remoteWSConfigWithBuildHost)
	srv := NewServer(Config{
		ListenAddr:  "127.0.0.1:0",
		RemoteFleet: &fakeRemoteFleetLoader{snapshot: remoteFleetSnapshotWithSession("build", "remote-1")},
		// Test seam: skip real ssh entirely, matching what the JS e2e fixture
		// does (design's Verification section).
		RemoteAttachCommand: func(name string, cfg session.RemoteConfig, sessionID string) *exec.Cmd {
			return exec.Command("sh", "-c", "printf READY; cat")
		},
	})
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/remote/build/session/remote-1"), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	expectWSStatusEvent(t, conn, "connected")
	expectWSStatusEvent(t, conn, "ready")
	expectWSStatusEvent(t, conn, "terminal_attached")

	if _, err := readBinaryUntilContains(conn, "READY", 4*time.Second); err != nil {
		t.Fatalf("did not observe READY from remote attach command: %v", err)
	}

	if err := conn.WriteJSON(wsClientMessage{Type: "input", Data: "hi"}); err != nil {
		t.Fatalf("write input: %v", err)
	}
	if _, err := readBinaryUntilContains(conn, "hi", 4*time.Second); err != nil {
		t.Fatalf("input was not echoed back: %v", err)
	}
}

func TestRemoteWSReadOnlyBlocksInput(t *testing.T) {
	setupRemoteWSConfig(t, remoteWSConfigWithBuildHost)
	srv := NewServer(Config{
		ListenAddr:  "127.0.0.1:0",
		ReadOnly:    true,
		RemoteFleet: &fakeRemoteFleetLoader{snapshot: remoteFleetSnapshotWithSession("build", "remote-1")},
		RemoteAttachCommand: func(name string, cfg session.RemoteConfig, sessionID string) *exec.Cmd {
			return exec.Command("sh", "-c", "cat")
		},
	})
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/remote/build/session/remote-1"), nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	var connected wsServerMessage
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatalf("read connected frame: %v", err)
	}
	if connected.Type != "status" || connected.Event != "connected" || !connected.ReadOnly {
		t.Fatalf("expected read-only connected frame, got %+v", connected)
	}
	expectWSStatusEvent(t, conn, "ready")
	expectWSStatusEvent(t, conn, "terminal_attached")

	if err := conn.WriteJSON(wsClientMessage{Type: "input", Data: "hi"}); err != nil {
		t.Fatalf("write input: %v", err)
	}
	var msg wsServerMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if msg.Type != "error" || msg.Code != "READ_ONLY" {
		t.Fatalf("expected READ_ONLY error, got %+v", msg)
	}
}

func TestRemoteWSCloseKillsCommand(t *testing.T) {
	setupRemoteWSConfig(t, remoteWSConfigWithBuildHost)

	var mu sync.Mutex
	var spawned *exec.Cmd
	srv := NewServer(Config{
		ListenAddr:  "127.0.0.1:0",
		RemoteFleet: &fakeRemoteFleetLoader{snapshot: remoteFleetSnapshotWithSession("build", "remote-1")},
		RemoteAttachCommand: func(name string, cfg session.RemoteConfig, sessionID string) *exec.Cmd {
			cmd := exec.Command("sh", "-c", "sleep 30")
			mu.Lock()
			spawned = cmd
			mu.Unlock()
			return cmd
		},
	})
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/remote/build/session/remote-1"), nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	expectWSStatusEvent(t, conn, "connected")
	expectWSStatusEvent(t, conn, "ready")
	expectWSStatusEvent(t, conn, "terminal_attached")

	mu.Lock()
	cmd := spawned
	mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("expected the remote attach command to have been started")
	}
	pid := cmd.Process.Pid

	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(200*time.Millisecond),
	)
	_ = conn.Close()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // ESRCH: process is gone, bridge.Close() did its job.
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive after the websocket closed", pid)
}

// TestRemoteWSOfflineRemoteSessionStillAttachable pins the design's
// documented behavior (handlers_ws.go's remoteFleetHasSession comment): a
// stale/offline remote's last-known sessions must stay attachable — the
// attach itself surfaces any real connectivity failure to the terminal
// rather than the route 404ing a session that may well still be running.
// Every other fixture in this file hardcodes Online: true, so nothing else
// guards against a regression that adds an Online filter here.
func TestRemoteWSOfflineRemoteSessionStillAttachable(t *testing.T) {
	setupRemoteWSConfig(t, remoteWSConfigWithBuildHost)
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		RemoteFleet: &fakeRemoteFleetLoader{snapshot: session.RemoteFleetSnapshot{
			Remotes: []session.RemoteFleetRemote{
				{Name: "build", Online: false, Sessions: []session.RemoteSessionInfo{{ID: "remote-1", Title: "release"}}},
			},
		}},
		RemoteAttachCommand: func(name string, cfg session.RemoteConfig, sessionID string) *exec.Cmd {
			return exec.Command("sh", "-c", "printf READY; cat")
		},
	})
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/remote/build/session/remote-1"), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	expectWSStatusEvent(t, conn, "connected")
	expectWSStatusEvent(t, conn, "ready")
	expectWSStatusEvent(t, conn, "terminal_attached")

	if _, err := readBinaryUntilContains(conn, "READY", 4*time.Second); err != nil {
		t.Fatalf("did not observe READY from remote attach command: %v", err)
	}
}

// TestDefaultRemoteAttachCommand exercises the actual production ssh-command
// builder directly. Every other test overrides Config.RemoteAttachCommand
// (as does the JS e2e fixture), so without this, a bug in the real argv/env
// construction — wrong args, TERM not applied — ships uncaught.
func TestDefaultRemoteAttachCommand(t *testing.T) {
	cfg := session.RemoteConfig{Host: "test-host"}
	cmd := defaultRemoteAttachCommand("build", cfg, "remote-1")

	if got, want := filepath.Base(cmd.Path), "ssh"; got != want {
		t.Fatalf("cmd.Path = %q, want basename %q", cmd.Path, want)
	}

	wantArgs := append([]string{"ssh"}, session.NewSSHRunner("build", cfg).AttachArgs("remote-1")...)
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}

	foundTERM := false
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "TERM=") && strings.TrimPrefix(kv, "TERM=") != "" {
			foundTERM = true
		}
	}
	if !foundTERM {
		t.Fatalf("cmd.Env missing a non-empty TERM (ensureTERM not applied): %v", cmd.Env)
	}
}
