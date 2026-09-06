package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// loadDeclared reads the operator's declared catalogue and refuses one that
// names a provider or a kind no mounted pack checks (#126).
//
// The second half is the one that matters. A declaration is a contract the
// operator asserts, and a line of it nobody enforces reads exactly like one
// somebody does: `"image"` for `"images"` would check nothing while the file
// looked complete. Well formed is not enforced, and the place to say so is
// before anything listens.
//
// TestServeRefusesADeclarationNamingAKindNoPackChecks fails without the check.
func loadDeclared(path string, packs []emulator.Pack) (*emulator.Declared, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's own file, named on the command line
	if err != nil {
		return nil, fmt.Errorf("--strict-catalog: %w", err)
	}
	defer func() { _ = f.Close() }()
	declared, err := emulator.ParseDeclared(f)
	if err != nil {
		return nil, fmt.Errorf("--strict-catalog %s: %w", path, err)
	}
	if problems := emulator.CheckDeclared(declared, packs); len(problems) > 0 {
		return nil, fmt.Errorf("--strict-catalog %s names what no pack checks, so that line would look "+
			"enforced and be nothing:\n  %s", path, strings.Join(problems, "\n  "))
	}
	return declared, nil
}
