package resource

import (
	"crypto/sha256"
	"encoding/hex"
)

// DerivedID builds a UUID-shaped identifier from a seed, deterministically.
//
// Two packs need the same thing for the same reason, which is what makes this
// the core's business rather than either pack's: an emulated object that has no
// row of its own still has to answer an identifier, and that identifier must be
// the same on the second read as on the first. Scaleway derives the id of a
// subnet the API publishes but this emulator does not store separately
// (internal/providers/scaleway/vpc.go); Exoscale derives the id of a zone, which
// is a property of the deployment and not a resource anybody created
// (internal/providers/exoscale/catalog.go). Written twice, a defect fixed on one
// side survives on the other — CLAUDE.md's factorisation rule, and the seam it
// names.
//
// Derived rather than random so it survives a restart: a client that stored the
// value yesterday finds the same one today, the way it would upstream. The seed
// is the caller's, and it must name what the identifier is for as well as which
// object it belongs to — two records under one seed would be one UUID for two
// things, and a client holding both could not tell which it had stored.
//
// The shape is a version-4 variant-8 UUID because that is what all three of
// these APIs publish; the bits are a hash rather than randomness, which nothing
// on the wire can tell apart and no client parses for entropy.
func DerivedID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	hexed := hex.EncodeToString(sum[:])
	return hexed[0:8] + "-" + hexed[8:12] + "-4" + hexed[13:16] + "-8" + hexed[17:20] + "-" + hexed[20:32]
}
