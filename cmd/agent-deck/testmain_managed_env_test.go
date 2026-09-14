package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestTestMainClearsInheritedManagedSessionIdentity(t *testing.T) {
	if os.Getenv("AGENT_DECK_TEST_MANAGED_ENV_HELPER") == "1" {
		TestHandleAddUsesGlobalDefaultPath(t)
		return
	}

	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	cmd := exec.Command(testBinary, "-test.run=^TestTestMainClearsInheritedManagedSessionIdentity$")
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_TEST_MANAGED_ENV_HELPER=1",
		"AGENT_DECK_SESSION_ID=stale-managed-session",
		"AGENTDECK_INSTANCE_ID=stale-managed-instance",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated helper inherited managed-session identity: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "automatic parent") {
		t.Fatalf("helper resolved inherited managed-session identity:\n%s", out)
	}
}
