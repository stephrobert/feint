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
// puts it. Deriving beats storing it: a stored type can disagree with the id it
// sits beside, and nothing would notice.
//
// The values are the `TagResourceType` enum of the official SDK
// (`osc-sdk-go/pkg/osc/client.gen.go`, and its source in `patch.yaml`), not
// names of our own. Two of the four this table first carried were invented —
// `net` where Outscale says `vpc`, `vm` where it says `instance` — so a client
// filtering ReadTags on `instance` matched nothing. Their OpenAPI declares
// ResourceType as a bare string, which is why no contract check could see it.
//
// The table itself is the defect #99 reported: it held four prefixes, written
// when the pack served four kinds, and 0.6.0 added ten more without touching
// it. CreateTags therefore answered "the resource igw-… does not exist" about a
// resource the emulator had just created and was serving.
// TestEveryIdentifierPrefixIsTriagedForTagging fails when a prefix is added and
// this table is not, which is the only thing that stops it happening an
// eleventh time.

// taggable maps an identifier prefix to the kind that holds it, and to the
// ResourceType the API publishes.
//
// An empty resourceType means the resource carries Tags upstream while the SDK
// enum declares no value naming it: tags are stored and returned on the
// resource, and ReadTags leaves it out rather than invent a name. See
// notTaggable below for what carries no tags at all.
var taggable = []struct {
	prefix       string
	kind         string
	resourceType string
}{
	{"vpc-", kindNet, "vpc"},
	{"subnet-", kindSubnet, "subnet"},
	{"i-", kindVM, "instance"},
	{"key-", kindKeypair, "keypair"},
	{"vol-", kindVolume, "volume"},
	{"snap-", kindSnapshot, "snapshot"},
	{"ami-", kindImage, "image"},
	{"sg-", kindSecurityGroup, "security-group"},
	{"rtb-", kindRouteTable, "route-table"},
	{"eipalloc-", kindPublicIP, "public-ip"},
	{"eni-", kindNic, "network-interface"},
	{"dopt-", kindDhcpOptions, "dhcpoptions"},
	{"nat-", kindNatService, "natgateway"},

	// An internet service carries Tags in their own schema — the Terraform
	// provider sets them, which is how #99 was found — but the SDK's enum lists
	// twenty values and none of them names one. So it is taggable, and ReadTags
	// cannot say what type it is without inventing a name. docs/limits.md
	// records the gap.
	{"igw-", kindInternetService, ""},
}

// notTaggable is the other half of the same triage, and it exists for the same
// reason Declined() does: "carries no tags" and "nobody has looked" must not be
// indistinguishable. Every identifier prefix the pack mints is in one table or
// the other, and a test proves it.
var notTaggable = map[string]string{
	"eipassoc-": "the link between a public IP and its target, not a resource: their schema declares no Tags",
	"rtbassoc-": "the link between a route table and a subnet, not a resource: their schema declares no Tags",
	"sgr-":      "a rule inside a security group: SecurityGroupRule declares no Tags, the group it belongs to does",
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
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
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
		// A kind the SDK enum does not name is tagged and readable on the
		// resource itself, and left out of this flat view: every row here
		// carries a ResourceType, and there is no honest value to put in it.
		// TestReadTagsOmitsAKindUpstreamDoesNotName fails without this.
		if t.resourceType == "" {
			continue
		}
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
	// Paged like every other Read*. This flat view was one of the two lists of
	// the pack that ignored ResultsPerPage — found the day the probe started
	// holding paged answers to the size they asked (#156); the probe run of
	// `mise run conformance` fails without it.
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Tags":            page(out, req.ResultsPerPage),
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
