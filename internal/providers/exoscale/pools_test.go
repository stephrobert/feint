package exoscale_test

import (
	"net/http"
	"sync"
	"testing"
)

// Instance pools (EXO-7, #232): the arithmetic, and what it does to the members.
//
// The issue's closing conditions are what these assert, minus the one that needs
// a machine runtime — that `scale` moves the number of machines the runtime
// really holds, which the network suite measures under `FEINT_VM=incus` because
// a unit test has no runtime to count.
//
// What is checkable without one is the layer underneath, and it is the layer the
// runtime count depends on: a member is an ordinary instance, so if the members
// move, the machines move by the path that already starts and destroys them.

func aPool(t *testing.T, h http.Handler, name string, size int) string {
	t.Helper()
	status, out := callRaw(h, "POST", "/v2/instance-pool", `{
		"name": "`+name+`",
		"size": `+itoa(size)+`,
		"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template": {"id": "11111111-1111-4111-8111-111111111111"},
		"disk-size": 10
	}`)
	if status != http.StatusOK {
		t.Fatalf("create pool %s: status %d (%v)", name, status, out)
	}
	ref, _ := out["reference"].(map[string]any)
	id, _ := ref["id"].(string)
	if id == "" {
		t.Fatalf("the create operation names no resource: %v", out)
	}
	return id
}

func poolMembers(t *testing.T, h http.Handler, id string) []string {
	t.Helper()
	status, pool := callRaw(h, "GET", "/v2/instance-pool/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get pool: status %d (%v)", status, pool)
	}
	list, _ := pool["instances"].([]any)
	out := make([]string, 0, len(list))
	for _, entry := range list {
		ref, _ := entry.(map[string]any)
		if member, _ := ref["id"].(string); member != "" {
			out = append(out, member)
		}
	}
	return out
}

// A pool creates its members, and they are ordinary instances.
//
// That is the design in one assertion: `list-instances` shows them, so
// everything an instance already has — a machine, a lifecycle, an address —
// applies to a pool member with no second implementation.
func TestAPoolsMembersAreOrdinaryInstances(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	id := aPool(t, h, "web", 3)

	members := poolMembers(t, h, id)
	if len(members) != 3 {
		t.Fatalf("the pool holds %d member(s), want 3", len(members))
	}

	_, list := callRaw(h, "GET", "/v2/instance", "")
	instances, _ := list["instances"].([]any)
	if len(instances) != 3 {
		t.Errorf("the instance list shows %d, want the pool's 3: a member nothing lists "+
			"is a machine no client can find", len(instances))
	}

	// Each member names its manager, which is how a client reading one instance
	// learns what governs it without asking every pool.
	for _, member := range members {
		status, instance := callRaw(h, "GET", "/v2/instance/"+member, "")
		if status != http.StatusOK {
			t.Fatalf("get member: status %d", status)
		}
		manager, _ := instance["manager"].(map[string]any)
		if got, _ := manager["id"].(string); got != id {
			t.Errorf("member %s names manager %v, want the pool %s", member, instance["manager"], id)
		}
	}
}

// Scaling moves the members, in both directions, and touches nobody else.
//
// The second half is the one worth writing: an instance created outside the pool
// must survive a scale down. A pool that counted every instance in the store
// would pass the first assertion and destroy a stranger's machine.
func TestScalingAPoolMovesTheMembersAndNotTheirNeighbours(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	id := aPool(t, h, "web", 2)
	stranger := anInstance(t, h, "not-in-the-pool")

	if status, out := callRaw(h, "PUT", "/v2/instance-pool/"+id+":scale", `{"size": 4}`); status != http.StatusOK {
		t.Fatalf("scale up: status %d (%v)", status, out)
	}
	if members := poolMembers(t, h, id); len(members) != 4 {
		t.Errorf("after scaling to 4 the pool holds %d member(s)", len(members))
	}

	// Down again, and the two oldest survive: the machine serving longest is the
	// one a client would least expect to lose.
	before := poolMembers(t, h, id)
	if status, out := callRaw(h, "PUT", "/v2/instance-pool/"+id+":scale", `{"size": 2}`); status != http.StatusOK {
		t.Fatalf("scale down: status %d (%v)", status, out)
	}
	after := poolMembers(t, h, id)
	if len(after) != 2 {
		t.Fatalf("after scaling to 2 the pool holds %d member(s)", len(after))
	}
	for i, member := range after {
		if member != before[i] {
			t.Errorf("scaling down replaced member %d (%s became %s): the oldest members must stay",
				i, before[i], member)
		}
	}

	// And the pool says what it holds.
	_, pool := callRaw(h, "GET", "/v2/instance-pool/"+id, "")
	if size, _ := pool["size"].(float64); size != 2 {
		t.Errorf("the pool announces size %v while holding 2 members", pool["size"])
	}

	// The stranger is untouched.
	if status, _ := callRaw(h, "GET", "/v2/instance/"+stranger, ""); status != http.StatusOK {
		t.Error("scaling the pool down destroyed an instance that was never a member")
	}
}

// Evicting removes the members it names and no others.
//
// The distinction from a scale down is the whole reason the call exists: a scale
// says how many, an evict says which ones — the member whose machine is
// misbehaving. An id that is not a member is refused rather than skipped, so a
// client cannot believe it evicted something.
func TestEvictingRemovesTheNamedMembersAndRefusesAStranger(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	id := aPool(t, h, "web", 3)
	members := poolMembers(t, h, id)
	stranger := anInstance(t, h, "not-in-the-pool")

	if status, _ := callRaw(h, "PUT", "/v2/instance-pool/"+id+":evict",
		`{"instances": ["`+stranger+`"]}`); status == http.StatusOK {
		t.Error("evicting an instance that is not a member answered success")
	}
	if len(poolMembers(t, h, id)) != 3 {
		t.Error("the refused evict removed a member anyway")
	}

	// The middle one, so the assertion cannot pass by removing the last.
	victim := members[1]
	if status, out := callRaw(h, "PUT", "/v2/instance-pool/"+id+":evict",
		`{"instances": ["`+victim+`"]}`); status != http.StatusOK {
		t.Fatalf("evict: status %d (%v)", status, out)
	}
	left := poolMembers(t, h, id)
	if len(left) != 2 {
		t.Fatalf("after evicting one member the pool holds %d", len(left))
	}
	for _, member := range left {
		if member == victim {
			t.Error("the evicted member is still in the pool")
		}
	}
	if status, _ := callRaw(h, "GET", "/v2/instance/"+victim, ""); status != http.StatusNotFound {
		t.Error("the evicted member still answers as an instance")
	}
	// The neighbours are still there, which is what separates an evict from a
	// scale down that happened to land on the same count.
	if status, _ := callRaw(h, "GET", "/v2/instance/"+members[0], ""); status != http.StatusOK {
		t.Error("evicting one member took a neighbour with it")
	}
	if status, _ := callRaw(h, "GET", "/v2/instance/"+members[2], ""); status != http.StatusOK {
		t.Error("evicting one member took a neighbour with it")
	}
}

// Deleting a pool takes its members with it.
//
// They exist because it made them; leaving them behind would leave instances
// naming a manager that is gone, which is the orphan class #215 named and which
// storetest.Orphans now reports for this pack.
func TestDeletingAPoolTakesItsMembers(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	id := aPool(t, h, "web", 2)
	members := poolMembers(t, h, id)
	stranger := anInstance(t, h, "survivor")

	if status, _ := callRaw(h, "DELETE", "/v2/instance-pool/"+id, ""); status != http.StatusOK {
		t.Fatal("the pool refused its delete")
	}
	for _, member := range members {
		if status, _ := callRaw(h, "GET", "/v2/instance/"+member, ""); status != http.StatusNotFound {
			t.Errorf("member %s outlived the pool that made it", member)
		}
	}
	if status, _ := callRaw(h, "GET", "/v2/instance/"+stranger, ""); status != http.StatusOK {
		t.Error("deleting the pool destroyed an instance it never managed")
	}
}

// Two scales at once agree on one answer.
//
// The race the pool's lock exists for: both requests read three members, both
// decide to add one, and the pool ends at five having been told four twice. Each
// scale is a read-modify-write over a count, which is the shape CLAUDE.md
// devotes a section to — and the lock is keyed by the pool rather than globally,
// so two pools scaling at once do not queue behind each other.
func TestTwoConcurrentScalesAgreeOnOneSize(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	id := aPool(t, h, "web", 1)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callRaw(h, "PUT", "/v2/instance-pool/"+id+":scale", `{"size": 4}`)
		}()
	}
	wg.Wait()

	members := poolMembers(t, h, id)
	if len(members) != 4 {
		t.Errorf("eight concurrent scales to 4 left %d member(s): the count was read and "+
			"written by more than one request at a time", len(members))
	}
	_, pool := callRaw(h, "GET", "/v2/instance-pool/"+id, "")
	if size, _ := pool["size"].(float64); int(size) != len(members) {
		t.Errorf("the pool announces %v and holds %d: a client reading either is told "+
			"something the other denies", pool["size"], len(members))
	}
}

// A pool refuses a template the zone does not offer.
//
// Refused at the call the client can still fix, rather than at the boot of every
// member: a pool that accepted it would answer 200 and then produce machines
// whose start refuses, with the refusal arriving in a log nobody reads.
func TestAPoolRefusesATemplateNothingOffers(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)
	status, _ := callRaw(h, "POST", "/v2/instance-pool", `{
		"name": "doomed",
		"size": 2,
		"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template": {"id": "99999999-9999-4999-8999-999999999999"}
	}`)
	if status == http.StatusOK {
		t.Error("a pool was created against a template this zone does not offer")
	}
	_, list := callRaw(h, "GET", "/v2/instance", "")
	if instances, _ := list["instances"].([]any); len(instances) != 0 {
		t.Errorf("the refused pool created %d instance(s) before refusing", len(instances))
	}
}
