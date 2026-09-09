package session

import (
	"strings"
	"testing"
)

func TestNormalizeWorktreeSessionCwd(t *testing.T) {
	cases := map[string]string{
		"":            WorktreeSessionCwdWorktree,
		"worktree":    WorktreeSessionCwdWorktree,
		"  WORKTREE ": WorktreeSessionCwdWorktree,
		"nonsense":    WorktreeSessionCwdWorktree,
		"repo-root":   WorktreeSessionCwdRepoRoot,
		"repo_root":   WorktreeSessionCwdRepoRoot,
		"Root":        WorktreeSessionCwdRepoRoot,
		" RepoRoot ":  WorktreeSessionCwdRepoRoot,
	}
	for in, want := range cases {
		if got := NormalizeWorktreeSessionCwd(in); got != want {
			t.Errorf("NormalizeWorktreeSessionCwd(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveWorktreeSessionCwd(t *testing.T) {
	const (
		wt   = "/repo/.worktrees/feature-x"
		root = "/repo"
	)

	t.Run("default mode keeps the worktree", func(t *testing.T) {
		if got := ResolveWorktreeSessionCwd("", wt, root); got != wt {
			t.Fatalf("got %q, want %q", got, wt)
		}
	})

	t.Run("repo-root mode starts at the repository root", func(t *testing.T) {
		if got := ResolveWorktreeSessionCwd(WorktreeSessionCwdRepoRoot, wt, root); got != root {
			t.Fatalf("got %q, want %q", got, root)
		}
	})

	// worktree_reuse sessions point WorktreePath at the user's original repo;
	// there is nothing to relocate and returning root must not change the path.
	t.Run("worktree that is the repo root is left alone", func(t *testing.T) {
		if got := ResolveWorktreeSessionCwd(WorktreeSessionCwdRepoRoot, root, root); got != root {
			t.Fatalf("got %q, want %q", got, root)
		}
	})

	t.Run("missing repo root falls back to the worktree", func(t *testing.T) {
		if got := ResolveWorktreeSessionCwd(WorktreeSessionCwdRepoRoot, wt, ""); got != wt {
			t.Fatalf("got %q, want %q", got, wt)
		}
	})
}

func TestRunsOutsideItsWorktree(t *testing.T) {
	t.Run("non-worktree session", func(t *testing.T) {
		inst := &Instance{ProjectPath: "/repo"}
		if inst.RunsOutsideItsWorktree() {
			t.Fatal("a session with no worktree never runs outside one")
		}
	})

	t.Run("classic worktree session runs inside it", func(t *testing.T) {
		inst := &Instance{
			ProjectPath:      "/repo/.worktrees/wt",
			WorktreePath:     "/repo/.worktrees/wt",
			WorktreeRepoRoot: "/repo",
		}
		if inst.RunsOutsideItsWorktree() {
			t.Fatal("ProjectPath == WorktreePath must report inside")
		}
	})

	t.Run("repo-root session runs outside it", func(t *testing.T) {
		inst := &Instance{
			ProjectPath:      "/repo",
			WorktreePath:     "/repo/.worktrees/wt",
			WorktreeRepoRoot: "/repo",
		}
		if !inst.RunsOutsideItsWorktree() {
			t.Fatal("ProjectPath == repo root must report outside")
		}
	})
}

func TestWorktreeCwdDirective(t *testing.T) {
	got := WorktreeCwdDirective("/repo/.worktrees/wt", "feature/x", "/repo")
	for _, want := range []string{"/repo/.worktrees/wt", "feature/x", "cd there", "Never edit"} {
		if !strings.Contains(got, want) {
			t.Errorf("directive %q missing %q", got, want)
		}
	}

	if got := WorktreeCwdDirective("", "b", "/repo"); got != "" {
		t.Errorf("no worktree should produce no directive, got %q", got)
	}

	// A branchless worktree must not emit a dangling "(branch )".
	if got := WorktreeCwdDirective("/wt", "", "/repo"); strings.Contains(got, "(branch") {
		t.Errorf("blank branch leaked into directive: %q", got)
	}
}
