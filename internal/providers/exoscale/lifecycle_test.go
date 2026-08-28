package exoscale_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// The lifecycle batch (EXO-2). Every shape asserted here was measured against
// the real API through `feint proxy` on 2026-08-10; the tests hold what the
// recording showed, not what a schema promises.

// createDemo posts the smallest valid instance and returns its id.
func createDemo(t *testing.T, h http.Handler) string {
	t.Helper()
	rec, _ := call(t, h, "POST", "/v2/instance", `{
		"name": "demo",
		"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template": {"id": "11111111-1111-4111-8111-111111111111"},
		"disk-size": 10
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create answered %d: %s", rec.Code, rec.Body.String())
	}
	_, listed := call(t, h, "GET", "/v2/instance", "")
	instances, _ := listed["instances"].([]any)
	if len(instances) == 0 {
		t.Fatal("no instance after create")
	}
	instance, _ := instances[len(instances)-1].(map[string]any)
	id, _ := instance["id"].(string)
	if id == "" {
		t.Fatalf("no id on %v", instance)
	}
	return id
}

func instanceState(t *testing.T, h http.Handler, id string) string {
	t.Helper()
	rec, body := call(t, h, "GET", "/v2/instance/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get answered %d: %s", rec.Code, rec.Body.String())
	}
	state, _ := body["state"].(string)
	return state
}

// stop and start walk the two-state machine. Their envelope is the reference
// one, and that is a decision the code records: the live API answers
// `resource: {id, type}` on exactly these two verbs, a field their published
// schema does not declare and egoscale's Operation struct cannot decode, so
// the emulator answers what every client actually consumes and what its own
// contract check accepts.
func TestStopAndStartWalkTheTwoStateMachine(t *testing.T) {
	h := serve(t)
	id := createDemo(t, h)

	rec, op := call(t, h, "PUT", "/v2/instance/"+id+":stop", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stop answered %d: %s", rec.Code, rec.Body.String())
	}
	if _, hasResource := op["resource"]; hasResource {
		t.Fatalf("stop answered a resource field no official decoder reads and the contract refuses: %v", op)
	}
	ref, _ := op["reference"].(map[string]any)
	if ref["command"] != "get-instance" {
		t.Fatalf("stop's reference is %v", ref)
	}
	if got := instanceState(t, h, id); got != "stopped" {
		t.Fatalf("state after stop is %q", got)
	}

	rec, _ = call(t, h, "PUT", "/v2/instance/"+id+":start", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("start answered %d: %s", rec.Code, rec.Body.String())
	}
	if got := instanceState(t, h, id); got != "running" {
		t.Fatalf("state after start is %q", got)
	}
}

// Every other lifecycle mutation answers a reference naming the read that
// follows — command "get-instance", never the mutation's own name, plus the
// link the provider follows.
func TestLifecycleMutationsReferToTheReadThatFollows(t *testing.T) {
	h := serve(t)
	id := createDemo(t, h)
	call(t, h, "PUT", "/v2/instance/"+id+":stop", "")

	for _, action := range []struct{ path, body string }{
		{":scale", `{"instance-type": {"id": "b6cd1ff5-3a2f-4e9d-a4d1-8988c1191fe8"}}`},
		{":resize-disk", `{"disk-size": 11}`},
		{":reset", `{}`},
		{":add-protection", ""},
		{":remove-protection", ""},
		{":reboot", ""},
	} {
		rec, op := call(t, h, "PUT", "/v2/instance/"+id+action.path, action.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s answered %d: %s", action.path, rec.Code, rec.Body.String())
		}
		ref, _ := op["reference"].(map[string]any)
		if ref["command"] != "get-instance" {
			t.Fatalf("%s referred to %v; the measured reference names the read that follows", action.path, ref)
		}
		if ref["link"] != "/v2/instance/"+id {
			t.Fatalf("%s's link is %v", action.path, ref["link"])
		}
	}

	// scale and resize-disk landed on the record.
	_, body := call(t, h, "GET", "/v2/instance/"+id, "")
	instanceType, _ := body["instance-type"].(map[string]any)
	if instanceType["id"] != "b6cd1ff5-3a2f-4e9d-a4d1-8988c1191fe8" {
		t.Fatalf("scale did not land: %v", instanceType)
	}
	if size, _ := body["disk-size"].(float64); size != 11 {
		t.Fatalf("resize-disk did not land: %v", body["disk-size"])
	}
}

// reset refuses a running instance, with the sentence the real API used.
func TestResetRefusesARunningInstance(t *testing.T) {
	h := serve(t)
	id := createDemo(t, h)

	rec, body := call(t, h, "PUT", "/v2/instance/"+id+":reset", "{}")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reset of a running instance answered %d, want 400", rec.Code)
	}
	if body["message"] != "The instance needs to be stopped." {
		t.Fatalf("the message is %q; the measured one is \"The instance needs to be stopped.\"", body["message"])
	}
}

// A protected instance refuses its delete, and the protection is removable.
func TestAProtectedInstanceRefusesItsDelete(t *testing.T) {
	h := serve(t)
	id := createDemo(t, h)

	call(t, h, "PUT", "/v2/instance/"+id+":add-protection", "")
	rec, _ := call(t, h, "DELETE", "/v2/instance/"+id, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete of a protected instance answered %d, want 400", rec.Code)
	}
	if got := instanceState(t, h, id); got == "" {
		t.Fatal("the protected instance is gone")
	}

	call(t, h, "PUT", "/v2/instance/"+id+":remove-protection", "")
	rec, _ = call(t, h, "DELETE", "/v2/instance/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete after remove-protection answered %d: %s", rec.Code, rec.Body.String())
	}
}

// An action the pack does not serve on a served base path answers the pack's
// own envelope, not net/http's page: the dispatcher hands the miss to the
// pack, and with dozens of actions still untriaged this is an answer real
// clients will meet.
//
// Still 404 and not 501, measured rather than chosen: `exo compute instance
// create` calls GET /v2/reverse-dns/instance/{id} after every create and treats
// anything but a 404 as fatal, so a louder refusal fails a client the real
// cloud would have served (#477). The marker a program can read is the
// X-Feint-Not-Emulated header the shared layer sets —
// emulator.TestAnUnroutedAnswerCarriesTheNotEmulatedHeader.
func TestAnUnservedActionAnswersThePacksOwnRefusal(t *testing.T) {
	h := serve(t)
	id := createDemo(t, h)

	rec, body := call(t, h, "PUT", "/v2/instance/"+id+":enable-tpm", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an unserved action answered %d, want 404", rec.Code)
	}
	if _, ok := body["message"].(string); !ok {
		t.Fatalf("no message field in %v: that is the whole of Exoscale's error envelope", body)
	}
}

// Two lifecycle actions on one instance serialise on the target.
//
// The issue that scoped this batch requires every new lifecycle path to take
// machine.Binding.Serialise and prove it with a concurrency test; this is that
// test, run with -race. Without the lock in transitionInstance, concurrent
// stop and start interleave their read-act-write cycles and the race detector
// reports the write conflict on the shared resource.
func TestTwoLifecycleActionsOnOneInstanceDoNotRace(t *testing.T) {
	h := serve(t)
	id := createDemo(t, h)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			action := ":stop"
			if i%2 == 0 {
				action = ":start"
			}
			rec, _ := call(t, h, "PUT", "/v2/instance/"+id+action, "")
			if rec.Code != http.StatusOK {
				panic(fmt.Sprintf("%s answered %d", action, rec.Code))
			}
		}(i)
	}
	wg.Wait()

	// Whatever won, the record is coherent: one of the two terminal states,
	// never a half-transition.
	if got := instanceState(t, h, id); got != "running" && got != "stopped" {
		t.Fatalf("state after concurrent actions is %q", got)
	}
}

// An instance is given a public address at creation, the way the real cloud
// does (#202).
//
// Measured on a real Exoscale account: an instance created with nothing but a
// type and a template answers `ip_address: 151.145.198.51` on the first read,
// with no private network and no elastic IP attached. This pack recorded the
// intent — `public-ip-assignment` defaults to `inet4` — and never honoured it,
// so an emulated instance published nothing, and the machine took whatever the
// runtime's default profile handed it, on the operator's own bridge.
//
// Both halves. A pack that always assigned one would pass the first check and
// break `public-ip-assignment: none`, which is a real client asking for a
// machine nobody can reach from outside.
func TestAnInstanceIsGivenAPublicAddressAtCreation(t *testing.T) {
	h := serve(t)

	id := createDemo(t, h)
	_, doc := call(t, h, "GET", "/v2/instance/"+id, "")
	address, _ := doc["public-ip"].(string)
	if address == "" {
		t.Fatalf("an instance created with the default assignment publishes no address: %v", doc)
	}
	if !strings.HasPrefix(address, "192.0.2.") {
		t.Errorf("the address is outside the pack's own RFC 5737 block: %s", address)
	}

	// A second instance must not be handed the same one, or the driver routes a
	// single /32 to two machines.
	other := createDemo(t, h)
	_, doc2 := call(t, h, "GET", "/v2/instance/"+other, "")
	if second, _ := doc2["public-ip"].(string); second == address {
		t.Errorf("two instances were handed %s", address)
	}

	// And the refusing half: a client that asks for none gets none.
	rec, _ := call(t, h, "POST", "/v2/instance", `{
		"name": "no-address",
		"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template": {"id": "11111111-1111-4111-8111-111111111111"},
		"disk-size": 10,
		"public-ip-assignment": "none"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create with no assignment answered %d: %s", rec.Code, rec.Body.String())
	}
	_, listed := call(t, h, "GET", "/v2/instance", "")
	instances, _ := listed["instances"].([]any)
	last, _ := instances[len(instances)-1].(map[string]any)
	if got, _ := last["public-ip"].(string); got != "" {
		t.Errorf("public-ip-assignment none was given %s anyway", got)
	}
}
