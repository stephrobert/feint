package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/corpus"
)

// No fixture of any pack lives in the namespace the corpus sanitiser mints
// (#395).
//
// The sanitiser replaces the identifiers of a recording with a counter: UUIDs
// under 00000000-0000-4000-8000-, addresses inside 198.18.0.0/15, and prefixed
// identifiers as eight hexadecimal digits starting 00 (ami-00000001, …). A
// pack fixture written in the same space is a value a recording can reach, and
// then a replay names an object of THIS emulator where the recording named one
// of the account: #395 measured DeleteImage on an image that did not exist,
// refused by the cloud with 400, replayed as ami-00000002 — a catalogue image —
// and refused here with 409 "belongs to the emulated catalogue", a plausible
// verdict on a request nobody made. The Exoscale default security group was
// the same class one UUID over: 00000000-0000-4000-8000-000000000001, the first
// UUID every sanitised corpus hands out.
//
// So the rule is held across packs rather than by inspection of one catalogue:
// every string literal of every non-test file under internal/providers is read
// with the sanitiser's own recogniser, corpus.Minted, so the two cannot drift
// apart. Test files are not scanned: a test legitimately names a minted-looking
// identifier as "one that exists nowhere".
//
// The scan proves it can find before it reports nothing: a planted file with
// one value of each family must be reported, or a scan that reads no literal
// would pass by silence.

// identifierLike picks, out of a string literal, the tokens the sanitiser could
// have minted: a UUID, a prefixed identifier, a dotted quad.
var identifierLike = regexp.MustCompile(
	`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}` +
		`|\b[a-z][a-z0-9]*(?:-[a-z][a-z0-9]*)*-[0-9a-f]{8,}\b` +
		`|\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// mintedFixturesIn parses one Go file and returns every token of a string
// literal the sanitiser would recognise as its own, with where it sits.
func mintedFixturesIn(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for _, candidate := range identifierLike.FindAllString(value, -1) {
			// A netmask is one of thirty-two values and the sanitiser maps
			// that set onto itself, so corpus.Minted answers true for every
			// one of them; it is not an identifier and is not what this
			// test is about.
			if ip := net.ParseIP(candidate); ip != nil && ip.To4() != nil {
				if _, bits := net.IPMask(ip.To4()).Size(); bits != 0 {
					continue
				}
			}
			if corpus.Minted(candidate) {
				found = append(found, fmt.Sprintf("%s:%d %q", filepath.Base(path), fset.Position(lit.Pos()).Line, candidate))
			}
		}
		return true
	})
	return found, nil
}

func TestNoPackFixtureLivesInTheSanitisersMintingSpace(t *testing.T) {
	// The witness: one value of each minted family, and one prefixed
	// identifier outside the space, which must not be reported.
	planted := filepath.Join(t.TempDir(), "planted.go")
	source := "package planted\n\nvar fixtures = []string{\n" +
		"\t\"ami-00000001\",\n" +
		"\t\"00000000-0000-4000-8000-000000000001\",\n" +
		"\t\"198.18.0.1\",\n" +
		"\t\"ami-fe1a7001\",\n" +
		"\t\"255.255.255.0\",\n" +
		"}\n"
	if err := os.WriteFile(planted, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	witnessed, err := mintedFixturesIn(planted)
	if err != nil {
		t.Fatal(err)
	}
	if len(witnessed) != 3 {
		t.Fatalf("the planted file carries three minted values and the scan reports %d: %v", len(witnessed), witnessed)
	}

	root := repoRoot(t)
	var problems []string
	files := 0
	err = filepath.WalkDir(filepath.Join(root, "internal", "providers"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		found, err := mintedFixturesIn(path)
		if err != nil {
			return err
		}
		for _, f := range found {
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			problems = append(problems, filepath.Join(rel, f))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no pack source was scanned: this test measured nothing")
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Errorf("%d pack fixture(s) live in the space the corpus sanitiser mints, so a sanitised "+
			"recording can name them and a replay then measures this emulator's fixture where the "+
			"recording named an object of the account:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
}
