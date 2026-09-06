package emulator

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Declared is the catalogue an operator declares as their own contract (#126):
// for a provider, the identifiers each kind of catalogue object may name.
//
// Nil is the compatibility mode, and the default. Nothing is checked: a create
// naming an image the emulator never heard of succeeds, because a team's first
// move is pointing an existing stack, production identifiers hardcoded, at an
// emulator that has no inventory (docs/limits.md, "Identifiers are not checked
// against anything"). What that costs is that `ami-1234567X` passes exactly
// like `ami-12345678`, and the typo ships green.
//
// With a declaration, a pack refuses an identifier outside it, in its own
// error shape, for the kinds the declaration names — and only those: a
// declaration that names images and not types checks images alone, so an
// operator can assert the half of their contract they know. An object the
// emulator holds because the client registered it (an image cut from a
// machine, a template it uploaded) is the client's own and is never refused
// by the declaration; the declaration is about what the fixed catalogue may
// answer for.
//
// The core holds strings. What a kind is called ("images", "types",
// "templates") is each pack's vocabulary, declared through Declaring, so the
// core learns no provider and no catalogue: it only remembers what the
// operator wrote and answers whether a value is in it.
type Declared struct {
	kinds map[string]map[string]map[string]bool // provider -> kind -> id
}

// ParseDeclared reads the JSON an operator writes:
//
//	{"outscale": {"images": ["ami-fe1a7001"], "types": ["tinav6.c2r4p2"]}}
//
// Every value is a list of identifier strings. An empty list is a kind that
// allows nothing, which is a declaration and not an omission; an absent kind
// is not checked at all.
func ParseDeclared(r io.Reader) (*Declared, error) {
	var raw map[string]map[string][]string
	dec := json.NewDecoder(r)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("a declared catalogue is {\"<provider>\": {\"<kind>\": [\"<identifier>\", …]}}: %w", err)
	}
	d := &Declared{kinds: map[string]map[string]map[string]bool{}}
	for provider, kinds := range raw {
		if strings.TrimSpace(provider) == "" {
			return nil, fmt.Errorf("a declared catalogue names its providers, and one name is empty")
		}
		d.kinds[provider] = map[string]map[string]bool{}
		for kind, ids := range kinds {
			if strings.TrimSpace(kind) == "" {
				return nil, fmt.Errorf("%s: a kind name is empty", provider)
			}
			set := map[string]bool{}
			for _, id := range ids {
				if strings.TrimSpace(id) == "" {
					return nil, fmt.Errorf("%s/%s: an empty identifier declares nothing", provider, kind)
				}
				set[id] = true
			}
			d.kinds[provider][kind] = set
		}
	}
	return d, nil
}

// Allows reports whether an identifier may name a catalogue object of that
// kind for that provider, and whether the declaration says anything about the
// kind at all. An undeclared kind allows everything — the compatibility mode,
// kind by kind — and so does a nil declaration.
func (d *Declared) Allows(provider, kind, id string) (allowed, declared bool) {
	if d == nil {
		return true, false
	}
	set, ok := d.kinds[provider][kind]
	if !ok {
		return true, false
	}
	return set[id], true
}

// Refuses is Allows read from the refusing side: true when the kind is
// declared and the identifier is outside it. What every pack asks.
func (d *Declared) Refuses(provider, kind, id string) bool {
	allowed, declared := d.Allows(provider, kind, id)
	return declared && !allowed
}

// Summary is one line per provider for the operator: what was declared, and
// how much of it.
func (d *Declared) Summary() []string {
	if d == nil {
		return nil
	}
	var out []string
	for provider, kinds := range d.kinds {
		parts := make([]string, 0, len(kinds))
		for kind, set := range kinds {
			parts = append(parts, fmt.Sprintf("%d %s", len(set), kind))
		}
		sort.Strings(parts)
		out = append(out, provider+": "+strings.Join(parts, ", "))
	}
	sort.Strings(out)
	return out
}

// Declaring is the optional half of a pack that checks a declared catalogue:
// the kinds it knows how to refuse. serve reads it to refuse a declaration
// naming a kind no pack checks — a typo in the file that would otherwise
// check nothing while reading like a contract, which is the well-formed-is-
// not-enforced defect this repository names.
type Declaring interface {
	DeclaredKinds() []string
}

// CheckDeclared lists what a declaration names that no mounted pack checks: a
// provider no pack carries, or a kind the pack does not know how to refuse.
// Empty means every line of the declaration is enforced by somebody.
//
// TestADeclarationNamingAKindNoPackChecksIsRefused fails without this.
func CheckDeclared(d *Declared, packs []Pack) []string {
	if d == nil {
		return nil
	}
	known := map[string]map[string]bool{}
	for _, p := range packs {
		kinds := map[string]bool{}
		if declaring, ok := p.(Declaring); ok {
			for _, kind := range declaring.DeclaredKinds() {
				kinds[kind] = true
			}
		}
		known[p.Name()] = kinds
	}
	var problems []string
	for provider, kinds := range d.kinds {
		checked, mounted := known[provider]
		if !mounted {
			problems = append(problems, provider+": no mounted pack has that name")
			continue
		}
		names := make([]string, 0, len(checked))
		for kind := range checked {
			names = append(names, kind)
		}
		sort.Strings(names)
		for kind := range kinds {
			if !checked[kind] {
				problems = append(problems, fmt.Sprintf("%s/%s: the %s pack checks %s, not %q",
					provider, kind, provider, strings.Join(names, " and "), kind))
			}
		}
	}
	sort.Strings(problems)
	return problems
}
