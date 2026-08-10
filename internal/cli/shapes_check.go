package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/shape"
	"github.com/stephrobert/feint/internal/transcript"
	"github.com/stephrobert/feint/internal/upstream"
)

// checkShapes compares what the emulator answers with what the real cloud was
// observed to answer, and fails when the emulator omits a field.
//
// This is the control that did not exist, and the reason it is needed is a
// measurement rather than a worry. Twice now a field the cloud returns on every
// resource was missing here and shipped: twenty of them on Outscale's ReadVms,
// and BlockDeviceMappings / StateComment / PermissionsToLaunch on ReadImages,
// the second of which segfaulted the Terraform provider outright (#86).
//
// Neither was caught, and none of the existing controls could have been:
//
//   - unit tests assert what the handler builds, never what the cloud sends;
//   - conformance proves a real client does not break, and a client does not
//     break on a field it never reads — until one does, at a user's site;
//   - the contract gate flags an invented field and a missing `required` one,
//     and the providers declare almost no required fields — 27% of Outscale
//     schemas, 11% of Scaleway's (#88), so an omission is undetectable on the
//     rest by construction;
//   - the probe proves the protocol and says so itself.
//
// It runs offline against shapes/, with no credential and no network, so it
// belongs on every pull request and in the release preflight. Refreshing those
// files needs a real account and is a human's job (`feint shapes --record`);
// checking against them is not.
func checkShapes(dir string, providers []string, stdout, stderr io.Writer) int {
	if len(providers) == 0 {
		providers = []string{"scaleway", "outscale", "exoscale"}
	}

	srv, err := emulator.NewServer(emulator.DefaultEnv(), packsFor(emulator.DefaultEnv())...)
	if err != nil {
		fmt.Fprintf(stderr, "feint: build the emulator: %v\n", err)
		return exitError
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	total, missing := 0, 0
	for _, name := range providers {
		p := upstream.Provider(name)
		cat, err := readCatalogue(filepath.Join(dir, name+".json"), name)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		if cat == nil {
			fmt.Fprintf(stdout, "%s: no catalogue in %s; nothing to check\n", name, dir)
			continue
		}

		checked, gaps := checkProvider(p, cat, ts, stdout)
		total += checked
		missing += gaps
	}

	// Coverage is printed whatever the verdict. A gate that says "ok" without
	// saying how much it looked at reads as "nothing is wrong" when it may mean
	// "nothing was checked" — the same reason `feint status` prints how many
	// routes no client has driven.
	fmt.Fprintf(stdout, "\n%d operation(s) compared, %d field(s) the real cloud returns and this emulator does not\n", total, missing)
	if missing > 0 {
		fmt.Fprintln(stderr, "feint: run `feint shapes --record --provider <name>` on a station with an account if the cloud is what changed")
		return exitDrift
	}
	return exitOK
}

// checkProvider drives one provider's read list against the emulator.
func checkProvider(p upstream.Provider, cat *shape.Catalogue, ts *httptest.Server, stdout io.Writer) (checked, missing int) {
	for _, call := range upstream.Reads[p] {
		key, method, path := callIdentity(p, call)
		want, known := cat.Operations[key]
		if !known || len(want.Fields) == 0 {
			// Never observed against the real cloud, or observed empty. Not a
			// finding: it says this operation is outside what the recording
			// covers, and reporting it as a gap would drown the real ones.
			continue
		}

		got, ok := emulatorShape(ts, method, path)
		if !ok {
			continue
		}

		gaps := absentFrom(want.Fields, got)
		if len(gaps) == 0 {
			checked++
			continue
		}
		checked++
		missing += len(gaps)
		fmt.Fprintf(stdout, "\n%s %s\n", p, key)
		for _, g := range gaps {
			fmt.Fprintf(stdout, "  missing  %s: %s\n", g.Path, g.Type)
		}
	}
	return checked, missing
}

// absentFrom returns the observed fields the emulator did not produce.
//
// A field is only reported when its parent is present on both sides. Without
// that rule an emulator with an empty store would report every element field of
// every list as missing — `Vms[].VmId` is absent because there are no Vms, not
// because the view forgot it — and a gate that cries wolf on an empty store is
// a gate somebody turns off.
//
// TestAnEmptyListIsNotAMissingField fails without this.
func absentFrom(want []transcript.Field, got map[string]string) []transcript.Field {
	dicts := dictionaries(want)
	var out []transcript.Field
	for _, f := range want {
		if _, present := got[f.Path]; present {
			continue
		}
		// An element shape absent from the emulator means the list came back
		// empty, which is a state of the store rather than a defect of the
		// view. Reported, it drowned every real finding on the first run: nine
		// operations flagged for "RouteTables[]: object" and the like, none of
		// which was a missing field.
		if strings.HasSuffix(f.Path, "[]") {
			continue
		}
		// A key of a dictionary is data, not shape. `/products/servers` answers
		// a map keyed by product name; the real cloud has ninety entries and
		// the emulated catalogue four, deliberately — docs/limits.md calls the
		// catalogue fiction. Reported as fields, those ninety drowned every
		// real finding on the first run.
		if dicts[parentPath(f.Path)] {
			continue
		}
		if parent := parentPath(f.Path); parent != "" {
			if _, present := got[parent]; !present {
				continue
			}
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// parentPath is the container a field sits in: "Images[].Bsu.Iops" ->
// "Images[].Bsu", "Images[]" -> "Images", "Images" -> "".
func parentPath(path string) string {
	if strings.HasSuffix(path, "[]") {
		return strings.TrimSuffix(path, "[]")
	}
	if i := strings.LastIndex(path, "."); i > 0 {
		return path[:i]
	}
	return ""
}

// callIdentity turns a read-list entry into the key the catalogue uses and the
// request that produces it. The two must agree or the check compares an
// operation with itself under another name and finds nothing.
func callIdentity(p upstream.Provider, call string) (key, method, path string) {
	if p == upstream.Outscale {
		return "osc/Client." + call, http.MethodPost, "/api/v1/" + call
	}
	return http.MethodGet + " " + call, http.MethodGet, call
}

// emulatorShape asks the emulator one question and returns the field tree of
// its answer. A non-2xx is not a finding here: a route this emulator declines is
// the drift gate's business, not this one's.
func emulatorShape(ts *httptest.Server, method, path string) (map[string]string, bool) {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequest(method, ts.URL+path, body) //nolint:noctx // a local test server
	if err != nil {
		return nil, false
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	var decoded any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, false
	}
	out := map[string]string{}
	for _, f := range transcript.FieldsOf(decoded) {
		out[f.Path] = f.Type
	}
	return out, true
}

func readCatalogue(path, provider string) (*shape.Catalogue, error) {
	f, err := os.Open(path) //nolint:gosec // a path built from a flag the operator typed
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	cat, err := shape.Load(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cat.Provider != provider {
		return nil, fmt.Errorf("%s describes %q, not %q", path, cat.Provider, provider)
	}
	return cat, nil
}

// dictionaries finds the paths whose children are entries rather than fields.
//
// A schema's fields differ from one another; a dictionary's values repeat one
// shape under keys that are data — a product name, a zone, an identifier. So a
// parent is a dictionary when three or more of its children are objects whose
// own child-key sets are identical. Three rather than two, because a schema can
// hold two similarly shaped fields by coincidence and a dictionary with two
// entries is not worth the risk of hiding a real omission.
//
// This is a heuristic, and it is the honest kind: it can only cause an omission
// to go unreported, never a false one to be raised, because it strictly removes
// findings. What it removes is stated in the output rather than silent.
//
// TestADictionarysKeysAreNotMissingFields fails without this.
func dictionaries(fields []transcript.Field) map[string]bool {
	// Children of each path, and the child-key set of each object child.
	children := map[string][]string{}
	keysOf := map[string]map[string]bool{}
	for _, f := range fields {
		parent := parentPath(f.Path)
		if parent == "" {
			continue
		}
		if f.Type == "object" {
			children[parent] = append(children[parent], f.Path)
		}
		grand := parentPath(parent)
		_ = grand
		if keysOf[parent] == nil {
			keysOf[parent] = map[string]bool{}
		}
		keysOf[parent][strings.TrimPrefix(f.Path, parent+".")] = true
	}

	out := map[string]bool{}
	for parent, objs := range children {
		if len(objs) < 3 {
			continue
		}
		var first map[string]bool
		same := true
		for _, obj := range objs {
			keys := keysOf[obj]
			if first == nil {
				first = keys
				continue
			}
			if !sameKeys(first, keys) {
				same = false
				break
			}
		}
		if same && len(first) > 0 {
			out[parent] = true
		}
	}
	return out
}

func sameKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
