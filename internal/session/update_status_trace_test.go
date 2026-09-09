package session

import (
	"testing"
	"time"
)

// The status sweep reported codex sessions at ~155ms per UpdateStatus with
// every traced phase at zero — the whole cost sat in the trace's unattributed
// "other" bucket. The cause was the i.mu acquisition itself: UpdateHookStatus
// holds the same lock across the Codex subagent gate. Attributing the wait is
// what makes that visible in a debug log instead of guessable.

func TestUpdateStatusTrace_AttributesTheLockWait(t *testing.T) {
	inst := NewInstanceWithTool("trace-lock", t.TempDir(), "codex")
	trace := newUpdateStatusTrace()

	const held = 40 * time.Millisecond
	inst.mu.Lock()
	released := make(chan struct{})
	go func() {
		time.Sleep(held)
		inst.mu.Unlock()
		close(released)
	}()

	inst.traceLock(trace)
	inst.mu.Unlock()
	<-released

	if trace.lockWait < held/2 {
		t.Fatalf("lockWait = %v, want at least %v", trace.lockWait, held/2)
	}
}

func TestUpdateStatusTrace_LockWaitLeavesOtherEmpty(t *testing.T) {
	trace := newUpdateStatusTrace()
	trace.lockWait = 150 * time.Millisecond

	total := 151 * time.Millisecond
	other := total - trace.lockWait - trace.exists - trace.terminated - trace.bgWork -
		trace.getStatus - trace.gateway - trace.hookFile - trace.persist
	if other > time.Millisecond {
		t.Fatalf("other = %v, want the lock wait excluded from it", other)
	}
}
