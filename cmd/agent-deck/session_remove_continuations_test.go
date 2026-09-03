package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeContinuationLookup is an in-memory lineage table.
type fakeContinuationLookup struct {
	edges map[string][]string
	fail  map[string]bool
}

func (f fakeContinuationLookup) ContinuationsOf(id string) ([]string, error) {
	if f.fail[id] {
		return nil, errors.New("lineage read failed")
	}
	return f.edges[id], nil
}

func inst(id, title string, archived bool) *session.Instance {
	i := &session.Instance{ID: id, Title: title, Status: session.StatusRunning}
	if archived {
		i.ArchivedAt = time.Now().UTC()
	}
	return i
}

// A "(cont.)" inherits its SOURCE's parent, so it is a sibling of the session
// it replaced and no parent/title check finds it at retirement time. Removing
// the source used to leave it running — twice in one run, once a reviewer
// holding write authority in a worktree a live implementer was committing
// from. The closure is what `session remove` consults instead.
func TestLiveContinuationClosureFindsTheWholeChain(t *testing.T) {
	// source -> cont1 -> cont2 ; cont1 is archived (it handed off in turn),
	// so only cont2 is still live and only cont2 blocks the removal.
	db := fakeContinuationLookup{edges: map[string][]string{
		"source": {"cont1"},
		"cont1":  {"cont2"},
	}}
	byID := instancesByID([]*session.Instance{
		inst("source", "review-gate-1", false),
		inst("cont1", "review-gate-1 (cont.)", true),
		inst("cont2", "review-gate-1 (cont.) (cont.)", false),
	})

	live := liveContinuationClosure(db, "source", byID)
	if len(live) != 1 {
		t.Fatalf("live = %d sessions, want 1 (the transitive continuation): %+v", len(live), live)
	}
	if live[0].ID != "cont2" {
		t.Errorf("live[0].ID = %q, want cont2 — a continuation can itself be continued", live[0].ID)
	}
}

func TestLiveContinuationClosureIgnoresArchivedAndUnknownRows(t *testing.T) {
	db := fakeContinuationLookup{edges: map[string][]string{
		"source": {"archived", "vanished"},
	}}
	byID := instancesByID([]*session.Instance{
		inst("source", "impl", false),
		inst("archived", "impl (cont.)", true),
	})

	if live := liveContinuationClosure(db, "source", byID); len(live) != 0 {
		t.Errorf("live = %+v, want none: an archived link and a row that no longer exists strand nothing", live)
	}
}

// A cyclic or corrupted lineage must be a bounded walk, not a hang.
func TestLiveContinuationClosureTerminatesOnACycle(t *testing.T) {
	db := fakeContinuationLookup{edges: map[string][]string{
		"a": {"b"},
		"b": {"a"},
	}}
	byID := instancesByID([]*session.Instance{inst("a", "a", false), inst("b", "b", false)})

	done := make(chan int, 1)
	go func() { done <- len(liveContinuationClosure(db, "a", byID)) }()
	select {
	case n := <-done:
		if n != 1 {
			t.Errorf("live = %d, want 1 (b, visited once)", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closure walk did not terminate on a cyclic lineage")
	}
}

// A database read failure must not masquerade as a stranded continuation:
// blocking removal on an unrelated fault is its own trap.
func TestLiveContinuationClosureTreatsAReadFailureAsNoLineage(t *testing.T) {
	db := fakeContinuationLookup{
		edges: map[string][]string{"source": {"cont1"}},
		fail:  map[string]bool{"source": true},
	}
	byID := instancesByID([]*session.Instance{inst("source", "s", false), inst("cont1", "s (cont.)", false)})

	if live := liveContinuationClosure(db, "source", byID); len(live) != 0 {
		t.Errorf("live = %+v, want none when the lineage cannot be read", live)
	}
}

func TestContinuationBlockMessageNamesIdsAndTheRemedy(t *testing.T) {
	msg := continuationBlockMessage("review-gate-1", []*session.Instance{
		inst("abc123", "review-gate-1 (cont.)", false),
	})
	for _, want := range []string{"abc123", "review-gate-1 (cont.)", "--cascade"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

func TestNilLookupStrandsNothing(t *testing.T) {
	if live := liveContinuationClosure(nil, "source", nil); live != nil {
		t.Errorf("live = %+v, want nil with no state DB available", live)
	}
}
