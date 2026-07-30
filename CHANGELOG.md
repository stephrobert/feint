# Changelog

Notable changes, in the format of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
versioned according to [Semantic Versioning](https://semver.org/).

This file is read by the release workflow: the section matching a tag becomes the
body of its GitHub Release. An entry that is not here is an entry nobody
downloading a binary will ever see.

Two kinds of change deserve their own line whatever their size, because they are
what this project is judged on: **a response shape a client can observe**, and
**a limit that moved**. A refactor that changes neither belongs in `git log`.

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
