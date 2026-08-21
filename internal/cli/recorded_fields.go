package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/transcript"
)

// The gate behind `recordedFields` in contracts/*.json.
//
// A contract is extracted from the provider's own API description and may not be
// edited by hand — the pre-commit hook re-runs the extraction and diffs. One
// exception exists, and it exists because a document can be behind the API it
// describes: tools/contract/*-recorded-fields.yaml adds a field a recording of
// the real cloud carries and the document does not declare. Exoscale's zone id
// and security-group visibility are the first two (#370, #371).
//
// That exception is a door, and a door into "the shape of a response" is
// precisely what rule 4 forbids leaving unlocked. What locks it is here: every
// entry names the recording that proves it, and this holds the entry against
// that recording. A field nothing in corpus/ still carries is not a field this
// emulator may answer, whatever the fragment says.
//
// It is the rule corpus/accepted.json already lives under, applied to the
// opposite kind of entry. There, an exemption that excuses no divergence is
// deleted, because a stale exemption is a gate that quietly stopped covering
// what it names. Here, a citation that cites nothing is deleted, because a
// stale citation is an invented field with a footnote.
//
// # Why this is not inside `feint corpus --check`
//
// That would be the obvious home: same artefacts, same rule, one command. It
// would also be wrong, and for the reason #88's two red pull requests wrote
// down. The corpus gate takes a --dir and is pointed at fixtures by half a dozen
// of its own tests — a temporary directory holding one self-recording. Its
// population is "whatever this run was pointed at", so a verdict of "no
// recording carries this field" would be true of every fixture run and would
// have to be either a false failure or a silent skip. This verdict needs the
// whole committed corpus, so it reads the whole committed corpus, and a test
// rather than a flag is what guarantees that.

// staleRecordedField is one entry of a contract's recordedFields list that the
// recording it names no longer supports, with what is wrong stated in the terms
// the reader has to act on.
type staleRecordedField struct {
	Contract string
	Field    contract.RecordedField
	Why      string
}

func (s staleRecordedField) String() string {
	return fmt.Sprintf("%s: %s.%s, cited from %s %s %s: %s",
		s.Contract, s.Field.Schema, s.Field.Property,
		s.Field.Corpus, s.Field.Operation, s.Field.Path, s.Why)
}

// staleRecordedFields holds every contract's recordedFields against the corpus
// each entry cites, and reports the entries the recordings no longer support.
//
// The second return is how many entries were examined, and a caller must refuse
// a run that examined none. That is not defensive noise: the way this control
// dies silently is `--recorded-fields` dropping out of tools/contract/update.sh,
// which empties the list, leaves the packs answering fields the contract no
// longer declares, and leaves this reporting nothing wrong.
func staleRecordedFields(contractsDir, corpusDir string) ([]staleRecordedField, int, error) {
	docs, err := loadContracts(contractsDir)
	if err != nil {
		return nil, 0, err
	}

	// Recordings are loaded once each and shared: exo-cli.jsonl is 203
	// exchanges, and every entry citing it would otherwise re-read it.
	shapes := map[string]map[string]bool{}
	pathsOf := func(corpus, operation string) (map[string]bool, error) {
		key := corpus + "\x00" + operation
		if known, ok := shapes[key]; ok {
			return known, nil
		}
		exs, err := loadTranscript(filepath.Join(corpusDir, filepath.FromSlash(corpus)))
		if err != nil {
			return nil, err
		}
		fields, found := transcript.Shape(exs, operation)
		if !found {
			shapes[key] = nil
			return nil, nil
		}
		known := make(map[string]bool, len(fields))
		for _, f := range fields {
			known[f.Path] = true
		}
		shapes[key] = known
		return known, nil
	}

	var stale []staleRecordedField
	checked := 0
	for _, provider := range sortedContractNames(docs) {
		doc := docs[provider]
		for _, f := range doc.RecordedFields {
			checked++
			bad := func(why string) {
				stale = append(stale, staleRecordedField{Contract: provider, Field: f, Why: why})
			}

			// The extraction folds the property into the schema and writes the
			// citation; the two arriving apart means the artefact was edited by
			// hand or the extraction stopped applying the fragment.
			schema, ok := doc.Schemas[f.Schema]
			if !ok {
				bad("the contract declares no such schema, so the citation stands for nothing")
				continue
			}
			if _, ok := schema.Properties[f.Property]; !ok {
				bad("the schema does not declare the property this entry says was added to it")
				continue
			}

			if _, err := os.Stat(filepath.Join(corpusDir, filepath.FromSlash(f.Corpus))); err != nil {
				bad("the recording it names is not under " + corpusDir +
					"; a citation whose recording left is a claim nobody can check")
				continue
			}
			known, err := pathsOf(f.Corpus, f.Operation)
			if err != nil {
				return nil, checked, err
			}
			if known == nil {
				bad("that recording holds no exchange for this operation any more")
				continue
			}
			if !known[f.Path] {
				bad("that recording's answers no longer carry this path, so nothing proves the " +
					"cloud still sends it: re-record it, or delete the entry and stop answering the field")
			}
		}
	}
	return stale, checked, nil
}

// sortedContractNames keeps the report in the same order on every run, whatever
// order the map iterates in.
func sortedContractNames(docs map[string]*contract.Doc) []string {
	out := make([]string, 0, len(docs))
	for name := range docs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
