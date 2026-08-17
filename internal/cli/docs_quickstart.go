package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The first screen: what Feint lets you do, in the fewest lines that still run.
//
// It exists because of what the README used to open with. The quick start was
// `feint serve` and a route count, which demonstrates that an HTTP server
// started — true, and not the thing anybody came for. The sentence a reader
// needs in the first ten seconds is *my Terraform ran and no cloud account was
// involved*, and it was four screens down.
//
// Two doors rather than one, and that is the other half. The OCI image is the
// format a CI actually consumes — a `services:` block, a compose file, a
// Testcontainers call — and it appeared nowhere in the README at all while
// living in docs/install.md. A reader deciding whether Feint fits their pipeline
// was being shown the mode that fits a laptop.
//
// Generated rather than typed for the reason every version in this repository
// is: the image tag is a release's own, and a hand-written one is wrong the day
// after the next release. `feint docs --check` fails until it is regenerated.

const (
	quickstartStartMarker = "<!-- quickstart:start -->"
	quickstartEndMarker   = "<!-- quickstart:end -->"
)

// renderQuickstart writes the two doors, both pinned to the released version.
func renderQuickstart(goMod, changelog string) (string, error) {
	slug := repositorySlug(goMod)
	if slug == "" {
		return "", fmt.Errorf("cannot render the quick start: no module path in %s", goMod)
	}
	version := latestReleased(changelog)
	if version == "" {
		return "", fmt.Errorf("cannot render the quick start: no released section in %s to pin it to", changelog)
	}
	image := "ghcr.io/" + strings.ToLower(slug)

	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n")

	b.WriteString("**On your machine** — one static binary, nothing else:\n\n")
	b.WriteString("```bash\n")
	b.WriteString("feint start                      # detaches, waits until it answers\n")
	b.WriteString("eval \"$(feint env scaleway)\"     # point the official client at it\n")
	b.WriteString("terraform apply                  # the real Scaleway provider\n")
	b.WriteString("```\n\n")

	b.WriteString("**In CI, or anywhere Docker runs** — the same emulator as a service:\n\n")
	b.WriteString("```yaml\n")
	b.WriteString("services:\n")
	b.WriteString("  feint:\n")
	fmt.Fprintf(&b, "    image: %s:v%s\n", image, version)
	b.WriteString("    ports: [\"4599:4599\"]\n")
	b.WriteString("```\n\n")
	fmt.Fprintf(&b, "The image is control-plane only and carries no `latest` tag; "+
		"[docs/install.md](docs/install.md) has the GitLab form, the compose form and "+
		"the signature verification. Pull it directly with "+
		"`docker run --rm -p 127.0.0.1:4599:4599 %s:v%s`.\n", image, version)
	return b.String(), nil
}

// Both READMEs carry it, and the block itself stays in English in both — the
// rule README.fr.md states about every generated block, for the reason it gives:
// they are rendered from the code rather than written by hand. The prose around
// them is translated; a command is not.
//
// The English one alone would have been the mistake this repository already made
// once: the French command table had fallen ten verbs behind and nobody was
// looking, because no check read that page (#237).
func quickstartPaths(root string) []string {
	return []string{
		filepath.Join(root, "README.md"),
		filepath.Join(root, "README.fr.md"),
	}
}

func spliceQuickstart(root, goMod, changelog string) (bool, error) {
	if root == "" {
		return false, nil
	}
	for _, path := range quickstartPaths(root) {
		current, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		if !strings.Contains(string(current), quickstartStartMarker) {
			continue
		}
		rendered, err := renderQuickstart(goMod, changelog)
		if err != nil {
			return false, err
		}
		updated, err := spliceSection(string(current), quickstartStartMarker, quickstartEndMarker, rendered)
		if err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
		if updated != string(current) {
			return true, nil
		}
	}
	return false, nil
}

func writeSplicedQuickstart(root, goMod, changelog string) error {
	for _, path := range quickstartPaths(root) {
		current, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !strings.Contains(string(current), quickstartStartMarker) {
			continue
		}
		rendered, err := renderQuickstart(goMod, changelog)
		if err != nil {
			return err
		}
		updated, err := spliceSection(string(current), quickstartStartMarker, quickstartEndMarker, rendered)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil { //nolint:gosec // documentation is world-readable by design
			return err
		}
	}
	return nil
}

// The route banner on the translated page.
//
// README.fr.md carried it as hand-written text, and it said 57, 72 and 46 routes
// while the packs mounted 102, 85 and 93. Nobody had lied — the numbers were
// true once, which is the exact failure docs.go's own header describes about the
// English tables, surviving in the one page no check read. It is the same block
// as the English one, rendered by the same function: a command is not translated.
func spliceTranslatedBanner(root string, order []string, routes map[string]map[string]int) (bool, error) {
	path := filepath.Join(root, "README.fr.md")
	current, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(current), bannerStartMarker) {
		return false, nil
	}
	updated, err := spliceSection(string(current), bannerStartMarker, bannerEndMarker, renderBanner(order, routes))
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return updated != string(current), nil
}

func writeSplicedTranslatedBanner(root string, order []string, routes map[string]map[string]int) error {
	path := filepath.Join(root, "README.fr.md")
	current, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !strings.Contains(string(current), bannerStartMarker) {
		return nil
	}
	updated, err := spliceSection(string(current), bannerStartMarker, bannerEndMarker, renderBanner(order, routes))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // documentation is world-readable by design
}
