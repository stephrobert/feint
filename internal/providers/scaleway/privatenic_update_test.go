package scaleway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// A clock that advances one second per read, because the shared test Env pins
// Now() and a pinned clock cannot show a date moving.
//
// That pinning is why TestAPrivateNICAnswersTheDateItLastChanged says out loud
// that it does not assert the date moves: with no route modifying a NIC and no
// clock advancing, such a test could not have failed. Only one of those two is
// fixed by serving the update, so the other is fixed here.
func newTickingServer(t *testing.T) *httptest.Server {
	t.Helper()
	var ticks atomic.Int64
	var seq int
	env := &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000+ticks.Add(1), 0).UTC() },
		NewID: func() string {
			seq++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
		},
	}
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// UpdatePrivateNIC, and the decline whose reason had stopped being true (#624).
//
// It was refused because "the pack stores no tag on a private NIC, so it would
// answer success over a field nothing reads back". Both halves were false by the
// time a consumer measured them: createPrivateNIC has stored the tags all along,
// listPrivateNICs filters on them, and privateNICView copies them onto all three
// doors.
//
// A decline is a decision and may be revisited. A decline's *reason* is a claim
// about the code, and it stops being one the day the code moves — which is why
// the reason is the part that had to change whatever the decision was.
func TestUpdatingAPrivateNICCarriesItsTagsAndItsDate(t *testing.T) {
	ts := newTickingServer(t)
	const zone = "/instance/v1/zones/fr-par-1"
	const region = "/vpc/v2/regions/fr-par"

	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"tagged","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	status, out = do(t, ts, "POST", region+"/private-networks",
		`{"name":"pn","subnets":["10.191.0.0/24"]}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create private network: status %d", status)
	}
	pnID, _ := out["id"].(string)

	status, out = do(t, ts, "POST", zone+"/servers/"+id+"/private_nics",
		`{"private_network_id":"`+pnID+`","tags":["before"]}`)
	if status != http.StatusCreated {
		t.Fatalf("attach: status %d (%v)", status, out)
	}
	nic, _ := out["private_nic"].(map[string]any)
	nicID, _ := nic["id"].(string)
	created, _ := nic["creation_date"].(string)

	// The write half, which is the only one that was missing.
	status, out = do(t, ts, "PATCH", zone+"/servers/"+id+"/private_nics/"+nicID,
		`{"tags":["after","second"]}`)
	if status != http.StatusOK {
		t.Fatalf("update: status %d (%v)", status, out)
	}
	// The envelope the cloud answers. Three witnesses disagree, and the recording
	// wins: see TestUpdatingAPrivateNICAnswersTheEnvelopeTheCloudAnswers below.
	updated, _ := out["private_nic"].(map[string]any)
	if got, _ := updated["id"].(string); got != nicID {
		t.Errorf("the update answered %v, want the NIC it changed inside a private_nic envelope", out)
	}

	status, out = do(t, ts, "GET", zone+"/servers/"+id+"/private_nics/"+nicID, "")
	if status != http.StatusOK {
		t.Fatalf("get: status %d", status)
	}
	nic, _ = out["private_nic"].(map[string]any)
	tags, _ := nic["tags"].([]any)
	if len(tags) != 2 {
		t.Fatalf("the NIC carries %v after an update that set two tags", nic["tags"])
	}
	if first, _ := tags[0].(string); first != "after" {
		t.Errorf("the tags came back as %v", nic["tags"])
	}

	// And the half the code was already written for. privateNICView serves
	// modification_date from res.Updated and says out loud that no route modifies
	// a NIC, so the two dates carried the same instant on every answer a client
	// could obtain — an assertion no state of the code could fail. This route is
	// what makes them differ, and this is that assertion becoming measurable.
	modified, _ := nic["modification_date"].(string)
	if modified == "" || created == "" {
		t.Fatalf("a NIC answers creation_date %q and modification_date %q", created, modified)
	}
	if modified == created {
		t.Errorf("modification_date is still the creation instant (%s) after an update: the field "+
			"is served from res.Updated and nothing was moving it", modified)
	}

	// The filter reads what the update wrote, which is the third place the old
	// reason said nothing read the field.
	status, out = do(t, ts, "GET", zone+"/servers/"+id+"/private_nics?tags=after", "")
	if status != http.StatusOK {
		t.Fatalf("filtered list: status %d", status)
	}
	if total, _ := out["total_count"].(float64); total != 1 {
		t.Errorf("the tag filter found %v NIC(s) for a tag the update had just written", total)
	}
}

// A request that names no tag changes nothing, which is what makes the module a
// consumer builds on this idempotent: read, compare, write only on a difference,
// and a second run reports no change.
func TestUpdatingAPrivateNICWithoutTagsLeavesThemAlone(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"
	const region = "/vpc/v2/regions/fr-par"

	_, out := do(t, ts, "POST", zone+"/servers", `{"name":"kept","commercial_type":"DEV1-S"}`)
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)
	_, out = do(t, ts, "POST", region+"/private-networks", `{"name":"pn2","subnets":["10.192.0.0/24"]}`)
	pnID, _ := out["id"].(string)
	_, out = do(t, ts, "POST", zone+"/servers/"+id+"/private_nics",
		`{"private_network_id":"`+pnID+`","tags":["keep"]}`)
	nic, _ := out["private_nic"].(map[string]any)
	nicID, _ := nic["id"].(string)

	if status, _ := do(t, ts, "PATCH", zone+"/servers/"+id+"/private_nics/"+nicID, `{}`); status != http.StatusOK {
		t.Fatalf("an empty update was refused")
	}
	_, out = do(t, ts, "GET", zone+"/servers/"+id+"/private_nics/"+nicID, "")
	nic, _ = out["private_nic"].(map[string]any)
	tags, _ := nic["tags"].([]any)
	if len(tags) != 1 {
		t.Errorf("an update naming no tag cleared them: %v", nic["tags"])
	}
}

// Which envelope the update answers, and why it is not the one two of its three
// witnesses give.
//
// The Go SDK decodes this body into PrivateNIC directly (instance_sdk.go:6751),
// and the portal document declares the same bare shape. A recording of a real
// fr-par account answers the wrapper: corpus/scaleway/scw-billed-shapes.jsonl
// seq 24, a PATCH on a private NIC answered 200 with {"private_nic": …}.
//
// The recording wins, because it is the cloud itself rather than a description
// of it. What that costs is worth stating: `scw instance private-nic update`
// cannot read this answer, and it cannot read it against the real Scaleway
// either. An emulator serving the bare object would be silently repairing a
// client bug, and the first person to meet it in production would meet it alone.
func TestUpdatingAPrivateNICAnswersTheEnvelopeTheCloudAnswers(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"
	const region = "/vpc/v2/regions/fr-par"

	_, out := do(t, ts, "POST", zone+"/servers", `{"name":"env","commercial_type":"DEV1-S"}`)
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)
	_, out = do(t, ts, "POST", region+"/private-networks", `{"name":"pn3","subnets":["10.193.0.0/24"]}`)
	pnID, _ := out["id"].(string)
	_, out = do(t, ts, "POST", zone+"/servers/"+id+"/private_nics", `{"private_network_id":"`+pnID+`"}`)
	nic, _ := out["private_nic"].(map[string]any)
	nicID, _ := nic["id"].(string)

	status, body := do(t, ts, "PATCH", zone+"/servers/"+id+"/private_nics/"+nicID, `{"tags":["x"]}`)
	if status != http.StatusOK {
		t.Fatalf("update: status %d", status)
	}
	wrapped, ok := body["private_nic"].(map[string]any)
	if !ok {
		t.Fatalf("the update answered the bare object; the recorded cloud answers a private_nic "+
			"envelope, and matching the SDK instead would repair a client bug in silence: %v", body)
	}
	if got, _ := wrapped["id"].(string); got != nicID {
		t.Errorf("the envelope carries %q, want the NIC that was updated", got)
	}
}
