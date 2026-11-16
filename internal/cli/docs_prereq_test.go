package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The table must follow the constants, not merely happen to agree with them
// today. A rendered "6.0.4" typed into the generator would pass every reading and
// fail the day the floor moves, which is the exact defect the generation exists
// to remove: the audit of 2026-07-29 found the install page arguing from a floor
// nobody had re-derived.
func TestPrereqTableFollowsTheVersionConstants(t *testing.T) {
	before := renderPrereq("")
	if !strings.Contains(before, versionText(incusMinimum[:])) {
		t.Fatalf("the table does not carry the floor %s:\n%s", versionText(incusMinimum[:]), before)
	}
	if !strings.Contains(before, versionText(incusRecommended[:])) {
		t.Fatalf("the table does not carry the recommended series %s:\n%s", versionText(incusRecommended[:]), before)
	}

	// machine.IncusMinimum too: the reasons on the page are
	// machine.IncusBelowMinimum's sentences, which read it (#979).
	floor, series, runtimeFloor := incusMinimum, incusRecommended, machine.IncusMinimum
	t.Cleanup(func() { incusMinimum, incusRecommended, machine.IncusMinimum = floor, series, runtimeFloor })
	incusMinimum, machine.IncusMinimum = [3]int{9, 9, 9}, [3]int{9, 9, 9}
	incusRecommended = [2]int{9, 9}

	after := renderPrereq("")
	if strings.Contains(after, versionText(floor[:])) {
		t.Fatalf("the old floor %s survived a change of the constant:\n%s", versionText(floor[:]), after)
	}
	if !strings.Contains(after, "9.9.9") || !strings.Contains(after, "9.9") {
		t.Fatalf("the table did not follow the constants:\n%s", after)
	}
}

// The Go version comes from go.mod for the same reason, and its absence must not
// break the render: somebody running the binary outside a checkout has no go.mod.
func TestPrereqReadsTheGoVersionFromTheModule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(path, []byte("module example.com/x\n\ngo 1.42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := renderPrereq(path); !strings.Contains(got, "| Go | 1.42 |") {
		t.Fatalf("the table does not carry the module's Go version:\n%s", got)
	}
	if got := renderPrereq(filepath.Join(dir, "absent")); strings.Contains(got, "| Go |") {
		t.Fatalf("a missing go.mod produced a Go row anyway:\n%s", got)
	}
}

// The two files that pin the conformance clients must agree, and this is the
// case that made it worth checking: `exo` v1.86 ignores the endpoint key in its
// own configuration file and calls the real Exoscale, so the workflow pinned a
// client that talked to a paying account while the Ansible role pinned the one
// the station had measured. Nothing compared them.
func TestClientPinsAreComparedAcrossTheTwoFiles(t *testing.T) {
	dir := t.TempDir()
	workflow := filepath.Join(dir, "conformance.yml")
	ansible := filepath.Join(dir, "main.yml")

	if err := os.WriteFile(workflow, []byte("          EXO_VERSION: 'v1.86.0'\n          SCW_VERSION: '2.56.3'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ansible, []byte("feint_clients_exo_version: \"v1.95.6\"\nfeint_clients_scw_version: \"2.56.3\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := clientPinMismatches(workflow, ansible)
	if len(got) != 1 {
		t.Fatalf("want exactly the exo mismatch, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "EXO_VERSION") {
		t.Fatalf("the mismatch does not name the client: %s", got[0])
	}

	// And the accepting half, which matters as much: a check that fires on
	// agreement is a check somebody switches off.
	if err := os.WriteFile(ansible, []byte("feint_clients_exo_version: \"v1.86.0\"\nfeint_clients_scw_version: \"2.56.3\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := clientPinMismatches(workflow, ansible); len(got) != 0 {
		t.Fatalf("agreeing pins were reported as a mismatch: %v", got)
	}

	// A file that is not there is not a mismatch: the binary runs outside a
	// checkout, where neither exists.
	if got := clientPinMismatches(filepath.Join(dir, "absent"), ansible); len(got) != 0 {
		t.Fatalf("a missing workflow was reported as a mismatch: %v", got)
	}
	if got := clientPinMismatches(workflow, filepath.Join(dir, "absent")); len(got) != 0 {
		t.Fatalf("a missing role default was reported as a mismatch: %v", got)
	}
}

// This repository's own two files, which is the check that would have caught the
// v1.86 pin on the day it landed rather than at an audit months later.
func TestThisRepositoryPinsTheSameClientsInBothPlaces(t *testing.T) {
	root := ".." + string(filepath.Separator) + ".."
	if got := clientPinMismatches(filepath.Join(root, conformanceWorkflow), filepath.Join(root, ansibleClientPins)); len(got) != 0 {
		for _, line := range got {
			t.Error(line)
		}
		t.Fatal("the conformance workflow and the Ansible role install different client versions")
	}
}
