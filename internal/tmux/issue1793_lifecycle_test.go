package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitForProbeCount(path string, want int, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		calls, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		var probes int
		if _, err := fmt.Sscanf(string(calls), "%d", &probes); err != nil {
			return 0, err
		}
		if probes >= want || !time.Now().Before(deadline) {
			return probes, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIssue1793_WatcherPreservesMarkerAfterPersistentIndeterminateIdentity(t *testing.T) {
	dir := t.TempDir()
	countPath := filepath.Join(dir, "probes")
	if err := os.WriteFile(countPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFakeTmux(t, dir, "if [ \"$1\" = \"-u\" ]; then shift; fi\n"+
		"if [ \"$1\" = \"-L\" ]; then shift 2; fi\n"+
		"if [ \"$1\" = \"display-message\" ]; then n=$(cat "+shellQuote(countPath)+"); n=$((n+1)); echo $n > "+shellQuote(countPath)+"; echo 'server busy' >&2; exit 1; fi\n"+"exit 1\n")

	ackPath := filepath.Join(t.TempDir(), "ack")
	if err := os.WriteFile(ackPath, []byte("exit:7\nPERSIST THIS EVIDENCE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := &Session{Name: "indeterminate-watcher", createdSessionID: "$owned", launchAckPath: ackPath}
	called := make(chan struct{}, 1)
	sess.WatchInitialProcessCompletion(make(chan struct{}), func(int, string) { called <- struct{}{} })

	// The watcher backs off between probes and starts an external tmux process
	// for each one. Poll for its observable terminal count instead of sampling
	// it at a fixed wall-clock instant under a loaded test run.
	probes, err := waitForProbeCount(countPath, launchAckIdentityMaxRetries, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
		t.Fatal("persistent indeterminate identity must not invoke the completion callback")
	default:
	}
	if marker, err := os.ReadFile(ackPath); err != nil || string(marker) == "" {
		t.Fatalf("persistent indeterminate identity must preserve marker evidence: marker=%q err=%v", marker, err)
	}
	if probes != launchAckIdentityMaxRetries {
		t.Fatalf("persistent indeterminate watcher probes=%d, want bounded %d", probes, launchAckIdentityMaxRetries)
	}
}

func TestIssue1793_WatcherResetsIndeterminateAttemptsAfterOwnedProbe(t *testing.T) {
	dir := t.TempDir()
	countPath := filepath.Join(dir, "probes")
	if err := os.WriteFile(countPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	ackPath := filepath.Join(t.TempDir(), "ack")
	writeFakeTmux(t, dir, "if [ \"$1\" = \"-u\" ]; then shift; fi\n"+
		"if [ \"$1\" = \"-L\" ]; then shift 2; fi\n"+
		"if [ \"$1\" = \"display-message\" ]; then n=$(cat "+shellQuote(countPath)+"); n=$((n+1)); echo $n > "+shellQuote(countPath)+"; if [ $((n % 2)) -eq 1 ]; then exit 1; fi; if [ \"$n\" -ge 10 ]; then printf 'exit:7\\nINTERLEAVED\\n' > "+shellQuote(ackPath)+"; fi; echo '$owned'; exit 0; fi\n"+"exit 1\n")
	sess := &Session{Name: "interleaved-watcher", createdSessionID: "$owned", launchAckPath: ackPath}
	called := make(chan string, 1)
	sess.WatchInitialProcessCompletion(make(chan struct{}), func(exitCode int, diagnostic string) {
		called <- fmt.Sprintf("%d:%s", exitCode, diagnostic)
	})
	select {
	case got := <-called:
		if got != "7:INTERLEAVED" {
			t.Fatalf("completion = %q, want exit and diagnostic from owned probe", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interleaved indeterminate probes prevented completion")
	}
	calls, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	var probes int
	if _, err := fmt.Sscanf(string(calls), "%d", &probes); err != nil || probes != 2*launchAckIdentityMaxRetries {
		t.Fatalf("interleaved watcher probes=%d, want %d proving attempts reset after ownership", probes, 2*launchAckIdentityMaxRetries)
	}
}

func TestIssue1793_CacheRemovalDoesNotDeleteSameNameReplacement(t *testing.T) {
	sessionCacheMu.Lock()
	previousData, previousGenerations, previousTime := sessionCacheData, sessionCacheGenerations, sessionCacheTime
	sessionCacheData = nil
	sessionCacheGenerations = nil
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()
	t.Cleanup(func() {
		sessionCacheMu.Lock()
		sessionCacheData, sessionCacheGenerations, sessionCacheTime = previousData, previousGenerations, previousTime
		sessionCacheMu.Unlock()
	})

	oldGeneration := registerSessionInCache("same-name")
	registerSessionInCache("same-name")
	removeSessionFromCacheIfGeneration("same-name", oldGeneration)
	if _, ok := sessionCacheData["same-name"]; !ok {
		t.Fatal("cache cleanup for the old generation deleted the same-name replacement")
	}
}

func TestIssue1793_CacheRefreshPreservesOwnerGenerationUntilRemoval(t *testing.T) {
	sessionCacheMu.Lock()
	previousData, previousGenerations, previousTime := sessionCacheData, sessionCacheGenerations, sessionCacheTime
	sessionCacheData = nil
	sessionCacheGenerations = nil
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()
	t.Cleanup(func() {
		sessionCacheMu.Lock()
		sessionCacheData, sessionCacheGenerations, sessionCacheTime = previousData, previousGenerations, previousTime
		sessionCacheMu.Unlock()
	})

	owner := registerSessionInCache("refresh-owner")
	sessionCacheMu.Lock()
	sessionCacheData = map[string]int64{"refresh-owner": 2}
	sessionCacheGenerations = newSessionCacheGenerations(sessionCacheData)
	sessionCacheMu.Unlock()
	removeSessionFromCacheIfGeneration("refresh-owner", owner)
	if _, ok := sessionCacheData["refresh-owner"]; ok {
		t.Fatal("owner removal after refresh left the unchanged session cached")
	}
}

func TestIssue1793_LaunchModeSelectionUsesWrapperAndPTY(t *testing.T) {
	cases := []struct {
		name    string
		command string
		mode    string
	}{
		{name: "interactive", command: "codex", mode: "interactive"},
		{name: "bounded", command: "codex --model gpt-5 exec --json work", mode: "isolated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := &Session{Name: "launch-mode", WorkDir: t.TempDir(), RunCommandAsInitialProcess: true, AllowInitialProcessExit: tc.mode == "isolated", launchAckPath: filepath.Join(t.TempDir(), "ack")}
			_, args := sess.startCommandSpec(sess.WorkDir, tc.command)
			if len(args) < 3 || args[len(args)-2] != tc.mode || args[len(args)-1] != tc.command {
				t.Fatalf("launch args %q do not select %s mode", args, tc.mode)
			}
		})
	}
}
