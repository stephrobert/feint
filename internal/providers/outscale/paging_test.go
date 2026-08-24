package outscale_test

import (
	"fmt"
	"net/http"
	"testing"
)

// The bound Outscale states on ResultsPerPage, and the shape of the field that
// makes it enforceable.
//
// These are written against the operations a real client can actually send the
// field to — the conformance suite drives five of them — but the guard is
// shared, so the population here is the whole set of actions whose request
// schema declares it.

// pagedActions are the actions whose Outscale request schema declares
// ResultsPerPage. ReadLoadBalancers is deliberately absent: its schema declares
// none, and this pack asserts no bound there.
var pagedActions = []string{
	"ReadDhcpOptions", "ReadImages", "ReadInternetServices", "ReadKeypairs",
	"ReadNatServices", "ReadNetAccessPointServices", "ReadNetPeerings",
	"ReadNets", "ReadNics", "ReadPublicIpRanges", "ReadPublicIps",
	"ReadRouteTables", "ReadSecurityGroups", "ReadSnapshots", "ReadSubnets",
	"ReadSubregions", "ReadTags", "ReadVmTypes", "ReadVms", "ReadVmsState",
	"ReadVolumes",
}

// A page size outside the published bound is refused, on every action that
// declares the field.
//
// "The maximum number of logs returned in a single response (between `1` and
// `1000`, both included)" — outscale.yaml, identical wording on all twenty-one
// request schemas. Before this, any value was taken and anything below one was
// silently read as "no limit", so the exact value the API refuses returned the
// whole inventory with a 200.
func TestAPageSizeOutsideTheBoundIsRefused(t *testing.T) {
	ts := newServer(t)
	for _, action := range pagedActions {
		for _, size := range []int{0, -1, 1001} {
			status, body := post(t, ts, action, fmt.Sprintf(`{"ResultsPerPage":%d}`, size))
			if status != http.StatusBadRequest {
				t.Errorf("%s with ResultsPerPage %d answered %d; the API takes 1 to 1000", action, size, status)
				continue
			}
			// A refusal a client cannot decode is a refusal it reports as a
			// parsing failure, which sends whoever reads it to the wrong place.
			errs, _ := body["Errors"].([]any)
			if len(errs) == 0 {
				t.Errorf("%s refused ResultsPerPage %d outside the Outscale envelope: %v", action, size, body)
			}
		}
	}
}

// An absent page size is not a zero page size, and the pointer is what keeps
// the two apart.
//
// This is the half a bound alone would get wrong. Decoded into an int, a
// request that never mentions ResultsPerPage arrives as zero — the value the
// API refuses — so a bound written against the int would refuse every ordinary
// read of every client. The witness is that both hold at once: 0 sent is
// refused (above), 0 never sent is served.
func TestAnAbsentPageSizeIsNotAZeroPageSize(t *testing.T) {
	ts := newServer(t)
	for _, action := range pagedActions {
		status, body := post(t, ts, action, `{}`)
		if status != http.StatusOK {
			t.Errorf("%s answered %d to a request that names no page size: %v", action, status, body)
		}
	}
}

// A page size inside the bound is honoured rather than merely accepted, so the
// guard cannot be satisfied by refusing everything.
func TestAPageSizeInsideTheBoundStillTruncates(t *testing.T) {
	ts := newServer(t)
	// The type catalogue is fixed and holds more than one row, which is what
	// makes the truncation observable without creating anything.
	_, all := post(t, ts, "ReadVmTypes", `{}`)
	rows, _ := all["VmTypes"].([]any)
	if len(rows) < 2 {
		t.Fatalf("the type catalogue holds %d rows; this test measures nothing below two", len(rows))
	}
	_, one := post(t, ts, "ReadVmTypes", `{"ResultsPerPage":1}`)
	got, _ := one["VmTypes"].([]any)
	if len(got) != 1 {
		t.Fatalf("ResultsPerPage 1 answered %d rows out of %d", len(got), len(rows))
	}
}

// A filter ReadVmTypes cannot apply is refused by name, and the one it serves
// selects.
//
// The defect: FiltersVmType declares nine filters and this handler read none of
// them, so `--Filters.VmTypeNames tinav6.c1r1p2` answered the whole table with
// a 200. That is indistinguishable from success for a client that then picks
// row zero, and it is the same family filters.go was written to remove from
// every other read of this pack.
func TestAVmTypeFilterIsAppliedRatherThanIgnored(t *testing.T) {
	ts := newServer(t)
	_, all := post(t, ts, "ReadVmTypes", `{}`)
	rows, _ := all["VmTypes"].([]any)
	if len(rows) < 2 {
		t.Fatalf("the type catalogue holds %d rows; this test measures nothing below two", len(rows))
	}
	first, _ := rows[0].(map[string]any)
	name, _ := first["VmTypeName"].(string)
	if name == "" {
		t.Fatal("the first catalogue row carries no VmTypeName")
	}

	status, one := post(t, ts, "ReadVmTypes", `{"Filters":{"VmTypeNames":["`+name+`"]}}`)
	if status != http.StatusOK {
		t.Fatalf("ReadVmTypes refused the filter it serves: %d %v", status, one)
	}
	got, _ := one["VmTypes"].([]any)
	if len(got) != 1 {
		t.Fatalf("VmTypeNames [%s] answered %d rows out of %d; the filter is not applied", name, len(got), len(rows))
	}

	// A name that matches nothing answers nothing. Without this, a filter that
	// is read but never narrows passes the assertion above whenever the
	// catalogue holds exactly one match by luck.
	_, none := post(t, ts, "ReadVmTypes", `{"Filters":{"VmTypeNames":["feint.nosuchtype"]}}`)
	if empty, _ := none["VmTypes"].([]any); len(empty) != 0 {
		t.Fatalf("a type name matching nothing answered %d rows", len(empty))
	}

	// And one it cannot apply is refused rather than ignored.
	status, refused := post(t, ts, "ReadVmTypes", `{"Filters":{"MemorySizes":[8]}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("ReadVmTypes answered %d to a filter it does not apply: %v", status, refused)
	}
}
