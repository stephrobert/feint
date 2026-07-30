# Security policy

## What Feint is, and what that means for you

Feint emulates cloud APIs so you can run tests without an account. It is a
**development tool**. It accepts every credential it is given without verifying
any of them, it answers on plain HTTP, and it holds its state in memory. That is
deliberate — the point is to run without an account — and it makes the emulator
unsuitable for anything but a workstation or a CI runner.

**Do not expose it to a network you do not control.** Bind it to a loopback
address, which is what `feint serve` does by default, or to a network the
container it runs in cannot leave.

Listening on the loopback is not by itself enough, and the emulator does not
pretend otherwise. A web page the operator visits can resolve its own name to
`127.0.0.1` and have the browser issue requests here on its behalf — DNS
rebinding, against which a listen address protects nothing. So when Feint is
bound to a loopback address it **refuses requests whose `Host` header names
anything else**, which a browser cannot forge. Bind it elsewhere with `--addr`
and the check steps aside, because that exposure was asked for.

Two consequences worth stating plainly:

- **Any credential is accepted.** Signature v4, `X-Auth-Token` and
  `EXO2-HMAC-SHA256` are parsed and never checked. Anything that can reach the
  port can read and delete everything the emulator holds.
- **With `--vm`, the emulator starts real machines.** It creates containers or
  virtual machines, bridges and firewall rules on the host, through the Incus
  CLI and with your privileges. Everything it creates is labelled and
  `feint clean` removes exactly those; nothing else is touched. But an emulator
  reachable from outside is, in that mode, a way to run containers on your host.

## Reporting a vulnerability

Report privately through GitHub's **Security → Report a vulnerability** on this
repository. That opens a private advisory only the maintainers can read.

Please do not open a public issue for anything exploitable.

Include what you would want to receive: what you did, what happened, what you
expected, and the smallest way to reproduce it. A proof of concept is welcome
and never required.

You will get an acknowledgement within a week. If a report leads to a fix, the
advisory names you unless you prefer otherwise.

## What counts

In scope, and taken seriously:

- Anything that lets a request escape the emulator: command injection into the
  runtime CLI, path traversal through a resource identifier, a route that
  touches a host resource the emulator did not create.
- Anything that makes `feint clean` remove something it did not create. The
  sweep is scoped by label precisely so it cannot, and a way around that is a
  real finding.
- A crash reachable from a request. The emulator is a test dependency: a panic
  takes somebody's test suite down with it.
- A dependency or build-pipeline compromise. The module has no external Go
  dependencies, which is a deliberate reduction of that surface.

Out of scope, because they are the documented design:

- Credentials are not verified.
- The default endpoint speaks HTTP rather than HTTPS.
- The fake credentials committed under `tools/conformance/*/fake-credentials.env`
  are public on purpose. They open nothing; they exist because the official
  clients refuse to sign a request whose credentials are not well-formed.

## Supported versions

The project is pre-1.0. Fixes land on the default branch and in the next
release; there is no backporting to earlier tags.
