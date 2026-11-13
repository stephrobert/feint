package store

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// The ledger of changes nobody's API made.
//
// `feint mutate` changes a stored resource behind the provider's back, so a
// client's next read sees a drift it did not cause (#193). That is the point of
// the verb, and it is also the reason the run that carries one can never again
// be read as a measurement of what a real cloud does: the state the client is
// reading was written by the operator, not by a handler.
//
// The boundary is the one fault injection already holds (#26): an injected
// answer moves exactly one counter, it is published, and tools/conformance/
// score.sh refuses a run carrying one. A mutated state gets the same treatment,
// and the ledger below is where it is written down.
//
// Why here, and not a flag on the server. The emulator receives `PUT
// /_feint/state` and cannot tell a `snapshot load` from a mutation — both are a
// whole state document arriving at the only door the store has. So the mark
// travels *inside* the document, in the same envelope as the resources, and the
// store is what reads it. Three properties follow, and they are what make the
// mark hard to lose:
//
//   - it is append-only. Restore merges the incoming ledger into the one the
//     store already holds instead of replacing it, so loading a clean snapshot
//     over a mutated emulator does not launder the run. A process that has held
//     a mutated state says so until it exits.
//   - it propagates. Snapshot writes the ledger back out, so a state saved from
//     a mutated emulator carries its own history into whatever loads it next.
//   - it is the store's, not the caller's word about itself. Nothing outside
//     this package can clear it; there is no setter that takes an empty ledger.
//
// What it does not claim: a state document composed by hand and sent with curl
// carries no ledger and is not marked. That is the same boundary as today —
// tools/conformance/scaleway/scw-cli.sh seeds an Elastic Metal server exactly
// that way — and it is named in docs/limits.md rather than pretended away.

const (
	// maxMutationRecords bounds the ledger a snapshot may carry.
	//
	// The ledger arrives over `PUT /_feint/state`, so it is request-shaped
	// input and gets request-shaped bounds. maxStateBody already caps the body;
	// this caps what one body can make the store keep for the rest of its life,
	// since the ledger is append-only and never shrinks.
	maxMutationRecords = 256
	// maxMutationRecord bounds one line. Each is printed by `feint status` and
	// served on /_feint/conformance, so it is display-bound, not storage-bound.
	maxMutationRecord = 512
)

// RecordMutation appends one line to the ledger of out-of-band changes.
//
// Exported because the CLI composes the line (it is the only thing that knows
// which attribute of which resource it changed) and the store keeps it.
// Refuses nothing: the caller is in-process and the line is validated on the
// way back in by checkMutations, which is where untrusted ones arrive.
func (s *Store) RecordMutation(record string) {
	record = strings.TrimSpace(record)
	if record == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mergeMutations(append(slices.Clone(s.mutations), record))
}

// Mutations returns the ledger, oldest first. Empty on every emulator nobody
// mutated, which is every emulator by default.
func (s *Store) Mutations() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.mutations))
	copy(out, s.mutations)
	return out
}

// mergeMutations folds an incoming ledger into the one the store holds. Called
// under the write lock.
//
// Two cases, and the first is the one that happens every time:
//
//   - the incoming ledger begins with ours. That is a read-modify-write — GET
//     the state, change one attribute, PUT it back — so the incoming ledger is
//     our own history plus what was just done, and it replaces ours whole. A
//     blind append would duplicate every earlier line on every mutation.
//   - anything else is a foreign history: a snapshot from another instance, or
//     a clean one loaded over a mutated store. Both are kept, ours first,
//     because neither cancels the other.
//
// Bounded, and the bound drops the newest rather than the oldest: the first
// mutation is what explains when the run stopped describing a cloud, and a
// ledger that forgot it to make room for the thousandth would answer the wrong
// question. mutationCount keeps rising so an overflow is visible.
func (s *Store) mergeMutations(incoming []string) {
	merged := incoming
	if !isPrefix(s.mutations, incoming) {
		merged = append(slices.Clone(s.mutations), incoming...)
	}
	s.mutationCount = len(merged)
	if len(merged) > maxMutationRecords {
		merged = merged[:maxMutationRecords]
	}
	s.mutations = merged
}

// isPrefix reports whether every line of a is the start of b.
func isPrefix(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// checkMutations validates a ledger arriving from outside.
//
// A restored state is untrusted input — CLAUDE.md says it in as many words, and
// snapshot.go says the format is made to outlive its instance. These lines end
// up on /_feint/conformance and in a terminal, so a record carrying a control
// character is refused at the door rather than escaped at every reader.
// TestARestoredMutationLedgerIsValidated fails without this.
func checkMutations(records []string) error {
	if len(records) > maxMutationRecords {
		return fmt.Errorf("the snapshot carries %d mutation records and this build keeps at most %d",
			len(records), maxMutationRecords)
	}
	for i, r := range records {
		if len(r) > maxMutationRecord {
			return fmt.Errorf("mutation record %d is %d bytes and the bound is %d",
				i, len(r), maxMutationRecord)
		}
		for _, c := range r {
			if c == unicode.ReplacementChar || (unicode.IsControl(c) && c != '\t') {
				return fmt.Errorf("mutation record %d carries a control character: "+
					"these lines are printed and served, so they hold text only", i)
			}
		}
	}
	return nil
}
