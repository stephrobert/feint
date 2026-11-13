package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store/storetest"
)

// A change the provider did not make (#193).
//
// `terraform plan` after `terraform apply` is empty, and the suites assert it.
// That proves one half: the emulator reads back what the provider sent. The
// other half is the one a user actually fears — somebody clicks in the console,
// a tag disappears, a group opens — and no test here had ever seen it, because
// there was no way to change a resource except through the API the provider
// itself calls.
//
// Three decisions shape this file, and all three come from the issue:
//
//  1. **No new route.** "This must never be an API route": an emulator serving
//     a mutation door to a network client would be a hole. The channel is the
//     one that already exists for exactly this traffic, GET and PUT on
//     /_feint/state, and snapshot.go is the model this file follows down to the
//     timeout and the body bound.
//  2. **No provider vocabulary.** The verb takes a kind and a path into Attrs,
//     which is where the packs put their own wire shape. `--open tcp/22` and
//     the other rich verbs the issue sketches would each have to know one
//     provider's words; a path does not, and it works for the three packs and
//     the fourth that does not exist yet.
//  3. **The result must be a state the emulator could have been in.**
//     storetest.Sweep is the emulator's own word on what is impossible — one
//     address held by two live resources, one runtime object claimed twice —
//     and a mutation that breaks it tests nothing. See mutateSweep below for
//     why the comparison is a delta rather than an absolute.
//
// And the boundary that costs the most to get wrong: a mutated state is not a
// measurement. internal/core/store/mutation.go holds that half.

// mutateNow is the clock stamped into a ledger line. A variable so a test can
// freeze it; nothing in a running binary ever replaces it.
var mutateNow = time.Now

// mutateCommand parses one mutation and sends it.
//
// The positional pair is taken before the flags for the reason `env` and
// `snapshot` document: the standard flag package stops at the first non-flag
// argument, so `mutate instance/server web-01 --addr …` would leave the address
// unparsed. One flag set for both forms, named after the verb, because `-h`
// must render the whole surface and a second set would freeze half of it.
func mutateCommand(args []string, stdout, stderr io.Writer) int {
	var kind, target string
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		kind, rest = rest[0], rest[1:]
	}
	if kind != "" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		target, rest = rest[0], rest[1:]
	}

	fs := newFlagSet("mutate")
	addr := fs.String("addr", DefaultAddr, "address of the running emulator")
	list := fs.Bool("list", false, "print what is mutable: kind, identifier, name")
	format := fs.String("format", "text", "output format of --list: text or json")
	var sets, unsets repeatedFlag
	fs.Var(&sets, "set", "<path>=<json>: set an attribute, repeatable")
	fs.Var(&unsets, "unset", "<path>: remove an attribute, repeatable")
	if err := fs.Parse(rest); err != nil {
		return exitError
	}
	if left := fs.Args(); len(left) != 0 {
		fmt.Fprintf(stderr, "feint: unexpected argument %q\n", left[0])
		return exitError
	}

	if *list {
		if kind != "" {
			fmt.Fprintf(stderr, "feint: --list takes no resource; drop %q or drop --list\n", kind)
			return exitError
		}
		return mutateList(*addr, *format, stdout, stderr)
	}
	if kind == "" {
		fmt.Fprintln(stderr, "feint: mutate needs a kind and a resource: "+
			"feint mutate <kind> <name-or-id> --set <path>=<json>\n"+
			"       feint mutate --list shows what is mutable")
		return exitError
	}
	if target == "" {
		fmt.Fprintf(stderr, "feint: mutate %s needs a resource name or identifier\n", kind)
		return exitError
	}
	if len(sets) == 0 && len(unsets) == 0 {
		fmt.Fprintln(stderr, "feint: mutate needs at least one --set or --unset; "+
			"a mutation that changes nothing is a state nobody drifted")
		return exitError
	}

	body, err := fetchState(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	doc, err := decodeState(body)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %s\n", err)
		return exitError
	}

	res, err := findResource(doc.Resources, kind, target)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %s\n", err)
		return exitError
	}

	// Taken before the change, so the comparison below is about this mutation
	// and not about whatever the store was already carrying.
	before := storetest.Sweep(doc.Resources, nil, nil)

	records, err := applyMutations(res, sets, unsets, mutateNow().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "feint: %s\n", err)
		return exitError
	}

	if broke := mutateSweep(before, doc.Resources); len(broke) != 0 {
		fmt.Fprintln(stderr, "feint: this mutation produces a state the emulator calls impossible, "+
			"so nothing was written:")
		for _, line := range broke {
			fmt.Fprintf(stderr, "       %s\n", line)
		}
		return exitError
	}

	out, err := encodeMutatedState(doc, records)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %s\n", err)
		return exitError
	}

	// Read again, immediately before writing, and refuse if the store moved.
	//
	// This verb reads the whole store and writes the whole store back, and
	// `Restore` rebuilds the map from what it receives. A resource some client
	// created between the two therefore disappears, with no error and no trace
	// — and that is the verb's own scenario, since the point is to drift a
	// resource while a suite is running.
	//
	// **This narrows the window, it does not close it.** The real fix is a
	// revision the state door checks on write, which is a change to the
	// emulator and not to this verb: #915. What this does is turn the common
	// case — a client wrote seconds ago — from a silent loss into a refusal.
	// docs/limits.md says so rather than leaving the gap implicit.
	// TestAMutationRefusesWhenTheStoreMovedUnderIt fails without it.
	if again, err := fetchState(*addr); err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	} else if !bytes.Equal(again, body) {
		fmt.Fprintln(stderr, "feint: the store changed while this mutation was being prepared, "+
			"so nothing was written. Something else is writing to this emulator; "+
			"run it again when it is quiet.")
		return exitError
	}

	count, err := putState(*addr, out)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	for _, r := range records {
		fmt.Fprintf(stdout, "%s\n", r)
	}
	fmt.Fprintf(stdout, "the emulator holds %d resources and is now marked as mutated: "+
		"its conformance numbers describe a state no provider API produced\n", count)
	return exitOK
}

// mutateList prints what there is to mutate: kind, identifier, name.
//
// Nothing else, and nothing provider-shaped: the three columns are fields of
// resource.Resource plus the one attribute every pack happens to call `name`,
// printed when it is there and left blank when it is not. A listing that had to
// know each pack's display attribute would be a listing the fourth pack escapes.
func mutateList(addr, format string, stdout, stderr io.Writer) int {
	body, err := fetchState(addr)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %v\n", err)
		return exitError
	}
	doc, err := decodeState(body)
	if err != nil {
		fmt.Fprintf(stderr, "feint: %s\n", err)
		return exitError
	}

	type row struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := make([]row, 0, len(doc.Resources))
	for _, res := range doc.Resources {
		if res == nil {
			continue
		}
		out = append(out, row{Kind: res.Kind, ID: res.ID, Name: attrName(res)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})

	switch format {
	case "json":
		if err := writeJSON(stdout, out); err != nil {
			fmt.Fprintf(stderr, "feint: %v\n", err)
			return exitError
		}
		return exitOK
	case "text":
	default:
		fmt.Fprintf(stderr, "feint: unknown format %q; expected text or json\n", format)
		return exitError
	}
	// An empty emulator says so instead of printing a header over nothing: a
	// bare header reads as "nothing matched my filter", and there is no filter.
	if len(out) == 0 {
		fmt.Fprintf(stdout, "the emulator on %s holds no resource to mutate\n", addr)
		return exitOK
	}
	fmt.Fprintf(stdout, "%-28s %-40s %s\n", "KIND", "ID", "NAME")
	for _, r := range out {
		fmt.Fprintf(stdout, "%-28s %-40s %s\n", r.Kind, r.ID, r.Name)
	}
	return exitOK
}

// mutateState is the state document, read and written whole.
//
// It mirrors internal/core/store's envelope rather than importing it, for the
// reason snapshot.go states about its own bound: this is a client speaking HTTP
// to an emulator that may be a different build.
//
// Unknown keys at the document's top level are kept as raw JSON under Extra, so
// a round trip never drops one. **Inside a resource, nothing is kept**: a key
// this build does not declare makes the decode fail rather than disappear. That
// asymmetry is deliberate — a dropped document key loses metadata, a dropped
// resource key rewrites every resource — and it is the half an earlier version
// of this comment claimed to have and did not.
type mutateState struct {
	Format    string
	Version   int
	Resources []*resource.Resource
	Mutations []string
	Extra     map[string]json.RawMessage
}

func decodeState(body []byte) (*mutateState, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("that address answered something that is not a state document: %w", err)
	}
	doc := &mutateState{Extra: map[string]json.RawMessage{}}
	for k, v := range raw {
		var err error
		switch k {
		case "format":
			err = json.Unmarshal(v, &doc.Format)
		case "version":
			err = json.Unmarshal(v, &doc.Version)
		case "resources":
			// Strict, and the refusal is the point. `json.Unmarshal` into
			// []*resource.Resource drops any key the struct does not declare,
			// silently — so a verb from build N talking to an emulator from
			// build N+1 would rewrite every resource without the field N+1
			// added. The Extra map above only protects the document's top
			// level; it never reached inside a resource.
			//
			// Refusing is fail-closed and matches what the emulator's own
			// Restore does with an unknown key. The alternative, keeping each
			// resource as raw JSON and re-marshalling only the target, is a
			// larger change for a case that only arises across builds.
			// TestAResourceFieldThisBuildCannotReadIsRefused fails without it.
			dec := json.NewDecoder(bytes.NewReader(v))
			dec.DisallowUnknownFields()
			err = dec.Decode(&doc.Resources)
		case "mutations":
			err = json.Unmarshal(v, &doc.Mutations)
		default:
			doc.Extra[k] = v
		}
		if err != nil {
			return nil, fmt.Errorf("the state document has a %q this version cannot read: %w", k, err)
		}
	}
	if doc.Format == "" {
		return nil, fmt.Errorf("the state document carries no format header; is that address an emulator?")
	}
	return doc, nil
}

// encodeMutatedState writes the document back, ledger included.
//
// It refuses an empty ledger, and that refusal is the whole marking mechanism
// rather than a sanity check. This is the only function in this file that
// produces a body for `PUT /_feint/state`, and it cannot produce one that does
// not say a mutation happened. Adding a second mutation path later that forgets
// to mark the run therefore means writing a second encoder, which is a thing a
// reviewer sees, where a forgotten `tainted = true` is not.
//
// TestAMutationMarksTheEmulatorAndItDidNotSaySoBefore and
// TestAnUnrecordedMutationIsRefused fail without this.
func encodeMutatedState(doc *mutateState, records []string) ([]byte, error) {
	if len(records) == 0 {
		return nil, fmt.Errorf("refusing to write a mutated state with no record of the mutation: " +
			"a state nobody can tell apart from one a client produced is the one thing this verb must not create")
	}
	out := map[string]json.RawMessage{}
	for k, v := range doc.Extra {
		out[k] = v
	}
	var err error
	put := func(key string, value any) {
		if err != nil {
			return
		}
		var b []byte
		b, err = json.Marshal(value)
		out[key] = b
	}
	put("format", doc.Format)
	put("version", doc.Version)
	put("resources", doc.Resources)
	put("mutations", append(append([]string{}, doc.Mutations...), records...))
	if err != nil {
		return nil, fmt.Errorf("encode the mutated state: %w", err)
	}
	return json.Marshal(out)
}

// mutateSweep returns the invariants this mutation broke, and only those.
//
// A delta rather than an absolute, and the difference matters. Sweep's strict
// reading (nil, nil) flags pairs that are legitimate for a pack that declares
// them — a Scaleway server and the instance/ip record that *is* its address
// both carry that address, and storetest.Sweep says so in as many words. Run
// absolutely here, every mutation against a healthy Scaleway store would be
// refused for a finding that was already there and has nothing to do with it.
//
// The predicates that would exempt those pairs are the pack's knowledge, and
// this verb refuses to learn three providers' vocabularies (CLAUDE.md rule 5).
// So the question asked is the one that is answerable without them: did this
// mutation add a finding? A finding present before and after is the store's
// business, not the mutation's.
//
// TestAMutationThatBreaksTheSweepWritesNothing fails without this.
func mutateSweep(before []string, after []*resource.Resource) []string {
	was := map[string]int{}
	for _, f := range before {
		was[f]++
	}
	var broke []string
	for _, f := range storetest.Sweep(after, nil, nil) {
		if was[f] > 0 {
			was[f]--
			continue
		}
		broke = append(broke, f)
	}
	return broke
}

// findResource resolves <kind> <name-or-id> against the state.
//
// By identifier first, then by the `name` attribute, and an ambiguity is an
// error naming the candidates rather than a pick. Two resources of one kind
// sharing a name is a thing a store can hold, and mutating whichever came first
// in a map iteration is how a test drifts the wrong machine.
func findResource(resources []*resource.Resource, kind, target string) (*resource.Resource, error) {
	var byName []*resource.Resource
	kinds := map[string]bool{}
	for _, res := range resources {
		if res == nil {
			continue
		}
		kinds[res.Kind] = true
		if res.Kind != kind {
			continue
		}
		if res.ID == target {
			return res, nil
		}
		if attrName(res) == target {
			byName = append(byName, res)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		if !kinds[kind] {
			known := make([]string, 0, len(kinds))
			for k := range kinds {
				known = append(known, k)
			}
			sort.Strings(known)
			if len(known) == 0 {
				return nil, fmt.Errorf("the emulator holds no resource at all, so there is no %q to mutate", kind)
			}
			return nil, fmt.Errorf("no resource of kind %q; the emulator holds %s\n"+
				"       feint mutate --list prints every resource with its kind", kind, strings.Join(known, ", "))
		}
		return nil, fmt.Errorf("no %s named or identified %q; "+
			"feint mutate --list prints what there is", kind, target)
	default:
		ids := make([]string, 0, len(byName))
		for _, res := range byName {
			ids = append(ids, res.ID)
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("%d resources of kind %s are named %q (%s); name one by its identifier",
			len(byName), kind, target, strings.Join(ids, ", "))
	}
}

func attrName(res *resource.Resource) string {
	if res == nil {
		return ""
	}
	name, _ := res.Attrs["name"].(string)
	return name
}

// applyMutations changes the resource and returns the ledger lines that
// describe what it changed — one call, both results.
//
// The pairing is deliberate: the record is computed from the edit, by the
// function that performs it, so there is no way to perform one without
// producing the other. See encodeMutatedState for the other half.
// refuseRuntimePath refuses a path that looks like it aims at Resource.Runtime.
//
// This function only ever walks Attrs, so `--set runtime.machine=…` did not
// reach the runtime map: it quietly created an attribute *named* runtime. That
// is worse than a refusal, because the operator who typed it believes the
// opposite, and the field they aimed at is the one the Incus driver turns into
// commands — a crafted one reached `incus exec <that name>` as recently as
// 2026-11-13.
//
// Runtime stays out of reach on purpose. It is emulator bookkeeping, not
// something a cloud console could change, so drifting it would not reproduce
// anything a user can meet.
//
// TestAMutationCannotReachRuntime fails without this.
func refuseRuntimePath(assignment string) error {
	path, _, _ := strings.Cut(assignment, "=")
	head, _, _ := strings.Cut(path, ".")
	if !strings.EqualFold(strings.TrimSpace(head), "runtime") {
		return nil
	}
	return fmt.Errorf("%q aims at the resource's runtime bookkeeping, which this verb never "+
		"touches: it holds the names the machine driver turns into commands, and no cloud "+
		"console can change them. Drift an attribute a client can read instead", path)
}

func applyMutations(res *resource.Resource, sets, unsets []string, at time.Time) ([]string, error) {
	if res.Attrs == nil {
		res.Attrs = map[string]any{}
	}
	for _, path := range append(append([]string{}, sets...), unsets...) {
		if err := refuseRuntimePath(path); err != nil {
			return nil, err
		}
	}
	stamp := at.Format(time.RFC3339)
	var records []string

	for _, assignment := range sets {
		path, raw, found := strings.Cut(assignment, "=")
		if !found {
			return nil, fmt.Errorf("--set %q has no value; the form is --set <path>=<json>", assignment)
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, fmt.Errorf("--set %s: %q is not JSON. A string needs its quotes: --set %s='\"web-01\"'",
				path, raw, path)
		}
		if err := setPath(res.Attrs, path, value); err != nil {
			return nil, fmt.Errorf("--set %s: %w", path, err)
		}
		records = append(records, fmt.Sprintf("%s set %s %s %s=%s", stamp, res.Kind, res.ID, path, raw))
	}

	for _, path := range unsets {
		if err := unsetPath(res.Attrs, path); err != nil {
			return nil, fmt.Errorf("--unset %s: %w", path, err)
		}
		records = append(records, fmt.Sprintf("%s unset %s %s %s", stamp, res.Kind, res.ID, path))
	}

	// Updated moves, because a cloud that changed a resource says when. A
	// mutation that left the timestamp alone would be a drift no client polling
	// on it could ever see, which is half the thing being tested.
	res.Updated = at
	return records, nil
}

// setPath writes value at a dotted path inside an attribute tree.
//
// A numeric segment indexes a list that is already there; it never grows one.
// Growing on demand would let `--set ips.7=…` write a sparse list with six nils
// in it, and a nil in a list of addresses is a shape no pack ever produced and
// every pack would then have to survive.
func setPath(attrs map[string]any, path string, value any) error {
	segments, err := splitPath(path)
	if err != nil {
		return err
	}
	last := len(segments) - 1
	var cursor any = attrs
	for i, seg := range segments {
		if i == last {
			return assign(cursor, seg, value)
		}
		next, err := descend(cursor, seg)
		if err != nil {
			return err
		}
		cursor = next
	}
	return nil
}

func unsetPath(attrs map[string]any, path string) error {
	segments, err := splitPath(path)
	if err != nil {
		return err
	}
	last := len(segments) - 1
	var cursor any = attrs
	for i, seg := range segments {
		if i == last {
			holder, ok := cursor.(map[string]any)
			if !ok {
				return fmt.Errorf("a list element cannot be removed, only replaced")
			}
			if _, present := holder[seg]; !present {
				return fmt.Errorf("the resource carries no attribute %q", seg)
			}
			delete(holder, seg)
			return nil
		}
		next, err := descend(cursor, seg)
		if err != nil {
			return err
		}
		cursor = next
	}
	return nil
}

func splitPath(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("the path is empty")
	}
	segments := strings.Split(path, ".")
	for _, s := range segments {
		if s == "" {
			return nil, fmt.Errorf("the path has an empty segment")
		}
	}
	return segments, nil
}

func descend(cursor any, segment string) (any, error) {
	switch holder := cursor.(type) {
	case map[string]any:
		next, ok := holder[segment]
		if !ok {
			return nil, fmt.Errorf("nothing at %q on the way down", segment)
		}
		return next, nil
	case []any:
		i, err := index(segment, len(holder))
		if err != nil {
			return nil, err
		}
		return holder[i], nil
	default:
		return nil, fmt.Errorf("%q is not a container, so the path cannot go through it", segment)
	}
}

func assign(cursor any, segment string, value any) error {
	switch holder := cursor.(type) {
	case map[string]any:
		holder[segment] = value
		return nil
	case []any:
		i, err := index(segment, len(holder))
		if err != nil {
			return err
		}
		holder[i] = value
		return nil
	default:
		return fmt.Errorf("%q is not a container, so nothing can be written under it", segment)
	}
}

func index(segment string, length int) (int, error) {
	i, err := strconv.Atoi(segment)
	if err != nil {
		return 0, fmt.Errorf("%q indexes a list, so it must be a number", segment)
	}
	if i < 0 || i >= length {
		return 0, fmt.Errorf("index %d is outside a list of %d", i, length)
	}
	return i, nil
}

// repeatedFlag collects a flag given more than once, in order.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }

func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}
