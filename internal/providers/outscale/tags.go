package outscale

import (
	"net/http"
	"sort"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Tags are what the Terraform provider calls on almost every resource, and the
// first thing an `outscale_net` with a `tags` block does after creating it.
//
// They are stored on the resource they name rather than in a table of their
// own. A tag is an attribute of a Net or a Vm — the API addresses them by
// ResourceIds — and a second store would be a second place to keep in step, the
// mistake this repository has already paid for with volume ownership.
//
// ResourceType is derived from the identifier's prefix, which is where Outscale
// puts it: vpc-, subnet-, i-, key- are the pack's own shapes, declared once in
// ids.go. Deriving beats storing it: a stored type can disagree with the id it
// sits beside, and nothing would notice.

// taggable maps an identifier prefix to the kind that holds it, and to the
// ResourceType the API publishes.
var taggable = []struct {
	prefix       string
	kind         string
	resourceType string
}{
	{"vpc-", kindNet, "net"},
	{"subnet-", kindSubnet, "subnet"},
	{"i-", kindVM, "vm"},
	{"key-", kindKeypair, "keypair"},
}

// resourceOf finds what an identifier names, whatever kind it is.
func (p *Pack) resourceOf(id string) (*resource.Resource, string, bool) {
	for _, t := range taggable {
		if len(id) < len(t.prefix) || id[:len(t.prefix)] != t.prefix {
			continue
		}
		res, found := p.env.Store.Get(Name, t.kind, id)
		if !found {
			return nil, "", false
		}
		return res, t.resourceType, true
	}
	return nil, "", false
}

type tagsRequest struct {
	ResourceIDs []string `json:"ResourceIds"`
	Tags        []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	} `json:"Tags"`
}

// createTags adds or replaces tags on every named resource.
//
// Adding a key that already exists replaces its value, which is what the API
// does: a Tags block in Terraform is the desired state, and a second apply must
// not produce two entries for one key.
//
// TestTagsAreStoredOnTheResourceTheyName fails without this.
func (p *Pack) createTags(w http.ResponseWriter, r *http.Request) {
	var req tagsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if len(req.ResourceIDs) == 0 || len(req.Tags) == 0 {
		p.badRequest(w, "ResourceIds and Tags are both required")
		return
	}

	// Resolved before anything is written: tagging four resources and failing on
	// the fifth would leave a partial answer the client was told nothing about.
	targets := make([]*resource.Resource, 0, len(req.ResourceIDs))
	for _, id := range req.ResourceIDs {
		res, _, found := p.resourceOf(id)
		if !found {
			p.notFound(w, "resource", id)
			return
		}
		targets = append(targets, res)
	}

	for _, res := range targets {
		tags := tagsOf(res)
		for _, want := range req.Tags {
			tags[want.Key] = want.Value
		}
		res.Attrs["Tags"] = tagsList(tags)
		p.env.Store.Put(res)
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// deleteTags removes the named keys. A key that is not there is not an error:
// the client asked for a state, and that state is already reached.
func (p *Pack) deleteTags(w http.ResponseWriter, r *http.Request) {
	var req tagsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	for _, id := range req.ResourceIDs {
		res, _, found := p.resourceOf(id)
		if !found {
			p.notFound(w, "resource", id)
			return
		}
		tags := tagsOf(res)
		for _, want := range req.Tags {
			// A value given must match: DeleteTags removes a key only when the
			// value matches, which is how the API refuses to drop a tag someone
			// else changed under you.
			if want.Value != "" && tags[want.Key] != want.Value {
				continue
			}
			delete(tags, want.Key)
		}
		res.Attrs["Tags"] = tagsList(tags)
		p.env.Store.Put(res)
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// readTags answers the flat view: every tag of every resource, with what it is
// attached to. It is a different shape from the Tags a resource carries, which
// is why the API has both.
func (p *Pack) readTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters filterSet `json:"Filters"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, "ResourceIds", "Keys", "Values", "ResourceTypes") {
		return
	}

	out := make([]map[string]any, 0)
	for _, t := range taggable {
		for _, res := range p.env.Store.List(t.kind, resource.Tenant{Provider: Name}) {
			for key, value := range tagsOf(res) {
				if !matchesStrings(req.Filters, "ResourceIds", res.ID) ||
					!matchesStrings(req.Filters, "Keys", key) ||
					!matchesStrings(req.Filters, "Values", value) ||
					!matchesStrings(req.Filters, "ResourceTypes", t.resourceType) {
					continue
				}
				out = append(out, map[string]any{
					"Key":          key,
					"Value":        value,
					"ResourceId":   res.ID,
					"ResourceType": t.resourceType,
				})
			}
		}
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Tags":            out,
		"ResponseContext": p.context(),
	})
}

// tagsOf reads the tags a resource carries, as a map so a key can be replaced
// rather than repeated.
func tagsOf(res *resource.Resource) map[string]string {
	out := map[string]string{}
	entries, _ := res.Attrs["Tags"].([]any)
	for _, entry := range entries {
		tag, _ := entry.(map[string]any)
		key, _ := tag["Key"].(string)
		value, _ := tag["Value"].(string)
		if key != "" {
			out[key] = value
		}
	}
	return out
}

// tagsList renders the map back into the wire shape, sorted so two reads of an
// unchanged resource are identical — an order that moves is a permanent diff in
// a Terraform plan.
func tagsList(tags map[string]string) []any {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{"Key": key, "Value": tags[key]})
	}
	return out
}
