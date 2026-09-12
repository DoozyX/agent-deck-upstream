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
			name: "review-round",
			args: []string{
				"AGENT_DECK_REPO=/tmp/agent-deck",
				"BASE_REF=main",
				"BASELINE=baseline: none",
				"FOCUSED_TESTS=go test ./internal/usage",
				"PREVIOUS_FINDINGS=One previous finding",
				"REVIEWED_SHA=0123456789abcdef",
				"SPEC_BLOCK=Review requirements block",
				"VERDICT_FILE=/tmp/orchestrate/review-r2.md",
			},
			required: []string{"One previous finding", "git diff 0123456789abcdef...HEAD", "git diff main...HEAD", "/tmp/orchestrate/review-r2.md"},
		},
		{
			name: "fix",
			args: []string{
				"FINDINGS=Fix this concrete finding",
				"FOCUSED_TESTS=go test ./internal/usage",
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
// in the foreground on later rounds, keeps the implementer alive for fix
// rounds, and records per-round timing (design 2026-09-02-review-round-overlap).
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

	round := render(t, "review-round",
		"AGENT_DECK_REPO=/tmp/agent-deck", "BASE_REF=main", "BASELINE=baseline: none",
		"FOCUSED_TESTS=go test ./internal/usage", "PREVIOUS_FINDINGS=One previous finding",
		"REVIEWED_SHA=0123456789abcdef", "SPEC_BLOCK=Review requirements block",
		"VERDICT_FILE=/tmp/orchestrate/review-r2.md")
	requireAll(t, "review-round", round, []string{
		// D3 (superseded by 2026-09-10): focused tests run in the foreground,
		// the full suite detached — a later round is full-branch and terminal.
		"go test ./internal/usage",
		"Start the full suite FIRST, detached",
		"Dispatch every layer in ONE message",
		"Checked: tests focused cmd=",
	})

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

// TestUsageAwareLaunchContract pins the usage-aware launch contract the
// orchestrate workflow gained once `agent-deck usage recommend` shipped. The
// decision is made by a Go function behind a CLI, so what the documents owe is
// the exact call, the exact place the answer is stored, the exact launch shape
// it produces, and the exact manifest line it is recorded on — each of which a
// later run reads back as a contract.
func TestUsageAwareLaunchContract(t *testing.T) {
	repoRoot := filepath.Clean("..")
	normalize := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	read := func(parts ...string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(append([]string{repoRoot}, parts...)...))
		if err != nil {
			t.Fatalf("read %v: %v", parts, err)
		}
		return string(b)
	}
	requireAll := func(label, text string, rules []string) {
		t.Helper()
		flat := normalize(text)
		for _, rule := range rules {
			if !strings.Contains(flat, normalize(rule)) {
				t.Errorf("%s missing %q", label, rule)
			}
		}
	}

	skill := read("skills", "orchestrate", "SKILL.md")
	requireAll("orchestrate skill", skill, []string{
		// One call per distinct role+tier per wave, saved under $RUN_DIR.
		"once per distinct role+tier",
		"$RUN_DIR/usage/<wave>-<role>-<tier>.json",
		// The re-run rule, including the null-fetched_at case the CLI really
		// emits when no snapshot was fetched.
		"`usage-limit` substate",
		"older than five minutes",
		// The launch shape the decision produces, and the empty-model rule.
		`agent-deck launch <worktree-path> -c <tool> \
  -t "impl-<task-slug>" \
  --extra-arg --model --extra-arg <model> \
  --message-file "$RUN_DIR/<task-slug>/impl-prompt.md"`,
		"Omit the `--extra-arg --model --extra-arg <model>` flag entirely when `model` is empty",
		// Exhausted pauses the wave and waits on the real reset time.
		"do not launch that wave",
		"earliest `resets_at` from `agent-deck usage --all --json`",
		// Explicit choices yield only on exhausted, and the override is recorded.
		"yield to the recommendation **only** when that provider's state is `exhausted`",
		// The one guarantee that survives unchanged.
		"never switches an account automatically",
		// The frontier tier: its baseline-table row, its ladder entries, and
		// the rule that it is never a baseline.
		"| Implementer of a plan task tagged `tier: frontier` | frontier |",
		"frontier is never a baseline",
		"`fable`",
		"`gpt-6-astra`",
		// Escalations now land on frontier, not on strong.
		"escalate the reviewer to strong → frontier",
		// The planner's tier vocabulary, restated in the skill. Criterion 9a
		// names two sites, so both are pinned: a revert of either one alone
		// leaves the skill contradicting the prompt it describes.
		"It tags every task `tier: mid | strong | frontier`",
		"the planner tags `mid`, `strong` or `frontier` for that reason — never below mid",
		// Criterion 8 requires BOTH escalation bullets to land on frontier;
		// pinning only the reviewer bullet let the implementer bullet revert.
		"as a NEW session in the same worktree, escalated strong → frontier",
		// Escalation is one-way to the tier actually reached, not to strong —
		// the bullets above end on frontier, so the rule introducing them
		// must not say the role "stays strong".
		"once a role escalates, it stays at the escalated tier",
		// Frontier's two entry points, without claiming every escalation
		// starts from strong (the reviewer's baseline is mid).
		"or by an escalation, which reaches it through strong",
		// The empty-`tool` decision is a decision, and its remedy is on
		// stderr only — the section's own `>` redirect does not capture it.
		"An empty `tool` is different",
		"the remedy reaches **stderr only**",
	})
	// Criterion 3: the manifest line is owed at three sites — the code block
	// and both cross-references. A whole-file Contains passed with BOTH
	// cross-references deleted, so count instead of testing presence.
	const manifestLine = "role=<role> tool=<tool> model=<model> tier=<applied> state=<state> reason=<one line>"
	if got := strings.Count(normalize(skill), manifestLine); got < 3 {
		t.Errorf("orchestrate skill records the manifest line at %d sites, want >= 3 (the code block plus both cross-references)", got)
	}
	// Criterion 6a: the two clauses this change falsifies must not survive.
	// A document asserting both the old rule and the new one is worse than one
	// asserting only the old one, so this is checked over the whole file.
	for _, dead := range []string{"never blocks a launch", "overrides an explicit tool"} {
		if strings.Contains(normalize(skill), dead) {
			t.Errorf("orchestrate skill still asserts the now-false clause %q", dead)
		}
	}

	requireAll("plan prompt", read("skills", "orchestrate", "references", "prompts", "plan.md"), []string{
		"`tier: mid | strong | frontier`",
		"There is no tier below mid",
	})

	requireAll("fleet skill", read("skills", "fleet", "SKILL.md"), []string{
		"agent-deck usage recommend",
		"skills/orchestrate/SKILL.md",
	})

	configReference := read("skills", "agent-deck", "references", "config-reference.md")
	requireAll("config reference", configReference, []string{
		"## [usage.policy] Section",
		"- [[usage.policy] Section](#usagepolicy-section)",
		"`exhausted_below`",
		"`constrained_below`",
		"`failover`",
		"[usage.policy.ladder.claude]",
		"[usage.policy.frontier_window]",
		// The bare tokens above are all satisfied by the TOML example block
		// and by cross-references, so each key-table ROW is anchored on text
		// unique to that row. These also pin the corrections this round made:
		// an exhausted provider is returned though never preferred; only the
		// two usage providers may name a ladder; an empty non-frontier rung
		// yields an empty model rather than falling back to strong; the
		// default failover order excludes a non-provider `default_tool`; an
		// empty failover list keeps the default; and a named-but-absent
		// frontier window does not gate.
		"| `exhausted_below` | integer 0–100 | `15` | Remaining percent below which a provider is `exhausted`.",
		"An exhausted provider is never *preferred*",
		"it is still returned when no candidate is eligible, so read `state` on every decision",
		"| `constrained_below` | integer 0–100 | `35` | Remaining percent below which a provider is `constrained`:",
		"| `failover` | array of strings | `[claude, codex]`, or `[codex, claude]` when `default_tool = \"codex\"` |",
		"A `default_tool` that is not itself a usage provider does not enter the order at all.",
		"An explicitly empty list is treated exactly like an omitted key and keeps the default order",
		"| `[usage.policy.ladder.<claude\\|codex>]` | table of strings |",
		"Only `claude` and `codex` are accepted; any other table name fails config validation with exit 1",
		"an explicitly empty `cheap`, `mid` or `strong` rung yields an empty `model` with the tier unchanged",
		"| `[usage.policy.frontier_window]` | table of strings | `{ claude = \"fable\" }` |",
		"a window that is named here but absent from the snapshot the provider actually returned",
		"`null` when the selected tool has no snapshot",
		"agent-deck usage recommend --role <role> --tier <cheap|mid|strong|frontier> [--prefer <tool>] [--profile <name>] [--json]",
		"exits 0 for every decision",
		"Exit 2 is reserved for a bad flag",
	})
	// The section is placed where its table-of-contents entry says it is.
	orchestrateSection := strings.Index(configReference, "\n## [orchestrate] Section")
	usagePolicySection := strings.Index(configReference, "\n## [usage.policy] Section")
	logsSection := strings.Index(configReference, "\n## [logs] Section")
	if orchestrateSection < 0 || usagePolicySection < 0 || logsSection < 0 {
		t.Fatalf("config reference sections: orchestrate=%d usage.policy=%d logs=%d", orchestrateSection, usagePolicySection, logsSection)
	}
	if !(orchestrateSection < usagePolicySection && usagePolicySection < logsSection) {
		t.Errorf("[usage.policy] must sit between [orchestrate] and [logs]: orchestrate=%d usage.policy=%d logs=%d", orchestrateSection, usagePolicySection, logsSection)
	}
	// Criterion 11: the TOC entry sits in the SAME position as the section.
	// Presence alone let the entry be moved anywhere in the list while the
	// section-body ordering above stayed green.
	orchestrateTOC := strings.Index(configReference, "- [[orchestrate] Section](#orchestrate-section)")
	usagePolicyTOC := strings.Index(configReference, "- [[usage.policy] Section](#usagepolicy-section)")
	logsTOC := strings.Index(configReference, "- [[logs] Section](#logs-section)")
	if orchestrateTOC < 0 || usagePolicyTOC < 0 || logsTOC < 0 {
		t.Fatalf("config reference TOC entries: orchestrate=%d usage.policy=%d logs=%d", orchestrateTOC, usagePolicyTOC, logsTOC)
	}
	if !(orchestrateTOC < usagePolicyTOC && usagePolicyTOC < logsTOC) {
		t.Errorf("the [usage.policy] TOC entry must sit between the [orchestrate] and [logs] entries, matching the section order: orchestrate=%d usage.policy=%d logs=%d", orchestrateTOC, usagePolicyTOC, logsTOC)
	}

	// Criterion 11a: the command row goes IMMEDIATELY after the `usage --all`
	// row. A plain Contains let it be moved anywhere in the table.
	agentDeckSkill := read("skills", "agent-deck", "SKILL.md")
	requireAll("agent-deck skill command table", agentDeckSkill, []string{
		"| `agent-deck usage recommend --role <role> --tier <tier>` |",
	})
	agentDeckLines := strings.Split(agentDeckSkill, "\n")
	usageAllRow, recommendRow := -1, -1
	for i, line := range agentDeckLines {
		switch {
		case strings.HasPrefix(line, "| `agent-deck usage --all [--json]` |"):
			usageAllRow = i
		case strings.HasPrefix(line, "| `agent-deck usage recommend --role <role> --tier <tier>` |"):
			recommendRow = i
		}
	}
	if usageAllRow < 0 || recommendRow < 0 {
		t.Fatalf("agent-deck skill command table rows: usage --all=%d usage recommend=%d", usageAllRow, recommendRow)
	}
	if recommendRow != usageAllRow+1 {
		t.Errorf("the `usage recommend` row must sit immediately after the `usage --all` row: usage --all on line %d, usage recommend on line %d", usageAllRow+1, recommendRow+1)
	}
}
