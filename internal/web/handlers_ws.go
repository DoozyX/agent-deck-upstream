package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/gorilla/websocket"
)

type wsClientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type wsServerMessage struct {
	Type      string    `json:"type"` // status, error
	Event     string    `json:"event,omitempty"`
	Code      string    `json:"code,omitempty"`
	Message   string    `json:"message,omitempty"`
	Hint      string    `json:"hint,omitempty"` // #782: actionable next step for terminal-fatal errors
	SessionID string    `json:"sessionId,omitempty"`
	Profile   string    `json:"profile,omitempty"`
	ReadOnly  bool      `json:"readOnly,omitempty"`
	Time      time.Time `json:"time,omitempty"`
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     allowWSOrigin,
}

// WebSocket keepalive tunables. The terminal socket sends protocol-level pings
// every wsPingPeriod and tears the connection down if no pong (or any message)
// arrives within wsPongWait, so a vanished peer can't leave the read loop — and
// its tmux attach bridge — blocked forever. Overridable in tests.
var (
	wsPongWait   = 60 * time.Second
	wsPingPeriod = (wsPongWait * 9) / 10
)

func allowWSOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil || originURL.Host == "" {
		return false
	}

	return strings.EqualFold(originURL.Host, r.Host)
}

func (s *Server) handleSessionWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	if !s.authorizeWSRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	const prefix = "/ws/session/"
	sessionID := strings.TrimPrefix(r.URL.Path, prefix)
	if sessionID == "" || strings.Contains(sessionID, "/") {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "session id is required")
		return
	}
	snapshot, err := s.menuData.LoadMenuSnapshot()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load session data")
		return
	}

	menuSession, found := snapshotSessionByID(snapshot, sessionID)
	if !found {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	attach := func(writer *wsConnWriter) (*tmuxPTYBridge, error) {
		if menuSession.TmuxSession == "" {
			return nil, nil
		}
		return newTmuxPTYBridge(menuSession.TmuxSession, menuSession.TmuxSocketName, sessionID, writer, mapLocalAttachError)
	}
	s.serveTerminalWS(w, r, sessionID, snapshot.Profile, attach, mapLocalAttachError)
}

// handleRemoteSessionWS attaches a web terminal to a session on a configured
// `[remotes.<name>]` SSH host, the same way TUI's SSHRunner.Attach does but
// piping through a local PTY instead of os.Stdin (design doc:
// .agent-deck/2026-09-04-web-remote-terminal/design/design.md). Attach only —
// there is no remote mutation surface (start/stop/fork/etc.).
func (s *Server) handleRemoteSessionWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.authorizeWSRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}

	const prefix = "/ws/remote/"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.SplitN(rest, "/session/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(parts[1], "/") {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "remote name and session id are required")
		return
	}
	remoteName, sessionID := parts[0], parts[1]

	cfg, err := session.LoadUserConfig()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load user config")
		return
	}
	remoteCfg, ok := cfg.Remotes[remoteName]
	if !ok {
		writeAPIError(w, http.StatusNotFound, ErrCodeRemoteNotFound, "remote not found")
		return
	}
	if err := session.ValidateSSHHost(remoteCfg.Host); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}

	if s.remoteFleet == nil {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "remote fleet is unavailable")
		return
	}
	// Stale/offline remotes still count here: their last-known sessions stay
	// in the snapshot, and the attach below fails at ssh with the error
	// streamed to the terminal rather than a 404 for a session that may well
	// still be running.
	if !remoteFleetHasSession(s.remoteFleet.Snapshot(), remoteName, sessionID) {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "session not found")
		return
	}

	attachCmdFn := s.cfg.RemoteAttachCommand
	if attachCmdFn == nil {
		attachCmdFn = defaultRemoteAttachCommand
	}
	mapErr := func(error) (code, message, hint string) {
		return "REMOTE_ATTACH_FAILED", "failed to attach remote terminal",
			fmt.Sprintf("Check that ssh can reach %s from the web server host.", remoteCfg.Host)
	}
	attach := func(writer *wsConnWriter) (*tmuxPTYBridge, error) {
		return newPTYBridge(attachCmdFn(remoteName, remoteCfg, sessionID), sessionID, "", "", writer, mapErr)
	}
	s.serveTerminalWS(w, r, sessionID, "", attach, mapErr)
}

// defaultRemoteAttachCommand builds the real `ssh ... agent-deck session
// attach <id>` command TUI's SSHRunner.Attach runs, wired through a local PTY
// instead of os.Stdin. Overridden by Config.RemoteAttachCommand in tests and
// the JS e2e fixture so neither has to spawn real ssh.
func defaultRemoteAttachCommand(name string, cfg session.RemoteConfig, sessionID string) *exec.Cmd {
	runner := session.NewSSHRunner(name, cfg)
	sshArgs := runner.AttachArgs(sessionID)
	// #nosec G204 G702 -- "ssh" is a fixed binary. sshArgs is built by
	// SSHRunner.AttachArgs from a [remotes.*] config entry (host validated by
	// handleRemoteSessionWS's session.ValidateSSHHost call before this
	// function ever runs, closing ssh-option injection via the host) and the
	// attacker-controlled sessionID, which AttachArgs embeds into the remote
	// command string via buildRemoteCommand's shellQuote (internal/session/
	// ssh.go) — that's what makes a crafted sessionID safe to pass through
	// here, not ValidateSSHHost, which only ever sees the host. Same
	// validate-then-exec.Command shape as SSHRunner.Attach's own identical
	// call.
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Env = ensureTERM(cmd.Env)
	return cmd
}

func remoteFleetHasSession(snapshot session.RemoteFleetSnapshot, remoteName, sessionID string) bool {
	for _, remote := range snapshot.Remotes {
		if remote.Name != remoteName {
			continue
		}
		for _, sess := range remote.Sessions {
			if sess.ID == sessionID {
				return true
			}
		}
		return false
	}
	return false
}

// wsAttachFunc opens the terminal bridge for an already-upgraded connection.
// Returning (nil, nil) means "no bridge for this session" (mirrors local's
// menuSession.TmuxSession == "" case: connected, but nothing to attach to
// yet) — distinct from a non-nil error, which is a failed attach attempt.
type wsAttachFunc func(writer *wsConnWriter) (*tmuxPTYBridge, error)

// wsAttachErrorFunc maps a failed attach into the code/message/hint the
// client renders. Local and remote report different codes for "the thing on
// the other end doesn't exist" (TMUX_SESSION_NOT_FOUND vs REMOTE_ATTACH_FAILED).
type wsAttachErrorFunc func(err error) (code, message, hint string)

func mapLocalAttachError(err error) (code, message, hint string) {
	code, message, hint = "TERMINAL_ATTACH_FAILED", "failed to attach terminal bridge", "Check the server logs for details."
	// #782: terminal-fatal errors get an actionable hint so the WebUI can
	// render guidance instead of repeating an opaque `[error:CODE]` line on
	// every reconnect attempt.
	if errors.Is(err, ErrTmuxSessionNotFound) {
		code = "TMUX_SESSION_NOT_FOUND"
		message = "tmux session is not available"
		hint = "The tmux session for this entry no longer exists. Restart it from the sidebar (Restart icon, or press 'r' with the row focused) to create a fresh tmux session."
	}
	return code, message, hint
}

// serveTerminalWS is the shared body of handleSessionWS and
// handleRemoteSessionWS from the upgrade onward: keepalive ping/pong,
// connected/ready/terminal_attached status frames, and the input/resize/ping
// message loop. attach opens the terminal bridge (tmux existence-check +
// local attach, or ssh attach — the two handlers differ only in how); mapErr
// translates an attach failure into the error frame's code/message/hint.
func (s *Server) serveTerminalWS(w http.ResponseWriter, r *http.Request, sessionID, profile string, attach wsAttachFunc, mapErr wsAttachErrorFunc) {
	// sessionID is attacker-controlled (raw URL path segment); every log call
	// below must use this sanitized copy, never sessionID itself, so a crafted
	// CRLF/control-char id can't forge fake log lines (go/log-injection).
	logSessionID := logging.SanitizeValue(sessionID)

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Keepalive so dead peers are detected and the deferred bridge teardown
	// actually runs. Without this, an idle session whose client vanished
	// (network drop, mobile app killed) leaves the read loop below blocked
	// forever in ReadMessage, so `defer bridge.Close()` never fires and the
	// tmux attach client leaks. Under `window-size largest` a single leaked
	// wide client then pins the shared window geometry for every other viewer
	// — the symptom being a phone terminal that stops wrapping to its screen.
	// We send protocol-level pings and require a pong within pongWait; both
	// browsers and URLSessionWebSocketTask answer pings automatically.
	pongWait, pingPeriod := wsPongWait, wsPingPeriod
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ticker.C:
				// WriteControl may be called concurrently with the writer. A
				// transient send failure (e.g. brief writer-lock contention)
				// must not permanently stop pings and strand a healthy peer —
				// the read deadline is the sole liveness arbiter, so just retry
				// on the next tick. A genuinely dead conn is reaped read-side,
				// after which close(stopPing) ends this goroutine.
				_ = conn.WriteControl(websocket.PingMessage, nil,
					time.Now().Add(10*time.Second))
			}
		}
	}()

	writer := newWSConnWriter(conn)

	_ = writer.WriteJSON(wsServerMessage{
		Type:      "status",
		Event:     "connected",
		SessionID: sessionID,
		Profile:   profile,
		ReadOnly:  s.cfg.ReadOnly,
		Time:      time.Now().UTC(),
	})
	_ = writer.WriteJSON(wsServerMessage{
		Type:      "status",
		Event:     "ready",
		SessionID: sessionID,
		Time:      time.Now().UTC(),
	})

	// attach is always a non-nil closure at both call sites (handleSessionWS,
	// handleRemoteSessionWS); no nil-guard needed here.
	bridge, err := attach(writer)
	if err != nil {
		logging.ForComponent(logging.CompWeb).Error("terminal_attach_failed",
			slog.String("session_id", logSessionID),
			slog.String("error", err.Error()))
		code, message, hint := mapErr(err)
		_ = writer.WriteJSON(wsServerMessage{
			Type:      "error",
			Code:      code,
			Message:   message,
			Hint:      hint,
			SessionID: sessionID,
			Time:      time.Now().UTC(),
		})
	} else if bridge != nil {
		defer bridge.Close()
		_ = writer.WriteJSON(wsServerMessage{
			Type:      "status",
			Event:     "terminal_attached",
			SessionID: sessionID,
			Time:      time.Now().UTC(),
		})
	}

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			// A keepalive read-deadline reap surfaces as a net.Error timeout,
			// which IsUnexpectedCloseError treats as expected — so log it
			// explicitly, otherwise a dead-peer teardown leaves no trace and is
			// indistinguishable from a normal close in production logs.
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				logging.ForComponent(logging.CompWeb).Warn("websocket_keepalive_timeout",
					slog.String("session_id", logSessionID),
					slog.String("error", logging.SanitizeValue(err.Error())))
			} else if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseNoStatusReceived,
			) {
				// err may be a *websocket.CloseError whose Text is the
				// peer-supplied close reason (attacker-controlled) — sanitize
				// it same as logSessionID above (go/log-injection).
				logging.ForComponent(logging.CompWeb).Warn("websocket_closed_unexpectedly",
					slog.String("session_id", logSessionID),
					slog.String("error", logging.SanitizeValue(err.Error())))
			}
			return
		}
		// A real message is also liveness — extend the deadline alongside pongs.
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))

		var msg wsClientMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			_ = writer.WriteJSON(wsServerMessage{
				Type:      "error",
				Code:      "INVALID_MESSAGE",
				Message:   "invalid json payload",
				SessionID: sessionID,
				Time:      time.Now().UTC(),
			})
			continue
		}

		switch msg.Type {
		case "ping":
			_ = writer.WriteJSON(wsServerMessage{
				Type:      "status",
				Event:     "pong",
				SessionID: sessionID,
				Time:      time.Now().UTC(),
			})
		case "input":
			if s.cfg.ReadOnly {
				_ = writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      "READ_ONLY",
					Message:   "input is disabled in read-only mode",
					SessionID: sessionID,
					Time:      time.Now().UTC(),
				})
				continue
			}
			if bridge == nil {
				_ = writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      "NO_TERMINAL_BRIDGE",
					Message:   "terminal bridge is not attached",
					SessionID: sessionID,
					Time:      time.Now().UTC(),
				})
				continue
			}
			if err := bridge.WriteInput(msg.Data); err != nil {
				_ = writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      "INPUT_WRITE_FAILED",
					Message:   "failed to send input to terminal",
					SessionID: sessionID,
					Time:      time.Now().UTC(),
				})
			}
		case "resize":
			if bridge == nil {
				_ = writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      "NO_TERMINAL_BRIDGE",
					Message:   "terminal bridge is not attached",
					SessionID: sessionID,
					Time:      time.Now().UTC(),
				})
				continue
			}
			if err := bridge.Resize(msg.Cols, msg.Rows); err != nil {
				_ = writer.WriteJSON(wsServerMessage{
					Type:      "error",
					Code:      "RESIZE_FAILED",
					Message:   "failed to resize terminal",
					SessionID: sessionID,
					Time:      time.Now().UTC(),
				})
			}
		default:
			_ = writer.WriteJSON(wsServerMessage{
				Type:      "error",
				Code:      "UNSUPPORTED_MESSAGE",
				Message:   "supported message types: ping,input,resize",
				SessionID: sessionID,
				Time:      time.Now().UTC(),
			})
		}
	}
}

func snapshotSessionByID(snapshot *MenuSnapshot, sessionID string) (*MenuSession, bool) {
	if snapshot == nil {
		return nil, false
	}
	for _, item := range snapshot.Items {
		if item.Type != MenuItemTypeSession || item.Session == nil {
			continue
		}
		if item.Session.ID == sessionID {
			return item.Session, true
		}
	}
	return nil, false
}
