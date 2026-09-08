package tmux

import (
	"context"
	"fmt"
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

func TestWatchInitialProcessCompletionRetriesIndeterminateIdentityProbe(t *testing.T) {
	dir := t.TempDir()
	countPath := filepath.Join(dir, "identity-calls")
	writeFakeTmux(t, dir, "if [ \"$1\" = \"-u\" ]; then shift; fi\n"+
		"if [ \"$1\" = \"-L\" ]; then shift 2; fi\n"+"if [ \"$1\" = \"display-message\" ]; then\n"+"  n=0; [ -f "+shellQuote(countPath)+" ] && n=$(cat "+shellQuote(countPath)+")\n"+"  n=$((n + 1)); echo $n > "+shellQuote(countPath)+"\n"+"  if [ $n -eq 1 ]; then echo 'server busy' >&2; exit 1; fi\n"+"  echo '$owned'\n"+"  exit 0\n"+"fi\nexit 1\n")

	ackPath := filepath.Join(t.TempDir(), "ack")
	if err := os.WriteFile(ackPath, []byte("exit:7\nTRANSIENT_PROBE_DIAGNOSTIC\n"), 0o600); err != nil {
		t.Fatalf("write acknowledgement: %v", err)
	}
	sess := &Session{Name: "probe-retry", createdSessionID: "$owned", launchAckPath: ackPath}
	called := make(chan string, 1)
	sess.WatchInitialProcessCompletion(make(chan struct{}), func(code int, diagnostic string) {
		called <- fmt.Sprintf("%d:%s", code, diagnostic)
	})
	select {
	case got := <-called:
		if got != "7:TRANSIENT_PROBE_DIAGNOSTIC" {
			t.Fatalf("callback = %q, want completion after retry", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher abandoned marker after an indeterminate identity probe")
	}
}

func TestKillIfOwnedPerformsOwnedTeardown(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls")
	writeFakeTmux(t, dir, "if [ \"$1\" = \"-u\" ]; then shift; fi\n"+"if [ \"$1\" = \"-L\" ]; then shift 2; fi\n"+"echo \"$*\" >> "+shellQuote(callLog)+"\n"+"case \"$1\" in\n"+"display-message) echo '$owned' ;;\n"+"list-panes) echo \"$TEARDOWN_PID\" ;;\n"+"kill-session) ;;\n"+"*) exit 0 ;;\n"+"esac\n")
	proc := exec.Command("sh", "-c", "sleep 30 & wait")
	if err := proc.Start(); err != nil {
		t.Fatalf("start owned process: %v", err)
	}
	t.Setenv("TEARDOWN_PID", fmt.Sprint(proc.Process.Pid))
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	logPath := filepath.Join(LogDir(), "owned-teardown.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("create log directory: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("legacy"), 0o600); err != nil {
		t.Fatalf("create legacy log: %v", err)
	}

	sess := &Session{Name: "owned-teardown", createdSessionID: "$owned"}
	if err := sess.KillIfOwned(); err != nil {
		t.Fatalf("KillIfOwned: %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("legacy log remains, stat error %v", err)
	}
	_ = proc.Wait()
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read tmux calls: %v", err)
	}
	if !strings.Contains(string(calls), "kill-session -t $owned") {
		t.Fatalf("teardown did not target immutable session id: %q", calls)
	}
}

func TestLaunchAckScriptDoesNotWaitForOutlivingDescendant(t *testing.T) {
	ackPath := filepath.Join(t.TempDir(), "ack")
	latePath := filepath.Join(t.TempDir(), "late")
	command := fmt.Sprintf("(sleep 2; printf LATE > %s) & printf DIRECT; exit 7", shellQuote(latePath))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", launchAckScript, "agent-deck-launch-ack", ackPath, command)
	err := cmd.Run()
	if err == nil {
		t.Fatal("wrapper returned nil for child exit 7")
	}
	if ctx.Err() != nil {
		t.Fatal("wrapper blocked on a descendant-held output descriptor")
	}
	marker, err := os.ReadFile(ackPath)
	if err != nil {
		t.Fatalf("read completion marker: %v", err)
	}
	if !strings.Contains(string(marker), "exit:7\nDIRECT") {
		t.Fatalf("marker lost direct-child diagnostics: %q", marker)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(latePath); !os.IsNotExist(err) {
		t.Fatalf("outliving descendant survived wrapper cleanup, stat error %v", err)
	}
}

func TestAcknowledgeInitialProcessWaitsForSlowCompletionDrain(t *testing.T) {
	ackPath := filepath.Join(t.TempDir(), "ack")
	if err := os.WriteFile(ackPath, []byte("pid:999999\n"), 0o600); err != nil {
		t.Fatalf("write pid marker: %v", err)
	}
	if err := os.WriteFile(ackPath+".output", []byte("initial\n"), 0o600); err != nil {
		t.Fatalf("write initial diagnostic: %v", err)
	}
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(300 * time.Millisecond)
			f, err := os.OpenFile(ackPath+".output", os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(f, "%s-progress-%d\n", strings.Repeat("L", 64*1024), i)
			_ = f.Close()
		}
		_ = os.WriteFile(ackPath, []byte("exit:7\nSLOW_DIAGNOSTIC_COMPLETE\n"), 0o600)
	}()
	sess := &Session{Name: "slow-completion", launchAckPath: ackPath}
	err := sess.AcknowledgeInitialProcess()
	if err == nil || !strings.Contains(err.Error(), "SLOW_DIAGNOSTIC_COMPLETE") {
		t.Fatalf("AcknowledgeInitialProcess = %v, want complete slow diagnostic", err)
	}
}
