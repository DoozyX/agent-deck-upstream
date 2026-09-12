package costs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceFingerprintDetectsMiddleOfScannedPrefixRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.jsonl")
	body := make([]byte, 20_000)
	for i := range body {
		body[i] = 'a'
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, err := sourceFingerprint(path, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	body[10_000] = 'b'
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	after, err := sourceFingerprint(path, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatalf("fingerprint unchanged after same-size/same-mtime middle rewrite: %s", before)
	}
}
