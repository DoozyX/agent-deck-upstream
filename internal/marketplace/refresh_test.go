package marketplace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestNativeRefreshUnsupportedRuntimeIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := RefreshProfile(context.Background(), dir, []string{"not-codex"}, Checkout{Path: "/clone", Revision: "0123456789012345678901234567890123456789"}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRefreshUnprovenBasenameNeverExecutes(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	log := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nif [ \"$1\" = plugin ] && [ \"$2\" = list ]; then echo '{\"plugins\":[{\"id\":\"agent-deck@agent-deck\",\"enabled\":true},{\"id\":\"agent-deck-mcp@agent-deck\",\"enabled\":false}]}' ; else echo \"$@\" >> '" + log + "'; fi\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := RefreshProfile(context.Background(), dir, []string{bin}, Checkout{Path: "/clone", Revision: "0123456789012345678901234567890123456789"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("unproven basename ran native command: %v", err)
	}
}

func TestNativeRefreshAcceptsOnlyTaskOneRuntimeIdentities(t *testing.T) {
	if !supportedRuntime([]string{"/Applications/ChatGPT.app/Contents/Resources/codex", "--disable", "apps"}) {
		t.Fatal("approved ChatGPT runtime rejected")
	}
	if supportedRuntime([]string{"/Users/doozyx/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex", "--disable", "apps"}) {
		t.Fatal("unproven standalone runtime accepted")
	}
}

// Removing installed=true admission or reading only id instead of pluginId
// would break selection against the actual Task 01 inventory schema.
func TestNativeRefreshTaskOneInventory(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "native")
	writeTest(t, bin, "#!/bin/sh\nprintf '%s' '{\"installed\":[{\"pluginId\":\"agent-deck@agent-deck\",\"installed\":true,\"enabled\":true},{\"pluginId\":\"agent-deck-mcp@agent-deck\",\"installed\":true,\"enabled\":false}],\"available\":[{\"pluginId\":\"agent-deck-mcp@agent-deck\",\"enabled\":true}]}'\n")
	if err := os.Chmod(bin, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := nativeList(context.Background(), t.TempDir(), []string{bin})
	if err != nil || !got[nativeSelectors[0]] || got[nativeSelectors[1]] {
		t.Fatalf("installed enabled selection = %v, %v", got, err)
	}
}

func TestNativeRefreshTimeoutClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := nativeOutput(ctx, t.TempDir(), []string{"/bin/sh"}, "-c", "echo secret >&2; sleep 20")
	if err == nil || err.Error() != "marketplace: native timeout" {
		t.Fatalf("cancellation classification = %v", err)
	}
}

func TestNativeRefreshStderrBound(t *testing.T) {
	_, err := nativeOutput(context.Background(), t.TempDir(), []string{"/bin/sh"}, "-c", "head -c 100000 /dev/zero >&2")
	if err == nil || err.Error() != "marketplace: native output exceeded limit" {
		t.Fatalf("stderr overflow = %v", err)
	}
}

var approvedTestArgv = []string{"/Applications/ChatGPT.app/Contents/Resources/codex", "--disable", "apps"}

func nativeFixture(t *testing.T) (string, Checkout) {
	t.Helper()
	home, source := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "plugins/cache/agent-deck"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".codex-plugin", "skills/example", "plugins/agent-deck-mcp/.codex-plugin"} {
		if err := os.MkdirAll(filepath.Join(source, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeTest(t, filepath.Join(source, ".codex-plugin/plugin.json"), `{"name":"agent-deck","version":"1.0.0"}`)
	writeTest(t, filepath.Join(source, "skills/example/SKILL.md"), "new skill")
	writeTest(t, filepath.Join(source, "plugins/agent-deck-mcp/.codex-plugin/plugin.json"), `{"name":"agent-deck-mcp","version":"1.0.0"}`)
	writeTest(t, filepath.Join(source, "plugins/agent-deck-mcp/.mcp.json"), `{"mcpServers":{}}`)
	writeTest(t, filepath.Join(home, "config.toml"), fmt.Sprintf("unknown = 'preserve'\n[marketplaces.agent-deck]\nsource_type = 'local'\nsource = %q\n[plugins.\"agent-deck@agent-deck\"]\nenabled = true\n[plugins.\"agent-deck-mcp@agent-deck\"]\nenabled = true\n", source))
	writeTest(t, filepath.Join(home, "inventory.json"), `{"installed":[{"pluginId":"agent-deck@agent-deck","installed":true,"enabled":true},{"pluginId":"agent-deck-mcp@agent-deck","installed":true,"enabled":true}]}`)
	bin := filepath.Join(t.TempDir(), "native")
	script := fmt.Sprintf(`#!%s
import os,sys,json,shutil,pathlib,re
h=pathlib.Path(os.environ['CODEX_HOME']); a=sys.argv[1:]
assert a[:2]==['--disable','apps'],a
a=a[2:]
if a==['plugin','list','--available','--json']:
 print((h/'inventory.json').read_text()); sys.exit(0)
assert a[:2]==['plugin','add'] and a[3:]==['--json'],a
name=a[2].split('@')[0]
assert name in ['agent-deck','agent-deck-mcp']
with (h/'calls').open('a') as f: f.write(name+'\n')
mode=(h/'mode').read_text() if (h/'mode').exists() else ''
if mode=='fail-second' and name=='agent-deck-mcp':
 with (h/'config.toml').open('a') as f: f.write('\n[projects.unrelated]\ntrust_level = "trusted"\n')
 print('secret credential',file=sys.stderr); sys.exit(9)
if mode=='stale': sys.exit(0)
s=pathlib.Path(re.search(r'source = "([^"]+)"', (h/"config.toml").read_text()).group(1))
if name=='agent-deck-mcp': s=s/'plugins'/name
v=json.loads((s/'.codex-plugin/plugin.json').read_text())['version']
d=h/'plugins/cache/agent-deck'/name/v
if d.exists(): shutil.rmtree(d)
shutil.copytree(s,d)
print('{}')
`, testPythonPath)
	writeTest(t, bin, script)
	writeTest(t, filepath.Join(home, "fixture-binary"), bin)
	if err := os.Chmod(bin, 0700); err != nil {
		t.Fatal(err)
	}
	previous := executeNative
	executeNative = func(ctx context.Context, home string, argv []string, args ...string) ([]byte, error) {
		if !reflect.DeepEqual(argv, approvedTestArgv) {
			t.Fatalf("native argv: %v", argv)
		}
		return nativeOutput(ctx, home, append([]string{bin}, argv[1:]...), args...)
	}
	t.Cleanup(func() { executeNative = previous })
	return home, Checkout{Path: source, Revision: strings.Repeat("a", 40)}
}

func TestNativeRefreshRequiresRegistration(t *testing.T) {
	home, checkout := nativeFixture(t)
	writeTest(t, filepath.Join(home, "config.toml"), "unknown = 'preserve'\n")
	_ = RefreshProfile(context.Background(), home, approvedTestArgv, checkout)
	if _, err := os.Stat(filepath.Join(home, "calls")); !os.IsNotExist(err) {
		t.Fatalf("unregistered profile was mutated: %v", err)
	}
}

func TestNativeRefreshPartialFailureRestoresCachesAndPreservesDrift(t *testing.T) {
	home, checkout := nativeFixture(t)
	// Seed a previous working installation, then change source without bumping version.
	if err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, "plugins/cache/agent-deck/agent-deck/1.0.0/skills/example/SKILL.md")
	writeTest(t, filepath.Join(checkout.Path, "skills/example/SKILL.md"), "changed same version")
	writeTest(t, filepath.Join(home, "mode"), "fail-second")
	err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout)
	if err == nil {
		t.Fatal("partial failure succeeded")
	}
	got, _ := os.ReadFile(cache)
	if string(got) != "new skill" {
		t.Fatalf("previous cache not restored: %q", got)
	}
	config, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if !strings.Contains(string(config), "projects.unrelated") {
		t.Fatal("unrelated drift lost")
	}
	backup := filepath.Join(home, ".agent-deck-refresh/last/before/agent-deck/1.0.0/skills/example/SKILL.md")
	got, err = os.ReadFile(backup)
	if err != nil || string(got) != "new skill" {
		t.Fatalf("protected backup unavailable: %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(home, ".agent-deck-refresh/last"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("backup protection: %v %v", info, err)
	}
}

func TestNativeRefreshVerifiesInstalledContent(t *testing.T) {
	home, checkout := nativeFixture(t)
	if err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(checkout.Path, "skills/example/SKILL.md"), "new same-version body")
	writeTest(t, filepath.Join(home, "mode"), "stale")
	if err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout); err == nil {
		t.Fatal("native exit 0 marked stale content refreshed")
	}
}

func TestNativeRefreshSelectionAndInvalidConfig(t *testing.T) {
	for _, kind := range []string{"disabled", "absent", "malformed", "missing", "wrong-registration"} {
		t.Run(kind, func(t *testing.T) {
			home, checkout := nativeFixture(t)
			cfg, _ := os.ReadFile(filepath.Join(home, "config.toml"))
			switch kind {
			case "disabled":
				writeTest(t, filepath.Join(home, "config.toml"), strings.ReplaceAll(string(cfg), "enabled = true", "enabled = false"))
			case "absent":
				writeTest(t, filepath.Join(home, "inventory.json"), `{"installed":[],"available":[{"pluginId":"agent-deck@agent-deck","installed":false,"enabled":true}]}`)
			case "malformed":
				writeTest(t, filepath.Join(home, "config.toml"), "[broken")
			case "missing":
				if err := os.Remove(filepath.Join(home, "config.toml")); err != nil {
					t.Fatal(err)
				}
			case "wrong-registration":
				writeTest(t, filepath.Join(home, "config.toml"), strings.ReplaceAll(string(cfg), checkout.Path, t.TempDir()))
			}
			before, _ := os.ReadFile(filepath.Join(home, "config.toml"))
			refreshed, err := refreshProfile(context.Background(), home, approvedTestArgv, checkout)
			if err != nil || refreshed {
				t.Fatalf("ineligible result: %v %v", refreshed, err)
			}
			if _, err := os.Stat(filepath.Join(home, "calls")); !os.IsNotExist(err) {
				t.Fatal("ineligible selector mutated")
			}
			after, _ := os.ReadFile(filepath.Join(home, "config.toml"))
			if string(before) != string(after) {
				t.Fatal("config changed")
			}
		})
	}
}

func TestNativeRefreshInterruptedTransactionRefusesOverwrite(t *testing.T) {
	home, checkout := nativeFixture(t)
	dir := filepath.Join(home, ".agent-deck-refresh/pending")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(dir, "transaction.json"), "previous protected journal")
	err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout)
	if err == nil || err.Error() != "marketplace: native recovery required" {
		t.Fatalf("interruption: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "calls")); !os.IsNotExist(err) {
		t.Fatal("overwrote interrupted transaction")
	}
}

func TestNativeRefreshDescendantsBounded(t *testing.T) {
	home := t.TempDir()
	pidFile := filepath.Join(home, "child")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := nativeOutput(ctx, home, []string{"/bin/sh"}, "-c", `sleep 30 & echo $! > "$CODEX_HOME/child"; wait`)
	if err == nil || err.Error() != "marketplace: native timeout" || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout: %v after %v", err, time.Since(start))
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	// kill(0) also sees zombies; ps state distinguishes a killed, unreaped child.
	out, _ := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if s := strings.TrimSpace(string(out)); s != "" && !strings.HasPrefix(s, "Z") {
		t.Fatalf("descendant still running: %s", s)
	}
}

func TestNativeRefreshOutputRedacted(t *testing.T) {
	for _, script := range []string{"echo secret >&2; exit 7", "head -c 100000 /dev/zero"} {
		out, err := nativeOutput(context.Background(), t.TempDir(), []string{"/bin/sh"}, "-c", script)
		if err == nil || len(out) != 0 || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe diagnostic: %q %v", out, err)
		}
	}
}

// Explicit opt-in: operates only on the freshly prepared disposable run tree.
func TestNativeRefreshDisposableReplay(t *testing.T) {
	root := os.Getenv("TASK03_NATIVE_PROOF_ROOT")
	if root == "" {
		t.Skip("disposable native proof requires an explicit prepared root")
	}
	host := filepath.Join(root, "host")
	clone := ManagedClonePath(host)
	revision := gitTest(t, clone, "rev-parse", "HEAD")
	if err := os.MkdirAll(StateDir(host), 0700); err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(ownership{1, clone, approvedOrigin})
	writeTest(t, filepath.Join(StateDir(host), "ownership.json"), string(marker))
	if err := saveState(StateDir(host), checkoutState{Revision: revision, LastSuccess: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"revision": revision, "runtime_argv": approvedTestArgv, "status": "fail"}
	var commands []map[string]any
	defer func() {
		report["commands"] = commands
		data, _ := json.MarshalIndent(report, "", "  ")
		_ = os.WriteFile(filepath.Join(root, "native-results.json"), data, 0600)
	}()
	realExecute := executeNative
	executeNative = func(ctx context.Context, home string, argv []string, args ...string) ([]byte, error) {
		start := time.Now()
		out, err := nativeOutput(ctx, home, argv, args...)
		code := 0
		diagnostic := "success"
		if err != nil {
			code = 1
			diagnostic = err.Error()
		}
		commands = append(commands, map[string]any{"command_argv": append(append([]string{}, argv...), args...), "codex_home": home, "exit_code": code, "elapsed_seconds": time.Since(start).Seconds(), "diagnostic": diagnostic})
		return out, err
	}
	t.Cleanup(func() { executeNative = realExecute })
	targets := func(home string) map[string]string {
		result := map[string]string{}
		for _, rel := range []string{"config.toml", "plugins/cache/agent-deck/agent-deck/1.3.3/.codex-plugin/plugin.json", "plugins/cache/agent-deck/agent-deck/1.3.3/skills/agent-deck/SKILL.md", "plugins/cache/agent-deck/agent-deck-mcp/0.1.1/.codex-plugin/plugin.json", "plugins/cache/agent-deck/agent-deck-mcp/0.1.1/.mcp.json"} {
			data, err := os.ReadFile(filepath.Join(home, rel))
			if err != nil {
				t.Fatal(err)
			}
			result[rel] = fmt.Sprintf("%x", sha256.Sum256(data))
		}
		return result
	}
	first, second := filepath.Join(root, "a"), filepath.Join(root, "b")
	report["a_before"], report["b_before"] = targets(first), targets(second)
	t.Setenv("CODEX_HOME", first)
	_ = Run(context.Background(), Request{host, first, approvedTestArgv})
	report["a_after"] = targets(first)
	if !reflect.DeepEqual(report["b_before"], targets(second)) {
		t.Fatal("profile B changed during A refresh")
	}
	t.Setenv("CODEX_HOME", second)
	_ = Run(context.Background(), Request{host, second, approvedTestArgv})
	report["b_after"] = targets(second)
	receipts, _ := filepath.Glob(filepath.Join(StateDir(host), "profiles", "*.json"))
	if len(receipts) != 2 {
		t.Fatalf("native replay receipt count %d (see update.log)", len(receipts))
	}
	report["receipt_paths"] = receipts
	failed := filepath.Join(root, "failure")
	before := targets(failed)
	report["failure_before"] = before
	executeWithLog := executeNative
	executeNative = func(ctx context.Context, home string, argv []string, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "plugin" && args[1] == "add" && args[2] == nativeSelectors[1] {
			// Hash the same explicit targets in the protected backup; no whole-home walk.
			backup := map[string]string{}
			for rel := range before {
				p := filepath.Join(failed, ".agent-deck-refresh/pending/before", strings.TrimPrefix(rel, "plugins/cache/agent-deck/"))
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				backup[rel] = fmt.Sprintf("%x", sha256.Sum256(data))
			}
			report["failure_backup"] = backup
			if !reflect.DeepEqual(before, backup) {
				t.Fatal("immediate native backup mismatch")
			}
			configPath := filepath.Join(failed, "config.toml")
			f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString("\n[projects.task03-unrelated]\ntrust_level = \"trusted\"\n")
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			// Temporarily remove only the second plugin's source from this disposable
			// managed clone; execute the real, unchanged Task 01 native add argv.
			source := filepath.Join(clone, "plugins/agent-deck-mcp")
			saved := filepath.Join(root, "held-mcp-source")
			if err := os.Rename(source, saved); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Rename(saved, source); err != nil {
					t.Fatal(err)
				}
			}()
			return executeWithLog(ctx, home, argv, args...)
		}
		return executeWithLog(ctx, home, argv, args...)
	}
	t.Setenv("CODEX_HOME", failed)
	_ = Run(context.Background(), Request{host, failed, approvedTestArgv})
	after := targets(failed)
	report["failure_after_restore"] = after
	for rel, hash := range before {
		if rel != "config.toml" && after[rel] != hash {
			t.Fatalf("rollback mismatch: %s", rel)
		}
	}
	config, _ := os.ReadFile(filepath.Join(failed, "config.toml"))
	if !strings.Contains(string(config), "projects.task03-unrelated") {
		t.Fatal("unrelated drift lost")
	}
	receipts, _ = filepath.Glob(filepath.Join(StateDir(host), "profiles", "*.json"))
	if len(receipts) != 2 {
		t.Fatal("partial failure published receipt")
	}
	report["commands"] = commands
	report["unrelated_drift_preserved"] = true
	beforeConfig, err := os.ReadFile(filepath.Join(failed, ".agent-deck-refresh/last/before/config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var parsedBefore, parsedAfter map[string]any
	if _, err := toml.Decode(string(beforeConfig), &parsedBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(string(config), &parsedAfter); err != nil {
		t.Fatal(err)
	}
	differences := map[string]any{}
	keys := map[string]bool{}
	for key := range parsedBefore {
		keys[key] = true
	}
	for key := range parsedAfter {
		keys[key] = true
	}
	for key := range keys {
		if !reflect.DeepEqual(parsedBefore[key], parsedAfter[key]) {
			differences[key] = map[string]any{"before": parsedBefore[key], "after": parsedAfter[key]}
		}
	}
	if len(differences) != 1 || differences["projects"] == nil {
		t.Fatalf("unexpected semantic config drift: %v", differences)
	}
	report["config_semantic_difference"] = differences
	report["status"] = "pass"
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "native-results.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("two profiles refreshed at %s; second-plugin failure restored targeted caches and preserved trust drift; %d native commands", revision, len(commands))
}

func TestNativeRefreshConcurrentDisableStopsSecondOperation(t *testing.T) {
	home, checkout := nativeFixture(t)
	original := executeNative
	executeNative = func(ctx context.Context, home string, argv []string, args ...string) ([]byte, error) {
		out, err := original(ctx, home, argv, args...)
		if len(args) > 2 && args[1] == "add" && args[2] == nativeSelectors[0] {
			cfg, _ := os.ReadFile(filepath.Join(home, "config.toml"))
			writeTest(t, filepath.Join(home, "config.toml"), strings.ReplaceAll(string(cfg), "enabled = true", "enabled = false"))
		}
		return out, err
	}
	err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout)
	if err == nil {
		t.Fatal("concurrent disable was ignored")
	}
	calls, _ := os.ReadFile(filepath.Join(home, "calls"))
	if string(calls) != "agent-deck\n" {
		t.Fatalf("disabled second selector mutated: %q", calls)
	}
}

func TestNativeRefreshCrashProcess(t *testing.T) {
	home := os.Getenv("TASK03_CRASH_HOME")
	if home == "" {
		t.Skip("crash helper")
	}
	executeNative = func(ctx context.Context, home string, argv []string, args ...string) ([]byte, error) {
		out, err := nativeOutput(ctx, home, []string{os.Getenv("TASK03_CRASH_BIN"), "--disable", "apps"}, args...)
		if len(args) > 2 && args[1] == "add" && args[2] == nativeSelectors[0] && err == nil {
			os.Exit(91)
		}
		return out, err
	}
	_ = RefreshProfile(context.Background(), home, approvedTestArgv, Checkout{Path: os.Getenv("TASK03_CRASH_SOURCE"), Revision: strings.Repeat("a", 40)})
	t.Fatal("crash stage was not reached")
}

func TestNativeRefreshCrashRetainsVerifiedRecovery(t *testing.T) {
	home, checkout := nativeFixture(t)
	if err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(checkout.Path, "skills/example/SKILL.md"), "changed before crash")
	bin, err := os.ReadFile(filepath.Join(home, "fixture-binary"))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestNativeRefreshCrashProcess$")
	cmd.Env = append(os.Environ(), "TASK03_CRASH_HOME="+home, "TASK03_CRASH_SOURCE="+checkout.Path, "TASK03_CRASH_BIN="+string(bin))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 91 {
		t.Fatalf("crash helper: %s %v", out, err)
	}
	backup := filepath.Join(home, ".agent-deck-refresh/pending/before/agent-deck/1.0.0/skills/example/SKILL.md")
	data, err := os.ReadFile(backup)
	if err != nil || string(data) != "new skill" {
		t.Fatalf("crash backup: %q %v", data, err)
	}
	if err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout); err == nil || err.Error() != "marketplace: native recovery required" {
		t.Fatalf("crash recovery guard: %v", err)
	}
}

func TestNativeRefreshMixedSelectorsPreserveDisabledCache(t *testing.T) {
	home, checkout := nativeFixture(t)
	cfg, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	writeTest(t, filepath.Join(home, "config.toml"), strings.Replace(string(cfg), "[plugins.\"agent-deck-mcp@agent-deck\"]\nenabled = true", "[plugins.\"agent-deck-mcp@agent-deck\"]\nenabled = false", 1))
	cache := cacheRoot(home, nativeSelectors[1])
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(cache, "preserve"), "disabled snapshot")
	if err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(home, "calls"))
	if string(calls) != "agent-deck\n" {
		t.Fatalf("selector operations: %q", calls)
	}
	data, _ := os.ReadFile(filepath.Join(cache, "preserve"))
	if string(data) != "disabled snapshot" {
		t.Fatal("disabled cache changed")
	}
}

func TestNativeRefreshRollbackPreservesModesAndSymlinks(t *testing.T) {
	home, checkout := nativeFixture(t)
	if err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(cacheRoot(home, nativeSelectors[0]), "1.0.0")
	path := filepath.Join(root, "skills/example/SKILL.md")
	if err := os.Chmod(path, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("skills/example/SKILL.md", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(home, "mode"), "fail-second")
	if err := RefreshProfile(context.Background(), home, approvedTestArgv, checkout); err == nil {
		t.Fatal("failure missing")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0750 {
		t.Fatalf("mode: %v %v", info, err)
	}
	link, err := os.Readlink(filepath.Join(root, "link"))
	if err != nil || link != "skills/example/SKILL.md" {
		t.Fatalf("symlink: %q %v", link, err)
	}
}
