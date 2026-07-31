# Changelog

Notable changes, in the format of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
versioned according to [Semantic Versioning](https://semver.org/).

This file is read by the release workflow: the section matching a tag becomes the
body of its GitHub Release. An entry that is not here is an entry nobody
downloading a binary will ever see.

Two kinds of change deserve their own line whatever their size, because they are
what this project is judged on: **a response shape a client can observe**, and
**a limit that moved**. A refactor that changes neither belongs in `git log`.

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
