#!/usr/bin/env python3
"""Extract an OpenAPI document into the compact contract the emulator checks against.

Why this exists
---------------
The packs' request and response shapes were written by reading an SDK, and
reading is where invented fields come from. Three had already shipped before
this: a `private_ip` on Scaleway's PrivateNIC that the API does not define, read
back by our own conformance suite; a `VmCount` on Outscale's CreateVms, where the
API says MinVmsCount and MaxVmsCount, so every request created one machine
whatever it asked for; and a `public_ip` the Scaleway CLI sends on every create
and the pack never read, so an address the client had just reserved was silently
dropped. None was caught by a test, a review, or a real client.

One extractor for every provider that publishes an OpenAPI document, rather than
a copy per provider: the format the emulator reads is the same, and two copies of
a hundred lines drift apart the first time one of them is fixed.

Closed schemas
--------------
`additionalProperties: false` is what makes an unknown field a violation the
provider declared rather than a rule invented here. The two documents differ
sharply and the artefact records which case it is:

  Outscale  643 of 650 schemas closed  -> policy "declared"
  Exoscale    7 of 299 schemas closed  -> policy "assumed", with --assume-closed

Under an assumed policy the emulator still refuses unknown fields, but that is
our decision — a field the document does not define is one no client reads, so
it is either invented or dead — and the artefact says so out loud instead of
letting a reader believe the provider wrote it.

One document or several
-----------------------
Outscale and Exoscale each publish one document for their whole API. Scaleway
publishes one per product, so its artefact folds several together and qualifies
every name with the product: ListImages exists in both instance/v1 and
marketplace/v2, and its routes already tell them apart.

The error shape
---------------
A refusal is half of the protocol — Terraform's retry loop reads
`transient_state` out of the error body — so the artefact records the shape a
4xx must have, under `errorSchema`. Where the document declares its error
responses (Outscale puts the same ErrorResponse on every 4xx it lists), the
name is derived from those declarations. Where the document declares none, or
declares a shape its own SDK does not read, `--error-shape` supplies a YAML
fragment transcribed from the SDK's error structs — rule 4: the shape comes
from the SDK, never from a guess. The fragment file carries the citation.

Usage:
  tools/contract/extract-openapi.py <output.json> --provider <name>
      --spec <file>                      one document for the whole API
      --spec <namespace>=<file> ...      one per product, names qualified
      [--source <where it came from>] [--assume-closed]
      [--error-shape <fragment.yaml>]    the refusal shape, when the documents
                                         declare none or the SDK disagrees
"""

import argparse
import json
import sys
from collections import Counter
from urllib.parse import urlsplit

import yaml

# The namespace a merged spec's names live under, empty for a provider that
# publishes one document for its whole API.
NAMESPACE = ""


def qualify(name: str) -> str:
    return NAMESPACE + name if NAMESPACE else name


def ref_name(ref: str) -> str:
    return qualify(ref.rsplit("/", 1)[-1])


def scalar_type(kind) -> str | None:
    """The one JSON type a field carries.

    OpenAPI 3.1 writes a nullable field as a list — `type: [string, "null"]` —
    where 3.0 used a `nullable` flag. Outscale is 3.0 and Exoscale 3.1, so both
    forms turn up. Null is dropped because the validator already accepts it
    everywhere: an absent optional field is how all of these APIs say "not
    applicable", and refusing it would flag half of every response.
    """
    if isinstance(kind, list):
        rest = [k for k in kind if k != "null"]
        return rest[0] if len(rest) == 1 else None
    return kind


# The protobuf well-known wrappers, and the JSON type each one actually becomes.
#
# Scaleway's documents are generated from protobuf and leak these into the
# OpenAPI: dest_port_from is declared as $ref google.protobuf.UInt32Value, an
# object with a value field. On the wire it is a bare number, which is what the
# canonical protobuf-JSON mapping says and what their own Go SDK declares
# (*uint32). Following the document literally would report a violation on every
# optional integer the emulator answers correctly.
PROTOBUF_WRAPPERS = {
    "google.protobuf.BoolValue": "boolean",
    "google.protobuf.BytesValue": "string",
    "google.protobuf.DoubleValue": "number",
    "google.protobuf.FloatValue": "number",
    "google.protobuf.Int32Value": "integer",
    "google.protobuf.Int64Value": "integer",
    "google.protobuf.StringValue": "string",
    "google.protobuf.UInt32Value": "integer",
    "google.protobuf.UInt64Value": "integer",
}


def property_shape(prop: dict) -> dict:
    """Reduce one property to what a validator needs: its type, or the schema it
    points at. Descriptions, examples and formats are dropped — they document,
    they do not constrain."""
    if "$ref" in prop:
        bare = prop["$ref"].rsplit("/", 1)[-1]
        if wrapped := PROTOBUF_WRAPPERS.get(bare):
            return {"type": wrapped}
        return {"ref": ref_name(prop["$ref"])}

    # A nullable reference — `oneOf: [$ref X, type: "null"]` — is the reference:
    # the validator already accepts null everywhere, so the wrapper only hid the
    # shape. Scaleway writes its exclusive request branches this way (block/v1's
    # CreateVolume.from_empty), and dropping the oneOf left the property with no
    # shape at all, which the probe read as "nothing to build" and the emulator
    # answered with the refusal #163 measured.
    if "oneOf" in prop:
        refs = [b for b in prop["oneOf"] if "$ref" in b]
        others = [b for b in prop["oneOf"] if "$ref" not in b and b.get("type") != "null"]
        if len(refs) == 1 and not others:
            return property_shape(refs[0])

    out: dict = {}
    kind = scalar_type(prop.get("type"))
    if kind:
        out["type"] = kind
    if kind == "array":
        items = prop.get("items", {})
        if "$ref" in items:
            out["items"] = property_shape(items)
        elif item_kind := scalar_type(items.get("type")):
            out["items"] = {"type": item_kind}
    if "enum" in prop:
        out["enum"] = prop["enum"]
    return out


def json_schema_of(body: dict, schemas: dict, name_hint: str, args) -> str | None:
    """The name of the schema behind a request or response body.

    A $ref resolves to its own name. A body declared inline gets one: Scaleway
    writes every one of its bodies that way, so reading only $refs left its whole
    contract with no request schema at all — 21 routes the probe could not build
    a call for, and every response validated against nothing, which the checker
    then reported as success.

    The synthetic name carries the operation and the side it belongs to, so it
    cannot collide with a schema the document names itself (Scaleway's are
    scaleway.<product>.<version>.<Type>).
    """
    schema = (body or {}).get("content", {}).get("application/json", {}).get("schema", {})
    if "$ref" in schema:
        return ref_name(schema["$ref"])
    if not schema.get("properties"):
        return None

    generated = qualify(name_hint)
    schemas[generated] = schema_entry(schema, args)
    return generated


def schema_entry(sch: dict, args) -> dict:
    """One schema reduced to what the validator needs."""
    additional = sch.get("additionalProperties")
    closed = additional is False
    if not closed and args.assume_closed and additional is None:
        closed = True

    entry: dict = {"closed": closed}
    if sch.get("required"):
        entry["required"] = sorted(sch["required"])
    if scalar_type(sch.get("type")) == "string":
        entry["type"] = "string"
        if "enum" in sch:
            entry["enum"] = sch["enum"]
    props = sch.get("properties") or {}
    if props:
        entry["properties"] = {k: property_shape(v) for k, v in sorted(props.items())}
    return entry


def success_body(op: dict) -> dict:
    """The body of whichever 2xx the operation declares as its success.

    Reading only "200" left every operation answering 201 or 204 with no response
    schema, so the check passed them without looking. Exoscale answers 200
    throughout, Outscale too, but an API is free to change that and the silence
    would not be visible.
    """
    responses = op.get("responses") or {}
    for status in sorted(responses):
        if str(status).startswith("2"):
            return responses[status]
    return {}


def declared_error_refs(op: dict) -> list[str]:
    """The schema names the operation's own 4xx responses reference.

    Only 4xx: a refusal is what the probe validates, and a 500's shape
    (Exoscale declares a distinct impact-error-response) is not it. Only $refs:
    an inline error body would get a synthetic per-operation name, which is the
    opposite of what a document-wide error shape is.
    """
    out = []
    for status, response in (op.get("responses") or {}).items():
        if not str(status).startswith("4"):
            continue
        schema = (response or {}).get("content", {}).get("application/json", {}).get("schema", {})
        if "$ref" in schema:
            out.append(ref_name(schema["$ref"]))
    return out


def query_params(item: dict, op: dict) -> dict:
    """The query parameters an operation declares, path-item level included.

    Reduced with property_shape like any field, so an enum survives (Scaleway's
    order) and a protobuf wrapper unwraps (its per_page is a UInt32Value).
    """
    out = {}
    for prm in (item.get("parameters") or []) + (op.get("parameters") or []):
        if isinstance(prm, dict) and prm.get("in") == "query" and "name" in prm:
            out[prm["name"]] = property_shape(prm.get("schema") or {})
    return out


def tag_roots(spec: dict) -> dict:
    """Map each declared tag to the root of its parent chain.

    Exoscale's document declares a two-level hierarchy in its top-level tags
    section: `role` carries `parent: iam`, `dbaas-mysql` carries `parent:
    dbaas`. The root of that chain is the provider's own product-level
    grouping, and it is what the coverage tables group by. Outscale declares
    no hierarchy, so every tag is its own root and the mapping is empty.

    A parent that is not itself a declared tag (Exoscale's `general`) is still
    a valid root: the walk simply stops there. A cycle would make the walk
    loop forever on a malformed document, so each chain refuses to revisit a
    tag it has already seen.
    """
    parent = {t["name"]: t.get("parent") for t in spec.get("tags", []) if "name" in t}
    roots = {}
    for name in parent:
        tag, seen = name, set()
        while parent.get(tag) and tag not in seen:
            seen.add(tag)
            tag = parent[tag]
        roots[name] = tag
    return roots


def group_of(op: dict, roots: dict) -> str | None:
    """The upstream group an operation belongs to: the root of its first tag.

    The first tag, because that is the one the provider files the operation
    under (Exoscale's create-snapshot is tagged instance then snapshot, and
    their own reference lists it under the instance product). The name is the
    document's own, never translated: inventing a grouping is exactly what
    reading tags exists to avoid. An operation with no tag gets no group, and
    the readers say so rather than filing it somewhere plausible.
    """
    tags = op.get("tags") or []
    if not tags:
        return None
    first = tags[0]
    return roots.get(first, first)


def path_prefix(spec: dict) -> str:
    """The path every operation hangs off, taken from the declared server URL.

    It matters because the spec's paths are relative to it — Outscale's /ReadVms
    is served at /api/v1/ReadVms, Exoscale's /instance at /v2/instance — and the
    route table check compares against the mounted path.
    """
    servers = spec.get("servers") or []
    if not servers:
        return ""
    return urlsplit(servers[0].get("url", "")).path.rstrip("/")


def read_spec(
    path: str, args, schemas: dict, operations: dict, error_refs: Counter
) -> tuple[str, str, int]:
    """Fold one document into the artefact. Returns its API version, its path
    prefix, and how many schemas it closed itself."""
    with open(path) as f:
        spec = yaml.safe_load(f)

    roots = tag_roots(spec)

    declared_closed = 0
    for name, sch in (spec.get("components", {}).get("schemas") or {}).items():
        if sch.get("additionalProperties") is False:
            declared_closed += 1
        if name in PROTOBUF_WRAPPERS:
            continue  # unwrapped at every use; the object form never reaches the wire
        schemas[qualify(name)] = schema_entry(sch, args)

    for path, item in spec["paths"].items():
        for method, op in item.items():
            if not isinstance(op, dict) or "operationId" not in op:
                continue
            oid = op["operationId"]
            entry = {"path": path, "method": method.upper()}
            if group := group_of(op, roots):
                entry["group"] = group
            if req := json_schema_of(op.get("requestBody"), schemas, oid + ".Request", args):
                entry["request"] = req
            if resp := json_schema_of(success_body(op), schemas, oid + ".Response", args):
                entry["response"] = resp
            if query := query_params(item, op):
                entry["query"] = query
            error_refs.update(declared_error_refs(op))
            operations[qualify(oid)] = entry

    return spec["info"]["version"], path_prefix(spec), declared_closed


def main() -> int:
    global NAMESPACE

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output")
    parser.add_argument("--provider", required=True)
    parser.add_argument("--source", default="")
    parser.add_argument(
        "--spec",
        action="append",
        required=True,
        metavar="[NAMESPACE=]FILE",
        help="a document to fold in; with a namespace when the provider publishes one per product",
    )
    parser.add_argument(
        "--assume-closed",
        action="store_true",
        help="treat a schema with no additionalProperties as closed; recorded as an assumed policy",
    )
    parser.add_argument(
        "--error-shape",
        metavar="FILE",
        help="a YAML fragment {name, schema} stating the refusal shape, transcribed "
        "from the SDK's error structs; overrides whatever the documents declare",
    )
    args = parser.parse_args()

    schemas: dict = {}
    operations: dict = {}
    versions: list[str] = []
    prefixes: set[str] = set()
    declared_closed = 0
    error_refs: Counter = Counter()

    for entry in args.spec:
        namespace, _, path = entry.rpartition("=")
        # A provider publishing one document per product needs its names
        # qualified: Scaleway defines ListImages twice, in instance/v1 and in
        # marketplace/v2, and its routes already tell them apart.
        NAMESPACE = namespace + "." if namespace else ""
        version, prefix, closed_here = read_spec(path, args, schemas, operations, error_refs)
        versions.append(namespace or version)
        prefixes.add(prefix)
        declared_closed += closed_here

    if not operations or not schemas:
        print("no operation or no schema found; the layout changed", file=sys.stderr)
        return 1
    if len(prefixes) > 1:
        print(f"documents disagree on the path prefix: {sorted(prefixes)}", file=sys.stderr)
        return 1

    # The refusal shape. A fragment from the SDK wins over the documents (rule
    # 4: an error shape is read in the SDK's own structs, and Exoscale's
    # document declares an RFC 9457 envelope its own egoscale does not require
    # — egoscale's error tests feed it {"message": ...}). With no fragment, the
    # shape is what the documents themselves put on their 4xx responses, and a
    # document declaring several gets the one it declares most, said out loud.
    error_schema = ""
    if args.error_shape:
        with open(args.error_shape) as f:
            fragment = yaml.safe_load(f)
        error_schema = fragment["name"]
        schemas[error_schema] = schema_entry(fragment["schema"], args)
    elif error_refs:
        error_schema, _ = error_refs.most_common(1)[0]
        if others := sorted(set(error_refs) - {error_schema}):
            print(
                f"{args.output}: 4xx responses also reference {', '.join(others)}; "
                f"errorSchema keeps the majority, {error_schema}",
                file=sys.stderr,
            )
    if error_schema and error_schema not in schemas:
        print(f"errorSchema {error_schema} names no extracted schema", file=sys.stderr)
        return 1

    artefact = {
        "provider": args.provider,
        "source": args.source or ", ".join(args.spec),
        "apiVersion": ", ".join(versions),
        "pathPrefix": prefixes.pop(),
        "closedPolicy": "assumed" if args.assume_closed else "declared",
    }
    if error_schema:
        artefact["errorSchema"] = error_schema
    artefact["operations"] = dict(sorted(operations.items()))
    artefact["schemas"] = dict(sorted(schemas.items()))

    with open(args.output, "w") as f:
        json.dump(artefact, f, indent=1, sort_keys=False)
        f.write("\n")

    # Said out loud rather than left to be discovered: an operation with no
    # response schema is one the emulator can answer anything for.
    unchecked = sorted(name for name, op in operations.items() if "response" not in op)
    if unchecked:
        print(
            f"{args.output}: {len(unchecked)} operation(s) declare no response schema "
            f"and cannot be validated, e.g. {', '.join(unchecked[:3])}",
            file=sys.stderr,
        )

    closed = sum(1 for s in schemas.values() if s["closed"])
    print(
        f"{args.output}: {args.provider} api {artefact['apiVersion']}, "
        f"prefix {artefact['pathPrefix'] or '/'}, "
        f"{len(operations)} operations, {len(schemas)} schemas "
        f"({closed} closed, {declared_closed} of them declared), "
        f"errorSchema {error_schema or 'none — refusals cannot be validated'}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
