package machine

import "net/netip"

// Dialect implements Dialected: what this driver mode knows about itself, for
// the claims a plan makes (#667).
//
// Every value here is one the driver already acts on, restated rather than
// decided: the routed next hop is the constant routedDevice hands the NIC, the
// aggregates are the blocks installGuestPrivateRoutes lays, and the default
// route is laid by RouteEgress under OVN alone — under a bridge that method
// answers nil without touching the guest, and a claim there would judge a
// route nobody was asked to lay.
//
// TestTheIncusDialectRestatesWhatTheModeLays fails if any of the three drifts
// from the code it describes.
func (d *Incus) Dialect() Dialect {
	out := Dialect{RoutedNextHop: netip.MustParseAddr(routedNextHop)}
	if d.OVN {
		out.Aggregates = privateAggregatePrefixes()
		out.LaysDefaultRoute = true
	}
	return out
}

// privateAggregatePrefixes is privateAggregates parsed, for the claims that
// compare blocks rather than strings. The strings stay the driver's own
// vocabulary because they are what reaches `ip route add`.
func privateAggregatePrefixes() []netip.Prefix {
	out := make([]netip.Prefix, 0, len(privateAggregates))
	for _, block := range privateAggregates {
		out = append(out, netip.MustParsePrefix(block))
	}
	return out
}
