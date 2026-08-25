package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The status table names the clients CI drives, and only those.
//
// It was maintained by hand and drifted the way a hand-written claim does: the
// README said Outscale was proven by `oapi-cli` and Terraform, and both halves
// were true of the repository while only one was true of CI. The fixture exists,
// applies twenty-one resources, plans empty and destroys clean — and no workflow
// ran it, so a regression in it would have reached a release without one red
// check. A reader comparing two files found that. Nothing else could.
//
// What this asserts is the property that replaces the reader: the table is a
// function of the workflow. Add a suite to CI and the client appears; remove it
// and the client goes. Neither is a sentence anybody has to remember to update.
func TestTheStatusTableNamesOnlyClientsCIRuns(t *testing.T) {
	workflow := filepath.Join("..", "..", ".github", "workflows", "conformance.yml")
	proven, err := clientsProvenInCI(workflow)
	if err != nil {
		t.Fatalf("read the clients CI drives: %v", err)
	}
	if len(proven) == 0 {
		t.Fatal("no provider is proven by any client, so this test would pass while measuring nothing")
	}

	// The accepting half, on the state of the workflow as it stands.
	for _, provider := range []string{"scaleway", "outscale", "exoscale"} {
		if len(proven[provider]) == 0 {
			t.Errorf("CI drives no client against %s, and the pack is served", provider)
		}
	}

	// And the refusing half: a workflow that stops running a suite must take the
	// client out of the table. Asserted on a copy of the real file with one
	// invocation removed, because a table that answers the same either way is a
	// table that is still hand-written, whatever it says.
	body, err := os.ReadFile(workflow) //nolint:gosec // a path this repository owns
	if err != nil {
		t.Fatalf("read the workflow: %v", err)
	}
	withoutOutscaleTerraform := strings.Replace(string(body),
		"run: tools/conformance/outscale/terraform.sh", "run: true #", 1)
	if withoutOutscaleTerraform == string(body) {
		t.Fatal("the workflow no longer runs tools/conformance/outscale/terraform.sh: " +
			"either it was removed, in which case the table must have lost Terraform, " +
			"or this test is measuring a file it does not understand")
	}

	trimmed := filepath.Join(t.TempDir(), "conformance.yml")
	if err := os.WriteFile(trimmed, []byte(withoutOutscaleTerraform), 0o600); err != nil {
		t.Fatalf("write the trimmed workflow: %v", err)
	}
	after, err := clientsProvenInCI(trimmed)
	if err != nil {
		t.Fatalf("read the trimmed workflow: %v", err)
	}
	for _, client := range after["outscale"] {
		if strings.Contains(client, "Terraform") {
			t.Errorf("Terraform still proves Outscale after CI stopped running its suite: %v",
				after["outscale"])
		}
	}
	// The rest of the row survives: a guard that empties the cell would pass the
	// assertion above and lose a proof that is still taken.
	if len(after["outscale"]) == 0 {
		t.Error("removing one suite emptied the Outscale row, and octl still runs")
	}
}

// A suite CI runs and nobody mapped is an error, not a silent omission.
//
// The mirror of the defect above: a table that quietly drops the client it does
// not recognise understates what is proven, which is just as wrong as
// overstating it and much harder to notice.
func TestAnUnmappedConformanceSuiteIsRefused(t *testing.T) {
	invented := filepath.Join(t.TempDir(), "conformance.yml")
	body := "jobs:\n  x:\n    steps:\n      - run: tools/conformance/scaleway/pulumi.sh http://127.0.0.1:4599\n"
	if err := os.WriteFile(invented, []byte(body), 0o600); err != nil {
		t.Fatalf("write the workflow: %v", err)
	}

	_, err := clientsProvenInCI(invented)
	if err == nil {
		t.Fatal("a suite no client is mapped to was accepted, so the table would omit it in silence")
	}
	if !strings.Contains(err.Error(), "pulumi") {
		t.Errorf("the refusal does not name the suite, so nobody can act on it: %v", err)
	}
}
