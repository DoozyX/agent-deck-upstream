package session

import (
	"strings"
	"testing"
)

func TestLaunchAcknowledgement_ImmediateExitsFailAndClean(t *testing.T) {
	skipIfNoTmuxBinary(t)
	for _, tc := range []struct{ name, command, status string }{
		{"clean", "sh -c 'exit 0'", "exit status 0"},
		{"nonzero", "sh -c 'exit 7'", "exit status 7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withConfig(t, &UserConfig{Tmux: TmuxSettings{Options: map[string]string{"remain-on-exit": "on"}}})
			inst := NewInstance("launch-ack-"+tc.name, t.TempDir())
			inst.Tool, inst.Command = "custom-launch-ack-"+tc.name, tc.command
			err := inst.Start()
			if err == nil || !strings.Contains(err.Error(), tc.status) {
				t.Fatalf("Start() = %v, want %s", err, tc.status)
			}
			if inst.Exists() {
				t.Fatal("failed launch left a stale tmux session")
			}
		})
	}
}

func TestLaunchAcknowledgement_LongRunningStartSucceeds(t *testing.T) {
	skipIfNoTmuxBinary(t)
	inst := NewInstance("launch-ack-long-running", t.TempDir())
	inst.Tool, inst.Command = "custom-launch-ack-long-running", "sleep 5"
	t.Cleanup(func() { _ = inst.Kill() })
	if err := inst.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if !inst.Exists() {
		t.Fatal("successful start has no tmux session")
	}
}
