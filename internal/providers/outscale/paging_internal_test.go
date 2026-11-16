package outscale

import (
	"strings"
	"testing"
)

// The token's spelling is this package's own, and it has one form per offset:
// a client only ever echoes it, but a test asserting on the bytes needs the
// encoding to be canonical, and a decoder that accepted "02" and "2" would
// answer two tokens for one page.
//
// Standard padded base64, because the field is `format: byte` in the SDK's
// api.yaml: a client that decodes and re-encodes a raw "Mg" sends "Mg==" on the
// next request, and a decoder that refused the padded spelling would refuse the
// page it had just issued.
func TestAPageTokenIsCanonical(t *testing.T) {
	for _, offset := range []int{0, 1, 2, 41, 1000} {
		token := encodePageToken(offset)
		decoded, err := decodePageToken(token)
		if err != nil {
			t.Fatalf("offset %d encoded to %q and would not decode: %v", offset, token, err)
		}
		if decoded != offset {
			t.Fatalf("offset %d came back as %d", offset, decoded)
		}
		// Padded, and able to round-trip through a client that re-encodes.
		if rest := len(token) % 4; rest != 0 {
			t.Fatalf("token %q is not padded base64 (length %d)", token, len(token))
		}
		if reencoded := encodePageToken(decoded); reencoded != token {
			t.Fatalf("offset %d spells %q and round-trips as %q", offset, token, reencoded)
		}
	}
	if got := encodePageToken(2); got != "Mg==" {
		t.Fatalf("offset 2 spells %q; format: byte is padded base64, which is Mg==", got)
	}

	for _, bad := range []string{
		"",
		"not-a-token",
		"LTE=",  // "-1": a negative offset names no page
		"MDI=",  // "02": not the one spelling of 2
		"MS4w",  // "1.0": not a whole number
		"Mg",    // unpadded: the envelope is standard base64
		" Mg==", // leading space
		"Mg== ", // trailing space
	} {
		if _, err := decodePageToken(bad); err == nil {
			t.Fatalf("%q decoded as a token this emulator issued", bad)
		}
	}

	// An offset past any inventory a test would build still decodes: whether the
	// page holds rows is paginate's question, not the token's.
	if offset, err := decodePageToken("OTk5OTk="); err != nil || offset != 99999 {
		t.Fatalf("a canonical but far offset decoded as %d, %v", offset, err)
	}
}

// paginate serves the window the size and the token name, and answers the token
// of the page after it.
func TestPaginateWalksTheRowsOnce(t *testing.T) {
	rows := []int{10, 20, 30}
	size := 2

	first, next, err := paginate(rows, &size, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0] != 10 || first[1] != 20 {
		t.Fatalf("first page is %v", first)
	}
	if next == "" {
		t.Fatal("a truncated page issued no token")
	}

	second, last, err := paginate(rows, &size, &next)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0] != 30 {
		t.Fatalf("second page is %v", second)
	}
	if last != "" {
		t.Fatalf("the last page issued %q", last)
	}
}

// A token naming a page past the end is an empty page, not an error and not a
// repeat of the last one: the page exists and holds nothing.
func TestATokenPastTheEndAnswersAnEmptyPage(t *testing.T) {
	rows := []int{10, 20}
	size := 2
	past := encodePageToken(5)

	served, next, err := paginate(rows, &size, &past)
	if err != nil {
		t.Fatal(err)
	}
	if len(served) != 0 || next != "" {
		t.Fatalf("a token past the end served %v with next %q", served, next)
	}
}

// Absent size means no limit, and a token without a size still moves the
// window: a client that sends both is the common case, but refusing one without
// the other would answer a page the client cannot name.
func TestATokenWithoutASizeServesTheRemainder(t *testing.T) {
	rows := []int{10, 20, 30}
	token := encodePageToken(1)

	served, next, err := paginate(rows, nil, &token)
	if err != nil {
		t.Fatal(err)
	}
	if len(served) != 2 || served[0] != 20 || served[1] != 30 {
		t.Fatalf("an unbounded page from offset 1 served %v", served)
	}
	if next != "" {
		t.Fatalf("an unbounded page issued a token %q", next)
	}
}

// A token whose bytes are valid base64 but whose spelling this package never
// wrote has one answer: refused. "MQ" is "1" without its padding, and accepting
// it would let one page answer to two tokens in a transcript.
func TestATokenMustBeTheOneSpellingThisPackageWrites(t *testing.T) {
	for _, offset := range []int{1, 2, 41} {
		canonical := encodePageToken(offset)
		unpadded := strings.TrimRight(canonical, "=")
		if unpadded == canonical {
			continue // nothing to drop: this offset needs no padding
		}
		if _, err := decodePageToken(unpadded); err == nil {
			t.Fatalf("the unpadded spelling %q of %q decoded", unpadded, canonical)
		}
	}
}
