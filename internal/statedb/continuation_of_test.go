package statedb

import (
	"path/filepath"
	"testing"
	"time"
)

func newContinuationTestDB(t *testing.T, ids ...string) *StateDB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rows := make([]*InstanceRow, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, &InstanceRow{
			ID: id, Title: id, ProjectPath: "/tmp/p", GroupPath: "g",
			Tool: "claude", Status: "running", CreatedAt: time.Unix(1000, 0),
		})
	}
	if err := db.SaveInstances(rows); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	return db
}

// The continuation fork inherits the source's PARENT, so nothing else on disk
// records which session a "(cont.)" came from. Retiring a source without this
// key leaves the continuation running loose.
func TestContinuationOf_RoundTrip(t *testing.T) {
	db := newContinuationTestDB(t, "source", "cont")

	got, err := db.ReadContinuationOf("cont")
	if err != nil {
		t.Fatalf("ReadContinuationOf(unset): %v", err)
	}
	if got != "" {
		t.Errorf("unset continuation_of = %q, want empty", got)
	}

	if err := db.WriteContinuationOf("cont", "source"); err != nil {
		t.Fatalf("WriteContinuationOf: %v", err)
	}
	got, err = db.ReadContinuationOf("cont")
	if err != nil {
		t.Fatalf("ReadContinuationOf: %v", err)
	}
	if got != "source" {
		t.Errorf("continuation_of = %q, want source", got)
	}
}

// The write shares tool_data with the handoff generation, so it must be a
// targeted json_set that cannot clobber its neighbour.
func TestContinuationOf_DoesNotClobberHandoffGeneration(t *testing.T) {
	db := newContinuationTestDB(t, "cont")

	if err := db.WriteHandoffGeneration("cont", 3); err != nil {
		t.Fatalf("WriteHandoffGeneration: %v", err)
	}
	if err := db.WriteContinuationOf("cont", "source"); err != nil {
		t.Fatalf("WriteContinuationOf: %v", err)
	}

	gen, err := db.ReadHandoffGeneration("cont")
	if err != nil {
		t.Fatalf("ReadHandoffGeneration: %v", err)
	}
	if gen != 3 {
		t.Errorf("handoff generation = %d after writing continuation_of, want 3", gen)
	}
}

func TestContinuationsOf_ListsChildren(t *testing.T) {
	db := newContinuationTestDB(t, "source", "cont1", "cont2", "unrelated")

	if err := db.WriteContinuationOf("cont1", "source"); err != nil {
		t.Fatalf("WriteContinuationOf(cont1): %v", err)
	}
	// A continuation can itself be continued: cont2 hangs off cont1, not
	// off source, and must not appear in source's direct children.
	if err := db.WriteContinuationOf("cont2", "cont1"); err != nil {
		t.Fatalf("WriteContinuationOf(cont2): %v", err)
	}

	ids, err := db.ContinuationsOf("source")
	if err != nil {
		t.Fatalf("ContinuationsOf: %v", err)
	}
	if len(ids) != 1 || ids[0] != "cont1" {
		t.Errorf("ContinuationsOf(source) = %v, want [cont1]", ids)
	}

	ids, err = db.ContinuationsOf("cont1")
	if err != nil {
		t.Fatalf("ContinuationsOf(cont1): %v", err)
	}
	if len(ids) != 1 || ids[0] != "cont2" {
		t.Errorf("ContinuationsOf(cont1) = %v, want [cont2]", ids)
	}

	if ids, err := db.ContinuationsOf("unrelated"); err != nil || len(ids) != 0 {
		t.Errorf("ContinuationsOf(unrelated) = %v, %v; want no rows", ids, err)
	}
	if ids, err := db.ContinuationsOf(""); err != nil || ids != nil {
		t.Errorf("ContinuationsOf(\"\") = %v, %v; want nil, nil", ids, err)
	}
}
