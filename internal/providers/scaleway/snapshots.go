package scaleway

import (
	"net/http"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Snapshots, as control-plane records.
//
// They were declined until SW-2, on the argument that "a snapshot copies the
// bytes of a volume, and an emulated volume is a size and a name without any".
// That is true and it is not a reason to refuse: the same argument was made for
// Outscale in 0.4.0, reversed in 0.6.0, and Outscale has served snapshots as
// records ever since. Refusing here while serving there made the two packs
// disagree about what an emulator is for.
//
// What a client does with a snapshot is a control-plane sequence — take one of a
// volume, cut an image from it, boot a server from that image — and every step
// of it is an API call this emulator can answer truthfully. The bytes are the
// one part it cannot provide, and #115 carries that: an image with nothing
// behind it refuses to boot rather than substituting a distribution nobody
// asked for, which is #83's rule applied to a resource the client created.
//
// The shapes come from the SDK (instance/v1 Snapshot, SnapshotBaseVolume) rather
// than from a recording, and that is worth stating: shapes/scaleway.json holds
// `snapshots: array` and nothing under it, because the account it was recorded
// from has none. `feint shapes --record` on an account carrying one would learn
// the element shape and is the way to replace this reading with a measurement.

const kindSnapshot = "instance/snapshot"

type createSnapshotRequest struct {
	Name         string    `json:"name"`
	VolumeID     *string   `json:"volume_id"`
	Tags         *[]string `json:"tags"`
	Organization *string   `json:"organization"`
	Project      *string   `json:"project"`
	VolumeType   string    `json:"volume_type"`
	// Bucket and Key belong to the import-from-Object-Storage path, which stays
	// declined: docs/limits.md says why Object Storage is not emulated, and a
	// snapshot restored from a bucket that does not exist would answer success
	// about bytes nobody has.
	Bucket *string `json:"bucket"`
	Key    *string `json:"key"`
}

// createSnapshot records a snapshot of a volume.
//
// A missing volume is refused rather than recorded: the client named a resource,
// and answering success about a snapshot of nothing is the half-success this
// project exists to avoid. An unknown volume_id is the one case where refusing
// beats accepting, because unlike an image identifier nobody hardcodes a volume
// id from production into a fixture.
// TestASnapshotOfNothingIsRefused fails without this.
func (p *Pack) createSnapshot(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req createSnapshotRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "name", Reason: "required"})
		return
	}
	if req.Bucket != nil || req.Key != nil {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "bucket",
			Reason:       "constraint",
			HelpMessage:  "importing a snapshot reads Object Storage, which this emulator does not serve; see docs/limits.md",
		})
		return
	}

	volumeType := req.VolumeType
	size := uint64(rootVolumeSize)
	base := map[string]any{"id": "", "name": ""}
	if req.VolumeID != nil && *req.VolumeID != "" {
		// Both products. A server's root disk lives in block as soon as the
		// client asks for sbs_volume, and this resolved kindVolume alone — so
		// `scw instance snapshot create volume-id=<that root>` answered 404 on
		// the disk the same emulator had just published in the server's own
		// volumes map (#571). The conformance suite's golden-image path takes
		// exactly that route: it snapshots volumes["0"] and cuts an image from
		// the snapshot.
		volume, found := p.anyVolume(*req.VolumeID)
		if !found {
			writeNotFound(w, "volume", *req.VolumeID)
			return
		}
		base = map[string]any{
			"id":   volume.ID,
			"name": textOf(volume.Attrs["name"]),
		}
		if volumeType == "" {
			volumeType = textOf(volume.Attrs["volume_type"])
		}
		// A block volume carries no volume_type attribute — its product has one
		// class, "sbs" — so the reading above leaves it empty and the default
		// below would call the snapshot b_ssd, which is a different product.
		//
		// `unified` rather than `sbs_snapshot`, and the difference was MEASURED
		// rather than reasoned. sbs_snapshot was the first answer here, read
		// straight off the SDK's VolumeVolumeType enum, and it broke a command:
		// `scw instance image list` calls block.GetSnapshot for every image whose
		// root_volume.volume_type is sbs_snapshot and fails the WHOLE listing on
		// error (scaleway-cli 2.56.3,
		// internal/namespaces/instance/v1/custom_image.go:222). Cutting an image
		// from such a snapshot therefore made `scw instance image list` answer
		// "cannot find resource 'snapshot'" for the entire zone — measured
		// 2026-08-28. sbs_snapshot is a promise that the id resolves in the
		// BLOCK product, and this snapshot lives in instance/v1.
		//
		// `unified` is the value the CLI itself sends for this very input: with
		// `unified=true` it skips the volume lookup entirely and asks for
		// SnapshotVolumeTypeUnified, whatever the volume is. And without that
		// flag it reads the volume through instance.GetVolume and gives up on a
		// 404 — so unified is the ONLY instance snapshot of a block volume any
		// scw user can ask for.
		//
		// What is not settled, and is not this change's to settle: whether the
		// cloud makes such a snapshot readable through block/v1alpha1 as well.
		// If it does, the honest answer is sbs_snapshot AND a snapshot that
		// answers on both doors — which is the volume work of #571 done again
		// for snapshots, and nothing here has measured it.
		//
		// TestAnInstanceSnapshotOfABlockVolumeDoesNotPromiseTheBlockProduct
		// fails without this.
		//
		// Only when the client named none: the request field "overrides the
		// volume_type of the snapshot", which is the SDK's own wording, so a
		// client that asked for one keeps it.
		if volumeType == "" && volume.Kind == kindBlockVolume {
			volumeType = "unified"
		}
		// Through the shared reader: the assertion this replaces answered
		// ok=false on a volume that had crossed a snapshot, so a snapshot taken
		// after a `feint snapshot load` recorded a size of zero (#542).
		// TestAnInstanceSnapshotOfARestoredVolumeInheritsItsSize fails without
		// this.
		if _, present := volume.Attrs["size"]; present {
			size = resource.Uint64(volume, "size")
		}
	}
	// l_ssd since #393: the fallback named b_ssd, which instance/v1 no longer
	// mints, so a snapshot reaching this line was typed after a product that can
	// no longer be created. Every instance volume now carries l_ssd or scratch,
	// which makes this a fallback for a volume with no readable type at all —
	// and l_ssd is the one of the two a snapshot can be taken of.
	if volumeType == "" {
		volumeType = "l_ssd"
	}

	if p.refuseUnknownProject(w, textPtr(req.Project), projectDeniedToInstance) {
		return
	}
	project, organization := projectOf(textPtr(req.Project))
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindSnapshot, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "available", now)
	res.Attrs = map[string]any{
		"name":         req.Name,
		"organization": organization,
		"project":      project,
		"tags":         orEmpty(slicePtr(req.Tags)),
		"volume_type":  volumeType,
		"size":         size,
		"zone":         zone,
		"base_volume":  base,
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"snapshot": p.snapshotView(res)})
}

// snapshotView renders the SDK's instance/v1 Snapshot.
//
// state is "available" immediately: docs/limits.md carries the decision that
// transitions are immediate here, and a snapshot that lingered in "snapshotting"
// would only make a client wait for information this emulator does not have.
// error_reason is present and null, because the SDK declares it as a pointer a
// caller may read.
func (p *Pack) snapshotView(res *resource.Resource) map[string]any {
	return map[string]any{
		"id":                res.ID,
		"name":              textOf(res.Attrs["name"]),
		"organization":      textOf(res.Attrs["organization"]),
		"project":           textOf(res.Attrs["project"]),
		"tags":              res.Attrs["tags"],
		"volume_type":       textOf(res.Attrs["volume_type"]),
		"size":              res.Attrs["size"],
		"state":             res.State,
		"base_volume":       res.Attrs["base_volume"],
		"creation_date":     res.Created.Format(time.RFC3339),
		"modification_date": res.Updated.Format(time.RFC3339),
		"zone":              textOf(res.Attrs["zone"]),
		"error_reason":      nil,
	}
}

// listSnapshots reads every parameter its operation declares — it read `name`
// alone, so `?base_volume_id=…` answered every snapshot in the zone and
// `?per_page=2` answered them all at once (#277).
func (p *Pack) listSnapshots(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	scope := resource.Tenant{Provider: Name}
	switch {
	case q.Get("project") != "":
		scope.Project = q.Get("project")
	case q.Get("organization") != "":
		// The whole account — one organization lives here (scopeOf's rule),
		// never an equality against the pack's constant.
	}
	all := p.env.Store.List(kindSnapshot, scope)
	all = filterResources(all, func(res *resource.Resource) bool {
		return textOf(res.Attrs["zone"]) == zone
	})
	// The SDK documents this filter as a substring, with its own example: the
	// same reading that fixed `scw instance volume list name=vol` coming
	// back empty against a volume called myvolume.
	if name := q.Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	// "With the requested tag", instance/v1's exact-tags conjunction.
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasEveryTag(res, tags)
		})
	}
	// "Snapshots originating only from this volume."
	if baseVolumeID := q.Get("base_volume_id"); baseVolumeID != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			base, _ := res.Attrs["base_volume"].(map[string]any)
			return base != nil && base["id"] == baseVolumeID
		})
	}

	page := parsePage(r)
	start, end := page.slice(len(all))
	out := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		out = append(out, p.snapshotView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"snapshots":   out,
		"total_count": len(all),
	})
}

func (p *Pack) getSnapshot(w http.ResponseWriter, r *http.Request) {
	res, ok := p.snapshotOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"snapshot": p.snapshotView(res)})
}

// updateSnapshot carries the name and the tags, which is what the SDK's
// UpdateSnapshotRequest holds beyond the identifier.
func (p *Pack) updateSnapshot(w http.ResponseWriter, r *http.Request) {
	res, ok := p.snapshotOf(w, r)
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
	// Inside the store lock: the clone-mutate-Commit shape erased a concurrent
	// write to the other field of the same snapshot after its 200 (#295).
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindSnapshot, res.ID, func(stored *resource.Resource) error {
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
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"snapshot": p.snapshotView(updated)})
}

// deleteSnapshot refuses while an image is cut from it.
//
// The same invariant volumes already hold: a client destroying in the wrong
// order needs the refusal in order to retry, and Terraform walks exactly that
// order when it removes an image and its snapshot in one plan.
// TestASnapshotAnImageIsCutFromDoesNotDelete fails without this.
func (p *Pack) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	res, ok := p.snapshotOf(w, r)
	if !ok {
		return
	}
	for _, img := range p.env.Store.List(kindImage, resource.Tenant{Provider: Name}) {
		if textOf(img.Attrs["snapshot_id"]) == res.ID {
			writePrecondition(w, "snapshot", res.ID,
				"the image "+img.ID+" is cut from this snapshot")
			return
		}
	}
	p.env.Store.Delete(Name, kindSnapshot, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Pack) snapshotOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	if _, ok := zoneOf(w, r); !ok {
		return nil, false
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindSnapshot, id)
	if !found {
		writeNotFound(w, "snapshot", id)
		return nil, false
	}
	return res, true
}

// textOf reads a stored attribute as a string, the way every view in this pack
// does. Written once here rather than repeated at each field.
func textOf(v any) string {
	s, _ := v.(string)
	return s
}

// textPtr and slicePtr read an optional field the SDK declares as a pointer,
// which is how it tells "absent" from "empty" on the wire. Named apart from the
// pack's existing deref, which answers a *bool.
func textPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func slicePtr(s *[]string) []string {
	if s == nil {
		return nil
	}
	return *s
}
