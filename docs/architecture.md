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
internal/core/emulator/     Env, Pack, Route, the single-port mount, /_feint/*
internal/core/machine/      the machine runtime behind --vm: Incus containers, VMs, OVN
internal/core/network/      address plans, subnets, firewall bindings
internal/core/cloudinit/    the boot payload a provider hands its machines
internal/core/serialise/    keyed mutual exclusion: one lock per named target
internal/core/sshkey/       the OpenSSH one-line public key format, read and refused
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
