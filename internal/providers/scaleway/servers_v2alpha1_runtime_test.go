package scaleway_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// A detach through instance/v2alpha1 takes the machine off its network.
//
// #426 was a destruction that answered success and left the device on the
// container, and releaseNIC's comment (privatenics.go) names three doors into
// that state. The detach is a fourth, with a worse shape: it clears the
// interface's server link, so the DELETE the client sends next cannot find the
// machine any more and detaches nothing. If this call is the one that does not
// reach the runtime, nothing ever does.
func TestADetachTakesTheMachineOffItsNetwork(t *testing.T) {
	rt := newFakeRuntime()
	close(rt.release)
	ts := newRuntimeTestServer(t, machine.Use(rt))

	_, serverID, nicID := nicOnRunningServer(t, ts, "10.79.0.0/24")
	if before := rt.detaches(); len(before) != 0 {
		t.Fatalf("the runtime was asked to detach %v before anything was detached", before)
	}

	status, body := detach(t, ts, serverID, `{"private_network_interface_id":"`+nicID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("detach: expected 200, got %d (%v)", status, body)
	}

	got := rt.detaches()
	if len(got) == 0 {
		t.Fatal("the interface left its server and the runtime was never asked to detach anything: " +
			"the device stays on the machine, and the later DELETE cannot name the machine to remove it")
	}
	if !strings.Contains(got[0], "feint-scw-"+serverID) {
		t.Errorf("the detach named %q, which is not this server's machine", got[0])
	}
	if !strings.Contains(got[0], " "+machine.NetworkPrefix+"-") {
		t.Errorf("the detach named %q, which is not a network this emulator created", got[0])
	}

	// The client's own DELETE follows, and it must neither fail nor ask the
	// runtime a second time: the server link is gone, so there is no machine to name.
	if status, _ := do(t, ts, "DELETE", v2alphaNICs+"/"+nicID, ""); status != http.StatusNoContent {
		t.Fatalf("deleting the detached interface answered %d, want 204", status)
	}
	if after := rt.detaches(); len(after) != 1 {
		t.Errorf("the runtime was asked to detach %d time(s) over detach + delete, want exactly 1: %v", len(after), after)
	}
}

// A second detach of the same interface is a 404 naming the interface, and harms nothing.
//
// The provider treats a 404 on this call as "already done" at both of its call
// sites (private_nic.go and helpers_instance.go at 2.83.0) and returns without
// sending its own DELETE, so a repeated detach must answer 404 and not an error.
// What the real API answers here is not recorded in this repository: this pins
// the behaviour, it does not claim it is the cloud's.
func TestADetachRepeatedAnswersNotFoundAndHarmsNothing(t *testing.T) {
	ts := newTestServer(t)
	serverID, _, nic := attachedNIC(t, ts, "twice", "10.180.0.0/24")
	nicID, _ := nic["id"].(string)
	body := `{"private_network_interface_id":"` + nicID + `"}`

	if status, out := detach(t, ts, serverID, body); status != http.StatusOK {
		t.Fatalf("first detach: expected 200, got %d (%v)", status, out)
	}
	status, out := detach(t, ts, serverID, body)
	if status != http.StatusNotFound {
		t.Fatalf("second detach: expected 404, got %d (%v)", status, out)
	}
	if out["type"] != "not_found" || out["resource"] != "private_nic" || out["resource_id"] != nicID {
		t.Errorf("the refusal does not name the interface: %v", out)
	}

	// Still there, still deletable: the refusal acted on nothing.
	if status, out := do(t, ts, "GET", v2alphaNICs+"/"+nicID, ""); status != http.StatusOK || out["server_id"] != "" {
		t.Errorf("after a repeated detach the interface reads %d server_id=%v, want 200 and no server", status, out["server_id"])
	}
	if status, _ := do(t, ts, "DELETE", v2alphaNICs+"/"+nicID, ""); status != http.StatusNoContent {
		t.Errorf("delete after a repeated detach answered %d, want 204", status)
	}
}
