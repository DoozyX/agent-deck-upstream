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
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "plugin add agent-deck@agent-deck --json\n" {
		t.Fatalf("native argv = %q", b)
	}
}
