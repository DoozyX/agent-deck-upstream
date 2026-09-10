package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Design 2026-09-10-parallel-orchestrate-loop: the brainstorm session hands
// an approved design to a detached conductor, review rounds after the first
// are full-branch so any clean round is terminal, minor-only findings never
// park a task, relay subtasks stack instead of waiting for a clean sibling,
// and the design itself carries architecture, interfaces and a decomposition
// sketch so the planner elaborates instead of re-deciding.
func TestParallelOrchestrateLoop(t *testing.T) {
	repoRoot := filepath.Clean("..")
	readNormalized := func(t *testing.T, rel ...string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(append([]string{repoRoot}, rel...)...))
		if err != nil {
			t.Fatalf("read %v: %v", rel, err)
		}
		return strings.Join(strings.Fields(string(b)), " ")
	}
	requireAll := func(t *testing.T, label, text string, rules []string) {
		t.Helper()
		for _, rule := range rules {
			if !strings.Contains(text, strings.Join(strings.Fields(rule), " ")) {
				t.Errorf("%s missing %q", label, rule)
			}
		}
	}
	forbidAll := func(t *testing.T, label, text string, rules []string) {
		t.Helper()
		for _, rule := range rules {
			if strings.Contains(text, strings.Join(strings.Fields(rule), " ")) {
				t.Errorf("%s still contains %q", label, rule)
			}
		}
	}
	promptDir := filepath.Join(repoRoot, "skills", "orchestrate", "references", "prompts")
	renderScript := filepath.Join(promptDir, "render.sh")
	render := func(t *testing.T, name string, inputs ...string) string {
		t.Helper()
		outputPath := filepath.Join(t.TempDir(), name+".md")
		args := append([]string{renderScript, name, outputPath}, inputs...)
		if output, err := exec.Command("bash", args...).CombinedOutput(); err != nil {
			t.Fatalf("render %s: %v\n%s", name, err, output)
		}
		rendered, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("read rendered %s: %v", name, err)
		}
		return strings.Join(strings.Fields(string(rendered)), " ")
	}

	t.Run("brainstorming launches a detached conductor and demands interfaces", func(t *testing.T) {
		skill := readNormalized(t, "skills", "brainstorming", "SKILL.md")
		requireAll(t, "brainstorming skill", skill, []string{
			// D1: the orchestrated exit is a launch, not an in-session skill call.
			`-t "conductor-$RUN_ID" --no-parent`,
			`--message-file "$RUN_ROOT/design/conductor-prompt.md"`,
			"this session is free",
			// D5: feature designs settle contracts before any planner runs.
			"## Architecture",
			"## Interfaces",
			"## Decomposition sketch",
			"parallel-safe: yes|no",
			"A planner that later has to ask a data-model question is a design gap",
		})
	})

	t.Run("review-round replaces review-incremental", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(promptDir, "review-incremental.md")); !os.IsNotExist(err) {
			t.Errorf("review-incremental.md must be deleted (stat err=%v)", err)
		}
		round := render(t, "review-round",
			"AGENT_DECK_REPO=/tmp/agent-deck", "BASE_REF=main", "BASELINE=baseline: none",
			"FOCUSED_TESTS=go test ./internal/usage", "PREVIOUS_FINDINGS=One previous finding",
			"REVIEWED_SHA=0123456789abcdef", "SPEC_BLOCK=Review requirements block",
			"VERDICT_FILE=/tmp/orchestrate/review-r2.md")
		requireAll(t, "review-round", round, []string{
			"One previous finding",
			"git diff 0123456789abcdef...HEAD",
			"git diff main...HEAD",
			"Start the full suite FIRST, detached",
			"/tmp/orchestrate/review-r2.md.suite.log",
			"go test ./internal/usage",
			"Dispatch every layer in ONE message",
			"Checked: tests full cmd=",
			"Checked: tests focused cmd=",
			"already-dispositioned",
		})
		forbidAll(t, "review-round", round, []string{"Do NOT run the full suite"})
	})

	t.Run("fix rounds run focused tests", func(t *testing.T) {
		fix := render(t, "fix", "FINDINGS=Fix this concrete finding", "ROUND=2",
			"FOCUSED_TESTS=go test ./internal/usage")
		requireAll(t, "fix", fix, []string{"go test ./internal/usage", "the next reviewer runs the full suite"})
		forbidAll(t, "fix", fix, []string{"Rerun the full test suite"})
	})

	t.Run("planner elaborates the decomposition sketch", func(t *testing.T) {
		plan := render(t, "plan", "SPEC_PATH=/tmp/approved-design.md", "TASK_DIR=/tmp/orchestrate/task")
		requireAll(t, "plan", plan, []string{"## Decomposition sketch", "do not re-decompose"})
	})

	t.Run("orchestrate has one review track and minor never parks", func(t *testing.T) {
		skill := readNormalized(t, "skills", "orchestrate", "SKILL.md")
		requireAll(t, "orchestrate skill", skill, []string{
			// D1: the hand-off entrance.
			"a session titled `conductor-<run-id>`",
			// D2: any clean round is terminal; the template table names review-round.
			"`VERDICT: clean` from **any** round is terminal",
			"| `review-round` | `VERDICT_FILE` `SPEC_BLOCK` `BASE_REF` `REVIEWED_SHA` `PREVIOUS_FINDINGS` `BASELINE` `FOCUSED_TESTS` `AGENT_DECK_REPO` |",
			"| `fix` | `ROUND` `FINDINGS` `FOCUSED_TESTS` |",
			// D3: counted vs in-place rounds.
			"**in-place round**",
			"counted=<yes|no>",
			"bounded at **3 per task**",
			"Needs-attention is reserved for counted-cap exhaustion",
			// D7: keep the host awake.
			"caffeinate -i",
		})
		forbidAll(t, "orchestrate skill", skill, []string{
			"review-incremental",
			"2 full-branch gate reviews",
			"Full-branch end gate",
		})
	})

	t.Run("relay stacks subtasks", func(t *testing.T) {
		split := readNormalized(t, "skills", "orchestrate", "references", "single-issue-split.md")
		requireAll(t, "single-issue-split", split, []string{
			"## Stacked relay",
			"the moment N's implementer reports `done_status=ok`",
			"At most **2 tasks ahead**",
			"N is clean, and N+1's implementer has merged N's final HEAD",
			"stacked-on=<task-n> start-sha=<sha>",
		})
		forbidAll(t, "single-issue-split", split, []string{"deferred full-branch gate", "Sequential relay (default)"})
	})
}
