package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestClassifyTerminatedPane_CleanExitVsCrash pins the classification of a
// session whose tmux pane has terminated after having been started.
//
// A one-shot worker runs a command that finishes and exits. When tmux still
// holds the dead pane (remain-on-exit), the real process exit code is
// available: exit 0 is a clean completion (■ StatusStopped), not a crash — the
// bug was that every terminated pane read as StatusError (✕), making a
// successful one-shot exit indistinguishable from a genuine failure. A
// non-zero exit is a real crash → StatusError.
//
// When no exit code is available (pane torn down without remain-on-exit, so
// tmux discarded the exit status), classification falls back to the per-tool
// heuristic: OpenCode's hookless `/exit` reads as stopped (#1617); every other
// tool's vanished pane stays a crash signal.
func TestClassifyTerminatedPane_CleanExitVsCrash(t *testing.T) {
	tests := []struct {
		name         string
		exitCode     int
		haveExitCode bool
		tool         string
		want         Status
	}{
		// Exit code known (remain-on-exit): the code decides, tool is irrelevant.
		{"clean exit 0 (shell)", 0, true, "shell", StatusStopped},
		{"clean exit 0 (claude)", 0, true, "claude", StatusStopped},
		{"clean exit 0 (sandboxed worker)", 0, true, "codex", StatusStopped},
		{"crash exit 1", 1, true, "shell", StatusError},
		{"crash exit 137 (SIGKILL)", 137, true, "claude", StatusError},
		{"crash exit 2 (opencode)", 2, true, "opencode", StatusError},

		// No exit code (pane torn down): fall back to the per-tool heuristic.
		{"no exit code, opencode clean /exit", 0, false, "opencode", StatusStopped},
		{"no exit code, claude crash", 0, false, "claude", StatusError},
		{"no exit code, shell", 0, false, "shell", StatusError},
		{"no exit code, unknown tool", 0, false, "", StatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyTerminatedPane(tt.exitCode, tt.haveExitCode, tt.tool)
			if got != tt.want {
				t.Errorf("classifyTerminatedPane(%d, %v, %q) = %q, want %q",
					tt.exitCode, tt.haveExitCode, tt.tool, got, tt.want)
			}
		})
	}
}

func TestBoundedShellCodexExecReportsLiveThenCleanCompletion(t *testing.T) {
	skipIfNoTmuxBinary(t)

	bin := t.TempDir()
	// The script name is intentionally codex: process-tree inspection must see
	// the descendant beneath the shell launcher, rather than trusting pane text.
	assert.NoError(t, os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n[ \"$1\" = exec ] || exit 9\nsleep 1\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	inst := NewInstance("bounded-shell-codex", t.TempDir())
	inst.Tool = "shell"
	inst.Command = "codex exec --json work"
	assert.NoError(t, inst.Start())
	t.Cleanup(func() { _ = inst.Kill() })

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	sawRunning := false
	for {
		assert.NoError(t, inst.UpdateStatus())
		if inst.GetStatusThreadSafe() == StatusRunning {
			sawRunning = true
		}
		if sawRunning && inst.GetStatusThreadSafe() == StatusStopped {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("status=%s, sawRunning=%v; want live descendant then clean stopped completion", inst.GetStatusThreadSafe(), sawRunning)
		case <-tick.C:
		}
	}
}

func TestBoundedShellCodexExecReportsNonzeroExit(t *testing.T) {
	skipIfNoTmuxBinary(t)

	bin := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n[ \"$1\" = exec ] || exit 9\nexit 7\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	inst := NewInstance("bounded-shell-codex-fail", t.TempDir())
	inst.Tool = "shell"
	inst.Command = "codex exec --json fail"
	err := inst.Start()
	if err == nil {
		t.Fatal("Start() succeeded after the initial command exited 7")
	}
	assert.Contains(t, err.Error(), "exit status 7")
	t.Cleanup(func() { _ = inst.Kill() })

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		assert.NoError(t, inst.UpdateStatus())
		if inst.GetStatusThreadSafe() == StatusError {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("status=%s; want error for codex exec exit 7", inst.GetStatusThreadSafe())
		case <-tick.C:
		}
	}
}

// TestInitialProcessAcknowledgementRejectsCleanImmediateExit is intentionally
// a real isolated-tmux regression: tmux creation itself succeeded, but a
// bounded command that finishes before Start returns is not a launched
// interactive session. Exit zero stays visible in the error so callers cannot
// mistake completion for a usable pane.
func TestInitialProcessAcknowledgementRejectsCleanImmediateExit(t *testing.T) {
	skipIfNoTmuxBinary(t)

	bin := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	inst := NewInstance("bounded-shell-codex-clean-exit", t.TempDir())
	inst.Tool = "shell"
	inst.Command = "codex exec --json done"
	err := inst.Start()
	if err == nil {
		t.Fatal("Start() succeeded after the initial command exited 0")
	}
	assert.Contains(t, err.Error(), "exit status 0")
	t.Cleanup(func() { _ = inst.Kill() })
}

func TestBoundedCodexExecKeepsExitStatus(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"codex exec --json fix-it", true},
		{"bash -lc 'codex exec --json fix-it'", true},
		{"codex --model gpt-5.6-terra", false},
		{"echo codex exec", false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			inst := NewInstance("bounded-codex", t.TempDir())
			inst.Command = tt.command
			assert.Equal(t, tt.want, inst.isBoundedCodexExec())
			overrides := inst.buildTmuxOptionOverrides()
			if tt.want {
				assert.Equal(t, "on", overrides["remain-on-exit"], "exit 0 must remain observable as stopped")
			}
		})
	}
}

// TestTerminatedPaneStatus_NilTmuxFallsBackToTool guards the no-tmux path: with
// no session to read an exit code from, terminatedPaneStatus must degrade to
// the per-tool heuristic rather than assume a clean exit.
func TestTerminatedPaneStatus_NilTmuxFallsBackToTool(t *testing.T) {
	cases := map[string]Status{
		"opencode": StatusStopped,
		"claude":   StatusError,
		"shell":    StatusError,
		"":         StatusError,
	}
	for tool, want := range cases {
		i := &Instance{Tool: tool}
		if got := i.terminatedPaneStatus(); got != want {
			t.Errorf("terminatedPaneStatus() nil tmux, tool %q = %q, want %q", tool, got, want)
		}
	}
}
