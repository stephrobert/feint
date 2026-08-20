package scaleway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// Releasing an address is an allocation event, and it takes the allocator's lock.
//
// This is the defect an audit found in SW-4 and the reason it is tested from
// inside the package rather than through a barrage. `bookIP` and
// `createPrivateNIC` hold `p.lockAddresses()` from the moment they rebuild occupancy
// to the moment they store the result; `releaseOne` held nothing, while being
// the operation that makes an address available again — `allocatorFor` rebuilds
// occupancy from the IPAM resources alone, so deleting one is exactly what frees
// an address.
//
// Why not a barrage. Two were tried and both stayed green with the lock removed,
// six runs each: the sequence needs a release to be interleaved *between* another
// request's read and write of the same address, which no amount of concurrent
// HTTP traffic reliably produces, and the second NIC that would receive the
// duplicate never coexists with the first in a cycle that tears its server down.
// A scenario that cannot fail is worth less than a smaller question asked
// exactly: does this path take the lock at all?
//
// Holding the lock from the test answers that, deterministically. With the fix,
// the release cannot finish while the lock is held. Without it, it finishes
// immediately — which is what this asserts, and what makes it fail when the fix
// is removed.
func TestAReleasedAddressCannotBeTakenWhileItIsBeingAttached(t *testing.T) {
	env := emulator.DefaultEnv()
	pack := New(env)
	srv, err := emulator.NewServer(env, pack)
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// A network, then an address booked from it.
	status, network := postJSON(t, ts, "/vpc/v2/regions/fr-par/private-networks",
		`{"name":"locked","subnets":["10.201.0.0/24"]}`)
	if status != http.StatusOK {
		t.Fatalf("create the network: %d (%v)", status, network)
	}
	pnID, _ := network["id"].(string)

	status, booked := postJSON(t, ts, "/ipam/v1/regions/fr-par/ips",
		`{"source":{"private_network_id":"`+pnID+`"}}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("book an address: %d (%v)", status, booked)
	}
	id, _ := booked["id"].(string)

	// The allocator is busy, the way it is for the seconds another request
	// spends between rebuilding occupancy and storing its result. The test
	// holds the same domain the pack's own allocations take.
	unlock := pack.lockAddresses()

	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/ipam/v1/regions/fr-par/ips/"+id, nil)
		resp, err := ts.Client().Do(req)
		if err != nil {
			done <- -1
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()

	select {
	case status := <-done:
		unlock()
		t.Fatalf("the release answered %d while the allocator was held: it frees an "+
			"address without taking the lock every allocation takes", status)
	case <-time.After(150 * time.Millisecond):
		// Blocked, which is the point.
	}

	// And it completes once the allocator is free: a release that never returned
	// would satisfy the assertion above and break the product.
	unlock()
	select {
	case status := <-done:
		if status != http.StatusNoContent {
			t.Errorf("the release answered %d once the lock was free, want 204", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the release never completed after the allocator was released")
	}

	// Gone, so the address is available again — the state the lock protects.
	if _, found := env.Store.Get(Name, kindIPAMIP, id); found {
		t.Error("the address survived its own release")
	}
}

// Commit refuses to resurrect a released address; Put did not.
//
// What this proves, exactly, and what it does not. updateIPAMIP wrote with Put,
// which re-inserts unconditionally: an address released while the PATCH was
// decoding came back carrying the new tags, and the client that released it had
// no way to know. CLAUDE.md records that resurrection as already paid for three
// times in other packs.
//
// This asserts the store-level half — the one that has a seam. Staging the
// handler-level race deterministically would need a hook between the moment it
// resolves the resource and the moment it writes, and no such seam exists; a
// concurrent-HTTP version of this test stayed green with the fix removed, which
// makes it a scenario that cannot fail. So the connection between the two halves
// is the call site in updateIPAMIP, read rather than triggered, and that is
// stated here rather than dressed up as a control.
func TestCommitDoesNotResurrectAReleasedAddress(t *testing.T) {
	env := emulator.DefaultEnv()
	pack := New(env)
	srv, err := emulator.NewServer(env, pack)
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	status, network := postJSON(t, ts, "/vpc/v2/regions/fr-par/private-networks",
		`{"name":"resurrect","subnets":["10.202.0.0/24"]}`)
	if status != http.StatusOK {
		t.Fatalf("create the network: %d (%v)", status, network)
	}
	pnID, _ := network["id"].(string)

	status, booked := postJSON(t, ts, "/ipam/v1/regions/fr-par/ips",
		`{"source":{"private_network_id":"`+pnID+`"}}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("book an address: %d (%v)", status, booked)
	}
	id, _ := booked["id"].(string)

	// The resource as a handler would have resolved it before the release,
	// which is the stale clone Put used to write back.
	stale, found := env.Store.Get(Name, kindIPAMIP, id)
	if !found {
		t.Fatal("the booked address is not in the store")
	}
	env.Store.Delete(Name, kindIPAMIP, id)

	// What updateIPAMIP did with that clone, before it moved under Store.Update.
	base := stale.Clone()
	stale.Attrs["tags"] = []string{"late"}
	if pack.env.Store.Commit(base, stale, pack.env.Now()) {
		t.Error("Commit reported success on a resource the client had released")
	}
	if _, back := env.Store.Get(Name, kindIPAMIP, id); back {
		t.Error("a released address came back, carrying the tags of a request that raced it")
	}
}

// postJSON is a small helper for these two: the package's exported tests live in
// scaleway_test and cannot be reached from here.
func postJSON(t *testing.T, ts *httptest.Server, path, body string) (int, map[string]any) {
	t.Helper()
	resp, err := ts.Client().Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}
