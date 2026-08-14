package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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
			"op": {Driven: true, Probed: emulator.ProbeResponse, Behaviour: true, Contract: emulator.ContractClean, Shape: emulator.ShapeObserved},
		}}
	runtime := &evidenceArtefact{Format: evidenceFormat, Version: evidenceVersion,
		Machines: []string{"incus"},
		Operations: map[string]emulator.Evidence{
			"op": {Driven: true, Probed: emulator.ProbeRefusal, Dataplane: true, Negative: true, Contract: emulator.ContractViolating, Shape: emulator.ShapeObserved},
		}}

	joined := joinEvidence(runtime, off)
	ev := joined.Operations["op"]
	if !ev.Driven || !ev.Dataplane || !ev.Behaviour || !ev.Negative {
		t.Errorf("each boolean axis keeps the leg that earned it: %+v", ev)
	}
	if ev.Probed != emulator.ProbeResponse {
		t.Errorf("a validated success in either leg outranks a validated refusal, got %q", ev.Probed)
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
		Driven: true, Probed: emulator.ProbeResponse, Contract: emulator.ContractClean,
		Dataplane: true, Shape: emulator.ShapeObserved,
		Behaviour: true, Negative: true,
	})
	for _, token := range []string{"`client`", "`contract`", "`shape`", "`runtime`", "`probe`", "`behaviour`", "`negative`"} {
		if !strings.Contains(full, token) {
			t.Errorf("missing %s in %q", token, full)
		}
	}
	// A refusal-only probe verdict names itself and never upgrades to `probe`.
	refusal := evidenceTokens(emulator.Evidence{Probed: emulator.ProbeRefusal, Contract: emulator.ContractUnchecked, Shape: emulator.ShapeUnknown})
	if refusal != "`probe-refusal`" {
		t.Errorf("a refusal-only verdict renders its own token, got %q", refusal)
	}
	// The doctrine in its falsifiable form: the rendering must never collapse
	// the axes into a count or a fraction — "5", "5/5" or "of 5" is exactly
	// the score the record exists to refuse.
	for _, forbidden := range []string{"5", "7", "/", "of"} {
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

// Every mounted operation has a row in the evidence artefact.
//
// Not a completeness nicety: the artefact is the record `docs/routes.md` prints
// from, and an operation missing from it renders as "—" — indistinguishable from
// an operation nothing has proven. An audit found the artefact fourteen
// operations behind at a release candidate, because #146 mounted routes after
// #145 wrote the file, and nothing was red. The page was honest and the record
// was stale, which is the half-truth this project exists to refuse, committed on
// the artefact whose whole purpose is to measure rather than assume.
//
// Regenerating is `mise run evidence:update`. This test is what makes forgetting
// it a failure instead of a silent gap.
func TestEveryMountedOperationHasAnEvidenceRow(t *testing.T) {
	// The repository root from this package's directory: moduleRoot lives in the
	// external test package and cannot be reached from here.
	artefact, err := loadEvidenceArtefact(filepath.Join("..", "..", "coverage", "evidence.json"))
	if err != nil {
		t.Fatalf("read the evidence artefact: %v", err)
	}
	if len(artefact.Operations) == 0 {
		t.Fatal("the evidence artefact is empty, so this test would pass while measuring nothing")
	}

	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, packsFor(env)...)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}

	var missing []string
	mounted := 0
	for _, route := range srv.AllRoutes() {
		if route.Operation == "" {
			continue
		}
		mounted++
		if _, recorded := artefact.Operations[route.Operation]; !recorded {
			missing = append(missing, route.Operation)
		}
	}
	if mounted == 0 {
		t.Fatal("no mounted operation was found, so this test measured nothing")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d of %d mounted operations have no row in coverage/evidence.json, "+
			"so docs/routes.md prints \"—\" for them and nothing says the record is behind.\n"+
			"Run `mise run evidence:update`. Missing:\n  %s",
			len(missing), mounted, strings.Join(firstMissing(missing, 8), "\n  "))
	}
}

func firstMissing(all []string, n int) []string {
	if len(all) <= n {
		return all
	}
	return append(all[:n:n], "…")
}

// A record does not quietly narrow the runtimes it was earned under.
//
// Twice in one day an artefact was regenerated from a machines-off run alone and
// silently dropped the dataplane axis for 169 operations. Nothing was red: the
// file declared `machines: ["none"]`, so it was honest — and `docs/routes.md`
// then printed the absence exactly like an operation nothing has proven.
//
// The comparison is on runtimes, not on axes, and that distinction is the whole
// design. An axis can legitimately shrink when a claim is corrected: #156 took
// `probed` from 181 arrivals down to 83 verdicts, and that is a fix. Losing a
// *runtime* is never a fix — it means a leg did not run.
func TestEvidenceRefusesToNarrowTheRuntimesItWasEarnedUnder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.json")

	earned := &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines:   []string{"incus", "none"},
		Operations: map[string]emulator.Evidence{"instance/v1/API.ListServers": {}},
	}
	writeArtefact(t, path, earned)

	// A run that reaches machines off only would drop every proof taken under
	// incus. It must refuse, and name what would go.
	offOnly := &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines:   []string{"none"},
		Operations: map[string]emulator.Evidence{"instance/v1/API.ListServers": {}},
	}
	lost := runtimesLost(path, offOnly)
	if len(lost) != 1 || lost[0] != "incus" {
		t.Fatalf("narrowing to machines-off reported %v as lost, want [incus]", lost)
	}

	// The accepting halves, because a guard that refuses every write would pass
	// the assertion above and stop the tool working.
	if lost := runtimesLost(path, earned); len(lost) != 0 {
		t.Errorf("rewriting the same runtimes reported %v as lost", lost)
	}
	wider := &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines:   []string{"incus", "incus-ovn", "none"},
		Operations: earned.Operations,
	}
	if lost := runtimesLost(path, wider); len(lost) != 0 {
		t.Errorf("gaining a runtime reported %v as lost", lost)
	}
	// And the first write of an artefact narrows nothing.
	if lost := runtimesLost(filepath.Join(dir, "absent.json"), offOnly); len(lost) != 0 {
		t.Errorf("writing where no record exists reported %v as lost", lost)
	}
}

func writeArtefact(t *testing.T, path string, art *evidenceArtefact) {
	t.Helper()
	blob, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, append(blob, '\n'), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
