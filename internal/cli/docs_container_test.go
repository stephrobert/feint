package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A container section can only be rendered against a workflow that pushes the
// image it names. A page telling readers to pull what nothing publishes is the
// 404-on-first-contact failure the install commands were generated to prevent,
// one artefact later.
func TestContainerSectionRefusesAWorkflowThatPushesNothing(t *testing.T) {
	dir := t.TempDir()
	workflow := filepath.Join(dir, "release.yml")
	if err := os.WriteFile(workflow, []byte("jobs:\n  binaries:\n    steps: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := ".." + string(filepath.Separator) + ".."
	_, err := renderContainer(workflow, filepath.Join(root, "go.mod"), filepath.Join(root, changelogPath))
	if err == nil {
		t.Fatal("a workflow that pushes no image rendered a container section anyway")
	}
	if !strings.Contains(err.Error(), "ghcr.io") {
		t.Fatalf("the refusal does not name the missing image reference: %v", err)
	}
}

// The section the repository actually publishes: the image is the module's own,
// the tag is the CHANGELOG's latest release, nothing is mutable, and the
// verification identity is anchored on the release workflow and the tag ref —
// the same anchoring #129 established for the binaries.
func TestContainerSectionNamesTheReleasedVersionAndNothingMutable(t *testing.T) {
	root := ".." + string(filepath.Separator) + ".."
	got, err := renderContainer(
		filepath.Join(root, releaseWorkflow),
		filepath.Join(root, "go.mod"),
		filepath.Join(root, changelogPath),
	)
	if err != nil {
		t.Fatal(err)
	}

	version := latestReleased(filepath.Join(root, changelogPath))
	if version == "" {
		t.Fatal("no released version in the CHANGELOG to check against")
	}
	image := "ghcr.io/" + strings.ToLower(repositorySlug(filepath.Join(root, "go.mod")))
	ref := image + ":v" + version
	if !strings.Contains(got, ref) {
		t.Fatalf("the section does not name %s:\n%s", ref, got)
	}
	if strings.Contains(got, ":latest") {
		t.Fatal("the section carries a mutable tag, which no page here may")
	}
	if !strings.Contains(got, `release\.yml@refs/tags/v`) {
		t.Fatal("the verification identity is not anchored on the release workflow and the tag ref (#129)")
	}
	if !strings.Contains(got, "--vm off") {
		t.Fatal("the section does not say the image is control-plane only, which is its one decision")
	}
}

// The repository's own install page must carry the markers. Every splice here
// is optional by design — a document that never claimed a section is skipped —
// which means deleting the markers silently retires the gate. For this page
// that would leave the image undocumented while CI keeps proving it, so the
// claim "the section is generated" is pinned by this test rather than by the
// splice's good will.
func TestThisRepositorysInstallPageCarriesTheContainerSection(t *testing.T) {
	root := ".." + string(filepath.Separator) + ".."
	body, err := os.ReadFile(filepath.Join(root, "docs", "install.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), containerStartMarker) {
		t.Fatalf("docs/install.md no longer carries %s; the image would be published undocumented", containerStartMarker)
	}
}
