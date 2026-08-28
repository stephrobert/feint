package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The three properties the incus-ovn leg of runtime-proof.yml demanded on
// 2026-08-28, when it failed at its own witness gate on a runner nothing had
// ever touched. Each is a sentence the report was getting wrong, and a report
// that names the wrong culprit, the wrong mark, or nothing at all, is an
// instrument rather than a finding.
//
// The two rule-set names below are the ones the job printed, digests of the
// providers' default security groups: a Scaleway project default, minted per
// run, and Exoscale's account default, whose identifier is fixed.
const (
	leakedScalewaySet = "scw-" + "45e6b24f03f"
	leakedExoscaleSet = "exo-" + "11e594f4819"
)

// TestTheClosingCheckBlamesTheRunThatLeaked: the same objects, the same
// refusal, and the culprit named as this run rather than a previous one.
//
// "a previous run left 0 machine(s) and 2 network(s) on this host" is what the
// job printed about objects its own ssh suites had created eight steps earlier.
// The refusal was right; the sentence sent the reader to a run that never
// existed.
func TestTheClosingCheckBlamesTheRunThatLeaked(t *testing.T) {
	quietDHCP(t)
	withDriver(t, machine.Use(&sweptDriver{
		left: machine.Leftovers{Networks: []string{"fnt-default", "feint-uplink"}},
	}))

	var closing bytes.Buffer
	err := reportStuckLeftovers(&closing, newLedger(&closing, false, time.Now()), "incus-ovn", momentClosing)
	if err == nil {
		t.Fatal("the closing check accepted a host this run left two networks on")
	}
	if strings.Contains(closing.String(), "previous run") || strings.Contains(err.Error(), "earlier run") {
		t.Errorf("the closing check blames a run that does not exist:\n%s\n%v", closing.String(), err)
	}
	if !strings.Contains(closing.String(), "this run") {
		t.Errorf("the closing check never says whose leak it found:\n%s", closing.String())
	}

	// The witness: the doorstep must keep saying the opposite, or this test
	// passes on code that simply renamed the sentence everywhere.
	withDriver(t, machine.Use(&sweptDriver{
		left: machine.Leftovers{Networks: []string{"fnt-default", "feint-uplink"}},
	}))
	var door bytes.Buffer
	if err := reportStuckLeftovers(&door, newLedger(&door, false, time.Now()), "incus-ovn", momentDoorstep); err == nil {
		t.Fatal("the doorstep accepted the same host")
	}
	if !strings.Contains(door.String(), "previous run") {
		t.Errorf("the doorstep stopped naming the earlier run it exists to name:\n%s", door.String())
	}
}

// TestTheDoorstepNamesRuleSetsItDoesNotRefuseOn: a host holding nothing but
// rule sets of this emulator is not refused — they hold no address block — and
// it must still be told about them.
//
// Until this, they were printed only when some *other* object had already made
// the check refuse, so the one artefact that accumulates once per session on an
// operator's station was invisible exactly on the hosts where it was all that
// was left.
func TestTheDoorstepNamesRuleSetsItDoesNotRefuseOn(t *testing.T) {
	quietDHCP(t)
	withDriver(t, machine.Use(&sweptDriver{
		left: machine.Leftovers{Firewalls: []string{leakedScalewaySet, leakedExoscaleSet}},
	}))

	var out bytes.Buffer
	if err := reportStuckLeftovers(&out, newLedger(&out, false, time.Now()), "incus-ovn", momentDoorstep); err != nil {
		t.Fatalf("a rule set holds no address block, so it must not refuse a run: %v", err)
	}
	for _, name := range []string{leakedScalewaySet, leakedExoscaleSet} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("the check found %s and said nothing about it:\n%s", name, out.String())
		}
	}
	if !strings.Contains(out.String(), "feint clean") {
		t.Errorf("the remainder is admitted without naming what removes it:\n%s", out.String())
	}

	// The witness: a host holding none must not grow the paragraph, or every
	// clean run would read as one holding waste.
	withDriver(t, machine.Use(&sweptDriver{}))
	var clean bytes.Buffer
	if err := reportStuckLeftovers(&clean, newLedger(&clean, false, time.Now()), "incus-ovn", momentDoorstep); err != nil {
		t.Fatalf("the check refused a runtime holding nothing: %v", err)
	}
	if strings.Contains(clean.String(), "rule set(s) of this emulator") {
		t.Errorf("a host holding no rule set was told it holds some:\n%s", clean.String())
	}
}

// TestTheLedgerAttributesEachObjectToTheMarkItWasFoundBy: the column that says
// how this run knows an object is the emulator's must name a mark the object
// actually carries.
//
// It did not. Networks were recorded as `name-prefix:fnt-` while Survey selects
// them by `user.feint.provider`, exactly as it does machines — and the first
// network the leg of 2026-08-28 reported was `feint-uplink`, which carries no
// such prefix. A column that can name a mark the object does not carry cannot
// be used to decide what may be touched, which is the only thing it is for.
func TestTheLedgerAttributesEachObjectToTheMarkItWasFoundBy(t *testing.T) {
	quietDHCP(t)
	withDriver(t, machine.Use(&sweptDriver{
		left: machine.Leftovers{
			Machines:  []string{"feint-scw-a"},
			Networks:  []string{"feint-uplink"},
			Firewalls: []string{leakedScalewaySet},
		},
	}))

	var out bytes.Buffer
	if err := reportStuckLeftovers(&out, newLedger(&out, true, time.Now()), "incus-ovn", momentDoorstep); err == nil {
		t.Fatal("the doorstep accepted a host holding a machine and a network")
	}
	want := map[string]string{
		"feint-scw-a":     "label:" + machine.LabelKey,
		"feint-uplink":    "label:" + machine.LabelKey,
		leakedScalewaySet: "description:" + machine.FirewallDescription,
	}
	for _, rec := range ledgerLines(t, out.String()) {
		expected, known := want[rec.Name]
		if !known {
			continue
		}
		if rec.Attribution != expected {
			t.Errorf("%s is attributed to %q, and it carries %q", rec.Name, rec.Attribution, expected)
		}
		delete(want, rec.Name)
	}
	for name := range want {
		t.Errorf("%s never appeared in the ledger at all", name)
	}
}

// TestTheTwoMomentsCannotBeAskedAtOnce: --doorstep and --closing name opposite
// culprits, so a caller who typed both asked for a sentence that cannot be
// true. Resolving it in either direction would print a confident lie, which is
// the whole subject of the leg that made the flag exist.
func TestTheTwoMomentsCannotBeAskedAtOnce(t *testing.T) {
	var out bytes.Buffer
	err := clean([]string{"--check", "--doorstep", "--closing", "--vm", "incus"}, &out)
	if err == nil {
		t.Fatal("clean accepted --doorstep and --closing together and picked one of the two sentences")
	}
	if !strings.Contains(err.Error(), "one of the two") {
		t.Errorf("the refusal does not tell the caller what to do: %v", err)
	}
}
