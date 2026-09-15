package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const approvedOrigin = "https://github.com/DoozyX/agent-deck-upstream.git"
const stateLimit = 8192

type ownership struct {
	SchemaVersion int    `json:"schema_version"`
	ClonePath     string `json:"clone_path"`
	Origin        string `json:"origin"`
}

// StateDir is host-wide, independent of CODEX_HOME. An invalid home yields "".
func StateDir(hostHome string) string {
	home, err := canonicalHome(hostHome)
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local/state/agent-deck/marketplace-update")
}

// ManagedClonePath is the sole checkout eligible for managed updates.
func ManagedClonePath(hostHome string) string {
	home, err := canonicalHome(hostHome)
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local/share/codex-marketplaces/agent-deck")
}

func canonicalHome(home string) (string, error) {
	if home == "" || !filepath.IsAbs(home) {
		return "", errors.New("marketplace: absolute host home required")
	}
	p, err := filepath.EvalSymlinks(filepath.Clean(home))
	if err != nil {
		return "", errors.New("marketplace: host home unavailable")
	}
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		return "", errors.New("marketplace: host home is not a directory")
	}
	return p, nil
}

// Reject aliases below the canonical home, including aliases through ancestors.
func physicalPath(home, path string) error {
	relative, err := filepath.Rel(home, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return errors.New("marketplace: path outside host home")
	}
	current := home
	for _, part := range strings.Split(relative, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return errors.New("marketplace: required path unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("marketplace: symlink path refused")
		}
	}
	return nil
}

func readJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > stateLimit {
		return errors.New("marketplace: invalid state file")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("marketplace: unreadable state")
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, stateLimit+1))
	if err = dec.Decode(value); err != nil {
		return errors.New("marketplace: invalid state JSON")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("marketplace: trailing state data")
	}
	return nil
}

const freshness = 15 * time.Minute
const minBackoff = 30 * time.Second
const maxBackoff = 5 * time.Minute
const logLimit = 64 * 1024

// checkoutState is shared by every profile. Cache task state belongs in profiles/.
type checkoutState struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      string    `json:"revision"`
	LastSuccess   time.Time `json:"last_success"`
	NextAttempt   time.Time `json:"next_attempt"`
	Failures      int       `json:"failures"`
}

func loadState(dir string) (checkoutState, error) {
	var state checkoutState
	path := filepath.Join(dir, "checkout.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return state, nil
	}
	if err := readJSON(path, &state); err != nil {
		return state, err
	}
	if state.SchemaVersion != 1 || state.Failures < 0 || state.Failures > 5 || state.Revision != "" && !validRevision(state.Revision) {
		return state, errors.New("marketplace: invalid checkout state")
	}
	return state, nil
}

// A fixed temporary name is safe under the host lock and bounds crash debris.
// O_NOFOLLOW and rename prevent state aliases from becoming write targets.
func saveState(dir string, state checkoutState) error {
	state.SchemaVersion = 1
	data, err := json.Marshal(state)
	if err != nil {
		return errors.New("marketplace: encode state")
	}
	path := filepath.Join(dir, "checkout.json")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("marketplace: invalid state target")
	}
	tmp := path + ".tmp"
	if info, err := os.Lstat(tmp); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
			return errors.New("marketplace: unsafe state temporary file")
		}
		if err := os.Remove(tmp); err != nil {
			return errors.New("marketplace: stale state temporary file unavailable")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("marketplace: state temporary file unavailable")
	}
	fd, err := syscall.Open(tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return errors.New("marketplace: state write unavailable")
	}
	f := os.NewFile(uintptr(fd), tmp)
	defer func() { _ = f.Close(); _ = os.Remove(tmp) }()
	if _, err = f.Write(data); err != nil {
		return errors.New("marketplace: state write failed")
	}
	if err = f.Sync(); err != nil {
		return errors.New("marketplace: state sync failed")
	}
	if err = f.Close(); err != nil {
		return errors.New("marketplace: state close failed")
	}
	if err = os.Rename(tmp, path); err != nil {
		return errors.New("marketplace: state publish failed")
	}
	return nil
}

func retryDelay(failures int) time.Duration {
	delay := minBackoff
	for i := 1; i < failures && delay < maxBackoff; i++ {
		delay *= 2
	}
	if delay > maxBackoff {
		return maxBackoff
	}
	return delay
}

// The log contains only allowlisted outcomes and hexadecimal revisions, never
// paths, environment, callback errors or raw Git output. Two files maximum.
func logEvent(dir, outcome, revision string) {
	switch outcome {
	case "success", "coalesced", "git_failure", "callback_failure", "backoff":
	default:
		return
	}
	if !validRevision(revision) {
		revision = ""
	}
	line := fmt.Sprintf("%s %s %s\n", time.Now().UTC().Format(time.RFC3339), outcome, revision)
	path := filepath.Join(dir, "update.log")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return
		}
		if info.Size() > logLimit {
			if err := os.Remove(path); err != nil {
				return
			}
		} else if info.Size()+int64(len(line)) > logLimit {
			if err := os.Rename(path, path+".1"); err != nil {
				return
			}
		}
	}
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_APPEND|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	_, _ = f.WriteString(line)
}

const recoveryLimit = 64 * 1024 * 1024
const recoveryFileLimit = 10000

type recoveryEntry struct {
	Path    string      `json:"path"`
	Mode    os.FileMode `json:"mode"`
	Data    []byte      `json:"data,omitempty"`
	Missing bool        `json:"missing,omitempty"`
}

type recoveryJournal struct {
	SchemaVersion int             `json:"schema_version"`
	ClonePath     string          `json:"clone_path"`
	Previous      string          `json:"previous"`
	Target        string          `json:"target"`
	Entries       []recoveryEntry `json:"entries"`
}

// Save only files the fast-forward can replace, plus Git's mutable index/ref
// metadata. Refuse over-budget transactions before changing the working tree.
func prepareRecovery(ctx context.Context, dir, path, previous, target string) (recoveryJournal, error) {
	journal := recoveryJournal{SchemaVersion: 1, ClonePath: path, Previous: previous, Target: target}
	names, err := runGit(ctx, path, "diff", "--no-renames", "--name-only", "-z", previous, target)
	if err != nil {
		return journal, err
	}
	paths := strings.Split(strings.TrimSuffix(names, "\x00"), "\x00")
	paths = append(paths, ".git/index", ".git/HEAD", ".git/refs/heads/main", ".git/ORIG_HEAD", ".git/logs/HEAD", ".git/logs/refs/heads/main")
	total := 0
	for _, name := range paths {
		if name == "" {
			continue
		}
		if !filepath.IsLocal(name) || filepath.Clean(name) != name {
			return journal, errors.New("marketplace: invalid checkout path")
		}
		entry := recoveryEntry{Path: name}
		info, err := os.Lstat(filepath.Join(path, name))
		if os.IsNotExist(err) {
			entry.Missing = true
		} else if err != nil {
			return journal, errors.New("marketplace: cannot snapshot checkout")
		} else {
			entry.Mode = info.Mode()
			switch {
			case info.Mode().IsRegular():
				if info.Size() > recoveryLimit/2-int64(total) {
					return journal, errors.New("marketplace: checkout recovery exceeds byte limit")
				}
				entry.Data, err = os.ReadFile(filepath.Join(path, name))
			case info.Mode()&os.ModeSymlink != 0:
				var link string
				link, err = os.Readlink(filepath.Join(path, name))
				entry.Data = []byte(link)
			default:
				return journal, errors.New("marketplace: directory/file transition requires manual update")
			}
			if err != nil {
				return journal, errors.New("marketplace: snapshot read failed")
			}
			total += len(entry.Data)
		}
		journal.Entries = append(journal.Entries, entry)
		if len(journal.Entries) > recoveryFileLimit {
			return journal, errors.New("marketplace: checkout recovery exceeds file limit")
		}
	}
	data, err := json.Marshal(journal)
	if err != nil || len(data) > recoveryLimit {
		return journal, errors.New("marketplace: recovery journal exceeds limit")
	}
	name := filepath.Join(dir, "recovery.json")
	// Exclusive creation also preserves interrupted transactions for inspection.
	fd, err := syscall.Open(name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return journal, errors.New("marketplace: recovery journal unavailable")
	}
	file := os.NewFile(uintptr(fd), name)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(name)
		return journal, errors.New("marketplace: recovery journal write failed")
	}
	return journal, nil
}

// This runs only in the same lock-held attempt that prepared the journal.
// After a process crash we refuse automatic restoration over possible user edits.
func restoreRecovery(path string, journal recoveryJournal) error {
	for _, entry := range journal.Entries {
		name := filepath.Join(path, entry.Path)
		// No recursive deletion: concurrent directories are never swept away.
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
			return errors.New("marketplace: recovery removal failed")
		}
	}
	for _, entry := range journal.Entries {
		if entry.Missing {
			continue
		}
		name := filepath.Join(path, entry.Path)
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return errors.New("marketplace: recovery directory failed")
		}
		if entry.Mode&os.ModeSymlink != 0 {
			if err := os.Symlink(string(entry.Data), name); err != nil {
				return errors.New("marketplace: recovery symlink failed")
			}
		} else {
			if err := os.WriteFile(name, entry.Data, entry.Mode.Perm()); err != nil {
				return errors.New("marketplace: recovery file failed")
			}
		}
	}
	return nil
}
