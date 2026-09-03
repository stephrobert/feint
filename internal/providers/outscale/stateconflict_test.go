package outscale_test

import (
	"net/http"
	"strings"
	"testing"
)

// conflictCode reads the Code of the first error, empty when the body carries
// no Errors array at all — which is the finding #621 reports beside the status:
// "absent: Errors".
func conflictCode(body map[string]any) string {
	errs, _ := body["Errors"].([]any)
	if len(errs) == 0 {
		return ""
	}
	first, _ := errs[0].(map[string]any)
	code, _ := first["Code"].(string)
	return code
}

// StartVms refuses a machine that is already running (#621).
//
// Recorded against a real account on 2026-08-30: 409 ResourceConflict, where
// this pack answered 200 and echoed the machine's own state back. The other
// divergences that recording found make the emulator answer LESS than the
// cloud; this one made it answer yes where the cloud says no, which is the
// direction that turns a green run into a false promise.
func TestStartVmsRefusesAMachineAlreadyRunning(t *testing.T) {
	ts := newServer(t)

	status, created := post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","VmType":"tinav4.c1r1p2"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVms: %d (%v)", status, created)
	}
	vms, _ := created["Vms"].([]any)
	first, _ := vms[0].(map[string]any)
	id, _ := first["VmId"].(string)

	// A created machine is already running here, as it is upstream once its
	// `pending` is over, so the sequence a client drives is stop then start.
	if status, body := post(t, ts, "StopVms", `{"VmIds":["`+id+`"]}`); status != http.StatusOK {
		t.Fatalf("StopVms answered %d (%v)", status, body)
	}
	// The accepting half: a stopped machine starts, and the refusal below is
	// worth nothing without it.
	if status, body := post(t, ts, "StartVms", `{"VmIds":["`+id+`"]}`); status != http.StatusOK {
		t.Fatalf("StartVms on a stopped machine answered %d (%v)", status, body)
	}
	status, refused := post(t, ts, "StartVms", `{"VmIds":["`+id+`"]}`)
	if status != http.StatusConflict {
		t.Errorf("StartVms on a running machine answered %d, want 409: %v", status, refused)
	}
	// The status alone is half the finding. "absent: Errors" is the other half,
	// and a 409 with no Errors array is a refusal no client can read.
	if code := conflictCode(refused); code == "" {
		t.Errorf("the refusal carries no Errors array: %v", refused)
	} else if !strings.HasPrefix(code, "9") {
		t.Errorf("the refusal carries Code %q; osc.IsConflict reads 6000-6999 and 9000-9999", code)
	}
}

// UnlinkVolume refuses a volume attached to nothing (#621).
//
// Recorded the same day, on a volume that had never successfully attached: 409
// where this pack answered 200 and told the client a detach had happened.
func TestUnlinkVolumeRefusesAVolumeAttachedToNothing(t *testing.T) {
	ts := newServer(t)

	status, made := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVolume: %d (%v)", status, made)
	}
	volume, _ := made["Volume"].(map[string]any)
	id, _ := volume["VolumeId"].(string)

	status, refused := post(t, ts, "UnlinkVolume", `{"VolumeId":"`+id+`"}`)
	if status != http.StatusConflict {
		t.Errorf("UnlinkVolume on a free volume answered %d, want 409: %v", status, refused)
	}
	if code := conflictCode(refused); code == "" {
		t.Errorf("the refusal carries no Errors array: %v", refused)
	}
}

// LinkVolume refuses a volume already linked elsewhere, which this pack has
// always done (#621 lists it as reachable, and it was already reached).
//
// Held here rather than assumed: #621 names three invariants the emulator
// could serve, and a reader of that issue has to be able to tell which two were
// added from the one that was already true. An accepting half beside it, so a
// guard that refused every link would not pass.
func TestLinkVolumeStillRefusesAVolumeHeldElsewhere(t *testing.T) {
	ts := newServer(t)

	newVM := func() string {
		status, created := post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","VmType":"tinav4.c1r1p2"}`)
		if status != http.StatusOK {
			t.Fatalf("CreateVms: %d (%v)", status, created)
		}
		vms, _ := created["Vms"].([]any)
		first, _ := vms[0].(map[string]any)
		id, _ := first["VmId"].(string)
		return id
	}
	one, two := newVM(), newVM()

	status, made := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`)
	if status != http.StatusOK {
		t.Fatalf("CreateVolume: %d (%v)", status, made)
	}
	volume, _ := made["Volume"].(map[string]any)
	id, _ := volume["VolumeId"].(string)

	// The accepting half: a free volume links.
	if status, body := post(t, ts, "LinkVolume",
		`{"VolumeId":"`+id+`","VmId":"`+one+`","DeviceName":"/dev/xvdb"}`); status != http.StatusOK {
		t.Fatalf("linking a free volume answered %d (%v)", status, body)
	}
	status, refused := post(t, ts, "LinkVolume",
		`{"VolumeId":"`+id+`","VmId":"`+two+`","DeviceName":"/dev/xvdb"}`)
	if status != http.StatusConflict {
		t.Errorf("LinkVolume on a held volume answered %d, want 409: %v", status, refused)
	}
	// And the detach that follows a real attach still works: the new refusal
	// must not have made every UnlinkVolume a 409.
	if status, body := post(t, ts, "UnlinkVolume", `{"VolumeId":"`+id+`"}`); status != http.StatusOK {
		t.Errorf("detaching a linked volume answered %d (%v), and that is the ordinary path", status, body)
	}
}
