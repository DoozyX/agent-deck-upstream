//go:build eval_smoke

package helpconsent_test

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	defer harness.RemoveBuildArtifacts()
	return m.Run()
}
