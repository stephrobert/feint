package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The screenshots of the page, on the same freshness rail as everything else
// generated here.
//
// A picture of a screen is a document that rots exactly like a hand-written
// table of coverage numbers, and this repository already learned that once: the
// README claimed 12 routes where 55 were served, and the fix was to generate the
// table and gate it. So the images go under the same command — `feint docs
// --check`, which the pre-commit hook, `mise run docs:check` and
// tools/release/preflight.sh all already run — rather than under a second
// mechanism somebody would have to remember.
//
// What the gate compares, and what it deliberately does not.
//
// It does not compare pixels. It cannot: the page renders wall-clock values by
// design — the time of each call, the age of each resource — so two captures a
// second apart differ, and the same capture taken on a workstation and on a
// runner differs again because font rendering does. A gate demanding byte
// equality would be red permanently, and a gate that is always red is one
// somebody disarms, which is worse than none because it still looks like a
// control.
//
// It compares the page the images were taken from with the page this binary
// serves. That is a digest of the three embedded assets, recorded in the
// manifest beside the images when they were written. Change the stylesheet
// without regenerating and the gate fails; regenerate and it passes. The
// property it holds is "these pictures are of this page", which is the one that
// goes wrong in practice.
//
// What it does not hold is that the pictures show the page *correctly*: that is
// the browser's job, in tools/ui/screenshots.sh, whose assertions run against
// the live document and fail on a renamed node.
//
// TestStaleScreenshotsAreReported fails without this.

// screenshotManifest is docs/assets/ui/manifest.json.
//
// It carries no timestamp, and that is the coverage lesson applied here: the
// drift workflow decides that the upstream surface moved with `git diff --quiet
// -- coverage/`, so a field that changed on every run would open a pull request
// every Monday. A manifest that moved on every run would make this gate noise in
// exactly the same way.
type screenshotManifest struct {
	// Page is the digest of the assets the images were captured from.
	Page string `json:"page"`
	// Images names what was written, so a picture deleted by hand is caught too.
	Images []string `json:"images"`
}

// screenshotState is what the check found.
type screenshotState int

const (
	// screenshotsAbsent means no manifest: a checkout without images, or an
	// installed binary with no docs/ beside it. Not an error, for the same
	// reason a missing coverage/ directory is not one.
	screenshotsAbsent screenshotState = iota
	screenshotsCurrent
	screenshotsStale
)

// checkScreenshots compares the recorded page with the one this binary serves.
func checkScreenshots(path string) (screenshotState, string) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path the caller passed, like every other artefact here
	if errors.Is(err, os.ErrNotExist) {
		return screenshotsAbsent, ""
	}
	if err != nil {
		return screenshotsStale, fmt.Sprintf("%s: %v", path, err)
	}

	var manifest screenshotManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return screenshotsStale, fmt.Sprintf("%s: %v", path, err)
	}

	if manifest.Page != emulator.UIDigest() {
		return screenshotsStale, "the page changed since the screenshots were taken"
	}
	// A manifest naming an image nobody can find is as stale as an old one: the
	// document that embeds it shows a broken link, which is the failure this is
	// supposed to prevent.
	for _, name := range manifest.Images {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), name)); err != nil {
			return screenshotsStale, fmt.Sprintf("the manifest names %s and it is not there", name)
		}
	}
	return screenshotsCurrent, ""
}

// writeScreenshotManifest records the page the images were just taken from.
//
// Called by tools/ui/screenshots.sh once the browser has written them, rather
// than by the browser itself: the digest belongs to the binary that serves the
// page, and asking a Python script to recompute it would be a second
// implementation of one fact.
func writeScreenshotManifest(path string, stdout io.Writer) error {
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	images := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".png" {
			images = append(images, entry.Name())
		}
	}
	if len(images) == 0 {
		return fmt.Errorf("no image in %s: refusing to record a manifest of nothing", dir)
	}

	body, err := json.MarshalIndent(screenshotManifest{Page: emulator.UIDigest(), Images: images}, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o644); err != nil { //nolint:gosec // a committed, non-secret artefact
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(stdout, "recorded %d screenshot(s) of page %s\n", len(images), emulator.UIDigest()[:12])
	return nil
}
