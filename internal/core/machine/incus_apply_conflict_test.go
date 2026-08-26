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
