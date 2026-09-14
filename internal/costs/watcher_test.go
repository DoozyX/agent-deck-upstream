package costs_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestCostEventWatcher(t *testing.T) {
	dir := t.TempDir()

	w, err := costs.NewCostEventWatcher(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	go w.Start()

	// Small delay for watcher to start
	time.Sleep(50 * time.Millisecond)

	cf := costs.RawCostEvent{
		InstanceID:   "inst-1",
		Model:        "claude-sonnet-4-6",
		InputTokens:  1000,
		OutputTokens: 500,
		Timestamp:    time.Now().UnixNano(),
	}
	data, _ := json.Marshal(cf)
	tmpPath := filepath.Join(dir, "inst-1_123.json.tmp")
	finalPath := filepath.Join(dir, "inst-1_123.json")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-w.EventCh():
		if ev.InstanceID != "inst-1" {
			t.Errorf("instance = %q, want inst-1", ev.InstanceID)
		}
		if ev.InputTokens != 1000 {
			t.Errorf("input = %d, want 1000", ev.InputTokens)
		}
		time.Sleep(100 * time.Millisecond)
		if _, err := os.Stat(finalPath); err != nil {
			t.Fatalf("durable queue file removed before persistence acknowledgment: %v", err)
		}
		if err := ev.Ack(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
			t.Fatalf("acknowledged queue file still exists: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for cost event")
	}
}

func TestCostEventWatcherDeliversQueueFilePresentAtStartup(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(dir, "predating.json")
	data, err := json.Marshal(costs.RawCostEvent{
		InstanceID: "inst-predating", Model: "claude-sonnet-5",
		InputTokens: 7, Timestamp: time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queuePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	w, err := costs.NewCostEventWatcher(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	go w.Start()

	select {
	case delivery := <-w.EventCh():
		if delivery.InstanceID != "inst-predating" || delivery.InputTokens != 7 {
			t.Fatalf("startup delivery=%+v", delivery.RawCostEvent)
		}
		if err := delivery.Ack(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("startup did not deliver pre-existing durable queue file")
	}
}
