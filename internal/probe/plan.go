package probe

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/contract"
)

// Step is one call the probe will make.
type Step struct {
	// Operation is the route's declared name, for reporting.
	Operation string
	// Contract is the key the contract knows it by.
	Contract string
	Method   string
	Path     string
	// Request is the schema of the body to build, empty for none.
	Request string
	// Skip says why this step will not run, empty when it will.
	Skip string
	// Product is the contract-key prefix ("block/v1" out of
	// "block/v1.CreateVolume"), empty for the providers whose document is flat.
	// It scopes the pool: a block volume and an instance volume share a word
	// and nothing else.
	Product string
	// Makes is the folded resource type a success of this step brings into
	// being ("volume"), empty for steps that create nothing. Derived from the
	// operation's own name; it is what the planner's seeding order and the
	// pool's created tier key on.
	Makes string
	// Kind says which phase the step belongs to.
	Kind opKind
}

// Plan orders the routes so that what produces an identifier runs before what
// needs one, and what destroys runs last.
//
// The order is derived, not written. Reads come first because they need nothing
// and they are where the emulated inventory is published — a client that creates
// a machine reads the catalogue first, and so does this. Creates follow, and
// deletes come last so the run leaves the store as it found it.
//
// Within the creates the order is the seeding order (#163): a step that needs a
// volume_id runs after the step whose name says it makes volumes. Before this,
// creates ran alphabetically, CreateSnapshot arrived before the CreateVolume it
// depends on, and the guaranteed refusal that followed was counted as if the
// operation could answer nothing else. TestAConsumerRunsAfterItsProducer fails
// without the ordering. Everything is derived from the contract and the
// operations' own names: nothing here writes a scenario by hand, and an
// operation whose needs no step produces still runs — against the wrong
// identifier, into the refusal that honestly describes it.
func Plan(doc *contract.Doc, routes []contract.MountedRoute) ([]Step, error) {
	if doc == nil {
		return nil, fmt.Errorf("no contract to plan from")
	}

	var reads, creates, deletes []Step
	for _, route := range routes {
		op, name, known := doc.OperationFor(route.Operation)
		if !known {
			// The route table check already fails on this. Reported here too
			// rather than skipped in silence, because a probe that quietly does
			// nothing is worse than one that does not run.
			return nil, fmt.Errorf("route %s serves %q, which the contract does not define",
				route.Path, route.Operation)
		}

		step := Step{
			Operation: route.Operation,
			Contract:  name,
			Method:    route.Method,
			Path:      op.Path,
			Request:   op.Request,
			Product:   productOf(name),
		}
		step.Kind = kind(name, route.Method)
		step.Makes = makes(name, step.Kind)
		if op.Response == "" {
			// Nothing to validate against, so calling it would prove only that
			// it answered. Said out loud: these are what the extraction reports
			// as operations with no response schema.
			step.Skip = "the contract declares no response schema"
		}

		switch step.Kind {
		case kindDelete:
			deletes = append(deletes, step)
		case kindCreate:
			creates = append(creates, step)
		default:
			reads = append(reads, step)
		}
	}

	byName := func(s []Step) { sort.Slice(s, func(i, j int) bool { return s[i].Contract < s[j].Contract }) }
	byName(reads)
	byName(deletes)
	rank := orderCreates(doc, creates)
	orderDeletes(doc, deletes, rank)

	// Reads run twice, and the second pass is what makes most of the plan
	// reachable. The first finds an empty account, so nothing is harvested and
	// every read-by-identifier skips; the creates then produce the identifiers,
	// and the same reads become answerable. Measured on Scaleway: 19 operations
	// probed with one pass, 34 with two; on Outscale, 12 against 18.
	plan := make([]Step, 0, len(reads)*2+len(creates)+len(deletes))
	plan = append(plan, reads...)
	plan = append(plan, creates...)
	plan = append(plan, reads...)
	plan = append(plan, deletes...)
	return plan, nil
}

// orderCreates sorts the create steps so producers run before consumers, and
// returns each resource type's rank for the teardown to mirror.
//
// Kahn's algorithm over the needs/makes edges, with the contract's own
// alphabetical order inside each layer so two runs plan the same thing. A cycle
// — two creates each naming a type the other makes — falls back to name order
// for whatever remains, which is the old behaviour, stated rather than special.
func orderCreates(doc *contract.Doc, creates []Step) map[string]int {
	sort.Slice(creates, func(i, j int) bool { return creates[i].Contract < creates[j].Contract })

	// A type is pending while a not-yet-ordered step makes it.
	pending := map[string]int{}
	for _, s := range creates {
		if s.Makes != "" {
			pending[s.Makes]++
		}
	}

	needsOf := make([][]string, len(creates))
	for i, s := range creates {
		needsOf[i] = needs(doc, s)
	}

	ordered := make([]Step, 0, len(creates))
	rank := map[string]int{}
	done := make([]bool, len(creates))
	place := func(i int) {
		done[i] = true
		if m := creates[i].Makes; m != "" {
			if _, seen := rank[m]; !seen {
				rank[m] = len(ordered)
			}
		}
		ordered = append(ordered, creates[i])
	}
	for len(ordered) < len(creates) {
		// One whole layer per sweep, against the pending set as it stood
		// before the sweep. Placing steps mid-sweep unblocked whatever
		// happened to sort after its producer and not before: Exoscale's
		// remove-instance-protection ran the sweep create-instance landed in,
		// while add-instance-protection waited for the next one — so the
		// protection was added back after being removed, and delete-instance
		// refused a resource the plan itself had locked
		// (TestALayerRunsInNameOrder).
		var layer []int
		for i, s := range creates {
			if done[i] {
				continue
			}
			blocked := false
			for _, need := range needsOf[i] {
				if need == "resource" {
					// A need that names only "resource" — Outscale's
					// CreateTags — is satisfied by anything, so it waits for
					// everything: it runs once every producer has run.
					for made, left := range pending {
						if made != s.Makes && left > 0 {
							blocked = true
							break
						}
					}
				} else if need != s.Makes && pending[need] > 0 {
					blocked = true
				}
				if blocked {
					break
				}
			}
			if !blocked {
				layer = append(layer, i)
			}
		}
		if len(layer) == 0 {
			// A dependency cycle — Outscale's CreateImage wants a Vm and
			// CreateVms wants an image. Release the first remaining step by
			// name and keep sorting: everything the cycle does not bind stays
			// correctly ordered, and the released step gets the retry pass
			// (Run) once its needs exist. Releasing everything at once was the
			// first version, and it dumped the whole provider back into
			// alphabetical order for one cycle.
			for i := range creates {
				if !done[i] {
					layer = []int{i}
					break
				}
			}
		}
		for _, i := range layer {
			place(i)
		}
		for _, i := range layer {
			if m := creates[i].Makes; m != "" {
				pending[m]--
			}
		}
	}
	copy(creates, ordered)
	return rank
}

// orderDeletes sorts the teardown as the mirror of the seeding: what was made
// last is unmade first, so DeleteSubnet runs before the DeleteNet that would
// otherwise refuse a net still holding subnets. Unlinking sorts before
// deleting at equal depth — an attachment must come off before either of its
// ends can go — and name order settles the rest.
func orderDeletes(doc *contract.Doc, deletes []Step, rank map[string]int) {
	depth := func(s Step) int {
		deepest := -1
		for _, need := range needs(doc, s) {
			if r, known := rank[need]; known && r > deepest {
				deepest = r
			}
		}
		// The type the step destroys counts too: DeleteRoute names no route
		// id — a route is addressed by table and destination — and without
		// this its depth was its table's, the reverse-name tie-break put
		// DeleteRouteTable first, and the route died with its table before
		// its own delete was measured.
		if r, known := rank[unmakes(s.Contract)]; known && r > deepest {
			deepest = r
		}
		return deepest
	}
	unlinks := func(s Step) bool {
		verb := strings.ToLower(leafOf(s.Contract))
		return strings.HasPrefix(verb, "unlink") || strings.HasPrefix(verb, "detach")
	}
	sort.SliceStable(deletes, func(i, j int) bool {
		di, dj := depth(deletes[i]), depth(deletes[j])
		if di != dj {
			return di > dj
		}
		ui, uj := unlinks(deletes[i]), unlinks(deletes[j])
		if ui != uj {
			return ui
		}
		// Reverse name order at equal depth, because the creates ran in name
		// order: DeleteSecurityGroupRule after DeleteSecurityGroup deleted a
		// rule inside a group already gone.
		return deletes[i].Contract > deletes[j].Contract
	})
}

// needs lists the resource types one step's call will look for: its path
// parameters, and the identifier fields of its request schema — optional ones
// included, because the seeding fills them when their type exists and the
// order must make that possible.
func needs(doc *contract.Doc, s Step) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}

	rest := s.Path
	for {
		open := strings.Index(rest, "{")
		if open < 0 {
			break
		}
		closing := strings.Index(rest[open:], "}")
		if closing < 0 {
			break
		}
		param := rest[open+1 : open+closing]
		switch strings.ToLower(param) {
		case "zone", "region":
		default:
			if isID(param) {
				add(typeOfIDField(param))
			} else if t := lastCollection(rest[:open]); t != "" {
				add(t)
			}
		}
		rest = rest[open+closing+1:]
	}

	addSchema(doc, s.Request, add, map[string]bool{})
	return out
}

// addSchema walks one request schema for identifier fields, refs included —
// BookIP's need for a private network lives one level down, in its source.
func addSchema(doc *contract.Doc, name string, add func(string), visiting map[string]bool) {
	if name == "" || visiting[name] {
		return
	}
	visiting[name] = true
	schema := doc.Schemas[name]
	for field, prop := range schema.Properties {
		if isID(field) {
			add(typeOfIDField(field))
			continue
		}
		if prop.Ref != "" {
			// The field's own name is a need too: Exoscale's detach bodies say
			// {"instance": {"id": …}}, and a teardown order that missed the
			// instance need ran delete-instance before the detaches that still
			// address it.
			add(foldField(field))
			addSchema(doc, prop.Ref, add, visiting)
		}
		if prop.Type == "object" && prop.Ref == "" {
			add(foldField(field))
		}
	}
}

// productOf reads the product qualifier off a contract key:
// "block/v1.CreateVolume" → "block/v1", "ReadVms" → "".
func productOf(contractName string) string {
	if dot := strings.LastIndex(contractName, "."); dot >= 0 && strings.Contains(contractName[:dot], "/") {
		return contractName[:dot]
	}
	return ""
}

// leafOf strips the product qualifier: "block/v1.CreateVolume" → CreateVolume.
func leafOf(contractName string) string {
	if dot := strings.LastIndex(contractName, "."); dot >= 0 {
		return contractName[dot+1:]
	}
	return contractName
}

// makes names the resource type a create-kind operation brings into being,
// read from the operation's own name: CreateVolume → volume, register-ssh-key
// → sshkey, add-rule-to-security-group → rule, BookIP → ip. Link operations
// make their own link (LinkPublicIp answers a LinkPublicIpId), so the whole
// name is the type. Empty for everything else: an update makes nothing.
func makes(contractName string, k opKind) string {
	if k != kindCreate {
		return ""
	}
	leaf := leafOf(contractName)
	lower := strings.ToLower(leaf)
	for _, verb := range []string{"create", "register", "book", "add"} {
		if !strings.HasPrefix(lower, verb) {
			continue
		}
		made := strings.TrimPrefix(leaf[len(verb):], "-")
		for _, cut := range []string{"-to-", "-from-"} {
			if i := strings.Index(made, cut); i >= 0 {
				made = made[:i]
			}
		}
		return foldField(singular(made))
	}
	if strings.HasPrefix(lower, "link") || strings.HasPrefix(lower, "attach") {
		return foldField(leaf)
	}
	return ""
}

// unmakes names the resource type a delete-kind operation removes, read from
// its name the same way makes reads creations: DeleteRoute → route.
func unmakes(contractName string) string {
	leaf := leafOf(contractName)
	lower := strings.ToLower(leaf)
	for _, verb := range []string{"delete", "terminate", "release", "unlink", "detach", "remove"} {
		if !strings.HasPrefix(lower, verb) {
			continue
		}
		gone := strings.TrimPrefix(leaf[len(verb):], "-")
		for _, cut := range []string{"-to-", "-from-"} {
			if i := strings.Index(gone, cut); i >= 0 {
				gone = gone[:i]
			}
		}
		return foldField(singular(gone))
	}
	return ""
}

type opKind int

const (
	kindRead opKind = iota
	kindCreate
	kindDelete
)

// kind classifies an operation from its name and method.
//
// The verbs are each provider's own — Scaleway creates and gets, Outscale
// creates and reads, Exoscale uses kebab-case — so all three vocabularies are
// here, the same way internal/drift's triage carries both. The HTTP method
// decides when the name says nothing.
func kind(name, method string) opKind {
	verb := name
	if i := strings.LastIndex(verb, "."); i >= 0 {
		verb = verb[i+1:]
	}
	lower := strings.ToLower(verb)

	switch {
	case strings.HasPrefix(lower, "delete"), strings.HasPrefix(lower, "terminate"),
		strings.HasPrefix(lower, "release"), strings.HasPrefix(lower, "unlink"),
		strings.HasPrefix(lower, "detach"):
		return kindDelete
	case strings.HasPrefix(lower, "create"), strings.HasPrefix(lower, "register"),
		strings.HasPrefix(lower, "link"), strings.HasPrefix(lower, "attach"):
		return kindCreate
	case strings.HasPrefix(lower, "read"), strings.HasPrefix(lower, "list"),
		strings.HasPrefix(lower, "get"):
		return kindRead
	}

	switch method {
	case "DELETE":
		return kindDelete
	case "POST", "PUT", "PATCH":
		return kindCreate
	default:
		return kindRead
	}
}
