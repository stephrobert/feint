package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The artefact is what docs/routes.md prints and what the /falsify criterion
// bites on: a level must not survive the removal of its evidence, a foreign
// file must be refused rather than misread, and nothing anywhere may turn the
// axes into one number.

func TestMatchPatternIsSegmentExact(t *testing.T) {
	cases := []struct {
		pattern, path string
		wild          int
		ok            bool
	}{
		{"/instance/v1/zones/{zone}/servers", "/instance/v1/zones/fr-par-1/servers", 1, true},
		{"/instance/v1/zones/{zone}/servers", "/instance/v1/zones/fr-par-1/ips", 0, false},
		{"/instance/v1/zones/{zone}/servers/{id}", "/instance/v1/zones/fr-par-1/servers", 0, false},
		{"/marketplace/v2/local-images", "/marketplace/v2/local-images", 0, true},
		{"/api/v1/ReadVms", "/api/v1/ReadVms", 0, true},
	}
	for _, c := range cases {
		wild, ok := matchPattern(c.pattern, c.path)
		if ok != c.ok || (ok && wild != c.wild) {
			t.Errorf("matchPattern(%q, %q) = (%d, %v), want (%d, %v)", c.pattern, c.path, wild, ok, c.wild, c.ok)
		}
	}
}

func TestOperationForCallPrefersTheMostSpecificRoute(t *testing.T) {
	routes := []emulator.Route{
		{Method: "GET", Path: "/v2/{resource}", Operation: "generic"},
		{Method: "GET", Path: "/v2/instance", Operation: "specific"},
	}
	op, err := operationForCall(routes, "GET", "/v2/instance")
	if err != nil {
		t.Fatal(err)
	}
	if op != "specific" {
		t.Errorf("the exact segment must win over the wildcard, got %q", op)
	}
}

func TestOperationForCallRefusesATie(t *testing.T) {
	routes := []emulator.Route{
		{Method: "GET", Path: "/v2/{a}/x", Operation: "one"},
		{Method: "GET", Path: "/v2/{b}/x", Operation: "two"},
	}
	if _, err := operationForCall(routes, "GET", "/v2/thing/x"); err == nil {
		t.Fatal("two equally specific routes must be refused, not resolved by luck")
	}
}

// TestShapeCoverageMapsTheCatalogueOntoMountedOperations drives the whole
// resolver against the real packs: a catalogue entry for the servers list must
// come out as the SDK's own operation name, because that name is what every
// other artefact keys on.
func TestShapeCoverageMapsTheCatalogueOntoMountedOperations(t *testing.T) {
	dir := t.TempDir()
	catalogue := `{
	  "provider": "scaleway",
	  "operations": {
	    "GET /instance/v1/zones/fr-par-1/servers": {
	      "method": "GET",
	      "path": "/instance/v1/zones/fr-par-1/servers",
	      "fields": [{"path": "servers", "type": "array"}],
	      "statuses": [200]
	    }
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, "scaleway.json"), []byte(catalogue), 0o600); err != nil {
		t.Fatal(err)
	}

	covered, err := shapeCoveredOperations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !covered["instance/v1/API.ListServers"] {
		t.Errorf("the servers list is observed and mounted; covered = %v", covered)
	}
	if len(covered) != 1 {
		t.Errorf("one observed operation must cover exactly one mounted operation, got %v", covered)
	}
}

func TestShapeCoverageWithoutACatalogueIsNilNotEmpty(t *testing.T) {
	covered, err := shapeCoveredOperations(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if covered != nil {
		t.Fatal("no catalogue at all must read as unknown (nil), not as nothing observed (empty)")
	}
}

func TestJoinEvidenceKeepsTheStrongerAnswerOfTwoFreshLegs(t *testing.T) {
	off := &evidenceArtefact{Format: evidenceFormat, Version: evidenceVersion,
		Machines: []string{"none"},
		Operations: map[string]emulator.Evidence{
			"op": {Driven: true, Probed: true, Contract: emulator.ContractClean, Shape: emulator.ShapeObserved},
		}}
	runtime := &evidenceArtefact{Format: evidenceFormat, Version: evidenceVersion,
		Machines: []string{"incus"},
		Operations: map[string]emulator.Evidence{
			"op": {Driven: true, Dataplane: true, Contract: emulator.ContractViolating, Shape: emulator.ShapeObserved},
		}}

	joined := joinEvidence(runtime, off)
	ev := joined.Operations["op"]
	if !ev.Driven || !ev.Probed || !ev.Dataplane {
		t.Errorf("each boolean axis keeps the leg that earned it: %+v", ev)
	}
	if ev.Contract != emulator.ContractViolating {
		t.Errorf("a violation in either leg must survive the join, got %q", ev.Contract)
	}
	if strings.Join(joined.Machines, ",") != "incus,none" {
		t.Errorf("the join must say which runtimes contributed, got %v", joined.Machines)
	}
}

func TestReadEvidenceRefusesWhatItCannotAccountFor(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "foreign.json")
	if err := os.WriteFile(foreign, []byte(`{"format":"something-else","version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEvidence(foreign); err == nil {
		t.Fatal("a file of another format must be refused, not half-read")
	}

	future := filepath.Join(dir, "future.json")
	if err := os.WriteFile(future, []byte(`{"format":"feint-evidence","version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEvidence(future); err == nil {
		t.Fatal("another version must be refused, not guessed at")
	}
}

func TestEvidenceTokensNameAxesAndNeverCount(t *testing.T) {
	full := evidenceTokens(emulator.Evidence{
		Driven: true, Probed: true, Contract: emulator.ContractClean,
		Dataplane: true, Shape: emulator.ShapeObserved,
	})
	for _, token := range []string{"`client`", "`contract`", "`shape`", "`runtime`", "`probe`"} {
		if !strings.Contains(full, token) {
			t.Errorf("missing %s in %q", token, full)
		}
	}
	// The doctrine in its falsifiable form: the rendering must never collapse
	// the axes into a count or a fraction — "5", "5/5" or "of 5" is exactly
	// the score the record exists to refuse.
	for _, forbidden := range []string{"5", "/", "of"} {
		if strings.Contains(full, forbidden) {
			t.Errorf("the tokens carry %q, which reads as a score: %q", forbidden, full)
		}
	}

	if got := evidenceTokens(emulator.Evidence{Contract: emulator.ContractUnchecked, Shape: emulator.ShapeUnknown}); got != "—" {
		t.Errorf("no proof reads as a dash, got %q", got)
	}
	violated := evidenceTokens(emulator.Evidence{Driven: true, Contract: emulator.ContractViolating})
	if !strings.Contains(violated, "contract-violated") {
		t.Errorf("a violation must be printed loudly, got %q", violated)
	}
}
