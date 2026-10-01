package scaleway

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// SCIM is how an organization provisions its IAM users from a directory it
// already runs — Entra ID, Okta — instead of creating them one by one. The
// product is a configuration on the organization plus bearer tokens the
// directory authenticates with.
//
// WHY THIS FAMILY IS SERVED, decided 2026-10-01 (#800). The nightly scan found
// `GetScimToken` new upstream, and it was the one operation of the family nobody
// had triaged: the other six were declined. Serving that one alone would have
// been the worst of the three options, and worth writing down because it reads
// as the cheapest: the route would have existed, answered 404 to every
// identifier, and counted as implemented in `coverage/scaleway-coverage.json` —
// a number this project exists to keep honest. Nothing could have created the
// token it reads, because creating one needs a SCIM configuration and enabling
// one was declined.
//
// So the chain is served end to end, and it is short:
//
//	POST   /organizations/{organization_id}/scim   -> Scim
//	GET    /organizations/{organization_id}/scim   -> Scim
//	DELETE /scim/{scim_id}
//	POST   /scim/{scim_id}/tokens                  -> {token, bearer_token}
//	GET    /scim/{scim_id}/tokens                  -> {scim_tokens, total_count}
//	GET    /scim-tokens/{id}                       -> ScimToken
//	DELETE /scim-tokens/{id}
//
// WHAT IS NOT CLAIMED. The SCIM endpoints themselves — the /scim/v2/Users and
// /scim/v2/Groups a directory actually calls — are not served, and the bearer
// token this answers authenticates nothing here. This is the control plane of
// the product, which is what a client configures; provisioning users from a
// directory is a second product and it is not emulated. Said here rather than
// implied, because a token that looks usable and is not would send somebody
// debugging their directory.
//
// AND THE BEARER TOKEN IS NOT KEPT. `CreateScimTokenResponse` carries it once
// and no read ever answers it again — which is how the real product behaves, and
// also the only shape that leaves no secret in the store for `PUT /_feint/state`
// to hand back. Nothing here needs to verify it, so nothing here stores it.
const (
	kindScim = "iam/scim"
	// The store kind, which is a type name and not a credential: gosec's G101
	// matches the word rather than the value, and the value is the string this
	// pack files a resource under — the same shape as kindSSHKey above it.
	kindScimToken = "iam/scim-token" //nolint:gosec // a store kind, not a secret
)

// scimView is the upstream `Scim`, which has exactly two fields.
func scimView(res *resource.Resource) map[string]any {
	return map[string]any{
		"id":         res.ID,
		"created_at": res.Created,
	}
}

// scimTokenView is the upstream `ScimToken`.
//
// `expires_at` is null, and that is a refusal rather than an omission: the SDK
// types it `*time.Time`, nothing upstream states a lifetime, and inventing one
// would put an expiry date in a client's state that this emulator would then
// have to honour. A token that does not expire is the honest answer to "we did
// not measure this".
func scimTokenView(res *resource.Resource) map[string]any {
	return map[string]any{
		"id":         res.ID,
		"scim_id":    textOf(res.Attrs["scim_id"]),
		"created_at": res.Created,
		"expires_at": nil,
	}
}

// scimConfiguration answers the account's SCIM configuration, if it has one.
//
// One per account, and the organization in the path is NOT compared against the
// pack's own constant. That rule is listSSHKeys', measured: the CLI names its
// configured organization on every call, nothing obliges that configuration to
// spell the emulator's, and comparing told `scw iam ssh-key list` that the key
// it had just created did not exist.
func (p *Pack) scimConfiguration() (*resource.Resource, bool) {
	all := p.env.Store.List(kindScim, resource.Tenant{Provider: Name})
	if len(all) == 0 {
		return nil, false
	}
	return all[0], true
}

// enableOrganizationScim turns SCIM on for the account.
//
// Idempotent: enabling twice answers the configuration that exists rather than
// a second one. The real product has one per organization, so a second
// identifier would be a configuration a client could never reach through the
// GET door, which takes an organization and not an id.
//
// TestEnablingScimTwiceAnswersTheSameConfiguration fails without this.
func (p *Pack) enableOrganizationScim(w http.ResponseWriter, r *http.Request) {
	if existing, found := p.scimConfiguration(); found {
		emulator.WriteJSON(w, http.StatusOK, scimView(existing))
		return
	}
	res := resource.New(p.env.NewID(), kindScim, resource.Tenant{Provider: Name}, "enabled", p.env.Now())
	res.Attrs = map[string]any{
		// The organization the client named, kept as it was sent. Published
		// nowhere — `Scim` has no such field — and here only so an operator
		// reading `/_feint/state` sees which identifier asked for it.
		"organization_id": r.PathValue("organization_id"),
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, scimView(res))
}

// getOrganizationScim reads the configuration, or 404 when SCIM is off.
func (p *Pack) getOrganizationScim(w http.ResponseWriter, r *http.Request) {
	existing, found := p.scimConfiguration()
	if !found {
		writeNotFound(w, "scim", r.PathValue("organization_id"))
		return
	}
	emulator.WriteJSON(w, http.StatusOK, scimView(existing))
}

// deleteScim turns SCIM off, and takes its tokens with it.
//
// The tokens go because they cannot outlive what they authenticate to: a token
// whose `scim_id` names nothing would still be answered by GetScimToken, which
// is the dangling read this pack refuses elsewhere. Upstream has no
// DeleteScimTokens, so nothing else would ever remove them.
//
// TestDeletingScimRemovesItsTokens fails without this.
func (p *Pack) deleteScim(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("scim_id")
	existing, found := p.scimConfiguration()
	if !found || existing.ID != id {
		writeNotFound(w, "scim", id)
		return
	}
	for _, token := range p.scimTokensOf(id) {
		p.env.Store.Delete(Name, kindScimToken, token.ID)
	}
	p.env.Store.Delete(Name, kindScim, id)
	w.WriteHeader(http.StatusNoContent)
}

// scimTokensOf answers the tokens of one configuration.
func (p *Pack) scimTokensOf(scimID string) []*resource.Resource {
	return filterResources(p.env.Store.List(kindScimToken, resource.Tenant{Provider: Name}),
		func(res *resource.Resource) bool { return textOf(res.Attrs["scim_id"]) == scimID })
}

// createScimToken mints a bearer token under a configuration.
//
// The bearer token is answered once and never stored — see the file comment.
// It is built from the emulator's own identifier source rather than from a
// random one, because every identifier here is deterministic for the tests that
// pin them.
//
// TestCreateScimTokenAnswersABearerTokenOnlyOnce fails without this.
func (p *Pack) createScimToken(w http.ResponseWriter, r *http.Request) {
	scimID := r.PathValue("scim_id")
	existing, found := p.scimConfiguration()
	if !found || existing.ID != scimID {
		writeNotFound(w, "scim", scimID)
		return
	}
	res := resource.New(p.env.NewID(), kindScimToken, resource.Tenant{Provider: Name}, "enabled", p.env.Now())
	res.Attrs = map[string]any{"scim_id": scimID}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"token":        scimTokenView(res),
		"bearer_token": "scim-" + p.env.NewID(),
	})
}

// listScimTokens lists the tokens of one configuration.
func (p *Pack) listScimTokens(w http.ResponseWriter, r *http.Request) {
	scimID := r.PathValue("scim_id")
	existing, found := p.scimConfiguration()
	if !found || existing.ID != scimID {
		writeNotFound(w, "scim", scimID)
		return
	}
	all := p.scimTokensOf(scimID)
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
	}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	tokens := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		tokens = append(tokens, scimTokenView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"scim_tokens": tokens,
		"total_count": len(all),
	})
}

// getScimToken is the operation the scan found new (#800), and it reads a token
// that something can actually create.
func (p *Pack) getScimToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindScimToken, id)
	if !found {
		writeNotFound(w, "scim_token", id)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, scimTokenView(res))
}

func (p *Pack) deleteScimToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, found := p.env.Store.Get(Name, kindScimToken, id); !found {
		writeNotFound(w, "scim_token", id)
		return
	}
	p.env.Store.Delete(Name, kindScimToken, id)
	w.WriteHeader(http.StatusNoContent)
}
