// Project-local skill packages installed with `npx skills` (vercel-labs/skills).
//
// [skills_packages.X] is a catalog; [groups.G].skills_packages attaches catalog
// keys to a group. Every group whose default_path is a git repo gets the
// resolved packages installed (or updated) there. Fail-open like plugin
// install: every error is logged per target and never surfaces to callers.

package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/safego"
)

const (
	skillsPackageDefaultIntervalHours = 24
	// Under pluginLockLegacyStaleTTL so a peer never reclaims a live install lock.
	skillsExecTimeout = 100 * time.Second
	// How often the background loop checks which targets are due.
	skillsRefresherTick = time.Hour

	SkillsStatusOK      = "ok"
	SkillsStatusFailed  = "failed"
	SkillsStatusSkipped = "skipped"
)

// SkillsPackageDef is one [skills_packages.X] catalog entry.
type SkillsPackageDef struct {
	Source             string   `toml:"source" json:"source"`
	FullDepth          bool     `toml:"full_depth,omitempty" json:"full_depth,omitempty"`
	AutoUpdate         bool     `toml:"auto_update,omitempty" json:"auto_update,omitempty"`
	Agents             []string `toml:"agents,omitempty" json:"agents,omitempty"`
	Skills             []string `toml:"skills,omitempty" json:"skills,omitempty"` // empty or ["*"] => all
	CheckIntervalHours int      `toml:"check_interval_hours,omitzero" json:"check_interval_hours,omitempty"`
}

// SkillsGroup is a group path with its default_path, the unit install targets
// are derived from.
type SkillsGroup struct {
	Path        string
	DefaultPath string
}

// SkillsPackageResult reports the outcome for one (directory, package) target.
type SkillsPackageResult struct {
	Group   string `json:"group"`
	Dir     string `json:"dir"`
	Package string `json:"package"`
	Action  string `json:"action,omitempty"` // add | update
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
}

// SkillsRefreshOptions narrows RunSkillsPackagesRefresh.
type SkillsRefreshOptions struct {
	// Groups overrides the group source (nil => skillsGroupSource).
	Groups []SkillsGroup
	// GroupFilter keeps only this group path and its descendants.
	GroupFilter string
	// Due gates each target; nil means every target is due.
	Due func(dir, pkg string, def SkillsPackageDef) bool
}

// skillsExec is the test seam for spawning `npx skills ...` in a project dir.
var skillsExec = func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(scrubbedEnvForPluginInstall(), env...)
	return cmd.CombinedOutput()
}

// skillsGroupSource is the test seam for group discovery.
var skillsGroupSource = defaultSkillsGroupSource

// skillsConfigLoader is the test seam for the background loop's config read.
var skillsConfigLoader = func() *UserConfig {
	cfg, _ := LoadUserConfig()
	return cfg
}

// GetGroupSkillsPackages returns the catalog keys attached along the group
// ancestry, root-first and deduplicated. Keys missing from the catalog are
// skipped with a warning.
func (c *UserConfig) GetGroupSkillsPackages(groupPath string) []string {
	if c == nil || groupPath == "" {
		return nil
	}
	var chain [][]string
	for p := groupPath; p != ""; p = getParentPath(p) {
		if g, ok := c.Groups[p]; ok && len(g.SkillsPackages) > 0 {
			chain = append(chain, g.SkillsPackages)
		}
	}
	seen := make(map[string]bool)
	var out []string
	for i := len(chain) - 1; i >= 0; i-- {
		for _, key := range chain[i] {
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			if _, ok := c.SkillsPackages[key]; !ok {
				sessionLog.Warn("skills_package_unknown_catalog_key",
					slog.String("group", groupPath), slog.String("package", key))
				continue
			}
			out = append(out, key)
		}
	}
	return out
}

func (c *UserConfig) skillsPackageInterval(def SkillsPackageDef) time.Duration {
	hours := def.CheckIntervalHours
	if hours <= 0 && c != nil {
		hours = c.SkillsPackagesCheckIntervalHours
	}
	if hours <= 0 {
		hours = skillsPackageDefaultIntervalHours
	}
	return time.Duration(hours) * time.Hour
}

// ResolveGroupSkillsPackages is the read-only view used by `group show --resolved`.
func ResolveGroupSkillsPackages(groupPath string) []string {
	cfg, _ := LoadUserConfig()
	return cfg.GetGroupSkillsPackages(groupPath)
}

// RefreshSkillsPackages installs or updates every attached skills package.
// Always returns nil (fail-open); per-target failures are logged.
func RefreshSkillsPackages(ctx context.Context, cfg *UserConfig) error {
	RunSkillsPackagesRefresh(ctx, cfg, SkillsRefreshOptions{})
	return nil
}

// RunSkillsPackagesRefresh is RefreshSkillsPackages with options and a
// per-target report.
func RunSkillsPackagesRefresh(ctx context.Context, cfg *UserConfig, opts SkillsRefreshOptions) []SkillsPackageResult {
	if cfg == nil || len(cfg.SkillsPackages) == 0 {
		return nil
	}
	groups := opts.Groups
	if groups == nil {
		groups = skillsGroupSource()
	}
	sorted := append([]SkillsGroup(nil), groups...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var results []SkillsPackageResult
	visited := make(map[string]bool) // dir\x00pkg
	updatedDirs := make(map[string]bool)
	for _, g := range sorted {
		if ctx.Err() != nil {
			break
		}
		if !groupMatchesFilter(g.Path, opts.GroupFilter) || strings.TrimSpace(g.DefaultPath) == "" {
			continue
		}
		pkgs := cfg.GetGroupSkillsPackages(g.Path)
		if len(pkgs) == 0 {
			continue
		}
		dir := filepath.Clean(ExpandPath(strings.TrimSpace(g.DefaultPath)))
		for _, name := range pkgs {
			key := dir + "\x00" + name
			if visited[key] {
				continue
			}
			visited[key] = true
			res := SkillsPackageResult{Group: g.Path, Dir: dir, Package: name}
			def := cfg.SkillsPackages[name]
			switch {
			case !def.AutoUpdate:
				res.Status, res.Reason = SkillsStatusSkipped, "auto_update is false"
			case strings.TrimSpace(def.Source) == "":
				res.Status, res.Reason = SkillsStatusFailed, "catalog entry has no source"
			case !isGitTarget(dir):
				res.Status, res.Reason = SkillsStatusSkipped, "default_path is not a git repo"
			case opts.Due != nil && !opts.Due(dir, name, def):
				continue
			default:
				runSkillsTarget(ctx, &res, def, updatedDirs)
			}
			if res.Status == SkillsStatusFailed {
				sessionLog.Warn("skills_package_refresh_failed",
					slog.String("group", res.Group), slog.String("dir", res.Dir),
					slog.String("package", res.Package), slog.String("reason", res.Reason))
			}
			results = append(results, res)
		}
	}
	return results
}

func groupMatchesFilter(path, filter string) bool {
	filter = strings.Trim(filter, "/")
	return filter == "" || path == filter || strings.HasPrefix(path, filter+"/")
}

func isGitTarget(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func runSkillsTarget(ctx context.Context, res *SkillsPackageResult, def SkillsPackageDef, updatedDirs map[string]bool) {
	var args []string
	if _, err := os.Stat(filepath.Join(res.Dir, "skills-lock.json")); err == nil {
		res.Action = "update"
		if updatedDirs[res.Dir] {
			res.Status, res.Reason = SkillsStatusSkipped, "dir already updated this run"
			return
		}
		args = []string{"--yes", "skills", "update", "-p", "-y"}
	} else {
		res.Action = "add"
		args = skillsAddArgs(def)
	}

	lockPath, err := skillsLockPath(res.Dir, res.Package)
	if err != nil {
		res.Status, res.Reason = SkillsStatusFailed, "lock path: "+err.Error()
		return
	}
	release, err := acquirePluginLock(lockPath)
	if err != nil {
		res.Status, res.Reason = SkillsStatusFailed, "lock: "+err.Error()
		return
	}
	defer release()

	runCtx, cancel := context.WithTimeout(ctx, skillsExecTimeout)
	defer cancel()
	out, err := skillsExec(runCtx, res.Dir, nil, "npx", args...)
	if err != nil {
		res.Status = SkillsStatusFailed
		res.Reason = fmt.Sprintf("%v: %s", err, tailString(string(out), 400))
		return
	}
	if res.Action == "update" {
		updatedDirs[res.Dir] = true
	}
	res.Status = SkillsStatusOK
	sessionLog.Info("skills_package_refreshed",
		slog.String("group", res.Group), slog.String("dir", res.Dir),
		slog.String("package", res.Package), slog.String("action", res.Action))
}

func skillsAddArgs(def SkillsPackageDef) []string {
	args := []string{"--yes", "skills", "add", def.Source}
	if def.FullDepth {
		args = append(args, "--full-depth")
	}
	skills := def.Skills
	if len(skills) == 0 {
		skills = []string{"*"}
	}
	args = append(args, "--skill")
	args = append(args, skills...)
	if len(def.Agents) > 0 {
		args = append(args, "-a")
		args = append(args, def.Agents...)
	}
	return append(args, "-y")
}

func skillsLockPath(dir, pkg string) (string, error) {
	locks, err := dataPath("locks", "locks")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(locks, fmt.Sprintf("skills-%s-%s.lock", fnv1aHex(canonicalProfileDir(dir)), pluginLockSafeName(pkg))), nil
}

func tailString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// SkillsGroupsFor merges registry groups (from the profile's storage) with
// [groups.X].default_path from cfg; a registry default_path wins for the same
// group path. Storage failures fall back to config-declared groups.
func SkillsGroupsFor(cfg *UserConfig, profile string) []SkillsGroup {
	byPath := make(map[string]string)
	if cfg != nil {
		for p, g := range cfg.Groups {
			if g.DefaultPath != "" {
				byPath[p] = g.DefaultPath
			}
		}
	}
	for _, g := range loadSkillsGroupsFromStorage(profile) {
		if g.DefaultPath != "" {
			byPath[g.Path] = g.DefaultPath
		}
	}
	out := make([]SkillsGroup, 0, len(byPath))
	for p, d := range byPath {
		out = append(out, SkillsGroup{Path: p, DefaultPath: d})
	}
	return out
}

func defaultSkillsGroupSource() []SkillsGroup {
	cfg, _ := LoadUserConfig()
	return SkillsGroupsFor(cfg, "")
}

func loadSkillsGroupsFromStorage(profile string) []SkillsGroup {
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		sessionLog.Warn("skills_package_group_load_failed", slog.String("error", err.Error()))
		return nil
	}
	defer storage.Close()
	_, groups, err := storage.LoadWithGroups()
	if err != nil {
		sessionLog.Warn("skills_package_group_load_failed", slog.String("error", err.Error()))
		return nil
	}
	out := make([]SkillsGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, SkillsGroup{Path: g.Path, DefaultPath: g.DefaultPath})
	}
	return out
}

// StartSkillsPackageRefresher runs one refresh at start, then re-checks hourly,
// refreshing each target once its package interval has elapsed. Config is
// re-read every check so edits apply without a restart. Never blocks the caller.
func StartSkillsPackageRefresher(ctx context.Context) {
	safego.Go(sessionLog, "skills-package-refresher", func() {
		var mu sync.Mutex
		lastRun := make(map[string]time.Time)
		pass := func() {
			cfg := skillsConfigLoader()
			if cfg == nil || len(cfg.SkillsPackages) == 0 {
				return
			}
			now := time.Now()
			results := RunSkillsPackagesRefresh(ctx, cfg, SkillsRefreshOptions{
				Due: func(dir, pkg string, def SkillsPackageDef) bool {
					mu.Lock()
					defer mu.Unlock()
					last, ok := lastRun[dir+"\x00"+pkg]
					return !ok || now.Sub(last) >= cfg.skillsPackageInterval(def)
				},
			})
			mu.Lock()
			for _, r := range results {
				if r.Status == SkillsStatusOK {
					lastRun[r.Dir+"\x00"+r.Package] = now
				}
			}
			mu.Unlock()
		}
		pass()
		ticker := time.NewTicker(skillsRefresherTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pass()
			}
		}
	})
}
