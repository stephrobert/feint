package exoscale

import (
	"net/http"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Templates a tenant owns, beside the fixed catalogue (#173).
//
// The catalogue stays exactly what it was, and that is the trap CLAUDE.md opens
// with: an emulator owns no inventory and must serve one anyway, or the client
// fails before it creates anything. What is added here is the other half — a
// template a client registered, or promoted from a snapshot, which is tenant data
// and lives in the store.
//
// Both are listed together, because a client resolving a template by name has no
// idea which of the two it is asking for. Serving them from separate endpoints
// would be an emulator detail leaking into an API the client reads as one list.

const (
	kindTemplate = "template"
	nounTemplate = "template"
)

// templateView publishes a stored template in the same shape the catalogue uses,
// so a client cannot tell a registered one from a built-in by its shape alone —
// only by what it says about itself.
func (p *Pack) templateView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":         res.ID,
		"created-at": res.Created.UTC().Format(time.RFC3339),
		"visibility": "private",
		"family":     "linux",
	}
	for key, value := range res.Attrs {
		out[key] = value
	}
	return out
}

type templateRequest struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	URL             string `json:"url"`
	Checksum        string `json:"checksum"`
	DefaultUser     string `json:"default-user"`
	PasswordEnabled *bool  `json:"password-enabled"`
	SSHKeyEnabled   *bool  `json:"ssh-key-enabled"`
	BootMode        string `json:"boot-mode"`
	// Sent by `exo compute instance-template register` on every call, declared
	// by register-template.Request and by the template schema itself, and read
	// by nothing here until a real client drove the route (#174): the field gate
	// named it the moment the suite registered its first template. A field a
	// client sends and a handler drops is a setting the client believes it made.
	ApplicationConsistentSnapshotEnabled *bool `json:"application-consistent-snapshot-enabled"`
}

func (r templateRequest) invalid() string {
	for field, value := range map[string]string{
		"name": r.Name, "description": r.Description,
		"url": r.URL, "default-user": r.DefaultUser,
	} {
		if strings.ContainsAny(value, "\n\r\x00") {
			return field + " carries control characters"
		}
	}
	return ""
}

// registerTemplate stores a template a client brought. The URL is recorded and
// never fetched: this emulator boots from its own images, and downloading a disk
// a test named would turn an offline suite into a network one.
//
// What the boot then does with it is the honest half, and it is the rule the
// machine layer already states: a template that resolves to nothing this runtime
// can run refuses the boot rather than substituting one. A registered template is
// a control-plane record until the day the image repository holds a match.
func (p *Pack) registerTemplate(w http.ResponseWriter, r *http.Request) {
	var req templateRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if bad := req.invalid(); bad != "" {
		writeError(w, http.StatusBadRequest, bad)
		return
	}

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindTemplate, resource.Tenant{Provider: Name}, "ready", now)
	res.Attrs = templateAttrs(req)
	p.env.Store.Put(res)
	p.writeOperation(w, p.operationReferring(nounTemplate, res.ID))
}

func templateAttrs(req templateRequest) map[string]any {
	attrs := map[string]any{
		"name":        req.Name,
		"description": req.Description,
		"url":         req.URL,
		"checksum":    req.Checksum,
		// The login the template declares, which is this provider's own shape:
		// Exoscale carries the user per template rather than per image family,
		// and machine.Boot.User exists because of it.
		"default-user":     orDefaultUser(req.DefaultUser),
		"password-enabled": boolOrTrue(req.PasswordEnabled),
		"ssh-key-enabled":  boolOrTrue(req.SSHKeyEnabled),
		// False when the client says nothing, unlike the two above: upstream
		// declares application-consistent snapshots as something a template opts
		// into, and defaulting it to true would promise a guarantee no emulated
		// snapshot can keep.
		"application-consistent-snapshot-enabled": boolOrFalse(req.ApplicationConsistentSnapshotEnabled),
	}
	if req.BootMode != "" {
		attrs["boot-mode"] = req.BootMode
	}
	return attrs
}

func orDefaultUser(user string) string {
	if user == "" {
		return "ubuntu"
	}
	return user
}

func boolOrTrue(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func boolOrFalse(value *bool) bool {
	return value != nil && *value
}

// copyTemplate and updateTemplate act on a stored template only. The catalogue is
// not editable, and saying so with a 404 rather than a silent success is the
// difference between a client that learns and a client that plans against a
// change that never happened.
func (p *Pack) copyTemplate(w http.ResponseWriter, r *http.Request) {
	source, found := p.env.Store.Get(Name, kindTemplate, r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	var req templateRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if bad := req.invalid(); bad != "" {
		writeError(w, http.StatusBadRequest, bad)
		return
	}

	copied := resource.New(p.env.NewID(), kindTemplate, resource.Tenant{Provider: Name}, "ready", p.env.Now())
	copied.Attrs = map[string]any{}
	for key, value := range source.Attrs {
		copied.Attrs[key] = value
	}
	if req.Name != "" {
		copied.Attrs["name"] = req.Name
	}
	if req.Description != "" {
		copied.Attrs["description"] = req.Description
	}
	p.env.Store.Put(copied)
	p.writeOperation(w, p.operationReferring(nounTemplate, copied.ID))
}

func (p *Pack) updateTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req templateRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if bad := req.invalid(); bad != "" {
		writeError(w, http.StatusBadRequest, bad)
		return
	}

	// Inside store.Update, which is read-modify-write under the store's own write
	// lock — the shape #211 established as this pack's, and the one a new
	// resource must not depart from.
	err := p.env.Store.Update(Name, kindTemplate, id, func(stored *resource.Resource) error {
		if req.Name != "" {
			stored.Attrs["name"] = req.Name
		}
		if req.Description != "" {
			stored.Attrs["description"] = req.Description
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounTemplate, id))
}

func (p *Pack) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, found := p.env.Store.Get(Name, kindTemplate, id); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.env.Store.Delete(Name, kindTemplate, id)
	p.writeOperation(w, p.operationReferring(nounTemplate, id))
}

type promoteSnapshotRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// promoteSnapshotToTemplate is the join between the two families, and the reason
// they were triaged together: it is how a client turns a machine it configured
// into something it can boot again, which is the golden-image path Scaleway
// already serves.
func (p *Pack) promoteSnapshotToTemplate(w http.ResponseWriter, r *http.Request) {
	snapshot, found := p.env.Store.Get(Name, kindSnapshot, r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	var req promoteSnapshotRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.ContainsAny(req.Name+req.Description, "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name or description carries control characters")
		return
	}

	name := req.Name
	if name == "" {
		name, _ = snapshot.Attrs["name"].(string)
	}
	res := resource.New(p.env.NewID(), kindTemplate, resource.Tenant{Provider: Name}, "ready", p.env.Now())
	res.Attrs = map[string]any{
		"name":             name,
		"description":      req.Description,
		"size":             snapshot.Attrs["size"],
		"default-user":     "ubuntu",
		"password-enabled": true,
		"ssh-key-enabled":  true,
	}
	// Kept so a reader of the store can tell where a template came from. Not in
	// the view: upstream publishes no such field, and inventing one is the thing
	// rule 4 forbids.
	res.Runtime = map[string]string{runtimeTemplateSnapshotKey: snapshot.ID}
	p.env.Store.Put(res)

	p.writeOperation(w, p.operationReferring(nounTemplate, res.ID))
}

const runtimeTemplateSnapshotKey = "template-snapshot"
