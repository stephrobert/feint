package outscale_test

import (
	"net/http"
	"testing"
)

// The secondary-interface lifecycle, shaped by the recorded run of 2026-08-08
// (values invented, shapes measured): CreateNic answers a NIC with no LinkNic
// key, LinkNic answers {LinkNicId, ResponseContext} alone, and the attached
// interface's LinkNic carries DeviceNumber from 1, State "in-use".
func TestANicLifecycleMatchesTheRecordedShapes(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	netID, subnetID := netAndSubnet(t, ts, "10.40.0.0/16", "10.40.1.0/24")
	_ = netID

	created := call(t, ts, doc, "CreateNic", `{"SubnetId":"`+subnetID+`","Description":"spare"}`)
	nic, _ := created["Nic"].(map[string]any)
	nicID, _ := nic["NicId"].(string)
	// A fresh interface is unattached: no LinkNic key, measured.
	if _, present := nic["LinkNic"]; present {
		t.Fatalf("a fresh NIC carries a LinkNic key the real cloud omits: %v", nic)
	}
	if nic["State"] != "available" {
		t.Fatalf("a fresh NIC is not available: %v", nic)
	}
	// It carries the Net's default group, like a machine does.
	if groups, _ := nic["SecurityGroups"].([]any); len(groups) != 1 {
		t.Fatalf("the NIC does not carry the default group: %v", nic["SecurityGroups"])
	}

	// A machine in the same Subnet, to attach it to.
	vmCreated := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	vm, _ := vmCreated["Vms"].([]any)[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)

	// Device 0 is the primary and refused for a secondary.
	if status, _ := post(t, ts, "LinkNic", `{"NicId":"`+nicID+`","VmId":"`+vmID+`","DeviceNumber":0}`); status != http.StatusBadRequest {
		t.Fatalf("device 0 was accepted for a secondary interface")
	}

	link := call(t, ts, doc, "LinkNic", `{"NicId":"`+nicID+`","VmId":"`+vmID+`","DeviceNumber":1}`)
	linkID, _ := link["LinkNicId"].(string)
	if linkID == "" {
		t.Fatalf("LinkNic did not answer a LinkNicId: %v", link)
	}
	if _, present := link["Nic"]; present {
		t.Fatalf("LinkNic answered a Nic the recording does not: %v", link)
	}

	// The attached interface now carries the measured LinkNic.
	attached := firstOf(t, call(t, ts, doc, "ReadNics", `{"Filters":{"NicIds":["`+nicID+`"]}}`), "Nics")
	linkNic, _ := attached["LinkNic"].(map[string]any)
	if linkNic["VmId"] != vmID {
		t.Fatalf("the attached NIC does not name its machine: %v", linkNic)
	}
	if dn, _ := linkNic["DeviceNumber"].(float64); dn != 1 {
		t.Fatalf("DeviceNumber = %v, want 1", linkNic["DeviceNumber"])
	}
	if linkNic["State"] != "in-use" {
		t.Fatalf("the attach state is not in-use: %v", linkNic)
	}

	// It can be found by the machine it is attached to.
	byVM := call(t, ts, doc, "ReadNics", `{"Filters":{"LinkNicVmIds":["`+vmID+`"]}}`)
	if list, _ := byVM["Nics"].([]any); len(list) != 2 {
		// The primary (device 0) and this secondary (device 1).
		t.Fatalf("the machine should have two interfaces: %v", len(list))
	}

	// A second interface at the same device number is a conflict.
	other := call(t, ts, doc, "CreateNic", `{"SubnetId":"`+subnetID+`"}`)
	otherID, _ := other["Nic"].(map[string]any)["NicId"].(string)
	if status, _ := post(t, ts, "LinkNic", `{"NicId":"`+otherID+`","VmId":"`+vmID+`","DeviceNumber":1}`); status != http.StatusConflict {
		t.Fatalf("device number 1 was used twice on one machine")
	}

	// An attached interface does not delete; a detached one does.
	if status, _ := post(t, ts, "DeleteNic", `{"NicId":"`+nicID+`"}`); status != http.StatusConflict {
		t.Fatalf("an attached NIC was deleted")
	}
	call(t, ts, doc, "UnlinkNic", `{"LinkNicId":"`+linkID+`"}`)
	detached := firstOf(t, call(t, ts, doc, "ReadNics", `{"Filters":{"NicIds":["`+nicID+`"]}}`), "Nics")
	if _, present := detached["LinkNic"]; present {
		t.Fatalf("the detached NIC still carries a LinkNic: %v", detached)
	}
	call(t, ts, doc, "DeleteNic", `{"NicId":"`+nicID+`"}`)
	call(t, ts, doc, "DeleteNic", `{"NicId":"`+otherID+`"}`)
}

// Deleting a machine detaches its secondary interfaces rather than taking them
// with it (DeleteOnVmDeletion false, measured), so a NIC never names a
// terminated Vm.
func TestDeletingAVmDetachesItsSecondaryNics(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	_, subnetID := netAndSubnet(t, ts, "10.41.0.0/16", "10.41.1.0/24")
	nicID, _ := call(t, ts, doc, "CreateNic", `{"SubnetId":"`+subnetID+`"}`)["Nic"].(map[string]any)["NicId"].(string)
	vm, _ := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2","SubnetId":"`+subnetID+`","BootOnCreation":false}`)["Vms"].([]any)[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)
	call(t, ts, doc, "LinkNic", `{"NicId":"`+nicID+`","VmId":"`+vmID+`","DeviceNumber":1}`)

	call(t, ts, doc, "DeleteVms", `{"VmIds":["`+vmID+`"]}`)

	nic := firstOf(t, call(t, ts, doc, "ReadNics", `{"Filters":{"NicIds":["`+nicID+`"]}}`), "Nics")
	if _, present := nic["LinkNic"]; present {
		t.Fatalf("the interface still names the terminated machine: %v", nic)
	}
	if nic["State"] != "available" {
		t.Fatalf("the detached interface is not available again: %v", nic)
	}
	call(t, ts, doc, "DeleteNic", `{"NicId":"`+nicID+`"}`)
}
