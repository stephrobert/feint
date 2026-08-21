// Package contract checks what the emulator answers against the provider's own
// API description.
//
// This is the shape half of the same idea as internal/drift. Drift asks whether
// the set of operations moved; this asks whether the fields inside them are the
// ones the provider defines. Both replace a human reading an SDK, which is where
// invented fields come from: a private_ip on Scaleway's PrivateNIC that the API
// does not have, read back by our own conformance suite; a VmCount on CreateVms
// where the API says MinVmsCount and MaxVmsCount, so every request created one
// machine whatever it asked for. Neither survived contact with this package.
//
// The rule is not invented here. 643 of Outscale's 650 schemas declare
// additionalProperties: false, which is the provider stating that a field they
// do not define must not appear. The artefact under contracts/ is extracted from
// their OpenAPI document by tools/contract/, versioned, and read with
// encoding/json — no YAML parser, no dependency.
//
// What it does not do: formats, patterns, string lengths, numeric bounds. The
// document carries almost none of them (8 examples and 10 patterns across 2,489
// properties), so a validator built around them would mostly validate nothing
// while looking thorough.
package contract

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Doc is a provider's contract: what each operation exchanges, and the shape of
// every schema those operations name.
type Doc struct {
	Provider   string `json:"provider"`
	Source     string `json:"source"`
	APIVersion string `json:"apiVersion"`
	// PathPrefix is what the document's paths hang off, taken from the server
	// URL it declares: Outscale's /ReadVms is served at /api/v1/ReadVms.
	PathPrefix string `json:"pathPrefix"`
	// ClosedPolicy is "declared" when the provider set additionalProperties:
	// false themselves, "assumed" when this project decided that a schema
	// saying nothing forbids unknown fields anyway.
	//
	// The distinction is not decoration. Outscale closed 643 of 650 schemas, so
	// an unknown field is a violation they wrote down. Exoscale closed 7 of 299,
	// so the same refusal is our call — defensible, since a field their document
	// does not define is one no client reads, but ours to defend.
	ClosedPolicy string `json:"closedPolicy"`
	// ErrorSchema names the shape the provider declares for a refusal — the
	// body of a 4xx. One name for the whole document because that is how all
	// three providers publish it: Outscale declares the same ErrorResponse on
	// every 4xx it lists, Exoscale's SDK decodes every error through one
	// struct, and Scaleway's scw/errors.go does the same. Empty when nothing
	// upstream states the shape, and a refusal is then not validatable — which
	// the probe reports as such rather than passing it.
	ErrorSchema string `json:"errorSchema,omitempty"`
	// RecordedFields are the properties the extraction added to a schema
	// because a recording of the real cloud carries them and the provider's own
	// document does not declare them. Empty for a provider whose document is
	// level with its API, which is where every provider is presumed to be until
	// a recording says otherwise.
	//
	// They are here rather than folded silently into Schemas so that the
	// artefact says which of its fields came from a wire instead of a document,
	// and so that a gate can hold each one against the recording it names —
	// internal/cli's TestEveryRecordedFieldIsStillOnTheWire, on the model
	// corpus/accepted.json states: an entry that covers nothing is deleted, or
	// the register quietly becomes a place where a shape can be invented.
	RecordedFields []RecordedField      `json:"recordedFields,omitempty"`
	Operations     map[string]Operation `json:"operations"`
	Schemas        map[string]Schema    `json:"schemas"`

	// folded indexes the operations by their name lower-cased, so a lookup can
	// survive the one difference between a document and the SDK generated from
	// it: Go capitalises initialisms, so Scaleway's CreateIp is CreateIP in
	// their SDK, and the routes here carry the SDK spelling because that is what
	// the drift scan reads. Same operation, two ways of writing it.
	folded map[string]string
}

// RecordedField is one property a recording of the real cloud carries and the
// provider's own API description does not declare, with the citation that
// proves it.
//
// Every field is load-bearing, and each one answers a question somebody will
// ask of the entry later: Schema and Property say what was added and where;
// Corpus, Operation and Path say where to go and look; Recorded says how old
// the measurement is; Why says what the reader is meant to conclude. An entry
// missing any of them is refused by the extraction, because a field with no
// citation behind it is exactly what rule 4 calls an invented format.
type RecordedField struct {
	Schema   string `json:"schema"`
	Property string `json:"property"`
	// Corpus is the recording, relative to corpus/ — the same spelling
	// corpus/accepted.json uses for its own files, so one name reaches both.
	Corpus string `json:"corpus"`
	// Operation is the name the recording files the exchange under, and Path is
	// where the field sits in the answer, in the notation internal/transcript
	// produces: zones[].id, security-groups[].visibility.
	Operation string `json:"operation"`
	Path      string `json:"path"`
	// Recorded is the day the recording was made, YYYY-MM-DD.
	Recorded string `json:"recorded"`
	Why      string `json:"why"`
}

// Operation ties an API call to the schemas on either side of it.
type Operation struct {
	Path   string `json:"path"`
	Method string `json:"method"`
	// Group is where the provider's own document files the operation: the root
	// of its first tag's parent chain (Exoscale declares role under iam,
	// dbaas-mysql under dbaas; Outscale declares a flat tag per resource).
	// Empty when the document leaves the operation untagged — the readers fall
	// back to the product rather than filing it somewhere plausible.
	Group    string `json:"group,omitempty"`
	Request  string `json:"request"`
	Response string `json:"response"`
	// Query maps each query parameter the document declares to what it may
	// hold. Scaleway's lists take per_page and their filters here; Outscale
	// takes nothing (its pagination travels in the request body); Exoscale
	// declares a handful of filters. Empty when the operation declares none.
	Query map[string]Property `json:"query,omitempty"`
}

// Schema is one object shape.
type Schema struct {
	// Closed mirrors additionalProperties: false. When it is true, a field the
	// schema does not list is a violation the provider declared, not a rule this
	// package invented.
	Closed bool `json:"closed"`
	// Required lists the properties that must be present.
	Required []string `json:"required,omitempty"`
	// Type is set for the handful of schemas that are a bare scalar rather than
	// an object, such as the state enumerations.
	Type string `json:"type,omitempty"`
	// Enum lists the accepted values, when the document gives them. Untyped
	// because a few of them are numbers rather than strings, and a []string
	// would fail to decode the contract rather than fail to validate a value.
	Enum []any `json:"enum,omitempty"`
	// Properties maps a field name to what it may hold.
	Properties map[string]Property `json:"properties,omitempty"`
}

// Property is what one field may hold: a JSON type, or another schema.
type Property struct {
	Type  string    `json:"type,omitempty"`
	Ref   string    `json:"ref,omitempty"`
	Items *Property `json:"items,omitempty"`
	Enum  []any     `json:"enum,omitempty"`
}

// Load reads a contract artefact.
func Load(path string) (*Doc, error) {
	f, err := os.Open(path) //nolint:gosec // caller-supplied path, a versioned artefact
	if err != nil {
		return nil, fmt.Errorf("open contract %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return Read(f)
}

// Read decodes a contract artefact.
func Read(r io.Reader) (*Doc, error) {
	var doc Doc
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode contract: %w", err)
	}
	if len(doc.Schemas) == 0 {
		return nil, fmt.Errorf("contract carries no schema: it would validate everything")
	}
	doc.folded = make(map[string]string, len(doc.Operations))
	for name := range doc.Operations {
		doc.folded[strings.ToLower(name)] = name
	}
	return &doc, nil
}

// Violation is one thing the value does that the contract does not allow.
type Violation struct {
	// Path locates it inside the value, e.g. "Vms[0].PrivateIp".
	Path string
	// Reason says what is wrong, in the terms of the contract.
	Reason string
}

func (v Violation) String() string { return v.Path + ": " + v.Reason }

// Violations is a set of them, ordered so two runs report the same thing.
type Violations []Violation

func (vs Violations) Error() string {
	parts := make([]string, 0, len(vs))
	for _, v := range vs {
		parts = append(parts, v.String())
	}
	return strings.Join(parts, "; ")
}

// OperationFor resolves a route's declared operation to a contract operation.
//
// The contract keys on the provider's own operationId — "ReadVms" — while a
// route declares the name the drift scan produces from their SDK, qualified by
// receiver and package: "osc/Client.ReadVms". The qualifier is what makes the
// name unique across products in the coverage report, and what has to come off
// here. Both forms are tried so a contract written either way resolves.
func (d *Doc) OperationFor(routeOperation string) (Operation, string, bool) {
	candidates := operationKeys(routeOperation)
	for _, candidate := range candidates {
		if op, ok := d.Operations[candidate]; ok {
			return op, candidate, true
		}
	}
	for _, candidate := range candidates {
		if name, ok := d.folded[strings.ToLower(candidate)]; ok {
			return d.Operations[name], name, true
		}
	}
	return Operation{}, "", false
}

// operationKeys are the forms a route's operation name can take in a contract,
// most specific first.
//
// "instance/v1/API.ListServers" can be keyed three ways, and all three exist in
// this project. Outscale's contract keys on the bare operationId, because its
// document covers the whole API. Scaleway's keys on product and version, because
// it publishes one document per product and defines ListImages twice — in
// instance/v1 and in marketplace/v2 — so the bare name would resolve to whichever
// happened to be written last.
//
// The receiver is dropped rather than matched: a document names an operation,
// not the Go type a generated client hangs it off, and API, ZonedAPI and
// MetadataAPI are the SDK's business.
func operationKeys(routeOperation string) []string {
	keys := []string{routeOperation}

	dot := strings.LastIndex(routeOperation, ".")
	if dot < 0 {
		return keys
	}
	method := routeOperation[dot+1:]
	keys = append(keys, method)

	if slash := strings.LastIndex(routeOperation[:dot], "/"); slash >= 0 {
		product := routeOperation[:slash]
		keys = append([]string{routeOperation, product + "." + method}, method)
	}
	return keys
}

// ValidateResponse checks a decoded response body against the schema the named
// operation declares it returns.
//
// An operation the contract does not know is a violation in itself: it means a
// route claims to serve something upstream does not have, which is the same
// defect the drift report calls an orphan.
func (d *Doc) ValidateResponse(operation string, value any) Violations {
	op, ok := d.Operations[operation]
	if !ok {
		return Violations{{Path: operation, Reason: "no such operation in the contract"}}
	}
	if op.Response == "" {
		return nil
	}
	return d.Validate(op.Response, value)
}

// Validate checks a decoded value against a named schema.
func (d *Doc) Validate(schema string, value any) Violations {
	var out Violations
	d.validate(&out, "", schema, value)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].Reason < out[j].Reason
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func (d *Doc) validate(out *Violations, path, schemaName string, value any) {
	schema, ok := d.Schemas[schemaName]
	if !ok {
		add(out, path, "the contract has no schema named "+schemaName)
		return
	}

	// A scalar schema: the state enumerations, and nothing else today.
	if schema.Type != "" && schema.Properties == nil {
		checkScalar(out, path, Property{Type: schema.Type, Enum: schema.Enum}, value)
		return
	}

	obj, ok := value.(map[string]any)
	if !ok {
		add(out, path, fmt.Sprintf("expected an object for %s, got %s", schemaName, jsonKind(value)))
		return
	}

	for _, name := range schema.Required {
		if _, present := obj[name]; !present {
			add(out, join(path, name), "required by "+schemaName+" and absent")
		}
	}

	for name, field := range obj {
		prop, known := schema.Properties[name]
		if !known {
			// Only when the provider closed the schema. Where they left it open,
			// an extra field is theirs to allow and not ours to refuse.
			if schema.Closed {
				add(out, join(path, name), schemaName+" does not define this field")
			}
			continue
		}
		d.validateProperty(out, join(path, name), prop, field)
	}
}

func (d *Doc) validateProperty(out *Violations, path string, prop Property, value any) {
	// null stands for "not set" in every shape here, and the document marks
	// almost nothing as non-nullable, so it is accepted wherever it appears.
	if value == nil {
		return
	}

	switch {
	case prop.Ref != "":
		d.validate(out, path, prop.Ref, value)
	case prop.Type == "array":
		items, ok := value.([]any)
		if !ok {
			add(out, path, "expected an array, got "+jsonKind(value))
			return
		}
		if prop.Items == nil {
			return
		}
		for i, item := range items {
			d.validateProperty(out, fmt.Sprintf("%s[%d]", path, i), *prop.Items, item)
		}
	default:
		checkScalar(out, path, prop, value)
	}
}

// checkScalar compares a JSON value against a declared type. JSON has one number
// type, so integer and number are the same check: a client decoding into an int
// will not care that the emulator wrote 3 rather than 3.0.
func checkScalar(out *Violations, path string, prop Property, value any) {
	if value == nil || prop.Type == "" {
		return
	}
	got := jsonKind(value)
	want := prop.Type
	if want == "integer" {
		want = "number"
	}
	if want != "object" && got != want {
		add(out, path, "expected "+prop.Type+", got "+got)
		return
	}
	if len(prop.Enum) == 0 {
		return
	}
	// Compared as text: JSON gives every number as a float64, so 3 and 3.0 must
	// not read as two different values.
	text := fmt.Sprint(value)
	allowed := make([]string, 0, len(prop.Enum))
	for _, v := range prop.Enum {
		s := fmt.Sprint(v)
		if s == text {
			return
		}
		allowed = append(allowed, s)
	}
	add(out, path, "value "+text+" is not one of "+strings.Join(allowed, ", "))
}

func jsonKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, int, int64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func add(out *Violations, path, reason string) {
	*out = append(*out, Violation{Path: path, Reason: reason})
}

// PageSize reports the parameter that bounds how many items a list answer may
// carry, and where the operation declares it: in its query parameters
// (Scaleway's per_page) or in its request body (Outscale's ResultsPerPage).
//
// The names are each provider's own, read from their documents — per_page and
// page_size in Scaleway's query parameters, ResultsPerPage in Outscale's
// Read*Request schemas — plus the two generic spellings (limit, max_results)
// so a fourth provider's document is recognised without touching this file.
// The same doctrine as the verb table in internal/probe: all vocabularies live
// side by side, none is guessed.
//
// This lives here rather than in the probe because two consumers need the same
// answer: the probe, to build the call, and the emulator's observer, to refuse
// to publish a probe axis for a paged operation whose page size was never
// exercised (TestPagedOperationNeedsItsPageSizeExercised).
func (d *Doc) PageSize(op Operation) (name string, inBody bool, ok bool) {
	for candidate := range op.Query {
		if isPageSize(candidate) {
			return candidate, false, true
		}
	}
	if op.Request != "" {
		for candidate := range d.Schemas[op.Request].Properties {
			if isPageSize(candidate) {
				return candidate, true, true
			}
		}
	}
	return "", false, false
}

// isPageSize folds a field name to its bare words before comparing, so
// per_page, PerPage and per-page all read as the same parameter.
func isPageSize(name string) bool {
	folded := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(name))
	switch folded {
	case "perpage", "pagesize", "resultsperpage", "limit", "maxresults":
		return true
	}
	return false
}

// PageBound reports the response's top-level list fields that overflow the
// page size the request asked for. An answer that ignores per_page is not a
// schema violation — the array is still an array — which is exactly why the
// schema check alone could not see it and this exists
// (TestAnAnswerIgnoringPageSizeIsAViolation).
//
// Shared for the same reason PageSize is: the probe fails its run on it, and
// the emulator's observer refuses to publish a probe axis on it, and two
// copies of a bound check drift the first time one is fixed.
func (d *Doc) PageBound(op Operation, asked int, value any) Violations {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	var out Violations
	for _, field := range d.ListFields(op) {
		if items, ok := obj[field].([]any); ok && len(items) > asked {
			add(&out, field, fmt.Sprintf("%d items where the request asked for at most %d", len(items), asked))
		}
	}
	return out
}

// ListFields names the top-level array fields of an operation's response
// schema — the fields a page size bounds. Scaleway's ListServersResponse
// declares servers, Outscale's ReadVmsResponse declares Vms; total counts and
// response contexts are not arrays and stay out. Nil when the operation has no
// response schema or the schema declares no array at its root.
func (d *Doc) ListFields(op Operation) []string {
	if op.Response == "" {
		return nil
	}
	var out []string
	for name, prop := range d.Schemas[op.Response].Properties {
		if prop.Type == "array" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// EnumValues is every string value this document enumerates, anywhere.
//
// The provider's own closed vocabularies: an order ("created_at_desc"), a state
// ("available"), a boot type, a protocol. They are the API's words, published
// by the API, and knowing them is what lets internal/corpus keep them verbatim
// in a sanitised transcript instead of replacing them with a synthetic string
// the emulator then answers 400 to.
//
// Measured before it was written: without this, replaying the first real
// Scaleway corpus turned four list operations red — "order=created_at_desc"
// became "order=redacted-747", and a 400 the sanitiser had manufactured read
// exactly like an emulator defect.
//
// Sorted, so two callers building an allowlist from it agree.
func (d *Doc) EnumValues() []string {
	seen := map[string]bool{}
	add := func(values []any) {
		for _, v := range values {
			if s, isText := v.(string); isText && s != "" {
				seen[s] = true
			}
		}
	}
	for _, schema := range d.Schemas {
		add(schema.Enum)
		for _, prop := range schema.Properties {
			addPropertyEnums(add, prop)
		}
	}
	for _, op := range d.Operations {
		for _, prop := range op.Query {
			addPropertyEnums(add, prop)
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// addPropertyEnums walks a property and its element type, which is where a list
// of enumerated values sits.
func addPropertyEnums(add func([]any), prop Property) {
	add(prop.Enum)
	if prop.Items != nil {
		addPropertyEnums(add, *prop.Items)
	}
}
