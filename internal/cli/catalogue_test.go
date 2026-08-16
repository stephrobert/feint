package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// Every pack serves the inventory its client reads before creating anything.
//
// This is trap #1 of CLAUDE.md, given a control it never had (#218). `scw
// instance server create` reads the server types, the default image and the
// resolved image before it posts a server; a 404 on any of them fails the command
// with an error that names none of this. Each pack answers it with its own fixed
// table, and until now nothing checked that across packs — so a fourth pack could
// decline its inventory, pass every unit test it had, and be found out by a real
// client on its first create.
//
// It lives here rather than in each pack for the reason the declines guard gives
// one file over: a check copied into three packs is a check the fourth will not
// have.

func cataloguePacks(t *testing.T) []emulator.Pack {
	t.Helper()
	env := emulator.DefaultEnv()
	return []emulator.Pack{scaleway.New(env), outscale.New(env), exoscale.New(env)}
}

// A pack that declares nothing is named, rather than skipped.
//
// Optional in the type system and required here on purpose: silence is exactly
// how the trap is fallen into, so the absence has to be as loud as a wrong
// answer. A pack that genuinely serves no inventory writes one entry saying so
// and the next author cannot forget by accident.
func TestEveryPackDeclaresTheCatalogueItsClientReads(t *testing.T) {
	for _, pack := range cataloguePacks(t) {
		catalogued, ok := pack.(emulator.Catalogued)
		if !ok {
			t.Errorf("%s declares no catalogue: a client reads an inventory before it can "+
				"create anything, and nothing here says which routes that is", pack.Name())
			continue
		}
		entries := catalogued.Catalogue()
		if len(entries) == 0 {
			t.Errorf("%s declares an empty catalogue", pack.Name())
			continue
		}
		if unexplained := emulator.UnexplainedCatalogue(entries); len(unexplained) > 0 {
			t.Errorf("%s declares catalogue routes that say nothing about what a client reads "+
				"them for: %s", pack.Name(), strings.Join(unexplained, ", "))
		}
		if missing := emulator.CatalogueRoutesNotMounted(entries, pack.Routes()); len(missing) > 0 {
			t.Errorf("%s declares catalogue routes it does not serve: %s — which is the trap "+
				"itself, a catalogue that reads as present and answers 404",
				pack.Name(), strings.Join(missing, ", "))
		}
	}
}

// And the answer is driven, because a mounted route proves nothing.
//
// A catalogue that answers `[]` fails a client exactly as a 404 does: the CLI
// resolves a type or an image out of that list and finds nothing. So each entry
// is called against a fresh emulator, and its collection must come back with at
// least one item.
func TestEveryDeclaredCatalogueAnswersSomething(t *testing.T) {
	env := emulator.DefaultEnv()
	packs := []emulator.Pack{scaleway.New(env), outscale.New(env), exoscale.New(env)}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	for _, pack := range packs {
		catalogued, ok := pack.(emulator.Catalogued)
		if !ok {
			continue // reported by the test above; not reported twice
		}
		for _, entry := range catalogued.Catalogue() {
			// A path parameter is filled with the value a client would send.
			// Only the zone appears in these routes, and only in one pack.
			path := strings.ReplaceAll(entry.Path, "{zone}", "fr-par-1")
			if strings.Contains(path, "{") {
				t.Errorf("%s: %s carries a path parameter the guard cannot fill: %s",
					pack.Name(), entry.Path, path)
				continue
			}

			req, err := http.NewRequest(entry.Method, ts.URL+path, strings.NewReader("{}"))
			if err != nil {
				t.Fatalf("%s %s: %v", entry.Method, path, err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", entry.Method, path, err)
			}
			var body map[string]any
			decodeErr := json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s: %s %s answers %d — a client reading %s fails before it creates "+
					"anything", pack.Name(), entry.Method, path, resp.StatusCode, entry.Reads)
				continue
			}
			if decodeErr != nil {
				t.Errorf("%s: %s %s did not answer JSON: %v", pack.Name(), entry.Method, path, decodeErr)
				continue
			}
			// A list or an object, because the clouds differ and neither shape is
			// wrong: Scaleway keys its server types by name
			// ({"servers": {"DEV1-S": …}}), the other two answer arrays. What the
			// guard is about is whether anything is in there.
			count, found := collectionSize(body[entry.Collection])
			if !found {
				t.Errorf("%s: %s %s carries no %q; a client reading %s finds nothing to resolve",
					pack.Name(), entry.Method, path, entry.Collection, entry.Reads)
				continue
			}
			if count == 0 {
				t.Errorf("%s: %s %s answers an empty %s — which fails a client exactly as a 404 "+
					"does, since it resolves %s out of that list",
					pack.Name(), entry.Method, path, entry.Collection, entry.Reads)
			}
		}
	}
}

// collectionSize counts an inventory whatever shape the provider gives it, and
// reports whether the key held one at all.
func collectionSize(value any) (int, bool) {
	switch held := value.(type) {
	case []any:
		return len(held), true
	case map[string]any:
		return len(held), true
	default:
		return 0, false
	}
}
