package session

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

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

func TestIssue1793_PublicStartPathsUseIsolatedWrapperAndCleanDescendants(t *testing.T) {
	skipIfNoTmuxBinary(t)
	bin := t.TempDir()
	pidPath := filepath.Join(t.TempDir(), "descendant.pid")
	codexPath := filepath.Join(bin, "codex")
	script := fmt.Sprintf("#!/bin/sh\n(trap '' TERM; while :; do sleep 1; done) & printf '%%s' $! > %q\nprintf PUBLIC_START_PATH\nsleep 0.3\nexit 0\n", pidPath)
	if err := os.WriteFile(codexPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	command := codexPath + " --model gpt-5 exec --json work"

	cases := []struct {
		name  string
		start func(*Instance) error
	}{
		{name: "Start", start: func(inst *Instance) error { return inst.Start() }},
		{name: "StartWithMessage", start: func(inst *Instance) error { return inst.StartWithMessage("") }},
		{name: "Restart", start: func(inst *Instance) error {
			if err := inst.Start(); err != nil {
				return err
			}
			return inst.Restart()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := NewInstance("issue1793-"+tc.name, t.TempDir())
			inst.Tool = "shell"
			inst.Command = command
			t.Cleanup(func() { _ = inst.KillAndWait() })
			if err := tc.start(inst); err != nil {
				t.Fatalf("%s(): %v", tc.name, err)
			}
			var raw []byte
			var err error
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				raw, err = os.ReadFile(pidPath)
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Fatalf("read descendant pid after %s: %v", tc.name, err)
			}
			var pid int
			if _, err := fmt.Sscanf(string(raw), "%d", &pid); err != nil || pid <= 0 {
				t.Fatalf("descendant pid after %s = %q: %v", tc.name, raw, err)
			}
			deadline = time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && syscall.Kill(pid, syscall.Signal(0)) == nil {
				time.Sleep(10 * time.Millisecond)
			}
			if syscall.Kill(pid, syscall.Signal(0)) == nil {
				t.Fatalf("%s() returned while isolated descendant %d was alive", tc.name, pid)
			}
		})
	}
}
