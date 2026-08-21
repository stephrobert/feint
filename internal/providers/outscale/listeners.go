package outscale

import (
	"net/http"
	"slices"
	"strconv"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Listeners after the create (#344).
//
// CreateLoadBalancer carries its listeners inline, so a first `terraform apply`
// never needs these two operations and all three surveyed stacks that build an
// LBU converged without them (#281, examples/stacks/surveyed.md). What needed
// them is the second apply: editing a `listeners` block on a load balancer that
// already exists.
//
// Measured rather than assumed, in the provider's own resource code and then
// against the emulator with the real client:
//
//	v1.1.3  outscale/resource_outscale_load_balancer.go:671,695
//	v1.7.0  internal/services/oapi/resource_outscale_load_balancer.go:732,745
//	v1.8.0  internal/services/oapi/resource_load_balancer.go:990,1001
//
// All three call these two from the Update path and from nowhere else, and all
// three delete the departing front ports before creating the arriving ones.
// Changing one listener's port on provider 1.8.0 answered, before #344:
//
//	Error: Unable to update Load Balancer listeners
//	404 … "feint does not serve DeleteLoadBalancerListeners"
//
// and every later plan stayed at `0 to add, 1 to change, 0 to destroy`.
//
// The delete-then-create order is why a load balancer here is allowed to hold
// no listener at all for the span of an update: refusing to remove the last one
// would refuse the single-listener port change, which is the common case. What
// must not happen is the runtime going on distributing on a port the API has
// stopped listing, and syncBalancer is where that is handled.
//
// Shapes from the SDK: CreateLoadBalancerListenersRequest at
// .upstream/osc-sdk-go/pkg/osc/client.gen.go:2295, its response at :2307,
// DeleteLoadBalancerListenersRequest at :3430, its response at :3442. Both
// responses carry the whole LoadBalancer, which is why each handler ends on the
// same view the create and the read serve.

type createLoadBalancerListenersRequest struct {
	Listeners        []listenerForCreation `json:"Listeners"`
	LoadBalancerName string                `json:"LoadBalancerName"`
	DryRun           *bool                 `json:"DryRun"`
}

type deleteLoadBalancerListenersRequest struct {
	LoadBalancerName  string `json:"LoadBalancerName"`
	LoadBalancerPorts []int  `json:"LoadBalancerPorts"`
	DryRun            *bool  `json:"DryRun"`
}

// listenerViews validates a batch of listeners and renders them as the SDK's
// Listener (client.gen.go:6169). It returns the rendered batch, or the refusal
// the caller writes out; an empty string means the batch is good.
//
// Shared by CreateLoadBalancer and CreateLoadBalancerListeners on purpose: the
// two accept the same ListenerForCreation, and a rule enforced on one path only
// is a rule the other path does not have. `taken` carries the front ports the
// balancer already holds, and is nil on the create.
func listenerViews(in []listenerForCreation, taken []int) ([]any, string) {
	out := make([]any, 0, len(in))
	ports := slices.Clone(taken)
	for _, l := range in {
		backendProtocol := orDefault(l.BackendProtocol, l.LoadBalancerProtocol)
		if !slices.Contains(listenerProtocols, l.LoadBalancerProtocol) ||
			!slices.Contains(listenerProtocols, backendProtocol) {
			return nil, "the listener protocol must be HTTP, HTTPS, TCP or SSL"
		}
		if !validPort(l.LoadBalancerPort) || !validPort(l.BackendPort) {
			return nil, "the listener ports must be between 1 and 65535"
		}
		if l.ServerCertificateID != "" {
			// Server certificates are declined by name (declined.go); accepting
			// the reference here would store a certificate nothing can read back.
			return nil, "ServerCertificateId is not served: server certificates are declined, see /_feint/routes"
		}
		if slices.Contains(ports, l.LoadBalancerPort) {
			// One front port, one listener. The refusal is load-bearing rather
			// than tidy: two listeners on one port are two runtime listeners on
			// one port, which is the thing the balancer cannot build, so
			// storing them would leave the API describing a balancer the
			// runtime had refused.
			//
			// The wording deliberately avoids the token the real service uses
			// for this. Provider 1.1.3 retries for five minutes on any error
			// whose text contains "DuplicateListener"
			// (resource_outscale_load_balancer.go:707), because on a real
			// account the condition is transient. Here it never is, so echoing
			// that token would turn an immediate, accurate refusal into a
			// five-minute hang.
			//
			// TestTwoListenersOnOnePortAreRefused fails without this.
			return nil, "two listeners cannot share the front port " + strconv.Itoa(l.LoadBalancerPort)
		}
		ports = append(ports, l.LoadBalancerPort)
		out = append(out, map[string]any{
			"BackendPort":          l.BackendPort,
			"BackendProtocol":      backendProtocol,
			"LoadBalancerPort":     l.LoadBalancerPort,
			"LoadBalancerProtocol": l.LoadBalancerProtocol,
			"PolicyNames":          []any{},
		})
	}
	return out, ""
}

// listenerPorts is the set of front ports a stored balancer already serves.
// numOf rather than a cast, because a listener that has crossed a snapshot
// carries json.Number where the handler stored an int.
func listenerPorts(res *resource.Resource) []int {
	listeners, _ := res.Attrs["Listeners"].([]any)
	ports := make([]int, 0, len(listeners))
	for _, raw := range listeners {
		listener, _ := raw.(map[string]any)
		ports = append(ports, int(numOf(listener["LoadBalancerPort"])))
	}
	return ports
}

func (p *Pack) createLoadBalancerListeners(w http.ResponseWriter, r *http.Request) {
	var req createLoadBalancerListenersRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.LoadBalancerName == "" {
		p.badRequest(w, "LoadBalancerName is required")
		return
	}
	if len(req.Listeners) == 0 {
		p.badRequest(w, "Listeners is required")
		return
	}

	// The whole batch is validated against the stored ports inside Store.Update,
	// not before it: the listener set is a collection, and validating against a
	// set read outside the write is the #289 shape — the check passes on a
	// balancer the concurrent write has already changed.
	//
	// The refusal travels as an error rather than as a variable set beside a
	// nil return, and that is not style: Store.Update writes the draft back and
	// notifies watchers for every callback that returns nil, so a refused
	// request would publish an "updated" event for a balancer nothing changed.
	// An error aborts the write and the notification together.
	var refusal listenerRefusal
	err := p.env.Store.Update(Name, kindLoadBalancer, req.LoadBalancerName, func(stored *resource.Resource) error {
		added, why := listenerViews(req.Listeners, listenerPorts(stored))
		if why != "" {
			refusal = listenerRefusal(why)
			return refusal
		}
		listeners, _ := stored.Attrs["Listeners"].([]any)
		stored.Attrs["Listeners"] = append(slices.Clone(listeners), added...)
		stored.Updated = p.env.Now()
		return nil
	})
	switch {
	case refusal != "":
		p.badRequest(w, string(refusal))
		return
	case err != nil:
		p.notFound(w, "load balancer", req.LoadBalancerName)
		return
	}
	p.finishListenerChange(w, r, req.LoadBalancerName)
}

// listenerRefusal carries a validation refusal out of a Store.Update callback.
// It is an error so the callback can abort the write; the handler reads the
// text off it rather than matching on a string.
type listenerRefusal string

func (r listenerRefusal) Error() string { return string(r) }

func (p *Pack) deleteLoadBalancerListeners(w http.ResponseWriter, r *http.Request) {
	var req deleteLoadBalancerListenersRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.LoadBalancerName == "" {
		p.badRequest(w, "LoadBalancerName is required")
		return
	}
	if len(req.LoadBalancerPorts) == 0 {
		p.badRequest(w, "LoadBalancerPorts is required")
		return
	}
	for _, port := range req.LoadBalancerPorts {
		if !validPort(port) {
			p.badRequest(w, "the listener ports must be between 1 and 65535")
			return
		}
	}

	// Naming a port that carries no listener is not refused, and that is a
	// choice rather than a measurement: nothing here has watched a real account
	// answer it. What the request asks for is that these ports carry no
	// listener afterwards, and a port that carried none already satisfies it.
	// docs/limits.md records the difference rather than letting the code imply
	// it was measured.
	err := p.env.Store.Update(Name, kindLoadBalancer, req.LoadBalancerName, func(stored *resource.Resource) error {
		listeners, _ := stored.Attrs["Listeners"].([]any)
		kept := make([]any, 0, len(listeners))
		for _, raw := range listeners {
			listener, _ := raw.(map[string]any)
			if slices.Contains(req.LoadBalancerPorts, int(numOf(listener["LoadBalancerPort"]))) {
				continue
			}
			kept = append(kept, raw)
		}
		stored.Attrs["Listeners"] = kept
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		p.notFound(w, "load balancer", req.LoadBalancerName)
		return
	}
	p.finishListenerChange(w, r, req.LoadBalancerName)
}

// finishListenerChange hands the new listener set to the runtime and answers
// with the whole balancer, which is what both responses carry.
//
// The runtime call is outside Store.Update for the reason the rest of this
// repository is: reconfiguring a balancer is a call to another process, and the
// store's lock is measured in microseconds.
func (p *Pack) finishListenerChange(w http.ResponseWriter, r *http.Request, name string) {
	p.syncBalancer(r.Context(), name)
	res, found := p.env.Store.Get(Name, kindLoadBalancer, name)
	if !found {
		p.notFound(w, "load balancer", name)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"LoadBalancer":    p.loadBalancerView(res),
		"ResponseContext": p.context(),
	})
}
