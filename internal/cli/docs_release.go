package cli

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// The install commands name assets, and a release is where that goes wrong.
//
// README.md and docs/install.md tell a reader to fetch
// `releases/latest/download/<asset>`. `latest` is what keeps them from naming a
// version that has moved on, but nothing kept them from naming a *file* that the
// release workflow no longer builds: rename an artefact in the build matrix and
// the documented command starts answering 404, on the page a first-time reader
// meets, with no test failing anywhere.
//
// So the asset names are checked against the workflow that produces them. The
// check is read-only on purpose. Regenerating documentation from a tag-triggered
// job would mean write credentials on a workflow whose whole job is to publish,
// a commit landing outside any pull request and outside branch protection, and a
// release whose sources are not the commit it was cut from. A gate that refuses
// is safe; a gate that repairs the repository is a second way in.

const (
	releaseWorkflow = ".github/workflows/release.yml"
	// goWorkflow runs the tests, the race detector and the linters. Together
	// with the conformance workflow it is everything that actually executes a
	// build, which is what the install page may call exercised.
	goWorkflow = ".github/workflows/go.yml"

	installStartMarker = "<!-- install:start -->"
	installEndMarker   = "<!-- install:end -->"
)

// modulePath reads the module line, which is where the repository's own name
// lives. The install commands need it, and typing it into the generator would
// put a third copy of it in the repository.
var modulePath = regexp.MustCompile(`(?m)^module\s+github\.com/([^/\s]+/[^/\s]+)`)

// changelogVersion finds the newest released section, which is this repository's
// record of what the latest published version is.
var changelogVersion = regexp.MustCompile(`(?m)^##\s*\[(\d+\.\d+\.\d+)\]`)

const changelogPath = "CHANGELOG.md"

// latestReleased is the version the install commands name.
//
// Not `releases/latest/download`, which is where this started and which was
// wrong for the same reason a container tag is: it is a mutable reference. A
// documented command that fetches "whatever is newest" adopts a compromised
// release the day it is published, hands the reader a binary neither of you can
// name afterwards, and cannot be checked against a hash. This repository already
// refuses that everywhere else — actions pinned by commit SHA, `latest` in the
// forbidden container tags, scanners installed from a pinned version with its
// checksum, a fourteen-day Dependabot cooldown so a fresh release is never
// adopted on day one.
//
// So the page names a version, and the version comes from the CHANGELOG section
// the release workflow itself reads for the release body. Cutting a release
// moves that heading, which makes the pages stale, which makes `docs --check`
// exit 2 until they are regenerated. Being a release behind stops being possible
// rather than being something somebody remembers.
func latestReleased(changelog string) string {
	body, err := os.ReadFile(changelog) //nolint:gosec // a path this repository owns
	if err != nil {
		return ""
	}
	if m := changelogVersion.FindStringSubmatch(string(body)); m != nil {
		return m[1]
	}
	return ""
}

func repositorySlug(goMod string) string {
	body, err := os.ReadFile(goMod) //nolint:gosec // a path this repository owns
	if err != nil {
		return ""
	}
	if m := modulePath.FindStringSubmatch(string(body)); m != nil {
		return m[1]
	}
	return ""
}

// renderInstall writes the download commands, and it writes them verified.
//
// Generated for two reasons, and the second one is why it exists at all. The
// asset names come from the release build matrix, so a renamed artefact cannot
// leave a 404 behind on the page a first-time reader meets. And the verification
// travels with the command: an earlier revision of this page carried a bare
// `curl` and an `install`, three paragraphs above its own sentence promising that
// every download here is verified before it runs. One source, spliced into both
// pages, is what stops the short version from quietly becoming the documented
// one.
func renderInstall(workflow, goMod, changelog string) (string, error) {
	assets, _, err := releaseAssets(workflow)
	if err != nil {
		return "", err
	}
	slug := repositorySlug(goMod)
	if slug == "" || len(assets) == 0 {
		return "", fmt.Errorf("cannot render the install commands: no module path in %s, or no build matrix in %s", goMod, workflow)
	}
	version := latestReleased(changelog)
	if version == "" {
		return "", fmt.Errorf("cannot render the install commands: no released section in %s to pin them to", changelog)
	}

	names := make([]string, 0, len(assets))
	for name := range assets {
		names = append(names, name)
	}
	sort.Strings(names)

	// The example uses the platform most readers are on, and the full list
	// follows so nobody has to guess how the others are spelled.
	primary := "feint-linux-amd64"
	if !assets[primary] {
		primary = names[0]
	}

	var b strings.Builder
	b.WriteString(docsGenerated)
	fmt.Fprintf(&b, "\n\nVersion **%s**, named rather than fetched as `latest`: a mutable reference"+
		"\ndownloads whatever is newest, which is a binary neither of us can name"+
		"\nafterwards and a release adopted the day it is published. This line moves when"+
		"\nthe CHANGELOG does, and `feint docs --check` fails until it has.\n\n", version)
	b.WriteString("Needs `cosign` (or `gh`, further down) and nothing else. Every file is fetched\nto disk and verified before anything runs it.\n\n")
	b.WriteString("```bash\n")
	fmt.Fprintf(&b, "base=https://github.com/%s/releases/download/v%s\n\n", slug, version)

	// The four published binaries, selected rather than left to the reader.
	// Naming one and expecting an arm64 user to substitute is how somebody ends
	// up with an amd64 binary that fails at exec with a message naming neither
	// the file nor the reason.
	b.WriteString("# The published binary for this machine, or a refusal: no default, because\n")
	b.WriteString("# the wrong architecture fails at exec with a message naming neither.\n")
	b.WriteString("case \"$(uname -s)-$(uname -m)\" in\n")
	unmapped := make([]string, 0, len(names))
	for _, name := range names {
		uname, known := unameOf[name]
		if !known {
			unmapped = append(unmapped, name)
			continue
		}
		fmt.Fprintf(&b, "  %-14s asset=%s ;;\n", uname+")", name)
	}
	b.WriteString("  *) echo \"no published binary for $(uname -s)-$(uname -m)\" >&2; exit 1 ;;\n")
	b.WriteString("esac\n\n")

	fmt.Fprintf(&b, "curl -fsSLO \"$base/$asset\"\n")
	b.WriteString("curl -fsSLO \"$base/checksums.txt\"\n")
	b.WriteString("curl -fsSLO \"$base/checksums.txt.cosign.bundle\"\n\n")
	b.WriteString("# Who produced the list, before trusting a single hash inside it. One\n")
	b.WriteString("# signature covers every artefact, because every hash is in this file.\n")
	b.WriteString("#\n")
	b.WriteString("# The identity names the release workflow and the tag ref, not the\n")
	b.WriteString("# repository: 'github.com/<slug>/.*' would accept any workflow here that\n")
	b.WriteString("# ever gets id-token: write, which is a claim about who owns the\n")
	b.WriteString("# repository rather than about what built this file. Anchored at both\n")
	b.WriteString("# ends, because an unanchored pattern matches anywhere in the string.\n")
	b.WriteString("cosign verify-blob --bundle checksums.txt.cosign.bundle \\\n")
	fmt.Fprintf(&b, "  --certificate-identity-regexp '^https://github\\.com/%s/\\.github/workflows/release\\.yml@refs/tags/v' \\\n", regexpSlug(slug))
	b.WriteString("  --certificate-oidc-issuer https://token.actions.githubusercontent.com \\\n")
	b.WriteString("  checksums.txt\n\n")
	b.WriteString("# Then the bytes against the list. Without --ignore-missing this fails on\n")
	b.WriteString("# every platform you did not download, which reads like a bad binary.\n")
	b.WriteString("sha256sum -c checksums.txt --ignore-missing\n\n")
	b.WriteString("install -m 0755 \"$asset\" ~/.local/bin/feint\n")
	b.WriteString("```\n\n")
	if len(unmapped) > 0 {
		// Published and not installable by the block above. Said rather than
		// skipped: a binary nobody can select is a binary nobody uses.
		fmt.Fprintf(&b, "Published with no case above, so fetch them by name: `%s`.\n\n", strings.Join(unmapped, "`, `"))
	}

	b.WriteString("With `gh`, which checks the build provenance instead: it proves which\n")
	b.WriteString("workflow and which commit produced the binary, where the signature above\n")
	b.WriteString("proves who published the list. `--signer-workflow` is what makes the\n")
	b.WriteString("difference between an identity and a provenance: without it the check\n")
	b.WriteString("accepts anything this repository attested, whichever workflow did it.\n\n")
	b.WriteString("```bash\n")
	fmt.Fprintf(&b, "gh release download v%s --repo %s --pattern '%s'\n", version, slug, primary)
	fmt.Fprintf(&b, "gh attestation verify %s --repo %s \\\n", primary, slug)
	fmt.Fprintf(&b, "  --signer-workflow %s/.github/workflows/release.yml\n", slug)
	b.WriteString("```\n\n")

	b.WriteString(renderPlatformTable(names))
	b.WriteString("\nOlder versions stay downloadable at their own tag: nothing here rewrites what\na previous release published.\n")
	return b.String(), nil
}

// renderPlatformTable says what each published binary is worth, in the two
// columns that differ.
//
// One table rather than a sentence, because the two claims are not the same
// shape and merging them is what produced "the published platforms are …", a
// line a reader hears as "and they all work". The control-plane column is
// computed from the runners; the --vm column is a property of Incus.
// regexpSlug escapes the dots of an owner/repo so the identity pattern matches
// that repository and not one whose name differs by a character the regexp reads
// as "any". A slug is user input to this generator, and a dot is the cheapest
// way a pattern silently widens.
func regexpSlug(slug string) string {
	return strings.ReplaceAll(slug, ".", `\\.`)
}

func renderPlatformTable(names []string) string {
	exercised := make(map[string]bool)
	for _, platform := range exercisedPlatforms(goWorkflow, conformanceWorkflow) {
		exercised[platform] = true
	}
	conditional := make(map[string]bool)
	for _, platform := range conditionalPlatforms(goWorkflow, conformanceWorkflow) {
		conditional[platform] = true
	}

	var b strings.Builder
	b.WriteString("| Binary | Control plane | `--vm`: real machines |\n|---|---|---|\n")
	for _, name := range names {
		platform := strings.TrimPrefix(name, "feint-")

		control := "built, run by nothing here"
		switch {
		case conditional[platform]:
			control = "**exercised**: tests and race detector, skipped on private forks"
		case exercised[platform]:
			control = "**exercised**: tests, race detector, and it answers"
		}

		// Incus is the only runtime, and it is a Linux server. Measured on
		// 2026-07-30 rather than assumed: the lxc/incus releases publish
		// `bin.macos.incus`, `-agent` and `-benchmark` — the client side — and no
		// server for macOS, and Zabbly packages Debian amd64 and arm64 only. So a
		// macOS binary emulates three control planes and can never back a server
		// with a machine.
		vm := "**impossible**: Incus publishes no macOS server"
		if strings.HasPrefix(platform, "linux-") {
			// The honest state of the other half, and it moved with #139.
			//
			// This said "no workflow in this repository starts a machine runtime
			// at all", which stopped being true the day runtime-proof.yml landed
			// — in the same release train, without touching this file. An audit
			// found the sentence still here, propagated to five places in two
			// languages, and `docs --check` reconducted it at every release
			// because it compares the page with this generator: it proves the
			// form, never the claim. Verifying is not parsing, applied to the
			// project's own documentation.
			//
			// The distinction that is true: no `pull_request` gate starts a
			// runtime — the nightly job is advisory until its promotion criterion
			// is met (#125) — and amd64 is what that job runs on.
			vm = "possible; a nightly CI job proves it, and no pull-request gate does"
			if !strings.HasSuffix(platform, "-amd64") {
				vm = "Incus is packaged for it; the nightly runtime job runs amd64 only"
			}
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", name, control, vm)
	}

	b.WriteString("\n**`--vm` is off by default, and no pull-request gate turns it on.** Starting\n" +
		"machines is a side effect on whoever's host runs it, so it is asked for rather\n" +
		"than assumed, and the conformance suite has to stay runnable where no runtime\n" +
		"exists — which is every pull request. The network suites skip themselves there\n" +
		"and say so.\n\n" +
		"A nightly job does run it: `.github/workflows/runtime-proof.yml` installs Incus\n" +
		"and OVN on a GitHub-hosted runner and drives the network, ssh and crash suites\n" +
		"in both modes. It is advisory until its promotion criterion is met, which is\n" +
		"why no pull request depends on it. Locally, `FEINT_VM=incus-ovn mise run\n" +
		"conformance` on a host with Incus proves the same thing.\n")
	if guardedByVisibility(goWorkflow) {
		b.WriteString("\nThe non-amd64 runners are free here and billed on a private repository, so the\n" +
			"job skips on a private fork rather than spending somebody's minutes without\n" +
			"asking. A fork that wants that coverage removes the condition, which is one\n" +
			"line.\n")
	}
	return b.String()
}

// guardedByVisibility reports whether a workflow gates jobs on the repository
// being public. The install page must not read as "exercised on four platforms"
// while three of those jobs are skipping.
func guardedByVisibility(workflow string) bool {
	body, err := os.ReadFile(workflow) //nolint:gosec // a path this repository owns
	if err != nil {
		return false
	}
	return strings.Contains(string(body), "github.event.repository.private")
}

// spliceInstall renders the install commands into a document, on the same
// optional terms as every other generated section.
func spliceInstall(path, workflow, goMod, changelog string) (bool, error) {
	updated, current, err := installSplice(path, workflow, goMod, changelog)
	if err != nil || updated == "" {
		return false, err
	}
	return updated != current, nil
}

func writeSplicedInstall(path, workflow, goMod, changelog string) error {
	updated, _, err := installSplice(path, workflow, goMod, changelog)
	if err != nil || updated == "" {
		return err
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // documentation is world-readable by design
}

func installSplice(path, workflow, goMod, changelog string) (updated, current string, err error) {
	if path == "" {
		return "", "", nil
	}
	body, err := os.ReadFile(path) //nolint:gosec // an operator-supplied path
	if os.IsNotExist(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if !strings.Contains(string(body), installStartMarker) {
		return "", "", nil
	}
	rendered, err := renderInstall(workflow, goMod, changelog)
	if err != nil {
		return "", "", err
	}
	out, err := spliceSection(string(body), installStartMarker, installEndMarker, rendered)
	if err != nil {
		return "", "", err
	}
	return out, string(body), nil
}

var (
	// The build matrix, which is where the binary names come from.
	matrixEntry = regexp.MustCompile(`(?m)^\s*-\s*\{\s*goos:\s*([a-z0-9]+),\s*goarch:\s*([a-z0-9]+)\s*\}`)
	// What a document tells a reader to download: a full tagged URL, a name
	// under the `$base` the generated block sets, or a `gh release download`
	// pattern. Three forms because the commands are written for humans, and a
	// check that only reads one of them is a check with a blind spot.
	documentedAsset = regexp.MustCompile(`(?:releases/download/v[0-9][^/\s"']*/|\$base/|--pattern\s+'|asset=)([A-Za-z0-9._-]+)`)
	// A mutable reference, which no page here may carry.
	mutableDownload = regexp.MustCompile(`releases/latest/download`)

	// The runners the test workflows actually use. What a matrix builds and
	// what anything runs are two different claims, and this project's whole
	// argument is the difference between them.
	runnerLabel = regexp.MustCompile(`(?m)^\s*runs-on:\s*([A-Za-z0-9._-]+)`)
	// And the same labels as matrix entries, since `runs-on: ${{ matrix.x }}`
	// resolves to nothing on its own.
	matrixRunner = regexp.MustCompile(`(?m)^\s*-\s*([A-Za-z0-9._-]+)\s*$`)
)

// unameOf maps a built asset onto what `uname -s`-`uname -m` prints on that
// platform, which is what the install command switches on.
//
// Written out rather than derived, and it is not the same spelling on either
// side: Go says arm64, Linux says aarch64, macOS says arm64. Guessing that
// would hand an arm64 reader an amd64 binary, which fails at exec with a message
// naming neither.
//
// A built asset missing from this map is surfaced in the generated block rather
// than dropped: an architecture published and not installable is exactly the
// gap this whole section was written to close.
var unameOf = map[string]string{
	"feint-linux-amd64":  "Linux-x86_64",
	"feint-linux-arm64":  "Linux-aarch64",
	"feint-darwin-amd64": "Darwin-x86_64",
	"feint-darwin-arm64": "Darwin-arm64",
}

// runnerPlatforms maps a GitHub runner label onto the GOOS/GOARCH it is. Written
// out rather than derived: a wrong guess here would claim a platform is tested
// when nothing runs on it, which is the one error this table exists to prevent.
// A label absent from this map is reported as unknown rather than assumed.
// Read off GitHub's hosted-runner reference on 2026-07-30 rather than
// remembered: macOS labels without a suffix are Apple Silicon, `-intel` is the
// x86 one, and `macos-13` is no longer listed at all.
var runnerPlatforms = map[string]string{
	"ubuntu-22.04":     "linux-amd64",
	"ubuntu-24.04":     "linux-amd64",
	"ubuntu-latest":    "linux-amd64",
	"ubuntu-22.04-arm": "linux-arm64",
	"ubuntu-24.04-arm": "linux-arm64",
	"ubuntu-26.04-arm": "linux-arm64",
	"macos-14":         "darwin-arm64",
	"macos-15":         "darwin-arm64",
	"macos-26":         "darwin-arm64",
	"macos-latest":     "darwin-arm64",
	"macos-15-intel":   "darwin-amd64",
	"macos-26-intel":   "darwin-amd64",
	"windows-2022":     "windows-amd64",
	"windows-2025":     "windows-amd64",
	"windows-latest":   "windows-amd64",
	"windows-11-arm":   "windows-arm64",
}

// exercisedPlatforms is what the test workflows run on, as opposed to what the
// release builds for.
//
// The distinction is the project's own: docs/install.md separates "proven" from
// "installs" for exactly this reason, and a line reading "the published
// platforms are …" invites a reader to hear "and they all work". Four binaries
// are built; one platform runs the tests and the conformance suite. Saying so is
// the difference between a claim and a measurement.
func exercisedPlatforms(workflows ...string) []string {
	seen := make(map[string]bool)
	for _, path := range workflows {
		body, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
		if err != nil {
			continue
		}
		for _, m := range runnerLabel.FindAllStringSubmatch(string(body), -1) {
			platform, known := runnerPlatforms[m[1]]
			if !known {
				platform = "unknown runner " + m[1]
			}
			seen[platform] = true
		}
		// A matrix writes `runs-on: ${{ matrix.runner }}`, which the pattern
		// above cannot resolve: read the list items too. Without this, the day
		// somebody moves the job to a matrix, the table silently drops every
		// platform it lists — a documentation gate that goes quiet is worse than
		// none, because the page keeps looking generated.
		for _, m := range matrixRunner.FindAllStringSubmatch(string(body), -1) {
			if platform, known := runnerPlatforms[m[1]]; known {
				seen[platform] = true
			}
		}
	}
	return sorted(seen)
}

// conditionalPlatforms are the ones a workflow only exercises under a condition,
// which here means a public repository.
//
// They are separated from the rest because a table saying "exercised" about a
// job that is currently skipping is the half-truth this project exists to avoid:
// a reader of the table alone would believe four platforms are measured while
// one is. The same mistake was caught once already, in the README's status table
// where Exoscale carried no maturity label.
func conditionalPlatforms(workflows ...string) []string {
	seen := make(map[string]bool)
	for _, path := range workflows {
		body, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
		if err != nil || !strings.Contains(string(body), "github.event.repository.private") {
			continue
		}
		for _, m := range matrixRunner.FindAllStringSubmatch(string(body), -1) {
			if platform, known := runnerPlatforms[m[1]]; known {
				seen[platform] = true
			}
		}
	}
	return sorted(seen)
}

func sorted(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for platform := range seen {
		out = append(out, platform)
	}
	sort.Strings(out)
	return out
}

// releaseAssets is the set of files the release workflow attaches, as the
// documentation may name them.
func releaseAssets(workflow string) (map[string]bool, string, error) {
	body, err := os.ReadFile(workflow) //nolint:gosec // a path this repository owns
	if err != nil {
		return nil, "", err
	}
	assets := make(map[string]bool)
	for _, m := range matrixEntry.FindAllStringSubmatch(string(body), -1) {
		assets[fmt.Sprintf("feint-%s-%s", m[1], m[2])] = true
	}
	return assets, string(body), nil
}

// documentedAssetMismatches reports asset names a document tells a reader to
// download and the release does not publish.
//
// A binary must be in the build matrix. Anything else — checksums, the signature
// bundle, the provenance — has to appear literally in the workflow, which is
// weaker than parsing it and is the honest limit: those files are produced by
// steps rather than by a matrix, and a name that no longer appears there is the
// case worth catching.
//
// A missing file is not a mismatch. The binary runs outside a checkout, where
// neither the workflow nor the documentation is there to read.
func documentedAssetMismatches(workflow string, docs ...string) []string {
	assets, body, err := releaseAssets(workflow)
	if err != nil || len(assets) == 0 {
		return nil
	}

	var out []string
	for _, doc := range docs {
		if doc == "" {
			continue
		}
		text, err := os.ReadFile(doc) //nolint:gosec // an operator-supplied path
		if err != nil {
			continue
		}
		// `releases/latest/download` is the same class of mistake as a container
		// tag or an action pinned to a branch: it hands the reader whatever is
		// newest, which cannot be checked against a hash and adopts a
		// compromised release on the day it is published. This repository
		// refuses mutable references everywhere else; a page is not an exception.
		if mutableDownload.Match(text) {
			out = append(out, fmt.Sprintf("%s downloads from releases/latest, a mutable reference: name the version the CHANGELOG released", doc))
		}
		seen := make(map[string]bool)
		for _, m := range documentedAsset.FindAllStringSubmatch(string(text), -1) {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true

			if strings.HasPrefix(name, "feint-") {
				if !assets[name] {
					out = append(out, fmt.Sprintf("%s tells a reader to download %s, which %s does not build",
						doc, name, workflow))
				}
				continue
			}
			if !strings.Contains(body, name) {
				out = append(out, fmt.Sprintf("%s tells a reader to download %s, which %s never produces",
					doc, name, workflow))
			}
		}
	}
	sort.Strings(out)
	return out
}
