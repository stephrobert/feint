#!/usr/bin/env bash
# Every changed path is triaged by some rule of tools/testplan/rules.go.
#
#   bash --noprofile --norc tools/testplan/check.sh
#
# Exit: 0 every path is triaged, 1 the plan could not be made, 2 a path no rule
# triages — the repository's contract (CLAUDE.md, rule 9).
#
# Why a built binary and not `go run`. `go run` reports any non-zero exit of the
# program as its own exit 1 and prints `exit status 2` beside it, so
# `go run ./tools/testplan --check` answered 1 for an untriaged path: the task
# lied about its own contract, measured on 2026-11-14 with an untracked
# internal/zz_untriaged/probe.txt. Building first and running the binary hands
# back the program's own code. TestTheTestplanCheckHandsBackItsOwnExitCode
# (tools/ci/falsify_harness_test.go) requires 2 on that case and fails if this
# script collapses it to 1.
#
# The binary goes to a fresh mktemp directory, never into the tree: an untracked
# file there would count as a changed path, and a predictable /tmp name is a
# symlink somebody else can plant. The plan is taken in the directory this is
# run from, so a caller can point it at another clone.
set -uo pipefail

here="$(cd "$(dirname "$0")/../.." && pwd)" || exit 1
bin_dir="$(mktemp -d)" || exit 1
trap 'rm -rf "$bin_dir"' EXIT

if ! go build -C "$here" -o "$bin_dir/testplan" ./tools/testplan; then
  echo "FAIL: tools/testplan does not build, so no plan was made" >&2
  exit 1
fi

status=0
"$bin_dir/testplan" --check || status=$?
exit "$status"
