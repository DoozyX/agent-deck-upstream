// Tests for Session.LaunchAs (v1.7.21 defense-in-depth).
//
// LaunchAs is the new config-driven spawn-mode selector that sits ABOVE
// LaunchInUserScope. Values: "scope" | "service" | "direct" | "auto" | "".
// Empty preserves legacy LaunchInUserScope behavior so existing users on
// v1.7.20 get zero behavior change until they opt in.
//
// These tests pin the invariants of startCommandSpec(): exact argv shape
// for each mode, case-insensitivity, whitespace-tolerance, invalid-value
// fallback, and regression guard on the legacy scope argv shape.
//
// See .planning/v1721-scope-to-service/PLAN.md for the full data-flow
// trace and scope boundaries.
package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStartCommandSpec_LaunchAs_Service_UsesServiceForm pins the argv
// shape for service mode. Restart=on-failure and Type=forking are the
// two properties whose absence would silently defeat the entire feature
// (Type=simple was the pre-v1.7.21 pre-check failure mode: service marks
// itself inactive the moment tmux daemonizes, NRestarts stays 0 forever).
func TestStartCommandSpec_LaunchAs_Service_UsesServiceForm(t *testing.T) {
	s := &Session{
		Name:     "agentdeck_test-service_1234abcd",
		WorkDir:  "/tmp/project",
		LaunchAs: "service",
	}
	pinInitialWindowSize(t, 173, 41, true) // #1694 birth size

	launcher, args := s.startCommandSpec("/tmp/project", "")
	require.Equal(t, "systemd-run", launcher, "service mode must spawn via systemd-run")

	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "--user", "service invocation must be --user scoped")
	assert.Contains(t, joined, "--unit agentdeck-tmux-agentdeck-test-service-1234abcd.service",
		"service mode unit name must carry .service suffix so systemd treats it as a service not a scope")
	assert.Contains(t, joined, "--property=Type=forking",
		"Type=forking is the ONLY Type= that survives tmux daemonization — Type=simple would mark the service dead immediately")
	assert.Contains(t, joined, "--property=Restart=on-failure",
		"Restart=on-failure is the entire point of v1.7.21 — its absence silently breaks the feature")
	assert.Contains(t, joined, "--property=RestartSec=")
	assert.Contains(t, joined, "--property=KillMode=control-group",
		"KillMode=control-group ensures systemctl stop kills tmux cleanly via cgroup")

	assert.NotContains(t, joined, "--scope",
		"service mode MUST NOT include --scope (would conflict and produce an invalid unit)")
	assert.NotContains(t, joined, "--collect",
		"--collect removes the unit when inactive — breaks Restart=on-failure reattach")

	// The tmux args shape after the systemd-run prefix must match the scope
	// form's tmux args shape exactly. We use stripSystemdRunPrefix to verify.
	tmuxArgs := stripSystemdRunPrefix(args)
	assert.Equal(t, []string{"-u", "new-session", "-d", "-s", "agentdeck_test-service_1234abcd", "-c", "/tmp/project",
		"-x", "173", "-y", "41"}, tmuxArgs)
}

func TestStartCommandSpec_LaunchAs_ServiceCarriesTmuxTmpdir(t *testing.T) {
	const isolated = "/tmp/agent-deck-service-tmux"
	t.Setenv("TMUX_TMPDIR", isolated)

	s := &Session{Name: "service-env", WorkDir: "/tmp/project", LaunchAs: "service"}
	launcher, args := s.startCommandSpec("/tmp/project", "")

	require.Equal(t, "systemd-run", launcher)
	assert.Contains(t, args, "--setenv=TMUX_TMPDIR="+isolated,
		"service-mode tmux must receive the caller's isolated socket base")
}

func TestStartCommandSpec_CarriesCreationIdentityThroughLaunchers(t *testing.T) {
	for _, launchAs := range []string{"direct", "scope", "service"} {
		t.Run(launchAs+" captures created identity", func(t *testing.T) {
			calls, session := startWithFakeLauncher(t, launchAs, false)
			require.NoError(t, session.Start(""), "fake launcher calls: %s", calls())
			assert.Equal(t, "$created", session.createdSessionID)
			wantLauncher := "tmux"
			if launchAs != "direct" {
				wantLauncher = "systemd-run"
			}
			assert.Contains(t, calls(), wantLauncher+" ")
		})

		t.Run(launchAs+" rejects replacement during capture", func(t *testing.T) {
			calls, state, session := startWithFakeLauncherOptions(t, launchAs, true, false)
			require.Error(t, session.Start(""))
			assert.Equal(t, "$created", session.createdSessionID)
			assert.Contains(t, calls(), "KILLED -u kill-session -t $created")
			assert.NotContains(t, calls(), "KILLED -u kill-session -t $replacement")
			assert.True(t, fakeSessionAlive(state(), "$replacement", session.Name),
				"same-name replacement must survive failed identity capture")
		})
	}
}

func TestStart_RepeatedStartWithMarkerCaptureDoesNotReusePriorOwnership(t *testing.T) {
	calls, state, session := startWithFakeLauncherOptions(t, "direct", false, true)
	require.NoError(t, session.Start(""), "first fake launcher calls: %s", calls())
	oldName := session.Name
	oldID := session.createdSessionID
	spawn := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "tmux" && containsArg(args, "new-session") && session.createdSessionID != "" {
			t.Errorf("new Start spawn retained prior createdSessionID %q", session.createdSessionID)
		}
		return spawn(name, args...)
	}
	t.Cleanup(func() { execCommand = spawn })

	require.NoError(t, session.Start(""), "second fake launcher calls: %s", calls())
	if session.Name == oldName {
		t.Fatalf("repeated Start must regenerate the existing session name")
	}
	assert.Equal(t, "$new", session.createdSessionID)
	assert.Contains(t, state(), "$created\t"+oldName+"\t", "the prior session must survive the second Start")
	assert.Contains(t, state(), "$new\t"+session.Name+"\t", "the new marker-owned session must survive capture")
	assert.NotContains(t, calls(), "KILLED -u kill-session -t "+oldID,
		"stale ownership must not roll back the prior session")
}

func TestStartSendKeysFailureCleansUpOnlyNewOwnedSession(t *testing.T) {
	calls, state, session := startWithFakeLauncherOptions(t, "direct", false, false)
	t.Setenv("FAKE_LAUNCH_SEND_FAIL", "1")
	if err := session.Start("echo issue1793"); err == nil {
		t.Fatal("Start unexpectedly succeeded after pane-shell delivery failure")
	}
	if fakeSessionAlive(state(), "$created", session.Name) {
		t.Fatalf("pane-shell delivery failure left the newly created session live: %s", state())
	}
	if session.Exists() {
		t.Fatal("pane-shell delivery failure left the newly created session persisted in the session cache")
	}
	if !strings.Contains(calls(), "KILLED -u kill-session -t $created") {
		t.Fatalf("pane-shell delivery failure did not clean up the owned session: %s", calls())
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func fakeSessionAlive(state, wantID, wantName string) bool {
	for _, line := range strings.Split(strings.TrimSpace(state), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 6 && fields[0] == wantID && fields[1] == wantName && fields[4] == "1" && fields[5] == "1" {
			return true
		}
	}
	return false
}

func startWithFakeLauncher(t *testing.T, launchAs string, replacement bool) (func() string, *Session) {
	calls, _, session := startWithFakeLauncherOptions(t, launchAs, replacement, false)
	return calls, session
}

func startWithFakeLauncherOptions(t *testing.T, launchAs string, replacement, repeated bool) (func() string, func() string, *Session) {
	t.Helper()
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls")
	stateLog := filepath.Join(dir, "state")
	fake := filepath.Join(dir, "launcher")
	script := `#!/bin/sh
case "$0" in
*/tmux) mode="tmux" ;;
*) mode="$1"; shift ;;
esac
printf '%s %s\n' "$mode" "$*" >> "$FAKE_LAUNCH_CALLS"
name=""
marker=""
workdir="/"
previous=""
for arg in "$@"; do
  if [ "$previous" = "-s" ]; then name="$arg"; fi
  if [ "$previous" = "-e" ]; then marker="${arg#*=}"; fi
  if [ "$previous" = "-c" ]; then workdir="$arg"; fi
  previous="$arg"
done
is_new=0
is_list=0
is_display=0
is_pane_path=0
is_has=0
	is_kill=0
	is_send=0
	for arg in "$@"; do
  [ "$arg" = "new-session" ] && is_new=1
  [ "$arg" = "list-sessions" ] && is_list=1
  [ "$arg" = "display-message" ] && is_display=1
	[ "$arg" = "#{pane_current_path}" ] && is_pane_path=1
  [ "$arg" = "has-session" ] && is_has=1
	  [ "$arg" = "kill-session" ] && is_kill=1
	  [ "$arg" = "send-keys" ] && is_send=1
	done
	if [ "$is_send" = "1" ] && [ "$FAKE_LAUNCH_SEND_FAIL" = "1" ]; then
	  printf 'send failure\n' >&2
	  exit 42
	fi
if [ "$is_new" = "1" ]; then
  expected="$FAKE_LAUNCH_EXPECTED_MODE"
  if [ "$expected" = "tmux" ] && [ "$mode" != "tmux" ]; then
    printf 'wrong launcher: expected direct tmux, got %s\n' "$mode" >&2
    exit 97
  fi
  if [ "$expected" = "scope" ]; then
    [ "$mode" = "systemd-run" ] || { printf 'wrong launcher: expected systemd scope, got %s\n' "$mode" >&2; exit 97; }
    printf '%s\n' "$*" | grep -q -- '--scope' || { printf 'wrong launcher: expected scope form\n' >&2; exit 97; }
    printf '%s\n' "$*" | grep -q -- '\.service' && { printf 'wrong launcher: scope received service form\n' >&2; exit 97; }
  fi
  if [ "$expected" = "service" ]; then
    [ "$mode" = "systemd-run" ] || { printf 'wrong launcher: expected systemd service, got %s\n' "$mode" >&2; exit 97; }
    printf '%s\n' "$*" | grep -q -- '\.service' || { printf 'wrong launcher: expected service form\n' >&2; exit 97; }
    printf '%s\n' "$*" | grep -q -- '--scope' && { printf 'wrong launcher: service received scope form\n' >&2; exit 97; }
  fi
fi
if [ "$is_new" = "1" ]; then
    count=0
    [ -f "$FAKE_LAUNCH_COUNT" ] && count=$(cat "$FAKE_LAUNCH_COUNT")
    count=$((count + 1))
    printf '%s\n' "$count" > "$FAKE_LAUNCH_COUNT"
    id='$created'
    output='$created'
    if [ "$count" -gt 1 ] && [ "$FAKE_LAUNCH_REPEATED" = "1" ]; then
      id='$new'
      output=''
    fi
	if [ "$FAKE_LAUNCH_REPLACEMENT" = "1" ]; then
	      printf '%s\t%s\t%s\t%s\t1\t0\n' '$created' "$name" "$marker" "$workdir" >> "$FAKE_LAUNCH_STATE"
	      printf '%s\t%s\t%s\t%s\t1\t1\n' '$replacement' "$name" 'new-marker' "$workdir" >> "$FAKE_LAUNCH_STATE"
	    else
	      printf '%s\t%s\t%s\t%s\t1\t1\n' "$id" "$name" "$marker" "$workdir" >> "$FAKE_LAUNCH_STATE"
    fi
    printf '%s\n' "$output"
    exit 0 ;
fi
if [ "$is_list" = "1" ]; then
	    while IFS='	' read -r id name marker workdir alive visible; do
	      [ "$alive" = "1" ] && [ "$visible" = "1" ] && printf '%s\t%s\t%s\n' "$id" "$name" "$marker"
    done < "$FAKE_LAUNCH_STATE"
    exit 0 ;
fi
if [ "$is_display" = "1" ]; then
    target=""
    previous=""
    for arg in "$@"; do
      [ "$previous" = "-t" ] && target="$arg"
      previous="$arg"
    done
    found=0
	    while IFS='	' read -r id name marker workdir alive visible; do
      [ "$alive" = "1" ] || continue
      if [ "$target" = "$name" ] || [ "$target" = "$id" ]; then
        found=1
        if [ "$is_pane_path" = "1" ]; then printf '%s\n' "$workdir"; else printf '%s\n' "$id"; fi
        break
      fi
    done < "$FAKE_LAUNCH_STATE"
    [ "$found" = "1" ] || { printf "can't find session\n" >&2; exit 1; }
    exit 0 ;
fi
if [ "$is_has" = "1" ]; then
    target=""
    previous=""
    for arg in "$@"; do [ "$previous" = "-t" ] && target="$arg"; previous="$arg"; done
	    while IFS='	' read -r id name marker workdir alive visible; do
	      [ "$alive" = "1" ] && [ "$visible" = "1" ] && [ "$target" = "$name" ] && exit 0
    done < "$FAKE_LAUNCH_STATE"
    exit 1
fi
if [ "$is_kill" = "1" ]; then
    target=""
    previous=""
    for arg in "$@"; do [ "$previous" = "-t" ] && target="$arg"; previous="$arg"; done
    tmp="$FAKE_LAUNCH_STATE.tmp"
	    while IFS='	' read -r id name marker workdir alive visible; do
	      if [ "$target" = "$id" ] || [ "$target" = "$name" ]; then alive=0; fi
	      printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$id" "$name" "$marker" "$workdir" "$alive" "$visible" >> "$tmp"
    done < "$FAKE_LAUNCH_STATE"
    mv "$tmp" "$FAKE_LAUNCH_STATE"
    printf 'KILLED %s\n' "$*" >> "$FAKE_LAUNCH_CALLS"
fi
exit 0
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fake, filepath.Join(dir, "tmux")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LAUNCH_CALLS", callLog)
	t.Setenv("FAKE_LAUNCH_STATE", stateLog)
	t.Setenv("FAKE_LAUNCH_COUNT", filepath.Join(dir, "count"))
	expectedMode := launchAs
	if expectedMode == "direct" {
		expectedMode = "tmux"
	}
	t.Setenv("FAKE_LAUNCH_EXPECTED_MODE", expectedMode)
	if replacement {
		t.Setenv("FAKE_LAUNCH_REPLACEMENT", "1")
	} else {
		t.Setenv("FAKE_LAUNCH_REPLACEMENT", "0")
	}
	if repeated {
		t.Setenv("FAKE_LAUNCH_REPEATED", "1")
	} else {
		t.Setenv("FAKE_LAUNCH_REPEATED", "0")
	}
	original := execCommand
	originalContext := execCommandContext
	execCommand = func(name string, arg ...string) *exec.Cmd {
		return exec.Command(fake, append([]string{name}, arg...)...)
	}
	execCommandContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, fake, append([]string{name}, arg...)...)
	}
	t.Cleanup(func() {
		execCommand = original
		execCommandContext = originalContext
	})

	session := NewSession("launcher-behavior", t.TempDir())
	session.LaunchAs = launchAs
	return func() string {
			raw, err := os.ReadFile(callLog)
			if err != nil {
				t.Fatalf("read fake launcher calls: %v", err)
			}
			return string(raw)
		}, func() string {
			raw, err := os.ReadFile(stateLog)
			if err != nil {
				t.Fatalf("read fake launcher state: %v", err)
			}
			return string(raw)
		}, session
}

// TestStartCommandSpec_LaunchAs_Scope_UsesScopeForm explicitly pins the
// SCOPE-MODE argv (legacy PR #467 shape) so a refactor to service mode
// can't silently break users who set LaunchAs="scope" to keep the old
// behavior. This is the backward-compat lifeline.
func TestStartCommandSpec_LaunchAs_Scope_UsesScopeForm(t *testing.T) {
	s := &Session{
		Name:     "agentdeck_test-scope_1234abcd",
		WorkDir:  "/tmp/project",
		LaunchAs: "scope",
	}

	launcher, args := s.startCommandSpec("/tmp/project", "")
	require.Equal(t, "systemd-run", launcher)
	require.GreaterOrEqual(t, len(args), 7)
	assert.Equal(t, []string{"--user", "--scope", "--quiet", "--collect"}, args[:4])
	assert.Equal(t, "--unit", args[4])
	assert.Equal(t, "agentdeck-tmux-agentdeck-test-scope-1234abcd", args[5])
	assert.Equal(t, "tmux", args[6])

	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, "--property=Type=forking",
		"scope mode must not contain service-only properties")
	assert.NotContains(t, joined, "--property=Restart=",
		"Restart= is invalid on scopes (systemd rejects)")
	assert.NotContains(t, joined, "--pipe")
}

// TestStartCommandSpec_LaunchAs_Direct_UsesDirectTmux pins that an
// explicit LaunchAs="direct" forces direct tmux EVEN IF
// LaunchInUserScope=true. This is the "opt out of isolation" path for
// users on hosts where systemd-run misbehaves.
func TestStartCommandSpec_LaunchAs_Direct_UsesDirectTmux(t *testing.T) {
	s := &Session{
		Name:              "agentdeck_test-direct_1234abcd",
		WorkDir:           "/tmp/project",
		LaunchAs:          "direct",
		LaunchInUserScope: true, // explicit override must WIN
	}
	pinInitialWindowSize(t, 173, 41, true) // #1694 birth size

	launcher, args := s.startCommandSpec("/tmp/project", "")
	assert.Equal(t, "tmux", launcher, "LaunchAs=direct must override LaunchInUserScope=true")
	assert.Equal(t, []string{"-u", "new-session", "-d", "-s", "agentdeck_test-direct_1234abcd", "-c", "/tmp/project",
		"-x", "173", "-y", "41"}, args)
}

// TestStartCommandSpec_LaunchAs_Empty_RespectsLegacyLaunchInUserScope
// is the zero-behavior-change guarantee for v1.7.20 users who don't set
// launch_as in config.toml. Empty string = defer to the pre-existing
// LaunchInUserScope flag.
func TestStartCommandSpec_LaunchAs_Empty_RespectsLegacyLaunchInUserScope(t *testing.T) {
	t.Run("empty + LaunchInUserScope=true → scope form (legacy PR #467)", func(t *testing.T) {
		s := &Session{
			Name:              "agentdeck_test-empty-scope_1234abcd",
			WorkDir:           "/tmp/project",
			LaunchInUserScope: true,
			// LaunchAs intentionally unset
		}
		launcher, args := s.startCommandSpec("/tmp/project", "")
		assert.Equal(t, "systemd-run", launcher)
		assert.Contains(t, strings.Join(args, " "), "--scope")
	})

	t.Run("empty + LaunchInUserScope=false → direct", func(t *testing.T) {
		s := &Session{
			Name:              "agentdeck_test-empty-direct_1234abcd",
			WorkDir:           "/tmp/project",
			LaunchInUserScope: false,
		}
		launcher, _ := s.startCommandSpec("/tmp/project", "")
		assert.Equal(t, "tmux", launcher)
	})
}

// TestStartCommandSpec_LaunchAs_Invalid_FallsBackToLegacy asserts an
// unknown string does NOT panic and does NOT silently pick service — it
// falls back to the LaunchInUserScope-driven legacy behavior. A typo in
// config.toml must not put a user on an unintended spawn path.
func TestStartCommandSpec_LaunchAs_Invalid_FallsBackToLegacy(t *testing.T) {
	s := &Session{
		Name:              "agentdeck_test-invalid_1234abcd",
		WorkDir:           "/tmp/project",
		LaunchAs:          "typo-gibberish",
		LaunchInUserScope: true,
	}

	launcher, args := s.startCommandSpec("/tmp/project", "")
	assert.Equal(t, "systemd-run", launcher)
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "--scope", "invalid LaunchAs value must not accidentally select service mode")
	assert.NotContains(t, joined, "--property=Type=forking")
}

// TestStartCommandSpec_LaunchAs_CaseInsensitive guards against config
// typos where the user writes "Service" or "SERVICE". These must still
// resolve to the service mode, not silently fall through to legacy.
func TestStartCommandSpec_LaunchAs_CaseInsensitive(t *testing.T) {
	for _, variant := range []string{"service", "Service", "SERVICE", " service ", "service\t"} {
		t.Run(variant, func(t *testing.T) {
			s := &Session{
				Name:     "agentdeck_test-ci_1234abcd",
				WorkDir:  "/tmp/project",
				LaunchAs: variant,
			}
			launcher, args := s.startCommandSpec("/tmp/project", "")
			assert.Equal(t, "systemd-run", launcher, "case/whitespace variant %q must still resolve to service", variant)
			assert.Contains(t, strings.Join(args, " "), "--property=Type=forking")
		})
	}
}

// TestStartCommandSpec_LaunchAs_ServiceWithInitialProcess pins that the
// RunCommandAsInitialProcess path (sandbox sessions, custom claude
// commands) composes correctly with service mode. Failure here means
// sandbox sessions silently lose their auto-restart guarantee.
func TestStartCommandSpec_InitialProcessKeepsInteractivePTY(t *testing.T) {
	s := &Session{
		Name:                       "agentdeck_test-interactive_1234abcd",
		WorkDir:                    "/tmp/project",
		RunCommandAsInitialProcess: true,
		launchAckPath:              "/tmp/ack",
	}
	_, args := s.startCommandSpec("/tmp/project", "codex")
	if len(args) < 4 || args[len(args)-2] != "interactive" || args[len(args)-1] != "codex" {
		t.Fatalf("interactive initial process must use the PTY-preserving wrapper mode, args=%q", args)
	}
}

func TestStartCommandSpec_LaunchAs_ServiceWithInitialProcess(t *testing.T) {
	s := &Session{
		Name:                       "agentdeck_test-svcinit_1234abcd",
		WorkDir:                    "/tmp/project",
		LaunchAs:                   "service",
		RunCommandAsInitialProcess: true,
	}
	launcher, args := s.startCommandSpec("/tmp/project", "claude --resume xyz")

	require.Equal(t, "systemd-run", launcher)
	tmuxArgs := stripSystemdRunPrefix(args)
	// #1567/#1580: initial process delivered as argv tokens bash -c COMMAND.
	require.GreaterOrEqual(t, len(tmuxArgs), 3)
	assert.Equal(t, "bash", tmuxArgs[len(tmuxArgs)-3], "initial process must be exec'd under bash")
	assert.Equal(t, "-c", tmuxArgs[len(tmuxArgs)-2])
	assert.Equal(t, "claude --resume xyz", tmuxArgs[len(tmuxArgs)-1])
}

// TestStripSystemdRunPrefix_RecoversTmuxArgsFromServiceForm is the
// regression guard for stripSystemdRunPrefix when it's fed SERVICE-mode
// argv (which has ~12 leading elements vs scope's 7). Adding a property
// to the service spawn must not break fallback-to-direct-tmux.
func TestStripSystemdRunPrefix_RecoversTmuxArgsFromServiceForm(t *testing.T) {
	in := []string{
		"--user", "--unit", "agentdeck-tmux-foo.service", "--quiet",
		"--property=Type=forking",
		"--property=Restart=on-failure",
		"--property=RestartSec=5s",
		"--property=StartLimitBurst=10",
		"--property=StartLimitIntervalSec=60",
		"--property=KillMode=control-group",
		"--property=TimeoutStopSec=15s",
		"tmux",
		"new-session", "-d", "-s", "name",
	}
	want := []string{"new-session", "-d", "-s", "name"}
	got := stripSystemdRunPrefix(in)
	assert.Equal(t, want, got)
}
