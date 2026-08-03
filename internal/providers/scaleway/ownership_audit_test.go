package scaleway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// What a second whole-pack audit found in the fixes of the first one. Every
// defect here was introduced by a correction, which is why they are together:
// the lesson is not "volumes are hard", it is that a fix which invents a value
// or writes a fact in a second place is a fix that costs more than the defect.

const zone = "/instance/v1/zones/fr-par-1"

func aVolume(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	_, out := do(t, ts, "POST", zone+"/volumes",
		`{"name":"`+name+`","volume_type":"b_ssd","size":10000000000}`)
	vol, _ := out["volume"].(map[string]any)
	id, _ := vol["id"].(string)
	if id == "" {
		t.Fatalf("no volume created: %v", out)
	}
	return id
}

func aServer(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	_, out := do(t, ts, "POST", zone+"/servers",
		`{"name":"`+name+`","commercial_type":"DEV1-S","image":"ubuntu_jammy"}`)
	srv, _ := out["server"].(map[string]any)
	id, _ := srv["id"].(string)
	if id == "" {
		t.Fatalf("no server created: %v", out)
	}
	return id
}

// A volume's state is one the SDK declares, whatever it is attached to.
//
// The previous wave answered "in_use" for an attached volume. `VolumeState` in
// instance_sdk.go has eight values and that is not one of them, and
// WaitForVolume treats only available and error as terminal: `tofu apply` with
// additional_volume_ids timed out after five minutes, `tofu destroy` after ten,
// and the volume became undeletable by Terraform — measurably worse than the
// defect the fix replaced. Rule 4, inside a fix that cited rule 4.
func TestAVolumeStateIsOneTheSDKDeclares(t *testing.T) {
	// The SDK's own enum, copied from instance_sdk.go. A state outside it is a
	// state no official client knows how to wait on.
	declared := map[string]bool{
		"available": true, "snapshotting": true, "fetching": true, "saving": true,
		"attaching": true, "resizing": true, "hotsyncing": true, "error": true,
	}

	ts := newTestServer(t)
	volID := aVolume(t, ts, "data")
	srvID := aServer(t, ts, "host")

	if status, out := do(t, ts, "POST", zone+"/servers/"+srvID+"/attach-volume",
		`{"volume_id":"`+volID+`"}`); status != http.StatusOK {
		t.Fatalf("attach-volume: %d %v", status, out)
	}

	_, out := do(t, ts, "GET", zone+"/volumes/"+volID, "")
	vol, _ := out["volume"].(map[string]any)
	state, _ := vol["state"].(string)
	if !declared[state] {
		t.Errorf("an attached volume answers state %q, which the SDK does not declare", state)
	}
	// And it does say it is attached — a check that only refused states would
	// pass with the server field dropped entirely.
	if server, _ := vol["server"].(map[string]any); server == nil || server["id"] != srvID {
		t.Errorf("the attached volume names %v, want the server %s", vol["server"], srvID)
	}
}

// Attaching a volume must not take it from the server that owns it.
//
// The guard read Attrs["server"] while every other reader in the pack — the
// view, volumesOf, the delete and terminate paths — reads
// Runtime[runtimeServerKey]. So it saw nothing on a root volume, and an audit
// moved one server's root volume onto another: both then listed it.
func TestAttachingDoesNotStealAnotherServersVolume(t *testing.T) {
	// Three doors onto the same fact. A third audit walked the two the previous
	// fix had not touched: only attach-volume asked the question, so a create
	// naming another server's root volume moved it and both servers listed it.
	for _, door := range []struct {
		what string
		take func(t *testing.T, ts *httptest.Server, thief, volume string) int
	}{
		{"attach-volume", func(t *testing.T, ts *httptest.Server, thief, volume string) int {
			status, _ := do(t, ts, "POST", zone+"/servers/"+thief+"/attach-volume", `{"volume_id":"`+volume+`"}`)
			return status
		}},
		{"a create naming it", func(t *testing.T, ts *httptest.Server, _, volume string) int {
			status, _ := do(t, ts, "POST", zone+"/servers",
				`{"name":"thief","commercial_type":"DEV1-S","image":"ubuntu_jammy","volumes":{"1":{"id":"`+volume+`"}}}`)
			return status
		}},
		{"an update naming it", func(t *testing.T, ts *httptest.Server, thief, volume string) int {
			status, _ := do(t, ts, "PATCH", zone+"/servers/"+thief,
				`{"volumes":{"1":{"id":"`+volume+`"}}}`)
			return status
		}},
	} {
		t.Run(door.what, func(t *testing.T) {
			ts := newTestServer(t)
			owner := aServer(t, ts, "owner")
			thief := aServer(t, ts, "thief-host")

			_, out := do(t, ts, "GET", zone+"/servers/"+owner, "")
			srv, _ := out["server"].(map[string]any)
			volumes, _ := srv["volumes"].(map[string]any)
			root, _ := volumes["0"].(map[string]any)
			rootID, _ := root["id"].(string)
			if rootID == "" {
				t.Fatalf("the owner has no root volume: %v", srv)
			}

			// The thief's own root, which a failed steal must not cost it.
			_, out = do(t, ts, "GET", zone+"/servers/"+thief, "")
			srv, _ = out["server"].(map[string]any)
			volumes, _ = srv["volumes"].(map[string]any)
			own, _ := volumes["0"].(map[string]any)
			ownRoot, _ := own["id"].(string)

			door.take(t, ts, thief, rootID)

			// Whatever the status, the volume must not have moved: a create that
			// skips an unavailable volume answers 201, and that is fine — what is
			// not fine is the owner losing its disk.
			_, out = do(t, ts, "GET", zone+"/volumes/"+rootID, "")
			vol, _ := out["volume"].(map[string]any)
			server, _ := vol["server"].(map[string]any)
			if server == nil || server["id"] != owner {
				t.Errorf("%s moved the root volume: it now names %v", door.what, vol["server"])
			}

			// The two consequences the first version of this test did not
			// assert, which is how the PATCH door went on stealing through
			// three audits: the thief must not list the volume, and must not
			// have lost its own root doing so.
			_, out = do(t, ts, "GET", zone+"/servers/"+thief, "")
			srv, _ = out["server"].(map[string]any)
			volumes, _ = srv["volumes"].(map[string]any)
			for key, entry := range volumes {
				listed, _ := entry.(map[string]any)
				if id, _ := listed["id"].(string); id == rootID {
					t.Errorf("%s: the thief lists the owner's volume under %q — both servers hold it", door.what, key)
				}
			}
			_, out = do(t, ts, "GET", zone+"/volumes/"+ownRoot, "")
			vol, _ = out["volume"].(map[string]any)
			if holder, _ := vol["server"].(map[string]any); holder == nil || holder["id"] != thief {
				t.Errorf("%s: the thief's own root was detached by the attempt: %v", door.what, vol["server"])
			}
		})
	}
}

// Terminate gives back exactly what delete gives back.
//
// They are two doors to one state and they disagreed twice: first about
// addresses, then about volumes. Terminate kept the volumes attached to a
// server that answered 404, so `tofu destroy` on a server with
// additional_volume_ids failed with "volume is still attached to a server" on
// every retry — the provider walks terminate, not delete.
func TestTerminateReleasesWhatDeleteReleases(t *testing.T) {
	for _, door := range []struct {
		what string
		kill func(t *testing.T, ts *httptest.Server, id string)
	}{
		{"delete", func(t *testing.T, ts *httptest.Server, id string) {
			do(t, ts, "DELETE", zone+"/servers/"+id, "")
		}},
		{"terminate", func(t *testing.T, ts *httptest.Server, id string) {
			do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"terminate"}`)
		}},
	} {
		t.Run(door.what, func(t *testing.T) {
			ts := newTestServer(t)
			srvID := aServer(t, ts, "doomed")
			volID := aVolume(t, ts, "extra")
			if status, out := do(t, ts, "POST", zone+"/servers/"+srvID+"/attach-volume",
				`{"volume_id":"`+volID+`"}`); status != http.StatusOK {
				t.Fatalf("attach-volume: %d %v", status, out)
			}

			door.kill(t, ts, srvID)

			_, out := do(t, ts, "GET", zone+"/volumes/"+volID, "")
			vol, _ := out["volume"].(map[string]any)
			if server := vol["server"]; server != nil {
				t.Errorf("after %s the volume still names %v", door.what, server)
			}
			// And it can be deleted, which is the operation Terraform retries.
			if status, _ := do(t, ts, "DELETE", zone+"/volumes/"+volID, ""); status != http.StatusNoContent {
				t.Errorf("after %s the volume cannot be deleted: %d", door.what, status)
			}
		})
	}
}

// Creating a server does not take an address off a live machine.
//
// updateIP unroutes the previous holder before it routes the new one; the
// create path set the new owner and left the address on the old machine, so
// under a runtime two machines claimed the same /32. Nothing in the API shows
// it — public_ips is computed, so the first server simply stops listing it —
// which is why this is asserted through the runtime, the way the machine-driver
// skill says argument-level facts have to be.
func TestCreatingAServerDoesNotStealALiveAddress(t *testing.T) {
	runtime := &routingRuntime{fakeRuntime: newFakeRuntime()}
	close(runtime.release) // nothing here needs to block
	ts := newRuntimeTestServer(t, runtime)

	_, out := do(t, ts, "POST", zone+"/ips", `{}`)
	ip, _ := out["ip"].(map[string]any)
	ipID, _ := ip["id"].(string)
	address, _ := ip["address"].(string)

	_, out = do(t, ts, "POST", zone+"/servers",
		`{"name":"first","commercial_type":"DEV1-S","image":"ubuntu_jammy","public_ip":"`+ipID+`"}`)
	srv, _ := out["server"].(map[string]any)
	firstID, _ := srv["id"].(string)
	// Started, so there is a machine to take the address off: a create leaves
	// the server stopped, and withdrawing from nothing proves nothing.
	if status, body := do(t, ts, "POST", zone+"/servers/"+firstID+"/action",
		`{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron: %d %v", status, body)
	}
	before := runtime.unrouted()

	do(t, ts, "POST", zone+"/servers",
		`{"name":"second","commercial_type":"DEV1-S","image":"ubuntu_jammy","public_ip":"`+ipID+`"}`)

	after := runtime.unrouted()
	if len(after) != len(before)+1 {
		t.Fatalf("the address moved without being withdrawn from the first machine: %v", after)
	}
	if got := after[len(after)-1]; got.address != address {
		t.Errorf("withdrew %q, want the address that moved (%q)", got.address, address)
	}
	if after[len(after)-1].machine == "" {
		t.Error("withdrew the address from no machine at all")
	}
}

// routingRuntime is a fakeRuntime that also carries addresses, so a test can
// assert what was withdrawn and from which machine.
type routingRuntime struct {
	*fakeRuntime

	mu     sync.Mutex
	routes []withdrawal
}

type withdrawal struct{ machine, address string }

func (r *routingRuntime) RouteAddress(_ context.Context, _ machine.AddressSpec) error { return nil }

func (r *routingRuntime) UnrouteAddress(_ context.Context, name, address string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, withdrawal{machine: name, address: address})
	return nil
}

func (r *routingRuntime) unrouted() []withdrawal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]withdrawal(nil), r.routes...)
}

// The volume list filters by substring, the way the SDK documents it.
//
// instance_sdk.go gives the example itself: "vol" will return "myvolume" but
// not "data". Compared by equality, `scw instance volume list name=vol` came
// back empty against a volume called myvolume — while filterServers, twenty
// lines away in the sibling file, had just been fixed to match the SDK and
// carries the quote in its comment. Same defect, one file over, found by a
// third audit.
func TestVolumesFilterByNameLikeTheSDKSays(t *testing.T) {
	ts := newTestServer(t)
	aVolume(t, ts, "myvolume")
	aVolume(t, ts, "data")

	_, out := do(t, ts, "GET", zone+"/volumes?name=vol", "")
	volumes, _ := out["volumes"].([]any)
	if len(volumes) != 1 {
		t.Fatalf("name=vol matched %d volume(s), want the one called myvolume: %v", len(volumes), out)
	}
	vol, _ := volumes[0].(map[string]any)
	if name, _ := vol["name"].(string); name != "myvolume" {
		t.Errorf("name=vol matched %q", name)
	}
	// And it still refuses what does not contain it, or the filter would just be
	// a way to return everything.
	_, out = do(t, ts, "GET", zone+"/volumes?name=absent", "")
	if volumes, _ = out["volumes"].([]any); len(volumes) != 0 {
		t.Errorf("name=absent matched %d volume(s)", len(volumes))
	}
}

// A precondition renders as a sentence in the client, not as an empty string.
//
// PreconditionFailedError.Error() in scw/errors.go switches on exactly three
// tokens and returns "" for anything else, then appends help_message after a
// comma. The pack built "<kind>_resource_still_in_use", so `scw instance
// security-group delete <default>` printed "precondition failed: " with
// nothing after the colon, and the paths that had not been migrated printed a
// dangling comma. Reproducing the SDK's switch here rather than importing it
// is the zero-dependency rule; the table is copied from that file and the
// comment above each site names it.
//
// This is the second of the two tests a previous commit cited without writing.
func TestAPreconditionRendersAsASentence(t *testing.T) {
	rendered := func(body map[string]any) string {
		var msg string
		switch body["precondition"] {
		case "unknown_precondition":
			msg = "unknown precondition"
		case "resource_still_in_use":
			msg = "resource is still in use"
		case "attribute_must_be_set":
			msg = "attribute must be set"
		}
		if help, _ := body["help_message"].(string); help != "" {
			msg += ", " + help
		}
		return msg
	}

	ts := newTestServer(t)
	srvID := aServer(t, ts, "holder")
	volID := aVolume(t, ts, "held")
	if status, out := do(t, ts, "POST", zone+"/servers/"+srvID+"/attach-volume",
		`{"volume_id":"`+volID+`"}`); status != http.StatusOK {
		t.Fatalf("attach-volume: %d %v", status, out)
	}
	_, groups := do(t, ts, "GET", zone+"/security_groups", "")
	list, _ := groups["security_groups"].([]any)
	first, _ := list[0].(map[string]any)
	groupID, _ := first["id"].(string)

	for _, probe := range []struct {
		what   string
		method string
		path   string
	}{
		// The volume path, which the audit found still on the old shape.
		{"an attached volume", "DELETE", zone + "/volumes/" + volID},
		// The security-group path, which is the one it reproduced live.
		{"the project's default security group", "DELETE", zone + "/security_groups/" + groupID},
	} {
		status, body := do(t, ts, probe.method, probe.path, "")
		if status != http.StatusBadRequest {
			t.Errorf("%s: deleting it answered %d, want 400", probe.what, status)
			continue
		}
		if body["type"] != "precondition_failed" {
			t.Errorf("%s: type is %v, want precondition_failed", probe.what, body["type"])
		}
		sentence := rendered(body)
		if sentence == "" {
			t.Errorf("%s: the SDK would print \"precondition failed: \" and nothing else (precondition=%v)",
				probe.what, body["precondition"])
		}
		if strings.HasPrefix(sentence, ",") {
			t.Errorf("%s: the SDK would print a dangling comma: %q", probe.what, sentence)
		}
	}
}

// An address is a valid reference for every verb that takes one, delete
// included.
//
// The lookup by address was added for GET and PATCH; delete resolved through it
// and then removed the *reference the client typed* rather than the id it had
// just resolved. `scw instance ip delete 203.0.113.7` answered 204 and the
// address survived — a 200-that-does-nothing, which is the class the lookup was
// added to fix, reintroduced one door over.
//
// This is one of the two tests a previous commit cited by name without ever
// writing it. It exists now.
func TestAnAddressIsAValidIPReference(t *testing.T) {
	ts := newTestServer(t)
	_, out := do(t, ts, "POST", zone+"/ips", `{}`)
	ip, _ := out["ip"].(map[string]any)
	address, _ := ip["address"].(string)
	id, _ := ip["id"].(string)
	if address == "" || id == "" {
		t.Fatalf("no address allocated: %v", out)
	}

	if status, _ := do(t, ts, "GET", zone+"/ips/"+address, ""); status != http.StatusOK {
		t.Errorf("GET by address: %d", status)
	}
	if status, _ := do(t, ts, "DELETE", zone+"/ips/"+address, ""); status != http.StatusNoContent {
		t.Errorf("DELETE by address: %d", status)
	}
	// The one the previous version got wrong: it answered, and kept it.
	if status, _ := do(t, ts, "GET", zone+"/ips/"+id, ""); status != http.StatusNotFound {
		t.Errorf("the address survived its own delete: GET by id answered %d", status)
	}
}

// A refused attachment is visible on the NIC.
//
// Measured under --vm incus-vm: Incus cannot hot-plug a NIC into a running
// virtual machine on this host ("PCI: slot 0 function 0 not available for
// virtio-net-pci, in use by virtio-balloon-pci"). The attachment failed, the
// pack logged it, and the API went on publishing an address the machine did not
// carry — three minutes of polling confirmed the guest never took it.
//
// PrivateNICState declares syncing_error for exactly this, so nothing is
// invented. A client that reads the NIC now learns what the log knew.
func TestARefusedAttachmentIsVisibleOnTheNIC(t *testing.T) {
	refusing := &refusingRuntime{fakeRuntime: newFakeRuntime()}
	close(refusing.release)
	ts := newRuntimeTestServer(t, refusing)

	srvID := aServer(t, ts, "vm-host")
	if status, _ := do(t, ts, "POST", zone+"/servers/"+srvID+"/action",
		`{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron")
	}
	_, out := do(t, ts, "POST", "/vpc/v2/regions/fr-par/private-networks",
		`{"name":"net","subnets":["10.190.0.0/24"]}`)
	pnID, _ := out["id"].(string)
	if pnID == "" {
		t.Fatalf("no private network: %v", out)
	}

	_, out = do(t, ts, "POST", zone+"/servers/"+srvID+"/private_nics",
		`{"private_network_id":"`+pnID+`"}`)
	nic, _ := out["private_nic"].(map[string]any)
	if nic == nil {
		t.Fatalf("no NIC created: %v", out)
	}
	nicID, _ := nic["id"].(string)

	_, out = do(t, ts, "GET", zone+"/servers/"+srvID+"/private_nics/"+nicID, "")
	nic, _ = out["private_nic"].(map[string]any)
	if state, _ := nic["state"].(string); state != "syncing_error" {
		t.Errorf("the NIC says %q after the runtime refused the attachment, want syncing_error", state)
	}
}

// refusingRuntime attaches nothing, the way Incus refuses a hot-plugged NIC on a
// running virtual machine.
type refusingRuntime struct {
	*fakeRuntime
}

func (r *refusingRuntime) Attach(context.Context, string, machine.Attachment) error {
	return errors.New(`Failed to start device "eth1": PCI: slot 0 function 0 not available`)
}
