package session

// `agent-deck artifacts sync` needs a duplex remote subprocess: the pull leg
// reads a tar off the remote's STDOUT while the push leg writes one to its
// STDIN, and neither may be buffered in memory (one real leg is 127 MB).
//
// OpenStream cannot serve: it wires stdin only and discards stdout. Run and
// RunCommand cannot serve: they buffer the whole output and default to a 30s
// timeout, which would abort a large pull mid-tar.
//
// What these pin, without a network: the duplex variant builds the same
// shell-quoted remote command as the rest of the ssh surface (the wire contract
// a bug would silently corrupt), and it refuses a host that fails host
// validation instead of shelling out with it.

import (
	"context"
	"strings"
	"testing"
)

func TestBuildRemoteCommand_ArtifactsPathsAreQuoted(t *testing.T) {
	r := NewSSHRunner("m1", RemoteConfig{
		Host:          "skletnikov@m1",
		AgentDeckPath: "/Users/skletnikov/.local/bin/agent-deck",
	})

	got := r.buildRemoteCommand("artifacts", "pack", "--root", "/Users/skletnikov/My Repos/x")

	if strings.Contains(got, "My Repos/x") && !strings.Contains(got, "'") {
		t.Errorf("root with a space reached the remote shell unquoted: %s", got)
	}
	if !strings.Contains(got, "artifacts") || !strings.Contains(got, "pack") {
		t.Errorf("subcommand missing: %s", got)
	}
	if !strings.HasPrefix(got, "'/Users/skletnikov/.local/bin/agent-deck'") &&
		!strings.HasPrefix(got, "/Users/skletnikov/.local/bin/agent-deck") {
		t.Errorf("remote command does not start with the agent-deck binary: %s", got)
	}
}

func TestOpenExecStream_RejectsAnInvalidHost(t *testing.T) {
	r := NewSSHRunner("bad", RemoteConfig{
		Host:          "-oProxyCommand=touch /tmp/pwned",
		AgentDeckPath: "agent-deck",
	})
	if _, err := r.OpenExecStream(context.Background(), "artifacts", "manifest", "--root", "/x"); err == nil {
		t.Fatal("OpenExecStream accepted a host that fails validation")
	}
}
