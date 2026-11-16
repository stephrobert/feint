package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// A group written stateful, then turned stateless, on a bridge mode (#979).
// The decision is the conservative one ApplyMachine documents: the refused set
// is not written and nothing is removed, so the version written earlier stays
// attached and enforced. This test holds that behaviour and the log that says
// it: no detach, no `network unset`, no `acl delete`, and the warning names
// the old version as kept.
func TestAGroupTurnedStatelessKeepsItsOldSet(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/feint-bench-m1": twoNICs,
		"/1.0/networks/":                `{"type": "bridge"}`,
	}}
	d := newFakeDriver(f)
	var log bytes.Buffer
	b := newGroupSyncBench()
	sync := b.sync()
	sync.Binding.driver = d
	sync.Binding.Log = slog.New(slog.NewTextHandler(&log, nil))
	stateless := false
	spec := sync.SpecOf
	sync.SpecOf = func(group, fresh *testResource) FirewallSpec {
		s := spec(group, fresh)
		if stateless {
			s.Rules[0].Action = "allow-stateless"
		}
		return s
	}
	group := b.group("sg", "")
	b.machine("m1", "10.0.0.5", "sg")

	sync.SyncGroup(context.Background(), group, nil)
	written := false
	for _, call := range f.calls {
		written = written || strings.Contains(strings.Join(call, " "), "security.acls="+FirewallName("bench", "sg"))
	}
	if !written {
		t.Fatalf("the stateful set was not attached first; calls: %v", f.calls)
	}

	before := len(f.calls)
	stateless = true
	sync.SyncGroup(context.Background(), group, nil)
	for _, call := range f.calls[before:] {
		joined := strings.Join(call, " ")
		for _, forbidden := range []string{"acl delete", "unset", "security.acls", "device remove"} {
			if strings.Contains(joined, forbidden) {
				t.Errorf("the refused set reached the host as %q", joined)
			}
		}
	}
	// The warning's own message, not the error it carries: the driver's
	// error says it too, and a log that only forwards it would pass on the
	// error's words alone.
	msg, _, _ := strings.Cut(log.String(), " provider=")
	if !strings.Contains(msg, "a version written earlier under this name stays attached and enforced as it was") ||
		!strings.Contains(msg, "until the group is stateful again or the server wears no refused stateless group") {
		t.Errorf("the warning does not say the old version stays:\n%s", log.String())
	}
	if strings.Contains(log.String(), "enforced nowhere") {
		t.Errorf("the warning still claims the group is enforced nowhere:\n%s", log.String())
	}
}

// A host Verify found below the floor writes no rule set (#979): zero PUT,
// zero create, and the refusal names the floor's reason.
func TestABelowFloorHostWritesNoRuleSet(t *testing.T) {
	d := hostSaying("tcp:10.0.0.1:6641", "6.0.4")
	if caps, _ := d.Verify(context.Background()); caps.Firewall {
		t.Fatal("Verify kept the firewall on 6.0.4")
	}
	var writes []string
	runner := d.runner
	d.runner = func(ctx context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "acl create") || strings.Contains(joined, "acl edit") ||
			strings.Contains(joined, "--request PUT") || strings.Contains(joined, "-X PUT") {
			writes = append(writes, joined)
		}
		return runner(ctx, args...)
	}
	err := d.EnsureFirewall(context.Background(), FirewallSpec{Name: "scw-sg", DefaultIngress: "drop", DefaultEgress: "allow",
		Rules: []FirewallRule{{Direction: "ingress", Action: "allow", Protocol: "tcp", PortFrom: 22, PortTo: 22}}})
	if !errors.Is(err, ErrFirewallUnverified) {
		t.Fatalf("want the below-floor refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "lxc/incus#2222") || !strings.Contains(err.Error(), "stays attached") {
		t.Errorf("the refusal does not name the floor's reason and what stays: %v", err)
	}
	// Its only way out is the floor; the stateless exit does not apply here.
	if !strings.Contains(err.Error(), "nothing is rewritten until the host runs Incus 6.0.5") || strings.Contains(err.Error(), "stateful again") {
		t.Errorf("the below-floor refusal names the wrong way out: %v", err)
	}
	if len(writes) != 0 {
		t.Errorf("rule sets written below the floor: %v", writes)
	}
	// The accepting half: on the floor itself, the same set is written.
	ok := hostSaying("tcp:10.0.0.1:6641", "6.0.5")
	ok.Verify(context.Background())
	if err := ok.EnsureFirewall(context.Background(), FirewallSpec{Name: "scw-sg", DefaultIngress: "drop", DefaultEgress: "allow"}); errors.Is(err, ErrFirewallUnverified) {
		t.Errorf("6.0.5 was refused: %v", err)
	}
}

// A stateless set on an OVN host Verify narrowed names the host's reason, not
// the OVN mode the process already runs.
func TestAnOVNHostVerifyNarrowedNamesItsOwnReason(t *testing.T) {
	d := hostSayingWithSocket("", "7.6", false) // default northbound, socket absent
	if caps, _ := d.Verify(context.Background()); caps.StatelessRules {
		t.Fatal("Verify kept stateless rules with no northbound")
	}
	err := d.EnsureFirewall(context.Background(), statelessGroup("scw-stateless-ovn"))
	if !errors.Is(err, ErrStatelessUnenforceable) {
		t.Fatalf("want the stateless refusal, got %v", err)
	}
	if strings.Contains(err.Error(), "run with --vm incus-ovn") {
		t.Errorf("the refusal sends an incus-ovn host to incus-ovn: %v", err)
	}
	if !strings.Contains(err.Error(), "this host cannot enforce one under --vm incus-ovn") || !strings.Contains(err.Error(), "northbound") {
		t.Errorf("the refusal does not name the host's reason: %v", err)
	}
}

// Below the floor, the binding reports the refusal as the declared limit it
// is, at warning level, naming what stays on the host.
func TestABelowFloorRefusalIsReportedAsTheDeclaredLimit(t *testing.T) {
	d := hostSaying("tcp:10.0.0.1:6641", "6.0.4")
	d.Verify(context.Background())
	var log bytes.Buffer
	b := Binding{Provider: "bench", Log: slog.New(slog.NewTextHandler(&log, nil))}
	if b.SyncRuleSet(context.Background(), d, FirewallSpec{Name: "scw-sg", DefaultIngress: "drop", DefaultEgress: "allow"}) {
		t.Fatal("a set was reported written below the floor")
	}
	out := log.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "below what the runtime supports") || !strings.Contains(out, "stays attached") {
		t.Errorf("not the declared-limit warning:\n%s", out)
	}
}

// Below the floor, isolation is part of the withdrawn firewall (#979):
// Verify stops publishing it, and IsolateNetwork writes and attaches nothing.
// The accepting half: on the floor itself the same call writes and attaches.
func TestABelowFloorHostWritesNoIsolation(t *testing.T) {
	isolate := func(version string) (caps Capabilities, writes []string, err error) {
		d := hostSaying("tcp:10.0.0.1:6641", version)
		caps, _ = d.Verify(context.Background())
		runner := d.runner
		d.runner = func(ctx context.Context, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "/1.0/network-acls/") || strings.Contains(joined, "security.acls") ||
				strings.Contains(joined, "acl create") {
				writes = append(writes, joined)
			}
			return runner(ctx, args...)
		}
		err = d.IsolateNetwork(context.Background(), "fnt-aaa", []string{"10.2.0.0/24"})
		return caps, writes, err
	}

	caps, writes, err := isolate("6.0.4")
	if caps.Isolation {
		t.Error("Verify still publishes isolation below the floor")
	}
	if !errors.Is(err, ErrFirewallUnverified) || !strings.Contains(err.Error(), "lxc/incus#2222") {
		t.Errorf("want the below-floor refusal naming its reason, got %v", err)
	}
	if len(writes) != 0 {
		t.Errorf("isolation written below the floor: %v", writes)
	}

	caps, writes, err = isolate("6.0.5")
	if err != nil || !caps.Isolation {
		t.Fatalf("6.0.5: isolation %v, err %v", caps.Isolation, err)
	}
	if len(writes) < 2 {
		t.Errorf("6.0.5 did not write and attach the isolation set: %v", writes)
	}
}

// The reconciliation reports a below-floor refusal as the declared limit, at
// warning level, never as an operational error.
func TestABelowFloorIsolationIsReportedAsTheDeclaredLimit(t *testing.T) {
	var log bytes.Buffer
	report(slog.New(slog.NewTextHandler(&log, nil)), "vpc", IsolationMember{ID: "v1", Network: "fnt-aaa"},
		fmt.Errorf("isolation of fnt-aaa: %w", ErrFirewallUnverified), "isolate")
	if out := log.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "is not isolated") {
		t.Errorf("not the declared-limit warning:\n%s", out)
	}
}
