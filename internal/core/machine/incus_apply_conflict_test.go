package machine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// The apply-side sibling of #493's retry, measured on the Scaleway example
// stack under --vm incus-ovn: the two machines of one security group are
// applied concurrently, both device edits make the daemon ensure the group's
// rule set in OVN, both ask for its port group, and the loser dies on the
// OVSDB uniqueness constraint:
//
//	Failed creating port group "incus_acl2597_all" for referenced security
//	ACL "scw-…" setup: constraint violation: Transaction causes multiple
//	rows in "Port_Group" table to have identical values
//
// Nothing retried, so one NIC ended with no rule set at all — used_by one
// short of the group's members — while the API reported the group applied.
// The same run also measured the daemon's own optimistic-concurrency refusal
// ("ETag doesn't match") on two concurrent Outscale applies. Both conflicts
// are transient by construction: the port group exists on the second look,
// the re-read sees the new ETag. The edit rides them out.
func TestApplyFirewallRidesOutADuplicatePortGroupRace(t *testing.T) {
	const machineName = "feint-scw-conflict1"
	const network = "fnt-c0ffee00001"

	var mu sync.Mutex
	attempts := 0
	applied := ""
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		switch {
		case key == "config get "+machineName+" user."+LabelKey:
			return []byte("scaleway\n"), nil
		case key == "query /1.0/instances/"+machineName:
			devices := `{"eth0":{"type":"nic","network":"` + network + `"}}`
			return []byte(`{"name":"` + machineName + `","devices":` + devices +
				`,"expanded_devices":` + devices + `}`), nil
		case key == "query /1.0/networks/"+network:
			return []byte(`{"type":"ovn","config":{}}`), nil
		case strings.HasPrefix(key, "config device set "+machineName+" eth0 security.acls="):
			mu.Lock()
			defer mu.Unlock()
			attempts++
			switch attempts {
			case 1:
				return nil, &textError{`incus config: Error: Failed to update device "eth0": ` +
					`Failed creating port group "incus_acl2597_all" for referenced security ACL "scw-x" setup: ` +
					`constraint violation: Transaction causes multiple rows in "Port_Group" table ` +
					`to have identical values (incus_acl2597_all) for index on column "name".`}
			case 2:
				return nil, &textError{"incus config: Error: ETag doesn't match: aaaa vs bbbb"}
			}
			applied = strings.TrimPrefix(key, "config device set "+machineName+" eth0 ")
			return nil, nil
		}
		return nil, nil
	}

	d := NewIncusOVN()
	d.runner = runner
	d.busyPoll = time.Millisecond

	err := d.ApplyFirewall(context.Background(), machineName, FirewallBinding{Names: []string{"scw-x"}})
	if err != nil {
		t.Fatalf("the apply gave up on a transient conflict: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Fatalf("expected the edit to be asked again through both conflicts, got %d attempt(s)", attempts)
	}
	if !strings.Contains(applied, "security.acls=scw-x") {
		t.Fatalf("the rule set never landed on the NIC: %q", applied)
	}
}

// The predicate is the guard's edge: a wording outside the three measured
// transient shapes is not retried. "Cannot find security ACL ID" is the
// ordering defect the network lock closes — retrying it would paper over a
// deleted rule set — and it must come back as the error it is, first try.
func TestANonTransientFailureIsNotRetried(t *testing.T) {
	const machineName = "feint-scw-conflict2"
	const network = "fnt-c0ffee00002"

	var mu sync.Mutex
	attempts := 0
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		switch {
		case key == "config get "+machineName+" user."+LabelKey:
			return []byte("scaleway\n"), nil
		case key == "query /1.0/instances/"+machineName:
			devices := `{"eth0":{"type":"nic","network":"` + network + `"}}`
			return []byte(`{"name":"` + machineName + `","devices":` + devices +
				`,"expanded_devices":` + devices + `}`), nil
		case key == "query /1.0/networks/"+network:
			return []byte(`{"type":"ovn","config":{}}`), nil
		case strings.HasPrefix(key, "config device set "+machineName+" eth0 security.acls="):
			mu.Lock()
			defer mu.Unlock()
			attempts++
			return nil, &textError{`incus config: Error: Failed setting up OVN port: ` +
				`Cannot find security ACL ID for "iso-fnt-c0ffee00002"`}
		}
		return nil, nil
	}

	d := NewIncusOVN()
	d.runner = runner
	d.busyPoll = time.Millisecond

	err := d.ApplyFirewall(context.Background(), machineName, FirewallBinding{Names: []string{"scw-x"}})
	if err == nil {
		t.Fatal("a vanished rule set was reported applied")
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Fatalf("a non-transient failure was retried %d times; it should come back first try", attempts)
	}
}

// textError keeps the fake's wording exactly as the daemon produced it.
type textError struct{ s string }

func (e *textError) Error() string { return e.s }

// portGroupCollision is the wording measured in the wild on 2026-08-28
// (serve-after.log of the full outscale chain): EnsureFirewall's PUT losing
// the OVSDB port-group creation to a concurrent NIC edit — the same conflict
// TestApplyFirewallRidesOutADuplicatePortGroupRace records for the device
// side, on the neighbouring door.
const portGroupCollision = `incus query: Error: Failed ensuring ACL is configured in OVN: ` +
	`Failed creating port group "incus_acl4448_net4613" for security ACL "osc-x" and network "fnt-a" setup: ` +
	`constraint violation: Transaction causes multiple rows in "Port_Group" table ` +
	`to have identical values (incus_acl4448_net4613) for index on column "name".`

// The write-side sibling of the device edit's retry: replacing a rule set
// that is in use makes the daemon re-ensure it in OVN, and that ensure loses
// the same port-group collision. Observed twice in one run, nine seconds
// apart, both inserts colliding with a row that predated them both — so the
// daemon's own existence check can stay stale for seconds under load, no
// ordering of this process's calls could have prevented the second one, and
// asking again is the remedy, exactly as for the device edit.
func TestEnsureFirewallRidesOutADuplicatePortGroupRace(t *testing.T) {
	var mu sync.Mutex
	puts := 0
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		switch {
		case key == "network acl show osc-x":
			return []byte("name: osc-x\n"), nil
		case strings.HasPrefix(key, "query -X PUT "):
			mu.Lock()
			defer mu.Unlock()
			puts++
			if puts == 1 {
				return nil, &textError{portGroupCollision}
			}
			return nil, nil
		}
		return nil, nil
	}

	d := NewIncusOVN()
	d.runner = runner
	d.busyPoll = time.Millisecond

	if err := d.EnsureFirewall(context.Background(), FirewallSpec{
		Name:  "osc-x",
		Rules: []FirewallRule{{Direction: "ingress", Action: "allow", Protocol: "tcp", PortFrom: 22, PortTo: 22}},
	}); err != nil {
		t.Fatalf("the write gave up on a transient conflict: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 2 {
		t.Fatalf("expected the PUT to be asked again through the conflict, got %d attempt(s)", puts)
	}
	if !d.Capabilities().Firewall {
		t.Fatal("a conflict the retry rode out still withdrew the firewall capability")
	}
}

// Two EnsureFirewall of one absent name — the two machines of a group applied
// concurrently both ensuring the permissive set, or two rule writes of one
// group — both pass the existence check, and the daemon answers the loser
// "The network ACL already exists" (measured on this station, 2026-08-28).
// The object the loser wanted is there: the rules must still be written, and
// the capability must stand — before this, the loser's error flowed through
// firewallRefused and one lost create disarmed capabilities.firewall for the
// rest of the process.
func TestALostCreateRaceIsNotARefusal(t *testing.T) {
	var mu sync.Mutex
	var put string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		switch {
		case key == "network acl show opn-fnt":
			return nil, &textError{"incus network: Error: Network ACL not found"}
		case key == "network acl create opn-fnt":
			return nil, &textError{"incus network: Error: The network ACL already exists"}
		case strings.HasPrefix(key, "query -X PUT "):
			mu.Lock()
			put = key
			mu.Unlock()
			return nil, nil
		}
		return nil, nil
	}

	d := NewIncusOVN()
	d.runner = runner
	d.busyPoll = time.Millisecond

	if err := d.EnsureFirewall(context.Background(), FirewallSpec{Name: "opn-fnt"}); err != nil {
		t.Fatalf("a create the winner had already done was reported as a failure: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(put, "/1.0/network-acls/opn-fnt") {
		t.Fatalf("the rules were never written after the lost create: %q", put)
	}
	if !d.Capabilities().Firewall {
		t.Fatal("a lost create race withdrew the firewall capability")
	}
}

// A conflict that outlives the retry budget is an error — returned, logged by
// the caller — but it is not the host refusing the rule set, and it must not
// disarm the capability: one timing blip used to flip capabilities.firewall
// for the rest of the process, and every suite keyed on the capability then
// skipped its enforcement assertions — the instrument muted by the very noise
// it existed to see through. A real refusal still withdraws it
// (TestAFirewallWriteTheHostRefusesWithdrawsTheCapability).
func TestASpentConflictBudgetDoesNotWithdrawTheCapability(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		if strings.HasPrefix(key, "query -X PUT ") {
			return nil, &textError{portGroupCollision}
		}
		return nil, nil
	}

	d := NewIncusOVN()
	d.runner = runner
	d.busyPoll = time.Millisecond
	d.busyBudget = 5 * time.Millisecond

	err := d.EnsureFirewall(context.Background(), FirewallSpec{Name: "osc-x"})
	if err == nil {
		t.Fatal("a write that never landed was reported written")
	}
	if !d.Capabilities().Firewall {
		t.Fatal("a transient conflict outliving the budget withdrew the firewall capability")
	}
}
