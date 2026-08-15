package emulator_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
)

// The omission check (#88): the contract gate sees what an answer invents,
// this sees what it forgets. Every test here is one rule that keeps the check
// honest — the finding must be real (an omitted declared field, its container
// served), and the silence must be real too (an empty list, an absent
// container, a field a pack declines with a reason).

const omissionsContract = `{
  "provider": "stub",
  "operations": {
    "ListThings": {"method": "GET", "path": "/stub/things", "response": "ListView"},
    "GetOther": {"method": "GET", "path": "/stub/other", "response": "OtherView"}
  },
  "schemas": {
    "ListView": {"closed": true, "properties": {
      "things": {"type": "array", "items": {"ref": "Thing"}}
    }},
    "Thing": {"closed": true, "properties": {
      "id": {"type": "string"},
      "name": {"type": "string"},
      "image": {"ref": "Image"}
    }},
    "Image": {"closed": true, "properties": {"id": {"type": "string"}, "arch": {"type": "string"}}},
    "OtherView": {"closed": true, "properties": {"ok": {"type": "boolean"}}}
  }
}`

// declStubPack is a stubPack that also declines fields, the way a real pack
// does through emulator.FieldDecliner.
type declStubPack struct {
	stubPack
	declines []emulator.FieldDecline
}

func (p declStubPack) DeclinedFields() []emulator.FieldDecline { return p.declines }

// realThings is what the "real cloud" was recorded to answer on ListThings —
// the corroboration the failing verdict requires. Tests that want a field to
// be able to fail hand this in; the one test about the uncorroborated case
// hands nothing.
var realThings = map[string][]string{
	"stub/v1/API.ListThings": {
		"things", "things[]", "things[].id", "things[].name",
		"things[].image", "things[].image.id", "things[].image.arch",
	},
}

func omissionsServer(t *testing.T, things http.HandlerFunc, declines []emulator.FieldDecline, observed map[string][]string) *httptest.Server {
	t.Helper()
	doc, err := contract.Read(strings.NewReader(omissionsContract))
	if err != nil {
		t.Fatalf("read the stub contract: %v", err)
	}
	env := emulator.DefaultEnv()
	env.Contracts = map[string]*contract.Doc{"stub": doc}

	pack := declStubPack{
		stubPack: stubPack{name: "stub", routes: []emulator.Route{
			{Method: "GET", Path: "/stub/things", Operation: "stub/v1/API.ListThings", Handler: things},
			{Method: "GET", Path: "/stub/other", Operation: "stub/v1/API.GetOther",
				Handler: answer(http.StatusOK, "application/json", `{"ok": true}`)},
		}},
		declines: declines,
	}
	srv, err := emulator.NewServer(env, pack)
	if err != nil {
		t.Fatalf("mount the stub pack: %v", err)
	}
	if observed != nil {
		srv.SetObservedFields(observed)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestAnOmittedDeclaredFieldIsPublished is the test the check exists for, and
// the falsification target: an answer whose element omits one field the
// document declares — while the contract gate stays green, because nothing
// marks the field required — must appear under fields.missing.
func TestAnOmittedDeclaredFieldIsPublished(t *testing.T) {
	ts := omissionsServer(t,
		answer(http.StatusOK, "application/json",
			`{"things": [{"id": "thing-1", "image": {"id": "img-1", "arch": "x86_64"}}]}`),
		nil, realThings)

	driveRoute(t, ts, "/stub/things", false)

	view := viewOf(t, ts)
	missing := view.Fields.Missing["stub/v1/API.ListThings"]
	if len(missing) != 1 || missing[0] != "things[].name: string" {
		t.Errorf("the omitted declared field must be the one finding, got %v", missing)
	}
	// The two gates measure different things, and this is the proof: the same
	// answer is clean for the contract axis, because `name` is not required.
	if len(view.Violations["stub/v1/API.ListThings"]) != 0 {
		t.Errorf("the contract gate has nothing to say here, got %v",
			view.Violations["stub/v1/API.ListThings"])
	}
}

func TestACompleteAnswerReportsNothing(t *testing.T) {
	ts := omissionsServer(t,
		answer(http.StatusOK, "application/json",
			`{"things": [{"id": "t", "name": "n", "image": {"id": "i", "arch": "arm64"}}]}`),
		nil, realThings)

	driveRoute(t, ts, "/stub/things", false)

	view := viewOf(t, ts)
	if len(view.Fields.Missing) != 0 {
		t.Errorf("a complete answer must report nothing, got %v", view.Fields.Missing)
	}
	if len(view.Fields.Compared) != 1 || view.Fields.Compared[0] != "stub/v1/API.ListThings" {
		t.Errorf("the comparison still counts as done, got %v", view.Fields.Compared)
	}
}

// TestAnEmptyListIsNotAnOmission: with no element in the store, no element
// field can be observed, and reporting them would make the gate cry wolf on
// every fresh emulator — the same rule the shapes gate learned on its first
// run.
func TestAnEmptyListIsNotAnOmission(t *testing.T) {
	ts := omissionsServer(t,
		answer(http.StatusOK, "application/json", `{"things": []}`),
		nil, realThings)

	driveRoute(t, ts, "/stub/things", false)

	if missing := viewOf(t, ts).Fields.Missing; len(missing) != 0 {
		t.Errorf("an empty list is the store's state, not the view's defect: %v", missing)
	}
}

// TestAMissingContainerIsOneOmissionNotMany: an element that omits its whole
// image object is one finding at the container's level, not three.
func TestAMissingContainerIsOneOmissionNotMany(t *testing.T) {
	ts := omissionsServer(t,
		answer(http.StatusOK, "application/json",
			`{"things": [{"id": "t", "name": "n"}]}`),
		nil, realThings)

	driveRoute(t, ts, "/stub/things", false)

	missing := viewOf(t, ts).Fields.Missing["stub/v1/API.ListThings"]
	if len(missing) != 1 || missing[0] != "things[].image: object" {
		t.Errorf("one absent container is one finding, got %v", missing)
	}
}

// TestANullContainerDoesNotAccuseItsChildren: the real clouds answer null for
// an unset object — a real account's images carry `default_bootscript: null` —
// and an emulator doing the same must not be accused of omitting every field
// the null would have carried. The first run of this check reported all twelve
// children of that very field before this rule existed.
func TestANullContainerDoesNotAccuseItsChildren(t *testing.T) {
	ts := omissionsServer(t,
		answer(http.StatusOK, "application/json",
			`{"things": [{"id": "t", "name": "n", "image": null}]}`),
		nil, realThings)

	driveRoute(t, ts, "/stub/things", false)

	if missing := viewOf(t, ts).Fields.Missing; len(missing) != 0 {
		t.Errorf("a null container has no observable children, got %v", missing)
	}
}

// TestAFieldServedInAnyAnswerIsNotAnOmission: the union of the run counts. A
// field present while the resource was in the state that carries it must not
// be reported because a later answer, in another state, omitted it.
func TestAFieldServedInAnyAnswerIsNotAnOmission(t *testing.T) {
	bodies := []string{
		`{"things": [{"id": "t", "name": "n", "image": {"id": "i", "arch": "a"}}]}`,
		`{"things": [{"id": "t"}]}`,
	}
	call := 0
	ts := omissionsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodies[call]))
		if call < len(bodies)-1 {
			call++
		}
	}, nil, realThings)

	driveRoute(t, ts, "/stub/things", false)
	driveRoute(t, ts, "/stub/things", false)

	if missing := viewOf(t, ts).Fields.Missing; len(missing) != 0 {
		t.Errorf("a field the run served once was served, got %v", missing)
	}
}

// TestADeclaredFieldNobodyObservedDoesNotFail: the document alone does not
// convict. Measured on the first instrumented run: of 106 declared-but-absent
// fields a recording could arbitrate, 83 were absent from the real cloud's
// answer too — pagination tokens, echoed client tokens. Without corroboration
// the absence is published as unconfirmed, never as missing.
func TestADeclaredFieldNobodyObservedDoesNotFail(t *testing.T) {
	ts := omissionsServer(t,
		answer(http.StatusOK, "application/json",
			`{"things": [{"id": "thing-1", "image": {"id": "img-1", "arch": "x86_64"}}]}`),
		nil, nil) // no recording handed in

	driveRoute(t, ts, "/stub/things", false)

	view := viewOf(t, ts)
	if len(view.Fields.Missing) != 0 {
		t.Errorf("no source but the document vouches for the field; it must not fail: %v",
			view.Fields.Missing)
	}
	unconfirmed := view.Fields.Unconfirmed["stub/v1/API.ListThings"]
	if len(unconfirmed) != 1 || unconfirmed[0] != "things[].name: string" {
		t.Errorf("the absence must stay visible as unconfirmed, got %v", unconfirmed)
	}
}

// TestADeclinedFieldIsExcusedWithItsReason: a pack that argues an absence is a
// decision moves the finding to the excused list, reason attached — the gate
// prints it and does not fail on it.
func TestADeclinedFieldIsExcusedWithItsReason(t *testing.T) {
	ts := omissionsServer(t,
		answer(http.StatusOK, "application/json",
			`{"things": [{"id": "t", "image": {"id": "i", "arch": "a"}}]}`),
		[]emulator.FieldDecline{{
			Operation: "stub/v1/API.ListThings",
			Path:      "things[].name",
			Reason:    "the emulated inventory has no display names, and inventing one would be a format nothing upstream states",
		}}, realThings)

	driveRoute(t, ts, "/stub/things", false)

	view := viewOf(t, ts)
	if len(view.Fields.Missing) != 0 {
		t.Errorf("an argued absence is not a finding, got %v", view.Fields.Missing)
	}
	excused := view.Fields.Excused["stub/v1/API.ListThings"]
	if len(excused) != 1 || !strings.HasPrefix(excused[0], "things[].name: ") {
		t.Errorf("the excuse must be visible with its reason, got %v", excused)
	}
}

// TestAProvablyStaleFieldDeclineIsPublished: a decline arguing for an omission
// the emulator does not have — the run served the very field — is a decision
// that outlived its subject, and it is published for the gate to fail on.
// Provably only: an operation the run never compared stays silent, or the
// gate would flap with the traffic.
func TestAProvablyStaleFieldDeclineIsPublished(t *testing.T) {
	ts := omissionsServer(t,
		answer(http.StatusOK, "application/json",
			`{"things": [{"id": "t", "name": "n", "image": {"id": "i", "arch": "a"}}]}`),
		[]emulator.FieldDecline{{
			Operation: "stub/v1/API.ListThings",
			Path:      "things[].name",
			Reason:    "the emulated inventory has no display names, and inventing one would be a format nothing upstream states",
		}}, realThings)

	driveRoute(t, ts, "/stub/things", false)

	stale := viewOf(t, ts).Fields.StaleDeclines
	if len(stale) != 1 || !strings.HasPrefix(stale[0], "stub/v1/API.ListThings things[].name") {
		t.Errorf("a decline for a served field is stale and must say so, got %v", stale)
	}
}

// TestAnUncomparedOperationIsNotAccused: no decoded 2xx answer this run means
// nothing was compared — the operation stays off Compared and out of Missing,
// because "unchecked" and "complete" are different answers.
func TestAnUncomparedOperationIsNotAccused(t *testing.T) {
	ts := omissionsServer(t,
		answer(http.StatusOK, "application/json", `{"things": []}`),
		nil, realThings)

	driveRoute(t, ts, "/stub/other", false)

	view := viewOf(t, ts)
	if len(view.Fields.Compared) != 1 || view.Fields.Compared[0] != "stub/v1/API.GetOther" {
		t.Errorf("only the driven operation was compared, got %v", view.Fields.Compared)
	}
	if len(view.Fields.Missing) != 0 {
		t.Errorf("an operation nobody drove has nothing missing, got %v", view.Fields.Missing)
	}
}

// A synthetic answer does not vouch for a served field, and CI is what proved
// it needed saying.
//
// The probe leg of conformance.yml drives no client at all, so every object in
// it is the minimal one the probe's own seeding builds: no address linked, no
// tags, no user data. The gate accused ReadVms of omitting PublicIp, Tags and
// UserData — fields that exist only on a machine a user configured, absent for
// a reason belonging to the run rather than to the emulator.
//
// Both halves are asserted, because a guard that swallowed both would silence
// the check this file exists for: the same incomplete answer accuses nobody
// when only the probe asked for it, and is a finding the moment a client does.
func TestASyntheticAnswerDoesNotVouchForAServedField(t *testing.T) {
	incomplete := answer(http.StatusOK, "application/json",
		`{"things": [{"id": "thing-1", "image": {"id": "img-1", "arch": "x86_64"}}]}`)

	ts := omissionsServer(t, incomplete, nil, realThings)
	driveRoute(t, ts, "/stub/things", true)
	if missing := viewOf(t, ts).Fields.Missing["stub/v1/API.ListThings"]; len(missing) != 0 {
		t.Errorf("a synthetic answer must not be held to the document's field list, got %v", missing)
	}

	// The accepting half, on a second server so the first cannot have recorded
	// anything for it: the identical answer from a real client is the finding.
	real := omissionsServer(t, incomplete, nil, realThings)
	driveRoute(t, real, "/stub/things", false)
	missing := viewOf(t, real).Fields.Missing["stub/v1/API.ListThings"]
	if len(missing) != 1 || missing[0] != "things[].name: string" {
		t.Errorf("the same omission from a client is what the gate exists for, got %v", missing)
	}
}
