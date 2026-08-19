package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #320: Server.public_ips is a list, and Terraform stores it as one — the
// provider rebuilds ip_ids index by index from it (flattenServerIPIDs,
// provider 2.43.0 types.go:99). The emulator answered the attached addresses
// in store order, so a create that named [b, a] read back [a, b] and the
// talos stack re-planned the same two-way swap for ever: the apply path is
// set-based UpdateIP calls (provider server.go:1288) and cannot reorder, so
// the diff never converged. The order is the client's, not the store's.

const ipOrderZone = "/instance/v1/zones/fr-par-1"

// createFlexibleIP books one address and returns its id.
func createFlexibleIP(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	status, out := do(t, ts, "POST", ipOrderZone+"/ips", `{}`)
	if status != http.StatusCreated {
		t.Fatalf("create ip: %d %v", status, out)
	}
	ip, _ := out["ip"].(map[string]any)
	id, _ := ip["id"].(string)
	if id == "" {
		t.Fatalf("no ip id in %v", out)
	}
	return id
}

// publicIPIDs reads the id of every entry the server's public_ips carries, in
// the order the emulator serves them.
func publicIPIDs(t *testing.T, srv map[string]any) []string {
	t.Helper()
	entries, _ := srv["public_ips"].([]any)
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		ip, _ := entry.(map[string]any)
		id, _ := ip["id"].(string)
		out = append(out, id)
	}
	return out
}

func wantIPOrder(t *testing.T, got, want []string, when string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s: public_ips order is %v, the client named %v", when, got, want)
	}
}

// A server created with public_ips [c, a] answers [c, a] on the create
// response and on every later GET — and [a, c] answers [a, c]: whichever
// order the client names is the one served, so at least one of the two
// subtests disagrees with any fixed store order.
func TestPublicIPsKeepTheOrderTheCreateNamed(t *testing.T) {
	ts := newTestServer(t)
	a := createFlexibleIP(t, ts)
	createFlexibleIP(t, ts) // b, attached to nothing: order must not lean on the full list
	c := createFlexibleIP(t, ts)

	for _, named := range [][]string{{c, a}, {a, c}} {
		body := `{"name":"ordered","commercial_type":"DEV1-S","image":"ubuntu_jammy",` +
			`"public_ips":["` + named[0] + `","` + named[1] + `"]}`
		status, out := do(t, ts, "POST", ipOrderZone+"/servers", body)
		if status != http.StatusCreated {
			t.Fatalf("create server: %d %v", status, out)
		}
		srv, _ := out["server"].(map[string]any)
		id, _ := srv["id"].(string)
		wantIPOrder(t, publicIPIDs(t, srv), named, "create response")

		// The deprecated singular is the SDK's own "first address", so it
		// follows the named order too.
		first, _ := srv["public_ip"].(map[string]any)
		if firstID, _ := first["id"].(string); firstID != named[0] {
			t.Errorf("public_ip is %s, the first named address is %s", firstID, named[0])
		}

		_, out = do(t, ts, "GET", ipOrderZone+"/servers/"+id, "")
		srv, _ = out["server"].(map[string]any)
		wantIPOrder(t, publicIPIDs(t, srv), named, "read after create")

		// Release the pair for the next round.
		do(t, ts, "POST", ipOrderZone+"/servers/"+id+"/action", `{"action":"terminate"}`)
	}
}

// An address attached on its own — PATCH /ips naming the server — joins the
// end of the list instead of shuffling what the create ordered.
func TestALaterAttachJoinsTheEndOfTheList(t *testing.T) {
	ts := newTestServer(t)
	a := createFlexibleIP(t, ts)
	b := createFlexibleIP(t, ts)
	c := createFlexibleIP(t, ts)

	_, out := do(t, ts, "POST", ipOrderZone+"/servers",
		`{"name":"grows","commercial_type":"DEV1-S","image":"ubuntu_jammy","public_ips":["`+b+`","`+a+`"]}`)
	srv, _ := out["server"].(map[string]any)
	id, _ := srv["id"].(string)

	if status, out := do(t, ts, "PATCH", ipOrderZone+"/ips/"+c, `{"server":"`+id+`"}`); status != http.StatusOK {
		t.Fatalf("attach: %d %v", status, out)
	}

	_, out = do(t, ts, "GET", ipOrderZone+"/servers/"+id, "")
	srv, _ = out["server"].(map[string]any)
	wantIPOrder(t, publicIPIDs(t, srv), []string{b, a, c}, "read after a later attach")
}

// UpdateServer.public_ips is the SDK's "list of reserved IP IDs to attach to
// the Instance" (instance_sdk.go:3961). It was declared upstream and read by
// nobody here, so a PATCH naming it answered 200 and changed nothing. It now
// makes the attachments be exactly the list, in the list's order: dropped
// addresses detach and survive, and the response reads back the new order.
func TestUpdateServerPublicIPsReconcilesAttachmentsInOrder(t *testing.T) {
	ts := newTestServer(t)
	a := createFlexibleIP(t, ts)
	b := createFlexibleIP(t, ts)
	c := createFlexibleIP(t, ts)

	_, out := do(t, ts, "POST", ipOrderZone+"/servers",
		`{"name":"reconciled","commercial_type":"DEV1-S","image":"ubuntu_jammy","public_ips":["`+a+`","`+b+`"]}`)
	srv, _ := out["server"].(map[string]any)
	id, _ := srv["id"].(string)

	status, out := do(t, ts, "PATCH", ipOrderZone+"/servers/"+id, `{"public_ips":["`+c+`","`+a+`"]}`)
	if status != http.StatusOK {
		t.Fatalf("update: %d %v", status, out)
	}
	srv, _ = out["server"].(map[string]any)
	wantIPOrder(t, publicIPIDs(t, srv), []string{c, a}, "update response")

	_, out = do(t, ts, "GET", ipOrderZone+"/servers/"+id, "")
	srv, _ = out["server"].(map[string]any)
	wantIPOrder(t, publicIPIDs(t, srv), []string{c, a}, "read after update")

	// The dropped address detached; it was not deleted, because a reserved
	// address outlives its attachment.
	_, out = do(t, ts, "GET", ipOrderZone+"/ips/"+b, "")
	ip, _ := out["ip"].(map[string]any)
	if ip["server"] != nil || ip["state"] != "detached" {
		t.Errorf("the dropped address still carries server=%v state=%v", ip["server"], ip["state"])
	}
}

// An unknown id in UpdateServer.public_ips is refused before anything moves:
// a 404 that had already detached half the list would leave the server in a
// state neither the request nor the previous one described.
func TestUpdateServerRefusesAnUnknownPublicIPUntouched(t *testing.T) {
	ts := newTestServer(t)
	a := createFlexibleIP(t, ts)
	b := createFlexibleIP(t, ts)

	_, out := do(t, ts, "POST", ipOrderZone+"/servers",
		`{"name":"kept","commercial_type":"DEV1-S","image":"ubuntu_jammy","public_ips":["`+b+`","`+a+`"]}`)
	srv, _ := out["server"].(map[string]any)
	id, _ := srv["id"].(string)

	status, _ := do(t, ts, "PATCH", ipOrderZone+"/servers/"+id,
		`{"public_ips":["`+a+`","00000000-dead-4000-8000-000000000000"]}`)
	if status != http.StatusNotFound {
		t.Fatalf("an unknown ip id answered %d, want 404", status)
	}

	_, out = do(t, ts, "GET", ipOrderZone+"/servers/"+id, "")
	srv, _ = out["server"].(map[string]any)
	wantIPOrder(t, publicIPIDs(t, srv), []string{b, a}, "read after the refused update")
}
