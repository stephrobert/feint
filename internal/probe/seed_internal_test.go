package probe

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
)

// Internal tests for the pool and the planner: the properties here — tiering,
// determinism, ordering — are what the end-to-end seed tests rest on, and each
// one exists because its absence was measured as wrong verdicts, never as a
// crash.

func docOf(t *testing.T, body string) *contract.Doc {
	t.Helper()
	doc, err := contract.Read(strings.NewReader(body))
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	return doc
}

const planDoc = `{
  "provider": "stub",
  "operations": {
    "CreateVolume":   {"method": "POST", "path": "/volumes", "request": "CreateVolumeRequest", "response": "V"},
    "CreateSnapshot": {"method": "POST", "path": "/snapshots", "request": "CreateSnapshotRequest", "response": "V"},
    "CreateServer":   {"method": "POST", "path": "/servers", "response": "V"},
    "AddProtection":    {"method": "POST", "path": "/servers/{server_id}/protect", "response": "V"},
    "RemoveProtection": {"method": "POST", "path": "/servers/{server_id}/unprotect", "response": "V"},
    "DeleteVolume":   {"method": "DELETE", "path": "/volumes/{volume_id}", "response": "V"},
    "DeleteSnapshot": {"method": "DELETE", "path": "/snapshots/{snapshot_id}", "response": "V"}
  },
  "schemas": {
    "CreateVolumeRequest":   {"closed": true, "properties": {"name": {"type": "string"}}},
    "CreateSnapshotRequest": {"closed": true, "properties": {"volume_id": {"type": "string"}}},
    "V": {"closed": false, "properties": {"ok": {"type": "boolean"}}}
  }
}`

var planRoutes = []contract.MountedRoute{
	{Method: "POST", Path: "/volumes", Operation: "stub/v1/API.CreateVolume"},
	{Method: "POST", Path: "/snapshots", Operation: "stub/v1/API.CreateSnapshot"},
	{Method: "POST", Path: "/servers", Operation: "stub/v1/API.CreateServer"},
	{Method: "POST", Path: "/servers/{server_id}/protect", Operation: "stub/v1/API.AddProtection"},
	{Method: "POST", Path: "/servers/{server_id}/unprotect", Operation: "stub/v1/API.RemoveProtection"},
	{Method: "DELETE", Path: "/volumes/{volume_id}", Operation: "stub/v1/API.DeleteVolume"},
	{Method: "DELETE", Path: "/snapshots/{snapshot_id}", Operation: "stub/v1/API.DeleteSnapshot"},
}

func positions(plan []Step) map[string]int {
	out := map[string]int{}
	for i, s := range plan {
		out[s.Operation] = i // last occurrence, which is the one that matters
	}
	return out
}

// TestAConsumerRunsAfterItsProducer: CreateSnapshot names a volume_id, so it
// runs after CreateVolume whatever the alphabet says — alphabetically it comes
// first, and that order was the guaranteed refusal #163 measured.
func TestAConsumerRunsAfterItsProducer(t *testing.T) {
	plan, err := Plan(docOf(t, planDoc), planRoutes)
	if err != nil {
		t.Fatal(err)
	}
	at := positions(plan)
	if at["stub/v1/API.CreateSnapshot"] < at["stub/v1/API.CreateVolume"] {
		t.Errorf("CreateSnapshot must follow the CreateVolume that seeds it: %v", at)
	}
}

// TestALayerRunsInNameOrder: both protection steps depend on the server, and
// they must run in name order within their layer — add, then remove. The
// mid-sweep version ran remove in the producer's own sweep and add in the
// next, so the run ended protected and delete-instance refused a lock the
// plan itself had taken.
func TestALayerRunsInNameOrder(t *testing.T) {
	plan, err := Plan(docOf(t, planDoc), planRoutes)
	if err != nil {
		t.Fatal(err)
	}
	at := positions(plan)
	server := at["stub/v1/API.CreateServer"]
	add, remove := at["stub/v1/API.AddProtection"], at["stub/v1/API.RemoveProtection"]
	if add < server || remove < server {
		t.Fatalf("both protection steps need the server first: %v", at)
	}
	if remove < add {
		t.Errorf("within one layer the order is the name order — add before remove: %v", at)
	}
}

// TestTeardownMirrorsTheSeeding: the snapshot was cut from the volume, so it
// dies first. Depth comes from the producers' ranks, and the volume's delete
// running first would refuse — a volume with snapshots is in use.
func TestTeardownMirrorsTheSeeding(t *testing.T) {
	plan, err := Plan(docOf(t, planDoc), planRoutes)
	if err != nil {
		t.Fatal(err)
	}
	at := positions(plan)
	if at["stub/v1/API.DeleteSnapshot"] > at["stub/v1/API.DeleteVolume"] {
		t.Errorf("teardown mirrors seeding: the snapshot dies before its volume: %v", at)
	}
}

// TestTheEnumSentinelIsNotChosen: Scaleway's documents put the proto3 zero
// value first, and unknown_action is a value the emulator refuses like the
// real API does. Sending it measures the sentinel, not the operation.
func TestTheEnumSentinelIsNotChosen(t *testing.T) {
	got := chooseEnum([]any{"unknown_action", "poweron", "poweroff"})
	if got != "poweron" {
		t.Errorf("chooseEnum picked %v, want the first non-sentinel value", got)
	}
	if got := chooseEnum([]any{"unknown_only"}); got != "unknown_only" {
		t.Errorf("with only the sentinel to choose from, it is still the answer: %v", got)
	}
}

// TestARefToAScalarSchemaYieldsAScalar: instance/v1's arch references a bare
// enum schema, and the nested walk used to answer an object for it — the
// emulator refused the JSON type itself ("cannot unmarshal object into … type
// string").
func TestARefToAScalarSchemaYieldsAScalar(t *testing.T) {
	doc := docOf(t, `{
	  "provider": "stub",
	  "operations": {"CreateImage": {"method": "POST", "path": "/images", "request": "Req", "response": "V"}},
	  "schemas": {
	    "Req":  {"closed": true, "required": ["arch"], "properties": {"arch": {"ref": "Arch"}}},
	    "Arch": {"closed": false, "type": "string", "enum": ["unknown_arch", "x86_64", "arm"]},
	    "V":    {"closed": false, "properties": {"ok": {"type": "boolean"}}}
	  }
	}`)
	body, err := minimalBody(doc, Step{Kind: kindCreate}, "Req", newPool())
	if err != nil {
		t.Fatal(err)
	}
	if body["arch"] != "x86_64" {
		t.Errorf("arch must be the first usable enum value, got %v", body["arch"])
	}
}

// TestACreateOnlyVouchesForItsOwnPayload: a create's response embeds the
// resources it references — CreateServer carries the catalogue image it
// booted from — and filing those as "created" made UpdateImage try to change
// the catalogue. Only the payload's own two levels earn the created tier.
func TestACreateOnlyVouchesForItsOwnPayload(t *testing.T) {
	doc := docOf(t, `{
	  "provider": "stub",
	  "operations": {"CreateServer": {"method": "POST", "path": "/servers", "response": "ServerView"}},
	  "schemas": {
	    "ServerView": {"closed": false, "properties": {"server": {"ref": "Server"}}},
	    "Server": {"closed": false, "properties": {"id": {"type": "string"}}},
	    "V": {"closed": false, "properties": {"ok": {"type": "boolean"}}}
	  }
	}`)
	pool := newPool()
	step := Step{Operation: "CreateServer", Contract: "CreateServer", Kind: kindCreate, Makes: "server"}
	pool.harvest(doc, step, map[string]any{
		"server": map[string]any{
			"id":    "srv-1",
			"image": map[string]any{"id": "img-catalogue"},
		},
	})

	if got, _ := pool.take("", "server"); got != "srv-1" {
		t.Errorf("the create vouches for its own id: %v", got)
	}
	byProduct := pool.ids["image"]
	if byProduct == nil || byProduct[""] == nil {
		t.Fatal("the embedded image id is still harvested, as observed")
	}
	if len(byProduct[""].created) != 0 {
		t.Errorf("an embedded reference must not enter the created tier: %+v", byProduct[""])
	}
	if len(byProduct[""].observed) != 1 || byProduct[""].observed[0] != "img-catalogue" {
		t.Errorf("the embedded image lands in the observed tier: %+v", byProduct[""])
	}
}

// TestHarvestIsDeterministic: the walk iterates maps, Go iterates maps in
// random order, and an order that varies makes take() answer different ids on
// identical runs — verdicts that flap between runs are the exact
// non-reproducibility #163 forbids.
func TestHarvestIsDeterministic(t *testing.T) {
	doc := docOf(t, `{
	  "provider": "stub",
	  "operations": {"ListThings": {"method": "GET", "path": "/things", "response": "V"}},
	  "schemas": {"V": {"closed": false, "properties": {"ok": {"type": "boolean"}}}}
	}`)
	response := map[string]any{
		"alpha_id": "a", "beta_id": "b", "gamma_id": "c",
		"things": []any{
			map[string]any{"id": "t1"}, map[string]any{"id": "t2"},
		},
	}
	step := Step{Operation: "ListThings", Contract: "ListThings"}

	reference := newPool()
	reference.harvest(doc, step, response)
	for range 50 {
		pool := newPool()
		pool.harvest(doc, step, response)
		if !reflect.DeepEqual(pool.ids, reference.ids) {
			t.Fatalf("two harvests of one response disagree:\n%v\n%v", pool.ids, reference.ids)
		}
	}
}

// TestTheGenericParameterNeverAnswersWithATenant: "any resource" excludes the
// ids that are not resources — a request id, the account — because each once
// stood in for a resource and bought an invented refusal (CreateTags tagging
// the account id).
func TestTheGenericParameterNeverAnswersWithATenant(t *testing.T) {
	doc := docOf(t, `{
	  "provider": "stub",
	  "operations": {"CreateNet": {"method": "POST", "path": "/nets", "response": "V"}},
	  "schemas": {"V": {"closed": false, "properties": {"ok": {"type": "boolean"}}}}
	}`)
	pool := newPool()
	step := Step{Operation: "CreateNet", Contract: "CreateNet", Kind: kindCreate, Makes: "net"}
	pool.harvest(doc, step, map[string]any{
		"RequestId": "req-1",
		"AccountId": "acc-1",
		"NetId":     "net-1",
	})
	if got, ok := pool.takeAny("ResourceId"); !ok || got != "net-1" {
		t.Errorf("takeAny must answer with infrastructure, never a tenant or a request: %v", got)
	}
}

// TestTheHintedBranchOfAChoiceIsFilled: block/v1's CreateVolume declares
// from_empty and from_snapshot, optional both, "precisely one must be set" in
// prose only. The hint table names from_empty as the branch a fresh run can
// always satisfy; without the fill the create is a guaranteed refusal and the
// whole block family stays unreachable — the /falsify hook for the hint
// family.
func TestTheHintedBranchOfAChoiceIsFilled(t *testing.T) {
	doc := docOf(t, `{
	  "provider": "stub",
	  "operations": {"CreateVolume": {"method": "POST", "path": "/volumes", "request": "Req", "response": "V"}},
	  "schemas": {
	    "Req": {"closed": true, "required": ["name"], "properties": {
	      "name": {"type": "string"},
	      "from_empty": {"ref": "FromEmpty"},
	      "from_snapshot": {"ref": "FromSnapshot"}
	    }},
	    "FromEmpty":    {"closed": true, "required": ["size"], "properties": {"size": {"type": "integer"}}},
	    "FromSnapshot": {"closed": true, "required": ["snapshot_id"], "properties": {"snapshot_id": {"type": "string"}}},
	    "V": {"closed": false, "properties": {"ok": {"type": "boolean"}}}
	  }
	}`)
	body, err := minimalBody(doc, Step{Kind: kindCreate}, "Req", newPool())
	if err != nil {
		t.Fatal(err)
	}
	from, ok := body["from_empty"].(map[string]any)
	if !ok || from["size"] != 1 {
		t.Errorf("the hinted branch must be filled from its own schema, got %v", body["from_empty"])
	}
	if _, present := body["from_snapshot"]; present {
		t.Errorf("the other branch stays absent — precisely one must be set: %v", body)
	}
}
