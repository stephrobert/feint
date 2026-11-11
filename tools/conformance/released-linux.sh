#!/usr/bin/env bash
# A first Linux apply from the PUBLISHED artefacts, without Homebrew (#843).
#
# ## What this proves that nothing else did
#
# tools/conformance/quickstart.sh already drives the four-command journey the
# README prints — up, apply, a second plan that is empty, down — but it does it
# with the binary this repository just built. That proves the code. It says
# nothing about whether a stranger can obtain that code.
#
# The gap was measured: the README's Quick Start opened with
# `brew install stephrobert/feint/feint`, and this project's audience is Linux
# engineers who do not run Homebrew. Their route was `docs/install.md`, which is
# correct and forty lines long, and it sat behind a link. So the first wall a
# stranger met was the install, not the emulator.
#
# This script closes the loop the other way round: it takes the install commands
# **out of the README**, runs them, and then runs the quickstart gate against
# what they installed. If the README's block stops working, this reddens. A
# tutorial nobody replays is a tutorial that rots, which is the same argument
# #593 made for the quickstart itself.
#
# ## Why it extracts rather than repeats
#
# The commands are read from README.md, never copied here. A copy is a second
# source: it drifts the day somebody improves the README, and then this gate
# proves that the copy works. The same discipline quickstart.sh uses when it
# takes its expected `Apply complete!` line out of the README rather than
# writing the number down.
#
# ## What it deliberately does not do
#
# No machine runtime. The example declares `runtime: mode: off` and this script
# never overrides it, so what is proven here is a control plane, an address and
# a server the API describes — **not** Incus or OVN networking. Saying that
# plainly is the point: the default mode must never be advertised as proving the
# runtime.
#
# No cloud credentials, and that is asserted rather than assumed: the run
# refuses to start if any provider credential is visible in the environment,
# because an acceptance that could have reached a real account proves nothing
# about locality.
#
# Usage: tools/conformance/released-linux.sh [addr]
set -uo pipefail

ADDR="${1:-127.0.0.1:4596}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
step() { echo "- $*"; }

echo "conformance: the published Linux binary, installed the way the README says"

# ---- the environment this acceptance requires to mean anything --------------
# A credential visible here would make "no cloud was contacted" a claim about
# luck. The list is the one feint's own env and doctor paths know about.
leaked=""
for v in SCW_ACCESS_KEY SCW_SECRET_KEY SCW_DEFAULT_PROJECT_ID \
         OSC_ACCESS_KEY OSC_SECRET_KEY OUTSCALE_ACCESSKEYID OUTSCALE_SECRETKEYID \
         EXOSCALE_API_KEY EXOSCALE_API_SECRET AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY; do
  if [ -n "${!v:-}" ]; then leaked="$leaked $v"; fi
done
[ -z "$leaked" ] || fail "a provider credential is visible, so this run could have reached a real account:$leaked"
ok "no provider credential is visible"

command -v terraform >/dev/null 2>&1 || command -v tofu >/dev/null 2>&1 \
  || fail "neither terraform nor tofu is installed; the stack asks for the engine, feint does not"
ok "the engine the stack declares is installed"

# ---- the install commands, taken out of the README --------------------------
# The fenced bash block that follows the Linux sentence. Anchored on the
# generated sentence rather than on a line number, and the extraction is checked
# rather than trusted: an empty or truncated block would otherwise install
# nothing and let everything below pass.
step "the install block the README publishes"
block="$(awk '
  /^Without Homebrew, the same thing on Linux:/ { looking = 1; next }
  looking && /^```bash$/ { inside = 1; looking = 0; next }
  inside && /^```$/ { exit }
  inside { print }
' "$ROOT/README.md")"

[ -n "$block" ] || fail "no Linux install block found in README.md: the anchor sentence or the fence moved"
case "$block" in
  *"checksums.txt"*) ;;
  *) fail "the README's Linux block installs without verifying anything; that is the one shape this gate exists to refuse" ;;
esac
case "$block" in
  *"install -Dm755"*) ;;
  *) fail "the README's Linux block does not install a binary" ;;
esac
ok "$(printf '%s\n' "$block" | grep -c .) line(s), verifying against checksums.txt"

# ---- run them, in a home of this run's own ----------------------------------
# A throwaway HOME, so the block's `~/.local/bin` is this run's and an operator's
# own installation is neither used nor overwritten. That also makes the PATH
# assertion below mean something: nothing else could have put a feint there.
home="$(mktemp -d)"
work="$(mktemp -d)"
cleanup() {
  "$home/.local/bin/feint" stop --addr "$ADDR" >/dev/null 2>&1
  rm -rf "$home" "$work"
}
trap cleanup EXIT

step "running it"
( cd "$work" && HOME="$home" bash -c "$block" ) || fail "the README's own install commands failed"
[ -x "$home/.local/bin/feint" ] || fail "the block ran and no binary landed in ~/.local/bin"
ok "installed at ~/.local/bin/feint"

# ---- it is the version the README pins --------------------------------------
# Read from the binary and compared with the README's own `v=`, so a Quick Start
# pinned to a version nobody published fails here rather than in a reader's
# terminal.
pinned="$(printf '%s\n' "$block" | sed -n 's/^ *v=\(.*\)$/\1/p' | head -1)"
[ -n "$pinned" ] || fail "the README's block names no version"
got="$("$home/.local/bin/feint" version 2>&1 | head -1)"
case "$got" in
  *"$pinned"*) ok "it reports $pinned, the version the README pins" ;;
  *) fail "the README pins $pinned and the published binary reports: $got" ;;
esac

# ---- and it drives the quickstart the README prints -------------------------
# Through the existing gate rather than a second copy of it: quickstart.sh
# already asserts the four commands, the emulator answering, the empty second
# plan and the silent port afterwards. What this script changes is which binary
# it holds.
step "the quickstart journey, driven by the published binary"
FEINT_BIN="$home/.local/bin/feint" "$SCRIPT_DIR/quickstart.sh" "$ADDR" \
  || fail "the quickstart gate failed against the published binary"

echo "conformance: a stranger without Homebrew reaches Apply complete from the published artefacts"
echo "  note: runtime mode off — this proves a control plane, not Incus or OVN networking"
