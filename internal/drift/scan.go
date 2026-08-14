// Package drift measures the gap between what a provider's upstream API offers
// and what feint emulates.
//
// This is the answer to the only hard problem of a cloud emulator: not writing
// the first endpoint, but noticing that the upstream API moved. Scaleway alone
// added 453 SDK methods and removed 26 over twelve months. A human will not
// track that; a CI job will.
//
// The scan reads the provider's official Go SDK source, which is generated from
// their IDL and is therefore an exact, always-current description of the
// surface. No network call, no scraping: point it at a checkout.
//
// One reader per provider, in scan_<provider>.go, because their SDKs share no
// shape. Scaleway generates a package per product and version, every operation a
// method building its own request; Outscale generates one client per service
// from OpenAPI, every operation a method on *Client and each of them emitted
// twice. What they share is this file: the vocabulary both produce, and the one
// AST helper both need. Anything else they appear to share is a coincidence, and
// folding two readers into one on the strength of it is how a scan starts
// reporting a surface that does not exist.
package drift

import "go/ast"

// Operation is one upstream API method.
type Operation struct {
	// Name is the canonical identifier a Route.Operation must match, e.g.
	// "instance/v1/API.ListServers" or "osc/Client.ReadVms".
	Name string
	// Product locates it in the upstream SDK layout: a product directory for
	// Scaleway, a service package for Outscale. It is what makes "decline this
	// whole surface" one decision.
	Product string
	// Version is the product's API version where the SDK carries one. Outscale's
	// generated clients do not, and it stays empty rather than invented.
	Version string
	// Group is the upstream's own grouping of the operation, where a document
	// declares one: the root of Exoscale's tag hierarchy, Outscale's flat tag.
	// Scaleway needs none — its SDK layout already groups by product. Empty when
	// nothing upstream groups the operation, and every reader then falls back to
	// Product, so a provider without tags renders exactly as it did before.
	Group string
}

// receiverName returns the type name of a method receiver, dereferencing the
// pointer form both SDKs use throughout.
func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}
