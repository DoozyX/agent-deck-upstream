package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

	time.Sleep(3 * time.Second)
	select {
	case <-called:
		t.Fatal("persistent indeterminate identity must not invoke the completion callback")
	default:
	}
	if marker, err := os.ReadFile(ackPath); err != nil || string(marker) == "" {
		t.Fatalf("persistent indeterminate identity must preserve marker evidence: marker=%q err=%v", marker, err)
	}
	calls, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	var probes int
	if _, err := fmt.Sscanf(string(calls), "%d", &probes); err != nil || probes != launchAckIdentityMaxRetries {
		t.Fatalf("persistent indeterminate watcher probes=%d, want bounded %d", probes, launchAckIdentityMaxRetries)
	}
}

func TestIssue1793_WatcherResetsIndeterminateAttemptsAfterOwnedProbe(t *testing.T) {
	dir := t.TempDir()
	countPath := filepath.Join(dir, "probes")
	if err := os.WriteFile(countPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFakeTmux(t, dir, "if [ \"$1\" = \"-u\" ]; then shift; fi\n"+
		"if [ \"$1\" = \"-L\" ]; then shift 2; fi\n"+
		"if [ \"$1\" = \"display-message\" ]; then n=$(cat "+shellQuote(countPath)+"); n=$((n+1)); echo $n > "+shellQuote(countPath)+"; if [ $((n % 2)) -eq 1 ]; then exit 1; fi; echo '$owned'; exit 0; fi\n"+"exit 1\n")
	ackPath := filepath.Join(t.TempDir(), "ack")
	go func() {
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			raw, _ := os.ReadFile(countPath)
			var probes int
			_, _ = fmt.Sscanf(string(raw), "%d", &probes)
			if probes >= 6 {
				_ = os.WriteFile(ackPath, []byte("exit:7\nINTERLEAVED\n"), 0o600)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	sess := &Session{Name: "interleaved-watcher", createdSessionID: "$owned", launchAckPath: ackPath}
	called := make(chan struct{}, 1)
	sess.WatchInitialProcessCompletion(make(chan struct{}), func(int, string) { called <- struct{}{} })
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("interleaved indeterminate probes prevented completion")
	}
	calls, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	var probes int
	if _, err := fmt.Sscanf(string(calls), "%d", &probes); err != nil || probes < launchAckIdentityMaxRetries+1 {
		t.Fatalf("interleaved watcher probes=%d, want attempts reset after ownership", probes)
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
