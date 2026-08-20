package release

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The Homebrew formula a release is installed by, derived rather than written.
//
// `brew install stephrobert/feint/feint` needs a formula, a formula needs a
// URL and a SHA-256 per platform, and those two facts only exist once a release
// is published. Whoever produces them is the decision inside #321, and this
// file is one half of the answer.
//
// The other half is what it is not: nothing here writes to another repository.
// A release workflow that pushes the formula to the tap would need a
// cross-repository token this repository does not hold, and the two precedents
// are already written down — .github/workflows/workflow-security.yml refused it
// for the Marketplace mirror ("a gate wired to a secret that does not exist is
// the drift.yml defect all over again"), and docs_release.go refused it for the
// generated pages ("a gate that refuses is safe; a gate that repairs the
// repository is a second way in"). So the tap is updated by hand, and
// tools/release/tap.sh re-derives the formula and exits 2 while the two differ.
//
// What makes the hand-update safe is that the hand never writes the bytes. The
// input is the release's own `checksums.txt` — the file cosign signs, and the
// only file whose digests are the release's rather than somebody's transcription
// of them. Feed it here and the formula falls out; feed it anywhere else and the
// digests are a claim.
//
// Every refusal below exists because the checksums list is fetched over the
// network at gate time, which makes it an input and not a fact: "un état
// restauré est une entrée non fiable" applies to a file pulled from a release
// exactly as it applies to a snapshot. A crafted list must not be able to make
// this write a URL that points elsewhere or a Ruby literal that closes early.
//
// Measured end to end on 2026-08-20, Homebrew 5.1.15, against the published
// v0.9.0 — because a formula that renders is not a formula that installs, and
// this repository's whole argument is that the real client is the only proof.
// The output of this file was put in a local tap: `brew install
// stephrobert/feint/feint` fetched the published binary, `feint version`
// answered `v0.9.0`, `brew test` passed, `brew audit` reported nothing, and one
// flipped byte in the digest made the install fail with "Formula reports
// different checksum" rather than installing anything.
//
// TestAFormulaCoversEveryBinaryTheChecksumsListPublishes,
// TestAnEntryTheFormulaHasNoPlatformForIsRefused,
// TestAChecksumsLineThatIsNotTwoFieldsIsRefused,
// TestADigestThatIsNotOneIsRefused and
// TestTwoDigestsForOneAssetAreRefused fail without them.

// Spec is everything the formula needs, and the three fields that are not the
// checksums list are all facts this repository already holds.
type Spec struct {
	// Slug is the repository the release lives in, `stephrobert/feint`. It is
	// read from go.mod rather than typed, so the formula cannot point at a
	// repository this binary was not built from.
	Slug string
	// Version is the released version without its leading `v`, `0.9.0`.
	Version string
	// License is the SPDX identifier, read from the LICENSE file.
	License string
	// Checksums is the release's `checksums.txt`, verbatim. Not a digest map:
	// taking the parsed form would let a caller assemble one by hand, which is
	// the transcription this whole file exists to make impossible.
	Checksums string
}

var (
	// A release asset name, as strictly as it can be spelled, with the two
	// segments that decide where Homebrew installs it. The URL and the Ruby
	// string literal are both built by concatenating this name, so anything a
	// quote, a backslash, a space or a path segment could do to either is
	// refused here rather than escaped later — the order of preference
	// CLAUDE.md sets out, refusal at the entrance ahead of escaping at the
	// render.
	assetName = regexp.MustCompile(`^feint-([a-z0-9]+)-([a-z0-9]+)$`)
	// A SHA-256, and nothing that merely looks like one. Homebrew compares the
	// digest it computes against this string, so a truncated one does not fail
	// open — but a formula carrying 63 characters is a formula somebody edited.
	sha256Digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// The released version, as the CHANGELOG spells it.
	semverOnly = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	// owner/repo, with the character set GitHub allows and nothing else. A slug
	// is the first thing interpolated into the download URL.
	slugShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// homebrewOS and homebrewArch translate a Go platform into the block Homebrew
// selects on. Two tables rather than one of pairs, so a new GOOS and a new
// GOARCH are separate facts — and an entry missing from either is a refusal
// below, never a platform quietly left out of the formula.
var (
	homebrewOS   = map[string]string{"darwin": "macos", "linux": "linux"}
	homebrewArch = map[string]string{"amd64": "intel", "arm64": "arm"}
)

// binary is one published binary and where Homebrew installs it from.
type binary struct {
	asset  string
	os     string // macos or linux, as `on_<os>` spells it
	arch   string // intel or arm, as `on_<arch>` spells it
	digest string
}

// checksumEntry is one line of a sha256sum listing.
type checksumEntry struct {
	name   string
	digest string
}

// parseChecksums reads the release's list, refusing everything it does not
// fully understand.
//
// Every line, not the lines it recognises. A parser that skips what it cannot
// read publishes a formula short by a platform and says nothing, which is the
// silent-skip shape this repository has just finished removing five of. If a
// future release adds an artefact to `checksums.txt`, this refuses and somebody
// decides what the formula does about it — which is the correct amount of
// thinking for a change to the signed list of what a release is.
func parseChecksums(text string) ([]checksumEntry, error) {
	var out []checksumEntry
	for n, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("checksums.txt line %d is not `<digest>  <name>`: %q", n+1, line)
		}
		// sha256sum writes `*name` in binary mode. Accepted because the tool
		// writes it, stripped because it is not part of the name.
		out = append(out, checksumEntry{name: strings.TrimPrefix(fields[1], "*"), digest: fields[0]})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("checksums.txt is empty: a formula built from it would install nothing, " +
			"which is not the same fact as a release that published no binary")
	}
	return out, nil
}

// platformOf answers where Homebrew installs one asset, and whether it can.
//
// Total and closed: the name has to be spelled `feint-<os>-<arch>` and both
// segments have to be in the tables above, so every string that reaches the
// renderer has already been matched against a fixed set. Nothing here indexes
// past what the pattern captured, which is what keeps the refusal below a
// refusal rather than a crash.
func platformOf(name string) (os, arch string, ok bool) {
	m := assetName.FindStringSubmatch(name)
	if m == nil {
		return "", "", false
	}
	os, knownOS := homebrewOS[m[1]]
	arch, knownArch := homebrewArch[m[2]]
	return os, arch, knownOS && knownArch
}

// binaries turns the parsed list into the platforms the formula serves.
func binaries(entries []checksumEntry) ([]binary, error) {
	seen := map[string]string{}
	var out []binary
	for _, entry := range entries {
		os, arch, known := platformOf(entry.name)
		// Every entry, not the ones it recognises. An entry the formula has no
		// platform for is either a binary a `brew install` would fail on with
		// no mention of why, or a name that was never a binary at all — and
		// this name is about to be concatenated into a download URL and a Ruby
		// string literal. Skipping it publishes a formula short by a platform
		// and says nothing.
		if !known {
			return nil, fmt.Errorf("checksums.txt lists %q and the formula has no platform for it: "+
				"either the release published a binary Homebrew cannot select, or the list carries "+
				"an artefact that is not one — say which, rather than leaving it out of the formula",
				entry.name)
		}
		if !sha256Digest.MatchString(entry.digest) {
			return nil, fmt.Errorf("the digest of %s is %q, which is not a SHA-256: Homebrew would "+
				"compare the bytes against a string nothing produced", entry.name, entry.digest)
		}
		if previous, ok := seen[entry.name]; ok && previous != entry.digest {
			return nil, fmt.Errorf("checksums.txt gives %s two different digests (%s and %s): "+
				"the list is not one release's", entry.name, previous, entry.digest)
		}
		if _, ok := seen[entry.name]; ok {
			continue
		}
		seen[entry.name] = entry.digest
		out = append(out, binary{asset: entry.name, os: os, arch: arch, digest: entry.digest})
	}
	// macos before linux, intel before arm: a stable order, so a formula
	// re-derived from the same release is byte-identical to the published one
	// and tools/release/tap.sh can diff them.
	rank := map[string]int{"macos": 0, "linux": 1, "intel": 0, "arm": 1}
	sort.Slice(out, func(i, j int) bool {
		if out[i].os != out[j].os {
			return rank[out[i].os] < rank[out[j].os]
		}
		return rank[out[i].arch] < rank[out[j].arch]
	})
	return out, nil
}

// className is the Ruby class Homebrew requires a formula named `feint` to
// declare. Derived from the repository name, because a second spelling of it in
// this file would be a second thing to disagree with go.mod.
func className(repo string) string {
	var b strings.Builder
	for _, word := range strings.Split(repo, "-") {
		if word == "" {
			continue
		}
		b.WriteString(strings.ToUpper(word[:1]) + word[1:])
	}
	return b.String()
}

// formulaDesc is what `brew info` prints. Not derived: no file in this
// repository holds a Homebrew-shaped description (under 80 characters, no
// leading article, not starting with the formula's own name), and pretending
// to derive one from the README's first line would produce a worse sentence
// that still needed hand-checking.
const formulaDesc = "Local emulator of the Scaleway, Outscale and Exoscale clouds"

// Formula renders the tap's `Formula/feint.rb` for one release.
func Formula(spec Spec) (string, error) {
	if !slugShape.MatchString(spec.Slug) {
		return "", fmt.Errorf("%q is not an owner/repo slug: it is the first thing this writes into "+
			"a download URL", spec.Slug)
	}
	if !semverOnly.MatchString(spec.Version) {
		return "", fmt.Errorf("%q is not a released version as `X.Y.Z`: the tag it names is what "+
			"every URL below points at", spec.Version)
	}
	if strings.TrimSpace(spec.License) == "" {
		return "", fmt.Errorf("no SPDX licence: `brew audit` refuses a formula without one, and " +
			"guessing it here would put a licence claim in a file nothing checks")
	}
	entries, err := parseChecksums(spec.Checksums)
	if err != nil {
		return "", err
	}
	bins, err := binaries(entries)
	if err != nil {
		return "", err
	}

	_, repo, _ := strings.Cut(spec.Slug, "/")
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s", spec.Slug, spec.Version)

	var b strings.Builder
	b.WriteString("# Generated by `mise run release:formula` from the checksums.txt this release\n")
	b.WriteString("# published and cosign signed. Do not edit by hand: tools/release/tap.sh derives\n")
	b.WriteString("# this file again from the release and exits 2 while the tap differs.\n")
	b.WriteString("#\n")
	b.WriteString("# It installs the published bytes and never rebuilds them, so the SHA-256 brew\n")
	b.WriteString("# checks is the one the release signed — the same reasoning the container image\n")
	b.WriteString("# follows.\n")
	fmt.Fprintf(&b, "class %s < Formula\n", className(repo))
	fmt.Fprintf(&b, "  desc %q\n", formulaDesc)
	fmt.Fprintf(&b, "  homepage \"https://github.com/%s\"\n", spec.Slug)
	// No `version` stanza: brew scans it from the download URL, and declaring it
	// again is what `brew audit` refuses — "`version 0.9.0` is redundant with
	// version scanned from URL", measured against Homebrew 5.1.15 on 2026-08-20
	// with a formula that carried it.
	//
	// Dropping it makes the version implicit, which would normally be the worse
	// trade here. It is safe because the `test do` block below asserts that the
	// version brew scanned is the one the installed binary reports: if the asset
	// naming ever changes shape and brew scans the wrong string, `brew test`
	// fails rather than a wrong version being published quietly.
	fmt.Fprintf(&b, "  license %q\n", spec.License)

	for _, group := range groupByOS(bins) {
		fmt.Fprintf(&b, "\n  on_%s do\n", group.os)
		for j, bin := range group.bins {
			if j > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "    on_%s do\n", bin.arch)
			fmt.Fprintf(&b, "      url \"%s/%s\"\n", base, bin.asset)
			fmt.Fprintf(&b, "      sha256 %q\n", bin.digest)
			b.WriteString("    end\n")
		}
		b.WriteString("  end\n")
	}

	// One install, outside the platform blocks. A `def install` inside each of
	// them is what goreleaser emits and what the first version of this rendered,
	// and `brew audit` reports four problems for it — "Do not define methods in
	// blocks". Measured on 2026-08-20 against Homebrew 5.1.15, before and after.
	//
	// Exactly one file is staged, because exactly one `url` is selected, and
	// the count is asserted rather than assumed: `Dir[...].first` on an empty
	// match installs nil, and on two matches installs whichever came first.
	b.WriteString("\n  def install\n")
	b.WriteString("    published = Dir[\"" + repo + "-*\"]\n")
	fmt.Fprintf(&b, "    odie \"expected one published %s binary in the download, found "+
		"#{published.length}\" if published.length != 1\n", repo)
	fmt.Fprintf(&b, "    bin.install published.first => %q\n", repo)
	b.WriteString("  end\n")

	// The one assertion a formula can make that is worth making: the binary it
	// installed says it is the version the formula claims. Measured rather than
	// assumed — the release builds with
	// `-ldflags "-X ...cli.Version=${GITHUB_REF_NAME}"`, and GITHUB_REF_NAME is
	// the tag, so `feint version` prints `v0.9.0` with the `v`. Asserting
	// `version` alone would pass on a binary printing anything containing
	// `0.9.0`.
	b.WriteString("\n  test do\n")
	fmt.Fprintf(&b, "    assert_match \"v#{version}\", shell_output(\"#{bin}/%s version\")\n", repo)
	b.WriteString("  end\n")
	b.WriteString("end\n")
	return b.String(), nil
}

// osGroup is the binaries one `on_<os>` block holds.
type osGroup struct {
	os   string
	bins []binary
}

// groupByOS keeps the order binaries already put them in.
func groupByOS(bins []binary) []osGroup {
	var out []osGroup
	for _, bin := range bins {
		if len(out) == 0 || out[len(out)-1].os != bin.os {
			out = append(out, osGroup{os: bin.os})
		}
		out[len(out)-1].bins = append(out[len(out)-1].bins, bin)
	}
	return out
}

// modulePath reads the repository slug out of a go.mod module line.
var modulePath = regexp.MustCompile(`(?m)^module\s+github\.com/([^/\s]+/[^/\s]+)`)

// SlugFromModule reads the repository a module is published from.
//
// One reader, used by the formula and by the install commands in
// internal/cli/docs_release.go: the slug appears in a download URL on the
// install page and in a download URL in the formula, and two readers of one
// line in go.mod is two chances for the two pages to name different
// repositories.
func SlugFromModule(goMod string) string {
	if m := modulePath.FindStringSubmatch(goMod); m != nil {
		return m[1]
	}
	return ""
}

// LicenseID names the licence a LICENSE file carries, for the one field
// Homebrew wants as an SPDX identifier.
//
// A table of two, and a refusal for anything else. Deriving a licence by
// guessing is how a formula ends up publishing a licence claim nobody made;
// this repository is Apache-2.0 and the day that changes, this refuses until
// somebody says what it changed to.
func LicenseID(license string) (string, error) {
	switch {
	case strings.Contains(license, "Apache License") && strings.Contains(license, "Version 2.0"):
		return "Apache-2.0", nil
	case strings.Contains(license, "MIT License"):
		return "MIT", nil
	}
	return "", fmt.Errorf("the LICENSE file matches no SPDX identifier this knows: " +
		"the formula must not guess one")
}
