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
		// instance/v1 alone, and the resource name in the refusal says which
		// product answered (#648).
		//
		// This resolved BOTH products until 2026-09-03, and the reason was
		// sound when it was written: a server's root disk lives in block since
		// #365, and resolving kindVolume alone answered 404 on the disk the same
		// emulator had just published in the server's own volumes map (#571).
		//
		// The measurement reverses it. Against fr-par, an instance/v1
		// CreateSnapshot naming a block volume answers:
		//
		//	404 {"type": "not_found", "message": "resource is not found",
		//	     "resource": "instance_volume", "resource_id": "<the id>"}
		//
		// A block volume is not an instance_volume, and the cloud says so in the
		// one field a client reads to know WHICH thing was not found. The
		// emulator was more permissive than the cloud in the direction that
		// costs: a golden-image path built this way is green here and fails
		// there.
		//
		// Nothing in this repository needed the permissive version any more.
		// The conformance suite already stopped taking that route — its own
		// comment says `scw instance snapshot create volume-id=<a block volume>`
		// cannot be the subject, because the CLI calls instance.GetVolume itself
		// before it sends anything — and it snapshots a block volume through
		// block/v1, the product that owns it.
		//
		// TestAnInstanceSnapshotRefusesAVolumeItDoesNotOwn fails without this.
		volume, found := p.env.Store.Get(Name, kindVolume, *req.VolumeID)
		if !found {
			writeNotFound(w, "instance_volume", *req.VolumeID)
			return
		}
		// A disk nothing ever wrote to has nothing to snapshot, and fr-par says
		// so in those words (#650):
		//
		//	400 {"type": "invalid_arguments", "message": "invalid argument(s)",
		//	     "details": [{"reason": "constraint",
		//	                  "help_message": "cannot create a RO disk from an empty disk"}]}
		//
		// No argument_name in that details entry, which is why ArgumentError
		// omits the field when it is empty.
		//
		// ATTACHMENT is the line, measured rather than assumed: a volume never
		// attached to anything is refused, and the root disk of a server that was
		// created and never started is ACCEPTED. So the question is not whether
		// the machine ran, it is whether the disk was ever anybody's.
		//
		// This is the third of the same shape in one report: the emulator more
		// permissive than the cloud, so a green run here says nothing. It cost
		// the reporter a real apply — the published example stack builds its
		// golden image exactly this way, and the cloud refused it.
		//
		// TestASnapshotOfAVolumeNothingEverWroteToIsRefused fails without this.
		if volume.Kind == kindVolume && volume.Runtime[runtimeAttachedKey] == "" {
			writeInvalidArguments(w, ArgumentError{
				Reason:      "constraint",
				HelpMessage: "cannot create a RO disk from an empty disk",
			})
			return
		}
		base = map[string]any{
			"id":   volume.ID,
			"name": textOf(volume.Attrs["name"]),
		}
		if volumeType == "" {
			volumeType = textOf(volume.Attrs["volume_type"])
		}
		// The `unified` branch that stood here is gone with its subject (#648).
		//
		// It typed the snapshot of a BLOCK volume, and this route no longer takes
		// one: fr-par answers 404 instance_volume for that request, and so does
		// this pack now. A branch for an input that cannot arrive is a control
		// that can never fire, which is the defect this repository names.
		//
		// What it was protecting is worth keeping written down, because it is
		// measured and it will matter again the day an instance snapshot really
		// can cross into block. `sbs_snapshot` is a PROMISE that the id resolves
		// in the block product: `scw instance image list` calls block.GetSnapshot
		// for every image whose root_volume.volume_type is sbs_snapshot and fails
		// the WHOLE listing on error (scaleway-cli 2.56.3,
		// internal/namespaces/instance/v1/custom_image.go:222). Answering it for
		// a snapshot block cannot resolve made `scw instance image list` say
		// "cannot find resource 'snapshot'" for an entire zone, measured
		// 2026-08-28. So: a type that names the block product must be answerable
		// by the block product.
		//
		// TestAnInstanceSnapshotRefusesAVolumeItDoesNotOwn holds the refusal that
		// replaced the branch.
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
