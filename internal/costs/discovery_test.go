package costs_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func writeFixtureFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverTranscriptSourcesCanonicalHomesAttributionAndNativeChildren(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "claude-real")
	claudeAlias := filepath.Join(root, "claude-alias")
	codexHome := filepath.Join(root, "codex-account")
	managedClaude := filepath.Join(claudeHome, "projects", "project", "managed-claude.jsonl")
	removedClaude := filepath.Join(claudeHome, "projects", "project", "removed-claude.jsonl")
	nativeChild := filepath.Join(claudeHome, "projects", "project", "managed-claude", "subagents", "agent-review.jsonl")
	managedCodex := filepath.Join(codexHome, "sessions", "2026", "09", "01", "rollout-2026-09-01T12-00-00-11111111-1111-1111-1111-111111111111.jsonl")
	for _, path := range []string{managedClaude, removedClaude, nativeChild, managedCodex} {
		writeFixtureFile(t, path)
	}
	if err := os.Symlink(claudeHome, claudeAlias); err != nil {
		t.Fatal(err)
	}

	sources, warnings, err := costs.DiscoverTranscriptSources(costs.DiscoveryConfig{
		Homes: []costs.ProviderHome{
			{Provider: costs.ProviderClaude, Path: claudeHome, Account: "work"},
			{Provider: costs.ProviderClaude, Path: claudeAlias, Account: "alias"},
			{Provider: costs.ProviderCodex, Path: codexHome, Account: "codex-work"},
		},
		Attributions: []costs.TranscriptAttribution{
			{Provider: costs.ProviderClaude, Home: claudeAlias, NativeSessionID: "managed-claude", SessionID: "deck-archived", ParentSessionID: "conductor", RunID: "run-1", Archived: true},
			{Provider: costs.ProviderCodex, Home: codexHome, NativeSessionID: "11111111-1111-1111-1111-111111111111", SessionID: "deck-codex"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(sources) != 4 {
		t.Fatalf("sources=%d warnings=%v", len(sources), warnings)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	byIdentity := make(map[string]costs.TranscriptSource)
	for _, source := range sources {
		byIdentity[source.Identity] = source
		if filepath.IsAbs(source.Path) == false || filepath.Clean(source.Path) == claudeAlias {
			t.Fatalf("source path is not canonical: %q", source.Path)
		}
	}
	managed := byIdentity["claude:managed-claude"]
	if managed.SessionID != "deck-archived" || managed.ParentSessionID != "conductor" || managed.RunID != "run-1" || managed.Account != "work" {
		t.Fatalf("managed attribution=%+v", managed)
	}
	removed := byIdentity["claude:removed-claude"]
	if removed.SessionID != costs.UnassignedSessionID {
		t.Fatalf("removed attribution=%+v", removed)
	}
	child := byIdentity["claude:managed-claude/subagents/agent-review"]
	if child.Kind != costs.SourceKindClaudeNativeChild || child.SessionID != "deck-archived" || child.ParentSessionID != "conductor" {
		t.Fatalf("native child attribution=%+v", child)
	}
	if byIdentity["codex:11111111-1111-1111-1111-111111111111"].SessionID != "deck-codex" {
		t.Fatalf("codex attribution=%+v", byIdentity["codex:11111111-1111-1111-1111-111111111111"])
	}
}
