package cli

import (
	"fmt"
	"os"
	"strings"
)

// The container section of the install page is generated, for the same reason
// the install commands are: it names an image, a tag and a verification
// identity, and every one of those is a claim about what the release workflow
// actually does. A hand-written copy would survive a renamed image or a
// re-anchored identity the way the hand-written route counts survived three
// releases — looking generated, being wrong.
//
// The version comes from the CHANGELOG, like the binary commands above it, so
// cutting a release makes the section stale and `docs --check` refuses until it
// is regenerated. The image name is derived from go.mod and checked against the
// release workflow: if the workflow stops pushing that image, the section
// cannot be rendered at all, which is the correct failure — a page must not
// tell a reader to pull what nothing publishes.

const (
	containerStartMarker = "<!-- container:start -->"
	containerEndMarker   = "<!-- container:end -->"

	// containerWorkflowAnchor is the image reference exactly as release.yml
	// spells it. The generator does not evaluate the expression; it requires
	// the workflow to carry it, so renaming the image there breaks `feint docs`
	// here instead of leaving the page pointing at a name nothing pushes.
	// TestContainerSectionRefusesAWorkflowThatPushesNothing fails without this.
	containerWorkflowAnchor = "ghcr.io/${{ github.repository }}"
)

// renderContainer writes the container section: what the image is and is not,
// how a CI consumes it, and how to verify what was pulled.
func renderContainer(workflow, goMod, changelog string) (string, error) {
	body, err := os.ReadFile(workflow) //nolint:gosec // a path this repository owns
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(body), containerWorkflowAnchor) {
		return "", fmt.Errorf("%s does not push %s: the container section cannot name an image nothing publishes", workflow, containerWorkflowAnchor)
	}
	slug := repositorySlug(goMod)
	if slug == "" {
		return "", fmt.Errorf("cannot render the container section: no module path in %s", goMod)
	}
	version := latestReleased(changelog)
	if version == "" {
		return "", fmt.Errorf("cannot render the container section: no released section in %s to pin it to", changelog)
	}
	// ghcr.io accepts lowercase only; the workflow's ${{ github.repository }}
	// is lowercased by the registry on push, so the page must spell it the way
	// a pull will.
	image := "ghcr.io/" + strings.ToLower(slug)
	ref := fmt.Sprintf("%s:v%s", image, version)

	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n")
	b.WriteString("**Control plane only.** The image runs `feint serve` with `--vm off` and\n" +
		"emulates nothing but the three control planes. Real machines behind `--vm` need\n" +
		"the binary on a host with Incus, exactly as above: an image that promised to\n" +
		"start containers from inside a container would be the half-truth this project\n" +
		"refuses. The image exists so the emulator can enter a `services:` block or a\n" +
		"compose file; the self-detaching binary stays the nominal mode.\n\n")
	fmt.Fprintf(&b, "One tag per release, the release's own, and nothing mutable: no `latest`,\n"+
		"for the same reason the commands above name a version.\n\n")
	b.WriteString("```bash\n")
	fmt.Fprintf(&b, "docker run --rm -p 127.0.0.1:4599:4599 %s\n", ref)
	b.WriteString("curl http://127.0.0.1:4599/_feint/health\n")
	b.WriteString("```\n\n")
	b.WriteString("In a GitHub Actions job — the runner holds the steps until the image's own\n" +
		"healthcheck answers, so the first step can talk to it immediately:\n\n")
	b.WriteString("```yaml\n")
	b.WriteString("services:\n")
	b.WriteString("  feint:\n")
	fmt.Fprintf(&b, "    image: %s\n", ref)
	b.WriteString("    ports:\n")
	b.WriteString("      - 4599:4599\n")
	b.WriteString("```\n\n")
	b.WriteString("In GitLab CI:\n\n")
	b.WriteString("```yaml\n")
	b.WriteString("services:\n")
	fmt.Fprintf(&b, "  - name: %s\n", ref)
	b.WriteString("    alias: feint\n")
	b.WriteString("```\n\n")
	b.WriteString("Then point the client at the port: the same endpoint settings the\n" +
		"conformance suites under `tools/conformance/` pass to `scw`, Terraform,\n" +
		"`oapi-cli` and `exo` work unchanged against the container.\n\n")
	b.WriteString("The image is signed and attested by the same release workflow as the\n" +
		"binaries, under the same identity — and this recipe is executed, not\n" +
		"published on faith: the release workflow runs it against the image it has\n" +
		"just pushed, and `tools/release/preflight.sh` runs it against the previous\n" +
		"release before a tag exists.\n\n")
	b.WriteString("```bash\n")
	fmt.Fprintf(&b, "cosign verify %s \\\n", ref)
	fmt.Fprintf(&b, "  --certificate-identity-regexp '^https://github\\.com/%s/\\.github/workflows/release\\.yml@refs/tags/v' \\\n", regexpSlug(slug))
	b.WriteString("  --certificate-oidc-issuer https://token.actions.githubusercontent.com\n\n")
	fmt.Fprintf(&b, "gh attestation verify oci://%s \\\n", ref)
	fmt.Fprintf(&b, "  --repo %s \\\n", slug)
	fmt.Fprintf(&b, "  --signer-workflow %s/.github/workflows/release.yml\n", slug)
	b.WriteString("```\n")
	return b.String(), nil
}

// spliceContainer renders the container section and reports whether the file
// would change, on the same optional terms as every other generated section: a
// document without the markers never claimed to carry it.
func spliceContainer(path, workflow, goMod, changelog string) (bool, error) {
	updated, current, err := containerSplice(path, workflow, goMod, changelog)
	if err != nil || updated == "" {
		return false, err
	}
	return updated != current, nil
}

func writeSplicedContainer(path, workflow, goMod, changelog string) error {
	updated, _, err := containerSplice(path, workflow, goMod, changelog)
	if err != nil || updated == "" {
		return err
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // documentation is world-readable by design
}

func containerSplice(path, workflow, goMod, changelog string) (updated, current string, err error) {
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
	if !strings.Contains(string(body), containerStartMarker) {
		return "", "", nil
	}
	rendered, err := renderContainer(workflow, goMod, changelog)
	if err != nil {
		return "", "", err
	}
	out, err := spliceSection(string(body), containerStartMarker, containerEndMarker, rendered)
	if err != nil {
		return "", "", err
	}
	return out, string(body), nil
}
