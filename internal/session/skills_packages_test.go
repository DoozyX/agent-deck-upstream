package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

type skillsExecCall struct {
	dir  string
	name string
	args []string
}

func stubSkillsEnv(t *testing.T, groups []SkillsGroup) (*[]skillsExecCall, func(func(dir string, args []string) ([]byte, error))) {
	t.Helper()
	var (
		mu       sync.Mutex
		recorded []skillsExecCall
		respFn   = func(dir string, args []string) ([]byte, error) { return nil, nil }
	)
	origExec, origGroups, origLock := skillsExec, skillsGroupSource, pluginLockAcquireFn
	skillsExec = func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
		mu.Lock()
		recorded = append(recorded, skillsExecCall{dir: dir, name: name, args: args})
		fn := respFn
		mu.Unlock()
		return fn(dir, args)
	}
	skillsGroupSource = func() []SkillsGroup { return groups }
	pluginLockAcquireFn = func(string) (func(), error) { return func() {}, nil }
	t.Cleanup(func() { skillsExec, skillsGroupSource, pluginLockAcquireFn = origExec, origGroups, origLock })
	return &recorded, func(fn func(string, []string) ([]byte, error)) {
		mu.Lock()
		respFn = fn
		mu.Unlock()
	}
}

func gitRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func skillsTestConfig(groupPaths ...string) *UserConfig {
	cfg := &UserConfig{
		SkillsPackages: map[string]SkillsPackageDef{
			"tam": {
				Source:     "ssh://forgejo@git.example.com/dev/tam-tools.git",
				FullDepth:  true,
				AutoUpdate: true,
				Agents:     []string{"claude-code", "codex", "cursor"},
			},
		},
		Groups: map[string]GroupSettings{},
	}
	for _, p := range groupPaths {
		cfg.Groups[p] = GroupSettings{SkillsPackages: []string{"tam"}}
	}
	return cfg
}

func TestGetGroupSkillsPackages_UnionDedupSkipsUnknown(t *testing.T) {
	cfg := &UserConfig{
		SkillsPackages: map[string]SkillsPackageDef{"a": {Source: "x"}, "b": {Source: "y"}},
		Groups: map[string]GroupSettings{
			"root":       {SkillsPackages: []string{"a", "ghost"}},
			"root/child": {SkillsPackages: []string{"b", "a"}},
		},
	}
	got := cfg.GetGroupSkillsPackages("root/child")
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := cfg.GetGroupSkillsPackages("other"); len(got) != 0 {
		t.Fatalf("unattached group got %v", got)
	}
}

func TestRefreshSkillsPackages_SkipsNonGitDefaultPath(t *testing.T) {
	plain := t.TempDir()
	calls, _ := stubSkillsEnv(t, []SkillsGroup{{Path: "adaptam", DefaultPath: plain}})
	if err := RefreshSkillsPackages(context.Background(), skillsTestConfig("adaptam")); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("expected no exec, got %v", *calls)
	}
}

func TestRefreshSkillsPackages_AddWhenNoLockfile(t *testing.T) {
	repo := gitRepoDir(t)
	calls, _ := stubSkillsEnv(t, []SkillsGroup{{Path: "adaptam/ui", DefaultPath: repo}})
	if err := RefreshSkillsPackages(context.Background(), skillsTestConfig("adaptam")); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 exec, got %v", *calls)
	}
	c := (*calls)[0]
	if c.dir != repo || c.name != "npx" {
		t.Fatalf("dir/name = %q/%q", c.dir, c.name)
	}
	want := []string{"--yes", "skills", "add", "ssh://forgejo@git.example.com/dev/tam-tools.git",
		"--full-depth", "--skill", "*", "-a", "claude-code", "codex", "cursor", "-y"}
	if !reflect.DeepEqual(c.args, want) {
		t.Fatalf("args = %v\nwant   %v", c.args, want)
	}
}

func TestRefreshSkillsPackages_UpdateWhenLockfilePresent(t *testing.T) {
	repo := gitRepoDir(t)
	if err := os.WriteFile(filepath.Join(repo, "skills-lock.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls, _ := stubSkillsEnv(t, []SkillsGroup{{Path: "adaptam", DefaultPath: repo}})
	RefreshSkillsPackages(context.Background(), skillsTestConfig("adaptam"))
	if len(*calls) != 1 {
		t.Fatalf("expected 1 exec, got %v", *calls)
	}
	want := []string{"--yes", "skills", "update", "-p", "-y"}
	if !reflect.DeepEqual((*calls)[0].args, want) {
		t.Fatalf("args = %v, want %v", (*calls)[0].args, want)
	}
}

func TestRefreshSkillsPackages_ExplicitSkillsList(t *testing.T) {
	repo := gitRepoDir(t)
	calls, _ := stubSkillsEnv(t, []SkillsGroup{{Path: "adaptam", DefaultPath: repo}})
	cfg := skillsTestConfig("adaptam")
	def := cfg.SkillsPackages["tam"]
	def.Skills = []string{"tam-commit", "tam-forgejo"}
	def.FullDepth = false
	cfg.SkillsPackages["tam"] = def
	RefreshSkillsPackages(context.Background(), cfg)
	joined := strings.Join((*calls)[0].args, " ")
	if !strings.Contains(joined, "--skill tam-commit tam-forgejo -a") || strings.Contains(joined, "--full-depth") {
		t.Fatalf("args = %s", joined)
	}
}

func TestRefreshSkillsPackages_AutoUpdateFalseSkipped(t *testing.T) {
	repo := gitRepoDir(t)
	calls, _ := stubSkillsEnv(t, []SkillsGroup{{Path: "adaptam", DefaultPath: repo}})
	cfg := skillsTestConfig("adaptam")
	def := cfg.SkillsPackages["tam"]
	def.AutoUpdate = false
	cfg.SkillsPackages["tam"] = def
	RefreshSkillsPackages(context.Background(), cfg)
	if len(*calls) != 0 {
		t.Fatalf("expected no exec, got %v", *calls)
	}
}

func TestRefreshSkillsPackages_UnknownCatalogKeyDoesNotFail(t *testing.T) {
	repo := gitRepoDir(t)
	calls, _ := stubSkillsEnv(t, []SkillsGroup{{Path: "adaptam", DefaultPath: repo}})
	cfg := skillsTestConfig()
	cfg.Groups["adaptam"] = GroupSettings{SkillsPackages: []string{"ghost", "tam"}}
	if err := RefreshSkillsPackages(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected the known package to run, got %v", *calls)
	}
}

func TestRefreshSkillsPackages_ExecFailureDoesNotAbortOthers(t *testing.T) {
	bad, good := gitRepoDir(t), gitRepoDir(t)
	calls, setResp := stubSkillsEnv(t, []SkillsGroup{
		{Path: "adaptam/a", DefaultPath: bad},
		{Path: "adaptam/b", DefaultPath: good},
	})
	setResp(func(dir string, _ []string) ([]byte, error) {
		if dir == bad {
			return []byte("boom"), errors.New("exit 1")
		}
		return nil, nil
	})
	if err := RefreshSkillsPackages(context.Background(), skillsTestConfig("adaptam")); err != nil {
		t.Fatalf("must fail open, got %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected both targets visited, got %v", *calls)
	}
}

func TestRefreshSkillsPackages_SameDirTwoGroupsRunsOnce(t *testing.T) {
	repo := gitRepoDir(t)
	calls, _ := stubSkillsEnv(t, []SkillsGroup{
		{Path: "adaptam/a", DefaultPath: repo},
		{Path: "adaptam/b", DefaultPath: repo},
	})
	RefreshSkillsPackages(context.Background(), skillsTestConfig("adaptam"))
	if len(*calls) != 1 {
		t.Fatalf("expected 1 exec for shared dir, got %v", *calls)
	}
}

func TestRunSkillsPackagesRefresh_GroupFilterAndReport(t *testing.T) {
	in, out := gitRepoDir(t), gitRepoDir(t)
	cfg := skillsTestConfig("adaptam", "other")
	calls, _ := stubSkillsEnv(t, []SkillsGroup{
		{Path: "adaptam/ui", DefaultPath: in},
		{Path: "other", DefaultPath: out},
	})
	res := RunSkillsPackagesRefresh(context.Background(), cfg, SkillsRefreshOptions{GroupFilter: "adaptam"})
	if len(*calls) != 1 || (*calls)[0].dir != in {
		t.Fatalf("filter leaked: %v", *calls)
	}
	if len(res) != 1 || res[0].Status != SkillsStatusOK || res[0].Action != "add" || res[0].Package != "tam" {
		t.Fatalf("unexpected report: %+v", res)
	}
}

func TestRunSkillsPackagesRefresh_DueGate(t *testing.T) {
	repo := gitRepoDir(t)
	cfg := skillsTestConfig("adaptam")
	calls, _ := stubSkillsEnv(t, []SkillsGroup{{Path: "adaptam", DefaultPath: repo}})
	res := RunSkillsPackagesRefresh(context.Background(), cfg, SkillsRefreshOptions{
		Due: func(dir, pkg string, def SkillsPackageDef) bool { return false },
	})
	if len(*calls) != 0 || len(res) != 0 {
		t.Fatalf("not-due target ran: calls=%v res=%+v", *calls, res)
	}
}

func TestSkillsPackageInterval(t *testing.T) {
	cfg := &UserConfig{}
	if got := cfg.skillsPackageInterval(SkillsPackageDef{}); got != 24*time.Hour {
		t.Fatalf("default = %v", got)
	}
	cfg.SkillsPackagesCheckIntervalHours = 6
	if got := cfg.skillsPackageInterval(SkillsPackageDef{}); got != 6*time.Hour {
		t.Fatalf("global = %v", got)
	}
	if got := cfg.skillsPackageInterval(SkillsPackageDef{CheckIntervalHours: 2}); got != 2*time.Hour {
		t.Fatalf("package = %v", got)
	}
}

func TestSkillsPackagesTOMLRoundTrip(t *testing.T) {
	src := `
skills_packages_check_interval_hours = 12

[skills_packages.tam]
source = "ssh://x/y.git"
full_depth = true
auto_update = true
agents = ["claude-code"]

[groups.adaptam]
skills_packages = ["tam"]
`
	cfg := &UserConfig{}
	if _, err := toml.Decode(src, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.SkillsPackagesCheckIntervalHours != 12 || cfg.SkillsPackages["tam"].Source != "ssh://x/y.git" ||
		!reflect.DeepEqual(cfg.GetGroupSkillsPackages("adaptam"), []string{"tam"}) {
		t.Fatalf("decoded %+v", cfg)
	}
}

func TestStartSkillsPackageRefresher_DoesNotBlockCaller(t *testing.T) {
	repo := gitRepoDir(t)
	_, setResp := stubSkillsEnv(t, []SkillsGroup{{Path: "adaptam", DefaultPath: repo}})
	release := make(chan struct{})
	setResp(func(string, []string) ([]byte, error) { <-release; return nil, nil })
	cfg := skillsTestConfig("adaptam")
	origLoad := skillsConfigLoader
	skillsConfigLoader = func() *UserConfig { return cfg }
	t.Cleanup(func() { skillsConfigLoader = origLoad; close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan struct{})
	go func() { StartSkillsPackageRefresher(ctx); close(returned) }()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("StartSkillsPackageRefresher blocked while exec was hung")
	}
}
