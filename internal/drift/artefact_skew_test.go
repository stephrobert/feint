package drift

import (
	"strings"
	"testing"
)

// The fixture is the #298 shape in miniature: one product, one served
// operation, one declined operation, one untriaged one.
func freshArtefact() CoverageFile {
	return CoverageFile{
		Provider: "example",
		Products: []ProductView{{Product: "compute"}},
		Entries: []Entry{
			{Operation: "compute/v1/API.CreateThing", Product: "compute", Status: StatusImplemented},
			{Operation: "compute/v1/API.DeleteThing", Product: "compute", Status: StatusDeclined, Reason: "the current sentence"},
			{Operation: "compute/v1/API.PatchThing", Product: "compute", Status: StatusUnknown},
		},
	}
}

func packDeclarations() (served []string, declined map[string]string) {
	return []string{"compute/v1/API.CreateThing"},
		map[string]string{"compute/v1/API.DeleteThing": "the current sentence"}
}

func TestAnArtefactThatMatchesItsPackReportsNoSkew(t *testing.T) {
	served, declined := packDeclarations()
	if skew := ArtefactSkew(freshArtefact(), served, declined); len(skew) != 0 {
		t.Fatalf("agreement reported as skew: %v", skew)
	}
}

// The measured defect: the pack rewrote its reason, the artefact keeps printing
// the old sentence. tools/falsify/specs/artefact-reasons.json neutralises the
// reason comparison and requires this test to fail.
func TestAStaleReasonIsSkew(t *testing.T) {
	served, declined := packDeclarations()
	declined["compute/v1/API.DeleteThing"] = "the corrected sentence"
	skew := ArtefactSkew(freshArtefact(), served, declined)
	if len(skew) != 1 {
		t.Fatalf("one stale reason, %d line(s): %v", len(skew), skew)
	}
	for _, want := range []string{"compute/v1/API.DeleteThing", "the current sentence", "the corrected sentence"} {
		if !strings.Contains(skew[0], want) {
			t.Errorf("the line does not carry %q: %s", want, skew[0])
		}
	}
}

func TestAFlippedStatusIsSkew(t *testing.T) {
	served, declined := packDeclarations()
	// The pack now serves what the artefact still records as declined.
	served = append(served, "compute/v1/API.DeleteThing")
	if skew := ArtefactSkew(freshArtefact(), served, declined); len(skew) != 1 || !strings.Contains(skew[0], "the pack serves it") {
		t.Fatalf("declined->implemented flip not reported as one line: %v", skew)
	}
}

func TestATriagedUnknownIsSkew(t *testing.T) {
	served, declined := packDeclarations()
	// Somebody triaged the untriaged operation and regenerated nothing.
	declined["compute/v1/API.PatchThing"] = "a decision the artefact never recorded"
	if skew := ArtefactSkew(freshArtefact(), served, declined); len(skew) != 1 || !strings.Contains(skew[0], "the pack declines it") {
		t.Fatalf("unknown->declined flip not reported as one line: %v", skew)
	}
}

func TestAWithdrawnDeclarationIsSkew(t *testing.T) {
	served, declined := packDeclarations()
	// The pack neither serves nor declines what the artefact calls declined.
	delete(declined, "compute/v1/API.DeleteThing")
	if skew := ArtefactSkew(freshArtefact(), served, declined); len(skew) != 1 || !strings.Contains(skew[0], "neither serves nor declines") {
		t.Fatalf("withdrawn decline not reported as one line: %v", skew)
	}
}

func TestADeclineTheArtefactNeverListedIsSkew(t *testing.T) {
	served, declined := packDeclarations()
	declined["compute/v1/API.RotateThing"] = "a decision on an operation the artefact has never seen"
	if skew := ArtefactSkew(freshArtefact(), served, declined); len(skew) != 1 || !strings.Contains(skew[0], "the artefact has no entry") {
		t.Fatalf("unrecorded decline not reported as one line: %v", skew)
	}
}

// The artefact is the record of a scoped scan (--products), so a pack
// declaration outside that scope is not the artefact's to carry, and reporting
// it would make the gate fail on something drift:update cannot repair.
func TestADeclarationOutsideTheArtefactsProductsIsNotSkew(t *testing.T) {
	served, declined := packDeclarations()
	served = append(served, "storage/v1/API.CreateBucket")
	declined["storage/v1/API.DeleteBucket"] = "a decision on a product the scan does not cover"
	if skew := ArtefactSkew(freshArtefact(), served, declined); len(skew) != 0 {
		t.Fatalf("out-of-scope declarations reported as skew: %v", skew)
	}
}

// A served operation also named by a stale decline is implemented, exactly as
// Compare ranks it: judging it declined here would report a skew that
// regenerating the artefact could never remove.
func TestPrecedenceFollowsCompare(t *testing.T) {
	served, declined := packDeclarations()
	declined["compute/v1/API.CreateThing"] = "a stale decline on a served operation"
	if skew := ArtefactSkew(freshArtefact(), served, declined); len(skew) != 0 {
		t.Fatalf("served-and-declined reported as skew where Compare says implemented: %v", skew)
	}
}
