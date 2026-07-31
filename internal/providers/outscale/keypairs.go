package outscale

import (
	"crypto/md5" //nolint:gosec // fingerprint format, not a security control
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Keypairs are on the critical path to a machine anyone can log into: without
// one, a client creates a VM it cannot reach, which makes every other assertion
// about the machine untestable.
//
// Outscale has two modes on CreateKeypair, and they differ in what comes back.
// With a PublicKey the caller supplies one and keeps its private half, so the
// response carries no PrivateKey. Without one, Outscale generates the pair and
// returns the private key once, never again. The emulator honours the first and
// refuses the second: generating a keypair means minting a real private key,
// and a local emulator handing out one that looks usable is worse than one that
// says plainly it does not do that.

type createKeypairRequest struct {
	KeypairName string `json:"KeypairName"`
	PublicKey   string `json:"PublicKey"`
}

type readKeypairsRequest struct {
	Filters struct {
		KeypairNames []string `json:"KeypairNames"`
	} `json:"Filters"`
}

type deleteKeypairRequest struct {
	KeypairName string `json:"KeypairName"`
}

func (p *Pack) createKeypair(w http.ResponseWriter, r *http.Request) {
	var req createKeypairRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.KeypairName == "" {
		p.badRequest(w, "KeypairName is required")
		return
	}
	if p.keypairExists(req.KeypairName) {
		p.conflict(w, "a keypair named "+req.KeypairName+" already exists")
		return
	}
	if req.PublicKey == "" {
		p.badRequest(w, "PublicKey is required: this emulator does not generate keypairs, "+
			"because it would have to hand out a private key that only looks usable")
		return
	}
	// Refused at entry, before the store, the way the Scaleway pack refuses an
	// SSH key. The value is rendered into a cloud-config by text/template, and a
	// newline in it opens a top-level YAML key; a value that is not a key at all
	// produces a machine that boots, holds the wrong bytes and refuses every
	// login — the failure machines.go warns about, arriving through the door
	// nobody guarded.
	//
	// TestAKeypairRefusesWhatIsNotAKey fails without this.
	if !looksLikePublicKey(req.PublicKey) {
		p.badRequest(w, "PublicKey is not an OpenSSH public key")
		return
	}

	now := p.env.Now()
	res := &resource.Resource{
		ID:      newKeypairID(p.env.NewID()),
		Kind:    kindKeypair,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"KeypairName":        req.KeypairName,
			"KeypairType":        "ssh-rsa",
			"KeypairFingerprint": fingerprint(req.PublicKey),
			// The material itself never leaves through the API: no Outscale
			// response carries a PublicKey field, so it lives out of Attrs, in
			// Runtime, where a view cannot pick it up by accident.
		},
		Runtime: map[string]string{runtimePublicKey: strings.TrimSpace(req.PublicKey)},
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Keypair":         keypairView(res),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) readKeypairs(w http.ResponseWriter, r *http.Request) {
	var req readKeypairsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	wanted := make(map[string]bool, len(req.Filters.KeypairNames))
	for _, name := range req.Filters.KeypairNames {
		wanted[name] = true
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindKeypair, resource.Tenant{Provider: Name}) {
		name, _ := res.Attrs["KeypairName"].(string)
		if len(wanted) > 0 && !wanted[name] {
			continue
		}
		out = append(out, keypairView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Keypairs":        out,
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteKeypair(w http.ResponseWriter, r *http.Request) {
	var req deleteKeypairRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res := p.keypairByName(req.KeypairName)
	if res == nil {
		p.notFound(w, "keypair", req.KeypairName)
		return
	}
	p.env.Store.Delete(Name, kindKeypair, res.ID)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"ResponseContext": p.context(),
	})
}

// runtimePublicKey is where the key material is kept. Runtime, never Attrs, so
// it cannot reach an API response through a view that copies every attribute.
const runtimePublicKey = "public_key"

func (p *Pack) keypairByName(name string) *resource.Resource {
	if name == "" {
		return nil
	}
	for _, res := range p.env.Store.List(kindKeypair, resource.Tenant{Provider: Name}) {
		if got, _ := res.Attrs["KeypairName"].(string); got == name {
			return res
		}
	}
	return nil
}

func (p *Pack) keypairExists(name string) bool { return p.keypairByName(name) != nil }

// authorizedKeys returns the key a machine should boot with. Outscale attaches
// one keypair to a VM by name, where Scaleway injects every key of the project,
// so this takes a name rather than a tenant.
func (p *Pack) authorizedKeys(name string) []string {
	res := p.keypairByName(name)
	if res == nil || res.Runtime == nil {
		return nil
	}
	if key := res.Runtime[runtimePublicKey]; key != "" {
		return []string{key}
	}
	return nil
}

func keypairView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+1)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["KeypairId"] = res.ID
	return out
}

// fingerprint is the MD5 digest of the key material, colon-separated, which is
// the shape ssh-keygen -l prints and what a client compares against. MD5 is the
// format, not a security choice: nothing here authenticates anything.
func fingerprint(publicKey string) string {
	sum := md5.Sum([]byte(strings.TrimSpace(publicKey))) //nolint:gosec // format, not security
	hexed := hex.EncodeToString(sum[:])
	parts := make([]string, 0, len(hexed)/2)
	for i := 0; i < len(hexed); i += 2 {
		parts = append(parts, hexed[i:i+2])
	}
	return strings.Join(parts, ":")
}

// looksLikePublicKey accepts the OpenSSH one-line form.
//
// The one-line part is the security check rather than tidiness: the value is
// concatenated into a YAML document by text/template, and strings.Fields splits
// on newlines exactly like spaces, so the control character test has to come
// first. Same reasoning, and the same order, as the Scaleway pack's own check.
func looksLikePublicKey(key string) bool {
	if strings.ContainsAny(key, "\n\r\x00") {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(key))
	if len(fields) < 2 {
		return false
	}
	switch fields[0] {
	case "ssh-rsa", "ssh-ed25519", "ssh-dss",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return true
	}
	return false
}
