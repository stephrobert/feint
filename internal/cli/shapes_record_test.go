package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/shape"
	"github.com/stephrobert/feint/internal/trace"
	"github.com/stephrobert/feint/internal/upstream"
)

// answering builds a reader that hands back one body per call, and records the
// order the calls were made in.
func answering(bodies map[string]any, asked *[]string) func(context.Context, string) (trace.Exchange, error) {
	return func(_ context.Context, call string) (trace.Exchange, error) {
		*asked = append(*asked, call)
		body, known := bodies[call]
		if !known {
			return trace.Exchange{Method: "GET", Path: call, Status: 404}, nil
		}
		return trace.Exchange{
			Method: "GET", Path: call, Status: 200,
			Res: &trace.Message{Body: body},
		}, nil
	}
}

// A templated entry is filled in from the collection's own answer, and the call
// that goes out carries the identifier the account actually holds.
//
// The recording that comes back names the concrete object; the catalogue does
// not, and that split is the whole reason this can be committed at all
// (internal/shape.AnonymisePath).
func TestATemplatedReadResolvesFromTheCollectionItFollows(t *testing.T) {
	const id = "3f2a91c4-77b0-4d19-9c2e-51ab8e0d64f7"
	collection := "/vpc/v2/regions/fr-par/private-networks"
	var asked []string
	var out strings.Builder

	exs, err := readEach(context.Background(), upstream.Scaleway,
		[]string{collection, collection + "/" + shape.Placeholder},
		answering(map[string]any{
			collection: map[string]any{
				"private_networks": []any{map[string]any{"id": id, "name": "feint-shape-270"}},
				"total_count":      1,
			},
			collection + "/" + id: map[string]any{"id": id, "has_s3_integration": false},
		}, &asked),
		&out)
	if err != nil {
		t.Fatalf("readEach: %v", err)
	}
	if len(asked) != 2 || asked[1] != collection+"/"+id {
		t.Fatalf("the element read went to %v, want the collection then %s", asked, collection+"/"+id)
	}
	if len(exs) != 2 {
		t.Fatalf("recorded %d exchange(s), want 2", len(exs))
	}

	cat := shape.New("scaleway")
	cat.Merge(exs)
	wanted := "GET " + collection + "/" + shape.Placeholder
	if _, known := cat.Operations[wanted]; !known {
		t.Errorf("the element read is not keyed %q; the gate looks it up under exactly the read-list entry", wanted)
	}
}

// An account holding none of the resource asks for nothing, and says so.
//
// The alternative failure is the one worth naming: reading the collection,
// finding it empty and calling `<collection>/` or `<collection>/{id}` anyway
// would record a 404 as this operation's shape, and a 404 body is a different
// schema entirely.
func TestATemplatedReadAsksNothingWhenTheAccountHasNone(t *testing.T) {
	collection := "/vpc/v2/regions/fr-par/private-networks"
	var asked []string
	var out strings.Builder

	exs, err := readEach(context.Background(), upstream.Scaleway,
		[]string{collection, collection + "/" + shape.Placeholder},
		answering(map[string]any{
			collection: map[string]any{"private_networks": []any{}, "total_count": 0},
		}, &asked),
		&out)
	if err != nil {
		t.Fatalf("readEach: %v", err)
	}
	if len(asked) != 1 {
		t.Fatalf("calls made: %v, want the collection alone", asked)
	}
	if len(exs) != 1 {
		t.Fatalf("recorded %d exchange(s), want 1", len(exs))
	}
	if !strings.Contains(out.String(), "no element to read") {
		t.Errorf("the run went quiet about an unresolved entry:\n%s", out.String())
	}
}

// A templated entry placed before its collection is refused, not skipped.
//
// Skipped, it would be indistinguishable from an account holding none of the
// resource — a mistake in the read list would read as a fact about somebody's
// cloud, and the catalogue would quietly stop covering an operation nobody
// noticed had dropped out.
func TestATemplatedReadBeforeItsCollectionIsRefused(t *testing.T) {
	collection := "/vpc/v2/regions/fr-par/private-networks"
	var asked []string
	var out strings.Builder

	_, err := readEach(context.Background(), upstream.Scaleway,
		[]string{collection + "/" + shape.Placeholder, collection},
		answering(nil, &asked), &out)
	if err == nil {
		t.Fatalf("an element read before its collection was accepted; calls made: %v", asked)
	}
	if !strings.Contains(err.Error(), collection) {
		t.Errorf("the refusal does not name the collection that is missing: %v", err)
	}
}

// Every templated entry of every read list is preceded by its collection.
//
// The refusal above only bites when somebody runs the recorder, which needs an
// account; this holds the same rule against the list as committed, offline, on
// every pull request.
func TestEveryTemplatedReadFollowsItsCollection(t *testing.T) {
	templated := 0
	for p, calls := range upstream.Reads {
		seen := map[string]bool{}
		for _, call := range calls {
			if collection, ok := upstream.TemplateOf(call); ok {
				templated++
				if !seen[collection] {
					t.Errorf("%s: %s reads one element of %s, which no earlier entry asks for", p, call, collection)
				}
			}
			seen[call] = true
		}
	}
	// Asserted rather than assumed: with no templated entry anywhere this test
	// would pass while measuring nothing.
	if templated == 0 {
		t.Fatalf("no templated entry in any read list, so this check proves nothing")
	}
}
