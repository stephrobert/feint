package exoscale

import (
	"net/http"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/sshkey"
)

// SSH keys, which are on the critical path of a create and were not obvious.
//
// `exo compute instance create` registers a key of its own before it posts the
// instance — it generates a pair, calls POST /ssh-key, and fails the whole
// command on a 404. Nothing in the API description says the create depends on
// it; it was found by putting a logging proxy between the CLI and the emulator
// and reading what actually went past.
//
// That is the third time this pack shape has appeared: Scaleway resolves its
// catalogue before creating, Outscale reads its types and images, Exoscale
// registers a key. The inventory a client walks before it commits is never the
// route it says it is calling.

const kindSSHKey = "ssh-key"

type registerSSHKeyRequest struct {
	Name      string `json:"name"`
	PublicKey string `json:"public-key"`
}

func (p *Pack) registerSSHKey(w http.ResponseWriter, r *http.Request) {
	var req registerSSHKeyRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case req.Name == "":
		writeError(w, http.StatusBadRequest, "name is required")
		return
	case req.PublicKey == "":
		writeError(w, http.StatusBadRequest, "public-key is required")
		return
	}
	// The same reader the other two packs use. Nothing was checked here: a name
	// carrying a newline and a multi-line "key" were both stored verbatim and
	// restituted, and cloudinit renders YAML with text/template, which
	// concatenates without escaping. There is no injection while the material is
	// never rendered — and the fix below renders it, which is exactly why this
	// comes first.
	//
	// TestExoscaleRefusesWhatIsNotAKey fails without this.
	parsed, err := sshkey.Parse(req.PublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "public-key is not an OpenSSH public key")
		return
	}
	if strings.ContainsAny(req.Name, "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}
	if _, exists := p.env.Store.Get(Name, kindSSHKey, req.Name); exists {
		writeError(w, http.StatusConflict, "an SSH key named "+req.Name+" already exists")
		return
	}

	now := p.env.Now()
	res := &resource.Resource{
		// The name is the identifier: every route addresses a key by
		// /ssh-key/{name}, so a generated id would be a second identity nobody
		// can reach.
		ID:      req.Name,
		Kind:    kindSSHKey,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "registered",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"name":        req.Name,
			"fingerprint": parsed.FingerprintMD5(),
		},
		// The key material, which the API does not publish and a machine needs.
		// Dropping it meant a registered key could never open the machine it was
		// attached to: the instance booted with empty cloud-init, no user, no
		// sshd on a minimal image — and the pack published an address on it.
		//
		// Runtime rather than Attrs, because no route may return it.
		//
		// TestAnExoscaleKeyReachesTheMachine fails without this.
		// The canonical form, not the bytes the client sent: a key read from a
		// file carries its trailing newline, and cloud-init refuses that.
		Runtime: map[string]string{"public-key": parsed.String()},
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, p.operationFor(res.ID, "register-ssh-key"))
}

func (p *Pack) listSSHKeys(w http.ResponseWriter, _ *http.Request) {
	list := p.env.Store.List(kindSSHKey, resource.Tenant{Provider: Name})
	keys := make([]map[string]any, 0, len(list))
	for _, res := range list {
		keys = append(keys, sshKeyView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ssh-keys": keys})
}

func (p *Pack) getSSHKey(w http.ResponseWriter, r *http.Request) {
	res, ok := p.env.Store.Get(Name, kindSSHKey, r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "no SSH key named "+r.PathValue("name"))
		return
	}
	emulator.WriteJSON(w, http.StatusOK, sshKeyView(res))
}

func (p *Pack) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := p.env.Store.Get(Name, kindSSHKey, name); !ok {
		writeError(w, http.StatusNotFound, "no SSH key named "+name)
		return
	}
	p.env.Store.Delete(Name, kindSSHKey, name)
	emulator.WriteJSON(w, http.StatusOK, p.operationFor(name, "delete-ssh-key"))
}

// sshKeyView is the whole of what the API publishes: a name and a fingerprint.
// The public key is deliberately absent, because their schema does not declare
// it — ssh-key carries two properties and nothing else, and the contract check
// fails a response that adds one.
func sshKeyView(res *resource.Resource) map[string]any {
	return map[string]any{
		"name":        res.Attrs["name"],
		"fingerprint": res.Attrs["fingerprint"],
	}
}
