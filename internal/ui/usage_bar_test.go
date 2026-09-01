package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/usage"
	"strings"
	"testing"
)

func TestRenderUsageBarWrapsEveryAccount(t *testing.T) {
	s := []usage.Snapshot{{Available: true, Provider: usage.Claude, Account: "Personal", Windows: usage.Windows{Session5H: &usage.Window{RemainingPercent: 82}, Weekly: &usage.Window{RemainingPercent: 61}}}, {Available: true, Provider: usage.Codex, Account: "Pro", Windows: usage.Windows{Weekly: &usage.Window{RemainingPercent: 98}}}}
	got := renderUsageBar(s, 25)
	if !strings.Contains(got, "Claude Personal 5h 82% W61%") || !strings.Contains(got, "Codex Pro W98%") || !strings.Contains(got, "\n") {
		t.Fatalf("bar=%q", got)
	}
}
