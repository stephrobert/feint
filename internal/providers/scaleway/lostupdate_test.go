package scaleway_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/store/storetest"
)

// The update path is a read-modify-write, and it was the only one of the three
// packs' that ordered nothing (#211).
//
// Outscale holds the per-target lock around UpdateVm; Exoscale mutates inside
// store.Update, which is read-modify-write under the store's own write lock;
// Scaleway read a copy, changed a field and committed the whole Attrs map. The
// store documents what that costs, on Commit itself: it "does not merge", so a
// concurrent write to a different field of the same resource is lost — and both
// clients were told 200.
//
// The property lives in the core (storetest.NoLostUpdate) so a fourth pack fails
// this rather than merely differing from the three. What is Scaleway's own is the
// traffic below: PATCH /servers/{id}, one field per writer, a fresh server per
// trial.
func TestConcurrentUpdatesKeepEveryAcknowledgedField(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	found := storetest.NoLostUpdate(40, func(trial int) []storetest.Write {
		status, out := do(t, ts, "POST", zone+"/servers",
			fmt.Sprintf(`{"name":"before-%d","commercial_type":"DEV1-S"}`, trial))
		if status != http.StatusCreated {
			t.Fatalf("trial %d create: status %d", trial, status)
		}
		server, _ := out["server"].(map[string]any)
		id, _ := server["id"].(string)

		// A goroutine may not call t.Fatalf, so the request helper here reports
		// rather than fails.
		patch := func(body string) bool {
			status, _ := doRaw(ts, "PATCH", zone+"/servers/"+id, body)
			return status == http.StatusOK
		}
		field := func(name string) func() string {
			return func() string {
				status, out := do(t, ts, "GET", zone+"/servers/"+id, "")
				if status != http.StatusOK {
					t.Fatalf("get: status %d", status)
				}
				server, _ := out["server"].(map[string]any)
				switch value := server[name].(type) {
				case string:
					return value
				case bool:
					return fmt.Sprintf("%v", value)
				case []any:
					parts := make([]string, 0, len(value))
					for _, v := range value {
						parts = append(parts, fmt.Sprintf("%v", v))
					}
					return strings.Join(parts, ",")
				default:
					return fmt.Sprintf("%v", value)
				}
			}
		}

		// Three fields, three writers, none of them touching another's. Whatever
		// interleaving happens, each of the three values was acknowledged and
		// must still be there: they cannot legitimately overwrite one another.
		return []storetest.Write{
			{
				Field: "name",
				Apply: func() bool { return patch(`{"name":"after"}`) },
				Got:   field("name"),
				Want:  "after",
			},
			{
				Field: "tags",
				Apply: func() bool { return patch(`{"tags":["barrage"]}`) },
				Got:   field("tags"),
				Want:  "barrage",
			},
			{
				Field: "protected",
				Apply: func() bool { return patch(`{"protected":true}`) },
				Got:   field("protected"),
				Want:  "true",
			},
		}
	})

	if len(found) > 0 {
		t.Errorf("the update path lost a field it had acknowledged:\n%s", strings.Join(found, "\n"))
	}
}

// A NIC create reads the server, writes a NIC and an address beside it, and then
// writes the server back — and none of that was ordered against a concurrent
// delete of the same server.
//
// The write-back used to be a Put, which reinserts unconditionally, so a DELETE
// landing in the window was undone and the server came back with its volumes and
// its address. It is a Commit now, and the handler re-reads the server under the
// hold, so the resurrection is answered twice over.
//
// Three guards answer it and none of them is spare, which took two wrong answers
// to establish. The hold alone still stranded a NIC on trial 10, because serverOf
// reads before the lock exists and a delete that had already finished by then
// goes unseen. The re-read alone leaves the window open on the other side. And
// p.lockAddresses(), which does serialise NIC creates against each other, is not
// the lock deleteServer takes, so it orders nothing here.
//
// What the barrage measures is the pair working together; each half is falsified
// on its own by tools/falsify/specs/serialise-then-commit.json.
//
// Trials rather than one run: written when a delete released nothing, any
// completed attach left an orphan and once was enough. Once the delete learned to
// release its NICs, an orphan needs an attach landing in the narrow window after
// that release, and repetition is what keeps the assertion worth making.
const nicBarrageTrials = 12

func TestAttachingANICDoesNotResurrectADeletedServer(t *testing.T) {
	runtime := newBarrageRuntime()
	ts, st := newBarrageServer(t, runtime)
	const zone = "/instance/v1/zones/fr-par-1"
	const region = "/vpc/v2/regions/fr-par"

	// One network per attacher, shared by every trial: the handler refuses a
	// second NIC on a network the server already has, and that refusal returns
	// before anything is written — so sharing one network within a trial would
	// measure the guard rather than the race. Across trials the servers differ,
	// so the networks can be reused.
	const attachers = 8
	networks := make([]string, 0, attachers)
	for i := range attachers {
		status, out := do(t, ts, "POST", region+"/private-networks",
			fmt.Sprintf(`{"name":"barrage-%d","subnets":["10.%d.0.0/24"]}`, i, 60+i))
		if status != http.StatusOK && status != http.StatusCreated {
			t.Fatalf("private network %d: status %d (%v)", i, status, out)
		}
		pnID, _ := out["id"].(string)
		if pnID == "" {
			t.Fatalf("private network %d: no id in %v", i, out)
		}
		networks = append(networks, pnID)
	}

	for trial := range nicBarrageTrials {
		status, out := do(t, ts, "POST", zone+"/servers",
			fmt.Sprintf(`{"name":"victim-%d","commercial_type":"DEV1-S"}`, trial))
		if status != http.StatusCreated {
			t.Fatalf("trial %d create: status %d", trial, status)
		}
		server, _ := out["server"].(map[string]any)
		id, _ := server["id"].(string)

		var wg sync.WaitGroup
		deleted := make(chan int, 1)
		for _, pnID := range networks {
			wg.Add(1)
			go func(pnID string) {
				defer wg.Done()
				doRaw(ts, "POST", zone+"/servers/"+id+"/private_nics",
					`{"private_network_id":"`+pnID+`"}`)
			}(pnID)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// The create leaves the server stopped, which is the state a delete
			// needs, so this is one call.
			status, _ := doRaw(ts, "DELETE", zone+"/servers/"+id, "")
			deleted <- status
		}()
		wg.Wait()
		close(deleted)

		if status := <-deleted; status != http.StatusNoContent {
			t.Fatalf("trial %d: the delete answered %d, so this run measured nothing", trial, status)
		}
		if status, _ := do(t, ts, "GET", zone+"/servers/"+id, ""); status != http.StatusNotFound {
			t.Fatalf("trial %d: the server came back after a 204 delete: get answers %d", trial, status)
		}

		// The half Commit cannot answer. A NIC left naming a deleted server is
		// invisible to a client — every list is scoped by server — and it holds
		// an address the pool will not hand out again.
		var orphans []string
		for _, res := range st.All() {
			if res.Kind != "instance/private-nic" {
				continue
			}
			if res.Runtime["server"] == id {
				orphans = append(orphans, res.ID)
			}
		}
		if len(orphans) > 0 {
			t.Fatalf("trial %d: %d private NIC(s) outlived the server they name (%s): %s",
				trial, len(orphans), id, strings.Join(orphans, ", "))
		}
	}
}

// The barrage above found this while looking for a race, and it is not one: no
// concurrency is needed. A server deleted while it carries a private NIC left the
// NIC and its IPAM address in the store, sequentially, every time.
//
// What that costs is an address nobody can reach and nobody can reclaim. A NIC
// listing is scoped by its server, so a client cannot see it once the server
// answers 404, while the address stays in IPAM — and the network's allocator is
// rebuilt from what IPAM holds, so it never offers that address again. Repeated
// create-attach-delete empties the subnet.
//
// Measured on fr-par-1 rather than assumed: attaching a server to a private
// network booked two addresses there, and deleting the server left the network
// with none.
func TestDeletingAServerReleasesItsPrivateNICs(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"
	const region = "/vpc/v2/regions/fr-par"

	_, out := do(t, ts, "POST", zone+"/servers", `{"name":"victim","commercial_type":"DEV1-S"}`)
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	status, out := do(t, ts, "POST", region+"/private-networks",
		`{"name":"one","subnets":["10.77.0.0/24"]}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("private network: status %d", status)
	}
	pnID, _ := out["id"].(string)

	status, out = do(t, ts, "POST", zone+"/servers/"+id+"/private_nics",
		`{"private_network_id":"`+pnID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("attach: status %d (%v)", status, out)
	}

	// The address is there before the delete, or the assertion after it would
	// pass on a network that never held one.
	status, out = do(t, ts, "GET", "/ipam/v1/regions/fr-par/ips?private_network_id="+pnID, "")
	if status != http.StatusOK {
		t.Fatalf("ipam list: status %d", status)
	}
	if total, _ := out["total_count"].(float64); total < 1 {
		t.Fatalf("the attach booked no address, so this test measures nothing: %v", out)
	}

	if status, _ := do(t, ts, "DELETE", zone+"/servers/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("delete: status %d", status)
	}

	// The NIC is gone with its server.
	if status, _ := do(t, ts, "GET", zone+"/servers/"+id+"/private_nics", ""); status != http.StatusNotFound {
		t.Errorf("the NIC listing still answers %d for a deleted server", status)
	}
	// And the address went back to the pool, which is the half a client can act
	// on: the allocator is rebuilt from IPAM, so an address left here is an
	// address the network has permanently lost.
	status, out = do(t, ts, "GET", "/ipam/v1/regions/fr-par/ips?private_network_id="+pnID, "")
	if status != http.StatusOK {
		t.Fatalf("ipam list: status %d", status)
	}
	if total, _ := out["total_count"].(float64); total != 0 {
		t.Errorf("the network still holds %v address(es) after its only server was deleted: %v",
			total, out)
	}
}
