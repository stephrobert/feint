# Feint 1.0 — The local cloud lab

This document is the focused execution plan for the next major release. The
long-form project history remains in [roadmap.md](roadmap.md); the issue that
owns this cut is [#847](https://github.com/stephrobert/feint/issues/847).

## The release claim

> **Run it. Break it. Reset it. Prove it.**

Feint 1.0 is not gated by a route count. It is the release where a user can take
a real Scaleway, OUTSCALE or Exoscale infrastructure workflow and use Feint as a
local cloud lab rather than as an API fixture.

The reference workflow is:

```text
real Terraform/OpenTofu or official CLI
                │
                ▼
             feint run
                │
      ┌─────────┼─────────┐
      │         │         │
 control plane  │    deterministic faults
      │         │
      ▼         ▼
 desired topology
      │
      ▼
 runtime reconciliation
      │
      ├── Incus / OVN
      └── local Object Storage
      │
      ▼
 normalized observation
      │
      ├── topology debugger
      ├── explain/evidence
      └── checkpoint/reset
```

The release is valuable only if that workflow is simpler than assembling those
pieces by hand.

## Why this cut

Demand in comparable emulators repeatedly clusters around workflow features,
not route counts:

- LocalStack users asked for functional local compute so the same Terraform can
  drive local development and CI smoke tests:
  https://github.com/localstack/localstack/issues/691
- losing local state across restart makes the tool unusable for some workflows:
  https://github.com/localstack/localstack/issues/6281
- Floci users describe zero-copy hot reload as a "game-changer for iteration
  speed":
  https://github.com/floci-io/floci/issues/553
- a reset/nuke primitive is requested as the cloud equivalent of transaction
  rollback between tests:
  https://github.com/floci-io/floci/issues/803
- deterministic fault injection is requested to make retry and recovery paths
  reproducible:
  https://github.com/floci-io/floci/issues/4623

Feint already has the unusual pieces needed to turn those requests into one
coherent product: real provider clients, measured error dialects, snapshots,
runtime ownership, Incus/OVN witnesses and Runtime Contract v1.

## Workstreams

### 1. One-command lab lifecycle — #848

`feint run -- <command>` owns start, provider environment, child process,
evidence receipt and cleanup. It reuses the existing lifecycle instead of
inventing a second one.

The command is the adoption door for 1.0.

### 2. Hardcoded endpoint capture — #849

A run advertised as local must not silently leave for a real cloud. Some clients
construct product endpoints themselves, so the local lab needs an opt-in,
session-scoped interception mechanism with no durable DNS or trust-store
mutation.

This is a prerequisite for the Object Storage promise, not a generic transparent
proxy feature.

### 3. Object Storage — #850

One neutral local S3 data plane sits behind provider-specific doors. The target
surface is driven by real clients and the surveyed stacks, not by the size of the
S3 specification.

Bytes must really exist, survive the documented checkpoint semantics and be
cleaned with the rest of the lab.

### 4. Replayable faults — #851

The existing `/_feint/faults` primitive gains deterministic schedules,
replayable campaigns and the transport failures that cannot be represented as
an HTTP status.

Injected outcomes remain excluded from conformance evidence.

### 5. Topology debugger — #852

A machine-readable topology view explains routing, isolation, firewall, public
addresses and load balancing from the same normalized observation used by the
runtime contract.

`feint ui` renders this model; the UI is not the source.

### 6. Checkpoint and reset — #853

Snapshots are the storage primitive. Checkpoint/reset is the lifecycle
guarantee: restore the store, reconcile the runtime, restore local object bytes
and remove extra Feint-owned resources without touching foreign ones.

### 7. Drift and proof — existing work

- #193 adds out-of-band mutation so Terraform can see real drift.
- #487 exposes the evidence behind one operation.
- the evidence-baseline work from #488 remains the consumer-facing proof guard.
- #490, the scenario DSL, is deliberately deferred until the individual
  primitives have real downstream use.

## Dependency order

```text
Runtime Contract v1 / normalized observation
                 │
        ┌────────┴────────┐
        │                 │
      #849              #852
 endpoint capture       topology
        │
      #850
 object storage
        │
        └────────┐
                 │
#193 ──┐       #853
mutate │       reset/checkpoint
       │          │
#851 ──┼──────────┼──> #848 feint run
faults │          │
       └──────────┘
                 │
               #854
          release acceptance
```

Work can overlap where the dependency is not real. The arrows are contracts,
not a schedule.

## What 1.0 explicitly does not buy

- managed Kubernetes emulation (Kapsule/SKS);
- every S3 operation;
- a scenario DSL;
- a general-purpose private cloud controller;
- hidden telemetry;
- system-wide DNS or CA installation as the normal path;
- support claims inferred from unit tests or HTTP 200s.

Those may be valuable later. None is required to make the local-cloud-lab
workflow real.

## Release gate — #854

The tag is cut only when a stranger can, from released artefacts on a clean
supported Linux host:

1. run a named public stack with no cloud account or real credential;
2. exercise a runtime-backed path where the selected mode claims one;
3. keep Object Storage local for the reference workflow;
4. inspect an allowed and a denied path through the topology debugger;
5. checkpoint the lab;
6. inject a deterministic provider-shaped fault and observe the real client's
   behaviour;
7. mutate one resource out of band and have Terraform/OpenTofu observe drift;
8. reset to the checkpoint and recover the same normalized observable state;
9. leave no Feint-owned runtime object behind on normal cleanup.

Each pillar carries a falsification proving its acceptance test can go red.

The adoption half is independent: before the tag, the downstream evidence
register must contain at least five independently chosen public configurations
run by someone other than the maintainer, or five externally owned repositories
with reproducible Feint integration. The longer-term goal in
[adoption.md](adoption.md) remains ten real configurations.

## The product demo

If the release cannot be demonstrated in a few commands, the release is not
finished:

```bash
feint run --provider scaleway --runtime incus-ovn -- terraform apply

feint checkpoint healthy
feint fault add scaleway.instance.CreateServer --status 429 --nth 3
terraform apply

feint topology explain web db --protocol tcp --port 5432
feint mutate securitygroup web --open tcp/22 0.0.0.0/0
terraform plan

feint reset healthy
```

The exact syntax may move while implementation is measured. The workflow may
not.
