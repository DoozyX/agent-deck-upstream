package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLaunchAckMarker(t *testing.T) {
	tests := []struct {
		marker   string
		wantExit *int
		wantPID  int
		wantOK   bool
	}{
		{marker: "exit:0", wantExit: intPtr(0), wantOK: true},
		{marker: "exit:17", wantExit: intPtr(17), wantOK: true},
		{marker: "pid:42", wantPID: 42, wantOK: true},
		{marker: "exit:nope", wantOK: false},
		{marker: "pid:0", wantOK: false},
		{marker: "started", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.marker, func(t *testing.T) {
			gotExit, gotPID, gotOK := parseLaunchAckMarker(tt.marker)
			if gotOK != tt.wantOK || gotPID != tt.wantPID {
				t.Fatalf("parseLaunchAckMarker(%q) = (%v, %d, %v), want (%v, %d, %v)", tt.marker, gotExit, gotPID, gotOK, tt.wantExit, tt.wantPID, tt.wantOK)
			}
			if (gotExit == nil) != (tt.wantExit == nil) || gotExit != nil && *gotExit != *tt.wantExit {
				t.Fatalf("parseLaunchAckMarker(%q) exit = %v, want %v", tt.marker, gotExit, tt.wantExit)
			}
		})
	}
}

func intPtr(value int) *int { return &value }

func TestLaunchAckScriptPublishesDrainedOutputAndPreservesExit(t *testing.T) {
	ackPath := filepath.Join(t.TempDir(), "ack")
	command := `printf 'DIAGNOSTIC_ONCE\n'; exit 7`
	cmd := exec.Command("bash", "-c", launchAckScript, "agent-deck-launch-ack", ackPath, command)
	err := cmd.Run()
	if err == nil {
		t.Fatal("launch acknowledgement wrapper returned nil for child exit 7")
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 7 {
		t.Fatalf("wrapper error = %v, want exit status 7", err)
	}

	marker, readErr := os.ReadFile(ackPath)
	if readErr != nil {
		t.Fatalf("read completion marker: %v", readErr)
	}
	text := string(marker)
	if !strings.Contains(text, "exit:7\n") {
		t.Fatalf("completion marker = %q, want exit:7", text)
	}
	if !strings.Contains(text, "DIAGNOSTIC_ONCE") {
		t.Fatalf("completion marker = %q, want drained diagnostic", text)
	}
	if strings.Contains(text, "exit:7\nDIAGNOSTIC_ONCE\n") == false {
		t.Fatalf("completion marker published before diagnostic drain: %q", text)
	}
	for _, suffix := range []string{".output", ".tmp", ".fifo"} {
		if _, err := os.Stat(ackPath + suffix); !os.IsNotExist(err) {
			t.Errorf("temporary file %s still exists (stat error %v)", ackPath+suffix, err)
		}
	}
}

func TestSessionIdentityMismatchIsNotOwned(t *testing.T) {
	if sessionIdentityMatches("$1", "$2") {
		t.Fatal("different tmux session identities must not be treated as owned")
	}
	if !sessionIdentityMatches("$1", "$1") {
		t.Fatal("matching tmux session identity must be treated as owned")
	}
}

func TestKillIfOwnedDoesNotKillRecreatedSession(t *testing.T) {
	skipIfNoTmuxBinary(t)
	name := "launch-ack-owner-race"
	if output, err := exec.Command("tmux", "new-session", "-d", "-s", name, "sleep", "30").CombinedOutput(); err != nil {
		t.Fatalf("create original session: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	sess := &Session{Name: name}
	sess.createdSessionID = sess.sessionIdentity()
	if sess.createdSessionID == "" {
		t.Fatal("original session has no tmux session identity")
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })
	if err := exec.Command("tmux", "kill-session", "-t", name).Run(); err != nil {
		t.Fatalf("delete original session: %v", err)
	}
	if output, err := exec.Command("tmux", "new-session", "-d", "-s", name, "sleep", "30").CombinedOutput(); err != nil {
		t.Fatalf("recreate replacement session: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	if err := sess.KillIfOwned(); err == nil {
		t.Fatal("ownership mismatch unexpectedly allowed cleanup")
	}
	if !sess.Exists() {
		t.Fatal("ownership mismatch cleanup killed the replacement session")
	}
}

func TestWatchInitialProcessCompletionIgnoresRecreatedSession(t *testing.T) {
	skipIfNoTmuxBinary(t)
	name := "launch-ack-watcher-race"
	if output, err := exec.Command("tmux", "new-session", "-d", "-s", name, "sleep", "30").CombinedOutput(); err != nil {
		t.Fatalf("create original session: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	sess := &Session{Name: name}
	sess.createdSessionID = sess.sessionIdentity()
	if sess.createdSessionID == "" {
		t.Fatal("original session has no tmux session identity")
	}
	ackPath := filepath.Join(t.TempDir(), "ack")
	sess.launchAckPath = ackPath
	if err := os.WriteFile(ackPath, []byte("pid:42\n"), 0o600); err != nil {
		t.Fatalf("write launch marker: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })
	if err := exec.Command("tmux", "kill-session", "-t", name).Run(); err != nil {
		t.Fatalf("delete original session: %v", err)
	}
	if output, err := exec.Command("tmux", "new-session", "-d", "-s", name, "sleep", "30").CombinedOutput(); err != nil {
		t.Fatalf("recreate replacement session: %v (%s)", err, strings.TrimSpace(string(output)))
	}

	called := make(chan struct{}, 1)
	sess.WatchInitialProcessCompletion(make(chan struct{}), func(int, string) { called <- struct{}{} })
	if err := os.WriteFile(ackPath, []byte("exit:7\nSTALE\n"), 0o600); err != nil {
		t.Fatalf("write stale completion marker: %v", err)
	}

	select {
	case <-called:
		t.Fatal("stale watcher callback ran for the replacement session")
	case <-time.After(250 * time.Millisecond):
	}
}
