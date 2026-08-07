package emulator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

type inventory struct {
	Count     int                     `json:"count"`
	Resources []emulator.ResourceView `json:"resources"`
}

func readInventory(t *testing.T, srv *emulator.Server) inventory {
	t.Helper()
	rec := uiGet(t, srv, "/_feint/resources")
	if rec.Code != http.StatusOK {
		t.Fatalf("the inventory answered %d: %s", rec.Code, rec.Body.String())
	}
	var out inventory
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode the inventory: %v", err)
	}
	return out
}

// A provider the page has never heard of appears in the inventory.
//
// This is rule 5 made observable. The store holds resource.Resource, whose
// provider, kind and state are values rather than types, so the inventory is
// neutral by construction and not by anybody's discipline. The test mounts a
// pack whose name exists nowhere in this repository and asserts that its
// resource is listed with its own vocabulary intact — which is what "a fourth
// pack shows up without a line of the page changing" means, said in code.
func TestTheInventoryShowsAPackThePageHasNeverHeardOf(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, stubPack{name: "fourth-cloud"})
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	if !srv.MountUI(emulator.UI{Addr: "127.0.0.1:4599"}) {
		t.Fatal("the page was not mounted")
	}

	env.Store.Put(&resource.Resource{
		ID:      "wid-1",
		Kind:    "widget",
		Tenant:  resource.Tenant{Provider: "fourth-cloud", Project: "p1", Zone: "somewhere-1"},
		State:   "humming",
		Created: time.Now().UTC(),
		Updated: time.Now().UTC(),
		Attrs:   map[string]any{"colour": "green", "nested": map[string]any{"depth": 2}},
	})

	inv := readInventory(t, srv)
	if inv.Count != 1 || len(inv.Resources) != 1 {
		t.Fatalf("the inventory holds %d resources, want the one that was stored", inv.Count)
	}
	got := inv.Resources[0]
	if got.Provider != "fourth-cloud" || got.Kind != "widget" || got.State != "humming" {
		t.Errorf("the inventory reshaped a pack it does not know: %+v", got)
	}
	if got.Zone != "somewhere-1" || got.Project != "p1" {
		t.Errorf("the tenant did not survive: %+v", got)
	}
	// Whole, not curated. A hand-picked list of interesting fields is a list
	// somebody has to maintain, and the day it goes stale is the day it hides
	// the field somebody was looking for.
	if got.Attrs["colour"] != "green" {
		t.Errorf("attributes were filtered: %+v", got.Attrs)
	}
	if nested, ok := got.Attrs["nested"].(map[string]any); !ok || nested["depth"] == nil {
		t.Errorf("nested attributes were flattened away: %+v", got.Attrs)
	}
}

// An empty store answers an empty inventory rather than null.
//
// The page reads the length of that array on every refresh. A null there is one
// undefined away from a blank region that looks like a bug, on the very first
// screen a new user sees.
func TestTheInventoryOfAnEmptyStoreIsAnEmptyList(t *testing.T) {
	srv, _ := newUIServer(t, "127.0.0.1:4599")
	body := uiGet(t, srv, "/_feint/resources").Body.String()
	if !strings.Contains(body, `"resources":[]`) {
		t.Errorf("an empty store answers %s, want an empty array", body)
	}
}

// Runtime is published by the inventory and by nothing a provider serves.
//
// resource.Resource says Runtime "must never reach a client", and that rule is
// about the provider views: an emulator-side container name in a Scaleway
// response is a field the real cloud does not have and a client could come to
// depend on. The inventory is not a provider view — it is the control plane, on
// loopback, and naming the container that backs a machine is exactly what makes
// it worth opening.
//
// So the opening has to be held where it was opened. Every stored resource is
// stamped, then every GET route of every pack is driven, plus the read actions
// of the pack whose reads are POSTs, and the stamp must appear in none of them.
//
// What it does not cover, said plainly rather than implied: a view reachable
// only through a path this test cannot build, and any non-GET route other than
// the Read* actions below. It covers the reads a client actually makes.
func TestNoProviderViewSerializesRuntime(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env), outscale.New(env), exoscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	handler := srv.Handler()

	drive := func(method, path, body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// One resource per pack, created through the real routes, because a store
	// seeded by hand would not prove that the packs' own serializers ignore the
	// field — only that this test can write a map.
	creates := []struct{ method, path, body string }{
		{"POST", "/instance/v1/zones/fr-par-1/servers", `{"name":"demo","commercial_type":"DEV1-S"}`},
		{"POST", "/api/v1/CreateVms", `{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2"}`},
		{"POST", "/v2/instance", `{"name":"demo","instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},` +
			`"template":{"id":"11111111-1111-4111-8111-111111111111"},"disk-size":10}`},
	}
	for _, c := range creates {
		if status, body := drive(c.method, c.path, c.body); status >= 400 {
			t.Fatalf("%s %s answered %d: %s", c.method, c.path, status, body)
		}
	}

	const stamp = "feint-runtime-stamp-a5f3"
	ids := []string{}
	zones := []string{}
	for _, r := range env.Store.All() {
		if r.Runtime == nil {
			r.Runtime = map[string]string{}
		}
		r.Runtime["machine"] = stamp
		env.Store.Put(r)
		ids = append(ids, r.ID)
		if r.Tenant.Zone != "" {
			zones = append(zones, r.Tenant.Zone)
		}
	}
	if len(ids) == 0 {
		t.Fatal("nothing was created, so nothing was stamped and this test measures nothing")
	}

	// Path parameters are filled from the store rather than from a table written
	// here: the zones are the ones the packs themselves recorded, so this keeps
	// working when a pack changes its default.
	fill := func(path, id string) string {
		out := path
		for {
			open := strings.Index(out, "{")
			if open < 0 {
				return out
			}
			closing := strings.Index(out[open:], "}")
			if closing < 0 {
				return out
			}
			name := out[open+1 : open+closing]
			value := id
			if strings.Contains(name, "zone") || strings.Contains(name, "region") {
				if len(zones) == 0 {
					return ""
				}
				value = zones[0]
			}
			out = out[:open] + value + out[open+closing+1:]
		}
	}

	proofs := 0
	check := func(method, path, body string) {
		status, answer := drive(method, path, body)
		if strings.Contains(answer, stamp) {
			t.Errorf("%s %s serialized the emulator's runtime bookkeeping: %s", method, path, answer)
		}
		if status != http.StatusOK {
			return
		}
		for _, id := range ids {
			if strings.Contains(answer, id) {
				proofs++
				return
			}
		}
	}

	for _, r := range srv.AllRoutes() {
		if r.Method != http.MethodGet {
			// The pack that reads over POST is driven below. Every other verb is
			// left alone: walking Delete* with a body would be a test that
			// dismantles its own fixture halfway through.
			if strings.HasPrefix(lastSegment(r.Path), "Read") {
				check(http.MethodPost, r.Path, `{}`)
			}
			continue
		}
		for _, id := range ids {
			if path := fill(r.Path, id); path != "" {
				check(http.MethodGet, path, "")
			}
		}
	}

	// Without this the whole loop could have 404ed its way to a green run.
	if proofs < len(creates) {
		t.Fatalf("only %d response(s) carried a stamped resource; the views were never reached", proofs)
	}

	// And the inventory does publish it, which is the half that makes the
	// machines region possible.
	if !srv.MountUI(emulator.UI{Addr: "127.0.0.1:4599"}) {
		t.Fatal("the page was not mounted")
	}
	if !strings.Contains(uiGet(t, srv, "/_feint/resources").Body.String(), stamp) {
		t.Error("the inventory hides Runtime, so no page can name the container backing a machine")
	}
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
