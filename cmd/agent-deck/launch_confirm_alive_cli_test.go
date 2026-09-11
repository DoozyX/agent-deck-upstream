package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end guard for `agent-deck launch --confirm-alive`, built around the
// incident that produced it.
//
// 2026-09-11 11:49:58 CEST: an orchestrate conductor rotation launched its
// successor on codex. `agent-deck launch … --json` returned
// {"success": true, "submitted": true, "delivery": "submitted"}. Three seconds
// later the session was dead — its codex rollout carried
// `codex_error_info: usage_limit` and a zero credit balance. The caller acted
// on `success: true` and destroyed a live run.
//
// The fake codex below is that failure, reduced: a tool that prints its refusal
// and exits a couple of seconds in. A real tool (unlike `-c shell`) runs as the
// pane's initial process, so its exit takes the tmux session with it, which is
// what the launch CLI has to notice.
//
// Two halves are pinned here and both matter:
//   - WITHOUT the flag, the output is exactly what it has always been. Other
//     scripts and skills parse this JSON; the fix is worth nothing if adopting
//     the binary breaks them.
//   - WITH the flag, the same launch exits 1 with success:false, alive:false,
//     the session id still in hand, and the tool's OWN words in doa_detail.

// fakeToolBinDir writes an executable named `name` whose body is `script` into
// a fresh directory, and returns that directory for prepending to PATH.
func fakeToolBinDir(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return dir
}

// launchCLIEnv is cliEnvForIssue1031 with a PATH that finds the fake tool
// first. The TMUX* stripping is inherited from there and is load-bearing: the
// teardown of the isolated socket has to resolve the same socket path this
// subprocess does (see tmuxEnvForIssue1031's postmortem).
func launchCLIEnv(home, binDir string) []string {
	env := cliEnvForIssue1031(home)
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			out = append(out, "PATH="+binDir+string(os.PathListSeparator)+strings.TrimPrefix(kv, "PATH="))
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return out
}

// runLaunchCLI runs one real `agent-deck launch` subprocess and decodes its
// JSON. Decoding is required to succeed: a launch that cannot be parsed is
// useless to the scripted callers this whole feature exists for.
func runLaunchCLI(t *testing.T, home, binDir string, args ...string) (map[string]interface{}, int, string) {
	t.Helper()
	cmd := exec.Command(channelsCLIBinary(t), append([]string{"launch"}, args...)...)
	cmd.Env = launchCLIEnv(home, binDir)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	exitCode := 0
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("launch did not run: %v\nstderr: %s", err, stderr.String())
		}
		exitCode = exitErr.ExitCode()
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(stdout.String()), &payload); err != nil {
		t.Fatalf("launch --json emitted unparseable output: %v\nstdout: %s\nstderr: %s",
			err, stdout.String(), stderr.String())
	}
	return payload, exitCode, stderr.String()
}

// requireTmuxForLaunchCLI skips when no tmux binary exists; these cases need a
// real pane whose disappearance the CLI can observe.
func requireTmuxForLaunchCLI(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; the launch CLI needs a real tmux server")
	}
}

const usageLimitRefusal = "You've hit your usage limit. Try again at Sep 16th, 2026 4:41 PM."

// dyingCodexScript is the incident's tool: it announces the refusal and exits.
const dyingCodexScript = "#!/bin/sh\necho \"" + usageLimitRefusal + "\"\nsleep 1\nexit 1\n"

// livingCodexScript is the control: the same tool, staying up.
const livingCodexScript = "#!/bin/sh\nsleep 30\n"

// TestLaunchConfirmAlive_DyingToolIsReportedDeadOnArrival is the incident
// repro. The prompt genuinely reached the process's argv, so `delivery` stays
// "submitted" — what changed is that the caller is now told the process it was
// delivered to is gone.
func TestLaunchConfirmAlive_DyingToolIsReportedDeadOnArrival(t *testing.T) {
	requireTmuxForLaunchCLI(t)

	home := t.TempDir()
	socket := isolatedTmuxSocket1031(t)
	binDir := fakeToolBinDir(t, "codex", dyingCodexScript)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	payload, exitCode, stderr := runLaunchCLI(t, home, binDir,
		project, "-t", "doa-codex", "--no-parent", "--tmux-socket", socket,
		"-c", "codex", "-m", "rotate the conductor",
		"--confirm-alive", "--alive-window", "15s", "--json")

	if exitCode != 1 {
		t.Fatalf("exit = %d, want 1: a caller that checks only the exit status must learn "+
			"its child is dead\npayload: %v\nstderr: %s", exitCode, payload, stderr)
	}
	if payload["success"] != false {
		t.Errorf("success = %v, want false — this is the field the conductor acted on", payload["success"])
	}
	if payload["alive"] != false {
		t.Errorf("alive = %v, want false", payload["alive"])
	}
	if payload["code"] != ErrCodeSessionDOA {
		t.Errorf("code = %v, want %q", payload["code"], ErrCodeSessionDOA)
	}
	// The id has to survive the failure: a caller that learns its child is dead
	// still has to remove the row, and an error with no handle strands it.
	for _, key := range []string{"id", "session_id", "title"} {
		if v, _ := payload[key].(string); v == "" {
			t.Errorf("payload[%q] is missing: the caller cannot clean up a session it cannot name", key)
		}
	}
	// `delivery` describes the prompt's journey into argv, which really did
	// succeed. Repurposing it here would put a second meaning on a word other
	// scripts already read.
	if payload["delivery"] != "submitted" {
		t.Errorf("delivery = %v, want it left at %q", payload["delivery"], "submitted")
	}
	// The tool's own words are the whole point: they are what separates a usage
	// limit from a crash, and nothing else in agent-deck was reporting them.
	detail, _ := payload["doa_detail"].(string)
	if !strings.Contains(detail, "usage limit") {
		t.Errorf("doa_detail = %q, want the tool's refusal (%q)", detail, usageLimitRefusal)
	}
	if payload["doa_reason"] != "spawn_died_fast" {
		t.Errorf("doa_reason = %v, want spawn_died_fast", payload["doa_reason"])
	}
	// The `error` string has to stand on its own: an operator reading a log line
	// should not have to cross-reference doa_detail to learn why.
	errText, _ := payload["error"].(string)
	if !strings.Contains(errText, "usage limit") || !strings.Contains(errText, "doa-codex") {
		t.Errorf("error = %q, want it to name the session and carry the cause", errText)
	}
}

// TestLaunchConfirmAlive_WithoutTheFlagTheOutputIsUnchanged pins the
// compatibility half. The very same dying launch must still look exactly as it
// did before this flag existed — including reporting success, which is the bug
// the flag exists to let a caller opt out of. A silent default-on would break
// every script and skill that reads this JSON.
func TestLaunchConfirmAlive_WithoutTheFlagTheOutputIsUnchanged(t *testing.T) {
	requireTmuxForLaunchCLI(t)

	home := t.TempDir()
	socket := isolatedTmuxSocket1031(t)
	binDir := fakeToolBinDir(t, "codex", dyingCodexScript)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	payload, exitCode, stderr := runLaunchCLI(t, home, binDir,
		project, "-t", "legacy-codex", "--no-parent", "--tmux-socket", socket,
		"-c", "codex", "-m", "rotate the conductor", "--json")

	if exitCode != 0 || payload["success"] != true {
		t.Fatalf("the default launch contract changed: exit = %d, success = %v\nstderr: %s",
			exitCode, payload["success"], stderr)
	}
	for _, key := range []string{"alive", "liveness_window_ms", "liveness_observed_ms", "doa_reason", "doa_detail"} {
		if _, ok := payload[key]; ok {
			t.Errorf("payload carries %q without --confirm-alive: a caller that never opted in "+
				"must see the keys it has always seen, and no others", key)
		}
	}
}

// TestLaunchConfirmAlive_LiveToolReportsAlive is the control. Without it the
// DOA test above would pass just as well against a check that condemns every
// session, which would be worse than the bug.
func TestLaunchConfirmAlive_LiveToolReportsAlive(t *testing.T) {
	requireTmuxForLaunchCLI(t)

	home := t.TempDir()
	socket := isolatedTmuxSocket1031(t)
	binDir := fakeToolBinDir(t, "codex", livingCodexScript)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	payload, exitCode, stderr := runLaunchCLI(t, home, binDir,
		project, "-t", "live-codex", "--no-parent", "--tmux-socket", socket,
		"-c", "codex", "-m", "rotate the conductor",
		"--confirm-alive", "--alive-window", "2s", "--json")

	if exitCode != 0 {
		t.Fatalf("exit = %d for a session that stayed up\npayload: %v\nstderr: %s",
			exitCode, payload, stderr)
	}
	if payload["success"] != true || payload["alive"] != true {
		t.Errorf("success = %v, alive = %v; want both true", payload["success"], payload["alive"])
	}
	// The window rides along so a caller can see what "alive" was measured
	// over. It is "did not die in 2s", never "is healthy".
	if payload["liveness_window_ms"] != float64(2000) {
		t.Errorf("liveness_window_ms = %v, want 2000", payload["liveness_window_ms"])
	}
	if _, ok := payload["doa_reason"]; ok {
		t.Errorf("a live session must carry no doa_reason: %v", payload)
	}
}

// TestLaunchConfirmAlive_RejectsAWindowWithoutTheFlag: an --alive-window that
// silently does nothing is the same class of quiet failure this whole change is
// about, and the refusal must land before anything is spawned.
func TestLaunchConfirmAlive_RejectsAWindowWithoutTheFlag(t *testing.T) {
	requireTmuxForLaunchCLI(t)

	home := t.TempDir()
	binDir := fakeToolBinDir(t, "codex", livingCodexScript)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	payload, exitCode, stderr := runLaunchCLI(t, home, binDir,
		project, "-t", "orphan-window", "--no-parent", "-c", "codex",
		"--alive-window", "5s", "--json")

	if exitCode == 0 {
		t.Fatalf("exit = 0 for --alive-window without --confirm-alive\npayload: %v", payload)
	}
	if payload["success"] != false || payload["code"] != ErrCodeInvalidOperation {
		t.Errorf("success = %v, code = %v; want false / %s\nstderr: %s",
			payload["success"], payload["code"], ErrCodeInvalidOperation, stderr)
	}
	// Nothing may have been created: the refusal is a precondition, not a
	// cleanup.
	if _, ok := payload["session_id"]; ok {
		t.Errorf("a refused launch created a session anyway: %v", payload)
	}
}
