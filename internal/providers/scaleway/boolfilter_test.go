package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A boolean filter reads every spelling the real API reads, and refuses the ones
// it refuses (#630).
//
// The filter compared against "true" alone, on the ground that this is what the
// Go SDK writes. It is not what every client writes: the official Python SDK
// renders a Python True as `True`, and requests puts that on the wire, so
// `list_i_ps_all(attached=True)` — the documented way to ask the question from
// Python — matched nothing here and answered an empty inventory.
//
// Both halves are measured against fr-par on 2026-09-01 rather than reasoned
// about. The real API accepts true, True and 1, and refuses `banana` with 400
// and strconv's own error text, because it is a Go service using
// strconv.ParseBool.

func anAttachedAddress(t *testing.T, ts *httptest.Server) {
	t.Helper()
	const zone = "/instance/v1/zones/fr-par-1"
	const region = "/vpc/v2/regions/fr-par"

	status, out := do(t, ts, "POST", region+"/private-networks",
		`{"name":"filtered","subnets":["10.194.0.0/24"]}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("private network: status %d", status)
	}
	pnID, _ := out["id"].(string)

	status, out = do(t, ts, "POST", zone+"/servers", `{"name":"filtered","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("server: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	if status, out := do(t, ts, "POST", zone+"/servers/"+id+"/private_nics",
		`{"private_network_id":"`+pnID+`"}`); status != http.StatusCreated {
		t.Fatalf("attach: status %d (%v)", status, out)
	}
}

// Every spelling Go accepts partitions the set the same way. The listing is the
// subject: a filter that matches nothing looks exactly like an empty fleet, and
// that is what the reporter's inventory saw.
func TestABooleanFilterReadsEverySpellingTheCloudReads(t *testing.T) {
	ts := newTestServer(t)
	anAttachedAddress(t, ts)

	total := func(value string) float64 {
		t.Helper()
		status, out := do(t, ts, "GET", "/ipam/v1/regions/fr-par/ips?attached="+value, "")
		if status != http.StatusOK {
			t.Fatalf("attached=%s answered %d (%v)", value, status, out)
		}
		n, ok := out["total_count"].(float64)
		if !ok {
			t.Fatalf("attached=%s carries no total_count: %v", value, out)
		}
		return n
	}

	attached := total("true")
	if attached < 1 {
		t.Fatalf("attached=true found %v addresses; the fixture attached one", attached)
	}
	// The spelling the Python SDK sends, and the ones strconv also takes. Each is
	// asserted against the lowercase answer rather than against a constant: what
	// matters is that they agree, not what the number is.
	for _, spelling := range []string{"True", "1", "t", "T", "TRUE"} {
		if got := total(spelling); got != attached {
			t.Errorf("attached=%s found %v where attached=true found %v", spelling, got, attached)
		}
	}
	free := total("false")
	for _, spelling := range []string{"False", "0", "f", "FALSE"} {
		if got := total(spelling); got != free {
			t.Errorf("attached=%s found %v where attached=false found %v", spelling, got, free)
		}
	}
	// And the filter still partitions rather than matching everything, or the
	// parsing fix would have turned a wrong answer into a useless one.
	if attached == free {
		t.Errorf("attached=true and attached=false both answer %v: the filter stopped filtering", attached)
	}
}

// The second half, and the one this milestone is named for: an unparseable value
// is a client mistake, and an empty page says the fleet is empty instead.
//
// The two dialects are the real API's own, measured on fr-par: instance/v1
// answers its typed invalid_arguments, and ipam/v1, vpc/v2, vpc-gw/v2, lb/v1,
// block/v1 and iam/v1alpha1 answer strconv's error verbatim. Copying one onto
// the other would be inventing a format.
func TestAnUnparseableBooleanIsRefusedInEachProductsOwnDialect(t *testing.T) {
	ts := newTestServer(t)

	t.Run("the products that surface strconv", func(t *testing.T) {
		for _, c := range []struct{ path, field string }{
			{"/ipam/v1/regions/fr-par/ips?attached=banana", "attached"},
			{"/vpc/v2/regions/fr-par/vpcs?is_default=banana", "is_default"},
		} {
			status, out := do(t, ts, "GET", c.path, "")
			if status != http.StatusBadRequest {
				t.Errorf("%s answered %d, want 400: an empty page says the fleet is empty, "+
					"which is a different and wrong statement", c.path, status)
				continue
			}
			msg, _ := out["message"].(string)
			want := `parsing field "` + c.field + `": strconv.ParseBool: parsing "banana": invalid syntax`
			if msg != want {
				t.Errorf("%s answered %q, want the message fr-par answers: %q", c.path, msg, want)
			}
			// No type key at all, which is the shape the cloud answers; an empty
			// one would be a shape it never sends.
			if _, typed := out["type"]; typed {
				t.Errorf("%s carries a type key: %v", c.path, out)
			}
		}
	})

	t.Run("instance/v1, which answers its own typed error", func(t *testing.T) {
		for _, c := range []struct{ path, field string }{
			{"/instance/v1/zones/fr-par-1/images?public=banana", "public"},
			{"/instance/v1/zones/fr-par-1/servers?without_ip=banana", "without_ip"},
		} {
			status, out := do(t, ts, "GET", c.path, "")
			if status != http.StatusBadRequest {
				t.Errorf("%s answered %d, want 400", c.path, status)
				continue
			}
			if out["type"] != "invalid_arguments" {
				t.Errorf("%s answered type %v, want invalid_arguments", c.path, out["type"])
			}
			details, _ := out["details"].([]any)
			if len(details) != 1 {
				t.Fatalf("%s carries %d detail(s): %v", c.path, len(details), out)
			}
			first, _ := details[0].(map[string]any)
			if name, _ := first["argument_name"].(string); name != c.field {
				t.Errorf("%s names argument %q, want %q", c.path, name, c.field)
			}
		}
	})

	// And a well-formed value is not refused, or the guard would answer 400 to
	// every client and the filter would be worse than before.
	if status, _ := do(t, ts, "GET", "/ipam/v1/regions/fr-par/ips?attached=True", ""); status != http.StatusOK {
		t.Errorf("a value the cloud accepts was refused with %d", status)
	}
}

// An action flag reads the same way, and the silence costs more there: a client
// sending release_ip=True kept its address and was told nothing.
func TestAnActionFlagRefusesAnUnparseableValueRatherThanIgnoringIt(t *testing.T) {
	ts := newTestServer(t)

	// A bogus identifier is enough: the real API parses the query before it looks
	// the object up, measured on fr-par with a UUID that exists nowhere.
	const bogus = "00000000-0000-4000-8000-000000000000"
	status, out := do(t, ts, "DELETE",
		"/lb/v1/zones/fr-par-1/lbs/"+bogus+"?release_ip=banana", "")
	if status != http.StatusBadRequest {
		t.Errorf("release_ip=banana answered %d, want 400 before the lookup: an unread flag "+
			"keeps the address and says nothing", status)
	}
	if msg, _ := out["message"].(string); msg == "" {
		t.Errorf("the refusal carries no message: %v", out)
	}
}
