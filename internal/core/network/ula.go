package network

import (
	"crypto/sha256"
	"net/netip"
)

// ULA64Within derives a unique-local IPv6 /64, deterministically, inside the
// /48 that belongs to a space.
//
// The layout is RFC 4193's: the fd00::/8 prefix, a 40-bit global ID, a 16-bit
// subnet ID, and 64 zeroed host bits. The RFC wants the global ID picked at
// random; here it is derived from the space instead, and the subnet ID from the
// seed, because an emulator's blocks must be reproducible — a prefix that
// changed between two reads of one resource is a permanent Terraform diff, and
// one that changed across a snapshot round-trip breaks every stored reference
// to it.
//
// Two seeds rather than one, and that split is a measurement. On 2026-08-20 two
// Private Networks created in one Scaleway project answered
// fdb2:1bb5:120a:9b::/64 and fdb2:1bb5:120a:6cad::/64 — the same global ID, two
// subnet IDs, which is RFC 4193 applied the way the RFC describes it. Derived
// from the resource alone, as this was first written, sibling networks landed
// in unrelated /48s and nothing in one tenancy looked related to anything else
// in it.
//
// The caller supplies both: the space is whatever owns the /48 — a project, a
// tenancy, an account — and the seed is the resource's own identity. What
// neither can be is a provider's name: the honest bound of that measurement is
// that this account's project and organization identifiers are the same value,
// so which of the two Scaleway keys the /48 to is not distinguishable from
// here.
//
// The collision odds of a 16-bit subnet ID are the caller's to close — real,
// not astronomical, at a few hundred networks in one space: check the result
// against the blocks already held, and re-derive with a salted seed on a clash.
// Salting the seed keeps the space's /48 and moves only the subnet ID, which is
// what makes that loop terminate inside the right prefix.
func ULA64Within(space, seed string) netip.Prefix {
	global := sha256.Sum256([]byte(space))
	subnet := sha256.Sum256([]byte(seed))
	var addr [16]byte
	addr[0] = 0xfd
	copy(addr[1:6], global[:5])
	copy(addr[6:8], subnet[:2])
	return netip.PrefixFrom(netip.AddrFrom16(addr), 64)
}
