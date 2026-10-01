package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// SCIM, served end to end since 2026-10-01 (#800).
//
// The scan found `GetScimToken` new upstream while its six neighbours were
// declined. Serving that one alone was the option that looked cheapest and was
// the worst: a route answering 404 to every identifier, counted as implemented,
// with nothing able to create the token it reads. These tests exist to hold the
// chain that makes it reachable — which is the only thing that turns the
// coverage number back into a fact.

const scimOrg = "/iam/v1alpha1/organizations/11111111-1111-1111-1111-111111111111/scim"

// enabledScim turns SCIM on and answers its identifier.
func enabledScim(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	status, body := do(t, ts, "POST", scimOrg, `{}`)
	if status != http.StatusOK {
		t.Fatalf("enable scim: expected 200, got %d (%v)", status, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("enable scim: no id in %v", body)
	}
	return id
}

// The whole chain, in the order a client walks it, ending on the operation the
// scan found: enable, create a token, read it back by its own identifier.
//
// This is the test that makes `GetScimToken` mean something. Without the three
// calls before it, the only answer it could give is 404.
func TestScimTokenIsReachableThroughTheChainThatCreatesIt(t *testing.T) {
	ts := newTestServer(t)
	scimID := enabledScim(t, ts)

	status, created := do(t, ts, "POST", "/iam/v1alpha1/scim/"+scimID+"/tokens", `{}`)
	if status != http.StatusOK {
		t.Fatalf("create token: expected 200, got %d (%v)", status, created)
	}
	token, _ := created["token"].(map[string]any)
	tokenID, _ := token["id"].(string)
	if tokenID == "" {
		t.Fatalf("create token: no token id in %v", created)
	}
	if token["scim_id"] != scimID {
		t.Errorf("the token names %v as its configuration, not %s: %v", token["scim_id"], scimID, token)
	}

	status, read := do(t, ts, "GET", "/iam/v1alpha1/scim-tokens/"+tokenID, "")
	if status != http.StatusOK {
		t.Fatalf("get token: expected 200, got %d (%v) — this is the operation the scan found, "+
			"and a 404 here is what serving it alone would have answered for ever", status, read)
	}
	if read["id"] != tokenID {
		t.Errorf("the read answered a different token: %v", read)
	}

	// And it is listed under its configuration.
	status, list := do(t, ts, "GET", "/iam/v1alpha1/scim/"+scimID+"/tokens", "")
	if status != http.StatusOK {
		t.Fatalf("list tokens: expected 200, got %d (%v)", status, list)
	}
	if got := list["total_count"]; got != float64(1) {
		t.Errorf("the list counts %v tokens rather than one: %v", got, list)
	}
}

// Enabling twice answers the configuration that exists.
//
// One per organization upstream, and the GET door takes an organization rather
// than an identifier: a second configuration would be one no client could ever
// read back.
func TestEnablingScimTwiceAnswersTheSameConfiguration(t *testing.T) {
	ts := newTestServer(t)
	first := enabledScim(t, ts)
	second := enabledScim(t, ts)
	if first != second {
		t.Errorf("enabling twice made two configurations, %s then %s, and the GET door can only "+
			"reach one of them", first, second)
	}

	status, read := do(t, ts, "GET", scimOrg, "")
	if status != http.StatusOK {
		t.Fatalf("get scim: expected 200, got %d (%v)", status, read)
	}
	if read["id"] != first {
		t.Errorf("the organization reads %v as its configuration, not %s", read["id"], first)
	}
}

// The bearer token is answered once, and no read ever answers it again.
//
// That is how the real product behaves, and it is also the only shape that
// leaves no secret in the store for `PUT /_feint/state` to hand back — the rule
// this repository holds about restored state being untrusted input cuts both
// ways, and the cheapest way to honour it is to keep nothing.
func TestCreateScimTokenAnswersABearerTokenOnlyOnce(t *testing.T) {
	ts := newTestServer(t)
	scimID := enabledScim(t, ts)

	status, created := do(t, ts, "POST", "/iam/v1alpha1/scim/"+scimID+"/tokens", `{}`)
	if status != http.StatusOK {
		t.Fatalf("create token: expected 200, got %d (%v)", status, created)
	}
	bearer, _ := created["bearer_token"].(string)
	if bearer == "" {
		t.Fatalf("the create answered no bearer_token, which is the only thing a directory "+
			"could authenticate with: %v", created)
	}

	token, _ := created["token"].(map[string]any)
	tokenID, _ := token["id"].(string)
	_, read := do(t, ts, "GET", "/iam/v1alpha1/scim-tokens/"+tokenID, "")
	if _, present := read["bearer_token"]; present {
		t.Errorf("a read answered the bearer token again: %v", read)
	}
	// And it is not kept where a snapshot would hand it back.
	status, state := do(t, ts, "GET", "/_feint/state", "")
	if status == http.StatusOK {
		if raw, found := state["resources"]; found {
			if containsString(raw, bearer) {
				t.Errorf("the bearer token is in the emulator's own state, which `PUT /_feint/state` " +
					"restores verbatim into another instance")
			}
		}
	}
}

// Deleting the configuration takes its tokens with it.
//
// A token whose `scim_id` names nothing would still be answered by
// GetScimToken, and upstream has no operation that would ever remove it: there
// is no DeleteScimTokens, so nothing else could.
func TestDeletingScimRemovesItsTokens(t *testing.T) {
	ts := newTestServer(t)
	scimID := enabledScim(t, ts)

	_, created := do(t, ts, "POST", "/iam/v1alpha1/scim/"+scimID+"/tokens", `{}`)
	token, _ := created["token"].(map[string]any)
	tokenID, _ := token["id"].(string)
	if tokenID == "" {
		t.Fatalf("create token: no id in %v", created)
	}

	status, body := do(t, ts, "DELETE", "/iam/v1alpha1/scim/"+scimID, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete scim: expected 204, got %d (%v)", status, body)
	}
	if status, left := do(t, ts, "GET", "/iam/v1alpha1/scim-tokens/"+tokenID, ""); status != http.StatusNotFound {
		t.Errorf("a token outlived the configuration it authenticates to: got %d (%v)", status, left)
	}
	// The organization has no configuration any more.
	if status, read := do(t, ts, "GET", scimOrg, ""); status != http.StatusNotFound {
		t.Errorf("the organization still reads a configuration after it was deleted: %d (%v)", status, read)
	}
}

// A token of no configuration is not found, and a configuration identifier that
// is not this account's is not either: both doors take an identifier from the
// client, and neither trusts it because it parsed.
func TestScimRefusesIdentifiersItNeverIssued(t *testing.T) {
	ts := newTestServer(t)

	if status, _ := do(t, ts, "GET", "/iam/v1alpha1/scim-tokens/22222222-2222-2222-2222-222222222222", ""); status != http.StatusNotFound {
		t.Errorf("an unknown token answered %d rather than 404", status)
	}
	// Before SCIM is enabled at all.
	if status, _ := do(t, ts, "GET", scimOrg, ""); status != http.StatusNotFound {
		t.Errorf("an organization with SCIM off answered %d rather than 404", status)
	}
	scimID := enabledScim(t, ts)
	if status, _ := do(t, ts, "POST", "/iam/v1alpha1/scim/"+scimID+"-nope/tokens", `{}`); status != http.StatusNotFound {
		t.Errorf("a create under an unknown configuration answered %d rather than 404", status)
	}
}

// containsString reports whether any string anywhere inside a decoded JSON value
// equals the needle.
func containsString(value any, needle string) bool {
	switch typed := value.(type) {
	case string:
		return typed == needle
	case []any:
		for _, item := range typed {
			if containsString(item, needle) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if containsString(item, needle) {
				return true
			}
		}
	}
	return false
}
