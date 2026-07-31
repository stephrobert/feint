package outscale_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The defects a whole-pack audit found on paths the conformance suite does not
// walk. Each test fails without its fix; each fix was falsified in a copy
// outside the repository.
//
// They live in one file on purpose: they are not a feature, they are the record
// of what the suite could not see, and a later reader deciding whether the suite
// is broad enough should be able to read them together.

// post is a raw call, without the contract check the other file's helper does:
// these tests assert on refusals and on state, and several of them expect a
// non-200.
func post(t *testing.T, ts *httptest.Server, action, body string) (int, map[string]any) {
	t.Helper()
	res, err := http.Post(ts.URL+"/api/v1/"+action, "application/json", strings.NewReader(body)) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	defer func() { _ = res.Body.Close() }()
	var decoded map[string]any
	_ = json.NewDecoder(res.Body).Decode(&decoded)
	return res.StatusCode, decoded
}

func netAndSubnet(t *testing.T, ts *httptest.Server, cidr, subnet string) (netID, subnetID string) {
	t.Helper()
	_, out := post(t, ts, "CreateNet", `{"IpRange":"`+cidr+`"}`)
	n, _ := out["Net"].(map[string]any)
	netID, _ = n["NetId"].(string)
	_, out = post(t, ts, "CreateSubnet", `{"NetId":"`+netID+`","IpRange":"`+subnet+`"}`)
	s, _ := out["Subnet"].(map[string]any)
	subnetID, _ = s["SubnetId"].(string)
	return netID, subnetID
}

// Two concurrent creates must not receive the same address.
//
// The pack carries a mutex whose comment names this exact case — "Terraform
// creates ten resources at a time by default" — and the Vm path never took it,
// while the Net and Subnet paths always did. An audit ran twelve concurrent
// creates and got one address twice; with a runtime that is two containers
// configured with the same static IP, which defeats the addressing claim this
// pack is built on.
func TestConcurrentCreatesDoNotShareAnAddress(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.42.0.0/16", "10.42.1.0/24")

	const creates = 12
	var wg sync.WaitGroup
	addresses := make([]string, creates)
	for i := range creates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, out := post(t, ts, "CreateVms",
				`{"ImageId":"ami-12345678","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
			vms, _ := out["Vms"].([]any)
			if len(vms) == 0 {
				return
			}
			vm, _ := vms[0].(map[string]any)
			addresses[i], _ = vm["PrivateIp"].(string)
		}()
	}
	wg.Wait()

	seen := map[string]int{}
	for _, a := range addresses {
		if a != "" {
			seen[a]++
		}
	}
	for address, n := range seen {
		if n > 1 {
			t.Errorf("%s was handed to %d machines", address, n)
		}
	}
	if len(seen) < creates {
		t.Errorf("only %d of %d creates produced an address", len(seen), creates)
	}
}

// A Subnet must not delete under the machines placed in it.
//
// deleteNet has always refused while subnets remained; deleteSubnet checked
// nothing, so an audit deleted a subnet under a running Vm and then its Net,
// while ReadVms went on naming both. With a runtime it tore down the backing
// network under attached machines.
func TestASubnetDoesNotDeleteUnderAVm(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.43.0.0/16", "10.43.1.0/24")

	post(t, ts, "CreateVms", `{"ImageId":"ami-12345678","SubnetId":"`+subnetID+`","BootOnCreation":false}`)

	status, out := post(t, ts, "DeleteSubnet", `{"SubnetId":"`+subnetID+`"}`)
	if status == http.StatusOK {
		t.Fatalf("the subnet deleted under a live Vm: %v", out)
	}

	// And it is still there, rather than half deleted.
	_, out = post(t, ts, "ReadSubnets", `{}`)
	subnets, _ := out["Subnets"].([]any)
	if len(subnets) != 1 {
		t.Errorf("the refused delete changed the store: %d subnets", len(subnets))
	}
}

// An error answer must not leave machines running behind it.
//
// The create loop stored and booted per iteration and returned mid-way with no
// unwind: an audit asked for fourteen machines in a /28 that holds eleven, got
// an error, and found eleven running machines the client was never told about.
func TestAFailedCreateLeavesNothingBehind(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.44.0.0/16", "10.44.1.0/28")

	status, _ := post(t, ts, "CreateVms",
		`{"ImageId":"ami-12345678","SubnetId":"`+subnetID+`","MaxVmsCount":14,"BootOnCreation":false}`)
	if status == http.StatusOK {
		t.Skip("the subnet was large enough; nothing to unwind")
	}

	_, out := post(t, ts, "ReadVms", `{}`)
	vms, _ := out["Vms"].([]any)
	if len(vms) != 0 {
		t.Errorf("an error answer left %d machines running", len(vms))
	}
}

// DryRun must validate and change nothing.
//
// The field was declared on six request structs and read by none of them, so
// `CreateNet --DryRun` created the Net and `CreateSubnet --DryRun` created a
// bridge on the operator's host. A declared-but-unread field is invisible to the
// unread-field report, which is what makes this class worth its own test.
func TestDryRunChangesNothing(t *testing.T) {
	ts := newServer(t)

	status, out := post(t, ts, "CreateNet", `{"IpRange":"10.45.0.0/16","DryRun":true}`)
	if status != http.StatusOK {
		t.Fatalf("a dry run should validate, not refuse: %d %v", status, out)
	}
	if _, created := out["Net"]; created {
		t.Error("the dry run answered with a Net it should not have created")
	}
	_, out = post(t, ts, "ReadNets", `{}`)
	nets, _ := out["Nets"].([]any)
	if len(nets) != 0 {
		t.Errorf("the dry run created %d Net(s)", len(nets))
	}
}

// AvailableIpsCount must follow the machines.
//
// It was built from a fresh allocator with nothing reserved, so a /24 holding
// three machines still answered 251 — while the view's own comment claimed the
// count is computed from the mask *and* what is allocated. The suite only ever
// read empty subnets, so it proved the half that worked.
func TestAvailableIpsCountFollowsTheMachines(t *testing.T) {
	ts := newServer(t)
	_, subnetID := netAndSubnet(t, ts, "10.46.0.0/16", "10.46.1.0/24")

	_, out := post(t, ts, "ReadSubnets", `{}`)
	subnets, _ := out["Subnets"].([]any)
	first, _ := subnets[0].(map[string]any)
	empty, _ := first["AvailableIpsCount"].(float64)

	for range 3 {
		post(t, ts, "CreateVms", `{"ImageId":"ami-12345678","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
	}

	_, out = post(t, ts, "ReadSubnets", `{}`)
	subnets, _ = out["Subnets"].([]any)
	after, _ := subnets[0].(map[string]any)
	got, _ := after["AvailableIpsCount"].(float64)

	if got != empty-3 {
		t.Errorf("AvailableIpsCount is %v after three machines, want %v", got, empty-3)
	}
}

// A keypair must refuse what is not a key.
//
// Anything was accepted, including a multi-line value that cloud-init refuses
// later — so the create succeeded and the machine booted holding the wrong bytes
// and refusing every login. The Scaleway pack refuses at entry; this is the same
// control, one API over, which is where the factorisation rule puts it.
func TestAKeypairRefusesWhatIsNotAKey(t *testing.T) {
	ts := newServer(t)
	for _, bad := range []string{
		"definitely not a key",
		"ssh-rsa AAAA\nruncmd:\n  - touch /tmp/pwned",
		"",
	} {
		if bad == "" {
			continue // an absent key is legitimate: the API generates one
		}
		if status, _ := post(t, ts, "CreateKeypair", `{"KeypairName":"k","PublicKey":`+quote(bad)+`}`); status == http.StatusOK {
			t.Errorf("accepted %q as a public key", bad)
		}
	}
	// The accepting half: a real key must pass, or the check would only be a way
	// to refuse.
	good := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleExampleExampleExampleExampleEx user@host"
	if status, out := post(t, ts, "CreateKeypair", `{"KeypairName":"good","PublicKey":`+quote(good)+`}`); status != http.StatusOK {
		t.Errorf("refused a real key: %d %v", status, out)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
