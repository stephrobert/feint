package scaleway_test

import (
	"fmt"
	"net/http"
	"testing"
)

// `regional` is the opposite of the `zonal` filter the handler already
// served: every address booked here is regional, so regional=true keeps
// everything and regional=false matches nothing (#277).
func TestListIPAMIPsRegionalFilterSelects(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := createPN(t, ts, "10.62.0.0/24")
	booked := bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q},"address":"10.62.0.9"}`, pnID))
	id, _ := booked["id"].(string)
	if id == "" {
		t.Fatalf("booked ip has no id in %v", booked)
	}

	ids := func(query string) []string {
		t.Helper()
		status, out := do(t, ts, "GET", ipamRegion+"/ips"+query, "")
		if status != http.StatusOK {
			t.Fatalf("GET /ips%s: %d (%v)", query, status, out)
		}
		list, _ := out["ips"].([]any)
		got := make([]string, 0, len(list))
		for _, raw := range list {
			ip, _ := raw.(map[string]any)
			ipID, _ := ip["id"].(string)
			got = append(got, ipID)
		}
		return got
	}

	if got := ids("?regional=true"); len(got) != 1 || got[0] != id {
		t.Errorf("regional=true lists %v, want only the booked %s", got, id)
	}
	if got := ids("?regional=false"); len(got) != 0 {
		t.Errorf("regional=false lists %v, want none: no zonal address is served here", got)
	}
	if status, _ := do(t, ts, "GET", ipamRegion+"/ips?regional=banana", ""); status != http.StatusBadRequest {
		t.Errorf("regional=banana answered %d, want 400", status)
	}
}
