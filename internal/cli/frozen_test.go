package cli

// What CI is allowed to depend on is frozen by a test, not by a sentence (#132).
//
// The surfaces here — the JSON shape of /_feint/health, /_feint/routes,
// /_feint/conformance and /_feint/trace, and the CLI's verbs, flags and exit
// codes — are consumed by pipelines outside this repository. Each one has a
// committed fixture under testdata/frozen/: a history of entries, every entry a
// schema version and the canonical form observed under it. What is frozen is the
// form — paths and JSON types, verb and flag names — never a value, because a
// fixture holding an identifier or a timestamp goes red on every run and gets
// disarmed within the week (this repository measured exactly that twice, which
// is why internal/shape stores field trees and coverage/ carries no scan date).
//
// The gate has two teeth, and both are needed:
//
//   - TestTheFrozenSurfacesStillMatchTheirFixture fails when a shape moves and
//     the fixture did not: the accidental change.
//   - TestASurfaceChangeDemandsItsVersionBump fails when the fixture moved and
//     the version the code declares did not: the regenerated-without-deciding
//     change. Regeneration appends a history entry with the next version and
//     never rewrites an old one, so the only way to move a shape under an
//     unchanged version is to edit a committed history entry in place — a diff
//     no review mistakes for routine.
//
// A deliberate change therefore goes: change the code, run
// `mise run frozen:update`, bump the matching version constant (schema.go in
// the emulator, cliSurfaceVersion here), and write the CHANGELOG line the bump
// is the signal for.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/transcript"
)

const frozenDir = "testdata/frozen"

// frozenEntry is one version of a surface: the version declared for it and the
// canonical content observed under that version.
type frozenEntry struct {
	SchemaVersion int             `json:"schema_version"`
	Content       json.RawMessage `json:"content"`
}

// frozenFixture is the append-only history of a surface. The last entry is the
// current promise; the ones before it are why in-place edits cannot hide.
type frozenFixture struct {
	History []frozenEntry `json:"history"`
}

// frozenSurface binds a name to what it currently renders and to the version
// the code declares for it.
type frozenSurface struct {
	name string
	// version is what the code says this surface's shape is worth. For the
	// three object payloads it is also served on the wire as schema_version;
	// /_feint/routes answers a bare array (see emulator/schema.go for why that
	// stays), and the CLI has no wire at all.
	version int
	// content is the canonical form: field trees for the HTTP surfaces, verbs
	// with flags and exit codes for the CLI.
	content json.RawMessage
}

// TestTheFrozenSurfacesStillMatchTheirFixture is the freeze: the live form of
// each surface equals the last committed entry of its fixture. It goes red on
// any change of the form — a key added, removed or retyped, a verb or flag
// appearing or going — and stays green across values, counters and timestamps.
func TestTheFrozenSurfacesStillMatchTheirFixture(t *testing.T) {
	for _, s := range observedSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			fixture := loadFrozen(t, s.name)
			last := fixture.History[len(fixture.History)-1]
			if !sameJSON(t, last.Content, s.content) {
				t.Errorf("the frozen surface %q moved:\n  committed: %s\n  observed:  %s\n"+
					"If this change is deliberate: run `mise run frozen:update`, bump the surface's "+
					"version constant (emulator/schema.go or cliSurfaceVersion), and add a CHANGELOG line.",
					s.name, compactJSON(t, last.Content), compactJSON(t, s.content))
			}
		})
	}
}

// TestASurfaceChangeDemandsItsVersionBump is the other tooth: the version the
// code declares (and, for the object payloads, serves) equals the version of
// the fixture's latest entry. After `mise run frozen:update` appends a new
// entry, this stays red until the constant moves — which is the moment a human
// decides the change and owes the CHANGELOG its line.
func TestASurfaceChangeDemandsItsVersionBump(t *testing.T) {
	for _, s := range observedSurfaces(t) {
		t.Run(s.name, func(t *testing.T) {
			fixture := loadFrozen(t, s.name)
			last := fixture.History[len(fixture.History)-1]
			if s.version != last.SchemaVersion {
				t.Errorf("surface %q: the code declares schema version %d, the fixture's latest entry is %d.\n"+
					"A frozen surface only moves with its version: bump the constant "+
					"(emulator/schema.go or cliSurfaceVersion) and say what changed in the CHANGELOG.",
					s.name, s.version, last.SchemaVersion)
			}
		})
	}
}

// TestFrozenHistoryOnlyGrows refuses a fixture whose entries were reordered or
// whose versions repeat: the history is the audit trail that makes an in-place
// edit visible, so it must stay strictly increasing.
func TestFrozenHistoryOnlyGrows(t *testing.T) {
	for _, s := range observedSurfaces(t) {
		fixture := loadFrozen(t, s.name)
		prev := 0
		for i, e := range fixture.History {
			if e.SchemaVersion <= prev {
				t.Errorf("surface %q: history entry %d has version %d after %d; versions must strictly increase",
					s.name, i, e.SchemaVersion, prev)
			}
			prev = e.SchemaVersion
		}
	}
}

// TestExitCodesAreFrozen holds the exit-code promise the README makes and CI
// depends on: 0 success, 1 error, 2 drift. The constants are compared against
// the frozen fixture, and the two ends of the range are proven by running the
// dispatcher rather than by reading the constants back: help answers 0, an
// unknown verb answers 1, `status` answers 0 even when nothing runs (a stopped
// emulator is a fact, not a failure), and a `wait` that times out answers 1,
// never a fourth code. The drift paths return exitDrift and are each proven
// where they live (baseline, shapes --check, docs --check); freezing the
// constant here is what keeps those proofs meaning "2".
func TestExitCodesAreFrozen(t *testing.T) {
	fixture := loadFrozen(t, "cli")
	var content struct {
		ExitCodes map[string]int `json:"exit_codes"`
	}
	if err := json.Unmarshal(fixture.History[len(fixture.History)-1].Content, &content); err != nil {
		t.Fatalf("decode the cli fixture content: %v", err)
	}
	for name, got := range map[string]int{"ok": exitOK, "error": exitError, "drift": exitDrift} {
		if want, known := content.ExitCodes[name]; !known || got != want {
			t.Errorf("exit code %q is %d, the frozen fixture says %d: CI depends on these", name, got, want)
		}
	}

	var discard strings.Builder
	if code := Run([]string{"feint", "help"}, &discard, &discard); code != exitOK {
		t.Errorf("`feint help` exited %d, want %d", code, exitOK)
	}
	if code := Run([]string{"feint", "no-such-verb"}, &discard, &discard); code != exitError {
		t.Errorf("an unknown verb exited %d, want %d", code, exitError)
	}
	// 127.0.0.1:1 is reserved and refuses connections at once, so neither call
	// waits out its timeout against a service that could exist.
	if code := Run([]string{"feint", "status", "--addr", "127.0.0.1:1"}, &discard, &discard); code != exitOK {
		t.Errorf("`feint status` against nothing exited %d, want %d: not running is a fact, not a failure", code, exitOK)
	}
	if code := Run([]string{"feint", "wait", "--addr", "127.0.0.1:1", "--timeout", "50ms"}, &discard, &discard); code != exitError {
		t.Errorf("a timed-out `feint wait` exited %d, want %d and never a fourth code", code, exitError)
	}
}

// observedSurfaces renders every frozen surface once, from a live server for
// the HTTP ones and from the dispatcher's own help for the CLI. When
// FEINT_UPDATE_FROZEN is set it also appends any changed form to the fixture
// history, at the next version — never rewriting an entry, which is what makes
// the version test above able to demand the bump.
func observedSurfaces(t *testing.T) []frozenSurface {
	t.Helper()
	surfaces := []frozenSurface{
		{name: "health", version: emulator.HealthSchemaVersion, content: observeShape(t, "/_feint/health", nil)},
		{name: "routes", version: emulator.RoutesSchemaVersion, content: observeShape(t, "/_feint/routes", nil)},
		{name: "conformance", version: emulator.ConformanceSchemaVersion, content: observeShape(t, "/_feint/conformance",
			// These maps are keyed by operation names: data, not schema. Their
			// keys are collapsed so that mounting one more route — never a
			// break, serving more of a provider's API is the point — cannot
			// move the fixture.
			[]string{"calls", "probes", "violations", "unread_request_fields", "evidence"})},
		{name: "trace", version: emulator.TraceSchemaVersion, content: observeShape(t, "/_feint/trace", nil)},
		{name: "cli", version: cliSurfaceVersion, content: cliSurfaceContent(t)},
	}
	if os.Getenv("FEINT_UPDATE_FROZEN") != "" {
		for _, s := range surfaces {
			updateFrozen(t, s)
		}
	}
	return surfaces
}

// frozenServer serves the emulator once per test run, with enough traffic
// through it that the optional parts of each payload exist to be observed: one
// real client call carrying a query string, one synthetic probe call, and one
// request nothing routes. Values stay out of the fixture by construction — only
// the field tree is kept — so none of this drives volatility.
var frozenBody = func() func(t *testing.T, path string) []byte {
	var once sync.Once
	bodies := map[string][]byte{}
	var buildErr error
	return func(t *testing.T, path string) []byte {
		t.Helper()
		once.Do(func() {
			srv, _, err := newServer(nil)
			if err != nil {
				buildErr = err
				return
			}
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			drive := func(path string, probe bool) error {
				req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
				if err != nil {
					return err
				}
				if probe {
					req.Header.Set(emulator.ProbeHeader, "frozen-test")
				}
				res, err := ts.Client().Do(req)
				if err != nil {
					return err
				}
				return res.Body.Close()
			}
			for _, call := range []struct {
				path  string
				probe bool
			}{
				{"/instance/v1/zones/fr-par-1/servers?page=1", false},
				{"/instance/v1/zones/fr-par-1/servers", true},
				{"/no-such-route-anywhere", false},
			} {
				if err := drive(call.path, call.probe); err != nil {
					buildErr = err
					return
				}
			}

			for _, path := range []string{"/_feint/health", "/_feint/routes", "/_feint/conformance", "/_feint/trace"} {
				res, err := ts.Client().Get(ts.URL + path)
				if err != nil {
					buildErr = err
					return
				}
				var body strings.Builder
				if _, err := io.Copy(&body, res.Body); err != nil {
					buildErr = err
					return
				}
				if err := res.Body.Close(); err != nil {
					buildErr = err
					return
				}
				if res.StatusCode != http.StatusOK {
					buildErr = fmt.Errorf("%s answered %d", path, res.StatusCode)
					return
				}
				bodies[path] = []byte(body.String())
			}
		})
		if buildErr != nil {
			t.Fatalf("drive the emulator for the frozen surfaces: %v", buildErr)
		}
		body, known := bodies[path]
		if !known {
			t.Fatalf("no recorded body for %s", path)
		}
		return body
	}
}()

// observeShape turns one live payload into its canonical content: the field
// tree, with data-keyed maps collapsed under "*".
func observeShape(t *testing.T, path string, dataKeyed []string) json.RawMessage {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(frozenBody(t, path), &decoded); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if top, isObject := decoded.(map[string]any); isObject {
		for _, key := range dataKeyed {
			m, isMap := top[key].(map[string]any)
			if !isMap || len(m) == 0 {
				continue
			}
			// The values, in key order, merged by the same walk that merges
			// array elements: one entry per distinct field, whatever the keys.
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			values := make([]any, 0, len(m))
			for _, k := range keys {
				values = append(values, m[k])
			}
			top[key] = map[string]any{"*": values}
		}
	}
	fields := transcript.FieldsOf(decoded)
	content, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		t.Fatalf("marshal the canonical shape of %s: %v", path, err)
	}
	return content
}

// cliSurfaceContent is the canonical CLI surface: every flag set the binary
// builds, the flags registered on it, and the exit codes.
//
// It is read from the flag.FlagSet each verb builds, never from the rendered
// help (#334). The help is prose: a flag can be added to a FlagSet and never
// written down — `feint proxy --intercept` shipped in v0.9.0 exactly that way —
// and, worse, a flag can be deleted from a FlagSet while its help line survives,
// which is the direction that breaks the consumer #132 froze this surface for.
// A surface observed through a document freezes the document.
//
// The help keeps a promise here, but as its own assertion with its own subject:
// TestTheHelpNamesEveryFlagTheBinaryAccepts.
func cliSurfaceContent(t *testing.T) json.RawMessage {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"verbs":      flagsTheBinaryAccepts(t),
		"exit_codes": map[string]int{"ok": exitOK, "error": exitError, "drift": exitDrift},
	})
	if err != nil {
		t.Fatalf("marshal the CLI surface: %v", err)
	}
	return content
}

// flagsTheBinaryAccepts drives every dispatched verb as far as its flag parsing
// and reads back the flags of the FlagSet it built, through newFlagSet's seam.
//
// `-h` is what stops each verb there: flag.ContinueOnError makes Parse answer
// flag.ErrHelp, every command returns on that error, and not one of them does
// anything before building its set. So this observes the registration and
// nothing else — no listener opened, no file written, no host touched.
//
// The result is keyed by the flag set's own name, which is the verb everywhere
// but `snapshot`: that one dispatches a second time, and "snapshot save"
// registers --force where "snapshot list" does not. Keying by the set says so,
// where a union under `snapshot` would claim all three take all three flags.
func flagsTheBinaryAccepts(t *testing.T) map[string][]string {
	t.Helper()

	sets := map[string]*flag.FlagSet{}
	builtBy := map[string]string{}
	driving := ""
	observeFlagSet = func(name string, fs *flag.FlagSet) {
		// A verb stopped with -h renders its set's defaults; the render is
		// discarded, because the flags are read from the set itself below.
		fs.SetOutput(io.Discard)
		sets[name] = fs
		builtBy[name] = driving
	}
	t.Cleanup(func() { observeFlagSet = nil })

	var discard strings.Builder
	drive := func(verb string, args ...string) {
		driving = verb
		Run(append([]string{"feint", verb}, args...), &discard, &discard)
	}

	dispatched := topLevelCommands(t)
	if len(dispatched) < 15 {
		t.Fatalf("only %d verbs found in the dispatch: the scan is broken, not the surface", len(dispatched))
	}
	for _, verb := range dispatched {
		drive(verb, "-h")
	}
	// `snapshot` dispatches again before building anything, so its subcommands
	// are driven by name. save, load and rm read the snapshot's name off the
	// command line ahead of the flags, hence the placeholder. rm registers no
	// flag at all and so contributes no entry, which is the truth about it and
	// the reason the coverage assertion below is per verb and not per set.
	drive("snapshot", "save", "a-name", "-h")
	drive("snapshot", "load", "a-name", "-h")
	drive("snapshot", "list", "-h")
	drive("snapshot", "rm", "a-name", "-h")

	// Assert the subject rather than skip on its absence. A verb whose set was
	// never observed would otherwise leave the fixture quietly smaller, and a
	// frozen surface that shrinks in silence is the defect being fixed here.
	covered := map[string]bool{}
	for name, verb := range builtBy {
		if name != verb && !strings.HasPrefix(name, verb+" ") {
			t.Errorf("`feint %s` built a flag set named %q: the surface is keyed by that name, "+
				"so it has to be the verb itself or one of its subcommands", verb, name)
		}
		covered[verb] = true
	}
	for _, verb := range dispatched {
		if !covered[verb] {
			t.Errorf("`feint %s -h` built no flag set, so this observation cannot see the verb's "+
				"flags and the frozen surface would freeze nothing for it", verb)
		}
	}

	verbs := make(map[string][]string, len(sets))
	for name, fs := range sets {
		flags := []string{}
		fs.VisitAll(func(f *flag.Flag) { flags = append(flags, dashed(f.Name)) })
		sort.Strings(flags)
		verbs[name] = flags
	}
	return verbs
}

// dashed spells a flag the way the help and every user does: one dash for a
// single letter, two for a word. Go's flag package accepts either spelling for
// either, so this is a rendering choice, and it is made once — here — for the
// frozen surface and the help assertion alike.
func dashed(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// loadFrozen reads a surface's fixture, and fails loudly on a missing or empty
// one: a gate that skips what it cannot read is a gate somebody disarms by
// deleting a file.
func loadFrozen(t *testing.T, name string) frozenFixture {
	t.Helper()
	path := filepath.Join(frozenDir, name+".json")
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed testdata path
	if err != nil {
		t.Fatalf("read the frozen fixture %s: %v\n(regenerate with `mise run frozen:update`)", path, err)
	}
	var fixture frozenFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(fixture.History) == 0 {
		t.Fatalf("%s holds no history entry: nothing is frozen", path)
	}
	return fixture
}

// updateFrozen appends the observed content to the surface's history when it
// changed, at the next version. It never rewrites an existing entry: the
// version test is what then demands the constant follow.
func updateFrozen(t *testing.T, s frozenSurface) {
	t.Helper()
	path := filepath.Join(frozenDir, s.name+".json")
	var fixture frozenFixture
	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // a fixed testdata path
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatalf("decode %s before updating it: %v", path, err)
		}
	}
	next := 1
	if n := len(fixture.History); n > 0 {
		last := fixture.History[n-1]
		if sameJSON(t, last.Content, s.content) {
			return
		}
		next = last.SchemaVersion + 1
	}
	fixture.History = append(fixture.History, frozenEntry{SchemaVersion: next, Content: s.content})
	raw, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.MkdirAll(frozenDir, 0o750); err != nil {
		t.Fatalf("create %s: %v", frozenDir, err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("%s: appended schema version %d; bump the matching constant and write the CHANGELOG line", path, next)
}

// sameJSON compares two documents structurally, so formatting and map order
// cannot fail the gate.
func sameJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("decode for comparison: %v", err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("decode for comparison: %v", err)
	}
	return reflect.DeepEqual(va, vb)
}

func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
