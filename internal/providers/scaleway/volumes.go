package scaleway

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// A server always owns at least its root volume, and that volume is a resource
// of its own: the Terraform provider reads it back by ID right after creating
// the server, and a 404 there fails the apply with "failed to read instance
// volume". Serving the volume inline on the server is not enough.
//
// Shapes come from the SDK (api/instance/v1/instance_sdk.go): Volume, and the
// List/Get/Create/Update response envelopes.

const kindVolume = "instance/volume"

// runtimeServerKey links a volume to the server it was created for, so deleting
// the server can take its root volume with it. Out of Attrs: the wire shape
// carries the server as an object, not as a bare ID.
const runtimeServerKey = "server"

type createVolumeRequest struct {
	Name         string   `json:"name"`
	Project      string   `json:"project"`
	Organization string   `json:"organization"`
	Tags         []string `json:"tags"`
	VolumeType   string   `json:"volume_type"`
	Size         *uint64  `json:"size"`
	BaseVolume   string   `json:"base_volume"`
	BaseSnapshot string   `json:"base_snapshot"`
}

type updateVolumeRequest struct {
	Name *string   `json:"name"`
	Tags *[]string `json:"tags"`
	Size *uint64   `json:"size"`
}

func (p *Pack) listVolumes(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}

	all := p.env.Store.List(kindVolume, p.scopeOf(r, zone))
	// A substring, not an equality: instance_sdk.go documents the filter with its
	// own example — "vol" returns "myvolume" but not "data". Compared by equality
	// here, `scw instance volume list name=vol` came back empty against a volume
	// called myvolume, while the servers list next door had just been fixed to
	// match the SDK. TestVolumesFilterByNameLikeTheSDKSays fails without this.
	if name := r.URL.Query().Get("name"); name != "" {
		filtered := all[:0]
		for _, res := range all {
			if stored, _ := res.Attrs["name"].(string); strings.Contains(stored, name) {
				filtered = append(filtered, res)
			}
		}
		all = filtered
	}

	page := parsePage(r)
	start, end := page.slice(len(all))
	volumes := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		volumes = append(volumes, volumeView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"volumes":     volumes,
		"total_count": len(all),
	})
}

func (p *Pack) getVolume(w http.ResponseWriter, r *http.Request) {
	res, ok := p.volumeOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"volume": volumeView(res)})
}

func (p *Pack) createVolume(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}

	var req createVolumeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "name", Reason: "required"})
		return
	}

	size := uint64(rootVolumeSize)
	if req.Size != nil && *req.Size > 0 {
		size = *req.Size
	}
	project, organization := projectOf(req.Project)
	res := p.newVolume(zone, project, organization, req.Name,
		orDefault(req.VolumeType, "b_ssd"), size)
	res.Attrs["tags"] = orEmpty(req.Tags)
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"volume": volumeView(res)})
}

func (p *Pack) updateVolume(w http.ResponseWriter, r *http.Request) {
	res, ok := p.volumeOf(w, r)
	if !ok {
		return
	}

	var req updateVolumeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name != nil {
		res.Attrs["name"] = *req.Name
	}
	if req.Tags != nil {
		res.Attrs["tags"] = orEmpty(*req.Tags)
	}
	if req.Size != nil {
		res.Attrs["size"] = *req.Size
	}

	res.Updated = p.env.Now()
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"volume": volumeView(res)})
}

func (p *Pack) deleteVolume(w http.ResponseWriter, r *http.Request) {
	res, ok := p.volumeOf(w, r)
	if !ok {
		return
	}
	// The API refuses to delete a volume still attached, and a client that
	// destroys in the wrong order depends on that error to retry.
	if res.Runtime[runtimeServerKey] != "" {
		if _, stillThere := p.env.Store.Get(Name, kindServer, res.Runtime[runtimeServerKey]); stillThere {
			writePrecondition(w, "volume", res.ID,
				"the volume is attached to a server and cannot be deleted")
			return
		}
	}
	p.env.Store.Delete(Name, kindVolume, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

// newVolume builds a volume resource without storing it.
func (p *Pack) newVolume(zone, project, organization, name, volumeType string, size uint64) *resource.Resource {
	now := p.env.Now()
	return &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindVolume,
		Tenant:  resource.Tenant{Provider: Name, Project: project, Zone: zone},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"name":         name,
			"project":      project,
			"organization": organization,
			"size":         size,
			"volume_type":  volumeType,
			"tags":         []string{},
			"export_uri":   nil,
			"boot":         false,
		},
	}
}

// attachVolume records which server a volume belongs to, refusing a volume
// another live server already holds.
//
// The question lives here rather than in a handler because there are three
// doors onto it — POST /servers with a volumes map, PATCH /servers with the
// same, and POST /servers/{id}/attach-volume — and an audit walked every one
// that lacked the check. Only attach-volume had it, so a create naming another
// server's root volume moved it and left both servers listing it. CLAUDE.md
// names this exact shape: a control copied per caller is a control one caller
// is missing.
//
// A volume whose server no longer exists is free: terminate and delete both
// release their volumes, but a snapshot restored from another instance can
// carry the stale name, and refusing over a server nobody can see would be
// unfixable through the API.
//
// TestAttachingDoesNotStealAnotherServersVolume covers all three doors and
// fails without this.
func (p *Pack) attachVolume(vol *resource.Resource, server *resource.Resource, serverName string) error {
	if owner := vol.Runtime[runtimeServerKey]; owner != "" && owner != server.ID {
		if _, alive := p.env.Store.Get(Name, kindServer, owner); alive {
			return fmt.Errorf("volume %s is attached to server %s", vol.ID, owner)
		}
	}
	if vol.Runtime == nil {
		vol.Runtime = map[string]string{}
	}
	vol.Runtime[runtimeServerKey] = server.ID
	vol.Attrs["server_name"] = serverName
	return nil
}

// detachVolume releases a volume from its server, leaving it available. This is
// what deleting a server does on Scaleway: the disk outlives the machine.
func (p *Pack) detachVolume(vol *resource.Resource) {
	delete(vol.Runtime, runtimeServerKey)
	delete(vol.Attrs, "server_name")
	vol.Updated = p.env.Now()
}

// volumesOf returns the volumes attached to a server.
func (p *Pack) volumesOf(serverID string) []*resource.Resource {
	all := p.env.Store.List(kindVolume, resource.Tenant{Provider: Name})
	out := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if res.Runtime[runtimeServerKey] == serverID {
			out = append(out, res)
		}
	}
	return out
}

func (p *Pack) volumeOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return nil, false
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindVolume, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "volume", id)
		return nil, false
	}
	return res, true
}

// volumeView is the wire shape, shared by the volume endpoints and by the
// volume map a server carries: the two must not drift apart, because a client
// reads the same volume through both.
func volumeView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":                res.ID,
		"zone":              res.Tenant.Zone,
		"state":             res.State,
		"creation_date":     res.Created.Format(time.RFC3339),
		"modification_date": res.Updated.Format(time.RFC3339),
		"server":            nil,
	}
	for k, v := range res.Attrs {
		if k == "server_name" {
			continue
		}
		out[k] = v
	}
	if serverID := res.Runtime[runtimeServerKey]; serverID != "" {
		name, _ := res.Attrs["server_name"].(string)
		out["server"] = map[string]any{"id": serverID, "name": name}
	}
	return out
}
