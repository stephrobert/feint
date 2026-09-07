package machine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/stephrobert/feint/internal/core/resource"
)

// A value a client controls cannot forge a line in the machine layer's log.
//
// CodeQL reported go/log-injection thirteen times over binding.go and plan.go
// (alerts 40-44, 51-53 and 56-60, read live on 2026-09-06), and every one of
// them is a real flow: the image identifier a request carried, the address a
// pack asked to route, the address a membership carries, and the error a
// driver built out of one of those. None of them is a value of ours, however
// internal it looks — "well formed is not authorised" in CLAUDE.md: Runtime and
// Attrs are restored verbatim by PUT /_feint/state, and a snapshot is designed
// to travel.
//
// What is measured is what the layer hands to the logger, read through
// ReplaceAttr before the handler spells it. That choice is the whole test:
// slog.TextHandler quotes a newline on its own (internal/cli's
// TestALoggedClientValueCannotForgeALine measured it), so a test reading the
// handler's output would stay green with every sanitiser removed. The property
// this repository wants is on the value — one line, whatever handler an
// operator plugs in — and logline.Sanitise is what puts it there.
//
// tools/falsify/specs/a-log-line-cannot-be-forged.json removes each call in
// turn and requires the matching test here to go red.

// forgedLine is the payload an injection uses: end the record, start one that
// reads as the emulator's own. "evil" is the part a reader must still see.
const forgedLine = "evil\nlevel=ERROR msg=\"the emulator was compromised\" attacker=yes"

// recordedLogger reads every attribute as the layer wrote it, before the
// handler spells it.
func recordedLogger() (*slog.Logger, func() []slog.Attr) {
	var mu sync.Mutex
	var seen []slog.Attr
	h := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, a)
			return a
		},
	})
	return slog.New(h), func() []slog.Attr {
		mu.Lock()
		defer mu.Unlock()
		return append([]slog.Attr(nil), seen...)
	}
}

// assertOneLine requires every attribute handed to the logger to stand on one
// line, and the readable part of the payload to be in one of them: a value
// that was hidden rather than spelled would pass the first half and fail the
// second, which is the difference between sanitising and censoring.
func assertOneLine(t *testing.T, attrs []slog.Attr, readable string) {
	t.Helper()
	if len(attrs) == 0 {
		t.Fatal("nothing was logged, so nothing was measured")
	}
	seen := false
	for _, a := range attrs {
		v := a.Value.String()
		if i := strings.IndexFunc(v, unicode.IsControl); i >= 0 {
			t.Errorf("attribute %s reached the logger carrying a control character at %d: a handler "+
				"that does not quote writes it as a record of its own\n%s=%q", a.Key, i, a.Key, v)
		}
		if strings.Contains(v, readable) {
			seen = true
		}
	}
	if !seen {
		t.Errorf("no attribute carries %q: the value was hidden where an operator needs to read it", readable)
	}
}

// forgingDriver answers the way a real runtime does when it fails: with an
// error that repeats what it was handed, over several lines — incus prints
// its stderr into the error, and the name or the address in it is the
// client's.
type forgingDriver struct {
	recordingDriver
	startErr, routeErr, unrouteErr, attachErr error
}

func (d *forgingDriver) Start(ctx context.Context, spec Spec) (Machine, error) {
	if d.startErr != nil {
		return Machine{}, d.startErr
	}
	return d.recordingDriver.Start(ctx, spec)
}

func (d *forgingDriver) Attach(context.Context, string, Attachment) error { return d.attachErr }

func (d *forgingDriver) RouteAddress(context.Context, AddressSpec) error { return d.routeErr }

func (d *forgingDriver) UnrouteAddress(context.Context, string, string) error {
	return d.unrouteErr
}

func TestNoClientValueForgesALineInTheBootLog(t *testing.T) {
	ctx := context.Background()
	keys := []string{"ssh-ed25519 AAAA test"}

	t.Run("the identifier a refusal names", func(t *testing.T) {
		log, attrs := recordedLogger()
		b := bootBinding(&recordingDriver{})
		b.Log = log
		res := &resource.Resource{ID: "srv-1", State: "stopped"}
		if b.PowerOn(ctx, res, Boot{Requested: forgedLine}) {
			t.Fatal("a boot with no resolved image reported success")
		}
		assertOneLine(t, attrs(), "evil")
	})

	t.Run("the identifier a declaration resolved", func(t *testing.T) {
		log, attrs := recordedLogger()
		b := bootBinding(&recordingDriver{})
		b.Log = log
		b.Declared = map[string]Image{forgedLine: {Ref: "ubuntu:24.04"}}
		res := &resource.Resource{ID: "srv-1", State: "stopped"}
		if !b.PowerOn(ctx, res, Boot{Requested: forgedLine, AuthorizedKeys: keys}) {
			t.Fatal("the declared image did not boot")
		}
		assertOneLine(t, attrs(), "evil")
	})

	t.Run("the identifier a failed build names", func(t *testing.T) {
		log, attrs := recordedLogger()
		b := bootBinding(&buildingDriver{fail: errors.New("Failed getting remote image info")})
		b.Log = log
		res := &resource.Resource{ID: "srv-1", State: "stopped"}
		if b.PowerOn(ctx, res, Boot{Image: "debian:9", Requested: forgedLine, AuthorizedKeys: keys}) {
			t.Fatal("a boot whose build failed reported success")
		}
		assertOneLine(t, attrs(), "evil")
	})

	t.Run("the error a failed start carries", func(t *testing.T) {
		log, attrs := recordedLogger()
		b := bootBinding(&forgingDriver{startErr: errors.New("incus launch: " + forgedLine)})
		b.Log = log
		res := &resource.Resource{ID: "srv-1", State: "stopped"}
		if b.PowerOn(ctx, res, Boot{Image: "ubuntu:24.04", Requested: "ubuntu", AuthorizedKeys: keys}) {
			t.Fatal("a start the runtime refused reported success")
		}
		assertOneLine(t, attrs(), "evil")
	})
}

func TestNoClientValueForgesALineInTheAddressLog(t *testing.T) {
	ctx := context.Background()
	bench := func(d *forgingDriver) (Reconciler, func() []slog.Attr, *resource.Resource) {
		log, attrs := recordedLogger()
		b := bootBinding(d)
		b.Log = log
		r := Reconciler{
			Groups:      GroupSync{Binding: b},
			PlanOf:      func(*resource.Resource) Plan { return Plan{} },
			PublicBlock: netip.MustParsePrefix("203.0.113.0/24"),
		}
		res := &resource.Resource{
			ID:      "srv-1",
			State:   "running",
			Runtime: map[string]string{"machine": "feint-acme-srv-1"},
		}
		return r, attrs, res
	}

	// The three log calls are reached directly rather than through the
	// choreography around them: what is under test is the argument of the
	// call, and the order of the replay has its own witnesses in plan_test.go.
	t.Run("the address a route refuses", func(t *testing.T) {
		r, attrs, res := bench(&forgingDriver{})
		r.route(ctx, res, Plan{}, forgedLine)
		assertOneLine(t, attrs(), "evil")
	})

	t.Run("the error a route failure carries", func(t *testing.T) {
		r, attrs, res := bench(&forgingDriver{routeErr: errors.New("incus: " + forgedLine)})
		r.route(ctx, res, Plan{}, "203.0.113.9")
		assertOneLine(t, attrs(), "evil")
	})

	t.Run("the error an unroute failure carries", func(t *testing.T) {
		r, attrs, _ := bench(&forgingDriver{unrouteErr: errors.New("incus: " + forgedLine)})
		r.Unroute(ctx, "feint-acme-srv-1", "203.0.113.9")
		assertOneLine(t, attrs(), "evil")
	})

	t.Run("the address and the error an attach failure carries", func(t *testing.T) {
		r, attrs, res := bench(&forgingDriver{attachErr: errors.New("incus: " + forgedLine)})
		if err := r.attach(ctx, res, Attachment{Network: "fnt-acme-net", Address: forgedLine}); err == nil {
			t.Fatal("an attach the runtime refused reported success")
		}
		assertOneLine(t, attrs(), "evil")
	})
}
