package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The banner's figures come from the artefacts, never from prose (#177).
//
// This is the whole reason it is generated. A hand-written banner restates a
// number, and a restated number disagrees with its source by the next release —
// which is how the page ends up telling a user something the repository can
// disprove. The issue names it as the thing that must not happen.
func TestTheBannerCountsComeFromTheArtefacts(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("coverage/evidence.json", `{"operations":{
		"a/API.One":   {"driven": true},
		"a/API.Two":   {"driven": true},
		"a/API.Three": {"driven": false}
	}}`)
	write("docs/limits.md", "# Limits\n\n## One\n\n## Two\n\n## Three\n\n## Four\n")
	write("README.md", "# feint\n\n<!-- safety:start -->\n<!-- safety:end -->\n\nrest\n")

	if err := writeSplicedSafety(root); err != nil {
		t.Fatalf("write the banner: %v", err)
	}
	rendered, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(rendered)

	// Two driven of three mounted, one unknown, four limit sections — every one
	// of those read rather than typed.
	for _, want := range []string{"2 of the 3 mounted", "1 operations are mounted", "The 4 sections"} {
		if !strings.Contains(page, want) {
			t.Errorf("the banner does not carry %q:\n%s", want, page)
		}
	}

	// And the check follows the artefact rather than the page: moving the
	// evidence must make the page stale.
	write("coverage/evidence.json", `{"operations":{
		"a/API.One":   {"driven": true},
		"a/API.Two":   {"driven": false},
		"a/API.Three": {"driven": false}
	}}`)
	stale, err := spliceSafety(root)
	if err != nil {
		t.Fatalf("check the banner: %v", err)
	}
	if !stale {
		t.Error("the evidence moved and the banner was reported up to date")
	}
}

// The banner splits "unknown" into what is explained and what is not (#174).
//
// One number for two facts is what the issue measured as the debt: an operation
// nobody has written a scenario for and one no client path exists for read
// identically in a total, and only the first is work anybody can do. The split
// is read from the routes the binary mounts joined onto the record — never
// typed — so an operation that gains a client moves between the two figures on
// its own.
func TestTheBannerSeparatesExplainedFromUnknown(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// One real operation that declares why no client reaches it, one real
	// operation that does not, and one driven. Real names on purpose: the count
	// is a join against the mounted routes, so invented ones would measure the
	// join rather than the rule.
	write("coverage/evidence.json", `{"operations":{
		"ipam/v1/API.MoveIP":       {"driven": false},
		"ipam/v1/API.BookIP":       {"driven": false},
		"instance/v1/API.ListServers": {"driven": true}
	}}`)
	write("docs/limits.md", "# Limits\n\n## One\n")
	write("README.md", "# feint\n\n<!-- safety:start -->\n<!-- safety:end -->\n\nrest\n")

	if err := writeSplicedSafety(root); err != nil {
		t.Fatalf("write the banner: %v", err)
	}
	rendered, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(rendered)

	if !strings.Contains(page, "2 operations are mounted and have never been driven") {
		t.Errorf("the banner lost the total it always printed:\n%s", page)
	}
	// MoveIP declares its reason; BookIP does not, and is driven by the fixture
	// in the real record — here it stands for the operation still waiting for a
	// scenario.
	if !strings.Contains(page, "1 of them state why") {
		t.Errorf("the banner does not separate the explained ones:\n%s", page)
	}
	if !strings.Contains(page, "the other 1 are waiting") {
		t.Errorf("the banner does not say what is left to do:\n%s", page)
	}

	// And when nothing is left waiting, it must not describe a remainder that
	// does not exist. The first version printed "17 … 17 of them … the rest are
	// waiting", which is a sentence about zero operations written as if they
	// were there.
	write("coverage/evidence.json", `{"operations":{
		"ipam/v1/API.MoveIP":          {"driven": false},
		"instance/v1/API.ListServers": {"driven": true}
	}}`)
	if err := writeSplicedSafety(root); err != nil {
		t.Fatalf("rewrite the banner: %v", err)
	}
	rendered, err = os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	page = string(rendered)
	if strings.Contains(page, "are waiting") {
		t.Errorf("every undriven operation is explained and the banner still promises a remainder:\n%s", page)
	}
	if !strings.Contains(page, "Every one of them states why") {
		t.Errorf("the banner does not say that all of them are accounted for:\n%s", page)
	}
}

// An artefact that cannot be read is an error, never a zero.
//
// A banner announcing "0 of 0 operations are proven" because a file moved would
// be worse than no banner at all, and it is exactly the failure a generated
// block exists to remove: a claim nobody wrote and nobody can trace.
func TestTheBannerRefusesToCountNothing(t *testing.T) {
	root := t.TempDir()
	// A document that claims the banner, because the strictness only applies to
	// one that does: a set carrying no marker never asked for a banner, and
	// failing it for a missing artefact turned every fixture's drift verdict
	// into an error one.
	if err := os.WriteFile(filepath.Join(root, "README.md"),
		[]byte("# feint\n\n<!-- safety:start -->\n<!-- safety:end -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := spliceSafety(root); err == nil {
		t.Error("a missing evidence record was reported as a banner with nothing to say")
	}

	if err := os.MkdirAll(filepath.Join(root, "coverage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "coverage", "evidence.json"),
		[]byte(`{"operations":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := spliceSafety(root); err == nil {
		t.Error("an empty evidence record was accepted; the banner would claim nothing is proven")
	}
}

// Every link the banner makes resolves to a file that exists.
//
// A claim that resolves to nothing is a marketing sentence, which is the one
// thing this page must never become. The anchors are listed once and read by
// both the rendering and this check, so the two cannot agree by accident.
func TestEveryBannerAnchorResolves(t *testing.T) {
	root := repoRoot(t)
	for _, anchor := range safetyAnchors() {
		if _, err := os.Stat(filepath.Join(root, anchor)); err != nil {
			t.Errorf("the banner points at %s and it does not exist: %v", anchor, err)
		}
	}

	// And the rendered page really names them, or the list above would be a
	// declaration nobody honours.
	page, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Skipf("no README to read: %v", err)
	}
	banner := between(string(page), safetyStartMarker, safetyEndMarker)
	if banner == "" {
		t.Fatal("README.md carries no safety banner")
	}
	for _, anchor := range safetyAnchors() {
		if !strings.Contains(banner, anchor) {
			t.Errorf("the banner does not link %s, which safetyAnchors promises", anchor)
		}
	}
}

// between answers what lies between two markers, searching for the second one
// *after* the first.
//
// Searching the whole document for the end marker was the first version, and it
// returned an empty section every time the end marker also occurred earlier —
// which for a heading like "\n## " it always does. The section then read as
// absent and the test blamed the document.
func between(s, start, end string) string {
	from := strings.Index(s, start)
	if from < 0 {
		return ""
	}
	rest := s[from+len(start):]
	to := strings.Index(rest, end)
	if to < 0 {
		return rest
	}
	return rest[:to]
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("not running from inside the repository")
	return ""
}
