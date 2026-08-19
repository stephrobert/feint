#!/usr/bin/env bash
# What this release changed about the surface it serves, and whether it says so.
#
#   bash --noprofile --norc tools/release/surface.sh [ref]
#
# ref defaults to the latest tag. Offline throughout: the previous release's
# coverage artefacts are recovered from this repository's own history, because
# coverage/*-coverage.json is committed — `git show <tag>:coverage/...` is the
# whole recovery, and tools/compat/compat.sh already rebuilds a whole binary the
# same way.
#
# The judgement lives in internal/release, in Go, so it is unit-tested and
# falsifiable (tools/falsify/specs/release-surface.json). This script's job is to
# produce the two sides honestly.
#
# Exit: 0 every consumer-visible change is named or signed, 1 the comparison
# could not be made, 2 the release note is incomplete.
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

REF="${1:-}"
if [ -z "$REF" ]; then
  REF="$(git describe --tags --abbrev=0 2>/dev/null)"
fi
if [ -z "$REF" ]; then
  # Not a skip. A shallow clone has no tags, and a run that cannot see the
  # previous release must say so rather than report that nothing changed — an
  # absent measurement and a green one are different answers.
  echo "FAIL: no tag to compare against (a shallow clone carries none: fetch-depth: 0)" >&2
  exit 1
fi
if ! git rev-parse -q --verify "refs/tags/$REF" >/dev/null; then
  echo "FAIL: $REF is not a tag of this repository" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/old"

found=0
for path in coverage/*-coverage.json; do
  [ -e "$path" ] || continue
  found=$((found + 1))
  if git cat-file -e "$REF:$path" 2>/dev/null; then
    git show "$REF:$path" >"$WORK/old/$(basename "$path")" || exit 1
  fi
done
if [ "$found" -eq 0 ]; then
  echo "FAIL: no coverage/*-coverage.json in the working tree; run 'mise run drift:update'" >&2
  exit 1
fi

# Built rather than `go run`: `go run` reports its child's non-zero exit as 1,
# whatever it was, and this gate's contract is 0 / 1 / 2 like every other verdict
# in this repository. The distinction is the whole difference between "the note
# is incomplete" and "the comparison could not be made".
go build -o "$WORK/surface" ./tools/release/surface || exit 1
"$WORK/surface" \
  --since "$REF" \
  --old "$WORK/old" \
  --new coverage \
  --changelog CHANGELOG.md \
  --exemptions tools/release/unnamed.json
