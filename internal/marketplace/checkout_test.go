package marketplace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

var testGitPath, _ = exec.LookPath("git")
var testPythonPath, _ = exec.LookPath("python3")

const testOrigin = "https://github.com/DoozyX/agent-deck-upstream.git"

type fixture struct{ home, path, source, bin, realGit string }

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(testGitPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	t.Cleanup(testutil.IsolateHome())
	home, err := filepath.EvalSymlinks(os.Getenv("HOME"))
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{home: home, path: filepath.Join(home, ".local/share/codex-marketplaces/agent-deck"), source: filepath.Join(home, "source"), bin: filepath.Join(home, "bin")}
	f.realGit, err = exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.source, filepath.Dir(f.path), f.bin, filepath.Join(home, ".local/state/agent-deck/marketplace-update")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	gitTest(t, f.source, "init", "-b", "main")
	gitTest(t, f.source, "config", "user.email", "fixture@example.invalid")
	gitTest(t, f.source, "config", "user.name", "Fixture")
	f.advance(t, "initial")
	gitTest(t, home, "clone", "--depth=1", "--single-branch", "--branch=main", "file://"+f.source, f.path)
	gitTest(t, f.path, "remote", "set-url", "origin", testOrigin)
	f.mark(t)
	// Only the fixed read-only URL is redirected; all Git operations are real.
	// Pin the fixture interpreter too: production's safe PATH must not select
	// a different Python runtime (or a slow platform launcher) for the shim.
	script := fmt.Sprintf(`#!%s
import os, sys, time, subprocess
mode_path = %q
mode = open(mode_path).read() if os.path.exists(mode_path) else ""
args = sys.argv[1:]
if "fetch" in args:
    with open(%q, "a") as out: out.write("fetch\n")
    if mode == "fetch-dirty":
        with open("marker", "w") as out: out.write("concurrent edit")
    if mode == "fail":
        with open(mode_path + ".failed", "w") as out: out.write(str(time.time_ns()))
        sys.stderr.write("https://user:secret@example.invalid/ " * 10000)
        sys.exit(1)
    if mode == "barrier":
        with open(mode_path + ".pid", "w") as out: out.write(str(os.getpid()))
        while not os.path.exists(os.path.join(os.path.dirname(mode_path), "git-release")): time.sleep(0.01)
    if mode == "hang":
        child = subprocess.Popen(["sleep", "30"])
        with open(mode_path + ".pid", "w") as out: out.write(str(child.pid))
        child.wait()
if "merge" in args and mode == "merge-fail":
    with open("marker", "w") as out: out.write("partial checkout")
    sys.exit(1)
args = ["file://" + %q if a == %q else a for a in args]
if "merge" in args and mode == "cleanup-fail":
    result = subprocess.run([%q, "-c", "protocol.file.allow=always"] + args)
    os.chmod(os.path.join(os.path.dirname(mode_path), ".local/state/agent-deck/marketplace-update"), 0o500)
    sys.exit(result.returncode)
os.execv(%q, [%q, "-c", "protocol.file.allow=always"] + args)
`, testPythonPath, filepath.Join(f.home, "git-mode"), filepath.Join(f.home, "fetches"), f.source, testOrigin, f.realGit, f.realGit, f.realGit)
	writeTest(t, filepath.Join(f.bin, "git"), script)
	if err := os.Chmod(filepath.Join(f.bin, "git"), 0700); err != nil {
		t.Fatal(err)
	}
	oldGit := trustedGitPath
	trustedGitPath = filepath.Join(f.bin, "git")
	t.Cleanup(func() { trustedGitPath = oldGit })
	return f
}

func (f fixture) mark(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"schema_version": 1, "clone_path": f.path, "origin": testOrigin})
	writeTest(t, filepath.Join(f.home, ".local/state/agent-deck/marketplace-update/ownership.json"), string(data))
}
func (f fixture) advance(t *testing.T, text string) string {
	writeTest(t, filepath.Join(f.source, "marker"), text)
	gitTest(t, f.source, "add", "marker")
	gitTest(t, f.source, "commit", "-m", text)
	return gitTest(t, f.source, "rev-parse", "HEAD")
}

func TestManagedCheckoutOwned(t *testing.T) {
	f := newFixture(t)
	revision := gitTest(t, f.path, "rev-parse", "HEAD")
	t.Setenv("GIT_SSH_COMMAND", "/nonexistent-ssh-for-managed-checkout")
	t.Setenv("GIT_DIR", filepath.Join(f.home, "nonexistent-inherited-repo"))
	called := false
	err := WithManagedCheckout(context.Background(), f.home, func(c Checkout) error {
		called = true
		if c.Path != f.path || c.Revision != revision {
			t.Fatalf("checkout=%+v", c)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("owned checkout callback=%v error=%v", called, err)
	}
}

func TestManagedCheckoutRefusesUnsafe(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, fixture)
	}{
		{"unmarked", func(t *testing.T, f fixture) {
			os.Remove(filepath.Join(f.home, ".local/state/agent-deck/marketplace-update/ownership.json"))
		}},
		{"missing", func(t *testing.T, f fixture) { os.RemoveAll(f.path) }},
		{"symlink", func(t *testing.T, f fixture) {
			os.Rename(f.path, f.path+"-developer")
			os.Symlink(f.path+"-developer", f.path)
		}},
		{"git-objects-alias", func(t *testing.T, f fixture) {
			objects := filepath.Join(f.path, ".git/objects")
			if err := os.Rename(objects, filepath.Join(f.home, "developer-objects")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(f.home, "developer-objects"), objects); err != nil {
				t.Fatal(err)
			}
		}},
		{"marker-alias", func(t *testing.T, f fixture) {
			marker := filepath.Join(StateDir(f.home), "ownership.json")
			os.Rename(marker, marker+".real")
			os.Symlink(marker+".real", marker)
		}},
		{"lock-alias", func(t *testing.T, f fixture) {
			writeTest(t, filepath.Join(f.home, "developer-lock"), "unchanged")
			os.Symlink(filepath.Join(f.home, "developer-lock"), filepath.Join(StateDir(f.home), "update.lock"))
		}},
		{"custom-filter", func(t *testing.T, f fixture) { gitTest(t, f.path, "config", "filter.custom.smudge", "false") }},

		{"origin", func(t *testing.T, f fixture) {
			gitTest(t, f.path, "remote", "set-url", "origin", "ssh://git@example.invalid/repo")
		}},
		{"dirty", func(t *testing.T, f fixture) { writeTest(t, filepath.Join(f.path, "marker"), "local") }},
		{"index", func(t *testing.T, f fixture) {
			writeTest(t, filepath.Join(f.path, "marker"), "local")
			gitTest(t, f.path, "add", "marker")
		}},
		{"untracked", func(t *testing.T, f fixture) { writeTest(t, filepath.Join(f.path, "local"), "untracked") }},
		{"branch", func(t *testing.T, f fixture) { gitTest(t, f.path, "checkout", "-b", "developer") }},
		{"linked-worktree", func(t *testing.T, f fixture) {
			gitTest(t, f.path, "worktree", "add", "-b", "developer", f.path+"-linked")
		}},
		{"linked-path", func(t *testing.T, f fixture) {
			os.RemoveAll(f.path)
			gitTest(t, f.source, "worktree", "add", "-b", "developer", f.path)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.mutate(t, f)
			before := snapshot(t, f.path)
			called := false
			err := WithManagedCheckout(context.Background(), f.home, func(Checkout) error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("unsafe checkout accepted: err=%v called=%v", err, called)
			}
			after := snapshot(t, f.path)
			if before != after {
				t.Fatal("unsafe checkout mutated")
			}
			t.Logf("preserved snapshot: bytes=%d sha256=%x", len(before), sha256.Sum256([]byte(before)))
		})
	}
}

func snapshot(t *testing.T, path string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			b.WriteString("absent")
			return nil
		}
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b.WriteString(strings.TrimPrefix(p, path))
		b.WriteString(info.Mode().String())
		if info.Mode()&os.ModeSymlink != 0 {
			v, e := os.Readlink(p)
			if e != nil {
				return e
			}
			b.WriteString(v)
		} else if !d.IsDir() {
			v, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			b.Write(v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestManagedCheckoutLargeGapFastForward(t *testing.T) {
	f := newFixture(t)
	old := gitTest(t, f.path, "rev-parse", "HEAD")
	var want string
	for i := 0; i < 80; i++ {
		want = f.advance(t, fmt.Sprintf("revision-%d", i))
	}
	called := false
	err := WithManagedCheckout(context.Background(), f.home, func(c Checkout) error {
		called = true
		if c.Revision != want || !c.Updated {
			t.Fatalf("checkout=%+v want %s", c, want)
		}
		content, err := os.ReadFile(filepath.Join(c.Path, "marker"))
		if err != nil || string(content) != "revision-79" {
			t.Fatalf("worktree=%q err=%v", content, err)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("callback=%v error=%v", called, err)
	}
	if got := gitTest(t, f.path, "rev-parse", "HEAD"); got != want || got == old {
		t.Fatalf("HEAD=%s", got)
	}
	// Bounded shallow requests may include the complete history of a short origin.
	if count := gitTest(t, f.path, "rev-list", "--count", "HEAD"); count != "81" {
		t.Fatalf("reachable commits=%s", count)
	}
	if refs := gitTest(t, f.path, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "tags/") {
		t.Fatalf("fetched tags: %s", refs)
	}
}

func TestManagedCheckoutCoalescesFetchNotCallbacks(t *testing.T) {
	f := newFixture(t)
	want := f.advance(t, "new")
	for i := 0; i < 2; i++ {
		called := false
		err := WithManagedCheckout(context.Background(), f.home, func(c Checkout) error {
			called = true
			if c.Revision != want || c.Updated != (i == 0) {
				t.Fatalf("call %d checkout=%+v", i, c)
			}
			return nil
		})
		if err != nil || !called {
			t.Fatalf("call %d err=%v called=%v", i, err, called)
		}
	}
	data, err := os.ReadFile(filepath.Join(f.home, "fetches"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fetch\n" {
		t.Fatalf("network checks=%q", data)
	}
}

func TestManagedCheckoutRejectsRewrite(t *testing.T) {
	f := newFixture(t)
	before := gitTest(t, f.path, "rev-parse", "HEAD")
	gitTest(t, f.source, "checkout", "--orphan", "replacement")
	writeTest(t, filepath.Join(f.source, "marker"), "rewritten")
	gitTest(t, f.source, "add", "marker")
	gitTest(t, f.source, "commit", "-m", "rewrite")
	gitTest(t, f.source, "branch", "-M", "main")
	refs := gitTest(t, f.path, "show-ref")
	err := WithManagedCheckout(context.Background(), f.home, func(Checkout) error { t.Fatal("cache write after rewrite"); return nil })
	if err == nil {
		t.Fatal("accepted rewrite")
	}
	if gitTest(t, f.path, "rev-parse", "HEAD") != before || gitTest(t, f.path, "show-ref") != refs {
		t.Fatal("rewrite changed refs")
	}
	data, _ := os.ReadFile(filepath.Join(f.path, "marker"))
	if string(data) != "initial" {
		t.Fatal("rewrite changed working tree")
	}
}

func TestManagedCheckoutCallbackFailureIsNotSuccess(t *testing.T) {
	f := newFixture(t)
	sentinel := errors.New("private callback secret")
	err := WithManagedCheckout(context.Background(), f.home, func(Checkout) error { return sentinel })
	if err != sentinel {
		t.Fatalf("callback error identity lost: %v", err)
	}
	state, err := loadState(StateDir(f.home))
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastSuccess.IsZero() {
		t.Fatal("failed callback recorded success")
	}
	data, _ := os.ReadFile(filepath.Join(StateDir(f.home), "update.log"))
	if strings.Contains(string(data), "secret") {
		t.Fatal("callback error leaked")
	}
}

func TestManagedCheckoutGitFailureBackoff(t *testing.T) {
	f := newFixture(t)
	writeTest(t, filepath.Join(f.home, "git-mode"), "fail")
	before := snapshot(t, f.path)
	err := WithManagedCheckout(context.Background(), f.home, func(Checkout) error { t.Fatal("cache callback after Git failure"); return nil })
	if err == nil {
		t.Fatal("Git failure accepted")
	}
	if snapshot(t, f.path) != before {
		t.Fatal("failed fetch changed clone")
	}
	failureTime, err := os.ReadFile(filepath.Join(f.home, "git-mode.failed"))
	if err != nil {
		t.Fatal(err)
	}
	nanos, err := strconv.ParseInt(string(failureTime), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	state, err := loadState(StateDir(f.home))
	if err != nil {
		t.Fatal(err)
	}
	if state.NextAttempt.UnixNano() < nanos+int64(minBackoff) {
		t.Fatal("failure backoff starts before Git failure")
	}

	err = WithManagedCheckout(context.Background(), f.home, func(Checkout) error { t.Fatal("cache callback during backoff"); return nil })
	if !errors.Is(err, ErrBackoff) {
		t.Fatalf("backoff=%v", err)
	}
	data, _ := os.ReadFile(filepath.Join(f.home, "fetches"))
	if string(data) != "fetch\n" {
		t.Fatalf("failure retries=%q", data)
	}
	logs, _ := os.ReadFile(filepath.Join(StateDir(f.home), "update.log"))
	if strings.Contains(string(logs), "secret") {
		t.Fatal("Git stderr leaked")
	}
}

func TestManagedCheckoutMergeFailureRestoresWorktree(t *testing.T) {
	f := newFixture(t)
	f.advance(t, "next")
	writeTest(t, filepath.Join(f.home, "git-mode"), "merge-fail")
	old := gitTest(t, f.path, "rev-parse", "HEAD")
	err := WithManagedCheckout(context.Background(), f.home, func(Checkout) error { t.Fatal("cache callback after failed checkout"); return nil })
	if err == nil {
		t.Fatal("failed merge accepted")
	}
	data, _ := os.ReadFile(filepath.Join(f.path, "marker"))
	if string(data) != "initial" || gitTest(t, f.path, "rev-parse", "HEAD") != old || gitTest(t, f.path, "status", "--porcelain") != "" {
		t.Fatalf("failed merge lost usable checkout: marker=%q", data)
	}
}

func TestManagedCheckoutStateWriteFailure(t *testing.T) {
	f := newFixture(t)
	before := snapshot(t, f.path)
	if err := os.Mkdir(filepath.Join(StateDir(f.home), "checkout.json.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	err := WithManagedCheckout(context.Background(), f.home, func(Checkout) error { t.Fatal("cache callback without writable state"); return nil })
	if err == nil {
		t.Fatal("state write failure accepted")
	}
	if snapshot(t, f.path) != before {
		t.Fatal("state write failure changed clone")
	}
	if _, err := os.Stat(filepath.Join(f.home, "fetches")); !os.IsNotExist(err) {
		t.Fatal("network before state preflight")
	}
}

func TestManagedCheckoutTimeoutKillsSubprocessGroup(t *testing.T) {
	f := newFixture(t)
	writeTest(t, filepath.Join(f.home, "git-mode"), "hang")
	before := snapshot(t, f.path)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- WithManagedCheckout(ctx, f.home, func(Checkout) error { return errors.New("unexpected callback") })
	}()
	waitFile(t, filepath.Join(f.home, "git-mode.pid"))
	pidBytes, _ := os.ReadFile(filepath.Join(f.home, "git-mode.pid"))
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancel=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Git cancellation exceeded bound")
	}
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) && syscall.Kill(pid, 0) == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Git descendant survives: %v", err)
	}
	if snapshot(t, f.path) != before {
		t.Fatal("interrupted fetch changed worktree")
	}
}

func TestManagedCheckoutRevalidatesUnchangedFetch(t *testing.T) {
	f := newFixture(t)
	writeTest(t, filepath.Join(f.home, "git-mode"), "fetch-dirty")
	err := WithManagedCheckout(context.Background(), f.home, func(Checkout) error { t.Fatal("callback consumed checkout dirtied during unchanged fetch"); return nil })
	if err == nil {
		t.Fatal("concurrent checkout edit accepted")
	}
	data, _ := os.ReadFile(filepath.Join(f.path, "marker"))
	if string(data) != "concurrent edit" {
		t.Fatal("concurrent edit overwritten")
	}
}

func TestManagedCheckoutIgnoresInheritedGitPATH(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	shim := filepath.Join(dir, "git")
	writeTest(t, shim, "#!/bin/sh\n/usr/bin/touch '"+marker+"'\nexit 1\n")
	if err := os.Chmod(shim, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	out, err := runGit(context.Background(), dir, "--version")
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("inherited PATH shim executed")
	}
	if err != nil || !strings.HasPrefix(out, "git version ") {
		t.Fatalf("trusted Git unavailable: output=%q err=%v", out, err)
	}
}

func TestManagedCheckoutCleanupFailureRestoresWorktree(t *testing.T) {
	f := newFixture(t)
	old := gitTest(t, f.path, "rev-parse", "HEAD")
	index, err := os.ReadFile(filepath.Join(f.path, ".git/index"))
	if err != nil {
		t.Fatal(err)
	}
	f.advance(t, "next")
	dir := StateDir(f.home)
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	writeTest(t, filepath.Join(f.home, "git-mode"), "cleanup-fail")
	err = WithManagedCheckout(context.Background(), f.home, func(Checkout) error {
		t.Fatal("callback after recovery cleanup failure")
		return nil
	})
	if err == nil {
		t.Fatal("cleanup write failure accepted")
	}
	if got := gitTest(t, f.path, "rev-parse", "HEAD"); got != old {
		t.Fatalf("cleanup failure advanced HEAD: got %s want %s", got, old)
	}
	data, err := os.ReadFile(filepath.Join(f.path, "marker"))
	if err != nil || string(data) != "initial" {
		t.Fatalf("worktree not restored: %q %v", data, err)
	}
	restoredIndex, err := os.ReadFile(filepath.Join(f.path, ".git/index"))
	if err != nil || string(restoredIndex) != string(index) {
		t.Fatalf("index not restored: %v", err)
	}
	if got := gitTest(t, f.path, "status", "--porcelain"); got != "" {
		t.Fatalf("dirty restored checkout: %s", got)
	}
	journal := filepath.Join(dir, "recovery.json")
	evidence, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	// Persistent cleanup failure must preserve the journal for inspection.
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	err = WithManagedCheckout(context.Background(), f.home, func(Checkout) error { t.Fatal("callback before inspection"); return nil })
	if err == nil || !strings.Contains(err.Error(), "recovery inspection") {
		t.Fatalf("lost recovery gate: %v", err)
	}
	after, err := os.ReadFile(journal)
	if err != nil || string(after) != string(evidence) {
		t.Fatalf("recovery evidence changed: %v", err)
	}
}

func TestManagedCheckoutCoalescedCallbackFailureBackoff(t *testing.T) {
	f := newFixture(t)
	if err := WithManagedCheckout(context.Background(), f.home, func(Checkout) error { return nil }); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("private callback failure")
	calls := 0
	failing := func(Checkout) error { calls++; return sentinel }
	t.Setenv("CODEX_HOME", filepath.Join(f.home, "profile-a"))
	if err := WithManagedCheckout(context.Background(), f.home, failing); err != sentinel {
		t.Fatalf("callback identity: %v", err)
	}
	if err := WithManagedCheckout(context.Background(), f.home, failing); !errors.Is(err, ErrBackoff) || calls != 1 {
		t.Fatalf("coalesced failure retried: calls=%d err=%v", calls, err)
	}
	t.Setenv("CODEX_HOME", filepath.Join(f.home, "profile-b"))
	caughtUp := false
	if err := WithManagedCheckout(context.Background(), f.home, func(c Checkout) error {
		caughtUp = true
		if c.Updated {
			t.Fatal("coalesced profile fetched again")
		}
		return nil
	}); err != nil || !caughtUp {
		t.Fatalf("other profile suppressed: %v", err)
	}
	t.Setenv("CODEX_HOME", filepath.Join(f.home, "profile-a"))
	if err := WithManagedCheckout(context.Background(), f.home, failing); !errors.Is(err, ErrBackoff) || calls != 1 {
		t.Fatalf("other profile cleared failure: calls=%d err=%v", calls, err)
	}
	key, err := callbackProfileKey(f.home)
	if err != nil {
		t.Fatal(err)
	}
	state, err := loadState(StateDir(f.home))
	if err != nil {
		t.Fatal(err)
	}
	success := state.LastSuccess
	retry := state.CallbackRetries[key]
	if retry.Failures != 1 || time.Until(retry.NextAttempt) <= 0 || time.Until(retry.NextAttempt) > minBackoff {
		t.Fatalf("first callback retry=%+v", retry)
	}
	retry.NextAttempt = time.Now().Add(-time.Second)
	state.CallbackRetries[key] = retry
	if err := saveState(StateDir(f.home), state); err != nil {
		t.Fatal(err)
	}
	failedAt := time.Now()
	if err := WithManagedCheckout(context.Background(), f.home, failing); err != sentinel || calls != 2 {
		t.Fatalf("expired retry did not run: calls=%d err=%v", calls, err)
	}
	state, err = loadState(StateDir(f.home))
	if err != nil {
		t.Fatal(err)
	}
	retry = state.CallbackRetries[key]
	if retry.Failures != 2 || retry.NextAttempt.Before(failedAt.Add(2*minBackoff)) || !state.LastSuccess.Equal(success) {
		t.Fatalf("failed retry accounting: %+v", state)
	}
	retry.NextAttempt = time.Now().Add(-time.Second)
	state.CallbackRetries[key] = retry
	if err := saveState(StateDir(f.home), state); err != nil {
		t.Fatal(err)
	}
	if err := WithManagedCheckout(context.Background(), f.home, func(Checkout) error { return nil }); err != nil {
		t.Fatal(err)
	}
	state, err = loadState(StateDir(f.home))
	if err != nil || len(state.CallbackRetries) != 0 || !state.LastSuccess.Equal(success) {
		t.Fatalf("successful callback did not clear only retry state: %+v %v", state, err)
	}

	fetches, err := os.ReadFile(filepath.Join(f.home, "fetches"))
	if err != nil || string(fetches) != "fetch\n" {
		t.Fatalf("coalescing lost: %q %v", fetches, err)
	}
}

func TestManagedCheckoutGitChildUsesSafePATH(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "trusted-git")
	writeTest(t, shim, "#!/bin/sh\nprintf '%s' \"$PATH\"\n")
	if err := os.Chmod(shim, 0700); err != nil {
		t.Fatal(err)
	}
	previous := trustedGitPath
	trustedGitPath = shim
	t.Cleanup(func() { trustedGitPath = previous })
	t.Setenv("PATH", dir)
	got, err := runGit(context.Background(), dir, "--version")
	if err != nil || got != "/usr/bin:/bin" {
		t.Fatalf("unsafe child PATH: %q %v", got, err)
	}
}
