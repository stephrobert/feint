# `feint.yaml`: one environment, declared once

`feint serve --vm incus-ovn --contracts contracts` is a command somebody types,
remembers, and types differently next week. The flags that decide what a
colleague's emulator can do — which runtime, which provider, which contracts,
which state to start from — live in shell history and in a README paragraph, and
a repository that needs a particular one has no way to say so.

`feint.yaml` is that way. `feint up` reads it and brings the environment up;
`feint down` takes it back down. A declaration nothing reads is a comment, which
is why the two arrived together.

## From `git clone` to a `terraform apply` that passes

This is the whole path, and it is the one the three stacks under
[`examples/stacks/`](../examples/stacks/) are checked against.

```bash
git clone https://github.com/stephrobert/feint && cd feint
mise run build                       # or: go install github.com/stephrobert/feint/cmd/feint@latest

cd examples/stacks/scaleway          # a feint.yaml sits beside main.tf
../../../feint up
```

That single command does six things, in order, and says each one out loud:

1. **checks what the host can deliver, and refuses before anything starts.** A
   declaration naming `incus-ovn` on a host with no OVN wiring is refused here,
   naming the missing half — not four minutes later, half-applied;
2. **starts the emulator** with the address, the state and the contracts the
   file names. It is an ordinary `feint start`, so `feint status`, `feint logs`
   and `feint stop` all know about it;
3. **exports the client environment** from the pack itself — the same variables
   `feint env <provider>` prints, endpoint form included;
4. **runs the engine** in the directory the file names, in place, with its
   output passed straight through;
5. **waits for each `ready:` condition**, each with a deadline and each named
   while it waits;
6. **prints the endpoints** and what proved them.

Then, when you are done:

```bash
../../../feint down
```

which destroys what the declaration built and stops the emulator, saying what it
discards.

### The Exoscale stack is suspended: `feint up` refuses its engine

No Terraform for Exoscale, until upstream fixes
[#573](https://github.com/exoscale/terraform-provider-exoscale/issues/573):
the published provider builds two clients and only one honours
`EXOSCALE_API_ENDPOINT`, so an apply or destroy **splits** between the
emulator and a paying account. #525 measured exactly that from `feint down`
on this stack — five signed requests left for `api-ch-*.exoscale.com` — so
the refusal now falls at the doorstep, before anything starts:

```bash
cd examples/stacks/exoscale && feint up
# feint: `iac.engine: terraform` is refused for `cloud.provider: exoscale`: …
# Nothing was started. Two ways on: …
```

That refusal is the stack's declared state, not a defect. The exo CLI drives
the Exoscale pack end to end (`feint up --no-iac`, then
`eval "$(feint env exoscale)"` and `exo`), and the whole history — the split,
the fork that once proved the surface holds, and the condition of Terraform's
return — is dated in
[limits.md](limits.md#the-exoscale-terraform-provider-is-refused-and-why).

A declaration cannot lift the emulator-side refusal either:
`FEINT_EXOSCALE_ALLOW_TERRAFORM` is refused by name in `emulator.env`, because
this stack's own `feint.yaml` carried it on the day of #525 and armed it for
whatever provider the engine resolved. The variable survives as a hand-export
to `feint serve`, for verifying a candidate upstream fix, and nothing else.

## What the file is, and what it is not

| what | says | where it lives |
|---|---|---|
| `feint.yaml` | how to bring the environment up | this project |
| Terraform / OpenTofu | what the infrastructure is | your repository |
| a snapshot | what the state currently is | an artefact |

The day this file grows a block describing a subnet, this project has started
rewriting Terraform badly. The day it grows a `packages:` list, it has started
rewriting Devbox badly. Both refusals are structural rather than advisory: the
schema is a closed table, and **a key it does not name is refused by name at
load** — with the list of the keys that block does take.

Two consequences worth stating before the reference:

- **Nothing is accepted and then ignored.** A key this schema knows and no verb
  reads yet is said out loud at load. A file that accepts everything and applies
  half of it is the exact lie this project exists to avoid.
- **Nothing here describes what a provider can do.** The catalogue, the image
  table and the login of a machine live in the packs. A second place describing
  them would be a second place to keep in agreement.

## The shortest one that works

```yaml
version: 1

cloud:
  provider: scaleway

iac:
  engine: terraform
  directory: .
  vars:
    endpoint: ${feint.endpoint}
```

Everything else has a default, and `runtime.mode` defaults to `off` because
starting machines is a side effect this project asks for rather than assumes.

## The four traps this replaces

Every one of these was paid for by hand, on 2026-08-24, applying these same
three stacks. Each was a parameter somebody had to reconstitute by reading a
script.

1. **`examples/stacks/outscale` carries a local module**, so copying `*.tf` on
   their own does not run. The engine runs in the declared directory, in place.
2. **Scaleway and Outscale declare an `endpoint` variable** whose default is
   `127.0.0.1:4599`. Pointed at a port nothing listens on, Terraform blocks to
   its own ceiling, the plugin dies, and the message blames the provider.
   `iac.vars` carries it, with `${feint.endpoint}` — the one substitution this
   file has, so the address is written once.
3. **Exoscale declares no such variable**: its endpoint travels in
   `EXOSCALE_API_ENDPOINT`, and the `/v2` path belongs to the value. That comes
   from the pack's own `Env`, never from a field here, so no reader has to learn
   which provider wants its path inside the value and which does not. The
   mechanism is `TF_VAR_`, not `-var`, and the difference is measured: an
   undeclared `TF_VAR_` is ignored, where `-var endpoint=…` fails outright on a
   stack that declares no such variable.
4. **FEINT_\* knobs are read server-side**, so exporting one after the emulator
   started leaves it unread. `emulator.env` sets them before the spawn — with
   one name refused outright since #525: `FEINT_EXOSCALE_ALLOW_TERRAFORM`,
   which a stack declaration once armed for whatever provider the engine
   resolved, is a hand-export to `feint serve` or nothing.

## Machines, and the refusal that comes before them

`runtime.mode` is the `--vm` values and nothing else. It defaults to `off`,
because starting machines is a side effect this project asks for rather than
assumes — and the three example stacks ship with `off`, which is what lets them
run on a CI runner with nothing installed.

Flipping it is the whole difference between a control plane and a cloud. The
same Scaleway declaration, on a station with Incus and OVN, measured on
2026-08-25:

```bash
$ feint up --runtime incus-ovn
examples/stacks/scaleway/feint.yaml: scaleway, runtime off, terraform in .
  runtime off, overridden to incus-ovn by --runtime
  runtime.images: 1 of 1 present on this station
...
Apply complete! Resources: 50 added
  ok: resource:instance/server:6
```

and on the host, while it was up: six running containers, three OVN networks
carrying the blocks the stack declares (`10.30.1.0/24`, `10.30.2.0/24`,
`10.40.1.0/24`), three rule sets marked `feint security group` used by six
interfaces between them, and one isolation ACL per network. After `feint down`,
nothing of it remains.

**`up` never downgrades on its own.** A declaration naming a runtime the host
cannot deliver is refused *before anything is started*, naming the missing half
— a developer who believes their subnets are separate and finds out otherwise in
production is the exact failure this project exists to prevent:

```text
feint: --vm incus-ovn requested but this host cannot deliver it:
  isolation: the daemon did not answer for network.ovn.northbound

Nothing was started. Three ways on, in the order they cost:
  feint doctor --vm incus-ovn        the whole diagnosis, including what to install
  feint up --runtime off              run this environment without machines, and say so
  feint.yaml                          change runtime.mode to what this host delivers
```

A guard with no way past it gets worked around by copying the emulator, which is
why the refusal carries the doors rather than only the wall.

### `runtime.images`, and the three answers it gives

The declaration names the images this environment boots. `up` checks the station
before anything starts and never builds one — a build launches a container and
takes minutes, which is a side effect of its own. Three answers, and the third
is the one a two-way check would get wrong:

- **present** — `runtime.images: 1 of 1 present on this station`;
- **in the warm-up set and absent** — refused, with `feint images --only <name>`;
- **outside the warm-up set** — announced, never refused: the boot path derives
  an image on demand for a family and version `feint images` does not build, so
  refusing it would refuse something the runtime can do. The line says the first
  boot will cost minutes.

Under `mode: off` nothing boots, and the check says it is skipping for that
reason rather than passing quietly.

## Asking for less on purpose

```bash
feint up                      # what the file asks for
feint up --runtime off        # deliberately less, and it says so
feint up --no-iac             # the control plane only
```

Asking for something other than what the file declares is a flag, and the run
prints the override. `--no-iac` skips the engine, and therefore skips the ready
conditions that describe what the engine builds — out loud, and the summary then
says `proved: nothing` rather than listing conditions nothing evaluated.

## The field reference

Generated from the schema itself, on the `feint docs --check` rail: a field
added to `internal/environment` and a page that never learned about it fails the
gate. The sentence a field carries lives on the field.

<!-- environment:start -->
<!-- Generated by `mise run docs:coverage`. Do not edit by hand. -->

| field | takes | default | read by | what it says |
|---|---|---|---|---|
| `version` | a number | — | `feint up`, `feint down` | The schema version. Only 1 is read; another is refused naming both. |
| `cloud.provider` | a provider name | — | `feint up`, `feint down` | The pack whose client environment `up` exports before running the engine — the same variables `feint env <provider>` prints, including the endpoint form that provider's clients want. Refused when the binary mounts no such pack. |
| `cloud.projects` | a list of project names | — | `feint up` | The projects the emulated account holds, in order: `serve --projects`. A stack whose `project_name` is its own production project names it here, and the emulator holds it rather than answering that it exists because somebody asked (#572). Omitted, the pack serves its own single default project. |
| `emulator.addr` | a listen address | `127.0.0.1:4599` | `feint up`, `feint down` | Where the emulator listens: `serve --addr`. |
| `emulator.state` | a path | — | `feint up` | The JSON file the store is loaded from and persisted to: `serve --state`. Relative to the declaration's own directory. |
| `emulator.contracts` | a directory | — | `feint up` | The API descriptions every response is checked against: `serve --contracts`. Relative to the declaration's own directory. |
| `emulator.log_level` | error, warn, info or debug | `info` | `feint up` | `serve --log-level`, which is what `feint logs` then shows. |
| `emulator.cleanup` | true or false | `false` | `feint up` | Remove the machines and networks the run created before exiting: `serve --cleanup`. |
| `emulator.env` | a block of FEINT_* variables | — | `feint up` | The environment the emulator's own process starts with — FEINT_BOOT_IMAGES and the other FEINT_* knobs, which are read server-side and so cannot be exported after the start. FEINT_* only: a declaration that could set any variable of a process it spawns would be a different kind of file. One name is refused outright: FEINT_EXOSCALE_ALLOW_TERRAFORM, because #525 measured a stack declaration arming it for whatever provider the engine resolved — an escape hatch that consequential is exported by hand, in the shell that runs `feint serve`, never carried by a file that travels. |
| `runtime.mode` | off, incus, incus-vm, incus-ovn, auto | `off` | `feint up` | What backs a powered-on server: the `--vm` values and nothing else. Absent means `off`, because starting machines is a side effect this project asks for rather than assumes. `up` checks the host can deliver the named mode before it starts anything, and refuses rather than downgrading. |
| `runtime.images` | a list of family/version | — | `feint up` | The machine images this environment needs present. `up` checks them and refuses naming what is missing and the `feint images` command that builds it; it never builds them itself, because a build takes minutes. Ignored when `runtime.mode` is `off`, where nothing boots. |
| `snapshot.load` | a snapshot name | — | `feint up` | A snapshot loaded once the emulator answers, so this environment starts from a known state rather than from nothing: `feint snapshot load <name>`. The name, never a path — snapshots live where `feint snapshot list` says they live. |
| `iac.engine` | terraform or opentofu | — | `feint up`, `feint down` | The engine `up` runs and `down` destroys with. Absent means this environment declares no infrastructure, and `up` brings the control plane up and stops there. |
| `iac.directory` | a directory | `.` | `feint up`, `feint down` | Where the engine runs, relative to the declaration's own directory. The engine runs there in place — a copy of `*.tf` would leave a local module behind, which is one of the four things this file exists to make impossible. |
| `iac.vars` | a block of variables | — | `feint up`, `feint down` | Engine variables, exported as `TF_VAR_<name>`. The value `${feint.endpoint}` is replaced by the emulator's endpoint, and it is the only substitution this file has: a stack that takes its endpoint through a variable then carries the address once, here. |
| `ready` | a list of conditions | — | `feint up` | What `up` waits for before it says the environment is up, each with a deadline and each said out loud while it waits. Three forms: `http:<path>` (the emulator answers below 400), `tcp:<host>:<port>` (a connection is accepted), and `resource:<kind>[:<count>]` (the emulator's own inventory holds at least that many). Every one is asserted against the emulator, never against the engine's state file. |

### What this file deliberately does not carry

| field | why not |
|---|---|
| `emulator.coverage` | `--coverage` is a flag of `feint serve` alone, which `feint start` refuses and `feint up` composes; run `feint coverage` against the running emulator instead |
| `emulator.expose_to_network` | putting an emulator that accepts every credential on the network is a decision the person at the keyboard makes, never one a file they cloned makes for them; type `feint serve --expose-to-network` and read SECURITY.md first |
| `emulator.shapes` | `--shapes` is a flag of `feint serve` alone, which `feint start` refuses and `feint up` composes; run `feint shapes --check` against the running emulator instead |
| `proxy` | a recorder needs a real cloud endpoint and real credentials, which is the one thing this file must never carry; run `feint proxy --upstream … --record …` beside the emulator |

### The ready conditions

- `http:<path>`
- `tcp:<host>:<port>`
- `resource:<kind>[:<count>]`
- `service:<resource name>:<port>`

A condition that is not one of those forms is refused at load, with the list.
<!-- environment:end -->

## What is not here yet

- **A transmissible bundle** ([#191]): `feint env export` / `import`, so an
  environment attaches to a bug report as one file. The declaration is the half
  that was missing; the bundle wraps it around a snapshot.
- **Assertions that skip out loud** ([#192]): a suite that cannot prove
  isolation because the host has no OVN should say which assertions it therefore
  skipped, keyed on the capability the runtime declares rather than on a mode
  name. `--runtime` is the half of it that exists today.

[#191]: https://github.com/stephrobert/feint/issues/191
[#192]: https://github.com/stephrobert/feint/issues/192
