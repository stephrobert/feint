package drift

import (
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/contract"
)

// Two inventories are committed here and they can disagree about which
// operations exist (#622).
//
// The surface scan reads the provider's Go SDK, and it is right about its own
// source. The contract reads the provider's published document. For Exoscale
// those are one file, and for Outscale the SDK is generated from the document,
// so the two agree by construction. Scaleway is the case where they do not: the
// portal publishes `PUT /instance/v1/zones/{zone}/volumes/{id}` and the Go SDK
// never wrapped it, so the operation is outside `total` — and `unknown: 0` is
// then exact over a set that does not contain it.
//
// Nothing is wrong at runtime; an unserved route answers 501 and points at the
// route list. What was wrong is the triage: by this repository's own rule, *not
// done yet* and *out of scope* are different answers, and an operation nobody was
// ever offered has given neither. This is the second witness that offers them.

// ContractOnly returns the operations a document declares that the scanned
// surface does not contain, in the document's own spelling.
//
// The comparison folds the two naming conventions rather than trusting either:
// the SDK writes `account/v3/ContractAPI.CheckContractSignature` where the
// document writes `account/v3.CreateProject`, and `instance/v1/API.CreateIP`
// where the document writes `instance/v1.CreateIp`. Segments ending in `API` are
// dropped and the method is folded to lower case, which is what makes
// `CreateIp` and `CreateIP` one operation instead of two.
//
// A document that names no product at all — Outscale writes `ReadVms`, Exoscale
// `list-instances` — matches on the method alone. That escape is deliberate and
// it only ever merges: an operation this returns is one no reading of the names
// could reconcile, so the count is a **lower bound** and never an invention. A
// gate that cried wolf on 42 naming differences would be switched off in a week,
// and the six that survive the folding are worth more than forty-two that do not.
func ContractOnly(upstream []Operation, doc *contract.Doc) []string {
	if doc == nil || len(doc.Operations) == 0 {
		return nil
	}

	scoped := make(map[string]bool, len(upstream))
	methods := make(map[string]bool, len(upstream))
	for _, op := range upstream {
		scope, method := foldOperation(op.Name)
		scoped[scope+"."+method] = true
		methods[method] = true
	}

	var only []string
	for name := range doc.Operations {
		scope, method := foldOperation(name)
		if scoped[scope+"."+method] || methods[method] {
			continue
		}
		only = append(only, name)
	}
	sort.Strings(only)
	return only
}

// foldOperation splits a name into the product scope it carries and the method,
// folded so the SDK's spelling and the document's meet.
func foldOperation(name string) (scope, method string) {
	// The last dot, not the first: a Scaleway name carries one after the version
	// (`instance/v1.CreateIp`) and an Outscale document name carries none at all.
	method = name
	if i := strings.LastIndex(name, "."); i >= 0 {
		scope, method = name[:i], name[i+1:]
	}
	kept := make([]string, 0, 3)
	for _, segment := range strings.Split(scope, "/") {
		if segment != "" && !strings.HasSuffix(segment, "API") {
			kept = append(kept, segment)
		}
	}
	return strings.Join(kept, "/"), strings.ToLower(method)
}
