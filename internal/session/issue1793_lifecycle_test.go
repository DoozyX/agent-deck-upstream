package session

import "testing"

func TestIssue1793_BoundedCodexExecUsesIsolatedInitialProcess(t *testing.T) {
	bounded := &Instance{Tool: "codex", Command: "codex exec --json 'echo hello'"}
	if !bounded.runCommandAsInitialProcess() {
		t.Fatal("bounded Codex exec must launch as the initial process")
	}
	if !bounded.expectsFastExit() {
		t.Fatal("bounded Codex exec must select the isolated process-group wrapper")
	}

	interactive := &Instance{Tool: "codex", Command: "codex"}
	if interactive.expectsFastExit() {
		t.Fatal("interactive Codex must retain PTY-preserving initial-process behavior")
	}
}
