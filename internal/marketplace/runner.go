package marketplace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Request struct {
	HostHome  string
	CodexHome string
	CodexArgv []string
}

type profileReceipt struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      string    `json:"revision"`
	Runtime       string    `json:"runtime"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Run is fail-open for a missing or unmanaged checkout: session launchers must
// not turn an updater diagnostic into a foreground failure.
func Run(ctx context.Context, request Request) error {
	home, err := canonicalHome(request.HostHome)
	if err != nil {
		return nil
	}
	if request.CodexHome == "" {
		request.CodexHome = filepath.Join(home, ".codex")
	}
	_ = WithManagedCheckoutContext(ctx, home, func(ctx context.Context, checkout Checkout) error {
		if !supportedRuntime(request.CodexArgv) {
			logUnsupportedRuntime(StateDir(home))
			return errors.New("marketplace: unsupported runtime")
		}
		if profileCurrent(ctx, home, request, checkout) {
			return nil
		}
		refreshed, err := refreshProfile(ctx, request.CodexHome, request.CodexArgv, checkout)
		if err != nil {
			return err
		}
		if !refreshed {
			return nil
		}
		return saveProfileReceipt(home, request.CodexHome, request.CodexArgv, checkout.Revision)
	})
	// The updater is a background best-effort worker. Ownership, network, lock
	// and native diagnostics are deliberately not session-launch errors.
	return nil
}

func saveProfileReceipt(home, codexHome string, argv []string, revision string) error {
	identity := runtimeIdentity(argv)
	if !validRevision(revision) || identity == "" {
		return nil
	}
	p, err := filepath.EvalSymlinks(codexHome)
	if err != nil {
		if os.IsNotExist(err) {
			p = filepath.Clean(codexHome)
		} else {
			return err
		}
	}
	key := sha256.Sum256([]byte(p + "\x00" + identity))
	dir := filepath.Join(StateDir(home), "profiles")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("marketplace: receipt unavailable")
	}
	if err := physicalPath(home, dir); err != nil {
		return errors.New("marketplace: receipt unavailable")
	}
	b, err := json.Marshal(profileReceipt{1, revision, identity, time.Now().UTC()})
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fmtHex(key[:])+".json")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("marketplace: unsafe receipt")
	}
	f, err := os.CreateTemp(dir, ".receipt-")
	if err != nil {
		return errors.New("marketplace: receipt unavailable")
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("marketplace: receipt write failed")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return errors.New("marketplace: receipt publish failed")
	}
	return nil
}

func fmtHex(v []byte) string {
	const hex = "0123456789abcdef"
	b := make([]byte, len(v)*2)
	for i, x := range v {
		b[i*2] = hex[x>>4]
		b[i*2+1] = hex[x&15]
	}
	return string(b)
}

// Receipts coalesce only this canonical profile/runtime, and only while its
// selected installed content still matches the current checkout.
func profileCurrent(ctx context.Context, home string, request Request, checkout Checkout) bool {
	identity := runtimeIdentity(request.CodexArgv)
	if identity == "" {
		return false
	}
	profile, err := canonicalHome(request.CodexHome)
	if err != nil {
		return false
	}
	if _, err := os.Lstat(filepath.Join(profile, ".agent-deck-refresh/pending")); !os.IsNotExist(err) {
		return false
	}
	key := sha256.Sum256([]byte(profile + "\x00" + identity))
	var receipt profileReceipt
	if err := readJSON(filepath.Join(StateDir(home), "profiles", fmtHex(key[:])+".json"), &receipt); err != nil {
		return false
	}
	if receipt.SchemaVersion != 1 || receipt.Revision != checkout.Revision || receipt.Runtime != identity {
		return false
	}
	config, err := readNativeConfig(profile)
	if err != nil || !registeredClone(config, checkout.Path) {
		return false
	}
	installed, err := nativeList(ctx, profile, request.CodexArgv)
	if err != nil {
		return false
	}
	var selectors []string
	for _, selector := range nativeSelectors {
		if installed[selector] && config.Plugins[selector].Enabled {
			selectors = append(selectors, selector)
		}
	}
	return len(selectors) > 0 && verifyNativeContent(ctx, profile, checkout.Path, selectors) == nil
}

// Use the checkout logger's two-file/64 KiB contract under its host lock. The
// fixed classification has no caller-controlled text. Returning a callback error
// separately reserves the existing bounded retry and emits callback_failure.
func logUnsupportedRuntime(dir string) {
	line := time.Now().UTC().Format(time.RFC3339) + " unsupported_runtime\n"
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
