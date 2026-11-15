package corpus

import (
	"os"
	"testing"

	"github.com/stephrobert/feint/internal/hermetic"
)

// TestMain drops every GIT_* variable before any test runs, so that a git this
// test binary starts (directly, through a script, or through any function a
// test calls) finds its repository from its working directory and never from a
// hook's GIT_DIR (#955). TestEveryTestBinaryThatStartsAProcessDropsInheritedGit
// (tools/ci) refuses this package without it.
func TestMain(m *testing.M) {
	hermetic.DropInheritedGit()
	os.Exit(m.Run())
}
