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
	res := resource.New(newID("snap", p.env.NewID()), kindSnapshot, resource.Tenant{Provider: Name}, "completed", now)
	res.Attrs = map[string]any{
		"VolumeId":    req.VolumeID,
		"VolumeSize":  size,
		"Description": req.Description,
		"Progress":    100,
		"PermissionsToCreateVolume": map[string]any{
			"AccountIds":       []any{},
			"GlobalPermission": false,
		},
		"Tags": []any{},
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
		ResultsPerPage *int      `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refusePageSize(w, req.ResultsPerPage) {
		return
	}
	if p.refuseUnsupported(w, req.Filters, snapshotFilters...) {
		return
	}

	// The catalogue first and the client's own after, exactly as readImages
	// orders the two halves it answers: an image names one of these, so an
	// image a client can read must not name a snapshot it cannot (#389).
	out := make([]map[string]any, 0, len(catalogueSnapshotViews))
	for _, view := range catalogueSnapshotViews {
		if snapshotMatches(view, req.Filters) {
			out = append(out, view)
		}
	}
	for _, res := range p.env.Store.List(kindSnapshot, resource.Tenant{Provider: Name}) {
		if view := snapshotView(res); snapshotMatches(view, req.Filters) {
			out = append(out, view)
		}
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Snapshots":       page(out, pageSize(req.ResultsPerPage)),
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
		// A catalogue snapshot is not the client's to destroy, the way a
		// catalogue image is not — and here it would also strand every image
		// that names it, which is the dangling identifier #389 removed.
		// TestACatalogueSnapshotRefusesItsDelete fails without this.
		if isCatalogueSnapshot(req.SnapshotID) {
			p.conflict(w, "the snapshot "+req.SnapshotID+" backs the emulated catalogue and cannot be deleted")
			return
		}
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

// snapshotMatches filters a rendered snapshot, so a catalogue entry and one a
// client cut are filtered by exactly the same rules — the reason imageMatches
// exists on the other half of the same pair.
func snapshotMatches(view map[string]any, f filterSet) bool {
	return matchesStrings(f, "SnapshotIds", stringOf(view["SnapshotId"])) &&
		matchesStrings(f, "VolumeIds", stringOf(view["VolumeId"])) &&
		matchesStrings(f, "States", stringOf(view["State"])) &&
		matchesStrings(f, "Descriptions", stringOf(view["Description"]))
}

// findSnapshot resolves a SnapshotId to what it holds, whichever half of the
// answer it comes from. Every path that turns a SnapshotId into a size goes
// through it — createVolume, createImage and the root volume CreateVms cuts —
// because a client that can read a snapshot must be able to use it, and three
// separate Store.Get calls is three places for one of them to forget the
// catalogue.
//
// TestAVolumeIsCutFromACatalogueSnapshot fails without this.
func (p *Pack) findSnapshot(id string) (map[string]any, bool) {
	if res, found := p.env.Store.Get(Name, kindSnapshot, id); found {
		return snapshotView(res), true
	}
	for _, view := range catalogueSnapshotViews {
		if view["SnapshotId"] == id {
			return view, true
		}
	}
	return nil, false
}

// snapshotSize is a snapshot's VolumeSize, whatever numeric type it was stored
// as. The catalogue holds ints and a client's snapshot copies the volume's, so
// reading it with a single type assertion returned zero for one of the two.
func snapshotSize(view map[string]any) int { return int(numOf(view["VolumeSize"])) }
