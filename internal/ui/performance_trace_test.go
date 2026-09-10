package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	tea "github.com/charmbracelet/bubbletea"
)

func TestUIInputLabelIdentifiesScrollAndNewSession(t *testing.T) {
	tests := []struct {
		name   string
		msg    tea.Msg
		kind   string
		action string
	}{
		{name: "keyboard down", msg: tea.KeyMsg{Type: tea.KeyDown}, kind: "scroll", action: "down"},
		{name: "vim down", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, kind: "scroll", action: "down"},
		{name: "vim up", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, kind: "scroll", action: "up"},
		{name: "wheel up", msg: tea.MouseMsg{Button: tea.MouseButtonWheelUp}, kind: "scroll", action: "up"},
		{name: "new session", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}, kind: "key", action: "new_session"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, action := uiInputLabel(tt.msg)
			if kind != tt.kind || action != tt.action {
				t.Fatalf("uiInputLabel() = (%q, %q), want (%q, %q)", kind, action, tt.kind, tt.action)
			}
		})
	}
}

func TestRawKeypressTraceSkipsNavigationKeys(t *testing.T) {
	for _, key := range []string{"up", "down", "k", "j", "ctrl+p", "ctrl+n"} {
		if isNavigationKey(key) == false {
			t.Errorf("isNavigationKey(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"n", "enter", "ctrl+r", "q"} {
		if isNavigationKey(key) {
			t.Errorf("isNavigationKey(%q) = true, want false", key)
		}
	}
}

func TestNavigationHotWindowSuppressesBackgroundStatusWork(t *testing.T) {
	h := &Home{}
	if h.navigationIsHot() {
		t.Fatal("navigationIsHot() = true before navigation activity")
	}
	h.navigationHotUntil.Store(time.Now().Add(time.Second).UnixNano())
	if !h.navigationIsHot() {
		t.Fatal("navigationIsHot() = false during the hot window")
	}
	h.navigationHotUntil.Store(time.Now().Add(-time.Second).UnixNano())
	if h.navigationIsHot() {
		t.Fatal("navigationIsHot() = true after the hot window")
	}
}

func TestTakeStatusSweepBatchBoundsWorkAndRotates(t *testing.T) {
	instances := make([]*session.Instance, 10)
	for i := range instances {
		instances[i] = &session.Instance{ID: string(rune('a' + i))}
	}
	include := func(*session.Instance) bool { return true }

	first, next := takeStatusSweepBatch(instances, 0, 3, include)
	if len(first) != 3 {
		t.Fatalf("first batch size = %d, want 3", len(first))
	}
	if next != 3 {
		t.Fatalf("next sweep index = %d, want 3", next)
	}
	second, next := takeStatusSweepBatch(instances, next, 3, include)
	if len(second) != 3 {
		t.Fatalf("second batch size = %d, want 3", len(second))
	}
	for i := range first {
		if first[i].ID == second[0].ID {
			t.Fatalf("round-robin repeated %q before exhausting the first batch", first[i].ID)
		}
	}
	if next != 6 {
		t.Fatalf("second next sweep index = %d, want 6", next)
	}
}

func TestSessionRenderSnapshotCachesFooterCapabilities(t *testing.T) {
	h := NewHome()
	inst := &session.Instance{
		ID:               "footer-capabilities",
		Tool:             "hermes",
		Status:           session.StatusIdle,
		Sandbox:          &session.SandboxConfig{Enabled: true},
		MultiRepoEnabled: true,
	}

	h.refreshSessionRenderSnapshot([]*session.Instance{inst})
	state := h.getSessionRenderState(inst)
	if !state.canRestartFresh {
		t.Fatal("render snapshot canRestartFresh = false, want true")
	}
	if !state.sandboxed {
		t.Fatal("render snapshot sandboxed = false, want true")
	}
	if !state.multiRepo {
		t.Fatal("render snapshot multiRepo = false, want true")
	}
}

func TestMCPInfoForRenderUsesOnlyCachedValues(t *testing.T) {
	h := NewHome()
	inst := &session.Instance{ID: "mcp-render-cache", Tool: "codex"}

	if got := h.mcpInfoForRender(inst); got != nil {
		t.Fatalf("uncached MCP info = %#v, want nil", got)
	}

	want := &session.MCPInfo{Global: []string{"cached-server"}}
	h.mcpPreviewCache[inst.ID] = want
	if got := h.mcpInfoForRender(inst); got != want {
		t.Fatalf("cached MCP info = %#v, want %#v", got, want)
	}
}

func TestMCPInfoFetchCompletionBeforeInvalidationIsDiscarded(t *testing.T) {
	h := NewHome()
	t.Cleanup(h.Close)
	inst := &session.Instance{ID: "mcp-invalidated-in-flight", Tool: "codex"}
	h.refreshSessionRenderSnapshot([]*session.Instance{inst})

	staleFetch := h.fetchMCPInfo(inst)
	if staleFetch == nil {
		t.Fatal("initial MCP fetch was not started")
	}
	h.invalidatePreviewCache(inst.ID)
	_, _ = h.Update(staleFetch())

	h.mcpPreviewCacheMu.RLock()
	_, staleCached := h.mcpPreviewCache[inst.ID]
	h.mcpPreviewCacheMu.RUnlock()
	if staleCached {
		t.Fatal("MCP completion predating invalidation restored stale cache data")
	}
	if retry := h.fetchMCPInfo(inst); retry == nil {
		t.Fatal("stale MCP completion suppressed the post-invalidation retry")
	}
}

func TestRecentSessionsLoadedMessageUpdatesVisibleLocalDialog(t *testing.T) {
	h, _, _ := newWatcherEffectsHome(t)
	h.newDialog.ShowInGroup("work", "work", "/tmp", nil, "")
	want := []*statedb.RecentSessionRow{{Title: "recent", ProjectPath: "/tmp"}}

	_, _ = h.Update(recentSessionsLoadedMsg{sessions: want})

	if len(h.newDialog.recentSessions) != 1 || h.newDialog.recentSessions[0].Title != "recent" {
		t.Fatalf("recent sessions = %#v, want the loaded local session", h.newDialog.recentSessions)
	}
}

func TestDialogDefaultPathUsesStoredValueWithoutFilesystemProbe(t *testing.T) {
	h, _, _ := newWatcherEffectsHome(t)
	groupPath := h.instances[0].GroupPath
	requireGroup := h.groupTree.SetDefaultPathForGroup(groupPath, "/path/that/does/not/exist")
	if !requireGroup {
		t.Fatalf("expected group %q to exist", groupPath)
	}
	if got := h.getDefaultPathForDialog(groupPath); got != "/path/that/does/not/exist" {
		t.Fatalf("dialog default path = %q, want stored path", got)
	}
}

func TestNewDialogWithConfigUsesProvidedDefaults(t *testing.T) {
	d := NewNewDialog()
	cfg := &session.UserConfig{
		Docker:   session.DockerSettings{DefaultEnabled: true},
		Worktree: session.WorktreeSettings{DefaultEnabled: true},
	}
	d.ShowInGroupWithConfig("work", "work", "/tmp", nil, "", cfg)
	if !d.sandboxEnabled {
		t.Fatal("provided Docker default was not applied")
	}
	if !d.worktreeEnabled {
		t.Fatal("provided worktree default was not applied")
	}
}

func TestRenderDebugBarUsesCachedRuntimeStats(t *testing.T) {
	h := &Home{debugMode: true}
	h.debugHeapBytes.Store(12 * 1024 * 1024)
	h.debugGoroutines.Store(17)

	got := h.renderDebugBar()
	if !strings.Contains(got, "goroutines: 17") {
		t.Fatalf("debug bar = %q, want cached goroutine count", got)
	}
	if !strings.Contains(got, "heap: 12.0MB") {
		t.Fatalf("debug bar = %q, want cached heap size", got)
	}
}

func TestDebugStatsSnapshotReportsRuntimeMetrics(t *testing.T) {
	msg, ok := debugStatsSnapshot().(debugStatsMsg)
	if !ok {
		t.Fatalf("debugStatsSnapshot() = %T, want debugStatsMsg", debugStatsSnapshot())
	}
	if msg.heapBytes == 0 {
		t.Fatal("debugStatsSnapshot() reported zero heap bytes")
	}
	if msg.goroutines < 1 {
		t.Fatalf("debugStatsSnapshot() reported %d goroutines", msg.goroutines)
	}
}

// BenchmarkHomeViewLargeSessionList keeps the interaction path measurable in
// the same package as Home. A scroll key itself only changes cursor state; the
// expensive work users perceive is the following frame render.
func BenchmarkHomeViewLargeSessionList(b *testing.B) {
	previousWorkers := homeBackgroundWorkersEnabled
	homeBackgroundWorkersEnabled = false
	b.Cleanup(func() { homeBackgroundWorkersEnabled = previousWorkers })

	h := NewHome()
	b.Cleanup(h.Close)
	h.width = 120
	h.height = 40
	h.initialLoading = false

	instances := make([]*session.Instance, 2000)
	for i := range instances {
		inst := session.NewInstanceWithTool("session", "/tmp", "shell")
		inst.Title = "session-" + string(rune('a'+i%26))
		inst.GroupPath = "work"
		instances[i] = inst
	}
	h.instances = instances
	h.instanceByID = make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		h.instanceByID[inst.ID] = inst
	}
	h.groupTree = session.NewGroupTree(instances)
	h.rebuildFlatItems()
	h.cursor = 1
	h.previewCache[instances[0].ID] = strings.Repeat("cached preview line\n", 20)
	h.previewCacheTime[instances[0].ID] = time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.cursor = 1 + i%min(100, len(h.flatItems)-1)
		h.syncViewport()
		_ = h.View()
	}
}

// BenchmarkHomeRenderPreviewLargeHistory isolates the selected-preview work
// that runs again when the list cursor moves. It keeps the history large enough
// to expose accidental whole-scrollback scans without requiring a live tmux
// server.
func BenchmarkHomeRenderPreviewLargeHistory(b *testing.B) {
	previousWorkers := homeBackgroundWorkersEnabled
	homeBackgroundWorkersEnabled = false
	b.Cleanup(func() { homeBackgroundWorkersEnabled = previousWorkers })

	h := NewHome()
	b.Cleanup(h.Close)
	h.width = 120
	h.height = 40
	h.initialLoading = false
	inst := session.NewInstanceWithTool("preview-session", "/tmp", "shell")
	h.instances = []*session.Instance{inst}
	h.instanceByID = map[string]*session.Instance{inst.ID: inst}
	h.groupTree = session.NewGroupTree(h.instances)
	h.rebuildFlatItems()
	for i, item := range h.flatItems {
		if item.Type == session.ItemTypeSession {
			h.cursor = i
			break
		}
	}
	h.previewCache[inst.ID] = strings.Repeat("preview history line\n", 100000)
	h.previewCacheTime[inst.ID] = time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.previewScrollOffset = i % 100
		_ = h.renderPreviewPane(80, 30)
	}
}
