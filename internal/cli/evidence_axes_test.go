package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// What this file is for, and why it does not simply call the code it checks.
//
// #402 exists because the per-provider table was computed once by a throwaway
// script that was wrong twice before it was right. Its first version looked for
// a key named "operation" inside each entry — the operation name is the map
// *key* — so every one of the 370 rows landed in one bucket and it printed
// "scaleway: 370 served, 93 % driven". Right shape, right headers, plausible
// numbers, no relation to the record.
//
// A test that recomputed the table with the same helpers the command uses would
// have agreed with that script. So these tests take two deliberately different
// paths to the same numbers:
//
//   - the record is decoded into map[string]any, not into evidenceArtefact, with
//     an explicit type assertion per axis. A field that changes JSON type, an
//     axis that appears or disappears, `operations` that becomes a list: each is
//     a named failure here rather than a zero value counted as "not earned".
//   - the provider of each operation is read from coverage/<provider>-coverage.json,
//     written by the drift scan, while the command asks the mounted packs. Two
//     paths from the code to a per-provider set; they are compared, and a
//     disagreement fails.
//
// The falsification is tools/falsify/specs/evidence-axes.json: file every
// operation under one provider — the throwaway script's own first bug — and
// TestTheAxisTableIsTheCommittedRecordCountedPerProvider goes red.

const committedEvidence = "../../coverage/evidence.json"

// axisJSONTypes is what each axis is allowed to be in the record. Three of the
// seven are verdicts rather than booleans, which is the mistake a hand-written
// reader makes first: counting `probed` by truthiness counts "none" as earned.
var axisJSONTypes = map[string]string{
	"driven":    "bool",
	"probed":    "string",
	"contract":  "string",
	"dataplane": "bool",
	"shape":     "string",
	"behaviour": "bool",
	"negative":  "bool",
}

// The values that count as earned, restated here in literals rather than read
// from the emulator's constants. Sharing the constants would make this half of
// the check agree with the other half by construction, which is what it exists
// not to do.
var axisEarnedValue = map[string]any{
	"driven":    true,
	"probed":    "!none",
	"contract":  "clean",
	"dataplane": true,
	"shape":     "observed",
	"behaviour": true,
	"negative":  true,
}

// rawTally is provider -> axis -> earned, plus provider -> served, computed
// without any type of this package.
type rawTally struct {
	served map[string]int
	earned map[string]map[string]int
}

// readRecordRaw decodes the record as plain JSON and counts it per provider,
// failing by name on anything the format does not hold any more.
func readRecordRaw(t *testing.T, path string, owner map[string]string) rawTally {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("%s is not a JSON object: %v", path, err)
	}
	ops, ok := doc["operations"].(map[string]any)
	if !ok {
		t.Fatalf("%s: \"operations\" is %T, not a JSON object keyed by operation name. "+
			"The name is the key; a reader that looks for an \"operation\" field inside each "+
			"entry files every row under one provider and prints a plausible wrong table (#402)",
			path, doc["operations"])
	}
	if len(ops) == 0 {
		t.Fatalf("%s holds no operation, so this test would pass while measuring nothing", path)
	}

	out := rawTally{served: map[string]int{}, earned: map[string]map[string]int{}}
	for name, raw := range ops {
		entry, isObject := raw.(map[string]any)
		if !isObject {
			t.Fatalf("%s: entry %q is %T, not an object of axes", path, name, raw)
		}
		provider, known := owner[name]
		if !known {
			t.Fatalf("%s names %q, which no coverage artefact reports as implemented by any "+
				"provider, so no per-provider table can account for it", path, name)
		}
		out.served[provider]++
		if out.earned[provider] == nil {
			out.earned[provider] = map[string]int{}
		}
		if len(entry) != len(axisJSONTypes) {
			t.Fatalf("%s: entry %q carries %d axes, not the %d this test knows how to count (%v). "+
				"An axis was added or removed: teach this test before trusting any table",
				path, name, len(entry), len(axisJSONTypes), sortedAxisKeys(entry))
		}
		for axis, want := range axisJSONTypes {
			value, present := entry[axis]
			if !present {
				t.Fatalf("%s: entry %q has no %q axis", path, name, axis)
			}
			switch want {
			case "bool":
				got, isBool := value.(bool)
				if !isBool {
					t.Fatalf("%s: %q.%s is %T, not a bool; a reader counting it by truthiness "+
						"would be counting something else", path, name, axis, value)
				}
				if got == axisEarnedValue[axis] {
					out.earned[provider][axis]++
				}
			case "string":
				got, isString := value.(string)
				if !isString {
					t.Fatalf("%s: %q.%s is %T, not a verdict string", path, name, axis, value)
				}
				earnedIf := axisEarnedValue[axis].(string)
				if (earnedIf == "!none" && got != "none") || got == earnedIf {
					out.earned[provider][axis]++
				}
			}
		}
	}
	return out
}

func sortedAxisKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ownersFromCoverageArtefacts reads which provider implements which operation
// from coverage/<provider>-coverage.json — the drift scan's own output, which
// reaches the same fact by a different route from the mounted packs.
func ownersFromCoverageArtefacts(t *testing.T) map[string]string {
	t.Helper()
	matches, err := filepath.Glob("../../coverage/*-coverage.json")
	if err != nil {
		t.Fatalf("list the coverage artefacts: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no coverage artefact found, so the independent provider split has no source")
	}
	owner := map[string]string{}
	for _, path := range matches {
		body, err := os.ReadFile(path) //nolint:gosec // a committed artefact of this repository
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var file struct {
			Provider string `json:"provider"`
			Entries  []struct {
				Operation string `json:"operation"`
				Status    string `json:"status"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(body, &file); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if file.Provider == "" || len(file.Entries) == 0 {
			t.Fatalf("%s carries no provider or no entry, so it cannot arbitrate anything", path)
		}
		for _, e := range file.Entries {
			if e.Status == "implemented" {
				owner[e.Operation] = file.Provider
			}
		}
	}
	return owner
}

// tallyDifferences names every cell on which the two paths disagree. Shared by
// the check and by its witness, so the witness proves this comparison bites.
func tallyDifferences(table *evidenceTable, raw rawTally) []string {
	var out []string
	seen := map[string]bool{}
	for _, row := range table.Providers {
		seen[row.Provider] = true
		if row.Served != raw.served[row.Provider] {
			out = append(out, fmt.Sprintf("%s: the command served %d, the record holds %d",
				row.Provider, row.Served, raw.served[row.Provider]))
		}
		for _, a := range row.Axes {
			if a.Earned != raw.earned[row.Provider][a.Axis] {
				out = append(out, fmt.Sprintf("%s/%s: the command counted %d, the record holds %d",
					row.Provider, a.Axis, a.Earned, raw.earned[row.Provider][a.Axis]))
			}
		}
	}
	for provider := range raw.served {
		if !seen[provider] {
			out = append(out, fmt.Sprintf("%s: the record holds %d operations and the command "+
				"printed no row for it", provider, raw.served[provider]))
		}
	}
	sort.Strings(out)
	return out
}

// The table the command prints is the committed record, counted per provider.
//
// The one test #402 asks for by name: it holds the totals against
// coverage/evidence.json itself, so a change to the artefact's format breaks
// loudly instead of printing a plausible wrong table.
func TestTheAxisTableIsTheCommittedRecordCountedPerProvider(t *testing.T) {
	owner := ownersFromCoverageArtefacts(t)
	raw := readRecordRaw(t, committedEvidence, owner)

	art, err := readEvidence(committedEvidence)
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	owners, providers, err := operationOwners()
	if err != nil {
		t.Fatalf("ask the packs who owns what: %v", err)
	}
	table, err := tallyEvidence(committedEvidence, art, owners, providers, "", "")
	if err != nil {
		t.Fatalf("count the record: %v", err)
	}

	if diffs := tallyDifferences(table, raw); len(diffs) > 0 {
		t.Errorf("the per-provider table disagrees with coverage/evidence.json read directly, "+
			"%d cell(s):\n  %s", len(diffs), strings.Join(diffs, "\n  "))
	}

	// The whole population, checked once more end to end: a split that loses or
	// duplicates an operation can still leave every row plausible.
	total := 0
	for _, n := range raw.served {
		total += n
	}
	if table.Total.Served != total {
		t.Errorf("the command totals %d operations, the record holds %d", table.Total.Served, total)
	}
	if len(table.Providers) < 2 {
		t.Errorf("the table has %d provider row(s): a table that does not split by provider "+
			"answers \"which cloud is weakest\" with one cloud", len(table.Providers))
	}
}

// The witness: the comparison above must be able to find a miscount, or its
// green means nothing.
//
// A control that looks for the absence of something proves nothing until it has
// been shown finding one. This plants a disagreement of exactly one operation —
// the smallest the record has ever wobbled by (#398 measured one) — and demands
// that tallyDifferences name it.
func TestTheAxisComparisonFindsAMiscountOfOneOperation(t *testing.T) {
	owner := ownersFromCoverageArtefacts(t)
	raw := readRecordRaw(t, committedEvidence, owner)

	art, err := readEvidence(committedEvidence)
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	owners, providers, err := operationOwners()
	if err != nil {
		t.Fatalf("ask the packs who owns what: %v", err)
	}

	// Move one operation to another provider, the way a name-prefix guess would.
	var moved, from, to string
	for op, p := range owners {
		if from == "" {
			from, moved = p, op
			continue
		}
		if p != from {
			to = p
			break
		}
	}
	if moved == "" || to == "" {
		t.Fatal("fewer than two providers are mounted, so no misfiling can be planted")
	}
	owners[moved] = to

	table, err := tallyEvidence(committedEvidence, art, owners, providers, "", "")
	if err != nil {
		t.Fatalf("count the record: %v", err)
	}
	diffs := tallyDifferences(table, raw)
	if len(diffs) == 0 {
		t.Fatalf("moving %s from %s to %s changed no cell the comparison looks at, so "+
			"TestTheAxisTableIsTheCommittedRecordCountedPerProvider cannot see a misfiled "+
			"operation either", moved, from, to)
	}
}

// The seven axes this command knows are exactly the record's own fields.
//
// The cheap half of "breaks loudly": an eighth axis added to emulator.Evidence,
// or one renamed, is a silent omission from every table above until something
// reads the struct rather than a list written by hand. reflect is the only way
// to make the list and the type one fact.
func TestTheAxisListIsEveryFieldTheRecordPublishes(t *testing.T) {
	declared := map[string]bool{}
	typ := reflect.TypeOf(emulator.Evidence{})
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			t.Fatalf("emulator.Evidence field %s carries no JSON name, so no reader can "+
				"count it by name", typ.Field(i).Name)
		}
		declared[name] = true
	}

	counted := map[string]bool{}
	for _, a := range evidenceAxisList() {
		if !declared[a.Name] {
			t.Errorf("evidenceAxisList counts %q, which emulator.Evidence does not publish", a.Name)
		}
		if counted[a.Name] {
			t.Errorf("evidenceAxisList counts %q twice", a.Name)
		}
		counted[a.Name] = true
		if a.Meaning == "" {
			t.Errorf("axis %q has no one-line meaning, so docs/routes.md would print a score "+
				"nobody can read", a.Name)
		}
	}
	for name := range declared {
		if !counted[name] {
			t.Errorf("the record publishes the %q axis and no table counts it: add it to "+
				"evidenceAxisList, with the line the documentation prints for it", name)
		}
	}
	// The same list the raw reader above type-checks against: two lists of seven
	// names in one package is one of them going stale.
	if len(axisJSONTypes) != len(declared) {
		t.Errorf("axisJSONTypes knows %d axes and the record publishes %d", len(axisJSONTypes), len(declared))
	}
}

// An operation the packs do not serve is refused, never filed under a default.
//
// This is the failure mode of #402's throwaway script made impossible: it filed
// everything under one provider and printed a table. Counting an unclaimed
// operation under the first pack, or dropping it, both produce a table that
// looks right.
func TestAnOperationNoPackServesIsRefusedRatherThanFiled(t *testing.T) {
	art := &evidenceArtefact{
		Machines: []string{"none"},
		Operations: map[string]emulator.Evidence{
			"instance/v1/API.ListServers": {Driven: true},
			"nowhere/v9/API.Invented":     {Driven: true},
		},
	}
	owners := map[string]string{"instance/v1/API.ListServers": "scaleway"}
	_, err := tallyEvidence("record.json", art, owners, []string{"scaleway"}, "", "")
	if err == nil {
		t.Fatal("an operation no pack serves was counted, so a stale record renders a table " +
			"that is missing rows and says so nowhere")
	}
	if !strings.Contains(err.Error(), "nowhere/v9/API.Invented") {
		t.Errorf("the refusal does not name the operation, so nobody can act on it: %v", err)
	}
}

// Naming an axis lists the operations at zero on it — a queue, not a score.
func TestNamingAnAxisListsTheOperationsAtZeroOnIt(t *testing.T) {
	art := &evidenceArtefact{
		Machines: []string{"none"},
		Operations: map[string]emulator.Evidence{
			"a/v1/API.One":   {Driven: true, Contract: emulator.ContractClean},
			"a/v1/API.Two":   {Driven: false, Contract: emulator.ContractUnchecked},
			"a/v1/API.Three": {Driven: false, Contract: emulator.ContractViolating},
		},
	}
	owners := map[string]string{
		"a/v1/API.One": "scaleway", "a/v1/API.Two": "scaleway", "a/v1/API.Three": "scaleway",
	}

	table, err := tallyEvidence("record.json", art, owners, []string{"scaleway"}, "", "driven")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	got := []string{}
	for _, q := range table.Providers[0].Queue {
		got = append(got, q.Operation)
	}
	want := []string{"a/v1/API.Three", "a/v1/API.Two"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("queue on driven = %v, want %v", got, want)
	}

	// A non-boolean axis carries its verdict, because "unchecked" and
	// "violating" are the same "not earned" and very different work.
	table, err = tallyEvidence("record.json", art, owners, []string{"scaleway"}, "", "contract")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	verdicts := map[string]string{}
	for _, q := range table.Providers[0].Queue {
		verdicts[q.Operation] = q.Verdict
	}
	if verdicts["a/v1/API.Two"] != emulator.ContractUnchecked ||
		verdicts["a/v1/API.Three"] != emulator.ContractViolating {
		t.Errorf("the queue does not separate an unchecked answer from a violating one: %v", verdicts)
	}

	// The total row carries no queue: a work queue belongs to the pack that
	// would clear it.
	if len(table.Total.Queue) != 0 {
		t.Errorf("the \"all\" row carries a queue of %d", len(table.Total.Queue))
	}
}

// percentOf rounds to nearest rather than truncating.
//
// The cells #402 measured are the cases: 166 of 173 is 95.95 %, and truncation
// prints 95 where the reference table says 96. Three of its twelve cells turn on
// this alone, and a reader comparing them would have gone looking for a defect
// in the count.
func TestAPercentageIsRoundedToNearestNotTruncated(t *testing.T) {
	for _, c := range []struct{ n, total, want int }{
		{166, 173, 96}, {157, 173, 91}, {141, 173, 82},
		{66, 93, 71}, {77, 93, 83}, {93, 93, 100},
		{85, 104, 82}, {10, 104, 10}, {78, 104, 75},
		{0, 104, 0}, {0, 0, 0},
	} {
		if got := percentOf(c.n, c.total); got != c.want {
			t.Errorf("percentOf(%d, %d) = %d, want %d", c.n, c.total, got, c.want)
		}
	}
}

// The whole command, end to end, offline, in both formats.
func TestCoverageEvidenceRendersTheCommittedRecordInBothFormats(t *testing.T) {
	var out, errs bytes.Buffer
	if code := coverage([]string{"--evidence", committedEvidence}, &out, &errs); code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, errs.String())
	}
	text := out.String()
	for _, want := range []string{"scaleway", "outscale", "exoscale", "all", "behaviour", "negative"} {
		if !strings.Contains(text, want) {
			t.Errorf("the text table never mentions %q:\n%s", want, text)
		}
	}

	out.Reset()
	errs.Reset()
	if code := coverage([]string{"--evidence", committedEvidence, "--format", "json"}, &out, &errs); code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, errs.String())
	}
	var table evidenceTable
	if err := json.Unmarshal(out.Bytes(), &table); err != nil {
		t.Fatalf("--format json is not decodable: %v", err)
	}
	if len(table.Providers) < 2 || table.Total.Served == 0 || len(table.Total.Axes) != len(axisJSONTypes) {
		t.Errorf("--format json published %d providers, %d served, %d axes",
			len(table.Providers), table.Total.Served, len(table.Total.Axes))
	}

	// --provider narrows the rows and never the "all" row: the question is
	// comparative, and a row with nothing to be weaker than is not an answer.
	out.Reset()
	errs.Reset()
	if code := coverage([]string{"--evidence", committedEvidence, "--provider", "exoscale",
		"--format", "json"}, &out, &errs); code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, errs.String())
	}
	var narrowed evidenceTable
	if err := json.Unmarshal(out.Bytes(), &narrowed); err != nil {
		t.Fatalf("--format json is not decodable: %v", err)
	}
	if len(narrowed.Providers) != 1 || narrowed.Providers[0].Provider != "exoscale" {
		t.Errorf("--provider exoscale printed %d rows", len(narrowed.Providers))
	}
	if narrowed.Total.Served != table.Total.Served {
		t.Errorf("--provider narrowed the \"all\" row to %d of %d, so there is nothing left "+
			"to compare the named provider against", narrowed.Total.Served, table.Total.Served)
	}
}

// An unknown axis and an unknown provider are refused by name.
func TestTheEvidenceViewRefusesAnAxisAndAProviderItDoesNotKnow(t *testing.T) {
	for _, c := range []struct{ flag, value, want string }{
		{"--axis", "coverage", "driven"},
		{"--provider", "aws", "exoscale"},
	} {
		var out, errs bytes.Buffer
		code := coverage([]string{"--evidence", committedEvidence, c.flag, c.value}, &out, &errs)
		if code != exitError {
			t.Errorf("%s %s exited %d, want %d", c.flag, c.value, code, exitError)
		}
		if !strings.Contains(errs.String(), c.want) {
			t.Errorf("%s %s: the refusal does not name what is available (%q): %s",
				c.flag, c.value, c.want, errs.String())
		}
	}
}

// The table in docs/routes.md is the committed record, rendered.
//
// The coordinator's requirement for #402 and the reason the block is generated:
// a conformance table nobody regenerates reads as a measurement. `feint docs
// --check` is the gate, and this is the assertion that gate stands on — it
// compares the page's bytes with what the record renders today, so a table
// edited by hand, or a record that moved, fails here as well as at the gate.
func TestTheAxisTableInTheRouteReferenceIsTheCommittedRecord(t *testing.T) {
	page, err := os.ReadFile("../../docs/routes.md")
	if err != nil {
		t.Fatalf("read the route reference: %v", err)
	}
	art, err := readEvidence(committedEvidence)
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	rendered, err := renderAxes(art)
	if err != nil {
		t.Fatalf("render the table: %v", err)
	}
	if !strings.Contains(string(page), rendered) {
		t.Errorf("docs/routes.md does not carry the table coverage/evidence.json renders today; " +
			"run `mise run docs:coverage`")
	}

	// What the block must carry beyond the numbers, because a percentage
	// nobody can read is not a measurement either.
	for _, want := range []string{
		docsGenerated,
		"feint coverage --evidence coverage/evidence.json",
		"--axis negative",
		"--format json",
		"An injected fault earns none of them",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the generated block never says %q", want)
		}
	}
	// One line per axis, so a reader knows what a column means without opening
	// the code. The command and the page take it from one list.
	for _, a := range evidenceAxisList() {
		if !strings.Contains(rendered, "| `"+a.Name+"` | "+a.Meaning+" |") {
			t.Errorf("the axis legend does not carry the one-line meaning of %q", a.Name)
		}
	}
}

// The block quotes no figure a hand typed.
//
// The defect docs.go's own header describes twice: a generated table whose
// surrounding prose repeats one of its numbers goes stale in the paragraph
// rather than in the table, where nobody looks. Every percentage in the block
// must be one the record produces.
func TestTheAxisBlockQuotesNoFigureItDidNotCompute(t *testing.T) {
	art, err := readEvidence(committedEvidence)
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	owners, providers, err := operationOwners()
	if err != nil {
		t.Fatalf("ask the packs who owns what: %v", err)
	}
	table, err := tallyEvidence(committedEvidence, art, owners, providers, "", "")
	if err != nil {
		t.Fatalf("count the record: %v", err)
	}
	computed := map[string]bool{}
	for _, row := range append(append([]providerTally(nil), table.Providers...), table.Total) {
		computed[fmt.Sprint(row.Served)] = true
		for _, a := range row.Axes {
			computed[fmt.Sprint(a.Percent)] = true
			computed[fmt.Sprint(a.Earned)] = true
		}
	}
	rendered, err := renderAxes(art)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// "10 %" is two whitespace-separated fields, so a scan over Fields sees no
	// percentage at all and passes while measuring nothing — which is the shape
	// of defect this whole issue is about. A regex, and a count that must not be
	// zero: the control proves it can find what it looks for before it reports
	// finding none that are wrong.
	percentages := regexp.MustCompile(`(\d+)\s*%`)
	seen := 0
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "|") {
			continue // the table itself, which is where a figure belongs
		}
		for _, m := range percentages.FindAllStringSubmatch(line, -1) {
			seen++
			if !computed[m[1]] {
				t.Errorf("the prose quotes %s %%, which is not a figure this record produces: %q", m[1], line)
			}
		}
	}
	if seen == 0 {
		t.Fatal("the scan found no percentage in the block's prose, so it would pass over a " +
			"hand-typed one; the paragraph about injected faults quotes two")
	}
}
