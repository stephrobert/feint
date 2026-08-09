package outscale

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// ReadLoadBalancers, and only it: the inventory of load balancers this emulator
// has, which is none.
//
// The rest of the family stays declined, and the line between the two is the
// difference between a truth and an invention. A load balancer is a data plane —
// it accepts connections, spreads them, health-checks what answers — and this
// emulator has none, so CreateLoadBalancer would hand back a DNS name resolving
// nowhere. That refusal stands. But "there are no load balancers" is not an
// invention: it is exactly what an account without any answers, and it is what a
// fresh real account answered in the read sweep of 2026-08-08.
//
// It is served because the refusal broke a real client, measured rather than
// supposed: `terraform destroy` asks which load balancers are linked to a
// security group before removing it, and a 404 there failed the destroy of a
// fixture whose apply had just succeeded — twelve resources left standing
// because of a question whose honest answer is "none".
//
// The general rule this is an instance of: declining a READ whose true answer is
// empty costs a client the ability to ask, and buys no honesty at all. Decline
// what would require inventing; serve what is simply empty.
func (p *Pack) readLoadBalancers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	// Every filter is accepted rather than refused, and this is the one place in
	// the pack where that is right: the list is empty whatever is asked, so no
	// filter can be silently ignored — there is nothing for it to fail to match.
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"LoadBalancers":   []any{},
		"ResponseContext": p.context(),
	})
}
