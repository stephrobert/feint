# Copy one of these, and your pipeline tests a cloud without a cloud

Each file here is meant to be copied into your own repository and to work as it
is. None of them needs a secret, because none of them authenticates to anything:
the emulator parses the shape of a credential and verifies nothing, which is what
lets a real client start against values that mean nothing.

| File | What it gives you |
|---|---|
| [`github-actions/terraform.yml`](github-actions/terraform.yml) | a GitHub Actions job that applies your Terraform, re-plans to prove the emulator read back what it was sent, and destroys |
| [`gitlab-ci/.gitlab-ci.yml`](gitlab-ci/.gitlab-ci.yml) | the same pipeline for GitLab CI, with the emulator as a service |

There is a third way, for a job that does not already run in containers or one
that wants real machines behind `--vm` on a host with Incus:

```yaml
- uses: stephrobert/feint/.github/actions/setup-feint@v0.9.0
  with:
    version: 0.9.0
    provider: scaleway     # exports what the official client needs
- run: terraform apply -auto-approve
```

The action installs the released binary, **verifies its checksum before running
it**, starts the emulator through the lifecycle verbs and waits until it answers.
Its source is [`.github/actions/setup-feint`](../.github/actions/setup-feint/action.yml),
and the comments there say why it exists at all when three shell lines do the
same thing.

## The one assertion worth keeping when you adapt these

```bash
terraform plan -detailed-exitcode
```

An `apply` that succeeds proves the emulator answered. A **second plan that is
empty** proves it read back what the provider sent — which is where an emulator
that answers `200` and stores something else shows up, and it is the assertion
this project runs against every provider on every pull request.

## What these examples do not prove

They exercise the control plane. Nothing here boots a machine, filters a packet
or isolates a subnet: that needs `--vm` and a host with Incus, and
[docs/confidence.md](../docs/confidence.md) says row by row what changes when you
have one — keyed on the capability the runtime declares, never on a mode name.
