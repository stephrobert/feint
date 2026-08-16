package scaleway

import (
	"net/http"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

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
		size, _ = snapshot.Attrs["size"].(uint64)
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
	emulator.WriteJSON(w, http.StatusCreated, p.blockVolumeView(res))
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
		// Present and null on every volume the recorded account returned. Neither
		// is emulated: no Key Manager, and no detachment history.
		"kms_key_id":       nil,
		"last_detached_at": nil,
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

func (p *Pack) listBlockVolumes(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindBlockVolume, resource.Tenant{Provider: Name}) {
		if textOf(res.Attrs["zone"]) != zone {
			continue
		}
		if name != "" && !strings.Contains(textOf(res.Attrs["name"]), name) {
			continue
		}
		out = append(out, p.blockVolumeView(res))
	}
	// Paged like every other list of this pack. This list predates the helper
	// and served everything whatever the client asked; invisible while the
	// emulated account never held two block volumes, measured the day the
	// probe seeded them (TestBlockListsHonourThePageSize fails without it).
	page := parsePage(r)
	start, end := page.slice(len(out))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"volumes":     out[start:end],
		"total_count": len(out),
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
	current, _ := res.Attrs["size"].(uint64)
	if req.Size != nil && *req.Size < current {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "size",
			Reason:       "constraint",
			HelpMessage:  "a volume grows and does not shrink",
		})
		return
	}
	updated := res.Clone()
	if req.Name != nil {
		updated.Attrs["name"] = *req.Name
	}
	if req.Size != nil {
		updated.Attrs["size"] = *req.Size
	}
	if req.Tags != nil {
		updated.Attrs["tags"] = *req.Tags
	}
	if req.PerfIops != nil {
		updated.Attrs["perf_iops"] = *req.PerfIops
	}
	p.env.Store.Commit(updated, p.env.Now())
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
	emulator.WriteJSON(w, http.StatusCreated, p.blockSnapshotView(res))
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

func (p *Pack) listBlockSnapshots(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindBlockSnapshot, resource.Tenant{Provider: Name}) {
		if textOf(res.Attrs["zone"]) != zone {
			continue
		}
		if name != "" && !strings.Contains(textOf(res.Attrs["name"]), name) {
			continue
		}
		out = append(out, p.blockSnapshotView(res))
	}
	// Same paging, same reason as listBlockVolumes above.
	page := parsePage(r)
	start, end := page.slice(len(out))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"snapshots":   out[start:end],
		"total_count": len(out),
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
	updated := res.Clone()
	if req.Name != nil {
		updated.Attrs["name"] = *req.Name
	}
	if req.Tags != nil {
		updated.Attrs["tags"] = *req.Tags
	}
	p.env.Store.Commit(updated, p.env.Now())
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
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"volume_types": []map[string]any{{
			"type":             blockStorageClass,
			"pricing":          price(),
			"snapshot_pricing": price(),
			"specs": map[string]any{
				"class":     blockStorageClass,
				"perf_iops": blockDefaultIOPS,
			},
			"zone": zone,
		}},
		"total_count": 1,
	})
}

// ---- The bridge with instance/v1 -------------------------------------------

// blockRootVolumeServerView renders a block volume the way instance/v1 lists it
// inside a server.
//
// Two shapes for one disk, and both are needed: the server's `volumes` map is an
// instance VolumeServer whatever product owns the volume, and the fallback read
// that follows is block's own. Rendering the block shape here instead would give
// the Terraform provider a field set it does not decode, and the id it needs to
// follow would be the only part it recognised.
//
// volume_type is "sbs_volume", which is what tells the provider to fall back at
// all: it reads instance.GetVolume first and only tries block on a typed 404.
func blockRootVolumeServerView(res *resource.Resource) map[string]any {
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
func (p *Pack) newBlockRootVolume(zone, project, name string, size uint64) *resource.Resource {
	now := p.env.Now()
	return &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindBlockVolume,
		Tenant:  resource.Tenant{Provider: Name, Project: project, Zone: zone},
		State:   "in_use",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"name":               name,
			"project":            project,
			"tags":               []string{},
			"size":               size,
			"zone":               zone,
			"parent_snapshot_id": "",
			"perf_iops":          uint32(blockDefaultIOPS),
		},
	}
}
