package probe

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/stephrobert/feint/internal/contract"
)

// minimalBody builds the smallest request the contract accepts: the required
// fields, plus the optional fields the run can honestly satisfy — an identifier
// the run already holds, a name the run already recorded, or a field the value
// vocabulary below covers.
//
// Optional fields outside those three rules are never filled. Filling them
// would mean choosing values the document does not constrain — a commercial
// type, an image name — and those live in the emulated catalogue, not in the
// schema. A probe that guessed them would be inventing a format, which no part
// of this emulator is allowed to do.
//
// A required field that is an identifier is taken from what earlier calls
// returned. When nothing produced it, this fails with the field named, and the
// caller skips the step rather than sending a value nobody can justify.
func minimalBody(doc *contract.Doc, step Step, schemaName string, pool *pool) (map[string]any, error) {
	return buildBody(doc, step, schemaName, pool, true)
}

func buildBody(doc *contract.Doc, step Step, schemaName string, pool *pool, top bool) (map[string]any, error) {
	body := map[string]any{}
	if schemaName == "" {
		// No request schema. Either the operation takes no body, or the document
		// declares it inline and the extraction could not name it — which is
		// what every Scaleway operation does, and why an empty body is the only
		// honest thing to send.
		return body, nil
	}

	schema, known := doc.Schemas[schemaName]
	if !known {
		return nil, fmt.Errorf("the contract has no schema named %s", schemaName)
	}

	for _, field := range schema.Required {
		prop, declared := schema.Properties[field]
		if !declared {
			return nil, fmt.Errorf("%s requires %s and does not declare it", schemaName, field)
		}
		value, err := valueFor(doc, step, field, prop, pool)
		if err != nil {
			return nil, err
		}
		body[field] = value
	}

	// The seeding half (#163): an optional field is filled when, and only when,
	// the run already holds what it names. This is what lets a create succeed
	// instead of refusing for the want of a field its own schema marks
	// optional — instance/v1's CreateVolume refuses a body without name, block's
	// CreateSnapshot refuses one without volume_id, and both fields are
	// optional in the extracted document. TestASeededCreateFillsWhatTheRunHolds
	// fails without this loop.
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, field := range names {
		if _, done := body[field]; done {
			continue
		}
		value, ok := optionalValue(doc, step, schemaName, field, schema.Properties[field], pool, top)
		if ok {
			body[field] = value
		}
	}
	return body, nil
}

// optionalValue fills one optional field, under the rules that keep a fill
// honest: a value the document itself pins (an enum), a format the vocabulary
// covers, an identifier or a name the run already holds, or a field the hint
// table names. Reports false when the field should stay absent.
//
// The rules are self-limiting on purpose: an identifier is filled only when a
// value of its own type exists, so kms_key_id or nexthop_vpc_connector_id —
// fields whose type nothing in a run produces — stay absent, and the refusals
// the emulator reserves for them stay reachable.
func optionalValue(doc *contract.Doc, step Step, schemaName, field string, prop contract.Property, pool *pool, top bool) (any, bool) {
	fold := foldField(field)

	// A field the value vocabulary marks as fillable-when-optional: today the
	// CIDR family, whose absence is what made vpc/v2's CreateRoute unreachable.
	if sample, vocab := cidrFields[fold]; vocab {
		if prop.Type == "" || prop.Type == "string" {
			return sample, true
		}
	}
	hinted := fillWhenOptional[fold] || fillWhenOptional[qualifiedField(schemaName, fold)]
	if hinted && prop.Ref != "" {
		nested, err := buildBody(doc, step, prop.Ref, pool, false)
		if err != nil {
			return nil, false
		}
		return nested, true
	}
	if hinted && prop.Type == "array" && prop.Items != nil && prop.Items.Ref != "" {
		item, err := buildBody(doc, step, prop.Items.Ref, pool, false)
		if err != nil || len(item) == 0 {
			return nil, false
		}
		return []any{item}, true
	}

	// The format vocabulary's optional half: a public key or a MAC left out of
	// a body is, on every emulated path that names one, the whole reason the
	// call refuses — Outscale's CreateKeypair deliberately refuses to invent a
	// keypair, and IPAM's attach family validates the MAC before anything
	// else. An optional enum is filled too, from the document's own values:
	// Scaleway's ServerAction without an action is a refusal by definition.
	if sample, known := sampleFor(field); known && (prop.Type == "" || prop.Type == "string") {
		return sample, true
	}
	if len(prop.Enum) > 0 {
		return chooseEnum(prop.Enum), true
	}
	if prop.Ref != "" {
		if nested, known := doc.Schemas[prop.Ref]; known && nested.Properties == nil && len(nested.Enum) > 0 {
			return chooseEnum(nested.Enum), true
		}
	}

	// An identifier the run holds. Same take as a required identifier, minus
	// the failure: absence means absence, not a skip.
	if isID(field) && (prop.Type == "" || prop.Type == "string") {
		if value, found := pool.take(step.Product, typeOfIDField(field)); found {
			return value, true
		}
		return nil, false
	}
	// An undescribed object named after a resource the run holds, same rule as
	// valueFor's: {"instance": {"id": …}} is how Exoscale bodies point at one.
	if prop.Type == "object" && prop.Ref == "" {
		if value, found := pool.take(step.Product, fold); found {
			return map[string]any{"id": value}, true
		}
	}

	// A name the run recorded, for the providers that address a resource by
	// name: Outscale's DeleteKeypair carries KeypairName and no identifier at
	// all. A bare "name" on a create is filled with the probe's own constant,
	// which is also what makes the created resource findable by name later.
	if strings.HasSuffix(fold, "name") && (prop.Type == "" || prop.Type == "string") {
		if value, found := pool.takeName(strings.TrimSuffix(fold, "name")); found {
			return value, true
		}
		if top && fold == "name" && step.Kind == kindCreate {
			return probeValue, true
		}
	}
	return nil, false
}

// qualifiedField keys a hint by the schema it applies to:
// "setsecuritygrouprules.request.rules".
func qualifiedField(schemaName, fold string) string {
	leaf := schemaName
	if slash := strings.LastIndex(leaf, "/"); slash >= 0 {
		leaf = leaf[slash+1:]
	}
	if dot := strings.Index(leaf, "."); dot >= 0 {
		leaf = leaf[dot+1:]
	}
	return foldField(leaf) + "." + fold
}

// probeValue is the string sent for every unconstrained text field.
const probeValue = "feint-probe"

// chooseEnum picks one value out of an enumeration, deterministically: the
// first value that is not the "unspecified" sentinel. Scaleway's documents put
// the proto3 zero value first (unknown_action, unknown_direction), a value the
// emulator — like the real API — refuses; sending it would measure the
// sentinel, not the operation (TestTheEnumSentinelIsNotChosen).
func chooseEnum(values []any) any {
	for _, v := range values {
		if text, ok := v.(string); ok && strings.HasPrefix(strings.ToLower(text), "unknown") {
			continue
		}
		return v
	}
	return values[0]
}

// valueFor produces one required field's value, from the pool when it is an
// identifier and from the schema when the schema constrains it. Required is
// what turns an empty pool into an error: the call cannot be built honestly.
func valueFor(doc *contract.Doc, step Step, field string, prop contract.Property, pool *pool) (any, error) {
	// An enumerated field has its answer in the document. Always the same one,
	// so two runs send the same thing.
	if len(prop.Enum) > 0 {
		return chooseEnum(prop.Enum), nil
	}

	// The value vocabulary: fields whose type is a string to the schema and a
	// format to the emulator. Checked before the identifier rule so a
	// public_key is never mistaken for one.
	if sample, known := sampleFor(field); known {
		return sample, nil
	}

	switch {
	case prop.Type == "array":
		if prop.Items == nil {
			return []any{}, nil
		}
		item, err := valueFor(doc, step, singular(field), *prop.Items, pool)
		if err != nil {
			return nil, err
		}
		return []any{item}, nil

	case prop.Ref != "":
		nested, known := doc.Schemas[prop.Ref]
		if known && nested.Properties == nil && nested.Type != "" {
			// A reference to a bare scalar — Scaleway's enum types, like
			// instance/v1's Arch. Before this, the nested walk answered an
			// object and the emulator refused the JSON type itself
			// (TestARefToAScalarSchemaYieldsAScalar).
			return valueFor(doc, step, field, contract.Property{Type: nested.Type, Enum: nested.Enum}, pool)
		}
		built, err := buildBody(doc, step, prop.Ref, pool, false)
		if err != nil {
			return nil, err
		}
		// A reference named after a resource the run holds is that resource:
		// Exoscale's create-instance says {"template": {…}}, the template
		// schema declares an id, and the id of a real template is the one
		// honest thing to put there (TestASeededCreateFillsWhatTheRunHolds).
		if _, hasID := built["id"]; !hasID {
			if _, declares := nested.Properties["id"]; declares {
				if value, found := pool.take(step.Product, foldField(field)); found {
					built["id"] = value
				}
			}
		}
		return built, nil

	case prop.Type == "object":
		// An object the document does not describe. When its field names a
		// resource the run holds — Exoscale's attach bodies carry
		// {"instance": {"id": …}} — the identifier is the one honest thing to
		// put inside; otherwise an empty object is.
		if value, found := pool.take(step.Product, foldField(field)); found {
			return map[string]any{"id": value}, nil
		}
		return map[string]any{}, nil

	case prop.Type == "boolean":
		return false, nil
	case prop.Type == "integer", prop.Type == "number":
		return 1, nil
	}

	// A string. If it looks like an identifier, it must come from somewhere
	// real; anything else is a name the emulator will not validate.
	if isID(field) {
		if typeOfIDField(field) == "resource" {
			// A field that says only "resource" means any resource: Outscale's
			// CreateTags tags whatever the ids name. Any identifier the run
			// created is an honest answer.
			if value, found := pool.takeAny(field); found {
				return value, nil
			}
		}
		if value, found := pool.take(step.Product, typeOfIDField(field)); found {
			return value, nil
		}
		if value, found := pool.takeAny(field); found {
			// Nothing of this type exists, so the call cannot succeed — but it
			// can still be refused, and a refusal in the declared error shape
			// is a verdict (#156). An identifier of the wrong kind is still an
			// identifier something answered, never one this probe made up.
			return value, nil
		}
		return nil, fmt.Errorf("%s is an identifier and nothing in the plan produced one", field)
	}
	if strings.HasSuffix(foldField(field), "name") {
		if value, found := pool.takeName(strings.TrimSuffix(foldField(field), "name")); found {
			return value, nil
		}
	}
	return probeValue, nil
}

// The value vocabulary. Each entry is a field whose schema type is string and
// whose value the emulator validates as a format; the sample is a fixed,
// syntactically valid instance of that format, the same way "feint-probe"
// stands for every unconstrained name. All providers' spellings live side by
// side, the same doctrine as the verb table in plan.go. Removing an entry must
// drop the operations it feeds back to refusal: /falsify replays that, and
// TestTheVocabularySeedsWhatAFormatFieldNeeds pins each family.
const (
	// cidrSample is RFC 5737 TEST-NET-1: a block reserved for documentation,
	// which an emulator's private inventory can carve without colliding with
	// anything real.
	cidrSample = "192.0.2.0/24"
	// destinationSample is TEST-NET-2, and it is a different block on purpose:
	// a created Net carries cidrSample, so a route whose destination reused it
	// would collide with the implicit local route every route table is born
	// with — Outscale answered "the destination already has a route", and the
	// refusal was this probe's own making.
	destinationSample = "198.51.100.0/24"
	// macSample is a locally-administered unicast MAC (x2 prefix), the address
	// family reserved for exactly this kind of synthetic interface.
	macSample = "02:00:00:00:00:01"
	// sshKeySample is a real ed25519 public key generated once for this probe
	// and thrown away private-half first; it exists because all three providers
	// refuse to store a string that does not parse as an OpenSSH public key —
	// the alternative is generating keys at probe time, and a probe must send
	// the same thing twice.
	sshKeySample = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC6ycFhUIlDdSHUOy5rF+z9dJdV1zLc6UGCXAAIPI50e feint-probe"
	// flowSample is Outscale's own vocabulary for a rule's direction: the
	// document types Flow as a bare string, and the SDK's docstring says
	// "Inbound or Outbound" (osc-sdk-go, CreateSecurityGroupRuleRequest.Flow).
	flowSample = "Inbound"
)

// cidrFields names the fields that carry an IP block. destination is Scaleway's
// route spelling; the others are the three providers' subnet spellings.
var cidrFields = map[string]string{
	"iprange":            cidrSample,
	"ipranges":           cidrSample,
	"cidr":               cidrSample,
	"destination":        destinationSample,
	"destinationiprange": destinationSample,
}

// fillWhenOptional names the optional reference fields worth filling: the
// document marks them optional because they are one branch of a choice it
// cannot express. block/v1's CreateVolume declares from_empty and from_snapshot
// with "precisely one must be set" in prose; from_empty is the branch whose
// requirements a fresh run can always meet (a size, not a snapshot).
var fillWhenOptional = map[string]bool{
	"fromempty": true,
	// A MoveIP without to_resource is a detach — ipam's own handler treats it
	// so — and a probe that means "move" must say where to.
	"toresource": true,
	// SetSecurityGroupRules with no rules is a wipe: it replaced the rule the
	// run had just created, and every later read of that rule found a 404 the
	// probe had caused itself. Qualified by schema, because "rules" alone
	// would also fill Outscale's CreateSecurityGroupRule, whose one-rule form
	// already works.
	"setsecuritygrouprules.request.rules": true,
}

// idAliases names the fields that hold an identifier without saying so:
// Scaleway's CreateImage types root_volume as a string, and its document says
// "the UID of the snapshot" in prose. One entry per finding, each cited; a
// field absent from here and from isID stays a plain string.
var idAliases = map[string]string{
	"rootvolume": "snapshot",
	// Outscale's GatewayId holds an internet service id: CreateRoute's own
	// docstring says "the ID of an internet service or virtual gateway", and
	// the emulator serves the first.
	"gateway": "internetservice",
}

// sampleFor answers the vocabulary for one field, whatever the provider's
// casing.
func sampleFor(field string) (string, bool) {
	fold := foldField(field)
	switch {
	case strings.HasSuffix(fold, "publickey"):
		return sshKeySample, true
	case strings.HasSuffix(fold, "macaddress"):
		return macSample, true
	case fold == "flow":
		return flowSample, true
	}
	if sample, ok := cidrFields[fold]; ok {
		return sample, true
	}
	return "", false
}

// foldField normalises a field name across the three providers' casings:
// VmId, vm_id and vm-id all fold to "vmid".
func foldField(name string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(name))
}

// isID recognises the fields whose value has to exist. All three providers
// name them the same way, in their own case: VmId, ImageId, net-id,
// private_network_id — plus the aliased few whose name hides it.
func isID(field string) bool {
	fold := strings.TrimSuffix(foldField(field), "s")
	if _, aliased := idAliases[fold]; aliased {
		return true
	}
	return strings.HasSuffix(fold, "id") && fold != "id"
}

// typeOfIDField names the resource an identifier field addresses: VmId → vm,
// private_network_ids → privatenetwork. This is what lets a harvested server id
// answer for server_id and never for volume_id — the wrong-type identifiers
// were where the 404 refusals of #163 came from.
func typeOfIDField(field string) string {
	fold := strings.TrimSuffix(foldField(field), "s")
	// Aliases resolve both spellings: root_volume carries no id suffix,
	// GatewayId does, and both name a type other than their own word.
	if alias, ok := idAliases[fold]; ok {
		return alias
	}
	bare := strings.TrimSuffix(fold, "id")
	if alias, ok := idAliases[bare]; ok {
		return alias
	}
	return bare
}

// singular turns a plural field name into the key its elements are harvested
// under: VmIds holds VmId values.
func singular(field string) string {
	if strings.HasSuffix(field, "s") {
		return field[:len(field)-1]
	}
	return field
}

// pool holds what earlier calls returned: identifiers by the type of resource
// they belong to, and names for the resources their provider addresses by
// name.
//
// This is what replaces a scenario written by hand: a create answers with an
// id, and the delete that follows takes it from here. Two tiers per type,
// because where a value came from decides what it may stand for:
//
//   - created: what this run's own creates brought into being. Preferred
//     always, because it is the only tier a verdict may safely rest on — an
//     id harvested from a listing can name a leftover of whatever suite ran
//     before, and a verdict that changes with the store's history is the
//     defect this issue's reproducibility clause names.
//   - observed: what listings published — the emulated catalogue's images and
//     templates, mostly. Good enough to address what the run cannot create,
//     never preferred over what it did.
type pool struct {
	mu sync.Mutex
	// ids: resource type → product → tiered values.
	ids map[string]map[string]*tier
	// names: resource type → the names recorded for it ("" for a bare name).
	names map[string][]string
	// coords are the path parameters no call produces because they are not
	// resources: a zone and a region are coordinates, and every provider's
	// paths carry one. The values are the emulator's own defaults, which is
	// what makes them safe: a zone it does not know is refused, and the probe
	// would be measuring its own bad guess rather than the emulator.
	coords map[string]string
}

type tier struct {
	created  []string
	observed []string
}

func newPool() *pool {
	return &pool{
		ids:   map[string]map[string]*tier{},
		names: map[string][]string{},
		coords: map[string]string{
			"zone":   "fr-par-1",
			"region": "fr-par",
		},
	}
}

// fill substitutes a path's parameters. It reports the first one it cannot
// satisfy, so the step is skipped with a reason rather than sent with a literal
// brace — which is what every Scaleway route did on the first run, and produced
// a wall of 400s that said nothing.
func (p *pool) fill(product, path string) (string, error) {
	out := path
	for {
		open := strings.Index(out, "{")
		if open < 0 {
			return out, nil
		}
		closing := strings.Index(out[open:], "}")
		if closing < 0 {
			return "", fmt.Errorf("malformed path %q", path)
		}
		name := out[open+1 : open+closing]

		value, found := p.pathValue(product, out[:open], name)
		if !found {
			return "", fmt.Errorf("%s is a path parameter and nothing in the plan produced one", name)
		}
		out = out[:open] + value + out[open+closing+1:]
	}
}

// pathValue resolves one path parameter: a coordinate, a typed identifier, a
// name for the name-addressed resources, and only then any identifier at all.
//
// The typed lookup is what changed for #163. {server_id} used to be answered by
// whatever identifier was harvested first — an organisation id, mostly — and
// the guaranteed 404 that follows is exactly the refusal this issue measures.
// The type is read from the parameter's own name, or from the collection
// segment before it when the parameter is generic ({id}, {ip}):
// /snapshots/{snapshot_id} and /v2/template/{id} both name their resource.
// TestAPathParameterIsAnsweredByItsOwnKind fails without the typed lookup.
func (p *pool) pathValue(product, prefix, param string) (string, bool) {
	if value, ok := p.coords[strings.ToLower(param)]; ok {
		return value, true
	}

	candidates := []string{}
	if t := typeOfIDField(param); t != "" && isID(param) {
		candidates = append(candidates, t)
	}
	if t := lastCollection(prefix); t != "" {
		candidates = append(candidates, t)
	}
	for _, t := range candidates {
		if value, found := p.take(product, t); found {
			return value, true
		}
	}
	if strings.ToLower(param) == "name" {
		if t := lastCollection(prefix); t != "" {
			if value, found := p.takeName(t); found {
				return value, true
			}
		}
	}
	// Nothing of the right kind exists. An identifier of the wrong kind keeps
	// the call — and the refusal verdict it earns — reachable; inventing one
	// would not (#163 forbids it, and it is what produced the 404s).
	return p.takeAny(param)
}

// lastCollection names the resource of the last concrete path segment before a
// parameter: ".../snapshots/" → snapshot, "/v2/template/" → template.
func lastCollection(prefix string) string {
	segments := strings.Split(strings.Trim(prefix, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		s := segments[i]
		if s == "" || strings.HasPrefix(s, "{") {
			continue
		}
		if colon := strings.Index(s, ":"); colon >= 0 {
			s = s[:colon]
		}
		return foldField(singular(s))
	}
	return ""
}

// take answers one identifier of the named type: this run's own creations
// first, the same product before the neighbours, listings last.
func (p *pool) take(product, typeFold string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	byProduct, found := p.ids[typeFold]
	if !found {
		return "", false
	}
	products := make([]string, 0, len(byProduct))
	for prod := range byProduct {
		products = append(products, prod)
	}
	sort.Strings(products)
	ordered := make([]string, 0, len(products)+1)
	ordered = append(ordered, product)
	for _, prod := range products {
		if prod != product {
			ordered = append(ordered, prod)
		}
	}
	for _, prod := range ordered {
		if t := byProduct[prod]; t != nil && len(t.created) > 0 {
			// The newest creation, not the first: a run can make the same kind
			// twice — SetSecurityGroupRules replaces the rule an earlier
			// create made — and only the newest is certain to still exist.
			// Listings stay first-value: a catalogue's order is stable.
			return t.created[len(t.created)-1], true
		}
	}
	for _, prod := range ordered {
		if t := byProduct[prod]; t != nil && len(t.observed) > 0 {
			return t.observed[0], true
		}
	}
	return "", false
}

// takeName answers a recorded name for one resource type; "" matches the bare
// "name" fields.
func (p *pool) takeName(typeFold string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	got := p.names[typeFold]
	if len(got) == 0 {
		return "", false
	}
	return got[0], true
}

// notAResource names the identifier types takeAny must never answer with:
// they are real ids of things that are not infrastructure — a request, the
// account itself, a tenant — and every one of them once stood in for "any
// resource" and bought a refusal the probe had invented.
var notAResource = map[string]bool{
	"account":      true,
	"organization": true,
	"project":      true,
	"request":      true,
}

// takeAny answers a generic parameter with any identifier harvested so far,
// created tier first, keys in stable order. It exists so an operation nobody
// can seed still gets called and still earns its refusal verdict; a typed
// lookup that found nothing must never quietly invent instead.
func (p *pool) takeAny(name string) (string, bool) {
	if !isID(name) && strings.ToLower(name) != "id" {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	types := make([]string, 0, len(p.ids))
	for t := range p.ids {
		if !notAResource[t] {
			types = append(types, t)
		}
	}
	sort.Strings(types)
	for _, pick := range []func(*tier) []string{
		func(t *tier) []string { return t.created },
		func(t *tier) []string { return t.observed },
	} {
		for _, t := range types {
			products := make([]string, 0, len(p.ids[t]))
			for prod := range p.ids[t] {
				products = append(products, prod)
			}
			sort.Strings(products)
			for _, prod := range products {
				if values := pick(p.ids[t][prod]); len(values) > 0 {
					return values[len(values)-1], true
				}
			}
		}
	}
	return "", false
}

// harvest records what one response carried: every identifier, typed by the
// context it appeared in, and every name.
//
// The type comes from the response itself, never from a table kept beside it:
// a field named VmId types its own value; an id inside {"server": {…}} or
// {"servers": […]} is typed by the wrapper; a bare top-level object is typed by
// the operation's own response schema (block/v1's Volume answers unwrapped);
// and an Exoscale operation's reference is typed by the command it links to
// ("get-private-network" names what was just made). What a create-kind step
// returns lands in the created tier; what a listing shows stays observed.
func (p *pool) harvest(doc *contract.Doc, step Step, value any) {
	context := ""
	if op, ok := doc.Operations[step.Contract]; ok {
		context = schemaLeaf(op.Response)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.walk(step, value, context, 0)
}

// schemaLeaf folds a schema name to the resource it describes:
// "block/v1.scaleway.block.v1.Volume" → volume, "operation" → operation.
// Response wrappers ("CreateServerResponse", "ListServersResponse") answer
// nothing: their inner wrapper keys carry the context instead.
func schemaLeaf(name string) string {
	if name == "" {
		return ""
	}
	leaf := name
	if dot := strings.LastIndex(leaf, "."); dot >= 0 {
		leaf = leaf[dot+1:]
	}
	if strings.HasSuffix(leaf, "Response") || strings.HasSuffix(leaf, "Request") {
		return ""
	}
	return foldField(leaf)
}

// walk records a response's identifiers. depth counts map descents — the
// top-level object is 0, a value under one of its keys is 1 — and arrays do
// not count: an item of a list sits at its list's depth.
func (p *pool) walk(step Step, value any, context string, depth int) {
	switch typed := value.(type) {
	case map[string]any:
		// The Exoscale operation envelope: the reference says, in the
		// document's own words, what resource the operation touched.
		if ref, ok := typed["reference"].(map[string]any); ok {
			if id, ok := ref["id"].(string); ok && id != "" {
				refType := step.Makes
				if command, ok := ref["command"].(string); ok {
					if t := typeOfCommand(command); t != "" {
						refType = t
					}
				}
				if refType != "" {
					p.record(step, refType, id, depth+1)
				}
			}
		}
		if id, ok := typed["id"].(string); ok && id != "" && context != "" {
			p.record(step, context, id, depth+1)
		}
		// Keys in sorted order: a map walked in Go's random order would append
		// ids in a different order on every run, take() answers the first one,
		// and the verdicts would flap with it — the exact non-reproducibility
		// this issue forbids (TestHarvestIsDeterministic).
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			nested := typed[key]
			if text, ok := nested.(string); ok && text != "" {
				if isID(key) {
					p.record(step, typeOfIDField(key), text, depth+1)
				}
				if fold := foldField(key); strings.HasSuffix(fold, "name") {
					p.recordName(step, strings.TrimSuffix(fold, "name"), context, text)
				}
			}
			p.walk(step, nested, contextOfKey(key, context), depth+1)
		}
	case []any:
		for _, item := range typed {
			p.walk(step, item, context, depth)
		}
	}
}

// contextOfKey types the values under one wrapper key: {"server": {…}} and
// {"servers": […]} both say server. Keys that are not resource words —
// "items", "data" — still produce a fold, and a fold nothing ever asks for is
// harmless.
func contextOfKey(key, parent string) string {
	fold := foldField(singular(key))
	if fold == "" {
		return parent
	}
	return fold
}

// typeOfCommand reads the resource out of an Exoscale reference command:
// "get-private-network" → privatenetwork.
func typeOfCommand(command string) string {
	for _, verb := range []string{"get-", "list-"} {
		if strings.HasPrefix(command, verb) {
			return foldField(strings.TrimPrefix(command, verb))
		}
	}
	return ""
}

// record files one identifier. The created tier takes what a create-kind
// step's own payload carries — depth 2 at most: the top-level id, or the id
// one wrapper down ({"server": {"id": …}}). Deeper finds are references to
// other resources and stay observed whoever answered them: CreateServer's
// response embeds the catalogue image and the default security group, and
// filing those as "created" made UpdateImage try to change the catalogue
// (TestACreateOnlyVouchesForItsOwnPayload).
func (p *pool) record(step Step, typeFold, id string, depth int) {
	byProduct := p.ids[typeFold]
	if byProduct == nil {
		byProduct = map[string]*tier{}
		p.ids[typeFold] = byProduct
	}
	t := byProduct[step.Product]
	if t == nil {
		t = &tier{}
		byProduct[step.Product] = t
	}
	if step.Kind == kindCreate && depth <= 2 {
		t.created = append(t.created, id)
		return
	}
	t.observed = append(t.observed, id)
}

// recordName files a name under the type it belongs to. A bare "name" field is
// typed by its wrapper ({"keypair": {"name": …}} → keypair) or recorded
// untyped; a compound one carries its own type (KeypairName → keypair).
func (p *pool) recordName(step Step, explicit, context, name string) {
	typeFold := explicit
	if typeFold == "" {
		typeFold = context
	}
	// Created names first: the run's own names are the ones its deletes must
	// address. Observed names still land, for the name-addressed reads.
	if step.Kind == kindCreate {
		p.names[typeFold] = append([]string{name}, p.names[typeFold]...)
		return
	}
	p.names[typeFold] = append(p.names[typeFold], name)
}

// harvestSentName records the name a successful create carried, under the type
// it made. A created resource's name is as real as its id — and for the
// name-addressed resources (Exoscale's SSH keys) it is the only handle the
// response ever gives back.
func (p *pool) harvestSentName(step Step, body map[string]any) {
	if step.Kind != kindCreate || step.Makes == "" {
		return
	}
	name, ok := body["name"].(string)
	if !ok || name == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.names[step.Makes] = append([]string{name}, p.names[step.Makes]...)
}
