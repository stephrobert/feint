package outscale_test

import (
	"net/http"
	"testing"
)

// The chain of #389, #383 and #378, asserted one link at a time.
//
//	image du catalogue
//	      | adossée à
//	      v
//	snapshot ReadSnapshots answers for
//	      | dont CreateVms dérive
//	      v
//	root BSU volume ReadVolumes answers for
//	      | lié à
//	      v
//	Vm, whose BlockDeviceMappings publish it
//
// Every assertion below re-reads the object at the far end of the link. That
// is deliberate and it is the lesson of the defect this chain removes: the
// emulator once published a root VolumeId that no read answered for, and the
// Terraform provider found out for us — "volume vol-rooti149 not found" killed
// a whole conformance run. A create's own 200 says nothing about whether the
// identifier it hands back resolves; only the read does.

// readIDs pulls one field from every element of a list in an answer, so an
// assertion can be written about a set of identifiers rather than about a
// count. A count is the shape of measurement this repository has been burnt
// by: it stays right while the elements underneath it are wrong.
func readIDs(body map[string]any, list, field string) []string {
	rows, _ := body[list].([]any)
	out := make([]string, 0, len(rows))
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if id, _ := row[field].(string); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// rootMapping is a Vm's root device mapping, or nil.
func rootMapping(vm map[string]any) (device string, bsu map[string]any) {
	mappings, _ := vm["BlockDeviceMappings"].([]any)
	for _, raw := range mappings {
		mapping, _ := raw.(map[string]any)
		name, _ := mapping["DeviceName"].(string)
		if name != "/dev/sda1" {
			continue
		}
		bsu, _ := mapping["Bsu"].(map[string]any)
		return name, bsu
	}
	return "", nil
}

// Link one: every catalogue image names a snapshot ReadSnapshots answers for.
//
// This is the whole of #389. The mapping was empty for a year because the only
// other way to fill it was to name a SnapshotId nothing held, which is rule 4.
// The assertion is on the set of identifiers the catalogue publishes and the
// set ReadSnapshots answers, never on how many of either there are: a count
// that matches while the ids do not is exactly the reading that would pass on
// the unfixed code.
func TestACatalogueImageNamesASnapshotReadSnapshotsAnswersFor(t *testing.T) {
	ts := newServer(t)

	status, images := post(t, ts, "ReadImages", `{}`)
	if status != http.StatusOK {
		t.Fatalf("ReadImages answered %d", status)
	}
	rows, _ := images["Images"].([]any)
	if len(rows) == 0 {
		t.Fatal("the catalogue answered no image, so this test measures nothing")
	}

	named := 0
	for _, raw := range rows {
		image, _ := raw.(map[string]any)
		id, _ := image["ImageId"].(string)
		mappings, _ := image["BlockDeviceMappings"].([]any)
		if len(mappings) == 0 {
			t.Errorf("%s publishes no device mapping, so a client sizing a root disk from it reads nothing (#383)", id)
			continue
		}
		mapping, _ := mappings[0].(map[string]any)
		bsu, _ := mapping["Bsu"].(map[string]any)
		if bsu == nil {
			t.Errorf("%s: the mapping carries no Bsu", id)
			continue
		}
		snapshotID, _ := bsu["SnapshotId"].(string)
		if snapshotID == "" {
			t.Errorf("%s: the mapping names no snapshot", id)
			continue
		}

		// The link, verified by reading the far end rather than by trusting
		// the near one.
		status, snapshots := post(t, ts, "ReadSnapshots",
			`{"Filters":{"SnapshotIds":["`+snapshotID+`"]}}`)
		if status != http.StatusOK {
			t.Errorf("%s: ReadSnapshots answered %d for %s", id, status, snapshotID)
			continue
		}
		if got := readIDs(snapshots, "Snapshots", "SnapshotId"); !has(got, snapshotID) {
			t.Errorf("%s names the snapshot %s and ReadSnapshots answers %v: the identifier is decorative", id, snapshotID, got)
			continue
		}
		named++
	}
	if named != len(rows) {
		t.Errorf("%d of %d catalogue images name a snapshot that exists", named, len(rows))
	}
}

// Link two and three: a machine is born with a root volume cut from its
// image's snapshot, ReadVolumes answers for it, and ReadVms publishes it.
//
// This is #378's remaining third. The three reads below are the acceptance
// criteria of the issue, in the order the issue states them, and each is a
// read of the object the previous answer named.
func TestAMachineIsBornWithARootVolumeItsImageNames(t *testing.T) {
	ts := newServer(t)

	_, images := post(t, ts, "ReadImages", `{}`)
	first, _ := images["Images"].([]any)[0].(map[string]any)
	imageID, _ := first["ImageId"].(string)
	imageMappings, _ := first["BlockDeviceMappings"].([]any)
	imageMapping, _ := imageMappings[0].(map[string]any)
	imageBsu, _ := imageMapping["Bsu"].(map[string]any)
	wantSnapshot, _ := imageBsu["SnapshotId"].(string)

	status, created := post(t, ts, "CreateVms", `{"ImageId":"`+imageID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVms answered %d: %v", status, created)
	}
	vms, _ := created["Vms"].([]any)
	if len(vms) != 1 {
		t.Fatalf("CreateVms answered %d machines", len(vms))
	}
	vmID, _ := vms[0].(map[string]any)["VmId"].(string)

	// The create's own answer already has to carry it: #378 measured the
	// omission on CreateVms as well as on ReadVms.
	if _, bsu := rootMapping(vms[0].(map[string]any)); bsu == nil {
		t.Fatalf("CreateVms answered no root device mapping: %v", vms[0])
	}

	// ReadVms -> the root mapping is not empty.
	status, read := post(t, ts, "ReadVms", `{"Filters":{"VmIds":["`+vmID+`"]}}`)
	if status != http.StatusOK {
		t.Fatalf("ReadVms answered %d", status)
	}
	readVms, _ := read["Vms"].([]any)
	if len(readVms) != 1 {
		t.Fatalf("ReadVms answered %d machines for %s", len(readVms), vmID)
	}
	vm, _ := readVms[0].(map[string]any)
	device, bsu := rootMapping(vm)
	if bsu == nil {
		t.Fatalf("ReadVms answers no root device on %s, so a client cannot find the volume it must not delete (#378)", vmID)
	}
	if device != "/dev/sda1" {
		t.Errorf("the root device is %q", device)
	}
	if del, _ := bsu["DeleteOnVmDeletion"].(bool); !del {
		t.Error("the root device answers DeleteOnVmDeletion false; the recorded account answers true on every read of its machine's life")
	}
	volumeID, _ := bsu["VolumeId"].(string)
	if volumeID == "" {
		t.Fatal("the root mapping names no volume")
	}

	// ReadVolumes -> the VolumeId of that mapping exists.
	status, volumes := post(t, ts, "ReadVolumes", `{"Filters":{"VolumeIds":["`+volumeID+`"]}}`)
	if status != http.StatusOK {
		t.Fatalf("ReadVolumes answered %d", status)
	}
	got := readIDs(volumes, "Volumes", "VolumeId")
	if !has(got, volumeID) {
		t.Fatalf("the machine names the root volume %s and ReadVolumes answers %v: the identifier is decorative, and this is the shape that killed a conformance run", volumeID, got)
	}

	// And the volume's own provenance names the image's snapshot, which is
	// what makes this one chain rather than two coincidences.
	rows, _ := volumes["Volumes"].([]any)
	volume, _ := rows[0].(map[string]any)
	if got, _ := volume["SnapshotId"].(string); got != wantSnapshot {
		t.Errorf("the root volume was cut from %q where the image names %q", got, wantSnapshot)
	}
	if size, _ := volume["Size"].(float64); int(size) == 0 {
		t.Error("the root volume has no size, so a client sizing a disk from the machine reads zero")
	}

	// ReadSnapshots -> the SnapshotId of the image exists. Read again from the
	// volume's side, because that is the identifier a client following the
	// chain downward actually holds.
	status, snapshots := post(t, ts, "ReadSnapshots", `{"Filters":{"SnapshotIds":["`+wantSnapshot+`"]}}`)
	if status != http.StatusOK {
		t.Fatalf("ReadSnapshots answered %d", status)
	}
	if got := readIDs(snapshots, "Snapshots", "SnapshotId"); !has(got, wantSnapshot) {
		t.Fatalf("the root volume names the snapshot %s and ReadSnapshots answers %v", wantSnapshot, got)
	}
}

// Terminating a machine destroys the root volume and frees the rest, each by
// its own DeleteOnVmDeletion.
//
// Measured as a difference rather than as a total: the set of volume
// identifiers before the terminate, and the set after. An assertion on how
// many volumes the store holds would be an assertion about everything else
// running in the process.
func TestTerminatingAVmDeletesItsRootVolumeAndFreesTheRest(t *testing.T) {
	ts := newServer(t)

	_, images := post(t, ts, "ReadImages", `{}`)
	first, _ := images["Images"].([]any)[0].(map[string]any)
	imageID, _ := first["ImageId"].(string)

	_, created := post(t, ts, "CreateVms", `{"ImageId":"`+imageID+`"}`)
	vms, _ := created["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)
	_, rootBsu := rootMapping(vm)
	rootID, _ := rootBsu["VolumeId"].(string)

	// A volume the client linked itself, which must survive: upstream's
	// LinkVolume declares no DeleteOnVmDeletion, so what a client attaches is
	// never destroyed with the machine.
	_, madeVolume := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`)
	volume, _ := madeVolume["Volume"].(map[string]any)
	linkedID, _ := volume["VolumeId"].(string)
	if status, out := post(t, ts, "LinkVolume",
		`{"VolumeId":"`+linkedID+`","VmId":"`+vmID+`","DeviceName":"/dev/xvdc"}`); status != http.StatusOK {
		t.Fatalf("LinkVolume answered %d: %v", status, out)
	}

	if status, out := post(t, ts, "DeleteVms", `{"VmIds":["`+vmID+`"]}`); status != http.StatusOK {
		t.Fatalf("DeleteVms answered %d: %v", status, out)
	}

	_, after := post(t, ts, "ReadVolumes", `{}`)
	ids := readIDs(after, "Volumes", "VolumeId")
	if has(ids, rootID) {
		t.Errorf("the root volume %s outlived its machine although the API published DeleteOnVmDeletion true on it", rootID)
	}
	if !has(ids, linkedID) {
		t.Errorf("the volume the client linked (%s) was destroyed with the machine, although its link published DeleteOnVmDeletion false; ReadVolumes answers %v", linkedID, ids)
	}

	// And the survivor is usable again, which is the whole reason a terminate
	// has to release what it held (#215).
	_, freed := post(t, ts, "ReadVolumes", `{"Filters":{"VolumeIds":["`+linkedID+`"]}}`)
	rows, _ := freed["Volumes"].([]any)
	survivor, _ := rows[0].(map[string]any)
	if state, _ := survivor["State"].(string); state != "available" {
		t.Errorf("the freed volume is %q, so LinkVolume will refuse to attach it anywhere else", state)
	}

	// The terminated machine answers an empty mapping, which is what the real
	// account answered in the two ReadVms that follow its DeleteVms
	// (corpus/outscale/oapi-cli-lifecycle.jsonl).
	_, readBack := post(t, ts, "ReadVms", `{"Filters":{"VmIds":["`+vmID+`"]}}`)
	terminated, _ := readBack["Vms"].([]any)
	if len(terminated) == 1 {
		if _, bsu := rootMapping(terminated[0].(map[string]any)); bsu != nil {
			t.Error("a terminated machine still publishes a root device, naming a volume no read answers for")
		}
	}
}

// A volume may be cut from a catalogue snapshot, which is what makes the
// catalogue's snapshots real rather than decorative: a client that can read one
// must be able to use it.
func TestAVolumeIsCutFromACatalogueSnapshot(t *testing.T) {
	ts := newServer(t)

	_, images := post(t, ts, "ReadImages", `{}`)
	first, _ := images["Images"].([]any)[0].(map[string]any)
	mappings, _ := first["BlockDeviceMappings"].([]any)
	mapping, _ := mappings[0].(map[string]any)
	bsu, _ := mapping["Bsu"].(map[string]any)
	snapshotID, _ := bsu["SnapshotId"].(string)

	status, out := post(t, ts, "CreateVolume",
		`{"SubregionName":"eu-west-2a","SnapshotId":"`+snapshotID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVolume refused a snapshot ReadSnapshots answers for: %d %v", status, out)
	}
	volume, _ := out["Volume"].(map[string]any)
	if got, _ := volume["SnapshotId"].(string); got != snapshotID {
		t.Errorf("the volume records its provenance as %q", got)
	}
	if size, _ := volume["Size"].(float64); int(size) == 0 {
		t.Error("the volume did not inherit the snapshot's size")
	}
}

// A catalogue snapshot refuses its delete the way a catalogue image does, and
// the refusal has to be the RIGHT one.
//
// The first version of this test asserted only that the delete was not a 200,
// and a falsification proved it vacuous: with the guard removed the call falls
// through to notFound, which is also not a 200, so the mutation stayed green.
// The snapshot survives either way — nothing in the store answers to that id,
// so Store.Delete removes nothing — and "it is still there afterwards" is
// therefore structural rather than earned.
//
// What the guard actually buys is the shape, and the shape is the whole point:
// notFound answers 400 "the snapshot snap-… does not exist" about an object
// ReadSnapshots answers for one call earlier. That is the API contradicting
// itself between two operations, which is #269's defect in another family, and
// it is worse than a wrong status code — a client is told an identifier it can
// read is not real.
func TestACatalogueSnapshotRefusesItsDelete(t *testing.T) {
	ts := newServer(t)

	_, images := post(t, ts, "ReadImages", `{}`)
	first, _ := images["Images"].([]any)[0].(map[string]any)
	mappings, _ := first["BlockDeviceMappings"].([]any)
	mapping, _ := mappings[0].(map[string]any)
	bsu, _ := mapping["Bsu"].(map[string]any)
	snapshotID, _ := bsu["SnapshotId"].(string)

	// The premise: this operation answers for the id. Without it the assertion
	// below would be about an object nothing serves.
	_, before := post(t, ts, "ReadSnapshots", `{"Filters":{"SnapshotIds":["`+snapshotID+`"]}}`)
	if got := readIDs(before, "Snapshots", "SnapshotId"); !has(got, snapshotID) {
		t.Fatalf("ReadSnapshots does not answer for %s, so this test measures nothing", snapshotID)
	}

	status, out := post(t, ts, "DeleteSnapshot", `{"SnapshotId":"`+snapshotID+`"}`)
	if status == http.StatusOK {
		t.Fatalf("DeleteSnapshot destroyed a catalogue snapshot: %v", out)
	}
	if status != http.StatusConflict {
		t.Errorf("DeleteSnapshot answered %d for %s, where ReadSnapshots answers for it: the refusal has to say the object belongs to the catalogue, not that it does not exist", status, snapshotID)
	}
	if code := errorTypeOf(out); code != "ResourceConflict" {
		t.Errorf("the refusal is typed %q; a client branching on the type is told this snapshot is not real", code)
	}

	// And it is still there afterwards, which the status code alone does not say.
	_, after := post(t, ts, "ReadSnapshots", `{"Filters":{"SnapshotIds":["`+snapshotID+`"]}}`)
	if got := readIDs(after, "Snapshots", "SnapshotId"); !has(got, snapshotID) {
		t.Fatalf("the catalogue snapshot %s is gone after a refused delete; ReadSnapshots answers %v", snapshotID, got)
	}
}

// An identifier that names no image still yields a machine with a root volume.
//
// The 200 is #392's standing decision — an unknown ImageId is accepted here,
// docs/limits.md says so and corpus/accepted.json carries what it costs — and
// this test exists so that giving machines a disk does not quietly turn that
// 200 into a 404 from inside another issue.
func TestAnUnknownImageStillYieldsARootVolume(t *testing.T) {
	ts := newServer(t)

	status, created := post(t, ts, "CreateVms", `{"ImageId":"ami-deadbeef"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVms answered %d for an unknown image, changing #392's decision: %v", status, created)
	}
	vms, _ := created["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	_, bsu := rootMapping(vm)
	if bsu == nil {
		t.Fatal("a machine created from an unknown image has no root device")
	}
	volumeID, _ := bsu["VolumeId"].(string)
	_, volumes := post(t, ts, "ReadVolumes", `{"Filters":{"VolumeIds":["`+volumeID+`"]}}`)
	if got := readIDs(volumes, "Volumes", "VolumeId"); !has(got, volumeID) {
		t.Fatalf("the machine names %s and ReadVolumes answers %v", volumeID, got)
	}

	// And it is the DEFAULT CATALOGUE ENTRY's disk, which is what #392's
	// decision actually says: an unknown identifier resolves to the default
	// entry rather than to nothing. A falsification proved the earlier version
	// of this test blind to that — it only asked whether some volume existed,
	// so resolving the image to nothing at all left it green.
	_, catalogue := post(t, ts, "ReadImages", `{"Filters":{"ImageIds":["ami-00000001"]}}`)
	def, _ := catalogue["Images"].([]any)[0].(map[string]any)
	defMappings, _ := def["BlockDeviceMappings"].([]any)
	defMapping, _ := defMappings[0].(map[string]any)
	defBsu, _ := defMapping["Bsu"].(map[string]any)
	wantSnapshot, _ := defBsu["SnapshotId"].(string)

	rows, _ := volumes["Volumes"].([]any)
	volume, _ := rows[0].(map[string]any)
	if got, _ := volume["SnapshotId"].(string); got != wantSnapshot {
		t.Fatalf("the machine's disk was cut from %q where the default catalogue entry names %q: an unknown image resolves to the default entry (#392), so its machine gets that entry's disk", got, wantSnapshot)
	}
	_, snapshots := post(t, ts, "ReadSnapshots", `{"Filters":{"SnapshotIds":["`+wantSnapshot+`"]}}`)
	if got := readIDs(snapshots, "Snapshots", "SnapshotId"); !has(got, wantSnapshot) {
		t.Fatalf("the root volume was cut from %s and ReadSnapshots answers %v", wantSnapshot, got)
	}
}

// The root volume answers the link filter the Terraform provider polls, with
// the value the machine publishes.
//
// LinkVolumeDeleteOnVmDeletion was matched against a hardcoded false, which was
// true only while no volume could carry anything else. A filter that answers a
// constant tells a client every volume survives its machine.
func TestARootVolumeAnswersItsDeleteOnVmDeletionFilter(t *testing.T) {
	ts := newServer(t)

	_, images := post(t, ts, "ReadImages", `{}`)
	first, _ := images["Images"].([]any)[0].(map[string]any)
	imageID, _ := first["ImageId"].(string)
	_, created := post(t, ts, "CreateVms", `{"ImageId":"`+imageID+`"}`)
	vms, _ := created["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	_, bsu := rootMapping(vm)
	rootID, _ := bsu["VolumeId"].(string)

	_, kept := post(t, ts, "ReadVolumes", `{"Filters":{"LinkVolumeDeleteOnVmDeletion":true}}`)
	if got := readIDs(kept, "Volumes", "VolumeId"); !has(got, rootID) {
		t.Errorf("the root volume %s does not answer the filter for DeleteOnVmDeletion true; ReadVolumes answers %v", rootID, got)
	}
	_, others := post(t, ts, "ReadVolumes", `{"Filters":{"LinkVolumeDeleteOnVmDeletion":false}}`)
	if got := readIDs(others, "Volumes", "VolumeId"); has(got, rootID) {
		t.Errorf("the root volume %s answers the filter for DeleteOnVmDeletion false as well, so the filter selects nothing", rootID)
	}
}

// CreateImage refuses an Iops it cannot honour, which is what keeps the
// decline in declined_fields.go true for every image ReadImages can answer.
//
// Without the refusal the field is served on one kind of object and declined
// for the operation that answers both, which is #389 exactly: score.sh fails
// the terraform, opentofu, oapi-cli and fields legs at once.
func TestCreateImageRefusesAnIopsItCannotHonour(t *testing.T) {
	ts := newServer(t)

	_, images := post(t, ts, "ReadImages", `{}`)
	first, _ := images["Images"].([]any)[0].(map[string]any)
	mappings, _ := first["BlockDeviceMappings"].([]any)
	mapping, _ := mappings[0].(map[string]any)
	bsu, _ := mapping["Bsu"].(map[string]any)
	snapshotID, _ := bsu["SnapshotId"].(string)

	status, out := post(t, ts, "CreateImage",
		`{"ImageName":"feint-iops","BlockDeviceMappings":[{"DeviceName":"/dev/sda1","Bsu":{"SnapshotId":"`+snapshotID+`","Iops":3000}}]}`)
	if status == http.StatusOK {
		t.Fatalf("CreateImage stored an Iops on a standard root device: %v", out)
	}

	// And the same call without it is accepted, so the refusal is about the
	// field rather than about the request.
	status, made := post(t, ts, "CreateImage",
		`{"ImageName":"feint-no-iops","BlockDeviceMappings":[{"DeviceName":"/dev/sda1","Bsu":{"SnapshotId":"`+snapshotID+`"}}]}`)
	if status != http.StatusOK {
		t.Fatalf("CreateImage refused a mapping with no Iops: %d %v", status, made)
	}
	image, _ := made["Image"].(map[string]any)
	made0, _ := image["BlockDeviceMappings"].([]any)
	madeMapping, _ := made0[0].(map[string]any)
	madeBsu, _ := madeMapping["Bsu"].(map[string]any)
	if _, present := madeBsu["Iops"]; present {
		t.Error("a registered image carries Iops, so the ReadImages decline is fiction for one of the two kinds of object it answers")
	}
}

// A create that fails partway leaves no root volume behind.
//
// The batch undo already removed the machines it had stored — an error answer
// that leaves resources running is a client owning things it was told it never
// got. The root volume is one more object on the same path, and left behind it
// would be worse than a leak: nothing answers to its LinkedVmId, so the orphan
// sweep reports it for ever and LinkVolume refuses to attach it anywhere else.
//
// The failure has to happen AFTER a machine has been stored, or the undo loop
// never runs and this test measures nothing. The first version of it named a
// Subnet that does not exist, which fails on the batch's first machine, so it
// passed with the fix removed — a vacuous test, and the exact shape this
// repository keeps paying for. What is used instead is address exhaustion: a
// /29 subnet holds three usable addresses, so two machines are placed and a
// batch of three then fails on its second, with its first already in the store.
//
// Measured as a difference — the volumes before and the volumes after — because
// a total would be an assertion about everything else the process is doing.
func TestAFailedBatchLeavesNoRootVolume(t *testing.T) {
	ts := newServer(t)

	_, net := post(t, ts, "CreateNet", `{"IpRange":"10.77.0.0/16"}`)
	netID, _ := net["Net"].(map[string]any)["NetId"].(string)
	_, sn := post(t, ts, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"10.77.1.0/29"}`)
	subnetID, _ := sn["Subnet"].(map[string]any)["SubnetId"].(string)

	// Two of the three addresses taken, so the batch below can place one more
	// machine and no second.
	if status, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","MaxVmsCount":2,"SubnetId":"`+subnetID+`"}`); status != http.StatusOK {
		t.Fatalf("the setup create answered %d: %v", status, out)
	}

	_, before := post(t, ts, "ReadVolumes", `{}`)
	was := readIDs(before, "Volumes", "VolumeId")

	status, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-00000001","MaxVmsCount":3,"SubnetId":"`+subnetID+`"}`)
	if status == http.StatusOK {
		t.Fatalf("CreateVms placed three machines in a subnet holding one free address: %v", out)
	}

	_, after := post(t, ts, "ReadVolumes", `{}`)
	left := readIDs(after, "Volumes", "VolumeId")
	for _, id := range left {
		if !has(was, id) {
			t.Errorf("the refused batch left the volume %s behind, linked to a machine the client was told it never got", id)
		}
	}
	if len(left) != len(was) {
		t.Errorf("the volumes went from %v to %v across a create that refused", was, left)
	}
}

// A machine created from an image that names no snapshot gets a disk with a
// size and no provenance — never a borrowed one.
//
// This is the fallback path, and it is reached by an image a client registered
// from a VmId: createImage stores an empty mapping there, so nothing tells the
// machine what its root device was cut from. The honest answer is a volume with
// a size and no SnapshotId key, which is exactly what createVolume answers for
// a volume with no provenance ("absent and empty are not the same claim").
//
// The alternative, and the reason this test exists rather than a comment: an
// earlier version borrowed the catalogue's snapshot for this case. That reads
// as a chain and is a false relation — this machine's disk did not come from
// Ubuntu — which is the same defect as an invented identifier, one step
// subtler because the identifier resolves.
func TestAMachineFromAnImageWithNoMappingGetsAnUnsourcedRootVolume(t *testing.T) {
	ts := newServer(t)

	// A machine to cut the image from, which is the only way to reach an image
	// with no device mapping.
	_, madeVM := post(t, ts, "CreateVms", `{"ImageId":"ami-00000001"}`)
	sourceVM, _ := madeVM["Vms"].([]any)[0].(map[string]any)
	sourceID, _ := sourceVM["VmId"].(string)

	status, madeImage := post(t, ts, "CreateImage",
		`{"ImageName":"feint-from-a-machine","VmId":"`+sourceID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateImage from a VmId answered %d: %v", status, madeImage)
	}
	image, _ := madeImage["Image"].(map[string]any)
	imageID, _ := image["ImageId"].(string)
	if mappings, _ := image["BlockDeviceMappings"].([]any); len(mappings) != 0 {
		t.Skip("an image cut from a machine now carries a mapping, so this fallback is unreachable and the test would measure nothing")
	}

	status, created := post(t, ts, "CreateVms", `{"ImageId":"`+imageID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVms from an image with no mapping answered %d: %v", status, created)
	}
	vm, _ := created["Vms"].([]any)[0].(map[string]any)
	_, bsu := rootMapping(vm)
	if bsu == nil {
		t.Fatal("a machine from an image with no mapping has no root device, which is the omission #378 measured")
	}
	volumeID, _ := bsu["VolumeId"].(string)

	_, volumes := post(t, ts, "ReadVolumes", `{"Filters":{"VolumeIds":["`+volumeID+`"]}}`)
	if got := readIDs(volumes, "Volumes", "VolumeId"); !has(got, volumeID) {
		t.Fatalf("the machine names %s and ReadVolumes answers %v", volumeID, got)
	}
	rows, _ := volumes["Volumes"].([]any)
	volume, _ := rows[0].(map[string]any)
	if size, _ := volume["Size"].(float64); int(size) == 0 {
		t.Error("the root volume has no size, so a client sizing a disk from the machine reads zero")
	}
	if got, present := volume["SnapshotId"]; present {
		t.Errorf("the root volume claims to have been cut from %v, where its image names no snapshot: a relation that resolves and is false", got)
	}
}
