package contract

import "sort"

// The other direction of the same check (#88).
//
// Validate holds a response to what the schema forbids, and that is
// one-directional by construction: an absent field only violates a schema when
// the provider declared it required, which Scaleway does on 9% of its schemas
// and Outscale on 27%. So the gate caught every field this emulator invented
// and none of the fields it forgot — twenty of them on ReadVms alone, each
// green for as long as it existed, found only because a recording of a real
// account happened to cover that one operation.
//
// What this file adds is the list to hold an answer against: every field the
// provider's own document declares the response carries. The document is the
// source the SDKs are generated from, so a property listed here is a field a
// generated client knows how to decode — and a served answer that never once
// carries it is either an omission or a decision, never nothing. Which of the
// two it is belongs to the caller: this package states what the document
// declares, the emulator's observer compares, and a pack's DeclinedFields()
// argues the decisions.

// ResponseFields flattens the response schema of an operation into the field
// paths a complete answer would carry, mapped to their declared type.
//
// The path grammar is the transcript's, so the two sides of a comparison speak
// the same language: "servers" is an array, "servers[]" one of its elements,
// "servers[].name" a field of each element. An array of scalars contributes
// its own path and nothing below it; an object property that names no schema
// is opaque — its keys are the provider's data, not the document's declaration
// — and contributes nothing below itself either.
//
// Nil when the contract does not know the operation or declares no response
// schema: "nothing declared" must stay distinct from "nothing to serve", or a
// consumer would hold an answer to a list this method invented.
func (d *Doc) ResponseFields(operation string) map[string]string {
	op, ok := d.Operations[operation]
	if !ok || op.Response == "" {
		return nil
	}
	schema, ok := d.Schemas[op.Response]
	if !ok || len(schema.Properties) == 0 {
		return nil
	}
	out := map[string]string{}
	d.flattenSchema(out, "", op.Response, map[string]bool{})
	return out
}

// flattenSchema records every property of a schema under path, descending
// through refs.
//
// visiting guards the current branch: a schema reachable from itself — and the
// documents have them — is recorded where first met and not descended into
// again, so the flattening terminates instead of recursing forever.
// TestResponseFieldsSurviveARecursiveSchema fails without it.
func (d *Doc) flattenSchema(out map[string]string, path, schemaName string, visiting map[string]bool) {
	if visiting[schemaName] {
		return
	}
	visiting[schemaName] = true
	defer delete(visiting, schemaName)

	schema := d.Schemas[schemaName]
	for name, prop := range schema.Properties {
		d.flattenProperty(out, join(path, name), prop, visiting)
	}
}

func (d *Doc) flattenProperty(out map[string]string, path string, prop Property, visiting map[string]bool) {
	switch {
	case prop.Ref != "":
		ref, known := d.Schemas[prop.Ref]
		switch {
		case !known:
			// A dangling ref declares nothing checkable below this point.
			out[path] = "object"
		case ref.Type != "" && ref.Properties == nil:
			// A named scalar: the state enumerations.
			out[path] = ref.Type
		default:
			out[path] = "object"
			d.flattenSchema(out, path, prop.Ref, visiting)
		}
	case prop.Type == "array":
		out[path] = "array"
		if prop.Items == nil {
			return
		}
		if prop.Items.Ref != "" {
			d.flattenProperty(out, path+"[]", *prop.Items, visiting)
			return
		}
		// An array of scalars ends here: its elements carry no fields, and an
		// element path for them would demand a non-empty list from a store
		// whose contents are the client's business.
	default:
		typ := prop.Type
		if typ == "" {
			// An untyped property is opaque: the document said "a field exists
			// here" and nothing more, so nothing below it is declared.
			typ = "object"
		}
		out[path] = typ
	}
}

// SortedFieldPaths orders a field map for deterministic reporting.
func SortedFieldPaths(fields map[string]string) []string {
	out := make([]string, 0, len(fields))
	for path := range fields {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
