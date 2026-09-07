package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestOrchestrationSkillDeployedVerificationStructure(t *testing.T) {
	repoRoot := filepath.Clean("..")
	skillPath := filepath.Join(repoRoot, "skills", "orchestrate", "SKILL.md")
	skillBytes, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read orchestration skill: %v", err)
	}
	skill := string(skillBytes)

	entrance := strings.Index(skill, "- A **deployed-system verification** request")
	verificationFlow := strings.Index(skill, "## Deployed-system verification")
	deliveryPipeline := strings.Index(skill, "## Per-task pipeline")
	if entrance < 0 || verificationFlow < 0 || deliveryPipeline < 0 {
		t.Fatalf("missing orchestration sections: entrance=%d verification=%d delivery=%d", entrance, verificationFlow, deliveryPipeline)
	}
	if !(entrance < verificationFlow && verificationFlow < deliveryPipeline) {
		t.Fatalf("verification entrance and flow must precede delivery: entrance=%d verification=%d delivery=%d", entrance, verificationFlow, deliveryPipeline)
	}
	verificationSection := skill[verificationFlow:deliveryPipeline]
	normalizedVerificationSection := strings.Join(strings.Fields(verificationSection), " ")
	requiresStart := strings.Index(skill, "**Requires:**")
	requiresEnd := strings.Index(skill, "**Read `skills/fleet/SKILL.md` first.**")
	if requiresStart < 0 || requiresEnd < 0 || requiresStart >= requiresEnd || requiresEnd >= entrance {
		t.Fatalf("missing or misplaced orchestration prerequisites: requires=%d end=%d entrance=%d", requiresStart, requiresEnd, entrance)
	}
	requiresSection := strings.Join(strings.Fields(skill[requiresStart:requiresEnd]), " ")
	if !strings.Contains(requiresSection, "Delivery/PR entrances additionally require an authenticated `gh` for the target repo; verification-only work does not.") {
		t.Error("verification-only entrance no longer documents that GitHub authentication is unnecessary")
	}

	entrances := []string{
		"list of tasks/issues (2+)",
		"single small task",
		"single big task, no spec",
		"design/spec document",
		"implementation plan",
		"deployed-system verification",
	}
	for _, entranceName := range entrances {
		if !strings.Contains(skill, entranceName) {
			t.Errorf("skill no longer documents existing entrance %q", entranceName)
		}
	}

	phases := []string{
		"1. **Recon.**",
		"2. **Independent measurement arms.**",
		"3. **Conductor validation and adjudication.**",
		"4. **Consolidated report.**",
	}
	previous := verificationFlow
	for _, phase := range phases {
		position := strings.Index(skill[verificationFlow:deliveryPipeline], phase)
		if position < 0 {
			t.Fatalf("verification flow missing phase %q", phase)
		}
		position += verificationFlow
		if position <= previous {
			t.Fatalf("verification phase %q is out of order", phase)
		}
		previous = position
	}

	requiredContracts := []string{
		"with **no assumed edit**: do not enter implementation, pull-request, CI, or deployment stages unless the outcome and authorized scope explicitly permit delivery",
		"Before reading even deciding fields, validate each artifact against the declared schema file, plus its provenance, producer completion, and freshness against recon",
		"Recon writes the arm schema to one file",
		"Pass that path as `ARM_SCHEMA_PATH` to every arm and to the report child",
		"`inconclusive` is for evidence that is missing, stale, unattributable or contradictory — **never for packaging**",
		"Read only the deciding fields where possible",
		"For a flaky external measurement, preserve and diagnose the first failure evidence, then permit at most **one clean rerun** by default",
		"A second failure is a product `defect` when it demonstrates product behavior, or `inconclusive` when the harness, environment, or license prevents a trustworthy decision",
		"A `pass` is terminal with no edits, pull request, CI run, or deployment",
		"A `defect` enters the delivery pipeline only when the defect is within the authorized scope",
		"An `inconclusive` result terminates honestly with what blocked a trustworthy decision; do not claim success or retry indefinitely",
		"The existing child and conductor rotation/handoff rules apply throughout this flow",
		"verification-only `pass` and `inconclusive` outcomes stop before those stages",
	}
	for _, contract := range requiredContracts {
		if !strings.Contains(normalizedVerificationSection, contract) {
			t.Errorf("skill missing deployed-verification contract %q", contract)
		}
	}

	if !strings.Contains(skill, `cp <agent-deck-repo>/skills/orchestrate/references/rotate-conductor.sh "$RUN_DIR/"`) {
		t.Error("run setup no longer installs the conductor rotation/handoff artifact")
	}

	rotationPath := filepath.Join(repoRoot, "skills", "orchestrate", "references", "rotate-conductor.sh")
	rotationBytes, err := os.ReadFile(rotationPath)
	if err != nil {
		t.Fatalf("read conductor rotation artifact: %v", err)
	}
	rotation := string(rotationBytes)
	rotationContracts := []string{
		`HANDOFF="$D/conductor-handoff.md"`,
		`for f in "$MANIFEST" "$HANDOFF"; do`,
		`if [ ! -s "$f" ]; then`,
		"Re-read the orchestrate skill first",
		"Recovery after compaction or rotation",
		"Read these two files to restore durable run state:",
		`agent-deck session set-parent "$cid" "$NEW_ID"`,
		`agent-deck session archive "$SELF_ID"`,
	}
	for _, contract := range rotationContracts {
		if !strings.Contains(rotation, contract) {
			t.Errorf("rotation artifact missing handoff contract %q", contract)
		}
	}
}

func TestExistingDeliveryPromptTemplatesStillRender(t *testing.T) {
	repoRoot := filepath.Clean("..")
	promptDir := filepath.Join(repoRoot, "skills", "orchestrate", "references", "prompts")
	renderScript := filepath.Join(promptDir, "render.sh")
	unresolved := regexp.MustCompile(`\{\{(?:include:)?[A-Za-z0-9_.-]+\}\}`)

	tests := []struct {
		name     string
		args     []string
		required []string
	}{
		{
			name: "plan",
			args: []string{
				"SPEC_PATH=/tmp/approved-design.md",
				"TASK_DIR=/tmp/orchestrate/task",
			},
			required: []string{"/tmp/approved-design.md", "/tmp/orchestrate/task/plan.md"},
		},
		{
			name: "impl",
			args: []string{
				"RUN_DIR=/tmp/orchestrate/run",
				"SPEC_BLOCK=Approved requirements block",
				"TASK_SLUG=contract-task",
				"TASK_TITLE=Contract task",
			},
			required: []string{"Task: Contract task", "Approved requirements block", "/tmp/orchestrate/run/contract-task/handoff.md"},
		},
		{
			name: "review-full",
			args: []string{
				"AGENT_DECK_REPO=/tmp/agent-deck",
				"BASE_BRANCH=main",
				"BASELINE=baseline: none",
				"SPEC_BLOCK=Review requirements block",
				"VERDICT_FILE=/tmp/orchestrate/review-r1.md",
			},
			required: []string{"git merge-base main HEAD", "Review requirements block", "/tmp/orchestrate/review-r1.md"},
		},
		{
			name: "review-incremental",
			args: []string{
				"AGENT_DECK_REPO=/tmp/agent-deck",
				"BASELINE=baseline: none",
				"FOCUSED_TESTS=go test ./internal/usage",
				"PREVIOUS_FINDINGS=One previous finding",
				"REVIEWED_SHA=0123456789abcdef",
				"SPEC_BLOCK=Review requirements block",
				"VERDICT_FILE=/tmp/orchestrate/review-r2.md",
			},
			required: []string{"One previous finding", "git diff 0123456789abcdef...HEAD", "/tmp/orchestrate/review-r2.md"},
		},
		{
			name: "fix",
			args: []string{
				"FINDINGS=Fix this concrete finding",
				"ROUND=2",
			},
			required: []string{"Review round 2", "Fix this concrete finding"},
		},
		{
			name: "ab-judge",
			args: []string{
				"PAIRS_DIR=/tmp/orchestrate/run/task/ab",
				"VERDICT_FILE=/tmp/orchestrate/run/task/ab-judge.md",
			},
			required: []string{"/tmp/orchestrate/run/task/ab", "AB_VERDICT: pair=", "not what the task was"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), tt.name+".md")
			args := []string{renderScript, tt.name, outputPath}
			args = append(args, tt.args...)
			if output, err := exec.Command("bash", args...).CombinedOutput(); err != nil {
				t.Fatalf("render existing %s template: %v\n%s", tt.name, err, output)
			}

			rendered, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("read rendered %s prompt: %v", tt.name, err)
			}
			if match := unresolved.Find(rendered); match != nil {
				t.Fatalf("rendered %s prompt contains unresolved token %q", tt.name, match)
			}
			for _, required := range tt.required {
				if !strings.Contains(string(rendered), required) {
					t.Errorf("rendered %s prompt missing substituted contract %q", tt.name, required)
				}
			}
		})
	}
}

// TestOrchestrationSkillRetroHardenedRules pins the run-safety rules added
// after the 2026-08-19-ob-live-e2e retrospective. Each one exists because a
// real run lost time to its absence: a fix-round send that was accepted and
// never arrived, a landing policy that was defaulted and cost eight declined
// PRs, and a deploy child that fast-forwarded a primary checkout.
func TestOrchestrationSkillRetroHardenedRules(t *testing.T) {
	repoRoot := filepath.Clean("..")
	skillBytes, err := os.ReadFile(filepath.Join(repoRoot, "skills", "orchestrate", "SKILL.md"))
	if err != nil {
		t.Fatalf("read orchestration skill: %v", err)
	}
	skill := strings.Join(strings.Fields(string(skillBytes)), " ")

	rules := []string{
		// Silent send drop: never send into a mid-turn child unguarded, and
		// never treat a zero exit as arrival.
		"`--defer-if-busy` on every send to a working child, without exception",
		"--message-file \"$RUN_DIR/<slug>/fix-r<n>.md\" --defer-if-busy",
		"**A zero exit is not arrival.**",
		// Landing policy is asked, not defaulted.
		"Then settle the landing policy with the user, at triage, before a single branch is cut",
		"**Mechanism** — pull request, or direct merge into an integration branch?",
		"**Target** — *which* branch, by name, for each repo in the run?",
		"Do not infer either from the inspect child's summary and proceed",
		// No child touches a primary checkout.
		"**No child of this run works in a primary checkout",
		"sh \"$GUARD\" snapshot --repo <repo> --run-dir \"$RUN_DIR\" --label deploy-<repo>",
		"sh \"$GUARD\" verify --repo <repo> --run-dir \"$RUN_DIR\" --label deploy-<repo>",
		"Verify **before deleting the child**",
	}
	for _, rule := range rules {
		if !strings.Contains(skill, strings.Join(strings.Fields(rule), " ")) {
			t.Errorf("skill missing retro-hardened rule %q", rule)
		}
	}

	if !strings.Contains(skill, `cp <agent-deck-repo>/skills/orchestrate/references/primary-checkout-guard.sh "$RUN_DIR/"`) {
		t.Error("run setup no longer installs the primary-checkout guard")
	}

	guardPath := filepath.Join(repoRoot, "skills", "orchestrate", "references", "primary-checkout-guard.sh")
	info, err := os.Stat(guardPath)
	if err != nil {
		t.Fatalf("read primary-checkout guard: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("primary-checkout guard is not executable")
	}

	// The guard is only useful if it actually fails when the primary moves.
	repo := t.TempDir()
	runDir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", repo},
		{"-C", repo, "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	snapshot := exec.Command("sh", guardPath, "snapshot", "--repo", repo, "--run-dir", runDir, "--label", "deploy")
	if out, err := snapshot.CombinedOutput(); err != nil {
		t.Fatalf("guard snapshot: %v: %s", err, out)
	}
	verify := exec.Command("sh", guardPath, "verify", "--repo", repo, "--run-dir", runDir, "--label", "deploy")
	if out, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("guard verify on an untouched primary must pass: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "moved").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	moved := exec.Command("sh", guardPath, "verify", "--repo", repo, "--run-dir", runDir, "--label", "deploy")
	out, err := moved.CombinedOutput()
	if err == nil {
		t.Fatalf("guard verify must fail after the primary moved; got:\n%s", out)
	}
	if !strings.Contains(string(out), "PRIMARY MOVED: HEAD") {
		t.Errorf("guard must name what moved; got:\n%s", out)
	}
}

// The review round overlaps its test run with its layers, runs focused tests
// on incremental rounds, keeps the implementer alive for fix rounds, and
// records per-round timing (design 2026-09-02-review-round-overlap).
func TestOrchestrationReviewRoundOverlap(t *testing.T) {
	repoRoot := filepath.Clean("..")
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
	requireAll := func(t *testing.T, label, text string, rules []string) {
		t.Helper()
		for _, rule := range rules {
			if !strings.Contains(text, strings.Join(strings.Fields(rule), " ")) {
				t.Errorf("%s missing %q", label, rule)
			}
		}
	}

	full := render(t, "review-full",
		"AGENT_DECK_REPO=/tmp/agent-deck", "BASE_BRANCH=main", "BASELINE=baseline: none",
		"SPEC_BLOCK=Review requirements block", "VERDICT_FILE=/tmp/orchestrate/review-r1.md")
	requireAll(t, "review-full", full, []string{
		// D1: the suite starts first, detached, into the sibling log.
		"Start the full suite FIRST, detached",
		"/tmp/orchestrate/review-r1.md.suite.log",
		"SUITE_EXIT=",
		"Never run the suite twice in a round",
		// D2: layers are parallel subagents and the turn waits for them.
		"Dispatch every layer in ONE message",
		"Do not end your turn while any layer subagent is still running",
		// D5: timing evidence.
		"Checked: tests full cmd=",
		// Critic gates: the reviewer looks for itself and scores anchored criteria.
		"Own-eyes verification",
		"Seen: <criterion>",
		"Scored: <criterion> <n>/10 (threshold <t>)",
		"claims, not evidence",
	})

	incremental := render(t, "review-incremental",
		"AGENT_DECK_REPO=/tmp/agent-deck", "BASELINE=baseline: none",
		"FOCUSED_TESTS=go test ./internal/usage", "PREVIOUS_FINDINGS=One previous finding",
		"REVIEWED_SHA=0123456789abcdef", "SPEC_BLOCK=Review requirements block",
		"VERDICT_FILE=/tmp/orchestrate/review-r2.md")
	requireAll(t, "review-incremental", incremental, []string{
		// D3: focused tests replace the suite on incremental rounds.
		"go test ./internal/usage",
		"Do NOT run the full suite",
		"Dispatch every layer in ONE message",
		"Checked: tests focused cmd=",
	})
	if strings.Contains(incremental, "Run the test suite") {
		t.Error("review-incremental still asks for the full test suite")
	}

	skillBytes, err := os.ReadFile(filepath.Join(repoRoot, "skills", "orchestrate", "SKILL.md"))
	if err != nil {
		t.Fatalf("read orchestration skill: %v", err)
	}
	skill := strings.Join(strings.Fields(string(skillBytes)), " ")
	requireAll(t, "orchestrate skill", skill, []string{
		// D3: the contract carries a focused-test command for incremental rounds.
		"the focused-test command",
		"FOCUSED_TESTS=",
		// D4: fix rounds go to the live implementer; deletion is a deviation.
		"confirm `impl-<task-slug>` is still registered",
		"deviation: implementer deleted before task-done",
		// D5: per-round timing in the manifest.
		"launched=<unix> done=<unix> span=<s>",
		// Critic gates: blind A/B judge and its reveal are part of the UI end gate.
		"references/ab-pair.sh",
		"references/ab-reveal.sh",
		"AB_SUMMARY: pairs=<n> regressions=<n> unchanged=<n>",
		"reveal with `regressions=0`",
	})

	plan := render(t, "plan", "SPEC_PATH=/tmp/approved-design.md", "TASK_DIR=/tmp/orchestrate/task")
	requireAll(t, "plan", plan, []string{
		"## Quality bar",
		"anchors at 10, 8 and 5",
		"pass threshold",
	})
}
