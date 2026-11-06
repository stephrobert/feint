# ADR: explicit Object Storage doors on the existing port

Status: **accepted**, 2026-11-06, for #850. Implementation is incremental; this
decision is not a claim of shipped Object Storage support.

## Context

The neutral ObjectStore persists verified bytes. The unmounted S3 gateway has
an official AWS CLI HTTPS round-trip proof. Neither gives `feint serve` an S3
endpoint yet. Raw path-style S3 (`/bucket/key`) can collide with control-plane
paths; `http.ServeMux` also cleans dot segments and repeated slashes, which are
legitimate logical object keys.

The existing multi-provider server, browser guards and SDK-surface drift checks
must remain effective. S3 operations must not acquire fabricated provider SDK
operation names merely to appear in the control-plane coverage table.

## Decision

Keep one listening port. Opt-in S3 doors use explicit endpoint prefixes:

```text
http://127.0.0.1:4599/_feint/s3/scaleway/fr-par/
http://127.0.0.1:4599/_feint/s3/exoscale/ch-gva-2/
```

Dispatch a declared S3 prefix before the control-plane mux. Remove only that
prefix, preserving the encoded path and decoding bucket/key once in the S3
gateway. Never clean the remainder or redirect a key containing dot segments.
Unknown doors are refused, not treated as a default provider or region.
The outer Host/Origin browser guard stays in place. S3 remains loopback-only;
network exposure is not implied by the control-plane exposure option.

Use one private neutral ObjectStore. Provider/region separation is a declared
gateway namespace, not a provider name embedded in `internal/core`, and never
a bucket/key-derived filesystem path. Provider-specific XML belongs at the
protocol boundary. Declare enabled doors at startup rather than infer a tenant
from an unverified signature. This provides namespace separation, **not IAM**.

Activation and persistence must be explicit. Wire storage ownership through
serve/start/restart shutdown, configured endpoints through env, and accurate
diagnostics through status/doctor. S3 traffic needs its own honest operation
evidence; do not silently count it as an official control-plane SDK route.

The selected initial limits, configurable at startup, are:

- **1 GiB** cumulative blob bytes per store, shared across doors.
- **256 MiB** per decoded simple PUT.

Count retained blobs, crash leftovers and in-flight upload reservations.
Deleting an object or bucket removes metadata references, not its byte charge.
An upload reserves its declared byte count before reading or creating a file.
It cannot write an extra probe byte beyond that reservation. Distinct uploads
reserve atomically; identical uploads still need temporary space before their
digest is known. Reopen recounts retained bytes, counting hard-linked names of
one file once. No metadata format change or persisted quota counter is needed.
The budget excludes metadata, filesystem block/inode overhead and other files
outside the blob directory; it is not an OS filesystem quota.

Refuse over-budget uploads without changing existing metadata or deleting
existing data. Reads and metadata deletion remain available at the limit.
Opening existing data over the selected limit fails explicitly; increasing the
configured budget is non-destructive. Do not add automatic garbage collection.

## Alternatives

- A second listener would weaken the single-port product contract and require
  another endpoint/lifecycle surface.
- S3 hostnames under `.localhost` avoid path collisions but require proving
  name resolution, virtual-host addressing and the corresponding client
  behavior. They are not needed for the first configurable endpoint.
- Root-level S3 fallback makes URL ownership ambiguous and risks mux redirects.
- Independent provider storage engines duplicate byte, integrity and quota
  behavior. Provider-specific wire envelopes do not justify that duplication.

## Acceptance and deferred work

Prove declared door dispatch, exact key preservation, namespace separation,
browser refusal, budget thresholds and concurrent reservations. Falsify guards
in an external copy. Drive complete `scw`/`exo` configurable-client lifecycles
with canonical fake credentials in isolated configurations; verify bytes after
restart of the real emulator. Implement ancillary operations from measured
client requests and trustworthy response shapes, not successful empty stubs.

The configured-client door must be proved against clients that actually support
the chosen endpoint prefix. If a client rejects it, revisit this decision rather
than quietly add a second port or change its workload.

Virtual hosts, trusted intercepted-host handoff, hardcoded Scaleway Terraform
endpoints, checkpoint/reset, garbage collection and public-stack certification
remain later work. Existing Object Storage support limits stay unchanged until
the mounted-client proofs exist.
