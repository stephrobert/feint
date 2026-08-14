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

### Fixed

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
