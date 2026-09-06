package emulator_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The declared catalogue (#126): what an operator writes is what a pack asks
// about, kind by kind, and nothing else changes.

// declaring is a pack that checks two kinds, for the tests below.
type declaring struct {
	emulator.Pack
	name  string
	kinds []string
}

func (d declaring) Name() string            { return d.name }
func (d declaring) DeclaredKinds() []string { return d.kinds }

func parsed(t *testing.T, text string) *emulator.Declared {
	t.Helper()
	d, err := emulator.ParseDeclared(strings.NewReader(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

func TestADeclaredKindRefusesWhatItDoesNotNameAndNothingElse(t *testing.T) {
	d := parsed(t, `{"acme": {"images": ["img-1", "img-2"], "types": []}}`)

	if !d.Refuses("acme", "images", "img-typo") {
		t.Error("an identifier outside a declared kind was allowed")
	}
	if d.Refuses("acme", "images", "img-1") {
		t.Error("a declared identifier was refused")
	}
	// An empty list is a declaration that allows nothing.
	if !d.Refuses("acme", "types", "anything") {
		t.Error("an empty declared kind allowed an identifier")
	}
	// An undeclared kind, and an undeclared provider, are the compatibility
	// mode: not checked.
	if d.Refuses("acme", "zones", "zone-1") {
		t.Error("an undeclared kind refused an identifier")
	}
	if d.Refuses("other", "images", "img-typo") {
		t.Error("an undeclared provider refused an identifier")
	}
	if allowed, declared := d.Allows("acme", "zones", "x"); !allowed || declared {
		t.Errorf("an undeclared kind answers allowed=%v declared=%v", allowed, declared)
	}
}

// Nil is the compatibility mode: every pack asks, and a nil declaration
// answers "not checked" without a guard at every call site.
func TestANilDeclarationChecksNothing(t *testing.T) {
	var d *emulator.Declared
	if d.Refuses("acme", "images", "img-typo") {
		t.Error("nil refused something")
	}
	if got := d.Summary(); got != nil {
		t.Errorf("nil summarises %v", got)
	}
}

func TestADeclarationThatIsNotOneIsRefusedAtParse(t *testing.T) {
	for _, text := range []string{
		`["not", "an", "object"]`,
		`{"acme": {"images": [""]}}`,
		`{"acme": {"": ["x"]}}`,
		`{"": {"images": ["x"]}}`,
		`{"acme": {"images": "img-1"}}`,
	} {
		if _, err := emulator.ParseDeclared(strings.NewReader(text)); err == nil {
			t.Errorf("accepted %s", text)
		}
	}
}

// A declaration naming a kind no pack checks would check nothing while reading
// like a contract, so serve refuses it before anything listens.
func TestADeclarationNamingAKindNoPackChecksIsRefused(t *testing.T) {
	packs := []emulator.Pack{declaring{name: "acme", kinds: []string{"images", "types"}}}

	if problems := emulator.CheckDeclared(parsed(t, `{"acme": {"images": ["x"], "types": ["y"]}}`), packs); len(problems) > 0 {
		t.Fatalf("a declaration every line of which a pack checks was refused: %v", problems)
	}
	problems := emulator.CheckDeclared(parsed(t, `{"acme": {"image": ["x"]}, "nobody": {"images": ["x"]}}`), packs)
	if len(problems) != 2 {
		t.Fatalf("a typo in the kind and an unmounted provider should be two problems: %v", problems)
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, `"image"`) || !strings.Contains(joined, "images and types") {
		t.Errorf("the refusal does not name the typo and what the pack checks:\n%s", joined)
	}
	if !strings.Contains(joined, "nobody: no mounted pack") {
		t.Errorf("the refusal does not name the unmounted provider:\n%s", joined)
	}
	if problems := emulator.CheckDeclared(nil, packs); problems != nil {
		t.Errorf("nil has problems: %v", problems)
	}
}
