package storetest

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
)

func owned(kind, id, ownerKind, ownerID string) *resource.Resource {
	return &resource.Resource{
		Kind:    kind,
		ID:      id,
		Runtime: map[string]string{"owner-kind": ownerKind, "owner-id": ownerID},
		Attrs:   map[string]any{},
	}
}

// The declaration a pack makes: which key names an owner, and of what kind.
func testOwnership(res *resource.Resource) (kind, id string, ok bool) {
	kind, id = res.Runtime["owner-kind"], res.Runtime["owner-id"]
	return kind, id, id != ""
}

// Both halves, because a control that reports everything passes every attack
// test and makes the barrage useless.
func TestOrphansReportsWhatOutlivedItsOwnerAndNothingElse(t *testing.T) {
	server := &resource.Resource{Kind: "server", ID: "srv-1", Attrs: map[string]any{}}
	live := owned("volume", "vol-live", "server", "srv-1")
	stranded := owned("volume", "vol-stranded", "server", "srv-gone")
	standalone := &resource.Resource{Kind: "volume", ID: "vol-free", Attrs: map[string]any{}}

	found := Orphans([]*resource.Resource{server, live, stranded, standalone}, testOwnership, nil)
	if len(found) != 1 {
		t.Fatalf("want exactly one report, got %d:\n%s", len(found), strings.Join(found, "\n"))
	}
	if !strings.Contains(found[0], "vol-stranded") || !strings.Contains(found[0], "srv-gone") {
		t.Errorf("the report names neither the resource nor the owner it lost: %q", found[0])
	}
}

// The owner's liveness is the pack's word, not the store's. Outscale keeps a
// terminated Vm readable because the Terraform provider polls for that state, so
// the record is present and owns nothing — a volume still linked to it is exactly
// the defect this looks for, and reading presence alone would miss it.
func TestOrphansAsksThePackWhetherTheOwnerIsStillAlive(t *testing.T) {
	dead := &resource.Resource{Kind: "vm", ID: "vm-1", State: "terminated", Attrs: map[string]any{}}
	volume := owned("volume", "vol-1", "vm", "vm-1")
	all := []*resource.Resource{dead, volume}

	if found := Orphans(all, testOwnership, nil); len(found) != 0 {
		t.Fatalf("with no liveness predicate the owner is present, so nothing is stranded:\n%s",
			strings.Join(found, "\n"))
	}

	gone := func(res *resource.Resource) bool { return res.State == "terminated" }
	found := Orphans(all, testOwnership, gone)
	if len(found) != 1 {
		t.Fatalf("a volume linked to a terminated Vm is stranded; got %d report(s):\n%s",
			len(found), strings.Join(found, "\n"))
	}
}

// A pack that declares no ownership gets no findings rather than a panic: the
// same direction of failure as a nil Gone or a nil Shared, and the shape a pack
// keeping its references on the owner legitimately has.
func TestOrphansIsSilentWithoutADeclaration(t *testing.T) {
	stranded := owned("volume", "vol-1", "server", "srv-gone")
	if found := Orphans([]*resource.Resource{stranded}, nil, nil); found != nil {
		t.Errorf("a pack that declares nothing was reported anyway: %v", found)
	}
}
