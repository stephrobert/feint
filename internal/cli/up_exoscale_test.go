package cli

import (
	"bytes"
	"testing"

	"github.com/stephrobert/feint/internal/environment"
)

// An Exoscale declaration passes preflight, with the engine and without it.
//
// This was the accepting half of a refusal: until #644, `feint up` stopped an
// Exoscale declaration naming Terraform before any process started, and this
// held the two doors that refusal left open. The refusal is gone — upstream
// fixed the split its condition named — and what the test now holds is the
// plain fact that both declarations pass, which is what a client meets.
func TestPreflightPassesAnExoscaleDeclarationWithoutTheEngine(t *testing.T) {
	decl, err := environment.Parse("version: 1\ncloud:\n  provider: exoscale\nruntime:\n  mode: off\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out bytes.Buffer
	if err := preflight(decl, false, &out); err != nil {
		t.Fatalf("preflight refused a declaration that asks for no engine: %v", err)
	}
	// And --no-iac opens the same door with the engine still declared.
	withEngine, err := environment.Parse("version: 1\ncloud:\n  provider: exoscale\n" +
		"iac:\n  engine: terraform\n  directory: .\nruntime:\n  mode: off\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := preflight(withEngine, true, &out); err != nil {
		t.Fatalf("preflight refused the --no-iac door its own refusal names: %v", err)
	}
}
