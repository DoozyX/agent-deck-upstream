package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/usage"
	"strings"
	"testing"
	"time"
)

func TestRenderUsageBarWrapsEveryAccount(t *testing.T) {
	s := []usage.Snapshot{{Available: true, Provider: usage.Claude, Account: "Personal", Windows: usage.Windows{Session5H: &usage.Window{RemainingPercent: 82}, Weekly: &usage.Window{RemainingPercent: 61}}}, {Available: true, Provider: usage.Codex, Account: "Pro", Windows: usage.Windows{Weekly: &usage.Window{RemainingPercent: 98}}}}
	got := renderUsageBar(s, 25)
	if !strings.Contains(got, "Claude Personal 5h 82% W61%") || !strings.Contains(got, "Codex Pro W98%") || !strings.Contains(got, "\n") {
		t.Fatalf("bar=%q", got)
	}
	t.Logf("rendered usage bar:\n%s", got)
}

func TestUsageRefreshIsDueOnlyAfterThirtySecondsAndNeverWhileInFlight(t *testing.T) {
	now := time.Now()
	if usageRefreshDue(now.Add(-29*time.Second), false, now) {
		t.Fatal("refresh before 30 seconds")
	}
	if !usageRefreshDue(now.Add(-30*time.Second), false, now) {
		t.Fatal("refresh at 30 seconds")
	}
	if usageRefreshDue(now.Add(-time.Minute), true, now) {
		t.Fatal("duplicate in-flight refresh")
	}
}
