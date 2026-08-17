package network

import (
	"crypto/sha256"
	"net/netip"
)

// ULA64 derives a unique-local IPv6 /64 from a seed, deterministically.
//
// The layout is RFC 4193's: the fd00::/8 prefix, a 40-bit global ID, a 16-bit
// subnet ID, and 64 zeroed host bits. The RFC wants the global ID picked at
// random; here it is the first seven bytes of SHA-256(seed) instead, because an
// emulator's blocks must be reproducible: a prefix that changed between two
// reads of the same resource would be a permanent Terraform diff, and one that
// changed across a snapshot round-trip would break every stored reference to
// it. The seed is the caller's resource identity, so two resources get two
// blocks and one resource always gets the same.
//
// The collision odds of a 56-bit hash are the caller's to close: check the
// result against the blocks already held, and re-derive with a salted seed on
// the (astronomically unlikely) clash.
func ULA64(seed string) netip.Prefix {
	sum := sha256.Sum256([]byte(seed))
	var addr [16]byte
	addr[0] = 0xfd
	copy(addr[1:8], sum[:7])
	return netip.PrefixFrom(netip.AddrFrom16(addr), 64)
}
