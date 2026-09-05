package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/environment"
)

// The repository satisfies its own capability rule.
//
// The accepting half, and it belongs first: a matrix that refused everything
// would pass every mutation below and break the product.
func TestTheCapabilityMatrixHoldsAgainstItsOwnInstruments(t *testing.T) {
	root := repoRoot(t)
	workflow := filepath.Join(root, conformanceWorkflow)
	if problems := capabilityProblems(workflow); len(problems) != 0 {
		t.Fatalf("the repository does not satisfy its own rule:\n  %s", strings.Join(problems, "\n  "))
	}
	if problems := capabilityClaimProblems(workflow, capabilityClaimPages(root)); len(problems) != 0 {
		t.Fatalf("a generated block claims something no matrix row carries:\n  %s",
			strings.Join(problems, "\n  "))
	}
}

// The population is not empty, and both directions of both proofs really run.
//
// capabilityProblems answers nothing when the workflow is absent, because
// `feint docs` also regenerates a README outside this repository. That
// tolerance is exactly the shape of a check that stops measuring when its
// subject moves, so the subject is asserted here: there are rows, both verdicts
// occur, both proofs occur, and the pages the claim reader walks really carry
// generated blocks.
func TestTheCapabilityChecksHaveASubjectToMeasure(t *testing.T) {
	root := repoRoot(t)

	var supported, refused, byCI int
	for _, row := range capabilityMatrix {
		switch row.Support {
		case capabilitySupported:
			supported++
		case capabilityRefused:
			refused++
		}
		switch row.Proof {
		case provenInCI:
			byCI++
		}
	}
	// One verdict is where this matrix arrived: #644 lifted the last refusal
	// when upstream shipped the fix its condition named, so every row is
	// supported and every proof is the CI one. That is a fact about the world,
	// not a matrix that stopped measuring — but the refused branch of every
	// check below then rests on nothing real, which is exactly what this guard
	// exists to refuse.
	//
	// So the refused half moved onto fixtures, and this asserts they are there:
	// TestARefusedRowCarriesItsReasonAndItsMarker mutates a planted refused row,
	// and the claim reader's two tests plant the Exoscale row as it stood.
	// internal/core/emulator/TestEveryCitedTestExists is what keeps those names honest.
	if supported == 0 {
		t.Fatal("no supported row: the matrix claims nothing at all")
	}
	if byCI == 0 {
		t.Fatal("no row proved by CI: the one resolver this matrix still uses is exercised by nothing")
	}
	if refused != 0 {
		t.Fatalf("%d refused row(s) and no instrument left that can establish one: the doorstep "+
			"proof went with the veto in #644, so a refusal here rests on nothing", refused)
	}

	// And the claim reader has something to read. A page with no generated
	// block passes every claim rule while reading nothing.
	for _, page := range capabilityClaimPages(root) {
		body, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		blocks := generatedBlockBodies(string(body))
		if len(blocks) == 0 {
			t.Errorf("%s carries no generated block: the claim reader walks it and finds nothing "+
				"to judge", page)
		}
		units := 0
		for _, block := range blocks {
			units += len(capabilityUnits(block.body))
		}
		if units == 0 {
			t.Errorf("%s splits into no unit at all: the reader would report nothing whatever the "+
				"page said", page)
		}
	}
}

// A supported row rests on a workflow that really drives that pair, and a pair
// CI drives has a row.
//
// The second direction is the understated half, and this repository has paid
// for it once already: an external review recommended deleting Terraform from
// the README's Outscale row on the strength of a table that had understated it,
// which would have erased a suite applying twenty-one resources.
func TestASupportedRowNamesAWorkflowThatDrivesIt(t *testing.T) {
	root := repoRoot(t)
	workflow := filepath.Join(root, conformanceWorkflow)
	if problems := provenInCIProblems(workflow); len(problems) != 0 {
		t.Fatalf("the repository does not satisfy its own rule:\n  %s", strings.Join(problems, "\n  "))
	}

	restore := capabilityMatrix
	t.Cleanup(func() { capabilityMatrix = restore })

	// A pair nothing drives, claiming the workflow proves it.
	capabilityMatrix = append(append([]capabilityRow{}, restore...), capabilityRow{
		Provider: "exoscale", Client: "scw", Mode: capabilityControlPlane,
		Support: capabilitySupported, Proof: provenInCI,
	})
	problems := provenInCIProblems(workflow)
	if len(problems) == 0 {
		t.Error("a row claiming the conformance workflow drives `scw` against Exoscale passed: " +
			"the proof column would be decoration")
	}

	// And a pair the workflow drives with no row at all.
	var missing []capabilityRow
	dropped := ""
	for _, row := range restore {
		if row.Proof == provenInCI && dropped == "" {
			dropped = row.Provider + "/" + row.Client
			continue
		}
		missing = append(missing, row)
	}
	if dropped == "" {
		t.Fatal("no row rests on the workflow: this test is measuring a table it does not understand")
	}
	capabilityMatrix = missing
	problems = provenInCIProblems(workflow)
	if len(problems) == 0 {
		t.Errorf("%s is driven on every pull request and no row carries it, and it passed: no "+
			"generated sentence could ever mention a client this project really proves", dropped)
	}
}

// The mode column says what the proofs cover, and it is read from the workflow
// rather than asserted.
//
// Every row claims the control plane, because the conformance matrix starts its
// emulator with no machine runtime. The mutation is the one that would make the
// column a lie: a leg that arms one.
func TestTheModeColumnIsReadFromTheRunItDescribes(t *testing.T) {
	root := repoRoot(t)
	workflow := filepath.Join(root, conformanceWorkflow)
	if problems := modeProblems(workflow); len(problems) != 0 {
		t.Fatalf("the conformance workflow already arms a runtime:\n  %s", strings.Join(problems, "\n  "))
	}

	body, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatal(err)
	}
	armed := strings.Replace(string(body),
		"/tmp/feint start --addr 127.0.0.1:4599",
		"/tmp/feint start --vm incus-ovn --addr 127.0.0.1:4599", 1)
	if armed == string(body) {
		t.Fatal("the workflow no longer starts the emulator the way this test expects: it is " +
			"measuring a file it does not understand")
	}
	copied := filepath.Join(t.TempDir(), "conformance.yml")
	if err := os.WriteFile(copied, []byte(armed), 0o600); err != nil {
		t.Fatal(err)
	}
	problems := modeProblems(copied)
	if len(problems) == 0 {
		t.Error("a workflow arming `--vm incus-ovn` left every row claiming the control plane " +
			"unchallenged: the column would describe a run that no longer exists")
	}
}

// A refused row is a decision, and a decision has a reason and a way to check
// whether it still holds.
func TestARefusedRowCarriesItsReasonAndItsMarker(t *testing.T) {
	if problems := matrixShapeProblems(); len(problems) != 0 {
		t.Fatalf("the matrix does not satisfy its own shape rule:\n  %s", strings.Join(problems, "\n  "))
	}

	restore := capabilityMatrix
	t.Cleanup(func() { capabilityMatrix = restore })

	for _, mutation := range []struct {
		name  string
		apply func(row *capabilityRow)
		want  string
	}{
		{"no reason", func(row *capabilityRow) { row.Reason = "   " }, "no reason"},
		{"no marker", func(row *capabilityRow) { row.Marker = "" }, "no marker"},
		{"a reason that does not name the marker",
			func(row *capabilityRow) { row.Reason = "it splits between two clouds" }, "does not name"},
		{"an unknown mode", func(row *capabilityRow) { row.Mode = "with a machine runtime" }, "mode"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			// The row as it stood while the refusal was in force, planted:
			// nothing in the matrix is refused since #644, and a guard about
			// refused rows cannot wait for the world to be wrong again.
			mutated := append([]capabilityRow{}, restore...)
			mutated = append(mutated, capabilityRow{
				Provider: "exoscale", Client: "terraform", Mode: capabilityControlPlane,
				Support: capabilityRefused, Proof: provenInCI,
				Marker: "exoscale/terraform-provider-exoscale#573",
				Reason: "the published provider builds two clients and only one honours " +
					"`EXOSCALE_API_ENDPOINT`, so an apply splits between this emulator and a " +
					"paying account; exoscale/terraform-provider-exoscale#573",
			})
			mutation.apply(&mutated[len(mutated)-1])
			capabilityMatrix = mutated
			problems := matrixShapeProblems()
			if len(problems) == 0 {
				t.Fatalf("a refused row with %s passed", mutation.name)
			}
			if !strings.Contains(strings.Join(problems, "\n"), mutation.want) {
				t.Errorf("the refusal does not say what is wrong:\n  %s", strings.Join(problems, "\n  "))
			}
		})
	}
}

// Every engine clientSources names is an engine `feint.yaml` accepts.
//
// A row resting on a veto for an engine internal/environment refuses could
// never be asked for, so the proof would be unfalsifiable rather than true.
func TestEveryEngineTheMatrixKnowsIsOneUpCanBeAskedToRun(t *testing.T) {
	seen := 0
	for _, c := range clientSources {
		if c.engine == "" {
			continue
		}
		seen++
		found := false
		for _, engine := range environment.Engines {
			if engine == c.engine {
				found = true
			}
		}
		if !found {
			t.Errorf("clientSources maps %s to the engine %q and internal/environment does not "+
				"accept it", c.name, c.engine)
		}
	}
	if seen == 0 {
		t.Fatal("clientSources names no engine at all: every engine assertion here measures nothing")
	}
}
