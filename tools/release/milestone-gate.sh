#!/usr/bin/env bash
# What shipped is measured by the tags, so no open milestone may claim a version.
#
# ## The measurement that asked for this
#
# On 2026-11-11, with v0.14.0 and v0.15.0 both tagged and published, the
# repository carried two OPEN milestones titled `0.14.0 — The topology outranks
# its history` (19 open issues) and `0.15.0 — The cloud lab lives in the
# repository` (10 open). Neither described what its release had shipped: 0.14.0
# shipped drift triage and block import, 0.15.0 shipped `feint run` and the S3
# data plane. A reader of the tracker could only conclude that two published
# releases were unfinished.
#
# It was not the practice slipping once. Every minor from 0.9.0 to 0.13.0 has a
# CLOSED milestone with zero open issues — 0.9.0 with 63, 0.11.0 with 79,
# 0.12.0 with 45 — so the convention was real and these two broke it. And
# `CONTRIBUTING.md` still said "Milestones are the six waves of the roadmap's
# sequence", stale since 0.9.0, which is how nobody noticed: the sentence that
# would have caught it was itself out of date.
#
# ## Why a gate and not a tidy-up
#
# The titles were corrected by hand the same day. That fixes the state and not
# the cause: the next release that ships by triage rather than by theme
# recreates it, and the only person who would notice is the one tagging. So the
# tag is what asks the question.
#
# The rule is narrower than "no version number on an open milestone", and the
# narrowing was not a softening. A milestone for work still AHEAD is
# legitimate — `1.0.0 — the local cloud lab` is one, and it is the case where
# the tag should wait for the milestone rather than the reverse. What is
# refused is a milestone claiming a version git has already tagged, because
# that is a statement about the past that the tags disprove.
#
# Both halves are therefore asked:
#
#   1. an OPEN milestone whose version is already tagged: it describes a
#      release that shipped without it;
#   2. the version being tagged right now holding open issues, whatever its
#      title's shape: the release would ship with work filed under its own
#      number still open.
#
# ## Exit codes, and why three
#
# 0 coherent, 1 incoherent, 2 could not measure. A failed measurement and an
# absent result are different answers and only one of them is about the
# repository — the lesson preflight.sh paid for twice on its CI query, written
# at length there.
#
# tools/ci's TestTheMilestoneGateRefusesAVersionOnAnOpenMilestone and
# TestTheMilestoneGateAcceptsTheShippedShape fail without this.
set -u

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  echo "usage: milestone-gate.sh vX.Y.Z" >&2
  exit 2
fi
# The tag carries a leading v and a milestone title does not.
NUMBER="${VERSION#v}"

if ! command -v gh >/dev/null 2>&1; then
  echo "gh is not installed, so no milestone was read"
  exit 2
fi

# state=all, because the milestone of the version being tagged may legitimately
# be closed already, and a query restricted to open ones could not tell that
# from absent.
if ! milestones="$(gh api 'repos/{owner}/{repo}/milestones?state=all&per_page=100' \
                   --paginate --jq '.[] | "\(.state)\t\(.open_issues)\t\(.title)"' 2>&1)"; then
  printf 'could not read the milestones: %s\n' "$milestones" >&2
  exit 2
fi

findings=0

# A version number at the START of an open milestone's title. Anchored, because
# a theme is free to mention a version in passing ("Runtime Contract v1"), and
# only the leading position is the claim this refuses.
while IFS=$'\t' read -r state open title; do
  [ -n "${state:-}" ] || continue
  case "$state" in open) ;; *) continue ;; esac

  # The subject is a milestone claiming a version that ALREADY SHIPPED, which
  # is what was measured: v0.14.0 and v0.15.0 were published tags while
  # milestones titled after them stayed open.
  #
  # Not "any version number", and the difference matters. A milestone for work
  # still ahead — `1.0.0 — the local cloud lab` — is legitimate and is the one
  # case where the tag should wait for the milestone rather than the reverse.
  # A rule written as "no number at all" would have refused every tag from the
  # day that milestone was created, which is the gate-that-blocks-the-release
  # shape preflight.sh already carries a scar from.
  #
  # So the question is answered by git, offline: does a tag for this title's
  # version exist?
  claimed="$(printf '%s' "$title" | grep -Eo '^[0-9]+\.[0-9]+(\.[0-9]+)?' || true)"
  if [ -n "$claimed" ] && git rev-parse "v$claimed" >/dev/null 2>&1; then
    printf 'the open milestone "%s" claims v%s, which is already tagged, and holds %s open issue(s)\n' \
      "$title" "$claimed" "$open"
    findings=$((findings + 1))
  fi

  # And the version being tagged must not have open work under its own name,
  # whatever the title's shape.
  if printf '%s' "$title" | grep -Fq "$NUMBER" && [ "$open" != 0 ]; then
    printf 'the milestone "%s" is named after the version being tagged and holds %s open issue(s)\n' \
      "$title" "$open"
    findings=$((findings + 1))
  fi
done <<EOF
$milestones
EOF

if [ "$findings" -gt 0 ]; then
  exit 1
fi
echo "no open milestone claims a version that has already shipped"
exit 0
