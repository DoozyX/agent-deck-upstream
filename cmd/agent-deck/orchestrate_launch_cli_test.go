package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func writeFakeOrchestrateTool(t *testing.T, home, tool string) string {
	t.Helper()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(binDir, tool)
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return binDir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func readOrchestrateReceiptFromShow(t *testing.T, home, id string) session.ResolvedLaunch {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "session", "show", id, "--json")
	if code != 0 {
		t.Fatalf("session show failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var shown struct {
		OrchestrateLaunch *session.ResolvedLaunch `json:"orchestrate_launch"`
	}
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatalf("parse session show: %v\nstdout: %s", err, stdout)
	}
	if shown.OrchestrateLaunch == nil {
		t.Fatalf("session show lost persisted orchestrate receipt:\n%s", stdout)
	}
	return *shown.OrchestrateLaunch
}

func TestLaunchOrchestrateRole_SupportedCLIEntryPointPersistsPrecedenceAndTaskTools(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; launch CLI needs a real tmux server")
	}

	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestConfig(t, home, `
[mcps.memory]
command = "true"

[groups."work".claude]
model = "claude-haiku-4-5"

[orchestrate.routine]
claude_model = "sonnet"
claude_effort = "medium"
`)

	socket := isolatedTmuxSocket1031(t)
	pathEnv := "PATH=" + writeFakeOrchestrateTool(t, home, "claude")
	stdout, stderr, code := runAgentDeckWithEnv(t, home, []string{pathEnv},
		"launch", "--title", "role-cli-explicit", "--cmd", "claude", "--group", "work",
		"--orchestrate-role", "routine", "--orchestrate-browser", "--mcp", "memory",
		"--extra-arg", "--model", "--extra-arg", "claude-sonnet-4-6",
		"--extra-arg", "--effort=high", "--no-parent", "--no-wait",
		"--tmux-socket", socket, "--json", project,
	)
	if code != 0 {
		t.Fatalf("role launch failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var launched struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &launched); err != nil || launched.ID == "" {
		t.Fatalf("parse launch response: id=%q err=%v\nstdout: %s", launched.ID, err, stdout)
	}

	receipt := readOrchestrateReceiptFromShow(t, home, launched.ID)
	if receipt.Model != "claude-sonnet-4-6" || receipt.Effort != "high" {
		t.Fatalf("receipt ignored explicit extra args: %#v", receipt)
	}
	if receipt.ModelSource != "explicit" || receipt.EffortSource != "explicit" {
		t.Fatalf("explicit provenance missing: %#v", receipt)
	}
	if !receipt.ToolLoadout.Browser || receipt.ToolLoadout.StrictEmptyMCP || len(receipt.ToolLoadout.MCPs) != 1 || receipt.ToolLoadout.MCPs[0] != "memory" {
		t.Fatalf("named task MCP missing or disabled: %#v", receipt.ToolLoadout)
	}
	if data, err := os.ReadFile(filepath.Join(project, ".mcp.json")); err != nil || !strings.Contains(string(data), "memory") {
		t.Fatalf("supported --mcp path did not materialize memory: err=%v data=%s", err, data)
	}
}

func TestLaunchOrchestrateRole_SupportedCLIEntryPointUsesAncestorGroupBeforeRoleDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; launch CLI needs a real tmux server")
	}

	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestConfig(t, home, `
[groups."work".claude]
model = "claude-sonnet-4-6"

[orchestrate.routine]
claude_model = "haiku"
`)

	socket := isolatedTmuxSocket1031(t)
	stdout, stderr, code := runAgentDeckWithEnv(t, home, []string{"PATH=" + writeFakeOrchestrateTool(t, home, "claude")},
		"launch", "--title", "role-cli-group", "--cmd", "claude", "--group", "work/child",
		"--orchestrate-role", "routine", "--no-parent", "--no-wait",
		"--tmux-socket", socket, "--json", project,
	)
	if code != 0 {
		t.Fatalf("role launch failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var launched struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &launched); err != nil || launched.ID == "" {
		t.Fatalf("parse launch response: id=%q err=%v\nstdout: %s", launched.ID, err, stdout)
	}
	receipt := readOrchestrateReceiptFromShow(t, home, launched.ID)
	if receipt.Model != "claude-sonnet-4-6" || receipt.ModelSource != "group:work" {
		t.Fatalf("ancestor group precedence missing from supported launch: %#v", receipt)
	}
	if !receipt.ToolLoadout.StrictEmptyMCP {
		t.Fatalf("non-browser launch did not retain strict empty MCP loadout: %#v", receipt.ToolLoadout)
	}
}

func TestOrchestrateExplicitExtraArgs_NormalizesSupportedForms(t *testing.T) {
	tests := []struct {
		name       string
		tool       string
		args       []string
		wantModel  string
		wantEffort string
	}{
		{name: "Codex long model", tool: "codex", args: []string{"--model", "gpt-5.5"}, wantModel: "gpt-5.5"},
		{name: "Codex short model", tool: "codex", args: []string{"-m", "gpt-5.5"}, wantModel: "gpt-5.5"},
		{name: "Codex short model equals", tool: "codex", args: []string{"-m=gpt-5.5"}, wantModel: "gpt-5.5"},
		{name: "Codex TOML model", tool: "codex", args: []string{"-c", `model="gpt-5.5"`}, wantModel: "gpt-5.5"},
		{name: "Codex TOML effort", tool: "codex", args: []string{"-c", `model_reasoning_effort="high"`}, wantEffort: "high"},
		{name: "Codex long config equals", tool: "codex", args: []string{`--config=model="gpt-5.5"`, `--config=model_reasoning_effort="xhigh"`}, wantModel: "gpt-5.5", wantEffort: "xhigh"},
		{name: "Claude split effort", tool: "claude", args: []string{"--effort", "high"}, wantEffort: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, effort, err := orchestrateExplicitExtraArgs(tt.tool, tt.args)
			if err != nil || model != tt.wantModel || effort != tt.wantEffort {
				t.Fatalf("orchestrateExplicitExtraArgs() = model=%q effort=%q err=%v; want model=%q effort=%q", model, effort, err, tt.wantModel, tt.wantEffort)
			}
		})
	}
}

func TestOrchestrateExplicitExtraArgs_RejectsEmptyExplicitValues(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args []string
	}{
		{name: "long model split empty", tool: "codex", args: []string{"--model", "  "}},
		{name: "long model equals empty", tool: "codex", args: []string{"--model="}},
		{name: "short model split empty", tool: "codex", args: []string{"-m", ""}},
		{name: "short model equals empty", tool: "codex", args: []string{"-m="}},
		{name: "TOML model raw empty", tool: "codex", args: []string{"-c", "model="}},
		{name: "TOML model quoted empty", tool: "codex", args: []string{"-c", `model=""`}},
		{name: "TOML effort raw empty", tool: "codex", args: []string{"-c", "model_reasoning_effort="}},
		{name: "TOML effort quoted empty", tool: "codex", args: []string{"-c", `model_reasoning_effort=""`}},
		{name: "Claude effort split empty", tool: "claude", args: []string{"--effort", " "}},
		{name: "Claude effort equals empty", tool: "claude", args: []string{"--effort="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if model, effort, err := orchestrateExplicitExtraArgs(tt.tool, tt.args); err == nil {
				t.Fatalf("empty explicit value accepted: model=%q effort=%q", model, effort)
			}
		})
	}
}

func TestLaunchOrchestrateRole_CodexExtraArgFormsReachPersistedCLIReceipt(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; launch CLI needs a real tmux server")
	}
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	socket := isolatedTmuxSocket1031(t)
	stdout, stderr, code := runAgentDeckWithEnv(t, home, []string{"PATH=" + writeFakeOrchestrateTool(t, home, "codex")},
		"launch", "--title", "role-cli-codex", "--cmd", "codex",
		"--orchestrate-role", "routine",
		"--extra-arg", "--model=gpt-5.5",
		"--extra-arg", "--config", "--extra-arg", "model_reasoning_effort=minimal",
		"--no-parent", "--no-wait", "--tmux-socket", socket, "--json", project,
	)
	if code != 0 {
		t.Fatalf("Codex role launch failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var launched struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &launched); err != nil || launched.ID == "" {
		t.Fatalf("parse launch response: id=%q err=%v\nstdout: %s", launched.ID, err, stdout)
	}
	receipt := readOrchestrateReceiptFromShow(t, home, launched.ID)
	if receipt.Model != "gpt-5.5" || receipt.Effort != "minimal" || receipt.ModelSource != "explicit" || receipt.EffortSource != "explicit" {
		t.Fatalf("Codex extra-arg precedence missing from CLI receipt: %#v", receipt)
	}
}

func TestLaunchOrchestrateRole_ConfiguredUnsupportedChoiceParksBeforeStart(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestConfig(t, home, "[orchestrate.routine]\nclaude_model = \"claude-sonnet-9\"\n")

	stdout, stderr, code := runAgentDeck(t, home,
		"launch", "--title", "role-cli-parked", "--cmd", "claude",
		"--orchestrate-role", "routine", "--no-parent", "--no-wait", "--json", project,
	)
	if code == 0 || !strings.Contains(stdout+stderr, "parked") {
		t.Fatalf("unsupported configured choice was not parked: exit=%d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	listed := readSessionsJSON(t, home)
	if strings.Contains(listed, "role-cli-parked") {
		t.Fatalf("parked launch persisted or started a session:\n%s", listed)
	}
}
