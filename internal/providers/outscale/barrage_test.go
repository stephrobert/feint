package outscale_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/core/store/storetest"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The barrage, and the invariant sweep that follows it.
//
// Same reasoning as the Scaleway one, and the sweep is literally the same code:
// it lives in the core, keyed on nothing but resource.Resource, so a pack does
// not get to define its own idea of coherent — and a fourth pack inherits it by
// calling one function.
//
// What each pack still owns is the traffic. Outscale's is a POST per action, its
// resources are Nets and Subnets and Vms, and its addresses come from a pool
// this pack allocates itself.

const outscaleBarrageWorkers = 8

func newOutscaleBarrageServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, env.Store
}

// postRaw is post() without the testing.T: a goroutine may not call t.Fatalf,
// and a helper that does turns a failure into undefined behaviour.
func postRaw(ts *httptest.Server, action, body string) (int, map[string]any) {
	resp, err := ts.Client().Post(ts.URL+"/api/v1/"+action, "application/json", strings.NewReader(body))
	if err != nil {
		return -1, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func TestAnOutscaleBarrageLeavesTheStoreCoherent(t *testing.T) {
	ts, st := newOutscaleBarrageServer(t)

	var wg sync.WaitGroup
	problems := make(chan string, outscaleBarrageWorkers*8)

	for w := range outscaleBarrageWorkers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			tag := fmt.Sprintf("w%d", worker)

			status, net := postRaw(ts, "CreateNet", fmt.Sprintf(`{"IpRange":"10.%d.0.0/16"}`, 100+worker))
			if status != http.StatusOK {
				problems <- fmt.Sprintf("%s: CreateNet answered %d", tag, status)
				return
			}
			netID := nestedID(net, "Net", "NetId")

			status, subnet := postRaw(ts, "CreateSubnet",
				fmt.Sprintf(`{"NetId":%q,"IpRange":"10.%d.1.0/24"}`, netID, 100+worker))
			if status != http.StatusOK {
				problems <- fmt.Sprintf("%s: CreateSubnet answered %d", tag, status)
				return
			}
			subnetID := nestedID(subnet, "Subnet", "SubnetId")

			// Two addresses and two machines per worker: enough interleaving for
			// a shared pool to hand one out twice if nothing serialises it.
			for i := range 2 {
				status, ip := postRaw(ts, "CreatePublicIp", `{}`)
				if status != http.StatusOK {
					problems <- fmt.Sprintf("%s: CreatePublicIp answered %d", tag, status)
					continue
				}
				_ = ip

				status, vm := postRaw(ts, "CreateVms",
					fmt.Sprintf(`{"ImageId":"ami-00000001","SubnetId":%q}`, subnetID))
				if status != http.StatusOK {
					problems <- fmt.Sprintf("%s-%d: CreateVms answered %d", tag, i, status)
					continue
				}
				vms, _ := vm["Vms"].([]any)
				if len(vms) == 0 {
					problems <- fmt.Sprintf("%s-%d: CreateVms answered no machine", tag, i)
					continue
				}
				first, _ := vms[0].(map[string]any)
				vmID, _ := first["VmId"].(string)
				postRaw(ts, "DeleteVms", fmt.Sprintf(`{"VmIds":[%q]}`, vmID))
			}
		}(w)
	}
	wg.Wait()
	close(problems)

	var refused []string
	for p := range problems {
		refused = append(refused, p)
	}
	if len(refused) > 0 {
		t.Errorf("%d request(s) the barrage did not expect to fail:\n%s",
			len(refused), strings.Join(refused, "\n"))
	}

	if found := storetest.Sweep(st.All()); len(found) != 0 {
		t.Errorf("the store is incoherent after the barrage:\n%s", strings.Join(found, "\n"))
	}
}

func nestedID(body map[string]any, object, field string) string {
	nested, _ := body[object].(map[string]any)
	id, _ := nested[field].(string)
	return id
}
