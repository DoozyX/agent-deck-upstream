package main

// The ssh-backed artifactRemote: the transport, and nothing else.
//
// Every decision about what to move lives in artifacts_sync_cmd.go and runs
// identically on both machines. This file only carries bytes, which is why the
// sync logic is provable against a temp directory: swapping this implementation
// for a local one changes no behavior.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type sshArtifactRemote struct {
	name   string
	runner *session.SSHRunner
}

// openArtifactRemote resolves a registered remote name, or a bare user@host for
// a destination that was never registered.
func openArtifactRemote(target string) (artifactRemote, error) {
	if strings.Contains(target, "@") {
		return &sshArtifactRemote{
			name:   target,
			runner: session.NewSSHRunner(target, session.RemoteConfig{Host: target, AgentDeckPath: "agent-deck"}),
		}, nil
	}
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	rc, ok := cfg.Remotes[target]
	if !ok {
		return nil, errUnknownArtifactRemote
	}
	return &sshArtifactRemote{name: target, runner: session.NewSSHRunner(target, rc)}, nil
}

func (s *sshArtifactRemote) Home(ctx context.Context) (string, error) {
	out, err := s.runner.RunCommand(ctx, "artifacts", "home")
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(string(out))
	if home == "" {
		return "", fmt.Errorf("remote %s did not report a home directory", s.name)
	}
	return home, nil
}

func (s *sshArtifactRemote) Manifest(ctx context.Context, root string) ([]artifactEntry, bool, error) {
	out, err := s.runner.RunCommand(ctx, "artifacts", "manifest", "--root", root)
	if err != nil {
		return nil, false, err
	}
	var payload artifactManifestPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		// An old remote binary has no `artifacts` verb and answers with usage
		// text. Say so, rather than reporting an unparseable manifest.
		return nil, false, fmt.Errorf("remote %s did not return a manifest (run `agent-deck remote update %s`): %w",
			s.name, s.name, err)
	}
	return payload.Entries, payload.Present, nil
}

func (s *sshArtifactRemote) Pull(ctx context.Context, root string, paths []string, dst io.Writer) error {
	stream, err := s.runner.OpenExecStream(ctx, "artifacts", "pack", "--root", root)
	if err != nil {
		return err
	}
	defer stream.Close()
	// The file list goes in before the tar comes out: `artifacts pack` reads
	// stdin to EOF before writing a byte, so this cannot deadlock.
	if _, err := io.WriteString(stream.Stdin, strings.Join(paths, "\n")+"\n"); err != nil {
		return fmt.Errorf("send file list to %s: %w", s.name, err)
	}
	if err := stream.Stdin.Close(); err != nil {
		return fmt.Errorf("close file list to %s: %w", s.name, err)
	}
	if _, err := io.Copy(dst, stream.Stdout); err != nil {
		return fmt.Errorf("read artifacts from %s: %w", s.name, err)
	}
	return stream.Wait()
}

func (s *sshArtifactRemote) Push(ctx context.Context, root string, src io.Reader) (int, error) {
	stream, err := s.runner.OpenExecStream(ctx, "artifacts", "unpack", "--root", root)
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	if _, err := io.Copy(stream.Stdin, src); err != nil {
		return 0, fmt.Errorf("send artifacts to %s: %w", s.name, err)
	}
	if err := stream.Stdin.Close(); err != nil {
		return 0, fmt.Errorf("finish sending to %s: %w", s.name, err)
	}
	out, readErr := io.ReadAll(stream.Stdout)
	if err := stream.Wait(); err != nil {
		return 0, err
	}
	if readErr != nil {
		return 0, readErr
	}
	var written int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &written); err != nil {
		// The remote extracted successfully but did not report a count. The
		// files are there; only the number is unknown, so do not fail the sync.
		return 0, nil
	}
	return written, nil
}
