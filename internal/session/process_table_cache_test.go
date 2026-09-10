package session

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcessTableSnapshotCacheCoalescesConcurrentReads(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	collect := func() ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []byte("101 1\n102 101\n"), nil
	}
	oldCache := codexProcessTableCache
	oldCollector := codexProcessTableCollector
	codexProcessTableCache = newProcessTableSnapshotCache(time.Second)
	codexProcessTableCollector = collect
	t.Cleanup(func() {
		codexProcessTableCache = oldCache
		codexProcessTableCollector = oldCollector
	})

	const readers = 8
	results := make(chan []byte, readers)
	var workers sync.WaitGroup
	for range readers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			got, err := loadCodexProcessTable()
			if err != nil {
				t.Errorf("cache.load: %v", err)
				return
			}
			results <- got
		}()
	}

	<-started
	close(release)
	workers.Wait()
	close(results)

	if got := calls.Load(); got != 1 {
		t.Fatalf("process-table collector calls = %d, want 1", got)
	}
	for got := range results {
		if string(got) != "101 1\n102 101\n" {
			t.Errorf("cache result = %q", got)
		}
	}
}

func TestParsePSProcessArgs(t *testing.T) {
	procTable := []byte(" 100 1 /bin/bash -lc codex\n 200 100 /opt/codex/codex --foo\n 300 100 /bin/zsh\n")

	got, err := parsePSProcessArgs(procTable)
	if err != nil {
		t.Fatalf("parsePSProcessArgs() error = %v", err)
	}

	want := map[int]string{
		100: "/bin/bash -lc codex",
		200: "/opt/codex/codex --foo",
		300: "/bin/zsh",
	}
	if len(got) != len(want) {
		t.Fatalf("parsePSProcessArgs() returned %d processes, want %d: %#v", len(got), len(want), got)
	}
	for pid, wantArgs := range want {
		if gotArgs := got[pid]; gotArgs != wantArgs {
			t.Errorf("parsePSProcessArgs()[%d] = %q, want %q", pid, gotArgs, wantArgs)
		}
	}
}
