# Where to go in this directory

Four questions, and the pages that answer each one. This is a guide, not a
listing: GitHub already shows you every file above, and a page that names all of
them would only tell you what you can already see. What it cannot tell you is
which one to open, which is the whole job here.

The English pages are the source. Where a French translation exists it is named
beside its original, and the English one prevails if the two disagree.

**The handbook lives outside this repository**, in both languages, and it is what
the repository's own homepage field points at:
[English](https://blog.stephane-robert.info/en/docs/cloud/outils/feint/) ·
[Français](https://blog.stephane-robert.info/docs/cloud/outils/feint/).
It carries screenshots and a maturity table per provider — a thing this directory
does not do, and the reason it is named here rather than left to be found.

What it cannot be is checked. Nothing offline can tell you whether an external
page still says what it said, so it is listed as a known surface rather than
gated — the same problem as #599, one field over.

## I want to run something

Start at the [quick start](../README.md#quick-start): four commands, a stack
small enough to read, and no cloud account.

- [install.md](install.md) — the released binary and the signature to check it
  against, the container image, Homebrew, and the prerequisites per
  distribution. Building from source is in the README, beside the other
  install routes.
- [environment.md](environment.md) — every field of `feint.yaml`, generated from
  the schema the code validates against, so a field added without a doc update
  fails a gate rather than a reader.

## I want to know whether it answers my question

- [confidence.md](confidence.md) — one table: what you can reasonably validate
  against this emulator, in your vocabulary. Read this before installing
  anything. Each verdict points at the suite that proves it or the limit that
  refuses it.
- [limits.md](limits.md) — the record behind every **no**, measured rather than
  assumed. It is long on purpose and not meant to be read through: search it for
  the section a verdict sent you to.
- [routes.md](routes.md) — the fine view, generated: what every mounted operation
  has earned, operation by operation.

## I want to know what "proven" means here

- [conformance.md](conformance.md) ([fr](conformance.fr.md)) — the chain from a
  real client to a verdict, and what a green run still does not prove.
- [clients.md](clients.md) — which client drives which pack, at which version,
  and the workflow that says so.

## I want to change it

- [architecture.md](architecture.md) — the packs, the neutral core, and the
  boundary between them.
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — how a change is proved before it is
  proposed.
- [proxy.md](proxy.md) — record a real cloud, read the recording, and turn it
  into the next operation to serve.
- [fourth-pack.md](fourth-pack.md) — what adding a provider costs, written from
  the three that exist.
