package outscale

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// recordingDriver records every Spec it was asked to start, so a test can
// assert on the image and the login rather than on a state name (#83).
type recordingDriver struct {
	specs []machine.Spec
}

func (d *recordingDriver) Name() string                   { return "recording" }
func (d *recordingDriver) Available(context.Context) bool { return true }
func (d *recordingDriver) Start(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	d.specs = append(d.specs, spec)
	return machine.Machine{Name: spec.Name, IP: "10.42.0.9", Running: true}, nil
}
func (d *recordingDriver) Stop(context.Context, string) error   { return nil }
func (d *recordingDriver) Remove(context.Context, string) error { return nil }
func (d *recordingDriver) Inspect(_ context.Context, name string) (machine.Machine, bool, error) {
	return machine.Machine{Name: name}, false, nil
}
func (d *recordingDriver) EnsureNetwork(context.Context, machine.NetworkSpec) error { return nil }
func (d *recordingDriver) Attach(context.Context, string, machine.Attachment) error { return nil }
func (d *recordingDriver) Detach(context.Context, string, string) error             { return nil }
func (d *recordingDriver) RemoveNetwork(context.Context, string) error              { return nil }

func runtimePack(driver machine.Driver) *Pack {
	env := &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string { return "00000000-0000-4000-8000-000000000001" },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env.UseMachines(driver)
	return New(env)
}

// One case per row of the OMI table: image and login resolve together, and an
// identifier outside the table resolves to nothing at all.
func TestOutscaleImageResolutionIsExact(t *testing.T) {
	rows := map[string]machine.Image{
		"ami-00000001": {Ref: "ubuntu:24.04", User: "outscale"},
		"ami-00000002": {Ref: "debian:12", User: "outscale"},
		"ami-00000003": {Ref: "alpine:3.21", User: "outscale"},
	}
	if len(rows) != len(runtimeImages) {
		t.Fatalf("the table serves %d OMIs and this test knows %d: add the row here, one per OMI", len(runtimeImages), len(rows))
	}
	for id, want := range rows {
		got, known := imageFor(id)
		if !known {
			t.Errorf("%s: a served OMI stopped resolving", id)
			continue
		}
		if got != want {
			t.Errorf("%s: resolved to %+v, want %+v — image and login travel together", id, got, want)
		}
	}
	for _, id := range []string{"ami-deadbeef", "ami-99999999", ""} {
		if img, known := imageFor(id); known {
			t.Errorf("%q: resolved to %+v, want no resolution at all", id, img)
		}
	}
}

// The wiring of #83 for this pack: an unknown OMI under a runtime reaches
// stopped — the true state Outscale declares for a Vm that did not start —
// and the runtime is never asked for anything.
func TestAnUnknownOmiDoesNotBootASubstitute(t *testing.T) {
	driver := &recordingDriver{}
	p := runtimePack(driver)
	res := &resource.Resource{
		ID:    "i-00000001",
		State: stateStopped,
		Attrs: map[string]any{"ImageId": "ami-deadbeef"},
	}

	p.powerOn(context.Background(), res)

	if res.State != stateStopped {
		t.Fatalf("state %q, want stopped: running on a substitute is the defect under test", res.State)
	}
	if len(driver.specs) != 0 {
		t.Fatalf("the runtime was asked to boot %q for an OMI nobody serves", driver.specs[0].Image)
	}
}

// The other way an OMI resolves to nothing (#83, scope note): an image the
// client registered through CreateImage. ReadImages serves it, yet no disk
// contents exist behind the record, so it refuses to boot exactly like an
// identifier nobody created — and the log must say which case it is, because
// "the emulator serves this image" and "this image never existed" are not the
// same embarrassment.
func TestARegisteredImageRefusesToBootAndSaysWhy(t *testing.T) {
	driver := &recordingDriver{}
	var log bytes.Buffer
	p := runtimePack(driver)
	p.env.Log = slog.New(slog.NewTextHandler(&log, nil))
	p.env.Store.Put(&resource.Resource{
		ID:     "ami-0000cafe",
		Kind:   kindImage,
		Tenant: resource.Tenant{Provider: Name},
		State:  "available",
		Attrs:  map[string]any{"ImageName": "golden"},
	})
	res := &resource.Resource{
		ID:    "i-00000001",
		State: stateStopped,
		Attrs: map[string]any{"ImageId": "ami-0000cafe"},
	}

	p.powerOn(context.Background(), res)

	if res.State != stateStopped {
		t.Fatalf("state %q, want stopped: booting the source's base image would drop what the image was made to carry", res.State)
	}
	if len(driver.specs) != 0 {
		t.Fatalf("the runtime was asked to boot %q for a record-only image", driver.specs[0].Image)
	}
	if !strings.Contains(log.String(), "CreateImage") {
		t.Fatalf("the refusal log does not say the image was registered: %s", log.String())
	}
}

// The accepting half: a served OMI still boots, with the login Outscale
// provisions on its images.
func TestAServedOmiBootsWithItsLogin(t *testing.T) {
	driver := &recordingDriver{}
	p := runtimePack(driver)
	res := &resource.Resource{
		ID:    "i-00000001",
		State: stateStopped,
		Attrs: map[string]any{"ImageId": "ami-00000002"},
	}

	p.powerOn(context.Background(), res)

	if res.State != stateRunning {
		t.Fatalf("state %q, want running: a guard that refuses everything breaks the product", res.State)
	}
	if len(driver.specs) != 1 {
		t.Fatalf("%d boots, want 1", len(driver.specs))
	}
	if got := driver.specs[0]; got.Image != "debian:12" || got.User != DefaultUser {
		t.Fatalf("booted image=%q user=%q, want debian:12 as %s", got.Image, got.User, DefaultUser)
	}
}
