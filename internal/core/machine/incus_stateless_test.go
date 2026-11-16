package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// A stateless rule set on a mode that cannot enforce one (#979).
//
// Measured on Incus 7.6 on 2026-11-16: a rule set holding allow-stateless, used
// by a bridge network, is refused whole by the daemon. The driver now refuses
// it itself, by name and with the remedy, before any command. The guard asks
// two questions — does the set hold a stateless rule, and does this process
// declare the capability — and each has a row that fails only that question:
// a stateful set on a bridge (written), and a stateless set on OVN (written).

func statelessGroup(name string) FirewallSpec {
	return FirewallSpec{
		Name:           name,
		DefaultIngress: "drop",
		DefaultEgress:  "allow",
		Rules: []FirewallRule{
			{Direction: "ingress", Action: "allow-stateless", Protocol: "tcp", Source: "0.0.0.0/0", PortFrom: 80, PortTo: 80},
			{Direction: "ingress", Action: "drop", Protocol: "udp", Source: "0.0.0.0/0", PortFrom: 53, PortTo: 53},
		},
	}
}

// TestABridgeModeRefusesAStatelessRuleSetByName is the refusing half, for both
// bridge modes: the error names the mode, the reason and the remedy, nothing
// reaches the runtime, and the firewall capability is left alone because the
// host refused nothing.
func TestABridgeModeRefusesAStatelessRuleSetByName(t *testing.T) {
	for _, vm := range []bool{false, true} {
		f := &fakeRuntime{}
		d := newFakeDriver(f)
		d.VM = vm
		name := "scw-stateless-" + d.Name()

		err := d.EnsureFirewall(context.Background(), statelessGroup(name))
		if err == nil {
			t.Fatalf("--vm %s wrote a stateless rule set it cannot enforce", d.Name())
		}
		if !errors.Is(err, ErrStatelessUnenforceable) {
			t.Errorf("--vm %s: the refusal is not the typed declared limit: %v", d.Name(), err)
		}
		for _, want := range []string{
			name,
			"--vm " + d.Name() + " cannot enforce",
			"Incus applies allow-stateless only on OVN networks",
			"stays attached and enforced as it was",
			"measured on Incus 7.6, 2026-11-16",
			"run with --vm incus-ovn",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("--vm %s: the refusal does not say %q:\n  %v", d.Name(), want, err)
			}
		}
		if len(f.calls) != 0 {
			t.Errorf("--vm %s: the refusal must come before any command, got %q", d.Name(), f.commands())
		}
		if !d.Capabilities().Firewall {
			t.Errorf("--vm %s: a refusal by the driver itself withdrew the host's firewall capability", d.Name())
		}
	}
}

// TestAnOVNModeWritesAStatelessRuleSet isolates the capability question: the
// same set, on the mode that declares the capability, is written with its
// stateless action intact.
func TestAnOVNModeWritesAStatelessRuleSet(t *testing.T) {
	f := &fakeRuntime{}
	d := newFakeDriver(f)
	d.OVN = true
	if err := d.EnsureFirewall(context.Background(), statelessGroup("scw-stateless-ovn")); err != nil {
		t.Fatalf("OVN refused a stateless rule set it enforces natively: %v", err)
	}
	puts := f.matching("-X PUT")
	if len(puts) != 1 || !strings.Contains(puts[0], `"action":"allow-stateless"`) {
		t.Errorf("OVN must write the set with its stateless action, got %q", f.commands())
	}
}

// TestABridgeModeStillWritesAStatefulRuleSet isolates the other question: a
// bridge lacks the capability, and a set with no stateless rule is written all
// the same. A guard that refused every set on a bridge would pass the refusing
// test above and remove the firewall from two modes of three.
func TestABridgeModeStillWritesAStatefulRuleSet(t *testing.T) {
	f := &fakeRuntime{}
	d := newFakeDriver(f)
	if err := d.EnsureFirewall(context.Background(), witnessGroup("scw-stateful", "")); err != nil {
		t.Fatalf("a bridge refused a stateful rule set: %v", err)
	}
	if len(f.matching("-X PUT")) != 1 {
		t.Errorf("a bridge must write a stateful rule set, got %q", f.commands())
	}
}

// TestTheStatelessCapabilityIsDeclaredAndPublished: true under OVN, false on
// the bridge modes, gone with the firewall when the host refuses a set, and
// carried by the JSON /_feint/health serves.
func TestTheStatelessCapabilityIsDeclaredAndPublished(t *testing.T) {
	bridge, ovn := NewIncus(), NewIncusOVN()
	if bridge.Capabilities().StatelessRules {
		t.Error("the bridge mode claims stateless rules, which Incus 7.6 refuses on a bridge network")
	}
	if !ovn.Capabilities().StatelessRules {
		t.Error("the OVN mode does not claim stateless rules, which it enforces natively")
	}
	ovn.firewallDenied.Store(true)
	if ovn.Capabilities().StatelessRules {
		t.Error("the host refused a rule set and the process still claims stateless rules")
	}
	if CapabilitiesOf(Noop{}).StatelessRules {
		t.Error("the no-op driver claims stateless rules")
	}
	payload, err := json.Marshal(NewIncusOVN().Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"stateless_rules":true`) {
		t.Errorf("the published capabilities do not carry stateless_rules: %s", payload)
	}
}

// TestTheStatelessRefusalReadsThePublishedCapability: an OVN mode whose host
// Verify narrowed is refused like a bridge, because the guard asks the
// capability rather than the mode's name.
func TestTheStatelessRefusalReadsThePublishedCapability(t *testing.T) {
	f := &fakeRuntime{}
	d := newFakeDriver(f)
	d.OVN = true
	narrowed := d.declaredCapabilities()
	narrowed.StatelessRules = false
	d.verified = &narrowed
	if err := d.EnsureFirewall(context.Background(), statelessGroup("scw-narrowed")); !errors.Is(err, ErrStatelessUnenforceable) {
		t.Errorf("a host that lost the capability still wrote a stateless set: %v, %q", err, f.commands())
	}
}

// TestVerifyNarrowsStatelessRulesWithOVN: no northbound, no OVN network, no
// stateless rule — and the narrowing is said.
func TestVerifyNarrowsStatelessRulesWithOVN(t *testing.T) {
	caps, unmet := mustVerifyBoth(t, hostSayingWithSocket("", "7.6", false))
	if caps.StatelessRules {
		t.Error("stateless rules survived a host with no northbound connection")
	}
	if !strings.Contains(strings.Join(unmet, "\n"), "stateless rules:") {
		t.Errorf("the narrowing never mentions stateless rules: %v", unmet)
	}
	if caps, unmet := mustVerifyBoth(t, hostSaying("tcp:10.0.0.1:6641", "7.6")); !caps.StatelessRules {
		t.Errorf("a wired host lost stateless rules: %v", unmet)
	}
}

// TestAStatelessRefusalIsReportedAsTheDeclaredLimit: the log line is a warning
// naming the capability, not one more operational ERROR.
func TestAStatelessRefusalIsReportedAsTheDeclaredLimit(t *testing.T) {
	var log bytes.Buffer
	b := Binding{Provider: "scaleway", Log: slog.New(slog.NewTextHandler(&log, nil))}
	if b.SyncRuleSet(context.Background(), newFakeDriver(&fakeRuntime{}), statelessGroup("scw-logged")) {
		t.Fatal("a refused rule set was reported as applicable")
	}
	got := log.String()
	if !strings.Contains(got, "level=WARN") || strings.Contains(got, "level=ERROR") {
		t.Errorf("a declared limit must be a warning, got %q", got)
	}
	for _, want := range []string{"capabilities.stateless_rules=false", "run with --vm incus-ovn"} {
		if !strings.Contains(got, want) {
			t.Errorf("the log does not say %q: %q", want, got)
		}
	}
}
