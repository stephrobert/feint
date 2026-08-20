#!/usr/bin/env bash
# The Homebrew tap serves the formula this release derives, or this exits 2.
#
#   bash --noprofile --norc tools/release/tap.sh [check|print] [version]
#
# version defaults to the CHANGELOG's newest released heading — the same reader
# `feint docs` uses for every install command it generates, so the tap is judged
# against the version this repository says is released and never against
# `latest`.
#
# ## Why a comparison and not a push (#321)
#
# The decision inside #321 is who writes the formula on release. This is the
# answer, and it is the shape #245 already settled on one repository over:
#
#   - `print` derives the formula from the release's own checksums.txt, the file
#     cosign signs. Its output is what goes into the tap. Nobody types a digest,
#     so "hand-updated" costs a copy rather than an authorship, and the failure
#     mode #321 names — a formula transcribed and then stale — has no door left.
#   - `check` derives it again and refuses any difference. The tap is public and
#     the default GITHUB_TOKEN reads it, so this needs no secret. A push step in
#     release.yml would need a cross-repository token this repository does not
#     hold, and .github/workflows/workflow-security.yml already refuses exactly
#     that: "a gate wired to a secret that does not exist is the drift.yml
#     defect all over again".
#
# ## Why it is not on the pull-request path
#
# The only person who can clear this red is whoever can push to the tap. A gate
# a contributor cannot clear is the gate everybody learns to skip, which is the
# argument CLAUDE.md makes for keeping `conformance` out of the pre-commit hook.
# So it runs on a schedule and on demand (.github/workflows/tap.yml), where its
# red lands on the person holding the tap.
#
# ## Which ref of the tap
#
# The tap's default branch, because that is what `brew update` fetches and
# therefore what every `brew install` resolves. The Marketplace gate reads a tag
# for the same reason in the other direction: read what consumers read.
#
# Exit: 0 the tap serves this formula, 1 the comparison could not be made,
# 2 the tap is stale, absent, or serves something else.
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

MODE="${1:-check}"
VERSION="${2:-}"

if [ -z "$VERSION" ]; then
  # The newest released heading, the same one internal/cli/docs_release.go
  # reads. `head -1` on the whole file rather than a range: the Unreleased
  # section carries no version, so the first match is the released one.
  VERSION="$(grep -m1 -oE '^## \[[0-9]+\.[0-9]+\.[0-9]+\]' CHANGELOG.md | tr -d '#[] ')"
fi
if [ -z "$VERSION" ]; then
  echo "FAIL: no released version in CHANGELOG.md; the reader is broken, not the tap" >&2
  exit 1
fi

SLUG="$(sed -n 's|^module github.com/\(.*\)$|\1|p' go.mod)"
if [ -z "$SLUG" ]; then
  echo "FAIL: no module path in go.mod to name the repository with" >&2
  exit 1
fi
OWNER="${SLUG%%/*}"
NAME="${SLUG##*/}"
# `brew install stephrobert/feint/feint` resolves to github.com/<owner>/homebrew-<name>.
# Derived, never typed: the day the repository is renamed, the tap this checks
# is renamed with it instead of staying right about the old one.
TAP="${OWNER}/homebrew-${NAME}"
FORMULA_PATH="Formula/${NAME}.rb"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# The release's own list. Fetched rather than rebuilt: the digests in the
# formula must be the ones the release published and cosign signed, and this is
# the only file that carries them.
if ! curl --retry 3 --retry-connrefused -fsSL \
  -o "$WORK/checksums.txt" \
  "https://github.com/${SLUG}/releases/download/v${VERSION}/checksums.txt"; then
  {
    echo "FAIL: no checksums.txt on the v${VERSION} release of ${SLUG}."
    echo "Either the network is down, or CHANGELOG.md names a version that was never tagged."
    echo "Not knowing is not the same answer as the tap being fine."
  } >&2
  exit 1
fi

if ! go run ./tools/release/formula \
  --version "$VERSION" --checksums "$WORK/checksums.txt" >"$WORK/derived.rb"; then
  echo "FAIL: the formula could not be derived from the published checksums" >&2
  exit 1
fi

if [ "$MODE" = "print" ]; then
  cat "$WORK/derived.rb"
  exit 0
fi
if [ "$MODE" != "check" ]; then
  echo "usage: tools/release/tap.sh [check|print] [version]" >&2
  exit 1
fi

# The tap as a `brew install` sees it. A 404 here is not a skip: the tap not
# existing is precisely the state this gate is meant to report, and treating it
# as "nothing to compare" would make the check pass loudest on the day it has
# the most to say.
if ! gh api -H "Accept: application/vnd.github.raw" \
  "repos/${TAP}/contents/${FORMULA_PATH}" >"$WORK/published.rb" 2>"$WORK/gh.err"; then
  {
    echo "DRIFT: ${TAP} does not serve ${FORMULA_PATH}."
    sed 's/^/  gh: /' "$WORK/gh.err"
    echo
    echo "\`brew install ${OWNER}/${NAME}/${NAME}\` installs nothing while this is true."
    echo "Create the tap, then:"
    echo "  mise run release:formula > ${FORMULA_PATH}"
  } >&2
  exit 2
fi

if ! diff -u "$WORK/published.rb" "$WORK/derived.rb"; then
  {
    echo
    echo "DRIFT: ${TAP} serves a formula this release does not derive."
    echo "The left side is what brew installs today; the right side is what v${VERSION} published."
    echo "Replace it:"
    echo "  mise run release:formula > ${FORMULA_PATH}"
  } >&2
  exit 2
fi

echo "ok: ${TAP} serves the formula v${VERSION} derives, digests included"
