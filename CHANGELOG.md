# Changelog

**Read this in another language:** [Français](./CHANGELOG.fr.md)

Notable changes, in the format of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
versioned according to [Semantic Versioning](https://semver.org/).

This file is read by the release workflow: the section matching a tag becomes the
body of its GitHub Release. An entry that is not here is an entry nobody
downloading a binary will ever see.

Two kinds of change deserve their own line whatever their size, because they are
what this project is judged on: **a response shape a client can observe**, and
**a limit that moved**. A refactor that changes neither belongs in `git log`.

## [Unreleased]

### Added

- **`feint replay` reissues a recording here and says what diverged** (#73).
  `feint replay run.jsonl --endpoint http://127.0.0.1:4599` takes every recorded
  request, sends it to a running emulator, and reports operation by operation.
  Three verdicts, never summed: **matched**, **divergent**, and **not served** —
  the last being #74's work queue rather than a failure, so the day it fails a
  build is not the day somebody stops recording. Exit 2 on a divergence, 1 only
  when the tool itself failed.

  **What is compared, and what is deliberately not.** A byte diff would be
  noise, so the comparison is graded: the status exactly, the fields present
  exactly minus what a pack's `DeclinedFields()` excuses, the types exactly, and
  values and ordering *only* where a pack declares them comparable
  (`emulator.Invariant`, new). Both of the last two are defects this repository
  has already paid for: #270 measured two `vpc/v2` creates answering 201 where
  the cloud answers 200, which the status line catches without a Scaleway
  account; #320 measured `Server.public_ips` coming back in store order rather
  than in the order the create named, which *only* the ordering line catches.
  The Scaleway pack declares that order for `CreateServer`, `GetServer` and
  `UpdateServer`, plus the two values a create's client always names.

  **Identifiers are rebound, not compared.** A recorded run addresses the
  objects the cloud minted for it, and this emulator mints its own. So the
  replay learns, from each answer, which recorded identifier this emulator
  answered in its place, and substitutes it into every later request — whole
  path segments, whole query values, whole body strings, and only for values
  shaped like something a cloud hands out (a UUID, an address, an Outscale
  `i-<hex>`). Without it, the identity case #73 puts first is unreachable: a
  transcript recorded against the emulator replays against a **fresh** one with
  zero divergences, and every read would otherwise answer 404.

  **Nothing from the recording reaches the output.** A transcript is redacted of
  credentials and is not anonymous — `docs/proxy.md` states, field by field,
  that the bodies hold an account's inventory. A finding therefore names a path,
  a type, a status and a *position*: an out-of-order list is reported as "0,1
  answered as 1,0", never by naming the identifiers that moved, and the request
  path is anonymised before it is printed.

- **`feint coverage --observed` ranks what the packs decline by what a client
  actually called** (#74). Every refusal in this repository carries a reason,
  which is the discipline; none carries a *demand*, which was the gap. Given a
  recording, or a directory of them, the view lists the declined operations a
  real client called anyway, most-called first, each with its own argument
  beside it and the client family that made the calls.

  Two facts are counted apart and never summed: **nobody called it** and
  **nobody triaged it**. Confusing them is the defect the view exists to
  correct, so the report states both populations in words that cannot be read as
  each other, and an operation nobody called is a count rather than a row — a
  ranking that carries every refusal is the alphabet again.

  It needs `--contract`, and that is what makes it possible at all: `feint
  proxy` names an exchange from the *mounted routes*, so a call to a declined
  operation carries no name, and only the provider's own document can say that
  `GET /v2/dns-domain` is `list-dns-domains`. `feint coverage` without
  `--observed` renders exactly what it rendered before — the observed view
  replaces the report rather than joining it, so `--format json` keeps producing
  the committed artefact byte for byte and `tools/drift/gate.sh` is untouched.

  Measured on a recording of `scw`, `exo`, `oapi-cli` and `terraform` driving
  this emulator through the proxy: one Exoscale decline (`list-dns-domains`, 7
  calls from `exo`) and two Outscale ones (`ReadApiAccessRules` and
  `ReadCatalog`, from `oapi-cli`).

- **A pack can declare what a replay may compare beyond presence and type**
  (`emulator.Invariant`, `ReplayInvariants()`). Optional in the manner of
  `FieldDecliner`, with a reason held to the same guard `Declined()` faces, plus
  one of its own: a kind nothing implements is refused rather than reading as
  "compared". A declaration naming an operation no route serves fails a test.
  The report counts value checks and order checks separately, so a declaration
  that evaluated nothing cannot read as one that held.

### Changed

- **CLI surface version 7.** Both entries above are additions — the verb
  `replay` with `--endpoint`, `--format` and `--timeout`, and `coverage
  --observed`. Nothing was removed and no exit code moved, so a pipeline keyed
  on version 6 keeps working.

- `internal/shape.IsUUID` is exported, so the replay asks the same question of a
  recorded value that the shape catalogue asks of a path segment. Two spellings
  of "is this an identifier" would answer differently the day one of them
  learned a case.

- **An Outscale load balancer's listeners can move after the create**
  (#344): `osc/Client.CreateLoadBalancerListeners` and
  `osc/Client.DeleteLoadBalancerListeners` are served, and the runtime balancer
  follows them.

  **The gap was never the first apply.** `CreateLoadBalancer` carries its
  listeners inline, which is why all three surveyed Outscale stacks that build
  an LBU already converged (#281). It was the *second* apply — editing a
  `listeners` block on a load balancer that already stands — and all three
  provider versions read here call the pair from their Update path and from
  nowhere else (v1.1.3 `resource_outscale_load_balancer.go:671,695`, v1.7.0
  `:732,745`, v1.8.0 `resource_load_balancer.go:990,1001`). Measured on
  2026-08-21 with provider 1.8.0 before the change: moving one listener's front
  port answered `Error: Unable to update Load Balancer listeners` carrying
  `feint does not serve DeleteLoadBalancerListeners`, and every plan afterwards
  stayed at `0 to add, 1 to change, 0 to destroy` for ever. After it, on
  providers **1.8.0 and 1.1.3 alike**: apply, empty plan, port moved, **second
  plan empty**, clean destroy — and `ReadLoadBalancers` holds exactly `[8080]`,
  the old port gone rather than kept beside the new one.

  **The dataplane follows the control plane, and that is measured too.** Under
  `--vm incus-ovn` the balancer really distributes packets (#315), so a listener
  the API moved while the runtime kept the old port would be a new lie rather
  than a new feature. `tools/conformance/outscale/balancer.sh` now moves the
  listener and asserts both ends: 8080 answers, served by the registered
  machine, and 80 stops answering. Run on 2026-08-21 against a real OVN
  runtime — 6/6 at t0, 6/6 at t+60s, the unlink respected, the move followed,
  the host left holding no balancer after the delete.

  The fix that makes it true is one branch in `syncBalancer`: a balancer that
  has lost every listener is *withdrawn* from the runtime instead of left alone.
  That is not a corner case — it is the middle of every single-listener port
  change, because the provider deletes the departing port before creating the
  arriving one. Neutralise the branch in a copy outside the repository and the
  OVN suite fails on `the balancer does not answer on its new port 8080`, which
  is the falsification, alongside five mutations in
  `tools/falsify/specs/listener-day-two.json` that all bite.

### Changed

- **The declined half of the Outscale LBU family is triaged four ways instead of
  one** (#344), because the reasons are not interchangeable and one shared
  sentence said "no surveyed stack calls these" about all of them. Now:
  listener rules and stickiness policies are **demand** — nobody has asked; a
  load balancer's tags after its create are **the named next wall**, measured
  on 2026-08-21 (provider 1.8.0 answers `Error: Unable to update Load Balancer`
  on `DeleteLoadBalancerTags`) and left out because #344 served the path that
  carries traffic while a tag reaches no runtime; `ReadVmsHealth` is
  **honesty**, and now says the sharper thing — not merely that `--vm off`
  probes nothing, but that `incus network load-balancer` reports no per-backend
  health *even under OVN where connections really are distributed*, so any
  verdict would be invented; and the server certificates are **nothing here
  terminates TLS**.

- **`osc/Client.DeregisterVmsInLoadBalancer` is declined on reachability rather
  than on demand** (#344), which is a stronger refusal and one this repository
  had not measured. Provider 1.1.3 is the only version whose code contains the
  call, on the update path of the load balancer's own `backend_vm_ids`, and that
  path cannot execute: the attribute is declared `schema.TypeList`
  (`resource_outscale_load_balancer.go:150`) while the update casts it to
  `*schema.Set` (`:726`). Measured against this emulator on 2026-08-21 — the
  plugin panics with `interface conversion: interface {} is []interface {}, not
  *schema.Set` before a request is built. Providers 1.7.0 and 1.8.0 removed the
  call outright. Detaching a backend goes through
  `UnlinkLoadBalancerBackendMachines`, which is served, so serving this one
  would be serving an operation no client can reach.

- **Two listeners can no longer share one front port**, on
  `CreateLoadBalancer` and `CreateLoadBalancerListeners` alike (#344). The
  refusal is load-bearing rather than tidy: two listeners on one port are two
  runtime listeners on one port, which the balancer cannot build, so storing
  them would leave the API describing a balancer the runtime had refused. Its
  wording deliberately avoids the token the real service uses — provider 1.1.3
  retries for five minutes on any error containing `DuplicateListener`, and here
  the condition is never transient, so echoing it would turn an accurate refusal
  into a five-minute hang.

## [0.10.0] - 2026-08-20

### Added

- **`feint proxy --forward` records a client whose endpoint is compiled in**
  (#336). The proxy accepts `CONNECT host:port`, terminates the TLS with a
  certificate minted for the run, records the exchange and re-originates to the
  host the client asked for. Nothing changes in the client: a Go client that
  installs no `Transport` honours `HTTPS_PROXY` on its own, and `SSL_CERT_FILE`
  is what makes it trust the tunnel. That is the case `--upstream` could never
  reach — Pépin's collectors hold their base URLs in their source, and making
  them configurable was refused on its own delivery audit, because every
  collection request carries a live secret key. Measured end to end on
  2026-08-20 against a local HTTPS server, with a client whose endpoint is a
  constant: one exchange recorded, named `instance/v1/API.CreateServer`, with
  `X-Auth-Token`, `X-Consumer`, the query signature and the answer's
  `X-Session-Token` all `REDACTED` while the server received every one of them
  in full.

  **The security requirements are the feature, and each has a falsification**
  (`tools/falsify/specs/forward-proxy.json`, seven mutations, all of which bite).
  The redaction survives the interception, because the tunnel records through the
  same `capture` — there is no second path to the writer. Loopback only, and
  `--expose-to-network` is *refused* with `--forward`: a proxy holding an
  authority a client trusts is, off loopback, a machine that decrypts and files
  whatever anyone who can reach it sends. The authority is minted in memory,
  written to one temporary file, removed at exit, never installed. And only the
  hosts named are intercepted — a `CONNECT` elsewhere is refused with a 403,
  counted and reported at exit, never relayed blind, and `--forward '*'` is
  refused outright.

  The transcript gained a `host` field, filled by the proxy and left empty by the
  emulator's own ring: one forward-proxy recording holds several clouds, and
  `POST /api/v1/ReadVms` is a different exchange depending on which one answered.
  CLI surface version 5. `docs/proxy.md` now states what a recording contains,
  field by field, and what has to be sanitised before it is shared — completely,
  because partial sanitisation is the trap that opened Pépin's audit.

- **An Outscale load balancer distributes real packets, inside its own
  network** (#315). Under `--vm incus-ovn`, a balancer's `PrivateIp` — an
  address of the Subnet it sits in — is handed to the runtime's own OVN load
  balancer: connections from inside that network are spread over the registered
  Vms, an unlinked Vm stops receiving them, and deleting the balancer takes it
  off the host. Measured on 2026-08-20 with two backends and one client on one
  network: 6/6 answered at t0, 6/6 a minute later, over both machines each
  time, and 6/6 to the survivor after an unlink.
  `tools/conformance/outscale/balancer.sh` replays it.

  The claim is declared and verified, never deduced from a mode name:
  `/_feint/health` gained `capabilities.balancing` (health schema version 4),
  the OVN mode alone sets it, startup verification clears it on a host with no
  OVN wiring, and a build that does not know the key answers nothing — which
  reads as absent. Gate on it.

  **What did not move, and why.** The public address of an internet-facing
  balancer still routes nowhere: a VIP outside the network answered 6/6 at t0,
  6/6 at t+60s and 0/6 from t+180s onwards, permanently, because the runtime
  announces such an address once at creation and never again. The driver now
  **refuses** a listen address outside the network's own block rather than
  configuring one that would go dark minutes after the test that proved it.
  `ReadVmsHealth` also stays declined: `incus network load-balancer info`
  answers "No load-balancer health information available", so nothing probes a
  backend even here. The Scaleway `lb/v1` family is untouched — the mechanism is
  shared, the wiring is per pack, and this pack asks the runtime for nothing.
  `docs/limits.md` carries the figures beside each refusal.

- **A real-cloud recording arbitrates a Private Network's shape** (#270).
  `feint shapes --record --provider scaleway`, run on 2026-08-20 against a real
  fr-par account holding one Private Network, learned 76 field paths, 62 of
  them the `PrivateNetwork`, `Subnet` and `VPC` objects — none of which had ever
  been observed populated, because the previous recording was taken on an
  account holding neither, so both element shapes were empty. The other 14 are a
  block snapshot's, observed for the same reason: the account had one.

  What it settles, and what 0.9.0 said in a callout it could not: creation
  allocates an IPv6 `/64` without being asked, the range is unique-local
  (`fdb2:1bb5:120a:9b::/64` on that account), and two networks of one project
  share their `/48` and differ only in the subnet ID — RFC 4193's own layout,
  which this emulator now follows instead of drawing an independent `/48` per
  network. The `Subnet` a real read carries is exactly the eight fields already
  served.

  The read list can now describe an operation that takes an identifier: an entry
  ending in `{id}` is filled in from the collection above it, in the same run,
  and the catalogue stores the templated path. `GET
  /vpc/v2/regions/fr-par/private-networks/{id}` — the read the Terraform
  provider refreshes with, and the one nothing here could arbitrate — is the
  first of them.

- **Scaleway load balancers, scoped by what the surveyed stacks call** (#282).
  `lb/v1` serves 35 operations on its zoned door: `ZonedAPI.CreateLB`,
  `ZonedAPI.GetLB`, `ZonedAPI.ListLBs`, `ZonedAPI.UpdateLB`,
  `ZonedAPI.DeleteLB`, `ZonedAPI.CreateIP`, `ZonedAPI.GetIP`,
  `ZonedAPI.ListIPs`, `ZonedAPI.UpdateIP`, `ZonedAPI.ReleaseIP`,
  `ZonedAPI.CreateBackend`, `ZonedAPI.GetBackend`, `ZonedAPI.ListBackends`,
  `ZonedAPI.UpdateBackend`, `ZonedAPI.DeleteBackend`,
  `ZonedAPI.SetBackendServers`, `ZonedAPI.UpdateHealthCheck`,
  `ZonedAPI.CreateFrontend`, `ZonedAPI.GetFrontend`, `ZonedAPI.ListFrontends`,
  `ZonedAPI.UpdateFrontend`, `ZonedAPI.DeleteFrontend`, `ZonedAPI.CreateACL`,
  `ZonedAPI.GetACL`, `ZonedAPI.ListACLs`, `ZonedAPI.UpdateACL`,
  `ZonedAPI.DeleteACL`, `ZonedAPI.CreateRoute`, `ZonedAPI.GetRoute`,
  `ZonedAPI.ListRoutes`, `ZonedAPI.UpdateRoute`, `ZonedAPI.DeleteRoute`,
  `ZonedAPI.AttachPrivateNetwork`, `ZonedAPI.DetachPrivateNetwork` and
  `ZonedAPI.ListLBPrivateNetworks` — the Private Network attachment in both
  spellings, because the 2.43-vendored SDK attaches at the path its own source
  no longer reads. The other 19 are declined by name (stats over health nothing
  probes, certificates over TLS nothing terminates, subscribers with no event
  to deliver), and the deprecated regional door wholesale. Nothing forwards a
  packet and nothing claims to: `docs/limits.md` states what a 200 means here.

- **Scaleway public gateways** (#282). `vpcgw/v2` serves 15 operations:
  `API.CreateGateway`, `API.GetGateway`, `API.ListGateways`,
  `API.UpdateGateway`, `API.DeleteGateway`, `API.CreateGatewayNetwork`,
  `API.GetGatewayNetwork`, `API.ListGatewayNetworks`,
  `API.UpdateGatewayNetwork`, `API.DeleteGatewayNetwork`, `API.CreateIP`,
  `API.GetIP`, `API.ListIPs`, `API.UpdateIP` and `API.DeleteIP`. `vpcgw/v1` is
  declined wholesale, and not because v2 supersedes it: the portal publishes no
  v1 document any more, and every mounted route is checked against that
  document. A provider pinned below 2.52 meets a named 501 rather than a
  silence.

- **Scaleway placement groups, on both doors** (#285). A refusal withdrawn:
  the family was declined with "any policy would be reported satisfied whatever
  it asked", and measuring what the provider does with the answer turned that
  sentence into an obligation rather than a refusal — 2.43.0 and 2.81.0 both
  store `policy_respected` as a computed attribute they never gate on.
  `instance/v1` now serves `API.CreatePlacementGroup`, `API.GetPlacementGroup`,
  `API.ListPlacementGroups`, `API.UpdatePlacementGroup`, `API.SetPlacementGroup`,
  `API.DeletePlacementGroup`, `API.GetPlacementGroupServers`,
  `API.SetPlacementGroupServers` and `API.UpdatePlacementGroupServers`;
  `instance/v2alpha1` serves the five the 2.81.0 provider moved the resource's
  CRUD onto (`API.CreatePlacementGroup`, `API.GetPlacementGroup`,
  `API.ListPlacementGroups`, `API.UpdatePlacementGroup`,
  `API.DeletePlacementGroup`). Placement is recorded, never enforced, and
  `policy_respected` tells the single-host truth rather than the flattering one.

- **Outscale load balancers** (#281). A refusal withdrawn, scoped to what three
  surveyed stacks actually call, measured with `feint proxy --record` rather
  than read off the SDK's 23-operation surface:
  `osc/Client.CreateLoadBalancer`, `osc/Client.UpdateLoadBalancer`,
  `osc/Client.DeleteLoadBalancer`, `osc/Client.RegisterVmsInLoadBalancer`,
  `osc/Client.LinkLoadBalancerBackendMachines` and
  `osc/Client.UnlinkLoadBalancerBackendMachines` — both attach spellings,
  because the measurement overturned the reading of the 1.1.3 source. The rest
  of the family stays declined by name; the first stack that calls one reopens
  it.

- **A release now has to say what it started and stopped serving** (#326).
  `mise run release:surface` diffs the committed `coverage/*-coverage.json` of
  the latest tag against this tree's and refuses (exit 2) when an operation
  that changed hands is named in neither `CHANGELOG.md` — which *is* the
  release body — nor `tools/release/unnamed.json`, where "not worth naming" is
  signed with a reason. Three transitions must be named: newly served,
  withdrawn, and **a refusal withdrawn**, which is the one that costs silently.
  0.9.0 mounted `instance/v2alpha1` private network interfaces and said so
  nowhere; a downstream consumer spent a day probing two binaries side by side
  to find a 501 had become a 200, and separately kept working around three
  refusals that had been features for weeks. Run on this train, the gate named
  70 operations no note carried — the four entries above.

- **A release declares the client versions that proved it** (#325).
  `docs/clients.md` is generated from the conformance workflow's pins and from
  every `required_providers` block under `tools/conformance/` and
  `examples/stacks/`, published in the release body, and checked by
  `feint docs --check` like every other generated page. A constraint that
  exists nowhere reads *not pinned* rather than being invented: two stacks
  resolve their provider fresh on every run, and the artefact says so. The
  consumer this comes from resolves providers fresh in CI, so a Scaleway
  release reached them the morning after it shipped whether or not this
  emulator had caught up.

- **A measurement can now tell who answered it** (#309). `GET /_feint/health`
  gains `instance` — the pid and start time of the process answering — and its
  `schema_version` moves to 3 (additive; every field of version 2 is unchanged).
  The field exists because its absence was measured: on 2026-08-19 a stale
  emulator on a shared port answered a probe with the previous build's
  catalogue, and nothing in the answer could say so.

- **`brew install stephrobert/feint/feint`, with the digests derived from the
  release rather than typed** (#321). The release already published signed
  macOS binaries and a macOS reader still had to find the release page, pick an
  architecture and verify a checksum by hand. The decision inside the issue was
  *who writes the formula on release*, and it is answered by neither of the two
  obvious halves: `mise run release:formula` fetches the release's own
  cosign-signed `checksums.txt` and **derives** the whole formula from it, so
  filling the tap costs a copy and never a transcription; `mise run release:tap`
  derives it again and exits 2 while the tap serves anything else, daily
  (`.github/workflows/tap.yml`). A push from `release.yml` was refused for the
  reason this repository has already written down twice — it would need a
  cross-repository token that does not exist, and *a gate that repairs the
  repository is a second way in*. The formula installs the published bytes and
  never rebuilds them, so what Homebrew verifies is what the release signed.
  The refusals are in `internal/release/formula.go`, falsified by
  `tools/falsify/specs/homebrew-formula.json`: a checksums list is fetched over
  the network, so an entry the formula has no platform for stops it rather than
  being dropped, a digest that is not a SHA-256 never reaches the file, and no
  name from that list becomes a URL or a Ruby literal unchecked. Proved with
  the real client rather than by rendering: on 2026-08-20 against Homebrew
  5.1.15, the derived formula in a tap installed the published v0.9.0 binary,
  `feint version` answered `v0.9.0`, `brew test` passed, `brew audit` reported
  nothing, and one flipped byte in a digest made the install fail with *Formula
  reports different checksum*. **The tap does not exist yet**: `mise run
  release:tap` exits 2 and names the one command that fills it.

- **The contract a third party's stack is asked to meet** (#327). A downstream
  consumer offered the lane that found the Scaleway 2.81.0 break as a sixteenth
  surveyed stack and asked what contract we wanted it to meet. It is written in
  `examples/stacks/README.md`, with the decision it forced: such a stack is
  **recorded and replayed on demand, never wired into this repository's CI**.
  A third party's repository changes without our decision, so a required gate
  over it can go red for a reason nobody here chose — and a red nobody can act
  on is what teaches people to skip a gate. `examples/stacks/surveyed.md`
  records the offer with its reported figures attributed as theirs and every
  cell we cannot fill named as unmeasured.

### Fixed

- **Attaching a private NIC no longer queues every machine behind the slowest
  one** (#348). Six attachments on the Scaleway example stack under
  `--vm incus-ovn` took 26s, 31s, 42s, 52s, 63s and 73s — a flat ten seconds
  each, which is one machine's work paid again by every machine behind it. Past
  the client's minute the apply gives up and retries, and the retry meets the
  interface its own first attempt created: *the server is already attached to
  this private network*. The driver held one package-level mutex across every
  call it makes into the machine; it now takes the repository's own per-target
  lock, keyed by machine, which is what the name collision it guards against
  has always been scoped to. Measured on `main` without this branch: the same
  slope, so the queue predates it and was merely unreachable behind #341.
- **A full `incus-ovn` conformance pass no longer races the daemon into
  deleting a firewall chain twice** (#341). The failure — `Failed deleting
  nftables chain "fwd.feint-uplink": No such file or directory`, killing the
  outscale-tofu suite — reproduces from a **clean station**, so the "state
  accumulated across runs" reading was only half right. Measured with `incus
  monitor` through a whole pass: `feint clean` at the end of the oapi-cli suite
  deletes the uplink, then OpenTofu's default parallelism recreates two subnets
  and a default machine network at once. A network `PUT` on the uplink and an
  OVN network `POST` attached to it both make the daemon rebuild the uplink's
  nftables firewall, and Incus' `removeChains` is a snapshot-then-delete with
  no lock shared between those two paths — so the loser's chain is deleted by
  the concurrent operation *between its own snapshot and its delete*. `uplinkMu`
  now serialises every operation that makes the daemon rebuild the uplink, the
  `network create` included.
- **A deleted OVN network takes its delegated block off the uplink** (#341).
  `RemoveNetwork` never withdrew the route, so one pass accumulated nine of
  them — the seven the issue reported were not the residue of seven runs. An
  uplink left behind by a dead run is also adopted once per process, dropping
  the routes of networks that no longer exist, and an uplink held by a **live**
  emulator is refused rather than shared: sharing it across processes is the
  same unlocked corruption by another name.
- **`feint doctor` asks whether a DHCP service outlives its network, not its
  interface** (#342). It answered `ok` while an orphan held `10.50.2.1` and
  broke the next run's conformance, because it looked for a service whose
  interface was gone — and there the interface had survived *alongside* its
  service. Both had outlived the network, which is the question nobody asked.
  A leftover is now a red line naming the block and the pid, `feint clean` kills
  the service and **says what it will not touch** — an unlabelled bridge is not
  demonstrably ours — and every green line of `doctor` was re-read against what
  it actually measured.
- **An ssh conformance suite refuses to start when the emulator holds none of
  the images it boots, and the runtime proof builds them** (#335).
  `runtime-proof.yml` failed on its *Scaleway ssh suite* step on five
  consecutive scheduled nights, 2026-08-16 to 2026-08-20, on both legs. The fix
  was printed in its own log every one of those nights — `WARN no image of ours
  for this system, booting the upstream one … fix="feint images"` — and nothing
  ran it: the subcommand appears in no step of that workflow and in no line of
  `mise run conformance:ssh`.

  Reproduced before being fixed, on a station that held the images, by deleting
  the five `feint/*` aliases and putting them back afterwards: the same suite
  passed in 21 seconds with them and failed in 93 without, on the same sentence
  CI had been printing. The workflow now runs `feint images` before it starts
  the emulator, and `tools/conformance/guard.sh` gained `guard_images`, which
  the three ssh suites call before they register a key: it reads `.machines`
  from `/_feint/health`, asks `feint images --check` about that runtime, and
  refuses in a twentieth of a second naming the command, instead of failing
  ninety seconds later on "no ssh daemon answered", which blames the address.
  The guard is in the shared file, not in each suite, for the reason CLAUDE.md
  gives: a control copied three times is one the fourth forgets. Falsified by
  `tools/falsify/specs/ssh-suite-needs-its-images.json`, five mutations,
  including the one where a suite keeps the guard and stops calling it.

  **Why the fallback stopped being a fallback**, which is the part worth
  keeping. #203 chose to boot the upstream image when the emulator holds none of
  its own, deliberately, so a first contact still works. #202 then gave a
  machine exactly the one address its provider publishes, on a routed NIC with
  no NAT. The two are fine apart and not together: measured on 2026-08-20, an
  upstream image's cloud-init dies on `Temporary failure resolving
  'archive.ubuntu.com'`, `openssh-server` never installs, and nothing ever
  listens on port 22. The emulator's warning said "the machine installs an ssh
  daemon at first boot and needs outbound network to do it", which was true when
  it was written and had quietly become a description of something that cannot
  happen. It now says what does happen.

  This also unblocks the counter of #125: its promotion criterion is a run of
  consecutive green scheduled nights, so while this stayed broken that number
  was pinned at zero by construction and nothing said so.

- **The frozen CLI surface is read from the flag sets the binary registers, not
  from the help it prints** (#334). `feint proxy --intercept` shipped in v0.9.0:
  the binary accepted it, `feint proxy --help` rendered it, and
  `internal/cli/testdata/frozen/cli.json` did not list it, for six days. That
  fixture is the surface #132 froze so a pipeline outside this repository can
  key on it. The cause was the observation itself: it parsed the rendered `feint
  --help`, so it recorded what the help *claimed*. The missing flag was the
  cheap half. A flag **deleted** from a flag set while its help line survived
  would have kept the same gate green, and that is the direction that breaks a
  consumer.

  The surface now comes from `flag.FlagSet.VisitAll`, through one seam every
  verb builds its flags with (`internal/cli/flagset.go`). The help keeps a
  promise, but as an assertion with a subject of its own:
  `TestTheHelpNamesEveryFlagTheBinaryAccepts` compares the two lists in both
  directions, so a flag the binary accepts and no help block names fails, and a
  help block naming a flag no flag set registers fails too. Falsified in both
  directions by `tools/falsify/specs/frozen-cli-surface.json`, seven mutations,
  each replayed against the test that has to bite.

- **The CLI surface is version 5, and 24 flags the binary always accepted are
  visible in it for the first time** (#334). What moved is the observation, not
  the binary: `--intercept` and `--expose-to-network` on `proxy`, `--shapes` and
  `--expose-to-network` on `serve`, `--check` on `version`, the six `serve`
  flags `start` really takes, three on `evidence` and ten on `docs`. Three
  entries left, and all three were the parser's: `--version` and `-v` under
  `version`, which are aliases of the verb rather than flags of it (a reader who
  typed `feint version --version` was answered `flag provided but not defined`),
  and `--state` under `snapshot`, which came from a sentence about `serve`.
  `snapshot` is now keyed per flag set — `snapshot save`, `snapshot load`,
  `snapshot list` — which is what says `--force` belongs to `save` alone.

  `feint --help` gained every flag it was hiding, including the two
  `--expose-to-network` switches, which are the ones a reader most needs to meet
  before setting them.

- **`docs/proxy.md` stopped refusing a tool this repository ships** (#334).
  The page told the reader that interception "is #76 and deliberately not this
  tool" while `docs/limits.md` sent that same reader there to use it. `feint
  proxy --intercept` has existed since v0.9.0; the page now documents it, with
  what it mints, what it prints, and the one thing it will not do: it installs
  nothing in the system trust store and never edits the operator's
  `/etc/hosts`. #76 and #92 are closed as delivered.

- **An address attached after launch reaches a machine that joins no network,
  and a security group there is a declared limit instead of a silent one**
  (#337). Since #202 a machine with only a public address carries it on a
  `routed` NIC, which has no `network` key — and both address paths of
  `internal/core/machine` selected interfaces by that key. Every address routed
  after the launch died on "machine has no network interface": the Exoscale ssh
  suite's elastic IP was reported attached by the API while nothing put it on
  the machine, and every Scaleway poweron replay logged the same error over a
  working address. The routed NIC is now recognised, and the mechanism is its
  own: the address lands in the device's `ipv4.routes` — measured accepted on
  Incus 7.2, cold and live — and the re-plug a live edit causes (measured: the
  guest interface comes back down and bare) is repaired from the device's own
  config. The Exoscale ssh suite passes end to end.

  The firewall half could not be fixed the same way, because the measurement
  says no: a routed NIC accepts no security option at all — every key an
  `Invalid device option` on Incus 7.2 and 7.3 alike, table in
  `docs/limits.md` — so applying a rule set there was an ERROR log the control
  plane answered over as if the group were enforced. The refusal is now
  declared instead of pretended: `/_feint/health` gained
  `capabilities.firewall_public_only` (health schema version 5), false in every
  Incus mode; `ApplyFirewall` answers the typed
  `machine.ErrFirewallUnenforceable` rather than sending doomed keys, while
  still covering the interfaces that can enforce; and a group that filters
  nothing — the default one riding every `scw instance server create` — binds
  nothing on any runtime, which is the only faithful translation of "filters
  nothing". Falsified by `tools/falsify/specs/routed-nic.json`.

- **A stack applied on every pull request pins the provider that answered, and
  a stack CI does not apply says why** (#325's table, first day). The generated
  client page exposed two things nothing had said before:
  `examples/stacks/outscale/modules/net` was applied on every pull request
  while declaring no provider constraint at all — `terraform init -upgrade`
  resolved it from the whole registry on every run, so the apply proved the
  emulator answered whatever was newest that morning and nothing replayable —
  and `examples/stacks/exoscale` is applied by nothing, which is a good
  decision written only in prose, in three files, in three wordings, checked by
  nothing. The module now carries the same `~> 1.7` floor its root does, and
  `feint docs --check` exits 2 on a stack CI applies without a constraint, on a
  stack CI does not apply that nothing declares, and on a declaration for a
  stack that has disappeared or that CI has started applying. The reason is
  printed on `docs/clients.md` from the same list the refusal reads.
  Falsified by `tools/falsify/specs/stack-proof.json`.

- **A Private Network and a VPC serve the Object Storage flag the real cloud
  carries** (#270). `has_s3_integration` and `s3_integration_enabled` were
  declared by the contract, returned by every real answer, and absent here. Both
  are invisible through `scw`, which drops them on the way to its own output, so
  only a recording could find them — and only one taken while the objects
  existed.

- **The two `vpc/v2` creates answer 200, which is what the wire carried**
  (#270). `CreateVPC` and `CreatePrivateNetwork` answered 201, the status every
  other create in the pack writes; both were measured at 200 on a real account,
  read off a `feint proxy` transcript rather than off a CLI that shows neither.
  No other product was measured, so no other product moved. It changes nothing
  for a client that tests 2xx, and it changes what this emulator is allowed to
  claim.

- **An identifier never reaches a committed shape catalogue** (#270). A
  recording of one resource carries that resource's path, and the path went into
  the operation key and into `Operation.Path` verbatim — so the first read-list
  entry addressing a single object would have committed somebody's account UUID
  to `shapes/*.json`. Paths are now anonymised at the boundary where a recording
  becomes an artefact, whatever wrote it, and a test reads the committed files
  themselves rather than trusting the rule.

- **`feint shapes --check` names what it could not compare** (#270). Eleven
  recorded operations were dropping out of its arithmetic without a word: the
  emulator answers a refusal offline and the comparison was skipped in silence.
  The coverage line now lists them as unchecked, which is the difference between
  "nothing is wrong" and "nothing was looked at".

- **`server.public_ips` answers in the order the create named** (#320).
  Scaleway's `Server.public_ips` is a list and Terraform stores it as one: the
  provider rebuilds `ip_ids` from it index by index, and its apply path is
  set-based `UpdateIP` calls that cannot reorder — so an emulator answering
  the attached addresses in store order made every `ip_ids = [a, b]` whose
  store order was `[b, a]` re-plan the same two-way swap for ever, measured on
  `sergelogvinov/terraform-talos` the moment its servers first applied. Each
  attach now records the position the client gave it, on create
  (`public_ips`/`public_ip`), on `UpdateServer.public_ips` — which was
  declared by the SDK and read by nobody here, a PATCH naming it answered 200
  and changed nothing — and on a bare `PATCH /ips` attach, which joins the end
  of the list. The identifiers themselves were already the API's own bare
  UUIDs; the `id → fr-par-1/id` half of the observed diff is the provider
  normalising its own state, and goes with the swap.

- **`feint start` refuses an answer from a process it did not spawn** (#309).
  Before, when something already held the address, the spawned child died on
  the bind error while `start` took the incumbent's health answer as the
  child's: it printed "listening (pid N)" about a dead pid, `feint wait` said
  ready, and every suite then measured whatever the incumbent served —
  reproduced against a stale build before the fix. `start` now compares
  `instance.pid` against the child it spawned and exits 1 naming the incumbent
  (pid, start time); `feint serve` refuses up front an address where an
  emulator already answers, instead of leaving the fact in a bind error a
  wrapper reads past. A second run after a clean stop is unaffected: the guard
  compares identities, it does not count runs.
- **`FEINT_ADDR` is honoured again** (#309). It was declared as a literal in
  `mise.toml`'s `[env]`, which beats an exported variable:
  `FEINT_ADDR=127.0.0.1:4699 mise run conformance` silently used 4599, so every
  parallel run converged on the port a stale emulator was most likely to hold.
  The declaration now reads the environment first and keeps 4599 only as the
  default.

- **`feint env outscale` now opens the door its own documentation points at**
  (#286). The printed `OSC_ENDPOINT_API` carries `/api/v1` — the shape the
  current Terraform provider line (>= 1.7) reads, measured on 1.8.0, which
  died on a 404 given the bare host the command used to print. The clients
  that append the path themselves get `--client oapi-cli` (alias
  `terraform-1.1`): measured on oapi-cli 0.13.0 and provider 1.1.3, which
  URL-escapes a path'd value into `invalid port ":4599%2Fapi%2Fv1"`
  client-side. Either mispairing now fails in seconds with the remedy named;
  the conformance suite drives provider 1.8.0 to a plan from a shell holding
  nothing but the command's exports. CLI surface v4: one flag added
  (`env --client`), nothing moved or removed — but the flagless
  `feint env outscale` value changed shape, which is the point.

- **The escape a shell can carry is named before the apply, not after**
  (#286; the cheapest instance of #280). With `OSC_PROFILE` set, the Outscale
  Terraform provider 1.1.x reads `~/.osc/config.json` and ignores
  `OSC_ENDPOINT_API` entirely — reproduced on 1.1.3: the plan left for
  `https://api.<region>.outscale.com` while the emulator received nothing.
  `feint env outscale` and `feint doctor` now warn when the shell carries it,
  on stderr, where an `eval` cannot swallow the warning. The legacy credential
  names (`OUTSCALE_ACCESSKEYID`/`OUTSCALE_SECRETKEYID`) are warned about too,
  with exactly what was measured: they do **not** override the endpoint on
  1.1.3 or 1.8.0 — four combinations, all reaching the emulator, refuting the
  survey register's earlier reading — but they are real-cloud credentials one
  lost export away from being signed with.

## [0.9.0]

The contract release. Feint can be consumed directly from a Go test or a CI job
— `feinttest.Start(t)`, `stephrobert/setup-feint@v1`, the OCI image as a service
— and every one of the 285 mounted operations is either driven by a real client
or declares, at the route, why no official client reaches it. The compatibility
model stopped being a promise: frozen schemas, a consumer-facing compatibility
check that runs before the tag, response checks that look for what is *missing*
as well as what is invented, and fifteen third-party Terraform stacks applied
against the emulator. Exoscale gains block storage and instance pools.

> **This section was completed on 2026-08-19, after the tag** (#326, #325).
> Nothing already written below was changed, and neither the tag, the binaries
> nor the image moved: only what this release says about itself did. A
> downstream consumer running 0.9.0 as a credential-less CI gate had to probe
> v0.8.0 and v0.9.0 side by side to learn that this release serves
> `instance/v2alpha1` — the string appears nowhere in the original section, and
> neither does `2.81.0`, the provider version the Scaleway suite was pinned to
> in order to exercise it. The three blocks that follow are that missing half.
> They are derived from `coverage/*-coverage.json` at the two tags rather than
> remembered, which is the only reason they can be trusted at this distance.

### What this release serves that 0.8.0 did not *(recorded 2026-08-19)*

Derived from `coverage/<provider>-coverage.json` as committed at `v0.8.0` and at
`v0.9.0`: an operation counts as newly served when its status moves to
`implemented`, from `declined` or from untriaged. The totals agree with the
generated coverage block in each tag's own README (220 routes mounted, then 285).

| Provider | Served at 0.8.0 | Served at 0.9.0 | Newly served | Untriaged at 0.8.0 |
|---|---|---|---|---|
| Scaleway | 102 / 315 | 107 / 315 | 5 | 0 |
| Outscale | 72 / 263 | 85 / 263 | 13 | 18 |
| Exoscale | 46 / 374 | 93 / 374 | 47 | 75 |
| **Total** | **220** | **285** | **65** | **93** |

Some of these operations are described in prose further down — #12 for Exoscale
block storage, #232 for instance pools, #161 for private networks, #172 for the
Outscale in-place half and the DHCP options. **None of them was named as an
operation**, and the operation name in the provider's own dialect is the string a
consumer greps for when their provider changes under them. Hence the lists.

- **Scaleway, 5 operations, and the only status change on that surface between
  the two tags: `instance/v2alpha1` private network interfaces** (#257, #260).
  `CreatePrivateNetworkInterface`, `DeletePrivateNetworkInterface`,
  `GetPrivateNetworkInterface`, `ListPrivateNetworkInterfaces`,
  `UpdatePrivateNetworkInterface` — declined in 0.8.0, served here.

  What that is worth to a consumer: `scaleway/scaleway` 2.81.0, published
  2026-08-17, still *creates* a private NIC through `instance/v1` and reads,
  creates and deletes it through
  `instance/v2alpha1/private-network-interfaces`, where the interface is a
  top-level resource carrying `server_id` rather than a sub-resource of the
  server. Against 0.8.0 that apply ends on a 501. A lane pinned at or below
  2.80.0 to stay green against 0.8.0 can be moved to 2.81.0 against this
  release — and `tools/conformance/scaleway/terraform/main.tf` pins exactly
  2.81.0, so the suite exercises those five operations rather than a version
  that never calls them.

- **Outscale, 13 operations**, all thirteen out of the untriaged column (#172,
  #177, #198): `AcceptNetPeering`, `CreateDhcpOptions`, `CreateNetPeering`,
  `DeleteDhcpOptions`, `DeleteNetPeering`, `LinkPrivateIps`, `ReadNetPeerings`,
  `RejectNetPeering`, `UnlinkPrivateIps`, `UpdateNet`, `UpdateNic`,
  `UpdateRouteTableLink`, `UpdateSubnet`. The remaining five left that column
  the other way, declined by name: `CheckAuthentication`, and the four
  `NetAccessPoint` operations.

- **Exoscale, 47 operations**, all forty-seven out of the untriaged column (#12,
  #161, #173, #232): the block-storage family
  (`create-block-storage-volume`, `get-block-storage-volume`,
  `list-block-storage-volumes`, `update-block-storage-volume`,
  `resize-block-storage-volume`, `delete-block-storage-volume`,
  `attach-block-storage-volume-to-instance`, `detach-block-storage-volume`,
  `create-block-storage-snapshot`, `get-block-storage-snapshot`,
  `list-block-storage-snapshots`, `update-block-storage-snapshot`,
  `delete-block-storage-snapshot`), the instance pools
  (`create-instance-pool`, `get-instance-pool`, `list-instance-pools`,
  `update-instance-pool`, `scale-instance-pool`, `evict-instance-pool-members`,
  `reset-instance-pool-field`, `delete-instance-pool`), the private networks
  (`create-private-network`, `get-private-network`, `list-private-networks`,
  `update-private-network`, `reset-private-network-field`,
  `delete-private-network`, `attach-instance-to-private-network`,
  `detach-instance-from-private-network`,
  `update-private-network-instance-ip`), the snapshots and templates
  (`create-snapshot`, `get-snapshot`, `list-snapshots`, `delete-snapshot`,
  `revert-instance-to-snapshot`, `promote-snapshot-to-template`,
  `copy-template`, `register-template`, `update-template`, `delete-template`),
  and `add-external-source-to-security-group`,
  `remove-external-source-from-security-group`, `enable-tpm`, `list-events`,
  `get-organization`, `reset-elastic-ip-field`, `reset-instance-field`.

  Twenty-eight more left the untriaged column declined by name (#173, #300):
  the eleven NLB operations, the sixteen `[BETA]` VPC ones, and
  `export-snapshot`.

**Nothing this release stopped serving.** No operation moved from `implemented`
to `declined` between the two tags, and no operation left any provider's
upstream surface. That direction is the more dangerous one and it is stated here
rather than left to be inferred from silence.

### Three declines a consumer took as settled: two had already gone *(recorded 2026-08-19)*

The same downstream report listed three refusals it agreed with and was not
asking us to change. Measured against the artefacts, **two of the three had been
lifted before 0.9.0 shipped, and the third still stands in it**. A withdrawn
decline is as load-bearing as an added route — a consumer keeps building around
an absence that is no longer there, and nothing anywhere turns red — and this
project published neither direction.

- **A Scaleway root volume of type `sbs_volume` is honoured** — since #8, which
  shipped in **0.8.0**, not here. `tools/conformance/scaleway/terraform/main.tf`
  declares the block, and the apply, the empty second plan and the destroy all
  pass. What stays overridden is the *local* types (`l_ssd`, `scratch`), for the
  reason `docs/limits.md` gives at this tag: the emulated catalogue declares
  `volumes_constraint.min_size` at 0 and the CLI sums local volumes against it,
  so attaching one would make the CLI refuse the very creation it just asked
  for. `b_ssd` will not plan either, and that is the provider's decision from
  2.79 on, not this emulator's.

- **`ipam/v1/API.BookIP` is served**, and with it `ReleaseIP`, `ReleaseIPSet`,
  `UpdateIP`, `AttachIP`, `DetachIP` and `MoveIP`.
  `coverage/scaleway-coverage.json` carries `BookIP` as `declined` at `v0.7.0`
  and as `implemented` from `v0.8.0` on: it was lifted by SW-4, the first half
  of #11, and 0.9.0 inherited it. A plan carrying a `scaleway_ipam_ip` works,
  and a booked address comes out of the Private Network's own subnet rather
  than being invented.

- **`osc/Client.CreateLoadBalancer` is still declined in 0.9.0**, and this one
  the report had right. `internal/providers/outscale/declined.go` carries it at
  this tag with the rest of the LBU family, on the stated reason that *a load
  balancer is a data plane accepting real connections, and the emulator has
  none*. `ReadLoadBalancers` is the deliberate exception and answers an empty
  list, because declining a read whose honest answer is "none" broke a measured
  `terraform destroy`. The family is served on `main` since #281, which landed
  after this tag; 0.9.0 does not serve it.

### The client versions 0.9.0 was proved against *(recorded 2026-08-19)*

Every claim in this note is true of these clients and of no others. They are the
versions the conformance workflow installs and runs, read from the tag rather
than remembered; the paths and line numbers are those of `v0.9.0`.

| Client | Version | Where the number is written, at `v0.9.0` |
|---|---|---|
| `scw` | 2.56.3 | `.github/workflows/conformance.yml:27` (`SCW_VERSION`) |
| Terraform | 1.13.3 | `.github/workflows/conformance.yml:28` (`TERRAFORM_VERSION`) |
| OpenTofu | 1.12.5 | `.github/workflows/conformance.yml:356` (`TOFU_VERSION`) |
| `oapi-cli` | 0.15.0 | `.github/workflows/conformance.yml:273` (`OAPI_VERSION`) |
| `exo` | 1.95.6 | `.github/workflows/conformance.yml:310` (`EXO_VERSION`) |
| `scaleway/scaleway` provider | **2.81.0, exact** | `tools/conformance/scaleway/terraform/main.tf:31`, and `examples/stacks/scaleway/main.tf:24` pins the same |
| `outscale/outscale` provider | `~> 1.7`, a constraint | `tools/conformance/outscale/terraform/main.tf:19`, and `examples/stacks/outscale/main.tf:27` |
| Go toolchain | 1.26.6 | `mise.toml:4` and `.github/workflows/conformance.yml:57` |

Terraform and OpenTofu each drive both provider constraints. This is the table
the README already generated at this tag under `<!-- clients:start -->`, plus the
sources; it was simply never carried into the release body, where a consumer
choosing a pin would meet it.

Two things the table cannot say, and does not:

- **The Outscale provider version that actually ran is not recoverable from this
  repository.** The fixture states `~> 1.7`, no lock file is committed beside
  it, and the resolution happened on the runner. The constraint is a documented
  floor rather than an oversight — the 1.7+ generation reads its endpoint path
  from the value where 1.1.x appends it, and pointing the emulator at the wrong
  generation is a six-minute timeout rather than an error — but what a consumer
  gets from us here is a range, not a number.
- **No Exoscale Terraform provider version is claimed, because none was
  proved.** `examples/stacks/exoscale/main.tf` declares none, and no CI job runs
  it: the published Exoscale provider compiles `.exoscale.com` into one of its
  two clients and cannot be pointed at a local emulator
  (exoscale/terraform-provider-exoscale#573), so an apply splits between the
  emulator and a paying account. The Exoscale pack's client proof in this
  release is the `exo` CLI at the version above, and nothing else.

`mise.toml` pins the toolchain and no client, so it is not a source for this
table.

### Added

- **A Go test can ask for a cloud, and so can a CI job** (#247, #245, #244,
  #246, #251). `feinttest.Start(t)` starts the published image and hands back
  the endpoint, with **zero dependencies** — deliberately not testcontainers,
  and its doc comment says why. `stephrobert/setup-feint@v1` installs the
  released binary, **verifies its checksum before running it**, and waits until
  the emulator answers; a gate compares the Marketplace copy against this
  repository's, so the mirror cannot drift in silence. `examples/` gained the
  GitHub Actions job, the GitLab `services:` template, the compose file and an
  Exoscale platform stack, and the Scaleway and Outscale stacks now apply
  against the image the release builds, on every pull request.

- **Two recordings, on the page rather than in the repository** (#252). Forty
  seconds of a cloud API answering an official client with no account behind
  it: one recording for the binary on a laptop, one for a GitHub Actions job
  pulling the image, each beside the snippet it belongs to, in both READMEs.
  Generated by `mise run demo` from tapes in the repository, so a command that
  breaks breaks the video.

- **Exoscale block storage** (#12). Thirteen operations — volumes, snapshots,
  the resize and the attach chain — driven by the `exo` CLI in the conformance
  suite. Every number a volume publishes says the storage holds no bytes, and
  the section of `docs/limits.md` that states so shipped with the routes.

- **Exoscale instance pools** (#232). One write that moves several machines: the
  pool creates, scales and evicts through the same lifecycle a single instance
  uses, driven by the official CLI.

- **An Exoscale private network is a range, and an attach leases from it**
  (#161). Addresses come out of the declared block instead of being echoed back,
  so two attachments cannot claim one address.

- **Every mounted operation is driven by a real client, or says why not**
  (#174). The last forty-two either gained a client in the conformance suite or
  declare, at the route, why no official client reaches them (`Route.Undriven`).
  The README banner counts the two apart, and `TestEveryUndrivenOperationSaysWhy`
  fails a reason that outlives its cause.

- **The Exoscale and Outscale untriaged columns were decided** (#173, #198).
  Twenty-four Exoscale operations and seven Outscale ones left the untriaged
  column the only two ways out: served with a client to prove it, or declined by
  name with the reason in the pack.

- **A release is measured against its consumers before it is tagged** (#170).
  `mise run compat:check` rebuilds the previous release from this repository's
  own history, runs expressions a consumer could legitimately have written
  against both binaries, and refuses the tag on any unaccepted silently-wrong
  verdict. Against 0.8 it found two, both the `probed`-as-boolean reading, now
  recorded in `tools/compat/accepted.json` with the reason a 0.8 consumer could
  not have checked a signal that did not exist yet.

- **The emulator builds its own machine images, and they carry an ssh daemon**
  (#203). No image on the upstream server has one — measured on four images,
  each read twice, all four with nothing listening on port 22. So a machine
  installed one at first boot, which required outbound internet, NAT and a
  managed bridge — and that bridge was a second, unpublished address a Scaleway
  server carried here and does not carry on the real cloud (#202, measured
  against real Scaleway and real Exoscale accounts: one address each, never
  two). A real cloud image has the daemon in it, so this is the faithful shape.

  `feint images` builds five images (one per cloud-init template family),
  `feint images --check` reports what is missing and exits 2, and `feint doctor`
  names them without ever building. The driver prefers a built image and
  **announces** its fallback to the upstream one instead of degrading silently.
  `tools/images/verify.sh` proves it the hard way: each machine gets a routed
  NIC on a block nothing routes and nothing masquerades, so it has no path to a
  package repository at all — twenty checks over five images, including
  **exactly one address** and two machines from one image carrying two different
  host keys.

  **Schema:** the CLI surface gains a verb, so `cliSurfaceVersion` moves from
  **1 to 2** (additive; nothing a consumer depended on changed shape).

### Changed

#### Observable schema changes

Read this section if a pipeline branches on Feint's output.

- **`/_feint/conformance` moves to schema version 3**, in two additive steps
  taken during this release. `evidence.*[].probed` stopped being a boolean and
  is now one of `response`, `refusal` or `none` (#156) — a consumer branching on
  `probed === true` would read a truthy string and count every refusal as a
  success, which is why the version moved rather than the meaning quietly
  widening. The payload then gained `fields`, the omission verdict described
  below (#88). **Version 3 is what 0.9.0 serves.**

- **`/_feint/health` moves to schema version 2** and gains `enforced` (#180).

- **`coverage/evidence.json` moves to version 3** and gains `generated_from`
  (#171).

- **Four surfaces are frozen by a test, not by a sentence** (#132). The shapes
  of `/_feint/health`, `/_feint/routes`, `/_feint/conformance` and
  `/_feint/trace`, the CLI's verbs and flags, and the exit codes (0 ok, 1 error,
  2 drift) each have a committed fixture — the field tree, never a value —
  compared by `go test` on every pull request. The three object payloads gain a
  `schema_version` field (**additive**: a new key) so a pipeline can branch on
  it, and the gate refuses a fixture change that does not move the declared
  version. The procedure for changing a frozen surface on purpose is in
  `RELEASING.md`.

#### Stronger proofs

- **The contract check now looks in the omission direction** (#88). The gate
  caught a field the emulator invents and could not see one it forgets: an
  absent field only violates a schema when the provider marked it `required`,
  which Scaleway does on 9% of its schemas. Twenty fields missing from
  `ReadVms`, and the `ReadImages` omission that segfaulted the Terraform
  provider (#86), were all green. Every answer a conformance run provokes is now
  also held to the *presence* of every field the provider's own API description
  declares, over the populated objects real clients create — and
  `tools/conformance/score.sh` fails on a missing field the way it fails on an
  invented one. A field a pack knowingly does not serve is excused through
  `DeclinedFields()` with its reason, and a decline whose field the run
  demonstrably serves fails as stale, so the excused list cannot rot.

  *Over the objects a client drove*, which is a rule rather than a phrase: the
  probe's answers vouch for nothing here, because every object in a probe-only
  run is the minimal one the seeding builds.

- **The probe seeds what it needs, so a refusal is a verdict rather than a
  shortage** (#163). Before probing an operation the probe brings into being
  what that operation needs, from the contract's own request schema and from
  resources it created earlier in the same run. No identifier is invented. For
  this change alone, regenerated the same way on both sides: `probed: response`
  **85 → 204**, `refusal` **106 → 4**, `none` **40 → 23**, and the contract axis
  **181 → 207 clean**. `driven`, `dataplane`, `behaviour` and `negative` are
  unchanged, which is the expected shape: seeding moves synthetic traffic and
  nothing a client drives.

  What still refuses is named rather than hidden: four operations that need an
  instance a synthetic prober does not start or an inventory the pack keeps
  empty, and twenty-three never probed — twenty declare no response schema at
  all, three take a path parameter that is not an identifier.

- **The evidence record's freshness rule becomes a control** (#171). *Deleting a
  conformance assertion demotes the operations it proved* was written twice and
  held by nothing. `coverage/evidence.json` now carries `generated_from` —
  digests of the contracts, the recordings and the conformance suites — and it
  gates the join rather than sitting beside it: remove an assertion from a suite
  and the suite digest moves, which makes the previous record unjoinable. Two
  legs merge by taking the stronger answer per axis, so a leg produced from
  others is refused by name.

- **A capability is verified against the host before it is published** (#181).
  `NewIncusOVN` set `OVN = true` whatever the host could do, so on any host
  whose daemon answered, the OVN driver reported available — measured on an
  Incus 7.2 host with no OVN wiring: `--vm auto` chose `incus-ovn` and
  `/_feint/health` published `isolation: true`, until the first network creation
  failed. **A false capability is strictly worse than none**, because this
  project sends every consumer to `capabilities.isolation` rather than to a mode
  name. `Verify` now asks the host once at startup, with two reads that create
  nothing; `auto` prints `incus-ovn passed over` with the reason and lands on
  the bridge that works, and `--vm incus-ovn` asked for by name refuses at
  startup, exit 1. The bound is stated rather than glossed: a wired northbound
  is necessary and not sufficient.

- **`/_feint/health` says which packs deliver a capability** (#180).
  `Capabilities()` declared `firewall: true` in every mode, one set for the
  whole process, while only the Scaleway pack ever handed a rule to the runtime
  — an Outscale or Exoscale security group was created, echoed back and
  reconciled onto nothing. A user following this project's own advice probed a
  port a deny-default group should have closed, found it open, and had been told
  the firewall was delivered. The payload gains `enforced`, keyed by capability:
  `capabilities.firewall` is what the runtime *can* do, `enforced.firewall` is
  who asks it to, and the honest check is both.

#### Behaviour

- **The in-place half of four Outscale resources is served** (#172). Create,
  read and delete of Nets, Subnets, Nics and route-table links were driven from
  the first day; the change a *second* `terraform apply` makes was not served at
  all, so a plan modifying a resource this emulator had created died on an
  operation nobody had decided about. `UpdateNet`, `UpdateSubnet`, `UpdateNic`
  and `UpdateRouteTableLink` now answer, all four driven by the real Terraform
  provider. Two rules the tests hold: **absent is not false** (reading a missing
  required flag as `false` would silently turn it off), and **an update writes
  only what was sent**.

- **The Outscale DHCP options lifecycle is served** (#172).
  `CreateDhcpOptions` and `DeleteDhcpOptions` join the already-served read, with
  the two refusals their document states: the account's default set cannot be
  deleted, and neither can a set a Net still wears. `UpdateNet` accepts the
  `default` keyword, and `ReadNets` answers the `DhcpOptionsSetIds` filter its
  provider walks. `CheckAuthentication` is declined with the IAM family: the
  emulator accepts every credential on purpose, so a validity verdict would
  describe an authentication it never performs.

- **`feint stop` says what it is about to discard** (#182). One line on stderr
  before the signal, naming the count and the way out — never a prompt and never
  a refusal, because CI drives `stop` and its exit codes are a frozen surface.
  Quiet when `--state` was recorded, when the store is empty, and when the
  instance no longer answers.

- **A mispointed client is told which side is wrong** (#179). First contact is
  the one moment a user cannot tell a broken emulator from a broken pointing,
  and the worst of the three documented traps was confident and wrong:
  `POST /api/v1/api/v1/CreateVms` replied *"feint does not serve
  api/v1/CreateVms"*, and `CreateVms` **is** served. All three stay 404; only
  the refusal starts telling the truth. The third is **derived, not declared** —
  the mounted route table already knows every prefix this process serves — so it
  will work for a fourth pack nobody has written.

### Fixed

#### Fifteen third-party Terraform stacks, and what they found

- **Fifteen Terraform stacks written by people who have never seen this
  repository were applied against the emulator** — five per provider, chosen for
  size rather than convenience, each recorded with its repository, commit and
  licence in `examples/stacks/surveyed.md` (#262). **Six needed no edit at all.
  Five needed exactly one, and in every case the cause was the third-party
  provider or the stack's own age, not Feint.**

  What that proves: configurations written independently of this project can be
  pointed at it and run. What it does not prove: that the products those stacks
  do *not* use work, or that fifteen repositories represent an ecosystem. The
  register keeps declined products — SKS, LBU, DBaaS, object storage, Kapsule —
  strictly separate from Feint defects, and counts them per provider, because a
  `501` naming an unserved route is the intended behaviour and a wrong `200` is
  not.

  The exercise found four defects, all of the second kind, listed below. It also
  produced a counter-proof worth as much: a 95-resource stack with three VPCs, a
  peering and routes across it applies, re-plans empty and destroys.

- **An Outscale subregion is a datum the reads restitute, not a constant**
  (#268, #269). A Vm placed in `eu-west-2b` read back `eu-west-2a`, so a
  stranger's second plan never converged; and `ReadSubregions` answered one
  element where a data source indexed two. Both halves now round-trip what was
  written, and every write path that takes a subregion validates against the
  same catalogue the read publishes.

- **An Exoscale declared query parameter is served or refused, never dropped**
  (#271). `GET /v2/template?visibility=private` answered the public catalogue,
  because the handler discarded its request — and the same signature sat on four
  more Exoscale operations and two Scaleway ones. All seven now read what their
  operation declares; `TestDeclaredQueryParametersAreRead` holds the rule for
  every mounted route, and the one deliberate refusal (`labels`, a filter whose
  wire format upstream never states) answers 400 and is documented in
  `docs/limits.md`.

- **A Scaleway Private Network publishes an IPv6 /64 beside its IPv4 block**
  (#270). A stack reading `one(pn.ipv6_subnets).subnet` found none and wedged
  even its own `terraform destroy` on the null. The network now carries a `/64`,
  deterministic across reads and stable through a snapshot.

  > **The shape is derived from the SDK and the published provider
  > documentation, not observed against a real account.** `shapes/scaleway.json`
  > holds no recording of a real Private Network read carrying a subnet, so
  > three things remain unverified: the prefix range actually allocated, whether
  > a `/64` is always the size, and whether a real `Subnet` carries fields this
  > emulator does not publish. #270 stays open until that recording exists.

- **An Outscale route reaches a Net peering, and a NIC keeps its tags** (#249,
  #250). Two defects two realistic stacks surfaced within an hour of being
  written, both invisible to the conformance suite of the day; the fixes held
  under a stranger's configuration in the survey above.

#### The same lesson, elsewhere

A valid response is not a proof of correct behaviour. A requested value must
round-trip, a catalogue must agree with the writes it validates, and a declared
filter cannot be ignored. Applying that rule beyond the four defects above:

- **A Vm's `BootMode`, `Performance` and `VmInitiatedShutdownBehavior`
  round-trip** (#276). Accepted with a 200 and read back as pack constants — the
  #268 shape, three fields over, and the same never-converging plan. The sweep
  that followed also corrected `ShutdownBehaviorConfiguration` (whose constant
  contradicted the SDK's own documented defaults), `TpmEnabled`,
  `ActionsOnNextBoot.SecureBoot` and `IsSourceDestChecked`. What the emulator
  echoes without enforcing is stated in `docs/limits.md`: no guest-initiated
  shutdown is ever observed, and no vTPM or secure boot reaches a guest.

- **Eighteen Scaleway list operations honour the 72 query parameters they
  declare** (#277). The rule from #271 held only where a handler read *nothing*;
  these read some parameters and dropped others, so `order_by=…_desc` answered
  ascending. Every `order_by` now applies with the SDK's documented
  per-operation default — a bare `scw instance server list` answers newest-first
  like the real API — along with the filters each operation declares. What cannot
  be served honestly is refused with a 400 naming the parameter, and recorded in
  `docs/limits.md`: an ordering by an attachment time nothing records, a value
  outside a declared enum, a filter upstream documents for one value only.

- **An acknowledged rule, route or tag survives its concurrent sibling** (#289).
  Two `CreateSecurityGroupRule` calls on one Outscale group each answered 200
  and one rule was never stored — eight acknowledgements, five rules — after
  which `terraform destroy` died on the phantom. The collection paths (security
  group rules, routes, route-table links, tag keys, and seven Scaleway update
  paths) now mutate inside the store lock. The shared barrage that should have
  caught this existed: its doctrine framed the property as *one field per
  writer*, so no pack ever drove a collection. It now names both forms.

- **The Outscale region is a datum, and its subregions follow it** (#290). Every
  Outscale region is served by the same API — a region is a property of the
  endpoint a client is pointed at, not of the surface — so freezing one region
  as the only one that exists refused stacks written for another, including the
  SecNumCloud region this project's audience uses. `FEINT_OUTSCALE_REGION`
  selects it; nothing configured keeps the previous behaviour. Reading the
  publication rather than the previous table also corrected it: **`eu-west-2`
  publishes three subregions today (PAR1, PAR4, PAR7), not two.**

- **The scalar half of the lost-update family** (#295). #289 fixed the
  collections; every fast path that read a resource, set one or two fields on
  the clone and committed it could still erase a concurrent write to a
  *different* field of the same resource — measured at 11 trials in 200, a tag
  answered 200 and gone. Volume, NIC, internet-service, public-IP, NAT and
  peering paths now hold read, check and write in one critical section.

- **`UpdateNic` serves `DeleteOnVmDeletion` on a NIC this pack attached**
  (#299). The handler looked for an attachment map nothing ever wrote, so the
  one request upstream provides to flip the flag answered *400, not attached*
  on an interface the emulator had itself attached with a 200 — and
  `outscale_nic_link` sends exactly that request. The flag is now stored beside
  the link and read back instead of being published as a constant `false`,
  which would have denied any write that did land.

- **A decline reason edited in a pack reaches the committed artefact** (#298).
  The doctrine of `Declined()` is that the reason travels as data; the mechanism
  delivered half of it. `coverage/scaleway-coverage.json` carried the pre-#260
  sentence **67 times** — the claim #260 had itself measured false — while the
  pack carried the corrected one, and both gates passed: the baselines compare
  operation names and statuses, and the README regenerates from the same stale
  artefact, so the two agreed with each other while disagreeing with the code.
  `feint coverage --artefact` now exits 2 on any skew, in `drift:check` and in a
  test that iterates the mounted packs rather than naming them.

- **The last untriaged operations in the repository were decided** (#300). The
  eleven Exoscale NLB operations and the sixteen VPC ones were the only
  `unknown` entries left across all three providers; both families are now
  declined by name, with reasons anchored on facts rather than intentions —
  `healthcheck-status` is a per-backend verdict whose enum has no third value,
  so an emulator that probes nothing would have to invent one, and the sixteen
  VPC operations are marked `[BETA]` upstream, a marker whose disappearance
  reopens the question by itself.

- **An Outscale subnet's `AvailableIpsCount` is read from the pool that hands
  the addresses out** (#217); a Scaleway server's protection flag governs
  exactly the actions it was measured to govern (#212); and an exclusive
  resource — an address, a volume attachment — has one live owner that the
  shared layer enforces for all three packs at once (#213, #214, #215).

#### Documentation and generated figures

- **The client matrix credits every provider CI drives a client against**
  (#155). The `Emulated provider` column was a constant in the generator, under
  a marker reading "Generated … Do not edit by hand", and it credited Terraform
  to Scaleway alone while CI had been running the Outscale Terraform suite on
  every pull request. Generated is not derived: the column is read from the same
  workflow scan the status table uses, and each row states the constraint that
  provider's own fixture pins, read from the fixture rather than restated —
  restated, it went stale within a release. Understating a proof costs as much
  as overstating one: an external review recommended deleting Terraform from the
  Outscale row on the strength of that column, which would have erased a suite
  applying twenty-one resources.

- **Around twenty documentation claims that the measurement contradicted were
  corrected**, several of them doubled in the French pages, which carried an
  extra revision of drift. Three counters written in prose disagreed with
  generated blocks a few lines above them; a page presented a closed issue's
  work as unmeasured; the examples told a reader to download a version that did
  not exist. Three of those claims became generated blocks or tests, so the
  next drift fails a run instead of being read as fact.

## [0.8.0]

### Fixed

- **A destroy assertion told two failures apart** (found while lifting a
  reserve on #152). The Outscale Terraform suite asked whether the emulator held
  *no* Net at all, which is true only when it is the sole creator against a
  fresh emulator. Run against a bench another run had touched, it announced "the
  destroyed Net still answers" while the destroy had worked perfectly: the
  message blamed the subject for the state of the bench.

  It now asks whether *this run's* Net is gone, names it when it is not, and says
  plainly when somebody else's resources are still around. Falsified: make the
  emulator keep a Net through its own delete and the suite fails naming the
  identifier.

- **The status table is generated from the workflow that proves it** (reported
  by a reader comparing the README with CI). It said Outscale was proven by
  `oapi-cli` and Terraform. Both were true of the repository and only one was
  true of CI: the Outscale Terraform fixture exists, applies twenty-one
  resources, plans empty and destroys clean — and no workflow ran it, so a
  regression in it would have reached a release without one red check.

  The suite is in CI now, on both engines, and the table is no longer
  hand-written: its *proven by* column is read from
  `.github/workflows/conformance.yml`, so a client appears when a workflow drives
  it and goes when it stops. A suite CI runs that nobody mapped is refused by
  name rather than dropped in silence — understating what is proven is the same
  defect wearing the other face.

- **Six defects an audit of the 0.8 train found, and the false verdict that
  found a seventh.** The delivery was audited twice before tagging; everything
  below was reproduced before being fixed.

  - **A block volume restored smaller than its snapshot answered 201.** The guard
    lived on `updateBlockVolume` while its comment stated a property of the
    volume — "a volume grows and does not shrink" — held on one of two paths. A
    10 GB snapshot restored into a 1 GB volume, `available`. Neither the barrage
    nor the invariant sweep could see it: one drives no block route, the other
    asks about identity, not about whether a size makes sense.
  - **Releasing an IPAM address took no lock**, while booking one held the
    allocator from rebuild to store. `allocatorFor` rebuilds occupancy from the
    IPAM resources alone, so deleting one is exactly what frees an address. Both
    release paths now hold it and re-read under it, and the writes go through
    `Commit` rather than `Put`, which re-inserts a resource the client released.
  - **A falsification claim was false.** "Neutralise any of the three locks and
    the barrage goes red on the first attempt" — thirty green runs with the
    private-NIC lock removed. Every worker takes its own subnet, so no contention
    defect can surface there whatever it drives. The claim now says which two it
    holds and names the test that holds the third.
  - **The evidence artefact was fourteen operations behind** at a release
    candidate, and nothing was red: `docs/routes.md` printed "—" for them, which
    reads exactly like an operation nothing has proven. A test now requires every
    mounted operation to have a row.
  - **A conformance assertion produced a false verdict.** `feint clean` gained a
    line reporting stale runtime records, printed before its tally; the Outscale
    suite prefix-matched the whole output and announced "the delete left a machine
    behind" while the tally said zero. It reads the count now, and refuses to
    decide when there is no count to read.
  - **The documentation claimed no workflow starts a machine runtime**, in five
    places across two languages, contradicted by the nightly job shipped in the
    same release train. It was frozen in the generator, so `feint docs --check`
    reconducted it at every release: the gate compares the page with its
    generator and proves the form, never the claim. The true distinction — no
    pull-request gate starts one — is what they say now.

### Changed

- **The verify recipe names the release workflow, not the repository** (#129).
  `docs/install.md` published `--certificate-identity-regexp
  'https://github.com/stephrobert/feint/.*'`, which accepts **any** workflow of
  this repository that ever gets `id-token: write` — a claim about who owns the
  repository rather than about what built the file. It is now anchored on
  `.github/workflows/release.yml@refs/tags/v`, and the `gh` recipe gains
  `--signer-workflow`.

  Both halves were run against the published 0.7.3: the new identity verifies it,
  and pointed at another workflow of the same repository it refuses, naming what
  it expected and what it got. The old one accepted that other workflow — the
  width being closed.

  `tools/release/preflight.sh` now extracts the identity **from the page** and
  runs it against the previous release, then checks it can still refuse. The
  recipe a reader copies and what the workflow signs cannot drift apart in
  silence, which is what happened here.

- **A barrage of concurrent traffic, and one invariant sweep over the store**
  (#134). Each pack now drives its own served routes from ten workers at once —
  Terraform's default parallelism — and the store is then swept by a
  provider-neutral check in `internal/core/store/storetest`: no identifier issued
  twice, no address held by two resources of one kind, no runtime object claimed
  by two resources. A snapshot restore racing live traffic has its own test.

  **It found a real defect on its first run against Exoscale**: elastic IP
  allocation was a read-modify-write with no lock, so three addresses out of
  sixteen creates went to two resources each. The Scaleway pack fixed that shape
  for its own pools long ago and this one never received it — written twice,
  fixed once, alive in the other copy.

  The sweep lives in the core and knows no provider, so a fourth pack inherits it
  by calling one function; it finds addresses by shape rather than by attribute
  name, because a sweep keyed on each pack's spelling is a sweep a new pack
  escapes.

- **A snapshot is understood or refused, and says which version it is** (#133).
  `feint snapshot save`, `GET /_feint/state` and `--state` now write
  `{"format": "feint-snapshot", "version": 1, "resources": [...]}` instead of a
  bare array, and `Restore` refuses anything it cannot account for: a version it
  does not read, a format that is not ours, an unknown field on a resource.

  **This is a breaking change to the snapshot and state formats.** A file written
  by 0.7.x is refused with a message saying how to convert it, and a `PUT
  /_feint/state` carrying a bare array is refused too. Taking a fresh snapshot
  from the instance that holds the state is the way through.

  The reason it is worth breaking: the old format lost data in silence. A
  snapshot carrying a field this build does not declare restored *successfully*,
  `encoding/json` dropped the field, and the next save wrote the file back
  without it. The store was coherent, wrong, and green — measured before the
  change, not feared. `snapshot.go` documents the format as made to outlive its
  instance and be loaded into another one, which is exactly when this bites.

  `Attrs` stays open, because its keys are data a pack chose rather than schema:
  a new attribute is not a format change, and refusing one would make every pack
  addition breaking.

  Two documents disagreed about whether any of this was a promise —
  `store.go` called the format an implementation detail with no compatibility
  promise, `RELEASING.md` listed it among the surfaces whose change is breaking.
  They now say the same thing, and the stricter one won.

### Added

- **A container image, control plane only, published with the release.** The
  release workflow pushes `ghcr.io/stephrobert/feint:<tag>` for linux/amd64 and
  linux/arm64 — the exact binaries the release signs, wrapped in `scratch`,
  16.6 MB, nothing else inside. It runs `feint serve` with `--vm off` and says
  so: real machines stay a property of the binary on an Incus host, and an
  image claiming otherwise would be the half-truth this project refuses. The
  proof is not "it starts": the conformance workflow's `image` job drives the
  emulator inside the container with the official `scw` CLI and the contract
  probe on every pull request, through the published port, the way a
  `services:` block reaches it. The image is signed (keyless, by digest) and
  carries provenance and SBOM attestations under the same
  `release.yml@refs/tags/v` identity as the binaries; the verification recipe
  in docs/install.md is executed by the release workflow against the image it
  has just pushed, and by the preflight against the previous release. One tag
  per release, nothing mutable: no `latest`. A release now refuses to push an
  image whose own `feint version` is not the tag being released.

- **Scaleway serves Block Storage, and a server's root volume can finally be
  written** (#8). `block/v1` and `block/v1alpha1` mount 22 routes: volumes,
  snapshots and the volume-type catalogue. `root_volume { volume_type =
  "sbs_volume" }` is honoured, the disk is created in Block, and the Terraform
  provider reads it back through the fallback it always used —
  `instance.GetVolume` first, then `block.GetVolume` on a typed 404.

  Before this, `root_volume` had no usable value at all: the provider refuses
  `b_ssd` outright from 2.79 on, and `sbs_volume` planned for ever. Measured and
  reported by @vde-dis, who tried honouring it, watched the apply die on
  "waiting for Volume failed: http error 404 Not Found", and threw the change
  away rather than guess. The conformance fixture omitted the block, so the
  suite was green while never asking the question — a test that avoids the one
  input that breaks. It declares the block now.

  Two things only the real clients could have said, both found on the first run
  of the suite. `scw` 2.56.3 calls `/block/v1alpha1` for **every** block command
  while the Terraform provider calls `/block/v1`: two official clients of one
  cloud, each pinned to a different spelling, so both are served from the same
  handlers rather than one being declined on a reason the CLI falsifies. And
  `scw block volume create` refuses a raw byte count where the instance command
  takes one, wanting `10G`.

  The volume shape comes from a recording of a real account rather than from the
  SDK, which is why `kms_key_id` and `last_detached_at` are present and null and
  why `references` is computed from the attachment rather than stored beside it.
  A reference identifier is derived from the pair it joins, so it reads back the
  same on every plan.

  Refusals a client can observe: a volume created from neither or both of
  `from_empty` and `from_snapshot`, a volume that would shrink, a `kms_key_id`
  naming a Key Manager this emulator does not serve, a volume deleted under its
  server, and a snapshot deleted under a volume restored from it. The two Object
  Storage transfers stay declined, for the reason `instance/v1.ExportSnapshot`
  carries.

  Conformance: **158 of 206 routes proven by a real client**, up from 146 of 184.

- **Scaleway serves snapshots and images the client creates** (#7). The
  golden-image sequence — snapshot a volume, cut an image from that snapshot,
  list and delete both in the order the API imposes — is a control-plane path
  answerable at every step, and Outscale has answered it since 0.6.0 while this
  pack declined it. Nine operations move from `Declined()` to served:
  `Create/Get/List/Update/DeleteSnapshot`, `Create/Update/DeleteImage` and
  `ListImages`, which now carries the client's images beside the fixed
  catalogue.

  The refusals they replace read "a snapshot copies the bytes of a volume" and
  "the catalogue is a table nothing can add to". Both sentences were true and
  neither was a reason: the bytes are the one part an emulator cannot provide,
  and that limit is named where it bites rather than at the door — an image cut
  here boots nothing and says so (#115), instead of substituting a distribution
  nobody asked for. `ExportSnapshot` stays declined, since it writes those bytes
  into Object Storage.

  Two refusals a client can observe: a snapshot of a volume that does not exist
  is a 404 rather than a record of nothing, and a snapshot an image is cut from
  refuses to delete until the image is gone — the order Terraform walks when one
  plan removes both. `GET /images/{id}` reads the client's image before the
  catalogue, without which a create and its following read described different
  objects.

  `scw instance snapshot create`, `image create`, `image list` and the deletion
  order are driven end to end by the conformance suite: 146 of 184 routes are
  now proven by a real client, up from 140.

### Fixed

- **`feint --help` names every command it dispatches** (reported by @vde-dis).
  `shapes` answered, was documented in the guide, and was missing from the help,
  so anyone exploring the CLI the way people do could not find it. The line is
  added, and a test now reads the dispatch from the source and requires each
  verb to appear: adding the missing entry fixes today, comparing the two lists
  is what stops the twenty-first verb from shipping invisible.

## [0.7.3]

### Fixed

- **The address a client reads now answers, on every provider and in both
  runtime modes** (#116, #117). A server carrying a flexible IP published one
  address while the machine held another, or none: the routing was recorded
  before the machine existed and never replayed at poweron, and a machine with
  no private NIC booted on the operator's own profile bridge, where applying a
  firewall rule re-plugged the interface and cost it the DHCP lease. So `ssh`
  to the published address timed out while the container was up and answering
  its neighbours.

  The address a client reads stays the provider's own — TEST-NET-3, -2 and -1,
  one block per pack so two emulated clouds on one host can never route the
  same /32 — and it is now genuinely routed, from the first boot, on a network
  the emulator owns. Publishing the runtime's DHCP address was rejected: it
  desynchronises `server.public_ips[].address` from `GET /ips/{id}`, which the
  real cloud never does, and it varies with `--vm` and with the host.

  `dynamic_ip_required` was decoded, echoed back in the response and read by
  nobody; it allocates an ephemeral address now, released at stop. Because the
  field *was* read, it never appeared in `unread_request_fields` — the one
  blind spot the contract gate has.

- **The login is proven on all three packs, not asserted on two.** `ssh.sh`
  existed for Scaleway alone, so `outscale` and Exoscale's template
  `default-user` were names in a table nobody drove. There are three suites
  now, each registering its key through its own provider's API and opening a
  real session, and each fails rather than skips when a runtime is present —
  the Scaleway one used to skip there, which is how a broken address shipped
  under a green run.

- **What OVN cannot do is declared rather than written as a limit.** A
  subnet-internal address answers the host in bridge mode and not in OVN,
  because the router that separates two VPCs also SNATs the host's connections
  on the way back — measured, with sshd up and answering its neighbours while
  the host read the port as closed. `capabilities.private_from_host` says
  which, so a probe skips with a reason instead of failing, and the routed
  public plane crosses in both modes, which is why every pack's ssh chain uses
  it.

- **A whole Scaleway product answered `404 page not found` in plain text.**
  Reported by @vde-dis on #74, measured under a real OpenTofu apply:
  `/lb/v1/…` and `/vpc-gw/v2/…` fell through to net/http's default mux, and the
  Scaleway SDK reads the content type first and drops a body that is not JSON,
  so the caller got `404 Not Found` and nothing else. Every other refusal
  measured answers in the provider's own dialect.

  The prefix list declared only the five products this pack serves, while
  Scaleway's URL space has sixty-two. It now declares the space rather than the
  served part, extracted from the SDK's generated request paths — not its
  directory names, which differ: the SDK says `vpcgw`, the URL says `/vpc-gw/`.

  The guard that was supposed to prevent this could not see it.
  `TestEveryRouteFallsUnderADeclaredPrefix` walks the routes the pack mounts, so
  a product with **no** routes is invisible to it by construction, and its
  comment claimed it stopped "a whole product answering net/http's plain text".
  Falsified: with the list back to five, the new test fails and that one still
  passes, which is the blind spot demonstrated rather than argued.

- **An unknown image identifier no longer boots a substitute** (#83). With a
  runtime configured, all three packs silently replaced an image no catalogue
  held — ask for Alpine, boot Ubuntu — while the API kept reporting the
  identifier the client sent; Scaleway's resolution matched labels by
  substring, so `centos`, `rocky` and `ubuntu_focal` all became Ubuntu 22.04.
  The create still succeeds, as `docs/limits.md` promises for hardcoded
  production identifiers, but the boot now refuses: the machine reaches the
  provider's own failed state and the log names the identifier. Image and
  login now resolve together — root on Scaleway, `outscale` on Outscale, the
  template's `default-user` on Exoscale — and the Scaleway marketplace answers
  one fixed UUID per label, so a label resolved through it by Terraform still
  names the distribution it chose. An image the client registered through
  Outscale's `CreateImage` refuses the same way — the emulator serves its
  record and holds no disk contents — and the log says which of the two cases
  it is.

### Security

- **Two CI installs took whatever was newest.** `pipx install uv` in the weekly
  drift workflow was pinned to nothing at all — in the job that regenerates
  committed artefacts and opens a pull request with them — and commitizen was
  pinned by version rather than by hash. Both install from a requirements file
  with hashes now, and `TestTheWorkflowsPinTheSameToolsAsMise` fails when a
  version there stops matching the file that owns it: `mise.toml` for uv, the
  pre-commit hook for commitizen, so a workstation and a runner cannot run
  different tools. Reported by OpenSSF Scorecard's Pinned-Dependencies.

- **`SECURITY.md` carries a reachable address.** It described how to report and
  linked nothing, so a reader had to know where GitHub keeps the form. It names
  the advisory URL now, and a second route for anyone GitHub is unavailable to.

## [0.7.2]

### Added

- **140 of 175 routes are proven by a real client**, up from 132. The eight are
  the ones OSC-3 and OSC-4 named among their deliverables and no client had ever
  driven: the four NIC operations, `ReadNatServices`, and the three updates
  Terraform never walks because it replaces rather than updates — `UpdateRoute`,
  `UpdateVolume`, `UpdateImage`. Both batches (#10, #13) close on that, rather
  than on operations being served.

### Fixed
- **An attached network interface reported a link state the Terraform provider
  refuses.** `LinkNic.State` published `in-use`, which is the state of the
  *interface*; the state of the *link* is `attached`, and the provider polls
  `ReadNics` until it reads that. It gave up with `unexpected state 'in-use',
  wanted target 'attached, detached, failed'`, leaving an apply half done and a
  destroy unable to finish. The same file rendered `attached` for the primary
  NIC inside a Vm four dozen lines away — one field, two spellings — and the
  unit test asserting `in-use` was holding the wrong one in place under a name
  claiming it matched a recording, when `feint shapes` records field trees and
  never values.

- **A recording could stop early and say nothing.** Some APIs hand the client an
  address in a response body — Exoscale publishes an `api-endpoint` per zone —
  and a client that follows it walks away from the proxy for everything after.
  Measured on a real account: a session worth about ninety exchanges recorded
  **eight**, and the transcript looked complete. `feint proxy` now counts
  responses that named a host other than the one the client is addressing, names
  those hosts when the run ends, and says plainly that whatever went there is
  absent from the file.

  Detected by shape rather than by field name — an absolute URL whose host is
  not the client's — because naming `api-endpoint` would put one provider's
  vocabulary into a tool that carries none, and the next API to do this would be
  silent all over again. Two properties carry their own tests: an answer naming
  the proxy itself is **not** a handoff, because the emulator's own zone list
  points back at itself and an alarm that fires on the normal case gets ignored;
  and a gzipped body is decompressed before the scan, because `scw` and `exo`
  both send `Accept-Encoding` and a scan over compressed bytes finds nothing in
  a way that reads like nothing being there.

  It does not rewrite the address, deliberately: a recorder that edits what it
  records is not one. `docs/proxy.md` now states what a recording can promise
  for all three providers rather than for Outscale alone — one refuses loudly,
  one used to truncate quietly, one records whole (#92).

## [0.7.1]

### Fixed

- **`CreateTags` answered that a resource the emulator had just created did not
  exist.** Reported by Vincent Dislaire against an `outscale_internet_service`
  with a `tags` block: the apply failed on `the resource igw-… does not exist`,
  about a resource `ReadInternetServices` was serving. The prefix table in
  `tags.go` held four entries, written when the pack served four kinds, and
  0.6.0 added ten resources without touching it. Three prefixes were reported;
  reading which schemas declare `Tags` found **ten** — volumes, snapshots,
  images, security groups, route tables, public IPs, NICs, DHCP options, NAT
  services and internet services.
- **Two of the four `ResourceType` values `ReadTags` published were invented**:
  `net` where Outscale's own SDK says `vpc`, `vm` where it says `instance`. A
  client filtering on `instance` matched nothing. No contract could see it —
  their OpenAPI declares `ResourceType` as a bare string — and a unit test had
  been asserting `net` for three releases, which is how an emulator's mistake
  becomes the thing its own suite protects.

  The values now come from `TagResourceType` in `osc-sdk-go`, and a test pins
  every row of the table to that enum.
- **The table that caused it can no longer fall behind in silence.** Every
  identifier prefix the pack mints is read from the source and has to be
  triaged: taggable with its upstream type, or refused with a reason, the same
  discipline `Declined()` applies to operations. Adding the ten missing rows
  fixes today; the eleventh resource would have done it again.

### Changed

- **Exoscale is *starter*, not *preview*.** The label was taken deliberately,
  with a written exit condition — *until the Terraform provider is proven
  against it* — and that condition turned out to rest on an assumption
  measurement refuted: the provider honours `EXOSCALE_API_ENDPOINT` for its
  egoscale v3 client and builds a v2 one with no endpoint option, so an apply
  splits between this emulator and a paying account. `ClientOptWithAPIEndpoint`
  exists in egoscale and is never called; three sites build a v2 client without
  it. Filed upstream as
  [exoscale/terraform-provider-exoscale#573](https://github.com/exoscale/terraform-provider-exoscale/issues/573).

  A condition nobody here can reach is not a condition, it is a hostage — and
  what the label warned about was fixed by EXO-2 anyway: `exo` drives stop,
  start, reboot, scale, resize, a delete refused while protected, a security
  group rule round-trip, an anti-affinity group, and an elastic IP attached,
  published and withdrawn. What still separates Exoscale from *usable* stays in
  the generated coverage tables, where it cannot flatter: 75 operations
  untriaged, against 18 for Outscale and 0 for Scaleway.

- **The Outscale row credits Terraform**, which has driven seventeen resources
  end to end since 0.6.0 and was still listed as `oapi-cli` alone.

## [0.7.0]

### Added

- **`feint shapes` records what a real cloud returns and checks the emulator
  against it.** The field tree only — paths and JSON types, no values and no
  identifiers — which is what makes it committable where a transcript is not: a
  transcript describes somebody's account, a shape describes an API. Recording
  needs a real account and stays a human's job; `feint shapes --check` compares
  offline, with no credential, and reports how much it compared so a green
  cannot be read as "nothing is wrong" when it means "nothing was checked".
- **`internal/upstream`, one place that knows how to talk to a real cloud** —
  signing, pacing and retrying for the three providers, answering with the same
  `trace.Exchange` the proxy writes. Seven copies of the Outscale signature had
  accumulated in throwaway scripts, and the difference between two of them cost
  an hour: one signed the request path, one signed `/`, and only the cloud could
  tell them apart. Each signature now comes from its provider's own source.
- **Exoscale serves a lifecycle**: instance start, stop, reboot, scale, resize
  and delete, security groups and their rules, anti-affinity groups, elastic IPs
  and their attachment — 16 to 46 operations served, 108 to 75 untriaged. Every
  lifecycle path goes through `Binding.Serialise`, with a concurrency test under
  `-race`.
- **131 of 175 routes are proven by a real client**, up from 109 of 145.

### Fixed

- **`data.outscale_images` segfaulted the Terraform provider.** Reported by
  Vincent Dislaire, who traced it to the provider's own sources: three fields
  read without a nil guard in a loop where every neighbour survives one, and the
  catalogue published none of them. He measured the fix rather than guessing it,
  injecting an empty `BlockDeviceMappings` through a proxy and watching the crash
  move to the next field, then the next, then stop.
- **Fields the real clouds return and these did not**, found by comparing each
  pack's answer with a recording rather than by a client breaking: eleven on
  every Scaleway server product — including `capabilities.placement_groups`,
  which this pack serves and a client checking the capability first would have
  read as unsupported — twelve on Outscale's `ReadImages`, three on
  `ReadVmTypes`, seven on Exoscale's `template`.
- **Sixteen Exoscale routes that were already served were answering the wrong
  shape**: `instance-type` and `template` inside an instance are bare references
  `{id}`, not expanded catalogue entries, and a unit test was locking that
  mistake in because it asserted the schema rather than the wire.
- **A header nobody vouched for is no longer written down.** Redaction matched
  eight name substrings, and the three dialects served here passed only because
  their bearers are called `Authorization` and `X-Auth-Token` — a coincidence,
  not a rule. Reproduced: `X-Auth-Token` redacted and `X-Consumer` carrying the
  same value written in full. Request and response headers are now an allowlist;
  bodies stay a denylist, because a body **is** the measurement and a header is
  not.

### Changed

- **The Scaleway root volume type has two reasons, and one was missing.** The
  comment beside `rootVolume` justified forcing `b_ssd` with an argument that
  covers local volumes only, so it said nothing about `sbs_volume` — and a
  reader lifted the restriction on that basis. `docs/limits.md` now states the
  consequence plainly: no `root_volume` type is writable today, `b_ssd` will not
  plan from provider 2.79 on, `sbs_volume` plans for ever, and omitting the
  block is the way through. It ends with #8. Reported by Vincent Dislaire.
- **`docs/fourth-pack.md`** measures what a fourth provider pack would touch:
  about 45 additive lines across 13 shared files, and no code in `internal/core`
  naming a provider. The neutral-core rule holds under measurement rather than
  by assertion.

## [0.6.0]

### Added

- **Outscale's routing and storage families**, driven end to end by the real
  Terraform provider: security groups and their rules, route tables, routes and
  their links, network interfaces, internet services, NAT services, public IPs
  and their links, snapshots, and images a client registers beside the fixed
  catalogue. `tools/conformance/outscale/terraform.sh` now applies the provider's
  own `examples/net_vm` plus the storage chain — seventeen resources — with an
  empty second plan and a clean destroy. The conformance score went from 88 to
  109 of 145 routes proven by a real client.
- **Twenty fields on `ReadVms` that the real cloud returns and the emulator did
  not**, including `Nics`, `Placement`, `Architecture`, `BootMode`,
  `RootDeviceType` and `PrivateDnsName`; `LinkPublicIp` on an interface; and
  `SnapshotId` on a volume cut from one. Found by recording a real account and
  diffing it per operation against the emulator — a class of defect no contract
  can see, because Outscale's schemas declare almost no required field (#88).

### Changed

- **Filters a real client sends are applied rather than refused**, on route
  tables (`RouteDestinationIpRanges`, the link filters), public IPs
  (`LinkPublicIpIds`), machines and interfaces (`SecurityGroupIds`) and volumes
  (`LinkVolumeVmIds`, which is how the provider waits for an attach and a
  detach). Each was a 400 that stopped a real apply or destroy partway.
- **`ReadLoadBalancers` answers an empty list** instead of being declined. The
  rest of the family stays declined: declining a read whose honest answer is
  "none" costs a client the ability to ask and buys no honesty.

### Added (tooling)

- **`feint proxy`**, a reverse proxy that sits between a real official client and
  a real cloud and writes down every exchange as JSON Lines of
  `internal/trace.Exchange` — the same shape the emulator's own ring publishes.
  Credentials never reach the file: redaction is a property of the recorded type,
  not a step a call site can forget. It is how this project stops guessing what a
  client sends and measures it instead, and the first real passage recorded a
  genuine finding — `scw` calls `GET /block/v1alpha1/zones/{zone}/volumes/{id}`,
  which no pack serves. Loopback only unless `--expose-to-network`, because every
  request through it carries a live credential.
- **`feint transcript`**, which turns a proxy recording into the three answers a
  developer needs before serving one more operation, so the file is queried by a
  verb instead of by knowing where in the JSON each fact sits:
  - with no flag, **the operations a real client called that no pack serves**,
    ranked by call count then response size — the work queue, derived from a
    measurement instead of the roadmap's guess;
  - `--shape <operation>`, **the field tree the real cloud actually returned**,
    which is not what the SDK says it may return;
  - `--shape <op> --against <emulator.jsonl>`, **the fields the real cloud returns
    that the emulator omits or types differently** — a response-shape defect no
    unit test can see, found before the handler is written rather than after.
    Measured against Outscale, this reported that the emulator's `ReadVolumes`
    omits `SnapshotId` and never populates `LinkedVolumes`.

## [0.5.0]

### Added

- **A page the binary serves about itself**, at `/_feint/ui`, opened by
  `feint ui`. Served on the loopback interface only — off loopback it is not
  mounted at all — read-only, with no authentication by design, and with no
  dependency: three files embedded in the binary, no build step, no framework.
  It shows served against driven against probed without ever adding them up, the
  versioned gap with each provider's upstream API, everything the session
  created, and a live log of the calls. Every aggregate opens onto what it is
  made of.
- **`GET /_feint/resources`**, an inventory read from the store rather than from
  a provider API, so a pack nobody has written yet is listed with its own
  vocabulary. Whole attributes, never a curated subset. It is the one endpoint
  that publishes `Runtime` — the container backing a machine — and a test drives
  every provider read route to prove none of them does.
- **`GET /_feint/events` and `GET /_feint/trace`**, the call log: a bounded ring
  of 256 exchanges, as a one-way event stream for the page and as a JSON array
  for a script or a CI job that reads it after the fact. Each line carries the
  time, the status, the duration, the operation, the fields a client sent that no
  handler read, and the fields an answer carried that the provider's own API
  description does not define. Both were already computed and displayed nowhere.
- **The record's shape is `internal/trace.Exchange`**, published once so the
  transcript `feint proxy` will write (X-2) and the replay that reads it (X-3)
  share one format with the emulator's own ring.
- **The coverage artefacts carry their per-operation verdicts**, with the reason
  each declined operation was declined. 625 arguments that were previously
  reachable only by running a scan against an SDK checkout.

- **The page is checked in a real browser, and photographed by the same run.**
  `mise run docs:ui` loads it against a live emulator, waits for its data, and
  asserts eighteen values against what the endpoints answered — the counts, a
  created resource and its attributes, a refusal reason in full, a logged call
  and the field no handler read — before writing the images the README shows. It
  runs on every pull request. `docs/limits.md` says what it covers and what it
  leaves out; the short version is that a renamed node now fails CI and an ugly
  stylesheet still does not.
- **The screenshots are on the existing freshness rail.** `feint docs --check`
  compares a digest of the page with the one recorded beside the images, so a
  change to the page without regenerating them fails the pre-commit hook, the
  docs gate and the release preflight. It compares the page, never the pixels:
  the page renders wall-clock values, so a byte comparison would be red forever.

### Changed

- **`/_feint/health` answers `capabilities: null`** when the machine driver
  declares nothing, instead of an object of five `false`. Silence and refusal
  were indistinguishable on the wire, so a reader printed "no" on behalf of a
  driver that had never been asked. `feint status` now says
  `isolation: not declared` in that case. A driver that declares — every one
  that ships today, including the no-op — is unaffected.
- **`/_feint/conformance` carries `probes`**, the per-operation probe counts,
  alongside `calls`. The scalar `probed` could say how many routes only a probe
  had reached, never which.

## [0.4.1]

### Fixed

- **`feint clean` collects the state directories nobody was collecting.**
  Measured on a development station after one day: fourteen directories under
  `XDG_RUNTIME_DIR` for two live emulators, twelve of which described nothing at
  all. `stop` clears the record and leaves the directory holding the log — right
  on its own, since a crash is read after the fact or not at all — but nothing
  ever swept them, so the state directory stopped being readable as an answer to
  "what is running". A live emulator is never touched, whatever its age.
- **`feint clean --vm off` now exits 0.** It swept, it said so, and it exited 1,
  because the sweep used to be reachable only through a machine runtime. `off`
  is the default of `serve` and the majority of runs, so this was the common
  path rather than an edge — and a success that exits like a failure is the
  ambiguity this project refuses everywhere else. A runtime that genuinely
  cannot be swept still fails.

  The message `nothing was left behind` gains `on the runtime`, and is now
  printed in every mode. `tools/conformance/scaleway/network.sh` decides the
  runtime is clean by matching that line, and it must not change its answer
  because a directory was collected.

### Changed

- **The roadmap carries the queue that came out of comparing this project with
  LocalStack**, paid tiers included, and the refusals that comparison forced.
  Three items outrank a batch, on one filter — which of them lower the cost of
  coverage: recording what a real client and a real cloud say to each other, the
  emulator as an importable package, and fault injection. The refusal to
  intercept DNS and terminate TLS is reopened as a question rather than
  decided: the measurement it rests on stands, the cost estimate that followed
  it was never made.
- **`CONTRIBUTING.md` says how an issue title reads.** A defect is titled by the
  symptom, because the diagnosis is often wrong when the issue is opened and the
  title outlives it; a unit of delivery carries its batch code, which is what a
  commit closes it by naming.

## [0.4.0]

### Added

- **Terraform drives Outscale.** `init`, `validate`, `plan`, `apply`, a second
  empty plan and `destroy`, against the published provider `outscale/outscale`
  v1.7.0. Until now everything this pack claimed was proven by `oapi-cli` alone,
  and the provider walks paths no CLI does. Writing the fixture was enough to
  find six defects, each listed below.
- **Volumes.** Create, read, update, delete, link and unlink. A volume grows and
  refuses to shrink — a filesystem does not survive its disk getting smaller —
  and a linked volume refuses to go, which is what a client destroying in the
  wrong order needs in order to retry. Snapshots stay declined: there are no
  bytes behind an emulated volume, so restoring one would produce a disk holding
  nothing.
- **Tags on every resource that carries them**, sorted, because their order is a
  permanent Terraform diff waiting to happen. And `ReadVmsState`, the
  lightweight view a client polls.
- **74 of 104 routes are proven by a real client**, up from 69 of 93. 23 more
  are probed only: the protocol holds and the behaviour is unproven.

### Fixed

- **`terraform apply` died on the first machine.** The catalogue published no
  `ProductCodes`, so the provider called `ReadAdminPassword` on every Vm it read
  back, Linux included — it is a Windows call, and an absent list reads as
  "unknown". That route did not exist. Images and Vms publish the Linux code
  now, and the call answers an empty password, never a generated one: a made-up
  credential is one a client could try to use.
- **Every `terraform destroy` crashed the provider outright** — "Plugin did not
  respond", reproduced with the published, signed provider v1.7.0. `DeleteVms`
  removed the record, and the provider answers a delete by polling `ReadVms`
  until the Vm reports `terminated`: an empty list is not a state its waiter
  knows. A terminated Vm now stays readable, as it does upstream, and holds
  nothing — it is skipped by the Subnet guard and by the address count.
- **The destroy then failed on the keypair**, with "the keypair  does not exist"
  and a gap where the id should be: the provider creates by name and destroys by
  id, and the pack read only the name.
- **Four fields the provider sends that the pack declared nowhere**, the worst
  being `DeletionProtection` — accepted and dropped, which told a client its
  machine was protected when nothing protected it. Also
  `NestedVirtualization`, `ResultsPerPage` on three reads, and `ForceStop`.
- **`feint status` reported 0 in the "driven by a client" column, always.**
  `internal/cli` declared its own shape for `/_feint/conformance`, with a key the
  server emits nowhere and `untouched` as an object where the wire carries an
  array. The decode failed, both callers fell back to empty, and the header
  comment of `status.go` promised exactly that number. Measured after two `scw`
  calls: 0 before, 2 after. The copy is deleted rather than corrected — the view
  is now one exported type both sides read.

### Changed

- **`feint serve` refuses a non-loopback address** unless
  `--expose-to-network` says otherwise. Off loopback the anti-rebinding guard
  stops refusing anything — correctly, since it can no longer tell what is local
  — and the only output was `feint dev listening on 0.0.0.0:4599`. Measured: a
  cross-origin page and a forged `Host` both get 200 where they get 403 on
  127.0.0.1. With `--vm` on, what was then reachable from the network is a
  container runtime.

  This is a behaviour change a user can observe. Anyone who was deliberately
  exposing the port adds one flag; anyone who was not was exposing more than
  they knew.

## [0.3.3]

### Fixed

- **An Exoscale instance could be created with none of the fields the API
  requires**, and one such instance made `exo compute instance list` stop
  listing: every instance created after it disappeared from the official CLI's
  output.
- **A registered SSH key never reached the machine it was attached to.** The
  pack kept a name and a fingerprint and dropped the key itself, so the instance
  booted with no user and no way in, while the API published an address on it.
- **A key sent as `ssh-key` was accepted and dropped.** Their API documents both
  `ssh-key` and `ssh-keys`, neither deprecated; the pack read only the plural.
- **Nothing was checked at the Exoscale entry**: names and keys carrying control
  characters were stored and given back verbatim.
- **`exo limits` answered 404.** It is a first-class command of the official
  CLI, and the quota routes were neither served nor refused.

### Added

- **Quotas, counted rather than invented.** The limit is a claim this emulator
  makes, like its catalogue; the usage is a fact it holds, counted from the
  store. `exo limits` reports zero instances on a fresh emulator and one after a
  create, and the conformance suite checks exactly that.
- **69 of 93 routes are proven by a real client**, up from 68 of 91.

### Changed

- **The Exoscale Terraform provider is refused, with an explanation.** It
  honours `EXOSCALE_API_ENDPOINT` for half of its calls and reaches the real
  cloud with the other half, so an apply split between this emulator and a
  paying account — measured, without a byte leaving the machine. Half serving a
  client is worse than refusing it: a half-success is indistinguishable from
  working until the invoice. `FEINT_EXOSCALE_ALLOW_TERRAFORM=1` lifts the
  refusal for anyone who understands the split, and `docs/limits.md` carries the
  whole reasoning. The `exo` CLI is unaffected.

  This is a behaviour change a user can observe, and it is a patch rather than a
  minor on purpose: what stops working was never working — it was creating
  resources on a real account while looking local.

## [0.3.2]

### Fixed

- **`FEINT_VM=incus-ovn mise run conformance` could not run at all.** The
  Outscale network suite built a binary name out of the mode name and looked for
  `incus-ovn`, which does not exist — so the one mode that delivers isolation
  between two VPCs was the one that could not be verified end to end. It passes
  now, both network suites included.
- **A virtual machine never carried the address the API published** with
  `--vm incus-vm`. Four causes: the interface was added while the machine was
  still coming up, which failed intermittently; the guest names its interfaces
  differently from the runtime, so the address was applied to a name that does
  not exist inside; the generated hardware address is not stored where the
  driver looked for it; and when the attachment failed anyway, the private NIC
  went on answering `available`. It now answers `syncing_error`, so a client
  learns what the log knew.
- **Every filter but one was ignored** on the Outscale reads, and answered with
  the whole inventory and a 200 — indistinguishable from success, so a script
  that deletes what a filter matched deleted everything. Filters are now applied
  or refused with the field named; Vms filter on ids, states, images, types,
  keypairs, subnets, nets and addresses, Nets and Subnets on ids, ranges and
  states, keypairs on names, fingerprints and types.
- **The SSH key fingerprint matched nothing a client can compute.** It was taken
  over the whole key line, comment included, instead of the decoded key, so it
  differed from what `ssh-keygen -l -E md5` prints and changed when only the
  comment changed. `KeypairType` also answered `ssh-rsa` for every key,
  including ed25519 ones, and a key whose material is not valid base64 was
  accepted — which boots a machine holding bytes no SSH daemon will read.
- **`UpdateVm` accepted what `CreateVms` refuses**: a user data over the 500 KiB
  cap, and a keypair that does not exist — the second boots a machine nobody can
  log into while the API states a key is attached.
- **A 200 whose write was lost**: `UpdateVm` took no per-target lock, so a
  concurrent start overwrote what it had just reported as saved, and Terraform
  re-proposed the same change on every plan.
- **A Subnet could land in a Net that was already deleted**, leaving a bridge on
  the host under nothing, and a `terraform apply` creating ten subnets
  serialised them behind each other because the addressing lock was held across
  the runtime call.
- **A stopped Vm outside a Subnet lost its private address.** Outscale keeps it
  until the machine is terminated.
- **A request over 1 MiB was truncated before the handler saw it**, and came back
  as a syntax error about a document the client had sent whole.
- **`DryRun: false`, a legitimate request, failed this project's own
  conformance gate.**

### Added

- **`docs/limits.md` says what `DryRun` does here**: it is answered before any
  handler runs, so nothing happens — the half that matters for a host — and it
  does not validate, so a dry run of a malformed request answers 200 here and
  400 upstream. The code cited that section for two releases before it existed.
- **The conformance suites drive what they had never driven**: a filter, a
  `DryRun`, and a real SSH key whose fingerprint is checked against the value
  `ssh-keygen` prints. Every defect above lived in the gap between the suite and
  the claim.

## [0.3.1]

### Fixed

- **The Outscale conformance suite measured whatever answered on 4599**,
  whatever port it was asked to drive: `oapi-cli` lets the environment win over
  `--config`, and the credentials file pinned `OSC_ENDPOINT_API`. The suite's
  own endpoint argument was inert, so a run could report on a different server
  than the one under test — or on nothing. The guard that refuses to run when
  that variable is unset existed and was called only by the Scaleway suite.
- **`scw instance server update <server> volumes.0.id=<another server's root>`
  answered 200** and moved the volume: both servers then listed it, and the
  patched server's own root was silently detached. The ownership check lived in
  the shared layer and one of its three callers discarded the verdict.
- **A create whose resource disappeared mid-boot left a machine running** with
  a runtime configured — invisible to the control plane, so nothing would ever
  stop it. Reachable through `PUT /_feint/state`, which the snapshot format
  documents as a supported path.
- **`feint coverage` gave four snapshot read operations a reason that describes
  a create**, which is the "true of the family, false of the member" defect the
  reasons exist to prevent.

### Added

- **`TestEveryCitedTestExists` indexes citations by package.** A comment naming
  a test that lives in another package is accepted only when it says which one;
  a homonym elsewhere no longer satisfies a citation pointing at nothing. It
  also joins comment lines before matching, so a citation split over two lines
  is seen.

### Changed

- **The release preflight derives the version from the commits again.** It
  reported "commitizen is not installed" on the machine that cuts releases,
  where the commitizen pre-commit hook runs on every commit from an environment
  that is not on the `PATH`; v0.3.0 was therefore tagged on a number derived by
  hand. It now runs commitizen through `uvx`, pinned to the version the hook
  uses. Checked after the fact: the commits did imply 0.3.0.

## [0.3.0]

### Changed

- **`Declined()` carries a reason for every refusal**, and the coverage report
  prints it. A refusal used to be a bare operation name whose justification lived
  in a comment only a reader of the code ever saw, which made "not triaged yet"
  and "out of scope" indistinguishable from the outside. They are different
  answers and only one is a refusal. Breaking for anyone implementing
  `emulator.Pack`: `Declined() []string` becomes `Declined() []Decline`.
- **A refusal without a usable reason stops the server**, rather than being
  reported later. Empty strings, `TODO`, `n/a`, a reason under five words, and a
  reason that is only the operation name restated are all refused at start-up
  with exit code 1. The gate exists to make untriaged surface visible; a
  placeholder reason is how it stops working.

### Added

- **The upstream surface of all three providers is triaged.** Exoscale goes from
  358 operations nobody had decided on to 110, Outscale from 199 to 96, and
  Scaleway's `iam` and `marketplace` come under the drift gate — served and
  unmeasured is the least defensible state a route can be in. Each refusal is
  written by name, grouped by family, with the measurement that justifies it: the
  emulator authenticates nothing, so IAM is refused; `ReadQuotas` is read only by
  data sources, so refusing it breaks no `apply`.

### Fixed

Defects found by auditing whole packs rather than diffs, all of them on paths no
conformance client walks:

- **Detaching an IP did nothing** (Scaleway). `PATCH /ips/{id}` with
  `{"server": null}` was indistinguishable from a request that did not mention
  the field, so the address stayed attached while the API answered success.
- **A server's volumes could not be attached or detached** (Scaleway), and a
  deleted or terminated server did not release its addresses.
- **Two concurrent creates could receive the same address** (Outscale). Twelve
  parallel creates handed one address to two machines; with a runtime, that is
  two containers configured with the same static IP.
- **A Subnet deleted under the machines placed in it** (Outscale), and with a
  runtime it tore down the backing network under attached machines.
- **A create that failed left machines running** (Outscale): fourteen machines
  asked for in a subnet holding eleven answered an error and kept the eleven,
  which no client tracks.
- **`DryRun` was declared on twenty actions and read by none.** `CreateSubnet
  --dry-run` created a bridge on the operator's host and `DeleteVms --dry-run`
  destroyed the machine. It is now answered at the mount point, before any
  handler runs.
- **A keypair accepted anything**, including a multi-line value that cloud-init
  refuses later — the machine booted holding the wrong bytes and refused every
  login.
- **`terraform destroy` failed for good on a server with `additional_volume_ids`**
  (Scaleway). Terminate did not detach its volumes, so the disk went on naming a
  server that answered 404 and every retry hit "volume is still attached". The
  provider walks terminate, not delete, and only delete released anything.
- **Three doors attached a volume and one asked whether it was free** (Scaleway):
  a create or an update naming another server's root volume moved it, and both
  servers then listed it.
- **Creating a server took an address off a live machine** without withdrawing
  it, so under a runtime two machines claimed the same address.
- **`scw instance ip delete <address>`** answered success and kept the address.
- **`precondition failed:` printed with nothing after the colon**: the token the
  pack emitted was not one of the three the SDK renders.
- **`scw instance volume list name=vol`** came back empty against a volume called
  `myvolume`: the SDK documents that filter as a substring, with that example.

### Added

- **`TestEveryCitedTestExists`** walks every comment in the repository and fails
  when it cites a test that does not exist. Three audits in a row found a fix
  whose comment named the test that would fail without it, when that test had
  never been written — including in the commit that invoked the rule while
  breaking it. A rule written down three times and broken three times needed a
  check rather than a fourth restatement.
- **The Scaleway upstream surface is fully triaged**: 0 operations left
  undecided across instance, vpc, ipam, iam and marketplace.
- **The conformance fixture walks volumes and addresses**: attach, refuse the
  delete under a server, detach, delete; then resolve and delete an IP by its
  address; and a Terraform apply/destroy over `additional_volume_ids`. Every
  defect the audits found lived in the gap between that fixture and what the
  pack claimed. 68 of 91 routes are now proven by a real client, up from 64.

### Limits that moved

- `DryRun` is honoured on every served Outscale action, but it does **not**
  validate: the answer is issued before the handler runs, so a dry run of a
  malformed request still answers 200.
- `MaxVmsCount` is capped per create; unbounded, one request allocated a million
  resources and tried to start a million containers inside the handler.
- `SecurityGroupIds` is refused rather than silently dropped: telling a client
  its rules were applied when no rule exists anywhere is the one answer worse
  than a 400.

## [0.2.0]

### Added

- **`feint version --check`**, which asks GitHub whether a newer release exists
  and prints the command to install it, pinned to that version. Asked for rather
  than volunteered: nothing reaches the network unless the flag is typed, and
  `FEINT_NO_UPDATE_CHECK=1` refuses it outright. The binary never updates itself
  — telling beats rewriting something somebody verified.
- **Brand files**, in `docs/assets/brand/`, with `docs/brand.md` for what may be
  done with them. The wordmark is outlines rather than text, so the lockup
  renders the same on GitHub as in a slide, and the licence section states what
  the Apache licence does not cover: the name and the logo.

## [0.1.0]

The first published version. It carries three emulated providers on one port,
the lifecycle verbs, and the machinery that keeps the emulated surface measured
against the providers' own SDKs rather than followed by hand.

### Added

- **Three emulated providers on one port.** Scaleway (Instance, VPC, IPAM, IAM,
  marketplace), Outscale (Vms, keypairs, catalogue, Nets and Subnets) and
  Exoscale (instances, catalogue, SSH keys), served by one binary with no
  external Go dependency.
- **Lifecycle commands**: `start`, `stop`, `restart`, `wait`, `status`, `logs`.
  The binary backgrounds itself — no Docker, no `&` — which none of the
  comparable emulators can do.
- **`feint env <provider>`**, so `eval "$(feint env scaleway)"` is enough to
  point a real client at the emulator. Exports on stdout, caveats on stderr.
- **`feint probe`**: every mounted route driven from its provider's own API
  description, proving the protocol without a client installed. It never counts
  towards the conformance score.
- **`feint docs`**: the README's coverage tables, its startup banner and the
  contract policy table in `docs/limits.md` are generated from the committed
  artefacts, and `--check` fails when they drift.
- **Response validation against the providers' own OpenAPI documents**, with the
  strength of each contract recorded rather than assumed.
- **Real machines behind the API** through Incus, with an addressing plane that
  is arithmetic: masks bounded, containment and overlap enforced, address counts
  computed, and a VM placed on a subnet carrying the address the API published.
- **Conformance suites driving the real clients**: `scw`, `oapi-cli`, `exo`,
  Terraform and OpenTofu.
- **`feint doctor`**: the host diagnostics that encode the traps this project
  already paid for — the port, the Incus version ACLs need, what `--vm auto`
  would actually pick here, the clients on PATH, and the `ProxyJump` in
  `~/.ssh/config` that makes a working `sshd` look absent.
- **`feint snapshot save/load/list/rm`**, over two new admin routes
  (`GET` and `PUT /_feint/state`). A snapshot is exactly what `serve --state`
  writes, taken mid-run rather than at exit, so a fixture can be reached once and
  returned to as often as a test needs. Loading replaces the store: a fixture
  must not depend on what the session did before it.
- **A generated route reference** (`docs/routes.md`) and a **generated client
  version matrix** in the README, both under `feint docs --check`. The first is
  written by the binary that mounts the routes; the second is read from the
  workflow that installs the clients.

### Security

- **`serve` binds `127.0.0.1` by default**, where it previously bound every
  interface. This emulator accepts every credential without checking one and,
  with `--vm`, starts containers with the operator's privileges; the old default
  offered that to whatever network the machine was on. `--addr` still exposes it
  for anyone who decides to.
- **The conformance suites refuse to run a client that is not pointed at a local
  emulator** (`tools/conformance/guard.sh`). Every official client falls back to
  stored credentials when the environment says nothing, so an empty environment
  has to fail loudly rather than reach a paying account.
- **`PUT /_feint/state` bounds the body it reads** (64 MiB). It decoded whatever
  arrived, which made an oversized request a crash — the class of defect
  `SECURITY.md` declares in scope.
- **DNS rebinding is refused.** Binding to the loopback stops a network, not a
  browser: a page the operator visits can resolve its own name to `127.0.0.1`
  and drive the emulator from there — reading and replacing its state, and with
  `--vm`, starting containers. An audit reached `/_feint/health` with
  `Host: evil.example` and got a 200. When the listen address is a loopback one,
  requests whose `Host` names anything else are now refused; `--addr` on any
  other address turns the check off, because that exposure was asked for.

### Fixed

- **The OVN mode could not create a single network on a fresh host.** The driver
  built its uplink without delegating any route, so Incus refused every network
  whose block fell outside the uplink's own /24 — which is every block, since a
  client picks its address plan from what it is testing. It now delegates each
  block as the network is created, one at a time: delegating a whole range up
  front turns into real host routes and collides with whatever already lives
  there. Invisible on a machine whose uplink predated the check; found by
  installing on a clean one and asking for one subnet.
- **A delete racing a boot could resurrect the machine.** Write-backs after a
  runtime call go through `store.Commit`, which refuses to re-insert a resource
  deleted meanwhile.
- **A machine that failed to start reported `running`.** It now reaches the
  provider's own failed state, while the documented no-runtime mode still reaches
  running.
- **A page number nobody sends panicked every list route**, taking the response
  with it.
- **Outscale's `SubnetId` was accepted and dropped**, so a Vm reported success
  and went nowhere.
- **`stop_in_place` answered `stopped`, a state no client asked for.** The SDK
  declares `stopped in place` beside `stopped`, and the Terraform provider polls
  for the exact one its `state = "standby"` requested: the plan failed with
  "expected state stopped in place but found stopped". Neither `scw` nor the
  conformance suite exercises standby, which is why a real provider found it
  first.
- **`per_page` was ignored on every `instance/v1` list route.** That product
  spells the page size `per_page`; the newer ones (`vpc`, `ipam`) spell it
  `page_size`, and the shared helper read only the second. Every instance list
  served fifty items whatever the client asked for. Both spellings are read now.
- **`ListServers` ignored `state`, `tags` and `commercial_type`.** A filter that
  is dropped is worse than one that is refused: `state=running` returned every
  stopped server there was, in an answer shaped exactly like the right one.
- **`UpdateServer` dropped `commercial_type` and `volumes`.** Retyping a server
  answered 200, changed nothing, and left Terraform asking for the same change on
  every plan. Both are applied now, with the restriction the SDK documents —
  a type cannot change while the server runs — and a volume named by an id that
  does not exist is refused rather than silently ignored.
- **Fields a client sends that no handler reads now fail the conformance run.**
  The emulator had been recording them at `/_feint/conformance` all along, and
  nothing looked: it listed `commercial_type` and `volumes` under
  `unread_request_fields` run after run while Terraform looped. An introspection
  nobody gates on is a confession nobody hears.

[0.2.0]: https://github.com/stephrobert/feint/releases/tag/v0.2.0
[0.1.0]: https://github.com/stephrobert/feint/releases/tag/v0.1.0
