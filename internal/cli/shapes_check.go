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
//
// A gap a pack has declined at field level (emulator.FieldDecliner) is excused
// rather than counted: it is printed with its reason, because what the gate
// subtracts must stay visible, and it does not redden the build. What does:
//   - an undeclared gap — exit 2, the drift code, because either the emulator
//     or the cloud moved;
//   - a decline whose reason carries no decision — exit 1, the same guard
//     Declined() faces;
//   - a decline that excuses nothing — exit 1, because a decline that outlives
//     its gap reads as a decision about a field the emulator now serves, or
//     never omitted, and keeping it would let the list rot into fiction.
func checkShapes(dir string, providers []string, stdout, stderr io.Writer) int {
	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		fmt.Fprintf(stderr, "feint: build the emulator: %v\n", err)
		return exitError
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		fmt.Fprintf(stderr, "feint: build the emulator: %v\n", err)
		return exitError
	}
	// The same gate the operation-level refusals face in coverage(): a decline
	// with an unusable reason, or written twice, is refused before anything is
	// compared. TestAnUnusableFieldDeclineReasonFailsTheGate fails without it.
	declines := map[string][]emulator.FieldDecline{}
	unusable := 0
	for _, p := range srv.Packs() {
		fd := emulator.FieldDeclinesOf(p)
		for _, f := range emulator.UnexplainedFieldDeclines(fd) {
			fmt.Fprintf(stderr, "feint: %s declines the field %s with no usable reason\n", p.Name(), f)
			unusable++
		}
		for _, f := range emulator.DuplicateFieldDeclines(fd) {
			fmt.Fprintf(stderr, "feint: %s declines the field %s more than once\n", p.Name(), f)
			unusable++
		}
		declines[p.Name()] = fd
	}
	if unusable > 0 {
		return exitError
	}
	// Derived from the mounted packs rather than written here. A hardcoded list
	// made a fourth pack invisible to this gate instead of reported: the honest
	// degradation below — "no catalogue; nothing to check" — was unreachable for
	// a pack the list omitted, and nothing failed to say so.
	// TestShapesCheckCoversEveryMountedPack fails without this.
	if len(providers) == 0 {
		for _, p := range srv.Packs() {
			providers = append(providers, p.Name())
		}
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	total, missing, excused, stale := 0, 0, 0, 0
	var unanswered []string
	for _, name := range providers {
		p := upstream.Provider(name)
		cat, err := readCatalogue(filepath.Join(dir, name+".json"), name)
		if err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		if cat == nil {
			// No catalogue means nothing observed, so nothing to compare — and
			// nothing to hold this provider's declines against either: their
			// staleness is only measurable where a comparison ran.
			fmt.Fprintf(stdout, "%s: no catalogue in %s; nothing to check\n", name, dir)
			continue
		}

		checked, gaps, excusedHere, unused, silent := checkProvider(p, cat, declines[name], ts, stdout)
		total += checked
		missing += gaps
		excused += excusedHere
		// Printed, not counted against anything. An operation the emulator
		// answers with a refusal is compared against nothing, and it used to
		// leave no trace at all: the coverage line said "50 compared" and eleven
		// recorded operations had quietly dropped out of the arithmetic. A
		// number that shrinks without saying so is the failure this file's own
		// coverage line exists to prevent.
		unanswered = append(unanswered, silent...)
		// A decline that excused no gap is stale: the field is served now, or
		// was never observed missing. Failing on it is what keeps the declined
		// list a set of live decisions instead of an archive — the analogue of
		// the orphan-route check on Route.Operation.
		// TestAStaleFieldDeclineFailsTheGate fails without this.
		for _, d := range unused {
			fmt.Fprintf(stderr, "feint: %s declines %s %s, and either the emulator serves it or the recording no longer carries it: the decline is stale, remove it\n", name, d.Operation, d.Path)
			stale++
		}
	}

	// Coverage is printed whatever the verdict. A gate that says "ok" without
	// saying how much it looked at reads as "nothing is wrong" when it may mean
	// "nothing was checked" — the same reason `feint status` prints how many
	// routes no client has driven.
	fmt.Fprintf(stdout, "\n%d operation(s) compared, %d field(s) the real cloud returns and this emulator does not\n", total, missing)
	if len(unanswered) > 0 {
		// Named, so the gap between "recorded" and "compared" is legible
		// instead of being a subtraction nobody performs. Two things land
		// here and they are both worth seeing: an operation this emulator does
		// not serve at all, and one that needs an object the offline store does
		// not hold — every element read is in the second class by construction.
		sort.Strings(unanswered)
		fmt.Fprintf(stdout, "%d recorded operation(s) this emulator did not answer offline, so nothing was compared:\n", len(unanswered))
		for _, u := range unanswered {
			fmt.Fprintf(stdout, "  unchecked %s\n", u)
		}
	}
	if excused > 0 {
		// Counted out loud so a growing declined list stays visible instead of
		// silently hollowing the gate.
		fmt.Fprintf(stdout, "%d field(s) knowingly not served, each printed above with its reason\n", excused)
	}
	if stale > 0 {
		return exitError
	}
	if missing > 0 {
		fmt.Fprintln(stderr, "feint: run `feint shapes --record --provider <name>` on a station with an account if the cloud is what changed")
		return exitDrift
	}
	return exitOK
}

// checkProvider drives one provider's read list against the emulator.
//
// Besides the counts, it returns the declines that excused nothing, for the
// caller to refuse — whether a decline is stale is only known once every
// operation of its provider has been compared — and the recorded operations the
// emulator did not answer, which are compared against nothing and used to
// vanish from the arithmetic without a word.
func checkProvider(p upstream.Provider, cat *shape.Catalogue, declines []emulator.FieldDecline, ts *httptest.Server, stdout io.Writer) (checked, missing, excused int, unused []emulator.FieldDecline, unanswered []string) {
	// Two gates read DeclinedFields(), each joining on its own spelling: this
	// one on the catalogue key ("GET /path", or the operation name where the
	// recording carried one), the live field gate on the mounted operation
	// name ("ipam/v1/API.ListIPs"). A decline addressed to the other gate is
	// not stale here — it is simply not this gate's to judge — so only the
	// declines this gate can resolve are held to the staleness rule.
	// TestADeclineSpelledForTheLiveGateIsNotStaleHere fails without this.
	keys := map[string]bool{}
	for _, call := range upstream.Reads[p] {
		key, _, _ := callIdentity(p, call)
		keys[key] = true
	}
	mine := make([]emulator.FieldDecline, 0, len(declines))
	for _, d := range declines {
		if keys[d.Operation] {
			mine = append(mine, d)
		}
	}
	declines = mine

	used := make([]bool, len(declines))
	// A decline can be judged stale only where this gate could judge it at
	// all. Offline, the store is empty: a list answers no element, and every
	// field under one is skipped rather than compared (absentFrom). A decline
	// for such a field excuses nothing here without being wrong — the live
	// field gate is where it works — so staleness needs two more facts per
	// decline: does the recording still carry a matching field, and does the
	// emulator demonstrably serve one. Stale is "served now" or "nothing in
	// the recording to excuse"; recorded-but-unreachable is silence.
	// TestAnUnevaluableElementDeclineIsNotStale fails without this.
	wantMatched := make([]bool, len(declines))
	servedMatched := make([]bool, len(declines))
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
			unanswered = append(unanswered, string(p)+" "+key)
			continue
		}
		checked++

		for i, d := range declines {
			if d.Operation != key {
				continue
			}
			for _, f := range want.Fields {
				if d.Matches(key, f.Path) {
					wantMatched[i] = true
					break
				}
			}
			for served := range got {
				if d.Matches(key, served) {
					servedMatched[i] = true
					break
				}
			}
		}

		var lines []string
		for _, g := range absentFrom(want.Fields, got) {
			if i := matchingDecline(declines, key, g.Path); i >= 0 {
				used[i] = true
				excused++
				lines = append(lines, fmt.Sprintf("  declined %s: %s", g.Path, declines[i].Reason))
				continue
			}
			missing++
			lines = append(lines, fmt.Sprintf("  missing  %s: %s", g.Path, g.Type))
		}
		if len(lines) > 0 {
			fmt.Fprintf(stdout, "\n%s %s\n", p, key)
			for _, l := range lines {
				fmt.Fprintln(stdout, l)
			}
		}
	}
	for i, ok := range used {
		if ok {
			continue
		}
		// Recorded, not served, and yet never excused: the comparison could
		// not reach the field (an element of a list the offline store leaves
		// empty). Not stale — the live gate is where this decline works.
		if wantMatched[i] && !servedMatched[i] {
			continue
		}
		unused = append(unused, declines[i])
	}
	return checked, missing, excused, unused, unanswered
}

// matchingDecline is the first decline covering this field, or -1. First match
// wins; two declines that could both cover one field are caught upstream — an
// exact duplicate by DuplicateFieldDeclines, an overlapping pair because the
// shadowed one excuses nothing and fails the gate as stale.
func matchingDecline(declines []emulator.FieldDecline, operation, path string) int {
	for i, d := range declines {
		if d.Matches(operation, path) {
			return i
		}
	}
	return -1
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
// operation with itself under another name and finds nothing — which is why
// the Outscale identity comes from upstream.OutscaleCall, the same function
// the recorder writes the catalogue with, rather than from a second copy here
// that a fix in one would leave broken in the other.
func callIdentity(p upstream.Provider, call string) (key, method, path string) {
	if p == upstream.Outscale {
		return upstream.OutscaleCall(call)
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
