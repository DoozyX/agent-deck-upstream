package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestAddJSONExplicitlyReportsRegistrationOnly keeps automation from treating
// successful registration as a successful launch. add deliberately does not
// create a tmux pane unless --attach is requested.
func TestAddJSONExplicitlyReportsRegistrationOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"add", "--title", "registration-only", "--cmd", "shell", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add --json exit = %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var response struct {
		Success bool   `json:"success"`
		Started *bool  `json:"started"`
		ID      string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("parse add JSON: %v\nstdout: %s", err, stdout)
	}
	if !response.Success || response.ID == "" {
		t.Fatalf("add result = %#v, want successful registration with an id", response)
	}
	if response.Started == nil || *response.Started {
		t.Fatalf("add JSON started = %v, want explicit false; stdout: %s", response.Started, stdout)
	}
}
