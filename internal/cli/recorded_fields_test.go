package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every citation in a committed contract's recordedFields still has its
// recording, and that recording still carries the field.
//
// This is the lock on the one door through which a hand-written shape can reach
// contracts/*.json. A contract is otherwise extracted from the provider's own
// API description and re-extracted by a pre-commit hook, so it cannot be edited;
// tools/contract/*-recorded-fields.yaml is the exception, and it exists because
// Exoscale's document is behind Exoscale's API. Without this test that exception
// is a place to write any field at all and call it measured.
//
// The rule and its wording come from corpus/accepted.json, which states it for
// the opposite kind of entry: an exemption that excuses nothing fails the gate,
// because a stale exemption is a gate that quietly stopped covering what it
// names. A citation that cites nothing is the same failure wearing the other
// hat — an invented field with a footnote.
//
// tools/falsify/specs/recorded-fields.json proves the comparison bites, on
// fixtures rather than on this, because a falsification that mutates the
// committed corpus proves nothing about the code.
func TestEveryRecordedFieldIsStillOnTheWire(t *testing.T) {
	stale, checked, err := staleRecordedFields(
		filepath.Join("..", "..", "contracts"), filepath.Join("..", "..", "corpus"))
	if err != nil {
		t.Fatalf("hold the contracts against the corpus: %v", err)
	}
	for _, s := range stale {
		t.Errorf("%s", s)
	}
	// A run that examined nothing reads exactly like a run that found nothing
	// wrong, which is the failure this whole family of gates exists to name.
	// The way it happens here is --recorded-fields dropping out of
	// tools/contract/update.sh: the list empties, the packs go on answering
	// fields the contract no longer declares, and this reports success.
	//
	// The day every recorded field is legitimately retired — Exoscale
	// publishing both in their document, which the extraction refuses to
	// paper over — the fragment goes and this test goes with it. Deleting a
	// control deliberately is a decision; letting one fall silent is not.
	if checked == 0 {
		t.Fatal("no recordedFields entry was examined, so this measured nothing: either " +
			"tools/contract/update.sh stopped passing --recorded-fields, or the fragment is gone " +
			"and this test should have gone with it")
	}
	t.Logf("%d recorded field(s) still carried by the recording each one names", checked)
}

// And the comparison itself, on fixtures: a citation the recording supports
// passes, and every way one can stop being supported is reported — the path
// gone, the operation gone, the recording gone, and either half of the pair the
// extraction writes arriving without the other.
//
// Fixtures rather than the committed artefacts, because the committed ones are
// meant to be green — a test that mutated corpus/ to prove the gate bites would
// be proving something about the corpus.
func TestARecordedFieldNoRecordingCarriesIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*recordedFieldFixture)
		wantStale string
	}{
		{
			name:   "the recording still carries it",
			mutate: func(*recordedFieldFixture) {},
		},
		{
			name: "the recording no longer carries the path",
			mutate: func(f *recordedFieldFixture) {
				f.Path = "zones[].no-such-field"
			},
			wantStale: "no longer carry this path",
		},
		{
			name: "the recording holds no such operation",
			mutate: func(f *recordedFieldFixture) {
				f.Operation = "exoscale/v2.list-nothing"
			},
			wantStale: "holds no exchange for this operation",
		},
		{
			name: "the recording it names is not there",
			mutate: func(f *recordedFieldFixture) {
				f.Corpus = "exoscale/gone.jsonl"
			},
			wantStale: "is not under",
		},
		{
			name: "the schema does not declare the property",
			mutate: func(f *recordedFieldFixture) {
				f.Property = "never-added"
			},
			wantStale: "does not declare the property",
		},
		{
			name: "the schema is not in the contract",
			mutate: func(f *recordedFieldFixture) {
				f.Schema = "no-such-schema"
			},
			wantStale: "declares no such schema",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecordedFieldFixture()
			tc.mutate(&f)
			contracts, corpus := f.write(t)

			stale, checked, err := staleRecordedFields(contracts, corpus)
			if err != nil {
				t.Fatalf("hold the fixture contract against the fixture corpus: %v", err)
			}
			if checked != 1 {
				t.Fatalf("examined %d entries, want the fixture's one", checked)
			}
			if tc.wantStale == "" {
				if len(stale) != 0 {
					t.Fatalf("a citation the recording supports was reported stale: %v", stale)
				}
				return
			}
			if len(stale) != 1 {
				t.Fatalf("%d entries reported stale, want 1: %v", len(stale), stale)
			}
			if got := stale[0].String(); !strings.Contains(got, tc.wantStale) {
				t.Fatalf("the report does not say %q: %s", tc.wantStale, got)
			}
		})
	}
}

// recordedFieldFixture is one citation and the pair of artefacts it is judged
// against, each field separately mutable so a subtest can break exactly one
// link of the chain.
type recordedFieldFixture struct {
	Schema, Property, Corpus, Operation, Path string
}

func newRecordedFieldFixture() recordedFieldFixture {
	return recordedFieldFixture{
		Schema:    "zone",
		Property:  "id",
		Corpus:    "exoscale/fixture.jsonl",
		Operation: "exoscale/v2.list-zones",
		Path:      "zones[].id",
	}
}

// write lays down a contract naming the citation and a one-line recording that
// carries the field, and returns the two directories.
//
// The recording is written here rather than copied from corpus/ so that the
// subtests above break the link and not the committed measurement.
func (f recordedFieldFixture) write(t *testing.T) (contracts, corpus string) {
	t.Helper()
	root := t.TempDir()
	contracts = filepath.Join(root, "contracts")
	corpus = filepath.Join(root, "corpus", "exoscale")
	for _, dir := range []string{contracts, corpus} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
	}

	doc := map[string]any{
		"provider":   "fixture",
		"source":     "a fixture, not a provider",
		"apiVersion": "0",
		"pathPrefix": "/v2",
		"recordedFields": []map[string]string{{
			"schema": f.Schema, "property": f.Property, "corpus": f.Corpus,
			"operation": f.Operation, "path": f.Path,
			"recorded": "2026-08-21", "why": "a fixture",
		}},
		"operations": map[string]any{},
		"schemas": map[string]any{
			"zone": map[string]any{
				"closed":     true,
				"properties": map[string]any{"id": map[string]any{"type": "string"}},
			},
		},
	}
	writeFixtureJSON(t, filepath.Join(contracts, "fixture.json"), doc)

	exchange := map[string]any{
		"seq": 1, "method": "GET", "path": "/v2/zone",
		"operation": "exoscale/v2.list-zones", "provider": "exoscale",
		"status": 200, "mounted": true,
		"res": map[string]any{"body": map[string]any{
			"zones": []any{map[string]any{"id": "00000000-0000-4000-8000-000000000001", "name": "ch-gva-2"}},
		}},
	}
	raw, err := json.Marshal(exchange)
	if err != nil {
		t.Fatalf("marshal the fixture exchange: %v", err)
	}
	if err := os.WriteFile(filepath.Join(corpus, "fixture.jsonl"), append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write the fixture recording: %v", err)
	}
	return contracts, filepath.Dir(corpus)
}

func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", " ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
