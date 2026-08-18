package outscale_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The false refusal #299 measured, and the constant that would have denied the
// fix: a NIC this pack itself attached answered 400 "not attached" to the one
// request upstream provides for its attachment (LinkNicToUpdate, osc-sdk-go
// client.gen.go), and storedNicView published DeleteOnVmDeletion:false whatever
// was stored, so even a successful write would have been denied by every read
// that followed. Both halves are driven here, with the non-default value —
// true — because true is precisely what the constant erased.

// linkedNic creates a Net, a Subnet, a Vm and a secondary NIC, attaches the
// NIC, and returns the pieces every test here starts from.
func linkedNic(t *testing.T, ts *httptest.Server, block string) (vmID, nicID, linkNicID string) {
	t.Helper()
	_, out := post(t, ts, "CreateNet", `{"IpRange":"`+block+`.0.0/16"}`)
	net, _ := out["Net"].(map[string]any)
	netID, _ := net["NetId"].(string)
	_, out = post(t, ts, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"`+block+`.1.0/24"}`)
	subnet, _ := out["Subnet"].(map[string]any)
	subnetID, _ := subnet["SubnetId"].(string)

	_, out = post(t, ts, "CreateVms",
		`{"ImageId":"ami-12345678","VmType":"tinav4.c1r1p2","SubnetId":"`+subnetID+`"}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ = vm["VmId"].(string)

	_, out = post(t, ts, "CreateNic", `{"SubnetId":"`+subnetID+`"}`)
	nic, _ := out["Nic"].(map[string]any)
	nicID, _ = nic["NicId"].(string)

	status, out := post(t, ts, "LinkNic",
		`{"NicId":"`+nicID+`","VmId":"`+vmID+`","DeviceNumber":1}`)
	if status != http.StatusOK {
		t.Fatalf("LinkNic: status %d (%v)", status, out)
	}
	linkNicID, _ = out["LinkNicId"].(string)
	return vmID, nicID, linkNicID
}

// readLinkFlag reads DeleteOnVmDeletion back through ReadNics, which is where
// the Terraform provider reads it.
func readLinkFlag(t *testing.T, ts *httptest.Server, nicID string) (bool, bool) {
	t.Helper()
	_, out := post(t, ts, "ReadNics", `{"Filters":{"NicIds":["`+nicID+`"]}}`)
	nics, _ := out["Nics"].([]any)
	if len(nics) == 0 {
		return false, false
	}
	nic, _ := nics[0].(map[string]any)
	link, _ := nic["LinkNic"].(map[string]any)
	if link == nil {
		return false, false
	}
	flag, _ := link["DeleteOnVmDeletion"].(bool)
	return flag, true
}

func TestDeleteOnVmDeletionRoundTripsThroughUpdateNic(t *testing.T) {
	ts, _ := newOutscaleBarrageServer(t)
	_, nicID, linkNicID := linkedNic(t, ts, "10.61")

	// The measured default first: LinkNicRequest carries no flag, so a fresh
	// attachment starts false, and a test that only wrote true could pass on
	// a pack that answers true as a constant.
	if flag, attached := readLinkFlag(t, ts, nicID); !attached || flag {
		t.Fatalf("a fresh attachment must read DeleteOnVmDeletion false (attached=%v flag=%v)", attached, flag)
	}

	status, out := post(t, ts, "UpdateNic",
		`{"NicId":"`+nicID+`","LinkNic":{"LinkNicId":"`+linkNicID+`","DeleteOnVmDeletion":true}}`)
	if status != http.StatusOK {
		t.Fatalf("UpdateNic on an attached NIC answered %d (%v) — the false refusal #299 measured", status, out)
	}
	// The response carries the new value, and so does the read that follows:
	// create-then-read must round-trip, field for field.
	nic, _ := out["Nic"].(map[string]any)
	link, _ := nic["LinkNic"].(map[string]any)
	if flag, _ := link["DeleteOnVmDeletion"].(bool); !flag {
		t.Fatal("the update answered 200 and its own response denies the value")
	}
	if flag, attached := readLinkFlag(t, ts, nicID); !attached || !flag {
		t.Fatalf("the read denies the acknowledged update (attached=%v flag=%v) — the constant #299 names", attached, flag)
	}

	// And back down, because a flag that can only rise is half a field.
	status, _ = post(t, ts, "UpdateNic",
		`{"NicId":"`+nicID+`","LinkNic":{"LinkNicId":"`+linkNicID+`","DeleteOnVmDeletion":false}}`)
	if status != http.StatusOK {
		t.Fatalf("clearing the flag answered %d", status)
	}
	if flag, attached := readLinkFlag(t, ts, nicID); !attached || flag {
		t.Fatalf("the flag did not clear (attached=%v flag=%v)", attached, flag)
	}

	// "You must specify the ID of the NIC attachment": an absent or foreign
	// LinkNicId is refused, not guessed around.
	if status, _ := post(t, ts, "UpdateNic",
		`{"NicId":"`+nicID+`","LinkNic":{"DeleteOnVmDeletion":true}}`); status != http.StatusBadRequest {
		t.Fatalf("an update without the attachment ID answered %d, want 400", status)
	}
}

func TestATrueDeleteOnVmDeletionDeletesTheNicWithItsVm(t *testing.T) {
	ts, _ := newOutscaleBarrageServer(t)
	vmID, doomedNic, doomedLink := linkedNic(t, ts, "10.62")

	// A second interface on the same Vm keeps the default, so one terminate
	// exercises both halves of the SDK's sentence: "If true, the NIC is
	// deleted when the VM is terminated. If false, the NIC is detached."
	_, out := post(t, ts, "ReadNics", `{"Filters":{"NicIds":["`+doomedNic+`"]}}`)
	nics, _ := out["Nics"].([]any)
	nic, _ := nics[0].(map[string]any)
	subnetID, _ := nic["SubnetId"].(string)
	_, out = post(t, ts, "CreateNic", `{"SubnetId":"`+subnetID+`"}`)
	kept, _ := out["Nic"].(map[string]any)
	keptNic, _ := kept["NicId"].(string)
	if status, out := post(t, ts, "LinkNic",
		`{"NicId":"`+keptNic+`","VmId":"`+vmID+`","DeviceNumber":2}`); status != http.StatusOK {
		t.Fatalf("LinkNic (kept): status %d (%v)", status, out)
	}

	if status, out := post(t, ts, "UpdateNic",
		`{"NicId":"`+doomedNic+`","LinkNic":{"LinkNicId":"`+doomedLink+`","DeleteOnVmDeletion":true}}`); status != http.StatusOK {
		t.Fatalf("UpdateNic: status %d (%v)", status, out)
	}

	post(t, ts, "StopVms", `{"VmIds":["`+vmID+`"]}`)
	if status, out := post(t, ts, "DeleteVms", `{"VmIds":["`+vmID+`"]}`); status != http.StatusOK {
		t.Fatalf("DeleteVms: status %d (%v)", status, out)
	}

	_, out = post(t, ts, "ReadNics", `{"Filters":{"NicIds":["`+doomedNic+`"]}}`)
	if gone, _ := out["Nics"].([]any); len(gone) != 0 {
		t.Errorf("a NIC whose attachment says DeleteOnVmDeletion survived its Vm: %v", gone)
	}
	_, out = post(t, ts, "ReadNics", `{"Filters":{"NicIds":["`+keptNic+`"]}}`)
	survivors, _ := out["Nics"].([]any)
	if len(survivors) != 1 {
		t.Fatalf("the default-flag NIC did not survive the terminate: %v", out)
	}
	survivor, _ := survivors[0].(map[string]any)
	if state, _ := survivor["State"].(string); state != "available" {
		t.Errorf("the surviving NIC must be detached, not %q", state)
	}
}
