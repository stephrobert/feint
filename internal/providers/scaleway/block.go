package scaleway

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// errBlockVolumeShrinks is the shrink refusal, answered from inside the store
// lock where the size it judges cannot move (#295).
var errBlockVolumeShrinks = errors.New("a volume grows and does not shrink")

// Block Storage (block/v1), Scaleway's SBS product.
//
// This is not a second volume product bolted beside the first: it is where the
// Terraform provider ends up whenever a volume is not an instance one. The
// provider reads every volume through GetUnknownVolume — instance.GetVolume
// first, and on a typed 404 a fallback to block.GetVolume. With block/v1
// unmounted that fallback met an unserved route, and an apply carrying
// `root_volume { volume_type = "sbs_volume" }` died on "waiting for Volume
// failed: http error 404 Not Found". Measured and reported by @vde-dis on #8,
// who tried the change, watched it fail, and threw it away rather than guess.
//
// So SW-3 is a release as much as an addition. Before it, `root_volume` had no
// usable value at all: the provider refuses b_ssd outright from 2.79 on, and
// sbs_volume planned for ever. Omitting the block was the only way through,
// which is exactly what the conformance fixture did — a fixture that avoids the
// one input that breaks is a test that cannot fail, and the fixture now asks the
// question.
//
// The shapes come from a recording of a real account (shapes/scaleway.json,
// `GET /block/v1/zones/fr-par-1/volumes`), not from the SDK. That matters here
// more than usual: the recording carries `kms_key_id` and `last_detached_at`
// present and null, `specs.class` and `specs.perf_iops`, and a `references`
// array the SDK describes but never shows populated. `feint shapes --check`
// compares this pack against that recording on every pull request.
//
// The block snapshot shape is the one part still read from the SDK: the account
// recorded held no block snapshot, so shapes/scaleway.json has `snapshots: array`
// and nothing under it. Recording an account that has one is how that reading
// becomes a measurement.

const (
	kindBlockVolume   = "block/volume"
	kindBlockSnapshot = "block/snapshot"

	// The class every emulated block volume reports. "sbs" is what the SDK
	// declares for this product; "bssd" belongs to the older instance volumes,
	// which this pack serves elsewhere.
	blockStorageClass = "sbs"

	// The IO/s the catalogue offers. The SDK names 5000 and 15000 as the values
	// available in stock, and the emulated catalogue offers the first: a number
	// nothing measures, published because a client reads it back and compares.
	blockDefaultIOPS = 5000
)

// blockCreateStatus is what a successful block/v1alpha1 create answers with.
//
// 200, not the 201 a create writes by habit. Measured on the wire on
// 2026-08-24: `scw block volume create` and `scw block snapshot create` against
// a real fr-par account both answered 200, recorded through `feint proxy` into
// corpus/scaleway/scw-billed-shapes.jsonl, and `feint corpus --check` reported
// this pack's 201 against both (#427). Read off the transcript rather than off
// the CLI's exit code, which shows neither.
//
// The third product measured this way, after vpc/v2 (vpcCreateStatus) and
// ipam/v1 (ipamBookStatus). Each is claimed only for the product whose answer
// was seen: a status is part of the answer, and an invented one is an invented
// format (rule 4).
//
// TestTheBlockCreatesAnswerWhatTheRealCloudAnswers fails without it.
const blockCreateStatus = http.StatusOK

// ---- Volumes ---------------------------------------------------------------

type createBlockVolumeRequest struct {
	Name      string    `json:"name"`
	ProjectID *string   `json:"project_id"`
	Tags      *[]string `json:"tags"`
	PerfIops  *uint32   `json:"perf_iops"`
	// Exactly one of the two, which is how the API distinguishes a blank volume
	// from one restored from a snapshot.
	FromEmpty *struct {
		Size uint64 `json:"size"`
	} `json:"from_empty"`
	FromSnapshot *struct {
		Size       *uint64 `json:"size"`
		SnapshotID string  `json:"snapshot_id"`
	} `json:"from_snapshot"`
	// KmsKeyID names a key in Scaleway's Key Manager, which is not emulated. It
	// is refused rather than stored: accepting it would answer success about an
	// encryption nothing performs, and a client reading the field back would
	// conclude its volume is protected.
	KmsKeyID *string `json:"kms_key_id"`
}

// createBlockVolume records a volume, blank or restored from a block snapshot.
//
// "Precisely one of FromEmpty, FromSnapshot must be set" is the SDK's own
// wording, and it is enforced here: neither leaves the size undecidable, both
// leaves it ambiguous, and answering success either way would invent a number.
// TestABlockVolumeFromNeitherOrBothIsRefused fails without this.
func (p *Pack) createBlockVolume(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req createBlockVolumeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.KmsKeyID != nil && *req.KmsKeyID != "" {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "kms_key_id",
			Reason:       "constraint",
			HelpMessage:  "the key names Scaleway's Key Manager, which this emulator does not serve, and no volume here is encrypted",
		})
		return
	}
	if (req.FromEmpty == nil) == (req.FromSnapshot == nil) {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "from_empty",
			Reason:       "constraint",
			HelpMessage:  "precisely one of from_empty and from_snapshot must be set",
		})
		return
	}

	size := uint64(0)
	parent := ""
	switch {
	case req.FromEmpty != nil:
		size = req.FromEmpty.Size
	default:
		snapshot, found := p.env.Store.Get(Name, kindBlockSnapshot, req.FromSnapshot.SnapshotID)
		if !found {
			writeNotFound(w, "snapshot", req.FromSnapshot.SnapshotID)
			return
		}
		parent = snapshot.ID
		// The snapshot's size unless a resize was asked for, which is what the
		// SDK says the optional size on this branch is for.
		// Through the shared reader: a size written as a uint64 comes back a
		// float64 once the store has crossed a snapshot, so the assertion this
		// replaces answered 0 and a volume restored from a snapshot was created
		// with no size at all (#542).
		// TestASnapshotChainTakenAfterARestoreKeepsItsSize fails without this.
		size = resource.Uint64(snapshot, "size")
		if req.FromSnapshot.Size != nil && *req.FromSnapshot.Size > 0 {
			// Larger only. A restore into something smaller than the snapshot is
			// a volume the data cannot fit in, and answering 201 to it is the
			// half-success this project exists to avoid.
			//
			// The refusal existed on updateBlockVolume and only there, while its
			// comment read "a volume grows and does not shrink" — a property of
			// the volume, stated once and held on one path. An audit created a
			// 1 GB volume from a 10 GB snapshot and got 201. The guard is on both
			// paths now, and the comment says which.
			// TestABlockVolumeRestoredSmallerThanItsSnapshotIsRefused fails
			// without this.
			if *req.FromSnapshot.Size < size {
				writeInvalidArguments(w, ArgumentError{
					ArgumentName: "from_snapshot.size",
					Reason:       "constraint",
					HelpMessage:  "a volume restored from a snapshot may grow, not shrink below it",
				})
				return
			}
			size = *req.FromSnapshot.Size
		}
	}

	if p.refuseUnknownProject(w, textPtr(req.ProjectID), projectDeniedToBlock) {
		return
	}
	project, _ := projectOf(textPtr(req.ProjectID))
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindBlockVolume, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "available", now)
	res.Attrs = map[string]any{
		"name":               req.Name,
		"project":            project,
		"tags":               orEmpty(slicePtr(req.Tags)),
		"size":               size,
		"zone":               zone,
		"parent_snapshot_id": parent,
		"perf_iops":          iopsOf(req.PerfIops),
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, blockCreateStatus, p.blockVolumeView(res))
}

func iopsOf(asked *uint32) uint32 {
	if asked == nil || *asked == 0 {
		return blockDefaultIOPS
	}
	return *asked
}

// blockVolumeView renders block/v1's Volume.
//
// Field for field from the recording of a real account, which is why
// `kms_key_id` and `last_detached_at` are present and null rather than omitted:
// the SDK declares both as pointers, so reading it alone would have justified
// dropping them, and a client that reads a key back would find nothing where the
// real API puts an explicit null.
//
// The envelope is the object itself, with no wrapper. block/v1 answers the
// resource bare where instance/v1 wraps it in {"volume": ...} — two products of
// one cloud that do not agree, and copying the neighbouring product's habit here
// would have broken every decode.
func (p *Pack) blockVolumeView(res *resource.Resource) map[string]any {
	view := map[string]any{
		"id":         res.ID,
		"name":       textOf(res.Attrs["name"]),
		"type":       blockStorageClass,
		"size":       res.Attrs["size"],
		"project_id": textOf(res.Attrs["project"]),
		"created_at": res.Created.Format(time.RFC3339),
		"updated_at": res.Updated.Format(time.RFC3339),
		"references": p.referencesTo(res),
		"status":     res.State,
		"tags":       res.Attrs["tags"],
		"zone":       textOf(res.Attrs["zone"]),
		"specs": map[string]any{
			"class":     blockStorageClass,
			"perf_iops": res.Attrs["perf_iops"],
		},
		// Present and null on every volume the recorded account returned. Not
		// emulated: there is no Key Manager here.
		"kms_key_id": nil,
		// A timestamp once something has released this volume, null before —
		// which is what both recordings show, null while attached and a string
		// on the read that follows the detach. Written by detachStoredVolume,
		// the one place a volume stops being held.
		"last_detached_at": nil,
	}
	if detached := textOf(res.Attrs["last_detached_at"]); detached != "" {
		view["last_detached_at"] = detached
	}
	// A string when the volume came from a snapshot, null otherwise. The SDK
	// declares a pointer and the recorded account only had volumes with a parent,
	// so the null branch is the SDK's reading and the string branch is measured.
	if parent := textOf(res.Attrs["parent_snapshot_id"]); parent != "" {
		view["parent_snapshot_id"] = parent
	} else {
		view["parent_snapshot_id"] = nil
	}
	return view
}

// referencesTo computes what is attached to a volume, rather than storing it.
//
// The attachment already lives on the server, which is the side that owns it:
// `scw instance server attach-volume` writes there, and so does a create. Storing
// it a second time on the volume would give two places to keep in step, and this
// repository has paid for that shape before — the instance volume's `server`
// field is computed for the same reason.
//
// A block volume attached to a server therefore reports one reference of type
// "exclusive" and status "attached", which is what the real API returns for a
// root volume, and an unattached one reports an empty array rather than null:
// the recording shows an array, and a client ranging over null crashes.
// The attachment is read from the volume's own Runtime, which is where this pack
// already keeps it for instance volumes — not from the server's rendered
// `volumes` map, which is a view built for the response. Reading a view to
// compute another view puts a derivation between two derivations, and the first
// one to change silently breaks the second.
func (p *Pack) referencesTo(vol *resource.Resource) []map[string]any {
	out := make([]map[string]any, 0)
	serverID := vol.Runtime[runtimeServerKey]
	if serverID == "" {
		return out
	}
	// A server that is gone leaves no reference: the volume outlives the machine
	// on Scaleway, and reporting an attachment to a deleted server would refuse
	// a delete for ever.
	if _, alive := p.env.Store.Get(Name, kindServer, serverID); !alive {
		return out
	}
	out = append(out, map[string]any{
		// Derived from the pair it describes, so two reads of the same attachment
		// answer the same identifier. A fresh UUID here would give Terraform a
		// permanent diff on a field it stores.
		"id":                    referenceID(vol.ID, serverID),
		"product_resource_type": "instance_server",
		"product_resource_id":   serverID,
		"created_at":            vol.Created.Format(time.RFC3339),
		"type":                  "exclusive",
		"status":                "attached",
	})
	return out
}

// referenceID builds a stable identifier for an attachment from the pair it
// joins. Deterministic on purpose: anything Terraform stores must read back the
// same, and this is read on every plan.
func referenceID(volumeID, serverID string) string {
	// The UUID shape a client expects, built from halves of the two identifiers
	// it joins rather than from a counter, so it survives a snapshot reload.
	//
	// Guarded rather than assumed: docs/limits.md declares identifiers unchecked
	// on the way in, so a restored snapshot can carry a shorter one, and slicing
	// it blind would panic the whole process — every provider in it, not just
	// this route.
	const half = 18
	if len(volumeID) < half || len(serverID) < half {
		return volumeID
	}
	return volumeID[:half] + serverID[half:]
}

// listBlockVolumes serves every filter the two block ListVolumes operations
// declare — v1 and v1alpha1 share this handler, so the union of both contracts
// is what it must read (#277). Reading the page and nothing else is exactly the
// hole the per-operation gate names: `?order_by=name_desc` answered store
// order, `?project_id=other` answered every project's volumes.
func (p *Pack) listBlockVolumes(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	scope := resource.Tenant{Provider: Name}
	switch {
	case q.Get("project_id") != "":
		scope.Project = q.Get("project_id")
	case q.Get("organization_id") != "":
		// The whole account — one organization lives here (scopeOf's rule),
		// and comparing the identifier against the pack's constant would
		// deny a client its own volumes for a configuration detail.
	}
	all := p.env.Store.List(kindBlockVolume, scope)
	all = filterResources(all, func(res *resource.Resource) bool {
		return textOf(res.Attrs["zone"]) == zone
	})
	if name := q.Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	// "One or more matching tags": block is a newer product, a disjunction.
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasAnyTag(res, tags)
		})
	}
	// "A product resource ID linked to this volume (such as an Instance ID)":
	// the server the volume is attached to.
	if productID := q.Get("product_resource_id"); productID != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return res.Runtime[runtimeServerKey] == productID
		})
	}
	// Every volume this product serves reports the one class it has.
	if volumeType := q.Get("volume_type"); volumeType != "" && volumeType != blockStorageClass {
		all = all[:0]
	}
	if ids := idSet(q, "volume_ids"); ids != nil {
		all = filterResources(all, func(res *resource.Resource) bool {
			return ids[res.ID]
		})
	}
	// "Display deleted volumes not erased yet": deletion is immediate here, so
	// the store never holds one and the flag widens the answer by nothing. It
	// is still read — the state filter is real — and docs/limits.md says why
	// both values answer alike.
	includeDeleted, _, err := queryBool(q, "include_deleted")
	if err != nil {
		writeParseFailure(w, "include_deleted", err)
		return
	}
	if !includeDeleted {
		all = filterResources(all, func(res *resource.Resource) bool {
			return res.State != "deleted"
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
	}, all) {
		return
	}

	// Paged like every other list of this pack. This list predates the helper
	// and served everything whatever the client asked; invisible while the
	// emulated account never held two block volumes, measured the day the
	// probe seeded them (TestBlockListsHonourThePageSize fails without it).
	page := parsePage(r)
	start, end := page.slice(len(all))
	out := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		out = append(out, p.blockVolumeView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"volumes":     out,
		"total_count": len(all),
	})
}

func (p *Pack) getBlockVolume(w http.ResponseWriter, r *http.Request) {
	res, ok := p.blockVolumeOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.blockVolumeView(res))
}

// updateBlockVolume carries the name, the size, the tags and the IO/s, which is
// what UpdateVolumeRequest holds beyond the identifier.
//
// A shrink is refused. The API grows a volume and does not shrink one, and
// answering success on a smaller size would tell a client its data survived a
// truncation nothing performed.
// TestABlockVolumeDoesNotShrink fails without this.
func (p *Pack) updateBlockVolume(w http.ResponseWriter, r *http.Request) {
	res, ok := p.blockVolumeOf(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     *string   `json:"name"`
		Size     *uint64   `json:"size"`
		Tags     *[]string `json:"tags"`
		PerfIops *uint32   `json:"perf_iops"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	// The shrink check and the write are one critical section held by the
	// store: as a clone-mutate-Commit sequence this erased a concurrent write
	// to another field of the same volume after its 200 (#295).
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindBlockVolume, res.ID, func(stored *resource.Resource) error {
		// Through the shared reader, and this is the site that carries the
		// most: the plain `.(uint64)` answered 0 on a volume that had crossed a
		// snapshot, so `*req.Size < current` was never true and a restored
		// block volume shrank with a 200 — the refusal this handler exists for,
		// gone, with nothing red anywhere (#542). Outscale had the same defect
		// on the same reader in its own vocabulary; Exoscale found it, wrote it
		// down and fixed it for its own block volumes on 2026-08-17 (5680efb),
		// ten days before #542, and nothing carried it across.
		// TestARestoredBlockVolumeStillRefusesToShrink fails without this.
		current := resource.Uint64(stored, "size")
		if req.Size != nil && *req.Size < current {
			return errBlockVolumeShrinks
		}
		if req.Name != nil {
			stored.Attrs["name"] = *req.Name
		}
		if req.Size != nil {
			stored.Attrs["size"] = *req.Size
		}
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		if req.PerfIops != nil {
			stored.Attrs["perf_iops"] = *req.PerfIops
		}
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	switch {
	case errors.Is(err, errBlockVolumeShrinks):
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "size",
			Reason:       "constraint",
			HelpMessage:  "a volume grows and does not shrink",
		})
		return
	case err != nil:
		writeNotFound(w, "volume", res.ID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.blockVolumeView(updated))
}

// deleteBlockVolume refuses while the volume is attached to a server.
//
// The invariant instance volumes already hold, and the one Terraform depends on
// when a plan destroys a server and its root volume: without the refusal the
// server is left naming a disk that is gone.
// TestABlockVolumeAttachedToAServerDoesNotDelete fails without this.
func (p *Pack) deleteBlockVolume(w http.ResponseWriter, r *http.Request) {
	res, ok := p.blockVolumeOf(w, r)
	if !ok {
		return
	}
	if refs := p.referencesTo(res); len(refs) > 0 {
		writePrecondition(w, "volume", res.ID,
			"the server "+textOf(refs[0]["product_resource_id"])+" still uses this volume")
		return
	}
	p.env.Store.Delete(Name, kindBlockVolume, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Pack) blockVolumeOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	if _, ok := zoneOf(w, r); !ok {
		return nil, false
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindBlockVolume, id)
	if !found {
		writeNotFound(w, "volume", id)
		return nil, false
	}
	return res, true
}

// ---- Snapshots -------------------------------------------------------------

type createBlockSnapshotRequest struct {
	VolumeID  string    `json:"volume_id"`
	Name      string    `json:"name"`
	ProjectID *string   `json:"project_id"`
	Tags      *[]string `json:"tags"`
	Public    bool      `json:"public"`
}

// createBlockSnapshot records a snapshot of a block volume.
//
// A missing volume is refused, for the reason instance's createSnapshot refuses
// one: the client named a resource, and answering success about a snapshot of
// nothing is the half-success this project exists to avoid.
// TestABlockSnapshotOfNothingIsRefused fails without this.
func (p *Pack) createBlockSnapshot(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req createBlockSnapshotRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.VolumeID == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "volume_id", Reason: "required"})
		return
	}
	volume, found := p.env.Store.Get(Name, kindBlockVolume, req.VolumeID)
	if !found {
		writeNotFound(w, "volume", req.VolumeID)
		return
	}

	if p.refuseUnknownProject(w, textPtr(req.ProjectID), projectDeniedToBlock) {
		return
	}
	project, _ := projectOf(textPtr(req.ProjectID))
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindBlockSnapshot, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "available", now)
	res.Attrs = map[string]any{
		"name":    req.Name,
		"project": project,
		"tags":    orEmpty(slicePtr(req.Tags)),
		"size":    volume.Attrs["size"],
		"zone":    zone,
		"public":  req.Public,
		"parent_volume": map[string]any{
			"id":     volume.ID,
			"name":   textOf(volume.Attrs["name"]),
			"type":   blockStorageClass,
			"status": volume.State,
		},
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, blockCreateStatus, p.blockSnapshotView(res))
}

// blockSnapshotView renders block/v1's Snapshot.
//
// Read from the SDK rather than measured, and that is worth stating: the account
// recorded held no block snapshot, so shapes/scaleway.json carries
// `snapshots: array` with nothing under it. `feint shapes --record` on an account
// that has one is how this reading becomes a measurement — the same situation
// instance snapshots were in at SW-2.
func (p *Pack) blockSnapshotView(res *resource.Resource) map[string]any {
	return map[string]any{
		"id":            res.ID,
		"name":          textOf(res.Attrs["name"]),
		"parent_volume": res.Attrs["parent_volume"],
		"size":          res.Attrs["size"],
		"project_id":    textOf(res.Attrs["project"]),
		"created_at":    res.Created.Format(time.RFC3339),
		"updated_at":    res.Updated.Format(time.RFC3339),
		"references":    make([]map[string]any, 0),
		"status":        res.State,
		"tags":          res.Attrs["tags"],
		"zone":          textOf(res.Attrs["zone"]),
		"class":         blockStorageClass,
		"public":        res.Attrs["public"],
	}
}

// listBlockSnapshots reads what its two operations declare, like
// listBlockVolumes above and for the same reason (#277).
func (p *Pack) listBlockSnapshots(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	scope := resource.Tenant{Provider: Name}
	switch {
	case q.Get("project_id") != "":
		scope.Project = q.Get("project_id")
	case q.Get("organization_id") != "":
		// The whole account, like listBlockVolumes above and for the same
		// reason.
	}
	all := p.env.Store.List(kindBlockSnapshot, scope)
	all = filterResources(all, func(res *resource.Resource) bool {
		return textOf(res.Attrs["zone"]) == zone
	})
	if name := q.Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasAnyTag(res, tags)
		})
	}
	// "Filter snapshots by the ID of the original volume."
	if volumeID := q.Get("volume_id"); volumeID != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			parent, _ := res.Attrs["parent_volume"].(map[string]any)
			return parent != nil && parent["id"] == volumeID
		})
	}
	// Same reading as listBlockVolumes: deletion is immediate, the filter is
	// real, docs/limits.md carries the consequence.
	includeDeleted, _, err := queryBool(q, "include_deleted")
	if err != nil {
		writeParseFailure(w, "include_deleted", err)
		return
	}
	if !includeDeleted {
		all = filterResources(all, func(res *resource.Resource) bool {
			return res.State != "deleted"
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
	}, all) {
		return
	}

	// Same paging, same reason as listBlockVolumes above.
	page := parsePage(r)
	start, end := page.slice(len(all))
	out := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		out = append(out, p.blockSnapshotView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"snapshots":   out,
		"total_count": len(all),
	})
}

func (p *Pack) getBlockSnapshot(w http.ResponseWriter, r *http.Request) {
	res, ok := p.blockSnapshotOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.blockSnapshotView(res))
}

func (p *Pack) updateBlockSnapshot(w http.ResponseWriter, r *http.Request) {
	res, ok := p.blockSnapshotOf(w, r)
	if !ok {
		return
	}
	var req struct {
		Name *string   `json:"name"`
		Tags *[]string `json:"tags"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	// Inside the store lock, same reason as updateBlockVolume (#295).
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindBlockSnapshot, res.ID, func(stored *resource.Resource) error {
		if req.Name != nil {
			stored.Attrs["name"] = *req.Name
		}
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "snapshot", res.ID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.blockSnapshotView(updated))
}

// deleteBlockSnapshot refuses while a volume was restored from it.
//
// The order Terraform walks when one plan removes a volume and the snapshot it
// came from, and the same invariant instance snapshots hold against images.
// TestABlockSnapshotAVolumeCameFromDoesNotDelete fails without this.
func (p *Pack) deleteBlockSnapshot(w http.ResponseWriter, r *http.Request) {
	res, ok := p.blockSnapshotOf(w, r)
	if !ok {
		return
	}
	for _, vol := range p.env.Store.List(kindBlockVolume, resource.Tenant{Provider: Name}) {
		if textOf(vol.Attrs["parent_snapshot_id"]) == res.ID {
			writePrecondition(w, "snapshot", res.ID,
				"the volume "+vol.ID+" was created from this snapshot")
			return
		}
	}
	p.env.Store.Delete(Name, kindBlockSnapshot, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Pack) blockSnapshotOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	if _, ok := zoneOf(w, r); !ok {
		return nil, false
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindBlockSnapshot, id)
	if !found {
		writeNotFound(w, "snapshot", id)
		return nil, false
	}
	return res, true
}

// ---- The catalogue ---------------------------------------------------------

// listBlockVolumeTypes serves the small fixed table this product needs.
//
// Declining it would reproduce the trap CLAUDE.md records for the instance
// catalogue: a client reads the inventory before it creates anything, and gives
// up on a 404. The emulator owns no stock, and it must still answer one.
//
// The prices are the SDK's own units — GB/hour, in the smallest currency unit —
// and they are fiction, like every number in the emulated catalogue.
// docs/limits.md says so where a reader will meet it.
func (p *Pack) listBlockVolumeTypes(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	// scw.Money declares Units as an int64 and Nanos as an int32, both numbers.
	// A string in units decodes as "cannot unmarshal string into Go struct field
	// Money.volume_types.pricing.units of type int64", and `scw block volume-type
	// list` dies on it — which is how this was found, on the first run of the
	// suite rather than by reading the struct carefully enough the first time.
	price := func() map[string]any {
		return map[string]any{
			"currency_code": "EUR",
			"units":         0,
			"nanos":         0,
		}
	}
	// One type, and still paged: the SDK declares page and page_size on
	// ListVolumeTypes, and a handler that drops a declared parameter is the
	// class #271 names — page=2 answered the same type again, which is a list
	// that never ends to the SDK's own pagination loop.
	//
	// TestBlockVolumeTypesArePaged fails without it.
	types := []map[string]any{{
		"type":             blockStorageClass,
		"pricing":          price(),
		"snapshot_pricing": price(),
		"specs": map[string]any{
			"class":     blockStorageClass,
			"perf_iops": blockDefaultIOPS,
		},
		"zone": zone,
	}}
	start, end := parsePage(r).slice(len(types))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"volume_types": types[start:end],
		"total_count":  len(types),
	})
}

// ---- The bridge with instance/v1 -------------------------------------------

// blockVolumeServerView renders a block volume the way instance/v1 lists it
// inside a server. Reached through serverVolumeView, which is what every builder
// of a `volumes` map calls.
//
// Two shapes for one disk, and both are needed: the server's `volumes` map is an
// instance VolumeServer whatever product owns the volume, and the fallback read
// that follows is block's own. Rendering the block shape here instead would give
// the Terraform provider a field set it does not decode, and the id it needs to
// follow would be the only part it recognised.
//
// volume_type is "sbs_volume", which is what tells the provider to fall back at
// all: it reads instance.GetVolume first and only tries block on a typed 404.
//
// It was named blockRootVolumeServerView while a root disk was the only block
// volume a server could carry. It is not: `scw instance server attach-volume
// volume-type=sbs_volume` puts one under any key, and the name said otherwise.
func blockVolumeServerView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":                res.ID,
		"name":              textOf(res.Attrs["name"]),
		"size":              res.Attrs["size"],
		"volume_type":       "sbs_volume",
		"zone":              textOf(res.Attrs["zone"]),
		"state":             res.State,
		"creation_date":     res.Created.Format(time.RFC3339),
		"modification_date": res.Updated.Format(time.RFC3339),
		"project":           textOf(res.Attrs["project"]),
		"organization":      defaultOrganization,
		"boot":              false,
		"export_uri":        nil,
		"server":            nil,
	}
	if serverID := res.Runtime[runtimeServerKey]; serverID != "" {
		name := textOf(res.Attrs["server_name"])
		out["server"] = map[string]any{"id": serverID, "name": name}
	}
	return out
}

// The two states a block volume of this emulator is ever in, named once because
// three files write them and `scw` polls on the difference: `in_use` while a
// server holds it, `available` the moment nothing does. Upstream declares more
// (creating, snapshotting, error, deleting) and none of them is reachable here,
// where every transition is immediate — docs/limits.md carries that decision.
const (
	blockVolumeInUse     = "in_use"
	blockVolumeAvailable = "available"
)

// newBlockRootVolume materialises a server's root volume in block rather than in
// instance, which is what `volume_type = "sbs_volume"` asks for.
//
// The two products are one namespace to a client and two stores here, and the
// Terraform provider is what makes that visible: it reads a volume through
// instance.GetVolume first, then falls back to block.GetVolume on a typed 404.
// So an sbs volume must be absent from the instance side and present on the
// block one — being in both would answer the first call and never exercise the
// fallback, which is precisely the path #8 exists to unblock.
// TestAnSbsRootVolumeIsReadableThroughTheBlockFallback fails without this.
//
// parentSnapshot is the image snapshot the disk was restored from, which is what
// the cloud publishes and what a client reads to know where its root came from
// (see imageRootSnapshot).
func (p *Pack) newBlockRootVolume(zone, project, name string, size uint64, parentSnapshot string) *resource.Resource {
	now := p.env.Now()
	return &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindBlockVolume,
		Tenant:  resource.Tenant{Provider: Name, Project: project, Zone: zone},
		State:   blockVolumeInUse,
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"name":               name,
			"project":            project,
			"tags":               []any{},
			"size":               size,
			"zone":               zone,
			"parent_snapshot_id": parentSnapshot,
			"perf_iops":          uint32(blockDefaultIOPS),
		},
	}
}
