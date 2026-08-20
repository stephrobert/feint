package network

import (
	"net/netip"
	"testing"
)

func TestULA64IsDeterministicAndWellFormed(t *testing.T) {
	ula := netip.MustParsePrefix("fd00::/8")

	first := ULA64Within("space", "11111111-2222-4333-8444-555555555555")
	second := ULA64Within("space", "11111111-2222-4333-8444-555555555555")
	if first != second {
		t.Fatalf("two derivations of one seed differ: %s vs %s", first, second)
	}
	if first.Bits() != 64 {
		t.Errorf("expected a /64, got %s", first)
	}
	if !ula.Contains(first.Addr()) {
		t.Errorf("expected a unique-local block under fd00::/8, got %s", first)
	}
	if first.Masked() != first {
		t.Errorf("host bits set: %s", first)
	}

	other := ULA64Within("space", "another-seed")
	if other == first {
		t.Errorf("two seeds derived the same block %s; the seed is not reaching the hash", first)
	}
}

// Two blocks of one space are siblings under one /48; two spaces are not.
//
// This is the measured half (see ULA64Within): two Private Networks created in
// one real Scaleway project on 2026-08-20 shared fdb2:1bb5:120a::/48 and
// differed only in the subnet ID. Derived from the resource alone, which is how
// this function was first written, they would have landed in unrelated /48s.
//
// The negative half matters as much: if the space stopped reaching the hash,
// every space would share one /48 and the first assertion alone would still
// pass.
func TestOneSpacesBlocksAreSiblingsAndTwoSpacesAreNot(t *testing.T) {
	within := func(p netip.Prefix) netip.Prefix {
		return netip.PrefixFrom(p.Addr(), 48).Masked()
	}

	a := ULA64Within("project-a", "network-1")
	b := ULA64Within("project-a", "network-2")
	if a == b {
		t.Fatalf("two networks of one space got the same /64: %s", a)
	}
	if within(a) != within(b) {
		t.Errorf("two networks of one space landed in %s and %s, which share no /48", a, b)
	}

	elsewhere := ULA64Within("project-b", "network-1")
	if within(elsewhere) == within(a) {
		t.Errorf("two spaces share the /48 %s; the space is not reaching the hash", within(a))
	}
}
