package machine

import (
	"context"
	"testing"
)

// TestRouteAddressOVNRefusesAnIllFormedNameOnItsOwnEntry measures the shape
// check routeAddressOVN re-asks of a name RouteAddress has already checked.
//
// #933 kept that check and recorded that deleting it left every test in this
// package green: every test reached routeAddressOVN through RouteAddress,
// whose entry refuses first. #945 measured attachExtra's inner call by being
// the second caller; this does the same here, with no RouteAddress in front.
//
// One row per way a name is ill-formed — a leading dash, a path separator, a
// space — and an accepting row, so a check that refused every name would not
// pass for a working one.
func TestRouteAddressOVNRefusesAnIllFormedNameOnItsOwnEntry(t *testing.T) {
	for _, machine := range []string{"-foo", "feint-scw-a/b", "feint-scw-a b"} {
		t.Run(machine, func(t *testing.T) {
			f := &fakeRuntime{}
			d := ovnDriver(f)
			err := d.routeAddressOVN(context.Background(), AddressSpec{
				Machine: machine, Address: "203.0.113.9", Network: "fnt-368798629f8",
			})
			if err == nil {
				t.Fatalf("routeAddressOVN accepted the machine name %q", machine)
			}
			if len(f.calls) != 0 {
				t.Fatalf("the name %q was refused, so nothing should have been sent: %v", machine, f.commands())
			}
		})
	}

	t.Run("a well-formed name goes past the check", func(t *testing.T) {
		f := &fakeRuntime{}
		d := ovnDriver(f)
		_ = d.routeAddressOVN(context.Background(), AddressSpec{
			Machine: "feint-scw-mine", Address: "203.0.113.9", Network: "fnt-368798629f8",
		})
		if len(f.calls) == 0 {
			t.Fatal("routeAddressOVN refused a well-formed machine name before asking the runtime anything")
		}
	})
}
