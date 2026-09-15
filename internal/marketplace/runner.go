package marketplace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	err = WithManagedCheckout(ctx, home, func(checkout Checkout) error {
		if err := RefreshProfile(ctx, request.CodexHome, request.CodexArgv, checkout); err != nil {
			return nil
		}
		return saveProfileReceipt(home, request.CodexHome, request.CodexArgv, checkout.Revision)
	})
	// The updater is a background best-effort worker. Ownership, network, lock
	// and native diagnostics are deliberately not session-launch errors.
	return nil
}

func saveProfileReceipt(home, codexHome string, argv []string, revision string) error {
	if !validRevision(revision) || !supportedRuntime(argv) {
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
	key := sha256.Sum256([]byte(p + "\x00" + runtimeIdentity(argv)))
	dir := filepath.Join(StateDir(home), "profiles")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(profileReceipt{1, revision, runtimeIdentity(argv), time.Now().UTC()})
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fmtHex(key[:])+".json")
	return os.WriteFile(path, b, 0600)
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

var _ = errors.New
