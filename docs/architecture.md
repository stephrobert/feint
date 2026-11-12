# Architecture

How Feint is put together, and why it is put together that way. This is the page
to read before changing anything under `internal/`; the [README](../README.md)
covers what the emulator does, and `docs/limits.md` what it deliberately does
not.

## The shape

```text
cmd/feint/                  the entry point, and nothing else
internal/cli/               the commands: lifecycle, serve, env, doctor, snapshot,
                            coverage, probe, docs, catalog, clean, version
internal/core/resource/     the neutral resource: ID, Kind, Tenant, State, Attrs, Runtime
internal/core/store/        in-memory storage plus JSON snapshot, with no provider knowledge
internal/core/objectstore/  neutral persisted object bytes and metadata
internal/s3/                bounded S3 protocol and same-port explicit door dispatch
internal/conformancetest/   isolated pinned AWS CLI harness, under the conformance tag
internal/core/emulator/     Env, Pack, Route, the single-port mount, /_feint/*
internal/core/machine/      the machine runtime behind --vm: Incus containers, VMs, OVN
internal/core/network/      address plans, subnets, firewall bindings
internal/core/cloudinit/    the boot payload a provider hands its machines
internal/core/serialise/    keyed mutual exclusion: one lock per named target
internal/core/sshkey/       the OpenSSH one-line public key format, read and refused
internal/core/logline/      a value a client controls, spelled as one line for the log
internal/contract/          API descriptions, and the check that answers match them
internal/corpus/            turns a recording of a real cloud into an artefact this repository
                            may commit: values synthetic, statuses and order kept
internal/drift/             the upstream surface scan, coverage, baselines
internal/environment/       feint.yaml: what one environment is, and the schema that
                            refuses by name what it does not carry
internal/probe/             drives every mounted route from its API description
internal/replay/            reissues a recorded exchange here and grades the answer
internal/proxy/             records what a real client and a real cloud say to each other
internal/shape/             what a real cloud was observed to return, and what is omitted from it
internal/stack/             reads what a Terraform or OpenTofu configuration declares: block
                            headers, never expressions, so the set of declared types is exact
                            and everything needing evaluation is reported unknown
internal/trace/             the record of one HTTP exchange, for /_feint/trace and the log
internal/transcript/        turns a proxy recording into what to serve next
internal/upstream/          talks to the real clouds, so that nothing else has to
internal/compat/            classifies a consumer's expression across two releases (compat:check)
internal/release/           what a release owes its consumers: whether the note names what
                            changed hands (release:surface), and the Homebrew formula
                            derived from the checksums it signed (release:formula)
internal/providers/<name>/  one pack per emulated cloud
tools/conformance/<name>/   that provider's real clients: CLI, Terraform, OpenTofu
coverage/, contracts/       versioned artefacts the gates read
```

Two directories carry the whole design: `internal/core/` knows how an emulated
cloud behaves, `internal/providers/` knows how *this* cloud says it.

## One port, three clouds

The three providers do not collide in URL space. Scaleway serves
`/<product>/v<N>/…`, Outscale answers `POST /api/v1/<Action>`, Exoscale uses
`/v2/<resource>`. One `http.ServeMux` therefore hosts all three, and `NewServer`
refuses to start if two packs claim the same pattern — a collision is a bug that
must surface at boot, not as a request served by the wrong pack.

That is what makes the multi-provider case practical: one process, one endpoint,
one thing to start in CI.

## The neutral core

`internal/core` must never know a provider exists. Not by name, not by prefix,
not by special case.

The rule earns its keep because violations are quiet. The machine-event watcher
once filtered on `"scw-"`, `"osc-"` and `"exo-"`: three provider names written
into the core, which would have forced a fourth pack to edit `internal/core`
before its own events could be reported. Nothing failed. It simply would have
stopped working for whoever came next.

**The test, applied to any line in a pack:** could this line be written
identically for another provider? If yes, it belongs to the core. What stays in
the pack is what only that provider knows — its image catalogue, the login its
machines use, the field its API publishes an address under, or the fact that it
publishes none.

## Neutral object bytes and explicitly mounted S3 doors

[objectstore](../internal/core/objectstore/store.go) is an independent library
for #850, activated only by `serve`/`start --objectstore`. It does not imply
complete provider Object Storage support. Its caller explicitly selects a private root
whose parent already exists. New roots are mode 0700; files are mode 0600.
The pinned parent must belong to this user or root; group/other write permission
requires its sticky bit so another UID cannot replace the newly created root
before it is opened. A sticky directory owned by a foreign UID is not trusted.
Existing unmarked roots, symlinks, non-regular files, foreign owners and public
permissions are refused, not repaired or adopted.

The store holds a directory lock across its lifetime on Linux/macOS local
filesystems. Logical bucket names and keys are JSON data, never paths. A
streaming PUT declares its exact decoded size, writes and syncs a temporary
blob, links it under its SHA-256 without overwriting an existing blob, and
syncs that directory before atomically publishing and syncing metadata.
Uploads to distinct keys run independently. A generation check prevents an
unfinished upload from repopulating a deleted or recreated bucket.

GET and HEAD verify the complete blob; listing is a metadata inventory only.
The digest is not an S3 ETag. Deletion removes references, not blobs. The
private `feint-objectstore` v1 index is not `feint-snapshot` v1: no snapshot,
checkpoint/reset or cleanup command reads it yet. Unreferenced blobs and
temporary files from a process crash are retained; there is no collector or
recursive root deletion.
An interrupted initial creation may leave an unmarked directory; inspect and
remove that explicitly rather than asking Open to adopt it.

A successful mutation follows file and directory syncs. If metadata was renamed
but its directory sync fails, the call reports failure and the instance requires
close/reopen; failure may therefore have published metadata. Reopen, corruption
and concurrency are tested, not actual power loss or network filesystem locks.
The root is not a sandbox against a malicious process sharing its owner's UID.
The caller must impose storage quotas and unblock its input reader on
cancellation; waiting for an existing same-key transaction may also delay
cancellation. No structured format passes through a text template.

`OpenWithLimit` adds an explicit cumulative blob-byte budget without changing
the index format or `Open`'s unlimited library behavior. It counts retained and
orphan data on reopen, with hard-linked names counted once, and reserves each
upload's declared size before file creation or input reads. The EOF probe is
read into memory, never written beyond that reservation. Reservations are
released after temporary-file cleanup; new published blobs remain charged even
when metadata publication fails. Failed cleanup retains the partial byte charge.
Deletion does not free blob bytes. Identical uploads still require temporary
space. Metadata and filesystem overhead are outside this byte budget.
See the [accepted door ADR](object-storage-routing.md) for the selected opt-in
defaults (1 GiB cumulative, 256 MiB per simple PUT) and the mounted
same-port server integration.
The gateway renders a budget refusal as a logged Feint HTTP 507 text response,
not an unmeasured S3 XML error. The official-client component proof uses a small
explicit cumulative budget; deployment limits are configurable CLI options.

### Explicit S3 protocol, not complete Object Storage support

[s3](../internal/s3/server.go) is a direct HTTP handler over that store, with no
listener, control-plane SDK route or snapshot integration.
[Doors](../internal/s3/doors.go) dispatch declared provider/region prefixes
before the mono-port server's cleaning mux, over one shared store with opaque
namespaces. CLI composition owns activation and shutdown. Trusted intercepted
host handoff and virtual-host routing remain deferred, not implicit promises.
It accepts loopback peers and literal loopback/localhost endpoint hosts only.
SigV4 credentials are accepted without authenticating their secret; they are
never logged. Local callers still have access to the whole selected store.

Run `mise run conformance:s3` with **AWS CLI 2.32.17** installed. Absence or a
different version fails, never skips. The isolated HOME uses the repository's
public fake credentials, an ephemeral trusted HTTPS certificate, metadata
discovery disabled and a non-forwarding proxy with a positive CONNECT control.
This is a client proxy boundary, **not a network namespace or a proof that
arbitrary clients cannot bypass proxies**.

The witness performs CreateBucket, PutObject, GetObject, DeleteObject and
DeleteBucket against the component, retrieves 73,728 identical binary bytes and
checks their independent SHA-256. HTTPS makes this client emit `aws-chunked`
with a CRC32 trailer. CRC32 is an explicit fixture selection, not a claim about
the default algorithm or unmodified Terraform stacks. A sixth GET deliberately
changes only the response checksum: the official client must reject it. No
ETag is emitted; this client succeeds without one. This does **not** establish
Scaleway/SOS ETag behavior or support clients that require ETags.

Uploads require an exact decoded length, capped by the caller's byte bound.
Framing is decoded and CRC32/SHA-256 checksums verified **before** EOF can let
the core publish metadata. The raw body is bounded to twice that cap plus
64 KiB; the trailer section is capped at 8 KiB. Logical object keys are decoded
once, never cleaned. HTTP metadata is validated at intake and again after
reading the neutral store, including case-insensitive header collisions.
Callers need server read/header deadlines, header limits and **cumulative**
disk quotas; this per-request bound does not prevent many uploads or reclaim
unreferenced blobs.

Only empty-body bucket creation, bucket HEAD/deletion and full object
PUT/GET/HEAD/deletion are implemented. Queries other than SDK `x-id`, range and
conditional requests, ACLs, policies, versioning, multipart, encryption,
Content-MD5 and other checksum algorithms are explicitly refused. There are no
successful empty bucket-listing or multipart stubs.
The only cloud XML error is NoSuchBucket, with explicitly selected Scaleway or
SOS envelopes from the anonymous HTTPS GET recordings of
2026-11-06T20:33:53Z at `s3.fr-par.scw.cloud` and `sos-ch-gva-2.exo.io`.
[The test](../internal/s3/server_test.go) decodes the recorded producer bodies;
their response identifiers are redacted, not their fields. Applying that error
family to other missing-bucket operations is an adapter choice, not another
cloud measurement. NoSuchKey, conflicting creation and BucketNotEmpty still
get a logged **Feint 501 text refusal**, not fabricated cloud XML.

That witness proves a protocol component. `mise run conformance:s3:mounted`
separately drives the actual `feint start`/`restart` process on both declared
doors and proves bytes, checksums, literal keys and persisted namespaces.
Neither proves complete `scw`/`exo` lifecycles, a surveyed Terraform stack,
interception or reset. The full control-plane conformance population is
unchanged. [The declared mutations](../tools/falsify/specs/s3-gateway.json)
hold checksum, framing, size, metadata and unsupported-feature guards.

## What varies is a field, not a convention

Three packs do the same things with different words, and confusing the two levels
is the expensive mistake here, in both directions.

**The shapes must differ.** Scaleway answers `{"server": {…}}`, Outscale
`{"Vms": […], "ResponseContext": {…}}`, Exoscale an asynchronous operation.
Flattening that into one shape would break the whole point: each client must find
its own cloud.

**The behaviour must not.** Powering on a machine is the same sequence
everywhere: render cloud-init if the client supplied none, name and label the
machine, keep its name out of the API's reach, publish the address, withdraw it
on stop, degrade without breaking the control plane when no runtime is
configured. Written twice, a defect fixed on one side survives on the other —
and it did.

So the variation is a field. `machine.Binding` takes a prefix, a login, an
address key: three packs, one sequence, no copy. And when a provider does not
fit — Exoscale declares its login *per template*, not per cloud — the
abstraction widens (`Boot.User`); the pack never invents a value to fit the
mould.

And what a pack may ask of the runtime is a closed list rather than whatever it
can reach: `machine.PackSurface()` names eight service families, and
`internal/cli`'s `TestNoPackReachesPastTheDeclaredDriverSurface` holds the packs'
own sources against it, naming the pack, the gesture and the line. The strongest
half is not that test, and it took two steps. #511 closed the way to *obtain*
a driver: `emulator.Env` keeps it unexported and hands back a finished
`Binding`, so `p.binding().Driver.EnsureNetwork(…)` stopped compiling. #514
closed the way to *name* one, because until it `var _ machine.Driver` in a pack
still compiled — measured on `154c204`, `go build ./internal/providers/scaleway/`
exited 0 — which left the surface held by a convention plus a scan. The driver
interface and its five pack-facing halves are unexported now, and what leaves
the package is `machine.Runtime`, a struct rather than a narrowed interface
because a type assertion needs no name. `internal/cli`'s
`TestThePacksCannotNameTheDriver` compiles the forbidden sentence and requires
the failure. A gesture the list lacks is added to it; the pack never works
around it.

## A request, end to end

1. `http.ServeMux` matches the pattern a pack registered, using Go 1.22 routing
   (specific wins, so a catch-all never shadows a real route).
2. The observer wrapper counts the call and, when contracts are loaded, checks
   the answer against the provider's own API description. That is what
   `/_feint/conformance` reports.
3. The handler in the pack reads and writes `resource.Resource` through the
   store: a neutral record of `ID`, `Kind`, `Tenant`, `State`, `Attrs`, plus
   `Runtime` for emulator-side bookkeeping that must survive a restart and must
   never reach a client.
4. If a machine runtime is configured, the pack asks `internal/core/machine` for
   the side effects — a container, an address, a firewall rule. With `--vm off`
   the same code path returns without one, and the control plane still answers.
5. The pack renders its own response shape. This is the only step where the
   provider's dialect appears.

## Why the drift machinery is part of the architecture

Emulators rot because the API they emulate moves and nobody notices. Scaleway
added 363 operations and removed 25 in the twelve months to 2026-07-28,
measured by running the surface scan on two dated checkouts of their SDK
(`de749e3` → `06ce682`) and diffing the operation lists. Three mechanisms make
that movement visible, and they outrank any other design consideration:

- **The surface scan** (`internal/drift`) reads the provider's official Go SDK,
  which is generated from their IDL and therefore exact. No network call, no
  assumption.
- **The baseline** (`coverage/*-baseline.json`) is versioned. An operation that
  appears upstream and that nobody triaged fails CI.
- **The conformance suite** replays the real clients against the emulator.

This is why every route declares the upstream operation it stands for
(`Route.Operation`, spelled exactly as the SDK does), and why a pack lists what
it knowingly does not serve in `Declined()`. Without the first, coverage lies;
without the second, "not done yet" and "out of scope" become the same thing.

A change that makes any of the three inoperative is a bad change, even if it
simplifies the code.

### A decline has to be legible on the wire, not only in `coverage/`

`Declined()` and the coverage artefacts are honest about what a pack does not
serve, and they are read by nobody at run time. A **client** meets the refusal
instead, and it must be able to tell "feint does not serve this operation" from
"the cloud has nothing for you" — otherwise a decline reads as an empty answer
and a green run proves something it did not (#477: three refusals of
`GET /v2/reverse-dns/elastic-ip/{id}` inside a stack that applied seven
resources, planned empty and destroyed seven, with nothing anywhere saying so).

Each pack answers in its own dialect, and each spelling was decided by what its
clients do rather than by a house style:

| pack | status | body | why that one |
|---|---|---|---|
| Scaleway | `501` | `{"type":"not_emulated", "message": …}` | a type their SDK does not map, so `errors.As(&ResourceNotFoundError{})` cannot agree that a resource is missing; their SDK has no retry policy, so the status costs nothing |
| Outscale | `404` | `{"Errors":[{"Code":"", "Type":"OperationNotEmulated", …}]}` | their envelope has a `Type` to put the marker in, and `501` costs `oapi-cli` 12 seconds of backed-off retries — measured |
| Exoscale | `404` | `{"message": …}` | their envelope has nowhere to put a marker, and `501` **fails `exo compute instance create`** — measured |

**No status and no body carries the marker for every operation, and that is a
measurement rather than a preference.** Exoscale is the case that proves it:
`exo compute instance create` calls `GET /v2/reverse-dns/instance/{id}` after
every create and treats anything but a `404` as fatal, so the status a program
could branch on is the status that breaks the client. Their error envelope
requires `message` and declares no code field, so a marker in the body would be
an invented format (rule 4). Both doors are shut, and `404` on a declined
operation is then the same bytes as "this elastic IP has no reverse record".

So the marker is **out of band**, set once in the shared layer for every pack:

```text
X-Feint-Not-Emulated: exoscale
```

`emulator.handleUnrouted` sets it on every unrouted refusal, with the name of
the pack that owns the URL space — read from this process's mount table, never
from the path the client sent. It sits beside `X-Feint-Fault` and
`X-Feint-Probe`, headers no real cloud sends, and no client can trip over it.

Two things it deliberately does not do. It does not mark a `404` that is an
ordinary missing object — only a refusal — or the marker would mean nothing.
And it does not tell an **operator**: a header is invisible to Terraform and to
a human reading an apply, so a run can still be green while three declined
operations were called (#477's remaining half).

Those three are the first links of a longer chain — the contract, the two
witnesses that drive it, the recordings that look in the omission direction, the
seven-axis evidence record, and the versioned surface a pipeline reads.
[The conformance system](conformance.md) describes the whole of it, what each
link proves, and which links are stated but not yet enforced.

## `shapes/` and `corpus/`: one derives nothing from the other

Two committed artefacts hold what a real cloud answered, and the difference
between them is a decision rather than an accident.

`shapes/*.json` holds **field paths and JSON types, no values**. That is what
makes it committable: a shape describes an API, not an account. `corpus/*.jsonl`
holds **whole exchanges, sanitised**: request, response body, status, so a
replay can reissue the call and compare the answer.

The second is strictly richer, which invites the obvious question: should
`shapes/` simply be generated from `corpus/`? **No, and the measurement is why.**

- **13 operations are recorded in `shapes/` and in no corpus.** Six of them are
  served by no pack at all: `osc/Client.ReadQuotas`, `ReadAccounts`,
  `ReadListenerRules`, `ReadNetAccessPoints`, `ReadServerCertificates`,
  `ReadVolumeUpdateTasks`. That is the "learning side" of `upstream.Reads` doing
  its job, a shape known before the handler is written. A corpus cannot hold
  them, because a corpus is a recording of a client that had a reason to call.
  Deriving would delete them.
- **The two artefacts pass different admissibility bars.** A shape is
  committable because it carries no value; a corpus is committable only after a
  sanitiser rewrites every value, and that sanitiser has had defects of its own
  (#390, #395). Deriving would put the shape axis behind the sanitiser's
  correctness, for no gain.
- **A sanitised value has lost its type.** The recorder writes a string over
  whatever it replaced, so `osc/Client.ReadKeypairs.Keypairs` reaches the corpus
  as `"REDACTED-20"` where the API answers an array, and
  `instance-types[].authorized` as a string where it is a bool. A shape is
  nothing but types, so folding those in would publish a polymorphism no
  provider has. `shape.IsRedacted` refuses them at the boundary.

So the relationship is a **one-way fold**, not a derivation:
`mise run shapes:fold` folds every committed corpus into `shapes/`, offline and
without an account, and `shapes/` remains the artefact the shape axis and the
field gate read. It closed 80 of the 292 recordings the axis was asking for.

One consequence worth knowing before reading a catalogue: an operation can
appear under two keys, `"GET /instance/v1/zones/fr-par-1/servers"` from a direct
`feint shapes --record` read and `"instance/v1/API.ListServers"` from a corpus
that could name it — 25 operations are in that state. The readers cope
(`observedFieldsByOperation` resolves both onto the mounted operation and unions
them), but the offline `feint shapes --check` walks the read list, so it reads
only the first spelling. That gate compares by *reissuing* the call against an
empty store, which is why it can only ever walk reads: it is a narrower
population than the shape axis by construction, and the two must not be read as
one number.

## Adding a provider

**Adding a provider requires no behavioural change to `internal/core`; the
external registration and integration points may receive additive data.**

A pack declares its name, its routes with their upstream operations, what it
declines, and the environment a real client of that cloud needs (`Pack.Env`,
which is why `feint env` names no provider). If something seems to require
changing how the core *behaves*, the boundary is in the wrong place — that is the
signal, not the exception.

The rule used to read "nothing outside `internal/providers/<name>/` should need
to change", and that was refuted by this repository's own audit:
[fourth-pack.md](fourth-pack.md) measured eleven shared files a fourth pack
edits — a constructor in `packsFor`, a row in the doctor's client table, a task
in `mise.toml`. Every one of them is additive, none can regress the three
existing packs, and none of them made the absolute sentence true. Two documents
disagreeing is worse than one narrower claim, and the narrower claim is the one
that survives measurement.

It also has what the absolute one could not have: a test.
`TestTheCoreNamesNoProvider` reads every non-test file under `internal/core` and
fails on a provider name in an identifier or a string literal — comments are
exempt, because citing a measured example is how this repository documents. The
defect it reproduces is real: the event watcher's filter once listed `"scw-"`,
`"osc-"` and `"exo-"` in the core, so a fourth pack would have had to edit
`internal/core` for its own events to be reported. A pack's differences reach the
core as field values (`Binding.Prefix`, `Boot.User`, `AddressKey`), never as a
name the core knows.

[CONTRIBUTING.md](../CONTRIBUTING.md) walks through it, and
`internal/providers/scaleway/` is the reference implementation.

## No dependencies

`go.mod` is three lines and a pre-commit hook keeps it that way. Routing,
JSON and Go source parsing all come from the standard library. This is not
minimalism for its own sake: an emulator that runs in everyone's CI is a
supply-chain surface, and the cheapest way to secure a dependency is not to have
it. A new one has to be justified in the pull request that adds it.
