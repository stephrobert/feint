# Conformance suites

One directory per provider, because a conformance suite is made of that
provider's real clients and nothing here is shared between them: a different
protocol, different credentials, a different fixture.

## What you need installed

Each script fails loudly when its client is missing rather than skipping in
silence, so you find out at once — but the list is worth having up front:

| Client | Needed by | Install |
|---|---|---|
| `jq` | every suite | your package manager |
| `curl` | every suite | your package manager |
| `scw` | `scaleway/scw-cli.sh` | [scaleway-cli releases](https://github.com/scaleway/scaleway-cli/releases) |
| `terraform` or `tofu` | `scaleway/terraform.sh` | [terraform.io](https://developer.hashicorp.com/terraform/install) or [opentofu.org](https://opentofu.org/docs/intro/install/) |
| `octl` | `outscale/octl.sh` | [octl releases](https://github.com/outscale/octl/releases) (a plain binary, with a checksums file) |
| `exo` | `exoscale/exo-cli.sh` | [exoscale/cli releases](https://github.com/exoscale/cli/releases) |
| `incus` | the `network.sh` suites and `ssh.sh` | [Zabbly packages](https://github.com/zabbly/incus), 6.0.4 or later |
| the machine images | the `ssh.sh` suites | `feint images --vm incus`, once, minutes |

`mise run conformance` runs everything that needs no machine runtime, which is
what CI does. The suites that need one skip themselves at exit 0 when `--vm` is
off, so a partial toolbox never turns into a false pass.

## Running only the part your change touches

**Ask rather than remember**: `mise run testplan` reads the diff and prints the
runs it has earned, cheapest first, along with what they still do not prove
(#564). `mise run prepush` calls it with `--check`, so a path no rule triages
refuses the push instead of quietly earning nothing.

One pass each, measured on 2026-08-27, which is what the plan orders by:

| run | measured |
|---|--:|
| `conformance:leg -- probe` | **0.7 s** |
| `conformance:leg -- exo-cli` | 4.0 s |
| `conformance:leg -- scw-cli` | 7.1 s |
| `conformance:leg -- terraform` | 45.4 s |
| `conformance:leg -- octl` | 141.3 s |
| `conformance:leg -- fields` | 208.1 s |
| `conformance:leg -- runtime` | 590 s |
| `mise run conformance`, no runtime | 256 s |
| `FEINT_VM=incus-ovn mise run conformance` | **1331 s** |

The gap that matters is the last one against the first: whether a change reaches
`internal/core/machine` decides between seconds and twenty-two minutes, and
every other choice in that table is worth tens of seconds.

`mise run conformance:leg -- <leg>` runs one leg. Seven of its names are the
matrix entries of `.github/workflows/conformance.yml`, and
`TestEveryMatrixLegCanBeReproducedLocally` holds the two lists together — a
renamed matrix leg used to leave this script refusing the name that now exists.

The eighth is `runtime`, and it is the one worth knowing about here:

```bash
FEINT_VM=incus-ovn mise run conformance:leg -- runtime
```

It runs the four suites that need real machines — the three `network.sh` and
`outscale/balancer.sh` — and nothing else. That is the part of
`FEINT_VM=incus-ovn mise run conformance` a change to `internal/core/machine`
or to a firewall actually exercises: measured on 2026-08-27, the whole run was
1331 s and those four suites were 675 s of it, so the client suites in front of
them were 656 s that such a change never needed. **The leg itself measured
590 s** on the same station, doorstep to doorstep. It refuses to run with no
runtime configured rather than letting its four suites skip themselves and
reporting success on nothing.

`FEINT_VM` is honoured by every leg, so any of them can be replayed with real
machines; the CI matrix itself runs with none, which is the default here too.

**The images are a prerequisite and not a convenience** (#335). No upstream
image carries an ssh daemon, and since #202 a machine holds exactly the one
address its provider publishes, on a routed NIC with no NAT — so an emulator
that has to fall back to an upstream image boots a machine that has no route to
a package repository and can never install one. `runtime-proof.yml` spent five
consecutive nights failing on that, with the fix in its own log. The `ssh.sh`
suites now ask `guard_images` before they register a key: they refuse in a
twentieth of a second, naming `feint images`, rather than timing out on an ssh
error that blames the address. Building is never automatic here — it launches a
container on your host and takes minutes, which this project asks for rather
than assumes; CI runs the command as a step of its own.

Two client quirks are worth knowing before you debug one:

- **`octl` takes the endpoint WITH its path**, `http://host/api/v1`. It reads
  that from `osc-sdk-go`, whose default endpoint template is
  `%s://api.%s.outscale.com/api/v1`, so the path is part of the value. The
  archived `oapi-cli` wanted the opposite — the bare host, appending
  `/api/v1/<Call>` itself — and that inversion is the single thing most likely
  to send you debugging a 404 that says nothing about the emulator.
- **`octl` reshapes its answers unless you ask for `-o raw`.** The default
  `-o json` unwraps `{"Nets":[…],"ResponseContext":{…}}` to a bare list, and a
  suite asserting on that measures the CLI rather than the emulator. Every call
  in `outscale/octl.sh` goes through one wrapper that pins `-o raw`, and one
  assertion at the top of that file is the witness proving the two forms still
  differ.
- **`exo` has no endpoint flag and no endpoint environment variable.** It is
  redirected only through the `endpoint` key of its own configuration file, and
  that value must carry the `/v2` suffix, because the CLI concatenates it with
  the route it wants. Both facts were measured with a logging proxy; neither is
  documented. The suite writes that file itself.

The layout is the point. The scripts were flat and generically named while every
one of them was Scaleway-only — `terraform.sh` built a Scaleway configuration,
`network.sh` called `/instance/v1` and `/vpc/v2`, `fake-credentials.env` held
`SCW_*`. A second provider would have had to rename all of them.

## Scaleway

| Script | What it proves |
|---|---|
| `scaleway/scw-cli.sh` | the official CLI drives create, list, get, stop, delete, and the security groups |
| `scaleway/terraform.sh` | the official provider applies, reads back without drift, and destroys |
| `scaleway/network.sh` | addresses, isolation and firewall rules on real machines |
| `scaleway/ssh.sh` | an IAM key reaches the guest and a real ssh login succeeds |

`network.sh` skips itself with no machine runtime configured, so CI stays
runtime-free; `ssh.sh` is not in the suite for the same reason and runs through
`mise run conformance:ssh`.

## Outscale

| Script | What it proves |
|---|---|
| `outscale/octl.sh` | the official CLI drives create, read, delete, and both error paths decode |
| `outscale/network.sh` | the block a Subnet declares exists on the host, with the range asked for, and goes away with it |

`octl` is the client, and since 2026-08-25 it is the only live one (#460):
`outscale/oapi-cli` and `outscale/osc-cli` are both `archived: true` on the
GitHub API, read-only, with "Deprecated Outscale CLI" in their own description.
`osc-cli` also addresses `/api/latest/<Call>` where the current API is
`/api/v1/<Call>`, so pointing it here would fail for a reason that says nothing
about the emulator.

It has no `--endpoint` flag, and wants the endpoint WITH the API path. Both
traps are described once, at the top of this file.

Two rules the suite holds and a reader should know before adding a case:
**`iaas api <Call>`, never an alias** (`octl iaas net list` resolves to
`octl iaas api ReadNets`, and an alias is a convenience of the CLI where the API
is what is measured), and **`-o raw` everywhere**.

`octl.sh` proves the addressing arithmetic with no runtime at all, which a
server storing JSON could also pass; `network.sh` measures the other half and
therefore skips itself with `--vm off`, like its Scaleway namesake.

## Exoscale

| Script | What it proves |
|---|---|
| `exoscale/exo-cli.sh` | the official CLI creates an instance, which means the four reads it issues first are served |

`exo compute instance create` issues, before it posts anything: `GET /zone`,
`GET /instance-type`, `POST /ssh-key`, `GET /template`. Every one of those was
declined by this emulator until a logging proxy showed them going past — a unit
test would never have found them, which is the reason this suite exists.

The redirection trap is the one described at the top of this file: no flag, no
environment variable, only the `endpoint` key of the CLI's own configuration
file, and that value must carry the `/v2` suffix. The suite writes that file
itself.

This suite is the whole of the pack's evidence, and it is enough: Exoscale is
*starter* since EXO-2. There is no Terraform suite here and there will not be
one until upstream moves — the provider reaches the real cloud with half its
calls, so this emulator refuses it. See
[docs/limits.md](../../docs/limits.md#the-exoscale-terraform-provider-is-refused-and-why).

Credentials are fake, well-formed and deliberately public: the SDKs validate
their shape even though the emulator ignores their value.

## The recorder, watched by the real clients

Two scripts here have the proxy as their subject rather than a pack. Neither is
part of `mise run conformance`: recording is a human's job, and the thing worth
recording — a real cloud — needs an account that will never exist in a runner.

| Script | What it proves |
|---|---|
| `proxy.sh` | two observers of one `scw` run agree: what `feint proxy --upstream` wrote down and what `/_feint/conformance` counted inside the emulator are the same set of operations |
| `forward.sh` | a real client reaching its endpoint **by name** is terminated by `feint proxy --forward`, recorded, and served by the emulator — no namespace, no `/etc/hosts` edit, no privileged port (#357) |

`forward.sh` points `scw` at `https://api.scaleway.test`, and that choice is the
suite's safety rather than a detail. `.test` is a reserved TLD (RFC 6761): no
resolver answers for it, so if the proxy is not what carries the traffic — it
died, the mapping is missing, the client ignored `HTTPS_PROXY` — the run fails
with `no such host` and nothing leaves the machine. Pointing it at the real
`api.scaleway.com` would prove the same mechanism while making the proxy the only
thing between a broken suite and the internet, which is the shape `guard.sh`
exists to refuse.
