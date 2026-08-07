package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// A screenshot of a page that has changed is reported as stale.
//
// This is the whole gate. The images in docs/ cannot be checked by comparing
// pixels — the page renders wall-clock values, so two captures a second apart
// differ, and font rendering differs again between a workstation and a runner —
// so what is compared is the page they were taken from against the page this
// binary serves. Without it, a change to the stylesheet ships with pictures of
// the previous design and nothing says so.
func TestStaleScreenshotsAreReported(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(filepath.Join(dir, "page.png"), []byte("not really a png"), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}

	// Written by the same function the harness calls, so the fixture is what the
	// producer produces rather than what a test author assumed it looks like.
	if err := writeScreenshotManifest(manifest, io.Discard); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}

	// The accepting half first: fresh images are not reported. A check that
	// called everything stale would pass every failure test and fail every run.
	if state, why := checkScreenshots(manifest); state != screenshotsCurrent {
		t.Fatalf("fresh screenshots were reported as %v: %s", state, why)
	}

	// Now the page changes under them.
	var recorded screenshotManifest
	raw, err := os.ReadFile(manifest) //nolint:gosec // a path this test built
	if err != nil {
		t.Fatalf("read back the manifest: %v", err)
	}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatalf("decode the manifest: %v", err)
	}
	if recorded.Page != emulator.UIDigest() {
		t.Fatalf("the manifest recorded %q, the binary serves %q", recorded.Page, emulator.UIDigest())
	}
	recorded.Page = "0000000000000000000000000000000000000000000000000000000000000000"
	changed, err := json.Marshal(recorded)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if err := os.WriteFile(manifest, changed, 0o600); err != nil {
		t.Fatalf("rewrite the manifest: %v", err)
	}
	if state, _ := checkScreenshots(manifest); state != screenshotsStale {
		t.Error("the screenshots were not reported stale after the page changed")
	}

	// An image the manifest names and nobody can find is stale too: the document
	// embedding it shows a broken link, which is the failure this prevents.
	if err := os.WriteFile(manifest, raw, 0o600); err != nil {
		t.Fatalf("restore the manifest: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "page.png")); err != nil {
		t.Fatalf("remove the image: %v", err)
	}
	if state, _ := checkScreenshots(manifest); state != screenshotsStale {
		t.Error("a manifest naming a missing image was not reported stale")
	}
}

// No manifest is not a failure.
//
// A binary installed with `go install` has no docs/ directory beside it, and
// `feint docs` must still regenerate a user's README. Same terms as the coverage
// artefacts, which are absent for the same people.
func TestAbsentScreenshotsAreNotAnError(t *testing.T) {
	state, why := checkScreenshots(filepath.Join(t.TempDir(), "manifest.json"))
	if state != screenshotsAbsent {
		t.Errorf("a missing manifest was reported as %v: %s", state, why)
	}
}

// A manifest of nothing is refused rather than written.
//
// Recording an empty list would make the gate pass forever: every image it names
// exists, because it names none.
func TestAManifestOfNoImagesIsRefused(t *testing.T) {
	if err := writeScreenshotManifest(filepath.Join(t.TempDir(), "manifest.json"), io.Discard); err == nil {
		t.Error("a manifest was written for a directory holding no image")
	}
}
