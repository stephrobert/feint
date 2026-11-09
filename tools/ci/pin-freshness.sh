#!/usr/bin/env bash
# Is a pinned tool version still the current minor series upstream?
#
# A version literal in a workflow has no way of knowing it has gone stale.
# plumber sat pinned at 0.4.22 from 2026-07-30 to 2026-11-09 while upstream
# shipped three minor series, and the cost was not the version number: an
# enumerated .plumber.yaml was missing 33 of the 52 controls the current
# release knows, and an absent control is a disabled control, so the badge
# read 100/100 over a shrinking subset.
#
# The verdict is on the MINOR series, not the patch, and that bound is measured
# rather than chosen: plumber shipped 0.7.16 through 0.7.23 in ten days. A gate
# on the patch level would red on most weekly runs and be gone within a month,
# which is the `--no-verify` argument CLAUDE.md makes about hooks, arriving
# through a scanner. A minor behind is the shape that actually hurt.
#
# Usage: pin-freshness.sh <owner/repo> <pinned-version>
#
# Exit codes follow the repository's contract: 0 ok, 1 error, 2 drift.
#   0  the pin is the latest release, or a patch behind within the same minor
#   1  the script could not conclude (bad arguments, upstream unreadable)
#   2  the pin is a whole minor series behind, or ahead of what upstream has
#
# 2 and not 1 for a stale pin, deliberately: the repository already uses 2 for
# "the surface moved and nobody triaged it", which is exactly this situation,
# and a caller that cannot tell "upstream moved" from "the check broke" learns
# nothing from the red. tools/ci/pin_freshness_test.go drives every branch.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <owner/repo> <pinned-version>" >&2
  exit 1
fi

repo="$1"
pinned="${2#v}"

if [ -z "${pinned}" ]; then
  echo "pin-freshness: the pinned version is empty; nothing to compare" >&2
  exit 1
fi

# An unreadable upstream is exit 1, never 0 and never 2: "we could not look" is
# not "nothing moved", and reporting it as a pass is the failure mode this
# repository has measured seven times. A `gh` that fails under `set -e` would
# kill the script before this check, so the call is guarded.
if ! raw="$(gh release view --repo "${repo}" --json tagName --jq '.tagName' 2>&1)"; then
  echo "pin-freshness: could not read the latest release of ${repo}: ${raw}" >&2
  exit 1
fi

latest="${raw#v}"

# A tag that is not a dotted version cannot be compared. Refusing it is the
# point: a silent fallback would compare "nightly" against "0.7.23" and answer
# something.
case "${latest}" in
  *[!0-9.]* | '' | *..* )
    echo "pin-freshness: ${repo} answered tag '${raw}', which is not a dotted version" >&2
    exit 1
    ;;
esac

echo "pinned ${pinned}, latest upstream ${latest}"

if [ "${pinned}" = "${latest}" ]; then
  echo "the pin is the latest release"
  exit 0
fi

# ${v%.*} drops the patch: 0.7.23 -> 0.7, 1.2.3 -> 1.2. A two-part version
# (0.7) yields its major, which still compares consistently on both sides.
if [ "${pinned%.*}" = "${latest%.*}" ]; then
  echo "::notice::${repo} ${latest} is out, pin is ${pinned}: same minor series," \
       "patch only, no action required."
  exit 0
fi

echo "::error::${repo} is pinned at ${pinned} and upstream is on ${latest}:" \
     "a different minor series. Controls, flags or defaults added since" \
     "${pinned%.*} are absent from anything written against the old surface," \
     "and absent means disabled. Move the pin, re-run the tool locally, and" \
     "triage what it finds before the pin lands." >&2
exit 2
