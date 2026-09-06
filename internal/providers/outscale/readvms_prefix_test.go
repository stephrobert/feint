package outscale_test

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/providers/outscale"
)

// ReadVms alone refuses a filter value that is not an identifier (#396).
//
// corpus/outscale/oapi-cli-refusals.jsonl, recorded 2026-08-21 against a real
// account: ReadVms with Filters.VmIds ["not-an-identifier"] answers 400 and
// Errors [{Code "4104", Type "InvalidParameterValue", Details "the provided
// value does not respect the expected ID prefix"}]. The same probe on sixteen
// other reads answered 200 and an empty list in the same run. The asymmetry is
// upstream's and measured, which is why the check lives in readVms and in no
// shared layer: an emulator that validated all seventeen would be wrong sixteen
// times. The second test below is the sixteen.

func TestReadVmsRefusesAFilterValueThatIsNotAnIdentifier(t *testing.T) {
	ts := newServer(t)
	inventory(t, ts)

	status, out := post(t, ts, "ReadVms", `{"Filters":{"VmIds":["not-an-identifier"]}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a VmIds value with no identifier prefix answered %d: %v", status, out)
	}
	errs, _ := out["Errors"].([]any)
	if len(errs) != 1 {
		t.Fatalf("the refusal carries %d errors, want the recorded one: %v", len(errs), out)
	}
	first, _ := errs[0].(map[string]any)
	if first["Code"] != "4104" || first["Type"] != "InvalidParameterValue" {
		t.Errorf("the refusal is %v/%v, and the recording says 4104/InvalidParameterValue", first["Code"], first["Type"])
	}
	if details, _ := first["Details"].(string); !strings.Contains(details, "ID prefix") {
		t.Errorf("the refusal does not name the prefix: %q", details)
	}

	// The accepting halves, without which a guard refusing every VmIds value
	// would pass the assertions above: a real identifier selects its machine,
	// and an identifier nothing carries selects nothing with a 200.
	_, all := post(t, ts, "ReadVms", `{}`)
	vms, _ := all["Vms"].([]any)
	if len(vms) == 0 {
		t.Fatal("inventory created no Vm; nothing here can be selected")
	}
	id, _ := vms[0].(map[string]any)["VmId"].(string)
	status, out = post(t, ts, "ReadVms", `{"Filters":{"VmIds":["`+id+`"]}}`)
	if selected, _ := out["Vms"].([]any); status != http.StatusOK || len(selected) != 1 {
		t.Errorf("a real VmId answered %d and %d machines: %v", status, len(selected), out)
	}
	status, out = post(t, ts, "ReadVms", `{"Filters":{"VmIds":["i-00000000"]}}`)
	if selected, _ := out["Vms"].([]any); status != http.StatusOK || len(selected) != 0 {
		t.Errorf("an identifier nothing carries answered %d and %d machines: %v", status, len(selected), out)
	}
}

// The other reads keep answering 200 on the same value: the refusal is
// ReadVms's alone, as measured, and not a rule. Every declared identifier
// filter of every other read is sent the value the cloud refused on ReadVms,
// and ReadVmsState with the very same filter name is among them.
func TestEveryOtherReadAcceptsAFilterValueThatIsNotAnIdentifier(t *testing.T) {
	ts := newServer(t)
	inventory(t, ts)

	declared := outscale.DeclaredFilters()
	actions := make([]string, 0, len(declared))
	for action := range declared {
		actions = append(actions, action)
	}
	sort.Strings(actions)

	witnessed := 0
	vmsState := false
	for _, action := range actions {
		if action == "ReadVms" {
			continue
		}
		for _, filter := range declared[action] {
			if !strings.HasSuffix(filter.Name, "Ids") {
				continue
			}
			status, out := post(t, ts, action, `{"Filters":{"`+filter.Name+`":["not-an-identifier"]}}`)
			if status != http.StatusOK {
				t.Errorf("%s %s [not-an-identifier] answered %d, and the cloud answers 200 on every read but ReadVms: %v",
					action, filter.Name, status, out)
				continue
			}
			witnessed++
			if action == "ReadVmsState" && filter.Name == "VmIds" {
				vmsState = true
			}
		}
	}
	if witnessed < 10 {
		t.Fatalf("only %d identifier filters were asked; the sixteen reads of the recording carry more than that", witnessed)
	}
	if !vmsState {
		t.Fatal("ReadVmsState's VmIds was not asked, and it is the filter name the refusal is about on the other read")
	}
}
