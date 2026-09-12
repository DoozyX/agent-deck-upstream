package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/usage"
)

// Config fixtures for the helper process. Each is the whole config.toml the
// child sees; the child reads no other config file.
const (
	// usageRecommendConfigDefaultTool has no [orchestrate] block, so the tool
	// strategy is empty and the candidate list collapses to default_tool.
	usageRecommendConfigDefaultTool = "default_tool = \"claude\"\n"

	// usageRecommendConfigAuto reaches the auto strategy's multi-candidate
	// path. gemini is in the failover list on purpose: it is a tool with no
	// usage provider, which the recommender scores as the unknown state.
	usageRecommendConfigAuto = "default_tool = \"claude\"\n\n" +
		"[orchestrate]\ntool_strategy = \"auto\"\n\n" +
		"[usage.policy]\nfailover = [\"claude\", \"codex\", \"gemini\"]\n"

	// usageRecommendConfigBadPolicy loads cleanly — the loader validates
	// [usage.policy] by shape only — and then fails usage.PolicyFromConfig,
	// because 90 inverts against the default constrained_below of 35.
	usageRecommendConfigBadPolicy = "default_tool = \"claude\"\n\n" +
		"[usage.policy]\nexhausted_below = 90\n"
)

// usageRecommendFakeOpenUsage logs the provider argument it was called with and
// answers claude with a healthy weekly window and codex with a remaining of
// -1, which parseWindow passes through unclamped.
const usageRecommendFakeOpenUsage = "#!/bin/sh\n" +
	"printf '%s\\n' \"$1\" >> \"$AGENT_DECK_FAKE_OPENUSAGE_LOG\"\n" +
	"case \"$1\" in\n" +
	"  claude) printf '%s\\n' '{\"limits\":{\"weekly\":{\"remaining\":80}}}' ;;\n" +
	"  codex)  printf '%s\\n' '{\"limits\":{\"weekly\":{\"remaining\":-1}}}' ;;\n" +
	"  *) exit 1 ;;\n" +
	"esac\n"

// usageRecommendEnv is the child environment one helper run observes.
type usageRecommendEnv struct {
	// openusage is the body of the fake `openusage` script. Empty leaves the
	// child with no openusage anywhere on its PATH, which is this machine's
	// real state and the case the command must answer with the unknown state.
	openusage string
	// config is written to the child's config.toml. Empty writes no file, so
	// the child loads the built-in defaults — a stock install.
	config string
	// tools are stub executables placed on the child's PATH. The child's PATH
	// is that directory ALONE, so which tools count as installed is a property
	// of the test rather than of the host.
	tools []string
	// logPath is filled in by runUsageRecommendHelper; the fake openusage
	// appends one line per call to it.
	logPath string
}

// runUsageRecommendHelper re-execs the test binary as `agent-deck usage
// recommend <args>`. It follows TestUsageHelperProcess's pattern in
// usage_cmd_test.go, with the fake payload, the PATH and the config as
// per-test parameters.
func runUsageRecommendHelper(t *testing.T, env *usageRecommendEnv, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	if env == nil {
		env = &usageRecommendEnv{}
	}
	home := t.TempDir()
	binDir := t.TempDir()
	env.logPath = filepath.Join(t.TempDir(), "openusage-calls.log")

	if env.openusage != "" {
		if err := os.WriteFile(filepath.Join(binDir, "openusage"), []byte(env.openusage), 0o755); err != nil {
			t.Fatalf("write fake openusage: %v", err)
		}
	}
	for _, tool := range env.tools {
		if err := os.WriteFile(filepath.Join(binDir, tool), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", tool, err)
		}
	}
	configHome := filepath.Join(home, ".config")
	if env.config != "" {
		dir := filepath.Join(configHome, "agent-deck")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(env.config), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestUsageRecommendHelperProcess", "--", "recommend"}, args...)...)
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_USAGE_RECOMMEND_HELPER=1",
		"HOME="+home,
		"XDG_CONFIG_HOME="+configHome,
		"PATH="+binDir,
		"AGENT_DECK_FAKE_OPENUSAGE_LOG="+env.logPath,
	)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err == nil {
		return out.String(), errOut.String(), 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run helper: %v (stderr=%q)", err, errOut.String())
	}
	return out.String(), errOut.String(), exitErr.ExitCode()
}

// usageRecommendCalls returns the provider arguments the fake openusage was
// called with, in call order.
func usageRecommendCalls(t *testing.T, env *usageRecommendEnv) []string {
	t.Helper()
	raw, err := os.ReadFile(env.logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read openusage call log: %v", err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

func TestUsageRecommendHelperProcess(t *testing.T) {
	if os.Getenv("AGENT_DECK_USAGE_RECOMMEND_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	handleUsage("", args[1:])
	os.Exit(0)
}

func TestUsageRecommendRejectsBadFlags(t *testing.T) {
	cases := []struct {
		name   string
		config string
		args   []string
		stderr string
	}{
		{name: "role omitted", args: []string{"--tier", "mid"}, stderr: "--role is required"},
		{name: "role empty", args: []string{"--role", "  ", "--tier", "mid"}, stderr: "--role is required"},
		{name: "tier omitted", args: []string{"--role", "implementer"}, stderr: "--tier"},
		{name: "tier empty", args: []string{"--role", "implementer", "--tier", ""}, stderr: "--tier"},
		{name: "tier unrecognised", args: []string{"--role", "implementer", "--tier", "titanic"}, stderr: "--tier"},
		{
			// The config names a tool, so without the registry check this
			// invocation would reach a decision and exit 0.
			name:   "prefer names no known tool",
			config: usageRecommendConfigDefaultTool,
			args:   []string{"--role", "implementer", "--tier", "mid", "--prefer", "nosuchtool"},
			stderr: "not a known tool",
		},
		{
			name:   "positional argument",
			config: usageRecommendConfigDefaultTool,
			args:   []string{"--role", "implementer", "--tier", "mid", "stray"},
			stderr: "flags only",
		},
		{name: "unknown flag", args: []string{"--role", "implementer", "--tier", "mid", "--nope"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runUsageRecommendHelper(t, &usageRecommendEnv{config: tc.config}, tc.args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (stdout=%q stderr=%q)", code, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if tc.stderr != "" && !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, tc.stderr)
			}
		})
	}
}

// TestUsageRecommendPreferIsCheckedOnlyWhenSet covers the two --prefer shapes
// that are NOT flag errors: an omitted flag, and a tool the registry knows that
// has no usage provider.
func TestUsageRecommendPreferIsCheckedOnlyWhenSet(t *testing.T) {
	t.Run("omitted prefer resolves from the config", func(t *testing.T) {
		stdout, stderr, code := runUsageRecommendHelper(t,
			&usageRecommendEnv{config: usageRecommendConfigDefaultTool},
			"--role", "implementer", "--tier", "mid")
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr=%q)", code, stderr)
		}
		if !strings.HasPrefix(stdout, "claude ") {
			t.Fatalf("stdout = %q, want the headline to start with the configured tool", stdout)
		}
	})

	t.Run("known tool with no usage provider is a decision", func(t *testing.T) {
		stdout, stderr, code := runUsageRecommendHelper(t,
			&usageRecommendEnv{config: usageRecommendConfigDefaultTool},
			"--role", "implementer", "--tier", "mid", "--prefer", "opencode")
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr=%q)", code, stderr)
		}
		// opencode has no usage provider, so it has no model ladder either:
		// the headline carries no model field at all rather than an empty one.
		if got, want := firstLine(stdout), "opencode tier=mid state=unknown"; got != want {
			t.Fatalf("headline = %q, want %q", got, want)
		}
	})
}

func TestUsageRecommendWithoutOpenUsageReportsUnknown(t *testing.T) {
	stdout, stderr, code := runUsageRecommendHelper(t,
		&usageRecommendEnv{config: usageRecommendConfigDefaultTool},
		"--role", "implementer", "--tier", "mid")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	if got, want := firstLine(stdout), "claude sonnet tier=mid state=unknown"; got != want {
		t.Fatalf("headline = %q, want %q", got, want)
	}
	// One candidate means no alternatives, and no alternatives means no
	// table — not a bare header with no rows under it.
	if got := strings.Split(strings.TrimRight(stdout, "\n"), "\n"); len(got) != 2 {
		t.Fatalf("stdout = %q, want exactly a headline and a reason line, got %d lines", stdout, len(got))
	}
	// This run NAMES a tool, so the empty-tool remedy must not fire. The
	// guard's body is covered by TestUsageRecommendWithoutCandidateToolStillDecides;
	// this negative is what covers the guard's CONDITION, which is otherwise
	// unobservable now that the empty-tool branch exits 0 like every other
	// decision. Without it, dropping the `if` and keeping the body prints a
	// false remedy on every healthy run and no test notices.
	if strings.Contains(stderr, "no candidate tool") {
		t.Fatalf("stderr = %q, want no remedy on a decision that names a tool", stderr)
	}
}

// TestUsageRecommendFrontierReasonIsPassedThrough pins what the second line
// carries on a machine with no openusage: usage.Decision.Reason verbatim,
// including the frontier gate clause the recommender writes when there is no
// snapshot to read the gate window from.
func TestUsageRecommendFrontierReasonIsPassedThrough(t *testing.T) {
	stdout, stderr, code := runUsageRecommendHelper(t,
		&usageRecommendEnv{config: usageRecommendConfigDefaultTool},
		"--role", "reviewer", "--tier", "frontier")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("stdout = %q, want a headline and a reason line", stdout)
	}
	if !strings.Contains(lines[1], "frontier gate window not present in the snapshot") {
		t.Fatalf("reason line = %q, want the recommender's own reason text", lines[1])
	}
}

func TestUsageRecommendQueriesOnlyCandidateProviders(t *testing.T) {
	env := &usageRecommendEnv{
		config:    usageRecommendConfigDefaultTool,
		openusage: usageRecommendFakeOpenUsage,
	}
	stdout, stderr, code := runUsageRecommendHelper(t, env, "--role", "implementer", "--tier", "mid")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	if got, want := usageRecommendCalls(t, env), []string{"claude"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("openusage calls = %v, want %v (stdout=%q)", got, want, stdout)
	}
}

// TestUsageRecommendAlternativesTellUnknownFromNegativeRemaining pins the one
// distinction the remaining% column cannot make from the number alone:
// usage.Alternative.RemainingPercent is -1 both for an unknown state and for a
// provider that genuinely reported -1.
func TestUsageRecommendAlternativesTellUnknownFromNegativeRemaining(t *testing.T) {
	env := &usageRecommendEnv{
		config:    usageRecommendConfigAuto,
		openusage: usageRecommendFakeOpenUsage,
		tools:     []string{"claude", "codex", "gemini"},
	}
	stdout, stderr, code := runUsageRecommendHelper(t, env, "--role", "implementer", "--tier", "mid")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if fields := strings.Fields(line); len(fields) == 3 {
			rows[fields[0]] = fields[1] + " " + fields[2]
		}
	}
	if got, want := rows["codex"], "exhausted -1"; got != want {
		t.Fatalf("codex row = %q, want %q (stdout=%q)", got, want, stdout)
	}
	if got, want := rows["gemini"], "unknown -"; got != want {
		t.Fatalf("gemini row = %q, want %q (stdout=%q)", got, want, stdout)
	}
	if !strings.Contains(stdout, "remaining%") {
		t.Fatalf("stdout = %q, want the alternatives table header", stdout)
	}
}

func TestUsageRecommendJSONShape(t *testing.T) {
	stdout, stderr, code := runUsageRecommendHelper(t,
		&usageRecommendEnv{config: usageRecommendConfigDefaultTool},
		"--role", "implementer", "--tier", "mid", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%q)", code, stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	want := []string{"account", "alternatives", "fetched_at", "model", "provider", "reason", "state", "tier_applied", "tier_requested", "tool"}
	if got := sortedJSONKeys(decoded); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if decoded["tool"] != "claude" || decoded["tier_requested"] != "mid" || decoded["state"] != usage.StateUnknown {
		t.Fatalf("decision = %v", decoded)
	}
	// No snapshot was fetched, so there is no fetch time. The zero time would
	// render as "0001-01-01T00:00:00Z" and read as a real one.
	if decoded["fetched_at"] != nil {
		t.Fatalf("fetched_at = %v, want null", decoded["fetched_at"])
	}
}

// TestUsageRecommendJSONMirrorsDecisionKeys pins usageRecommendJSON against
// usage.Decision, so a field added to the Decision cannot be dropped from the
// CLI's output unnoticed.
func TestUsageRecommendJSONMirrorsDecisionKeys(t *testing.T) {
	decision, err := json.Marshal(usage.Decision{})
	if err != nil {
		t.Fatalf("marshal decision: %v", err)
	}
	payload, err := json.Marshal(usageRecommendJSON{})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var decisionKeys, payloadKeys map[string]any
	if err := json.Unmarshal(decision, &decisionKeys); err != nil {
		t.Fatalf("decode decision: %v", err)
	}
	if err := json.Unmarshal(payload, &payloadKeys); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got, want := sortedJSONKeys(payloadKeys), sortedJSONKeys(decisionKeys); !reflect.DeepEqual(got, want) {
		t.Fatalf("payload keys = %v, want usage.Decision's %v", got, want)
	}
}

func TestUsageRecommendInvalidUsagePolicyIsFatal(t *testing.T) {
	stdout, stderr, code := runUsageRecommendHelper(t,
		&usageRecommendEnv{config: usageRecommendConfigBadPolicy},
		// --prefer names a tool, so a policy error that fell through to the
		// zero Policy would still reach a decision and exit 0.
		"--role", "implementer", "--tier", "mid", "--prefer", "claude")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	// 35 is the DEFAULT constrained_below, which only usage.PolicyFromConfig
	// knows: the loader validates shape alone and never hardcodes the
	// defaults. Naming it pins which layer rejected this config.
	if !strings.Contains(stderr, "must be <= constrained_below 35") {
		t.Fatalf("stderr = %q, want usage.PolicyFromConfig's merged-threshold error", stderr)
	}
	if strings.Contains(stderr, "load config") {
		t.Fatalf("stderr = %q, want the config to have loaded before the policy merge rejected it", stderr)
	}
}

// TestUsageRecommendWithoutCandidateToolStillDecides covers a stock install:
// no config file, so no default_tool and no tool strategy, and no --prefer.
// The recommender answers with an empty tool and an empty model, and no flag
// was wrong, so that is a decision: exit 0, the whole decision on stdout, the
// remedy on stderr. Exit 2 here would hand a --json consumer an empty stdout
// where the contract promises a decision.
func TestUsageRecommendWithoutCandidateToolStillDecides(t *testing.T) {
	assertRemedy := func(t *testing.T, stderr string) {
		t.Helper()
		for _, remedy := range []string{"no candidate tool", "--prefer", "default_tool"} {
			if !strings.Contains(stderr, remedy) {
				t.Fatalf("stderr = %q, want it to name %q", stderr, remedy)
			}
		}
	}

	t.Run("text", func(t *testing.T) {
		stdout, stderr, code := runUsageRecommendHelper(t, nil, "--role", "implementer", "--tier", "mid")
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
		}
		// stdout carries the decision and nothing else: the remedy belongs on
		// stderr, so it cannot be interleaved into the two lines below.
		lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("stdout = %q, want exactly a headline and a reason line, got %d lines", stdout, len(lines))
		}
		// The empty tool is omitted from the headline the way an empty model
		// is: the line starts at "tier=", with no leading empty field.
		if got, want := lines[0], "tier=mid state=unknown"; got != want {
			t.Fatalf("headline = %q, want %q", got, want)
		}
		if got, want := lines[1], `no candidate tools for tool strategy ""`; got != want {
			t.Fatalf("reason line = %q, want the recommender's own reason text %q", got, want)
		}
		assertRemedy(t, stderr)
	})

	t.Run("json", func(t *testing.T) {
		stdout, stderr, code := runUsageRecommendHelper(t, nil, "--role", "implementer", "--tier", "mid", "--json")
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
		}
		// Unmarshal over the WHOLE of stdout, so a remedy printed there too
		// would fail to decode rather than be skipped past.
		var decoded map[string]any
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatalf("decode stdout %q: %v", stdout, err)
		}
		want := []string{"account", "alternatives", "fetched_at", "model", "provider", "reason", "state", "tier_applied", "tier_requested", "tool"}
		if got := sortedJSONKeys(decoded); !reflect.DeepEqual(got, want) {
			t.Fatalf("keys = %v, want %v", got, want)
		}
		if decoded["tool"] != "" || decoded["tier_applied"] != "mid" || decoded["state"] != usage.StateUnknown {
			t.Fatalf("decision = %v, want an empty tool at tier mid in the unknown state", decoded)
		}
		assertRemedy(t, stderr)
	})
}

// TestUsageRecommendRemedyPrecedesTheDecisionOnOneStream pins the ordering
// half of the empty-tool guard's comment: the remedy is written before stdout
// is touched, so a terminal cannot interleave it into the decision.
//
// runUsageRecommendHelper cannot see this. It gives cmd.Stdout and cmd.Stderr
// two SEPARATE buffers, so the relative order of a write to one and a write to
// the other is simply not recorded anywhere. Pointing both at one buffer is
// what a terminal does, and os/exec hands the child a single pipe when the two
// writers are the same value — so the buffer records the child's own write
// order.
func TestUsageRecommendRemedyPrecedesTheDecisionOnOneStream(t *testing.T) {
	home := t.TempDir()
	binDir := t.TempDir()

	// No config file and no openusage: the stock install of
	// TestUsageRecommendWithoutCandidateToolStillDecides, which is the only
	// case that writes to both streams.
	cmd := exec.Command(os.Args[0], "-test.run=TestUsageRecommendHelperProcess", "--",
		"recommend", "--role", "implementer", "--tier", "mid")
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_USAGE_RECOMMEND_HELPER=1",
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"PATH="+binDir,
	)
	var merged bytes.Buffer
	cmd.Stdout, cmd.Stderr = &merged, &merged
	if err := cmd.Run(); err != nil {
		t.Fatalf("run helper: %v (output=%q)", err, merged.String())
	}
	out := merged.String()

	// Both anchors are unique to their own stream: the remedy says "tool ("
	// singular with a parenthesis, while the reason line on stdout says
	// "tools for tool strategy" plural with none.
	remedy := strings.Index(out, "no candidate tool (")
	headline := strings.Index(out, "tier=mid state=unknown")
	if remedy < 0 || headline < 0 {
		t.Fatalf("output = %q, want it to carry both the stderr remedy and the stdout headline", out)
	}
	if remedy > headline {
		t.Fatalf("output = %q, want the stderr remedy at %d to precede the stdout headline at %d", out, remedy, headline)
	}
}

// TestUsageRecommendJSONCarriesAlternativeElements pins the alternatives array
// at the JSON boundary, which is the surface the orchestrate skill consumes.
// TestUsageRecommendJSONShape runs on a single-candidate config, so the array
// it sees is empty; only the auto fixture populates it.
func TestUsageRecommendJSONCarriesAlternativeElements(t *testing.T) {
	env := &usageRecommendEnv{
		config:    usageRecommendConfigAuto,
		openusage: usageRecommendFakeOpenUsage,
		tools:     []string{"claude", "codex", "gemini"},
	}
	stdout, stderr, code := runUsageRecommendHelper(t, env, "--role", "implementer", "--tier", "mid", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	var decoded struct {
		Alternatives []map[string]any `json:"alternatives"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	// codex reported -1 and gemini has no usage provider at all: the same
	// remaining_percent for two different states, which is exactly why the
	// consumer needs `state` alongside the number.
	want := []map[string]any{
		{"tool": "codex", "state": usage.StateExhausted, "remaining_percent": float64(-1)},
		{"tool": "gemini", "state": usage.StateUnknown, "remaining_percent": float64(-1)},
	}
	if len(decoded.Alternatives) != len(want) {
		t.Fatalf("alternatives = %v, want %d elements (stdout=%q)", decoded.Alternatives, len(want), stdout)
	}
	for i, wantElem := range want {
		got := decoded.Alternatives[i]
		if !reflect.DeepEqual(got, wantElem) {
			t.Fatalf("alternatives[%d] = %v, want %v (stdout=%q)", i, got, wantElem, stdout)
		}
	}
}

// TestUsageRecommendProfileReachesTheRecommender pins --profile as more than a
// parsed flag: the recommender matches snapshots by account label, and a
// profile no snapshot carries earns its own clause in the reason. A --profile
// that never reached usage.Request would lose that clause silently.
func TestUsageRecommendProfileReachesTheRecommender(t *testing.T) {
	stdout, stderr, code := runUsageRecommendHelper(t,
		&usageRecommendEnv{config: usageRecommendConfigDefaultTool},
		"--role", "implementer", "--tier", "mid", "--profile", "Codex")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if want := `no snapshot for profile "Codex"`; !strings.Contains(stdout, want) {
		t.Fatalf("stdout = %q, want the reason to contain %q", stdout, want)
	}
}

// TestUsageRecommendFailedQueryKeepsTheAccount pins usageRecommendSnapshots'
// documented behaviour: a query that fails becomes an unavailable snapshot
// rather than being dropped. The difference is visible in `account` — the
// snapshot is what the recommender scored, so dropping it would leave the
// decision naming no account at all. That is the whole of what this test
// pins: run under a mutant that drops the failed snapshot,
// TestUsageRecommendProfileReachesTheRecommender still PASSES, because its
// `no snapshot for profile "Codex"` clause survives with zero snapshots.
func TestUsageRecommendFailedQueryKeepsTheAccount(t *testing.T) {
	// No openusage anywhere on the child's PATH, so every query fails.
	stdout, stderr, code := runUsageRecommendHelper(t,
		&usageRecommendEnv{config: usageRecommendConfigDefaultTool},
		"--role", "implementer", "--tier", "mid", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if got, want := decoded["account"], "Claude"; got != want {
		t.Fatalf("account = %v, want %q — the failed query's snapshot is what carries it (stdout=%q)", got, want, stdout)
	}
}

func TestUsageRecommendRemainingRendersNegativeReadings(t *testing.T) {
	cases := []struct {
		alt  usage.Alternative
		want string
	}{
		{usage.Alternative{State: usage.StateUnknown, RemainingPercent: -1}, "-"},
		{usage.Alternative{State: usage.StateExhausted, RemainingPercent: -1}, "-1"},
		{usage.Alternative{State: usage.StateHealthy, RemainingPercent: 0}, "0"},
	}
	for _, tc := range cases {
		if got := usageRecommendRemaining(tc.alt); got != tc.want {
			t.Fatalf("remaining(%+v) = %q, want %q", tc.alt, got, tc.want)
		}
	}
}

func TestUsageRecommendPayloadKeepsRealFetchTime(t *testing.T) {
	fetched := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	payload := usageRecommendPayload(usage.Decision{FetchedAt: fetched})
	if payload.FetchedAt == nil || !payload.FetchedAt.Equal(fetched) {
		t.Fatalf("fetched_at = %v, want %v", payload.FetchedAt, fetched)
	}
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func sortedJSONKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
