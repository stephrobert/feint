package probe_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/probe"
)

// The seeding half of the probe (#163): before an operation is probed, the run
// brings into being what that operation needs — from the contract's own request
// schema and from what its own earlier calls returned. These tests drive the
// whole Runner against small hand-built servers, because the property under
// test is end to end: a Get must end up exercised against an identifier that
// exists, and fall back to a refusal when its seed is taken away. Removing a
// seeding rule must turn the matching test red — that is the /falsify criterion
// the issue states.

// seedDoc declares a little cloud with a dependency in it: a snapshot is cut
// from a volume, and both are readable by id.
const seedDoc = `{
  "provider": "stub",
  "errorSchema": "StubError",
  "operations": {
    "CreateVolume":   {"method": "POST", "path": "/volumes", "request": "CreateVolumeRequest", "response": "VolumeView"},
    "CreateSnapshot": {"method": "POST", "path": "/snapshots", "request": "CreateSnapshotRequest", "response": "SnapshotView"},
    "GetVolume":      {"method": "GET", "path": "/volumes/{volume_id}", "response": "VolumeView"},
    "GetSnapshot":    {"method": "GET", "path": "/snapshots/{snapshot_id}", "response": "SnapshotView"}
  },
  "schemas": {
    "CreateVolumeRequest":   {"closed": true, "properties": {"name": {"type": "string"}}},
    "CreateSnapshotRequest": {"closed": true, "properties": {"volume_id": {"type": "string"}}},
    "VolumeView":   {"closed": true, "properties": {"volume": {"ref": "Volume"}}},
    "SnapshotView": {"closed": true, "properties": {"snapshot": {"ref": "Snapshot"}}},
    "Volume":   {"closed": true, "properties": {"id": {"type": "string"}}},
    "Snapshot": {"closed": true, "properties": {"id": {"type": "string"}}},
    "StubError": {"closed": false, "required": ["message"], "properties": {"message": {"type": "string"}}}
  }
}`

// seedServer emulates that little cloud with the strictness the real packs
// have: a snapshot of no volume is refused, a read of an absent id is a 404 in
// the declared error shape.
func seedServer() http.Handler {
	var volumeID, snapshotID string
	refuse := func(w http.ResponseWriter, status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": msg})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /volumes", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name == "" {
			refuse(w, 400, "name is required")
			return
		}
		volumeID = "vol-1"
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"volume": map[string]any{"id": volumeID}})
	})
	mux.HandleFunc("POST /snapshots", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			VolumeID string `json:"volume_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.VolumeID == "" || req.VolumeID != volumeID {
			refuse(w, 400, "volume_id is required and must exist")
			return
		}
		snapshotID = "snap-1"
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"snapshot": map[string]any{"id": snapshotID}})
	})
	mux.HandleFunc("GET /volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != volumeID || volumeID == "" {
			refuse(w, 404, "no such volume")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"volume": map[string]any{"id": volumeID}})
	})
	mux.HandleFunc("GET /snapshots/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != snapshotID || snapshotID == "" {
			refuse(w, 404, "no such snapshot")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"snapshot": map[string]any{"id": snapshotID}})
	})
	return mux
}

var seedRoutes = []contract.MountedRoute{
	{Method: "POST", Path: "/volumes", Operation: "stub/v1/API.CreateVolume"},
	{Method: "POST", Path: "/snapshots", Operation: "stub/v1/API.CreateSnapshot"},
	{Method: "GET", Path: "/volumes/{volume_id}", Operation: "stub/v1/API.GetVolume"},
	{Method: "GET", Path: "/snapshots/{snapshot_id}", Operation: "stub/v1/API.GetSnapshot"},
}

func runSeeded(t *testing.T, docJSON string, handler http.Handler, routes []contract.MountedRoute) probe.Report {
	t.Helper()
	doc, err := contract.Read(strings.NewReader(docJSON))
	if err != nil {
		t.Fatalf("read the stub contract: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	runner := &probe.Runner{Doc: doc, Base: ts.URL, Client: ts.Client()}
	report, err := runner.Run(context.Background(), routes)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return report
}

func lastResult(report probe.Report, operation string) probe.Result {
	var out probe.Result
	for _, res := range report.Results {
		if res.Operation == operation {
			out = res
		}
	}
	return out
}

// TestAGetIsProbedAgainstAnIdentifierThatExists is the issue in one test: the
// probe seeds a volume, then a snapshot of it, and both Gets validate their
// success shape instead of the guaranteed 404 an empty account answers. The
// snapshot's create itself only succeeds because the seeding filled its
// optional volume_id with the id the volume's create returned — no identifier
// is invented anywhere.
func TestAGetIsProbedAgainstAnIdentifierThatExists(t *testing.T) {
	report := runSeeded(t, seedDoc, seedServer(), seedRoutes)
	if failures := report.Failures(); len(failures) > 0 {
		t.Fatalf("nothing here disagrees with the contract: %v", failures)
	}
	for _, op := range []string{
		"stub/v1/API.CreateVolume", "stub/v1/API.CreateSnapshot",
		"stub/v1/API.GetVolume", "stub/v1/API.GetSnapshot",
	} {
		res := lastResult(report, op)
		if res.Refused || res.Skipped != "" || res.Status >= 300 {
			t.Errorf("%s must reach its success shape with the seeds in place, got %+v", op, res)
		}
	}
}

// TestASeededCreateFillsWhatTheRunHolds pins the fill rule itself: volume_id
// is optional in the schema, so before the seeding the snapshot's create sent
// an empty body and could only be refused. Take the volume producer out of the
// mounted routes — the family's seed — and the snapshot must fall back to that
// refusal: a promotion that survives the removal of its seed is not a
// promotion (#163's /falsify criterion, run here on every `go test`).
func TestASeededCreateFillsWhatTheRunHolds(t *testing.T) {
	report := runSeeded(t, seedDoc, seedServer(), seedRoutes)
	if res := lastResult(report, "stub/v1/API.CreateSnapshot"); res.Refused || res.Status != 201 {
		t.Fatalf("with a volume seeded, the snapshot create must succeed: %+v", res)
	}

	unseeded := make([]contract.MountedRoute, 0, len(seedRoutes)-1)
	for _, r := range seedRoutes {
		if r.Operation != "stub/v1/API.CreateVolume" {
			unseeded = append(unseeded, r)
		}
	}
	report = runSeeded(t, seedDoc, seedServer(), unseeded)
	if res := lastResult(report, "stub/v1/API.CreateSnapshot"); !res.Refused {
		t.Fatalf("without its seed the snapshot create must fall back to a refusal, got %+v", res)
	}
	if res := lastResult(report, "stub/v1/API.GetSnapshot"); !res.Refused && res.Skipped == "" {
		t.Fatalf("without the chain's root nothing downstream may pass: %+v", res)
	}
}

// TestAPathParameterIsAnsweredByItsOwnKind pins the typed lookup: the wrong-
// type identifiers were where the 404 refusals of #163 came from — {server_id}
// answered by an organisation id. Here the server holds a volume and a
// snapshot with distinct ids and refuses a read of the wrong one; only a typed
// pool can pass both reads in one run.
func TestAPathParameterIsAnsweredByItsOwnKind(t *testing.T) {
	report := runSeeded(t, seedDoc, seedServer(), seedRoutes)
	for _, op := range []string{"stub/v1/API.GetVolume", "stub/v1/API.GetSnapshot"} {
		if res := lastResult(report, op); res.Refused || res.Status != 200 {
			t.Errorf("%s got %+v: a typed pool answers each parameter with its own kind", op, res)
		}
	}
}

// cycleDoc declares the knot Outscale really has: an image is made from a
// machine, a machine is made from an image. Neither create can be ordered
// after the other.
const cycleDoc = `{
  "provider": "stub",
  "errorSchema": "StubError",
  "operations": {
    "CreateImage": {"method": "POST", "path": "/images", "request": "CreateImageRequest", "response": "ImageView"},
    "CreateVm":    {"method": "POST", "path": "/vms", "request": "CreateVmRequest", "response": "VmView"}
  },
  "schemas": {
    "CreateImageRequest": {"closed": true, "properties": {"vm_id": {"type": "string"}}},
    "CreateVmRequest":    {"closed": true, "properties": {"image_id": {"type": "string"}}},
    "ImageView": {"closed": true, "properties": {"image": {"ref": "Image"}}},
    "VmView":    {"closed": true, "properties": {"vm": {"ref": "Vm"}}},
    "Image": {"closed": true, "properties": {"id": {"type": "string"}}},
    "Vm":    {"closed": true, "properties": {"id": {"type": "string"}}},
    "StubError": {"closed": false, "required": ["message"], "properties": {"message": {"type": "string"}}}
  }
}`

// TestAnUnorderableCreateIsRetriedOnce: the cycle forces one of the two to run
// first and fail — the machine can boot without an image here, the image
// cannot exist without a machine — and the retry pass, run after the create
// phase with the pool full, is what turns that planned failure into the
// success the operation can actually produce. Without the retry, CreateImage
// stays a refusal forever, by construction.
func TestAnUnorderableCreateIsRetriedOnce(t *testing.T) {
	var vmID string
	refuse := func(w http.ResponseWriter, msg string) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": msg})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /images", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			VMID string `json:"vm_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.VMID == "" || req.VMID != vmID {
			refuse(w, "an image is cut from a machine")
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"image": map[string]any{"id": "img-1"}})
	})
	mux.HandleFunc("POST /vms", func(w http.ResponseWriter, _ *http.Request) {
		vmID = "vm-1"
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"vm": map[string]any{"id": vmID}})
	})

	routes := []contract.MountedRoute{
		{Method: "POST", Path: "/images", Operation: "stub/v1/API.CreateImage"},
		{Method: "POST", Path: "/vms", Operation: "stub/v1/API.CreateVm"},
	}
	report := runSeeded(t, cycleDoc, mux, routes)
	if res := lastResult(report, "stub/v1/API.CreateImage"); res.Refused || res.Status != 201 {
		t.Fatalf("the retry pass must give the cycle's loser its second chance: %+v", res)
	}
}

// vocabDoc declares one operation whose only obstacle is a format: the schema
// says string, the server validates an OpenSSH public key — exactly what all
// three real packs do.
const vocabDoc = `{
  "provider": "stub",
  "errorSchema": "StubError",
  "operations": {
    "CreateKey": {"method": "POST", "path": "/keys", "request": "CreateKeyRequest", "response": "KeyView"}
  },
  "schemas": {
    "CreateKeyRequest": {"closed": true, "required": ["public_key"], "properties": {"public_key": {"type": "string"}}},
    "KeyView": {"closed": true, "properties": {"key": {"ref": "Key"}}},
    "Key": {"closed": true, "properties": {"id": {"type": "string"}}},
    "StubError": {"closed": false, "required": ["message"], "properties": {"message": {"type": "string"}}}
  }
}`

// TestTheVocabularySeedsWhatAFormatFieldNeeds: a public_key filled with the
// generic probe string is a guaranteed refusal on every provider, and the
// vocabulary's fixed sample key is what turns the operation probeable. Remove
// the vocabulary entry and this fails — the /falsify hook for the format
// family.
func TestTheVocabularySeedsWhatAFormatFieldNeeds(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /keys", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PublicKey string `json:"public_key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !strings.HasPrefix(req.PublicKey, "ssh-ed25519 ") || len(strings.Fields(req.PublicKey)) < 2 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "not an OpenSSH public key"})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"key": map[string]any{"id": "key-1"}})
	})
	routes := []contract.MountedRoute{{Method: "POST", Path: "/keys", Operation: "stub/v1/API.CreateKey"}}
	report := runSeeded(t, vocabDoc, mux, routes)
	if res := lastResult(report, "stub/v1/API.CreateKey"); res.Refused || res.Status != 201 {
		t.Fatalf("the sample key must satisfy an OpenSSH validation: %+v", res)
	}
}
