package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The teardown gate is the run's last evidence step, so its position in the
// skill is load-bearing: after cleanup (there is nothing to prove before the
// deletions run) and before the final report (a report shipped over residue is
// the failure this gate exists to prevent).
func TestOrchestrationSkillTeardownGateOrdering(t *testing.T) {
	repoRoot := filepath.Clean("..")
	skillPath := filepath.Join(repoRoot, "skills", "orchestrate", "SKILL.md")
	skillBytes, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read orchestration skill: %v", err)
	}
	core := string(skillBytes)
	cleanupAndGate := readLinkedSkillDoc(t, skillPath, "failure, cleanup, and stopping")
	reporting := readLinkedSkillDoc(t, skillPath, "reporting")
	startup := readLinkedSkillDoc(t, skillPath, "delivery startup")

	cleanup := strings.Index(cleanupAndGate, "## Cleanup (successful tasks only)")
	gate := strings.Index(cleanupAndGate, "## Teardown gate")
	report := strings.Index(reporting, "## Final report")
	if cleanup < 0 || gate < 0 || report < 0 {
		t.Fatalf("missing sections: cleanup=%d gate=%d report=%d", cleanup, gate, report)
	}
	if cleanup >= gate {
		t.Fatalf("teardown gate must follow cleanup: cleanup=%d gate=%d", cleanup, gate)
	}
	normalizedCore := strings.Join(strings.Fields(core), " ")
	orderContract := "Before reporting, follow [failure, cleanup, and stopping](references/failure-cleanup-and-stopping.md), run the read-only teardown gate, then use [reporting](references/reporting.md)."
	if !strings.Contains(normalizedCore, orderContract) {
		t.Fatal("core must route cleanup and teardown before reporting")
	}

	section := cleanupAndGate[gate:]
	for _, required := range []string{
		`bash "$RUN_DIR/teardown-gate.sh" --repo`, // the conductor runs it from the run dir
		".agent-deck/tmp/<session-id>",            // the leak no other collector sees
		".needs-attention",                        // parked runs must not fail the gate
		"VERDICT: clean",                          // the only verdict that ships a report
	} {
		if !strings.Contains(section, required) {
			t.Errorf("teardown gate section is missing %q", required)
		}
	}
	if strings.Contains(section, "rm -rf") || strings.Contains(section, "worktree remove") {
		t.Error("the teardown gate reports residue; deletion stays with the cleanup child")
	}

	// A script the run directory never receives cannot be run from it.
	if !strings.Contains(startup, `references/teardown-gate.sh "$RUN_DIR/"`) {
		t.Error("run setup must copy teardown-gate.sh into the run directory")
	}

	gatePath := filepath.Join(repoRoot, "skills", "orchestrate", "references", "teardown-gate.sh")
	info, err := os.Stat(gatePath)
	if err != nil {
		t.Fatalf("stat teardown-gate.sh: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("teardown-gate.sh must be executable")
	}
}
