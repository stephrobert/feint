package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The client matrix names every provider CI drives a client against.
//
// The column used to be a constant — `{"Terraform", "TERRAFORM_VERSION",
// "Scaleway"}` — under a marker reading "Do not edit by hand", while
// conformance.yml ran tools/conformance/outscale/terraform.sh on every pull
// request. This is the refusing half of that: with the constant back, the row
// says Scaleway alone and the Outscale assertion below goes red.
func TestTheClientMatrixCreditsEveryProviderCIProves(t *testing.T) {
	workflow := filepath.Join("..", "..", ".github", "workflows", "conformance.yml")
	root := filepath.Join("..", "..", conformanceRoot)

	rendered, err := renderClients(workflow, root)
	if err != nil {
		t.Fatalf("render the client matrix: %v", err)
	}

	// The accepting half, on the workflow as it stands. Each of these is a
	// provider a Terraform fixture proves today; a row missing one is the defect.
	for _, client := range []string{"Terraform", "OpenTofu"} {
		row := rowFor(t, rendered, client)
		for _, provider := range []string{"Scaleway", "Outscale"} {
			if !strings.Contains(row, provider) {
				t.Errorf("the %s row does not credit %s, and CI runs its Terraform "+
					"suite on every pull request:\n  %s", client, provider, row)
			}
		}
		// The constraint, per provider, read from that provider's own fixture.
		for _, pin := range []string{"`scaleway/scaleway ", "`outscale/outscale "} {
			if !strings.Contains(row, pin) {
				t.Errorf("the %s row states no %sconstraint: the Terraform version "+
					"alone proves nothing, what answers the emulator is the provider:\n  %s",
					client, pin, row)
			}
		}
	}

	// And the single-provider clients stay single, so the derivation is not
	// simply crediting everything to everyone.
	if row := rowFor(t, rendered, "`oapi-cli`"); strings.Contains(row, "Scaleway") {
		t.Errorf("the oapi-cli row credits Scaleway, which no workflow drives it against:\n  %s", row)
	}
}

// A workflow that stops running a suite drops the provider from the row.
//
// Without this, the assertions above would also pass on a table that credited
// every provider to every client unconditionally. Asserted on a copy of the real
// file with one invocation removed, the way the status table's test does.
func TestAClientLosesAProviderCIStopsDriving(t *testing.T) {
	workflow := filepath.Join("..", "..", ".github", "workflows", "conformance.yml")
	root := filepath.Join("..", "..", conformanceRoot)

	body, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("read the workflow: %v", err)
	}
	withoutOutscaleTerraform := strings.Replace(string(body),
		"run: tools/conformance/outscale/terraform.sh", "run: true #", 1)
	if withoutOutscaleTerraform == string(body) {
		t.Fatal("the workflow no longer runs tools/conformance/outscale/terraform.sh: " +
			"either it was removed, in which case the table must have lost Outscale, " +
			"or this test is measuring a file it does not understand")
	}

	trimmed := filepath.Join(t.TempDir(), "conformance.yml")
	if err := os.WriteFile(trimmed, []byte(withoutOutscaleTerraform), 0o600); err != nil {
		t.Fatalf("write the trimmed workflow: %v", err)
	}

	rendered, err := renderClients(trimmed, root)
	if err != nil {
		t.Fatalf("render from the trimmed workflow: %v", err)
	}
	row := rowFor(t, rendered, "Terraform")
	if strings.Contains(row, "Outscale") {
		t.Errorf("Terraform still credits Outscale after CI stopped running its suite, "+
			"so the column is not read from the workflow:\n  %s", row)
	}
	if strings.Contains(row, "outscale/outscale") {
		t.Errorf("the row still pins the Outscale provider after CI stopped running "+
			"its suite:\n  %s", row)
	}
	// The accepting half of the same copy: Scaleway is untouched, so the removal
	// took one proof rather than breaking the scan.
	if !strings.Contains(row, "Scaleway") {
		t.Errorf("Scaleway vanished with Outscale, so the trimmed workflow broke the "+
			"scan instead of removing one invocation:\n  %s", row)
	}
}

// A client CI drives and clientSources does not list is refused, not dropped.
//
// This is the understatement defect in its general form: the table would be
// short by a row and read as complete.
func TestAnUnlistedClientIsRefusedRatherThanLeftOut(t *testing.T) {
	workflow := filepath.Join("..", "..", ".github", "workflows", "conformance.yml")
	root := filepath.Join("..", "..", conformanceRoot)

	// clientOf maps the suite; clientSources is what prints. Name a client in the
	// first that the second does not carry, which is what adding a suite without
	// finishing the job looks like.
	original := clientOf["exo-cli"]
	clientOf["exo-cli"] = "`exo`, `exo-experimental`"
	t.Cleanup(func() { clientOf["exo-cli"] = original })

	_, err := renderClients(workflow, root)
	if err == nil {
		t.Fatal("the matrix rendered while CI drove a client it does not list, so a " +
			"proof would be invisible on the page")
	}
	if !strings.Contains(err.Error(), "exo-experimental") {
		t.Errorf("the refusal does not name the missing client, which is the one thing "+
			"a reader needs to fix it: %v", err)
	}
}

// rowFor returns the rendered table row whose first cell is client.
func rowFor(t *testing.T, rendered, client string) string {
	t.Helper()
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "| "+client+" |") {
			return line
		}
	}
	t.Fatalf("no row for %s in the rendered matrix:\n%s", client, rendered)
	return ""
}
