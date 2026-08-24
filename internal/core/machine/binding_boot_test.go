package machine

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
)

// The property under test (#83): an image identifier that resolves to nothing
// must never boot a substitute. Ask for Alpine, boot Ubuntu, and every signal
// says success — the API reports the identifier the client sent while the
// machine runs something else. Either nothing starts and the resource says so
// through FailedState, or what starts is exactly what the resolution named.
//
// The guard lives in Binding.Start, the shared layer, so a pack cannot
// substitute silently and a fourth pack could not either. Every case here
// fails if the guard is removed; /falsify checks that claim.

// recordingDriver is a Driver that records every Spec it was asked to start,
// so a test can assert on what would have booted rather than on a state name.
type recordingDriver struct {
	specs []Spec
}

func (d *recordingDriver) Name() string                   { return "recording" }
func (d *recordingDriver) Available(context.Context) bool { return true }
func (d *recordingDriver) Start(_ context.Context, spec Spec) (Machine, error) {
	d.specs = append(d.specs, spec)
	return Machine{Name: spec.Name, IP: "10.42.0.9", Running: true}, nil
}
func (d *recordingDriver) Stop(context.Context, string) error   { return nil }
func (d *recordingDriver) Remove(context.Context, string) error { return nil }
func (d *recordingDriver) Inspect(_ context.Context, name string) (Machine, bool, error) {
	return Machine{Name: name}, false, nil
}
func (d *recordingDriver) EnsureNetwork(context.Context, NetworkSpec) error { return nil }
func (d *recordingDriver) Attach(context.Context, string, Attachment) error { return nil }
func (d *recordingDriver) Detach(context.Context, string, string) error     { return nil }
func (d *recordingDriver) RemoveNetwork(context.Context, string) error      { return nil }

func bootBinding(driver Driver) Binding {
	return Binding{
		Driver:       driver,
		Provider:     "acme",
		Prefix:       "feint-acme-",
		User:         "root",
		RuntimeKey:   "machine",
		AddressKey:   "address",
		RunningState: "running",
		FailedState:  "failed",
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestAnUnknownImageFailsTheBootInsteadOfSubstituting(t *testing.T) {
	driver := &recordingDriver{}
	b := bootBinding(driver)
	res := &resource.Resource{ID: "srv-1", State: "stopped"}

	if b.PowerOn(context.Background(), res, Boot{Requested: "totalement-inconnue"}) {
		t.Fatal("a boot with no resolved image reported success")
	}
	if res.State != "failed" {
		t.Fatalf("state %q, want the failed state: running on a substitute is the defect under test", res.State)
	}
	if len(driver.specs) != 0 {
		t.Fatalf("the runtime was asked to boot %q for an identifier that resolves to nothing", driver.specs[0].Image)
	}
}

func TestAnUnknownImageStaysMetadataOnlyWithoutARuntime(t *testing.T) {
	// Noop boots nothing, so there is nothing to substitute: the control plane
	// must keep accepting — docs/limits.md promises hardcoded production
	// identifiers keep working, and CI runs the conformance suites this way.
	for name, driver := range map[string]Driver{"noop": Noop{}, "nil": nil} {
		b := bootBinding(driver)
		res := &resource.Resource{ID: "srv-1", State: "stopped"}
		if !b.PowerOn(context.Background(), res, Boot{Requested: "totalement-inconnue"}) {
			t.Fatalf("%s: the metadata-only mode refused a boot it cannot lie about", name)
		}
		if res.State != "running" {
			t.Fatalf("%s: state %q, want running: with no runtime the control plane is the whole emulation", name, res.State)
		}
	}
}

func TestAResolvedImageBootsWithTheLoginItCarries(t *testing.T) {
	driver := &recordingDriver{}
	b := bootBinding(driver)
	res := &resource.Resource{ID: "srv-1", State: "stopped"}

	// Image and login travel together: a resolution that got the distribution
	// right and the login wrong hands the user a machine nobody can enter.
	ok := b.PowerOn(context.Background(), res, Boot{
		Image:          "alpine:3.21",
		User:           "alpine",
		Requested:      "alpine",
		AuthorizedKeys: []string{"ssh-ed25519 AAAA test"},
	})
	if !ok || res.State != "running" {
		t.Fatalf("ok=%v state=%q, want a running machine", ok, res.State)
	}
	if len(driver.specs) != 1 {
		t.Fatalf("%d boots, want 1", len(driver.specs))
	}
	if got := driver.specs[0]; got.Image != "alpine:3.21" || got.User != "alpine" {
		t.Fatalf("booted image=%q user=%q, want the pair the resolution named", got.Image, got.User)
	}
}

func TestAnImageWithoutItsOwnLoginKeepsTheProviderWideOne(t *testing.T) {
	driver := &recordingDriver{}
	b := bootBinding(driver)
	res := &resource.Resource{ID: "srv-1", State: "stopped"}

	b.PowerOn(context.Background(), res, Boot{
		Image:          "ubuntu:22.04",
		Requested:      "ubuntu_jammy",
		AuthorizedKeys: []string{"ssh-ed25519 AAAA test"},
	})
	if len(driver.specs) != 1 || driver.specs[0].User != "root" {
		t.Fatalf("specs=%v, want one boot as the binding's own login", driver.specs)
	}
}
