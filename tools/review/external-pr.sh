#!/usr/bin/env bash
# Review a pull request from outside the project before anything of it runs
# (#988, item 3): `tools/review/external-pr.sh [--json] <number>`.
#
# It builds tools/review from THIS checkout and reads the pull request through
# `gh api` only. It never checks the pull request out and never runs its code.
# The sensitive-path list is this checkout's tools/ci/sensitive-paths.txt, so a
# pull request that edits the list is judged by the list it would replace.
#
# Exit codes are the program's: 0 nothing to read first, 1 cannot judge, 2 at
# least one finding to read first. See tools/review/main.go.
#
# The program is built, then run, and NOT started with `go run`: `go run`
# prints "exit status 2" and exits 1 whatever its child returned, so a report
# with findings came out as "cannot judge" (measured on 2026-11-16 on #985).
# TestTheWrapperKeepsTheProgramsExitCode fails with `exec go run` here.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
bin_dir="$(mktemp -d)"
trap 'rm -rf "${bin_dir}"' EXIT

(cd "${root}" && go build -o "${bin_dir}/external-pr" ./tools/review) || {
  echo "external-pr: cannot judge: tools/review does not build" >&2
  exit 1
}

status=0
"${bin_dir}/external-pr" --paths-file "${root}/tools/ci/sensitive-paths.txt" "$@" || status=$?
exit "${status}"
