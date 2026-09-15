package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileBytes(t *testing.T, paths ...string) int64 {
	t.Helper()
	var total int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat routed skill document %s: %v", path, err)
		}
		total += info.Size()
	}
	return total
}

func TestCoreSkillByteCeilingAndRepresentativeRouteBudgets(t *testing.T) {
	repoRoot := filepath.Clean("..")
	agentCore := filepath.Join(repoRoot, "skills", "agent-deck", "SKILL.md")
	orchestrateCore := filepath.Join(repoRoot, "skills", "orchestrate", "SKILL.md")

	combinedCore := fileBytes(t, agentCore, orchestrateCore)
	t.Logf("combined core bytes: %d", combinedCore)
	if combinedCore > 72_935 {
		t.Fatalf("combined Agent Deck and Orchestrate cores = %d bytes, want <= 72935", combinedCore)
	}

	ordinarySessionRoute := fileBytes(t,
		agentCore,
		filepath.Join(repoRoot, "skills", "agent-deck", "references", "session-operations.md"),
	)
	t.Logf("ordinary session route bytes: %d", ordinarySessionRoute)
	if ordinarySessionRoute > 30_000 {
		t.Fatalf("ordinary session route = %d bytes, want <= 30000", ordinarySessionRoute)
	}

	deliveryStartupRoute := fileBytes(t,
		filepath.Join(repoRoot, "skills", "fleet", "SKILL.md"),
		orchestrateCore,
		filepath.Join(repoRoot, "skills", "orchestrate", "references", "delivery-startup.md"),
	)
	t.Logf("delivery startup route bytes: %d", deliveryStartupRoute)
	if deliveryStartupRoute > 50_000 {
		t.Fatalf("delivery startup route = %d bytes, want <= 50000", deliveryStartupRoute)
	}

	for label, contract := range map[string]struct {
		path      string
		stageOnly string
	}{
		"agent-deck": {
			path:      agentCore,
			stageOnly: "Load only the reference for the current operation",
		},
		"orchestrate": {
			path:      orchestrateCore,
			stageOnly: "Load only the row for the current stage",
		},
	} {
		core, err := os.ReadFile(contract.path)
		if err != nil {
			t.Fatalf("read %s core: %v", label, err)
		}
		if !strings.Contains(string(core), contract.stageOnly) {
			t.Errorf("%s core lost stage-only loading rule %q", label, contract.stageOnly)
		}
		if strings.Contains(string(core), "{{include:") {
			t.Errorf("%s core eagerly includes a routed reference", label)
		}
	}
}

func TestCoreSkillRoutingAndCompatibilityContracts(t *testing.T) {
	repoRoot := filepath.Clean("..")
	agentCoreBytes, err := os.ReadFile(filepath.Join(repoRoot, "skills", "agent-deck", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	orchestrateCoreBytes, err := os.ReadFile(filepath.Join(repoRoot, "skills", "orchestrate", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	planningBytes, err := os.ReadFile(filepath.Join(repoRoot, "skills", "orchestrate", "references", "planning-and-role-selection.md"))
	if err != nil {
		t.Fatal(err)
	}

	agentCore := string(agentCoreBytes)
	orchestrateCore := string(orchestrateCoreBytes)
	planning := string(planningBytes)
	normalizedPlanning := strings.Join(strings.Fields(planning), " ")
	for label, document := range map[string]string{
		"agent-deck core":  agentCore,
		"orchestrate core": orchestrateCore,
		"planning route":   planning,
	} {
		if !strings.Contains(document, "agent-deck launch --orchestrate-role") {
			t.Errorf("%s does not use the supported --orchestrate-role entrypoint", label)
		}
		if strings.Contains(document, "agent-deck launch --role") {
			t.Errorf("%s still advertises unsupported agent-deck launch --role", label)
		}
	}
	selfImprovementBytes, err := os.ReadFile(filepath.Join(repoRoot, "skills", "agent-deck", "references", "self-improvement.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(selfImprovementBytes), "(session-operations.md#script-path-resolution-important)") {
		t.Error("self-improvement route does not target the exact script-path-resolution anchor")
	}
	if strings.Contains(orchestrateCore, "<deterministic|routing|routine|architecture>") {
		t.Error("orchestrate core still advertises deterministic as a CLI role")
	}
	if !strings.Contains(orchestrateCore, "Deterministic checks run directly through shell/process execution") {
		t.Error("orchestrate core does not route deterministic checks to process execution")
	}
	if !strings.Contains(normalizedPlanning, "<routing|routine|architecture>") {
		t.Error("planning route does not assert the complete supported orchestrate role set")
	}
	if strings.Contains(normalizedPlanning, "<deterministic|routing|routine|architecture>") {
		t.Error("planning route advertises deterministic as a supported model role")
	}
	if !strings.Contains(normalizedPlanning, "Run deterministic checks directly through shell/process execution") {
		t.Error("planning route does not distinguish process-only deterministic checks")
	}
	if !strings.Contains(agentCore, "metadata:\n  compatibility: \"claude, codex, cursor\"") {
		t.Error("agent-deck core lost compatibility metadata")
	}
}
