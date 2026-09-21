package scripts

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// Startup-context budgets are the only thing standing between a slow accretion
// of plugins, MCP servers and memories and a session that spends a quarter of
// its window before reading an instruction. These tests keep the budget file
// honest: every profile the audit can probe must have a budget, and no budget
// may be quietly raised past the ceiling to hide a regression.

// budgetCeilings is the maximum a budget may be set to. Raising a budget past
// its ceiling is a deliberate change to this table, reviewed on its own, not a
// number edited in passing to make a red audit go green.
var budgetCeilings = map[string]int{
	"print":        22_000,
	"codex":        15_000,
	"interactive":  30_000,
	"child":        25_000,
	"memory_index": 3_000,
}

func loadContextBudgets(t *testing.T) map[string]int {
	t.Helper()
	raw, err := os.ReadFile("context-budgets.json")
	if err != nil {
		t.Fatalf("read context-budgets.json: %v", err)
	}
	var budgets map[string]int
	if err := json.Unmarshal(raw, &budgets); err != nil {
		t.Fatalf("parse context-budgets.json: %v", err)
	}
	return budgets
}

func TestContextBudgetsStayUnderDeclaredCeilings(t *testing.T) {
	budgets := loadContextBudgets(t)

	for name, ceiling := range budgetCeilings {
		got, ok := budgets[name]
		if !ok {
			t.Errorf("context-budgets.json is missing a budget for %q", name)
			continue
		}
		if got > ceiling {
			t.Errorf("budget %q is %d, above its ceiling of %d; raise the "+
				"ceiling in budgetCeilings deliberately, or bring the "+
				"profile back under budget", name, got, ceiling)
		}
		if got <= 0 {
			t.Errorf("budget %q is %d; a non-positive budget can never pass",
				name, got)
		}
	}

	for name := range budgets {
		if _, ok := budgetCeilings[name]; !ok {
			t.Errorf("context-budgets.json has budget %q with no declared "+
				"ceiling; add one to budgetCeilings", name)
		}
	}
}

// probesPattern pulls the profile names out of the PROBES table in
// context-audit.py. A profile the audit can measure but has no budget for is a
// silent gap: the audit would report it and never fail on it.
var probesPattern = regexp.MustCompile(`(?s)PROBES: dict\[str, list\[str\]\] = \{(.*?)\n\}`)
var probeKeyPattern = regexp.MustCompile(`"([a-z-]+)":\s*\[`)

func TestEveryProbeProfileHasABudget(t *testing.T) {
	src, err := os.ReadFile("context-audit.py")
	if err != nil {
		t.Fatalf("read context-audit.py: %v", err)
	}
	block := probesPattern.FindSubmatch(src)
	if block == nil {
		t.Fatal("could not find the PROBES table in context-audit.py; if it " +
			"was renamed, update probesPattern so this coupling stays checked")
	}
	matches := probeKeyPattern.FindAllSubmatch(block[1], -1)
	if len(matches) == 0 {
		t.Fatal("PROBES table parsed but contained no profiles")
	}

	budgets := loadContextBudgets(t)
	for _, m := range matches {
		name := string(m[1])
		if _, ok := budgets[name]; !ok {
			t.Errorf("context-audit.py can probe profile %q but "+
				"context-budgets.json has no budget for it", name)
		}
	}
}
