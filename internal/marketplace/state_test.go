package marketplace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagedCheckoutLogsBoundedAndSanitized(t *testing.T) {
	dir := t.TempDir()
	writeTest(t, filepath.Join(dir, "update.log"), strings.Repeat("x", logLimit*2))
	logEvent(dir, "success", strings.Repeat("a", 40))
	if info, err := os.Stat(filepath.Join(dir, "update.log.1")); err == nil && info.Size() > logLimit {
		t.Fatalf("oversized rotated log bytes=%d", info.Size())
	}
	for i := 0; i < 2000; i++ {
		logEvent(dir, "success", strings.Repeat("a", 40))
	}
	logEvent(dir, "https://user:secret@example.invalid", "private revision")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 2 {
		t.Fatalf("retained log files=%d", len(entries))
	}
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Size() > logLimit {
			t.Fatalf("%s bytes=%d limit=%d", entry.Name(), info.Size(), logLimit)
		}
		data, _ := os.ReadFile(filepath.Join(dir, entry.Name()))
		if strings.Contains(string(data), "secret") {
			t.Fatal("unsanitized log")
		}
		t.Logf("%s bytes=%d", entry.Name(), info.Size())
	}
}

func TestManagedCheckoutHostHomeAliases(t *testing.T) {
	f := newFixture(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(f.home, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", filepath.Join(f.home, "profile"))
	if StateDir(alias) != StateDir(f.home) || ManagedClonePath(alias) != f.path {
		t.Fatal("host alias changed roots")
	}
	if StateDir("") != "" || ManagedClonePath("relative") != "" {
		t.Fatal("invalid host home accepted")
	}
}

func TestManagedCheckoutRetryBounds(t *testing.T) {
	for i := 1; i < 100; i++ {
		delay := retryDelay(i)
		if delay < 30*time.Second || delay > 5*time.Minute {
			t.Fatalf("retry %d=%v", i, delay)
		}
	}
	var output cappedOutput
	data := []byte(strings.Repeat("secret", outputLimit))
	n, err := output.Write(data)
	if err != nil || n != len(data) || !output.overflow || output.Len() != outputLimit {
		t.Fatalf("unbounded subprocess output: n=%d len=%d", n, output.Len())
	}
}

func TestManagedCheckoutStateTempHardlinkPreserved(t *testing.T) {
	dir := t.TempDir()
	developer := filepath.Join(dir, "developer-state")
	writeTest(t, developer, "preserve developer state")
	if err := os.Link(developer, filepath.Join(dir, "checkout.json.tmp")); err != nil {
		t.Fatal(err)
	}
	if err := saveState(dir, checkoutState{}); err == nil {
		t.Fatal("hardlinked state temporary file accepted")
	}
	data, _ := os.ReadFile(developer)
	if string(data) != "preserve developer state" {
		t.Fatal("developer state changed through hardlink")
	}
}

func TestManagedCheckoutBareGoTestCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a nested bare go test")
	}
	scratch := t.TempDir()
	cmd := exec.Command("go", "test", ".", "-run", "^TestManagedCheckoutOwned$", "-count=1")
	cmd.Env = append(os.Environ(), "TMPDIR="+scratch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare go test: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("bare go test left scratch resources: %v", entries)
	}
	t.Log("bare go test scratch entries: 0")
}

func TestManagedCheckoutInterruptedJournalPreserved(t *testing.T) {
	f := newFixture(t)
	old := gitTest(t, f.path, "rev-parse", "HEAD")
	next := f.advance(t, "next")
	gitTest(t, f.path, "fetch", "--no-tags", "file://"+f.source, "refs/heads/main")
	if _, err := prepareRecovery(context.Background(), StateDir(f.home), f.path, old, next); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, f.path)
	journalPath := filepath.Join(StateDir(f.home), "recovery.json")
	journal, _ := os.ReadFile(journalPath)
	err := WithManagedCheckout(context.Background(), f.home, func(Checkout) error { t.Fatal("callback after interrupted checkout"); return nil })
	if err == nil {
		t.Fatal("interrupted transaction accepted")
	}
	after, _ := os.ReadFile(journalPath)
	if !bytes.Equal(journal, after) || snapshot(t, f.path) != before {
		t.Fatal("interrupted recovery evidence or checkout changed")
	}
	t.Logf("preserved recovery journal: bytes=%d; previous revision=%s", len(journal), old)
}
