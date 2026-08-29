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

- **A downstream project can pin the level of proof it depends on, and its own
  CI fails when feint stops delivering it: `feint evidence baseline` and
  `feint evidence verify`, CLI surface 21 (#488).** Not *"feint changed
  version"* — **"feint stopped proving what this project was relying on"**.
  #325 and #326 exist because a consumer discovered a change from the outside,
  after the fact.

  This is **not** `drift:check`, and the boundary is the design: that one
  watches the upstream SDK surface — *has the cloud moved?* — and this one
  watches this emulator's own level of evidence, from the point of view of
  somebody who is not in this repository — *has what I trusted moved?* A design
  folding one into the other answers one question twice and the other never.

  **Only what is proven is pinned.** An axis whose verdict is the absence of
  proof — `false`, `none`, `unchecked`, `unobserved` — is dropped at capture,
  because pinning it turns every later improvement into a regression. Measured:
  the first version of this pinned them, and run against `v0.11.0` it reported
  `osc/Client.AcceptNetPeering behaviour: false → true` as a fall.

  **Claims are meant to be withdrawn here.** #475, #481 and #483 are each *"this
  was claimed and should not have been"*, and a baseline that only grows would
  have stopped all three. `--accepted` carries a withdrawal **with its reason**,
  on the model `corpus/accepted.json` already uses; an entry with no reason is
  refused rather than honoured, because the whole value of the file is that a
  withdrawal carries why.

  **A baseline from a partial run is worse than none**, so a record earned with
  no machine runtime is refused at capture: it pins `dataplane: false` on every
  operation that would have earned it, and the consumer is then told nothing
  regressed on the day it did.

  `mise run evidence:verify` holds the tree against the previous release. Run
  today against `v0.11.0`: 364 operations pinned on seven axes, **every level
  still delivered**, which is 0.12.0's release invariant measured by an
  instrument rather than by hand.


- **The operator declares which projects the emulated account holds:
  `cloud.projects` in `feint.yaml`, `serve --projects`, and the CLI surface
  moves to 20 (#572).** Until now this emulator held one project, named
  `default`, and a stack whose `project_name` was its own production project
  died on the Terraform provider's `FindExact` after a truthful empty list —
  the obstacle for exactly the person #372 exists to serve, a platform team
  pointing an existing stack at the emulator. `data "scaleway_account_project"`
  now resolves a declared name, and `GetProject` reads it back as itself
  instead of answering `default` for everything.

  The list still **filters**. A name nobody declared answers an empty list, and
  that is the whole design: the cheap fix was to answer that a project exists
  because somebody asked for it, which is the class #83 measured and closed on
  all three packs — an identifier no catalogue held, silently substituted, and
  a green run that meant nothing. The inventory stays something the operator
  stated.

  Identifiers are derived from the name and never minted per run, for the
  reason the project's `created_at` is fixed: a value that moved between two
  reads is a permanent Terraform diff. `default` keeps the identifier every
  other product of this pack already scopes its answers to, so a declaration
  that omits the field — every declaration written before today — gets exactly
  the account it had.


- **A word for the shape the vocabulary could not describe:
  `capabilities.firewall_public_when_joined`, and `/_feint/health` moves to
  schema 7 (#548).** A consumer holding `capabilities.firewall: true`,
  `enforced.firewall` and `firewall_public_only: false` concluded that the one
  uncovered case was the machine with no private network. It was not, and the
  fix below is why the field exists: a machine that *does* join an emulated
  network now ends up with its published address on the interface that wears
  its rule sets, and that is a different claim from the one version 5 added.
  Both are true at once, and both are needed: the older field is still `false`,
  because a machine whose only interface is routed has nowhere to move an
  address to. The new one is a claim about the runtime and not about every
  pack — a pack that names no emulated network for its public addresses keeps
  them on a routed NIC, which Exoscale does on purpose.

- **The example stacks are applied to real machines every night, and the gate
  that does it says how many times (#504).** `conformance:functional` is the
  only thing here that applies `examples/stacks/` against a runtime, and
  nothing in CI played it: no leg of `runtime-proof.yml` applies a stack, and
  `mise run conformance` skips the stack suites without one. It is what
  surfaced the isolation-detach race that cost three pull requests (#577,
  #578), on a defect older than the whole range bisected. It now runs as a job
  of its own on that workflow — a job rather than a step, because the incus-ovn
  leg's ssh suites were red on four of the last seven scheduled nights and each
  of them leaves rule sets and networks `feint stop` does not sweep, so a step
  there would queue behind a red and then meet a doorstep it could not pass.
  **Three passes**, and the number is an arbitrage: the defect class struck 9
  times in 13 runs, so one pass calls it absent 31% of the time, and three bring
  that to 3%, at 295 s a pass.

- **The Account product's projects, so a third-party VPC stack reaches a VPC
  path at all: `account/v3/ProjectAPI.ListProjects` and
  `account/v3/ProjectAPI.GetProject` (#372).** `data "scaleway_account_project"`
  is evaluated ahead of every resource in a Terraform graph, and it answered
  501: two published modules that walk the VPC surface died in two exchanges
  each, before a single VPC, ACL, private network, gateway or IPAM address was
  attempted. Both routes are mounted because the provider's own read calls both
  — ListProjects when the configuration names the project, then always
  GetProject on the id it resolved. The whole `account` product joined the drift
  gate with them: twelve operations, two served, ten declined with a reason.

  The list answers one project, named `default`, and filters `name` and
  `project_ids` honestly — a name this emulator does not carry answers an empty
  list. The read echoes **any** identifier, which is
  [limits.md](docs/limits.md)'s "identifiers are not checked against anything"
  applied to the value a stack most often carries over from production. The cost
  is stated there too: a stack whose `project_name` is not `default` fails on
  the provider's own `FindExact`.

- **A capability matrix owns every sentence that claims a client for a
  provider, and `docs:check` reads the pages back (#592).** `README.md:41` said
  *"Run your Terraform against Scaleway, Outscale or Exoscale"* while
  `docs/confidence.md` said the opposite and `feint up` had been refusing
  `iac.engine: terraform` for that pack since #525 landed on 2026-08-26. Every
  doc gate stayed green throughout, and none of them was wrong: they compare
  **values** an artefact also holds, and a claim about capability is not one.
  The matrix (`internal/cli/capability.go`) is provider × client × mode ×
  support × **proof** × reason, and the proof column is what makes it more than
  a table: a `supported` row is resolved against the conformance workflow that
  drives that pair, a `refused` row against the pack's own `VetoEngine` — the
  code `up` and `down` consult — and both directions are checked, so a veto
  nobody wrote down and a row nothing proves are equally refused. The README's
  promise is generated from it in both languages, and every generated block of
  the front pages is read back: a sentence naming a refused pair passes only if
  it also names the upstream issue that would change it. Exit code 2, like the
  rest of the chain.

- **The Quick Start is four commands a reader can copy, on a stack short enough
  to read whole (#593).** It taught the 0.10 sequence — `feint start`, `eval
  "$(feint env scaleway)"`, `terraform apply` — with no directory, no `main.tf`
  and no provider block, under an `Apply complete! Resources: 5 added` that was
  not reachable from it. `examples/quickstart/scaleway` and
  `examples/quickstart/outscale` are the first example now: a provider, an
  address, one machine, 34 and 43 lines of Terraform, `runtime: mode: off`.
  `examples/stacks/` is unchanged and keeps its job — it is the qualification
  stack that found #249 and #250, and it was being asked to be a first read as
  well. The apply line is derived from the configuration and
  `tools/conformance/quickstart.sh` lifts it out of the README and requires the
  run to print it, so the output shown is the output produced. The suite runs
  `feint up`, an empty second plan and `feint down` on both examples, on the
  terraform and opentofu legs, and the quickstart directory joins the population
  `feint docs --check` already judges: applied by CI or declared with a reason,
  and pinning the provider that answered.

- **The generated blocks that carry prose are rendered per locale (#591).** The
  French README opened with an English Quick Start — "On your machine", "In CI,
  or anywhere Docker runs" — because the generator injected one block into both
  pages on the rule that a command needs no translation. True of the commands,
  and they are still shared along with the version, the image and the
  repository; the sentences around them are written twice.

### Fixed

- **A single `feint docs` run that changed two sections of a README kept only
  one of them.** The target was written from a copy spliced at the top of the
  run, after the helpers that re-read the same file and splice into what they
  find — so it put their sections back and reported success, and the only
  symptom was `docs --check` still red after a regeneration that had said
  `README.md updated`. Measured while adding the promise block.

- **A server created with its public IP no longer keeps an unfiltered
  interface beside its filtered one (#548).** Created *with* an `ip_id`, a
  Scaleway server boots carrying only that address, so the driver gives it a
  routed NIC (#202) — an interface Incus accepts no security option on at all
  (#337). The private NIC arrived afterwards, took the rule set, and left the
  published address on the bare one: measured 2026-08-27 on
  `examples/stacks/scaleway` and reproduced from the API alone on 2026-08-28 in
  **both** driver modes, a port the group's `drop` default never opened
  answered from the station, with a listener proved inside the machine and a
  bare port as the negative control.

  The address moves now. `RouteAddress` releases it from the routed device
  without removing the device — the two other remedies were tried and refused,
  and `docs/limits.md` keeps both: the uplink cannot take the `/32` while the
  routed NIC owns the host route, and removing the device unmasks the
  profile's `eth0` on the operator's own bridge — then hands it to the
  interface the pack named, which wears the rule sets. After it, in both modes:
  443 open because a rule opens it, the port no rule names closed with its
  listener still running, and the bare port as the control. The routed device
  stays on the instance with no address, and a routed NIC that carries nothing
  is no longer reported as an escape.

- **What a machine answers on is read, not guessed, and a restarted machine
  gets its interface back (#548).** `Inspect` used to answer one address — the
  first of the lowest-named interface — and the layer above then decided what
  *kind* of address that was from the pack's declared public block. The two
  agreed only while a routed NIC sorted before a managed one, which is exactly
  what the move above ends: after it the runtime answers
  `{"eth0":[],"eth1":["10.199.0.2","203.0.113.2"]}`, and the old reading would
  have published the private address where it published the public one — for
  three packs at once, since `Binding` is the shared layer and the Exoscale
  pack reads it. The driver reports every address it saw and settles nothing;
  `Reconciler.PublicAddressOf` and `PrivateAddressOf` pick out of that set by
  the block, which is the pack's own declaration. Nothing a client sees
  changes, and an entry a restored snapshot put there that is not an address is
  now dropped rather than published — only the public half ever went through a
  parser.

  The restart half was measured the same day and had to be fixed for the move
  to survive one: a NIC attached to a running machine is configured inside the
  guest by the driver, nothing in the guest remembers that across a reboot, and
  the machine came back with no address on that interface at all — the driver's
  own ninety-second wait giving up on a lease nobody offers, in both modes.
  While the published address rode a routed NIC of its own that cost only the
  routes to the peered subnets (#549); once it lives on that interface it costs
  the address itself. The start path restores what the device reserves before
  it waits.

  And the record a boot leaves is re-read at the end of the replay, because it
  was written before the replay installed the addresses the plan promised: the
  same rebooted machine recorded `10.199.0.2` alone while the station reached
  203.0.113.2 on it in the same pass. The pack's own bookkeeping hook moved
  with that read, from straight after the start to just after it — it ran early
  by exactly those addresses, and the Outscale test that keeps a stopped Vm's
  private address is what measured it.

- **A graceful exit gives back every piece of host plumbing no client can
  delete, and a run that leaks is the run that goes red (#521).** The incus-ovn
  leg of `runtime-proof.yml` failed at the doorstep of the next step on a
  GitHub runner nothing had ever touched: *a previous run left 0 machine(s) and
  2 network(s) on this host*, naming `feint-uplink`, `fnt-default` and the rule
  sets of two providers' default security groups. There was no previous run —
  the leg's own three ssh suites had made all four, each exiting 0.

  Four objects, one property: **no client call can remove any of them**, so
  leaving them measured nothing about the suites. `fnt-default` is the network
  a machine with no attachment of its own boots on, created here by an Outscale
  Vm outside a Net and owned by no resource; `feint-uplink` had been released
  since #521 and stayed because `fnt-default` still drew from it; the `scw-*`
  and `exo-*` rule sets belong to default security groups a client cannot
  delete, and Scaleway's is minted per run, so one host ACL accumulated per
  session. The exit now releases all four, in the only order the runtime
  accepts, and each release answers both questions — is it ours, and is
  anything still drawing from it — so a `feint stop` without `--cleanup` leaves
  the firewall on the machines it deliberately leaves running.

  The second half is the sentence. `feint clean --check --closing` and
  `tools/conformance/guard.sh leftovers-after` ask the identical question and
  name **this** run, `runtime-proof.yml` asks it after its own stop instead of
  letting the next step meet the residue, and the doorstep now names the rule
  sets it does *not* refuse on rather than passing over them in silence. The
  ledger's attribution column was corrected with them: a network is found by
  the label `Survey` reads, not by the `fnt-` prefix the column claimed — and
  the first network the failing leg reported, `feint-uplink`, carries no such
  prefix.

- **The stack gate's closing doorstep asks the question its own comment
  claimed, and the gate stops choosing its runtime in silence (#504).** It
  closed with `guard_leftovers_for "$RUNTIME" "the end of the run"` under a
  paragraph saying the doorstep was asked again on the way out; the guard arms
  that question on the literal `doorstep` alone, so a pass that leaked a
  machine or a network exited 0 and the next run met the refusal — the state
  #521 removed from `mise run conformance`, described here and never done. And
  it opened with `RUNTIME="${FEINT_FUNCTIONAL_RUNTIME:-incus-ovn}"`, so an
  exported `FEINT_VM` was ignored without a word: #574's line one directory
  over, on the one gate whose subject is the assertion the two modes disagree
  about. The resolution moved to `tools/runtime-mode.sh`, shared with
  `evidence:update`, whose unchanged tests are what prove the move changed
  nothing.

- **The Exoscale stack's two null outputs are not this emulator's (#520).** The
  filing deduced, without instrumenting it, that the pack's by-id GET raced its
  own async create-completion. Measured on 2026-08-28 against the pack
  directly: 100 back-to-back by-id reads of one balancer, 100 fresh
  create-then-read pairs, and 50 for the instance pool — **zero** empty names,
  addresses or sizes. There is no window: the create puts `name` and `ip` in
  the store *before* it writes an operation that already says `state: success`.
  The stack's third by-id door read back correctly in the same apply, so
  whatever produced the nulls tells the three apart and this pack does not.
  `docs/limits.md` carries the measurement and the disproof; what would lift
  the limit is upstream `terraform-provider-exoscale#573`, and no change to
  this pack would.

- **An Exoscale security group stops at the public interface, where the real
  cloud stops it (#574).** Exoscale states it in one sentence: "Security group
  rules do not apply to traffic inside private networks". Since `a344f8d`
  (#494/#475) this emulator applied them there. The emulated `default` group
  carries no ingress rule, so its rule set translates to a drop default, and
  the driver wrote it onto the membership NIC: **two instances of one private
  network could not reach each other**. Measured 2026-08-27 under `--vm incus`,
  one network and two instances created with `--public-ip none`, whose only
  interface is the membership one: `security.acls=exo-…` plus
  `security.acls.default.ingress.action=drop` on the NIC, 0/10 probes
  connected; with the fix, no `security.acls` key at all, 10/10. Under
  `--vm incus-ovn` the same wrong rule set was written and did *not* bite,
  because the sender's catch-all egress `allow` at priority 300 outranks the
  receiver's NIC default deny at 100/111 (#491) — so every green that segment
  produced under OVN rested on rule ordering, and the after-state is read off
  the NIC rather than off a connection succeeding.

  Where a security group applies is now a provider fact carried by the shared
  layer, not a rule of the layer: the pack declares it per interface on
  `machine.Attachment.Unfiltered`, `machine.GroupSync` reads it off the plan
  the pack already declares, and it reaches the driver on
  `FirewallBinding.Unfiltered`. Scaleway and Outscale declare nothing and keep
  filtering their private NICs, which their own network suites assert.
  Consequence a reader of the host will meet: an Exoscale instance with no
  public address and only private memberships carries **no rule set on any
  interface**, because it has no interface a group covers.

- **The three instruments that hid it, repaired in the same change (#574).**
  `.github/workflows/runtime-proof.yml` ran two of the three network suites and
  not `tools/conformance/exoscale/network.sh`, on either leg, so nothing in CI
  replayed it; `TestEveryNetworkSuiteIsReplayedByTheRuntimeProof` now derives
  the list from the suites on disk rather than from a list somebody remembers.
  `mise run evidence:update` overrode an exported `FEINT_VM` in silence on its
  runtime leg (`FEINT_VM="${FEINT_EVIDENCE_VM:-incus}"`), which manufactured
  two successive false attributions during the diagnosis; `tools/evidence/mode.sh`
  now honours the caller, refuses a disagreement and `--vm off` by name, and
  **announces the mode it runs** before anything starts. And the Exoscale
  network suite's own header claimed the pack "does not yet sync its security
  groups onto the machines" — true before `a344f8d`, read as a live fact for
  months after, and what steered every reader away from the firewall.

- **A server answer carries `bootscript` and `extra_networks` (#366), a
  catalogue image types `from_server` as a string (#367), and an attached public
  address publishes its gateway and its own tags (#368).** Four response shapes
  a client can observe, all four measured against a real fr-par account on
  2026-08-21 and again on 2026-08-24 and all four recorded in
  `corpus/accepted.json` until now: omitting a key is not the same answer as
  writing it null, and `feint corpus --check` went from 497 knowingly-accepted
  divergences to 425 with the thirty-seven exemptions these retire.

- **A block volume goes back to `available` when the server holding it goes, so
  `scw instance server delete` returns (#365).** Reachable since `sbs_volume`
  was honoured: the CLI polls the volume the server's own answer named until it
  settles, and nothing ever put the status back — measured at rc=124, never
  returning, with five identical polls in the client's trace. #365 itself stays
  open: making that volume the DEV1-S **default**, which is what the cloud does,
  moves every root disk out of `instance/v1` where the whole server-volume
  relationship is implemented, and that is a decision rather than a line.

## [0.11.0] - 2026-08-26

The release where the emulator stopped taking its own word for it. Four defects
found by hand on one evening — a security group served and enforced on nothing,
a balancing capability nobody materialised, a load balancer distributing
nothing, two machines given one address — were all four green: apply exit 0,
empty second plan, clean destroy, and three of them without a single ERROR
line. Nothing failed, so nothing could have caught them but a person reading the
host.

### Added

- **`/_feint/health` answers who delivers the balancer, not only whether the
  runtime could: `enforced.balancing`, schema_version 6 (#481).**
  `capabilities.balancing: true` was the truth about the runtime — OVN really
  does distribute — while a Scaleway stack's load balancer left no trace on the
  host, because that pack hands nothing to the runtime; both statements were
  true and a suite keyed on the capability, which is this project's own advice,
  would have asserted a distribution nobody promised. The `enforced` object
  gained `balancing` beside `firewall`: the packs that hand their load
  balancers to the runtime, today `["outscale"]`. A suite that wants to assert
  distribution gates on the conjunction of the two halves —
  `tools/conformance/outscale/balancer.sh` now does, and skips out loud when
  either half is absent. The Scaleway and Exoscale packs are deliberately not
  in the list: their balancers record configuration and forward nothing
  ([limits.md](docs/limits.md)), and an undeclared capability counting as
  absent is what keeps that honest.

- **`feint.yaml`, and the two verbs that read it: `feint up` and `feint down`
  (#189, #190).** The flags that decide what a colleague's emulator can do —
  which runtime, which provider, which contracts, which state to start from —
  lived in shell history and in a README paragraph, and a repository that needed
  a particular one had no way to say so. One declaration at the repository root
  now says it, and `feint up` reads it: it checks the host can deliver the
  runtime the file names and **refuses before anything starts** rather than
  downgrading, starts the emulator with the state and contracts it names,
  exports the pack's own client environment, runs the engine in the directory it
  names with its output passed straight through, waits for each `ready:`
  condition out loud and with a deadline, then prints the endpoints. `feint down`
  destroys and then stops, saying what it discards.

  The schema is closed: **a key it does not name is refused by name at load**,
  with the list of the keys that block does take, so a mistyped file fails at
  load rather than at the first surprising behaviour. What this file
  deliberately does not carry is refused with its reason rather than as an
  unknown key — `emulator.coverage`, `emulator.shapes`,
  `emulator.expose_to_network` and `proxy` each say where the thing a reader
  wanted actually lives. And a key the schema knows that no verb reads yet is
  said out loud at load, because a file that accepts everything and applies half
  of it is the lie this project exists to avoid.

  `up` composes the lifecycle rather than replacing it: it renders the
  declaration into the flags `feint start` already takes, so `status`, `logs`
  and `stop` know the instance it brought up. The CLI surface moves to version
  18 for the two verbs; nothing was removed and no exit code moved.

  Four traps this removes, each paid for by hand on 2026-08-24 applying the
  three example stacks: a local module that a copy of `*.tf` leaves behind (the
  engine runs in the declared directory, in place); an `endpoint` variable whose
  default points at a port nothing listens on (`iac.vars`, with
  `${feint.endpoint}` as the one substitution); an endpoint whose `/v2` path
  belongs inside the value for one provider and not the others (it comes from
  the pack's own `Env`, never from a field); and `FEINT_EXOSCALE_ALLOW_TERRAFORM`,
  which is read server-side so exporting it after the start does nothing
  (`emulator.env`, set before the spawn).

  Proved under a real machine runtime, not only as a control plane: the same
  Scaleway declaration with `--runtime incus-ovn` applied 50 resources, re-planned
  empty and destroyed, and the host carried what the API described — six running
  containers, three OVN networks on the blocks the stack declares, three rule sets
  marked `feint security group` across six interfaces, one isolation ACL per
  network, and nothing left after `feint down`. `runtime.images` is checked against
  the station before anything starts and never built, with three answers rather
  than two: present, absent from the warm-up set and refused with
  `feint images --only <name>`, or outside that set and announced as a first boot
  that will derive one. A runtime the host cannot deliver is refused before
  anything is started, naming the missing half and the three ways past it.

  Each example stack ships its declaration, and `tools/conformance/environment/`
  drives both verbs on a fixture repository with `--vm off`.
  [docs/environment.md](docs/environment.md) is the path from `git clone` to a
  `terraform apply` that passes, and its field reference is generated from the
  schema on the `feint docs --check` rail.

### Changed

- **The emulated Outscale public block is a `/28`, not a `/24`, and the
  conformance suite stopped spending four minutes releasing addresses.** The
  property the block holds — allocation refuses past the last address with a
  typed `9029`, a released address returns to the pool — does not depend on how
  many addresses there are, and the measured peak held at once across every
  suite, fixture and example stack here is two. Exhausting a `/24` cost 254
  `DeletePublicIp` calls at roughly 700 ms of client startup each. Measured on
  this station: the `octl` leg **368 s → 142 s**, the `fields` leg **435 s →
  209 s**, both still green with the omission gate armed.

  The allocator's bound is now derived from the prefix instead of written a
  second time, `ReadPublicIpRanges` publishes that same constant, and
  `TestTheAllocatorStopsWhereTheCatalogueSaysItDoes` walks the published range
  to its end and asserts the refusal lands on the address after it — so editing
  either side alone fails. Falsified three ways: publishing a different range,
  unpinning the bound from the prefix, and letting the pool hand one address
  twice; all three go red.

  Found on the way: `TestAnOutscaleBarrageLeavesTheStoreCoherent` claimed in a
  comment to catch a pool handing one address to two callers, and discarded the
  address it had just been given (`_ = ip`). It now records every address and
  checks for duplicates, and treats exhaustion as the expected answer it is.


- **The Outscale suite drives `octl`, and the client it drove for a year is
  archived** (#460). `outscale/oapi-cli` and `outscale/osc-cli` are both
  `archived: true` on the GitHub API, read-only, with "Deprecated Outscale CLI"
  in their own description; `outscale/octl` was pushed to the day this was
  measured. A suite driving an archived client proves the emulator against
  something Outscale no longer ships, so the north star moved with it.

  `tools/conformance/outscale/oapi-cli.sh` became
  `tools/conformance/outscale/octl.sh`, the matrix leg `oapi-cli` became `octl`,
  and every other Outscale script that drove a client moved with it:
  `faults.sh` (which runs on the `fields` leg and would otherwise have failed on
  a missing binary), `outscale/network.sh`, `outscale/balancer.sh`,
  `outscale/ssh.sh`, `parity.sh` and `tools/demo/osc.sh`.

  **Coverage was measured before and after, operation by operation, and nothing
  was lost.** The baseline was taken from the emulator's own
  `/_feint/conformance` on the unmodified suite and compared against the same
  reading of the migrated one: **77 distinct operations before, 77 after, none
  gained, none lost**, and the seven evidence axes byte-identical. The count of
  `prove_begin negative` sites is 13 before and 13 after, and `behaviour` 6 and
  6, so no assertion could have been dropped in the rewrite.

  The call totals moved from 700 to 677, and every one of the twelve differences
  has the same cause: **`oapi-cli` sent three requests for every 409 it met**,
  backing off between them, where `octl` sends one. `AcceptNetPeering` 7 → 3,
  `CreatePublicIp` 261 → 257, nine operations short by exactly two per refusal
  site. Two operations went up: `ReadNets` 2 → 4 for the new `-o raw` witness,
  `ReadPublicIps` 4 → 5 because the address inventory is now read three times.

  Three things about `octl` are load-bearing and each is stated in the suite:
  **the endpoint carries `/api/v1`** — the opposite of `oapi-cli`, because both
  `octl` and the Terraform provider ≥ 1.7 read `osc-sdk-go`, whose default
  endpoint template is `%s://api.%s.outscale.com/api/v1`; **`iaas api <Call>`
  and never an alias**, because `octl iaas net list` resolves to
  `octl iaas api ReadNets` and an alias is a convenience of the CLI where the
  API is what is measured; and **`-o raw` on every call**, because the default
  `-o json` reshapes the answer, unwrapping
  `{"Nets":[…],"ResponseContext":{…}}` to a bare list. One assertion at the top
  of the suite is the witness for the third: it fails if the raw form ever stops
  carrying the envelope, and it fails if `-o json` ever stops reshaping.

  `feint env outscale --client octl` prints the shape, and the `oapi-cli` note
  now says the client is archived and names the live replacement.
  `feint doctor` looks for `octl`, the Ansible role installs it against
  upstream's own checksums file (the AppImage's FUSE extraction, its APPDIR
  wrapper and its hand-pinned digest are all gone with it), and
  `feint proxy`'s client vocabulary gained `octl` — user agent measured through
  the proxy as `octl/v0.0.31`, and nothing else.

  **What it costs, stated rather than glossed.** `octl` spends ~700 ms starting
  up on every invocation with no network at all, against ~30 ms for the request,
  so the suite went from 177 s to 369 s: the bottleneck moved from a back-off on
  eleven calls to a fixed cost on all of them. The public-IP block is now filled
  through one `--waitfor` process (255 calls in 51 s, against 186 s as separate
  processes) rather than one process per address. Three filter arguments cannot
  be expressed as `octl` flags at all — a float array gets no flag generated, and
  the two date filters are registered as scalars while their setter asks for a
  slice — so they travel as `--payload`; `docs/limits.md` carries the
  measurement and why a mistyped payload cannot pass silently.

### Fixed

- **The two-tier load balancer distributes to the backends the runtime can
  take, withholds the others by name, and writes both halves where a reader
  can meet them (#483).** #457's whole-spec refusal was measured a second time
  and it cost more than it protected: the Outscale example stack — one backend
  on the balancer's own subnet, one on another, the ordinary public tier — left
  the host holding a registered balancer with **no backend and no port** while
  the API described two healthy ones; apply exit 0, second plan empty, zero
  ERROR lines, one WARN as the only trace, invisible to every gate. The driver
  now splits instead of refusing whole: in-block backends are distributed
  (`EnsureBalancer` returns a `BalancerDelivery`), out-of-block ones are
  withheld before the write — never handed to the daemon to die mid-update —
  and the pack records both lists on the balancer's `Runtime`
  (`balancer-distributed`, `balancer-undistributed`, readable through
  `/_feint/state`), keeping the record current on every register, unlink and
  listener change. The WARN stays a WARN because the limit is permanent on a
  working configuration, but the state now carries the fact instead of the log
  carrying it alone. What full delivery would take is named in
  [limits.md](docs/limits.md): Incus lifting its same-subnet restriction on
  `network load-balancer` targets, or one runtime network per Net — until
  then, this split is the whole truth the host can carry.

- **A load balancer in front of machines on another subnet stops claiming a
  dataplane it does not have, and an ordinary Terraform order stops failing**
  (#457). Replaying the fifteen third-party stacks of
  `examples/stacks/surveyed.md` under `--vm incus-ovn` found it, on
  `chimere-eu/ztiac`'s `two-tier-architecture`: a public balancer in front of
  private machines is the ordinary shape, and **both** of that stack's balancers
  hit it. The `apply` succeeded over 54 resources while the host held no
  balancer at all, and only the emulator's log knew — the same family as #454.

  **The cause was a guard applied to one end only.** `EnsureBalancer` checked
  the *listen* address against the network's block (the #315 guard) and merely
  `netip.ParseAddr`-ed each *target*, so a backend on another subnet passed
  every refusal and died inside the runtime, mid-write, leaving the balancer
  standing on the backends it already had.

  **Whether OVN could be made to serve the shape was measured before it was
  answered**, on Incus 7.2 with OVN on 2026-08-25, on two networks of the
  emulator's own making. A backend outside the balancer's network is refused,
  `Target address is not within the network subnet`; peering the two networks —
  both halves `CREATED` — does not relax it, word for word the same refusal;
  putting the balancer on the *backends'* network instead, which is the
  placement that would serve the shape, is refused on the other end,
  `Load balancer listen address "10.181.0.5/32" overlaps with another network or
  NIC`; and there is no key to declare the address with, an OVN network
  answering `Invalid option for network ... option "ipv4.routes"` because only a
  NIC carries `ipv4.routes.external` and a VIP has no NIC. The one placement the
  runtime accepts needs a listen address in no emulated block at all — exactly
  the address class #315 measured going dark in three minutes, and not the
  address the API published either.

  **So the limit moved rather than the dataplane.** The driver refuses the shape
  by name, whole, before anything is written; the pack reports it at WARN
  naming what the API still describes, because a limit that holds for the life
  of a stack reported at ERROR is how a log teaches people to skip its errors;
  and `capabilities.balancing` now states both of its bounds — the balancer's
  own address *and* its backends on its own network. `docs/limits.md` carries
  the four measurements. The `200` stands, as the real cloud's does.

  **And the ordinary Terraform order stops failing at all.** A balancer created
  before its machines was written with a port naming no backend, which the
  runtime refuses (`Missing VIP target(s)`), before repairing itself at the next
  register. The same body with **no port** is accepted — measured, along with
  the drain: a `PUT` carrying neither backend nor port stops the distribution,
  so a balancer that loses its last machine really does stop receiving. So the
  error is removed instead of being logged more quietly.

  **A limit moved on `/_feint/health` too:** `capabilities.balancing` can now go
  false during a run, on the rule #454 wrote for the firewall — one-way, and
  only a refusal *by the host* counts, never one this driver made on its own.

  Witnessed against the real runtime, reading `incus network load-balancer
  list`/`show` rather than the API's answer: the balancer appears on the host at
  create with 0 ports, the cross-subnet register leaves it untouched with one
  WARN and zero ERROR in the whole run, and — the positive control that makes
  the emptiness mean something — a same-subnet balancer does carry its backend
  and port. Falsified four more ways
  (`tools/falsify/specs/balancer-dataplane.json`, fourteen mutations in all):
  the target guard disarmed, the ports restored on a balancer with no backend,
  the capability withdrawal disarmed, and the limit levelled back to ERROR.

- **A security group carrying an ICMP rule with an IPv6 source keeps its
  firewall, and a rule set the host refuses stops reading as a success** (#454).
  Replaying the fifteen third-party stacks of `examples/stacks/surveyed.md`
  under `--vm incus-ovn` found it; `sergelogvinov/terraform-talos` ships
  `whitelist_admins = ["0.0.0.0/0", "::/0"]`, so a dual-stack admin whitelist is
  what a stack brings rather than an edge case.

  **Measured on this station, Incus 7.2 with OVN, on the campaign's own reduced
  witness: two groups identical but for one rule.** Before, group A described
  one rule and enforced one; group B described two and enforced **one**, its
  ICMP rule simply absent, with three
  `Cannot use IPv6 source addresses with "icmp4" protocol` refusals in the
  emulator's log and `capabilities.firewall: true` published the whole time.
  Worse than the count: the machine carrying group B held **no rule set at all**
  on its interface, because the pack returns before attaching when the write
  fails, so a deny-default group left its machine unfiltered. After, A is 1/1
  and B is 2/2, the second rule written `protocol: icmp6, source: ::/0`, both
  machines carrying their rule set, and no failure in the log.

  **The cause is one line and the all-or-nothing write beside it.**
  `toACLRule` chose the ACL protocol from the rule's own name
  (`case "icmp", "icmp4"`) and never read the address family, while
  `EnsureFirewall` writes the whole set in one PUT — deliberately, so a revoked
  rule disappears. One inexpressible rule therefore cost every rule of its
  group, and the API went on describing all of them: the function's own comment
  promised the opposite ("reports false for a rule the runtime cannot express,
  which the caller drops rather than approximating"), which is this
  repository's "a comment is not a control" pattern in the layer that acts on
  the operator's host.

  The family now comes from the rule's addresses: an IPv6 source or destination
  yields `icmp6`, an IPv4 one `icmp4`, and the `icmp6` / `icmpv6` / `ipv6-icmp`
  spellings are understood. `icmp` and `icmp4` both stay family-agnostic on
  purpose — `icmp4` is the runtime's wire name, not a claim any pack makes, and
  Scaleway's only value is `ICMP` — so a rule that fixes no family at all
  becomes **two** rules, one per family, because that is what "ICMP from
  anywhere" means. A rule no protocol expresses (both families in one rule, a
  name contradicting its addresses, an address that cannot be read) is dropped
  **alone** and reported at WARN naming the rule set and the rule, which is what
  "visibly absent" was supposed to mean.

  **And a limit moved, observable on `/_feint/health`:
  `capabilities.firewall` can now go false during a run.** A rule set the host
  refuses is the host answering that this process does not enforce what its API
  describes, so the claim is withdrawn and said in the log. It is one-way, and
  only a refusal *by the host* counts: a name this driver refuses itself never
  reached the daemon and arrives from a restorable snapshot, which would
  otherwise hand a crafted state file a switch on a published claim.

  Falsified nine ways (`tools/falsify/specs/icmp-family.json`), including the
  name-only mapping restored, the twin expansion removed, the withdrawal
  disarmed, and the own-guard refusal made to count as the host's; all nine go
  red. The unit witness drives `EnsureFirewall` through the injectable `runner`
  against a fake daemon that reproduces Incus's own refusal, so the test
  measures whether the write is *accepted* rather than how it is spelled.

- **A Net with three subnets or more peers under OVN, and a host already
  carrying the state that stopped it is reconciled** (#456). Replaying the
  fifteen third-party stacks under `--vm incus-ovn` cost the register its
  flagship result: `chimere-eu/ztiac` applied 80 of its 95 resources, wanted 15
  back on the second plan, and its `destroy` failed. Twelve lines of
  `Failed creating peer: More than one matching network peer was found` in one
  apply, naming six subnets, and the emulator carried on at ERROR — so the API
  kept describing a Net whose subnets route to each other while the runtime held
  no peering between them at all.

  **The cause is upstream, and it is not two declarations of the same pair.**
  Incus completes a peering by looking for a *pending* half aiming at the network
  the create lands on, and that lookup filters on the target network alone: there
  is no clause on which network holds the row (v7.2.0, `driver_ovn.go`,
  `PeerCreate`). Two pending halves aiming at one network therefore make **every**
  create on it fail, whichever pairs they belong to. Measured on the station with
  three real OVN networks and nothing concurrent: `peer create A B B` → pending,
  `peer create C B B` → pending, `peer create B A A` → the error above. `(A,B)`
  and `(C,B)` are different pairs, which is why a lock keyed by the pair would
  not have closed this, and why a Net needs three subnets to trip it while the
  two-subnet fixtures never did. The driver now excludes both **ends** of a pair,
  one lock per network taken in sorted order: two subnets of one Net can no
  longer declare halves aiming at the same network at once, and two pairs sharing
  no network still run in parallel, which a global lock would have cost.

  **A second upstream rule was measured on the way, and is fixed with it.**
  Deleting one peer blanks the target of every row aiming at the network the
  delete runs on, whatever pair it belongs to: on a three-network mesh,
  `peer delete B C` left **A** holding
  `{"name":"B","target_network":null,"status":"Errored"}`, and redeclaring that
  half answers `A peer for that name already exists` — which this driver
  tolerated as success, so a peering was reported applied and did not exist. A
  pair whose halves the runtime does not both call `Created` is now rebuilt from
  both ends, and every delete on that path asks the label `EnsureNetwork` wrote
  before touching a network, never the `fnt-` prefix anybody may type.

  **And "More than one matching network peer was found" is reconciled rather than
  reported**: the pending halves aiming at that network are cleared, only on
  networks carrying this emulator's label, and the create is re-issued once —
  because a host left in that state by a crashed run is repaired by nothing a
  user can type.

  Measured end to end against Incus 7.2 with OVN, on 2026-08-25: a Net whose
  three subnets are created concurrently gives three OVN networks, six peer rows,
  six `Created`, and no failure in the log; a host then broken by hand into
  exactly the state above and given a fourth subnet came back **12 rows of 12
  `Created`**, then 20 of 20 and 30 of 30 as two more subnets arrived; the six
  subnets and the Net then deleted with no 409, leaving no network and an empty
  `networks_peers`. `tools/falsify/specs/peering-pairs.json` proves the guards
  bite — four mutations, four red tests — and `docs/limits.md` carries both
  runtime rules with the commands that produced them, including the one case
  that is still not guaranteed in a single pass.

- **A sweep no longer traps the host it is cleaning, and `feint clean --force`
  frees a host already trapped** (#455). Fifteen third-party stacks replayed
  under `--vm incus-ovn` left two OVN networks, two rule sets and the uplink on
  a station where **no `incus` command could remove any of them**, and the
  eraser was the sweep rather than the next run.

  The upstream cause is Incus' own schema: of the three references
  `networks_peers` carries, only `target_network_id` has no cascade, so deleting
  a peering's *target* leaves the row behind holding an id that resolves to
  nothing. Every operation on the surviving network then fails on
  `Failed loading target network: Network not found` — the peer delete that
  would repair it included. `incus network peer edit` returns 0 and persists
  nothing; 7.3 changes none of it. Reproduced on this station, refusal by
  refusal, in `docs/limits.md`.

  Three things this emulator did turned that into permanence, and all three are
  now prevented rather than repaired: `Prune` stripped `feint-uplink`'s
  `ipv4.routes` on its way past, because the uplink carries the same label as
  everything else, and every management path of the networks drawing from it
  then failed validation; nothing detached `security.acls` before deleting a
  network the rule set holds and that holds the rule set; and nothing removed
  the half a peering leaves on its **surviving target**, which is what
  manufactures the dead id. The uplink now leaves the ordinary path and goes
  last with its routes intact, a rule set is detached before the delete and put
  back if the delete still refuses, and both halves of every peering go before
  the network does.

  For a station already carrying the state, **`feint clean --force`** removes
  such a row through `incus admin sql` — Incus' own supported mechanism, not an
  edit behind the daemon's back — after printing it whole so it can be put back.
  It removes a row **only when the network it belongs to carries the label this
  emulator wrote**: an operator's dangling row is the same table, the same shape
  and the same dead target, and a `--force` able to reach it would be a worse
  defect than the one it repairs.
  `TestForceLeavesAThirdPartysDanglingPeerAlone` is the witness, and
  `tools/falsify/specs/trapped-station.json` proves the guards bite — fifteen
  mutations, all red — and the pair was driven once against the real runtime,
  where the row planted on a bridge named `fnt-lab` was still there afterwards.

- **`feint clean --check` reports the states no sweep can leave, and exits 1 on
  them** (#455). It answered 0 in silence for the whole duration of the block
  above, because without `--doorstep` it only ever asked about orphaned DHCP
  services. It now names a peering row whose target no longer resolves, a
  network of the emulator's whose block the uplink no longer carries, and a rule
  set attached to a network already trapped by either.

  That third one is deliberately not the bare "a rule set is attached to a
  network": `IsolateNetwork` attaches one to every OVN network with a neighbour
  to keep out, so the bare form would refuse every healthy run — which is how
  #426's doorstep once fired on hosts nothing was going to fail on, and how a
  check gets disarmed.

- **`scw instance security-group list-default-rules` reaches a rule set instead
  of a 404, a rule answers `dest_ip_range`, and a private NIC answers the date
  it last changed** (#431, #432, #436). Three shapes a real `fr-par` account
  answered that no document could have found.

  `default` is a literal segment of the path Scaleway's own SDK builds, not an
  identifier, and this pack read it as one: the segment matched `{id}`, found no
  group, and answered 404 — so `instance/v1/API.ListDefaultSecurityGroupRules`
  read as *declined* in the coverage record while a live route answered the
  command wrong. It is now served, with the six account-wide outbound SMTP drops
  the recording measured, none of them editable, and the real CLI drives it in
  the conformance suite.

  `dest_ip_range` is on the wire on every security-group rule and is declared
  **neither** by Scaleway's published document **nor** by their own Go SDK. It
  is served as `null`, which is what the cloud answers, on all five operations
  that hand back a rule.

  A private NIC answered a creation date and no `modification_date`, on the
  create, the read and the list alike.

- **A load balancer answers the node it runs on, a backend answers the three
  defaults the cloud fills in, and a public gateway address answers a reverse**
  (#434, #435). Ninety of the divergences a real `fr-par` recording found had
  five causes between them, and each is a shape a client reads.

  A backend created without `send_proxy_v2`, `ssl_bridging` or `host` answered
  `null` on all three where the cloud answers `false`, `false` and `""`; the
  three fields appear on eleven operations, because a frontend nests a backend
  and an ACL nests a frontend. A balancer published an empty `instances` array
  where the cloud publishes one node — the recording reversed the argument that
  kept it empty, since the cloud's own node carries `ip_address: ""`, so what
  was being withheld was the shape and not an address. A gateway address
  answered `reverse: null` where the cloud always answers a name. And a list of
  `lb` or `vpc-gw` addresses that named no project was scoped to this pack's own
  default project, so a client that created an address under its own project got
  an empty page.

  Two fields are now **declined with their reason** instead of being answered
  hollow: a gateway's `version`, which is the version of software this emulator
  does not run, and the elements of `bastion_allowed_ips`, whose three writing
  operations were already declined — a filter no client can edit and nothing
  enforces is not a filter.

- **A zero the contract-driven probe can close is no longer filed as a decision
  nobody can act on** (#445). `feint coverage --evidence <record> --gaps` filed a
  zero as `declared` — "not work: no path exists to close this zero" — as soon as
  the record said no client drove the operation, on all seven axes at once, and
  printed the route's `Route.Undriven` reason beside every one of them.

  That reason is a sentence about clients: "`exo limits` reads the whole quota
  list, so the per-name read has no client path". It explains a zero on `driven`,
  `dataplane`, `behaviour` and `negative`, which no synthetic exchange can move.
  It explains nothing on `probed`, which the probe earns with no client
  whatsoever; nothing on `contract`, which any validated answer earns, the
  probe's included; and nothing on `shape`, which is resolved offline from the
  recordings catalogue and which no traffic moves either way.

  Each axis now declares what earns it, and two independent witnesses hold the
  declaration to it: one drives a marked and an unmarked exchange against a live
  emulator and reads the axes back, the other refuses a declaration the committed
  record contradicts. The record holds **16 operations no client drove that
  earned `probed`, 14 that earned `contract` and one that earned `shape`** —
  proof, from the artefact itself, that "no client reaches it" is not what keeps
  those three at zero.

  Measured on `coverage/evidence.json` as committed, with no axis moved and not
  one operation added to or removed from the queue: **38 lines across 22
  operations stop saying nobody can act**, and 64 more stop saying "the record
  does not say why". A sixth kind, `unvalidated`, names what those zeros are —
  an answer held to the provider's own API description, which needs neither a
  cloud account nor a client binary. #429 is the measurement behind it: 31
  Scaleway operations earned `contract` and 29 earned `probed` from a single fix
  to the contract extraction, no client and no pack code touched, and had they
  carried an `Undriven` reason this queue would have called all sixty "not work".

  `classifyGap` now returns the reason it classified on, so a `declared` line
  cannot print a sentence the classifier did not use.

- **An operation whose API description says it answers no body is now checked
  against exactly that, and thirty-one Scaleway operations stop reading
  "nobody looked"** (#429). Scaleway writes `204: {description: ''}` on 64 of
  its 370 documented operations, and it is the only provider here that does.
  That is the provider stating what its answer carries: nothing. Not simply
  "the DELETEs" either — four of its DELETEs declare a body and answer one, and
  twelve of the 64 are not DELETEs at all.

  The extraction recorded the statement as the *absence* of a response schema,
  which is also what a body it cannot name looks like, so two readers could only
  see silence. `internal/probe` skipped every such operation, and the contract
  check returned before recording anything as soon as a body was empty. A `scw
  instance server delete` answering precisely what Scaleway documents was filed
  as `unchecked` — the value that axis defines as *nobody has ever looked*.

  `noContent` now carries the declared status, and only where the document
  declares a 2xx with no content at all. The third case stays apart and stays
  unchecked: Exoscale's `list-events`, `get-sks-cluster-inspection` and
  `list-sks-cluster-deprecated-resources` declare a body this extraction cannot
  name, and reading their silence as "answers nothing" would be the axis
  inventing a verdict. What is validated is the document's own words in both
  directions — a body where none is declared, and a status the document does not
  name, which is what a generated SDK branches on to decide whether to unmarshal.

  Measured on two runs of `mise run conformance` differing only by this change,
  same host, same task: Scaleway goes from 141 to 170 of its 173 served
  operations on `probed`, and from 142 to **173 of 173** on `contract` — its
  second complete axis — while every other cell of the seven-axis table, on all
  three packs, is identical. The 31 operations that gained `contract` are
  exactly the 31 served operations whose document declares an empty answer, set
  for set. No pack code moved. The per-provider table is in `docs/routes.md`.

  Three stay at zero on `probed` and the cause is the probe's, not a pack's:
  `Get`, `Set` and `DeleteServerUserData` address a key by name, and nothing in a
  probe run produces one — a server the probe creates answers `{"user_data":[]}`,
  because the only operation that could put a key there is the one that needs it.
  A client invents that name; a probe may not.

- **Outscale bounds `ResultsPerPage` where its own API bounds it, and a page
  size outside 1 to 1000 is now refused** (#428). Twenty-one Read* request
  schemas of Outscale's published description carry the same sentence — "between
  `1` and `1000`, both included" — and this pack took any value, reading
  everything below one as "no limit". So `ResultsPerPage: 0`, the exact value the
  real API rejects, was answered with the whole inventory. A client that sends an
  out-of-range page size now gets a 400 naming the bound, as it would upstream.
  `ReadLoadBalancers` is deliberately not bounded: its schema declares no
  `ResultsPerPage` at all.

- **`ReadVmTypes` applies the filter a client sends it** (#428). `FiltersVmType`
  declares nine filters and this handler read none of them, so a client
  resolving its machine type by name was handed the whole catalogue with a 200 —
  indistinguishable from success for a client that then takes the first row.
  `VmTypeNames` is served; the eight that filter on hardware arithmetic are
  refused by name rather than ignored, which is what every other read of this
  pack already did.

- **An image's `FileLocation` is declined rather than invented** (#437). It is
  the object-storage URL an OMI's bytes live at upstream; this emulator copies no
  bytes and serves no object storage, so there is no location a client could
  fetch. Its neighbour `BlockDeviceMappings` is deliberately left undeclined: the
  same operation serves it when the client names a snapshot, and an
  operation-level decline true of one kind of object and false for the other is
  the shape #389 cost a release to understand.

- **Outscale is proven on every operation it serves: `shape` reaches 93 of 93**
  (#427). The last four were the Net peering family, and they had been declared
  unreachable twice — by #354 and again by this batch — for a reason that was
  never about the code: a peering needs two Nets of one's own, the quota is
  five, and four were held by the account's production infrastructure. A second,
  empty account made the same four operations trivial to record.

  The recording is folded into `shapes/outscale.json`; the transcript itself is
  **not** committed as a corpus, and #438 carries why: its replay cascades from
  one `CreateNet` conflict nobody has named, and writing 35 exemptions for an
  unnamed cause is what `corpus/accepted.json` exists to prevent.

- **A volume's `TaskId` is declined rather than invented, and the measurement is
  why** (#427, #437). A real Outscale account answers `TaskId` on a volume, and
  the naive reading of that is "the pack omits a field". It is not: of the eight
  volume records the recording holds, **seven carry no `TaskId` at all**, and the
  one that does is the volume with a resize in flight. It is a property of a
  volume that *has* a task, and this emulator has none — a resize completes
  inside the call. The `Iops` lesson of #389, a second time: a shape catalogue is
  the union of every field ever observed, and reading a union as a per-record
  requirement is how a defaulted value gets served to everybody.

- **A load balancer frontend answers `certificate`, and it is null** (#427). The
  real cloud carries the deprecated singular beside `certificate_ids` on every
  frontend and on the frontend an ACL embeds; this emulator omitted the key.
  Invisible to a client that decodes into a struct, visible to one that compares
  field sets — and it was the recording of a real LB-S that turned "we serve no
  certificates" from a silence into a stated answer. Null is the only value this
  emulator could ever hold there, and it is the value that was observed.

  Found by the omission gate of a conformance run, on eight operations at once,
  the moment the new shapes were committed. The gate did exactly what it is for.

- **The gateway offer was validated against a list nothing vouched for, and one
  recording proved it cost 143 replay findings** (#427). A sanitised transcript
  replaces every value a pack does not publish as its own, so a closed list the
  pack answers `400` against and does not vouch for makes its own recording
  unreplayable: the recorded `CreateGateway` was refused, and every read after
  it addressed a gateway that had never been created — 126 of the findings were
  `GetGateway` "omitting" fields of an object that did not exist.

  `PublicVocabulary` now reads `gatewayTypes` beside `knownZones` and
  `knownRegions`, from the map rather than from a copy. Its comment used to say
  the opposite in so many words — *"a commercial type … this emulator does not
  validate a request against"* — and `createGateway` validates one.
  `TestTheVocabularyVouchesForEveryListThePackValidatesAgainst` is written over
  the maps, so an offer arriving cannot make it pass while the vocabulary
  drifts, and four mutations in
  `tools/falsify/specs/vocabulary-covers-what-it-validates.json` bite.

- **The two `block/v1alpha1` creates answer the status the real cloud answers**
  (#427). `CreateVolume` and `CreateSnapshot` answered `201`; both answer `200`
  on a real `fr-par` account, measured on the wire on 2026-08-24. The third
  product measured this way after `vpc/v2` and `ipam/v1`, and each is claimed
  only for the product whose answer was seen.

- **Ten of the shape axis's own points were earned by an empty body, and six of
  them predate this batch** (#427). A `204` carries no body, so the field walk
  decoded `nil` and wrote one entry at the empty path with type `null`. That
  entry names no field and states no type of anything, but it makes
  `len(Fields)` non-zero — and two consumers branch on exactly that: the shape
  axis counts the operation *observed*, and `feint shapes --check` treats it as
  having a shape to compare.

  Measured on the committed `shapes/scaleway.json`: six operations carried it,
  every one of them a `DELETE`. The axis therefore published 134 where 128 had
  been observed. The count moved in the direction that reads like progress,
  which is what kept it invisible.

  A catalogue now holds no field at the root, on the way in and on the way out —
  the second half because a file committed before the rule must not go on being
  believed. `tools/falsify/specs/root-path-is-not-a-field.json` puts a phantom
  field back on each side, and both mutations bite.

- **Two Scaleway creates answer the status the real cloud answers, not the one
  this pack assumed** (#427). `vpc/v2/API.CreateRoute` and `ipam/v1/API.BookIP`
  answered `201` because every other create in the pack does; both answer `200`
  on a real `fr-par` account, measured on the wire on 2026-08-24 and recorded in
  `corpus/scaleway/scw-free-shapes.jsonl`.

  Neither `scw` nor the Terraform provider would ever have reported it — both
  accept any `2xx` and print no status — which is exactly how it could sit wrong
  indefinitely. `CreateRoute` had even been named in a test comment as the
  vpc/v2 create that was *not* measured and therefore kept the pack's `201`; the
  exception is retired by the measurement it asked for.

- **A run no longer leaves its networks on the host, and one that finds a
  previous run's is refused on the doorstep** (#426). `mise run evidence:update`
  could only be regenerated on a lucky run: leg 2 failed on any host with Incus,
  and the block it named differed every time.

  It was not a race. `machine.Driver` had `Attach` and no counterpart, so
  deleting a Scaleway private NIC only forgot it in the store — the device
  stayed on the container. `DeletePrivateNetwork` then got "The network is
  currently in use" from the runtime, logged it, and answered `204`. A *passing*
  run therefore left three bridges, three rule sets and three DHCP services
  holding their blocks, and the next run died on "Address already in use" for
  blocks the API had reported gone. Which of the three survived is Terraform's
  destroy scheduling, which is why the named block moved.

  What a client sees change:

  - `DELETE .../servers/{id}/private_nics/{nicID}` now detaches the interface
    from the machine runtime before it answers. It answered `204` and left the
    device attached.
  - `PUT /v2/private-network/{id}:detach` (Exoscale) does the same. That
    handler carried a comment saying the driver "deliberately has no hot-unplug
    ... the same window the Scaleway NIC has", which is how one defect lives in
    two packs: the capability now sits in the shared driver, so both close at
    once.
  - `DELETE .../private-networks/{pnID}` refuses with `precondition_failed`
    when the runtime will not release the network, instead of answering `204`
    and dropping the record. A network reported gone while its bridge holds the
    block is the same lie as a network created while nothing exists, and the
    create path already refused its half.

  A limit that moved: `feint clean --check --doorstep` refuses a host still
  holding a previous run's machines or networks, naming each and the one command
  that clears it. `--check` alone only ever asked about orphaned DHCP services,
  and answered "no DHCP service of this emulator's outlives its network" — exit
  0 — on a host holding three of this emulator's bridges.

  The question rides its own flag because the two have different safe moments.
  `guard_leftovers` is called before a run starts *and* twelve steps into one;
  a DHCP orphan is debris whenever it is found, but mid-run the machines and
  networks on the host belong to the emulator that is running. Asked at both,
  it failed a run for owning what it had just created.

### Added

- **The Exoscale stack has a suite that drives it**
  (`mise run conformance:exoscale-terraform`, `tools/conformance/exoscale/terraform.sh`).
  It applies `examples/stacks/exoscale` through the patched provider fork
  pinned in `docs/limits.md`, asserts an empty second plan and a clean destroy,
  and refuses **at the doorstep** when the fork is not built — printing the whole
  remedy, clone through build, so a reader never has to open the documentation to
  get past that line.

  **It is outside `mise run conformance` on purpose**, on the same terms as
  `conformance:ssh`: no gate here clones a third-party repository, which would
  put somebody else's availability in this pipeline, and a client this project
  patched is not the official client, so it could not count towards conformance
  anyway. Until now the procedure existed only as prose in `main.tf` and a
  hand-run noted in `docs/clients.md`.

  Run today, it fails on a named cause rather than on folklore: the fork corrects
  the v2 client and the stack now uses v3 resources (#448).

- **`feint clean --format json` records what a sweep found, one line per
  object** (#426). #316, #342, #375 and #386 each fixed one symptom of one
  family, and nobody could see the family because every sighting lived in the
  log of a failed run. Each line carries what the object is, how this run knows
  it is the emulator's, when it was seen, why it is still there and what was
  done about it, so `jq` answers which mechanism produces the waste.

  The `why` carries the value no return code reveals — a destruction that
  reported success and left the object standing — found by reading the host
  *after* the sweep rather than trusting the sweep's own counts. It is the
  shape that hid the defect above for four issues.

### Added

- **Every identifier an Outscale answer publishes now names an object a read
  answers for** (#389, #383, #378). One chain rather than three patches: the
  image catalogue is backed by snapshots `ReadSnapshots` really serves, and
  `CreateVms` cuts each machine a root BSU volume from the snapshot its image
  names.

  What a client sees change:

  - `ReadImages` answers `BlockDeviceMappings` on every catalogue image —
    `/dev/sda1`, its size, its type, `DeleteOnVmDeletion` and the `SnapshotId`
    it was cut from — where the list was empty. A stack that sizes its root
    device from the image read nothing.
  - `ReadSnapshots` answers the three snapshots behind the catalogue, and a
    volume can be cut from one.
  - `CreateVms`, `ReadVms` and `UpdateVm` answer the machine's root device,
    naming a volume `ReadVolumes` serves. It is how a stack finds the disk it
    must not delete, and it was an empty list.
  - `DeleteVms` destroys that volume and frees the ones the client linked, each
    by its own `DeleteOnVmDeletion` — which `ReadVolumes` now publishes and
    filters on per volume rather than as the constant `false`.
  - `CreateImage` refuses an `Iops` on a device mapping instead of storing one.

  **A limit moved**: an Outscale machine here owned no disk, and now owns one.
  "Nothing left behind" changes meaning by that much for every suite — the
  volume goes with the machine.

  The measurement that reversed a premise: `Iops` was defaulted to 100 on the
  ground that "the real cloud writes Iops on every image Bsu it returns". Of
  the 399 device mappings the recorded account answers, **396 carry no `Iops`
  key at all**, and the 3 that do are the 3 on a provisioned-IOPS volume type.
  `shapes/outscale.json` is a union of everything ever observed, not a
  per-element requirement. The field is declined with that measurement beside
  it, and the four `corpus/accepted.json` exemptions this chain retires are
  deleted.

- **The shape axis was saturated at its own ceiling, and 619 recorded exchanges
  fed nothing** (#407). `shape` read 52 of 370 operations, and its ceiling
  was **also 52** — per cloud to the unit: exoscale 14,
  outscale 23, scaleway 15; the published figures live in the generated
  table of docs/routes.md. It resolved coverage by walking `upstream.Reads`, a
  curated list of about sixty calls, so no amount of recording could move it.
  It nevertheless named 292 operations "no answer of the real cloud has been
  kept", including every one of the 619 exchanges this repository already holds
  under `corpus/`, which it never asked about. A control whose numerator cannot
  move is not a measurement, and this one had never been read since it was
  written.

  It now walks the whole catalogue — the traversal `observedFieldsByOperation`
  ten lines below it already used — and `mise run shapes:fold` folds every
  committed corpus into `shapes/`, offline, without an account. **`shape` goes
  from 52 to 134 of 370**, and the 52 it already had are all still there: it is
  the same set plus 82, not a new number. 80 of the 292 recording jobs were
  already paid for; the queue that remains is 212, and it is named by
  `feint coverage --evidence coverage/evidence.json --gaps --axis shape`.

  **A redaction erases a type rather than reporting one**, and folding blind
  would have written that into the artefact whose whole content is types: the
  recorder replaces a value with a string, so `osc/Client.ReadKeypairs.Keypairs`
  came back `array|string` and `LoadBalancers[].SecuredCookies` came back
  `bool|string`, on top of types a direct `--record` read had got right.
  Twenty-three (operation, field) pairs of the corpora carry a placeholder,
  seven of them over a non-string. `shape.IsRedacted` refuses them, and a path
  the sanitiser rewrote keys nothing at all.

  `shapes/` is **not** derived from `corpus/`, and the measurement is why: 13
  operations are recorded in `shapes/` and in no corpus — six of them served by
  no pack at all, which is the read list's "learning side", a shape known before
  a handler exists. Deriving would delete them. The fold is one-way.

- **`feint evidence --reshape`** (#407) recomputes only the shape axis of an
  existing record, offline, from the catalogues on disk. The shape axis is the
  one axis that is not a property of a run — `evidence` already discards what
  the server answered and re-derives it — so a catalogue that grew offline no
  longer costs two conformance legs, one of them on a host that can start
  machines, to publish. It refuses a record whose contracts or suites moved
  since it was written, and recomputes the column wholesale so a catalogue that
  lost evidence lowers the figure instead of leaving a high-water mark.

- **A score says where we stand; a queue says what to do next** (#408). `feint
  coverage --evidence <record> --gaps` lists, per cloud and per axis, the
  operations at zero **and the work each zero names**. That last half is the
  point: a zero does not mean one thing. An operation missing `shape` because no
  client ever drove it is a conformance-suite job; the same zero on one a client
  drives every run is a recording session against a real account; and an axis
  whose record says the operation *violated* its own contract is a defect in the
  pack. Three zeros, three different people — a queue that merges them hands one
  list of 158 names to all three.

  Four kinds, each derived from the record rather than from a name, a guess or a
  hand-kept list: `violating`, `unrecorded`, `undriven`, and `unproven` for what
  the record does not explain. The last one is named rather than folded into a
  neighbour, because a bucket that absorbs the unexplained is how a queue starts
  lying. The vocabulary travels inside `--format json`, so a consumer never has
  to open the source to learn what a kind means.

  Ordered by the work, then by name, and the order is declared rather than
  scored: a defect first because it is the only one here, then the recording
  that is one session from earned, then the suite that is upstream of most axes.
  **No target percentage anywhere** — a queue exists to be worked, not reached.

  Measured on the committed record the day it landed: Exoscale needs 111
  conformance suites, Scaleway 151 recordings. Two different jobs, which the
  score alone could never have said.

  `tools/falsify/specs/evidence-gaps.json`, six mutations, all red. One stayed
  green on the first run and named a weakness in the fixture rather than in the
  code: no operation of it was unclaimed by any pack, so the guard that skips
  such an operation could be removed with every assertion still passing.

- **The evidence axes are readable per provider by a command** (#402):
  `feint coverage --evidence coverage/evidence.json` prints, for each pack, the
  operations it serves and the count and percentage on each of the seven axes;
  `--axis <name>` lists the operations at zero on one of them, which is what
  turns a score into a work queue; `--format json` publishes the same numbers.
  Offline, from the committed record, no SDK checkout and no socket.

  It exists because the question was answered once by a throwaway script, and
  **that script was wrong twice before it was right**: it first looked for a key
  named `operation` inside each entry — the operation name is the map *key* — so
  all 370 operations fell into one bucket and it printed `scaleway: 370 served,
  93 % driven`. Right shape, right headers, plausible numbers, no relation to the
  record. The provider of an operation is therefore never inferred from its name:
  it is the pack that mounts a route declaring it, and a record naming an
  operation no pack serves is refused rather than filed somewhere plausible.

  The table is in [docs/routes.md](docs/routes.md), generated and held by
  `feint docs --check`, with one line per axis saying what earns it — including
  that an injected fault earns none of them, which is what the `negative` column
  is worth. `cliSurfaceVersion` moves 12 to 13: two additions to one existing
  verb, nothing removed, no exit code moved.

- **The `negative` evidence axis, measured again: 35 of 370 to 173 of 370**
  (#390), and the number is what came out rather than a figure anybody aimed at.
  Per provider: Scaleway 18 to 97 of 173, Outscale 11 to 66 of 93, Exoscale 6 to
  10 of 104. 138 operations gained it, none lost it, and 197 stay at zero —
  visibly, because an operation whose refusal nobody could record is not counted.
  Two identical runs produce the same 173, which is the property this number
  needed and `behaviour` does not have (#398).

- **The unhappy path is recorded, and the `negative` evidence axis moves for the
  first time by measuring rather than by asserting** (#390). Three corpora of
  refusals a real cloud actually answered — `corpus/scaleway/scw-refusals.jsonl`,
  `corpus/outscale/oapi-cli-refusals.jsonl`, `corpus/exoscale/exo-refusals.jsonl` —
  recorded from named accounts on 2026-08-21 through `feint proxy`, sanitised,
  committed, replayed by `feint corpus --check` on every pull request, and
  reissued against the run's own emulator by `tools/conformance/refusals.sh`.

  **No injected fault is in any of it, and that bound is what the number is
  worth.** `PUT /_feint/faults` can make any operation answer 403, and #26 made
  sure such an answer earns nothing: the axis can only be raised by observing
  refusals somebody's client really met. The definition now lives where the axis
  is computed (`internal/core/emulator/assert.go`): what counts as a *demanded*
  refusal, and why an injected one never will.

  An operation whose refusal nobody could record stays at zero, visibly, and
  three families are named in `corpus/README.md` with the reason: nothing was
  created, so no duplicate-name 409, no dependency 409 and no quota refusal is in
  here.

- **`feint replay --refusals-only`**: the flag reads a recording whole and sends
  nothing unless every exchange of it is a 4xx. It is what lets a corpus of
  refusals be replayed beside the other suites of a run, against the one emulator
  they share, instead of needing one of its own — a 4xx mutates nothing, and that
  is now read off the file rather than promised about it. CLI surface version 12.

- **A contract can now carry a field the provider's own document does not
  declare, and only with the recording that proves it** (#370, #371).
  `tools/contract/extract-openapi.py --recorded-fields` folds such a field into
  the schema it belongs to from a versioned YAML fragment, and copies the
  citation — corpus file, operation, path, date, reason — into
  `contracts/<provider>.json` under `recordedFields`. It exists because a
  published API description can be behind the API it describes, and Exoscale's
  is.

  **The point is the lock, not the door.** A contract is otherwise extracted and
  re-extracted by a pre-commit hook, so it cannot be hand-edited; a door into
  "what a response may contain" is exactly what rule 4 forbids leaving open. The
  extraction refuses a schema its documents do not define, and refuses a
  property the document *does* declare — upstream catching up retires the entry
  rather than leaving a citation nobody re-reads. Then
  `TestEveryRecordedFieldIsStillOnTheWire` replays the named recording on every
  run and fails an entry whose path it no longer carries. That is
  `corpus/accepted.json`'s own staleness rule pointed the other way: an
  exemption that excuses nothing is dead, and so is a citation that cites
  nothing.

- **The committed corpus is now replayed at the cloud, and catches the drift no
  SDK scan can see** (#359). `feint corpus --against-cloud --file <corpus>
  --endpoint <url>` reissues a committed recording at the provider it was
  recorded from. Same artefact, same comparator, opposite conclusion: `corpus
  --check` replays it against a fresh emulator and a divergence means *the
  emulator is wrong*; this replays it at the provider and a divergence means
  *the cloud has changed*. There is no second recorder and no second comparison
  to keep in step — `internal/replay` is called by both, and what the second
  direction adds is only what a real account demands.

  **This is what `internal/drift` cannot see.** The surface scan reads the
  providers' own generated SDKs and reports an operation that appeared or
  disappeared, exactly, because those SDKs are generated from their IDL. It sees
  that a method exists; it sees nothing of what the method answers. A status
  that moves from 200 to 201, a field that appears or goes, a list that
  reorders, a refusal that stops being one — the Go signature is identical
  before and after, the baseline stays green, and #270 found three of that
  family in one read of one private network.

  **Three verdicts, never blurred**: *the cloud answers differently* (exit 2),
  *the recording could not be reissued as recorded* (an instrument defect, never
  counted as a change), and *the call could not be made* (exit 1 — a 401, a 429,
  a 502, or a guard refusing). The middle one is not a courtesy: #73 found the
  proxy's own redaction manufacturing nine false divergences and #354 four more,
  three of which hid an entire lifecycle. So a request whose path or enumerated
  query value the sanitiser blanked is never reissued, one still carrying the
  recorder's `REDACTED` is never reissued, and a finding whose request still
  holds a value the sanitiser minted is attributed to the recording. **That last
  rule came from a measurement, not a worry**: dry-running
  `scaleway/terraform.jsonl` at the real account on 2026-08-21 produced 145
  findings, not one of them the cloud — the creates were refused, so every read
  that followed answered 404 and every recorded field read as absent.

  **It is not a gate and must not become one.** It needs a credential, it creates
  real objects, and its verdict depends on whose account ran it — three reasons
  where `conformance` has one. It runs on demand (`mise run corpus:cloud`) and on
  a schedule (`.github/workflows/corpus-cloud.yml`, the first of each month),
  opening a pull request when something moved, which is the shape `drift.yml`
  already has for the surface. **That workflow is red until the account holder
  adds `SCW_SECRET_KEY`, `SCW_ACCESS_KEY`, `SCW_DEFAULT_PROJECT_ID` and
  `SCW_DEFAULT_ORGANIZATION_ID`**, deliberately: a job that quietly did nothing
  without them would report success on every run and measure the provider on
  none, which is the SKIP that measures nothing this repository has shipped once
  and refuses to ship again.

  Measured against the real Scaleway account on 2026-08-21, the day both files
  were recorded: `scaleway/terraform.jsonl` compared 16 of 16 exchanges and
  `scaleway/scw-cli.jsonl` 42 of 58, **zero findings saying the cloud had
  moved**, eleven attributed to the recording (the blanked paths `corpus/README.md`
  already documents) and five to routes this emulator does not mount. Two facts
  worth not rediscovering came out of it: Scaleway accepts a private-network
  subnet inside `198.18.0.0/15`, the synthetic space the sanitiser mints, and it
  accepts the synthetic `ssh-ed25519` key whose material is all zeroes — either
  one refusing would have made a whole lifecycle unreplayable.

- **How a corpus ages is now measured rather than guessed** (#353's open
  question, answered). A run that finds the provider answering differently
  writes `cloud_moved_at` and `cloud_moved` back into that recording's entry in
  `corpus/accepted.json` (`--mark-stale`), and `corpus --check` then warns with a
  date somebody measured instead of the 180-day horizon somebody picked. It still
  warns and never fails, for the reason it always did: the file to change is the
  recording, not the emulator.
- **A corpus of a real Outscale account, machine included** (#354, #352, #353).
  `corpus/outscale/oapi-cli-lifecycle.jsonl` is 179 exchanges recorded on
  2026-08-21 against the account and region the owner named by hand, through
  `feint proxy --forward`: the catalogue reads a stack makes first, an imported
  keypair, a Net with a tag, a Subnet updated in place, two security groups with
  a rule each (one on `0.0.0.0/0`, one naming the other group), a route table
  linked to the subnet, an internet service and a default route, **a machine**
  created `BootOnCreation=false` and never booted, a public IP linked to it, a
  1 GiB volume attached and snapshotted, a second NIC, a NAT service, an
  internal load balancer, two deliberate refusals, and the teardown of all of it
  with every destruction proved by a read. Nothing was left behind: the
  inventory before and after is identical family by family.

  It is also the answer to a question `docs/proxy.md` had left open in writing —
  what a **valid** credential answers through the tunnel — because 4120 is the
  same code for an unknown key and for a wrong region and could not settle it.
  It answers 200, with real data, on reads and on writes.

- **The Outscale pack declares what a replay may compare** (#354). Five
  `ReplayInvariant`s: the VM type a create answers, the address range a Net and
  a Subnet answer, and **the order of a machine's security groups** on
  `UpdateVm` and on the reads that follow. The pack declared none before, and
  the consequence was the failure shape this repository files issues about —
  `feint corpus --check` printed "0 divergent finding(s)" over a run in which no
  value and no order of an Outscale answer had ever been compared. Its counters
  went from `values_checked=2, orders_checked=6`, all of them Scaleway's, to
  **5 and 56**.

  The first order it compared found a defect (#379), and it settles something
  that the obvious guard would have got backwards: **the cloud does not answer
  the order the client named.** The request sent web-then-db and the cloud
  answered db-then-web, so what a replay can hold the emulator to is the order
  the *cloud* answered.

- **The Outscale pack vouches for the closed lists it validates against**
  (#354). `PublicVocabulary` now publishes every region and subregion of the
  catalogue, the two flows of a security-group rule, the two kinds of load
  balancer and the four listener protocols — exactly the values a request is
  refused by name for. Without it the sanitiser replaced `cloudgouv-eu-west-1a`
  with a synthetic string, `knownSubregion` refused it (the #269 invariant doing
  its job) and `CreateSubnet` answered 400 where the cloud answered 200, taking
  the machine, the volume, the NIC, the public IP, the route table link, the NAT
  service and the load balancer with it. Falsified in
  `tools/falsify/specs/outscale-corpus.json`.

- **`--forward` can say where a terminated host actually goes** (#357). An entry
  of `feint proxy --forward` may now name its target — `--forward
  'api.scaleway.com=http://127.0.0.1:4599'` — so a client whose endpoint is
  compiled in is terminated, recorded, and re-originated to **the emulator**
  instead of to the real cloud. A host written without a target keeps sending it
  to the real one, so nothing changes for an existing caller, and the two forms
  mix in one run.

  This is the combination that could not be expressed before: `--forward`
  recorded a client nobody can redirect but sent it to the real cloud, and
  `--upstream` sent everything to one host but needed a redirectable client.
  Recording a compiled-in-endpoint client *against the emulator* took a user +
  mount + network namespace, a replaced `/etc/hosts` inside it, a listener on
  port 443 in that private stack and a second proxy stage — 89 lines of shell.
  It now takes two environment variables and one `=`. Proven with a real client
  on 2026-08-21: `terraform apply` of a `scaleway_object_bucket`, whose S3
  endpoint is compiled into the provider, recorded against a feint emulator with
  no namespace, no `/etc/hosts` edit and no privileged port.
  `tools/conformance/forward.sh` replays the mechanism on demand with `scw` and
  without an account — it points the CLI at `https://api.scaleway.test`, a
  reserved TLD that never resolves, so a run in which the proxy is *not* what
  carries the traffic fails with `no such host` instead of reaching a cloud.

  **This is not `--upstream` in disguise, and the difference is the record.**
  `--upstream` sends every request to one place regardless of what the client
  asked, which loses the very information a recording is for. Here the requested
  host is preserved in the transcript and only the socket moves. The outbound
  `Host` header is the one thing a mapped entry does move, and it has to: feint's
  own DNS-rebinding guard answers 403 to a `Host` it does not recognise, so
  forwarding `api.scaleway.com` verbatim to the emulator recorded a transcript of
  refusals — measured before the fix, on the very apply above. A bare entry is
  untouched, so a SigV4 signature over `Host` still verifies against the real
  host.

  **#336's four security requirements hold unchanged, and each is falsified again
  at this door** (`tools/falsify/specs/forward-proxy.json`, now 21 mutations):
  the redaction survives a mapped tunnel, `--expose-to-network` is still refused
  with `--forward`, the authority is still ephemeral and never installed, and
  `--forward '*=<target>'` is refused exactly as `--forward '*'` is — the
  wildcard is looked for in the host *after* the `=` is cut out, which is how a
  mapping could have turned the recorder into a wiretap without anybody noticing.
  A target names a socket and nothing else: a path, a query or user info is
  refused, because the proxy would then rewrite every request in a way its own
  transcript does not show. No new flag, so the frozen CLI surface (version 9)
  does not move.

  A host that is intercepted but not named is now diagnosed as the missing entry
  it is, in the form the flag takes, rather than as a bare connection failure —
  the case that costs an afternoon, since an API family can live on a different
  host than the main one (Outscale's managed-Kubernetes API does).

- **Measured: the Terraform Scaleway S3 client honours `HTTPS_PROXY`** (#346).
  This implements nothing; it answers the one question the object-storage
  refusal had been resting on. It does honour it. Measured on Linux on
  2026-08-21 — terraform 1.15.4, `scaleway/scaleway 2.81.0` — a
  `scaleway_object_bucket` apply emitted `CONNECT
  feint-346-measurement.s3.fr-par.scw.cloud:443` and its `CreateBucket` landed
  on a feint emulator, User-Agent `aws-sdk-go-v2/1.43.4 … api/s3#1.107.0
  terraform-provider-scaleway/2.81.0`. No account, no real endpoint, the public
  fake credentials.

  **The two negatives are told apart, because only one closes the door.** A
  control run against a proxy that does *not* name the S3 host recorded zero
  exchanges, terminated zero tunnels and refused twelve connections to that
  host, with terraform reporting `request send failed … Forbidden`: that is
  "arrived, and was refused". The other negative — "did not honour the proxy" —
  would have shown no `CONNECT` at all and a certificate complaint, which is
  what macOS produces and why this was measured on Linux.

  The consequence is written where the decision lives, `docs/limits.md`
  (number 7), with the transcript: the DNS/TLS redirect that #76 called the hard
  half now costs two process-scoped environment variables **on the exact client
  that blocked it**. Object storage stays refused, and the refusal now stands on
  its coverage argument alone — one product on one client, and an S3 surface
  nobody has costed — rather than on any part of the redirect being hard.
  Reopening it is its own issue, with its own numbers.

- **A Scaleway VPC's Network ACL is served, and the twenty other refusals of
  `vpc/v2` were measured rather than assumed** (#343). `vpc/v2/API.GetACL` and
  `vpc/v2/API.SetACL` are mounted at `GET` and `PUT
  /vpc/v2/regions/{region}/vpcs/{vpc_id}/acl-rules`, one rule set per address
  family. Scaleway's `vpc` product moves from 17 to 19 implemented operations
  and from 20 to 18 declined.

  **What decided the two is a recording, not the SDK's shape.** They were
  declined with the five ingress rules under one reason — *"a filter recorded
  but never applied is indistinguishable from protection"* — written before
  anything had measured who was calling. Driven through `feint proxy --record`
  on 2026-08-21 and ranked with `feint coverage --observed`, which is the first
  real use of that flag:

  - `scw vpc rule get` addresses `/vpcs/{id}/acl-rules` and took a **501**;
    the official Terraform provider 2.81.0 ships `scaleway_vpc_acl` as a
    resource and a data source; and a real third-party module,
    tf-scaleway-modules/terraform-scaleway-network @ 99f390bb, declares that
    resource in its own `complete` example. Served.
  - the five `*IngressRule` operations show **zero** observed calls: `scw` has
    no ingress-rule subcommand and no surveyed stack names
    `scaleway_vpc_ingress_rule`. Still declined, and their reason now says that
    rather than assuming it.
  - the five `*VPCConnector` operations **were** called — `scw vpc
    vpc-connector list` and `create` both recorded taking a 501 — and are still
    declined. Peering two VPCs is the one property the bridge mode cannot
    deliver, so answering would report done what was never apart. Demand
    decides what is worth serving; it never decides what can be served
    honestly, and the reason now carries both halves.

  **What is served is a record, and `docs/limits.md` says so** in the same words
  it uses for a custom route: the rule set round-trips, the protocols and
  actions are held to the SDK's enums, the sources and destinations are parsed
  as CIDRs, and no runtime mode programs a filter at the VPC edge. A 501
  protected nobody about enforcement and stopped every stack that declares the
  resource.

  **The empty answer is measured.** `scw vpc rule get` is a read a client makes
  before it has ever set anything, and a VPC with no ACL answers
  `{"rules":[],"default_policy":"accept"}` — read from the real cloud on
  2026-08-21, on the maintainer's own default VPC, creating nothing. The SDK's
  own default for `Action` is `unknown_action`, the protobuf zero, and it is not
  what the wire carries.

  Proved by both official clients: `scw vpc rule get/set` reads the rule set
  back through the other door, and OpenTofu with terraform-provider-scaleway
  2.81.0 applies `scaleway_vpc_acl`, re-plans **empty**, updates it in place,
  re-plans empty again, and destroys it.

- **A real Scaleway recording now carries a billed resource, so the value and
  order comparisons finally run** (#343, on #352's chain).
  `corpus/scaleway/scw-instance.jsonl` is the create, read, update and delete of
  one DEV1-S with one flexible IP against a real `fr-par` account — three
  seconds of existence on 2026-08-21, everything destroyed with the destruction
  proved by a read answering 404, the starting and final inventories identical
  family by family.

  **It closes a hole nothing was reporting.** Every `ReplayInvariant` this
  repository declares lives on `CreateServer`, `GetServer` and `UpdateServer`; a
  server is billed; the two free recordings therefore reached none of them. The
  gate ran with `values_checked=0` and `orders_checked=0` and printed *"0
  divergent finding(s)"* over it — including for the order of
  `Server.public_ips`, which is #320, a defect that cost a pull request. The
  same corpus now runs **2** value comparisons and **6** order comparisons, and
  the order matched.

  `feint corpus --check` prints both counts, and **fails when the packs declare
  invariants of a kind and the corpus runs none of them**: a check that never
  happened must not read as a check that passed. The condition is the packs'
  own declaration, so a repository declaring nothing is not asked to exercise
  anything.

- **Twenty-six divergences from the real cloud, classified and not fixed**
  (#365, #366, #367, #368, #369). The first recording of a billed resource found
  five causes, each carried in `corpus/accepted.json` with its reason and the
  issue that deletes its entry: the DEV1-S root volume is local here and a block
  volume on the cloud, so `scw`'s read of it answers 404 (#365); a server answer
  omits `bootscript` and `extra_networks`, which the cloud writes as `null` and
  `[]` (#366); `image.from_server` is `null` here and an empty string on the
  wire (#367); an attached public IP publishes no `gateway` and drops its own
  tags (#368); and `createServer` honours a project that `listServers` then
  hides (#369). Classifying and correcting in one pass is how a classification
  becomes whatever the patch happened to make green.

- **Exoscale serves the Network Load Balancer, and no backend carries a health
  verdict** (#345, successor to #14). The whole family is mounted:
  `exoscale/v2.create-load-balancer`, `exoscale/v2.list-load-balancers`,
  `exoscale/v2.get-load-balancer`, `exoscale/v2.update-load-balancer`,
  `exoscale/v2.delete-load-balancer`,
  `exoscale/v2.add-service-to-load-balancer`,
  `exoscale/v2.get-load-balancer-service`,
  `exoscale/v2.update-load-balancer-service`,
  `exoscale/v2.delete-load-balancer-service`,
  `exoscale/v2.reset-load-balancer-field` and
  `exoscale/v2.reset-load-balancer-service-field`. Exoscale moves from 93 to 104
  implemented operations.

  **The refusal that reopened the family is the one #14 wrote down.** #14
  declined all eleven because `load-balancer-service.healthcheck-status` is a
  per-backend verdict whose enum is `success` or `failure` with no third value,
  so an emulator that probes no backend would have to invent one of the two.
  That reading was right about the enum and wrong about the field:
  `healthcheck-status` is an **array**, and its element schema
  (`load-balancer-server-status`) declares no required property at all. An entry
  may therefore name a backend and carry no verdict, which is what this serves —
  one entry per member of the service's instance pool, each with the
  `public-ip` a client would probe and none with a `status`. Measured through
  the official CLI: `exo compute load-balancer service show` prints
  `"healthcheck_status":[{"instance_ip":"192.0.2.2","status":""}, …]`. An empty
  array was the other candidate and it is worse — it reads as "this service has
  no backend", which is a claim about the pool rather than about the
  measurement. What is *not* measured is said in `docs/limits.md`: no recording
  of a live NLB exists here, so the entry's shape comes from their published
  document and from two clients accepting it, never from the cloud's own
  answer.

  **No internal dataplane, and that is a measurement rather than a shortcut.**
  #345 asked whether the NLB could be the second customer of `machine.Balancer`,
  the provider-neutral interface #315 built for the Outscale LBU. It cannot, and
  `internal/core` gained nothing to make it so. On a live `incus-ovn` host on
  2026-08-21, on an OVN network of this emulator's own making (10.63.7.0/24):
  `EnsureBalancer` with the address this pack gives an NLB answers *"listens on
  192.0.2.1, which is outside … 10.63.7.0/24: an address the runtime has to
  announce goes dark within minutes (#315)"*; the same call with 10.63.7.240 and
  one backend answers `<nil>`; and the daemon itself refuses the public address
  with *Uplink network doesn't contain `"192.0.2.1/32"` in its routes*. An
  Exoscale NLB publishes exactly one address, `ip`, and their schema declares no
  other — no subnet, no private network, nothing like the LBU's `PrivateIp`. So
  the missing piece is an address, not a field of the interface, and the
  balancer's public face stays what `docs/limits.md` describes: a TEST-NET-1
  address routed nowhere.

  **What a real client settled, against what looked obvious.** A service
  mutation's operation refers to the **balancer**, not to the service it just
  created. Referring to the service — what every other mutation of this pack
  does — made `terraform apply` fail with `Get …/v2/load-balancer/<service id>:
  resource not found`, because egoscale v2 passes that reference straight to
  `GetNetworkLoadBalancer` and finds the new service by diffing the balancer's
  list (`v2/network_load_balancer_service.go:121` at v0.102.4). The exo CLI
  could not have found it: it resolves every object by listing and filtering,
  and never reads a reference.

  **A pool member now carries the public address its pool declares**, and
  `public-ip-assignment` is read on the pool. It was missing and nothing
  noticed, because nothing read it: a service's backends are identified by that
  address, so members without one made every service answer an empty backend
  list. The pack's TEST-NET-1 allocator counts balancers too, so a balancer and
  an elastic IP can no longer be handed the same address.

  **Proven with real clients.** `tools/conformance/exoscale/exo-cli.sh` drives
  the create, the service add with an https health check, the read-back, the
  port change, the service delete and the balancer delete, and asserts the
  backend entries exist and carry no verdict. The example stack
  `examples/stacks/exoscale/` gained an `exoscale_nlb` and an
  `exoscale_nlb_service`: with the patched provider `docs/limits.md` pins,
  **15 added, second plan empty, 15 destroyed**. And the surveyed stack the
  refusal blocked — PhilippeChepy/platform, layer `terraform-base`, replayed
  2026-08-21 against a `de-fra-1` emulator — moves from **19 applied / re-plan
  `6 to add`** to **20 applied / re-plan `5 to add` / 20 destroyed**: its
  `exoscale_nlb` applies and its `data "exoscale_nlb"` reads back. Its single
  `exoscale_nlb_service` still does not, and not for a reason of this emulator's:
  it sits behind the SOS bucket branch, which points at the real
  `sos-de-muc-1.exo.io` and fails on fake credentials exactly as the survey
  recorded.

  **Two per-field resets carry a reason nobody else's does.** Every other family
  of this pack says the CLI clears a field by sending the update with an empty
  value. Measured on this one on 2026-08-21, it does not: `exo compute
  load-balancer update --description ""` sends `PUT {}`, and the service form
  sends only the healthcheck block it re-sends on every call. This CLI clears no
  field at all, by update or by reset, and copying the familiar sentence would
  have recorded a behaviour it does not have.

- **The emulator can be made to refuse, per operation, off by default** (#26,
  #356). `PUT /_feint/faults` arms a rule naming an upstream operation and what
  to answer instead: a status, a delay, or a body cut short. `GET` lists the
  rules with their hit counts, `DELETE` clears them, and a fresh emulator arms
  nothing.

  **The measurement that asked for it.** `coverage/evidence.json` carries seven
  axes per mounted operation. `negative` stood at 34 of 357, far below every
  other — this emulator proved what its routes answer when everything goes well
  and almost nothing about what they answer when it does not, so a client's
  degradation paths could only be simulated in that client's own tests. #356
  measured the other end of the same gap: no authentication header at all
  answered `200`, and so did a junk bearer token.

  **The core decides when; the pack decides what a failure looks like.** A 503
  reaches a Scaleway client as `scw`'s own error shape, an Outscale client
  inside its `ResponseContext`, an Exoscale client as its own bare-message
  envelope (`emulator.Faulter`, new and optional). Where an SDK names a `type`
  for a status the pack emits it — Scaleway's `permissions_denied` and
  `denied_authentication`, both cases of `unmarshalStandardError` — so the
  client's own dispatch fires and `errors.As` matches. Where none is named,
  nobody here has measured how Scaleway spells a 429, and the value says plainly
  that it is this emulator's rather than publishing a plausible fact about a
  provider. A rule whose status a pack cannot render is refused when it is
  written, never answered with a body the core made up.

  **Deterministic, scoped, and refused early.** `times` bounds a rule to the
  first N calls; there is no probability knob at all, because a fault that fires
  at random cannot be the subject of a test. A rule names one operation, at the
  name `/_feint/routes` publishes, and one rule per operation. A rule naming an
  operation nothing serves is refused with a 400: a rule that never fires reads,
  from outside, exactly like a client that survived the fault.

  **An injected answer proves nothing**, and that is a control rather than a
  promise. It moves no counter but the new `injected` one: the operation stays
  `driven: false`, its answer is not contract-checked, its fields join no union,
  and the emulator *refuses to close* a `negative` assertion span on it, naming
  why. `tools/conformance/score.sh` fails any run carrying an injected answer,
  so the shared conformance run cannot be padded — fault injection has its own
  suite, its own emulator and its own port.

  **Measured with the real clients** (`tools/conformance/faults.sh`, in
  `mise run conformance` and on the `fields` leg of the workflow):

  - `scw` prints `scaleway-sdk-go: insufficient permissions: GET …` on an
    injected 403 and `Cannot find resource 'server' with ID …` on a real 404 —
    the distinction #356's consumer needs, and one nothing here could produce
    before;
  - `scw` **retries a 429 and not a 503**: 429, 429, 200 completes, while the
    first 503 ends the command. The retry is the CLI's, not the SDK's —
    `scaleway-sdk-go/scw` contains no retry at all;
  - the real Outscale Terraform provider survives **503, 503, 200** inside an
    `apply`, backing off twice through `go-retryablehttp`, and reaches `Apply
    complete`;
  - `oapi-cli` retries both (`attempt 0 failed. Retrying in 3520 ms.`) and
    decodes the 403 as code `4120`, which `osc.IsAuthError` reads and
    `osc.IsNotFound` does not;
  - `exo` retries a 503 five times and completes on the third answer, and
    surfaces a 403 as `Forbidden` against `not found` for a real absence.

  Not delivered, and stated rather than discovered: connection resets and true
  transport failures live below the route handler; a body cut short is what this
  offers instead. Deterministic control over how long an emulated asynchronous
  transition takes lives in a pack's lifecycle, not in front of a handler, and
  is not folded in.

- **The committed corpus is replayed on every pull request** (#353). `feint
  corpus --check`, `mise run corpus:check`, in `prepush` and in
  `.github/workflows/go.yml`. It replays every file of `corpus/` against an
  emulator of its own and fails on a divergence from what the real cloud
  answered. Thirty milliseconds, offline, no credential and no client binary,
  because every input is a versioned file — which is exactly why it can be a
  gate where `conformance` cannot: a hook that fails on an absent binary teaches
  `--no-verify`, and that disarms every other hook at once.

  **This is the first control here that compares an answer with the cloud's.**
  `mise run conformance` proves a real client *accepts* what this emulator says,
  and cannot prove the answer is the one the cloud would have given, because the
  cloud is not there. `shapes --check` compares field trees; a corpus carries the
  status, the order and the sequence as well.

  **Three verdicts, never blurred.** A divergence the acceptance list does not
  carry is exit 2. An operation no route serves is printed and never counted —
  the day it fails a build is the day somebody stops recording. A corpus that
  could not be read, or that compared nothing, is exit 1: an empty file, an
  empty directory and a file whose every exchange is unserved are each red with
  their own message. **A corpus that replays nothing is a failure, never a
  pass** — this repository has shipped the other shape twice, in the network
  conformance suite and in five checks of `tools/ui/check-page.py`.

  **`corpus/accepted.json`**, on the model of `tools/compat/accepted.json`,
  carries the eight divergences of the first run with their reason and the issue
  that retires them (#355), plus the date each file was recorded. Both halves are
  held: an entry that excuses nothing fails the gate, so a fix cannot leave its
  exemption behind, and a corpus file nobody dated fails it too.

  **How a corpus ages: it warns, and never fails.** A gate that fails because the
  *cloud* moved is a gate somebody disables, and this one holds only one side of
  the comparison — it can say the emulator and the recording disagree, never
  which of the two changed. Failing on age would assert exactly what it cannot
  measure; #359 is the half that can arbitrate. The horizon is 180 days, in a
  committed file, and the warning names the file, its age and the re-recording
  procedure.

- **`feint replay` binds a recorded identifier under the field name it was seen
  at** (#353). The corpus surfaced a recording the previous binding could not
  represent: on a Scaleway account with one project, `project_id` and
  `organization_id` are the *same string*, so one recorded value had two
  candidate answers and a map from value to value could hold only one. Which one
  was decided by Go's randomised map iteration — six replays of
  `corpus/scaleway/scw-cli.jsonl` against six fresh emulators graded
  `vpc/v2/API.ListPrivateNetworks` divergent three times and matched three
  times, and when the organisation won, the create filed its network under a
  project the unfiltered list does not cover. A divergence the replay had
  manufactured itself.

  Bindings are now scoped to the field name they were observed under, so
  `project_id` resolves to the project this emulator minted and
  `organization_id` to the organisation, and the walk that learns them is
  sorted, so a value with no field to scope it — a path segment — resolves the
  same way on every run. The unscoped map remains as the fallback, which is what
  keeps a path rebound at all. A run now reports how many recorded values two
  fields bound differently, rather than resolving them in silence. The verdict on
  the committed corpus went from "8 or 9" to 8 on every run of eight.

- **The frozen CLI surface moves to version 9** (#353): the verb `corpus`, with
  `--check`, `--dir` and `--accepted`. An addition — nothing was removed, no exit
  code moved, and a pipeline keyed on version 8 keeps working.

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

- **`feint transcript --sanitise` turns a recording of a real cloud into an
  artefact this repository may commit** (#351). A transcript is the inventory of
  somebody's account, and `shapes/*.json` — the one committable thing derived
  from a recording — throws away exactly what a replay grades beyond the field
  tree: the status, the order, and the sequence itself. So `feint replay` had
  only ever met its own output.

  ```bash
  feint transcript real.jsonl --sanitise corpus/scaleway/scw-cli.jsonl \
    --contract contracts/scaleway.json
  ```

  The output is a transcript like any other, read unchanged by `replay`,
  `transcript` and `shapes`, with **every value replaced by a synthetic one of
  the same shape**: a UUID becomes a UUID, an address an address, a CIDR a CIDR
  of the same prefix length, an OpenSSH key a valid OpenSSH key. That is what
  makes it still replayable — the replay rebinds a synthetic identifier exactly
  as it rebinds one a cloud handed out — where a value blanked to `REDACTED`
  would break the request carrying it and retype the field holding it, which is
  the defect the proxy's own redaction produced in #73.

  **Default deny.** A redaction by *name* answers "does this look like a secret"
  and never "is this not one", so a value stays only if this repository
  publishes it: a literal of the path the provider's document writes, a word a
  pack vouches for (`emulator.Vocabulary`, new — Scaleway's zones and regions,
  the two lists it answers 400 for), a value the contract enumerates, a boolean,
  a run of at most six digits. A path the document does not describe therefore
  loses **every** segment, and the command lists what it blanked rather than
  keeping the words that looked harmless.

  **Two controls, both before the file exists**, and nothing is written if
  either speaks: the output is cross-referenced against the source recording, so
  a value that survived is named whatever shape it had; and every value of the
  output must belong to the alphabet a sanitised transcript may carry. Two
  further tests read the committed files back, one of them by shape alone,
  knowing nothing of the rules that produced them.

  Falsified in both directions, twelve mutations
  (`tools/falsify/specs/sanitised-corpus.json`): remove any part of the
  substitution and a value of the account is published or the audit goes quiet;
  remove what a pack vouches for and the sanitised corpus replays **400 unknown
  zone** on every call, which is the museum piece the second direction exists to
  prevent.

- **A committed corpus of what real Scaleway answers** (#352), under `corpus/`,
  replayable with `feint replay`. Recorded on 2026-08-21 through `feint proxy`
  against a real account in `fr-par`, driving terraform-provider-scaleway 2.81.0
  and `scw` 2.56.3: the full create/read/update/destroy of a VPC and a private
  network, the reads every stack makes before it creates anything, an IAM SSH
  key, and two deliberate 404s. Free resources only, everything destroyed under
  a `trap`, each destruction proved by a read that answered 404, and the account
  byte-identical before and after across fifteen object families.

  What it measured, on the emulator of that day: the Terraform recording matches
  on all 16 exchanges — the first time a replay has met a real cloud's answer
  and agreed — and the `scw` one on 33 of 58, with 16 operations no pack serves
  and 8 or 9 divergences in three families. The "or" is a measurement too: six
  runs of the same file graded `ListPrivateNetworks` divergent three times and
  matched three times, because this account's `project_id` and
  `organization_id` are one string, the replay's rebinding has two candidates
  for it, and it walks a Go map to choose. `corpus/README.md` names all of it,
  and the account rules for recording the next provider.

- **A pack can declare what a replay may compare beyond presence and type**
  (`emulator.Invariant`, `ReplayInvariants()`). Optional in the manner of
  `FieldDecliner`, with a reason held to the same guard `Declined()` faces, plus
  one of its own: a kind nothing implements is refused rather than reading as
  "compared". A declaration naming an operation no route serves fails a test.
  The report counts value checks and order checks separately, so a declaration
  that evaluated nothing cannot read as one that held.

- **Two more corpora of a real cloud: Exoscale from a named account, Outscale
  from no account at all** (#354, after #351/#352/#353). `corpus/` now holds
  four files and 264 exchanges, and `mise run corpus:check` replays every one of
  them offline, per file, against a fresh emulator.

  `corpus/exoscale/exo-cli.jsonl` — 203 exchanges, `exo` 1.95.1 (egoscale
  v3.1.36) against a real Exoscale account in `ch-gva-2` on 2026-08-21, through
  `feint proxy --forward '*.exoscale.com'`. It carries the reads every stack
  makes before it creates anything (zones, instance types, templates under an
  explicit `visibility`, ssh keys, security groups, anti-affinity groups,
  private networks, instances, pools, elastic IPs, block storage, load
  balancers, quotas), two deliberate 404s, and the whole free lifecycle:
  register/get/list/delete an SSH key, create two security groups with a rule
  each — one on `0.0.0.0/0`, one naming the other group — create an
  anti-affinity group, and create/read/update/read/delete a private network.
  **Nothing billed was created**: an instance, an elastic IP, a block-storage
  volume, an NLB and an SKS cluster are all charged and none was made. Every
  delete was proved by a read answering 404 or an empty list, and the account
  ends as it began, one `default` security group and nothing else.

  `corpus/outscale/oapi-cli-catalogue.jsonl` — 5 exchanges, `oapi-cli` 0.13.0
  against `api.eu-west-2.outscale.com`, **driven with the public placeholder
  credentials of `tools/conformance/outscale/fake-credentials.env`**. Measured
  on 2026-08-21: five operations answer 200 to an unknown access key —
  `ReadRegions`, `ReadVmTypes`, `ReadPublicIpRanges`, `ReadPublicCatalog`,
  `ReadFlexibleGpuCatalog` — where every authenticated one answers 400
  `InvalidParameterValue` 4120. So a provider's own catalogue is recordable from
  any station with no account to put at risk and no inventory in the answers,
  and this is the first time this repository has compared its Outscale pack with
  the **cloud** rather than with a document. Three of the five replay with no
  divergence; the two catalogue reads nothing serves are #74's queue.

  **What the Exoscale recording found: three fields the cloud answers and this
  emulator omits**, each with an exemption in `corpus/accepted.json` naming the
  issue that deletes it. `zones[].id` on every zone list (#370, 51 findings, the
  most-answered operation of the whole file); `visibility` on a security group,
  on the list and on the get alike (#371, 46); and `rules[].security-group.name`
  where a rule names another group (#371, 8). All three have one root worth
  stating: **`contracts/exoscale.json` does not declare them either**, so the
  shapes gate, the probe and the pack all agree with each other because they
  read the same document — and the document is behind the cloud. Only a
  recording of a wire could disagree. It is the family of #352's
  `has_s3_integration`, one provider further out.

  **The account rules of #352 held without exception**, and #354 adds one that
  is not about money: *the profile is named explicitly on every command, and a
  region whose point is a compliance boundary is never a target*. The Exoscale
  account is named with `exo -A <account>` on all 60-odd calls of the recording
  script, and which account that was is in the pull request rather than here;
  the Outscale run names its single fake profile with `--profile`, so there is
  no default to fall back to and no stored profile of the station can be
  presented. `corpus/README.md` carries both procedures.

### Security

- **A replay against a real account refuses to touch what it did not create**
  (#359). Every identifier in the path of a state-changing request has to be one
  this run's own creates minted — `mustOwn` applied where getting it wrong
  destroys somebody's property, and it is needed because a recorded request is
  well formed by construction, which was never the same question as authorised.
  A create is refused unless its operation is on a written-down list of what
  costs nothing, and the refusal names the operation, so the report says which
  measurement is out of reach without spending rather than spending to find out.
  Everything created is named `feint-corpus-*` and destroyed at exit from a
  ledger armed before the first call, with **each destruction proved by a read
  answering 404** rather than by the delete's own answer; an object that survives
  fails the run whatever the comparison found. A credential travels by
  environment variable and never in argv, and is refused outright to a
  plain-HTTP endpoint off loopback.

  **Twenty-two mutations, all of them falsified**
  (`tools/falsify/specs/corpus-cloud.json`, run of 2026-08-21): remove the
  ownership question and a recorded `DELETE` removes an account's own VPC; remove
  the free-to-create list and the account is billed to make a measurement;
  believe the delete's own answer and a provider that deletes asynchronously
  leaves the object behind under a green run. Against the real account the same
  day, five objects were created and five destroyed with every destruction proved
  by a read, and the inventory taken before each run matched the one taken after.

### Changed

- **The record follows the divergence and queue lots** (#431, #445), and
  `mise run conformance:exoscale-terraform` arms its trap before the emulator it
  starts. As three steps it left one behind on a failing run, until
  `evidence:update` refused to start beside it — the doorstep #426 added, working
  on the task that had just been written. A task that starts a process owns its
  death on every path, not on the happy one.

- **The record follows the empty-answer fix** (#429): `contract` goes 330 to
  **361 of 370** and `probed` 330 to **359**. Scaleway's own `contract` reaches
  **173 of 173** and its `probed` 170. **No operation lost anything on any
  axis**, checked operation by operation against the replaced record. The thirty
  one operations that gained `contract` were already correct — nobody had ever
  looked, which is what `unchecked` means and why it is not `absent`.

- **The record follows the refusal lot on Scaleway and Exoscale** (#428):
  `negative` goes 197 to **247 of 370**. Scaleway's own 97 to **139 of 173**,
  Exoscale's 10 to 18. **No operation lost anything on any axis**, checked
  operation by operation against the replaced record rather than by comparing
  totals.

- **The record follows the Outscale refusal lot** (#440): `negative` goes 173 to
  **197 of 370**, and Outscale's own 66 to **90 of 93**. `behaviour` 319 to 320.
  **No operation lost anything on any axis**, checked operation by operation
  against the replaced record rather than by comparing totals. Twelve of the
  remaining Outscale zeros are declared out of reach at the route, with a reason
  the guard re-measures.

- **The record is regenerated on the new recordings, and `shape` reads 225 of
  370** (#427). Outscale reaches **93 of 93**, its fifth complete axis; Scaleway
  goes 37 to 99, Exoscale 31 to 33. The per-provider table lives in
  `docs/routes.md`.

  **Six operations lost the axis, and that is the correction rather than a
  regression**: all six are `DELETE`s that had earned it through a phantom field
  written at the empty path by a `204`. They are named — `DeleteVolume`,
  `DeleteSSHKey`, `DeleteIP`, `DeleteServer`, `DeletePrivateNetwork`,
  `DeleteVPC` — and checked operation by operation against the replaced record
  rather than by comparing totals, because a total can hide a loss under a gain.
  No other axis moved for any operation.

- **The record is regenerated after the suites gained the calls the fold
  surfaced** (#407): `driven` 344 to 345, `dataplane` 344 to 345, `behaviour`
  316 to 317. **No operation lost anything on any axis**, checked operation by
  operation against the replaced record rather than by comparing totals. `shape`
  holds at 134, which is what the fold measured and not a second draw.

- **The evidence record is regenerated on causal attribution, and `behaviour`
  reads 316** (#398). The committed record carried 312, a draw made before a
  store touch was credited to the request that made it. Outscale goes 77 to 79,
  Scaleway 157 to 159, and **no operation lost anything on any axis** — checked
  operation by operation against the replaced record, not by comparing totals,
  because a total can hide a loss under a gain. The other six axes are unchanged
  to the operation.

  Both legs ran on a quiet station, machines on for the second, and the host was
  read back afterwards: no container and no network of the run remained.

- **`internal/replay` gained the two seams a real account needs, and nothing
  else** (#359). `Options.Guard` is asked before every request goes out — on the
  request *after* rebinding, which is the only form worth judging, since a
  recorded `DELETE` names the identifier the cloud minted last time — and is
  handed every answer that comes back. `Options.Bind` seeds the rebinding table
  before the first request, which is what makes a recording that opens on a
  create replayable at all: `corpus/scaleway/terraform.jsonl` starts with a POST
  carrying a `project_id` that belongs to no account the replay is pointed at. A
  refused exchange is `Refused` — never a match, never a divergence, because the
  call was not made. Replaying at this emulator passes neither guard nor seed, so
  `feint corpus --check` and `feint replay` are unchanged.

- **The CLI surface is version 10.** `corpus` gains `--against-cloud`, `--file`,
  `--endpoint`, `--credential`, `--bind`, `--format`, `--timeout`, `--dry-run`
  and `--mark-stale`. Additions to one existing verb; nothing was removed, no
  exit code moved, and a pipeline keyed on version 9 keeps working.

- **CLI surface version 8.** Every entry above is an addition — the verb
  `replay` with `--endpoint`, `--format` and `--timeout`, `coverage --observed`,
  and `transcript --sanitise` with `--contract`. Nothing was removed and no exit
  code moved, so a pipeline keyed on version 6 keeps working.

- **`/_feint/conformance` schema version 4, and a fifth frozen surface.** The
  payload gained `injected`, the answers the fault injector produced per
  operation. Additive, and it changes what the whole document *means* rather
  than only what it carries: every other counter there describes what the
  emulator served, and this one names what it staged. `/_feint/faults` is frozen
  from its first version, with its own fixture, because a suite arming a fault
  from a committed file is a consumer on day one. `cliSurfaceVersion` does
  **not** move: the injector is reachable over the admin plane alone and adds no
  verb and no flag.

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

- `internal/shape.IsMintedIdentifier` holds the whole of "is this an identifier
  a cloud minted" — a UUID, an address, an Outscale `i-<hex>` — for the same
  reason `IsUUID` was exported. The replay rebinds on that answer and the
  sanitiser refuses to publish on it, and a sanitiser recognising one identifier
  less than the replay would publish exactly the values the replay knows are
  identifiers.

### Fixed

- **The `behaviour` axis was a function of the scheduler, and two identical runs
  agreed on the total while disagreeing on six operations** (#398). Two
  `mise run conformance` runs of the same commit, machines off, marked **311**
  operations each and did not mark the same 311: `block/v1/API.CreateVolume`,
  `osc/Client.DeleteSecurityGroupRule` and
  `osc/Client.UnlinkLoadBalancerBackendMachines` in the first,
  `instance/v2alpha1/API.DeletePrivateNetworkInterface`,
  `instance/v2alpha1/API.GetPlacementGroup` and `osc/Client.UnlinkVolume` in the
  second. The equal totals are the trap, and they are why the fix is measured on
  sets: the issue's own acceptance criterion — the same figure twice — passes on
  the broken code.

  A store touch was credited to an operation only while exactly one non-probe
  request was in flight anywhere in the process, and terraform runs at
  `-parallelism=10` under a span bracketing its whole lifecycle. The store
  already answers the question that rule approximated: `Observe` runs its
  callback synchronously, outside the store's lock, **on the goroutine that made
  the touch**, so the handler goroutine is the causal link between a request and
  what it touches. That is read now, and it ends an over-claim nobody had
  noticed as well: a touch made by the probe's goroutine, or by a handler
  `serveFault` calls directly and which never enters the flight set, used to be
  credited to whichever unrelated client request happened to be in flight beside
  it.

  Measured after the change, same host, same commit, machines off: **316 and
  316, and the same 316** — the whole record, all seven axes and all 370
  operations, is now identical between two runs, where before exactly six
  `behaviour` entries differed and nothing else. Five operations were recovered,
  none lost, and every span of every suite reported zero touches it could not
  attribute. What is still
  unattributable is bounded and said rather than dropped: a request already in
  flight when a span opens carries no identity, the close of the span publishes
  how many touches that cost, and `tools/conformance/prove.sh` prints it.

  And the other half of the same issue: `runtimesLost` refused a regeneration
  that reached fewer *runtimes*, and nothing ever looked at the operations, so a
  record could demote one whose assertion was still in the suite and still
  passing without a word. `feint evidence` now names, axis by axis, every
  operation the record it replaces had earned and this run does not. A report
  and not a refusal, because an axis may legitimately shrink when a claim is
  corrected and a suite that loses an assertion *must* demote what it proved —
  that is the falsification this record lives under.

- **Three axis percentages published in the documentation were wrong, and
  nothing in the repository refused a measured number written by hand** (#406).
  `docs/proxy.md`, `docs/conformance.md`, `corpus/README.md`, both CHANGELOGs and
  #390's opening table stated that six of the seven evidence axes stood in a
  percentage band. Measured with `feint coverage --evidence coverage/evidence.json`
  on the same artefact, three of the six were outside it and one by a factor of
  six. The cause is rule 2 of the measurement-integrity skill met on this
  project's own headline numbers: the figures came from a throwaway script that
  read each axis as a boolean, `if o.get(axis)`, when three of the seven are
  verdicts — `"unobserved"` is a non-empty string, so every operation whose shape
  had never been compared to a real cloud answer counted as one that had.

  Every one of them is corrected, and `docs/proxy.md` carries a note saying what
  it used to claim, because a number edited in silence teaches nothing. The
  recurrence is what the change is for: **an axis percentage now lives inside a
  generated block or nowhere**, and `feint docs --check` — which prepush and the
  pre-commit hook run — refuses a percentage sitting next to an axis name outside
  one, in any Markdown of this repository. Counts are untouched: "35 of 370" is
  what a work queue is made of.

  **The same defect was found in this repository's own reader while the
  correction was being tested**, which is the part worth keeping. `probed` was
  earned by `e.Probed != "none"`, so a row carrying no verdict at all — the empty
  string `encoding/json` leaves for a missing key — earned the axis, exactly as
  `if o.get(axis)` did in the script. It is named positively now, and
  `readEvidence` refuses a record whose `probed`, `contract` or `shape` is
  outside its own vocabulary: the function's comment already claimed it "refuses
  what it cannot account for" and did not, which is this repository's most
  expensive recurring defect met once more.

- **The conformance run orphaned one of its own networks mid-run, and that one
  teardown race is what #316, #342 and #375 were all downstream of** (#386).
  `mise run evidence:update` failed twice in bridge mode on 2026-08-21, each
  time on a subnet of `examples/stacks/outscale/main.tf`, and each failure was
  preceded in the emulator log by `detach isolation from fnt-…: open
  /var/lib/incus/networks/…/dnsmasq.raw: no such file or directory`. That is one
  request's isolation reconciliation reaching a network another request had
  already deleted: the pass lists the store, a concurrent delete removes one of
  the members it listed, and the config edit lands on a network the daemon no
  longer knows. The object dies, the interface and its `dnsmasq` outlive it, and
  the next run wanting that block dies minutes in on "Address already in use".
  **The three issues before it made the leftover visible or survivable; none
  addressed what produces it**, and no doorstep can, because the host is clean
  when the run starts and the run dirties it at step twelve.

  **Two halves, because either alone leaves the race open.** The Incus driver
  now takes `serialise.Lock("incus.network." + name)` in `EnsureNetwork`,
  `IsolateNetwork` and `RemoveNetwork`, so no config edit of a network is in
  flight while its delete runs. Per network and never global: a global lock
  would queue every subnet of a stack behind one delete, which is the mistake
  `internal/core/machine/serialise.go` already records having made once, with
  interface allocation (#348). And `IsolateNetwork`, holding that lock, asks the
  daemon whether the network is still there before it edits anything, because
  the lock alone still lets a delete that won it run first, and the question
  alone is a time-of-check a delete crosses.

  **A detach that could not happen is reported, never counted as done.** The
  driver returns `machine.ErrNetworkGone` and `ReconcileIsolation` logs it,
  naming the network. At warn rather than error: no rule set was needed and none
  is missing, and a line that fires on every parallel destroy is how a log stops
  being evidence. The rule set that isolated the network is now dropped by the
  delete itself, since the pass that used to drop it is the one that now refuses
  to run against a network that is gone.

  Reproduced deliberately before anything was changed. The fake runtime models
  the daemon behaviour the log showed — an edit re-applies the network, bringing
  its bridge and its `dnsmasq` back up, before it opens `dnsmasq.raw` — so an
  edit that meets a delete leaves a service standing for a network that is gone,
  which is exactly the leftover #342 measured. Falsified in
  `tools/falsify/specs/teardown-race.json`: five mutations, all red, and the
  lock mutation measured out of tree at red 10/10 with the lock removed and
  green 10/10 with it back.

- **Exoscale answers 409 for a public key it cannot read, and so does this pack**
  (#390). `POST /v2/ssh-key` carrying a string that is not an OpenSSH key
  answered 400 here and `409 {"message":"Public key is invalid"}` at a real
  `ch-gva-2` account, measured 2026-08-21. Both refuse; a client branches on
  which, and rule 4 says the provider decides.

- **An OpenSSH public key whose material names another algorithm is refused**
  (#390). `ssh-ed25519 AAAA` used to parse: the first field is a known algorithm
  and the second is valid base64, which was everything `sshkey.Parse` checked.
  The material names its own algorithm (RFC 4253) and it must be the one the
  line declares — the real cloud makes exactly that check and answers 400
  `invalid key type`, measured against a real account. Well formed is not valid,
  and two fixtures in this repository were leaning on the gap. Every pack that
  accepts a public key gains the refusal.

- **Listing the routes of a load balancer frontend that does not exist answers
  404 instead of an empty page** (#390). `scw lb route list frontend-id=<absent>`
  answered 200 with `routes: []`, which a client reads as "that frontend carries
  no route" rather than "there is no such frontend". The cloud answers 404
  `frontend not Found`.

- **An Outscale DHCP options set naming a server that is not an IPv4 address is
  refused** (#390). `CreateDhcpOptions` stored the string and answered 200; the
  cloud answers 400 `InvalidParameterValue` before it stores anything. The
  platform's own `OutscaleProvidedDNS` keyword is still accepted, being the one
  value of that field which is not an address.

- **The sanitiser no longer publishes an address outside its own synthetic
  space** (#390). A block shorter than `198.18.0.0/15` itself cannot be placed
  inside it, and masking the replacement to that length walked straight out:
  `10.0.0.0/8` came back as `198.0.0.0/8`, whose address half belongs to
  somebody. The prefix length is what an API validates, so it survives, and the
  address half becomes the space's own.

- **Three fields the real Exoscale API answers and this emulator omitted, all
  invisible to every control that reads a document** (#370, #371). They are 105
  of the 192 divergences the 2026-08-21 recording of a real `ch-gva-2` account
  reported, and `corpus/accepted.json` is down to 87 accordingly.

  `GET /v2/zone` now answers an `id` per zone, derived from the zone name so
  that two reads of one emulator agree and a restarted one still does — #370
  states why that half matters: an identifier that moved between reads would be
  worse than none, because a client that stored it would hold a value naming
  nothing. It is 51 findings on its own, because `exo` lists the zones before
  very nearly every command it runs. `GET /v2/security-group` now answers
  `visibility` on every group, on the list and on the read of one group alike
  (46). And a rule that points at another group now publishes that group's
  `name` beside its `id` (8) — the shape `examples/stacks/exoscale/main.tf`
  writes as `user_security_group_id`, "the application tier accepts the web tier
  and nobody else". **#371 says a consumer reading the name off the rule sees
  nothing, and that half does not survive measurement**: `exo compute
  security-group show` prints `SG:web` with the name and without it, because it
  resolves the reference by id against the groups it has already listed. The
  reason to serve it is the only one this project ever needs — the recording
  says the cloud sends a name — not a client symptom nobody has reproduced.

  **Why nothing had seen them, which is the part worth keeping.** Two of the
  three are not in Exoscale's published `source.yaml` either, so the contract,
  the shapes gate, the probe and the pack all agreed with one another and all
  four were wrong the same way. The zone id had even been raised once, as #94,
  and closed as not-a-defect on the grounds that serving it failed this
  emulator's own contract check — which was true, and was the wrong end to fix.
  Only a recording of the wire could disagree, and now one does. The third
  needed no contract change at all: the document already declared the field and
  only the pack had dropped it.

- **A DHCP service left behind by a run failed the runtime leg every time, and
  the only remedy was a human reading a log** (#375). The sweep in
  `tools/conformance/*/network.sh` found the leftover, named it exactly and
  printed `sudo kill <pid>` — then exited 1, because the process belongs to the
  `incus` user and the operator running the suite may not signal it. The
  diagnosis was right and nothing ran the remedy, so the runtime leg of
  `mise run evidence:update` died in the same place three runs in a row, each
  time *after* every client suite had already run. **A gate whose only remedy is
  a manual step somebody has to notice is a gate that gets worked around**, which
  is how `--no-verify` is learned.

  Two changes, and neither of them escalates. `feint clean --check` asks the
  sweep's own question without doing anything: it names what an earlier run left
  holding an address block, separates what this user may end from what it may
  not, and exits 1 only for the second. The permission probe behind it is signal
  0 — the kernel runs the check it would run for a real signal and delivers
  nothing, which is the only acceptable shape for a question whose subject is a
  process nobody here started. And `guard_leftovers`
  (`tools/conformance/guard.sh`) puts that question on the doorstep: the three
  network suites and `mise run conformance` itself ask it before anything starts,
  so the answer arrives at second zero instead of twelve steps in. Measured on
  the reproduction: **1 second to refuse, naming the pid**, where the same host
  used to spend the whole client run first.

  **What it is not is a suite that escalates.** A conformance run that acquired
  the right to end a daemon it did not start would be a worse defect than the one
  it works around — it is the question `mustOwn` asks of the driver, one layer
  up. The elevation is the operator's, in one line rather than a pid to retype:
  `sudo feint clean --vm <mode>`, the same sweep, re-asking every ownership
  question at the moment of the signal. Reproduced deliberately before anything
  was changed, and falsified in
  `tools/falsify/specs/unkillable-dhcp-orphan.json`.

  **And one premise it does not carry over.** These leftovers were measured as
  debris of an *earlier* run (#316, #342); the machines-on leg produced one of
  its own on 2026-08-21, mid-run and in bridge mode, from a network it had
  created minutes earlier — the runtime listed that network as unmanaged while
  its bridge and its `dnsmasq` stayed up. So the messages say "a run" rather
  than "an earlier run", because sending the operator to look for a previous run
  would send them to the wrong place, and no doorstep prevents what the run
  itself creates. It is deterministic: the leg was run twice and failed both
  times, in the same suite, on a subnet of `examples/stacks/outscale/main.tf`,
  each failure preceded by a detach of the isolation arriving at a network whose
  state directory the delete had already removed. `docs/limits.md` carries the
  log lines. That birth is a separate defect and is not fixed here.

- **The corpus gate withheld its "the cloud has moved" warning exactly when it
  mattered most.** Both warnings — the aged-recording one and the measured-move
  one #359 writes back — were emitted below the unexercised-invariant guard
  (#343) and below the stale-exemption guard, so a run that went red for either
  reason printed no word about a recording the provider has been measured to
  have moved under. That is the worst moment to withhold it: *re-record this
  file* is a candidate fix for the very redness being reported, and a maintainer
  who does not see it goes looking for a defect in the emulator instead. Neither
  warning reads anything but the acceptance file and neither moves an exit code,
  which their own doc comments state as their contract; the only thing a late
  placement could do was withhold them. `warnMovedCorpora` said "on every run"
  and nothing made it true —
  `TestTheMovedWarningSurvivesARunThatIsRedForAnotherReason` does, on the
  poorest run that reaches the print.
- **Five shapes the Outscale pack answered differently from the cloud**, each
  found by replaying the recording of a real account and each retiring its
  exemption from `corpus/accepted.json` (#378, #379, #381, #382, #383). The
  accepted count went from 289 to 141.

  - **A machine answers `UserData` and `Tags`** (#378). The cloud writes both on
    every machine — `""` and `[]` on one created with neither — and this pack
    wrote them only when it had something to put in them. Every other kind in
    the pack already set `"Tags": []` at create time; the Vm was the one that
    did not.
  - **Two lists come back in the order the API orders them** (#379). A
    machine's security groups are ordered by `SecurityGroupId` ascending, and a
    route table's routes by destination **on a read** while the create echoes
    them in append order. Both are measured, and the second is the cloud
    disagreeing with itself: a client that stores the create's answer and then
    reads sees the two swap, which an emulator that tidied it up would hide.
    Terraform stores both as lists, so an order of this emulator's own is a plan
    diff that never converges — #320, one provider out.
  - **`DeleteRoute` and `DeleteLoadBalancer` answer the object** (#381) rather
    than the envelope alone, which is how a client refreshes its state in one
    call instead of two.
  - **A rule that names another security group publishes that group's
    `AccountId` and name** (#382). The `Rules[].SecurityGroupsMembers[]` form
    copied the client's member through verbatim, so the answer said less than
    the cloud's about a group the client named by identifier alone — and
    `AccountId` is what distinguishes a member group of this account from one
    another account shared in.
  - **An image carries its root device mapping and its launch permissions**
    (#383): the device name, the root volume's size and type, which a client
    reads before it sizes the volume it creates. The `SnapshotId` and the `Iops`
    of that mapping are **declared** rather than served, and the line is the one
    the code already drew: naming a snapshot `ReadSnapshots` cannot answer for
    is how a client's resolve fails on an object that does not exist, and a
    standard volume has no provisioned IOPS.

  **One order is deliberately not declared as a `ReplayInvariant`, and that is
  a limit worth writing down.** A machine's security-group order is derived from
  identifiers the *cloud* minted; no emulator mints those, and `feint replay`
  compares position by position after rebinding — so declaring it would buy a
  permanent exemption, and a permanent exemption is a gate that has quietly
  stopped covering what it names. It is held by a unit test of the pack instead.
  What is declared is the route order, which the cloud derives from a value both
  sides carry.

- **The recorder no longer writes two different values as one** (#384). A
  redaction replaced every value the rules matched with the same string, which
  is right for a credential and wrong for everything else a *name*-pattern rule
  catches. Measured recording a real Outscale account: `KeypairName` matches
  `key`, so the imported key's name and the invented one of a deliberate refusal
  both reached the transcript as `REDACTED`, and the file said the two calls
  addressed the same object. Replayed, the emulator deleted the real key on the
  exchange meant to answer 404 and had nothing left for the one meant to answer
  200.

  A placeholder now carries a per-value suffix — `REDACTED-<8 hex>`, an HMAC
  under a key drawn once per process and never written down. Three properties,
  and the third is why it is keyed rather than hashed: the same original keeps
  the same placeholder throughout a recording, two originals get two, and **the
  value cannot be recovered from the placeholder whatever its entropy** — a
  plain digest of a short secret is a brute force away from being the secret.
  `internal/corpus` renumbers them to `REDACTED-<n>` so the committed artefact
  carries the sanitiser's own counter and the alphabet has one spelling to
  admit.

  Nothing about *which* names are redacted moved: `carriers` is untouched, the
  header allowlist is untouched, and the one value bought back by its own format
  (an OpenSSH public key line) still is. `internal/corpus` already refused this
  exact shape one stage later — "the transcript would then say that two objects
  of the account were one" — and could not see it, because the merge happened in
  the recorder before the sanitiser met two values. Six falsified mutations in
  `tools/falsify/specs/distinct-placeholders.json`.

- **A subnet no longer lands outside the net that contains it** (#354). The
  sanitiser minted each CIDR from one counter, so a recorded Net of
  `10.111.0.0/16` and its Subnet of `10.111.1.0/24` came out two disjoint
  blocks and the emulator answered `400 IpRange … is outside the Net range …`
  where the cloud answered 200 — taking the machine, the volume, the NIC, the
  public IP, the route table link, the NAT service and the load balancer behind
  that subnet with it, about a hundred findings and not one of them the
  emulator's. `mint.planBlocks` now decides every block of a recording in one
  pass, shortest prefix first, and places a child at the offset it held inside
  its parent.

  **It is the third defect of one family, and the family is the lesson.** A
  netmask that stopped being a netmask, an address range that ran backwards, and
  now a subnet outside its net: each was a *relation between* values rather than
  a property of one, which is exactly what a per-value walk cannot see. That is
  why this is a pre-pass beside `learnAddressOrder` rather than a fourth special
  case. `TestASubnetStaysInsideItsNet`.

- **A corpus is replayed against an emulator serving the region it was recorded
  from** (#354). `corpus/accepted.json` gained a `region` (and `zone`) per
  recording, and `feint corpus --check` builds the packs for each file from it.
  At Outscale and Exoscale a region is not a property of the API surface, it is
  which endpoint the client was pointed at, and a pack refuses a create naming a
  zone its deployment does not publish — the #269 invariant. So a
  `cloudgouv-eu-west-1` recording replayed against an `eu-west-2` emulator was
  refused on its own `CreateSubnet` and on everything downstream of it.

  Read from the versioned manifest and never from the environment, which is what
  makes this gate's verdict a property of committed files rather than of the
  runner — the claim `TestTheGatesVerdictDoesNotDependOnTheEnvironment` asserts,
  now true by construction instead of true by coincidence.
  `TestACorpusIsReplayedInTheRegionItNames` holds both halves: named, the
  recording replays clean; unnamed, the same recording is refused.

- **Four defects of the sanitiser, all found by recording a second provider, and
  all of them the kind that manufactures a divergence** (#354). Between them
  they hid the entire Exoscale private-network lifecycle behind about twenty
  findings, none of which was a defect of the emulator. Each is falsified in
  `tools/falsify/specs/sanitised-corpus.json`.

  **`0.0.0.0/0` could not be written down at all.** Masking a zero-length prefix
  yields the same prefix, so the mint handed the value back unchanged, the
  cross-reference against the recording found the same string on both sides, and
  `--sanitise` refused the whole run and wrote nothing — on a recording whose
  only sin was a security-group rule that opens a port to the internet. There is
  one such prefix per family and it selects every address there is, so it now
  survives verbatim: no replacement exists that is both of the same shape and a
  different value. `TestTheDefaultRouteSurvivesSanitisation`.

  **A dotted netmask went through the address mint.** `255.255.255.0` came out a
  host address of the synthetic space, `exoscale/v2.create-private-network`
  answered `400 netmask is not a usable IPv4 netmask` where the cloud answered
  200, and the get, the update, the delete and three operation polls behind it
  answered 404 for that one reason. A netmask is now replaced by a netmask,
  through a map of 1..32 onto itself with no fixed point, so the account's own
  mask never survives and the value written is always a mask.
  `TestANetmaskIsReplacedByANetmask`.

  **An address range came out running backwards.** `start-ip` and `end-ip` were
  minted in the order the walk met them, which is alphabetical, so the artefact
  carried `end` below `start` and the same create answered `400 end-ip is below
  start-ip`. Addresses are now ranked by sorting the recording's own before
  anything is written, so the synthetic ones sort the way the originals did. The
  rule names no field, because `start`/`end`, `first`/`last` and whatever a
  fourth provider calls them are one problem.
  `TestAnAddressRangeStillRunsForwards`.

  **A synthetic address could be minted outside the synthetic space.** Outscale's
  `ReadPublicIpRanges` publishes the provider's whole public address space — 90
  blocks on 2026-08-21, three /20s among 79 /24s — and the counter that reached
  the /20s shifted twelve bits and landed in 198.20.0.0, outside the only IPv4
  block a sanitised transcript may carry. The alphabet refused the artefact,
  which was the right outcome and the wrong message: the fault was arithmetic
  four functions away. `offsetV4` now confines whatever it is given, so a space
  with no room left repeats a replacement and `Sanitise` refuses *that* by name
  — a corpus in which two blocks of an account read as one is the finding #270
  made by hand, and it must never be manufactured here.
  `TestASyntheticAddressStaysInTheSyntheticSpace` and
  `TestASpaceWithNoRoomLeftIsRefusedRatherThanOverrun`.

- **The scan of the committed corpus read every file against Scaleway's
  contract** (#354). The alphabet a sanitised transcript may carry includes the
  values a provider's own description enumerates, and Exoscale's zone names and
  instance families are in Exoscale's document alone: read against Scaleway's,
  the first committed Exoscale corpus reported hundreds of leaks that were
  nothing of the sort. The contract is now the one named by the file's own
  directory. It was true while Scaleway was the only corpus and became a false
  verdict the day a second one landed, which is the shape of every gate that
  stops measuring in silence.

- **The eight divergences the first real Scaleway corpus recorded are gone, and
  `corpus/accepted.json` carries an empty acceptance list** (#355). The gate went
  in carrying them with #355 written beside each; the staleness rule made their
  deletion compulsory the day the emulator stopped producing them. Three causes,
  and saying which was the work — a defect, a declared limit, or the instrument
  lying about itself.

  **Three defects of the emulator, and two of them could not be seen until the
  instrument was fixed.** The default VPC answered no tags where the real one
  answers `tags: ["default"]` — measured twice on 2026-08-21, by `scw vpc vpc
  list` against a real fr-par account and by the recording itself.
  `iam/v1alpha1/API.CreateSSHKey` answered **201 where the wire carried 200**,
  the same family as the two `vpc/v2` creates #270 found by hand, hidden until
  the key's lifecycle could be replayed at all. And an SSH key was published
  **with the comment the client sent**, where the cloud drops it: a key created
  on a real account as `ssh-ed25519 <material> feint-corpus-echo` (98 bytes,
  three fields) reads back as `ssh-ed25519 <material>` (80 bytes, two fields),
  and the re-recorded corpus carries the same fact from the other side — the
  request body and the answer hold two *different* strings at `public_key`. The
  fingerprint does not move, being computed over the decoded blob rather than
  over the line. None of the three is visible to any other control here: no
  client reads the tag, `scw` accepts any 2xx and shows none, and a contract
  states the *type* of `public_key` rather than what the cloud puts in it — the
  last one is a **value**, which the corpus gate records without grading, so it
  is asserted by a test of its own rather than left to a gate.

  **Five findings were one substitution the recorder made.** `feint proxy`
  redacts the value under any JSON key whose *name* contains `key`, so
  `public_key` reached the transcript as `REDACTED`, `sshkey.Parse` refused it,
  the create answered 400, and the read and the delete that followed answered
  404 for that one reason: **the IAM SSH-key lifecycle was unrecordable**. The
  redaction now writes down a value whose own *format* proves it is published —
  an OpenSSH public key line, read by the same `internal/core/sshkey` the packs
  authenticate with — and nothing else moved. Headers keep their allowlist, the
  query keeps the denylist (SigV4 presigns there), and a container named for a
  credential is still replaced whole. That last one costs coverage and the cost
  is stated: `ssh_keys` matches `key`, so `ListSSHKeys` reaches a corpus as one
  string. The distinction is not a preference — a *name* check answers "does
  this look like a credential" and never "is this not one", which is why headers
  are an allowlist; a *value* that identifies itself answers the second question
  directly. Falsified in both directions, five mutations in
  `tools/falsify/specs/forward-proxy.json`.

  **Two findings were the replay grading an inventory as a shape.** `fr-par-1`
  publishes 136 commercial types and this catalogue stocks 18 on purpose, so 127
  entries of a map whose keys are *data* read as 127 missing fields — while
  `feint shapes --check` held the opposite rule on the same artefact and
  reported none of them. The rule now lives once, in `transcript.DataKeyed`, and
  both gates read it: a key of such a map is a value, and values are compared
  only where a pack declares an invariant. Recognition is three or more object
  children with identical key sets, so it under-recognises rather than over-,
  and a field of an entry both sides carry is still compared.

  **One finding was a decision spelled in the wrong dialect.** The pack argued
  the missing `per_volume_constraint.l_ssd` bound to the gate that joins on the
  catalogue key and to no other, so the replay — which joins on the mounted
  operation name — met no refusal and called nine deliberate omissions nine
  divergences. It is now spelled in both. The 118 unstocked types are
  deliberately *not* declined the same way: the only path that names them
  (`servers.*`) also names the 18 that are served, and the omission gate
  publishes such a decline as stale, which fails `tools/conformance/score.sh`.
  Measured, not reasoned.

  `corpus/scaleway/scw-cli.jsonl` was re-recorded on 2026-08-21 through the
  fixed recorder, against the same real fr-par account and under the rules of
  `corpus/README.md`: inventory before, free objects only, everything named
  `feint-corpus-*`, every destruction proved by a read inside the recording, and
  a closing inventory byte-identical to the opening one across all seven
  resource kinds.

<!-- the late-cycle work, written at release time from the merged pull requests -->

### Added

- **A claimed dataplane now needs a witness on the runtime** (#486).
  `mise run conformance:witness` drives the example stacks under
  `--vm incus-ovn`, reads the claims from each pack's own API, and reads the
  witnesses **only** through `incus` — asking the emulator whether the emulator
  is right is the failure this gate exists to remove. It renders four verdicts:
  a pack claiming a firewall and handing no rule set fails by name; a balancer
  whose spec the runtime refused fails rather than sitting registered and empty;
  a resource the API calls `running` with no machine behind it fails; and a pack
  that claims *nothing* is skipped by name with exit 0 — without that last one
  the gate would demand of Scaleway a property it never promised.

  **A green gate proves nothing, so the four reds were obtained on demand**,
  each by planting the defect it names in an out-of-tree copy. Every reader
  plants its positive control first, and three verdicts are distinguished, never
  two: "no witness because nobody could look" prints `NOTHING WAS MEASURED` and
  keeps exit 0. It runs outside the `conformance` aggregate and on the
  `incus-ovn` leg of `runtime-proof.yml`, the same terms as `conformance:ssh`.

- **`/_feint/health` answers which pack delivers balancing**, not only what the
  runtime can do (#481). `capabilities.balancing: true` was true — OVN really
  does forward — and said nothing about whether a pack materialises it, so a
  consumer told by this repository to key on the declared capability would have
  asserted a property Scaleway lacks. `enforced.balancing` now publishes
  `["outscale"]` alone, and `TestEveryPackThatWiresTheBalancerSaysSo` holds the
  declaration against the source **through the AST**: a substring reader would
  have manufactured a false finding against Exoscale, whose comments name
  `machine.Balancer` precisely to say it does not use it.

- **A red scheduled night opens one issue, and a green one closes it** (#502).
  `runtime-proof.yml` had been red on ten of twelve scheduled nights and the
  only trace was a job log — the one place nobody opens without already knowing
  there is a problem. The logic lives in `tools/ci/night-report.sh`, a versioned
  script rather than a `run:` block, because a `run:` block cannot be executed
  outside GitHub Actions and this repository has three scars from CI fixes
  described in comments and never run. It names the failing step and its mode,
  carries the consecutive-green streak #125 waits on, distinguishes an
  infrastructure failure from a measured one, and updates one issue rather than
  opening one per night.

- **A repository declares the cloud it develops against, and one verb reads it**
  (#189–#192, #485). `feint.yaml` carries the provider, the emulator's address,
  the runtime, the environment and the IaC engine; `feint up` reads it.

### Changed

- **No Terraform drives the Exoscale pack — the pinned fork included — until
  `exoscale/terraform-provider-exoscale#573` is fixed upstream** (#525). The
  published provider builds two API clients and only one honours
  `EXOSCALE_API_ENDPOINT`, so a single `apply` or `destroy` splits between the
  emulator and the real cloud. That is not theoretical: a `feint down` run
  without `TF_CLI_CONFIG_FILE` resolved the published provider and five signed
  `GET` requests left for `api-ch-*.exoscale.com` before the run stopped at
  refresh. Nothing was damaged — the requests carried the pack's deliberately
  public fake credentials and were refused at authentication — but the only
  reason nothing worse happened was the order in which `engineEnvironment`
  appends the pack's variables after `os.Environ()`, **a property no test
  asserted**. It has one now, and it protects all three packs.

  The refusal falls **client-side, before anything starts**: an emulator-side
  refusal is worthless here, since those five requests never reached it. It
  names its reason, the upstream issue, and what remains possible — the `exo`
  CLI drives this pack end to end. "Nothing was started" is measured, not
  asserted: no process before or after, and `feint.log`'s mtime identical to the
  byte, since the spawn creates that file before the child can fail.

- **The Outscale suite drives `octl`** (#462), the CLI Outscale now maintains,
  with no operation lost in the move.

- **The emulated public block is a /28** (#464), which stops the suite spending
  four minutes reclaiming addresses.

### Fixed

- **The three packs hand their security groups to the runtime** (#475). Only
  Scaleway did: an Outscale or Exoscale group was served, echoed back and
  reconciled onto nothing, so every port stayed open whatever the group said.
  The reconciliation now lives once in a provider-neutral layer; each pack keeps
  only what it alone knows — its rule vocabulary, who wears what, and the
  expansion of group-sourced rules into their members' /32.

- **A port no rule opens refuses, and two networks stay isolated** (#491). The
  isolation rule set carried a catch-all `allow` at priority 300 where the NIC
  default sits at 100/111, so on any multi-subnet OVN run a forbidden port
  answered — on all three packs, Scaleway included. Both properties now hold
  together, each with its positive control.

- **Exoscale pool members join their pool's private networks** (#492), so the
  app tier's rule set has an interface to attach to; and **a boot publishes the
  state the effect produced** (#484) — a refused start publishes `error` instead
  of leaving the API calling a machine that never started `running`.

- **OVN under concurrency** (#473, #493, #519). Fifteen concurrent subnet
  creates were serialised at 2.3 → 35.5 s strictly linear, and the tenth was cut
  at 60 s while its retry met its own subnet as a conflict; they now finish
  together at 11.6 s. A parallel destroy could leave a running machine, its
  network and its rule set behind `Destroy complete!`; edits and detaches now
  take turns and a teardown waits out what still holds the instance. Fifteen
  parallel deletes paid one uplink rebuild each and now share them: 28.8 s
  becomes 7.0 s.

  Found while measuring, and worth its own line: **two concurrent
  `ApplyFirewall` calls each created the ACL's OVN port group, the loser died on
  the OVSDB constraint, and a NIC was left wearing no rule set at all while the
  API reported it applied.** Nothing else was red; it surfaced because the
  branch brief carried the expected `used_by` counts as invariants.

- **An ordinary `CreateSubnet` no longer severs an active Net peering** (#508).
  Two reconcilers wrote one reachability state with two truths and the last
  writer erased the other's; there is one writer now, and the newborn subnet
  joins the peering it was born into.

- **The Outscale balancer distributes what the host can take, and writes down
  what it withheld** (#483). One backend outside the subnet used to refuse the
  whole spec at WARN, leaving a balancer registered and forwarding nothing.

- **A conformance run ends on the host state its own doorstep accepts** (#521).
  A green run left the shared uplink and one detached rule set behind, and the
  suite's own doorstep then refused the next launch. Traced rather than deduced:
  the default security group is the only one no client call can delete, so its
  host ACL could only ever fall through the pack's `deleteNet` cascade — no
  suite cleanup could have removed it.

- **The ssh suites name what they lack** (#501), instead of dying silently on a
  `grep -c` that returns 1 on zero; and **the peering check measures what it
  claims** (#499), an assertion that had been green only while no security group
  reached the runtime at all.

- **A package step with no route out is said at boot** (#507). Under a runtime,
  a machine on a routed NIC has no outbound route and no resolver, so
  `cloud-init` ended in `status: error` inside a machine log nobody opens.
  Measured by changing one variable: the same cloud-config on a NATed network
  finishes `done` with nginx really installed. The bound is the NIC's shape, not
  the station.

### Ships with seven documented limits

`docs/limits.md` goes from 43 to 50 sections. Seven measured defects ship with
this release rather than being fixed (#518), each written with its dated
measurement, the measured/deduced split its issue draws, what a user should do,
and what would lift it: the station reaching OVN private addresses only through
the network's router (#496), `routing_enabled` defaulting false where the real
cloud answers true (#497), a non-idempotent public route re-laid at reboot
(#498), one documented refusal logged at two different levels (#474),
`feint images resolve` printing an identifier that cannot boot (#476), the `scw`
CLI's recovered panic on every successful `lb acl delete` — an upstream defect
with nothing to fix here (#505), and the Exoscale stack's second plan carrying
two output-only additions (#520).

Shipping them is a decision, not an omission. What each section deliberately
leaves vague is what its issue leaves vague, and it says so.

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

[0.11.0]: https://github.com/stephrobert/feint/releases/tag/v0.11.0
[0.10.0]: https://github.com/stephrobert/feint/releases/tag/v0.10.0
[0.9.0]: https://github.com/stephrobert/feint/releases/tag/v0.9.0
[0.8.0]: https://github.com/stephrobert/feint/releases/tag/v0.8.0
[0.7.3]: https://github.com/stephrobert/feint/releases/tag/v0.7.3
[0.7.2]: https://github.com/stephrobert/feint/releases/tag/v0.7.2
[0.7.1]: https://github.com/stephrobert/feint/releases/tag/v0.7.1
[0.7.0]: https://github.com/stephrobert/feint/releases/tag/v0.7.0
[0.6.0]: https://github.com/stephrobert/feint/releases/tag/v0.6.0
[0.5.0]: https://github.com/stephrobert/feint/releases/tag/v0.5.0
[0.4.1]: https://github.com/stephrobert/feint/releases/tag/v0.4.1
[0.4.0]: https://github.com/stephrobert/feint/releases/tag/v0.4.0
[0.3.3]: https://github.com/stephrobert/feint/releases/tag/v0.3.3
[0.3.2]: https://github.com/stephrobert/feint/releases/tag/v0.3.2
[0.3.1]: https://github.com/stephrobert/feint/releases/tag/v0.3.1
[0.3.0]: https://github.com/stephrobert/feint/releases/tag/v0.3.0
[0.2.0]: https://github.com/stephrobert/feint/releases/tag/v0.2.0
[0.1.0]: https://github.com/stephrobert/feint/releases/tag/v0.1.0
