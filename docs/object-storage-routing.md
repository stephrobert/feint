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

**Amended 2026-11-07:** the measured Exoscale client discards endpoint paths.
An additional opt-in `--s3-host-doors` declares synthetic provider/region
authorities, served through absolute-form HTTP proxy requests on the same
listener. Prefix doors remain available. This advances local host dispatch,
not TLS interception or hardcoded cloud-host routing.

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

The sections below preserve the sequence of measured slices. The final sections
describe the current Exoscale discovery, isolated Terraform witness and neutral
byte checkpoints. Coordinated resource/byte reset, a user-facing isolated run
command and public-stack certification remain later work.

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

The separate `GET /_feint/s3` report (now schema version 2, previously 1) publishes
actual activation, declared paths and optional synthetic hosts, the store's configured blob-byte limit, the
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
ancillary operations and missing-object cloud errors still receive explicit
Feint refusals. Multipart is measured separately below; ordinary object reads
still do not advertise ETags.
The snapshot-import route still records an intention rather than reading these
bytes. Object Storage 1.0 is not delivered; the isolated Terraform storage
witness below now intercepts its hardcoded S3 endpoint.

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

Normal Exoscale creation and native usage are now measured below. Full Object
Storage 1.0 remains separate work. Multipart inventory and bucket removal
operate on real sessions.

## Exoscale prefix incompatibility measured

`mise run conformance:s3:exo:endpoint` pins **exo 1.95.1**, revision
`7aaf76d0b13b8e10a343ec88c9cb03116f09b4c1`, with an isolated HOME,
pack-owned public fake credentials and a positively tested refusing proxy.
A local measurement listener receives the commands; it never forwards them.

With `EXOSCALE_SOS_ENDPOINT` and `EXOSCALE_STORAGE_API_ENDPOINT` set to
`http://127.0.0.1:<port>/_feint/s3/exoscale/ch-gva-2/`, `storage upload`
issues **HEAD /feint-bucket**, without the configured prefix. When the
measurement listener supplies the bucket-region header, the next request is
**PUT /feint-bucket/exo-byte-proof**, also without that prefix.
The PUT is deliberately refused with 501: this is an addressing measurement,
not an upload success or a SOS cloud-response fixture.

The same client's normal `storage mb` additionally reads ACL/CORS/ownership,
download enumerates objects then uses a range downloader, and `storage rb`
lists multipart uploads before deletion. Fixing only region discovery cannot
make its lifecycle work.

The prefixed door remains proved with AWS CLI and `scw`, **not exo**.
Prefix-only endpoint exports do not change this client's measured addressing
behavior; they are not certified Exoscale CLI configuration. This measurement
prompted the host-door amendment above. Bare root requests
must not be guessed into a namespace: different providers/regions can
legitimately contain the same bucket name. No such fallback was added.

## Measured Exoscale upload through a synthetic host

Start with `--s3-host-doors` in addition to `--objectstore`. Each declared scope
then has an authority such as `exoscale.ch-gva-2.s3.feint.invalid`. This is
**path-style S3 at a scoped authority**, not bucket virtual-host addressing.
The `.invalid` name is not resolved: the client sends an absolute HTTP URL to
the emulator as its HTTP proxy, on the existing port.

```bash
feint start --objectstore "$PWD/object-storage/data" --s3-host-doors
# Use a dedicated shell with fake credentials, not an operator account.
eval "$(feint env exoscale --s3)"
exo storage upload input.bin sos://existing-bucket/key
eval "$(feint env exoscale --s3 --unset)"
```

For this initial upload-only slice, the bucket had to exist. Creation, download and
cleanup workflows are **not** certified by this upload proof.

For Exoscale, `env --s3` selects the live declared authority and exports upper/lower-case
HTTP/HTTPS/ALL proxy variables pointing at the literal loopback endpoint, with
NO_PROXY restricted to loopback control-plane names. This replaces the dedicated
shell's previous proxy settings. Apply `env --s3 --unset` to remove these exports;
it does not restore earlier values. Ordinary `env --unset` retains its previous
behavior and does not clear unrelated proxy variables. Discovery refuses
foreign or mismatched reported hosts instead of exporting them.
The pack declares whether its client needs host addressing: Scaleway keeps
its measured prefix endpoint and does not gain shell proxy exports when host
doors are enabled. The mounted witness also exercises this exported Scaleway
configuration, so enabling Exoscale does not silently change the other client.

Only exact declared authorities with matching absolute URL/Host and scheme
`http` are served. Undeclared targets, CONNECT, URL credentials, aliases and
authority mismatches are refused; nothing is forwarded. Host S3 cannot reach
health, state, reports or native routes. Prefix/control-plane requests retain
the rebinding guard; host S3 independently rejects every browser Origin and any
non-loopback peer, even under `--expose`. No host-header rewriting, global DNS,
certificate installation, extra listener or external S3 service is used.

The existing `conformance:s3:mounted` witness now also runs **exo 1.95.1**
`storage upload`, using the exact exported environment in its private HOME.
It proves actual HEAD/PUT host exchanges, binary identity and SHA-256 by AWS
retrieval through the corresponding prefix, persistence after real restart,
another Exo upload after restart, and a different same-name object in the other
provider. Unit tests additionally hold region separation, literal encoded keys,
browser/peer refusals and read deadlines. The declared guards are falsified in
`tools/falsify/specs/s3-host-doors.json`.

The witness uses pack-owned public fake credentials. Its refusing proxy is not
a network namespace and does not constrain unrelated clients that ignore proxy
settings. Host response counters are separate from prefix counters and start
again at zero on restart; neither is official-client or SDK-coverage evidence.

## Measured Exoscale listing and multi-range downloads

`conformance:s3:mounted` also drives exo 1.95.1 object listing and downloads
through the same scoped host. It checks actual listing keys, sizes and dates
and compares downloaded binary bytes and SHA-256 before and after a real process
restart. The small object is 73,728 bytes. A 6,340,608-byte object exceeds the
client's 5 MiB download part size and produces multiple `206` responses.
That object is now uploaded by **Exo multipart**, with independently verified
stored parts and assembled bytes, as described below.
AWS additionally decodes two actual one-entry listing pages, including a literal
key with escaped characters.

The gateway implements **ListObjectsV2 only**, with bounded prefix, delimiter,
start-after and max-keys (0..1000), deduplicated common prefixes and scoped
continuation tokens. An empty optional token means the first page, as the real
Exo downloader requests it. Cursors are bounded, closed JSON bound to the
provider/region/bucket/prefix/delimiter; they are neither signed nor IAM and do
not freeze an inventory against concurrent mutations. `encoding-type=url`
preserves keys unrepresentable in XML; without it, such keys are explicitly
refused, never silently replaced. Owner requests and v1 listing remain refused.

Single `bytes` ranges support closed, open-ended and suffix forms. GET returns
only the selected bytes with actual Content-Length and Content-Range.
Unsatisfiable ranges return a plain Feint `416` with `bytes */<actual size>`.
Multiple ranges, conditional reads and Range on other methods remain refused.
Every range still verifies the **entire stored blob** before seeking.
Partial responses deliberately omit whole-object checksum headers; full reads
retain the verified SHA-256. ETags remain absent.

Publication dates were introduced in private metadata **version 3**; the current
multipart format is **v4** and still reads v1/v2/v3 without rewriting them. PUT persists
its publication date, reused by GET/HEAD and listings across restart. Old
undated object references retain no Last-Modified header, and pages containing
them refuse listing until those objects are overwritten. Neither blob mtime nor
restart time is substituted. **Older binaries cannot read an upgraded v4 index**:
keep a pre-upgrade copy of the whole owned store if rollback is required.
Resource snapshots still do not own these bytes.

The date, listing and range properties are replayed by
`tools/falsify/specs/s3-download.json`. This milestone does **not** certify normal
Exo bucket creation, ACL/CORS/ownership discovery, native bucket usage,
version histories, TLS or Terraform redirection.

## Measured Exoscale bulk deletion

The pinned exo 1.95.1 `storage delete --force --recursive --verbose sos://BUCKET/`
enumerates keys with ListObjectsV2, then sends DeleteObjects. The mounted witness
failed with an actual 501 before this support. It now deletes five real keys,
including a literal escaped key, and checks every reported key. An independent
AWS CLI inventory is empty before and after another actual process restart;
same-name objects in the Scaleway namespace still return their original bytes.
AWS also decodes the idempotent deletion of a missing key using CRC32.

The request/response members and XML namespace follow the S3 v1.2.0 serializer
and deserializer used by that Exo release. The bounded subset accepts 1..1000
unversioned keys in at most 8 MiB of decoded XML. Content-MD5, CRC32 or SHA-256
transport validation finishes before any deletion. Unknown XML fields, versions,
attributes, duplicate singleton fields, invalid sizes/checksums and unrelated
query features are refused before changing any reference.

Verbose responses identify every requested deleted key, including missing keys.
Quiet responses omit successful keys only after actual deletion. The neutral
store publishes one metadata transaction and retains immutable blobs and their
quota charge: this is not GC. A store error yields an explicit logged
whole-request Feint refusal, not invented mixed per-key cloud errors. An
indeterminate directory-sync failure still faults the store until reopen.
The current multipart-enabled index is v4.

`tools/falsify/specs/s3-delete.json` holds the destructive-input checks and
publication properties. Mixed per-key cloud errors remain outside this measured
slice of #856; the complete bucket-creation workflow remains #858.

## Measured persistent multipart uploads

`conformance:s3:mounted` drives **exo 1.95.1** uploading the 6,340,608-byte
fixture through its actual multipart uploader. AWS retrieves identical assembled
bytes and SHA-256. The stopped owned store contains the distinct last-part blob;
this is not a simple PUT disguised as multipart.

AWS CLI **2.32.17** additionally initiates two real sessions and uploads parts.
Nonempty session and part inventories remain identical across an actual process
restart. AWS resumes one session, completes it and retrieves the expected bytes.
Finally, `exo storage rb --force` deletes five actual objects, lists and aborts
the real remaining session, then removes the bucket. The stopped store confirms
the bucket is absent; same-name Scaleway objects retain their original bytes.
This certifies removal of an existing bucket, not normal Exo bucket creation.

The served subset is initiate, upload/replace part, list uploads, list parts,
complete and abort. Part numbers are 1..10000; selected completion parts must be
ascending and match the exact observed tag. Every selected part except the last
must be at least 5 MiB. Completion verifies each immutable part before streaming
the assembled blob and commits object publication and session removal together.
Failed completion preserves the old object and session.

Sessions survive restart in private metadata **v4**. New stores and successful
mutations write v4; versions 1..3 remain readable and reads do not rewrite them.
Older binaries cannot read v4: rollback requires a pre-upgrade copy of the
**whole owned store**, not just its index. Resource snapshots still omit bytes.
The assembled object uses the configured simple-PUT limit, **256 MiB** by
default. Parts and assembly both count against the shared retained/reserved blob
budget, **1 GiB** by default, unless identical blobs deduplicate. Completion
requires sufficient peak reservation; abort and deletion remove references but
perform no GC and release no retained-byte charge.

Part ETags are quoted, verified SHA-256 opaque tokens, not promises of MD5 or
generic object ETag semantics. Completion returns Bucket, Key and this opaque
ETag; it does not certify consumers requiring a Location URL. Upload pages sort
by key and initiation time, with ID as tie-breaker. A same-key upload marker
must identify a live session; missing/deleted markers are explicitly refused.
The pinned Exo bucket remover does not paginate upload inventory, so its proof
does not certify cleanup beyond one upload page (1000 sessions). Multipart
copy, optional checksum negotiation beyond the measured requests and provider
cloud-error dialects remain explicit Feint refusals, not invented successes.

`tools/falsify/specs/s3-multipart.json` replays the input, scope, bound and
transactional-publication checks. The following slices add default discovery,
native usage and a bounded Terraform witness. Full Object Storage 1.0 and
public-stack certification remain outside this delivery.

## Ordinary Exoscale bucket lifecycle and native usage

`mise run conformance:s3:mounted` now runs ordinary `exo storage mb` and
`storage show`, then upload/list/download and bucket removal. Default private
ACL, missing CORS (404) and missing ownership controls (**400**, not 404) follow
the seven-exchange, user-authorized empty-bucket recording in
[exoscale-sos-empty-bucket.json](./evidence/exoscale-sos-empty-bucket.json).
That temporary cloud bucket was deleted; the ongoing witnesses use fake keys.
Setting writes remain refused, not invented successes.

`GET /v2/sos-buckets-usage` reports persisted creation dates and logical
published object sizes from the same neutral store. It excludes retained blobs
and unpublished upload parts. Disabled storage and malformed legacy metadata
produce explicit errors. The mounted witness compares `exo storage show` and
native usage across process restart, and proves native inventory empty after
removal. Its native control plane and S3 door both use `ch-gva-2`; this does not
certify a mismatched zone configuration.

## Isolated Scaleway Terraform storage witness

The official **Scaleway provider 2.82.0** ignores `SCW_S3_ENDPOINT` in its S3
client and uses cloud-shaped HTTPS virtual hosts. `s3.LocalTransport` maps only
declared authorities to an explicit loopback emulator; unknown hosts never dial.
The existing proxy supplies a temporary CA and exact-SNI leaves. Dotted bucket
names and literal encoded keys are held by transport tests; the Terraform
witness uses an explicitly allowed bucket hostname.

Recorded private bucket ACL writes, absent CORS/object-lock/lifecycle errors,
ListObjects v1 and object private-ACL/empty-tag reads follow the pinned provider's
[official object cassette](https://github.com/scaleway/terraform-provider-scaleway/blob/731e3dce545df08853ea490cd144de10b3216492/internal/services/object/testdata/object-basic.cassette.yaml).
The Scaleway ACL identity names the first declared local project (the default
project if none was declared). This is a single storage-tenant model, not IAM.
Public ACLs, tag writes and object-version histories remain refused.

`TestScalewayTerraformStorageInIsolatedNetwork` requires a disposable Linux
network namespace with **only loopback up**, and refuses root execution.
Download the published provider outside that namespace first; point
`FEINT_TF_PLUGIN_DIR` at its installed provider directory. Then:

```bash
testbin="$(mktemp)"
go test -c -tags=conformance ./internal/cli -o "$testbin"
cd internal/cli
sudo -n unshare --net -- sh -c \
  'ip link set lo up && exec setpriv --reuid="$1" --regid="$2" --init-groups env PATH="$3" FEINT_TF_PLUGIN_DIR="$4" "$5" -test.run=^TestScalewayTerraformStorageInIsolatedNetwork$ -test.v -test.timeout=5m' \
  sh "$(id -u)" "$(id -g)" "$PATH" "$FEINT_TF_PLUGIN_DIR" "$testbin"
rm "$testbin"
```

## The mirror witness: the same trip with no privilege

`TestScalewayTerraformStorageThroughFeintRunOnTheHostNetwork` is the other half,
and the two are only worth having together. It **refuses to run inside a
namespace**, because a pass obtained in one would prove nothing about the
command: the namespace itself would be doing the containment. It answers the
question #849 actually asks, which is whether an operator can get there on the
host network, with no privilege and no change to the machine.

It drives `feint run`, which takes no hostname. The mounted doors come from the
emulator's own report and each door's cloud authority from the pack in the
binary, so the only possible targets are doors that exist. Run it against a
provider mirror, never a plugin cache at the same path:

```bash
mkdir -p /tmp/tf-mirror /tmp/tf-seed
printf 'terraform {\n  required_providers {\n    scaleway = {\n      source = "scaleway/scaleway"\n      version = "= 2.82.0"\n    }\n  }\n}\n' > /tmp/tf-seed/main.tf
(cd /tmp/tf-seed && terraform providers mirror -platform=linux_amd64 /tmp/tf-mirror)
FEINT_TF_PLUGIN_DIR=/tmp/tf-mirror go test -tags=conformance ./internal/cli \
  -run '^TestScalewayTerraformStorageThroughFeintRunOnTheHostNetwork$' -count=1 -v
```

**Not `TF_PLUGIN_CACHE_DIR` pointing at that same directory.** The first run
then unpacks into the mirror and every later run fails with `cannot install
existing provider directory to itself`. This witness was green once and red the
second time for exactly that reason, which is a witness that proves nothing on a
machine that has already run it.

Measured on the published provider 2.82.0, host network, no sudo: apply
complete, **139,264 identical bytes** read back from the local byte store by
sha256, an empty second plan, a complete teardown, and the bucket inventory back
to empty. The byte digest is the proof rather than the apply's exit code: a
request that went anywhere else cannot produce it, and the real cloud would
refuse these deliberately public fake credentials long before storing anything.

Two findings the command carries from that measurement:

- **`AWS_CA_BUNDLE` is stripped, not set.** An earlier draft set it, reasoning
  that an S3 client would want it; the provider then failed its apply with
  `unable to add custom RootCAs HTTPClient, has no WithTransportOptions,
  *http.Client`, because it hands its SDK a prebuilt client. `SSL_CERT_FILE`,
  which `crypto/x509` reads, covers every Go client. The variable is also
  removed from an inherited environment, since the station's own bundle cannot
  contain a freshly minted authority.
- **The virtual-host form must be intercepted too.** A path-style probe passes
  while every `<bucket>.<authority>` request, the form Terraform uses, is
  refused and never tunnelled. The allow-list therefore carries a single-level
  wildcard per door, and it only widens what is intercepted: `LocalTransport`
  still refuses a leading label that is not a valid bucket name.

The witness checks an unreachable external address, apply, an empty second
plan, **122,880 identical bytes**, byte-store close/reopen, another empty plan,
destroy and explicit owned GC. This is the storage-shaped bucket/object graph,
not a certified Flatcar image: the qcow2-named payload is arbitrary test bytes.
There is no provider fork, global hosts/DNS/CA change or cloud-capable fallback.
The proxy is a test harness, **not a shipped `feint run` command**.

## Neutral byte checkpoints and explicit GC

The neutral store now exposes durable named save/reset/delete checkpoints and
explicit collection. Checkpoints preserve complete byte manifests and upload
sessions in the same owned root. Exclusive checkpoint/reset/GC operations block
active mutations; collection preserves current and checkpoint-referenced blobs,
with deduplication/hardlink accounting.

Race tests and `tools/falsify/specs/objectstore-checkpoints.json` hold this
library slice. Readers can delay exclusive operations; quota accounting excludes
unlinked files still held open by a reader. Failed GC can have partially collected
unreferenced files and reports the error. Malformed checkpoints require repair.
These checkpoints are neither portable exports nor coordinated control-plane/
runtime snapshots. The CLI's resource snapshots still omit object bytes; #853
is not complete.
