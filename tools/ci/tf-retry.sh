#!/usr/bin/env bash
# Run a Terraform or OpenTofu command that downloads, and say what it resolved.
#
# Usage: tools/ci/tf-retry.sh <binary> <label> <args...>
#
# Two problems, one wrapper, because they live on the same line (#895, #899).
#
# ## The download is not retried, and the registry does fail
#
# Measured on 2026-11-12, pull request #893, whose diff touched no route and no
# pack:
#
#     Error: Failed to install provider
#     Error while installing scaleway/scaleway v2.83.0: github.com: bad response
#     code: 500
#     FAIL: scaleway: init failed
#
# The same branch, the same commit, sixteen minutes apart: success at 15:18,
# failure at 15:34, while main passed at 15:34. Two verdicts on one tree, and
# the red reads as a red on the change under review.
#
# The budget is not a new guess. tools/ci/fetch.sh carries the measurement,
# taken on 2026-09-14 on a Terraform provider asset: the asset answered 200 to
# five requests out of fifteen for roughly twenty minutes, a 67% failure rate.
# Against that, four attempts fail 19.8% of the time and ten fail 1.7%. Ten
# attempts, a fixed fifteen-second interval rather than a backoff — what breaks
# is a CDN answering everyone, and more waiting does not help it.
#
# ## A failure that is not an outage must stay fast
#
# A pinned version that does not exist, a provider renamed, a lock file that
# disagrees: none of those is weather, and retrying them for two minutes turns a
# clear error into a slow one. That is the line tools/ci/fetch.sh already draws
# at 404, drawn here on the message: only a 5xx, a timeout or a reset is
# retried. Everything else fails on the first attempt, with the engine's own
# output.
#
# ## Nobody could say which version a green run used
#
# `init` was silenced in four places, so the resolution lines never reached the
# log. On the terraform leg of run 43271335604 the log carried
# `scaleway v2.83.0` and `exoscale v0.72.0` from other steps and NOTHING for
# the three example stacks, which are applied in that same log. A version
# without its run is a memory (#899).
#
# So the resolution lines are printed on success, always, whatever the caller
# does with the rest of the output.
#
# tools/ci/tf_retry_test.go drives this with a stubbed engine: a 5xx retried and
# then succeeding, a pinned version that does not exist failing on the first
# attempt, and the resolution lines reaching stdout.
set -uo pipefail

bin="${1:?usage: tf-retry.sh <binary> <label> <args...>}"
label="${2:?usage: tf-retry.sh <binary> <label> <args...>}"
shift 2

attempts="${FEINT_TF_RETRY_ATTEMPTS:-10}"
interval="${FEINT_TF_RETRY_INTERVAL:-15}"

log="$(mktemp)"
trap 'rm -f "${log}"' EXIT

attempt=1
while :; do
  if "${bin}" "$@" >"${log}" 2>&1; then
    # What the engine resolved. Printed on every success, because this is the
    # only place that knows it and the caller is usually redirecting the rest.
    grep -E '^- (Installing|Using|Finding) ' "${log}" | sed 's/^/  /' || true
    exit 0
  fi

  # curl's own notion of a transient error, read off the engine's message
  # rather than its exit code, which is 1 for every kind of failure.
  if ! grep -qiE 'bad response code: 5[0-9][0-9]|response code: 5[0-9][0-9]|timeout|timed out|connection reset|connection refused|temporarily unavailable|EOF' "${log}"; then
    cat "${log}" >&2
    echo "FAIL: ${label}: ${bin} $1 failed, and the failure is not an outage" >&2
    exit 1
  fi

  if [ "${attempt}" -ge "${attempts}" ]; then
    cat "${log}" >&2
    echo "FAIL: ${label}: ${bin} $1 failed ${attempts} times on what reads as a registry outage" >&2
    exit 1
  fi

  echo "  ${label}: transient registry failure, retrying in ${interval}s (attempt ${attempt} of ${attempts})" >&2
  attempt=$((attempt + 1))
  sleep "${interval}"
done
