package exoscale_test

import (
	"fmt"
	"net/http"
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
// own 404 envelope, not net/http's page: the dispatcher hands the miss to the
// pack, and with dozens of actions still untriaged this is an answer real
// clients will meet.
func TestAnUnservedActionAnswersThePacksOwn404(t *testing.T) {
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
