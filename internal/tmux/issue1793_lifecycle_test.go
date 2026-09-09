package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIssue1793_WatcherPreservesMarkerAfterPersistentIndeterminateIdentity(t *testing.T) {
	dir := t.TempDir()
	writeFakeTmux(t, dir, "if [ \"$1\" = \"-u\" ]; then shift; fi\n"+
		"if [ \"$1\" = \"-L\" ]; then shift 2; fi\n"+
		"if [ \"$1\" = \"display-message\" ]; then echo 'server busy' >&2; exit 1; fi\n"+"exit 1\n")

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
