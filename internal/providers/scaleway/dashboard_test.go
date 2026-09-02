package scaleway_test

import (
	"net/http"
	"testing"
)

// Two declines #626 asked to be arbitrated, withdrawn on a measurement rather
// than on an argument.
//
// Both had been filed under "capacity and quotas are the provider's fleet, and a
// local emulator that answered would be inventing headroom". That sentence still
// holds for GetServerTypesAvailability — fr-par-1 answered `shortage` for every
// type on 2026-09-01 — and it held for neither of these.

// compatible-types answers a list of type names and no headroom at all, which is
// what took it out of the capacity family. Derived from the catalogue this pack
// already serves, so there is no second list to keep in step with the first.
func TestCompatibleTypesComeFromTheCatalogueAndExcludeTheServersOwn(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"resizable","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	status, out = do(t, ts, "GET", zone+"/servers/"+id+"/compatible-types", "")
	if status != http.StatusOK {
		t.Fatalf("compatible-types: status %d (%v)", status, out)
	}
	types, _ := out["compatible_types"].([]any)
	if len(types) == 0 {
		t.Fatal("a server can move to no type at all")
	}

	// Its own type is not a move, which is what the real answer does.
	for _, raw := range types {
		if name, _ := raw.(string); name == "DEV1-S" {
			t.Errorf("the server's own type is listed as one it can move to: %v", types)
		}
	}

	// Every name is one the catalogue serves, or a client would resize to a type
	// the create then refuses — the plausible-wrong answer this route was
	// declined to avoid, arriving through the door that replaced the decline.
	status, catalogue := do(t, ts, "GET", zone+"/products/servers", "")
	if status != http.StatusOK {
		t.Fatalf("products/servers: status %d", status)
	}
	served, _ := catalogue["servers"].(map[string]any)
	for _, raw := range types {
		name, _ := raw.(string)
		if _, known := served[name]; !known {
			t.Errorf("compatible-types offers %q and the catalogue does not serve it", name)
		}
	}
}

// The counters, and the claim the decline rested on: that every total would be
// "short by the unemulated remainder with nothing saying which". A live read of
// fr-par-1 showed the remainder is empty — every key it answers names a family
// this pack serves — so the counts are computed from the store and are true.
func TestTheDashboardCountsWhatTheStoreHolds(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	read := func() map[string]any {
		t.Helper()
		status, out := do(t, ts, "GET", zone+"/dashboard", "")
		if status != http.StatusOK {
			t.Fatalf("dashboard: status %d (%v)", status, out)
		}
		d, _ := out["dashboard"].(map[string]any)
		if d == nil {
			t.Fatalf("the dashboard answers no dashboard object: %v", out)
		}
		return d
	}
	count := func(d map[string]any, key string) float64 {
		v, ok := d[key].(float64)
		if !ok {
			t.Fatalf("the dashboard carries no %s: %v", key, d)
		}
		return v
	}

	before := read()
	if count(before, "servers_count") != 0 {
		t.Fatalf("a fresh account already holds servers: %v", before)
	}

	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"counted","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	after := read()
	if count(after, "servers_count") != 1 {
		t.Errorf("servers_count is %v after one create", after["servers_count"])
	}
	// volumes_count is the *instance* volumes, and a server's root disk is not
	// one of them: since #365 it is an sbs_volume in the block product, like the
	// cloud's. So a create alone leaves this counter at zero, and what moves it
	// is a disk a client asked for.
	if count(after, "volumes_count") != 0 {
		t.Errorf("volumes_count is %v after a create whose root disk is an sbs_volume", after["volumes_count"])
	}
	if status, out := do(t, ts, "POST", zone+"/volumes",
		`{"name":"own","volume_type":"l_ssd","size":10000000000}`); status != http.StatusCreated {
		t.Fatalf("volume create: status %d (%v)", status, out)
	}
	withVolume := read()
	if count(withVolume, "volumes_count") != 1 {
		t.Errorf("volumes_count is %v after one instance volume", withVolume["volumes_count"])
	}
	// l_ssd, not b_ssd: since #393 CreateVolume refuses the type the cloud
	// retired, so the b_ssd counter is one no create can move and the l_ssd one
	// is what a client now watches.
	if count(withVolume, "volumes_l_ssd_count") != 1 {
		t.Errorf("volumes_l_ssd_count is %v after one l_ssd volume", withVolume["volumes_l_ssd_count"])
	}
	if count(withVolume, "volumes_b_ssd_count") != 0 {
		t.Errorf("volumes_b_ssd_count is %v and no create can mint one", withVolume["volumes_b_ssd_count"])
	}
	byType, _ := after["servers_by_types"].(map[string]any)
	if got, _ := byType["DEV1-S"].(float64); got != 1 {
		t.Errorf("servers_by_types is %v, want one DEV1-S", after["servers_by_types"])
	}
	if count(after, "running_servers_count") != 0 {
		t.Errorf("a created-but-stopped server counts as running: %v", after["running_servers_count"])
	}

	if status, _ := do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron: status %d", status)
	}
	if count(read(), "running_servers_count") != 1 {
		t.Error("running_servers_count did not follow the server that started")
	}

	// scratch is a type this API mints and this test never asked for, so its
	// counter is zero because the store holds none. It was a constant until #393
	// made both l_ssd and scratch creatable, at which point a hardcoded zero
	// would have answered none while the store held them.
	final := read()
	if count(final, "volumes_scratch_count") != 0 {
		t.Errorf("volumes_scratch_count is %v and nothing here made one", final["volumes_scratch_count"])
	}
}

// The total, which the count alone does not prove.
//
// The first version read the stored size with a bare .(int64) and answered zero
// while the count answered one: a size set in Go is an int64 and one restored
// from a snapshot is a float64, because JSON has a single number type. A client
// sums this against a quota, so a silent zero is exactly the plausible-wrong
// answer this route was declined to avoid.
func TestTheDashboardTotalsTheSizesItCounts(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	if status, out := do(t, ts, "POST", zone+"/volumes",
		`{"name":"sized","volume_type":"l_ssd","size":10000000000}`); status != http.StatusCreated {
		t.Fatalf("volume create: status %d (%v)", status, out)
	}
	status, out := do(t, ts, "GET", zone+"/dashboard", "")
	if status != http.StatusOK {
		t.Fatalf("dashboard: status %d", status)
	}
	d, _ := out["dashboard"].(map[string]any)
	total, _ := d["volumes_l_ssd_total_size"].(float64)
	if total != 10000000000 {
		t.Errorf("volumes_l_ssd_total_size is %v for one 10 GB volume; the count says %v",
			d["volumes_l_ssd_total_size"], d["volumes_l_ssd_count"])
	}
}
