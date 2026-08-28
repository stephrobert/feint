package machine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
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

func bootBinding(d driver) Binding {
	return Binding{
		driver:       d,
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
	for name, driver := range map[string]driver{"noop": Noop{}, "nil": nil} {
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

// buildingDriver is a recordingDriver that can also be asked what it holds and
// told to build — the two optional halves EnsureImage drives. It fails on
// demand, so the refusal path is testable without a runtime.
type buildingDriver struct {
	recordingDriver
	held  map[string]string
	built []ImageSpec
	fail  error
}

func (d *buildingDriver) LocalImages(context.Context) (map[string]string, error) {
	return d.held, nil
}

func (d *buildingDriver) BuildImage(_ context.Context, spec ImageSpec, _ io.Writer) error {
	if d.fail != nil {
		return d.fail
	}
	d.built = append(d.built, spec)
	if d.held == nil {
		d.held = map[string]string{}
	}
	d.held[spec.Alias()] = "made-by-test"
	return nil
}

// failingDriver is a recordingDriver whose Start refuses, so the difference
// between "this emulator declined" and "the runtime failed" can be asserted
// without one.
type failingDriver struct {
	recordingDriver
	err error
}

func (d *failingDriver) Start(context.Context, Spec) (Machine, error) {
	return Machine{}, d.err
}

func TestABootDerivesAndBuildsTheImageItNames(t *testing.T) {
	t.Run("a version the station lacks is built on the boot path", func(t *testing.T) {
		driver := &buildingDriver{}
		b := bootBinding(driver)
		res := &resource.Resource{ID: "srv-1", State: "stopped"}

		// debian:13 is the measured hole of #465: the Scaleway catalogue serves
		// debian_trixie mapped on it, and the fixed table never built it.
		ok := b.PowerOn(context.Background(), res, Boot{
			Image: "debian:13", Requested: "debian_trixie",
			AuthorizedKeys: []string{"ssh-ed25519 AAAA test"},
		})
		if !ok || res.State != "running" {
			t.Fatalf("ok=%v state=%q, want a machine that runs", ok, res.State)
		}
		if len(driver.built) != 1 || driver.built[0].Alias() != "feint/debian/13" {
			t.Fatalf("built %v, want exactly feint/debian/13 derived from the ref", driver.built)
		}
		if len(driver.specs) != 1 || driver.specs[0].Image != "debian:13" {
			t.Fatalf("booted %v, want one boot of the derived ref", driver.specs)
		}
	})

	t.Run("an image the station holds is not rebuilt", func(t *testing.T) {
		driver := &buildingDriver{held: map[string]string{"feint/debian/13": "already"}}
		b := bootBinding(driver)
		res := &resource.Resource{ID: "srv-1", State: "stopped"}

		b.PowerOn(context.Background(), res, Boot{
			Image: "debian:13", Requested: "debian_trixie",
			AuthorizedKeys: []string{"ssh-ed25519 AAAA test"},
		})
		if len(driver.built) != 0 {
			t.Fatalf("built %v for an image the station already holds", driver.built)
		}
	})

	t.Run("an unknown family keeps the announced fallback", func(t *testing.T) {
		driver := &buildingDriver{}
		b := bootBinding(driver)
		res := &resource.Resource{ID: "srv-1", State: "stopped"}

		// plan9:4 derives nothing; the boot must go on to the driver, whose own
		// resolveImage announces the upstream fallback. Building here would be
		// "build anything", the guard #465 says must hold.
		ok := b.PowerOn(context.Background(), res, Boot{
			Image: "plan9:4", Requested: "plan9",
			AuthorizedKeys: []string{"ssh-ed25519 AAAA test"},
		})
		if !ok || len(driver.built) != 0 || len(driver.specs) != 1 {
			t.Fatalf("ok=%v built=%v specs=%d, want a boot with nothing built", ok, driver.built, len(driver.specs))
		}
	})
}

func TestAFailedImageBuildRefusesTheBootAndNamesTheSource(t *testing.T) {
	driver := &buildingDriver{fail: errors.New("Failed getting remote image info")}
	var log bytes.Buffer
	b := bootBinding(driver)
	b.Log = slog.New(slog.NewTextHandler(&log, nil))
	res := &resource.Resource{ID: "srv-1", State: "stopped"}

	// debian:9 is a real witness, not a hypothesis: images: withdrew it, and
	// two of the surveyed stacks still hardcode identifiers that name it.
	ok := b.PowerOn(context.Background(), res, Boot{
		Image: "debian:9", Requested: "ami-47899c77",
		AuthorizedKeys: []string{"ssh-ed25519 AAAA test"},
	})
	if ok || res.State != "failed" {
		t.Fatalf("ok=%v state=%q, want a refused boot in the failed state", ok, res.State)
	}
	if len(driver.specs) != 0 {
		t.Fatal("the runtime was asked to boot an image whose build had just failed")
	}
	for _, needle := range []string{"images:debian/9/cloud", "debian:9", "ami-47899c77"} {
		if !strings.Contains(log.String(), needle) {
			t.Errorf("the refusal does not name %q; a reader cannot act on it\nlog: %s", needle, log.String())
		}
	}
}

func TestADeclaredIdentifierBootsTheImageTheOperatorNamed(t *testing.T) {
	t.Run("the declaration resolves what no catalogue holds", func(t *testing.T) {
		driver := &recordingDriver{}
		b := bootBinding(driver)
		b.Declared = map[string]Image{"ami-a3ca408c": {Ref: "ubuntu:22.04", User: "ubuntu"}}
		res := &resource.Resource{ID: "srv-1", State: "stopped"}

		ok := b.PowerOn(context.Background(), res, Boot{
			Requested:      "ami-a3ca408c",
			AuthorizedKeys: []string{"ssh-ed25519 AAAA test"},
		})
		if !ok || res.State != "running" {
			t.Fatalf("ok=%v state=%q, want the declared image to boot", ok, res.State)
		}
		if len(driver.specs) != 1 || driver.specs[0].Image != "ubuntu:22.04" || driver.specs[0].User != "ubuntu" {
			t.Fatalf("booted %+v, want the ref and login the operator declared", driver.specs)
		}
	})

	t.Run("a catalogue entry always wins over a declaration", func(t *testing.T) {
		driver := &recordingDriver{}
		b := bootBinding(driver)
		b.Declared = map[string]Image{"alpine_3.21": {Ref: "ubuntu:22.04"}}
		res := &resource.Resource{ID: "srv-1", State: "stopped"}

		b.PowerOn(context.Background(), res, Boot{
			Image: "alpine:3.21", Requested: "alpine_3.21",
			AuthorizedKeys: []string{"ssh-ed25519 AAAA test"},
		})
		if len(driver.specs) != 1 || driver.specs[0].Image != "alpine:3.21" {
			t.Fatalf("booted %+v, want the catalogue's resolution untouched", driver.specs)
		}
	})
}

func TestTheBootRefusalNamesTheGesturesThatUnblock(t *testing.T) {
	driver := &recordingDriver{}
	var log bytes.Buffer
	b := bootBinding(driver)
	b.Log = slog.New(slog.NewTextHandler(&log, nil))
	res := &resource.Resource{ID: "srv-1", State: "stopped"}

	if b.PowerOn(context.Background(), res, Boot{Requested: "ami-deadf00d"}) {
		t.Fatal("an undeclared unknown identifier booted")
	}
	// The refusal is only actionable if a reader can apply it without opening
	// the code: the identifier received, the lookup that needs no account, and
	// the declaration that unblocks — each named verbatim.
	for _, needle := range []string{"ami-deadf00d", "feint images resolve", "FEINT_BOOT_IMAGES", "ubuntu"} {
		if !strings.Contains(log.String(), needle) {
			t.Errorf("the refusal does not carry %q\nlog: %s", needle, log.String())
		}
	}
}

// A refusal this emulator declares is a WARN; only what it failed to do is an
// ERROR (#474).
//
// The measurement that scoped it: replaying fifteen surveyed stacks under
// `--vm incus-ovn`, five runs printed level=ERROR, fourteen lines in all, and
// every one was this refusal. The run that printed five of them was a success
// — ztiac applied 54 of 54, matched its reference and destroyed 54 cleanly. An
// operator grepping ERROR to find what went wrong found fourteen lines about a
// documented behaviour and nothing about the run that really failed, which is
// how a log teaches people to skip its errors.
//
// The sibling refusal 200 ms later, in the same log, was already a WARN:
// loadbalancer_dataplane.go's ErrBalancerNotDistributed (#457), whose comment
// states the rule — "a limit is not an incident". This is that rule applied to
// the other refusal in the same layer.
//
// The line separating the two, measured over the 48 ERROR sites in internal/ on
// 2026-08-28: an ERROR is something this emulator did not do that it was built
// to do; a WARN is something it deliberately declines and documents, where the
// API answer stays honest. Exactly one site was on the wrong side — this one.
// Its neighbours stay ERROR and are asserted here, because a change that made
// every refusal a warning would pass the first half of this test and hide the
// failures the log exists for:
//
//   - a start the driver refused (the runtime failed at something it accepted);
//   - a pack that declares no interface plan (a fault in the pack itself, not
//     a decline, and plan.go says why).
//
// The API answer is unchanged and asserted too: the machine still does not
// boot, and the resource still reads FailedState. Lowering the level must not
// quiet the refusal, only stop it claiming to be an incident.
func TestADocumentedRefusalIsAWarningAndAFailureStaysAnError(t *testing.T) {
	t.Run("the image refusal warns", func(t *testing.T) {
		var log bytes.Buffer
		b := bootBinding(&recordingDriver{})
		b.Log = slog.New(slog.NewTextHandler(&log, nil))
		res := &resource.Resource{ID: "srv-1", State: "stopped"}

		if b.PowerOn(context.Background(), res, Boot{Requested: "ami-538af795"}) {
			t.Fatal("an undeclared identifier booted")
		}
		if res.State != "failed" {
			t.Errorf("the resource reads %q, want failed: the level moved, the answer must not", res.State)
		}
		if strings.Contains(log.String(), "level=ERROR") {
			t.Errorf("the documented refusal is logged at ERROR:\n%s", log.String())
		}
		if !strings.Contains(log.String(), "level=WARN") {
			t.Errorf("the refusal is not logged at WARN, so it is quieter than a limit should be:\n%s", log.String())
		}
		// Still actionable at the lower level: the level is the only thing
		// that changed.
		for _, needle := range []string{"ami-538af795", "feint images resolve", "FEINT_BOOT_IMAGES"} {
			if !strings.Contains(log.String(), needle) {
				t.Errorf("the refusal lost %q on the way down:\n%s", needle, log.String())
			}
		}
	})

	t.Run("a start the runtime refused stays an error", func(t *testing.T) {
		var log bytes.Buffer
		b := bootBinding(&failingDriver{err: errors.New("the runtime said no")})
		b.Log = slog.New(slog.NewTextHandler(&log, nil))
		res := &resource.Resource{ID: "srv-2", State: "stopped"}

		if b.PowerOn(context.Background(), res, Boot{Image: "ubuntu:22.04", Requested: "ubuntu_jammy"}) {
			t.Fatal("a start the driver refused reported success")
		}
		if !strings.Contains(log.String(), "level=ERROR") {
			t.Errorf("a runtime failure is not an ERROR any more, so this change made the log quieter "+
				"about the thing it exists for:\n%s", log.String())
		}
	})

	t.Run("a pack with no interface plan stays an error", func(t *testing.T) {
		var log bytes.Buffer
		b := bootBinding(&recordingDriver{})
		b.Log = slog.New(slog.NewTextHandler(&log, nil))
		r := Reconciler{Groups: GroupSync{Binding: b}}
		if _, ok := r.plan(&resource.Resource{ID: "srv-3"}); ok {
			t.Fatal("a nil PlanOf answered a plan")
		}
		if !strings.Contains(log.String(), "level=ERROR") {
			t.Errorf("a pack that declares no plan is a fault in the pack, not a decline:\n%s", log.String())
		}
	})
}

// A client cloud-config that declares a package step cannot complete on a
// machine booting with no emulated network under it (#507): no NAT, no
// resolver, no route to a package repository (#202). The guest's own journal
// is the only place cloud-init reports the resulting `status: error`, and
// nobody opens it — so the shared layer says it in the emulator's log, once,
// at boot. Both halves are asserted: the shapes that must warn, and the shapes
// that must not, because a warning that fires for every machine is one nobody
// reads either.
func TestAPackageStepWithNoRouteOutIsSaidOutLoud(t *testing.T) {
	const packagesConfig = "#cloud-config\npackage_update: true\npackages:\n  - nginx\n"
	cases := map[string]struct {
		boot Boot
		warn bool
	}{
		"packages on a routed machine": {
			boot: Boot{Image: "ubuntu:22.04", Requested: "ubuntu_jammy", CloudInit: packagesConfig,
				PublicAddresses: []string{"203.0.113.9"}},
			warn: true,
		},
		"package_update alone on a routed machine": {
			boot: Boot{Image: "ubuntu:22.04", Requested: "ubuntu_jammy",
				CloudInit: "#cloud-config\npackage_update: true\n"},
			warn: true,
		},
		"packages on a machine with an emulated network": {
			boot: Boot{Image: "ubuntu:22.04", Requested: "ubuntu_jammy", CloudInit: packagesConfig,
				Attachments: []Attachment{{Network: "fnt-sub1"}}},
			warn: false,
		},
		"a shell script whose heredoc carries a column-zero packages line": {
			boot: Boot{Image: "ubuntu:22.04", Requested: "ubuntu_jammy",
				CloudInit: "#!/bin/sh\ncat > /tmp/x <<'E'\npackages:\n  - nginx\nE\n"},
			warn: false,
		},
		"packages indented inside write_files content": {
			boot: Boot{Image: "ubuntu:22.04", Requested: "ubuntu_jammy",
				CloudInit: "#cloud-config\nwrite_files:\n  - path: /etc/x\n    content: |\n      packages:\n"},
			warn: false,
		},
		"package_update declared false": {
			boot: Boot{Image: "ubuntu:22.04", Requested: "ubuntu_jammy",
				CloudInit: "#cloud-config\npackage_update: false\n"},
			warn: false,
		},
		"no client user data at all": {
			boot: Boot{Image: "ubuntu:22.04", Requested: "ubuntu_jammy",
				AuthorizedKeys: []string{"ssh-ed25519 AAAA test"}},
			warn: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			driver := &recordingDriver{}
			b := bootBinding(driver)
			var log bytes.Buffer
			b.Log = slog.New(slog.NewTextHandler(&log, nil))
			res := &resource.Resource{ID: "srv-1", State: "stopped"}
			if !b.PowerOn(context.Background(), res, tc.boot) {
				t.Fatal("the boot itself must proceed: the limit degrades the guest, not the control plane")
			}
			said := strings.Contains(log.String(), "package step it cannot complete")
			if said != tc.warn {
				t.Fatalf("warned=%v, want %v; log:\n%s", said, tc.warn, log.String())
			}
		})
	}

	// And never with a metadata-only runtime: nothing boots, so nothing fails.
	b := bootBinding(Noop{})
	var log bytes.Buffer
	b.Log = slog.New(slog.NewTextHandler(&log, nil))
	res := &resource.Resource{ID: "srv-1", State: "stopped"}
	b.PowerOn(context.Background(), res, Boot{Requested: "ubuntu_jammy", CloudInit: packagesConfig})
	if strings.Contains(log.String(), "package step") {
		t.Fatalf("a metadata-only boot warned about a machine that does not exist; log:\n%s", log.String())
	}
}
