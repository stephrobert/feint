package scaleway

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The API gateway's own route, which belongs to no product.
//
// # Why it is here
//
// The SDK that Terraform provider 2.83.0 embeds asks the gateway for its
// metadata after every read of a product that carries an SRN, and builds a
// `srn://…` client-side from the domain it answers. This emulator mounted no
// such route: measured 2026-09-14 through `feint proxy --record`, one apply plus
// destroy of the conformance fixture sent **148 `GET /metadata`, every one
// answered 404** — 148 of that run's 169 refusals (#776).
//
// Nothing failed, and that was luck rather than a decision: scw/client.go reads
// the metadata and its callers discard the error, so the SRN stayed empty. A
// caller that stops discarding it turns this into a failure with no change on
// this side, which is the shape of #257 exactly.
//
// # The values are measured, not invented
//
// A read-only shot at a real fr-par account, 2026-09-14 and again 2026-09-15:
//
//	GET https://api.scaleway.com/metadata  ->  200
//	{"platform": "external", "partition": "scw", "domain": "scw.eu"}
//
// Rule 4 says the shape comes from the provider rather than from a guess, and
// these three strings are the provider's own answer. They are constants because
// they describe the platform rather than the account: the same three values come
// back for any caller, which is what makes them safe to serve without an account
// to read them from.
//
// # Which artefact holds which half, because they do not hold the same one
//
// corpus/scaleway/scw-gateway.jsonl carries the exchange and `corpus:check`
// replays it, but a committed corpus is SANITISED: it keeps the status, the
// field tree and the types, and replaces every value with a synthetic one of the
// same shape. So it proves this route answers 200 with three string fields under
// those names — and it cannot prove the values.
//
// The values live in two places that a test can fail on:
// tools/contract/scaleway-gateway.yaml, which is what the contract extraction
// reads, and TestTheGatewayAnswersTheMetadataTheSdkAsksFor, which writes the
// three strings out rather than reading them from the map below — comparing the
// answer against its own source would pass whatever both became.
//
// # The operation had to exist before the route could
//
// `Route.Operation` must name an operation the drift scan finds, and the scan
// walked `api/<product>/<version>` only. `scw/Client.GetAPIMetadata` lives
// outside it, so the route would have been an orphan — and all three baselines
// carry zero. internal/drift/scan_scaleway.go now reads the gateway package too,
// on a criterion of its own: a method that BUILDS a request rather than one that
// carries someone else's.

// gatewayMetadata is what api.scaleway.com answers at /metadata. Exported
// nowhere: a client reads it through the route, and a test reads it from here so
// the two cannot drift apart.
var gatewayMetadata = map[string]any{
	"platform":  "external",
	"partition": "scw",
	"domain":    "scw.eu",
}

// metadata answers the gateway's description of the platform.
//
// No zone, no project, no authentication branch: the real gateway answers this
// to any caller that reaches it, which the recording shows, and inventing a
// refusal here would be a behaviour nobody measured.
//
// TestTheGatewayAnswersTheMetadataTheSdkAsksFor fails without this.
func (p *Pack) metadata(w http.ResponseWriter, _ *http.Request) {
	emulator.WriteJSON(w, http.StatusOK, gatewayMetadata)
}
