package session

// Duplex remote execution for `agent-deck artifacts sync`.
//
// The two existing seams cannot carry a tar:
//   - OpenStream wires stdin only and discards stdout, so a PULL has nowhere to
//     read the remote's tar from;
//   - Run/RunCommand buffer the whole output into a bytes.Buffer and default to
//     a 30s timeout, so a large pull would be both held in memory and killed
//     mid-stream (one real leg measured 127 MB).
//
// OpenExecStream is therefore a third, narrower thing: one remote agent-deck
// subprocess with both pipes attached and NO timeout of its own, whose lifetime
// the caller ends explicitly. It deliberately does not retry: a broken transfer
// is reported, and the union sync is safe to simply run again.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ExecStream is one running remote command. Close Stdin to signal EOF to the
// remote; call Wait to collect its exit status and whatever it wrote to stderr.
type ExecStream struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser

	cmd    *exec.Cmd
	stderr *strings.Builder
}

// Wait blocks until the remote command exits, and folds its stderr into the
// error so a failure explains itself instead of surfacing as "unexpected EOF"
// at the tar reader.
func (s *ExecStream) Wait() error {
	if s.cmd == nil {
		return nil
	}
	err := s.cmd.Wait()
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(s.stderr.String()); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// Close terminates the remote command without waiting for a clean exit. Safe to
// call after Wait.
func (s *ExecStream) Close() error {
	if s.Stdin != nil {
		_ = s.Stdin.Close()
	}
	if s.Stdout != nil {
		_ = s.Stdout.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	return nil
}

// OpenExecStream starts `agent-deck <args...>` on the remote with both pipes
// attached. The context bounds the subprocess; no additional timeout is applied,
// because the transfer's duration is a function of how much has diverged, not of
// how responsive the host is.
func (r *SSHRunner) OpenExecStream(ctx context.Context, args ...string) (*ExecStream, error) {
	if err := ValidateSSHHost(r.Host); err != nil {
		return nil, err
	}
	_ = os.MkdirAll(sshControlDir, 0700)

	cmd := exec.CommandContext(ctx, "ssh", r.sshBaseArgs(r.buildRemoteCommand(args...))...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("artifacts stream stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("artifacts stream stdout: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("artifacts stream start: %w", err)
	}
	return &ExecStream{Stdin: stdin, Stdout: stdout, cmd: cmd, stderr: &stderr}, nil
}
