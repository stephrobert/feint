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
| `oapi-cli` | `outscale/oapi-cli.sh` | [oapi-cli releases](https://github.com/outscale/oapi-cli/releases) (AppImage) |
| `exo` | `exoscale/exo-cli.sh` | [exoscale/cli releases](https://github.com/exoscale/cli/releases) |
| `incus` | the `network.sh` suites and `ssh.sh` | [Zabbly packages](https://github.com/zabbly/incus), 6.0.4 or later |
| the machine images | the `ssh.sh` suites | `feint images --vm incus`, once, minutes |

`mise run conformance` runs everything that needs no machine runtime, which is
what CI does. The suites that need one skip themselves at exit 0 when `--vm` is
off, so a partial toolbox never turns into a false pass.

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

- **`oapi-cli` takes the bare host.** It appends `/api/v1/<Call>` itself, so
  passing `http://host/api/v1` makes it request `/api/v1/api/v1/<Call>` — a 404
  that looks exactly like a missing route.
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
| `outscale/oapi-cli.sh` | the official CLI drives create, read, delete, and both error paths decode |
| `outscale/network.sh` | the block a Subnet declares exists on the host, with the range asked for, and goes away with it |

`oapi-cli` is the client, not `osc-cli`: the latter is deprecated and addresses
`/api/latest/<Call>` where the current API is `/api/v1/<Call>`, so it would fail
for a reason that says nothing about the emulator.

It also has no `--endpoint` flag, and wants the bare host rather than the API
path. Both traps are described once, at the top of this file.

`oapi-cli.sh` proves the addressing arithmetic with no runtime at all, which a
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
