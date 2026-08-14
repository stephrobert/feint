package drift

import (
	"fmt"
	"sort"

	"github.com/stephrobert/feint/internal/contract"
)

// A provider that publishes an OpenAPI document needs no SDK reader: the
// document already lists every operation it has, and the extraction under
// contracts/ has already turned it into something Go can read.
//
// One artefact, two mechanisms. The contract answers "is this field real"; the
// same file answers "did the surface move", which is what the baseline compares
// against. Exoscale is the case that made this obvious: it had neither, and its
// five routes declared operation names nothing could check — the exact state
// Outscale was in when a scan found all seven of its names wrong.

// ScanContract returns the upstream surface a contract describes.
//
// Operations are named the way a route must declare them: the provider key, the
// document's path prefix, then the operationId the document itself gives. That
// last part is not translated. Exoscale's identifiers are kebab-case
// (list-instances) and their Go SDK renames them; picking the renamed form would
// mean this project performing a translation nobody could check, which is how
// hand-written names go wrong.
func ScanContract(doc *contract.Doc) ([]Operation, error) {
	if len(doc.Operations) == 0 {
		return nil, fmt.Errorf("contract for %s lists no operation", doc.Provider)
	}

	product := doc.Provider + doc.PathPrefix
	ops := make([]Operation, 0, len(doc.Operations))
	for name, op := range doc.Operations {
		ops = append(ops, Operation{
			Name:    product + "." + name,
			Product: doc.Provider,
			Version: doc.APIVersion,
			// The document's own grouping, verbatim. Empty for an operation the
			// document leaves untagged, and left empty here too: the fallback to
			// Product is one decision, taken where the entries are read.
			Group: op.Group,
		})
	}

	sort.Slice(ops, func(i, j int) bool { return ops[i].Name < ops[j].Name })
	return ops, nil
}

// Regroup fills each operation's Group from the grouping the provider's API
// description declares, joining on the operation name.
//
// This exists for the provider whose surface is scanned from its Go SDK but
// whose grouping only its OpenAPI document carries: oapi-codegen flattens all
// 236 of Outscale's operations onto one Client and drops their tags, which is
// how the coverage table came to show one `osc` row for a surface upstream
// files under 50 tags. The SDK stays the authority on which operations exist —
// its method names are what routes declare — and the document only says where
// each one is filed.
//
// An operation the document does not know keeps its empty Group and falls back
// to Product downstream: Outscale's oks service publishes no document here, and
// inventing a grouping for it would be the exact translation this package
// refuses to perform. Asserted by TestRegroupTakesGroupsFromTheContract, which
// fails if the join stops filling groups or starts inventing them.
func Regroup(ops []Operation, doc *contract.Doc) {
	for i := range ops {
		if op, _, ok := doc.OperationFor(ops[i].Name); ok && op.Group != "" {
			ops[i].Group = op.Group
		}
	}
}
