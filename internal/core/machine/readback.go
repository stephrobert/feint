package machine

import (
	"context"
	"net/netip"
	"slices"

	"github.com/stephrobert/feint/internal/core/resource"
)

// Nothing in this repository read back what a machine actually carried after
// a lifecycle action (#668). Every step reported its own success, and the sum
// of successful steps was not a working machine: on 2026-09-04 a route was
// replaced, every exec returned 0, and the machine answered nothing at its
// published address (#660). This file is the reading half of the guard whose
// derivation half is claims.go: the driver reads a Shape, and every claim the
// plan made is answered against it.
//
// Three outcomes, never two. A read that fails is Unreadable — neither held
// nor broken, and counted — because folding it onto either is how a verifier
// starts lying. And a runtime that cannot be asked at all is a fourth state,
// held apart from the three: verify answers asked=false for it, so that
// nothing downstream mistakes "nobody looked" for "nothing was wrong".
//
// When to read. The layer already knows when a container has finished
// configuring itself, because it already waits: waitForGuestInterface blocks
// until the interface carries an address, and settleFirstBoot and
// restoreGuestNetwork run inside Start. When Binding.PowerOn returns, a
// container is configured, and the aggregates arrive in DHCP option 121 with
// the lease. A virtual machine is Unreadable until its agent answers, and the
// verification replays from ReplayAddresses, the late-address door that
// already exists for exactly that.

// Observer is the optional half a driver implements, on the model of
// EgressRouter and Capable: it reads back what a machine carries. A driver
// that does not implement it is a driver nobody can ask, which is not a
// failed read — see verify.
type Observer interface {
	// Observe reads the machine's interfaces, addresses, routes, rule-set
	// bindings and the gateways of its emulated networks, normalised the way
	// Shape documents.
	Observe(ctx context.Context, machine string) (Shape, error)
	// Door answers which door a reply from one address leaves by towards a
	// destination: `ip route get <to> from <from>`. A machine with no route
	// at all answers the zero Route and no error, because "no route" is a
	// fact about the machine and not a failure to read it.
	Door(ctx context.Context, machine string, from, to netip.Addr) (Route, error)
}

// doorAsker is the claim-side half of Door: a claim that needs one read says
// which, and the reading half asks on its behalf before checking.
type doorAsker interface {
	door() (from, to netip.Addr)
}

// observer answers the runtime's reading half, nil when it has none.
func (r Reconciler) observer() Observer {
	obs, _ := r.binding().driver.(Observer)
	return obs
}

// verify derives what the plan claims, adds what the caller claims on top —
// the shape before a reboot — reads what the machine carries, and answers
// every claim. asked is false when nobody could look: a runtime with no
// Observer, a resource with no machine, or a plan that was refused before the
// runtime was asked (its refusal is already in the log).
//
// A shape that could not be read answers Unreadable on every claim rather
// than nothing, so the count of what was not verified is visible where the
// count of what was is. A door that could not be read answers Unreadable on
// that claim alone.
//
// TestAPlantedDivergenceIsReported and TestAnUnreadableShapeIsNeitherHeldNorBroken
// are the instrument's two controls, and both fail without this.
func (r Reconciler) verify(ctx context.Context, res *resource.Resource, extra []Claim) (verdicts []Verdict, asked bool) {
	obs := r.observer()
	name := res.Runtime[r.binding().RuntimeKey]
	if obs == nil || name == "" {
		return nil, false
	}
	plan, declared := r.plan(res)
	if !declared {
		return nil, false
	}
	claims, err := r.Expect(plan, r.dialect())
	if err != nil {
		return nil, false
	}
	claims = append(claims, r.wears(res, plan)...)
	// And what the caller knew before the action: a reboot's shape before it
	// (#669), answered by the same reading as the plan's claims.
	claims = append(claims, extra...)
	if len(claims) == 0 {
		return nil, true
	}
	shape, err := obs.Observe(ctx, name)
	if err != nil {
		for _, c := range claims {
			verdicts = append(verdicts, unreadable(c, err.Error()))
		}
		return verdicts, true
	}
	for _, c := range claims {
		if asker, asks := c.(doorAsker); asks {
			from, to := asker.door()
			route, err := obs.Door(ctx, name, from, to)
			if err != nil {
				verdicts = append(verdicts, unreadable(c, err.Error()))
				continue
			}
			if shape.Doors == nil {
				shape.Doors = map[netip.Addr]Route{}
			}
			shape.Doors[from] = route
		}
		verdicts = append(verdicts, c.Check(shape))
	}
	return verdicts, true
}

// wears derives the rule-set claims from the same fields the firewall step
// reads, so the claim cannot drift from what ApplyRuleSets asked for: the
// machine's worn groups become names through the pack's own translation, and
// the networks its groups do not reach come off the plan (#574).
//
// Claimed only when the machine wears at least one enforcing group. A machine
// wearing none is left to the isolation pass, which binds either nothing or
// the permissive posture set depending on whether the network is isolated —
// a distinction that is the isolation pass's to make and would read as a
// broken claim here. And not at all when the host withdrew the firewall
// capability (#454): a claim about rule sets the host refused to bind would
// be broken on every machine, for a fact /_feint/health already publishes.
//
// TestTheRuleSetsAMachineWearsAreClaimedOnItsFilteredInterfaces fails without
// this.
func (r Reconciler) wears(res *resource.Resource, plan Plan) []Claim {
	s := r.Groups
	if !s.wired() || s.enforcer() == nil {
		return nil
	}
	if !CapabilitiesOf(r.binding().driver).Firewall {
		return nil
	}
	var names []string
	for _, id := range s.WornIDs(res) {
		group, found := s.Group(id)
		if !found {
			continue
		}
		spec := s.spec(group, res)
		if spec.EnforcesNothing() || slices.Contains(names, spec.Name) {
			continue
		}
		names = append(names, spec.Name)
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	unfiltered := s.unfiltered(res)
	var claims []Claim
	var seen []string
	for _, att := range plan.attachments() {
		if att.Network == "" || slices.Contains(seen, att.Network) {
			continue
		}
		seen = append(seen, att.Network)
		if slices.Contains(unfiltered, att.Network) {
			claims = append(claims, wears{Network: att.Network})
			continue
		}
		claims = append(claims, wears{Network: att.Network, Sets: names})
	}
	return claims
}
