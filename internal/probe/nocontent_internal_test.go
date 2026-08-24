package probe

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
)

// The planner used to skip every operation with no response schema, on the
// reading that there was nothing to hold the answer to. For Scaleway that read
// `204: {description: ''}` — the provider stating what its DELETEs answer — as
// silence, and skipped 31 of the 173 operations that pack serves. They were
// every one of its zeros on the probed axis (#429).

const noContentPlanDoc = `{
  "provider": "stub",
  "operations": {
    "CreateVolume": {"method": "POST",   "path": "/volumes", "response": "V"},
    "DeleteVolume": {"method": "DELETE", "path": "/volumes/{volume_id}", "noContent": 204},
    "MuteVolume":   {"method": "DELETE", "path": "/volumes/{volume_id}/mute"}
  },
  "schemas": {"V": {"closed": false, "properties": {"id": {"type": "string"}}}}
}`

var noContentRoutes = []contract.MountedRoute{
	{Method: "POST", Path: "/volumes", Operation: "stub/v1/API.CreateVolume"},
	{Method: "DELETE", Path: "/volumes/{volume_id}", Operation: "stub/v1/API.DeleteVolume"},
	{Method: "DELETE", Path: "/volumes/{volume_id}/mute", Operation: "stub/v1/API.MuteVolume"},
}

func skipsOf(t *testing.T) map[string]string {
	t.Helper()
	doc, err := contract.Read(strings.NewReader(noContentPlanDoc))
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	plan, err := Plan(doc, noContentRoutes)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	out := map[string]string{}
	for _, s := range plan {
		out[s.Operation] = s.Skip
	}
	return out
}

func TestAnOperationDeclaredWithNoBodyIsStillProbed(t *testing.T) {
	if skip := skipsOf(t)["stub/v1/API.DeleteVolume"]; skip != "" {
		t.Errorf("an operation whose document declares 204 with no content is checkable, "+
			"and must not be skipped: %q", skip)
	}
}

// The witness for the half that must not move: MuteVolume's document says
// nothing about its answer, so calling it would prove only that it answered.
// Without this case the test above passes on a planner that stopped skipping
// anything at all.
func TestAnOperationWhoseDocumentSaysNothingIsStillSkipped(t *testing.T) {
	if skip := skipsOf(t)["stub/v1/API.MuteVolume"]; skip == "" {
		t.Error("an operation with neither a response schema nor a declared empty answer " +
			"must still be skipped: nothing could hold its answer to anything")
	}
}
