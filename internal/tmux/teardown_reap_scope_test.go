//go:build !windows

package tmux

// Regression coverage for the 2026-09-11 orphan-leak incident.
//
// A session started a dev stack; the stack's wrapper exited and its children
// were reparented to PID 1. Both teardown paths built their reap set by
// walking parent links from `#{pane_pid}`, so the reparented tree was never
// captured and survived the session by 18 minutes (345 MB of swap on a box
// that was already OOM-killing).
//
// These tests spawn exactly that shape — a child that deliberately orphans
// itself, publishing its own PID so the assertion names a real process rather
// than pgrep-matching — and assert it dies with the session on BOTH paths:
// Session.Kill (normal end) and Session.KillAndWait (forced removal).
//
// Before the teardown_reap_scope.go fix both subtests fail: the orphan is
// still alive after teardown.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// startSessionWithOrphanedChild creates a tmux session on a private socket
// whose pane spawns a grandchild that is immediately orphaned (its subshell
// parent exits, so the kernel reparents it to PID 1). It returns the session
// and the orphan's PID, and registers unconditional cleanup — a test for a
// process-leak fix must never leak a process itself.
func startSessionWithOrphanedChild(t *testing.T) (*Session, int) {
	t.Helper()
	skipIfNoTmuxBinary(t)

	socket := "adreap-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	if len(socket) > 40 {
		socket = socket[:40]
	}
	killServer := func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() }
	killServer() // clear anything an aborted run stranded
	t.Cleanup(killServer)

	pidFile := filepath.Join(t.TempDir(), "orphan.pid")
	name := SessionPrefix + "reaporphan"

	// The pane runs an INTERACTIVE shell, and the orphan is spawned through
	// send-keys rather than as the pane command. Both details are load-bearing.
	//
	// Job control (which only an interactive shell turns on) puts a background
	// job in its OWN process group. That is the production condition: tmux's
	// kill-session SIGHUPs the pane leader's process group, so a job in a
	// different group is missed, and once its subshell parent exits it is
	// reparented to PID 1 and leaves the parent-link walk too. The only handle
	// left on it is the pane's controlling terminal.
	//
	// Run the same script as the pane command instead and the leak does NOT
	// reproduce: without job control the job stays in the pane leader's group
	// and tmux's own SIGHUP reaps it.
	//
	// `( ... & )` orphans the inner sh (its subshell parent exits immediately);
	// `exec sleep` keeps the PID it published; the trailing background sleep
	// keeps a still-parented sibling around so the test also proves the
	// pre-existing parent-walk reap is untouched.
	require.NoError(t,
		exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", name,
			"-x", "80", "-y", "24", "bash", "--norc", "-i").Run(),
		"failed to create test session on socket %s", socket)
	waitForPaneCommand(t, socket, name)

	script := fmt.Sprintf(`( sh -c 'echo $$ > %s; exec sleep 900' & ) ; sleep 900 &`, pidFile)
	require.NoError(t,
		exec.Command("tmux", "-L", socket, "send-keys", "-t", name, script, "Enter").Run(),
		"failed to send the orphan-spawning script")

	orphanPID := waitForPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(orphanPID, syscall.SIGKILL) })

	// Precondition: the child really is orphaned, so the parent-link walk
	// cannot reach it. Without this the test could pass for the wrong reason.
	require.Eventually(t, func() bool { return readPPID(orphanPID) == 1 },
		5*time.Second, 100*time.Millisecond,
		"child %d never reparented to PID 1 (ppid=%d); the repro shape is wrong",
		orphanPID, readPPID(orphanPID))

	// And it is genuinely invisible to the walk the old code used.
	panePID, walked, err := (&Session{Name: name, SocketName: socket}).paneProcessTreeFor(name)
	require.NoError(t, err)
	require.NotContains(t, walked, orphanPID,
		"orphan %d was reachable from pane %d — the repro no longer reproduces", orphanPID, panePID)

	return &Session{Name: name, SocketName: socket}, orphanPID
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || parsed <= 1 {
			return false
		}
		pid = parsed
		return true
	}, 10*time.Second, 100*time.Millisecond, "child never published its PID to %s", path)
	return pid
}

// waitForPaneCommand blocks until the pane's interactive shell is ready to
// accept send-keys. Sending before the shell has drawn its first prompt loses
// the keys and the test would hang on the PID file.
func waitForPaneCommand(t *testing.T, socket, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		out, err := exec.Command("tmux", "-L", socket, "list-panes",
			"-t", name+":", "-F", "#{pane_current_command}").Output()
		return err == nil && strings.Contains(string(out), "bash")
	}, 10*time.Second, 100*time.Millisecond, "pane shell never became ready")
}

func readPPID(pid int) int {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=").Output()
	if err != nil {
		return -1
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return -1
	}
	return ppid
}

func processIsAlive(pid int) bool {
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// TestKillReapsOrphanedDescendant covers the normal-end path (Session.Kill).
func TestKillReapsOrphanedDescendant(t *testing.T) {
	sess, orphanPID := startSessionWithOrphanedChild(t)

	require.NoError(t, sess.Kill())

	// Kill runs the escalation on a goroutine, so poll rather than assert once.
	require.Eventually(t, func() bool { return !processIsAlive(orphanPID) },
		10*time.Second, 100*time.Millisecond,
		"orphaned descendant %d survived Session.Kill", orphanPID)
}

// TestKillAndWaitReapsOrphanedDescendant covers the forced-removal path
// (`agent-deck session remove --force` reaches Session.KillAndWait). This one
// is synchronous: the orphan must already be dead when KillAndWait returns,
// because the CLI process exits immediately afterwards and would abort any
// background goroutine (issue #59).
func TestKillAndWaitReapsOrphanedDescendant(t *testing.T) {
	sess, orphanPID := startSessionWithOrphanedChild(t)

	require.NoError(t, sess.KillAndWait())

	require.False(t, processIsAlive(orphanPID),
		"orphaned descendant %d was still alive when KillAndWait returned", orphanPID)
}

// TestNormalizeTTYName pins the tty comparison both halves of the scope rely
// on: tmux prints "/dev/ttys043", ps prints "ttys043", and a process with no
// controlling terminal ("??" on macOS, "-" elsewhere) is never in scope.
func TestNormalizeTTYName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/dev/ttys043", "ttys043"},
		{"ttys043", "ttys043"},
		{"  /dev/pts/7 ", "pts/7"},
		{"??", ""},
		{"-", ""},
		{"", ""},
		{"   ", ""},
	} {
		require.Equal(t, tc.want, normalizeTTYName(tc.in), "normalizeTTYName(%q)", tc.in)
	}
}
