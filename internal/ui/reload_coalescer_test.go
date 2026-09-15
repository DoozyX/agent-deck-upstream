package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func loadSessionsMsgFromCmd(cmd tea.Cmd) (loadSessionsMsg, bool) {
	if cmd == nil {
		return loadSessionsMsg{}, false
	}
	return loadSessionsMsgFromMsg(cmd())
}

func loadSessionsMsgFromMsg(msg tea.Msg) (loadSessionsMsg, bool) {
	if load, ok := msg.(loadSessionsMsg); ok {
		return load, true
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return loadSessionsMsg{}, false
	}
	for _, cmd := range batch {
		if load, ok := loadSessionsMsgFromCmd(cmd); ok {
			return load, true
		}
	}
	return loadSessionsMsg{}, false
}

func TestReloadCoalescerRunsOneFollowupForBurst(t *testing.T) {
	var c reloadCoalescer

	if !c.request() {
		t.Fatal("first request did not start a reload")
	}
	if c.request() {
		t.Fatal("request during an in-flight reload started another reload")
	}
	if c.request() {
		t.Fatal("second request during an in-flight reload started another reload")
	}
	if !c.complete() {
		t.Fatal("completion did not retain one pending follow-up")
	}
	if c.complete() {
		t.Fatal("completion without a pending request started another reload")
	}
	if !c.request() {
		t.Fatal("request after completion did not start a reload")
	}
}

func TestStorageReloadMessagesCoalesceWhileLoadIsInFlight(t *testing.T) {
	h, _, _ := newWatcherEffectsHome(t)

	_, firstCmd := h.Update(storageChangedMsg{})
	if firstCmd == nil {
		t.Fatal("first storage change did not start a load")
	}
	_, secondCmd := h.Update(storageChangedMsg{})
	if secondCmd == nil || secondCmd() != nil {
		t.Fatal("second storage change started duplicate load")
	}

	firstResult := firstCmd()
	firstCommands, ok := firstResult.(tea.BatchMsg)
	if !ok || len(firstCommands) == 0 {
		t.Fatalf("first command result = %T, want non-empty tea.BatchMsg", firstResult)
	}
	firstMsg := firstCommands[0]()
	_, followupCmd := h.Update(firstMsg)
	if followupCmd == nil {
		t.Fatal("coalesced storage change did not schedule one follow-up load")
	}
	if _, ok := firstMsg.(loadSessionsMsg); !ok {
		t.Fatalf("first load message type = %T, want loadSessionsMsg", firstMsg)
	}
	followupMsg, ok := loadSessionsMsgFromCmd(followupCmd)
	if !ok {
		t.Fatalf("follow-up command message type = %T, want loadSessionsMsg", followupMsg)
	}

	// The follow-up clears the in-flight state after it is applied.
	_, _ = h.Update(followupMsg)
	if h.storageReloads.inFlight || h.storageReloads.pending {
		t.Fatal("reload coalescer remained active after follow-up completion")
	}
}
