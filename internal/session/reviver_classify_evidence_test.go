package session

import (
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// Issue #1705: a live conductor was restarted as if it were dead, and the
// investigation could not get past "the restart fired" because the READINGS
// behind that verdict were never written down. Classify now states its evidence
// for every non-alive verdict.

// classifyAttrs returns the attributes of the last reviver_classify record.
func classifyAttrs(t *testing.T, h *recordingHandler) map[string]string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.recs) - 1; i >= 0; i-- {
		if h.recs[i].Message != "reviver_classify" {
			continue
		}
		attrs := map[string]string{}
		h.recs[i].Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		attrs["__level"] = h.recs[i].Level.String()
		return attrs
	}
	t.Fatalf("no reviver_classify record emitted")
	return nil
}

func TestReviver_Classify_LogsEvidenceForErroredSession(t *testing.T) {
	inst := newReviverTestInstance("evidence-errored", StatusError)
	h := &recordingHandler{}
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { return false },
		ReviveAction: func(*Instance) error { return nil },
		Log:          slog.New(h),
	}

	if got := r.Classify(inst); got != ClassErrored {
		t.Fatalf("expected ClassErrored, got %v", got)
	}

	attrs := classifyAttrs(t, h)
	for key, want := range map[string]string{
		"tmux_alive":    "true",
		"pipe_alive":    "false",
		"stored_status": string(StatusError),
		"class":         "errored",
		"title":         "evidence-errored",
	} {
		if attrs[key] != want {
			t.Errorf("attr %q = %q, want %q", key, attrs[key], want)
		}
	}
	if attrs["sampled_at"] == "" {
		t.Error("evidence must be timestamped (sampled_at missing)")
	}
	if attrs["__level"] != slog.LevelInfo.String() {
		t.Errorf("a non-alive verdict must be retrievable without debug logging; level=%s", attrs["__level"])
	}
}

// The pipe reading must be recorded even when the stored status alone already
// settles the verdict — it is the reading that says whether the session was
// reachable, which is the whole question #1705 could not answer afterwards.
func TestReviver_Classify_RecordsPipeReadingEvenWhenStatusDecides(t *testing.T) {
	inst := newReviverTestInstance("evidence-live-pipe", StatusError)
	h := &recordingHandler{}
	probed := 0
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { probed++; return true },
		ReviveAction: func(*Instance) error { return nil },
		Log:          slog.New(h),
	}

	if got := r.Classify(inst); got != ClassErrored {
		t.Fatalf("a StatusError session on a live server stays ClassErrored, got %v", got)
	}
	if probed != 1 {
		t.Fatalf("expected the pipe to be probed once for the record, got %d probes", probed)
	}
	if attrs := classifyAttrs(t, h); attrs["pipe_alive"] != "true" {
		t.Errorf("pipe_alive = %q, want true (a live pipe under a StatusError reading is exactly the false-positive signal)", attrs["pipe_alive"])
	}
}

func TestReviver_Classify_DeadServerEvidence(t *testing.T) {
	inst := newReviverTestInstance("evidence-dead", StatusRunning)
	h := &recordingHandler{}
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return false },
		PipeAlive:    func(string) bool { t.Fatal("pipe must not be probed when the server is gone"); return false },
		ReviveAction: func(*Instance) error { return nil },
		Log:          slog.New(h),
	}

	if got := r.Classify(inst); got != ClassDead {
		t.Fatalf("expected ClassDead, got %v", got)
	}
	attrs := classifyAttrs(t, h)
	if attrs["tmux_alive"] != "false" || attrs["class"] != "dead" {
		t.Errorf("dead-server evidence wrong: tmux_alive=%q class=%q", attrs["tmux_alive"], attrs["class"])
	}
	if attrs["__level"] != slog.LevelDebug.String() {
		t.Errorf("dead-server evidence level = %s, want DEBUG", attrs["__level"])
	}
}

func TestReviver_Classify_CanSuppressDeadEvidenceForFleetSweeps(t *testing.T) {
	inst := newReviverTestInstance("evidence-dead-fleet", StatusRunning)
	h := &recordingHandler{}
	r := &Reviver{
		TmuxExists:                  func(string, string) bool { return false },
		Log:                         slog.New(h),
		suppressDeadClassifications: true,
	}

	if got := r.Classify(inst); got != ClassDead {
		t.Fatalf("expected ClassDead, got %v", got)
	}
	if got := h.count("reviver_classify"); got != 0 {
		t.Fatalf("fleet sweep emitted %d dead classification records, want 0", got)
	}
}

// An alive session is the overwhelming majority of every sweep: its verdict stays
// at debug level so the fleet does not drown the useful records.
func TestReviver_Classify_AliveVerdictStaysDebug(t *testing.T) {
	inst := newReviverTestInstance("evidence-alive", StatusRunning)
	h := &recordingHandler{}
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { return true },
		ReviveAction: func(*Instance) error { return nil },
		Log:          slog.New(h),
	}

	if got := r.Classify(inst); got != ClassAlive {
		t.Fatalf("expected ClassAlive, got %v", got)
	}
	if attrs := classifyAttrs(t, h); attrs["__level"] != slog.LevelDebug.String() {
		t.Errorf("alive verdict level = %s, want DEBUG", attrs["__level"])
	}
}

func TestReviver_Classify_RateLimitsUnchangedNonAliveEvidence(t *testing.T) {
	inst := newReviverTestInstance("evidence-rate-limit", StatusError)
	h := &recordingHandler{}
	newReviver := func() *Reviver {
		return &Reviver{
			TmuxExists: func(string, string) bool { return true },
			PipeAlive:  func(string) bool { return false },
			Log:        slog.New(h),
		}
	}

	if got := newReviver().Classify(inst); got != ClassErrored {
		t.Fatalf("first classification = %v, want errored", got)
	}
	if got := newReviver().Classify(inst); got != ClassErrored {
		t.Fatalf("repeated classification = %v, want errored", got)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	infoCount := 0
	for _, rec := range h.recs {
		if rec.Message == "reviver_classify" && rec.Level == slog.LevelInfo {
			infoCount++
		}
	}
	if infoCount != 1 {
		t.Fatalf("unchanged non-alive evidence emitted at INFO %d times, want 1", infoCount)
	}
}

func TestReviverClassificationLogPrunesExpiredSessionChurn(t *testing.T) {
	reviverClassificationLog.Lock()
	originalStates := reviverClassificationLog.states
	originalLastPruned := reviverClassificationLog.lastPruned
	reviverClassificationLog.states = make(map[string]reviverClassificationLogState)
	reviverClassificationLog.lastPruned = time.Time{}
	reviverClassificationLog.Unlock()
	t.Cleanup(func() {
		reviverClassificationLog.Lock()
		reviverClassificationLog.states = originalStates
		reviverClassificationLog.lastPruned = originalLastPruned
		reviverClassificationLog.Unlock()
	})

	started := time.Unix(1_000_000, 0)
	for i := 0; i < 1_000; i++ {
		inst := newReviverTestInstance(fmt.Sprintf("expired-%d", i), StatusError)
		shouldLogReviverInfo(inst, inst.Title, true, false, ClassErrored, started)
	}
	fresh := newReviverTestInstance("fresh", StatusError)
	shouldLogReviverInfo(fresh, fresh.Title, true, false, ClassErrored, started.Add(reviverClassificationLogStateTTL+time.Second))

	reviverClassificationLog.Lock()
	defer reviverClassificationLog.Unlock()
	if got := len(reviverClassificationLog.states); got != 1 {
		t.Fatalf("classification state count after churn expiry = %d, want 1", got)
	}
	if _, ok := reviverClassificationLog.states[fresh.ID]; !ok {
		t.Fatal("fresh classification state was pruned")
	}
}

// A Reviver built by hand without a PipeAlive func must not panic: Classify used
// to call it unconditionally on the non-error path only, and the evidence read
// widened when that call site moved.
func TestReviver_Classify_NilPipeAliveDoesNotPanic(t *testing.T) {
	inst := newReviverTestInstance("evidence-nilpipe", StatusRunning)
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		ReviveAction: func(*Instance) error { return nil },
	}
	if got := r.Classify(inst); got != ClassErrored {
		t.Fatalf("no pipe reading available → not provably alive; got %v", got)
	}
}
