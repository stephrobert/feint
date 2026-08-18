package exoscale

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Block Storage: a volume a client declares, attaches to an instance, snapshots
// and resizes (EXO-4, #12).
//
// A control plane and nothing more, which is this product's honest shape here:
// the emulator holds no bytes, so a volume is a record carrying a size, a state
// and the instance it is attached to. docs/limits.md owns the sentence; what
// this file owns is that every field a client reads is one the API declares, and
// that the relations behave the way a client's plan depends on.
//
// The two relations are computed on one side and stored on the other, which is
// the model the rest of this pack already uses:
//
//   - a volume stores the instance it is attached to; the instance's own view
//     does not list its volumes, because upstream's instance schema does not
//     declare them (pack.go says so where block-storage-volumes stays off);
//   - a snapshot stores the volume it was taken from, and the volume publishes
//     the list back — `block-storage-snapshots` on the volume is computed from
//     the store rather than maintained beside it, so a deleted snapshot cannot
//     survive in a list nobody updated.
//
// The deletion rules are upstream's, and each is a refusal a real client meets:
// an attached volume is not deletable, and a volume with snapshots is not
// either. Both are driven by the conformance suite rather than asserted here
// only, because a refusal nothing provokes is a refusal nobody has seen.
//
// The client that proves this batch is `exo compute block-storage`, and the
// reason it is not Terraform is settled elsewhere rather than here:
// docs/limits.md, "The Exoscale Terraform provider is refused, and why". The
// published provider builds two clients, honours EXOSCALE_API_ENDPOINT for one
// and ignores it for the other, so an apply *splits* between this emulator and a
// paying account instead of failing — which is why the emulator refuses it by
// user agent. It is filed upstream as
// exoscale/terraform-provider-exoscale#573, and a patched fork carrying the
// four-line fix is pinned in that same section.
//
// The fork can check this product by hand and does **not** count towards
// conformance, by a decision that section states: a client this project patched
// is no longer the official client, and adding that claim to a driven one would
// repeat the overstatement `probed` exists to avoid. So the issue's "terraform
// apply with exoscale_block_storage_volume" is not a criterion this batch can
// meet with a published client, and not for a reason block storage owns.

const (
	kindBlockVolume   = "block-storage-volume"
	kindBlockSnapshot = "block-storage-snapshot"

	nounBlockVolume   = "block-storage-volume"
	nounBlockSnapshot = "block-storage-snapshot"

	// The states upstream declares, and the ones this emulator can honestly
	// reach. A volume is not "ready" — that word is this pack's vocabulary for
	// instances, and the block-storage schema does not contain it: a volume is
	// `detached` until something holds it and `attached` once one does, so the
	// state *is* the attachment rather than a second copy of it.
	//
	// Measured rather than chosen: the first version published "ready" and the
	// contract check named it on three operations at once ("value ready is not
	// one of snapshotting, deleted, creating, detached, deleting, attaching,
	// error, attached, detaching"). Nothing transits through `creating` or
	// `attaching` here — an emulated volume is allocated the moment it is
	// recorded, and a transient state a client would poll out of would be a wait
	// invented for realism.
	blockVolumeDetached = "detached"
	blockVolumeAttached = "attached"

	// A snapshot's states are a different vocabulary again — `created`, not
	// `ready`, and destroyed rather than deleted.
	blockSnapshotCreated = "created"

	// runtimeBlockVolumeKey links a snapshot to the volume it was taken from, in
	// Runtime rather than in Attrs: the view is the API's, this is the
	// emulator's own bookkeeping. Same split as runtimeSnapshotInstanceKey.
	runtimeBlockVolumeKey = "block-storage-volume"
)

// Owns tells the shared orphan sweep which resources of this pack belong to
// another, and to which one.
//
// It exists because of this batch. `barrage_test.go` did not call
// `storetest.Orphans` and said exactly why: every reference in this pack was
// held by the owner, so deleting the owner took the reference with it and no
// record could be left naming something gone. The comment ended with the
// condition for its own expiry — *"the day a resource here names an instance, it
// declares Owns and joins the sweep the two other packs run (#215)"* — and a
// block volume names one.
//
// The defect it catches is the one #215 measured on Outscale: a volume that
// outlived its instance goes on refusing to attach anywhere else, because the
// attachment it still names can never be released by any client call.
//
// A snapshot names its volume, and that relation is deliberately *not* declared
// here: the volume refuses to be deleted while a snapshot exists, so the pairing
// cannot be broken from the API at all. Declaring it would ask the sweep to
// check something the refusal already makes impossible.
func Owns(res *resource.Resource) (kind, id string, ok bool) {
	switch res.Kind {
	case kindBlockVolume:
		attached, _ := res.Attrs["instance"].(map[string]any)
		if instanceID, _ := attached["id"].(string); instanceID != "" {
			return kindInstance, instanceID, true
		}
	case kindInstance:
		// A pool member (#232). The pool destroys its members with itself, so a
		// member naming a pool that is gone means that path was bypassed — by a
		// restore, by a future write, or by a bug — and the instance is then
		// managed by nothing while still claiming a manager.
		if poolID := res.Runtime[runtimePoolKey]; poolID != "" {
			return kindPool, poolID, true
		}
	}
	return "", "", false
}

// blockVolumeView publishes a volume in the shape the OpenAPI declares. The
// snapshot list is computed at read time; see the file header for why.
func (p *Pack) blockVolumeView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":         res.ID,
		"state":      res.State,
		"created-at": res.Created.UTC().Format(time.RFC3339),
		// Declared by the schema and constant here: the emulator has one storage
		// class, and a blocksize invented per volume would enter a client's
		// arithmetic. 4096 is what a measured Exoscale volume answers.
		"blocksize": 4096,
		// Their volumes are encrypted at rest by default, the same claim the
		// instance view makes about its disk.
		"encrypted":               true,
		"block-storage-snapshots": p.blockSnapshotRefs(res.ID),
	}
	for key, value := range res.Attrs {
		out[key] = value
	}
	return out
}

// blockSnapshotRefs answers the snapshots taken from one volume, as the
// reference objects the schema declares.
func (p *Pack) blockSnapshotRefs(volumeID string) []any {
	list := p.env.Store.List(kindBlockSnapshot, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	refs := make([]any, 0)
	for _, snap := range list {
		if snap.Runtime[runtimeBlockVolumeKey] == volumeID {
			refs = append(refs, map[string]any{"id": snap.ID})
		}
	}
	return refs
}

func (p *Pack) blockSnapshotView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":         res.ID,
		"state":      res.State,
		"created-at": res.Created.UTC().Format(time.RFC3339),
	}
	for key, value := range res.Attrs {
		out[key] = value
	}
	return out
}

type createBlockVolumeRequest struct {
	Name                 string            `json:"name"`
	Size                 *int64            `json:"size"`
	Labels               map[string]string `json:"labels"`
	BlockStorageSnapshot *struct {
		ID string `json:"id"`
	} `json:"block-storage-snapshot"`
}

// createBlockVolume records a volume, either sized by the client or restored
// from a snapshot.
//
// The two inputs are exclusive in the sense that matters: a volume created from
// a snapshot takes the snapshot's size, and a size given alongside is not a
// second opinion the emulator averages — upstream sizes the restored volume from
// its source, and answering anything else would make a client's `terraform plan`
// disagree with what it asked for.
func (p *Pack) createBlockVolume(w http.ResponseWriter, r *http.Request) {
	var req createBlockVolumeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.ContainsAny(req.Name, "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}

	size := int64(0)
	if req.Size != nil {
		size = *req.Size
	}
	if req.BlockStorageSnapshot != nil && req.BlockStorageSnapshot.ID != "" {
		snapshot, found := p.env.Store.Get(Name, kindBlockSnapshot, req.BlockStorageSnapshot.ID)
		if !found {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		size = int64Of(snapshot.Attrs["size"])
	}
	// A volume of no size is not a volume, and the refusal belongs at the door:
	// a client that forgot the field gets told, rather than receiving a record
	// whose size arithmetic is zero everywhere it is used.
	if size <= 0 {
		writeError(w, http.StatusBadRequest, "size is required and must be greater than zero")
		return
	}

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindBlockVolume, resource.Tenant{Provider: Name}, blockVolumeDetached, now)
	res.Attrs = map[string]any{
		"name": req.Name,
		"size": size,
		// Present and empty rather than absent, the lesson snapshots.go paid
		// for: `exo` dereferences what the schema declares.
		"labels": labelsOrEmpty(req.Labels),
	}
	p.env.Store.Put(res)
	p.writeOperation(w, p.operationReferring(nounBlockVolume, res.ID))
}

// int64Of reads a stored size back whatever shape it comes in.
//
// Not a convenience: a size written as int64 comes back as float64 after
// `PUT /_feint/state`, because a snapshot round-trips the store through JSON.
// A type assertion on int64 alone would quietly answer zero on a restored
// store, and the two readers of this value are the shrink refusal and the size a
// snapshot inherits — so a restored emulator would accept a shrink and hand out
// zero-sized snapshots, with nothing red anywhere. That is the "a restored state
// is untrusted input" rule of CLAUDE.md, met at the point where the value is
// read rather than hoped away.
//
// TestABlockVolumeSurvivesASnapshotRestore fails without this.
func int64Of(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// labelsOrEmpty keeps a declared map present. A nil map marshals to null, and a
// client ranging over null is a client that stops.
func labelsOrEmpty(labels map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{}
	}
	return labels
}

// listBlockVolumes answers the volumes, narrowed to one instance's when the
// client asks: their document declares an instance-id filter on this operation
// (.upstream/exoscale-openapi.yaml:24620-24626), and the handler used to
// discard the request, so a client reading one machine's disks read the whole
// account's (#271 names the class).
//
// TestBlockVolumeInstanceFilterIsHonoured fails without the filter.
func (p *Pack) listBlockVolumes(w http.ResponseWriter, r *http.Request) {
	instanceID := r.URL.Query().Get("instance-id")
	list := p.env.Store.List(kindBlockVolume, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	out := make([]map[string]any, 0, len(list))
	for _, res := range list {
		if instanceID != "" {
			attached, _ := res.Attrs["instance"].(map[string]any)
			if attached["id"] != instanceID {
				continue
			}
		}
		out = append(out, p.blockVolumeView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"block-storage-volumes": out})
}

func (p *Pack) getBlockVolume(w http.ResponseWriter, r *http.Request) {
	res, found := p.env.Store.Get(Name, kindBlockVolume, r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.blockVolumeView(res))
}

type updateBlockVolumeRequest struct {
	Name   *string           `json:"name"`
	Labels map[string]string `json:"labels"`
}

// updateBlockVolume applies the fields the client sent and only those, the same
// rule updateInstance states: an absent field is one to keep, not one to clear.
func (p *Pack) updateBlockVolume(w http.ResponseWriter, r *http.Request) {
	var req updateBlockVolumeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name != nil && strings.ContainsAny(*req.Name, "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}
	id := r.PathValue("id")
	err := p.env.Store.Update(Name, kindBlockVolume, id, func(stored *resource.Resource) error {
		if req.Name != nil {
			stored.Attrs["name"] = *req.Name
		}
		if req.Labels != nil {
			stored.Attrs["labels"] = req.Labels
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounBlockVolume, id))
}

type resizeBlockVolumeRequest struct {
	Size *int64 `json:"size"`
}

// resizeBlockVolume grows a volume and refuses to shrink it.
//
// The refusal is the half worth serving: a filesystem does not survive its disk
// getting smaller, upstream refuses it, and an emulator that accepted it would
// let a plan through that the real cloud stops. The Outscale pack refuses the
// same thing for the same reason, which is how this behaviour ended up written
// once per pack rather than once — the vocabulary differs, the rule does not.
//
// TestABlockVolumeGrowsAndRefusesToShrink fails without this.
func (p *Pack) resizeBlockVolume(w http.ResponseWriter, r *http.Request) {
	var req resizeBlockVolumeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Size == nil || *req.Size <= 0 {
		writeError(w, http.StatusBadRequest, "size is required and must be greater than zero")
		return
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindBlockVolume, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if current := int64Of(res.Attrs["size"]); *req.Size < current {
		writeError(w, http.StatusBadRequest, "a volume cannot be shrunk")
		return
	}
	if err := p.env.Store.Update(Name, kindBlockVolume, id, func(stored *resource.Resource) error {
		stored.Attrs["size"] = *req.Size
		return nil
	}); err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	// The one operation of this product that answers the resource rather than an
	// operation object, and it is the contract that said so: resize declares
	// `block-storage-volume` as its response where its twelve siblings declare
	// `operation`. Written the other way first, and the check named it —
	// "reference: block-storage-volume does not define this field".
	resized, found := p.env.Store.Get(Name, kindBlockVolume, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.blockVolumeView(resized))
}

type attachBlockVolumeRequest struct {
	Instance *struct {
		ID string `json:"id"`
	} `json:"instance"`
}

// attachBlockVolumeToInstance binds a volume to one instance.
//
// One instance, and the refusal that says so: a block volume is not shared
// storage, upstream attaches it to exactly one machine, and a second attach
// while the first holds is the case a client's retry loop produces. Refusing it
// here is what keeps the emulated relation the same shape as the real one — the
// address family of this defect is the one #213 measured on elastic IPs, where
// three packs disagreed about what "already attached" means.
//
// TestAnAttachedBlockVolumeRefusesASecondInstance fails without this.
func (p *Pack) attachBlockVolumeToInstance(w http.ResponseWriter, r *http.Request) {
	var req attachBlockVolumeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Instance == nil || req.Instance.ID == "" {
		writeError(w, http.StatusBadRequest, "instance is required")
		return
	}
	id := r.PathValue("id")
	volume, found := p.env.Store.Get(Name, kindBlockVolume, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if _, found := p.env.Store.Get(Name, kindInstance, req.Instance.ID); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if attached, ok := volume.Attrs["instance"].(map[string]any); ok {
		if held, _ := attached["id"].(string); held != "" && held != req.Instance.ID {
			writeError(w, http.StatusBadRequest, "the volume is already attached to another instance")
			return
		}
	}
	if err := p.env.Store.Update(Name, kindBlockVolume, id, func(stored *resource.Resource) error {
		stored.Attrs["instance"] = map[string]any{"id": req.Instance.ID}
		// The state is the attachment rather than a field beside it, which is
		// what keeps the two from disagreeing: there is no path here that sets
		// one without the other.
		stored.State = blockVolumeAttached
		return nil
	}); err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounBlockVolume, id))
}

// detachBlockVolume removes the attachment, and refuses when there is none.
//
// A detach of an unattached volume answering 200 is the kind of success that
// hides a bug in a client's state machine: it believes it detached something.
func (p *Pack) detachBlockVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	volume, found := p.env.Store.Get(Name, kindBlockVolume, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	attached, _ := volume.Attrs["instance"].(map[string]any)
	if held, _ := attached["id"].(string); held == "" {
		// This exact wording is load-bearing, and it is the Terraform provider
		// that made it so. Its destroy calls detach unconditionally and tolerates
		// the refusal only when the message says this:
		//
		//	if strings.HasSuffix(err.Error(), "Volume not attached") {
		//	    tflog.Debug(ctx, "volume not attached")
		//	} else { ...AddError("unable to detach volume"...) }
		//
		// — pkg/resources/block_storage/resource_volume.go. So the real cloud
		// refuses this call, as this emulator does, and a client depends on the
		// *sentence* to tell that refusal from a real failure. A first version
		// said "the volume is not attached to an instance", which is the same
		// fact in other words, and `terraform destroy` died on "unable to detach
		// volume" for every volume that was never attached.
		//
		// `exo` never showed it: the CLI does not detach before deleting.
		writeError(w, http.StatusBadRequest, "Volume not attached")
		return
	}
	if err := p.env.Store.Update(Name, kindBlockVolume, id, func(stored *resource.Resource) error {
		delete(stored.Attrs, "instance")
		stored.State = blockVolumeDetached
		return nil
	}); err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounBlockVolume, id))
}

// deleteBlockVolume removes a volume nothing depends on.
//
// Two refusals, both upstream's, and both are relations a destroy walks in a
// specific order: an attached volume goes after its detach, and a snapshotted
// one after its snapshots. A `terraform destroy` that meets neither refusal
// learns nothing; one that meets them in the wrong order fails on the real
// cloud and passed here, which is the divergence this pack exists to avoid.
//
// TestABlockVolumeRefusesToBeDeletedUnderItsInstanceOrItsSnapshots fails
// without this.
func (p *Pack) deleteBlockVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	volume, found := p.env.Store.Get(Name, kindBlockVolume, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if attached, ok := volume.Attrs["instance"].(map[string]any); ok {
		if held, _ := attached["id"].(string); held != "" {
			writeError(w, http.StatusBadRequest,
				"the volume is attached to an instance; detach it first")
			return
		}
	}
	if len(p.blockSnapshotRefs(id)) > 0 {
		writeError(w, http.StatusBadRequest,
			"the volume still has snapshots; delete them first")
		return
	}
	p.env.Store.Delete(Name, kindBlockVolume, id)
	p.writeOperation(w, p.operationReferring(nounBlockVolume, id))
}

type createBlockSnapshotRequest struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

// createBlockSnapshot records what a volume looked like. Its size is the
// volume's declared size rather than a measurement, for the reason the instance
// snapshot states: the emulator holds no bytes, and a size invented from nothing
// enters a client's arithmetic.
func (p *Pack) createBlockSnapshot(w http.ResponseWriter, r *http.Request) {
	volumeID := r.PathValue("id")
	volume, found := p.env.Store.Get(Name, kindBlockVolume, volumeID)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	var req createBlockSnapshotRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.ContainsAny(req.Name, "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindBlockSnapshot, resource.Tenant{Provider: Name}, blockSnapshotCreated, now)
	volumeName, _ := volume.Attrs["name"].(string)
	size := int64Of(volume.Attrs["size"])
	res.Attrs = map[string]any{
		"name": orSnapshotName(req.Name, volumeName, now),
		// size is what the snapshot occupies, volume-size what it restores to.
		// Both are declared by the schema and both are the volume's size here,
		// because there is no compression to model and inventing a ratio would
		// be fiction a client could act on.
		"size":                 size,
		"volume-size":          size,
		"labels":               labelsOrEmpty(req.Labels),
		"block-storage-volume": map[string]any{"id": volume.ID},
	}
	res.Runtime = map[string]string{runtimeBlockVolumeKey: volume.ID}
	p.env.Store.Put(res)
	p.writeOperation(w, p.operationReferring(nounBlockSnapshot, res.ID))
}

func (p *Pack) listBlockSnapshots(w http.ResponseWriter, _ *http.Request) {
	list := p.env.Store.List(kindBlockSnapshot, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	out := make([]map[string]any, 0, len(list))
	for _, res := range list {
		out = append(out, p.blockSnapshotView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"block-storage-snapshots": out})
}

func (p *Pack) getBlockSnapshot(w http.ResponseWriter, r *http.Request) {
	res, found := p.env.Store.Get(Name, kindBlockSnapshot, r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.blockSnapshotView(res))
}

type updateBlockSnapshotRequest struct {
	Name   *string           `json:"name"`
	Labels map[string]string `json:"labels"`
}

func (p *Pack) updateBlockSnapshot(w http.ResponseWriter, r *http.Request) {
	var req updateBlockSnapshotRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name != nil && strings.ContainsAny(*req.Name, "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}
	id := r.PathValue("id")
	err := p.env.Store.Update(Name, kindBlockSnapshot, id, func(stored *resource.Resource) error {
		if req.Name != nil {
			stored.Attrs["name"] = *req.Name
		}
		if req.Labels != nil {
			stored.Attrs["labels"] = req.Labels
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounBlockSnapshot, id))
}

func (p *Pack) deleteBlockSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, found := p.env.Store.Get(Name, kindBlockSnapshot, id); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.env.Store.Delete(Name, kindBlockSnapshot, id)
	p.writeOperation(w, p.operationReferring(nounBlockSnapshot, id))
}
