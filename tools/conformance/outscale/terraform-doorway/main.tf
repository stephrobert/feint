# The doorway fixture (#286): the provider block is deliberately EMPTY.
#
# Everything — credentials, region, endpoint — must come from the environment,
# because what this fixture proves is the documented first contact: a stranger
# runs `eval "$(feint env outscale)"` and then `terraform plan`, with no hand
# edit. A provider block carrying an endpoint would prove the main fixture's
# point a second time and this one's not at all.
#
# The pin is the current provider line, the one the printed endpoint shape is
# for. Measured on 2026-08-19: provider 1.8.0 wants the /api/v1 path inside
# OSC_ENDPOINT_API; given the bare host it posts /<Action> at the root and the
# plan dies on a 404 (a six-minute retry backoff before the #185 hint).
terraform {
  required_version = ">= 1.7.0"

  required_providers {
    outscale = {
      source  = "outscale/outscale"
      version = "~> 1.7"
    }
  }
}

provider "outscale" {}

# One data source is one real API call at plan time (ReadSubregions), which is
# exactly the depth first contact has: nothing created, nothing to destroy,
# and a wrong endpoint shape cannot hide behind an empty diff.
data "outscale_subregions" "all" {}

output "subregions" {
  value = data.outscale_subregions.all.subregions[*].subregion_name
}
