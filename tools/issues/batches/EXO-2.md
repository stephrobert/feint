---
id: EXO-2
title: "EXO-2: instance lifecycle, security groups, elastic IPs — the preview label comes off"
labels: roadmap, exoscale, conformance
milestone: "Wave 2 — first Terraform proofs"
after: EXO-1
size: M
---
Batch 2 of `docs/roadmap-exoscale-iaas.md`; wave 2 of `docs/roadmap.md`.

**Delivers.** The pack serves create, read and delete, and nothing else:
`exo compute instance stop` fails against the emulator today. Serve
`start-instance`, `stop-instance`, `reboot-instance`, `reset-instance`,
`scale-instance`, `resize-instance-disk`, instance protection add/remove
(`get-console-proxy-url` served or declined with a reason — there is no console
to proxy); security groups and their rules; anti-affinity groups; elastic IPs
with instance attachment. Every mutation mints an Operation with the correct
`reference.command` — the provider calls `client.Wait` 89 times and reads it.

**Where the work lands.** `internal/providers/exoscale/machines.go` and new
files; `tools/conformance/exoscale/exo-cli.sh`.

**Depends on.** EXO-1, for legibility only. Every new lifecycle path takes
`machine.Binding.Serialise` and proves it with a concurrency test.

**Closed by.**

```bash
mise run conformance
# exo: stop, start, reboot; scale and resize-disk; a delete refused while the
# instance is protected; a security group rule round-tripped; an anti-affinity
# group; an elastic IP attached, published on the instance, detached, deleted
```

**The Terraform fixture is not part of this batch and cannot be.** It was
specified as `tools/conformance/exoscale/terraform/` plus a
`conformance:terraform:exoscale` task, on the assumption that the Exoscale
Terraform provider could be pointed at an emulator. It cannot: it honours
`EXOSCALE_API_ENDPOINT` for its egoscale v3 client and builds a v2 one with no
endpoint option, so an apply splits between the emulator and a paying account,
and the emulator refuses that client rather than serve half of it. Filed as
[exoscale/terraform-provider-exoscale#573](https://github.com/exoscale/terraform-provider-exoscale/issues/573);
the measurement and a patched build are in
`docs/limits.md`. `exo` proves the same behaviour, and it is the official
client.

**This is the batch that removes the README's *preview* label**, in the same
commit, on what `exo` proves. Exoscale becomes *starter*, alongside Outscale.
What still separates it from *usable* stays in the generated coverage tables,
which cannot flatter.

**Done means all four** — the "When a batch is done" conditions of `docs/roadmap.md`:

- [ ] `mise run check` passes (gofmt, vet, golangci-lint, `go test -race`)
- [ ] Every new route declares its upstream `Route.Operation`, and `TestEveryRouteDeclaresAnOperation` proves it
- [ ] `mise run conformance` passes, including this batch's own new evidence
- [ ] `tools/drift/gate.sh check` returns 0, this batch's operations served or declined with a reason
