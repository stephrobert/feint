package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// An Incus OVN network load balancer is what an emulated internal load balancer
// becomes. The shapes below are the runtime's own, read off the wire on
// 2026-08-20 rather than taken from a document:
//
//	GET  /1.0/networks/{net}/load-balancers/{listen}
//	  {"listen_address":"10.61.1.240","description":"...","config":{},
//	   "backends":[{"name":"b1","description":"","target_address":"10.61.1.10",
//	                "target_port":"80"}],
//	   "ports":[{"description":"","protocol":"tcp","listen_port":"80",
//	             "target_backend":["b1"]}]}
//
// Both port fields are strings, which is why they are strings here: sending them
// as numbers is the kind of guess this repository has paid for before.
//
// A whole-object PUT replaces backends and ports at once, and that is what
// EnsureBalancer uses — the same reason EnsureFirewall replaces a rule set
// rather than patching it. An unregistered machine must actually stop receiving
// connections.

// balancerDescription marks a balancer as the emulator's own. A load balancer
// carries no user config that a sweep could key on, so the description is what
// ownership is read from — exactly as aclDescription is for a rule set.
const balancerDescription = "feint load balancer"

type lbBackend struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	TargetAddress string `json:"target_address"`
	TargetPort    string `json:"target_port"`
}

type lbPort struct {
	Description   string   `json:"description"`
	Protocol      string   `json:"protocol"`
	ListenPort    string   `json:"listen_port"`
	TargetBackend []string `json:"target_backend"`
}

type lbBody struct {
	ListenAddress string            `json:"listen_address,omitempty"`
	Description   string            `json:"description"`
	Config        map[string]string `json:"config"`
	Backends      []lbBackend       `json:"backends"`
	Ports         []lbPort          `json:"ports"`
}

// balancerProtocols is what OVN distributes.
//
// Anything else is refused, and refused rather than skipped on purpose. An
// emulated LBU listener speaks HTTP, HTTPS, TCP or SSL; all four ride TCP, none
// of them is terminated here, and translating them is the pack's business
// because only the pack knows its own vocabulary. A driver that quietly dropped
// a listener it did not recognise would serve half of what the API describes
// and say nothing — the silent-skip shape this repository keeps paying for.
//
// TestAnUndistributableProtocolIsRefused fails without the refusal.
var balancerProtocols = map[string]bool{"tcp": true, "udp": true}

// EnsureBalancer implements Balancer.
//
// Three refusals before anything is written, each of them a measured line rather
// than caution:
//
//   - not OVN. A managed bridge has no load balancer primitive at all; the
//     capability is declared by the OVN mode alone and this is the same
//     statement at the point of use.
//   - a network the emulator does not own. The balancer would survive every
//     sweep on somebody else's network, and this is the rule every destructive
//     and reconfiguring path here follows (mustOwn).
//   - a listen address outside the network's own block. This is the one that is
//     not obvious: the runtime accepts such an address, announces it with a
//     burst of gratuitous ARPs at creation time and never again, so the
//     balancer answers for a minute or two and then goes dark for ever
//     (#315, measured 2026-08-19). Refusing beats configuring a balancer whose
//     failure arrives three minutes after the test that proved it worked.
//
// TestABalancerOutsideTheNetworksBlockIsRefused fails without the third.
func (d *Incus) EnsureBalancer(ctx context.Context, spec BalancerSpec) error {
	if !d.OVN {
		return fmt.Errorf("balancing %s needs the OVN mode: a managed bridge has no load balancer", spec.Name)
	}
	if !safeName.MatchString(spec.Network) {
		return fmt.Errorf("invalid network name %q", spec.Network)
	}
	listen, err := netip.ParseAddr(spec.Listen)
	if err != nil {
		return fmt.Errorf("balancer %s: parse the listen address %q: %w", spec.Name, spec.Listen, err)
	}
	if err := d.mustOwn(ctx, spec.Network); err != nil {
		return err
	}
	block, err := d.networkGateway(ctx, spec.Network)
	if err != nil {
		return err
	}
	if !block.Contains(listen) {
		return fmt.Errorf("balancer %s listens on %s, which is outside %s's own block %s: "+
			"an address the runtime has to announce goes dark within minutes (#315)",
			spec.Name, spec.Listen, spec.Network, block.Masked())
	}

	for _, target := range spec.Targets {
		if _, err := netip.ParseAddr(target); err != nil {
			return fmt.Errorf("balancer %s: parse the backend address %q: %w", spec.Name, target, err)
		}
	}
	for _, listener := range spec.Listeners {
		if !balancerProtocols[strings.ToLower(listener.Protocol)] {
			return fmt.Errorf("balancer %s: %q is not a protocol this runtime distributes; "+
				"translate it to tcp or udp where the provider's vocabulary is known",
				spec.Name, listener.Protocol)
		}
	}

	body := lbBody{
		Description: balancerDescription + " " + spec.Name,
		Config:      map[string]string{},
		Backends:    expandBackends(spec),
		Ports:       []lbPort{},
	}
	// Every listener, with no skip: the loop above already refused anything
	// this runtime cannot distribute, so a `continue` here would be a second
	// filter with nothing left to filter and one more place for a listener to
	// disappear quietly.
	for _, listener := range spec.Listeners {
		port := lbPort{
			Protocol:      strings.ToLower(listener.Protocol),
			ListenPort:    strconv.Itoa(listener.Listen),
			TargetBackend: []string{},
		}
		for i := range spec.Targets {
			port.TargetBackend = append(port.TargetBackend, backendName(i, listener))
		}
		body.Ports = append(body.Ports, port)
	}
	if len(body.Ports) == 0 {
		return fmt.Errorf("balancer %s carries no listener the runtime can distribute", spec.Name)
	}

	path := "/1.0/networks/" + spec.Network + "/load-balancers"
	exists, err := d.balancerExists(ctx, spec.Network, spec.Listen)
	if err != nil {
		return err
	}
	if !exists {
		body.ListenAddress = spec.Listen
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode balancer %s: %w", spec.Name, err)
	}
	if exists {
		if _, err := d.run(ctx, "query", "-X", "PUT", "--data", string(encoded), path+"/"+spec.Listen); err != nil {
			return fmt.Errorf("write balancer %s: %w", spec.Name, err)
		}
		return nil
	}
	if _, err := d.run(ctx, "query", "-X", "POST", "--data", string(encoded), path); err != nil {
		return fmt.Errorf("create balancer %s: %w", spec.Name, err)
	}
	return nil
}

// balancerExists asks the collection rather than reading a refusal.
//
// The alternative was to PUT and treat the failure as "create it instead", and
// that alternative shipped broken for exactly the reason isNotFound documents
// about itself: matching prose is a last resort. The daemon says "Network load
// balancer not found", which is in no phrase list here, so every first write
// failed, was reported as a hard error, and the balancer was never created —
// the emulated load balancer answered on an address nothing was listening on.
//
// The collection endpoint answers a list of URLs and succeeds whether or not
// anything is in it, so the question is structural and no wording can change
// under it. TestABalancerIsCreatedWhenTheCollectionDoesNotHoldIt fails without
// this.
// balancerGone reads the daemon's own wording for a balancer that is not there.
//
// Prose, and a last resort, exactly as isNotFound says of itself — which is why
// it is used on one line only, the DELETE, where the alternative is a race
// nobody can close structurally. The wording is the daemon's, measured on Incus
// 7.2 on 2026-08-20: "Error: Network load balancer not found".
func balancerGone(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "load balancer not found")
}

func (d *Incus) balancerExists(ctx context.Context, network, listen string) (bool, error) {
	out, err := d.run(ctx, "query", "/1.0/networks/"+network+"/load-balancers")
	if err != nil {
		return false, fmt.Errorf("list the balancers of %s: %w", network, err)
	}
	var urls []string
	if err := json.Unmarshal(out, &urls); err != nil {
		return false, fmt.Errorf("read the balancers of %s: %w", network, err)
	}
	for _, url := range urls {
		if strings.HasSuffix(url, "/"+listen) {
			return true, nil
		}
	}
	return false, nil
}

// expandBackends builds one runtime backend per (machine, backend port).
//
// The runtime puts the target port on the backend and not on the listener, so a
// balancer whose listeners hand connections to two different ports needs two
// records per machine. Written once here rather than inline, because the names
// have to agree with the ones the ports reference and a second copy of that
// naming is how they stop agreeing.
func expandBackends(spec BalancerSpec) []lbBackend {
	seen := map[string]bool{}
	out := []lbBackend{}
	for _, listener := range spec.Listeners {
		for i, target := range spec.Targets {
			name := backendName(i, listener)
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, lbBackend{
				Name:          name,
				TargetAddress: target,
				TargetPort:    strconv.Itoa(backendPort(listener)),
			})
		}
	}
	return out
}

// backendName and backendPort are the two halves of one decision, and they are
// written once because a port that disagrees with the name referencing it
// produces a balancer the runtime accepts and that reaches nothing.
func backendName(i int, listener BalancerListener) string {
	return fmt.Sprintf("b%d-%d", i, backendPort(listener))
}

// backendPort is where the connection lands on the machine. A listener that
// names no backend port hands it to the port it answered on, which is what
// every one of these APIs means by leaving it out.
func backendPort(listener BalancerListener) int {
	if listener.Backend != 0 {
		return listener.Backend
	}
	return listener.Listen
}

// RemoveBalancer implements Balancer.
//
// A balancer that is already gone is not an error: the caller is undoing
// something, and "nothing left to undo" is success. A balancer that exists and
// is not the emulator's is refused, because a delete is a destructive path and
// those are the ones that destroy — the rule mustOwnACL states for a rule set,
// applied to the object this file creates.
//
// TestRemovingAForeignBalancerIsRefused fails without the ownership check.
func (d *Incus) RemoveBalancer(ctx context.Context, network, listen string) error {
	if !d.OVN {
		return nil
	}
	if !safeName.MatchString(network) {
		return fmt.Errorf("invalid network name %q", network)
	}
	if _, err := netip.ParseAddr(listen); err != nil {
		return fmt.Errorf("parse the listen address %q: %w", listen, err)
	}
	// Asked of the collection, for the reason balancerExists carries: "gone" and
	// "the daemon phrased its refusal differently" must not be the same answer
	// on a path whose whole job is to be idempotent.
	exists, err := d.balancerExists(ctx, network, listen)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	path := "/1.0/networks/" + network + "/load-balancers/" + listen
	out, err := d.run(ctx, "query", path)
	if err != nil {
		return fmt.Errorf("inspect balancer %s on %s: %w", listen, network, err)
	}
	var existing lbBody
	if err := json.Unmarshal(out, &existing); err != nil {
		return fmt.Errorf("read the description of balancer %s: %w", listen, err)
	}
	if !strings.HasPrefix(existing.Description, balancerDescription) {
		return fmt.Errorf("the balancer %s on %s was not created by the emulator; refusing to delete it", listen, network)
	}
	// A balancer that vanished between the check above and here is somebody
	// else's delete winning a race, and "gone" is what this call wanted.
	if _, err := d.run(ctx, "query", "-X", "DELETE", path); err != nil && !balancerGone(err) {
		return fmt.Errorf("delete balancer %s on %s: %w", listen, network, err)
	}
	return nil
}
