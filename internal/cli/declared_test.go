package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A declaration naming a kind no pack checks is refused before anything
// listens (#126): a line nobody enforces reads exactly like one somebody does.
func TestServeRefusesADeclarationNamingAKindNoPackChecks(t *testing.T) {
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// The typo: "image" for "images", on a pack that checks images and types.
	_, err = loadDeclared(write("typo.json", `{"outscale": {"image": ["ami-fe1a7001"]}}`), srv.Packs())
	if err == nil {
		t.Fatal("a declaration naming a kind no pack checks was accepted")
	}
	if !strings.Contains(err.Error(), `"image"`) || !strings.Contains(err.Error(), "images and types") {
		t.Errorf("the refusal does not name the typo and what the pack checks: %v", err)
	}
	// A provider no pack carries.
	if _, err = loadDeclared(write("nobody.json", `{"nobody": {"images": ["x"]}}`), srv.Packs()); err == nil {
		t.Error("a declaration naming an unmounted provider was accepted")
	}
	// The accepting half: every kind of the three packs, as documented.
	declared, err := loadDeclared(write("ok.json", `{
		"scaleway": {"images": ["debian_bookworm"], "types": ["DEV1-S"]},
		"outscale": {"images": ["ami-fe1a7001"], "types": ["tinav6.c1r1p2"]},
		"exoscale": {"templates": ["11111111-1111-4111-8111-111111111111"], "types": ["21624abb-764e-4def-81d7-9fc54b5957fb"]}
	}`), srv.Packs())
	if err != nil {
		t.Fatalf("the documented declaration was refused: %v", err)
	}
	if len(declared.Summary()) != 3 {
		t.Errorf("the summary names %d providers, want 3: %v", len(declared.Summary()), declared.Summary())
	}
	// And a file that is not there says so.
	if _, err = loadDeclared(filepath.Join(dir, "missing.json"), srv.Packs()); err == nil {
		t.Error("a missing file was accepted")
	}
}
