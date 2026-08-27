<!--
Two things that surprise first-time contributors here, both worth thirty seconds
before you push:

1. **From a fork, no CI runs until a maintainer approves it.** GitHub holds every
   workflow in `action_required` — its protection against a fork editing a
   workflow to reach this repository's secrets — so `gh pr checks` answers "no
   checks reported" and you get no feedback at all, not even a red one. That is
   normal. It is not your pull request being ignored, and it means the first
   review is human. A pull request has already been merged here that broke a
   conformance suite with nothing on the page to say so.

2. **Commit subjects must be Conventional Commits**, because the version is
   derived from them: `fix(exoscale): read both key forms`. A check enforces it,
   and it will fail after you push rather than before.

`pre-commit install` catches the second one locally, along with everything else
CI runs. It is the first step of CONTRIBUTING.md and it is not a courtesy: git
hooks are not versioned, so a fresh clone has none of them.
-->

# Summary

<!-- What does this change, and why? -->

## Type of change

- [ ] A route added or changed
- [ ] Bug fix
- [ ] Refactor
- [ ] Documentation
- [ ] Chore / tooling
- [ ] Security / supply chain

## Checklist

Only the **Always** block applies to every pull request. The other blocks are
conditional: if a block does not apply, write `N/A` rather than leaving it blank
— a blank box reads as "forgotten", an explicit `N/A` reads as "considered".

### Always

- [ ] `mise run check` passes (gofmt, vet, golangci-lint, `go test -race`)
- [ ] No new external Go dependency, or the pull request justifies it. The
      standard library covers routing, JSON and Go parsing; `go.mod` has three
      lines and a pre-commit hook keeps it that way
- [ ] `internal/core` still knows no provider. If a change seemed to require it,
      the pack changed instead
- [ ] Nothing in the diff could be written identically for another provider. If
      it could, it belongs in the core — that is the rule that stopped the same
      lifecycle bug being fixed once and surviving twice
- [ ] `mise run testplan` was run, and what it printed was run — or this pull
      request names the run it skipped and why. A run not played is **written
      down**, never left blank: the reviewer cannot tell an omission that was
      considered from one that was forgotten, and only one of the two is a
      decision

### When a model wrote a substantive part of this — otherwise N/A

See the AI-assisted contributions section of
[CONTRIBUTING.md](../CONTRIBUTING.md). The bar is the same as for everyone; what
is added here is disclosure and one extra question.

- [ ] An `Assisted-by:` trailer names the tool and the model version
- [ ] I ran `mise run conformance` myself — not "it should pass"
- [ ] Every field name in the diff comes from the provider's SDK, their OpenAPI
      document, or a run of the real client, and I can say which. "The model
      produced it" is not a source

### When a route is added or changed — otherwise N/A

- [ ] The route declares its upstream operation at the SDK's exact name
      (`Route.Operation`), or the coverage report lies
- [ ] `mise run conformance` passes — at minimum the suite of the provider
      touched. **A unit test alone does not prove a response shape.** The claim
      this project makes is that the official client cannot tell the difference,
      and only the official client can check it
- [ ] The response was validated against the provider's own API description
      (`--contracts contracts`), not against what looked right
- [ ] What the client sends is read, or refused. Check
      `/_feint/conformance | jq .unread_request_fields` — a field declared on a
      request struct and never read is invisible to that report, and is how a
      client got a 200 for a Vm that went nowhere
- [ ] `mise run drift:update` run, and `coverage/` committed with the change

### When behaviour a client can observe changes — otherwise N/A

- [ ] `docs/limits.md` updated if the change moves a documented limit
- [ ] The README's generated tables regenerated (`mise run docs:coverage`) — CI
      fails otherwise, so it is cheaper to do it here

### When `.github/workflows/` is touched — otherwise N/A

- [ ] Every action pinned to a full 40-character commit SHA with a `# vX.Y.Z`
      comment — never `@v4`, never `@main`
- [ ] `step-security/harden-runner` is the job's first step
- [ ] `permissions:` is least-privilege on every job
- [ ] No job renamed, or the required status checks updated — they match job
      names exactly, and a renamed job silently stops being required
- [ ] The exit-code contract still holds: `0` ok, `1` error, `2` drift. A job
      that pipes a gate through `tee` reads `tee`'s zero and can never fail —
      this happened

### When a machine runtime is involved — otherwise N/A

- [ ] Tested with `FEINT_VM=incus` (or `incus-vm`), not only with `--vm off`
- [ ] Nothing the emulator did not create was touched. Everything it creates is
      labelled, and `feint clean` sweeps exactly those
- [ ] The run leaves nothing behind — checked, not assumed

## Related issues

<!-- e.g. Closes #123 -->
