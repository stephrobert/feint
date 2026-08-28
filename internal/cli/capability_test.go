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

	var supported, refused, byCI, byVeto int
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
		case refusedAtTheDoorstep:
			byVeto++
		}
	}
	if supported == 0 || refused == 0 {
		t.Fatalf("%d supported and %d refused rows: a matrix with one verdict measures neither",
			supported, refused)
	}
	if byCI == 0 || byVeto == 0 {
		t.Fatalf("%d rows proved by CI and %d by the doorstep: a proof nothing rests on is a "+
			"resolver nobody exercises", byCI, byVeto)
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

// The matrix and the doorstep cannot disagree, in either direction.
//
// This is the property #592 asks for in one sentence: *a refusal that lives in
// up.go and a table that disagree is the same defect one level down.* Both
// mutations are the ones somebody will really make — a row flipped to
// supported, and a veto the table never learned about.
func TestTheMatrixAndTheDoorstepCannotDisagree(t *testing.T) {
	facts, err := readPackFacts()
	if err != nil {
		t.Fatalf("read the pack facts: %v", err)
	}
	if len(facts.Vetoes) == 0 {
		t.Fatal("no pack vetoes any engine: the doorstep of #525 is disarmed, and every assertion " +
			"below would pass by having nothing to compare")
	}
	if problems := vetoProblems(facts.Vetoes); len(problems) != 0 {
		t.Fatalf("the matrix and the packs already disagree:\n  %s", strings.Join(problems, "\n  "))
	}

	// A row that promises what the doorstep refuses — #592 written into the
	// table rather than into the README.
	restore := capabilityMatrix
	t.Cleanup(func() { capabilityMatrix = restore })

	flipped := append([]capabilityRow{}, restore...)
	found := false
	for i := range flipped {
		if flipped[i].Support != capabilityRefused {
			continue
		}
		flipped[i].Support = capabilitySupported
		flipped[i].Proof = provenInCI
		found = true
	}
	if !found {
		t.Fatal("no refused row to flip: this test is measuring a table it does not understand")
	}
	capabilityMatrix = flipped
	// Asserted on what the refusal *says*, not on there being one, and the
	// falsification is why. With the flipped-row guard neutralised, the other
	// rule below still fires about the same matrix — the pair is vetoed and no
	// row claims the doorstep — so a test reading only "something was reported"
	// stayed green through a mutation that had disarmed exactly the guard it
	// names. Two correct findings about one mutated table are not
	// interchangeable.
	flippedProblems := strings.Join(vetoProblems(facts.Vetoes), "\n")
	if !strings.Contains(flippedProblems, "before a process starts") {
		t.Errorf("a row promising a client the pack vetoes passed: `feint up` would refuse what "+
			"the README promises, which is the defect this table exists to make impossible\n  %s",
			flippedProblems)
	}

	// And the other direction: a veto the table never learned about. Dropping
	// the rows rather than editing them, because the silent failure is the one
	// where nothing is written down at all.
	var withoutRefusals []capabilityRow
	for _, row := range restore {
		if row.Support != capabilityRefused {
			withoutRefusals = append(withoutRefusals, row)
		}
	}
	capabilityMatrix = withoutRefusals
	problems := vetoProblems(facts.Vetoes)
	if len(problems) == 0 {
		t.Error("a pack vetoes an engine and no row says so, and it passed: the refusal would " +
			"exist in up.go with no generated sentence able to mention it")
	}
	if !strings.Contains(strings.Join(problems, "\n"), "exoscale") {
		t.Errorf("the refusal names no pack, which is the one thing needed to fix it:\n  %s",
			strings.Join(problems, "\n  "))
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
			mutated := append([]capabilityRow{}, restore...)
			touched := false
			for i := range mutated {
				if mutated[i].Support == capabilityRefused {
					mutation.apply(&mutated[i])
					touched = true
					break
				}
			}
			if !touched {
				t.Fatal("no refused row to mutate")
			}
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
