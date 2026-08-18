package scaleway

import (
	"net/http"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/sshkey"
)

// SSH keys are what turn an emulated server into a machine you can log into.
//
// Scaleway attaches them to the PROJECT, not to the server: there is no
// key_name field on a create request, unlike EC2. Every key of the project is
// injected into every server the project boots. Emulating that faithfully means
// serving the IAM product, and it is why the machine driver can hand a working
// cloud-init to Incus.

// kindSSHKey is the store kind for IAM SSH keys.
const kindSSHKey = "iam/ssh-key"

type createSSHKeyRequest struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	ProjectID string `json:"project_id"`
}

type updateSSHKeyRequest struct {
	Name     *string `json:"name"`
	Disabled *bool   `json:"disabled"`
}

func (p *Pack) listSSHKeys(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope := resource.Tenant{Provider: Name}
	switch {
	case q.Get("project_id") != "":
		scope.Project = q.Get("project_id")
	case q.Get("organization_id") != "":
		// The whole account, never an equality against the pack's own
		// constant: one organization lives here (scopeOf's rule, and
		// projectOf says why the identifier is not compared). Compared, it
		// told `scw iam ssh-key list` the key it had just created did not
		// exist — the CLI names its configured organization on every list,
		// and nothing obliges that configuration to spell the emulator's.
	}
	all := p.env.Store.List(kindSSHKey, scope)
	if name := q.Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	// Read as an equality on the field when present, absent means everything:
	// the same shape as vpc/v2's is_default and dhcp_enabled. The SDK comment
	// ("defines whether to include disabled SSH keys") could also read as a
	// widener over a default exclusion, but that default would be invented —
	// nothing upstream states one, and this pack has always answered a bare
	// list with every key.
	if wantDisabled, present := queryBool(q, "disabled"); present {
		all = filterResources(all, func(res *resource.Resource) bool {
			disabled, _ := res.Attrs["disabled"].(bool)
			return disabled == wantDisabled
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"updated_at": cmpUpdated,
		"name":       cmpName,
	}, all) {
		return
	}

	page := parsePage(r)
	start, end := page.slice(len(all))
	keys := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		keys = append(keys, sshKeyView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"ssh_keys":    keys,
		"total_count": len(all),
	})
}

func (p *Pack) createSSHKey(w http.ResponseWriter, r *http.Request) {
	var req createSSHKeyRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if strings.TrimSpace(req.PublicKey) == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "public_key", Reason: "required"})
		return
	}
	// The API rejects anything that is not an SSH public key, and so must the
	// emulator: a malformed key silently breaks every later login attempt.
	if !sshkey.Valid(req.PublicKey) {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "public_key",
			Reason:       "constraint",
			HelpMessage:  "not an OpenSSH public key",
		})
		return
	}

	// IAM keys belong to a project, and the organization above it is the
	// account, never the same identifier.
	project, organization := projectOf(req.ProjectID)
	now := p.env.Now()

	res := resource.New(p.env.NewID(), kindSSHKey, resource.Tenant{Provider: Name, Project: project}, "enabled", now)
	res.Attrs = map[string]any{
		"name":            orDefault(req.Name, "key"),
		"public_key":      strings.TrimSpace(req.PublicKey),
		"fingerprint":     sshkey.FingerprintMD5(req.PublicKey),
		"project_id":      project,
		"organization_id": organization,
		"disabled":        false,
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusCreated, sshKeyView(res))
}

func (p *Pack) getSSHKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, ok := p.env.Store.Get(Name, kindSSHKey, id)
	if !ok {
		writeNotFound(w, "ssh_key", id)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, sshKeyView(res))
}

func (p *Pack) updateSSHKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, ok := p.env.Store.Get(Name, kindSSHKey, id)
	if !ok {
		writeNotFound(w, "ssh_key", id)
		return
	}

	var req updateSSHKeyRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name != nil {
		res.Attrs["name"] = *req.Name
	}
	if req.Disabled != nil {
		res.Attrs["disabled"] = *req.Disabled
	}
	if !p.env.Store.Commit(res, p.env.Now()) {
		writeNotFound(w, "ssh key", res.ID)
		return
	}

	emulator.WriteJSON(w, http.StatusOK, sshKeyView(res))
}

func (p *Pack) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !p.env.Store.Delete(Name, kindSSHKey, id) {
		writeNotFound(w, "ssh_key", id)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authorizedKeys returns the enabled public keys of a project, which is what
// gets injected into every machine that project boots.
func (p *Pack) authorizedKeys(project string) []string {
	var keys []string
	for _, res := range p.env.Store.List(kindSSHKey, resource.Tenant{Provider: Name, Project: project}) {
		if disabled, _ := res.Attrs["disabled"].(bool); disabled {
			continue
		}
		if pub, _ := res.Attrs["public_key"].(string); pub != "" {
			keys = append(keys, pub)
		}
	}
	return keys
}

func sshKeyView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+3)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["id"] = res.ID
	out["created_at"] = res.Created.Format(time.RFC3339)
	out["updated_at"] = res.Updated.Format(time.RFC3339)
	return out
}
