# ADR: explicit Object Storage doors on the existing port

Status: **accepted**, 2026-11-06, for #850. Implementation is incremental; this
decision is not a claim of shipped Object Storage support.

## Context

At the time of the decision, the neutral ObjectStore persisted verified bytes
and the unmounted S3 gateway had an official AWS CLI HTTPS round-trip proof,
but neither gave `feint serve` an S3 endpoint. Raw path-style S3 (`/bucket/key`) can collide with control-plane
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

## Implemented mounted slice

`serve` and `start` now accept the same explicit storage settings; `restart`
replays the recorded absolute storage root even from a different working
directory. The parent must already exist and be safely owned: the command
does not change the permissions of an existing operator directory.

```bash
mkdir -m 700 object-storage
feint start --objectstore "$PWD/object-storage/data" \
  --objectstore-budget-mib 1024 --s3-object-limit-mib 256 \
  --s3-doors scaleway/fr-par,exoscale/ch-gva-2
feint env scaleway --s3
feint env exoscale --s3
feint status
feint doctor --s3 --vm off
feint restart
feint stop
```

Storage is disabled without `--objectstore`. Limits are integer MiB
(1 MiB = 1,048,576 bytes); the defaults are the selected 1 GiB / 256 MiB.
The door list defaults to the two paths above. A declaration selects a
provider dialect and a local region namespace; it is not a promise that the
region's complete cloud API is served or separately measured.

`env --s3` discovers the running instance on a loopback control-plane endpoint,
without an inherited proxy or redirect. Disabled, missing or ambiguous doors
fail before stdout contains any export. Use `--s3-region` to choose among
multiple enabled regions of a provider. Ordinary `env` remains offline and
control-plane-only. `env --unset` also removes S3 endpoint variables, without
requiring a running instance. All three shell renderers use this same selection.
These are provider CLI variables, not an AWS CLI credential configuration.

The separate `GET /_feint/s3` report (schema version 1) publishes actual
activation, declared paths, the store's configured blob-byte limit, the
per-object limit and completed HTTP-response counts per declared door/status.
It publishes no credentials, keys, payloads or storage paths. These counts
are **not official-client proof** and do not enter control-plane SDK coverage,
trace or conformance scores. Status reports disabled and unknown distinctly;
`doctor --s3` requires enabled storage. The health schema is unchanged.

Each mounted PUT installs a 30-second body-read deadline, independently of
the existing control-plane and runtime write deadlines. The server retains
its 10-second header deadline and the standard library's bounded HTTP header
size. The S3 handler rejects browser Origins and non-loopback peers/Hosts even
when the control plane is exposed.
An exposed wildcard listener advertises a usable literal loopback S3 URL,
not `0.0.0.0` or `::`. Activation on a specific non-loopback listen address is
refused: that socket cannot receive the peers the S3 boundary requires.
HTTP shutdown precedes releasing storage
ownership; an unsuccessful graceful shutdown force-closes HTTP connections.
Resource snapshots (`--state`, `/_feint/state`) do not include object bytes,
and `--cleanup` does not delete their persisted blobs.

`mise run conformance:s3:mounted` builds the real binary, runs `start`, drives
AWS CLI **2.32.17** through both prefixed doors, proves byte/checksum identity
and namespace separation, restarts from another directory, retrieves the
bytes again and independently checks that its literal key survived storage.
The 73,728-byte binary witness has SHA-256
`4e5fadf6593bcd790f284d715b339960185aa4c760e56bbd79a516d39f975ec1`.
It uses isolated configuration and canonical public fake credentials, with a
non-forwarding proxy and a positive CONNECT refusal control. The proxy is not
a network namespace. It stops only its recorded process and owns its scratch
data. This is an HTTP actual-process witness;
`mise run conformance:s3` separately retains the HTTPS streaming/trailer and
hostile-response-checksum controls.

This certifies simple CreateBucket, PutObject, GetObject, HeadObject,
DeleteObject and DeleteBucket through the mounted gateway, plus the bounded
Scaleway bucket workflow below, not complete `exo` lifecycles. Unsupported
ancillary operations, missing-object cloud errors and multipart still receive
explicit Feint refusals. No ETag is invented.
The snapshot-import route still records an intention rather than reading these
bytes. Object Storage 1.0 and hardcoded Terraform redirection are not delivered.

## Measured Scaleway bucket workflow

The declared Scaleway doors additionally serve `scw` **2.62.0** bucket
`create/list/get/delete`, with the ordinary defaults: private ACL, no Object
Lock and `enable-versioning=false`. `mise run conformance:s3:mounted` now
requires that pinned client as well as AWS CLI. It creates the bucket with
`scw`, uploads/downloads the binary witness with AWS CLI (the Scaleway CLI has
no object upload command), and repeats both bucket reads and byte retrieval
after the real process restarts. Creation dates and the canonical local owner
remain stable; listing reads the actual namespace-scoped inventory.

The wire contract comes from the official CLI source and historical HTTP
cassettes at revision `a2a40172bb27582c2a0fe37a4837f949cb10e6a6`,
particularly
[simple create](https://github.com/scaleway/scaleway-cli/blob/a2a40172bb27582c2a0fe37a4837f949cb10e6a6/internal/namespaces/object/v1/testdata/test-bucket-create-simple.cassette.yaml)
and
[list](https://github.com/scaleway/scaleway-cli/blob/a2a40172bb27582c2a0fe37a4837f949cb10e6a6/internal/namespaces/object/v1/testdata/test-bucket-list-simple.cassette.yaml).
This is historical Scaleway evidence, not an Exoscale response oracle or a
new authenticated cloud recording.

`CreateBucket` checks the optional XML region against its declared door.
`PutBucketVersioning` verifies its bounded body/checksums, including the
client's legacy Content-MD5, before persisting `Suspended`. A new bucket has
no explicit versioning status until that write. `Enabled`, tags writes,
non-private permissions and Object Lock remain logged Feint refusals.
`GetBucketTagging` uses the recorded `NoSuchTagSet` response because tag
creation is not supported; `GetBucketAcl` describes the only accepted policy.
The stable owner is a local fixture identity, not authenticated IAM.
The isolated SCW cache has a fresh `last-cli-checks` marker, using the pinned
client's own predicate to suppress unrelated GitHub version/API-key checks.
The non-forwarding proxy remains enabled; no host cache or credentials are used.

The SDK refuses a trailing slash in its configured endpoint, and ListBuckets
uses that bare root. `env --s3` therefore exports endpoints **without** their
terminal slash, for every shell. The report retains canonical slash-suffixed
door prefixes. The dispatcher accepts a canonical bare GET for the declared
bucket workflow, without redirecting, cleaning paths or accepting encoded
aliases. Literal object-key handling is unchanged.

Bucket attributes belong to the neutral byte store, in private metadata
version **2**. Version 1 indexes remain readable without a read-time rewrite;
the next mutation upgrades them. An older binary cannot read the upgraded
index. Existing bytes, generations and quotas remain unchanged. Legacy buckets
without a stored creation date receive an explicit listing refusal rather
than an invented date. Resource snapshots still do not own this metadata.

Exoscale upload/download/delete, its mandatory ListMultipartUploads during
bucket removal, native bucket-usage API, and full Object Storage 1.0 remain
separate follow-up work. No empty multipart success stub was added.
