package machine

import (
	"context"
	"net/netip"
	"slices"
	"strings"

	"github.com/stephrobert/feint/internal/core/resource"
)

// A reboot needs no table of expected values: the shape after is the shape
// before (#669).
//
// Nothing moves in the control plane between the two, because Reconciler.
// Reboot holds the per-target lock from one end to the other (Serialise). So
// the reboot is the one case where the verification costs no measurement
// campaign and no encoded expectation: what the machine carried while it was
// up is what it must carry once it is back, property by property, and the
// only tolerance is the one every reading already applies — nothing that does
// not compare ever entered either Shape.
//
// On the regression of 2026-09-04 this answers, at the moment of the reboot:
//
//	restart(default route) want via 169.254.0.1 dev eth0, got via 10.77.0.1 dev eth0
//
// where the runtime suite used to wait sixty seconds two steps later to say
// "nothing answers". The cause where the effect is, rather than the symptom
// where it surfaces.
//
// Where the before does NOT apply. A poweroff then a poweron as two API
// actions is not a reboot: a NIC attached to a stopped server legitimately
// changes the shape, and that is the ordinary Terraform order. There, the
// derived claims judge (#667, #668). The two are complementary: the reboot
// needs no table, the first boot needs one.

// observe reads the machine behind a resource. nil with no error is a runtime
// that cannot be asked, or a resource with no machine: said, not guessed.
func (r Reconciler) observe(ctx context.Context, res *resource.Resource) (*Shape, error) {
	obs := r.observer()
	name := res.Runtime[r.binding().RuntimeKey]
	if obs == nil || name == "" {
		return nil, nil
	}
	shape, err := obs.Observe(ctx, name)
	if err != nil {
		return nil, err
	}
	return &shape, nil
}

// restartClaims turns the shape before a reboot into the claims the shape
// after must answer: one per interface, one for the default route, one for
// the rest of the table. They ride the same reading as the plan's claims, so
// a reboot is read once and repaired once like any other boot.
func restartClaims(before Shape) []Claim {
	claims := make([]Claim, 0, len(before.Interfaces)+2)
	for _, name := range before.interfaceNames() {
		claims = append(claims, sameInterface{Name: name, Was: before.Interfaces[name]})
	}
	route, had := before.defaultRoute()
	claims = append(claims, sameDefaultRoute{Was: route, Had: had})
	var others []Route
	for _, route := range before.Routes {
		if route.Dst != defaultDst {
			others = append(others, route)
		}
	}
	return append(claims, sameRoutes{Was: others})
}

// sameInterface claims that an interface carries, after the reboot, what it
// carried before: the same network, the same kind, the same addresses, the
// same rule sets. Sets, not lists: the order the kernel or the runtime lists
// them in is not a property of the machine.
type sameInterface struct {
	Name string
	Was  Interface
}

func (c sameInterface) String() string { return "restart(" + c.Name + ")" }

func (c sameInterface) Check(s Shape) Verdict {
	got, has := s.Interfaces[c.Name]
	if !has {
		return broken(c, describeInterface(c.Was), "no interface "+c.Name)
	}
	if got.Network == c.Was.Network && got.Routed == c.Was.Routed &&
		slices.Equal(sortedPrefixes(got.Addresses), sortedPrefixes(c.Was.Addresses)) &&
		slices.Equal(sortedStrings(got.RuleSets), sortedStrings(c.Was.RuleSets)) {
		return held(c)
	}
	return broken(c, describeInterface(c.Was), describeInterface(got))
}

// sameDefaultRoute claims that the default route is, after the reboot, what
// it was before — present with the same door, or absent.
type sameDefaultRoute struct {
	Was Route
	Had bool
}

func (c sameDefaultRoute) String() string { return "restart(default route)" }

func (c sameDefaultRoute) Check(s Shape) Verdict {
	got, has := s.defaultRoute()
	switch {
	case !c.Had && !has:
		return held(c)
	case c.Had && !has:
		return broken(c, c.Was.String(), "no default route")
	case !c.Had && has:
		return broken(c, "no default route", got.String())
	}
	if got.Via == c.Was.Via && got.Dev == c.Was.Dev {
		return held(c)
	}
	return broken(c, c.Was.String(), got.String())
}

// sameRoutes claims that the rest of the table — every route with a next hop
// but the default — is, after the reboot, the set it was before.
type sameRoutes struct {
	Was []Route
}

func (c sameRoutes) String() string { return "restart(routes)" }

func (c sameRoutes) Check(s Shape) Verdict {
	var got []Route
	for _, route := range s.Routes {
		if route.Dst != defaultDst {
			got = append(got, route)
		}
	}
	want, have := routeLines(c.Was), routeLines(got)
	if slices.Equal(want, have) {
		return held(c)
	}
	return broken(c, strings.Join(want, ", "), strings.Join(have, ", "))
}

// routeLines renders routes for a verdict, sorted so two readings of one
// table compare equal whatever order the kernel printed them in.
func routeLines(routes []Route) []string {
	if len(routes) == 0 {
		return []string{"none"}
	}
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.Dst.String()+" "+r.String())
	}
	slices.Sort(out)
	return out
}

func sortedPrefixes(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, p.String())
	}
	slices.Sort(out)
	return out
}

func sortedStrings(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}

// describeInterface renders an interface for a verdict: where it sits, what
// it carries, what it wears.
func describeInterface(i Interface) string {
	where := i.Network
	if i.Routed {
		where = "routed"
	}
	if where == "" {
		where = "no network"
	}
	addresses := "nothing"
	if len(i.Addresses) > 0 {
		addresses = strings.Join(sortedPrefixes(i.Addresses), ", ")
	}
	sets := "no rule set"
	if len(i.RuleSets) > 0 {
		sets = strings.Join(sortedStrings(i.RuleSets), ", ")
	}
	return where + ": " + addresses + "; " + sets
}
