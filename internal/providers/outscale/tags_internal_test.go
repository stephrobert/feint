package outscale

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The defect #99 reported was not a wrong line, it was a table nobody had to
// keep in step. `taggable` held four prefixes, written when the pack served
// four kinds; 0.6.0 added ten resources and none of them reached it, so
// CreateTags answered "the resource igw-… does not exist" about a resource the
// emulator had just created.
//
// Adding the ten missing rows fixes today and nothing else: the eleventh
// resource would do it again. So the source of truth becomes the identifiers
// the pack actually mints, read from the source, and every one of them has to
// be triaged — taggable, or refused with a reason, the same discipline
// Declined() applies to operations.
func mintedPrefixes(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the pack: %v", err)
	}

	found := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || fn.Name != "newID" || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			prefix, err := strconv.Unquote(lit.Value)
			if err != nil || prefix == "" {
				return true
			}
			found[prefix+"-"] = name
			return true
		})
	}

	// A scan that finds nothing passes every table check and measures nothing.
	// The pack minted sixteen prefixes when this was written; the floor is set
	// well below that so it catches a broken scan without failing on a
	// legitimate removal.
	if len(found) < 10 {
		t.Fatalf("only %d identifier prefixes found across %d entries: the scan is "+
			"broken, not the table", len(found), len(entries))
	}
	return found
}

// TestEveryIdentifierPrefixIsTriagedForTagging is what #99 was missing. It
// fails when a resource is added with a new identifier prefix and neither
// table mentions it, which is exactly how ten prefixes went unnoticed.
func TestEveryIdentifierPrefixIsTriagedForTagging(t *testing.T) {
	canTag := map[string]bool{}
	for _, entry := range taggable {
		canTag[entry.prefix] = true
	}

	for prefix, file := range mintedPrefixes(t) {
		if canTag[prefix] || notTaggable[prefix] != "" {
			continue
		}
		t.Errorf("%s mints %q and no table says whether it can be tagged.\n"+
			"Add it to taggable with its TagResourceType from the SDK enum "+
			"(osc-sdk-go/pkg/osc/client.gen.go), or to notTaggable with the reason "+
			"its schema declares no Tags. Leaving it out is what made CreateTags "+
			"answer that a resource the emulator serves does not exist (#99).",
			file, prefix)
	}
}

// A prefix in both tables, or refused with a reason that is not one, would be a
// triage that says nothing — the placeholder-reason failure the Declined() gate
// already refuses at start-up.
func TestTheTwoTaggingTablesDoNotOverlapOrHedge(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range taggable {
		if seen[entry.prefix] {
			t.Errorf("%q appears twice in taggable", entry.prefix)
		}
		seen[entry.prefix] = true
	}
	for prefix, reason := range notTaggable {
		if seen[prefix] {
			t.Errorf("%q is in both tables: it cannot be taggable and not", prefix)
		}
		if len(strings.Fields(reason)) < 5 {
			t.Errorf("%q is refused with %q, which is not a reason", prefix, reason)
		}
	}
}

// TestEveryTaggableResourceTypeIsOneTheSDKDeclares pins the values to the enum
// rather than to what reads well. Two of the four this table first carried were
// invented — "net" for "vpc" and "vm" for "instance" — and no contract could
// see it, because their OpenAPI declares ResourceType as a bare string.
func TestEveryTaggableResourceTypeIsOneTheSDKDeclares(t *testing.T) {
	// TagResourceType, osc-sdk-go/pkg/osc/client.gen.go, whose source is the
	// enum block of patch.yaml. Twenty values, and none naming an internet
	// service — which is why one row of taggable carries no type at all.
	declared := map[string]bool{
		"customer-gateway": true, "dhcpoptions": true, "flexible-gpu": true,
		"image": true, "instance": true, "keypair": true, "natgateway": true,
		"network-interface": true, "public-ip": true, "route-table": true,
		"security-group": true, "snapshot": true, "subnet": true, "task": true,
		"virtual-private-gateway": true, "volume": true, "vpc": true,
		"vpc-endpoint": true, "vpc-peering-connection": true, "vpn-connection": true,
	}

	for _, entry := range taggable {
		if entry.resourceType == "" {
			continue // declared upstream with Tags and no name for its type
		}
		if !declared[entry.resourceType] {
			t.Errorf("%s publishes ResourceType %q, which the SDK's TagResourceType "+
				"enum does not declare: read the enum, do not invent a name",
				entry.prefix, entry.resourceType)
		}
	}
}
