#!/usr/bin/env bash
# Re-extract the API contracts the emulator checks its responses against.
#
# This lives in a script rather than inside the mise task because two callers
# need it and only one of them has mise: `mise run contract:update` on a
# workstation, and the weekly drift workflow on a GitHub runner, where mise is
# not installed. The workflow used to call `mise run` and fail with "command not
# found" every Monday — silently, behind continue-on-error, so the contracts half
# of the drift mechanism measured nothing at all.
#
# The upstream checkouts and documents are expected to be in place already
# (`mise run upstream:sync`, or the checkout steps of the workflow). The paths
# default to the layout both use.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

SDK_OUTSCALE="${FEINT_SDK_OUTSCALE:-$ROOT/.upstream/osc-sdk-go}"
SPEC_EXOSCALE="${FEINT_SPEC_EXOSCALE:-$ROOT/.upstream/exoscale-openapi.yaml}"
SPEC_SCALEWAY="${FEINT_SPEC_SCALEWAY:-$ROOT/.upstream/scaleway-openapi}"

extract() { uv run --with pyyaml tools/contract/extract-openapi.py "$@"; }

# Outscale needs no --error-shape: its document puts the same ErrorResponse on
# every 4xx it declares, and its SDK is generated from that document.
extract contracts/outscale.json --provider outscale \
  --source "outscale/osc-sdk-go pkg/osc/api.yaml" \
  --spec "$SDK_OUTSCALE/pkg/osc/api.yaml"

# Exoscale closes 7 of its schemas where Outscale closes almost all of them, so
# refusing an unknown field there is this project's decision rather than theirs.
# The artefact records which, and --assume-closed is where the decision is made.
#
# --error-shape overrides the document's RFC 9457 error envelope with what
# egoscale actually decodes; the fragment file carries the citation.
extract contracts/exoscale.json --provider exoscale \
  --source "https://openapi-v2.exoscale.com/source.yaml" --assume-closed \
  --error-shape tools/contract/exoscale-error.yaml \
  --spec "$SPEC_EXOSCALE"

# Scaleway publishes one document per product, so its names are qualified: it
# defines ListImages twice, in instance/v1 and in marketplace/v2.
#
# No --assume-closed here, and that is a measurement rather than caution. Their
# document is a lossier projection of the same internal IDL than their Go SDK, so
# assuming the schemas closed would report a violation on every list response the
# emulator answers correctly. docs/limits.md records what each contract is worth.
specs=""
while read -r product; do
  case "$product" in ''|\#*) continue ;; esac
  specs="$specs --spec $product=$SPEC_SCALEWAY/$(echo "$product" | tr / -).yml"
done < tools/contract/scaleway-products.txt

# --error-shape because their documents declare no error response at all; the
# fragment is transcribed from scw/errors.go, per rule 4.
# shellcheck disable=SC2086 # specs is a built argument list, and must split
extract contracts/scaleway.json --provider scaleway \
  --source "https://www.scaleway.com/en/developers/api/<product>/schema.yml" \
  --error-shape tools/contract/scaleway-error.yaml $specs
