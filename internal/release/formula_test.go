package release

import (
	"strings"
	"testing"
)

// The real v0.9.0 list, fetched from the release on 2026-08-20 and pasted here
// as the fixture every case below starts from. A synthetic one would let the
// renderer be right about a shape no release has.
const publishedChecksums = `c1ac05141df079d142a43b9f8d212a84e9c52661af98cb92753860c05aeaace8  feint-darwin-amd64
af8446dfaff8b657637bfbf295fe8ea58bcecb7fe3ab4fea33fa2d3790ed053b  feint-darwin-arm64
fa7b9c099239a5e66fadba7312722f84e5ba34ff606540101516a7170965e28a  feint-linux-amd64
de1d7c093b7755afbfbf053cb5dadf832f3b713d67c00316c39d2023732c34cd  feint-linux-arm64
`

func spec() Spec {
	return Spec{
		Slug:      "stephrobert/feint",
		Version:   "0.9.0",
		License:   "Apache-2.0",
		Checksums: publishedChecksums,
	}
}

// Every binary the release published is installable through the formula, and
// with the digest the release signed.
//
// The failure this refuses is the quiet one: a formula that omits a platform is
// a `brew install` that fails on that machine with a message about the formula
// rather than about the omission, and nothing in the tap says a platform is
// missing. So the assertion is over the checksums list, not over a list written
// here — a fifth binary added to a release makes this fail until the formula
// carries it.
func TestAFormulaCoversEveryBinaryTheChecksumsListPublishes(t *testing.T) {
	out, err := Formula(spec())
	if err != nil {
		t.Fatalf("the published checksums must render: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(publishedChecksums), "\n") {
		fields := strings.Fields(line)
		asset, digest := fields[1], fields[0]
		if !strings.Contains(out, "/v0.9.0/"+asset+"\"") {
			t.Errorf("the formula never downloads %s: brew install fails on that platform "+
				"and says nothing about why", asset)
		}
		if !strings.Contains(out, "sha256 \""+digest+"\"") {
			t.Errorf("the formula does not carry the released digest of %s, so what brew "+
				"verifies is not what the release signed", asset)
		}
	}
	// A formula that rebuilds locally verifies nothing the release signed.
	if strings.Contains(out, "system \"go\"") || strings.Contains(out, "depends_on \"go\"") {
		t.Error("the formula builds from source: the bytes brew installs would then be " +
			"outside the checksums and the provenance")
	}
}

// Every entry of the list is a platform the formula installs, or the formula is
// refused.
//
// Two failures share this one guard, which is why it is written as a closed set
// rather than as a filter. checksums.txt is pulled over the network by
// tools/release/tap.sh, so a name in it is an input and not a fact: `"` and a
// newline close the Ruby literal, and `../` points the download at another
// release. And an entry that is simply not a binary — an SBOM added to the
// signed list one day — must stop the formula rather than be dropped from it,
// because dropping is how a formula ends up short by a platform with nothing
// saying so.
func TestAnEntryTheFormulaHasNoPlatformForIsRefused(t *testing.T) {
	for _, name := range []string{
		`feint-darwin-arm64"` + "\n" + `  system "id`,
		"feint-../../../other/feint-linux-amd64",
		"feint-darwin-arm64;id",
		"feint-darwin-arm64.bak",
		"feint-freebsd-amd64", // published, and Homebrew has no block for it
		"sbom.cdx.json",       // an artefact the list could legitimately gain
		"feint-darwin",
	} {
		s := spec()
		s.Checksums = strings.Replace(publishedChecksums, "feint-darwin-arm64", name, 1)
		if out, err := Formula(s); err == nil {
			t.Errorf("%q was accepted into the formula:\n%s", name, out)
		}
	}
}

// A line that is not `<digest>  <name>` is refused rather than read for the two
// fields it happens to start with.
//
// The case is a line with a trailing third field: everything before it is a
// perfectly good entry, so a parser that takes fields[0] and fields[1] accepts
// it and silently ignores whatever the rest was.
func TestAChecksumsLineThatIsNotTwoFieldsIsRefused(t *testing.T) {
	s := spec()
	s.Checksums = strings.Replace(publishedChecksums,
		"  feint-darwin-arm64", "  feint-darwin-arm64  (recomputed)", 1)
	if out, err := Formula(s); err == nil {
		t.Errorf("a three-field checksums line was read as an entry anyway:\n%s", out)
	}
}

// A digest that is not a SHA-256 is refused rather than written into the
// formula, in both directions: too short, and not hexadecimal.
func TestADigestThatIsNotOneIsRefused(t *testing.T) {
	for _, digest := range []string{
		"af8446dfaff8b657637bfbf295fe8ea58bcecb7fe3ab4fea33fa2d3790ed053",  // 63
		"AF8446DFAFF8B657637BFBF295FE8EA58BCECB7FE3AB4FEA33FA2D3790ED053B", // brew wants lowercase
		"not-a-digest",
		"",
	} {
		s := spec()
		s.Checksums = strings.Replace(publishedChecksums,
			"af8446dfaff8b657637bfbf295fe8ea58bcecb7fe3ab4fea33fa2d3790ed053b", digest, 1)
		if _, err := Formula(s); err == nil {
			t.Errorf("digest %q was written into the formula", digest)
		}
	}
}

// Two digests for one asset is a list that is not one release's, and picking
// either would be picking one at random.
func TestTwoDigestsForOneAssetAreRefused(t *testing.T) {
	s := spec()
	s.Checksums = publishedChecksums +
		"0000000000000000000000000000000000000000000000000000000000000000  feint-darwin-arm64\n"
	if _, err := Formula(s); err == nil {
		t.Fatal("a contradictory checksums list rendered a formula")
	}

	// The same line twice is not a contradiction, and must not be treated as
	// one: sha256sum can legitimately be run twice into the same file.
	s.Checksums = publishedChecksums +
		"af8446dfaff8b657637bfbf295fe8ea58bcecb7fe3ab4fea33fa2d3790ed053b  feint-darwin-arm64\n"
	out, err := Formula(s)
	if err != nil {
		t.Fatalf("a repeated identical line is not a contradiction: %v", err)
	}
	if strings.Count(out, "/feint-darwin-arm64\"") != 1 {
		t.Errorf("the repeated asset was rendered twice:\n%s", out)
	}
}

// An empty list is a measurement that did not happen, and it must not render an
// empty formula.
func TestAnEmptyChecksumsListIsRefused(t *testing.T) {
	s := spec()
	s.Checksums = "\n  \n"
	if _, err := Formula(s); err == nil {
		t.Fatal("an empty checksums list rendered a formula")
	}
}

// The slug and the version are the two things interpolated into every URL, and
// neither is taken on trust.
func TestTheSlugAndTheVersionAreCheckedBeforeTheyBecomeAURL(t *testing.T) {
	for _, slug := range []string{"", "feint", "stephrobert/feint/extra", "steph robert/feint", "../feint"} {
		s := spec()
		s.Slug = slug
		if _, err := Formula(s); err == nil {
			t.Errorf("slug %q became a download URL", slug)
		}
	}
	for _, version := range []string{"", "v0.9.0", "0.9", "latest", "0.9.0-rc1"} {
		s := spec()
		s.Version = version
		if _, err := Formula(s); err == nil {
			t.Errorf("version %q became a download URL", version)
		}
	}
}

// Homebrew requires a licence and refuses to audit a formula without one;
// guessing it here would publish a licence claim nothing checks.
func TestAFormulaWithoutALicenceIsRefused(t *testing.T) {
	s := spec()
	s.License = "  "
	if _, err := Formula(s); err == nil {
		t.Fatal("a formula rendered with no licence")
	}
}

// The class name Homebrew requires is derived from the repository, and the
// formula it installs is named after it too.
func TestTheFormulaIsNamedAfterTheRepository(t *testing.T) {
	out, err := Formula(spec())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "class Feint < Formula") {
		t.Errorf("brew requires the class of Formula/feint.rb to be Feint:\n%s", out)
	}
	if !strings.Contains(out, `bin.install published.first => "feint"`) {
		t.Errorf("the installed command is not called feint:\n%s", out)
	}
	// One `def install`, outside the platform blocks: `brew audit` reports four
	// problems for a formula that defines a method inside each of them.
	if n := strings.Count(out, "def install"); n != 1 {
		t.Errorf("the formula defines install %d times; brew audit refuses more than one at "+
			"top level:\n%s", n, out)
	}
	if got := className("setup-feint"); got != "SetupFeint" {
		t.Errorf("className(setup-feint) = %q, want SetupFeint", got)
	}
}

// The formula asserts the version the binary reports, not a substring of it.
//
// Measured on 2026-08-20 against the published v0.9.0 linux binary: `feint
// version` prints `v0.9.0`, because the release builds with
// `-X ...cli.Version=${GITHUB_REF_NAME}` and GITHUB_REF_NAME is the tag.
func TestTheFormulaTestAssertsTheTagTheBinaryPrints(t *testing.T) {
	out, err := Formula(spec())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `assert_match "v#{version}"`) {
		t.Errorf("the formula's test does not assert the `v` the binary prints:\n%s", out)
	}
}

// Rendering the same release twice gives the same bytes, which is what lets
// tools/release/tap.sh diff the tap against a fresh derivation.
func TestTheSameReleaseRendersTheSameBytes(t *testing.T) {
	first, err := Formula(spec())
	if err != nil {
		t.Fatal(err)
	}
	shuffled := spec()
	lines := strings.Split(strings.TrimSpace(publishedChecksums), "\n")
	shuffled.Checksums = strings.Join([]string{lines[3], lines[1], lines[0], lines[2]}, "\n") + "\n"
	second, err := Formula(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("the order of checksums.txt changed the formula:\n%s\n---\n%s", first, second)
	}
}

func TestTheSlugComesFromTheModuleLine(t *testing.T) {
	if got := SlugFromModule("module github.com/stephrobert/feint\n\ngo 1.26\n"); got != "stephrobert/feint" {
		t.Errorf("SlugFromModule = %q", got)
	}
	if got := SlugFromModule("module example.test/feint\n"); got != "" {
		t.Errorf("a module outside GitHub named a slug: %q", got)
	}
}

func TestTheLicenceIsReadRatherThanGuessed(t *testing.T) {
	id, err := LicenseID("                                 Apache License\n   Version 2.0, January 2004\n")
	if err != nil || id != "Apache-2.0" {
		t.Errorf("LicenseID = %q, %v", id, err)
	}
	if _, err := LicenseID("All rights reserved.\n"); err == nil {
		t.Error("an unrecognised licence was given an SPDX identifier anyway")
	}
}
