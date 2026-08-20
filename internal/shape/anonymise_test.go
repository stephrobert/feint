package shape

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/trace"
)

// A recording of one resource must not carry that resource into the catalogue.
//
// The catalogue is committed and the recording is not, and the whole of that
// difference is the package's claim: paths and types, no values, no
// identifiers. A path broke the claim the day a read list grew a call that
// addresses one object — the identifier lands in Operation.Path and, when the
// recording could not name the operation, in the key as well.
//
// Both are asserted, because they are two writes: an early version normalised
// the stored path and keyed off the raw one, which put the account's UUID in
// the file anyway, under a name no gate would ever look up.
func TestARecordedIdentifierNeverReachesTheCatalogue(t *testing.T) {
	const id = "3f2a91c4-77b0-4d19-9c2e-51ab8e0d64f7"
	cat := New("scaleway")
	cat.Merge([]trace.Exchange{{
		Method: "GET",
		Path:   "/vpc/v2/regions/fr-par/private-networks/" + id,
		Status: 200,
		Res:    &trace.Message{Body: map[string]any{"id": id, "name": "feint-shape-270"}},
	}})

	wanted := "GET /vpc/v2/regions/fr-par/private-networks/" + Placeholder
	op, known := cat.Operations[wanted]
	if !known {
		t.Fatalf("the operation was keyed %v, want %q", keys(cat), wanted)
	}
	if strings.Contains(op.Path, id) {
		t.Errorf("Operation.Path carries the account's identifier: %s", op.Path)
	}
	if got := render(t, cat); strings.Contains(got, id) {
		t.Errorf("the saved catalogue carries the account's identifier:\n%s", got)
	}
}

// Only an identifier is replaced. A region, a zone and a product name are the
// call, not somebody's account, and a rule that swallowed them would make two
// different operations share one key — which is worse than a leak, because it
// silently merges two field trees.
func TestAnonymisingAPathTouchesNothingButIdentifiers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/instance/v1/zones/fr-par-1/servers", "/instance/v1/zones/fr-par-1/servers"},
		{"/instance/v1/zones/fr-par-1/products/servers", "/instance/v1/zones/fr-par-1/products/servers"},
		{"/v2/instance/6d4c02be-9a15-4f83-8b7d-2e91c40fa5d8", "/v2/instance/" + Placeholder},
		{"/api/v1/ReadVms", "/api/v1/ReadVms"},
		// Uppercase is the same identifier, and a segment one character short
		// of a UUID is not one.
		{"/x/6D4C02BE-9A15-4F83-8B7D-2E91C40FA5D8", "/x/" + Placeholder},
		{"/x/6d4c02be-9a15-4f83-8b7d-2e91c40fa5d", "/x/6d4c02be-9a15-4f83-8b7d-2e91c40fa5d"},
	} {
		if got := AnonymisePath(tc.in); got != tc.want {
			t.Errorf("AnonymisePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// identifier is the shape of a UUID anywhere in a file, not only in a path
// segment: the scan below is deliberately wider than AnonymisePath, because its
// job is to disbelieve AnonymisePath.
var identifier = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// The committed catalogues carry no identifier, read from disk.
//
// AnonymisePath is a rule about the paths this repository knows how to
// recognise; this asserts the property those files must actually have, on the
// bytes a commit would publish. The two are not the same claim, and a provider
// that adopted an identifier spelling AnonymisePath does not know would satisfy
// the first and fail this one — which is the order things should fail in.
func TestNoCommittedCatalogueCarriesAnIdentifier(t *testing.T) {
	dir := filepath.Join("..", "..", "shapes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	scanned := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // a committed artefact of this repository
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		scanned++
		if found := identifier.FindString(string(body)); found != "" {
			t.Errorf("%s carries %q, which belongs to whoever recorded it", e.Name(), found)
		}
	}
	// Asserted rather than assumed: a scan that found no file would pass while
	// measuring nothing, which is the failure mode this repository has already
	// paid for elsewhere.
	if scanned == 0 {
		t.Fatalf("no catalogue found in %s: the scan is broken, not the files", dir)
	}
}
