package network

import (
	"net/netip"
	"testing"
)

func TestULA64IsDeterministicAndWellFormed(t *testing.T) {
	ula := netip.MustParsePrefix("fd00::/8")

	first := ULA64("11111111-2222-4333-8444-555555555555")
	second := ULA64("11111111-2222-4333-8444-555555555555")
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

	other := ULA64("another-seed")
	if other == first {
		t.Errorf("two seeds derived the same block %s; the seed is not reaching the hash", first)
	}
}
