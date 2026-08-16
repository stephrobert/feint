package exoscale

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Instance snapshots, and the template a snapshot is promoted into (#173).
//
// Seven operations sat untriaged: they belonged to no open batch, and the
// archived roadmap had folded them into a machine batch that closed without
// them. "Not triaged yet" is exactly what Declined() refuses to hold, so each of
// them is decided here — six served, one refused with its reason.
//
// The decision pattern is not invented. Scaleway crossed this bridge in SW-2: a
// snapshot is a control-plane record, an image cut from it boots nothing, and
// the emulator says so rather than pretending to hold bytes. The same rule
// applies here, and it is what makes `exo compute instance snapshot create` and
// `revert` drivable without the emulator owning a disk.
//
// export-snapshot is the one refusal, in Declined(): it hands back a pre-signed
// URL to an object store, and this project does not emulate Object Storage
// (docs/limits.md). Answering a URL that resolves to nothing would be worse than
// refusing — a client would follow it.

const (
	kindSnapshot = "snapshot"
	nounSnapshot = "snapshot"
)

// snapshotView is the shape the API publishes, from the OpenAPI's own snapshot
// schema: id, name, created-at, state, size, instance, export.
//
// export is present and empty, and the first version of this omitted it on the
// argument that an empty export object says the export happened and produced
// nothing. The official client settled it: `exo compute instance snapshot show`
// dereferences the field without checking, so omitting it panics the CLI — and
// `snapshot create` calls show, so the whole path died on it.
//
// That is the north star doing its job. The reasoning was tidy and the client
// disagreed, which makes the client right: an emulator whose omission crashes
// `exo` is not serving a snapshot at all. The export operation itself stays
// declined, which is where the refusal belongs — a client that asks for an
// export is told no, rather than handed a URL to nothing.
func (p *Pack) snapshotView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":         res.ID,
		"state":      res.State,
		"created-at": res.Created.UTC().Format(time.RFC3339),
		"export":     map[string]any{"presigned-url": "", "md5sum": ""},
		// Every field the schema declares, present. Omitting one is what broke
		// `exo compute instance snapshot show`, and a client that dereferences
		// without checking is the client this emulator exists to satisfy.
		"application-consistent": false,
	}
	for key, value := range res.Attrs {
		out[key] = value
	}
	return out
}

type createSnapshotRequest struct {
	Name string `json:"name"`
}

// createSnapshot records what the instance looked like. It does not copy a disk,
// and the size it publishes is the instance's declared disk size rather than a
// measurement — the emulator holds no bytes, and a size invented from nothing
// would enter a client's arithmetic.
func (p *Pack) createSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	instance, found := p.env.Store.Get(Name, kindInstance, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	var req createSnapshotRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Refused at intake, the way updateInstance refuses a name: the value
	// reaches no template today, and the rule of this repository is that a
	// structured format is protected at the door rather than at the render.
	if strings.ContainsAny(req.Name, "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindSnapshot, resource.Tenant{Provider: Name}, "ready", now)
	name, _ := instance.Attrs["name"].(string)
	res.Attrs = map[string]any{
		// Upstream names a snapshot after its instance when the caller does not.
		"name": orSnapshotName(req.Name, name, now),
		"size": instance.Attrs["disk-size"],
		// The instance is carried as a reference, which is how a client finds
		// what a snapshot belongs to and how revert knows where to go back.
		"instance": map[string]any{"id": instance.ID},
	}
	res.Runtime = map[string]string{runtimeSnapshotInstanceKey: instance.ID}
	p.env.Store.Put(res)

	p.writeOperation(w, p.operationReferring(nounSnapshot, res.ID))
}

// runtimeSnapshotInstanceKey links a snapshot to the instance it was taken from.
// In Runtime rather than read back out of Attrs, because the view is the API's
// and this is the emulator's own bookkeeping.
const runtimeSnapshotInstanceKey = "snapshot-instance"

func orSnapshotName(requested, instance string, now time.Time) string {
	if requested != "" {
		return requested
	}
	if instance == "" {
		instance = "instance"
	}
	return instance + "-" + now.UTC().Format("20060102150405")
}

func (p *Pack) listSnapshots(w http.ResponseWriter, _ *http.Request) {
	list := p.env.Store.List(kindSnapshot, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	out := make([]map[string]any, 0, len(list))
	for _, res := range list {
		out = append(out, p.snapshotView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func (p *Pack) getSnapshot(w http.ResponseWriter, r *http.Request) {
	res, found := p.env.Store.Get(Name, kindSnapshot, r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.snapshotView(res))
}

func (p *Pack) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, found := p.env.Store.Get(Name, kindSnapshot, id); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.env.Store.Delete(Name, kindSnapshot, id)
	p.writeOperation(w, p.operationReferring(nounSnapshot, id))
}

type revertSnapshotRequest struct {
	ID string `json:"id"`
}

// revertInstanceToSnapshot is the lifecycle verb the served instance surface was
// missing, and the one with a real precondition: upstream reverts a stopped
// instance, because the disk is replaced underneath it.
//
// The refusal is the interesting half. An emulator that accepted a revert on a
// running machine would let a plan through that the real cloud stops.
//
// TestRevertingARunningInstanceIsRefused fails without this.
func (p *Pack) revertInstanceToSnapshot(w http.ResponseWriter, r *http.Request) {
	// {id} rather than upstream's {instance-id}: every route on this base shares
	// one wildcard name, because the mux dispatches ":action" itself and a group
	// cannot carry two. The URL a client sends is identical either way — the
	// parameter's name is ours, the path is theirs.
	instanceID := r.PathValue("id")
	instance, found := p.env.Store.Get(Name, kindInstance, instanceID)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	var req revertSnapshotRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, found := p.env.Store.Get(Name, kindSnapshot, req.ID)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	// A snapshot belongs to the instance it was taken from, and reverting an
	// instance to somebody else's snapshot is not a thing the API offers.
	if snapshot.Runtime[runtimeSnapshotInstanceKey] != instanceID {
		writeError(w, http.StatusBadRequest, "the snapshot was not taken from this instance")
		return
	}
	if instance.State == runningState {
		writeError(w, http.StatusBadRequest,
			"the instance must be stopped before it can be reverted to a snapshot")
		return
	}

	p.writeOperation(w, p.operationReferring(nounInstance, instanceID))
}
