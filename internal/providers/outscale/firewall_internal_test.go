package outscale

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// The witnesses of #475 for this pack. What lied was not a rule's shape but
// its absence: ten acknowledged security-group calls, six OVN networks, five
// running containers, zero rule sets on the host. So every test here asserts
// on what was handed to the runtime, through a recording Firewaller — a green
// HTTP answer proves nothing about the host.

// firewallDriver is recordingDriver plus the Firewaller half, recording what
// reaches the runtime.
type firewallDriver struct {
	recordingDriver

	mu      sync.Mutex
	ensured map[string]machine.FirewallSpec
	applied map[string]machine.FirewallBinding
}

func newFirewallDriver() *firewallDriver {
	return &firewallDriver{
		ensured: map[string]machine.FirewallSpec{},
		applied: map[string]machine.FirewallBinding{},
	}
}

func (d *firewallDriver) EnsureFirewall(_ context.Context, spec machine.FirewallSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensured[spec.Name] = spec
	return nil
}

func (d *firewallDriver) ApplyFirewall(_ context.Context, machineName string, binding machine.FirewallBinding) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.applied[machineName] = binding
	return nil
}

func (d *firewallDriver) RemoveFirewall(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.ensured, name)
	return nil
}

// firewallPack is runtimePack with unique identifiers, which several stored
// groups need.
func firewallPack(driver machine.Driver) *Pack {
	n := 0
	return New(&emulator.Env{
		Store:    store.New(),
		Machines: driver,
		Now:      func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			n++
			return fmt.Sprintf("%08d", n)
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func storedGroup(p *Pack, name string, inbound []any) *resource.Resource {
	now := p.env.Now()
	res := resource.New(newID("sg", p.env.NewID()), kindSecurityGroup, resource.Tenant{Provider: Name}, "available", now)
	res.Attrs = map[string]any{
		"SecurityGroupName": name,
		"Description":       name,
		"InboundRules":      inbound,
		"OutboundRules": []any{map[string]any{
			"FromPortRange": -1, "ToPortRange": -1, "IpProtocol": "-1",
			"IpRanges": []any{"0.0.0.0/0"},
		}},
		"Tags": []any{},
	}
	p.env.Store.Put(res)
	return res
}

func storedVM(p *Pack, groupIDs ...string) *resource.Resource {
	now := p.env.Now()
	res := resource.New(newVMID(p.env.NewID()), kindVM, resource.Tenant{Provider: Name}, stateStopped, now)
	res.Attrs = map[string]any{
		"ImageId":          "ami-00000001",
		"VmType":           defaultVMType,
		"SecurityGroupIds": groupIDs,
	}
	p.env.Store.Put(res)
	return res
}

// TestAnOutscaleGroupReachesTheHostWhenItsVmBoots is the wiring itself: the
// group a Vm wears becomes a rule set the runtime holds, attached to the Vm's
// machine with the allow-list defaults — drop what no rule matches, in both
// directions, which is what the API describes and the host never received.
func TestAnOutscaleGroupReachesTheHostWhenItsVmBoots(t *testing.T) {
	driver := newFirewallDriver()
	p := firewallPack(driver)
	group := storedGroup(p, "witness-osc", []any{map[string]any{
		"FromPortRange": 22, "ToPortRange": 22, "IpProtocol": "tcp",
		"IpRanges": []any{"0.0.0.0/0"},
	}})
	vm := storedVM(p, group.ID)

	p.powerOn(context.Background(), vm)

	setName := machine.FirewallName("osc", group.ID)
	spec, held := driver.ensured[setName]
	if !held {
		t.Fatalf("the runtime holds no rule set %s: the group stayed documentation, which is #475", setName)
	}
	if spec.DefaultIngress != "drop" || spec.DefaultEgress != "drop" {
		t.Fatalf("defaults %s/%s, want drop/drop: a security group is an allow-list both ways",
			spec.DefaultIngress, spec.DefaultEgress)
	}
	binding, attached := driver.applied[vm.Runtime[p.binding().RuntimeKey]]
	if !attached || len(binding.Names) != 1 || binding.Names[0] != setName {
		t.Fatalf("the machine's interfaces carry %+v, want [%s]", binding.Names, setName)
	}
}

// TestAMemberSourcedRuleExpandsToTheMembersAddresses holds the tiering rule
// the stacks write — the data tier accepts the web tier and nobody else. The
// runtime has no group selector, so the member reference must arrive expanded
// into the addresses the member machines answer on, and re-expanded when a
// member boots.
func TestAMemberSourcedRuleExpandsToTheMembersAddresses(t *testing.T) {
	driver := newFirewallDriver()
	p := firewallPack(driver)
	web := storedGroup(p, "web", nil)
	data := storedGroup(p, "data", []any{map[string]any{
		"FromPortRange": 5432, "ToPortRange": 5432, "IpProtocol": "tcp",
		"SecurityGroupsMembers": []any{map[string]any{"SecurityGroupId": web.ID}},
	}})
	dataVM := storedVM(p, data.ID)
	p.powerOn(context.Background(), dataVM)

	// Before any web machine runs, the expansion is honestly empty.
	spec := driver.ensured[machine.FirewallName("osc", data.ID)]
	for _, rule := range spec.Rules {
		if rule.PortFrom == 5432 {
			t.Fatalf("the member rule expanded to %q before any member had an address", rule.Source)
		}
	}

	// A web machine boots: the data group's set must now allow its address.
	webVM := storedVM(p, web.ID)
	p.powerOn(context.Background(), webVM)

	spec = driver.ensured[machine.FirewallName("osc", data.ID)]
	found := false
	for _, rule := range spec.Rules {
		if rule.PortFrom == 5432 && rule.Source == "10.42.0.9/32" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the data tier's set never learned the web machine's address; rules: %+v", spec.Rules)
	}
}

// TestARevokedRuleLeavesTheRuleSet drives the real handler: deleting a rule
// replays the whole set, which is what makes a revoked port actually close —
// EnsureFirewall replaces, so the assertion is on the set the runtime now
// holds.
func TestARevokedRuleLeavesTheRuleSet(t *testing.T) {
	driver := newFirewallDriver()
	p := firewallPack(driver)
	group := storedGroup(p, "witness-osc", []any{map[string]any{
		"FromPortRange": 22, "ToPortRange": 22, "IpProtocol": "tcp",
		"IpRanges": []any{"0.0.0.0/0"}, "SecurityGroupRuleId": "sgr-1",
	}})
	vm := storedVM(p, group.ID)
	p.powerOn(context.Background(), vm)

	p.syncSecurityGroup(context.Background(), group)
	setName := machine.FirewallName("osc", group.ID)
	before := driver.ensured[setName]
	hasPort := func(spec machine.FirewallSpec, port int) bool {
		for _, rule := range spec.Rules {
			if rule.PortFrom == port {
				return true
			}
		}
		return false
	}
	if !hasPort(before, 22) {
		t.Fatalf("the set never held the rule it was meant to lose: %+v", before.Rules)
	}

	// The revocation, through the store the way the handler writes it.
	_ = p.env.Store.Update(Name, kindSecurityGroup, group.ID, func(stored *resource.Resource) error {
		stored.Attrs["InboundRules"] = []any{}
		return nil
	})
	after, _ := p.env.Store.Get(Name, kindSecurityGroup, group.ID)
	p.syncSecurityGroup(context.Background(), after)

	if hasPort(driver.ensured[setName], 22) {
		t.Fatalf("the revoked rule survived in the runtime's set: %+v", driver.ensured[setName].Rules)
	}
}
