package main

import (
	"os"
	"testing"

	"github.com/stephrobert/feint/internal/hermetic"
)

// TestMain drops every GIT_* variable before any test runs (#955): these tests
// start a stubbed gh and, once, a go build, and none of them must inherit a
// hook's GIT_DIR.
func TestMain(m *testing.M) {
	hermetic.DropInheritedGit()
	os.Exit(m.Run())
}
