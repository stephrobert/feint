package outscale_test

import (
	"encoding/json"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// What this pack keeps after its own delete, measured rather than described.
//
// Two families here answer a delete with a state transition and leave the
// record in the store, because that is what the real cloud does: a terminated
// Vm stays readable while a client polls it, and a deleted Net peering stays
// readable in the `deleted` state — a state the SDK's own StateNames filter
// enumerates.
//
// That is a deliberate fidelity choice, and it has a measured consequence on
// this repository's own evidence: the `behaviour` axis marks an operation whose
// store touches fall on a resource created and DESTROYED inside a span, so
// seven operations whose only subject is one of these two kinds can never earn
// it. They declare that at their route (Route.Unearnable, CauseNoDestruction),
// and this is the control that makes the declaration a measurement instead of a
// sentence: if either family ever starts being removed from the store, this
// test goes red, and the seven declarations become wrong at the same moment.
func TestATerminatedVmAndADeletedPeeringAreKeptRatherThanRemoved(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// The observer is installed after the fixtures are built, so what it
	// records is the deletes and nothing else.
	_, images := post(t, ts, "ReadImages", `{}`)
	imageList, _ := images["Images"].([]any)
	if len(imageList) == 0 {
		t.Fatal("the image catalogue is empty, so no machine can be created here")
	}
	first, _ := imageList[0].(map[string]any)
	imageID, _ := first["ImageId"].(string)

	_, created := post(t, ts, "CreateVms", `{"ImageId":"`+imageID+`","VmType":"tinav6.c1r1p2"}`)
	vms, _ := created["Vms"].([]any)
	if len(vms) != 1 {
		t.Fatalf("CreateVms answered %d machines: %v", len(vms), created)
	}
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	netA, _ := netAndSubnet(t, ts, "10.230.0.0/16", "10.230.1.0/24")
	_, netBDoc := post(t, ts, "CreateNet", `{"IpRange":"10.231.0.0/16"}`)
	nb, _ := netBDoc["Net"].(map[string]any)
	netB, _ := nb["NetId"].(string)
	_, peering := post(t, ts, "CreateNetPeering", `{"SourceNetId":"`+netA+`","AccepterNetId":"`+netB+`"}`)
	pcx, _ := peering["NetPeering"].(map[string]any)
	pcxID, _ := pcx["NetPeeringId"].(string)
	if vmID == "" || pcxID == "" {
		t.Fatalf("the fixtures were not created: vm %q peering %q", vmID, pcxID)
	}

	var events []store.Event
	env.Store.Observe(func(ev store.Event) { events = append(events, ev) })

	if status, body := post(t, ts, "DeleteVms", `{"VmIds":["`+vmID+`"]}`); status != 200 {
		t.Fatalf("DeleteVms answered %d: %v", status, body)
	}
	if status, body := post(t, ts, "DeleteNetPeering", `{"NetPeeringId":"`+pcxID+`"}`); status != 200 {
		t.Fatalf("DeleteNetPeering answered %d: %v", status, body)
	}

	// A witness the observer is really wired: DeleteVms removes the root volume
	// it created, so at least one deletion of some kind must have been seen. A
	// test asserting only "no deletion of these two kinds" passes identically
	// when the observer is not attached at all.
	sawSomeDeletion := false
	var removed []string
	for _, ev := range events {
		if ev.Action != store.EventDeleted {
			continue
		}
		sawSomeDeletion = true
		if ev.Kind == "vm" || ev.Kind == "netpeering" {
			removed = append(removed, ev.Kind+" "+ev.ID)
		}
	}
	if !sawSomeDeletion {
		t.Fatal("the store observed no deletion at all during two deletes; the observer is not measuring")
	}
	sort.Strings(removed)
	if len(removed) > 0 {
		t.Errorf("the store saw %d resource(s) removed that this pack declares it keeps: %s\n"+
			"Route.Unearnable(CauseNoDestruction) is now wrong on the operations whose subject they are.",
			len(removed), strings.Join(removed, ", "))
	}

	// And they are still readable, which is the fidelity half: keeping a record
	// nobody can read would be a leak rather than a behaviour.
	_, states := post(t, ts, "ReadVmsState", `{"AllVms":true}`)
	if !strings.Contains(mustJSON(t, states), vmID) {
		t.Errorf("the terminated machine %s is not readable any more: %v", vmID, states)
	}
	_, peerings := post(t, ts, "ReadNetPeerings", `{"Filters":{"NetPeeringIds":["`+pcxID+`"]}}`)
	if !strings.Contains(mustJSON(t, peerings), pcxID) {
		t.Errorf("the deleted peering %s is not readable any more: %v", pcxID, peerings)
	}
}

// mustJSON renders a decoded body back to text, so a presence check reads the
// whole answer rather than one field somebody guessed the name of.
func mustJSON(t *testing.T, body map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("render the body: %v", err)
	}
	return string(raw)
}
