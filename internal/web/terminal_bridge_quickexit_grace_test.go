//go:build !windows

package web

import (
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestQuickExitGraceCoversSSHConnectTimeout pins the sizing of quickExitGrace
// against the ssh argv the remote bridge actually runs.
//
// Review round 6 (#2): the grace used to be a flat 3s while
// defaultRemoteAttachCommand runs ssh with `-o ConnectTimeout=10`. The most
// likely real failure — remote host down or unreachable, which is precisely
// what the REMOTE_ATTACH_FAILED hint asks the user to check — therefore had
// ssh block for ~10s and then exit, missing the quick-exit window entirely.
// The read error fell through to the session_closed branch, which on the
// client clears terminalAttached but leaves wsReconnectEnabled true: the
// terminal reconnects forever, paying another full 10s dial each round, with
// no banner and no `[error:CODE]` line.
//
// This test reads the timeout out of the real attach argv rather than a
// constant, so bumping ConnectTimeout in sessionSSHConnOpts without bumping
// the grace fails here instead of silently reintroducing the reconnect spam.
func TestQuickExitGraceCoversSSHConnectTimeout(t *testing.T) {
	cmd := defaultRemoteAttachCommand("m1", session.RemoteConfig{Host: "example.invalid"}, "sess-1")
	if cmd == nil {
		t.Fatal("defaultRemoteAttachCommand returned nil")
	}

	re := regexp.MustCompile(`^ConnectTimeout=(\d+)$`)
	var connectTimeout time.Duration
	for _, arg := range cmd.Args {
		m := re.FindStringSubmatch(arg)
		if m == nil {
			continue
		}
		secs, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("parse ConnectTimeout %q: %v", arg, err)
		}
		connectTimeout = time.Duration(secs) * time.Second
		break
	}
	if connectTimeout == 0 {
		t.Fatalf("remote attach argv carries no ConnectTimeout option: %v", cmd.Args)
	}

	if quickExitGrace <= connectTimeout {
		t.Fatalf("quickExitGrace = %s does not cover the ssh dial (ConnectTimeout=%s); "+
			"an unreachable host exits after the connect timeout and would fall through "+
			"to session_closed, leaving the client reconnecting forever with no banner",
			quickExitGrace, connectTimeout)
	}
}
