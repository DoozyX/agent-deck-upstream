package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// goneTmuxSession returns a *tmux.Session pointed at an isolated socket with no
// running server, so any capture returns tmux.ErrCaptureGone.
func goneTmuxSession() *tmux.Session {
	return &tmux.Session{
		Name:       "agentdeck_gone_response_probe",
		SocketName: fmt.Sprintf("adeck-gone-resp-%d", os.Getpid()),
	}
}

// TestGetTerminalLastResponse_GonePropagatesSentinel verifies the read path no
// longer wraps a vanished pane as the opaque "failed to capture terminal
// output: ... exit status 1"; it propagates tmux.ErrCaptureGone so callers can
// degrade cleanly.
func TestGetTerminalLastResponse_GonePropagatesSentinel(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	i := &Instance{Tool: "codex", tmuxSession: goneTmuxSession()}
	_, err := i.getTerminalLastResponse()
	if !errors.Is(err, tmux.ErrCaptureGone) {
		t.Fatalf("getTerminalLastResponse = %v, want tmux.ErrCaptureGone", err)
	}
}

// TestGetLastResponseBestEffort_GoneReturnsEmpty verifies the conductor-facing
// best-effort read degrades a vanished session to an empty response (no error)
// for a non-Claude/Gemini tool — the exact case that was surfacing the scary
// error string on codex children.
func TestGetLastResponseBestEffort_GoneReturnsEmpty(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	i := &Instance{Tool: "codex", tmuxSession: goneTmuxSession()}
	resp, err := i.GetLastResponseBestEffort()
	if err != nil {
		t.Fatalf("GetLastResponseBestEffort returned error for vanished session: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a non-nil empty response, got nil")
	}
	if resp.Content != "" {
		t.Fatalf("expected empty content for vanished session, got %q", resp.Content)
	}
}

func TestGetLastResponseBestEffort_UsesFinalCaptureError(t *testing.T) {
	permissionErr := errors.New("capture permission denied")
	for _, tc := range []struct {
		name        string
		first, last error
		wantGone    bool
	}{
		{"gone then permission", tmux.ErrCaptureGone, permissionErr, false},
		{"permission then gone", permissionErr, tmux.ErrCaptureGone, true},
		{"other failure then gone", errors.New("unknown capture failure"), tmux.ErrCaptureGone, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := 0
			i := &Instance{Tool: "codex", tmuxSession: goneTmuxSession()}
			capture := func() (*ResponseOutput, error) {
				attempt++
				if attempt == 1 {
					return nil, tc.first
				}
				return nil, tc.last
			}
			resp, err := i.getLastResponseBestEffort(capture)
			if attempt != 2 {
				t.Fatalf("capture attempts = %d, want 2", attempt)
			}
			if tc.wantGone {
				if err != nil || resp == nil || resp.Content != "" {
					t.Fatalf("final gone capture = %+v, %v; want empty response", resp, err)
				}
			} else {
				if !errors.Is(err, permissionErr) {
					t.Fatalf("final permission error = %v, want %v", err, permissionErr)
				}
			}
		})
	}
}
