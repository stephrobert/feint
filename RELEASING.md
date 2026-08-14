# Releasing

**Read this in another language:** [Français](./RELEASING.fr.md)

A release is a tag. There is no version to bump in any file: `release.yml`
stamps the tag into the binary with `-ldflags`, and a binary built any other way
falls back to the module version the Go toolchain records in it. Nothing can
drift from the tag, because nothing else holds the number.

## Which number

The commits decide, not taste. Subjects follow
[Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/), enforced
by the `commit-msg` hook and by the `Commits` workflow, and commitizen derives
the increment from them:

```bash
cz bump --dry-run     # e.g. "bump: version 0.1.0 → 0.2.0 / tag to create: v0.2.0"
```

`fix` moves the patch, `feat` the minor, a `!` before the colon marks a break.
Before 1.0 a break stays inside `0.x` (`major_version_zero` in `pyproject.toml`),
which is the same rule the Versioning section below states in prose.

Two deliberate limits. **The version lives only in git tags**
(`version_provider = "scm"`): a file carrying it would be a second source able to
disagree with the tag. And **the CHANGELOG is written, not generated**
(`update_changelog_on_bump = false`): its own header says which two kinds of
change deserve a line whatever their size, and a generator emitting every subject
would bury both under refactors. So commitizen proposes the number, the preflight
checks that the tag matches it, and the prose stays yours.

## Cutting one

1. **Move the `Unreleased` entries** in [CHANGELOG.md](./CHANGELOG.md) under a
   new `## [X.Y.Z]` heading. The release workflow reads that section for the
   body of the GitHub Release, so an entry that is not there is an entry nobody
   downloading a binary will see.

   Then **`mise run docs:coverage`**, in the same commit. That heading is where
   the install commands in README.md and docs/install.md get the version they
   tell a reader to download — pinned rather than `latest`, because a mutable
   reference cannot be checked against a hash and adopts a release the day it is
   published. Moving the heading without regenerating leaves both pages one
   release behind, and `feint docs --check` exits 2 until they are not: at the
   pre-commit hook, on the pull request, in the preflight below, and one last
   time inside the release workflow, which refuses to publish rather than
   repairing anything.

2. **Merge to `main` through a pull request, and wait for CI to be green.** The
   tag builds from that commit and a published release cannot be replayed.

3. **Run the preflight**, which replays offline everything that must hold:

   ```bash
   mise run release:check -- v0.1.0
   ```

   It checks a clean tree on `main`, that the tag is free locally *and* on the
   remote, `mise run check`, `mise run drift:check` at 0 — a release published
   with untriaged upstream operations advertises a coverage figure that is not
   true — `feint docs --check` at 0, the CHANGELOG section, committed
   `coverage/` and `contracts/`, and that conformance is green on this exact
   commit. It reports every verdict rather than stopping at the first, and it
   prints the commands to run once everything passes.

   It deliberately does **not** run the conformance suites itself: they need
   `scw`, `oapi-cli`, `exo` and Terraform installed, and a release cut from a
   machine missing one of them would silently skip the very proof this project
   rests on. It asks CI whether they passed instead.

4. **Tag and push**:

   ```bash
   git tag -a v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```

Pushing the tag is what publishes. It cannot be undone quietly: a tag has to be
deleted on both sides, and a release that reached the world has been downloaded.

## What the tag triggers

`.github/workflows/release.yml`, on `v*`:

- builds `linux` and `darwin` binaries for `amd64` and `arm64`, with the tag
  stamped in,
- generates SHA-256 checksums and a CycloneDX SBOM (Syft),
- records **SLSA build provenance** for the binaries and attests the SBOM,
- signs the checksums with **keyless Cosign**,
- creates the GitHub Release with every artefact attached, including
  `provenance.intoto.jsonl`.

That last asset is attached on purpose and is not redundant with the attestation
recorded through GitHub's API: it is the file OpenSSF Scorecard's
*Signed-Releases* control looks for. That control scores the **five most recent**
releases, so the maximum is only reached once five consecutive releases carry it.

## Verifying a release

Anyone can check that a binary really came from this repository's workflow, and
from which commit:

```bash
gh release download v0.1.0 --repo stephrobert/feint --pattern 'feint-linux-amd64'
gh attestation verify feint-linux-amd64 --repo stephrobert/feint

cosign verify-blob --bundle checksums.txt.cosign.bundle \
  --certificate-identity-regexp 'https://github.com/stephrobert/feint/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum -c checksums.txt --ignore-missing
```

## Versioning

[Semantic Versioning](https://semver.org/). Before 1.0, the minor version moves
on anything a client can observe.

What counts as breaking here is narrower than it looks, and it is worth stating
because this project's whole surface is somebody else's API. **Serving more of a
provider's API is never breaking**, however much it changes: a client asking for
an operation that used to 404 and now works is a client that got what it always
wanted. What breaks is on this project's own side — the CLI's verbs and flags,
the exit codes, the shape of `/_feint/*`, the state and snapshot formats, and
any emulated behaviour a test could have relied on.

### Frozen surfaces

Most of that list is no longer prose (#132). The shapes of `/_feint/health`,
`/_feint/routes`, `/_feint/conformance` and `/_feint/trace`, the CLI's verbs and
flags, and the exit codes each have a committed fixture under
`internal/cli/testdata/frozen/` — the field tree, never a value — and two tests
gate them on every pull request:
`TestTheFrozenSurfacesStillMatchTheirFixture` fails when a shape moves and the
fixture did not, and `TestASurfaceChangeDemandsItsVersionBump` fails when the
fixture moved and the declared version did not. The three object payloads serve
that version as `schema_version`, so a consumer can branch on it; the gate is
what keeps the field from lying.

Changing one of these surfaces on purpose is four steps, in one commit:

1. change the code;
2. `mise run frozen:update` — it appends the new form to the fixture's history
   at the next version, and never rewrites an entry;
3. bump the matching constant: `internal/core/emulator/schema.go` for the
   `/_feint/*` payloads, `cliSurfaceVersion` in `internal/cli/cli.go` for the
   CLI — the tests stay red until this step, which is the point;
4. write the CHANGELOG line the bump is the signal for. Whether it lands as a
   fix or a break is this section's ordinary question; the fixture only proves
   the surface moved, not which of the two it was.

What is deliberately not frozen: the prose of the help text around the verbs
and flags, the values behind every frozen key (counters, identifiers, lists
that grow when routes are mounted), and the trace fields only some exchanges
carry (`unread`, `violations`). A freeze that caught any of those would go red
on routine runs and be disarmed within the week.

The snapshot format is the one of those that says its own version. Since #133 a
snapshot is `{"format": "feint-snapshot", "version": N, "resources": [...]}`, and
`Restore` refuses anything it cannot account for: another version, another
format, an unknown field. Bumping `snapshotVersion` in
`internal/core/store/store.go` is therefore a breaking change under this
section — and a file written by an older feint fails loudly at the boundary
rather than restoring three quarters of itself in silence, which is what it did
before.

The one exception worth calling out: **a response shape corrected to match the
provider's document is a fix, not a break**, even when a downstream test was
asserting the wrong one. That is the point of the project, and a test that
depended on the emulator being wrong was measuring the emulator rather than the
cloud.
