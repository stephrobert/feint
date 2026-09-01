package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// The triage record for operations a document declares and an SDK never wrapped
// (#622).
//
// It exists because those operations cannot go in Declined(): drift.Compare
// reports a decline the scan does not know as an *orphan*, which is the one
// signal the gate exists to keep reliable. So they are held here instead, and
// the gate reads this file the way it reads a baseline.
//
// What it must not become is a place to park a name. Every entry states a status
// and a reason, and an entry that states neither is refused — the same rule
// Declined() already lives under, for the same reason: a refusal nobody can weigh
// is a refusal nobody triaged.

// contractOnlyRecord is coverage/contract-only.json.
type contractOnlyRecord struct {
	Providers map[string][]contractOnlyEntry `json:"providers"`
}

type contractOnlyEntry struct {
	Operation string `json:"operation"`
	// Status is "declined" or "backlog", and the distinction is the whole point
	// of this file. *Out of scope* and *not done yet* are different answers, and
	// an inventory that renders them the same way is the flatness #622 named.
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// contractOnlyStatuses is the closed list. An unknown status is refused rather
// than passed through: a third word would divide the two buckets without anybody
// deciding what it means.
var contractOnlyStatuses = map[string]bool{"declined": true, "backlog": true}

// contractOnlyGaps reports what fails the gate: an operation the document
// declares that neither the scan knows nor this record explains, and an entry
// this record carries that says nothing usable.
//
// The stale half matters as much as the missing one. An entry naming an
// operation the SDK has since wrapped is an exemption that stopped covering
// anything, and a record that only ever grows is a ratchet nobody can lower.
func contractOnlyGaps(path, provider string, only []string) ([]string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path the operator passed
	if err != nil {
		return nil, fmt.Errorf("read the contract-only record: %w", err)
	}
	var record contractOnlyRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("read the contract-only record: %w", err)
	}

	declared := map[string]contractOnlyEntry{}
	var problems []string
	for _, entry := range record.Providers[provider] {
		declared[entry.Operation] = entry
		switch {
		case !contractOnlyStatuses[entry.Status]:
			problems = append(problems, fmt.Sprintf(
				"%s carries status %q; it must be \"declined\" (out of scope) or \"backlog\" (not done yet)",
				entry.Operation, entry.Status))
		case len(strings.Fields(entry.Reason)) < 5:
			problems = append(problems, fmt.Sprintf(
				"%s says %q, which is not a reason a reader can weigh", entry.Operation, entry.Reason))
		}
	}

	found := map[string]bool{}
	for _, op := range only {
		found[op] = true
		if _, ok := declared[op]; !ok {
			problems = append(problems, fmt.Sprintf(
				"%s is declared by the document and absent from the scanned surface, and nothing "+
					"here says whether it is out of scope or not done yet", op))
		}
	}
	for op := range declared {
		if !found[op] {
			problems = append(problems, fmt.Sprintf(
				"%s is recorded here and the scan now knows it: the entry covers nothing and must go", op))
		}
	}
	sort.Strings(problems)
	return problems, nil
}
