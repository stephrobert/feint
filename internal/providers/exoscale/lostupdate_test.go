package exoscale_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/store/storetest"
)

// The same property as the two packs beside it, driven with this pack's own
// traffic: PUT /v2/instance/{id}, one field per writer, a fresh instance per
// trial.
//
// This pack is right for a third reason, and it is the best of the three:
// updateInstance mutates inside store.Update, so the read, the change and the
// write are one critical section held by the store itself. There is no window to
// lose a field in, and no lock for a later reader to forget.
//
// Running the shared control here anyway is the point of #211: a property proven
// once per pack is a property the fourth pack will be missing, and the one that
// had no ordering at all had simply been the one nobody audited.
func TestConcurrentUpdatesKeepEveryAcknowledgedField(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)

	found := storetest.NoLostUpdate(40, func(trial int) []storetest.Write {
		status, created := callRaw(h, "POST", "/v2/instance", fmt.Sprintf(`{
			"name": "before-%d",
			"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
			"template": {"id": "11111111-1111-4111-8111-111111111111"},
			"disk-size": 10
		}`, trial))
		if status != http.StatusOK {
			t.Fatalf("trial %d create: status %d (%v)", trial, status, created)
		}
		ref, _ := created["reference"].(map[string]any)
		id, _ := ref["id"].(string)
		if id == "" {
			t.Fatalf("the create operation names no resource: %v", created)
		}

		update := func(body string) bool {
			status, _ := callRaw(h, "PUT", "/v2/instance/"+id, body)
			return status == http.StatusOK
		}
		field := func(read func(map[string]any) string) func() string {
			return func() string {
				status, out := callRaw(h, "GET", "/v2/instance/"+id, "")
				if status != http.StatusOK {
					return fmt.Sprintf("<get answered %d>", status)
				}
				return read(out)
			}
		}

		return []storetest.Write{
			{
				Field: "name",
				Apply: func() bool { return update(`{"name":"after"}`) },
				Got: field(func(out map[string]any) string {
					name, _ := out["name"].(string)
					return name
				}),
				Want: "after",
			},
			{
				Field: "user-data",
				Apply: func() bool { return update(`{"user-data":"YmFycmFnZQ=="}`) },
				Got: field(func(out map[string]any) string {
					data, _ := out["user-data"].(string)
					return data
				}),
				Want: "YmFycmFnZQ==",
			},
			{
				Field: "labels",
				Apply: func() bool { return update(`{"labels":{"barrage":"yes"}}`) },
				Got: field(func(out map[string]any) string {
					labels, _ := out["labels"].(map[string]any)
					value, _ := labels["barrage"].(string)
					return value
				}),
				Want: "yes",
			},
		}
	})

	if len(found) > 0 {
		t.Errorf("the update path lost a field it had acknowledged:\n%s", strings.Join(found, "\n"))
	}
}

// The same control on the other shape a lost update takes: an acknowledged
// *element* of one collection (#289). This pack already appends its rules
// inside store.Update, so this is a regression test for a correct handler —
// exactly what the Outscale pack lacked when the k3s replay measured 8 rule
// creates acknowledged and 5 rules stored on the group next door.
func TestConcurrentRuleCreatesKeepEveryAcknowledgedRule(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)

	found := storetest.NoLostUpdate(40, func(trial int) []storetest.Write {
		status, created := callRaw(h, "POST", "/v2/security-group",
			fmt.Sprintf(`{"name":"race-%d","description":"membership barrage"}`, trial))
		if status != http.StatusOK {
			t.Fatalf("trial %d create: status %d (%v)", trial, status, created)
		}
		ref, _ := created["reference"].(map[string]any)
		id, _ := ref["id"].(string)
		if id == "" {
			t.Fatalf("the create operation names no resource: %v", created)
		}

		writes := make([]storetest.Write, 0, 8)
		for i := range 8 {
			port := 1001 + i
			writes = append(writes, storetest.Write{
				Field: fmt.Sprintf("ingress tcp %d on %s", port, id),
				Apply: func() bool {
					status, _ := callRaw(h, "POST", "/v2/security-group/"+id+"/rules", fmt.Sprintf(
						`{"flow-direction":"ingress","protocol":"tcp","network":"10.0.0.0/24","start-port":%d,"end-port":%d}`,
						port, port))
					return status == http.StatusOK
				},
				Got: func() string {
					status, out := callRaw(h, "GET", "/v2/security-group/"+id, "")
					if status != http.StatusOK {
						return fmt.Sprintf("<get answered %d>", status)
					}
					rules, _ := out["rules"].([]any)
					for _, raw := range rules {
						rule, _ := raw.(map[string]any)
						if start, _ := rule["start-port"].(float64); int(start) == port {
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

// The one update path of this pack that did NOT mutate inside store.Update —
// #295 called the pack clean, and the audit that fixed the family found this
// one: updatePrivateNetwork read a clone, possibly rebuilt the backing network
// on the runtime, and wrote the whole clone back with Put. Two writers on
// disjoint fields of one private network therefore erased each other after
// their 200s, and the Put also resurrected a network deleted meanwhile. The
// write-back is a per-field Commit now, and each writer's field must survive
// the other.
func TestConcurrentPrivateNetworkUpdatesKeepBothFields(t *testing.T) {
	h, _ := newExoscaleBarrageServer(t)

	found := storetest.NoLostUpdate(40, func(trial int) []storetest.Write {
		status, created := callRaw(h, "POST", "/v2/private-network",
			fmt.Sprintf(`{"name":"cross-%d"}`, trial))
		if status != http.StatusOK {
			t.Fatalf("trial %d create: status %d (%v)", trial, status, created)
		}
		ref, _ := created["reference"].(map[string]any)
		id, _ := ref["id"].(string)
		if id == "" {
			t.Fatalf("the create operation names no resource: %v", created)
		}

		update := func(body string) bool {
			status, _ := callRaw(h, "PUT", "/v2/private-network/"+id, body)
			return status == http.StatusOK
		}
		field := func(read func(map[string]any) string) func() string {
			return func() string {
				status, out := callRaw(h, "GET", "/v2/private-network/"+id, "")
				if status != http.StatusOK {
					return fmt.Sprintf("<get answered %d>", status)
				}
				return read(out)
			}
		}

		return []storetest.Write{
			{
				Field: "name",
				Apply: func() bool { return update(`{"name":"after"}`) },
				Got: field(func(out map[string]any) string {
					name, _ := out["name"].(string)
					return name
				}),
				Want: "after",
			},
			{
				Field: "description",
				Apply: func() bool { return update(`{"description":"held"}`) },
				Got: field(func(out map[string]any) string {
					description, _ := out["description"].(string)
					return description
				}),
				Want: "held",
			},
			{
				Field: "labels",
				Apply: func() bool { return update(`{"labels":{"probe":"held"}}`) },
				Got: field(func(out map[string]any) string {
					labels, _ := out["labels"].(map[string]any)
					value, _ := labels["probe"].(string)
					return value
				}),
				Want: "held",
			},
		}
	})

	if len(found) > 0 {
		t.Errorf("the private network update lost a field it had acknowledged:\n%s", strings.Join(found, "\n"))
	}
}
