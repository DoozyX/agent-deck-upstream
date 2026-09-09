package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeCommandCarriesTheDeckTitleAsItsName(t *testing.T) {
	// #2075: the deck title is the single startup name, and it is the address
	// ClaudeAddressName hands out. The derived peer slug is no longer launched
	// with, so it must not appear on the command line.
	startupNameConfig(t, "")
	inst := &Instance{
		ID:    "a1b2c3d4-1111-2222-3333-444455556666",
		Title: "Payments Review!",
		Tool:  "claude",
	}
	cmd := inst.buildClaudeCommand("claude")
	if !strings.Contains(cmd, "--name 'Payments Review!'") {
		t.Fatalf("Claude command does not carry the deck title as its name: %s", cmd)
	}
	if strings.Contains(cmd, "payments-review-a1b2c3d4") {
		t.Fatalf("Claude command still carries the retired peer address: %s", cmd)
	}
	if got := inst.ClaudeAddressName(); got != "Payments Review!" {
		t.Fatalf("ClaudeAddressName() = %q, want the name the command registers", got)
	}
}

func TestClaudeCommandPreservesExplicitPeerName(t *testing.T) {
	for _, extra := range [][]string{{"--name", "operator-choice"}, {"--name=operator-choice"}} {
		inst := &Instance{
			ID:        "a1b2c3d4-1111-2222-3333-444455556666",
			Title:     "Payments Review!",
			Tool:      "claude",
			ExtraArgs: extra,
		}
		cmd := inst.buildClaudeCommand("claude")
		if strings.Contains(cmd, "--name payments-review-a1b2c3d4") {
			t.Fatalf("generated peer name overrode explicit args %v: %s", extra, cmd)
		}
		if !strings.Contains(cmd, "operator-choice") {
			t.Fatalf("explicit peer name missing for args %v: %s", extra, cmd)
		}
	}
}

func TestClaudeCommandPreservesExplicitPeerNameFromWrapper(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{
		ID:          "a1b2c3d4-1111-2222-3333-444455556666",
		Title:       "Payments Review!",
		Tool:        "claude",
		Wrapper:     "{command} --name operator-choice",
		ProjectPath: project,
	}
	base := inst.buildClaudeCommand("claude")
	if strings.Contains(base, "--name payments-review-a1b2c3d4") {
		t.Fatalf("generated peer name conflicts with wrapper's explicit name: %s", base)
	}
	prepared, _, err := inst.prepareCommand(base)
	if err != nil {
		t.Fatalf("prepare command: %v", err)
	}
	if !strings.Contains(prepared, "--name operator-choice") {
		t.Fatalf("prepared command lost wrapper's explicit peer name: %s", prepared)
	}
}

func TestClaudePeerNameIsStableSanitizedAndCollisionResistant(t *testing.T) {
	a := &Instance{ID: "a1b2c3d4-1111-2222-3333-444455556666", Title: "Päyments Review!"}
	b := &Instance{ID: "ffeeddcc-1111-2222-3333-444455556666", Title: "Päyments Review!"}

	if got, want := a.ClaudePeerName(), "p-yments-review-a1b2c3d4"; got != want {
		t.Fatalf("ClaudePeerName() = %q, want ASCII-safe %q", got, want)
	}
	if a.ClaudePeerName() == b.ClaudePeerName() {
		t.Fatalf("duplicate titles produced the same peer name %q", a.ClaudePeerName())
	}
}

func TestClaudePeerNameHasNeutralFallbackAndBoundedLength(t *testing.T) {
	inst := &Instance{
		ID:    "12345678-1111-2222-3333-444455556666",
		Title: "!!! " + strings.Repeat("Very Long Title ", 20),
	}
	got := inst.ClaudePeerName()
	if len(got) > 64 {
		t.Fatalf("peer name length = %d, want <= 64: %q", len(got), got)
	}

	empty := (&Instance{ID: inst.ID, Title: "!!!"}).ClaudePeerName()
	if empty != "session-12345678" {
		t.Fatalf("punctuation-only title produced %q, want neutral fallback", empty)
	}
}

func TestPeerMessagingCandidateIsClaudeOnly(t *testing.T) {
	if !(&Instance{Tool: "claude"}).PeerMessagingCandidate() {
		t.Fatal("managed Claude session must be a peer-messaging candidate")
	}
	if (&Instance{Tool: "codex"}).PeerMessagingCandidate() {
		t.Fatal("Codex session must not be a Claude peer-messaging candidate")
	}
}

func TestClaudeForkCommandUsesTargetName(t *testing.T) {
	startupNameConfig(t, "")
	parent := &Instance{
		ID:               "aaaaaaaa-1111-2222-3333-444455556666",
		Title:            "Parent",
		Tool:             "claude",
		ProjectPath:      t.TempDir(),
		ClaudeSessionID:  "11111111-2222-3333-4444-555555555555",
		ClaudeDetectedAt: time.Now(),
	}
	target := &Instance{
		ID:          "bbbbbbbb-1111-2222-3333-444455556666",
		Title:       "Review Child",
		Tool:        "claude",
		ProjectPath: t.TempDir(),
	}
	cmd, err := parent.buildClaudeForkCommandForTarget(target, &ClaudeOptions{})
	if err != nil {
		t.Fatalf("build fork command: %v", err)
	}
	if !strings.Contains(cmd, "--name 'Review Child'") {
		t.Fatalf("fork command lacks the target's name: %s", cmd)
	}
	if strings.Contains(cmd, "--name Parent") {
		t.Fatalf("fork command incorrectly uses the parent's name: %s", cmd)
	}
}

func TestClaudeForkDoesNotInheritParentsExplicitName(t *testing.T) {
	startupNameConfig(t, "")
	parent := &Instance{
		ID:               "aaaaaaaa-1111-2222-3333-444455556666",
		Title:            "Parent",
		Tool:             "claude",
		ProjectPath:      t.TempDir(),
		ClaudeSessionID:  "11111111-2222-3333-4444-555555555555",
		ClaudeDetectedAt: time.Now(),
		ExtraArgs:        []string{"--name", "operator-parent", "--agent", "reviewer"},
	}
	forked, cmd, err := parent.CreateForkedInstanceWithOptions("Review Child", "", &ClaudeOptions{})
	if err != nil {
		t.Fatalf("create fork: %v", err)
	}
	if strings.Contains(cmd, "operator-parent") {
		t.Fatalf("fork inherited the parent's explicit name: %s", cmd)
	}
	if !strings.Contains(cmd, "--name 'Review Child'") {
		t.Fatalf("fork lacks its own name: %s", cmd)
	}
	if strings.Join(forked.ExtraArgs, " ") != "--agent reviewer" {
		t.Fatalf("fork extra args = %v, want non-name args preserved", forked.ExtraArgs)
	}
}
