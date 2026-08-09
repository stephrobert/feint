package outscale

import (
	"net/http"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Snapshots, as control-plane records.
//
// An emulated volume holds no bytes, so a snapshot of one copies nothing — and
// that is not the lie it sounds like: restoring an empty volume's snapshot
// produces exactly what the volume held. What the control plane can be honest
// about is the record: which volume, what size, when, and the immediate
// "completed" state that every lifecycle here has (transitions are immediate by
// design; a Progress below 100 would make a client poll for information that
// does not exist). docs/limits.md carries the no-bytes caveat.
//
// This lifts the old refusal in createVolume: a volume may now be created from
// a snapshot the emulator knows, and inherits its size. A SnapshotId nobody
// created is still refused — the real API refuses an unknown one too, and
// accepting it would mint a provenance out of thin air.
//
// Shape measured (X-2 sweep, 2026-08-08): State "completed", Progress 100,
// PermissionsToCreateVolume {AccountIds:[], GlobalPermission:false} on every
// snapshot of the account. UpdateSnapshot manages those cross-account
// permissions and is declined with the IAM family: one implicit account,
// nothing to grant.

const kindSnapshot = "snapshot"

type createSnapshotRequest struct {
	VolumeID    string `json:"VolumeId"`
	Description string `json:"Description"`
	ClientToken string `json:"ClientToken"`
	DryRun      *bool  `json:"DryRun"`
}

func (p *Pack) createSnapshot(w http.ResponseWriter, r *http.Request) {
	var req createSnapshotRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.VolumeID == "" {
		// FileLocation and SourceSnapshotId name the import and copy variants,
		// which read from Object Storage and another region respectively:
		// neither exists here, so the one served variant is the one that names a
		// volume.
		p.badRequest(w, "VolumeId is required; importing or copying a snapshot is not emulated")
		return
	}
	volume, found := p.env.Store.Get(Name, kindVolume, req.VolumeID)
	if !found {
		p.notFound(w, "volume", req.VolumeID)
		return
	}
	size, _ := volume.Attrs["Size"].(int)

	now := p.env.Now()
	res := &resource.Resource{
		ID:      newID("snap", p.env.NewID()),
		Kind:    kindSnapshot,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "completed",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"VolumeId":    req.VolumeID,
			"VolumeSize":  size,
			"Description": req.Description,
			"Progress":    100,
			"PermissionsToCreateVolume": map[string]any{
				"AccountIds":       []any{},
				"GlobalPermission": false,
			},
			"Tags": []any{},
		},
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Snapshot":        snapshotView(res),
		"ResponseContext": p.context(),
	})
}

// snapshotFilters: the same lesson as volumes — a client filters on what it
// knows, and a filter refused is an apply that stops.
var snapshotFilters = []string{
	"SnapshotIds", "VolumeIds", "States", "Descriptions",
	"AccountIds", "Progresses", "VolumeSizes",
}

func (p *Pack) readSnapshots(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, snapshotFilters...) {
		return
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindSnapshot, resource.Tenant{Provider: Name}) {
		if !matchesStrings(req.Filters, "SnapshotIds", res.ID) ||
			!matchesStrings(req.Filters, "VolumeIds", stringOf(res.Attrs["VolumeId"])) ||
			!matchesStrings(req.Filters, "States", res.State) ||
			!matchesStrings(req.Filters, "Descriptions", stringOf(res.Attrs["Description"])) {
			continue
		}
		out = append(out, snapshotView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Snapshots":       page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SnapshotID string `json:"SnapshotId"`
		DryRun     *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if _, found := p.env.Store.Get(Name, kindSnapshot, req.SnapshotID); !found {
		p.notFound(w, "snapshot", req.SnapshotID)
		return
	}
	// A volume created from this snapshot keeps its SnapshotId, exactly as the
	// real API keeps it: provenance is history, not a live reference.
	p.env.Store.Delete(Name, kindSnapshot, req.SnapshotID)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func snapshotView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+4)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["SnapshotId"] = res.ID
	out["State"] = res.State
	out["AccountId"] = accountID
	out["CreationDate"] = res.Created.Format(time.RFC3339)
	return out
}
