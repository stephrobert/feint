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
	}, "")
	for _, token := range []string{"`client`", "`contract`", "`shape`", "`runtime`", "`probe`", "`behaviour`", "`negative`"} {
		if !strings.Contains(full, token) {
			t.Errorf("missing %s in %q", token, full)
		}
	}
	// A refusal-only probe verdict names itself and never upgrades to `probe`.
	refusal := evidenceTokens(emulator.Evidence{Probed: emulator.ProbeRefusal, Contract: emulator.ContractUnchecked, Shape: emulator.ShapeUnknown}, "")
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

	if got := evidenceTokens(emulator.Evidence{Contract: emulator.ContractUnchecked, Shape: emulator.ShapeUnknown}, ""); got != "—" {
		t.Errorf("no proof reads as a dash, got %q", got)
	}
	violated := evidenceTokens(emulator.Evidence{Driven: true, Contract: emulator.ContractViolating}, "")
	if !strings.Contains(violated, "contract-violated") {
		t.Errorf("a violation must be printed loudly, got %q", violated)
	}

	// A declared reason renders as its own token, and only where the `client`
	// one is absent: an operation a client drives is proven, whatever a stale
	// sentence at the route might still say.
	explained := evidenceTokens(emulator.Evidence{Probed: emulator.ProbeResponse}, "no CLI subcommand maps to it")
	if !strings.Contains(explained, "`no-client`") {
		t.Errorf("a declared reason must be visible in the column, got %q", explained)
	}
	if driven := evidenceTokens(emulator.Evidence{Driven: true}, "no CLI subcommand maps to it"); strings.Contains(driven, "no-client") {
		t.Errorf("an operation a client drives must not be printed as unreachable, got %q", driven)
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
	packs, err := packsFor(env)
	if err != nil {
		t.Fatalf("build the packs: %v", err)
	}
	srv, err := emulator.NewServer(env, packs...)
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
		Machines: []string{"incus", "none"},
		// The three verdict fields carry a declared value, because a record that
		// leaves them empty is one readEvidence refuses (#406) — and a fixture
		// that cannot be read measures the reader, not the guard under test.
		Operations: map[string]emulator.Evidence{
			"instance/v1/API.ListServers": row(false, false, emulator.ShapeUnobserved),
		},
	}
	writeArtefact(t, path, earned)

	// A run that reaches machines off only would drop every proof taken under
	// incus. It must refuse, and name what would go.
	offOnly := &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines: []string{"none"},
		Operations: map[string]emulator.Evidence{
			"instance/v1/API.ListServers": row(false, false, emulator.ShapeUnobserved),
		},
	}
	lost, _ := runtimesLost(path, offOnly)
	if len(lost) != 1 || lost[0] != "incus" {
		t.Fatalf("narrowing to machines-off reported %v as lost, want [incus]", lost)
	}

	// The accepting halves, because a guard that refuses every write would pass
	// the assertion above and stop the tool working.
	if lost, _ := runtimesLost(path, earned); len(lost) != 0 {
		t.Errorf("rewriting the same runtimes reported %v as lost", lost)
	}
	wider := &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines:   []string{"incus", "incus-ovn", "none"},
		Operations: earned.Operations,
	}
	if lost, _ := runtimesLost(path, wider); len(lost) != 0 {
		t.Errorf("gaining a runtime reported %v as lost", lost)
	}
	// And the first write of an artefact narrows nothing.
	if lost, _ := runtimesLost(filepath.Join(dir, "absent.json"), offOnly); len(lost) != 0 {
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

// #398. `runtimesLost` refuses a regeneration that reaches fewer runtimes, and
// nothing looked at the operations. So a record could quietly demote one whose
// assertion was still in the suite and still passing — which is exactly the
// property #123 says this artefact must not have, stated for its own evidence
// and equally true of a scheduling accident.
//
// Report, never refusal: an axis may legitimately shrink when a claim is
// corrected, and a suite that loses an assertion *must* demote what it proved.
// Both halves are asserted here, because a check that named every write would
// pass the first and be worthless.
func TestARegenerationNamesTheOperationsItDemotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.json")

	writeArtefact(t, path, &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines: []string{"none"},
		Operations: map[string]emulator.Evidence{
			"instance/v1/API.ListServers": row(true, true, emulator.ShapeObserved),
			"instance/v1/API.GetServer":   row(true, true, emulator.ShapeUnobserved),
			"instance/v1/API.CreateIP":    row(true, false, emulator.ShapeUnobserved),
		},
	})

	// One operation loses `behaviour`, one loses `shape`, one disappears from
	// the record entirely, and one is untouched.
	thinner := &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines: []string{"none"},
		Operations: map[string]emulator.Evidence{
			"instance/v1/API.ListServers": row(true, false, emulator.ShapeUnobserved),
			"instance/v1/API.GetServer":   row(true, true, emulator.ShapeUnobserved),
		},
	}
	lost := axesLost(path, thinner)
	for axis, want := range map[string][]string{
		"behaviour": {"instance/v1/API.ListServers"},
		"shape":     {"instance/v1/API.ListServers"},
		"driven":    {"instance/v1/API.CreateIP"},
	} {
		if got := lost[axis]; len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
			t.Errorf("%s: demoted %v, want %v", axis, got, want)
		}
	}
	if len(lost) != 3 {
		t.Errorf("three axes lost something and the report names %d: %v", len(lost), lost)
	}

	var said strings.Builder
	reportAxesLost(&said, path, thinner)
	for _, want := range []string{"instance/v1/API.ListServers", "`behaviour`", "`shape`", "(#398)"} {
		if !strings.Contains(said.String(), want) {
			t.Errorf("the report does not mention %s:\n%s", want, said.String())
		}
	}

	// The accepting halves. A record that keeps everything says nothing, and a
	// record that gains an axis is a widening.
	wider := &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines: []string{"none"},
		Operations: map[string]emulator.Evidence{
			"instance/v1/API.ListServers": row(true, true, emulator.ShapeObserved),
			"instance/v1/API.GetServer":   negativeToo(row(true, true, emulator.ShapeUnobserved)),
			"instance/v1/API.CreateIP":    row(true, false, emulator.ShapeUnobserved),
		},
	}
	if got := axesLost(path, wider); len(got) != 0 {
		t.Errorf("a record that loses nothing reports %v", got)
	}
	var quiet strings.Builder
	reportAxesLost(&quiet, path, wider)
	if quiet.Len() != 0 {
		t.Errorf("a write that demotes nothing must say nothing, and says: %s", quiet.String())
	}
	// And the first write of an artefact demotes nothing.
	if got := axesLost(filepath.Join(dir, "absent.json"), thinner); len(got) != 0 {
		t.Errorf("writing where no record exists reports %v", got)
	}
}

// row is one evidence line whose three verdict fields carry a value the record
// actually uses. Written as a helper because a literal that leaves them empty is
// exactly the row TestAnAxisWithNoVerdictIsNotEarned refuses, and a fixture that
// cannot be read is a fixture that measures the reader.
func row(driven, behaviour bool, shape string) emulator.Evidence {
	return emulator.Evidence{
		Driven: driven, Behaviour: behaviour, Shape: shape,
		Probed: emulator.ProbeNone, Contract: emulator.ContractUnchecked,
	}
}

func negativeToo(e emulator.Evidence) emulator.Evidence {
	e.Negative = true
	return e
}

// #406's cause, in this repository's own reader rather than in the throwaway
// script that carried it first: three of the seven axes are verdicts, and a
// predicate written as "not the losing value" counts a row that carries no
// verdict at all as a success. `probed` was written that way — `!= "none"` — so
// an artefact missing the key, which encoding/json decodes to "", earned it.
//
// Two controls, because either alone leaves the hole open: the predicate names
// what earns the axis, and the boundary refuses a row it cannot account for so
// no future consumer has to decide again.
func TestAnAxisWithNoVerdictIsNotEarned(t *testing.T) {
	// The verdict axes, asked of the list rather than named here, so a fourth
	// one added later is held to the same rule without editing this test.
	full := emulator.Evidence{
		Probed: emulator.ProbeResponse, Contract: emulator.ContractClean, Shape: emulator.ShapeObserved,
	}
	var axes []evidenceAxis
	for _, a := range evidenceAxisList() {
		if a.verdict(full) != "" {
			axes = append(axes, a)
		}
	}
	if len(axes) != 3 {
		t.Fatalf("three axes are verdicts and the list carries %d", len(axes))
	}
	// A row with every verdict field empty: nothing about it was ever measured,
	// so it earns none of the three.
	for _, a := range axes {
		if a.earned(emulator.Evidence{}) {
			t.Errorf("`%s` is earned by a row that carries no verdict at all", a.Name)
		}
	}

	// And such a row never reaches a predicate, because the record is refused.
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.json")
	writeArtefact(t, path, &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines:   []string{"none"},
		Operations: map[string]emulator.Evidence{"instance/v1/API.ListServers": {Driven: true}},
	})
	_, err := readEvidence(path)
	if err == nil {
		t.Fatal("a record whose verdicts are outside their vocabulary was accepted")
	}
	if !strings.Contains(err.Error(), "instance/v1/API.ListServers") || !strings.Contains(err.Error(), "probed") {
		t.Errorf("the refusal must name the operation and the field, and says: %v", err)
	}

	// The accepting half: a record whose verdicts are all declared reads back.
	writeArtefact(t, path, &evidenceArtefact{
		Format: evidenceFormat, Version: evidenceVersion,
		Machines:   []string{"none"},
		Operations: map[string]emulator.Evidence{"instance/v1/API.ListServers": row(true, true, emulator.ShapeObserved)},
	})
	if _, err := readEvidence(path); err != nil {
		t.Errorf("a record that accounts for itself was refused: %v", err)
	}
}
