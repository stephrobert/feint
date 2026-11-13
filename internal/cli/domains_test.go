package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// What this file is for.
//
// #874: an upstream operation that matched no domain was counted in no parity
// issue, and nothing said so. The domain classifier — the mapping from an
// upstream operation to the parity domain that owns it — now lives as data in
// coverage/domains.json, and this test is the thing that says so: it fails in
// both directions, when an operation in the committed record has no domain,
// and when a domain entry names an operation the record does not have.
//
// Like evidence_axes_test.go, both records are decoded into map[string]any
// with an explicit type assertion per field, not into the command's own
// structs. A field that changes JSON type, `domains` that becomes a map,
// `operations` that becomes a list of objects: each is a named failure here
// rather than a zero value counted as owned.
//
// The falsification is tools/falsify/specs/domain-classifier.json: remove
// one product's entries from the classifier — the baremetal pair — and this
// test goes red naming them.

const committedDomains = "../../coverage/domains.json"

// readCommittedRecord decodes a committed coverage record as plain JSON.
func readCommittedRecord(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return decoded
}

// domainOwners reads the classifier and returns operation -> domain, failing
// on any shape it does not recognise. A domain entry naming an operation the
// record does not have is reported here, at the entry, not in the caller.
func domainOwners(t *testing.T, domains map[string]any, evidence map[string]any) map[string]string {
	t.Helper()

	format, ok := domains["format"].(string)
	if !ok {
		t.Fatalf("domains.json: \"format\" is %T, want a string", domains["format"])
	}
	if format != "feint-domains" {
		t.Fatalf("domains.json: format %q, want \"feint-domains\"", format)
	}
	version, ok := domains["version"].(float64)
	if !ok {
		t.Fatalf("domains.json: \"version\" is %T, want a number", domains["version"])
	}
	if version != 1 {
		t.Fatalf("domains.json: version %v, want 1; a new classifier version updates this test with it", version)
	}

	evOps, ok := evidence["operations"].(map[string]any)
	if !ok {
		t.Fatalf("evidence.json: \"operations\" is %T, want an object keyed by operation", evidence["operations"])
	}

	list, ok := domains["domains"].([]any)
	if !ok {
		t.Fatalf("domains.json: \"domains\" is %T, want a list", domains["domains"])
	}
	if len(list) == 0 {
		t.Fatalf("domains.json: no domain is named, so every operation is unowned")
	}

	owned := map[string]string{}
	for _, entry := range list {
		domain, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("domains.json: a domain entry is %T, want an object", entry)
		}
		name, ok := domain["name"].(string)
		if !ok || name == "" {
			t.Fatalf("domains.json: a domain entry has no name")
		}
		if _, ok := domain["issue"]; !ok {
			t.Fatalf("domains.json: domain %q names no parity issue, not even null; a domain with no issue says so", name)
		}
		notes, ok := domain["notes"].(string)
		if !ok || strings.TrimSpace(notes) == "" {
			t.Fatalf("domains.json: domain %q carries no notes; the boundary a domain draws is a judgment, and it is written down", name)
		}
		operations, ok := domain["operations"].([]any)
		if !ok {
			t.Fatalf("domains.json: domain %q: \"operations\" is %T, want a list", name, domain["operations"])
		}
		if len(operations) == 0 {
			t.Errorf("domain %q names no operation; remove the domain or give it an owner", name)
		}
		for _, op := range operations {
			opName, ok := op.(string)
			if !ok {
				t.Fatalf("domains.json: domain %q: an operation is %T, want a string", name, op)
			}
			if prev, dup := owned[opName]; dup {
				t.Errorf("operation %q is claimed by both %q and %q; a domain classifier owns an operation exactly once", opName, prev, name)
			}
			owned[opName] = name
			if _, ok := evOps[opName]; !ok {
				t.Errorf("domain %q names %q, which no coverage artefact reports as implemented by any provider", name, opName)
			}
		}
	}
	return owned
}

// domainList renders the domains with their parity issues, so a red test
// tells the next person where to file the operation instead of leaving them
// guessing which of #409–#414 it belongs to. Read from the classifier
// itself, so the list stays true as domains change. (#874, stephrobert:
// "The same test with the six domains and their issue numbers in the
// message turns a blocker into a two-minute edit.")
func domainList(domains map[string]any) string {
	list, _ := domains["domains"].([]any)
	var lines []string
	for _, entry := range list {
		domain, _ := entry.(map[string]any)
		name, _ := domain["name"].(string)
		if issue, ok := domain["issue"].(float64); ok {
			lines = append(lines, fmt.Sprintf("  - %s (#%.0f)", name, issue))
		} else {
			lines = append(lines, fmt.Sprintf("  - %s (no parity issue yet)", name))
		}
	}
	return strings.Join(lines, "\n")
}

// TestEveryOperationHasExactlyOneDomain fails in both directions: an
// operation in the committed record with no domain, and a domain entry
// naming an operation the record does not have. Either way the operation
// disappears from every parity issue, silently, which is the defect #874
// names.
//
// The unowned direction names the file to edit and the domains with their
// parity issues: a new operation appears when drift:update rewrites the
// record, and the message naming the file to edit is what keeps the two in
// step.
func TestEveryOperationHasExactlyOneDomain(t *testing.T) {
	evidence := readCommittedRecord(t, committedEvidence)
	domains := readCommittedRecord(t, committedDomains)
	owned := domainOwners(t, domains, evidence)

	evOps := evidence["operations"].(map[string]any)
	var unowned []string
	for op := range evOps {
		if _, ok := owned[op]; !ok {
			unowned = append(unowned, op)
		}
	}
	sort.Strings(unowned)
	if len(unowned) > 0 {
		t.Errorf("%d operation(s) match no domain and so are counted in no parity issue:\n%s\n\nFile each one in %s, under the domain that owns it:\n%s\n\nA new operation appears when drift:update rewrites the record; the file to edit is the classifier, not the record.", len(unowned), strings.Join(unowned, "\n"), committedDomains, domainList(domains))
	}
}
