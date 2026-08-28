package machine

import (
	"context"
	"io"
)

// Runtime is the only handle on a machine runtime that leaves this package.
//
// # Why it exists
//
// #511 unexported emulator.Env's driver field and machine.Binding's, so a pack
// could no longer *obtain* a driver value. It could still *name* the type: on
// 154c204 this file, dropped into internal/providers/scaleway, compiled and
// `go build ./internal/providers/scaleway/` exited 0 —
//
//	package scaleway
//	import "github.com/stephrobert/feint/internal/core/machine"
//	var _ machine.Driver
//
// — which left the boundary held by a convention plus an AST scan, exactly
// what #514 §2.1 says is not enough. So Driver and the five halves a pack
// would reach for (Router, Firewaller, Peerer, Isolator, Balancer) are
// unexported now, and a pack that writes any of those names fails the build.
// internal/cli's TestThePacksCannotNameTheDriver compiles that very file and
// requires the failure, with a companion that must still compile so a probe
// broken for some other reason cannot read as the door being shut.
//
// # Why the operator side needs a handle at all
//
// internal/cli builds the runtime the operator asked for (`--vm`), asks it
// what the host delivers, sweeps it on the way out, and substitutes doubles
// for it in some forty tests. None of that is a pack reaching past the shared
// layer, and none of it can name a driver type any more. This is what it names
// instead.
//
// # Why a struct and not an interface
//
// An interface narrowed to Name and Available would be defeated in one line,
// because a type assertion needs no name:
//
//	rt.(interface{ Remove(context.Context, string) error }).Remove(ctx, victim)
//
// That is not hypothetical — internal/cli already reaches two capabilities
// that way (a Verify half in machineDriver, a RemoveImage half in `feint
// images remove`), so the shape is in the repository and would be copied. A
// struct with an unexported field cannot be asserted on at all, and its method
// set is therefore the whole of what a holder can do.
//
// # What it deliberately does not offer
//
// Nothing that moves a machine, a network, an address or a rule set: no Start,
// Stop, Remove, Inspect, Attach, Detach, EnsureNetwork, RemoveNetwork,
// RouteAddress, EnsureFirewall, PeerNetworks, EnsureBalancer. Those belong to
// Binding, Reconciler and GroupSync, which apply the ownership checks and the
// one order, and machine.PackSurface is the list of what a pack may ask them
// for. What is here is the operator's half — identity, capability, sweep,
// repair, images — the half `feint doctor`, `feint clean` and `feint images`
// exist to serve.
//
// The zero value is the metadata-only runtime: no machine ever starts, which
// is what `--vm off` and CI get.
type Runtime struct {
	d driver
}

// Use binds a driver into the handle. It is the one door in, and deliberately
// one-way: nothing hands the driver back out.
//
// The parameter type is unexported on purpose. A caller outside this package
// can still pass any value whose method set satisfies it — which is how
// internal/cli passes the Incus driver, and how every fake runtime in the
// packs' tests is still injected — but it cannot declare a variable of that
// type, name it in a signature, or assert its way back to one.
func Use(d driver) Runtime { return Runtime{d: d} }

// backing returns the driver, or the metadata-only one for a zero Runtime, so
// every method below can be written without a nil branch.
func (r Runtime) backing() driver {
	if r.d == nil {
		return Noop{}
	}
	return r.d
}

// Name identifies the runtime in logs, in `feint status` and on
// /_feint/health.
func (r Runtime) Name() string { return r.backing().Name() }

// Available reports whether the runtime can actually run anything.
func (r Runtime) Available(ctx context.Context) bool { return r.backing().Available(ctx) }

// Runs reports whether anything is actually backed by a host. False is the
// metadata-only runtime: servers change state and nothing starts.
//
// It is one question asked in five places — the serve write deadline, `feint
// doctor`, `feint up`, `feint clean` and `feint images` each used to write
// `driver.(machine.Noop)` for themselves — and a question written five times
// is a question one caller answers differently.
func (r Runtime) Runs() bool {
	if r.d == nil {
		return false
	}
	_, none := r.d.(Noop)
	return !none
}

// Capabilities reports what this runtime delivers. See CapabilitiesOf: an
// undeclared capability counts as absent, so a check skips rather than
// asserting what nobody promised.
func (r Runtime) Capabilities() Capabilities { return CapabilitiesOf(r.backing()) }

// DeclaredCapabilities is the nullable half CapabilitiesOf cannot express:
// nil when the driver declares nothing, so /_feint/health can tell "this
// runtime says it cannot" from "this runtime was never asked". See Declared.
func (r Runtime) DeclaredCapabilities() *Capabilities { return Declared(r.backing()) }

// Verify asks the host what it actually delivers and names what it narrowed.
//
// A driver with no verification half answers its declared capabilities and an
// empty list, which is the honest reading of "nobody could check": it claims
// no narrowing it did not measure.
func (r Runtime) Verify(ctx context.Context) (Capabilities, []string) {
	v, ok := r.backing().(interface {
		Verify(context.Context) (Capabilities, []string)
	})
	if !ok {
		return r.Capabilities(), nil
	}
	return v.Verify(ctx)
}

// Survey names the labelled machines, networks and rule sets this runtime
// still holds, touching none of them.
//
// Three outcomes, never two: asked reports whether the runtime could be asked
// at all, because a runtime nobody looked at is not an empty host. That
// distinction is measurement-integrity's second rule and it is why this
// returns a bool the callers must read.
func (r Runtime) Survey(ctx context.Context) (left Leftovers, asked bool, err error) {
	s, ok := r.backing().(Surveyor)
	if !ok {
		return Leftovers{}, false, nil
	}
	left, err = s.Survey(ctx)
	return left, true, err
}

// Sweeps reports whether this runtime can be swept at all, which `feint clean`
// asks before it decides what an empty result means: a runtime nobody can
// sweep is not a clean one.
func (r Runtime) Sweeps() bool {
	_, ok := r.backing().(Pruner)
	return ok
}

// Prune removes everything this runtime carries the emulator's label on.
// asked is false for a runtime that cannot sweep.
func (r Runtime) Prune(ctx context.Context) (pruned Pruned, asked bool, err error) {
	p, ok := r.backing().(Pruner)
	if !ok {
		return Pruned{}, false, nil
	}
	pruned, err = p.Prune(ctx)
	return pruned, true, err
}

// ReleasePlumbing gives back the host objects no resource delete removes, when
// and only when this process is the one holding them and nothing draws from
// them. It names what went. asked is false for a runtime with no plumbing to
// give back.
func (r Runtime) ReleasePlumbing(ctx context.Context) (released []string, asked bool, err error) {
	u, ok := r.backing().(PlumbingReleaser)
	if !ok {
		return nil, false, nil
	}
	released, err = u.ReleasePlumbing(ctx)
	return released, true, err
}

// Traps names the states this runtime's own sweep cannot get out of. It issues
// no mutating command. asked is false for a runtime that cannot be asked.
func (r Runtime) Traps(ctx context.Context) (traps []Trap, asked bool, err error) {
	rep, ok := r.backing().(Repairer)
	if !ok {
		return nil, false, nil
	}
	traps, err = rep.Traps(ctx)
	return traps, true, err
}

// Repair clears the traps that can be cleared and returns those it cleared. It
// reaches past the runtime's own commands, so it runs only when an operator
// asks for it by name.
func (r Runtime) Repair(ctx context.Context) (cleared []Trap, asked bool, err error) {
	rep, ok := r.backing().(Repairer)
	if !ok {
		return nil, false, nil
	}
	cleared, err = rep.Repair(ctx)
	return cleared, true, err
}

// Watch streams what the runtime is doing until the context is cancelled.
// asked is false for a runtime that cannot report.
func (r Runtime) Watch(ctx context.Context) (events <-chan Event, asked bool, err error) {
	w, ok := r.backing().(Watcher)
	if !ok {
		return nil, false, nil
	}
	events, err = w.Watch(ctx)
	return events, true, err
}

// BuildsImages reports whether this runtime can build the images
// RequiredImages names.
func (r Runtime) BuildsImages() bool {
	_, ok := r.backing().(ImageBuilder)
	return ok
}

// LocalImages answers which of the emulator's images the station holds, keyed
// by alias. asked is false for a runtime that cannot be asked, which is not
// the same as a station holding none.
func (r Runtime) LocalImages(ctx context.Context) (held map[string]string, asked bool, err error) {
	l, ok := r.backing().(ImageLister)
	if !ok {
		return nil, false, nil
	}
	held, err = l.LocalImages(ctx)
	return held, true, err
}

// RemovesImages reports whether this runtime holds images it can be asked to
// remove, so `feint images remove` refuses before it names anything.
func (r Runtime) RemovesImages() bool {
	_, ok := r.backing().(imageRemover)
	return ok
}

// RemoveImage deletes one image this emulator published, named the way the
// operator types it — "<family>/<version>", without the emulator's own prefix,
// which the driver adds. asked is false for a runtime that holds no images to
// remove.
func (r Runtime) RemoveImage(ctx context.Context, name string) (asked bool, err error) {
	rm, ok := r.backing().(imageRemover)
	if !ok {
		return false, nil
	}
	return true, rm.RemoveImage(ctx, name)
}

// imageRemover is the optional half `feint images remove` drives. It was an
// anonymous interface at the call site until #514, which is the shape a
// Runtime holder can no longer write: an assertion needs no name, so a handle
// that returned a driver-shaped value would hand back everything this one
// exists to withhold.
type imageRemover interface {
	RemoveImage(ctx context.Context, alias string) error
}

// BuildImage builds one image unless the station already holds it, and reports
// whether it built anything. It is BuildIfMissing's door for a Runtime holder;
// the exclusion and the second look live there.
func (r Runtime) BuildImage(ctx context.Context, spec ImageSpec, progress io.Writer) (bool, error) {
	return BuildIfMissing(ctx, r.backing(), spec, progress)
}

// Inventory is what RequiredImages asks of this station: one status per image
// the emulator needs, present or not.
func (r Runtime) Inventory(ctx context.Context) ([]ImageStatus, error) {
	return ImageInventory(ctx, r.backing())
}

// DerivedInventory is the same reading for the images a boot derives.
func (r Runtime) DerivedInventory(ctx context.Context) ([]ImageStatus, error) {
	return DerivedImages(ctx, r.backing())
}
