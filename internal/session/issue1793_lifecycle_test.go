package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestIssue1793_CodexExecArgsRecognizeSupportedGlobalOptions(t *testing.T) {
	valueOptions := []struct {
		name   string
		option string
	}{
		{name: "image", option: "--image"},
		{name: "short image", option: "-i"},
		{name: "model", option: "--model"},
		{name: "short model", option: "-m"},
		{name: "local provider", option: "--local-provider"},
		{name: "profile", option: "--profile"},
		{name: "short profile", option: "-p"},
		{name: "sandbox", option: "--sandbox"},
		{name: "short sandbox", option: "-s"},
		{name: "cwd", option: "--cd"},
		{name: "short cwd", option: "-C"},
		{name: "add dir", option: "--add-dir"},
		{name: "config", option: "--config"},
		{name: "short config", option: "-c"},
		{name: "color", option: "--color"},
		{name: "approval", option: "--ask-for-approval"},
		{name: "short approval", option: "-a"},
		{name: "thread source", option: "--thread-source"},
		{name: "output schema", option: "--output-schema"},
		{name: "last message", option: "--output-last-message"},
		{name: "short last message", option: "-o"},
		{name: "remote", option: "--remote"},
		{name: "remote token env", option: "--remote-auth-token-env"},
		{name: "enable", option: "--enable"},
		{name: "disable", option: "--disable"},
	}
	tests := []struct {
		name   string
		fields []string
		want   bool
	}{
		{name: "exec subcommand", fields: []string{"codex", "exec"}, want: true},
		{name: "exec visible alias", fields: []string{"codex", "e"}, want: true},
		{name: "long value option", fields: []string{"codex", "--model", "gpt-5", "exec"}, want: true},
		{name: "short value option", fields: []string{"codex", "-m", "gpt-5", "exec"}, want: true},
		{name: "attached short value", fields: []string{"codex", "-mgpt-5", "exec"}, want: true},
		{name: "attached short value with equals", fields: []string{"codex", "-ckey=exec", "exec"}, want: true},
		{name: "attached short value without equals", fields: []string{"codex", "-ckeyexec", "exec"}, want: true},
		{name: "name equals value", fields: []string{"codex", "--model=gpt-5", "exec"}, want: true},
		{name: "all boolean families", fields: []string{"codex", "--strict-config", "--json", "--ephemeral", "--ignore-rules", "exec"}, want: true},
		{name: "config value named exec", fields: []string{"codex", "--config", "exec", "--json", "task"}, want: false},
		{name: "config equals exec", fields: []string{"codex", "--config=exec", "--json", "task"}, want: false},
		{name: "config value containing exec", fields: []string{"codex", "--config", "model=exec", "exec"}, want: true},
		{name: "boolean equals form is not a value option", fields: []string{"codex", "--json=exec", "task", "exec"}, want: false},
		{name: "each value option consumes exec", fields: []string{"codex", "--image", "exec"}, want: false},
		{name: "short image consumes exec", fields: []string{"codex", "-i", "exec"}, want: false},
		{name: "image consumes later exec", fields: []string{"codex", "--image", "foo", "exec"}, want: false},
		{name: "short image consumes later exec", fields: []string{"codex", "-i", "foo", "exec"}, want: false},
		{name: "image consumes prompt and later exec", fields: []string{"codex", "--image", "foo", "task", "exec"}, want: false},
		{name: "image equals consumes later exec", fields: []string{"codex", "--image=foo", "exec"}, want: false},
		{name: "short image equals consumes later exec", fields: []string{"codex", "-i=foo", "exec"}, want: false},
		{name: "image missing before option", fields: []string{"codex", "--image", "--model", "exec"}, want: false},
		{name: "image stops at later option", fields: []string{"codex", "--image", "foo", "--model", "gpt-5", "exec"}, want: true},
		{name: "image accepts lone hyphen value", fields: []string{"codex", "--image", "-", "exec"}, want: false},
		{name: "cwd consumes exec", fields: []string{"codex", "--cd", "exec"}, want: false},
		{name: "short cwd consumes exec", fields: []string{"codex", "-C", "exec"}, want: false},
		{name: "sandbox consumes exec", fields: []string{"codex", "--sandbox", "exec"}, want: false},
		{name: "profile consumes exec", fields: []string{"codex", "--profile", "exec"}, want: false},
		{name: "add dir consumes exec", fields: []string{"codex", "--add-dir", "exec"}, want: false},
		{name: "local provider consumes exec", fields: []string{"codex", "--local-provider", "exec"}, want: false},
		{name: "thread source consumes exec", fields: []string{"codex", "--thread-source", "exec"}, want: false},
		{name: "output schema consumes exec", fields: []string{"codex", "--output-schema", "exec"}, want: false},
		{name: "last message consumes exec", fields: []string{"codex", "--output-last-message", "exec"}, want: false},
		{name: "short last message consumes exec", fields: []string{"codex", "-o", "exec"}, want: false},
		{name: "color consumes exec", fields: []string{"codex", "--color", "exec"}, want: false},
		{name: "short config consumes exec", fields: []string{"codex", "-c", "exec"}, want: false},
		{name: "missing model before option", fields: []string{"codex", "--model", "-m", "exec"}, want: false},
		{name: "missing short model before option", fields: []string{"codex", "-m", "--image", "foo", "exec"}, want: false},
		{name: "missing value at end", fields: []string{"codex", "--model"}, want: false},
		{name: "lone hyphen prompt before exec", fields: []string{"codex", "-", "exec"}, want: true},
		{name: "double dash terminates options", fields: []string{"codex", "--", "exec"}, want: false},
		{name: "root prompt before exec", fields: []string{"codex", "task", "exec"}, want: true},
		{name: "root prompt and option before exec", fields: []string{"codex", "task", "--model", "gpt-5", "exec"}, want: true},
		{name: "two root positionals are not exec", fields: []string{"codex", "task", "other", "exec"}, want: false},
		{name: "known top-level command is not a root prompt", fields: []string{"codex", "review", "exec"}, want: false},
		{name: "known top-level command after options is not a root prompt", fields: []string{"codex", "--json", "resume", "exec"}, want: false},
		{name: "agents command is not a root prompt", fields: []string{"codex", "agents", "exec"}, want: false},
		{name: "plugin command is not a root prompt", fields: []string{"codex", "plugin", "exec"}, want: false},
		{name: "remote-control command is not a root prompt", fields: []string{"codex", "remote-control", "exec"}, want: false},
		{name: "update command is not a root prompt", fields: []string{"codex", "update", "exec"}, want: false},
		{name: "doctor command is not a root prompt", fields: []string{"codex", "doctor", "exec"}, want: false},
		{name: "queue command is not a root prompt", fields: []string{"codex", "queue", "exec"}, want: false},
		{name: "archive command is not a root prompt", fields: []string{"codex", "archive", "exec"}, want: false},
		{name: "delete command is not a root prompt", fields: []string{"codex", "delete", "exec"}, want: false},
		{name: "migrate-rollouts command is not a root prompt", fields: []string{"codex", "migrate-rollouts", "exec"}, want: false},
		{name: "unarchive command is not a root prompt", fields: []string{"codex", "unarchive", "exec"}, want: false},
		{name: "exec-server command is not a root prompt", fields: []string{"codex", "exec-server", "exec"}, want: false},
		{name: "login command is not a root prompt", fields: []string{"codex", "login", "exec"}, want: false},
		{name: "logout command is not a root prompt", fields: []string{"codex", "logout", "exec"}, want: false},
		{name: "mcp command is not a root prompt", fields: []string{"codex", "mcp", "exec"}, want: false},
		{name: "mcp-server command is not a root prompt", fields: []string{"codex", "mcp-server", "exec"}, want: false},
		{name: "app-server command is not a root prompt", fields: []string{"codex", "app-server", "exec"}, want: false},
		{name: "app command is not a root prompt", fields: []string{"codex", "app", "exec"}, want: false},
		{name: "completion command is not a root prompt", fields: []string{"codex", "completion", "exec"}, want: false},
		{name: "sandbox command is not a root prompt", fields: []string{"codex", "sandbox", "exec"}, want: false},
		{name: "debug command is not a root prompt", fields: []string{"codex", "debug", "exec"}, want: false},
		{name: "apply command is not a root prompt", fields: []string{"codex", "apply", "exec"}, want: false},
		{name: "apply alias is not a root prompt", fields: []string{"codex", "a", "exec"}, want: false},
		{name: "fork command is not a root prompt", fields: []string{"codex", "fork", "exec"}, want: false},
		{name: "cloud command is not a root prompt", fields: []string{"codex", "cloud", "exec"}, want: false},
		{name: "features command is not a root prompt", fields: []string{"codex", "features", "exec"}, want: false},
		{name: "execpolicy command is not a root prompt", fields: []string{"codex", "execpolicy", "exec"}, want: false},
		{name: "help command is not a root prompt", fields: []string{"codex", "help", "exec"}, want: false},
		{name: "resume command is not a root prompt", fields: []string{"codex", "resume", "exec"}, want: false},
		{name: "terminal long help flag stops scanning", fields: []string{"codex", "--help", "exec"}, want: false},
		{name: "terminal short help flag stops scanning", fields: []string{"codex", "-h", "exec"}, want: false},
		{name: "terminal long version flag stops scanning", fields: []string{"codex", "--version", "exec"}, want: false},
		{name: "terminal short version flag stops scanning", fields: []string{"codex", "-V", "exec"}, want: false},
		{name: "help flag after valid prompt is terminal", fields: []string{"codex", "task", "--help", "exec"}, want: false},
		{name: "version flag after valid prompt is terminal", fields: []string{"codex", "task", "--version", "exec"}, want: false},
		{name: "unknown option fails closed", fields: []string{"codex", "--future-option", "exec"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCodexExecArgs(tt.fields); got != tt.want {
				t.Fatalf("isCodexExecArgs(%q) = %v, want %v", tt.fields, got, tt.want)
			}
		})
	}
	for _, option := range valueOptions {
		if option.option == "--image" || option.option == "-i" {
			continue
		}
		t.Run("value option preserves later exec: "+option.name, func(t *testing.T) {
			fields := []string{"codex", option.option, "value", "exec"}
			if !isCodexExecArgs(fields) {
				t.Fatalf("isCodexExecArgs(%q) = false, want true", fields)
			}
		})
	}
}

func TestIssue1793_PublicStartPathsUseIsolatedWrapperAndCleanDescendants(t *testing.T) {
	skipIfNoTmuxBinary(t)
	bin := t.TempDir()
	pidDir := t.TempDir()
	pidPath := filepath.Join(pidDir, "descendant.pid")
	// Every descendant this stub ever spawns is also appended here. pidPath
	// holds only the LATEST one (the Restart case starts twice and overwrites
	// it), so a cleanup driven by pidPath alone strands the earlier one — and
	// the descendant traps TERM on purpose, so nothing short of KILL reaps it.
	// Observed before this: dozens of unkillable-by-TERM sleep loops surviving
	// for hours on the developer's machine.
	allPidsPath := filepath.Join(pidDir, "descendants.pids")
	codexPath := filepath.Join(bin, "codex")
	script := fmt.Sprintf("#!/bin/sh\n(trap '' TERM; while :; do sleep 1; done) & printf '%%s' $! > %q\nprintf '%%s\\n' $! >> %q\nprintf PUBLIC_START_PATH\nsleep 0.3\nexit 0\n", pidPath, allPidsPath)
	t.Cleanup(func() {
		raw, err := os.ReadFile(allPidsPath)
		if err != nil {
			return
		}
		for _, line := range strings.Fields(string(raw)) {
			var pid int
			if _, err := fmt.Sscanf(line, "%d", &pid); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
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
