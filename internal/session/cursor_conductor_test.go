package session

import "testing"

func TestCursorConductorSpec(t *testing.T) {
	spec, err := GetConductorAgentSpec("cursor")
	if err != nil {
		t.Fatal(err)
	}
	if spec.DefaultCommand != "agent" || spec.InstructionsFileName != "AGENTS.md" {
		t.Fatalf("Cursor conductor spec = %#v", spec)
	}
}
