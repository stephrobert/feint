package emulator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// newUIServer builds a server with the page mounted for addr, and drives it
// through the real mux rather than through the handlers directly: the question
// every test in this file asks is what a browser gets, and only the mux can
// answer that.
func newUIServer(t *testing.T, addr string, ui ...emulator.UI) (*emulator.Server, bool) {
	t.Helper()
	srv, err := emulator.NewServer(emulator.DefaultEnv())
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	conf := emulator.UI{Addr: addr}
	if len(ui) > 0 {
		conf = ui[0]
		conf.Addr = addr
	}
	return srv, srv.MountUI(conf)
}

func uiGet(t *testing.T, srv *emulator.Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The page answers on a loopback listener, and its two assets with it.
//
// This is the accepting half, and it is not decoration: a mount guard that
// refused every address would pass every refusal test in this file and ship a
// binary whose page never appears.
func TestThePageIsServedOnLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:4599", "localhost:4599", "[::1]:4599"} {
		srv, mounted := newUIServer(t, addr)
		if !mounted {
			t.Fatalf("%s: the page was not mounted on a loopback address", addr)
		}
		for _, path := range []string{"/_feint/ui", "/_feint/ui/app.css", "/_feint/ui/app.js", "/_feint/ui/data", "/_feint/resources"} {
			rec := uiGet(t, srv, path)
			if rec.Code != http.StatusOK {
				t.Errorf("%s: GET %s answered %d, want 200", addr, path, rec.Code)
			}
			if rec.Body.Len() == 0 {
				t.Errorf("%s: GET %s answered an empty body", addr, path)
			}
		}
	}
}

// Off loopback the page is not hidden, it does not exist.
//
// The distinction is the whole design: a page that was mounted and then refused
// per request would be one header away from being served to whatever a browser
// on another host asked for, and off loopback the anti-rebinding guard cannot
// tell a local page from a hostile one — measured in rebinding_test.go, where a
// forged Host is answered 200 on 0.0.0.0.
//
// The decision is tested rather than `serve` itself, deliberately: the same
// refusal was once tested by running the command it guards, and with the guard
// removed the command did its job — it listened — and the test never returned.
func TestThePageIsNotServedOffLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:4599", ":4599", "192.168.1.10:4599", "[::]:4599"} {
		srv, mounted := newUIServer(t, addr)
		if mounted {
			t.Errorf("%s: the page was mounted off loopback", addr)
		}
		for _, path := range []string{"/_feint/ui", "/_feint/ui/app.css", "/_feint/ui/app.js", "/_feint/ui/data", "/_feint/resources"} {
			rec := uiGet(t, srv, path)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: GET %s answered %d, want 404", addr, path, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "<html") {
				t.Errorf("%s: GET %s served the page anyway", addr, path)
			}
		}
		// The one endpoint that stays is the explanation. A bare 404 leaves an
		// operator hunting a typo in the URL when the cause is the address they
		// chose, which nothing in "404 page not found" could tell them.
		body := uiGet(t, srv, "/_feint/ui").Body.String()
		if !strings.Contains(body, addr) || !strings.Contains(body, "loopback") {
			t.Errorf("%s: the refusal does not say why: %s", addr, body)
		}
	}
}

// The core knows no provider, and the page is where that would leak first.
//
// Every name it shows — provider, product, operation, path — arrives as data
// from /_feint/routes, /_feint/health and the coverage artefacts. A name written
// into the asset would mean a fourth pack needs an edit here to appear, which is
// rule 5 broken in the place it is hardest to notice: a page looks right whether
// or not its labels came from the data.
func TestTheEmbeddedPageNamesNoProvider(t *testing.T) {
	assets := uiAssetFiles(t)
	if len(assets) < 3 {
		t.Fatalf("found %d embedded assets, expected the page and its two files", len(assets))
	}

	// The names come from the packs themselves rather than from a list written
	// here. A fourth pack must extend this test by existing, not by somebody
	// remembering to add it — which is the same argument as the rule it guards.
	env := emulator.DefaultEnv()
	var forbidden []string
	for _, pack := range []emulator.Pack{scaleway.New(env), outscale.New(env), exoscale.New(env)} {
		forbidden = append(forbidden, strings.ToLower(pack.Name()))
	}

	for name, body := range assets {
		lower := strings.ToLower(body)
		for _, provider := range forbidden {
			if strings.Contains(lower, provider) {
				t.Errorf("%s names %q; every provider name must arrive as data", name, provider)
			}
		}
	}
}

// Values reach the document through textContent, never through markup built
// from a string.
//
// This is the YAML lesson one layer up. cloudinit renders a structured format
// with a text template, which concatenates without escaping, and a multi-line
// public key was enough to open a top-level key in the cloud-config document.
// The same shape here would be a name a client chose becoming an element, and
// the emulator serves back plenty of client-chosen names.
func TestThePageNeverBuildsMarkupFromAString(t *testing.T) {
	forbidden := []string{"inner" + "HTML", "outer" + "HTML", "insertAdjacent" + "HTML", "document.write", "eval("}
	for name, body := range uiAssetFiles(t) {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("%s uses %s; every value must reach the document through textContent", name, bad)
			}
		}
		if !strings.Contains(body, "textContent") {
			t.Errorf("%s never writes textContent, so this test is measuring nothing", name)
		}
	}
}

// Nothing the page adds accepts a command.
//
// Both halves are asserted, because either alone is satisfiable by an accident:
// the declared list could name a GET while the mux answers a POST at the same
// path, and the mux probe alone would pass on a page that mounted nothing at
// all. The stream in /_feint/events is one-way for the same reason — an
// interface onto an emulator that starts containers must have no path back.
func TestThePageAddsOnlyGETRoutes(t *testing.T) {
	srv, mounted := newUIServer(t, "127.0.0.1:4599")
	if !mounted {
		t.Fatal("the page was not mounted")
	}

	// What the page adds is measured, not listed: the difference between a
	// server with the page and one without. A hand-kept list would stop covering
	// the day somebody mounts a fourth endpoint and forgets to add it here,
	// which is the day it would matter.
	bare, err := emulator.NewServer(emulator.DefaultEnv())
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	before := map[string]bool{}
	for _, pattern := range bare.SelfRoutes() {
		before[pattern] = true
	}

	added := []string{}
	for _, pattern := range srv.SelfRoutes() {
		if before[pattern] {
			continue
		}
		added = append(added, pattern)
		if !strings.HasPrefix(pattern, http.MethodGet+" ") {
			t.Errorf("the page adds %q, which is not a GET", pattern)
		}
	}
	if len(added) == 0 {
		t.Fatal("no route was attributed to the page, so this test measures nothing")
	}

	// And the mux itself refuses the verbs. Asserting the list alone would miss
	// a handler registered outside mountSelf.
	for _, pattern := range added {
		path := strings.TrimPrefix(pattern, http.MethodGet+" ")
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			if rec.Code == http.StatusOK {
				t.Errorf("%s %s answered 200; the page must accept no command", method, path)
			}
		}
	}
}

// The gap with the upstream API is carried with its provenance, and an absent
// artefact reads as unknown rather than as zero.
//
// A versioned number shown alone is read as a measurement taken now. The page
// therefore gets the directory it came from, when the files were written, and
// the command that refreshes them — and when there are none, Available is false
// so the page can say so instead of drawing three empty bars that look like a
// fully covered API.
func TestTheUpstreamGapCarriesItsProvenance(t *testing.T) {
	srv, mounted := newUIServer(t, "127.0.0.1:4599", emulator.UI{
		Version: "1.2.3",
		Upstream: func() emulator.UpstreamView {
			return emulator.UpstreamView{
				Available: true,
				Source:    "coverage",
				WrittenAt: "2026-08-07T10:00:00Z",
				Products: []emulator.UpstreamProduct{
					{Provider: "a-cloud", Product: "compute", Served: 3, Declined: 4, Untriaged: 1, Total: 8},
				},
			}
		},
	})
	if !mounted {
		t.Fatal("the page was not mounted")
	}

	var data struct {
		Version  string                `json:"version"`
		Upstream emulator.UpstreamView `json:"upstream"`
	}
	if err := json.Unmarshal(uiGet(t, srv, "/_feint/ui/data").Body.Bytes(), &data); err != nil {
		t.Fatalf("decode the page's data: %v", err)
	}
	if data.Version != "1.2.3" {
		t.Errorf("version is %q, want the one the caller stamped", data.Version)
	}
	if !data.Upstream.Available || data.Upstream.WrittenAt == "" || data.Upstream.Source == "" {
		t.Errorf("the gap arrived without its provenance: %+v", data.Upstream)
	}
	if data.Upstream.Refresh == "" {
		t.Error("no refresh command: a versioned number nobody can refresh is one nobody trusts")
	}
	if len(data.Upstream.Products) != 1 || data.Upstream.Products[0].Untriaged != 1 {
		t.Errorf("the products did not survive the round trip: %+v", data.Upstream.Products)
	}

	// No reader supplied: unknown, and never zero.
	bare, _ := newUIServer(t, "127.0.0.1:4599")
	if err := json.Unmarshal(uiGet(t, bare, "/_feint/ui/data").Body.Bytes(), &data); err != nil {
		t.Fatalf("decode the page's data: %v", err)
	}
	if data.Upstream.Available {
		t.Error("the gap reads as available with no artefact behind it")
	}
	if data.Upstream.Refresh == "" {
		t.Error("the fallback carries no refresh command, which is when it is most needed")
	}
}

// Every node the script writes into exists in the document it ships with.
//
// This is the silent half of a page. setText returns quietly on a missing node,
// which is the right behaviour at runtime — a page must not blank itself over
// one renamed element — and it means a typo in an identifier costs nothing at
// load time and shows up as a number that never updates. Nobody reads a
// dashboard closely enough to notice a field that was never going to move.
func TestEveryNodeTheScriptWritesToExists(t *testing.T) {
	assets := uiAssetFiles(t)
	page := assets["index.html"]
	script := assets["app.js"]
	if page == "" || script == "" {
		t.Fatal("the page or its script is missing from the asset directory")
	}

	lookups := regexp.MustCompile(`byId\("([^"]+)"\)`).FindAllStringSubmatch(script, -1)
	if len(lookups) == 0 {
		t.Fatal("the script looks up no element, so this test measures nothing")
	}
	for _, m := range lookups {
		if !strings.Contains(page, `id="`+m[1]+`"`) {
			t.Errorf("the script writes into %q and the page has no such element", m[1])
		}
	}
}

// uiAssetFiles reads the files the binary embeds, from the repository rather
// than from the embed.FS: the point of the two tests above is that what ships is
// what was reviewed, and reading the same bytes through the same package would
// pass on an asset the build had not picked up.
func uiAssetFiles(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join(repositoryRoot(t), "internal", "core", "emulator", "ui")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the asset directory: %v", err)
	}
	out := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // a path this test built
		if readErr != nil {
			t.Fatalf("read %s: %v", entry.Name(), readErr)
		}
		out[entry.Name()] = string(body)
	}
	return out
}
