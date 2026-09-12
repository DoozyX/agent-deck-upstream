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
		// The pin is the WHOLE clause, not just the "once per distinct
		// role+tier" fragment: that fragment alone left criterion 1's timing
		// half unasserted, and a mutant rewriting the trigger to "Whenever it
		// seems useful" — destroying both "Before each launch wave" and "in
		// that wave" — stayed green.
		"Before each launch wave, run `agent-deck usage recommend` **once per distinct role+tier** in that wave and save its JSON under `$RUN_DIR/usage/`:",
		"$RUN_DIR/usage/<wave>-<role>-<tier>.json",
		// The re-run rule, including the null-fetched_at case the CLI really
		// emits: the SELECTED tool has no available snapshot, which happens even
		// when snapshots were fetched for other candidates.
		// Round-4 finding 4: the two pins below are both true of "Never reuse a
		// saved decision; make a fresh call before every single launch", which
		// is the exact OPPOSITE of criterion 1's re-run rule and stayed green.
		// The opener carries the rule; pin it.
		"**Re-run rule.** Reuse the saved decision for the rest of its wave.",
		"`usage-limit` substate",
		"older than five minutes",
		"the command emits whenever the *selected* tool has no available snapshot, which happens even when snapshots were fetched for other candidates",
		// Criterion 12: the CLI surface must match in the orchestrate skill too,
		// not only in the config reference — this is the copy a conductor runs.
		// The flag string and the exit-code sentence are pinned verbatim; a
		// `--tier <tier>` collapse or an "exits 0 normally and 2 on error"
		// paraphrase both used to survive here.
		`agent-deck usage recommend --role <role> --tier <cheap|mid|strong|frontier> --json \
  > "$RUN_DIR/usage/<wave>-<role>-<tier>.json"`,
		"It exits 0 for every decision — including `state: exhausted` and `state: unknown` — and exit 2 is reserved for a bad flag (a missing `--role`, an unknown `--tier`, an unknown `--prefer` tool, a stray positional).",
		// Exit 1 was enumerated nowhere in the skill, though this section's own
		// recipe redirects stdout into a file the shell creates either way — a
		// conductor with no reason to check the code saves a zero-byte "decision".
		"Exit 1 means the configuration could not be loaded or validated — check the exit code before reading the saved file, because the `>` redirect creates that file even when nothing was written to it.",
		// Criterion 4 and criterion 5, second halves: the exhausted pause owes a
		// recorded, reported decision and the yield owes a recorded override.
		// Both were deletable without turning this test red.
		"record the decision, report it through the run's existing path",
		"Record the override on that launch's manifest line.",
		// Criterion 5's input: the yield rule scores a non-selected provider by
		// re-running with --prefer. Where that provider's state then LANDS is
		// strategy-dependent, and the round-2 wording ("makes it the selected
		// tool ... do not look for it in `alternatives[]`") was false under
		// `tool_strategy = "auto"`, where --prefer only ORDERS the candidates
		// and a healthier tool still wins. Both halves are pinned, so neither
		// can revert to the unqualified claim.
		"Score that provider by re-running the call with `--prefer <that provider>`, then read its state from wherever the strategy in force puts it.",
		"Under the default `tool_strategy` that provider is the only candidate, so the top-level `state` is its own and `alternatives[]` is empty.",
		// Round-4 finding 1: the round-3 wording named ONE "auto" sub-case and
		// then generalised from it. Driven, `--prefer codex` under "auto" with
		// both providers at 5% selects codex itself — top-level `state` is its
		// own and it is NOT in `alternatives[]` — and with codex hidden via
		// `[ui] hidden_tools` the decision is claude/healthy with `alternatives`
		// EMPTY, so codex appears in NEITHER place. `alternatives` is built by
		// skipping the selected tool over the EVALUATED candidates only
		// (internal/usage/recommend.go:569-572), so a provider filtered out
		// before evaluation has no entry. Both halves of the three-way rule are
		// pinned, and so is the membership rule that makes the third case
		// readable: neither can revert to the two-way claim. The two triggers named
		// are the two that were DRIVEN here (hidden via `[ui] hidden_tools`, and a
		// miscased failover entry, both yielding `alternatives: []`); the
		// not-installed path could not be driven on this machine, so the clause
		// gives examples rather than closing the set.
		"Under `tool_strategy = \"auto\"` `--prefer` only puts it FIRST among the candidates: a healthier tool can still be selected, so that provider's state lands in one of three places — the top-level `state` when `tool` names it, its entry in `alternatives[]` when a different tool was selected, and neither when it was never a candidate at all.",
		"`alternatives[]` lists the non-selected candidates, so a provider filtered out before it was ever scored — hidden by `[ui] hidden_tools`, say, or dropped from the failover order by a miscased entry — is absent from both, and that absence is the only signal you get.",
		// Finding 8: the probe is a SECOND recommend call for the same
		// role+tier, so it must not overwrite the wave's one saved decision.
		// Round-4 finding 7: the trailing clause was deletable while green.
		// Strict superset of the string it replaces.
		"The probe is diagnostic: do not save it over the wave's `$RUN_DIR/usage/<wave>-<role>-<tier>.json`, which the re-run rule reuses for the rest of the wave.",
		// Finding 9: the manifest format criterion 3 fixes has no override
		// field, so the override has to name where it actually goes.
		"The format above has no override field, so it goes in `reason=`.",
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
		// Criterion 7 requires these two to be the FRONTIER entries. The bare
		// tokens "`fable`" and "`gpt-6-astra`" pinned presence, not rung:
		// moving `fable` to the cheap rung, and swapping the codex frontier
		// rung for `gpt-5.6-nova` while leaving the token in prose, both
		// stayed green. Each string below contains the bare token it replaces.
		"Claude: `haiku` / `sonnet` / `opus` / `fable`",
		"`gpt-5.6-luna` / `gpt-5.6-terra` / `gpt-5.6-sol` / `gpt-6-astra`",
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
		// Round-4 finding 7: the two pins above hold the surrounding sentences
		// but not the remedy itself, which survived replacement by "Launch
		// without `-c` and let the connector default apply."
		"Re-run the call with `--prefer <tool>`, or set `default_tool` in `config.toml`, before launching anything for that wave.",
	})
	// Criterion 3: the manifest line is owed at three sites — the code block
	// and both cross-references. A whole-file Contains passed with BOTH
	// cross-references deleted, so count instead of testing presence.
	const manifestLine = "role=<role> tool=<tool> model=<model> tier=<applied> state=<state> reason=<one line>"
	if got := strings.Count(normalize(skill), manifestLine); got < 3 {
		t.Errorf("orchestrate skill records the manifest line at %d sites, want >= 3 (the code block plus both cross-references)", got)
	}
	// The count alone fixes the QUANTITY of citations, not where they are:
	// a mutant that replaced the "Then record per task:" citation with
	// "(see the launch line above)" and added one inside the usage section
	// kept the total at exactly 3 and stayed green. Criterion 3 names both
	// cross-reference sites, so each is anchored to its own heading line.
	skillLines := strings.Split(skill, "\n")
	citedWithin := func(anchor string, window int) bool {
		for i, line := range skillLines {
			if !strings.Contains(line, anchor) {
				continue
			}
			for j := i; j < len(skillLines) && j <= i+window; j++ {
				if strings.Contains(normalize(skillLines[j]), manifestLine) {
					return true
				}
			}
		}
		return false
	}
	for _, site := range []struct{ anchor string }{
		{"Then record per task:"},
		{"Record every session's connector + model in the manifest"},
	} {
		if !citedWithin(site.anchor, 6) {
			t.Errorf("criterion 3's manifest line must be cited within 6 lines of %q; it is not", site.anchor)
		}
	}
	// Round-4 finding 5: the third per-launch instruction carried the
	// pre-existing THREE-field form while the code block and both
	// cross-references mandate the six-field one, so the document gave two
	// formats for one artifact and the short one omitted `model=`, `tier=` and
	// `state=` — the field the exhausted pause and the final report both read.
	// The count above uses the six-field string, so this site contributes
	// nothing to it and a pointer added here would otherwise be
	// revertible-green. Pin the pointer itself.
	requireAll("orchestrate skill third manifest site", skill, []string{
		"append the full manifest line defined under \"Record every launch\" above — `role=`, `tool=`, `model=`, `tier=`, `state=` and `reason=`, all six fields — to `$RUN_DIR/manifest.md`",
	})
	// ...and the short form it replaced must not come back alongside it.
	if strings.Contains(normalize(skill), "`role=<role> tool=<tool> reason=<one line>`") {
		t.Errorf("orchestrate skill still gives the three-field manifest form; the six-field form at \"Record every launch\" is the only one")
	}

	// Criterion 6a: the two clauses this change falsifies must not survive.
	// A document asserting both the old rule and the new one is worse than one
	// asserting only the old one, so this is checked over the whole file.
	//
	// fold is deliberately LOCAL to this negative loop rather than folded into
	// normalize: normalize backs every positive pin above, and making those
	// case-insensitive and emphasis-blind would weaken ~60 assertions to
	// strengthen one. The loop needs it because the criterion's whole content
	// is a removal, and the bare Contains was case-sensitive and markup-blind
	// — "Never blocks a launch.", "never **blocks a launch**" and "Overrides
	// an explicit tool choice: never." all survived it.
	// Backticks are stripped for the same reason as `*` and `_`: this document
	// backticks `tool` everywhere, so "It overrides an explicit `tool` choice."
	// and "It never `blocks a launch`." both revert the dead clauses while
	// reading as ordinary prose, and both survived the emphasis-only fold.
	fold := func(s string) string {
		return strings.NewReplacer("*", "", "_", "", "`", "").Replace(strings.ToLower(normalize(s)))
	}
	for _, dead := range []string{"never blocks a launch", "overrides an explicit tool"} {
		if strings.Contains(fold(skill), fold(dead)) {
			t.Errorf("orchestrate skill still asserts the now-false clause %q", dead)
		}
	}

	requireAll("plan prompt", read("skills", "orchestrate", "references", "prompts", "plan.md"), []string{
		"`tier: mid | strong | frontier`",
		"There is no tier below mid",
		// Criterion 9's SECOND clause: the one-line frontier definition and the
		// rule that it is never a baseline. The vocabulary and the floor sentence
		// above both stayed green with the definition deleted.
		"frontier only for a task no strong session should be asked to carry alone",
		"Frontier is never a baseline: tag it deliberately or not at all.",
	})

	// Criterion 10's pointer, pinned as a SENTENCE. As two independent bare
	// tokens ("agent-deck usage recommend", "skills/orchestrate/SKILL.md")
	// nothing forced them into a pointer at all: a mutant reading "**Not
	// fleet's business.** `agent-deck usage recommend` has nothing to do with
	// fleet children; see `skills/orchestrate/SKILL.md` for teardown ordering."
	// asserted the OPPOSITE of the criterion and stayed green. The string
	// below contains both retired tokens.
	requireAll("fleet skill", read("skills", "fleet", "SKILL.md"), []string{
		"`agent-deck usage recommend --role <role> --tier <tier>` returns a read-only, advisory tool/model choice from live quota; `skills/orchestrate/SKILL.md` carries the full contract for using it.",
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
		// Round-4 finding 7: round 3 added this block to the TOML example
		// because round-2 finding 2 asked for it, and criterion 11 owes "a TOML
		// example matching the design's block" — yet the block survived
		// deletion. Pinned with its trailing blank line so it also holds its
		// position immediately ahead of `[usage.policy]`.
		"[orchestrate]\ntool_strategy = \"auto\"\n\n[usage.policy]",
		"An explicitly empty list is treated exactly like an omitted key and keeps the default order",
		// The failover row was entirely rewritten and gained ZERO pins: the
		// three above are context lines that rewrite never touched, so the
		// "auto"-only scoping, the silent-drop behaviour and both verbatim
		// reason strings could all be reverted while this test stayed green.
		// Each corrected claim is now held, and each was driven against a
		// built binary rather than read off the source.
		// Round-4 finding 3: three clauses written in cd175e91 carried no pin
		// and were invertible while green. All three are true today, each
		// driven against a built binary. The first string below is a strict
		// superset of the one it replaces — it adds the "no other tool it could
		// change to" clause; the two after it are new.
		"The ORDER is consulted **only** under `[orchestrate] tool_strategy = \"auto\"`; under the default strategy the list still decides whether an unknown-state tool counts as eligible, which shows up in `reason` but never changes the selected tool — that strategy has at most one candidate to choose from, so there is no other tool it could change to.",
		"Either way cross-provider failover does not happen there: no second provider is queried and `alternatives` comes back empty.",
		"`--prefer` is checked against the tool registry rather than the installed set, so a registry name that is not installed passes the flag check and is then dropped here like any other entry.",
		"there is at most one candidate, the `--prefer` tool when one was passed and otherwise `default_tool`, and none at all when neither is set: the decision then carries an empty `tool` and the reason `no candidate tools for tool strategy \"\"`",
		"Under `\"auto\"` the candidate order is the `--prefer` tool first when one was passed, followed by the failover entries in their configured order",
		"a misspelled or miscased entry is silently dropped — never queried, absent from `alternatives`, with nothing in the decision to reveal that the configured order changed",
		"the decision then carries the `--prefer` tool, or the first entry when `--prefer` was omitted, as its `tool`, with `state: unknown` and the reason `no candidate tools for tool strategy \"auto\"`",
		// Driven discriminator: an uninstalled name that IS a usage provider
		// keeps a non-empty `provider` and a model here, so the round-2 "with an
		// empty `provider`, no model" clause was false and is not re-pinned.
		"`provider` and `model` come back filled in when that surviving name maps to a usage provider and empty when it does not, even though nothing was queried either way",
		"| `[usage.policy.ladder.<claude\\|codex>]` | table of strings |",
		"Only `claude` and `codex` are accepted, and the rejection is not a startup error",
		"`usage recommend` is the one thing that rejects it, printing `invalid [usage.policy].ladder.<name>: unknown usage provider` and exiting 1",
		"an explicitly empty `cheap`, `mid` or `strong` rung yields an empty `model` with the tier unchanged",
		"| `[usage.policy.frontier_window]` | table of strings | `{ claude = \"fable\" }` |",
		"a window that is named here but absent from the snapshot the provider actually returned",
		"`null` whenever the selected tool has no available snapshot",
		// The round-2 wording closed an enumeration that is not closed and
		// attached a guarantee a third case falsifies: `--profile` naming a
		// label with no snapshot is queried, succeeds, and still yields
		// `fetched_at: null` with an EMPTY `account`. The sibling pin above
		// ("`null` whenever the selected tool has no available snapshot") is
		// true and is kept.
		"because it was never queried at all, because its own query failed, or because `--profile` named no snapshot for it; among those, only the failed-query case still reports a non-empty `account` alongside the `null`",
		"agent-deck usage recommend --role <role> --tier <cheap|mid|strong|frontier> [--prefer <tool>] [--profile <name>] [--json]",
		// Criterion 12: all ten JSON keys, named in order. Nine of them had no
		// assertion at all — only the `fetched_at` sub-clause was anchored.
		"`--json` prints the decision as ten snake_case keys: `tool`, `provider`, `model`, `tier_requested`, `tier_applied`, `account`, `state`, `reason`, `alternatives` (each entry `tool` / `state` / `remaining_percent`), and `fetched_at`",
		"exits 0 for every decision",
		"Exit 2 is reserved for a bad flag",
		// ...and the whole exit contract as one sentence. The two fragments
		// above are kept, but neither held the exit-1 clause, so the config
		// reference could drop it while the skill side stayed pinned.
		"It exits 0 for every decision — including `exhausted`, `unknown`, and the case where no candidate tool could be chosen at all (the decision then carries an empty `tool`, and a remedy hint goes to stderr) — so a caller reads `state`, not the exit code. Exit 2 is reserved for a bad flag: a missing `--role`, an unknown `--tier`, an unknown `--prefer` tool, or a stray positional argument. Exit 1 means the configuration could not be loaded or validated, or the JSON could not be encoded.",
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
		// The whole row, not just its command cell: criterion 11a owes the
		// read-only, advisory purpose too, and dropping those two words left
		// both the command-cell pin and the adjacency check below green.
		"| `agent-deck usage recommend --role <role> --tier <tier>` | Read-only, advisory connector + model pick for that role and tier from live quota; add `--json` for the decision object |",
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
