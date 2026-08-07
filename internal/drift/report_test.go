package drift_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/drift"
)

func upstream() []drift.Operation {
	return []drift.Operation{
		{Name: "instance/v1/API.ListServers", Product: "instance", Version: "v1"},
		{Name: "instance/v1/API.CreateServer", Product: "instance", Version: "v1"},
		{Name: "instance/v1/API.GetDashboard", Product: "instance", Version: "v1"},
		{Name: "rdb/v1/API.ListInstances", Product: "rdb", Version: "v1"},
	}
}

func TestCompareClassifies(t *testing.T) {
	rep := drift.Compare("scaleway", upstream(),
		[]string{"instance/v1/API.ListServers", "instance/v1/API.CreateServer"},
		map[string]string{"instance/v1/API.GetDashboard": "out of scope for this test"},
	)

	if rep.Total != 4 || rep.Implemented != 2 || rep.Declined != 1 || rep.Unknown != 1 {
		t.Fatalf("unexpected counts: %+v", rep)
	}
	if len(rep.Orphans) != 0 {
		t.Fatalf("unexpected orphans: %v", rep.Orphans)
	}

	// The unknown one is the whole point: rdb was never decided about.
	for _, e := range rep.Entries {
		if e.Operation == "rdb/v1/API.ListInstances" && e.Status != drift.StatusUnknown {
			t.Fatalf("expected rdb to be unknown, got %s", e.Status)
		}
	}
}

// A route pointing at an operation that no longer exists upstream is the signal
// that an API was removed under us. It must not be silently ignored.
func TestCompareDetectsOrphans(t *testing.T) {
	rep := drift.Compare("scaleway", upstream(),
		[]string{"instance/v1/API.ListServers", "instance/v1/API.TypoedName"},
		nil,
	)
	if len(rep.Orphans) != 1 || rep.Orphans[0] != "instance/v1/API.TypoedName" {
		t.Fatalf("expected the typo to surface as an orphan, got %v", rep.Orphans)
	}
}

func TestByProduct(t *testing.T) {
	rep := drift.Compare("scaleway", upstream(), []string{"instance/v1/API.ListServers"}, nil)
	byProduct := rep.ByProduct()

	if byProduct["instance"].Total != 3 {
		t.Fatalf("expected 3 instance operations, got %d", byProduct["instance"].Total)
	}
	if byProduct["rdb"].Implemented != 0 || byProduct["rdb"].Unknown != 1 {
		t.Fatalf("unexpected rdb counts: %+v", byProduct["rdb"])
	}
}

func TestWriteTextListsOnlyStartedProducts(t *testing.T) {
	rep := drift.Compare("scaleway", upstream(), []string{"instance/v1/API.ListServers"}, nil)

	var buf bytes.Buffer
	if err := rep.WriteText(&buf); err != nil {
		t.Fatalf("write text: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "instance") {
		t.Fatalf("expected the started product to be listed:\n%s", out)
	}
	if strings.Contains(out, "\n  rdb ") {
		t.Fatalf("untouched products should stay out of the summary:\n%s", out)
	}
	if !strings.Contains(out, "TOTAL") {
		t.Fatalf("expected a total line:\n%s", out)
	}
}

// mixedVersions is a surface where the product totals hide the decision: two
// versions of instance, one mostly served, the other declined as a block. This
// is the real shape of the Scaleway SDK (instance v1 next to the v2alpha1
// rewrite, block v1 next to v1alpha1).
func mixedVersions() []drift.Operation {
	return []drift.Operation{
		{Name: "instance/v1/API.ListServers", Product: "instance", Version: "v1"},
		{Name: "instance/v1/API.CreateServer", Product: "instance", Version: "v1"},
		{Name: "instance/v2alpha1/API.ListServers", Product: "instance", Version: "v2alpha1"},
		{Name: "rdb/v1/API.ListInstances", Product: "rdb", Version: "v1"},
	}
}

func TestVersionsSplitsAProductAcrossItsAPIVersions(t *testing.T) {
	rep := drift.Compare("scaleway", mixedVersions(),
		[]string{"instance/v1/API.ListServers"},
		map[string]string{"instance/v2alpha1/API.ListServers": "out of scope for this test"},
	)
	instance := rep.ByProduct()["instance"]

	versions := instance.Versions()
	if len(versions) != 2 {
		t.Fatalf("expected two versions for instance, got %+v", versions)
	}
	v1, v2 := versions[0], versions[1]
	if v1.Version != "v1" || v1.Total != 2 || v1.Implemented != 1 || v1.Unknown != 1 {
		t.Fatalf("unexpected v1 counts: %+v", v1)
	}
	if v2.Version != "v2alpha1" || v2.Total != 1 || v2.Declined != 1 {
		t.Fatalf("unexpected v2alpha1 counts: %+v", v2)
	}
}

// A version-blind text report reads "instance: 1 implemented, 1 declined" and
// hides that the declined half is an alpha rewrite nobody should act on. The
// summary must break a multi-version product down, and must not double the
// lines of a single-version one.
func TestWriteTextBreaksDownMultiVersionProducts(t *testing.T) {
	rep := drift.Compare("scaleway", mixedVersions(),
		[]string{"instance/v1/API.ListServers", "rdb/v1/API.ListInstances"},
		map[string]string{"instance/v2alpha1/API.ListServers": "out of scope for this test"},
	)

	var buf bytes.Buffer
	if err := rep.WriteText(&buf); err != nil {
		t.Fatalf("write text: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "v2alpha1") {
		t.Fatalf("expected a version row for the multi-version product:\n%s", out)
	}
	// rdb has one version: a sub-row would repeat the product row verbatim.
	if strings.Count(out, "v1 ")+strings.Count(out, "v1\n") > 1 {
		t.Fatalf("expected exactly one version sub-row (instance's v1):\n%s", out)
	}
}

func TestWriteJSONCarriesPerVersionCounts(t *testing.T) {
	rep := drift.Compare("scaleway", mixedVersions(),
		[]string{"instance/v1/API.ListServers"},
		map[string]string{"instance/v2alpha1/API.ListServers": "out of scope for this test"},
	)

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatalf("write json: %v", err)
	}
	var got struct {
		Products []struct {
			Product  string `json:"product"`
			Versions []struct {
				Version  string `json:"version"`
				Total    int    `json:"total"`
				Declined int    `json:"declined"`
			} `json:"versions"`
		} `json:"products"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	for _, p := range got.Products {
		if p.Product != "instance" {
			continue
		}
		if len(p.Versions) != 2 || p.Versions[1].Version != "v2alpha1" || p.Versions[1].Declined != 1 {
			t.Fatalf("unexpected instance versions: %+v", p.Versions)
		}
		return
	}
	t.Fatalf("no instance product in the payload: %s", buf.String())
}

func TestWriteJSONIsConsumableByTheDocsSite(t *testing.T) {
	rep := drift.Compare("scaleway", upstream(), []string{"instance/v1/API.ListServers"}, nil)

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatalf("write json: %v", err)
	}

	var got struct {
		Provider string `json:"provider"`
		Total    int    `json:"total"`
		Products []struct {
			Product     string `json:"product"`
			Implemented int    `json:"implemented"`
		} `json:"products"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("the docs site could not parse the report: %v", err)
	}
	if got.Provider != "scaleway" || got.Total != 4 || len(got.Products) != 2 {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

// An operation declined with no reason must stay declined.
//
// This is the test the fix shipped without, and its absence was the finding: the
// original defect used the reason string as an in-band sentinel
// (`declinedSet[op.Name] != ""`), so an empty reason silently became "untriaged"
// *and* was reported as an orphan — an operation that matches upstream announced
// as matching nothing. Reintroducing that defect passed the entire suite, which
// means the comment describing the repair was the only thing guarding it.
func TestAnEmptyReasonStaysDeclinedAndIsNamed(t *testing.T) {
	rep := drift.Compare("scaleway", upstream(), nil,
		map[string]string{"instance/v1/API.ListServers": ""})

	if rep.Declined != 1 {
		t.Errorf("an empty reason reclassified the operation: declined=%d, unknown=%d", rep.Declined, rep.Unknown)
	}
	if len(rep.Orphans) != 0 {
		t.Errorf("an operation that exists upstream was reported as an orphan: %v", rep.Orphans)
	}
	if len(rep.Unexplained) != 1 || rep.Unexplained[0] != "instance/v1/API.ListServers" {
		t.Errorf("the missing reason was not named: %v", rep.Unexplained)
	}
	for _, e := range rep.Entries {
		if e.Operation == "instance/v1/API.ListServers" && e.Status != drift.StatusDeclined {
			t.Errorf("status is %q, want declined", e.Status)
		}
	}
}

// And the accepting half: a reason that is there must not be reported.
func TestAReasonThatIsThereIsNotReported(t *testing.T) {
	rep := drift.Compare("scaleway", upstream(), nil,
		map[string]string{"instance/v1/API.ListServers": "a local emulator has no inventory to report on"})
	if len(rep.Unexplained) != 0 {
		t.Errorf("a declared reason was reported as missing: %v", rep.Unexplained)
	}
}

// The artefact carries the verdict of every operation, with the reason a
// declined one is declined.
//
// The counts alone are what a table needs and not what a reader needs: "111
// declined" on one product is a number nobody can act on, and the argument for
// each of those refusals is already written in the pack's Declined() block. It
// used to be reachable only through `feint coverage --format list`, which needs
// an SDK checkout and a scan, so nobody read it.
//
// Shipping the entries is also what stops a second implementation of the
// comparison. Compare owns the join between the upstream surface, what the packs
// serve and what they decline; a reader that recomputed it — the page, say —
// would be a second answer nobody could reconcile with the command.
func TestTheArtefactCarriesTheReasonEachOperationIsDeclined(t *testing.T) {
	rep := drift.Compare("scaleway", upstream(),
		[]string{"instance/v1/API.ListServers"},
		map[string]string{"instance/v1/API.CreateServer": "the emulator creates servers through another route"},
	)

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatalf("write json: %v", err)
	}
	// Decoded through the type the writer uses, which is the whole point of the
	// change this test guards: a hand-written reader here would prove that a
	// hand-written reader can be kept in step, not that the format has one owner.
	got, err := drift.LoadCoverage(&buf)
	if err != nil {
		t.Fatalf("read back the artefact: %v", err)
	}

	if len(got.Entries) != len(rep.Entries) {
		t.Fatalf("the artefact carries %d entries, the report has %d", len(got.Entries), len(rep.Entries))
	}
	var declined, served int
	for _, e := range got.Entries {
		switch e.Operation {
		case "instance/v1/API.CreateServer":
			declined++
			if e.Status != drift.StatusDeclined {
				t.Errorf("CreateServer is %q, want declined", e.Status)
			}
			if e.Reason == "" {
				t.Error("the reason did not survive the artefact, which is the only reason to carry entries at all")
			}
		case "instance/v1/API.ListServers":
			served++
			if e.Status != drift.StatusImplemented {
				t.Errorf("ListServers is %q, want implemented", e.Status)
			}
		}
	}
	if declined != 1 || served != 1 {
		t.Errorf("the artefact lost operations: %d declined, %d implemented", declined, served)
	}
}

// Two runs over an unchanged surface produce the same bytes.
//
// The artefact is committed, and the weekly workflow decides that the upstream
// API moved by diffing this directory. An unstable order — or a timestamp —
// would open a drift pull request every Monday whether or not anything had
// changed, and a gate that fires every week is a gate everybody learns to
// ignore.
func TestTheArtefactIsByteStableAcrossRuns(t *testing.T) {
	declined := map[string]string{"instance/v1/API.CreateServer": "a reason"}
	report := func() drift.Report {
		return drift.Compare("scaleway", upstream(), []string{"instance/v1/API.ListServers"}, declined)
	}

	// The order the scan happened to produce must not reach the file. Asserted
	// by reversing it rather than by running twice: two runs of the same code
	// over the same input agree whatever the order, so a repeat would pass with
	// the sort deleted — measured, in a falsification run where exactly that
	// mutation survived.
	shuffled := report()
	for i, j := 0, len(shuffled.Entries)-1; i < j; i, j = i+1, j-1 {
		shuffled.Entries[i], shuffled.Entries[j] = shuffled.Entries[j], shuffled.Entries[i]
	}

	var first, second bytes.Buffer
	if err := report().WriteJSON(&first); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if err := shuffled.WriteJSON(&second); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if first.String() != second.String() {
		t.Errorf("the artefact carries the order the scan produced:\n%s\n---\n%s", first.String(), second.String())
	}
	if strings.Contains(first.String(), time.Now().UTC().Format("2006-01-02")) {
		t.Error("the artefact carries today's date; a timestamp here opens a drift pull request every week")
	}
}
