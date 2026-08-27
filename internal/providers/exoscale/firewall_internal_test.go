package exoscale

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The witnesses of #475 for this pack: three machines of the register's best
// stack ran with zero ACLs while the API described two groups and four rules.
// Every assertion here is on what reaches the runtime, through a recording
// Firewaller — the green apply is exactly what hid the defect.

// firewallDriver is recordingDriver plus the Firewaller half.
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

func storedSecurityGroup(p *Pack, name string, rules []any) *resource.Resource {
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindSecurityGroup, resource.Tenant{Provider: Name}, "present", now)
	res.Attrs = map[string]any{"name": name, "description": name}
	if len(rules) > 0 {
		res.Attrs["rules"] = rules
	}
	p.env.Store.Put(res)
	return res
}

func storedInstance(p *Pack, publicIP string, groupIDs ...any) *resource.Resource {
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindInstance, resource.Tenant{Provider: Name}, stoppedState, now)
	res.Attrs = map[string]any{
		"name":                 "witness",
		"template":             map[string]any{"id": "11111111-1111-4111-8111-111111111111"},
		"public-ip-assignment": "inet4",
	}
	if publicIP != "" {
		res.Attrs["public-ip"] = publicIP
	}
	if len(groupIDs) > 0 {
		res.Attrs[attrSecurityGroupIDs] = groupIDs
	}
	p.env.Store.Put(res)
	return res
}

// TestAnExoscaleGroupReachesTheHostWhenAnInstanceBoots is the wiring itself:
// the group an instance wears becomes a rule set the runtime holds, attached
// to the instance's machine, with upstream's own defaults — incoming
// forbidden until a rule allows it.
func TestAnExoscaleGroupReachesTheHostWhenAnInstanceBoots(t *testing.T) {
	driver := newFirewallDriver()
	p := sequencedPack(machine.Use(driver))
	group := storedSecurityGroup(p, "platform-web", []any{map[string]any{
		"id": "r1", "flow-direction": "ingress", "protocol": "tcp",
		"network": "0.0.0.0/0", "start-port": 443, "end-port": 443,
	}})
	inst := storedInstance(p, "192.0.2.7", group.ID)

	p.start(context.Background(), inst)

	setName := machine.FirewallName("exo", group.ID)
	spec, held := driver.ensured[setName]
	if !held {
		t.Fatalf("the runtime holds no rule set %s: the group stayed documentation, which is #475", setName)
	}
	if spec.DefaultIngress != "drop" {
		t.Fatalf("ingress default %q, want drop: upstream forbids all incoming traffic by default", spec.DefaultIngress)
	}
	binding, attached := driver.applied[inst.Runtime[p.binding().RuntimeKey]]
	if !attached || len(binding.Names) != 1 || binding.Names[0] != setName {
		t.Fatalf("the machine's interfaces carry %+v, want [%s]", binding.Names, setName)
	}
}

// TestAnEgressRuleFlipsTheEgressDefault holds upstream's sentence: all
// outgoing traffic is allowed — written into the set as a catch-all, since an
// OVN NIC's own default is deny — until the group defines one outbound rule,
// after which only the defined outbound rules pass.
func TestAnEgressRuleFlipsTheEgressDefault(t *testing.T) {
	p := sequencedPack(machine.Use(&recordingDriver{}))
	open := storedSecurityGroup(p, "no-egress", []any{map[string]any{
		"id": "r1", "flow-direction": "ingress", "protocol": "tcp",
		"network": "0.0.0.0/0", "start-port": 22, "end-port": 22,
	}})

	spec := p.firewallSpecOf(open, nil)
	if spec.DefaultEgress != "allow" {
		t.Fatalf("egress default %q, want allow while no egress rule exists", spec.DefaultEgress)
	}
	catchAll := false
	for _, rule := range spec.Rules {
		if rule.Direction == "egress" && rule.Action == "allow" && rule.Destination == "" {
			catchAll = true
		}
	}
	if !catchAll {
		t.Fatal("the permissive egress must be written into the set as a catch-all, or an OVN NIC's own default closes it")
	}

	restricted := storedSecurityGroup(p, "one-egress", []any{map[string]any{
		"id": "r2", "flow-direction": "egress", "protocol": "tcp",
		"network": "10.0.0.0/8", "start-port": 443, "end-port": 443,
	}})
	spec = p.firewallSpecOf(restricted, nil)
	if spec.DefaultEgress != "drop" {
		t.Fatalf("egress default %q, want drop: one outbound rule restricts outbound to the defined rules", spec.DefaultEgress)
	}
	for _, rule := range spec.Rules {
		if rule.Direction == "egress" && rule.Destination == "" {
			t.Fatalf("a restricted group must carry no egress catch-all, got %+v", rule)
		}
	}
}

// TestAGroupSourcedRuleExpandsToTheMembersAddresses holds the sentence
// examples/stacks/exoscale writes: the application tier accepts the web tier
// and nobody else. The runtime has no group selector, so the reference must
// arrive expanded into the member machines' addresses — and the group's
// external sources, which upstream defines as extending that membership.
func TestAGroupSourcedRuleExpandsToTheMembersAddresses(t *testing.T) {
	p := sequencedPack(machine.Use(&recordingDriver{}))
	web := storedSecurityGroup(p, "web", nil)
	_ = p.env.Store.Update(Name, kindSecurityGroup, web.ID, func(stored *resource.Resource) error {
		stored.Attrs["external-sources"] = []any{"203.0.113.0/24"}
		return nil
	})
	storedInstance(p, "192.0.2.7", web.ID)
	app := storedSecurityGroup(p, "app", []any{map[string]any{
		"id": "r1", "flow-direction": "ingress", "protocol": "tcp",
		"security-group": map[string]any{"id": web.ID},
		"start-port":     8080, "end-port": 8080,
	}})

	spec := p.firewallSpecOf(app, nil)
	sources := map[string]bool{}
	for _, rule := range spec.Rules {
		if rule.PortFrom == 8080 {
			sources[rule.Source] = true
		}
	}
	if !sources["192.0.2.7/32"] {
		t.Fatalf("the member machine's address never reached the expansion, got %v", sources)
	}
	if !sources["203.0.113.0/24"] {
		t.Fatalf("the group's external sources must extend the membership, got %v", sources)
	}
}

// TestAPoolMemberWearsItsPoolsGroups is the half of #475 that lands on the
// stacks: the app tier's whole firewall rides the pool's security-groups, so a
// member that does not inherit them is a machine the group never reaches.
func TestAPoolMemberWearsItsPoolsGroups(t *testing.T) {
	driver := newFirewallDriver()
	p := sequencedPack(machine.Use(driver))
	group := storedSecurityGroup(p, "platform-app", []any{map[string]any{
		"id": "r1", "flow-direction": "ingress", "protocol": "tcp",
		"network": "0.0.0.0/0", "start-port": 8080, "end-port": 8080,
	}})

	body := `{"name":"app","size":1,` +
		`"template":{"id":"11111111-1111-4111-8111-111111111111"},` +
		`"instance-type":{"id":"71004023-bb72-4a97-b1e9-bc66dfce9470"},` +
		`"security-groups":[{"id":"` + group.ID + `"}]}`
	r := httptest.NewRequest("POST", "/v2/instance-pool", strings.NewReader(body))
	p.createInstancePool(httptest.NewRecorder(), r)

	members := p.poolMembersOfOnlyPool(t)
	member := members[0]
	worn := stringList(member.Attrs[attrSecurityGroupIDs])
	if len(worn) != 1 || worn[0] != group.ID {
		t.Fatalf("the member wears %v, want [%s]", worn, group.ID)
	}
	setName := machine.FirewallName("exo", group.ID)
	binding, attached := driver.applied[member.Runtime["machine"]]
	if !attached || !containsName(binding.Names, setName) {
		t.Fatalf("the member's machine carries %+v, want %s attached", binding.Names, setName)
	}
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// poolMembersOfOnlyPool reads the single pool's members back from the store.
func (p *Pack) poolMembersOfOnlyPool(t *testing.T) []*resource.Resource {
	t.Helper()
	pools := p.env.Store.List(kindPool, resource.Tenant{Provider: Name})
	if len(pools) != 1 {
		t.Fatalf("%d pools, want 1", len(pools))
	}
	members := p.poolMembers(pools[0].ID)
	if len(members) == 0 {
		t.Fatal("the pool has no members")
	}
	return members
}

// TestALateNetworkAttachCarriesTheRuleSets covers the interface born after the
// boot: the Terraform provider attaches private networks once the create has
// answered, and a rule set applied at boot never reaches a NIC that does not
// exist yet. The attach path must re-apply, so the machine's covered
// interfaces hold what the API describes.
//
// "Since ApplyFirewall covers every NIC of the machine" was the second half of
// this comment until 2026-08-27, and it was the defect: the interface this
// attach creates is precisely the one the groups must *not* reach (#574).
// TestALateNetworkAttachLeavesThePrivateInterfaceUnfiltered holds that half,
// and this one keeps the resync itself from being dropped along with it.
func TestALateNetworkAttachCarriesTheRuleSets(t *testing.T) {
	driver := newFirewallDriver()
	p := sequencedPack(machine.Use(driver))
	group := storedSecurityGroup(p, "web", []any{map[string]any{
		"id": "r1", "flow-direction": "ingress", "protocol": "tcp",
		"network": "0.0.0.0/0", "start-port": 443, "end-port": 443,
	}})
	inst := storedInstance(p, "192.0.2.7", group.ID)
	p.start(context.Background(), inst)
	p.env.Store.Put(inst)
	machineName := inst.Runtime["machine"]

	r := httptest.NewRequest("POST", "/v2/private-network",
		strings.NewReader(`{"name":"back","start-ip":"10.90.2.20","end-ip":"10.90.2.200","netmask":"255.255.255.0"}`))
	p.createPrivateNetwork(httptest.NewRecorder(), r)
	pns := p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name})
	if len(pns) != 1 {
		t.Fatalf("%d private networks, want 1", len(pns))
	}

	// The boot's own application is not what this test measures.
	driver.mu.Lock()
	driver.applied = map[string]machine.FirewallBinding{}
	driver.mu.Unlock()

	ar := httptest.NewRequest("POST", "/v2/private-network/"+pns[0].ID+":attach",
		strings.NewReader(`{"instance":{"id":"`+inst.ID+`"}}`))
	ar.SetPathValue("id", pns[0].ID)
	p.attachInstanceToPrivateNetwork(httptest.NewRecorder(), ar)

	binding, applied := driver.applied[machineName]
	if !applied || len(binding.Names) == 0 {
		t.Fatalf("the attach left the new interface without the instance's rule sets (applied=%v %+v)", applied, binding)
	}
}
