# Copy one of these, and your pipeline tests a cloud without a cloud

Each file here is meant to be copied into your own repository and to work as it
is. None of them needs a secret, because none of them authenticates to anything:
the emulator parses the shape of a credential and verifies nothing, which is what
lets a real client start against values that mean nothing.

| File | What it gives you |
|---|---|
| [`stacks/`](stacks/) | **three complete platform stacks** — multi-VPC, multi-machine, golden images, block storage — the Scaleway and Outscale ones applied against Feint on every pull request, the Exoscale one by hand behind the patched provider |
| [`github-actions/terraform.yml`](github-actions/terraform.yml) | a GitHub Actions job that applies your Terraform, re-plans to prove the emulator read back what it was sent, and destroys |
| [`gitlab-ci/.gitlab-ci.yml`](gitlab-ci/.gitlab-ci.yml) | the same pipeline for GitLab CI, with the emulator as a service |
| [`compose/compose.yaml`](compose/compose.yaml) | the emulator beside your application for the length of a `docker compose up`, with a healthcheck the app waits on |

If you want to see Feint hold up under something that looks like production
rather than under a snippet, start with [`stacks/`](stacks/). They are examples
and tests at once, and they have already found two defects nothing else saw.
The same method turned outward found four more: fifteen strangers' published
stacks, applied and scored in [`stacks/surveyed.md`](stacks/surveyed.md).

## From a Go test

An emulator enters a test suite through the test framework, not through a README:

```go
func TestProvisioning(t *testing.T) {
    cloud := feinttest.Start(t)          // starts the image, waits, cleans up

    client, _ := scw.NewClient(
        scw.WithAPIURL(cloud.Endpoint()),
        scw.WithAuth("SCWXXXXXXXXXXXXXXXXX", "11111111-1111-1111-1111-111111111111"),
    )
    // …drive the official SDK against it
}
```

[`feinttest`](../feinttest/) is that package. It drives the container runtime
through its command-line tool rather than importing a Docker client, so it adds
**no dependency** to anybody who imports it — the same reasoning that keeps
`go.mod` at three lines, and the same pattern the Incus driver already uses.
It asks the kernel for a free port, so two packages testing in parallel do not
fight, and it skips rather than fails when no runtime is installed.

There is a third way, for a job that does not already run in containers or one
that wants real machines behind `--vm` on a host with Incus:

```yaml
- uses: stephrobert/setup-feint@v1
  with:
    version: 0.9.0
    provider: scaleway     # exports what the official client needs
- run: terraform apply -auto-approve
```

The action installs the released binary, **verifies its checksum before running
it**, starts the emulator through the lifecycle verbs and waits until it answers.
Its source is [`.github/actions/setup-feint`](../.github/actions/setup-feint/action.yml),
and the comments there say why it exists at all when three shell lines do the
same thing. [`stephrobert/setup-feint`](https://github.com/stephrobert/setup-feint)
is only the Marketplace address for that file — the Marketplace requires
`action.yml` at a repository root — and a gate in this repository's CI fails
every pull request while the published `v1` differs from the source here.

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
