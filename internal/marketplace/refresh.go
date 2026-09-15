package marketplace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"golang.org/x/sys/unix"
)

const nativeRefreshTimeout = 60 * time.Second

// Test seam preserves the production argv and environment contract.
var executeNative = nativeOutput

var nativeSelectors = []string{"agent-deck@agent-deck", "agent-deck-mcp@agent-deck"}

// RefreshProfile uses the versioned native Codex install operation. It first
// inventories the profile, because plugin add would enable a disabled plugin.
func RefreshProfile(ctx context.Context, codexHome string, codexArgv []string, checkout Checkout) error {
	_, err := refreshProfile(ctx, codexHome, codexArgv, checkout)
	return err
}

func refreshProfile(ctx context.Context, codexHome string, codexArgv []string, checkout Checkout) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, nativeRefreshTimeout)
	defer cancel()
	if !validRevision(checkout.Revision) || !supportedRuntime(codexArgv) {
		return false, nil
	}
	home, err := canonicalHome(codexHome)
	if err != nil {
		return false, nil
	}
	config, err := readNativeConfig(home)
	if err != nil || !registeredClone(config, checkout.Path) {
		return false, nil
	}
	installed, err := nativeList(ctx, home, codexArgv)
	if err != nil {
		return false, err
	}
	var selectors []string
	for _, selector := range nativeSelectors {
		if installed[selector] && config.Plugins[selector].Enabled {
			selectors = append(selectors, selector)
		}
	}
	if len(selectors) == 0 {
		return false, nil
	}
	latest, err := readNativeConfig(home)
	if err != nil || !reflect.DeepEqual(config, latest) {
		return false, errors.New("marketplace: native config changed")
	}
	transaction, err := backupNative(ctx, home, selectors, config)
	if err != nil {
		return false, err
	}
	for _, selector := range selectors {
		if !transaction.configUnchanged() {
			_ = transaction.restore()
			return false, errors.New("marketplace: native rollback conflict")
		}
		if err = nativeCommand(ctx, home, codexArgv, "plugin", "add", selector, "--json"); err != nil {
			if restoreErr := transaction.restore(); restoreErr != nil {
				return false, restoreErr
			}
			return false, err
		}
		// A failed or unverifiable command cannot establish ownership of new bytes.
		if err := verifyNativeContent(ctx, home, checkout.Path, []string{selector}); err != nil {
			if restoreErr := transaction.restore(); restoreErr != nil {
				return false, restoreErr
			}
			return false, err
		}
		if err := transaction.recordMutation(ctx, selector); err != nil {
			return false, err
		}
	}
	if !transaction.configUnchanged() {
		_ = transaction.restore()
		return false, errors.New("marketplace: native rollback conflict")
	}
	if err := verifyNativeContent(ctx, home, checkout.Path, selectors); err != nil {
		if restoreErr := transaction.restore(); restoreErr != nil {
			return false, restoreErr
		}
		return false, err
	}
	if err := transaction.finish(); err != nil {
		return false, err
	}
	return true, nil
}

type nativeConfig struct {
	Marketplaces map[string]struct {
		SourceType string `toml:"source_type"`
		Source     string `toml:"source"`
	} `toml:"marketplaces"`
	Plugins map[string]struct {
		Enabled bool `toml:"enabled"`
	} `toml:"plugins"`
}

// Reading is a seam for deterministic concurrent-config regressions.
var readNativeConfig = readNativeConfigFile

func readNativeConfigFile(home string) (nativeConfig, error) {
	var config nativeConfig
	path := filepath.Join(home, "config.toml")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > outputLimit {
		return config, errors.New("marketplace: invalid profile config")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config, errors.New("marketplace: invalid profile config")
	}
	if _, err := toml.Decode(string(data), &config); err != nil {
		return config, errors.New("marketplace: invalid profile config")
	}
	return config, nil
}

func registeredClone(config nativeConfig, clone string) bool {
	reg := config.Marketplaces["agent-deck"]
	if reg.SourceType != "local" || !filepath.IsAbs(reg.Source) {
		return false
	}
	source, err := filepath.EvalSymlinks(reg.Source)
	owned, ownedErr := filepath.EvalSymlinks(clone)
	return err == nil && ownedErr == nil && source == owned
}

func supportedRuntime(argv []string) bool { return runtimeIdentity(argv) != "" }

// The SHA-256 binds the version string to the exact Task 01 executable, without
// running an unproven binary to ask it for its own identity. Never cache by path.
var nativeBinaryHash = hashNativeBinary

func hashNativeBinary(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<30 {
		return "", errors.New("marketplace: unsupported runtime")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (1<<30)+1))
	if err != nil || n != info.Size() {
		return "", errors.New("marketplace: unsupported runtime")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func nativeList(ctx context.Context, home string, argv []string) (map[string]bool, error) {
	out, err := executeNative(ctx, home, argv, "plugin", "list", "--available", "--json")
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(out, &value); err != nil {
		return nil, errors.New("marketplace: invalid native plugin inventory")
	}
	result := map[string]bool{}
	collectEnabled(value, result)
	return result, nil
}

func collectEnabled(v any, result map[string]bool) {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			collectEnabled(item, result)
		}
	case map[string]any:
		id, _ := x["pluginId"].(string)
		if id == "" {
			id, _ = x["selector"].(string)
		}
		enabled, ok := x["enabled"].(bool)
		installed, _ := x["installed"].(bool)
		if ok && enabled && installed {
			for _, wanted := range nativeSelectors {
				if id == wanted {
					result[id] = true
				}
			}
		}
		for _, item := range x {
			collectEnabled(item, result)
		}
	}
}

func nativeCommand(ctx context.Context, home string, argv []string, args ...string) error {
	_, err := executeNative(ctx, home, argv, args...)
	return err
}

func nativeOutput(ctx context.Context, home string, argv []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, nativeRefreshTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], args...)...)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	// Preserve the host lock if the worker dies while native descendants run.
	if lock, ok := ctx.Value(gitLockKey{}).(*os.File); ok {
		cmd.ExtraFiles = []*os.File{lock}
	}
	var out, stderr cappedOutput
	cmd.Stdout, cmd.Stderr = struct{ io.Writer }{&out}, struct{ io.Writer }{&stderr}
	err := cmd.Run()
	// A child may outlive a successfully exited parent, including closed pipes.
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return nil, errors.New("marketplace: native timeout")
	}
	if out.overflow || stderr.overflow {
		return nil, errors.New("marketplace: native output exceeded limit")
	}
	if err != nil {
		return nil, errors.New("marketplace: native refresh failed")
	}
	return out.Bytes(), nil
}

func runtimeIdentity(argv []string) string {
	var fingerprint, version string
	switch {
	case len(argv) == 3 && argv[0] == "/Applications/ChatGPT.app/Contents/Resources/codex" && argv[1] == "--disable" && argv[2] == "apps":
		fingerprint = "ecad78dbf98adb89ec475edac86630406cbe59d9f3070b17d88065f136b94bcb"
		version = "codex-cli 0.154.0-alpha.6.2"
	case len(argv) == 1 && argv[0] == "/opt/homebrew/Caskroom/codex/0.154.0/bin/codex.real":
		fingerprint = "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
		version = "codex-cli 0.154.0"
	default:
		return ""
	}
	actual, err := nativeBinaryHash(argv[0])
	if err != nil || actual != fingerprint {
		return ""
	}
	return strings.Join(argv, "\x00") + "\x00" + version + "\x00sha256:" + fingerprint
}

// Only selected cache roots belong to the rollback write set. Config is backed
// up for inspection but never replaced: native add may race an unrelated writer
// that does not participate in Agent Deck's host lock.
type nativeBackup struct {
	home, dir string
	selectors []string
	config    []byte
	expected  map[string]map[string]nativeEntry
}

func selectorName(selector string) string { name, _, _ := strings.Cut(selector, "@"); return name }
func cacheRoot(home, selector string) string {
	return filepath.Join(home, "plugins/cache/agent-deck", selectorName(selector))
}

func backupNative(ctx context.Context, home string, selectors []string, eligible nativeConfig) (*nativeBackup, error) {
	root := filepath.Join(home, ".agent-deck-refresh")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.New("marketplace: native backup failed")
	}
	if err := physicalPath(home, root); err != nil {
		return nil, errors.New("marketplace: unsafe native backup")
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, errors.New("marketplace: native backup failed")
	}
	dir := filepath.Join(root, "pending")
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, errors.New("marketplace: native recovery required")
	}
	tx := &nativeBackup{home: home, dir: dir, selectors: selectors, expected: make(map[string]map[string]nativeEntry)}
	fail := func() (*nativeBackup, error) {
		_ = os.RemoveAll(dir)
		return nil, errors.New("marketplace: native backup failed")
	}
	var err error
	tx.config, err = os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return fail()
	}
	var baseline nativeConfig
	if _, err := toml.Decode(string(tx.config), &baseline); err != nil || !reflect.DeepEqual(eligible, baseline) {
		_ = os.RemoveAll(dir)
		return nil, errors.New("marketplace: native config changed")
	}

	if err := os.Mkdir(filepath.Join(dir, "before"), 0700); err != nil {
		return fail()
	}
	if err := os.WriteFile(filepath.Join(dir, "before/config.toml"), tx.config, 0600); err != nil {
		return fail()
	}
	for _, selector := range selectors {
		path := cacheRoot(home, selector)
		// Refuse symlink ancestors (and absent native installations) before mutation.
		if err := physicalPath(home, path); err != nil {
			if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
				return fail()
			}
			parent := filepath.Dir(path)
			if err := physicalPath(home, parent); err != nil {
				return fail()
			}
		}
		dest := filepath.Join(dir, "before", selectorName(selector))
		if err := copyNativeTree(ctx, path, dest); err != nil {
			return fail()
		}
		before, err := nativeTree(ctx, path)
		if err != nil {
			return fail()
		}
		backup, err := nativeTree(ctx, dest)
		if err != nil || !reflect.DeepEqual(before, backup) {
			return fail()
		}
		tx.expected[selector] = before
	}
	// Durable, exclusive journal precedes the first native write. A killed worker
	// leaves this directory intact; later workers refuse to overwrite its evidence.
	journal, _ := json.Marshal(struct {
		SchemaVersion int      `json:"schema_version"`
		Selectors     []string `json:"selectors"`
	}{1, selectors})
	f, err := os.OpenFile(filepath.Join(dir, "transaction.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail()
	}
	_, err = f.Write(journal)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return fail()
	}
	return tx, nil
}

// Record only the root written by the completed native operation. Never adopt
// later changes to other roots as ours. Persist the observation for inspection;
// interrupted transactions still require manual recovery.
func (tx *nativeBackup) recordMutation(ctx context.Context, selector string) error {
	observed, err := nativeTree(ctx, cacheRoot(tx.home, selector))
	if err != nil {
		return errors.New("marketplace: native recovery required")
	}
	data, err := json.Marshal(observed)
	if err != nil {
		return errors.New("marketplace: native recovery required")
	}
	path := filepath.Join(tx.dir, "after-"+selectorName(selector)+".json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("marketplace: native recovery required")
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("marketplace: native recovery required")
	}
	tx.expected[selector] = observed
	return nil
}

func (tx *nativeBackup) restore() error {
	// Recovery has its own bounded budget even when the native deadline expired.
	ctx, cancel := context.WithTimeout(context.Background(), nativeRefreshTimeout)
	defer cancel()
	current, err := os.ReadFile(filepath.Join(tx.home, "config.toml"))
	var before, after map[string]any
	if err != nil {
		return errors.New("marketplace: native rollback conflict")
	}
	if _, err = toml.Decode(string(tx.config), &before); err != nil {
		return errors.New("marketplace: native rollback conflict")
	}
	if _, err = toml.Decode(string(current), &after); err != nil {
		return errors.New("marketplace: native rollback conflict")
	}
	conflict := !reflect.DeepEqual(before["marketplaces"], after["marketplaces"]) || !reflect.DeepEqual(before["plugins"], after["plugins"])
	for _, selector := range tx.selectors {
		path := cacheRoot(tx.home, selector)
		if err := physicalPath(tx.home, filepath.Dir(path)); err != nil {
			return errors.New("marketplace: native rollback conflict")
		}
		current, err := nativeTree(ctx, path)
		expected, recorded := tx.expected[selector]
		if err != nil || !recorded || !reflect.DeepEqual(current, expected) {
			conflict = true
			continue
		}
		backup := filepath.Join(tx.dir, "before", selectorName(selector))
		original, err := nativeTree(ctx, backup)
		if err != nil {
			return errors.New("marketplace: native rollback failed")
		}
		// Avoid touching a root that never changed, including read-only trees.
		if reflect.DeepEqual(current, original) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return errors.New("marketplace: native rollback failed")
		}
		if err := copyNativeTree(ctx, backup, path); err != nil {
			return errors.New("marketplace: native rollback failed")
		}
		restored, err := nativeTree(ctx, path)
		if err != nil {
			return errors.New("marketplace: native rollback failed")
		}
		if !reflect.DeepEqual(restored, original) {
			return errors.New("marketplace: native rollback failed")
		}
	}
	// Preserve all current config bytes, including unknown fields and concurrent
	// trust changes. A watched namespace conflict retains pending for inspection.
	if conflict {
		return errors.New("marketplace: native rollback conflict")
	}
	return tx.finish()
}

func (tx *nativeBackup) finish() error {
	last := filepath.Join(filepath.Dir(tx.dir), "last")
	if err := os.RemoveAll(last); err != nil {
		return errors.New("marketplace: native backup retention failed")
	}
	if err := os.Rename(tx.dir, last); err != nil {
		return errors.New("marketplace: native backup retention failed")
	}
	return nil
}

type nativeEntry struct {
	Mode os.FileMode
	Hash string
}

// These walks cover only installed selector files, never the whole CODEX_HOME.
// A 1 GiB/100000-entry ceiling bounds disk, memory, and recovery work.
func nativeTree(ctx context.Context, root string) (map[string]nativeEntry, error) {
	return nativeTreeFiltered(ctx, root, false)
}

func nativeTreeFiltered(ctx context.Context, root string, contentOnly bool) (map[string]nativeEntry, error) {
	entries := map[string]nativeEntry{}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return entries, nil
	}
	var total int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if contentOnly && (rel == ".git" || strings.HasPrefix(rel, ".git/")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entry := nativeEntry{Mode: info.Mode()}
		total += info.Size()
		if total > 1<<30 || len(entries) >= 100000 {
			return errors.New("native tree exceeds limit")
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			entry.Hash, err = os.Readlink(path)
		case info.Mode().IsRegular():
			f, openErr := os.Open(path)
			if openErr != nil {
				return openErr
			}
			hash := sha256.New()
			_, err = io.Copy(hash, io.LimitReader(f, 1<<30))
			_ = f.Close()
			entry.Hash = fmt.Sprintf("%x", hash.Sum(nil))
		case info.IsDir():
		default:
			return errors.New("unsupported native file")
		}
		if err != nil {
			return err
		}
		entries[rel] = entry
		return nil
	})
	return entries, err
}

func copyNativeTree(ctx context.Context, source, dest string) error {
	if _, err := os.Lstat(source); os.IsNotExist(err) {
		return nil
	}
	var total int64
	count := 0
	var directories []struct {
		path string
		mode os.FileMode
	}
	err := filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		count++
		if total > 1<<30 || count > 100000 {
			return errors.New("native backup exceeds limit")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
			copied, err := os.Lstat(target)
			if err != nil {
				return err
			}
			if copied.Mode().Perm() != info.Mode().Perm() {
				return unix.Fchmodat(unix.AT_FDCWD, target, uint32(info.Mode().Perm()), unix.AT_SYMLINK_NOFOLLOW)
			}
			return nil
		case info.IsDir():
			// Keep the new directory writable until its children are copied.
			if err := os.Mkdir(target, 0700); err != nil {
				return err
			}
			directories = append(directories, struct {
				path string
				mode os.FileMode
			}{target, info.Mode()})
			return nil
		case info.Mode().IsRegular():
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			defer src.Close()
			dst, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			n, err := io.Copy(dst, io.LimitReader(src, info.Size()+1))
			if err == nil && n != info.Size() {
				err = errors.New("native file changed during backup")
			}
			if err == nil {
				err = dst.Chmod(info.Mode())
			}
			if err == nil {
				err = dst.Sync()
			}
			closeErr := dst.Close()
			if err != nil {
				return err
			}
			return closeErr
		default:
			return errors.New("unsupported native file")
		}
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Chmod(directories[i].path, directories[i].mode); err != nil {
			return err
		}
	}
	return nil
}

func verifyNativeContent(ctx context.Context, home, clone string, selectors []string) error {
	for _, selector := range selectors {
		source := clone
		if selectorName(selector) == "agent-deck-mcp" {
			source = filepath.Join(clone, "plugins/agent-deck-mcp")
		}
		var manifest struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		data, err := os.ReadFile(filepath.Join(source, ".codex-plugin/plugin.json"))
		if err != nil || len(data) > outputLimit || json.Unmarshal(data, &manifest) != nil || manifest.Name != selectorName(selector) || !filepath.IsLocal(manifest.Version) || strings.ContainsAny(manifest.Version, "/\\") || manifest.Version == "." {
			return errors.New("marketplace: native content verification failed")
		}
		expected, err := nativeTreeFiltered(ctx, source, true)
		if err != nil {
			return errors.New("marketplace: native content verification failed")
		}
		actual, err := nativeTreeFiltered(ctx, filepath.Join(cacheRoot(home, selector), manifest.Version), true)
		if err != nil || !reflect.DeepEqual(expected, actual) {
			return errors.New("marketplace: native content verification failed")
		}
	}
	return nil
}

func (tx *nativeBackup) configUnchanged() bool {
	current, err := os.ReadFile(filepath.Join(tx.home, "config.toml"))
	if err != nil {
		return false
	}
	var before, after map[string]any
	if _, err := toml.Decode(string(tx.config), &before); err != nil {
		return false
	}
	if _, err := toml.Decode(string(current), &after); err != nil {
		return false
	}
	return reflect.DeepEqual(before["marketplaces"], after["marketplaces"]) && reflect.DeepEqual(before["plugins"], after["plugins"])
}
