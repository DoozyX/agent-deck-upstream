// Package marketplace guards the host-owned marketplace checkout.
package marketplace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Checkout describes the validated source while the host lock is held.
type Checkout struct {
	Path     string
	Revision string
	Updated  bool
}

// ErrBackoff means another background attempt should wait for the recorded retry time.
var ErrBackoff = errors.New("marketplace: retry backoff")

// WithManagedCheckout is synchronous and must be invoked by a background worker,
// never the session foreground path. The callback may update the caller's cache.
// CODEX_HOME (default hostHome/.codex) identifies its profile for callback backoff;
// it never selects the managed clone or host lock. Callers must keep it stable.
func WithManagedCheckout(ctx context.Context, hostHome string, use func(Checkout) error) error {
	return WithManagedCheckoutContext(ctx, hostHome, func(_ context.Context, checkout Checkout) error { return use(checkout) })
}

// WithManagedCheckoutContext passes the derived host-lock context to the
// callback. Native subprocesses must use it to inherit the lock descriptor,
// retaining serialization if the worker dies while a child is still active.
// The context and its descriptor are valid only until the callback returns.
// CODEX_HOME has the same stable callback-backoff identity contract as the wrapper.
func WithManagedCheckoutContext(ctx context.Context, hostHome string, use func(context.Context, Checkout) error) error {
	home, err := canonicalHome(hostHome)
	if err != nil {
		return err
	}
	path := ManagedClonePath(home)
	if err = validateOwnership(home, path); err != nil {
		return err
	}
	lock, err := acquireLock(ctx, filepath.Join(StateDir(home), "update.lock"))
	if err != nil {
		return err
	}
	defer lock.release()
	ctx = context.WithValue(ctx, gitLockKey{}, lock.file)
	if err = validateOwnership(home, path); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(StateDir(home), "recovery.json")); !os.IsNotExist(err) {
		return errors.New("marketplace: interrupted checkout requires recovery inspection")
	}
	revision, err := validateCheckout(ctx, path)
	if err != nil {
		return err
	}
	dir := StateDir(home)
	state, err := loadState(dir)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	profile, err := callbackProfileKey(home)
	if err != nil {
		return err
	}
	retry := state.CallbackRetries[profile]
	if retry.NextAttempt.After(now) && retry.NextAttempt.Sub(now) <= maxBackoff {
		logEvent(dir, "backoff", revision)
		return ErrBackoff
	}
	if state.NextAttempt.After(now) && state.NextAttempt.Sub(now) <= maxBackoff {
		logEvent(dir, "backoff", revision)
		return ErrBackoff
	}
	recent := state.Revision == revision && !state.LastSuccess.After(now) && now.Sub(state.LastSuccess) < freshness
	next := revision
	if !recent {
		// Persist a bounded retry reservation before the first network operation.
		// Process death cannot cause a retry storm or a false successful fetch.
		state.Failures = min(state.Failures+1, 5)
		state.NextAttempt = now.Add(retryDelay(state.Failures))
		if err = saveState(dir, state); err != nil {
			return err
		}
		next, err = updateCheckout(ctx, dir, path, revision)
		if err != nil {
			state.NextAttempt = time.Now().UTC().Add(retryDelay(state.Failures))
			if saveErr := saveState(dir, state); saveErr != nil {
				return saveErr
			}
			logEvent(dir, "git_failure", revision)
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Reserve this profile's retry before calling it, including coalesced calls
	// and process death. A cache failure must not back off other profiles.
	now = time.Now().UTC()
	if state.CallbackRetries == nil {
		state.CallbackRetries = make(map[string]callbackRetry)
	}
	for key, pending := range state.CallbackRetries {
		if pending.NextAttempt.Add(maxBackoff).Before(now) {
			delete(state.CallbackRetries, key)
		}
	}
	if _, exists := state.CallbackRetries[profile]; !exists && len(state.CallbackRetries) >= callbackRetryLimit {
		// Reuse the earliest due slot, with a stable tie-break independent of
		// map iteration. Never forget an active backoff: wait for its deadline
		// under the host lock before eviction. This background-only wait is
		// cancellable and bounded by maxBackoff, just like retry eligibility.
		victim := ""
		var deadline time.Time
		for key, pending := range state.CallbackRetries {
			if victim == "" || pending.NextAttempt.Before(deadline) || pending.NextAttempt.Equal(deadline) && key < victim {
				victim, deadline = key, pending.NextAttempt
			}
		}
		if delay := time.Until(deadline); delay > 0 && delay <= maxBackoff {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		delete(state.CallbackRetries, victim)
		now = time.Now().UTC()
	}
	retry.Failures = min(state.CallbackRetries[profile].Failures+1, 5)
	retry.NextAttempt = now.Add(retryDelay(retry.Failures))
	state.CallbackRetries[profile] = retry
	state.NextAttempt = time.Time{}
	state.Failures = 0
	if err = saveState(dir, state); err != nil {
		return err
	}
	if err = use(ctx, Checkout{Path: path, Revision: next, Updated: next != revision}); err != nil {
		retry.NextAttempt = time.Now().UTC().Add(retryDelay(retry.Failures))
		state.CallbackRetries[profile] = retry
		// A callback error must retain its original identity/classification.
		_ = saveState(dir, state)
		logEvent(dir, "callback_failure", next)
		return err
	}
	delete(state.CallbackRetries, profile)
	if !recent {
		state.Revision = next
		state.LastSuccess = time.Now().UTC()
	}
	if err = saveState(dir, state); err != nil {
		return err
	}
	if !recent {
		logEvent(dir, "success", next)
	} else {
		logEvent(dir, "coalesced", next)
	}
	return nil
}

func validateOwnership(home, path string) error {
	if err := physicalPath(home, path); err != nil {
		return err
	}
	dir := StateDir(home)
	if err := physicalPath(home, dir); err != nil {
		return err
	}
	var marker ownership
	if err := readJSON(filepath.Join(dir, "ownership.json"), &marker); err != nil {
		return err
	}
	if marker.SchemaVersion != 1 || marker.ClonePath != path || marker.Origin != approvedOrigin {
		return errors.New("marketplace: checkout is not owned")
	}
	return nil
}

func validateCheckout(ctx context.Context, path string) (string, error) {
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || !info.IsDir() {
		return "", errors.New("marketplace: standalone checkout required")
	}
	if err := filepath.WalkDir(filepath.Join(path, ".git"), func(_ string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("marketplace: unreadable git metadata")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return errors.New("marketplace: unreadable git metadata")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("marketplace: aliased git metadata")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && info.Mode().IsRegular() && stat.Nlink > 1 {
			return errors.New("marketplace: hardlinked git metadata")
		}
		return nil
	}); err != nil {
		return "", err
	}

	for _, name := range []string{"worktrees", "commondir", "objects/info/alternates", "index.lock", "shallow.lock", "HEAD.lock", "refs/heads/main.lock", "MERGE_HEAD", "rebase-merge", "rebase-apply"} {
		if _, err := os.Lstat(filepath.Join(path, ".git", name)); !os.IsNotExist(err) {
			return "", errors.New("marketplace: linked or interrupted checkout refused")
		}
	}
	// A managed clone has only ordinary clone configuration. Reject includes,
	// executable filters, URL rewriting and relocated worktrees before Git runs.
	config, err := runGit(ctx, path, "config", "--local", "--list", "--null")
	if err != nil {
		return "", err
	}
	allowed := map[string]bool{"core.repositoryformatversion": true, "core.filemode": true, "core.bare": true, "core.logallrefupdates": true, "core.ignorecase": true, "core.precomposeunicode": true, "remote.origin.url": true, "remote.origin.fetch": true, "remote.origin.tagopt": true, "branch.main.remote": true, "branch.main.merge": true}
	for _, entry := range strings.Split(config, "\x00") {
		if entry == "" {
			continue
		}
		key, _, _ := strings.Cut(entry, "\n")
		if !allowed[key] {
			return "", errors.New("marketplace: unmanaged git configuration")
		}
	}
	checks := [][]string{{"rev-parse", "--show-toplevel"}, {"symbolic-ref", "HEAD"}, {"remote", "get-url", "--all", "origin"}, {"status", "--porcelain=v1", "--untracked-files=all", "--ignored"}}
	expected := []string{path, "refs/heads/main", approvedOrigin, ""}
	for i, args := range checks {
		value, err := runGit(ctx, path, args...)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) != expected[i] {
			return "", errors.New("marketplace: unsafe checkout")
		}
	}
	revision, err := runGit(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	revision = strings.TrimSpace(revision)
	if !validRevision(revision) {
		return "", errors.New("marketplace: invalid revision")
	}
	return revision, nil
}

func validRevision(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

const outputLimit = 64 * 1024
const gitSafePATH = "/usr/bin:/bin"

// Both supported hosts provide Git here. Never resolve it from caller PATH.
// Kept private so package tests can inject a disposable transport.
var trustedGitPath = "/usr/bin/git"

// cappedOutput drains the pipe but never retains unbounded Git output.
type cappedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := outputLimit - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func runGit(ctx context.Context, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "credential.helper=", "-c", "gc.auto=0"}
	// #nosec G204 -- fixed executable, internal argv, validated owned path/revisions; no shell.
	cmd := exec.CommandContext(ctx, trustedGitPath, append(prefix, args...)...)
	cmd.Dir = path
	// An orphaned Git child must keep the same lock if its updater is killed.
	// Normal cancellation kills and waits for the process group before unlocking.
	if lock, ok := ctx.Value(gitLockKey{}).(*os.File); ok {
		cmd.ExtraFiles = []*os.File{lock}
	}
	// Explicit environment prevents inherited GIT_DIR, config injection, prompts,
	// SSH fallback and profile credentials from changing this read-only operation.
	cmd.Env = []string{"PATH=" + gitSafePATH, "HOME=" + os.Getenv("HOME"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/usr/bin/false", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	var out, stderr cappedOutput
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("marketplace: git operation failed")
	}
	if out.overflow {
		return "", errors.New("marketplace: git output exceeded limit")
	}
	return out.String(), nil
}

const fetchTimeout = 60 * time.Second
const initialDepth = 64

var deepenSteps = [...]int{64, 256, 1024, 4096}

func updateCheckout(ctx context.Context, dir, path, previous string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	fetch := func(bound string) error {
		_, err := runGit(ctx, path, "fetch", "--no-tags", "--no-recurse-submodules", "--no-auto-maintenance", bound, approvedOrigin, "refs/heads/main")
		return err
	}
	if err := fetch(fmt.Sprintf("--depth=%d", initialDepth)); err != nil {
		return "", err
	}
	target, err := runGit(ctx, path, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", err
	}
	target = strings.TrimSpace(target)
	if !validRevision(target) {
		return "", errors.New("marketplace: invalid fetched revision")
	}
	if target == previous {
		current, err := validateCheckout(ctx, path)
		if err != nil {
			return "", err
		}
		if current != previous {
			return "", errors.New("marketplace: checkout changed during fetch")
		}
		return target, nil
	}
	ancestor := func() bool {
		_, err := runGit(ctx, path, "merge-base", "--is-ancestor", previous, target)
		return err == nil
	}
	proven := ancestor()
	for _, step := range deepenSteps {
		if proven {
			break
		}
		if err := fetch(fmt.Sprintf("--deepen=%d", step)); err != nil {
			return "", err
		}
		// A moving remote must never swap the revision whose ancestry we proved.
		latest, err := runGit(ctx, path, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
		if err != nil || strings.TrimSpace(latest) != target {
			return "", errors.New("marketplace: remote moved during ancestry proof")
		}
		proven = ancestor()
	}
	if !proven {
		return "", errors.New("marketplace: fast-forward ancestry not established")
	}
	current, err := validateCheckout(ctx, path)
	if err != nil {
		return "", err
	}
	if current != previous {
		return "", errors.New("marketplace: checkout changed during fetch")
	}
	journal, err := prepareRecovery(ctx, dir, path, previous, target)
	if err != nil {
		return "", err
	}
	if _, err = runGit(ctx, path, "merge", "--ff-only", "--no-edit", target); err == nil {
		current, err = validateCheckout(ctx, path)
		if err == nil && current != target {
			err = errors.New("marketplace: checkout verification failed")
		}
	}
	if err != nil {
		if restoreErr := restoreRecovery(path, journal); restoreErr != nil {
			return "", restoreErr
		}
		if removeErr := os.Remove(filepath.Join(dir, "recovery.json")); removeErr != nil {
			return "", errors.New("marketplace: recovery cleanup failed")
		}
		return "", err
	}
	if err = os.Remove(filepath.Join(dir, "recovery.json")); err != nil {
		// Cleanup is part of this transaction: an error must leave the previous
		// usable revision, just like a failed merge. Retain the journal for
		// inspection while its directory remains unwritable.
		if restoreErr := restoreRecovery(path, journal); restoreErr != nil {
			return "", restoreErr
		}
		return "", errors.New("marketplace: recovery cleanup failed")
	}

	return target, nil
}
