package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// continuationLookup answers "what did this session hand off to". It is an
// interface so the closure walk below is testable without a database.
type continuationLookup interface {
	ContinuationsOf(sourceID string) ([]string, error)
}

// maxContinuationChainDepth bounds the closure walk. The handoff chain is
// already capped by MaxHandoffChain, so this only exists to make a corrupted
// or cyclic lineage a bounded traversal rather than a hang.
const maxContinuationChainDepth = 64

// liveContinuationClosure returns every un-archived session descended from
// sourceID through the continuation lineage, transitively and in a stable
// order.
//
// Transitively is the whole point. A continuation that reaches its own context
// budget hands off again, so retiring a source can strand a "(cont.) (cont.)"
// two links down — which is exactly what happened, and what made "re-list the
// children and check again, repeatedly" a rule an operator had to remember.
// Walking the closure here means it is answered once, correctly, by the tool.
//
// Archived sessions are skipped: registerContinuation archives the source as
// it hands off, so every link but the last is already retired and re-killing
// it is noise, not safety.
func liveContinuationClosure(db continuationLookup, sourceID string, byID map[string]*session.Instance) []*session.Instance {
	if db == nil || sourceID == "" {
		return nil
	}

	seen := map[string]bool{sourceID: true}
	frontier := []string{sourceID}
	var live []*session.Instance

	for depth := 0; depth < maxContinuationChainDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			ids, err := db.ContinuationsOf(id)
			if err != nil {
				// A lineage we cannot read is reported as no lineage rather
				// than as a blocker: failing the removal on a database read
				// error would make an unrelated fault look like a stranded
				// continuation.
				continue
			}
			for _, childID := range ids {
				if seen[childID] {
					continue
				}
				seen[childID] = true
				next = append(next, childID)
				inst, ok := byID[childID]
				if !ok || inst == nil || !inst.ArchivedAt.IsZero() {
					continue
				}
				live = append(live, inst)
			}
		}
		frontier = next
	}

	sort.Slice(live, func(a, b int) bool { return live[a].ID < live[b].ID })
	return live
}

// instancesByID indexes a session list for the closure walk.
func instancesByID(instances []*session.Instance) map[string]*session.Instance {
	byID := make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		if inst != nil {
			byID[inst.ID] = inst
		}
	}
	return byID
}

// continuationBlockMessage explains a refused removal in terms of the two
// things the operator has to choose between, with the ids they need for
// either. Titles alone are not enough: a chain renders as
// "review-gate-1 (cont.) (cont.)", which is unusable as a command argument
// and ambiguous when two links are alive.
func continuationBlockMessage(title string, live []*session.Instance) string {
	rows := make([]string, 0, len(live))
	for _, inst := range live {
		rows = append(rows, fmt.Sprintf("%s (%s, %s)", inst.ID, inst.Title, inst.Status))
	}
	noun := "continuation"
	if len(live) > 1 {
		noun = "continuations"
	}
	return fmt.Sprintf(
		"session '%s' has %d live %s that removal would leave running: %s — "+
			"pass --cascade to remove them too, or remove them by id first",
		title, len(live), noun, strings.Join(rows, "; "))
}

// continuationRows is the --json payload for the blocked and cascaded cases.
func continuationRows(live []*session.Instance) []map[string]interface{} {
	rows := make([]map[string]interface{}, 0, len(live))
	for _, inst := range live {
		rows = append(rows, map[string]interface{}{
			"id":     inst.ID,
			"title":  inst.Title,
			"status": string(inst.Status),
		})
	}
	return rows
}

// removeContinuationStateDB is the lookup used by the remove command,
// separated so tests can substitute one.
func removeContinuationStateDB(storage *session.Storage) continuationLookup {
	db := sendStateDB(storage)
	if db == nil {
		return nil
	}
	return continuationLookupDB{db}
}

type continuationLookupDB struct{ db *statedb.StateDB }

func (c continuationLookupDB) ContinuationsOf(id string) ([]string, error) {
	return c.db.ContinuationsOf(id)
}
