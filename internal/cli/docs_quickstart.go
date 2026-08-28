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
// **And then it taught the version before the one this project ships (#593).**
// 0.11 introduced `feint.yaml`, `feint up` and `feint down`; this block still
// opened with `feint start` / `eval "$(feint env scaleway)"` / `terraform
// apply`. Worse than old: **not copyable**. In which directory, with which
// `main.tf`, with which provider block? The `Apply complete!` printed under
// those three lines was not reachable from them, which is the one thing a quick
// start may never be. So the first door is now four commands against
// examples/quickstart/scaleway — a stack short enough to read whole — and the
// three-line hand-driven form stays underneath, where it is what a reader with
// an existing project actually needs.
//
// Two more doors, and that is the other half. The OCI image is the format a CI
// actually consumes — a `services:` block, a compose file, a Testcontainers
// call — and it appeared nowhere in the README at all while living in
// docs/install.md. A reader deciding whether Feint fits their pipeline was
// being shown the mode that fits a laptop.
//
// The third door is the Marketplace action (#245): `uses:
// stephrobert/setup-feint@v1` is the line people copy out of somebody else's
// pipeline, and a workflow is read more often than it is run. The `@v1` is
// floating because that is the convention every consumer of an action expects;
// the binary it installs is not — the `version:` input is required, and the
// action verifies the checksum before anything runs.
//
// Generated rather than typed for the reason every version in this repository
// is: the image tag is a release's own, and a hand-written one is wrong the day
// after the next release. So is the `version:` under the action. `feint docs
// --check` fails until it is regenerated.
//
// **Rendered per locale since #591.** The block used to be injected in English
// into both READMEs, on the rule README.fr.md states about every generated
// block: *a command needs no translation*. True of a command, and this block is
// not only commands — it carried "On your machine" and "In CI, or anywhere
// Docker runs" into the middle of a French page. What stays shared is what is
// structured: the version, the image, the repository, the commands themselves.
// TestTheFrenchQuickStartIsInFrench fails when an English sentence comes back.

const (
	quickstartStartMarker = "<!-- quickstart:start -->"
	quickstartEndMarker   = "<!-- quickstart:end -->"
)

// renderQuickstart writes the doors, all of them pinned to the released
// version, in the locale of the page that carries them.
//
// nolint:misspell // the French half is French: "ressemble" is a word, and the
// linter reads every Go string as English. The same exemption renderSafety
// carries, for the same reason.
func renderQuickstart(goMod, changelog string, french bool) (string, error) {
	slug := repositorySlug(goMod)
	if slug == "" {
		return "", fmt.Errorf("cannot render the quick start: no module path in %s", goMod)
	}
	version := latestReleased(changelog)
	if version == "" {
		return "", fmt.Errorf("cannot render the quick start: no released section in %s to pin it to", changelog)
	}
	image := "ghcr.io/" + strings.ToLower(slug)
	// The stack the first door lands in. Named from the same constant the gate
	// that applies it reads, so the README cannot send a reader to a directory
	// no suite keeps working.
	stack := quickstartRoot + "/" + quickstartLead

	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n")

	// The French half of this block avoids the em-dash throughout, which is a
	// style rule for this repository's French prose and not a rendering
	// accident: a colon, a comma pair or a full stop carries every one of the
	// breaks the English half sets with `—`.
	if french {
		b.WriteString("**Sur votre machine**, un binaire statique et une stack assez courte pour se lire :\n\n")
	} else {
		b.WriteString("**On your machine** — one static binary, and a stack short enough to read whole:\n\n")
	}
	b.WriteString("```bash\n")
	fmt.Fprintf(&b, "brew install %s/%s\n", slug, pathBase(slug))
	fmt.Fprintf(&b, "git clone https://github.com/%s\n", slug)
	fmt.Fprintf(&b, "cd %s/%s\n", pathBase(slug), stack)
	if french {
		b.WriteString("feint up      # vérifie la station, démarre l'émulateur, applique le Terraform\n")
		b.WriteString("feint down    # détruit ce qu'il a créé, puis arrête l'émulateur\n")
	} else {
		b.WriteString("feint up      # checks the host, starts the emulator, applies the Terraform\n")
		b.WriteString("feint down    # destroys what it created, then stops the emulator\n")
	}
	b.WriteString("```\n\n")

	// What those four commands print, derived from the configuration they run
	// rather than typed beside it.
	//
	// The line that used to sit under this block said `Resources: 5 added` and
	// was #593's second finding: it was not reachable from the commands above
	// it — three lines with no directory, no `main.tf` and no provider block —
	// so a reader following the quick start exactly could not arrive at the
	// output the quick start displayed. It is derived now, and
	// tools/conformance/quickstart.sh takes this very line out of the README and
	// requires the run to print it, so the output shown is the output produced.
	// Anchored on the module file the version already comes from, so the three
	// facts this block carries are read relative to one root rather than to
	// whatever directory the binary was run in.
	added, err := resourceCount(filepath.Join(filepath.Dir(goMod), quickstartRoot, quickstartLead, "main.tf"))
	if err != nil {
		return "", err
	}
	if french {
		b.WriteString("Ce que ces quatre commandes impriment :\n\n")
	} else {
		b.WriteString("What those four commands print:\n\n")
	}
	b.WriteString("```text\n")
	fmt.Fprintf(&b, "Apply complete! Resources: %d added, 0 changed, 0 destroyed.\n", added)
	b.WriteString("```\n\n")

	if french {
		fmt.Fprintf(&b, "`feint up` lit le `feint.yaml` posé à côté du Terraform : il vérifie la station, "+
			"démarre l'émulateur, exporte ce dont le client officiel a besoin, lance le moteur et "+
			"attend les conditions que le fichier déclare. La stack fait un serveur et une adresse, "+
			"sans runtime de machines. `%s/%s` est celle qui ressemble à de la production, et elle "+
			"est faite pour casser l'émulateur plutôt que pour se lire en premier.\n\n",
			stacksRoot, quickstartLead)
		b.WriteString("**Vous avez déjà un projet ?** Pilotez-le à la main, c'est ce qu'un `feint.yaml` " +
			"vous évite d'écrire :\n\n")
	} else {
		fmt.Fprintf(&b, "`feint up` reads the `feint.yaml` beside the Terraform: it checks the host, "+
			"starts the emulator, exports what the official client needs, runs the engine and waits "+
			"on the conditions the file declares. The stack is one server and one address, with no "+
			"machine runtime — `%s/%s` is the one shaped like production, and it exists to break the "+
			"emulator rather than to be read first.\n\n",
			stacksRoot, quickstartLead)
		b.WriteString("**Already have a project?** Drive it by hand, which is what a `feint.yaml` " +
			"saves you from writing:\n\n")
	}
	b.WriteString("```bash\n")
	if french {
		b.WriteString("feint start                      # se détache, attend qu'il réponde\n")
		b.WriteString("eval \"$(feint env scaleway)\"     # pointe le client officiel vers lui\n")
		b.WriteString("terraform apply                  # le vrai provider Scaleway\n")
	} else {
		b.WriteString("feint start                      # detaches, waits until it answers\n")
		b.WriteString("eval \"$(feint env scaleway)\"     # point the official client at it\n")
		b.WriteString("terraform apply                  # the real Scaleway provider\n")
	}
	b.WriteString("```\n\n")

	if french {
		b.WriteString("**En CI, ou partout où Docker tourne**, le même émulateur en service :\n\n")
	} else {
		b.WriteString("**In CI, or anywhere Docker runs** — the same emulator as a service:\n\n")
	}
	b.WriteString("```yaml\n")
	b.WriteString("services:\n")
	b.WriteString("  feint:\n")
	fmt.Fprintf(&b, "    image: %s:v%s\n", image, version)
	b.WriteString("    ports: [\"4599:4599\"]\n")
	b.WriteString("```\n\n")
	if french {
		fmt.Fprintf(&b, "L'image ne sert que le plan de contrôle et ne porte aucun tag `latest` ; "+
			"[docs/install.md](docs/install.md) donne la forme GitLab, la forme compose et la "+
			"vérification de signature. Pour la tirer directement : "+
			"`docker run --rm -p 127.0.0.1:4599:4599 %s:v%s`.\n\n", image, version)
		b.WriteString("**Dans GitHub Actions, sans conteneur**, l'action du Marketplace :\n\n")
	} else {
		fmt.Fprintf(&b, "The image is control-plane only and carries no `latest` tag; "+
			"[docs/install.md](docs/install.md) has the GitLab form, the compose form and "+
			"the signature verification. Pull it directly with "+
			"`docker run --rm -p 127.0.0.1:4599:4599 %s:v%s`.\n\n", image, version)
		b.WriteString("**In GitHub Actions without a container** — the action from the Marketplace:\n\n")
	}
	b.WriteString("```yaml\n")
	b.WriteString("- uses: stephrobert/setup-feint@v1\n")
	b.WriteString("  with:\n")
	fmt.Fprintf(&b, "    version: %s\n", version)
	if french {
		b.WriteString("    provider: scaleway   # exporte ce dont le client officiel a besoin\n")
	} else {
		b.WriteString("    provider: scaleway   # exports what the official client needs\n")
	}
	b.WriteString("```\n\n")
	if french {
		b.WriteString("Elle installe le binaire publié, **vérifie sa somme de contrôle avant de " +
			"l'exécuter**, et attend que l'émulateur réponde.\n")
	} else {
		b.WriteString("It installs the released binary, **verifies its checksum before running " +
			"it**, and waits until the emulator answers.\n")
	}
	return b.String(), nil
}

// resourceCount answers how many objects an apply of this configuration
// creates: one per `resource` block.
//
// That equality is only true while no block multiplies itself, so a `count` or
// a `for_each` is refused rather than counted wrong. Refusing is the right
// answer here and not a limitation of the parser: the number goes on the front
// page as what a reader will see, and a quick start short enough to read whole
// has no business multiplying resources. A quickstart that needs one has
// outgrown the job, and the refusal says so instead of publishing a figure that
// is quietly off by two.
func resourceCount(path string) (int, error) {
	body, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		return 0, fmt.Errorf("cannot count what the quick start creates: %w", err)
	}
	count := 0
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "resource \"") {
			count++
			continue
		}
		if strings.HasPrefix(trimmed, "count ") || strings.HasPrefix(trimmed, "count=") ||
			strings.HasPrefix(trimmed, "for_each ") || strings.HasPrefix(trimmed, "for_each=") {
			return 0, fmt.Errorf(
				"%s uses %q: one resource block is no longer one object, so the number of resources "+
					"the quick start prints cannot be derived from the file. Either drop it, or this "+
					"example has outgrown being the first one somebody reads",
				path, strings.Fields(trimmed)[0])
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("%s declares no resource: the quick start would tell a reader to run "+
			"an apply that creates nothing", path)
	}
	return count, nil
}

// pathBase is the repository's own directory name, the one `git clone` creates.
func pathBase(slug string) string {
	if i := strings.LastIndex(slug, "/"); i >= 0 {
		return slug[i+1:]
	}
	return slug
}

// Both READMEs carry it, each in its own language since #591. Sharing the
// English block was the rule README.fr.md still states about generated blocks —
// *a command needs no translation* — applied one word too far: this one also
// carries sentences, and they were landing in English in the middle of a French
// page. What is shared is the structure the code owns.
//
// Rendering only the English one would be the mistake this repository already
// made once: the French command table had fallen ten verbs behind and nobody was
// looking, because no check read that page (#237).
func spliceQuickstart(root, goMod, changelog string) (bool, error) {
	if root == "" {
		return false, nil
	}
	for _, page := range frontPages(root) {
		current, err := os.ReadFile(page.Path) //nolint:gosec // a path this repository owns
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		if !strings.Contains(string(current), quickstartStartMarker) {
			continue
		}
		rendered, err := renderQuickstart(goMod, changelog, page.French)
		if err != nil {
			return false, err
		}
		updated, err := spliceSection(string(current), quickstartStartMarker, quickstartEndMarker, rendered)
		if err != nil {
			return false, fmt.Errorf("%s: %w", page.Path, err)
		}
		if updated != string(current) {
			return true, nil
		}
	}
	return false, nil
}

func writeSplicedQuickstart(root, goMod, changelog string) error {
	for _, page := range frontPages(root) {
		current, err := os.ReadFile(page.Path) //nolint:gosec // a path this repository owns
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !strings.Contains(string(current), quickstartStartMarker) {
			continue
		}
		rendered, err := renderQuickstart(goMod, changelog, page.French)
		if err != nil {
			return err
		}
		updated, err := spliceSection(string(current), quickstartStartMarker, quickstartEndMarker, rendered)
		if err != nil {
			return fmt.Errorf("%s: %w", page.Path, err)
		}
		if err := os.WriteFile(page.Path, []byte(updated), 0o644); err != nil { //nolint:gosec // documentation is world-readable by design
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
