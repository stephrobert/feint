package machine

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The exit half of #521, one measurement later.
//
// On 2026-08-28 the incus-ovn leg of runtime-proof.yml failed at the doorstep
// of its own witness gate, on a runner nothing had touched, naming four
// objects the leg itself had left: `feint-uplink`, `fnt-default` and the rule
// sets of two providers' default security groups. The station reproduced it
// exactly. None of the four is removable by any client call, which is the
// property that makes releasing them honest rather than tidy — and the
// refusing halves matter as much, because an exit that deleted a rule set a
// machine still wears would be the audit's own firewall-disarming primitive
// with a friendlier name.

// releaseHost is the shape of a host holding one of everything this release
// gives back. Each answer is keyed on a fragment that matches exactly one
// command, because fakeRuntime scans its map in random order.
func releaseHost(aclUsedBy, defaultUsedBy string, defaultConfig string) map[string]string {
	self := strconv.Itoa(os.Getpid())
	if defaultConfig == "" {
		defaultConfig = `"user.` + LabelKey + `":"feint"`
	}
	return map[string]string{
		"network-acls?recursion=1": `[
		  {"name":"acltest","description":"","used_by":[]},
		  {"name":"scw-aaa","description":"feint security group","used_by":[` + aclUsedBy + `]},
		  {"name":"exo-bbb","description":"feint security group","used_by":[]}
		]`,
		"network-acls/scw-aaa":    `{"description":"feint security group"}`,
		"network-acls/exo-bbb":    `{"description":"feint security group"}`,
		"networks/fnt-default":    `{"type":"ovn","config":{` + defaultConfig + `},"used_by":[` + defaultUsedBy + `]}`,
		"networks/feint-uplink":   ourUplinkJSON(self, ""),
		"network-acls/probe-none": `{"description":""}`,
	}
}

// TestAGracefulExitReleasesEveryPieceOfPlumbingItHolds is the accepting half,
// and it is the one that fails on the code as it stood on 2026-08-28: the exit
// released the uplink alone, and the uplink is precisely the object that could
// not go while `fnt-default` still drew from it.
func TestAGracefulExitReleasesEveryPieceOfPlumbingItHolds(t *testing.T) {
	f := &fakeRuntime{answers: releaseHost("", "", "")}
	d := newFakeDriver(f)
	d.OVN = true

	released, err := d.ReleasePlumbing(context.Background())
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	for _, want := range []string{"scw-aaa", "exo-bbb", DefaultMachineNetwork, "feint-uplink"} {
		if !containsName(released, want) {
			t.Errorf("the exit did not give back %s; the next run's doorstep refuses exactly that\nreleased: %v\ncommands:\n%s",
				want, released, strings.Join(f.commands(), "\n"))
		}
	}
}

// TestTheReleaseOrderIsRuleSetsThenNetworkThenUplink holds the order the
// runtime imposes. A rule set keeps its network alive and a network keeps the
// uplink alive, so released the other way round every step answers "in use"
// and the host keeps all three — which is the state the leg was measured in.
func TestTheReleaseOrderIsRuleSetsThenNetworkThenUplink(t *testing.T) {
	f := &fakeRuntime{answers: releaseHost("", "", "")}
	d := newFakeDriver(f)
	d.OVN = true

	if _, err := d.ReleasePlumbing(context.Background()); err != nil {
		t.Fatalf("release: %v", err)
	}
	acl := indexOf(f.commands(), "network acl delete exo-bbb")
	network := indexOf(f.commands(), "network delete "+DefaultMachineNetwork)
	uplink := indexOf(f.commands(), "network delete feint-uplink")
	if acl < 0 || network < 0 || uplink < 0 {
		t.Fatalf("one of the three deletes never happened:\n%s", strings.Join(f.commands(), "\n"))
	}
	if acl >= network || network >= uplink {
		t.Fatalf("the release order is rule sets, then the default network, then the uplink; got acl=%d network=%d uplink=%d:\n%s",
			acl, network, uplink, strings.Join(f.commands(), "\n"))
	}
}

// TestAReleaseKeepsTheRuleSetOfAMachineLeftRunning is the refusal that keeps
// this from being a firewall teardown. `feint stop` without --cleanup leaves
// the run's machines up on purpose; a machine still wearing a rule set makes
// the runtime report it in use, and that rule set must not see a delete at all.
func TestAReleaseKeepsTheRuleSetOfAMachineLeftRunning(t *testing.T) {
	f := &fakeRuntime{answers: releaseHost(`"/1.0/instances/feint-scw-x"`, "", "")}
	d := newFakeDriver(f)
	d.OVN = true

	released, err := d.ReleasePlumbing(context.Background())
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if containsName(released, "scw-aaa") {
		t.Fatal("the release claims a rule set a running machine still wears")
	}
	if deletes := f.matching("network acl delete scw-aaa"); len(deletes) != 0 {
		t.Fatalf("a delete was issued against the rule set of a machine left running:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestAReleaseNeverTouchesARuleSetTheEmulatorDidNotWrite: an operator's rule
// set carries neither the description this driver writes nor a name it
// derives, and an unused one is exactly the shape a sweep is tempted by.
func TestAReleaseNeverTouchesARuleSetTheEmulatorDidNotWrite(t *testing.T) {
	f := &fakeRuntime{answers: releaseHost("", "", "")}
	d := newFakeDriver(f)
	d.OVN = true

	released, err := d.ReleasePlumbing(context.Background())
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if containsName(released, "acltest") {
		t.Fatal("the release claims a rule set the operator created")
	}
	if deletes := f.matching("acltest"); len(deletes) != 0 {
		t.Fatalf("a command was issued against an operator's rule set:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestAReleaseLeavesTheDefaultNetworkAMachineStillSitsOn: two emulators share
// one host and one default network, and `used_by` is what a holder pid would
// have said. The one exiting first must not take it from under the other's
// machines.
func TestAReleaseLeavesTheDefaultNetworkAMachineStillSitsOn(t *testing.T) {
	f := &fakeRuntime{answers: releaseHost("", `"/1.0/instances/feint-osc-y"`, "")}
	d := newFakeDriver(f)
	d.OVN = true

	released, err := d.ReleasePlumbing(context.Background())
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if containsName(released, DefaultMachineNetwork) {
		t.Fatal("the release claims a default network a machine still sits on")
	}
	if deletes := f.matching("network delete " + DefaultMachineNetwork); len(deletes) != 0 {
		t.Fatalf("a delete was issued against a default network still in use:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestAReleaseNeverTouchesAnUnlabelledNetworkUnderTheDefaultName: the name is
// short and ordinary enough for an operator to have typed it, and
// ensureDefaultNetwork already refuses to reuse one. This is that refusal read
// backwards, on the destructive side where it costs more.
func TestAReleaseNeverTouchesAnUnlabelledNetworkUnderTheDefaultName(t *testing.T) {
	f := &fakeRuntime{answers: releaseHost("", "", `"ipv4.address":"10.0.0.1/24"`)}
	d := newFakeDriver(f)
	d.OVN = true

	released, err := d.ReleasePlumbing(context.Background())
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if containsName(released, DefaultMachineNetwork) {
		t.Fatal("the release claims a network the emulator never labelled")
	}
	if deletes := f.matching("network delete " + DefaultMachineNetwork); len(deletes) != 0 {
		t.Fatalf("a delete was issued against an operator's own network:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestAReleaseThatCouldNotLookSaysSoAndKeepsGoing: a listing that failed is not
// an empty host, and it must not stop the network and the uplink behind it from
// going — one stuck rule set keeping the uplink standing is the shape this
// whole file exists to end.
func TestAReleaseThatCouldNotLookSaysSoAndKeepsGoing(t *testing.T) {
	f := &fakeRuntime{
		answers: releaseHost("", "", ""),
		fail: map[string]error{
			"network-acls?recursion=1": errors.New("connection refused"),
		},
	}
	d := newFakeDriver(f)
	d.OVN = true

	released, err := d.ReleasePlumbing(context.Background())
	if err == nil {
		t.Fatal("a rule-set listing that failed was reported as a host with nothing to release")
	}
	if !containsName(released, DefaultMachineNetwork) || !containsName(released, "feint-uplink") {
		t.Fatalf("a failed rule-set listing stopped the rest of the release: %v", released)
	}
}

// TestAReleaseAsksNothingOfARuntimeWithNoPlumbing: everything absent is the
// state this release exists to reach, and reaching it must be silent rather
// than an error — `serve --cleanup` already pruned the lot.
func TestAReleaseAsksNothingOfARuntimeWithNoPlumbing(t *testing.T) {
	f := &fakeRuntime{
		answers: map[string]string{"network-acls?recursion=1": `[]`},
		fail: map[string]error{
			"networks/fnt-default":  errors.New("Error: Network not found"),
			"networks/feint-uplink": errors.New("Error: Network not found"),
		},
	}
	d := newFakeDriver(f)
	d.OVN = true

	released, err := d.ReleasePlumbing(context.Background())
	if err != nil {
		t.Fatalf("an empty host is the outcome asked for, not an error: %v", err)
	}
	if len(released) != 0 {
		t.Fatalf("the release claims objects that were never there: %v", released)
	}
	if deletes := f.matching("delete"); len(deletes) != 0 {
		t.Fatalf("a delete was issued on a host holding nothing:\n%s", strings.Join(f.commands(), "\n"))
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

func indexOf(commands []string, want string) int {
	for i, cmd := range commands {
		if cmd == want {
			return i
		}
	}
	return -1
}

// TestAReleaseGivesBackTheDefaultNetworkUnderABridgeToo: the default machine
// network exists in both modes — ensureDefaultNetwork creates a managed bridge
// when the driver is not OVN — so the bridged leg leaks it exactly as the OVN
// one did. Only the uplink is OVN's alone.
func TestAReleaseGivesBackTheDefaultNetworkUnderABridgeToo(t *testing.T) {
	f := &fakeRuntime{answers: releaseHost("", "", "")}
	d := newFakeDriver(f)

	released, err := d.ReleasePlumbing(context.Background())
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !containsName(released, DefaultMachineNetwork) {
		t.Errorf("the bridged exit kept the default network: %v", released)
	}
	if containsName(released, "feint-uplink") {
		t.Error("a bridged runtime has no uplink, and the release claims one")
	}
	if asked := f.matching("networks/feint-uplink"); len(asked) != 0 {
		t.Errorf("bridge mode asked the runtime about an uplink it does not have:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}
