#!/usr/bin/env bash
# The published API documents the extraction reads, fetched in one place.
#
# WHY THIS FILE EXISTS, MEASURED
#
# The nightly drift scan checks the two Go SDKs out with actions/checkout and
# then runs tools/contract/update.sh. That script reads three sources, and only
# two of them are git repositories: Exoscale publishes a single OpenAPI document
# and Scaleway publishes one per product, both over HTTP, both fetched by
# `mise run upstream:sync` — which a GitHub runner does not run, since mise is
# not installed there.
#
# So the extraction died on the runner every night, at the first document it
# could not open:
#
#     contracts/outscale.json: outscale api 1.43.2, 236 operations, 656 schemas
#     FileNotFoundError: '/home/runner/work/feint/feint/.upstream/exoscale-openapi.yaml'
#
# Read off the scheduled run of 2026-11-13 (43344711412). It had never produced
# a complete result in CI, and nothing said so: the step runs under
# continue-on-error, so its failure left the contracts half of the drift
# mechanism measuring nothing while the night stayed green. The health job added
# in #892 is what finally made it visible, on its first real night (#901).
#
# One file, two callers — `mise run upstream:sync` and the drift workflow — for
# the reason tools/drift/gate.sh gives at length: the same procedure written
# twice drifts, and the copy that drifts is the one nobody runs by hand.
#
# TestTheDriftNightFetchesTheDocumentsItExtracts fails when the workflow stops
# calling this.
#
# Usage: tools/contract/documents.sh
#
#   FEINT_SPEC_EXOSCALE   where the Exoscale document lands
#   FEINT_SPEC_SCALEWAY   the directory the per-product documents land in
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SPEC_EXOSCALE="${FEINT_SPEC_EXOSCALE:-$ROOT/.upstream/exoscale-openapi.yaml}"
SPEC_SCALEWAY="${FEINT_SPEC_SCALEWAY:-$ROOT/.upstream/scaleway-openapi}"

mkdir -p "$(dirname "$SPEC_EXOSCALE")" "$SPEC_SCALEWAY"

# Through tools/ci/fetch.sh rather than a bare curl, and that is not polish: its
# header carries the measurement, taken on a Terraform provider asset, of an
# outage that answered 200 to five requests in fifteen for twenty minutes. Four
# attempts fail 19.8% of the time against that rate, ten fail 1.7%. A document
# this night cannot fetch is a night that reports nothing about what the
# providers publish.
#
# Exoscale ships no OpenAPI document inside its SDK; it publishes one, and their
# own Go SDK is generated from it. So the document is the source here, the way a
# checkout is for the other two.
"$ROOT/tools/ci/fetch.sh" https://openapi-v2.exoscale.com/source.yaml "$SPEC_EXOSCALE"

# Scaleway publishes one document per product, as the "Download schema" link of
# each product page. No repository carries them and there is no machine-readable
# index, so tools/contract/scaleway-products.txt is the index.
#
# The second field, when a line carries one, is the SDK name the contract uses;
# only the first names the portal download.
grep -v '^[[:space:]]*#' "$ROOT/tools/contract/scaleway-products.txt" \
  | grep -v '^[[:space:]]*$' \
  | while read -r product _sdkname; do
      "$ROOT/tools/ci/fetch.sh" \
        "https://www.scaleway.com/en/developers/api/$product/schema.yml" \
        "$SPEC_SCALEWAY/$(echo "$product" | tr / -).yml"
    done
