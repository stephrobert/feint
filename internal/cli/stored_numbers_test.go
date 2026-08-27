package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A stored number is never read by a type assertion (#542).
//
// The measurement, on 2026-08-27: Resource.Attrs is a map[string]any and
// store.Restore decodes the snapshot with encoding/json, so a number written
// as an int, an int64 or a uint32 all come back a float64 — while a string and
// a bool come back unchanged. A `.(int)` on a restored number therefore yields
// ok=false and 0, and the door it travels through is a real one:
// `PUT /_feint/state` restores a snapshot verbatim and snapshot.go documents
// the format as meant to outlive its instance.
//
// What makes this a discipline test rather than three fixes is that the three
// fixes were already written, six times, and did not travel. On the day #542
// was filed the tree held seven copies of the same tolerance under six names —
// exoscale's intOf and int64Of, exoscale's and outscale's numOf, scaleway's
// positionOf and portValue, and the fake fourth pack's portOf — and
// outscale/volumes.go still read a volume's size with a plain `.(int)` three
// files away from one of them. A restored 40 GiB volume shrank to 1 with a
// 200, and a snapshot taken of it recorded VolumeSize 0. Exoscale had found
// that exact pair of readers — a shrink refusal and the size a snapshot
// inherits — written the reason down in a doc comment and fixed it for its
// own block volumes on 2026-08-17 (5680efb), ten days before #542 was filed.
// Nothing carried it across, and nothing would have carried it to a fourth
// pack: testdata/provider-four wrote `res.Attrs["port"].(int)` on its first
// draft.
//
// So the reader lives in internal/core/resource and this refuses the gesture it
// replaces. It reads the packs' own sources, which is the property #220 asked
// for: a pack that forgets does not lack a test it never had to remember to
// write — it fails one.
//
// Numbers only, and that boundary is measured rather than assumed: strings and
// bools cross the snapshot unchanged, so `.(string)` and `.(bool)` on Attrs are
// correct and forbidding them would be a guard that refuses working code. The
// round trip that establishes it is
// store.TestEveryGoTypeAPackWritesCrossesTheSnapshotAsMeasured.

// numericTypes are the basic types a stored number can be asserted to. Bool and
// string are deliberately absent — measured to cross a snapshot unchanged — and
// so is any named type, which cannot be what a JSON decoder produced.
var numericTypes = map[string]bool{
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "float32": true, "float64": true,
	"complex64": true, "complex128": true, "byte": true, "rune": true,
}

// storedNumberExemptions lists the numeric assertions a pack legitimately
// keeps, each with the reason — the discipline Declined() applies to a refusal,
// applied to this one, and the shape TestEveryBarrageExemptionSaysWhy already
// holds for the maps beside it.
//
// Keyed "<pack>/<file>:<type>" so an exemption cannot quietly widen to a whole
// pack. One entry today, and it is the honest one: a value decoded straight out
// of a request body has never been anything but a float64, because that is what
// encoding/json produces for a JSON number, and reading it through the stored-
// value reader would say something untrue about where it came from.
var storedNumberExemptions = map[string]string{
	"outscale/loadbalancers.go:float64": "the value is read out of the request body this handler just decoded, " +
		"not out of a stored resource: encoding/json makes every JSON number a float64 on the way in, so the " +
		"assertion is the decoder's own type rather than a guess about what a snapshot left behind",
}

// numericAssertion is one refused gesture: where it is, and what it asserted.
type numericAssertion struct {
	pack  string
	file  string
	typ   string
	where string
	how   string
}

// numericAssertionScan is what a pass over one pack saw, so a silence can be
// told from a walk that read nothing.
type numericAssertionScan struct {
	files      int
	assertions int
	found      []numericAssertion
}

// storedNumberReads reports every numeric type assertion and numeric type-switch
// case in a pack's non-test sources.
//
// Both shapes, because they fail the same way and are written differently. The
// single assertion `v.(int)` is the defect #542 measured. The type switch is
// how six packs wrote the correct version by hand, and admitting it would be
// admitting the seventh copy — the one whose author forgets the float64 case,
// which is exactly the draft testdata/provider-four shipped.
func storedNumberReads(t *testing.T, dir string) numericAssertionScan {
	t.Helper()
	pack := filepath.Base(dir)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("list %s: %v", pack, err)
	}
	scan := numericAssertionScan{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			// A test may assert a concrete type: it is checking what a
			// handler produced, not reading a value back out of the store.
			continue
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		scan.files++
		base := filepath.Base(file)
		record := func(node ast.Node, typ, how string) {
			scan.found = append(scan.found, numericAssertion{
				pack: pack, file: base, typ: typ,
				where: fmt.Sprintf("%s/%s:%d", pack, base, fset.Position(node.Pos()).Line),
				how:   how,
			})
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.TypeAssertExpr:
				if n.Type == nil {
					// `x.(type)`, the head of a type switch: its cases are
					// visited below, and counting it here would report the
					// switch twice.
					return true
				}
				scan.assertions++
				if name, ok := n.Type.(*ast.Ident); ok && numericTypes[name.Name] {
					record(n, name.Name, "a type assertion to "+name.Name)
				}
			case *ast.TypeSwitchStmt:
				for _, clause := range n.Body.List {
					caseClause, ok := clause.(*ast.CaseClause)
					if !ok {
						continue
					}
					for _, typ := range caseClause.List {
						name, ok := typ.(*ast.Ident)
						if !ok || !numericTypes[name.Name] {
							continue
						}
						record(typ, name.Name, "a type switch case on "+name.Name)
					}
				}
			}
			return true
		})
	}
	return scan
}

// No pack reads a stored number by asserting its Go type (#542).
func TestNoPackReadsAStoredNumberByAssertion(t *testing.T) {
	var offences []string
	files, assertions := 0, 0
	// The fourth pack is in the population, and it is the member that matters:
	// the three real packs were migrated onto the shared reader, so each of
	// them knows what it used to do by hand. testdata/provider-four is the one
	// that wrote the defect on its first draft with nobody watching.
	for _, dir := range disciplinedPackDirs(t) {
		scan := storedNumberReads(t, dir)
		files += scan.files
		assertions += scan.assertions
		for _, found := range scan.found {
			if reason := storedNumberExemptions[found.pack+"/"+found.file+":"+found.typ]; reason != "" {
				continue
			}
			offences = append(offences, fmt.Sprintf("%s: %s", found.where, found.how))
		}
	}

	// A scan that found nothing looks exactly like a repository whose packs
	// never assert a type, and that repository does not exist: the packs assert
	// strings, bools and slices out of Attrs on nearly every read. The floors
	// are well under today's count — 89 files and 320 assertions on 2026-08-27
	// — and well over zero.
	if files < 40 || assertions < 200 {
		t.Fatalf("the scan read %d file(s) and %d type assertion(s) across the packs: it is broken, "+
			"and a broken scan reports a disciplined repository", files, assertions)
	}

	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("%d numeric type assertion(s) on values a pack may have read back out of a store. "+
			"Attrs crosses encoding/json on every snapshot, so an int, an int64 and a uint32 all come "+
			"back float64 and the assertion yields zero — measured as a restored volume shrinking with "+
			"a 200 (#542). Read it with resource.Int, resource.Int64 or resource.Number; if this one "+
			"genuinely never came from a store, add \"<pack>/<file>:<type>\" to storedNumberExemptions "+
			"with the reason:\n  %s",
			len(offences), strings.Join(offences, "\n  "))
	}
}

// And the detector finds what it looks for, in both directions.
//
// The refusing half plants the two shapes that fail: the plain assertion #542
// measured, and the hand-written type switch whose seventh copy forgets a case.
// The accepting half is the larger one and is asserted as an exact set, because
// a detector that also reported a string assertion, a bool assertion, a slice
// assertion, an int conversion, an int-typed struct field or an []int literal
// would pass every assertion above and refuse the vocabulary all four packs
// legitimately write.
func TestTheStoredNumberDetectorFindsWhatItLooksFor(t *testing.T) {
	dir := t.TempDir()
	fixture := `package p

import "fmt"

type box struct{ Size int }

func read(m map[string]any) {
	planted, _ := m["size"].(int)
	alsoPlanted, _ := m["big"].(int64)
	switch n := m["port"].(type) {
	case float64:
		fmt.Println(n)
	case string:
		fmt.Println(n)
	}
	name, _ := m["name"].(string)
	flag, _ := m["on"].(bool)
	list, _ := m["rules"].([]any)
	converted := int(planted)
	sizes := []int{1, 2}
	b := box{Size: converted}
	fmt.Println(alsoPlanted, name, flag, list, sizes, b)
}
`
	if err := writeFixture(t, dir, "pack.go", fixture); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}
	// A _test.go neighbour, because the scan skips those on purpose and a scan
	// that skipped everything would report a clean pack.
	if err := writeFixture(t, dir, "pack_test.go", "package p\n\nfunc t(v any) int { n, _ := v.(int); return n }\n"); err != nil {
		t.Fatalf("write the test fixture: %v", err)
	}

	scan := storedNumberReads(t, dir)
	if scan.files != 1 {
		t.Fatalf("the scan read %d file(s) of a fixture that has one non-test source: its silence "+
			"about the rest would prove nothing", scan.files)
	}
	if scan.assertions < 5 {
		t.Fatalf("the scan saw %d type assertion(s) in a fixture that writes five: it is not reading "+
			"the file it was pointed at", scan.assertions)
	}
	got := map[string]bool{}
	for _, found := range scan.found {
		got[found.how] = true
	}
	planted := []string{
		"a type assertion to int",
		"a type assertion to int64",
		"a type switch case on float64",
	}
	for _, want := range planted {
		if !got[want] {
			t.Errorf("the detector did not report the planted %q: a discipline test that finds "+
				"nothing reads exactly like a disciplined repository", want)
		}
	}
	expected := map[string]bool{}
	for _, want := range planted {
		expected[want] = true
	}
	for how := range got {
		if !expected[how] {
			t.Errorf("the detector reports %q in a fixture that plants three: a detector that "+
				"refuses more than the numeric read breaks every pack instead of holding it", how)
		}
	}
}

// writeFixture drops one source into a temporary pack directory.
func writeFixture(t *testing.T, dir, name, source string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600)
}
