package outscale_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/store/storetest"
)

// The same property as the two packs beside it, driven with this pack's own
// traffic: POST /api/v1/UpdateVm, one field per writer.
//
// This pack was already right — updateVm holds the per-target lock, and says why
// in a comment naming an audit. It runs the shared control anyway, and that is
// the point of #211: a discipline that only exists where an audit already bit is
// a discipline the next pack will be missing. Here it is a regression test for a
// correct handler; in the pack that lacked it, it was the failure.
func TestConcurrentUpdatesKeepEveryAcknowledgedField(t *testing.T) {
	ts, _ := newOutscaleBarrageServer(t)
	// One keypair for the whole run: it has to exist before UpdateVm will take
	// its name, and validVmFields refusing an unknown one is a guard, not a race.
	post(t, ts, "CreateKeypair",
		`{"KeypairName":"barrage-key","PublicKey":"ssh-ed25519 AAAAC3Nz fake@feint"}`)

	found := storetest.NoLostUpdate(40, func(trial int) []storetest.Write {
		_, out := post(t, ts, "CreateVms", `{"ImageId":"ami-12345678","VmType":"tinav4.c1r1p2"}`)
		vms, _ := out["Vms"].([]any)
		if len(vms) == 0 {
			t.Fatalf("create: no Vm in %v", out)
		}
		vm, _ := vms[0].(map[string]any)
		id, _ := vm["VmId"].(string)
		if id == "" {
			t.Fatalf("create: no VmId in %v", vm)
		}
		// Stopped first: UpdateVm refuses several fields on a running machine, and a
		// refusal would make the run measure the guard instead of the ordering.
		post(t, ts, "StopVms", `{"VmIds":["`+id+`"]}`)

		update := func(body string) bool {
			status, _ := postRaw(ts, "UpdateVm", body)
			return status == http.StatusOK
		}
		field := func(name string) func() string {
			return func() string {
				_, out := post(t, ts, "ReadVms", `{"Filters":{"VmIds":["`+id+`"]}}`)
				vms, _ := out["Vms"].([]any)
				if len(vms) == 0 {
					return "<the Vm is gone>"
				}
				vm, _ := vms[0].(map[string]any)
				switch value := vm[name].(type) {
				case string:
					return value
				case bool:
					return fmt.Sprintf("%v", value)
				default:
					raw, _ := json.Marshal(value)
					return string(raw)
				}
			}
		}

		return []storetest.Write{
			{
				Field: "KeypairName",
				Apply: func() bool { return update(`{"VmId":"` + id + `","KeypairName":"barrage-key"}`) },
				Got:   field("KeypairName"),
				Want:  "barrage-key",
			},
			{
				Field: "UserData",
				Apply: func() bool { return update(`{"VmId":"` + id + `","UserData":"YmFycmFnZQ=="}`) },
				Got:   field("UserData"),
				Want:  "YmFycmFnZQ==",
			},
			{
				Field: "DeletionProtection",
				Apply: func() bool { return update(`{"VmId":"` + id + `","DeletionProtection":true}`) },
				Got:   field("DeletionProtection"),
				Want:  "true",
			},
		}
	})

	if len(found) > 0 {
		t.Errorf("the update path lost a field it had acknowledged:\n%s", strings.Join(found, "\n"))
	}
}

// The shared control above found this while looking for something else, which is
// the argument for running it on a pack that was already right.
//
// UpdateVmRequest declares DeletionProtection upstream ("if true, you cannot
// delete the VM unless you change this parameter back to false"), and this pack
// read the flag on the delete, wrote it at create, and dropped it on the update.
// So protection could be set and never cleared: the one request that undoes it
// answered 200 and changed nothing, which is the worse half — the client is told
// the change landed.
func TestDeletionProtectionCanBeClearedByAnUpdate(t *testing.T) {
	ts, _ := newOutscaleBarrageServer(t)

	_, out := post(t, ts, "CreateVms",
		`{"ImageId":"ami-12345678","VmType":"tinav4.c1r1p2","DeletionProtection":true}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	id, _ := vm["VmId"].(string)
	post(t, ts, "StopVms", `{"VmIds":["`+id+`"]}`)

	// The flag is doing its job first: without this the test could pass on a
	// machine nothing was protecting.
	if status, _ := post(t, ts, "DeleteVms", `{"VmIds":["`+id+`"]}`); status == http.StatusOK {
		t.Fatal("a protected Vm was deleted, so this test measures nothing")
	}

	status, updated := post(t, ts, "UpdateVm", `{"VmId":"`+id+`","DeletionProtection":false}`)
	if status != http.StatusOK {
		t.Fatalf("clearing the protection answered %d: %v", status, updated)
	}
	got, _ := updated["Vm"].(map[string]any)
	if protected, _ := got["DeletionProtection"].(bool); protected {
		t.Error("the update answered 200 and gave the flag back set")
	}
	if status, out := post(t, ts, "DeleteVms", `{"VmIds":["`+id+`"]}`); status != http.StatusOK {
		t.Fatalf("the Vm is still protected after the flag was cleared: %d %v", status, out)
	}
}
