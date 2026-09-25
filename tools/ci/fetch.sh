#!/usr/bin/env bash
# Fetch a release asset, with a retry budget sized on a measured outage.
#
# Usage: tools/ci/fetch.sh <url> <destination>
#
# WHY THIS EXISTS
#
# Every install step in this repository already wrote `curl --retry 3
# --retry-connrefused`, and the nightly conformance run of 2026-09-25 still died
# on `curl: (22) The requested URL returned error: 504` installing the Scaleway
# CLI. The reflex reading is "curl does not retry on 504". Measured, it does:
# against a local server answering 504 forever, `--retry 3` sends four requests,
# one second apart then two then four.
#
# So retrying was never the gap. The BUDGET was: seven seconds.
#
# HOW LONG AN OUTAGE ACTUALLY LASTS HERE
#
# The one real measurement this repository has, taken 2026-09-14 while the same
# family of failure was killing three jobs: the asset
# `terraform-provider-outscale v1.8.0` answered 200 to five requests out of
# fifteen for roughly twenty minutes, then fifteen out of fifteen. A 67% failure
# rate, not a total one.
#
# Against that rate, and taking attempts as independent — which they are not
# when a nearby cache is what broke, so this is a floor rather than a promise:
#
#     4 attempts   19.8% chance of failing anyway   <- what the workflows had
#     8 attempts    3.9%
#    10 attempts    1.7%
#
# 19.8% is exactly what was observed: passing most nights, failing some.
#
# WHY A FIXED INTERVAL RATHER THAN A BACKOFF
#
# Exponential backoff exists to spare a service that is struggling under load.
# What breaks here is a CDN answering 504 to everyone, which more waiting does
# not help. At an equal time budget a fixed interval buys far more attempts:
# ten of them cost 135s spaced 15s apart, against 511s doubling.
#
# WHAT IS NOT RETRIED
#
# A 404 is not an outage, it is a pin naming an asset that does not exist, and
# retrying it for two minutes turns a clear error into a slow one. curl's own
# notion of a transient error is the right one and it excludes 404: timeouts,
# and HTTP 408, 429, 500, 502, 503, 504.
set -euo pipefail

url="${1:?usage: fetch.sh <url> <destination>}"
dest="${2:?usage: fetch.sh <url> <destination>}"

# Overridable so a test can drive this in seconds rather than minutes. The
# defaults are the measured ones, and a caller that lowers them is choosing a
# higher chance of a red night.
attempts="${FEINT_FETCH_ATTEMPTS:-10}"
delay="${FEINT_FETCH_DELAY:-15}"
max_time="${FEINT_FETCH_MAX_TIME:-200}"

# --retry counts retries, not attempts: `--retry 9` sends ten requests.
curl --fail --silent --show-error --location \
  --retry "$((attempts - 1))" \
  --retry-delay "$delay" \
  --retry-max-time "$max_time" \
  --retry-connrefused \
  --output "$dest" \
  "$url"
