package outscale

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/sshkey"
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
	Filters filterSet `json:"Filters"`
	// ResultsPerPage pages like every other Read* — the probe sends it since
	// it started exercising the parameter (#156), and the unread-fields gate
	// of `mise run conformance` fails when a handler ignores it.
	ResultsPerPage *int `json:"ResultsPerPage"`
}

// keypairFilters are what a keypair answers from what is stored. Tags are not
// modelled on a keypair here, so they are refused.
var keypairFilters = stringFilters("KeypairNames", "KeypairFingerprints", "KeypairTypes")

type deleteKeypairRequest struct {
	KeypairName string `json:"KeypairName"`
	KeypairID   string `json:"KeypairId"`
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
	parsed, err := publicKeyOf(req.PublicKey)
	if err != nil {
		p.badRequest(w, "PublicKey is not an OpenSSH public key")
		return
	}

	now := p.env.Now()
	res := resource.New(newKeypairID(p.env.NewID()), kindKeypair, resource.Tenant{Provider: Name}, "available", now)
	res.Attrs = map[string]any{
		"KeypairName": req.KeypairName,
		// The algorithm the client sent, not a constant. Hardcoding
		// "ssh-rsa" made every key answer that, ed25519 ones included, and
		// KeypairType is a declared schema field a client can filter on.
		"KeypairType":        parsed.Algorithm,
		"KeypairFingerprint": parsed.FingerprintMD5(),
		// The material itself never leaves through the API: no Outscale
		// response carries a PublicKey field, so it lives out of Attrs, in
		// Runtime, where a view cannot pick it up by accident.
	}
	// The canonical form, not the bytes the client sent: what travels here is
	// either a line or a base64 envelope around one, and a file read into either
	// carries its trailing newline, which cloud-init refuses as a control
	// character. The Exoscale pack stores the same way, for the same measurement.
	res.Runtime = map[string]string{runtimePublicKey: parsed.String()}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Keypair":         keypairView(res),
		"ResponseContext": p.context(),
	})
}

// publicKeyOf reads the two forms this API receives, and nothing else.
//
// Outscale documents the field as "This value must be Base64-encoded"
// (osc-sdk-go/pkg/osc/client.gen.go:2256), and its own CLI took until v0.0.32 to
// obey: measured on 2026-09-14 against a local listener, `octl v0.0.31
// --PublicKey <the key>` puts the line on the wire verbatim, where v0.0.32 takes
// the flag as a file path and puts base64 of its bytes there. The real cloud
// answers 200 to the bare line — corpus/outscale, which is how the suite passed
// for a year — so both forms reach the real API, and an emulator that reads only
// one diverges from it. Reading only the bare one made every v0.0.32
// CreateKeypair answer 400 here.
//
// The envelope is not a way past the refusal createKeypair states. What comes
// out of the decoder goes through the same Parse, so the multi-line payload that
// opens a top-level key in a cloud-config is refused encoded exactly as it is
// bare — and the bare form is tried first, so a line is judged as a line rather
// than as accidental base64.
//
// TestAKeypairReadsTheEncodedFormTheApiDocuments and
// TestAnEncodedKeypairIsHeldToTheSameRefusals fail without this.
func publicKeyOf(value string) (sshkey.Key, error) {
	if parsed, err := sshkey.Parse(value); err == nil {
		return parsed, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return sshkey.Key{}, sshkey.ErrNotAKey
	}
	return sshkey.Parse(string(decoded))
}

func (p *Pack) readKeypairs(w http.ResponseWriter, r *http.Request) {
	var req readKeypairsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refusePageSize(w, req.ResultsPerPage) {
		return
	}
	if p.refuseFilters(w, req.Filters, keypairFilters) {
		return
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindKeypair, resource.Tenant{Provider: Name}) {
		name, _ := res.Attrs["KeypairName"].(string)
		fingerprint, _ := res.Attrs["KeypairFingerprint"].(string)
		keyType, _ := res.Attrs["KeypairType"].(string)
		if !matchesStrings(req.Filters, "KeypairNames", name) ||
			!matchesStrings(req.Filters, "KeypairFingerprints", fingerprint) ||
			!matchesStrings(req.Filters, "KeypairTypes", keyType) {
			continue
		}
		out = append(out, keypairView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Keypairs":        page(out, pageSize(req.ResultsPerPage)),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteKeypair(w http.ResponseWriter, r *http.Request) {
	var req deleteKeypairRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res := p.keypairByRef(req.KeypairName, req.KeypairID)
	if res == nil {
		p.notFound(w, "keypair", firstNonEmpty(req.KeypairName, req.KeypairID))
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

// keypairByRef resolves either form the API accepts. Their DeleteKeypairRequest
// declares KeypairId and KeypairName side by side, and the Terraform provider
// sends the id.
//
// TestAKeypairIsAddressableByIdAndByName fails without this.
func (p *Pack) keypairByRef(name, id string) *resource.Resource {
	if name != "" {
		return p.keypairByName(name)
	}
	if id == "" {
		return nil
	}
	// The store's own identifier, which is what keypairView publishes as
	// KeypairId. An attribute of the same name would be a second identity: the
	// first version of this fix added one, the view went on publishing res.ID,
	// and the two never matched.
	res, found := p.env.Store.Get(Name, kindKeypair, id)
	if !found {
		return nil
	}
	return res
}

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
	out := make(map[string]any, len(res.Attrs)+2)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["KeypairId"] = res.ID
	// Empty, never absent: the real cloud writes Tags on every keypair it
	// returns — measured in shapes/outscale.json, and the field gate (#88)
	// holds ReadKeypairs to it. Tag *filters* stay refused (keypairFilters),
	// which is a different claim: serving the field is shape, filtering on it
	// would be behaviour this pack does not model. Guarded so a stored list,
	// should tagging ever reach keypairs, wins over the empty default.
	if _, ok := out["Tags"]; !ok {
		out["Tags"] = []any{}
	}
	return out
}

// firstNonEmpty names whichever reference the client used, so a refusal quotes
// what was sent rather than an empty string.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
