package marketplace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRefreshUnsupportedRuntimeIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := RefreshProfile(context.Background(), dir, []string{"not-codex"}, Checkout{Path: "/clone", Revision: "0123456789012345678901234567890123456789"}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRefreshOnlyAddsEnabledSelectors(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	log := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nif [ \"$1\" = plugin ] && [ \"$2\" = list ]; then echo '{\"plugins\":[{\"id\":\"agent-deck@agent-deck\",\"enabled\":true},{\"id\":\"agent-deck-mcp@agent-deck\",\"enabled\":false}]}' ; else echo \"$@\" >> '" + log + "'; fi\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := RefreshProfile(context.Background(), dir, []string{bin}, Checkout{Path: "/clone", Revision: "0123456789012345678901234567890123456789"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("unproven basename ran native command: %v", err)
	}
}

func TestNativeRefreshAcceptsOnlyTaskOneRuntimeIdentities(t *testing.T) {
	if !supportedRuntime([]string{"/Applications/ChatGPT.app/Contents/Resources/codex", "--disable", "apps"}) {
		t.Fatal("approved ChatGPT runtime rejected")
	}
	if supportedRuntime([]string{"/Users/doozyx/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex", "--disable", "apps"}) {
		t.Fatal("unproven standalone runtime accepted")
	}
}
