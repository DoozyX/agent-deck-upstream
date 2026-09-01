package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/usage"
	"github.com/charmbracelet/lipgloss"
	"strings"
	"testing"
	"time"
)

func TestRenderUsageBarWrapsEveryAccount(t *testing.T) {
	s := []usage.Snapshot{{Available: true, Provider: usage.Claude, Account: "Personal", Windows: usage.Windows{Session5H: &usage.Window{RemainingPercent: 82}, Weekly: &usage.Window{RemainingPercent: 61}}}, {Available: true, Provider: usage.Codex, Account: "Pro", Windows: usage.Windows{Weekly: &usage.Window{RemainingPercent: 98}}}}
	got := renderUsageBar(s, 32)
	if !strings.Contains(got, "Claude Personal 5h 82% W61%") || !strings.Contains(got, "Codex Pro W98%") || !strings.Contains(got, "\n") {
		t.Fatalf("bar=%q", got)
	}
	t.Logf("rendered usage bar:\n%s", got)
}

func TestRenderUsageBarHardWrapsSingleLongSegment(t *testing.T) {
	bar := renderUsageBar([]usage.Snapshot{{Available: true, Provider: usage.Claude, Account: "an-account-label-that-cannot-fit", Windows: usage.Windows{Weekly: &usage.Window{RemainingPercent: 1}}}}, 12)
	for _, line := range strings.Split(bar, "\n") {
		if lipgloss.Width(line) > 12 {
			t.Fatalf("line width = %d, want <= 12: %q", lipgloss.Width(line), line)
		}
	}
}

func TestRenderUsageBarMarksStaleSnapshots(t *testing.T) {
	bar := renderUsageBar([]usage.Snapshot{{Available: true, Stale: true, Provider: usage.Claude, Account: "work", Windows: usage.Windows{Weekly: &usage.Window{RemainingPercent: 55}}}}, 80)
	if !strings.Contains(bar, "~Claude work W55%") {
		t.Fatalf("stale marker missing from %q", bar)
	}
}

func TestUsageFetchedRetainsPriorSnapshotAsStaleAfterFailure(t *testing.T) {
	h := &Home{usageSnapshots: []usage.Snapshot{{Available: true, Provider: usage.Claude, Home: "/work", Account: "work", Windows: usage.Windows{Weekly: &usage.Window{RemainingPercent: 55}}}}}
	model, _ := h.updateInner(usageFetchedMsg{accounts: []usage.Account{{Provider: usage.Claude, Home: "/work", Label: "work"}}})
	got := model.(*Home).usageSnapshots
	if len(got) != 1 || !got[0].Stale || got[0].Windows.Weekly == nil || got[0].Windows.Weekly.RemainingPercent != 55 {
		t.Fatalf("snapshots = %#v, want prior snapshot retained and stale", got)
	}
}

func TestUsageFetchedRetainsPriorSnapshotAfterDiscoveryFailure(t *testing.T) {
	h := &Home{usageSnapshots: []usage.Snapshot{{Available: true, Provider: usage.Claude, Home: "/work", Account: "work", Windows: usage.Windows{Weekly: &usage.Window{RemainingPercent: 55}}}}}
	model, _ := h.updateInner(usageFetchedMsg{})
	got := model.(*Home).usageSnapshots
	if len(got) != 1 || !got[0].Stale || got[0].Windows.Weekly == nil || got[0].Windows.Weekly.RemainingPercent != 55 {
		t.Fatalf("snapshots = %#v, want prior snapshot retained and stale after discovery failure", got)
	}
}

func TestUsageFetchedDropsSnapshotsForUndiscoveredAccounts(t *testing.T) {
	h := &Home{usageSnapshots: []usage.Snapshot{
		{Available: true, Provider: usage.Claude, Home: "/gone", Account: "gone"},
		{Available: true, Provider: usage.Codex, Home: "/active", Account: "active"},
	}}
	model, _ := h.updateInner(usageFetchedMsg{accounts: []usage.Account{{Provider: usage.Codex, Home: "/active", Label: "active"}}})
	got := model.(*Home).usageSnapshots
	if len(got) != 1 || got[0].Home != "/active" || !got[0].Stale {
		t.Fatalf("snapshots = %#v, want only stale active account", got)
	}
}

func TestUsageBarHeightIsIncludedInVisibleHeight(t *testing.T) {
	h := &Home{width: 25, height: 20, usageSnapshots: []usage.Snapshot{{Available: true, Provider: usage.Claude, Account: "Personal", Windows: usage.Windows{Weekly: &usage.Window{RemainingPercent: 61}}}, {Available: true, Provider: usage.Codex, Account: "Pro", Windows: usage.Windows{Weekly: &usage.Window{RemainingPercent: 98}}}}}
	barHeight := lipgloss.Height(renderUsageBar(h.usageSnapshots, h.width))
	want := h.height - 1 - 2 - 1 - 2 - 1 - barHeight
	if got := h.getVisibleHeight(); got != want {
		t.Fatalf("visible height = %d, want %d with %d-row usage bar", got, want, barHeight)
	}
}

func TestUsageBarHeightContractIncludesStackedPreviewAndViewport(t *testing.T) {
	h := &Home{width: 60, height: 20, flatItems: make([]session.Item, 20), cursor: 12, usageSnapshots: []usage.Snapshot{{Available: true, Provider: usage.Claude, Account: "work", Windows: usage.Windows{Weekly: &usage.Window{RemainingPercent: 55}}}}}
	contentHeight := h.mainContentHeight()
	if got, want := h.stackedPreviewTopY(), h.contentChromeTop()+h.stackedListHeight(contentHeight)+1; got != want {
		t.Fatalf("stacked preview top = %d, want %d", got, want)
	}
	h.syncViewport()
	if h.cursor < h.viewOffset || h.cursor >= h.viewOffset+h.getVisibleHeight() {
		t.Fatalf("cursor %d not visible in viewport offset=%d height=%d", h.cursor, h.viewOffset, h.getVisibleHeight())
	}
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
