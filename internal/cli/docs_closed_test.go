package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The closed-schema figure comes from the artefact, never from prose.
//
// The README said 643 of 650 while contracts/outscale.json counted 648 of 655.
// A restated figure is a second source of truth, and this one sat in the
// paragraph whose argument is precisely that the number is the provider's own
// declaration.
func TestTheClosedSchemaFigureComesFromTheArtefact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outscale.json")
	body := `{"provider":"outscale","closedPolicy":"declared","schemas":{
		"A":{"closed":true},"B":{"closed":true},"C":{"closed":false}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rendered, err := renderClosedSchemas(path)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, "2 of Outscale's 3 schemas") {
		t.Errorf("the figure is not the artefact's 2 of 3:\n%s", rendered)
	}
	if !strings.Contains(rendered, docsGenerated) {
		t.Errorf("the block does not warn that it is generated:\n%s", rendered)
	}
}

// A closed count under an "assumed" policy is refused, not rendered.
//
// For a provider whose document does not say whether schemas are closed, the
// flags in the artefact record this project's own --assume-closed extraction
// flag. Rendering them as the provider's declaration would be this project's
// assumption wearing their signature.
func TestTheClosedSchemaFigureRefusesAnAssumedPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exoscale.json")
	body := `{"provider":"exoscale","closedPolicy":"assumed","schemas":{
		"A":{"closed":true},"B":{"closed":true}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := renderClosedSchemas(path); err == nil {
		t.Fatal("an assumed policy rendered a count as if the provider had declared it")
	} else if !strings.Contains(err.Error(), "assume-closed") {
		t.Errorf("the refusal does not say what the count would really measure: %v", err)
	}
}

// An empty or unreadable artefact is an error rather than a zero: a sentence
// claiming "0 schemas" because a file moved is worse than no sentence.
func TestTheClosedSchemaFigureRefusesToCountNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outscale.json")
	body := `{"provider":"outscale","closedPolicy":"declared","schemas":{}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := renderClosedSchemas(path); err == nil {
		t.Fatal("an empty artefact rendered as a claim instead of failing")
	}
}
