package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The protection flag was stored at create, stored again at update, published on
// every read, and consulted by nothing (#212).
//
// Everything below is measured against fr-par-1 rather than reasoned about,
// because the measurement reversed the issue that asked for it. The runs are
// named in the comments of servers.go; what they established is:
//
//	poweroff / stop_in_place / reboot / terminate  400 precondition_failed
//	                                               precondition protected_resource
//	                                               help_message "server is protected"
//	backup, and poweron on a stopped server        accepted
//	DELETE /servers/{id} on a stopped server       204, and a 404 after it
//	allowed_actions, running + protected           ["backup"]
//	allowed_actions, stopped + protected           ["poweron", "backup"]

// protect is the door a client actually uses, PATCH rather than a store poke, so
// the test exercises the same path Terraform does.
func protect(t *testing.T, ts *httptest.Server, zone, id string, on bool) {
	t.Helper()
	body := `{"protected":false}`
	if on {
		body = `{"protected":true}`
	}
	status, out := do(t, ts, "PATCH", zone+"/servers/"+id, body)
	if status != http.StatusOK {
		t.Fatalf("protect(%v): status %d", on, status)
	}
	server, _ := out["server"].(map[string]any)
	if got, _ := server["protected"].(bool); got != on {
		t.Fatalf("protect(%v) answered protected=%v", on, got)
	}
}

func newProtectedServer(t *testing.T, ts *httptest.Server, zone string, running bool) string {
	t.Helper()
	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)
	if running {
		if status, _ := do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweron"}`); status != http.StatusAccepted {
			t.Fatalf("poweron: status %d", status)
		}
	}
	protect(t, ts, zone, id, true)
	return id
}

// Every action that stops or destroys the machine is refused, in the dialect the
// API uses for it.
//
// The body is asserted field by field and not merely the status: the SDK reads
// `type` to pick a struct and `precondition` to tell one failure from another, so
// a 400 carrying the wrong type is an opaque error where a typed one belongs —
// and a client branching on errors.As stops seeing it.
func TestProtectionRefusesEveryStoppingAction(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"
	id := newProtectedServer(t, ts, zone, true)

	for _, action := range []string{"poweroff", "stop_in_place", "reboot", "terminate"} {
		status, out := do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"`+action+`"}`)
		if status != http.StatusBadRequest {
			t.Errorf("%s on a protected server: status %d, want 400", action, status)
			continue
		}
		for field, want := range map[string]string{
			"type":         "precondition_failed",
			"message":      "precondition is not respected",
			"precondition": "protected_resource",
			"help_message": "server is protected",
		} {
			if got, _ := out[field].(string); got != want {
				t.Errorf("%s answered %s=%q, want %q", action, field, got, want)
			}
		}
	}

	// And the machine is still there, running: a refusal that half-applied would
	// be worse than none, since the client is told nothing happened.
	status, out := do(t, ts, "GET", zone+"/servers/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("the protected server is gone: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	if got, _ := server["state"].(string); got != "running" {
		t.Fatalf("after four refused actions the server is %q, want running", got)
	}
}

// The accepting half. A guard that refuses everything passes every attack test
// and breaks the product, and here the API itself draws the line: backup and
// poweron are allowed on a protected server.
func TestProtectionLeavesTheOtherActionsAlone(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"
	id := newProtectedServer(t, ts, zone, false)

	for _, action := range []string{"backup", "poweron"} {
		if status, _ := do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"`+action+`"}`); status != http.StatusAccepted {
			t.Errorf("%s on a protected server: status %d, want 202", action, status)
		}
	}
	status, out := do(t, ts, "GET", zone+"/servers/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	if got, _ := server["state"].(string); got != "running" {
		t.Fatalf("a protected server refused to start: state %q", got)
	}
}

// Once the flag is cleared the machine stops again, so protection is a state and
// not a one-way door. Without this, a guard that never lets go would look right
// to every test above.
func TestClearingTheProtectionRestoresTheActions(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"
	id := newProtectedServer(t, ts, zone, true)

	protect(t, ts, zone, id, false)
	if status, _ := do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweroff"}`); status != http.StatusAccepted {
		t.Fatalf("poweroff after unprotecting: status %d, want 202", status)
	}
}

// The DELETE verb goes through, and this test exists to keep it that way.
//
// #212 asked for the opposite in good faith. Two runs against fr-par-1 answered
// 204 on a server whose protection a fresh GET had just confirmed, so refusing
// here would be an emulator inventing a rule its cloud does not have — which is
// the one failure this project is built to prevent.
func TestProtectionDoesNotBlockTheDeleteVerb(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"
	id := newProtectedServer(t, ts, zone, false)

	if status, _ := do(t, ts, "DELETE", zone+"/servers/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE on a protected server: status %d, want 204 (measured on fr-par-1)", status)
	}
	if status, _ := do(t, ts, "GET", zone+"/servers/"+id, ""); status != http.StatusNotFound {
		t.Fatalf("the server survived its delete: status %d", status)
	}
}

// allowed_actions carries the same news as the refusals, and a client reads it to
// decide what to offer rather than firing an action to find out.
func TestProtectionShrinksTheAllowedActions(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	list := func(id string) map[string]bool {
		t.Helper()
		status, out := do(t, ts, "GET", zone+"/servers/"+id, "")
		if status != http.StatusOK {
			t.Fatalf("get: status %d", status)
		}
		server, _ := out["server"].(map[string]any)
		actions, _ := server["allowed_actions"].([]any)
		set := make(map[string]bool, len(actions))
		for _, action := range actions {
			if name, ok := action.(string); ok {
				set[name] = true
			}
		}
		return set
	}

	id := newProtectedServer(t, ts, zone, true)
	running := list(id)
	for _, gone := range []string{"poweroff", "stop_in_place", "reboot", "terminate"} {
		if running[gone] {
			t.Errorf("a protected running server still advertises %s: %v", gone, running)
		}
	}
	if !running["backup"] {
		t.Errorf("a protected running server advertises no backup: %v", running)
	}

	// Unprotected, the same server lists terminate — which this pack omitted
	// until the measurement listed it, so a client reading the list to decide
	// whether it could destroy the server was told no.
	protect(t, ts, zone, id, false)
	if free := list(id); !free["terminate"] {
		t.Errorf("an unprotected running server advertises no terminate: %v", free)
	}
}
