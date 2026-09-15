package marketplace

import (
	"context"
	"testing"
)

func TestMarketplaceRunUnmanagedHomeIsNoop(t *testing.T) {
	if err := Run(context.Background(), Request{HostHome: t.TempDir(), CodexHome: t.TempDir(), CodexArgv: []string{"not-codex"}}); err != nil {
		t.Fatal(err)
	}
}
