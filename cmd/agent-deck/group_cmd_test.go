package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func runGitForManualGroupTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func setupManualGroupGitRepo(t *testing.T) (home, repo string) {
	t.Helper()
	home = t.TempDir()
	repo = filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGitForManualGroupTest(t, repo, "init", "-b", "main")
	runGitForManualGroupTest(t, repo, "config", "user.email", "test@example.com")
	runGitForManualGroupTest(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runGitForManualGroupTest(t, repo, "add", "README.md")
	runGitForManualGroupTest(t, repo, "commit", "-m", "seed")
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmanual_creation_only = true\n")
	return home, repo
}

// helper: create storage, add N root groups, return (storage, instances, groupTree).
// Each call overwrites the _test profile, so tests are independent when run sequentially.
func setupGroupsForReorder(t *testing.T, names ...string) *session.Storage {
	t.Helper()
	storage, err := session.NewStorageWithProfile("_test")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}

	instances := []*session.Instance{}
	groupTree := session.NewGroupTreeWithGroups(instances, nil)

	for _, name := range names {
		groupTree.CreateGroup(name)
	}

	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	return storage
}

// helper: reload groups from storage and return ordered paths (excluding default group)
func reloadGroupPaths(t *testing.T, storage *session.Storage) []string {
	t.Helper()
	_, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}

	instances := []*session.Instance{}
	tree := session.NewGroupTreeWithGroups(instances, groups)

	var paths []string
	for _, g := range tree.GroupList {
		if g.Path == session.DefaultGroupPath {
			continue
		}
		paths = append(paths, g.Path)
	}
	return paths
}

func TestGroupReorderUp(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Beta up — should swap with Alpha
	handleGroupReorder("_test", []string{"Beta", "--up"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Beta" || paths[1] != "Alpha" || paths[2] != "Gamma" {
		t.Errorf("expected [Beta Alpha Gamma], got %v", paths)
	}
}

func TestGroupReorderDown(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Beta down — should swap with Gamma
	handleGroupReorder("_test", []string{"Beta", "--down"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Alpha" || paths[1] != "Gamma" || paths[2] != "Beta" {
		t.Errorf("expected [Alpha Gamma Beta], got %v", paths)
	}
}

func TestGroupReorderPosition(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Gamma to position 0
	handleGroupReorder("_test", []string{"Gamma", "--position", "0"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Gamma" || paths[1] != "Alpha" || paths[2] != "Beta" {
		t.Errorf("expected [Gamma Alpha Beta], got %v", paths)
	}
}

func TestGroupReorderAlreadyAtTop(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Alpha up — already first, should be no-op
	handleGroupReorder("_test", []string{"Alpha", "--up"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Alpha" || paths[1] != "Beta" || paths[2] != "Gamma" {
		t.Errorf("expected [Alpha Beta Gamma], got %v", paths)
	}
}

func TestGroupReorderAlreadyAtBottom(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Gamma down — already last, should be no-op
	handleGroupReorder("_test", []string{"Gamma", "--down"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Alpha" || paths[1] != "Beta" || paths[2] != "Gamma" {
		t.Errorf("expected [Alpha Beta Gamma], got %v", paths)
	}
}

func TestGroupReorderPositionClamp(t *testing.T) {
	storage := setupGroupsForReorder(t, "Alpha", "Beta", "Gamma")

	// Move Alpha to position 99 (should clamp to last)
	handleGroupReorder("_test", []string{"Alpha", "--position", "99"})

	paths := reloadGroupPaths(t, storage)
	if len(paths) < 3 {
		t.Fatalf("expected 3 groups, got %d", len(paths))
	}
	if paths[0] != "Beta" || paths[1] != "Gamma" || paths[2] != "Alpha" {
		t.Errorf("expected [Beta Gamma Alpha], got %v", paths)
	}
}

// TestNormalizeGroupPathCasePreserving verifies that normalizeGroupPath does not
// lowercase its argument. GroupTree.Groups is keyed by the raw stored path, so
// lowercasing here would make any group with uppercase letters unreachable.
func TestNormalizeGroupPathCasePreserving(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"work", "work"},
		{"Work", "Work"},
		{"My Projects", "My-Projects"},
		{"work/Frontend", "work/Frontend"},
	}
	for _, tc := range cases {
		got := normalizeGroupPath(tc.input)
		if got != tc.want {
			t.Errorf("normalizeGroupPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestNormalizeGroupPathMatchesStoredKey verifies that after creating an uppercase
// group via GroupTree.CreateGroup, the result of normalizeGroupPath on the same
// name is a key that exists in GroupTree.Groups (regression guard for issue #1488).
func TestNormalizeGroupPathMatchesStoredKey(t *testing.T) {
	tree := session.NewGroupTreeWithGroups([]*session.Instance{}, nil)
	tree.CreateGroup("Parent")

	normalized := normalizeGroupPath("Parent")
	if _, exists := tree.Groups[normalized]; !exists {
		t.Errorf("normalizeGroupPath(%q) = %q, but Groups[%q] does not exist; stored keys: %v",
			"Parent", normalized, normalized, groupKeys(tree))
	}
}

// TestGroupDeleteAmbiguousNameError verifies that deleting by a bare leaf name
// that matches multiple groups returns an error rather than silently deleting one.
func TestGroupDeleteAmbiguousNameError(t *testing.T) {
	tree := session.NewGroupTreeWithGroups([]*session.Instance{}, nil)
	// Create pa, pb, then dup under each
	tree.CreateGroup("pa")
	tree.CreateGroup("pb")
	tree.CreateSubgroup("pa", "dup")
	tree.CreateSubgroup("pb", "dup")

	// Simulate the ambiguous-lookup logic from handleGroupDelete.
	name := "dup"
	type match struct {
		path  string
		group *session.Group
	}
	var matches []match
	for path, g := range tree.Groups {
		if strings.EqualFold(g.Name, name) {
			matches = append(matches, match{path: path, group: g})
		}
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 ambiguous matches for %q, got %d: %v", name, len(matches), matches)
	}
}

// groupKeys is a test helper that returns the keys of GroupTree.Groups.
func groupKeys(tree *session.GroupTree) []string {
	keys := make([]string, 0, len(tree.Groups))
	for k := range tree.Groups {
		keys = append(keys, k)
	}
	return keys
}

// writeGroupDefaultsConfig writes config.toml to the legacy path under the
// isolated HOME. runAgentDeck re-points XDG_CONFIG_HOME at an unpopulated dir,
// so EffectiveConfigPath falls through to this legacy file deterministically.
func writeGroupDefaultsConfig(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

// runGroupCreate runs `group create <args...> --json` under the isolated HOME
// and returns the parsed max_concurrent from the JSON payload.
func runGroupCreate(t *testing.T, home string, args ...string) int {
	t.Helper()
	full := append([]string{"group", "create"}, args...)
	full = append(full, "--json")
	stdout, stderr, code := runAgentDeck(t, home, full...)
	if code != 0 {
		t.Fatalf("group create %v failed (exit %d)\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
	}
	var parsed struct {
		MaxConcurrent int `json:"max_concurrent"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("parse group create JSON: %v\nstdout: %s", err, stdout)
	}
	return parsed.MaxConcurrent
}

// TestGroupCreate_DefaultMaxConcurrent_ConfigUnset: no config → new group is
// serial (1), byte-for-byte v1.9.1 behavior.
func TestGroupCreate_DefaultMaxConcurrent_ConfigUnset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	if got := runGroupCreate(t, home, "g"); got != 1 {
		t.Errorf("config unset: expected max_concurrent=1, got %d", got)
	}
}

// TestGroupCreate_DefaultMaxConcurrent_ConfigN: [group_defaults].max_concurrent = 3
// → new group capped at 3.
func TestGroupCreate_DefaultMaxConcurrent_ConfigN(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmax_concurrent = 3\n")
	if got := runGroupCreate(t, home, "g"); got != 3 {
		t.Errorf("config N=3: expected max_concurrent=3, got %d", got)
	}
}

// TestGroupCreate_FlagOverridesConfigDefault: --max-concurrent beats the config
// default (precedence: flag > config > built-in 1). Both spellings are checked:
// the space-separated form used to be mishandled by reorderGroupArgs, whose
// hardcoded "which flags take a value" map never listed --max-concurrent, so
// the group name was parsed as the int and the command exited printing usage.
func TestGroupCreate_FlagOverridesConfigDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	for _, args := range [][]string{
		{"g", "--max-concurrent=2"},
		{"g", "--max-concurrent", "2"},
		{"--max-concurrent", "2", "g"},
	} {
		home := t.TempDir()
		writeGroupDefaultsConfig(t, home, "[group_defaults]\nmax_concurrent = 5\n")
		if got := runGroupCreate(t, home, args...); got != 2 {
			t.Errorf("%v: expected max_concurrent=2, got %d", args, got)
		}
	}
}

// TestGroupCreate_ConfigZeroUnlimited: [group_defaults].max_concurrent = 0 →
// new group is unlimited (0). Proves *0 is not collapsed to the built-in 1.
func TestGroupCreate_ConfigZeroUnlimited(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmax_concurrent = 0\n")
	if got := runGroupCreate(t, home, "g"); got != 0 {
		t.Errorf("config 0: expected max_concurrent=0 (unlimited), got %d", got)
	}
}

func TestGroupCreate_ManualCreationOnlyRejectsManagedSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmanual_creation_only = true\n")

	stdout, stderr, code := runAgentDeckWithEnv(t, home,
		[]string{"AGENTDECK_INSTANCE_ID=managed-session"},
		"group", "create", "agent-made", "--json")
	if code == 0 {
		t.Fatalf("managed group creation succeeded; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout, "group creation is restricted to the user") {
		t.Fatalf("missing restriction error; stdout=%s stderr=%s", stdout, stderr)
	}

	listOut, listErr, listCode := runAgentDeck(t, home, "group", "list", "--json")
	if listCode != 0 {
		t.Fatalf("list groups failed (exit %d): %s / %s", listCode, listOut, listErr)
	}
	if strings.Contains(listOut, "agent-made") {
		t.Fatalf("denied command persisted agent-made group: %s", listOut)
	}
}

func TestAdd_ManualCreationOnlyRejectsMissingGroupWithoutSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmanual_creation_only = true\n")

	stdout, stderr, code := runAgentDeckWithEnv(t, home,
		[]string{"AGENTDECK_INSTANCE_ID=managed-session"},
		"add", "--title", "blocked-add", "--group", "agent-made", "--no-parent", "--json", project)
	if code == 0 {
		t.Fatalf("managed add to missing group succeeded; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "group creation is restricted to the user") {
		t.Fatalf("missing restriction error; stdout=%s stderr=%s", stdout, stderr)
	}

	listOut, listErr, listCode := runAgentDeck(t, home, "list", "--json", "--include-groups")
	if listCode != 0 {
		t.Fatalf("list failed (exit %d): %s / %s", listCode, listOut, listErr)
	}
	if strings.Contains(listOut, "blocked-add") || strings.Contains(listOut, "agent-made") {
		t.Fatalf("denied add persisted a session or group: %s", listOut)
	}
}

func TestAdd_ManualCreationOnlyRejectsAutoDerivedMissingGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	project := filepath.Join(home, "projects", "agent-work")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmanual_creation_only = true\n")

	stdout, stderr, code := runAgentDeckWithEnv(t, home,
		[]string{"AGENTDECK_INSTANCE_ID=managed-session"},
		"add", "--title", "blocked-derived", "--no-parent", "--json", project)
	if code == 0 {
		t.Fatalf("managed add auto-created a derived group; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "group creation is restricted to the user") {
		t.Fatalf("missing restriction error; stdout=%s stderr=%s", stdout, stderr)
	}

	listOut, listErr, listCode := runAgentDeck(t, home, "list", "--json", "--include-groups")
	if listCode != 0 {
		t.Fatalf("list failed (exit %d): %s / %s", listCode, listOut, listErr)
	}
	if strings.Contains(listOut, "blocked-derived") || strings.Contains(listOut, "projects") {
		t.Fatalf("denied auto-derived add persisted a session or group: %s", listOut)
	}
}

func TestGroupMove_ManualCreationOnlyRejectsMissingTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmanual_creation_only = true\n")
	if _, stderr, code := runAgentDeck(t, home, "group", "create", "source"); code != 0 {
		t.Fatalf("create source group failed: %s", stderr)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "add", "--title", "move-me", "--group", "source", "--no-parent", "--json", project); code != 0 {
		t.Fatalf("seed session failed: stdout=%s stderr=%s", stdout, stderr)
	}

	stdout, stderr, code := runAgentDeckWithEnv(t, home,
		[]string{"AGENTDECK_INSTANCE_ID=managed-session"},
		"group", "move", "move-me", "agent-made", "--json")
	if code == 0 {
		t.Fatalf("managed move auto-created a group; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "group creation is restricted to the user") {
		t.Fatalf("missing restriction error; stdout=%s stderr=%s", stdout, stderr)
	}

	showOut, showErr, showCode := runAgentDeck(t, home, "session", "show", "move-me", "--json")
	if showCode != 0 {
		t.Fatalf("show failed (exit %d): %s / %s", showCode, showOut, showErr)
	}
	if !strings.Contains(showOut, `"group": "source"`) || strings.Contains(showOut, "agent-made") {
		t.Fatalf("denied move changed the session group: %s", showOut)
	}
}

func TestSessionMove_ManualCreationOnlyRejectsBeforeHistoryMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home := t.TempDir()
	oldProject := filepath.Join(home, "old", "project")
	newProject := filepath.Join(home, "new", "project")
	for _, dir := range []string{oldProject, newProject} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeGroupDefaultsConfig(t, home, "[group_defaults]\nmanual_creation_only = true\n")
	if _, stderr, code := runAgentDeck(t, home, "group", "create", "source"); code != 0 {
		t.Fatalf("create source group failed: %s", stderr)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "add", "--title", "move-path", "--group", "source", "--no-parent", "--json", oldProject); code != 0 {
		t.Fatalf("seed session failed: stdout=%s stderr=%s", stdout, stderr)
	}

	oldHistory := filepath.Join(home, ".claude", "projects", session.SlugifyClaudeProjectPath(oldProject))
	newHistory := filepath.Join(home, ".claude", "projects", session.SlugifyClaudeProjectPath(newProject))
	if err := os.MkdirAll(oldHistory, 0o755); err != nil {
		t.Fatalf("mkdir old history: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldHistory, "session.jsonl"), []byte("history"), 0o600); err != nil {
		t.Fatalf("write old history: %v", err)
	}

	stdout, stderr, code := runAgentDeckWithEnv(t, home,
		[]string{"AGENTDECK_INSTANCE_ID=managed-session"},
		"session", "move", "move-path", newProject, "--group", "agent-made", "--no-restart", "--json")
	if code == 0 {
		t.Fatalf("managed session move auto-created a group; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "group creation is restricted to the user") {
		t.Fatalf("missing restriction error; stdout=%s stderr=%s", stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(oldHistory, "session.jsonl")); err != nil {
		t.Fatalf("denied move changed old history: %v", err)
	}
	if _, err := os.Stat(newHistory); !os.IsNotExist(err) {
		t.Fatalf("denied move created new history path; stat error=%v", err)
	}
}

func TestLaunch_ManualCreationOnlyRejectsBeforeWorktreeCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home, repo := setupManualGroupGitRepo(t)

	stdout, stderr, code := runAgentDeckWithEnv(t, home,
		[]string{"AGENTDECK_INSTANCE_ID=managed-session"},
		"launch", "--title", "blocked-worktree", "--group", "agent-made", "--no-parent",
		"--worktree", "blocked-agent-branch", "--new-branch", "--cmd", "shell", "--json", repo)
	if code == 0 {
		t.Fatalf("managed worktree launch succeeded; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "group creation is restricted to the user") {
		t.Fatalf("missing restriction error; stdout=%s stderr=%s", stdout, stderr)
	}

	worktrees := runGitForManualGroupTest(t, repo, "worktree", "list")
	if strings.Contains(worktrees, "blocked-agent-branch") {
		t.Fatalf("denied launch created a worktree before rejecting the group:\n%s", worktrees)
	}
}

func TestLaunch_ManualCreationOnlyRejectsAutoDerivedGroupBeforeWorktreeCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI subprocess test in -short mode")
	}
	home, repo := setupManualGroupGitRepo(t)

	stdout, stderr, code := runAgentDeckWithEnv(t, home,
		[]string{"AGENTDECK_INSTANCE_ID=managed-session"},
		"launch", "--title", "blocked-derived-worktree", "--no-parent",
		"--worktree", "blocked-derived-branch", "--new-branch", "--cmd", "shell", "--json", repo)
	if code == 0 {
		t.Fatalf("managed derived-group worktree launch succeeded; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "group creation is restricted to the user") {
		t.Fatalf("missing restriction error; stdout=%s stderr=%s", stdout, stderr)
	}

	worktrees := runGitForManualGroupTest(t, repo, "worktree", "list")
	if strings.Contains(worktrees, "blocked-derived-branch") {
		t.Fatalf("denied derived-group launch created a worktree before rejecting the group:\n%s", worktrees)
	}
}
