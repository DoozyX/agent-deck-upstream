package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmuxutf8"
)

var ErrTmuxSessionNotFound = errors.New("tmux session not found")

type wsConnWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func newWSConnWriter(conn *websocket.Conn) *wsConnWriter {
	return &wsConnWriter{conn: conn}
}

func (w *wsConnWriter) WriteJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.conn.WriteJSON(v)
}

func (w *wsConnWriter) WriteBinary(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.conn.WriteMessage(websocket.BinaryMessage, data)
}

type tmuxPTYBridge struct {
	tmuxSession    string
	tmuxSocketName string // tmux -L selector captured from Instance (issue #687)
	sessionID      string
	writer         *wsConnWriter

	cmd *exec.Cmd

	// ptmxMu guards ptmx against a concurrent Close/Resize race. Close
	// closes the PTY file and nils the pointer under the write lock;
	// Resize reads under the read lock so Setsize cannot hit a freshly
	// closed fd. Observed as an intermittent TestTmuxPTYBridgeResize
	// -race failure on CI (v1.7.4, v1.7.5 release workflows).
	ptmxMu sync.RWMutex
	ptmx   *os.File

	closeOnce sync.Once
	done      chan struct{}

	// closing is set by Close before it tears the PTY down, so streamOutput's
	// read error can distinguish "the attach process died on us" from "we
	// closed it ourselves" (the WS client went away — user closed the tab —
	// and serveTerminalWS's deferred Close ran). Without it the quick-exit
	// branch below treats our own teardown inside quickExitGrace as an attach
	// failure and writes a fatal frame; that is invisible today only because
	// the write lands on an already-dead connection, which is write-ordering
	// luck rather than a guarantee.
	closing atomic.Bool

	// startedAt and mapErr back the quick-exit signal in streamOutput: if a
	// REMOTE attach command dies within quickExitGrace of starting, that is
	// treated as an attach failure (bad host, rejected auth, missing remote
	// binary) rather than a normal detach, and mapErr's code/message/hint —
	// the same contract used for a synchronous attach() failure — is sent as
	// an error frame instead of the silent/session_closed handling a
	// long-lived session's eventual exit gets. Local tmux attaches are
	// excluded; see isRemote.
	startedAt time.Time
	mapErr    wsAttachErrorFunc

	// wroteOutput records whether the attach command ever put a byte on the
	// PTY. See attachFailed for why that matters.
	wroteOutput atomic.Bool

	// waitOnce/exitCode memoise the single permitted cmd.Wait. Both Close
	// (which reaps after killing) and attachFailed (which needs the status to
	// classify a quick exit) need the process reaped, and calling cmd.Wait
	// from two goroutines is a data race.
	waitOnce sync.Once
	exitCode atomic.Int32

	// reaped is set once cmd.Wait has RETURNED, i.e. the child is no longer a
	// zombie and its PID is free for the OS to hand to somebody else. Close
	// must not signal after that point.
	//
	// Round 8 (#1): before this branch, cmd.Wait only ever ran inside Close,
	// AFTER the kill, so b.cmd.Process.Pid was always still a (reaped-later)
	// zombie and syscall.Getpgid on it was always safe. attachFailed's
	// waitExitCode now reaps at the moment of a quick exit, and on that path
	// the server does not close the WebSocket — the client keeps the socket
	// open with reconnect disabled (TerminalPanel.js's fatal branch) — so
	// serveTerminalWS's deferred Close can run minutes or hours later. If the
	// host recycled the PID in between, Getpgid would succeed for an unrelated
	// process and Kill(-pgid, SIGTERM) would take down that whole process
	// group, which on a deck host means other agent-deck panes.
	//
	// Reading b.cmd.ProcessState instead would race with an in-flight Wait.
	reaped atomic.Bool

	// terminateProcessGroup indirects Close's signal step so the reaped-PID
	// regression test can observe whether Close signalled at all. nil selects
	// the production behavior (defaultTerminateProcessGroup); nothing outside
	// tests sets it.
	terminateProcessGroup func(*os.Process)
}

// sshFailureExitCode is the status ssh itself exits with when it could not
// establish the session at all: unreachable host, connection refused, rejected
// auth, host-key failure. Once ssh HAS connected, `ssh -tt` exits with the
// REMOTE command's status instead, so 255 is the one code that distinguishes
// "the dial failed" from "the thing you attached to ended".
const sshFailureExitCode = 255

// exitCodeUnknown marks a process whose status we never learned (no command, or
// the bounded wait in attachFailed timed out).
const exitCodeUnknown = -2

// reapWaitTimeout bounds attachFailed's wait for the exit status. On the path
// that calls it the child is already gone — that is why the PTY read failed —
// so this only exists so an exotic case (a grandchild still holding the pts)
// can never wedge streamOutput and leave b.done unclosed.
const reapWaitTimeout = 2 * time.Second

func newTmuxPTYBridge(tmuxSession, tmuxSocketName, sessionID string, writer *wsConnWriter, mapErr wsAttachErrorFunc) (*tmuxPTYBridge, error) {
	if tmuxSession == "" {
		return nil, fmt.Errorf("tmux session name is required")
	}
	exists, err := tmuxSessionExists(tmuxSession, tmuxSocketName)
	if err != nil {
		return nil, fmt.Errorf("check tmux session %q: %w", tmuxSession, err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrTmuxSessionNotFound, tmuxSession)
	}

	return newPTYBridge(tmuxAttachCommand(tmuxSession, tmuxSocketName), sessionID, tmuxSession, tmuxSocketName, writer, mapErr)
}

// newPTYBridge starts cmd in a local PTY and streams its output over writer.
// It is the shared foundation for both the local tmux attach bridge (via
// newTmuxPTYBridge, which existence-checks the tmux session first) and the
// remote SSH attach bridge (handleRemoteSessionWS, which has no local tmux
// session to check — the remote side owns that). tmuxSession/tmuxSocketName
// are empty for the remote (non-tmux) caller.
func newPTYBridge(cmd *exec.Cmd, sessionID, tmuxSession, tmuxSocketName string, writer *wsConnWriter, mapErr wsAttachErrorFunc) (*tmuxPTYBridge, error) {
	if cmd == nil {
		return nil, fmt.Errorf("command is required")
	}
	if writer == nil {
		return nil, fmt.Errorf("writer is required")
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	b := &tmuxPTYBridge{
		sessionID:      sessionID,
		tmuxSession:    tmuxSession,
		tmuxSocketName: tmuxSocketName,
		writer:         writer,
		cmd:            cmd,
		ptmx:           ptmx,
		done:           make(chan struct{}),
		startedAt:      time.Now(),
		mapErr:         mapErr,
	}

	go b.streamOutput()
	return b, nil
}

// snapshotPtmx returns the current ptmx *os.File under RLock. It returns
// nil if the bridge has been Closed. Consumers (WriteInput, streamOutput)
// use this to read the field race-free with respect to Close()'s
// Lock-guarded `b.ptmx = nil` store. The returned *os.File itself is
// goroutine-safe with respect to Close (Go's runtime poller handles
// Close vs. blocked I/O), so callers need not hold the RLock during the
// I/O syscall. (V1.9 T5, race-review 2.1.)
func (b *tmuxPTYBridge) snapshotPtmx() *os.File {
	b.ptmxMu.RLock()
	defer b.ptmxMu.RUnlock()
	return b.ptmx
}

// isRemote reports whether this bridge attaches to a session on another host
// over ssh rather than to a local tmux session. tmuxSession is the signal:
// newTmuxPTYBridge rejects an empty tmux session name outright, so every local
// bridge carries one, and handleRemoteSessionWS — the only caller that builds
// a bridge with no local tmux session behind it — is the only source of "".
func (b *tmuxPTYBridge) isRemote() bool {
	return b.tmuxSession == ""
}

func (b *tmuxPTYBridge) streamOutput() {
	defer close(b.done)

	buf := make([]byte, 4096)
	for {
		ptmx := b.snapshotPtmx()
		if ptmx == nil {
			return
		}
		n, err := ptmx.Read(buf)
		if n > 0 {
			b.wroteOutput.Store(true)
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if writeErr := b.writer.WriteBinary(chunk); writeErr != nil {
				b.Close()
				return
			}
		}

		if err != nil {
			switch {
			case b.isRemote() && !b.closing.Load() && time.Since(b.startedAt) < quickExitGrace && b.attachFailed():
				// The remote attach command died without ever giving the user
				// a session — ssh could not reach the host, auth was rejected,
				// or the remote agent-deck binary is missing. terminal_attached
				// already fired, so without this the terminal looks attached
				// and alive but is silently dead. Reuse the same code/message/
				// hint an attach() failure would have produced.
				//
				// attachFailed is what separates that from a remote session
				// that genuinely ran and ended inside the grace window; see
				// its doc comment.
				//
				// Deliberately remote-only: a LOCAL tmux attach that exits
				// this fast is an ordinary detach/close, and TerminalPanel.js
				// does not disable reconnect on TERMINAL_ATTACH_FAILED, so
				// promoting it to a fatal frame would only print a stray
				// `[error:TERMINAL_ATTACH_FAILED]` line where the user
				// previously got a clean session_closed.
				code, message, hint := "TERMINAL_ATTACH_FAILED", "attach process exited shortly after connecting", "Check the server logs for details."
				if b.mapErr != nil {
					code, message, hint = b.mapErr(err)
				}
				_ = b.writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      code,
					Message:   message,
					Hint:      hint,
					SessionID: b.sessionID,
					Time:      time.Now().UTC(),
				})
			case !errors.Is(err, io.EOF):
				_ = b.writer.WriteJSON(wsServerMessage{
					Type:      "status",
					Event:     "session_closed",
					SessionID: b.sessionID,
					Time:      time.Now().UTC(),
				})
			}
			b.Close()
			return
		}
	}
}

// quickExitGrace bounds how soon after start a REMOTE attach command's exit
// is treated as an attach failure (see streamOutput) rather than a normal
// detach of a session that ran for a while.
//
// It is derived from session.SSHConnectTimeout rather than picked by feel,
// because the window has to cover the ssh dial itself. defaultRemoteAttachCommand
// runs ssh with `-o ConnectTimeout=<session.SSHConnectTimeout>` (see
// sessionSSHConnOpts, reached via SSHRunner.AttachArgs), so the single most
// likely real failure — the remote host down or unreachable, exactly what the
// REMOTE_ATTACH_FAILED hint tells the user to check — has ssh block for the
// whole connect timeout and only *then* exit. With a grace shorter than that,
// the read error fell through to the session_closed branch: the client sets
// terminalAttached = false but leaves wsReconnectEnabled true, so the terminal
// reconnects forever, paying another full dial each time, with no banner and no
// `[error:CODE]` line. The margin covers ssh's own startup plus the PTY read
// waking up after the process exits.
var quickExitGrace = session.SSHConnectTimeout + quickExitGraceMargin

// quickExitGraceMargin is the headroom added on top of the ssh connect timeout
// so a dial that times out at exactly ConnectTimeout still lands inside the
// grace window.
const quickExitGraceMargin = 5 * time.Second

// attachFailed decides whether a remote attach command's death inside
// quickExitGrace was a failure to attach at all, or a remote session that
// genuinely ran and then ended.
//
// Round 7 (#2): elapsed time cannot tell those apart, and round 6 widened the
// window to 15s to cover the ssh dial. That made "attach, watch the agent
// finish, get told to check your ssh" a normal path rather than a corner: the
// user got a fatal REMOTE_ATTACH_FAILED banner with a false diagnosis, and the
// client disables reconnect on that code.
//
// Two independent signals mean "never attached", and either is enough:
//
//   - Exit status 255 (sshFailureExitCode). `ssh -tt` reserves 255 for its own
//     errors and otherwise passes the remote command's status through, so this
//     is the definitive "the dial failed" signal, and it stays true no matter
//     how much ssh printed on its way out.
//   - No output at all. NOTE: this is deliberately not the only test, because
//     the intuitive form of it is wrong. A failed dial DOES write to the PTY —
//     pty.Start points the child's stderr at the same pts, so
//     "ssh: connect to host … Operation timed out" is read here as ordinary
//     output (verified in-tree against a real ssh against an unresolvable
//     host). Keying solely on "wrote nothing" would therefore have silently
//     un-fixed round 6's unreachable-host case. It stays as a second clause
//     because a command that died without emitting a single byte never showed
//     the user a session either.
//
// Anything else — output was produced and the process exited with the remote
// side's own status — is a session that ran, and falls through to the ordinary
// EOF/session_closed handling.
func (b *tmuxPTYBridge) attachFailed() bool {
	if !b.wroteOutput.Load() {
		return true
	}
	return b.waitExitCode() == sshFailureExitCode
}

// waitExitCode reaps the attach process and returns its exit status, or
// exitCodeUnknown if it could not be determined within reapWaitTimeout.
func (b *tmuxPTYBridge) waitExitCode() int {
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.reap()
	}()
	select {
	case <-done:
		return int(b.exitCode.Load())
	case <-time.After(reapWaitTimeout):
		return exitCodeUnknown
	}
}

// reap runs the one permitted cmd.Wait and records the exit status. Safe to
// call from several goroutines and any number of times; only the first call
// waits, the rest block until it finishes.
func (b *tmuxPTYBridge) reap() {
	b.waitOnce.Do(func() {
		code := exitCodeUnknown
		if b.cmd != nil {
			_ = b.cmd.Wait()
			// Set before exitCode so any observer that sees a status has also
			// seen the PID become unsafe to signal.
			b.reaped.Store(true)
			if b.cmd.ProcessState != nil {
				code = b.cmd.ProcessState.ExitCode()
			}
		}
		b.exitCode.Store(int32(code))
	})
}

func (b *tmuxPTYBridge) WriteInput(data string) error {
	if b == nil {
		return fmt.Errorf("bridge not initialized")
	}
	if data == "" {
		return nil
	}
	ptmx := b.snapshotPtmx()
	if ptmx == nil {
		return fmt.Errorf("bridge not initialized")
	}
	_, err := ptmx.Write([]byte(data))
	return err
}

func (b *tmuxPTYBridge) Resize(cols, rows int) error {
	if b == nil {
		return fmt.Errorf("bridge not initialized")
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid dimensions: cols=%d rows=%d", cols, rows)
	}
	if cols < 10 || rows < 3 {
		return fmt.Errorf("dimensions too small for a usable terminal: cols=%d rows=%d", cols, rows)
	}

	b.ptmxMu.RLock()
	defer b.ptmxMu.RUnlock()
	if b.ptmx == nil {
		return fmt.Errorf("bridge not initialized")
	}

	// Resize the local PTY master. This sends SIGWINCH to the tmux attach
	// process. Because the attach client (see tmuxAttachCommand) is no longer
	// flagged `-f ignore-size`, the tmux server now uses this client's PTY
	// size as its declared geometry and re-arbitrates the window dimensions
	// per the session's `window-size` policy (`largest` — set at Session.Start
	// in internal/tmux/tmux.go). The previous `tmux resize-window` call here
	// was removed because it implicitly flipped the session option to
	// `window-size=manual` and pinned the window to the web viewport, which
	// dragged native attached clients (Ghostty, iTerm) along with it. Letting
	// tmux do the arbitration via `largest` keeps every client at the size of
	// the biggest viewer; smaller clients see a clipped portion of the larger
	// window content (no dot-filled void cells).
	if err := pty.Setsize(b.ptmx, &pty.Winsize{
		Rows: uint16(rows), // #nosec G115 -- terminal rows fits in uint16; PTY ABI enforces this
		Cols: uint16(cols), // #nosec G115 -- terminal cols fits in uint16; PTY ABI enforces this
	}); err != nil {
		return fmt.Errorf("resize pty: %w", err)
	}

	return nil
}

func (b *tmuxPTYBridge) Close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		// Set before the fd goes away: streamOutput's blocked Read wakes with
		// an error the moment ptmx closes, and it must already be able to see
		// that we are the cause (see the closing field).
		b.closing.Store(true)
		b.ptmxMu.Lock()
		if b.ptmx != nil {
			_ = b.ptmx.Close()
			b.ptmx = nil
		}
		b.ptmxMu.Unlock()
		// !b.reaped: never signal a PID that cmd.Wait has already returned
		// for — the kernel may have handed it to an unrelated process by now
		// (round 8 #1, see the reaped field).
		if !b.reaped.Load() && b.cmd != nil && b.cmd.Process != nil {
			terminate := b.terminateProcessGroup
			if terminate == nil {
				terminate = defaultTerminateProcessGroup
			}
			terminate(b.cmd.Process)
		}
		b.reap()
	})
}

// defaultTerminateProcessGroup SIGTERMs the attach process's whole group so
// the tmux/ssh client and anything it spawned go down together, falling back
// to signalling the process alone when it has no group of its own. Callers
// must have established that the process has not been reaped yet.
func defaultTerminateProcessGroup(proc *os.Process) {
	pgid, err := syscall.Getpgid(proc.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		return
	}
	_ = proc.Kill()
}

// tmuxHasSessionProbeTimeout bounds the has-session existence probe. The web
// bridge re-probes on every (re)connect, which is a cadence: on tmux 3.0a a
// client that has exhausted its fd table spins at 100% CPU in EMFILE retries
// and never exits, so an unbounded probe both hangs the connect request and
// leaks a core-burning orphan per reconnect attempt.
const tmuxHasSessionProbeTimeout = 3 * time.Second

func tmuxSessionExists(name, socketName string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxHasSessionProbeTimeout)
	defer cancel()
	cmd := tmuxCommandContext(ctx, socketName, "has-session", "-t", name)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}

	msg := strings.TrimSpace(string(output))
	if msg == "" {
		msg = err.Error()
	}
	return false, fmt.Errorf("tmux has-session failed: %s", msg)
}

// tmuxCommand assembles an `exec.Cmd` for tmux, selecting the server in the
// following precedence order: (1) explicit socketName from the caller — the
// session's stored TmuxSocketName captured at creation time, passed through
// as tmux `-L <name>`; (2) TMUX env var's socket path (legacy web-in-tmux
// behavior), passed through as `-S <path>`; (3) tmux's default server. The
// legacy env-based fallback is preserved so running `agent-deck web` inside
// an existing tmux pane keeps working for users who haven't opted into the
// new per-session socket config (issue #687 phase 1).
// Callers that poll on a cadence must use tmuxCommandContext with a deadline
// instead — see tmuxHasSessionProbeTimeout. This unbounded form is correct only
// for long-lived interactive commands such as attach-session.
func tmuxCommand(socketName string, args ...string) *exec.Cmd {
	return tmuxCommandContext(context.Background(), socketName, args...)
}

// tmuxCommandContext is the deadline-carrying variant of tmuxCommand. A context
// with a timeout lets exec.CommandContext SIGKILL a tmux client that has wedged
// on its own leaked fd table rather than blocking the caller forever.
//
// Every argv it builds carries tmux's global `-u` (#1867). This wrapper is the
// web daemon's counterpart to internal/tmux's tmuxArgs, and the daemon is the
// worst-affected process in the codebase: run under systemd or launchd it has
// no LANG/LC_* at all, so without `-u` tmux rewrites every non-ASCII byte it
// returns to "_". Unlike internal/tmux there is no interactive carve-out here,
// because the web attach's terminal is not the user's — it is xterm.js in a
// browser, which is unconditionally UTF-8. That is the same reasoning that
// already pins TERM=xterm-256color in tmuxAttachCommand below.
func tmuxCommandContext(ctx context.Context, socketName string, args ...string) *exec.Cmd {
	utf8Args := tmuxutf8.Prepend(args)
	// Explicit per-session socket name wins — this is the v1.7.50 path.
	if trimmed := strings.TrimSpace(socketName); trimmed != "" {
		finalArgs := append([]string{tmuxutf8.Flag, "-L", trimmed}, utf8Args[1:]...)
		cmd := exec.CommandContext(ctx, "tmux", finalArgs...)
		// Unset TMUX so tmux-in-tmux guards don't trip: we are explicitly
		// directing this to a different server than the one we're in.
		cmd.Env = environWithoutTMUX(os.Environ())
		return cmd
	}

	socketPath, hasSocket := tmuxSocketFromEnv()

	finalArgs := utf8Args
	if hasSocket {
		finalArgs = append([]string{tmuxutf8.Flag, "-S", socketPath}, utf8Args[1:]...)
	}

	cmd := exec.CommandContext(ctx, "tmux", finalArgs...)
	if hasSocket {
		cmd.Env = environWithoutTMUX(os.Environ())
	}
	return cmd
}

func tmuxAttachCommand(sessionName, socketName string) *exec.Cmd {
	// Web's attach is now a normal client whose PTY size participates in tmux's
	// `window-size=largest` arbitration (set at Session.Start). Previously we
	// passed `-f ignore-size` together with a manual `tmux resize-window` call
	// in (*tmuxPTYBridge).Resize; the manual resize-window flipped the session
	// option to `window-size=manual` and pinned the window to the web viewport
	// for ALL attached clients (Ghostty, iTerm) — the dots-in-window symptom.
	// With largest in effect, every client sees content sized to the biggest
	// viewer; smaller clients see a clipped portion rather than dot-filled void.
	// `-u` forces UTF-8 output regardless of the daemon's locale. Same class of
	// bug as the TERM handling below: when the web daemon runs under launchd/
	// systemd its environment carries no LANG/LC_*, so tmux treats this client as
	// non-UTF-8 and downgrades every non-ASCII glyph (⏵, box-drawing, spinners)
	// to '_' on the wire — the browser/mobile terminal then shows '_' where the
	// agent drew Unicode, while tmux's own buffer (capture-pane) stays correct.
	cmd := tmuxCommand(socketName, "-u", "attach-session", "-t", sessionName)
	// Guarantee a usable TERM for the attach client. When the web daemon runs
	// under launchd/systemd its environment carries no TERM, and a tmux attach
	// client with an empty/unset TERM aborts with "open terminal failed:
	// terminal does not support clear" — the web terminal then never renders
	// and the browser's resize message races the dying bridge into
	// RESIZE_FAILED. The browser side is xterm.js, so xterm-256color is the
	// correct client terminal type. A TERM the daemon legitimately inherited
	// (e.g. `agent-deck web` launched from an interactive shell) is preserved.
	cmd.Env = ensureTERM(cmd.Env)
	return cmd
}

// ensureTERM returns env with a non-empty TERM guaranteed. A nil env (the
// inherit-parent default) is materialized from os.Environ() first so the
// appended TERM is not dropped. An existing non-empty TERM is left untouched;
// an existing but empty TERM (`TERM=`) is replaced in place rather than
// shadowed by a duplicate entry — execve passes the slice verbatim and getenv
// resolution order for duplicate keys is unspecified, so a trailing append
// could leave the empty value winning and tmux would still abort.
func ensureTERM(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	for i, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			if strings.TrimSpace(kv[len("TERM="):]) == "" {
				env[i] = "TERM=xterm-256color"
			}
			return env
		}
	}
	return append(env, "TERM=xterm-256color")
}

func tmuxSocketFromEnv() (string, bool) {
	raw := strings.TrimSpace(os.Getenv("TMUX"))
	if raw == "" {
		return "", false
	}

	socketPart := raw
	if strings.Contains(raw, ",") {
		socketPart = strings.SplitN(raw, ",", 2)[0]
	}

	socketPart = strings.TrimSpace(socketPart)
	if socketPart == "" {
		return "", false
	}
	return socketPart, true
}

func environWithoutTMUX(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "TMUX=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	return filtered
}
