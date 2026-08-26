package outscale

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The delete half of #521's detached rule set. DeleteNet cascades the default
// group the Net was born with — the one group DeleteSecurityGroup refuses to
// touch — so this cascade is the only moment that group's runtime rule set can
// ever be dropped. It was not: two green conformance runs each left the host
// carrying one `osc-*` rule set at used_by 0, the default group of a Net whose
// teardown went Vms, Subnet, Net through the API, exactly balancer.sh's
// cleanup. deleteSecurityGroup already drops the set for every group a client
// deletes itself; this holds the same property on the one path a client
// cannot take.

// TestDeleteNetDropsTheDefaultGroupsRuleSet fails while deleteNet removes the
// default group from the store without removing its rule set from the host.
func TestDeleteNetDropsTheDefaultGroupsRuleSet(t *testing.T) {
	driver := newFirewallDriver()
	p := firewallPack(driver)

	w := httptest.NewRecorder()
	p.createNet(w, httptest.NewRequest(http.MethodPost, "/api/v1/CreateNet",
		strings.NewReader(`{"IpRange":"10.20.0.0/16"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("CreateNet answered %d:\n%s", w.Code, w.Body.String())
	}
	var created struct {
		Net struct {
			NetID string `json:"NetId"`
		} `json:"Net"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Net.NetID == "" {
		t.Fatalf("CreateNet answered no NetId (%v):\n%s", err, w.Body.String())
	}

	groups := p.securityGroupsOf(created.Net.NetID)
	if len(groups) != 1 {
		t.Fatalf("a fresh Net carries %d group(s), want its default one", len(groups))
	}
	setName := machine.FirewallName("osc", groups[0].ID)
	// A Vm wore the default group earlier in the run, so the host holds its
	// rule set. Seeded directly, because what this test measures is the delete
	// half; the boot half is TestAnOutscaleGroupReachesTheHostWhenItsVmBoots.
	driver.ensured[setName] = machine.FirewallSpec{Name: setName}

	w = httptest.NewRecorder()
	p.deleteNet(w, httptest.NewRequest(http.MethodPost, "/api/v1/DeleteNet",
		strings.NewReader(`{"NetId":"`+created.Net.NetID+`"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("DeleteNet answered %d:\n%s", w.Code, w.Body.String())
	}

	if _, still := driver.ensured[setName]; still {
		t.Fatalf("the default group's rule set %s outlived its Net on the host: DeleteSecurityGroup "+
			"refuses the default group, so nothing a client can call will ever remove it (#521)", setName)
	}
	if len(p.securityGroupsOf(created.Net.NetID)) != 0 {
		t.Fatal("the default group survived its Net in the store")
	}
}
