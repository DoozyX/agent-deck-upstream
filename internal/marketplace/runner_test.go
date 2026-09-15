package marketplace

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMarketplaceRunUnmanagedHomeIsNoop(t *testing.T) {
	if err := Run(context.Background(), Request{HostHome: t.TempDir(), CodexHome: t.TempDir(), CodexArgv: []string{"not-codex"}}); err != nil {
		t.Fatal(err)
	}
}

func TestMarketplaceRunNoEligibleReceipt(t *testing.T) {
	f := newFixture(t)
	profile := t.TempDir()
	writeTest(t, filepath.Join(profile, "config.toml"), "unknown = 'preserve'\n")
	if err := Run(context.Background(), Request{HostHome: f.home, CodexHome: profile, CodexArgv: approvedTestArgv}); err != nil {
		t.Fatal(err)
	}
	entries, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(entries) != 0 {
		t.Fatalf("ineligible profile has success receipts: %v", entries)
	}
}

func TestMarketplaceRunReceiptRejectsAliases(t *testing.T) {
	testProvenRuntime(t)
	home, profile := t.TempDir(), t.TempDir()
	revision := strings.Repeat("a", 40)
	if err := saveProfileReceipt(home, profile, approvedTestArgv, revision); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(StateDir(home), "profiles", "*.json"))
	if len(paths) != 1 {
		t.Fatalf("receipt count %d", len(paths))
	}
	victim := filepath.Join(home, "victim")
	writeTest(t, victim, "preserve")
	if err := os.Remove(paths[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, paths[0]); err != nil {
		t.Fatal(err)
	}
	_ = saveProfileReceipt(home, profile, approvedTestArgv, revision)
	got, _ := os.ReadFile(victim)
	if string(got) != "preserve" {
		t.Fatal("receipt followed symlink")
	}
}

func managedNativeFixture(t *testing.T) (fixture, string) {
	t.Helper()
	f := newFixture(t)
	profile, checkout := nativeFixture(t)
	if err := os.CopyFS(f.source, os.DirFS(checkout.Path)); err != nil {
		t.Fatal(err)
	}
	gitTest(t, f.source, "add", ".")
	gitTest(t, f.source, "commit", "-m", "native fixture")
	cfg, _ := os.ReadFile(filepath.Join(profile, "config.toml"))
	writeTest(t, filepath.Join(profile, "config.toml"), strings.ReplaceAll(string(cfg), checkout.Path, f.path))
	return f, profile
}

func TestMarketplaceRunNativeFailureBacksOff(t *testing.T) {
	f, profile := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", profile)
	writeTest(t, filepath.Join(profile, "mode"), "fail-second")
	req := Request{HostHome: f.home, CodexHome: profile, CodexArgv: approvedTestArgv}
	_ = Run(context.Background(), req)
	before, _ := os.ReadFile(filepath.Join(profile, "calls"))
	if len(before) == 0 {
		t.Fatal("failure fixture did not run")
	}
	_ = Run(context.Background(), req)
	after, _ := os.ReadFile(filepath.Join(profile, "calls"))
	if string(before) != string(after) {
		t.Fatal("native failure was retried without backoff")
	}
	paths, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(paths) != 0 {
		t.Fatal("failed native refresh wrote receipt")
	}
	log, _ := os.ReadFile(filepath.Join(StateDir(f.home), "update.log"))
	if !strings.Contains(string(log), "callback_failure") || strings.Contains(string(log), "secret") {
		t.Fatalf("failure log: %s", log)
	}
}

func TestMarketplaceRunSecondProfileAndAlias(t *testing.T) {
	f, first := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", first)
	req := Request{HostHome: f.home, CodexHome: first, CodexArgv: approvedTestArgv}
	_ = Run(context.Background(), req)
	second := t.TempDir()
	if err := os.CopyFS(second, os.DirFS(first)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(second, "calls")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", second)
	req.CodexHome = second
	_ = Run(context.Background(), req)
	calls, _ := os.ReadFile(filepath.Join(second, "calls"))
	if string(calls) != "agent-deck\nagent-deck-mcp\n" {
		t.Fatalf("second profile skipped: %q", calls)
	}
	paths, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(paths) != 2 {
		t.Fatalf("profile receipt count: %d", len(paths))
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", alias)
	req.CodexHome = alias
	_ = Run(context.Background(), req)
	paths, _ = filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(paths) != 2 {
		t.Fatalf("alias receipt count: %d", len(paths))
	}
	fetches, _ := os.ReadFile(filepath.Join(f.home, "fetches"))
	if strings.Count(string(fetches), "fetch") != 1 {
		t.Fatalf("checkout not coalesced: %s", fetches)
	}
}

func TestMarketplaceRunGitFailurePreventsNative(t *testing.T) {
	f, profile := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", profile)
	writeTest(t, filepath.Join(f.home, "git-mode"), "fail")
	_ = Run(context.Background(), Request{HostHome: f.home, CodexHome: profile, CodexArgv: approvedTestArgv})
	if _, err := os.Stat(filepath.Join(profile, "calls")); !os.IsNotExist(err) {
		t.Fatal("native executed after Git failure")
	}
	paths, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(paths) != 0 {
		t.Fatal("Git failure receipt")
	}
}

func TestMarketplaceRunReceiptCoalescesVerifiedProfile(t *testing.T) {
	f, profile := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", profile)
	req := Request{HostHome: f.home, CodexHome: profile, CodexArgv: approvedTestArgv}
	_ = Run(context.Background(), req)
	before, _ := os.ReadFile(filepath.Join(profile, "calls"))
	if len(before) == 0 {
		t.Fatal("initial refresh missing")
	}
	_ = Run(context.Background(), req)
	after, _ := os.ReadFile(filepath.Join(profile, "calls"))
	if string(before) != string(after) {
		t.Fatal("verified same-profile receipt did not coalesce native writes")
	}
}

func TestMarketplaceRunRejectsReplacedBinaryAndOldReceipt(t *testing.T) {
	f, profile := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", profile)
	req := Request{HostHome: f.home, CodexHome: profile, CodexArgv: approvedTestArgv}
	if err := Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	receipts, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(receipts) != 1 {
		t.Fatalf("initial receipt count: %d", len(receipts))
	}
	before, _ := os.ReadFile(receipts[0])
	original := runtimeIdentity(req.CodexArgv)
	replacement := filepath.Join(t.TempDir(), "replacement")
	writeTest(t, replacement, "an unproven replacement at the same argv path")
	// Keep the production argv unchanged while replacing the bytes its fingerprint
	// reader sees. The changed bytes are hashed with the real file implementation.
	nativeBinaryHash = func(string) (string, error) { return hashNativeBinary(replacement) }
	if got := runtimeIdentity(req.CodexArgv); got == original || supportedRuntime(req.CodexArgv) {
		t.Fatalf("same-path replacement admitted with old runtime identity: %q", got)
	}
	calls := 0
	executeNative = func(context.Context, string, []string, ...string) ([]byte, error) { calls++; return nil, nil }
	checkout := Checkout{Path: f.path, Revision: gitTest(t, f.path, "rev-parse", "HEAD")}
	if profileCurrent(context.Background(), f.home, req, checkout) {
		t.Fatal("replacement reused previous binary receipt")
	}
	if err := Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("unproven binary executed native inventory or mutation")
	}
	after, _ := os.ReadFile(receipts[0])
	if string(before) != string(after) {
		t.Fatal("unproven runtime rewrote previous receipt")
	}
	fresh := t.TempDir()
	if err := saveProfileReceipt(f.home, fresh, req.CodexArgv, checkout.Revision); err != nil {
		t.Fatal(err)
	}
	receipts, _ = filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(receipts) != 1 {
		t.Fatal("unproven runtime wrote new receipt")
	}
}

func TestMarketplaceRunActiveNativeWorkerProcess(t *testing.T) {
	host := os.Getenv("TASK03_ACTIVE_HOST")
	if host == "" {
		t.Skip("worker-death helper")
	}
	testProvenRuntime(t)
	trustedGitPath = os.Getenv("TASK03_ACTIVE_GIT")
	profile := os.Getenv("CODEX_HOME")
	bin, err := os.ReadFile(filepath.Join(profile, "fixture-binary"))
	if err != nil {
		t.Fatal(err)
	}
	executeNative = func(ctx context.Context, h string, argv []string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[1] == "add" {
			script := `import os,pathlib,time,json
h=pathlib.Path(os.environ['CODEX_HOME'])
try: inode=os.fstat(3).st_ino
except OSError: inode=0
(h/'plugins/cache/agent-deck/agent-deck/1.0.0/skills/example/SKILL.md').write_text('active native mutation')
(h/'active-child').write_text(json.dumps({'pid':os.getpid(),'lock_inode':inode}))
while not (h/'release-child').exists(): time.sleep(.01)
(h/'child-finished').write_text('finished')
`
			return nativeOutput(ctx, h, []string{testPythonPath}, "-c", script)
		}
		return nativeOutput(ctx, h, []string{string(bin), "--disable", "apps"}, args...)
	}
	_ = Run(context.Background(), Request{HostHome: host, CodexHome: profile, CodexArgv: approvedTestArgv})
	t.Fatal("worker returned before being killed")
}

func TestMarketplaceRunActiveNativeChildRetainsLockAfterWorkerDeath(t *testing.T) {
	f, profile := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", profile)
	req := Request{HostHome: f.home, CodexHome: profile, CodexArgv: approvedTestArgv}
	if err := Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	receipts, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(receipts) != 1 {
		t.Fatal("initial native seed failed")
	}
	if err := os.Remove(receipts[0]); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestMarketplaceRunActiveNativeWorkerProcess$")
	cmd.Env = append(os.Environ(), "TASK03_ACTIVE_HOST="+f.home, "TASK03_ACTIVE_GIT="+trustedGitPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var child struct {
		PID       int    `json:"pid"`
		LockInode uint64 `json:"lock_inode"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(profile, "active-child"))
		if err == nil && json.Unmarshal(data, &child) == nil && child.PID > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child.PID == 0 {
		t.Fatal("native child never reached active mutation")
	}
	defer syscall.Kill(-child.PID, syscall.SIGKILL)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := syscall.Kill(child.PID, 0); err != nil {
		t.Fatal("native child did not outlive worker", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	lock, err := acquireLock(ctx, filepath.Join(StateDir(f.home), "update.lock"))
	if err == nil {
		lock.release()
		t.Fatal("another worker acquired host lock while native child was mutating")
	}
	info, err := os.Stat(filepath.Join(StateDir(f.home), "update.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if child.LockInode != info.Sys().(*syscall.Stat_t).Ino {
		t.Fatalf("native child inherited wrong lock inode: %d", child.LockInode)
	}
	writeTest(t, filepath.Join(profile, "release-child"), "release")
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	lock, err = acquireLock(ctx2, filepath.Join(StateDir(f.home), "update.lock"))
	if err != nil {
		t.Fatal("native exit did not release host lock", err)
	}
	lock.release()
	if _, err := os.Stat(filepath.Join(profile, "child-finished")); err != nil {
		t.Fatal("native child did not finish", err)
	}
	out, _ := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(child.PID)).Output()
	if state := strings.TrimSpace(string(out)); state != "" && !strings.HasPrefix(state, "Z") {
		t.Fatal("native child remains running", state)
	}
	if _, err := os.Stat(filepath.Join(profile, ".agent-deck-refresh/pending/transaction.json")); err != nil {
		t.Fatal("worker death lost pending recovery", err)
	}
	receipts, _ = filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(receipts) != 0 {
		t.Fatal("dead worker published receipt")
	}
}

func TestMarketplaceRunUnsupportedRuntimeDiagnosticIsBoundedAndBacksOff(t *testing.T) {
	f, profile := managedNativeFixture(t)
	t.Setenv("CODEX_HOME", profile)
	calls := 0
	executeNative = func(context.Context, string, []string, ...string) ([]byte, error) { calls++; return nil, nil }
	logPath := filepath.Join(StateDir(f.home), "update.log")
	writeTest(t, logPath, strings.Repeat("x", logLimit-10))
	req := Request{HostHome: f.home, CodexHome: profile, CodexArgv: []string{"/secret/credential", strings.Repeat("secret-token", 10000)}}
	for i := 0; i < 2; i++ {
		if err := Run(context.Background(), req); err != nil {
			t.Fatal("unsupported runtime must remain fail-open", err)
		}
	}
	logs := ""
	for _, path := range []string{logPath, logPath + ".1"} {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if len(data) > logLimit {
			t.Fatalf("diagnostic exceeded log bound: %d", len(data))
		}
		logs += string(data)
	}
	if strings.Count(logs, "unsupported_runtime") != 1 || !strings.Contains(logs, "callback_failure") || !strings.Contains(logs, "backoff") {
		t.Fatalf("missing unsupported diagnostic/backoff: %s", logs[max(0, len(logs)-300):])
	}
	if strings.Contains(logs, "secret") || calls != 0 {
		t.Fatal("unsupported runtime leaked input or executed native command")
	}
	receipts, _ := filepath.Glob(filepath.Join(StateDir(f.home), "profiles", "*.json"))
	if len(receipts) != 0 {
		t.Fatal("unsupported runtime wrote receipt")
	}
	state, err := loadState(StateDir(f.home))
	if err != nil {
		t.Fatal(err)
	}
	key, err := callbackProfileKey(f.home)
	if err != nil {
		t.Fatal(err)
	}
	if !state.CallbackRetries[key].NextAttempt.After(time.Now()) {
		t.Fatal("unsupported runtime did not reserve retry backoff")
	}
}
