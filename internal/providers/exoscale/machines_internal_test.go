package exoscale

import (
	"context"
	"io"
	"log/slog"
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
	// detached keeps every "machine network" pair Detach was called with, so a
	// test can assert the command was emitted rather than trust a 200 (#426).
	detached []string
}

func (d *recordingDriver) Name() string                   { return "recording" }
func (d *recordingDriver) Available(context.Context) bool { return true }
func (d *recordingDriver) Start(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	d.specs = append(d.specs, spec)
	return machine.Machine{Name: spec.Name, Addresses: []string{"10.42.0.9"}, Running: true}, nil
}
func (d *recordingDriver) Stop(context.Context, string) error   { return nil }
func (d *recordingDriver) Remove(context.Context, string) error { return nil }
func (d *recordingDriver) Inspect(_ context.Context, name string) (machine.Machine, bool, error) {
	return machine.Machine{Name: name}, false, nil
}
func (d *recordingDriver) EnsureNetwork(context.Context, machine.NetworkSpec) error { return nil }
func (d *recordingDriver) Attach(context.Context, string, machine.Attachment) error { return nil }
func (d *recordingDriver) Detach(_ context.Context, name, network string) error {
	d.detached = append(d.detached, name+" "+network)
	return nil
}
func (d *recordingDriver) RemoveNetwork(context.Context, string) error { return nil }

func runtimePack(driver machine.Runtime) *Pack {
	env := &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string { return "00000000-0000-4000-8000-000000000001" },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env.UseMachines(driver)
	return New(env)
}

// One case per row of the template table: the boot and the login the template
// declares resolve together — the login is a property of the template on
// Exoscale, which is the reason Boot.User exists at all — and an identifier
// outside the table resolves to nothing.
func TestExoscaleTemplateResolutionIsExact(t *testing.T) {
	rows := map[string]machine.Image{
		"11111111-1111-4111-8111-111111111111": {Ref: "ubuntu:24.04", User: "ubuntu"},
		"22222222-2222-4222-8222-222222222222": {Ref: "debian:12", User: "debian"},
	}
	if len(rows) != len(runtimeTemplates) {
		t.Fatalf("the table serves %d templates and this test knows %d: add the row here, one per template", len(runtimeTemplates), len(rows))
	}
	for id, want := range rows {
		got, known := templateFor(id)
		if !known {
			t.Errorf("%s: a served template stopped resolving", id)
			continue
		}
		if got != want {
			t.Errorf("%s: resolved to %+v, want %+v — the template's own default-user must travel with it", id, got, want)
		}
	}
	for _, id := range []string{"99999999-9999-4999-8999-999999999999", "totalement-inconnue", ""} {
		if img, known := templateFor(id); known {
			t.Errorf("%q: resolved to %+v, want no resolution at all", id, img)
		}
	}
}

// The wiring of #83 for this pack: an unknown template under a runtime
// reaches error — the state Exoscale's own instance-state enum declares —
// and the runtime is never asked for anything.
func TestAnUnknownTemplateDoesNotBootASubstitute(t *testing.T) {
	driver := &recordingDriver{}
	p := runtimePack(machine.Use(driver))
	res := &resource.Resource{
		ID:    "00000000-0000-4000-8000-0000000000aa",
		State: "stopped",
		Attrs: map[string]any{
			"name":     "demo",
			"template": map[string]any{"id": "99999999-9999-4999-8999-999999999999"},
		},
	}

	p.start(context.Background(), res)

	if res.State != "error" {
		t.Fatalf("state %q, want error: running on a substitute is the defect under test", res.State)
	}
	if len(driver.specs) != 0 {
		t.Fatalf("the runtime was asked to boot %q for a template nobody serves", driver.specs[0].Image)
	}
}

// The accepting half: a served template still boots, as its own default-user.
func TestAServedTemplateBootsAsItsDefaultUser(t *testing.T) {
	driver := &recordingDriver{}
	p := runtimePack(machine.Use(driver))
	res := &resource.Resource{
		ID:    "00000000-0000-4000-8000-0000000000ab",
		State: "stopped",
		Attrs: map[string]any{
			"name":     "demo",
			"template": map[string]any{"id": "22222222-2222-4222-8222-222222222222"},
		},
	}

	p.start(context.Background(), res)

	if res.State != "running" {
		t.Fatalf("state %q, want running: a guard that refuses everything breaks the product", res.State)
	}
	if len(driver.specs) != 1 {
		t.Fatalf("%d boots, want 1", len(driver.specs))
	}
	if got := driver.specs[0]; got.Image != "debian:12" || got.User != "debian" {
		t.Fatalf("booted image=%q user=%q, want debian:12 as debian", got.Image, got.User)
	}
}

// The address an instance publishes as public-ip carries a kind, and the kind
// is checked (#541).
//
// Measured on 2026-08-27 under `--vm incus-ovn`, before the fix: an instance
// created with `public-ip-assignment: "none"` and joined to a private network
// answered `GET /v2/instance/{id}` with `"public-ip": "10.44.9.10"` — its
// private-network address — while the machine itself carried exactly what was
// asked, one NIC and nothing public. The fallback in view() had exactly one
// live population, the instances that must publish no public address at all.
//
// Both halves here, because a guard that refuses everything passes every
// attack test and breaks the product: an address of the emulated elastic block
// is still published, and one outside it never is.
func TestAnInstanceWithNoPublicIPPublishesNone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address string
		want    string
	}{
		{"a private-network address is not a public one", "10.44.9.10", ""},
		{"an address of the emulated elastic block is", "192.0.2.7", "192.0.2.7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := runtimePack(machine.Use(&recordingDriver{}))
			res := &resource.Resource{
				ID:    "00000000-0000-4000-8000-0000000000c1",
				State: "running",
				Attrs: map[string]any{
					"name":                 "audit-541",
					"public-ip-assignment": "none",
				},
				Runtime: map[string]string{
					"machine": "feint-exo-00000000-0000-4000-8000-0000000000c1",
					"address": tc.address,
				},
			}

			published, _ := p.view(res)["public-ip"].(string)
			if published != tc.want {
				t.Fatalf("public-ip %q for a machine answering on %s, want %q",
					published, tc.address, tc.want)
			}
		})
	}
}
