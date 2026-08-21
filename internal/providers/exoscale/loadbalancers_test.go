package exoscale_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stephrobert/feint/internal/core/store/storetest"
	"github.com/stephrobert/feint/internal/providers/exoscale"
)

// The Network Load Balancer (#345). What is pinned here is what #14 refused and
// what the measurement that reopened it says instead, plus the two references a
// client would file its resources under.

// nlbFixture builds what a real stack builds: a pool with `size` members, a
// balancer, and one service in front of the pool. It answers the balancer id,
// the service id, and the pool id.
func nlbFixture(t *testing.T, h http.Handler, size int) (balancerID, serviceID, poolID string) {
	t.Helper()

	rec, body := call(t, h, "POST", "/v2/instance-pool", fmt.Sprintf(
		`{"name":"pool","size":%d,"instance-type":{"id":"1"},"template":{"id":"22222222-2222-4222-8222-222222222222"}}`, size))
	if rec.Code != http.StatusOK {
		t.Fatalf("create the pool: status %d, %v", rec.Code, body)
	}
	poolID = referenceID(t, body)

	rec, body = call(t, h, "POST", "/v2/load-balancer", `{"name":"nlb","description":"front"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create the balancer: status %d, %v", rec.Code, body)
	}
	balancerID = referenceID(t, body)

	rec, body = call(t, h, "POST", "/v2/load-balancer/"+balancerID+"/service", fmt.Sprintf(
		`{"name":"web","instance-pool":{"id":%q},"port":80,"target-port":8080,
		  "protocol":"tcp","strategy":"round-robin",
		  "healthcheck":{"mode":"tcp","port":8080,"interval":10,"timeout":5,"retries":1}}`, poolID))
	if rec.Code != http.StatusOK {
		t.Fatalf("add the service: status %d, %v", rec.Code, body)
	}
	// Found the way egoscale v2 finds it, which is the way the real API makes a
	// client find it: read the balancer the operation refers to, and take the
	// service that was not there before. See serviceOperation.
	_, balancer := call(t, h, "GET", "/v2/load-balancer/"+referenceID(t, body), "")
	services := serviceViews(t, balancer)
	if len(services) != 1 {
		t.Fatalf("%d services after one add, want 1", len(services))
	}
	serviceID, _ = services[0]["id"].(string)
	if serviceID == "" {
		t.Fatalf("the service carries no id: %v", services[0])
	}
	return balancerID, serviceID, poolID
}

func referenceID(t *testing.T, operation map[string]any) string {
	t.Helper()
	reference, ok := operation["reference"].(map[string]any)
	if !ok {
		t.Fatalf("no reference in the operation %v: a client has nothing to read back", operation)
	}
	id, _ := reference["id"].(string)
	if id == "" {
		t.Fatalf("the reference of %v names no resource", operation)
	}
	return id
}

func serviceViews(t *testing.T, balancer map[string]any) []map[string]any {
	t.Helper()
	raw, ok := balancer["services"].([]any)
	if !ok {
		t.Fatalf("no services array on the balancer %v", balancer)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		service, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("a service of %v is not an object", balancer)
		}
		out = append(out, service)
	}
	return out
}

func backendEntries(t *testing.T, service map[string]any) []map[string]any {
	t.Helper()
	raw, ok := service["healthcheck-status"].([]any)
	if !ok {
		t.Fatalf("healthcheck-status is missing or not an array on %v", service)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		status, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("a healthcheck-status entry of %v is not an object", service)
		}
		out = append(out, status)
	}
	return out
}

// The refusal #14 stated, kept whole while the family is served.
//
// Nothing here probes a backend, so no backend may carry a verdict. The entries
// exist — they name the servers behind the service, which is true and useful —
// and not one of them says success or failure, on either read a client makes.
//
// This is the control the file header's prose is not: without the omission in
// backendStatuses, this fails on the first entry.
func TestNoBackendEverCarriesAHealthVerdict(t *testing.T) {
	h := serve(t)
	balancerID, serviceID, _ := nlbFixture(t, h, 2)

	_, service := call(t, h, "GET", "/v2/load-balancer/"+balancerID+"/service/"+serviceID, "")
	_, balancer := call(t, h, "GET", "/v2/load-balancer/"+balancerID, "")
	_, listed := call(t, h, "GET", "/v2/load-balancer", "")

	reads := []map[string]any{service}
	reads = append(reads, serviceViews(t, balancer)...)
	balancers, _ := listed["load-balancers"].([]any)
	if len(balancers) != 1 {
		t.Fatalf("the list answered %d balancers, want 1", len(balancers))
	}
	first, _ := balancers[0].(map[string]any)
	reads = append(reads, serviceViews(t, first)...)

	for _, read := range reads {
		entries := backendEntries(t, read)
		if len(entries) != 2 {
			t.Fatalf("%d backend entries, want the pool's 2: an empty list reads as a pool with nobody in it", len(entries))
		}
		for _, entry := range entries {
			if _, invented := entry["status"]; invented {
				t.Fatalf("a backend carries a health verdict (%v); nothing here probes one, so both enum values would be invented", entry)
			}
			if address, _ := entry["public-ip"].(string); address == "" {
				t.Fatalf("a backend entry names no address (%v): it identifies nothing", entry)
			}
		}
	}
}

// The backends are the pool's members, and they follow it.
//
// A service names a pool, never a list of machines, so scaling the pool moves
// what the service reports without anybody maintaining a second list.
func TestAServicesBackendsAreItsPoolsMembers(t *testing.T) {
	h := serve(t)
	balancerID, serviceID, poolID := nlbFixture(t, h, 2)

	rec, body := call(t, h, "PUT", "/v2/instance-pool/"+poolID+":scale", `{"size":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("scale the pool: status %d, %v", rec.Code, body)
	}

	_, service := call(t, h, "GET", "/v2/load-balancer/"+balancerID+"/service/"+serviceID, "")
	if got := len(backendEntries(t, service)); got != 3 {
		t.Fatalf("%d backends after a scale to 3, want 3", got)
	}
}

// A pool member carries the public address its pool declares.
//
// It did not until #345, and nothing noticed because nothing read it: a
// service's backends are identified by that address, and members without one
// made every service answer an empty backend list.
func TestAPoolMemberCarriesThePublicAddressItsPoolDeclares(t *testing.T) {
	h := serve(t)

	_, body := call(t, h, "POST", "/v2/instance-pool",
		`{"name":"addressed","size":2,"instance-type":{"id":"1"},"template":{"id":"22222222-2222-4222-8222-222222222222"}}`)
	addressed := referenceID(t, body)

	_, body = call(t, h, "POST", "/v2/instance-pool",
		`{"name":"bare","size":1,"public-ip-assignment":"none","instance-type":{"id":"1"},"template":{"id":"22222222-2222-4222-8222-222222222222"}}`)
	bare := referenceID(t, body)

	seen := map[string]string{}
	for _, id := range []string{addressed, bare} {
		_, listed := call(t, h, "GET", "/v2/instance?manager-id="+id+"&manager-type=instance-pool", "")
		instances, _ := listed["instances"].([]any)
		for _, entry := range instances {
			instance, _ := entry.(map[string]any)
			address, _ := instance["public-ip"].(string)
			name, _ := instance["name"].(string)
			if id == bare {
				if address != "" {
					t.Fatalf("a member of a pool that assigns no public address carries %q", address)
				}
				continue
			}
			if address == "" {
				t.Fatalf("member %s carries no public address, so no service can name it as a backend", name)
			}
			if other, twice := seen[address]; twice {
				t.Fatalf("%s and %s were both given %s", other, name, address)
			}
			seen[address] = name
		}
	}
	if len(seen) != 2 {
		t.Fatalf("%d addressed members, want 2", len(seen))
	}
}

// One address, one holder, across every kind that draws from the pack's block.
//
// The balancer joined that block in #345 and the allocator had to learn about
// it: without the balancer's own scan, the first elastic IP created after a
// balancer is handed the balancer's address, and two objects publish one
// address to two clients.
func TestABalancerAndAnElasticIPNeverShareAnAddress(t *testing.T) {
	h := serve(t)

	_, created := call(t, h, "POST", "/v2/load-balancer", `{"name":"nlb"}`)
	balancerID := referenceID(t, created)
	_, balancer := call(t, h, "GET", "/v2/load-balancer/"+balancerID, "")
	balancerAddress, _ := balancer["ip"].(string)
	if balancerAddress == "" {
		t.Fatalf("the balancer publishes no address: %v", balancer)
	}

	_, created = call(t, h, "POST", "/v2/elastic-ip", `{}`)
	elasticID := referenceID(t, created)
	_, elastic := call(t, h, "GET", "/v2/elastic-ip/"+elasticID, "")
	elasticAddress, _ := elastic["ip"].(string)

	if elasticAddress == balancerAddress {
		t.Fatalf("the elastic IP and the balancer were both given %s", balancerAddress)
	}
}

// A service is reachable through the balancer that holds it, and through no
// other.
//
// The service id is a path segment a client composes. Answering it under a
// balancer that does not hold it would let a client read and edit somebody
// else's service through a well-formed request — well formed is not authorised.
func TestAServiceIsOnlyReachableThroughItsOwnBalancer(t *testing.T) {
	h := serve(t)
	_, serviceID, _ := nlbFixture(t, h, 1)

	_, created := call(t, h, "POST", "/v2/load-balancer", `{"name":"other"}`)
	otherID := referenceID(t, created)

	for _, method := range []string{"GET", "PUT", "DELETE"} {
		body := ""
		if method == "PUT" {
			body = `{"description":"stolen"}`
		}
		rec, _ := call(t, h, method, "/v2/load-balancer/"+otherID+"/service/"+serviceID, body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s through the wrong balancer answered %d, want 404", method, rec.Code)
		}
	}
}

// A balancer takes its services with it.
//
// They are addressable only under its path, so one that outlived its balancer
// could never be read or deleted by any client call. The store's own sweep is
// what says so, which is why Owns declares the relation.
func TestDeletingABalancerTakesItsServices(t *testing.T) {
	h, st := newExoscaleBarrageServer(t)
	balancerID, _, _ := nlbFixture(t, h, 1)

	rec, body := call(t, h, "DELETE", "/v2/load-balancer/"+balancerID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete the balancer: status %d, %v", rec.Code, body)
	}
	if found := storetest.Orphans(st.All(), exoscale.Owns, nil); len(found) != 0 {
		t.Fatalf("a service outlived its balancer: %v", found)
	}
}

// Every service mutation refers to the balancer, and the reference is readable
// as one.
//
// Measured against the Terraform provider rather than reasoned about: egoscale
// v2 passes this reference straight to GetNetworkLoadBalancer and finds the new
// service by diffing the balancer's list. Referring to the service — which is
// what every other mutation of this pack does — made `terraform apply` fail
// with "Get .../v2/load-balancer/<service id>: resource not found".
func TestAServiceMutationReferencesItsBalancer(t *testing.T) {
	h := serve(t)
	balancerID, serviceID, poolID := nlbFixture(t, h, 1)

	mutations := []struct {
		method, path, body string
	}{
		{"POST", "/v2/load-balancer/" + balancerID + "/service", fmt.Sprintf(
			`{"name":"second","instance-pool":{"id":%q},"port":81,"target-port":81}`, poolID)},
		{"PUT", "/v2/load-balancer/" + balancerID + "/service/" + serviceID, `{"port":8081}`},
		{"DELETE", "/v2/load-balancer/" + balancerID + "/service/" + serviceID, ""},
	}
	for _, mutation := range mutations {
		rec, body := call(t, h, mutation.method, mutation.path, mutation.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status %d, %v", mutation.method, mutation.path, rec.Code, body)
		}
		referenced := referenceID(t, body)
		if referenced != balancerID {
			t.Fatalf("%s referenced %s, want the balancer %s: egoscale v2 reads this id as a load balancer",
				mutation.method, referenced, balancerID)
		}
		reference, _ := body["reference"].(map[string]any)
		if command, _ := reference["command"].(string); command != "get-load-balancer" {
			t.Fatalf("%s named the follow-up read %q, want get-load-balancer", mutation.method, command)
		}
		rec, balancer := call(t, h, "GET", "/v2/load-balancer/"+referenced, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("the reference of %s does not read back: status %d, %v", mutation.method, rec.Code, balancer)
		}
	}
}

// The ranges their document declares are refused where upstream refuses them.
//
// A value the real API rejects and this one stores is a plan that converges
// here and fails there, which is the difference this emulator exists to remove.
func TestAServiceRefusesAHealthcheckOutsideItsDeclaredRanges(t *testing.T) {
	h := serve(t)
	balancerID, serviceID, poolID := nlbFixture(t, h, 1)

	for _, check := range []string{
		`{"mode":"tcp","interval":1}`,
		`{"mode":"tcp","timeout":600}`,
		`{"mode":"tcp","retries":99}`,
		`{"mode":"gopher"}`,
		// Refused by encoding/json rather than by a check of ours, because the
		// block is decoded into a typed struct: their schema is closed, and a
		// `uri` stored as a number would come back on a read the emulator's own
		// contract check refuses. Listed here so the refusal is asserted rather
		// than assumed from the type declaration.
		`{"mode":"tcp","uri":7}`,
	} {
		create := fmt.Sprintf(
			`{"name":"bad","instance-pool":{"id":%q},"port":80,"target-port":80,"healthcheck":%s}`, poolID, check)
		rec, body := call(t, h, "POST", "/v2/load-balancer/"+balancerID+"/service", create)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("create with healthcheck %s answered %d, want 400 (%v)", check, rec.Code, body)
		}
		rec, body = call(t, h, "PUT", "/v2/load-balancer/"+balancerID+"/service/"+serviceID,
			fmt.Sprintf(`{"healthcheck":%s}`, check))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("update with healthcheck %s answered %d, want 400 (%v)", check, rec.Code, body)
		}
	}
}

// A service naming a pool nothing holds is refused rather than stored.
//
// Stored, it would answer 200 and then publish a backend list that can never be
// anything but empty, and a client would read that as a pool with no members
// rather than as the reference it got wrong.
func TestAServiceRefusesAPoolThatIsNotThere(t *testing.T) {
	h := serve(t)

	_, created := call(t, h, "POST", "/v2/load-balancer", `{"name":"nlb"}`)
	balancerID := referenceID(t, created)

	rec, body := call(t, h, "POST", "/v2/load-balancer/"+balancerID+"/service",
		`{"name":"web","instance-pool":{"id":"11111111-1111-4111-8111-111111111111"},"port":80,"target-port":80}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (%v)", rec.Code, body)
	}
}

// The balancer round-trips what a client sent, which is the property a second
// Terraform plan converges on.
func TestABalancerRoundTripsItsConfiguration(t *testing.T) {
	h := serve(t)

	_, created := call(t, h, "POST", "/v2/load-balancer", `{"name":"nlb","description":"front","labels":{"tier":"web"}}`)
	balancerID := referenceID(t, created)

	rec, balancer := call(t, h, "PUT", "/v2/load-balancer/"+balancerID, `{"description":"moved on"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status %d, %v", rec.Code, balancer)
	}
	_, balancer = call(t, h, "GET", "/v2/load-balancer/"+balancerID, "")

	want := map[string]any{"name": "nlb", "description": "moved on", "state": "running"}
	for field, expected := range want {
		if got := balancer[field]; got != expected {
			t.Fatalf("%s reads back %v, want %v", field, got, expected)
		}
	}
	labels, _ := balancer["labels"].(map[string]any)
	if labels["tier"] != "web" {
		t.Fatalf("the labels did not survive the update: %v", balancer["labels"])
	}
	encoded, _ := json.Marshal(balancer)
	if len(encoded) == 0 {
		t.Fatal("the balancer does not encode")
	}
}
