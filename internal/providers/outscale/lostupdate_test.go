package outscale_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store/storetest"
	"github.com/stephrobert/feint/internal/providers/outscale"
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
		`{"KeypairName":"barrage-key","PublicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"}`)

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

// A terminated Vm released its interfaces and kept its volumes (#215).
//
// The volume then names a machine that is gone, so LinkVolume refuses to attach
// it anywhere else and UnlinkVolume is the only way out — a call no client makes,
// because from where the client stands the Vm is terminated and the volume is
// supposed to be free. The emulator has to be restarted.
//
// The test drives the way out rather than reading the store: attach, kill the Vm,
// attach the same volume to another one. That is the sequence a user is stuck in.
func TestTerminatingAVmFreesItsVolumes(t *testing.T) {
	ts, _ := newOutscaleBarrageServer(t)

	vm := func(name string) string {
		t.Helper()
		_, out := post(t, ts, "CreateVms", `{"ImageId":"ami-12345678","VmType":"tinav4.c1r1p2"}`)
		vms, _ := out["Vms"].([]any)
		if len(vms) == 0 {
			t.Fatalf("%s: no Vm in %v", name, out)
		}
		first, _ := vms[0].(map[string]any)
		id, _ := first["VmId"].(string)
		return id
	}

	doomed, survivor := vm("doomed"), vm("survivor")

	_, out := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`)
	volume, _ := out["Volume"].(map[string]any)
	volID, _ := volume["VolumeId"].(string)
	if volID == "" {
		t.Fatalf("no VolumeId in %v", out)
	}

	if status, out := post(t, ts, "LinkVolume",
		`{"VolumeId":"`+volID+`","VmId":"`+doomed+`","DeviceName":"/dev/xvdb"}`); status != http.StatusOK {
		t.Fatalf("link: status %d (%v)", status, out)
	}
	// The exclusivity is real before the Vm dies, or the assertion after it would
	// pass on a volume nothing was holding.
	if status, _ := post(t, ts, "LinkVolume",
		`{"VolumeId":"`+volID+`","VmId":"`+survivor+`","DeviceName":"/dev/xvdb"}`); status == http.StatusOK {
		t.Fatal("a linked volume was attached to a second Vm, so this test measures nothing")
	}

	post(t, ts, "StopVms", `{"VmIds":["`+doomed+`"]}`)
	if status, out := post(t, ts, "DeleteVms", `{"VmIds":["`+doomed+`"]}`); status != http.StatusOK {
		t.Fatalf("delete: status %d (%v)", status, out)
	}

	if status, out := post(t, ts, "LinkVolume",
		`{"VolumeId":"`+volID+`","VmId":"`+survivor+`","DeviceName":"/dev/xvdb"}`); status != http.StatusOK {
		t.Fatalf("the volume is still held by a terminated Vm: status %d (%v)", status, out)
	}
}

// The same control, on the other shape a lost update takes: an acknowledged
// *element* of one collection, not an acknowledged field (#289).
//
// Replaying pli01/terraform-outscale-k3s measured it: Terraform creates 15
// security group rules through its default 10-way parallelism, the group
// received seven CreateSecurityGroupRule answering 200 each, and only six
// rules survived — the reduced reproduction acknowledged 8 and stored 5. The
// handler read a clone, appended to the clone's list and committed the whole
// resource, which is exactly the non-merge store.Commit documents. Each writer
// below owns one rule of one group, the way each writer above owns one field
// of one Vm.
func TestConcurrentRuleCreatesKeepEveryAcknowledgedRule(t *testing.T) {
	ts, _ := newOutscaleBarrageServer(t)

	found := storetest.NoLostUpdate(40, func(trial int) []storetest.Write {
		_, out := post(t, ts, "CreateSecurityGroup", fmt.Sprintf(
			`{"SecurityGroupName":"race-%d","Description":"membership barrage"}`, trial))
		sg, _ := out["SecurityGroup"].(map[string]any)
		id, _ := sg["SecurityGroupId"].(string)
		if id == "" {
			t.Fatalf("create: no SecurityGroupId in %v", out)
		}

		writes := make([]storetest.Write, 0, 8)
		for i := range 8 {
			port := 1001 + i
			writes = append(writes, storetest.Write{
				Field: fmt.Sprintf("inbound tcp %d on %s", port, id),
				Apply: func() bool {
					status, _ := postRaw(ts, "CreateSecurityGroupRule", fmt.Sprintf(
						`{"Flow":"Inbound","IpProtocol":"tcp","FromPortRange":%d,"ToPortRange":%d,"IpRange":"10.0.0.0/24","SecurityGroupId":%q}`,
						port, port, id))
					return status == http.StatusOK
				},
				Got: func() string {
					_, out := post(t, ts, "ReadSecurityGroups",
						`{"Filters":{"SecurityGroupIds":["`+id+`"]}}`)
					groups, _ := out["SecurityGroups"].([]any)
					if len(groups) == 0 {
						return "<the group is gone>"
					}
					group, _ := groups[0].(map[string]any)
					for _, raw := range listAny(group["InboundRules"]) {
						rule, _ := raw.(map[string]any)
						if from, _ := rule["FromPortRange"].(float64); int(from) == port {
							return "stored"
						}
					}
					return "lost"
				},
				Want: "stored",
			})
		}
		return writes
	})

	if len(found) > 0 {
		t.Errorf("the rule create path lost a rule it had acknowledged:\n%s", strings.Join(found, "\n"))
	}
}

// Routes are the same collection shape on the same stack: a route table takes
// its routes through Terraform's parallelism exactly as a group takes its
// rules, and createRoute had the same clone-append-Commit body.
func TestConcurrentRouteCreatesKeepEveryAcknowledgedRoute(t *testing.T) {
	ts, _ := newOutscaleBarrageServer(t)

	found := storetest.NoLostUpdate(40, func(trial int) []storetest.Write {
		// One block per trial: Nets whose ranges overlap are refused, and every
		// trial must build on a target nothing else has touched anyway.
		_, out := post(t, ts, "CreateNet", fmt.Sprintf(`{"IpRange":"10.%d.0.0/24"}`, trial))
		net, _ := out["Net"].(map[string]any)
		netID, _ := net["NetId"].(string)
		if netID == "" {
			t.Fatalf("create: no NetId in %v", out)
		}
		_, out = post(t, ts, "CreateRouteTable", `{"NetId":"`+netID+`"}`)
		table, _ := out["RouteTable"].(map[string]any)
		tableID, _ := table["RouteTableId"].(string)
		if tableID == "" {
			t.Fatalf("create: no RouteTableId in %v", out)
		}
		_, out = post(t, ts, "CreateInternetService", `{}`)
		gateway, _ := out["InternetService"].(map[string]any)
		gatewayID, _ := gateway["InternetServiceId"].(string)
		if gatewayID == "" {
			t.Fatalf("create: no InternetServiceId in %v", out)
		}
		post(t, ts, "LinkInternetService",
			`{"InternetServiceId":"`+gatewayID+`","NetId":"`+netID+`"}`)

		writes := make([]storetest.Write, 0, 8)
		for i := range 8 {
			destination := fmt.Sprintf("198.51.%d.0/24", 100+i)
			writes = append(writes, storetest.Write{
				Field: "route to " + destination,
				Apply: func() bool {
					status, _ := postRaw(ts, "CreateRoute", fmt.Sprintf(
						`{"RouteTableId":%q,"DestinationIpRange":%q,"GatewayId":%q}`,
						tableID, destination, gatewayID))
					return status == http.StatusOK
				},
				Got: func() string {
					_, out := post(t, ts, "ReadRouteTables",
						`{"Filters":{"RouteTableIds":["`+tableID+`"]}}`)
					tables, _ := out["RouteTables"].([]any)
					if len(tables) == 0 {
						return "<the table is gone>"
					}
					table, _ := tables[0].(map[string]any)
					for _, raw := range listAny(table["Routes"]) {
						route, _ := raw.(map[string]any)
						if route["DestinationIpRange"] == destination {
							return "stored"
						}
					}
					return "lost"
				},
				Want: "stored",
			})
		}
		return writes
	})

	if len(found) > 0 {
		t.Errorf("the route create path lost a route it had acknowledged:\n%s", strings.Join(found, "\n"))
	}
}

// Tags are the third member: CreateTags wrote its merge back with Put, which
// loses a concurrent key the way Commit does and resurrects a deleted resource
// on top of it. Each writer owns one key of one Net's tag collection.
func TestConcurrentTagCreatesKeepEveryAcknowledgedTag(t *testing.T) {
	ts, _ := newOutscaleBarrageServer(t)

	found := storetest.NoLostUpdate(40, func(trial int) []storetest.Write {
		// One block per trial, as above: overlapping Nets are refused.
		_, out := post(t, ts, "CreateNet", fmt.Sprintf(`{"IpRange":"10.%d.1.0/24"}`, trial))
		net, _ := out["Net"].(map[string]any)
		netID, _ := net["NetId"].(string)
		if netID == "" {
			t.Fatalf("create: no NetId in %v", out)
		}

		writes := make([]storetest.Write, 0, 8)
		for i := range 8 {
			key := fmt.Sprintf("barrage-%d", i)
			writes = append(writes, storetest.Write{
				Field: "tag " + key + " on " + netID,
				Apply: func() bool {
					status, _ := postRaw(ts, "CreateTags", fmt.Sprintf(
						`{"ResourceIds":[%q],"Tags":[{"Key":%q,"Value":"held"}]}`, netID, key))
					return status == http.StatusOK
				},
				Got: func() string {
					_, out := post(t, ts, "ReadNets", `{"Filters":{"NetIds":["`+netID+`"]}}`)
					nets, _ := out["Nets"].([]any)
					if len(nets) == 0 {
						return "<the Net is gone>"
					}
					net, _ := nets[0].(map[string]any)
					for _, raw := range listAny(net["Tags"]) {
						tag, _ := raw.(map[string]any)
						if tag["Key"] == key {
							return "stored"
						}
					}
					return "lost"
				},
				Want: "stored",
			})
		}
		return writes
	})

	if len(found) > 0 {
		t.Errorf("the tag create path lost a key it had acknowledged:\n%s", strings.Join(found, "\n"))
	}
}

// listAny reads a decoded JSON list, nil included.
func listAny(v any) []any {
	out, _ := v.([]any)
	return out
}

// The scalar half of the family, in the exact pair #295 measured: CreateTags,
// under the store lock since #289, against LinkVolume, a clone-mutate-Commit
// until this fix — two *different* handlers on one volume, which is the shape
// the per-handler barrages above cannot see. Before the fix the issue's
// reproduction lost the acknowledged tag 7/200 trials, and this repository's
// re-measurement 11/200 (5 runs of 40, 2026-08-18); after it, 0/200.
func TestConcurrentTagAndLinkKeepBothVolumeWrites(t *testing.T) {
	ts, _ := newOutscaleBarrageServer(t)

	_, out := post(t, ts, "CreateVms", `{"ImageId":"ami-12345678","VmType":"tinav4.c1r1p2"}`)
	vms, _ := out["Vms"].([]any)
	vm, _ := vms[0].(map[string]any)
	vmID, _ := vm["VmId"].(string)
	if vmID == "" {
		t.Fatalf("create: no VmId in %v", out)
	}

	found := storetest.NoLostUpdate(40, func(trial int) []storetest.Write {
		_, out := post(t, ts, "CreateVolume", `{"SubregionName":"eu-west-2a","Size":10}`)
		volume, _ := out["Volume"].(map[string]any)
		volID, _ := volume["VolumeId"].(string)
		if volID == "" {
			t.Fatalf("trial %d: no VolumeId in %v", trial, out)
		}

		readVolume := func() map[string]any {
			_, out := post(t, ts, "ReadVolumes", `{"Filters":{"VolumeIds":["`+volID+`"]}}`)
			volumes, _ := out["Volumes"].([]any)
			if len(volumes) == 0 {
				return nil
			}
			view, _ := volumes[0].(map[string]any)
			return view
		}

		return []storetest.Write{
			{
				Field: "tag probe on " + volID,
				Apply: func() bool {
					status, _ := postRaw(ts, "CreateTags",
						`{"ResourceIds":["`+volID+`"],"Tags":[{"Key":"probe","Value":"held"}]}`)
					return status == http.StatusOK
				},
				Got: func() string {
					view := readVolume()
					if view == nil {
						return "<the volume is gone>"
					}
					for _, raw := range listAny(view["Tags"]) {
						tag, _ := raw.(map[string]any)
						if tag["Key"] == "probe" {
							return "stored"
						}
					}
					return "lost"
				},
				Want: "stored",
			},
			{
				Field: "link to " + vmID,
				Apply: func() bool {
					status, _ := postRaw(ts, "LinkVolume",
						`{"VolumeId":"`+volID+`","VmId":"`+vmID+`","DeviceName":"/dev/xvdb"}`)
					return status == http.StatusOK
				},
				Got: func() string {
					view := readVolume()
					if view == nil {
						return "<the volume is gone>"
					}
					if len(listAny(view["LinkedVolumes"])) > 0 {
						return "linked"
					}
					return "lost"
				},
				Want: "linked",
			},
		}
	})

	if len(found) > 0 {
		t.Errorf("two different handlers on one volume lost an acknowledged write:\n%s", strings.Join(found, "\n"))
	}
}

// The lifecycle half of #295: a tag write against a state transition on the
// same Vm. StopVms goes through Binding.Observe, whose write-back replaced
// State, Runtime and Attrs wholesale from the clone it took before the runtime
// call — with no runtime configured the window is one handler's decode and the
// loss measured 5/200 trials (2026-08-18); with a machine runtime, a launch
// holds it open for seconds. The per-field merge in store.Commit closes it,
// and this test drives both directions: the tag must survive the stop, and
// the stop's state must survive the tag.
func TestConcurrentTagAndStopKeepBothVmWrites(t *testing.T) {
	ts, _ := newOutscaleBarrageServer(t)

	found := storetest.NoLostUpdate(40, func(trial int) []storetest.Write {
		_, out := post(t, ts, "CreateVms", `{"ImageId":"ami-12345678","VmType":"tinav4.c1r1p2"}`)
		vms, _ := out["Vms"].([]any)
		vm, _ := vms[0].(map[string]any)
		vmID, _ := vm["VmId"].(string)
		if vmID == "" {
			t.Fatalf("trial %d: no VmId in %v", trial, out)
		}

		readVM := func() map[string]any {
			_, out := post(t, ts, "ReadVms", `{"Filters":{"VmIds":["`+vmID+`"]}}`)
			read, _ := out["Vms"].([]any)
			if len(read) == 0 {
				return nil
			}
			view, _ := read[0].(map[string]any)
			return view
		}

		return []storetest.Write{
			{
				Field: "tag probe on " + vmID,
				Apply: func() bool {
					status, _ := postRaw(ts, "CreateTags",
						`{"ResourceIds":["`+vmID+`"],"Tags":[{"Key":"probe","Value":"held"}]}`)
					return status == http.StatusOK
				},
				Got: func() string {
					view := readVM()
					if view == nil {
						return "<the Vm is gone>"
					}
					for _, raw := range listAny(view["Tags"]) {
						tag, _ := raw.(map[string]any)
						if tag["Key"] == "probe" {
							return "stored"
						}
					}
					return "lost"
				},
				Want: "stored",
			},
			{
				Field: "stop of " + vmID,
				Apply: func() bool {
					status, _ := postRaw(ts, "StopVms", `{"VmIds":["`+vmID+`"]}`)
					return status == http.StatusOK
				},
				Got: func() string {
					view := readVM()
					if view == nil {
						return "<the Vm is gone>"
					}
					state, _ := view["State"].(string)
					return state
				},
				Want: "stopped",
			},
		}
	})

	if len(found) > 0 {
		t.Errorf("a lifecycle transition and a tag write on one Vm lost an acknowledged write:\n%s", strings.Join(found, "\n"))
	}
}

// The second defect #295 named in updateNic, distinct from the lost update:
// the handler wrote `link["DeleteOnVmDeletion"] = …` straight through the
// clone's LinkNic map, and resource.Clone is shallow on Attrs values — that
// map IS the stored one. The write was visible to every concurrent reader
// before the write-back, and stayed behind even when the write-back failed.
//
// The stored LinkNic map only exists on state that arrived from outside —
// PUT /_feint/state, `feint snapshot load` — because linkNic itself stores the
// attachment as flat keys; the seeding below is that restore path, reduced.
func TestUpdateNicDoesNotMutateTheStoredLinkInPlace(t *testing.T) {
	ts, st := newOutscaleBarrageServer(t)

	st.Put(&resource.Resource{
		ID:     "eni-restored1",
		Kind:   "nic",
		Tenant: resource.Tenant{Provider: outscale.Name},
		State:  "in-use",
		Attrs: map[string]any{
			"State": "in-use",
			"LinkNic": map[string]any{
				"LinkNicId":          "eni-attach-restored1",
				"DeleteOnVmDeletion": false,
			},
			"Tags": []any{},
		},
	})

	// A reader that resolved the NIC before the update: its clone shares the
	// nested LinkNic map with the store, which is exactly why the handler must
	// never write through it.
	witness, found := st.Get(outscale.Name, "nic", "eni-restored1")
	if !found {
		t.Fatal("the seeded NIC is not in the store")
	}

	status, _ := post(t, ts, "UpdateNic",
		`{"NicId":"eni-restored1","LinkNic":{"LinkNicId":"eni-attach-restored1","DeleteOnVmDeletion":true}}`)
	if status != http.StatusOK {
		t.Fatalf("UpdateNic answered %d, so this test measures nothing", status)
	}

	link, _ := witness.Attrs["LinkNic"].(map[string]any)
	if flag, _ := link["DeleteOnVmDeletion"].(bool); flag {
		t.Fatal("the update wrote through the shared map: a reader that resolved the NIC before the update saw the new value without any store write")
	}
	stored, _ := st.Get(outscale.Name, "nic", "eni-restored1")
	storedLink, _ := stored.Attrs["LinkNic"].(map[string]any)
	if flag, _ := storedLink["DeleteOnVmDeletion"].(bool); !flag {
		t.Fatal("the update did not land at all")
	}
}
