package drift_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

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
