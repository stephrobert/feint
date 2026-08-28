package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// The sentences derived from capabilityMatrix, and the table that publishes it.
//
// Two consumers, and they are the two halves of #592. The promise block is the
// README's own line 41, generated in both locales so that the claim is no
// longer something anybody types; the capability block in docs/clients.md is
// the matrix itself, printed with its proof column so a reader can check the
// promise against what establishes it.
//
// The prose is written per locale rather than shared, and that is #591's fourth
// finding rather than a preference: the generated blocks used to be injected in
// English into both pages, on the rule that *a command needs no translation*.
// True of a command; the blocks also carry prose, and README.fr.md carried
// "On your machine" and "In CI, or anywhere Docker runs" in the middle of a
// French page. So what is shared is what is structured — the matrix, the
// version, the image, the commands — and what is written twice is the sentence
// around them.

// supportedPairs answers, per client token, the packs the matrix marks
// supported — the raw material every promise sentence is built from.
func supportedPairs() map[string][]string {
	out := map[string][]string{}
	for _, row := range capabilityMatrix {
		if row.Support != capabilitySupported {
			continue
		}
		out[row.Client] = append(out[row.Client], row.Provider)
	}
	return out
}

// refusedRows answers the refused rows in the order capabilityMatrix declares
// them.
//
// Declaration order rather than alphabetical, here and in supportedPairs above,
// and the prose is why: sorted, the first sentence of the README read "OpenTofu
// and Terraform drive Outscale and Scaleway", which is every name in the right
// place and the emphasis in the wrong one. The order a maintainer writes the
// rows in is a judgement about which pack and which client a reader meets
// first, and it is worth keeping.
func refusedRows() []capabilityRow {
	var out []capabilityRow
	for _, row := range capabilityMatrix {
		if row.Support == capabilityRefused {
			out = append(out, row)
		}
	}
	return out
}

// joinNames renders a list of names the way the locale writes one.
func joinNames(names []string, and string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " " + and + " " + names[len(names)-1]
	}
}

// engineProviders answers the packs every IaC engine drives, and the packs
// where at least one engine is refused.
//
// It insists the engines agree. Terraform and OpenTofu resolve the same
// providers from the same registry, so a promise that lumped them together
// while the matrix split them would be a sentence truer than the table. If they
// ever diverge, the render fails rather than picking one.
func engineProviders() (driven []string, refused []capabilityRow, err error) {
	supported := supportedPairs()
	var engines []string
	for _, c := range clientSources {
		if c.engine != "" {
			engines = append(engines, c.token)
		}
	}
	if len(engines) == 0 {
		return nil, nil, fmt.Errorf("clientSources names no infrastructure-as-code engine: " +
			"the promise has no client to promise")
	}
	driven = supported[engines[0]]
	for _, engine := range engines[1:] {
		if strings.Join(supported[engine], ",") != strings.Join(driven, ",") {
			return nil, nil, fmt.Errorf(
				"capabilityMatrix has %s driving %v and %s driving %v: the promise names one engine "+
					"list for both, and it cannot while they disagree",
				capabilityClientName(engines[0]), driven, capabilityClientName(engine), supported[engine])
		}
	}
	for _, row := range refusedRows() {
		if capabilityEngineOf(row.Client) != "" {
			refused = append(refused, row)
		}
	}
	return driven, refused, nil
}

// renderPromise writes the README's promise: what this emulator lets a reader
// do, and for which pack with which client.
//
// Every name in it comes from capabilityMatrix. The sentence that used to sit
// here said "Run your Terraform against Scaleway, Outscale or Exoscale" and was
// false for two days; it cannot be written again, because nobody writes this
// paragraph.
// The shape is a sentence and a list rather than one paragraph, and that is the
// claim reader's doing rather than a layout choice: a unit naming two packs and
// two clients asserts every pair of them, so a single sentence reading "`scw`
// for Scaleway, `octl` for Outscale, `exo` for Exoscale" would be claiming
// `scw` against Exoscale. One pack per list item is what makes each line a
// claim the matrix can carry, and it is the honest shape anyway — the packs do
// not have the same clients.
func renderPromise(french bool) (string, error) {
	driven, refused, err := engineProviders()
	if err != nil {
		return "", err
	}

	var engineNames, drivenNames []string
	for _, c := range clientSources {
		if c.engine != "" {
			engineNames = append(engineNames, c.name)
		}
	}
	for _, provider := range driven {
		drivenNames = append(drivenNames, providerName(provider))
	}

	// The refusals a given pack carries, so the list item that names the pack
	// also names the way on — the rule VetoEngine states about its own reason.
	refusedBy := map[string][]capabilityRow{}
	for _, row := range refused {
		refusedBy[row.Provider] = append(refusedBy[row.Provider], row)
	}

	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n")

	if french {
		b.WriteString("**Pointez Terraform et les CLI officielles vers votre propre machine.**\n")
		b.WriteString("Aucun compte cloud, aucun identifiant de cloud, et rien de créé nulle part.\n\n")
		b.WriteString(wrapParagraph(fmt.Sprintf(
			"%s pilotent %s. Chaque pack a en plus son CLI officiel, et chacun d'eux pilote cet "+
				"émulateur de bout en bout :",
			joinNames(engineNames, "et"), joinNames(drivenNames, "et"))))
		b.WriteString("\n")
	} else {
		b.WriteString("**Point Terraform and the official cloud CLIs at your own machine.**\n")
		b.WriteString("No cloud account, no cloud credentials, and nothing created anywhere.\n\n")
		b.WriteString(wrapParagraph(fmt.Sprintf(
			"%s drive %s. Each pack also has its own official CLI, and every one of them drives "+
				"this emulator end to end:",
			joinNames(engineNames, "and"), joinNames(drivenNames, "and"))))
		b.WriteString("\n")
	}

	for _, row := range cliRows() {
		item := fmt.Sprintf("**%s** with `%s`", providerName(row.Provider), row.Client)
		if french {
			item = fmt.Sprintf("**%s** avec `%s`", providerName(row.Provider), row.Client)
		}
		// Every engine the pack refuses, not the first one: Terraform and
		// OpenTofu are refused for the same reason and carry the same marker,
		// and naming one of them would read as the other being allowed.
		if refusals := refusedBy[row.Provider]; len(refusals) > 0 {
			var names []string
			for _, refusal := range refusals {
				names = append(names, capabilityClientName(refusal.Client))
			}
			// A second sentence rather than a clause, and the claim reader is
			// why it may be one: it names the pack only through the marker, and
			// the marker is what makes naming a refused pair legitimate. It
			// also avoids "et Terraform et OpenTofu" in the French.
			if french {
				item += fmt.Sprintf(
					". %s reviennent le jour où une version publiée porte le correctif de %s, "+
						"que `feint up` refuse au portillon jusque-là",
					joinNames(names, "et"), refusals[0].Marker)
			} else {
				item += fmt.Sprintf(
					". %s join it the day a published release carries the fix for %s, which "+
						"`feint up` refuses at the doorstep until one does",
					joinNames(names, "and"), refusals[0].Marker)
			}
		}
		b.WriteString(wrapBullet(item + "."))
	}
	return b.String(), nil
}

// cliRows are the supported rows for clients that are not engines: one per
// pack, in the order clientSources declares them.
func cliRows() []capabilityRow {
	var out []capabilityRow
	for _, c := range clientSources {
		if c.engine != "" {
			continue
		}
		for _, row := range capabilityMatrix {
			if row.Client == c.token && row.Support == capabilitySupported {
				out = append(out, row)
			}
		}
	}
	return out
}

// renderCapabilityMatrix publishes the table itself, proof column included.
//
// The proof column is the reason this is more than a list. `supported` names
// the workflow that drives the pair on every pull request; `refused` names the
// doorstep that stops it. Neither is typed here: capabilityProblems resolves
// both against those instruments, and `feint docs --check` exits 2 when one of
// them stops carrying its row.
func renderCapabilityMatrix() string {
	var b strings.Builder
	b.WriteString(docsGenerated)
	b.WriteString("\n\n")
	b.WriteString("| Provider | Client | Mode | Support | Proof | Reason |\n|---|---|---|---|---|---|\n")

	rows := append([]capabilityRow{}, capabilityMatrix...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		return rows[i].Client < rows[j].Client
	})
	for _, row := range rows {
		reason := row.Reason
		if reason == "" {
			reason = "—"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | **%s** | `%s` | %s |\n",
			providerName(row.Provider), capabilityClientName(row.Client), row.Mode,
			row.Support, row.Proof, reason)
	}

	b.WriteString("\nEvery sentence in this repository that claims a client for a provider is\n")
	b.WriteString("generated from this table, and every row is resolved against the thing its\n")
	b.WriteString("proof column names: `conformance workflow` means\n")
	b.WriteString("`.github/workflows/conformance.yml` runs a suite driving that client against\n")
	b.WriteString("that pack on every pull request, and `up.go VetoEngine` means the pack itself\n")
	b.WriteString("refuses the engine before `feint up` starts a process. A row whose proof does\n")
	b.WriteString("not carry it fails `feint docs --check` at exit code 2, and so does a page\n")
	b.WriteString("that claims a pair this table does not.\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Splicing
// ---------------------------------------------------------------------------

// splicePromise reports whether either front page's promise is out of date.
func splicePromise(root string) (bool, error) {
	changed := false
	for _, page := range frontPages(root) {
		updated, current, err := promiseFor(page)
		if err != nil {
			return false, err
		}
		if current != "" && updated != current {
			changed = true
		}
	}
	return changed, nil
}

func writeSplicedPromise(root string) error {
	for _, page := range frontPages(root) {
		updated, current, err := promiseFor(page)
		if err != nil {
			return err
		}
		if current == "" || updated == current {
			continue
		}
		if err := os.WriteFile(page.Path, []byte(updated), 0o644); err != nil { //nolint:gosec // documentation is world-readable by design
			return err
		}
	}
	return nil
}

// promiseFor renders one page's promise and returns the updated document beside
// the current one. An empty `current` means the page is absent or claims no
// promise block, and the caller does nothing.
func promiseFor(page frontPage) (updated, current string, err error) {
	body, err := os.ReadFile(page.Path) //nolint:gosec // a path this repository owns
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}
	if !strings.Contains(string(body), promiseStartMarker) {
		return "", "", nil
	}
	rendered, err := renderPromise(page.French)
	if err != nil {
		return "", "", err
	}
	out, err := spliceSection(string(body), promiseStartMarker, promiseEndMarker, rendered)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", page.Path, err)
	}
	return out, string(body), nil
}

// spliceCapability keeps the published matrix in step with the declared one.
func spliceCapability(path string) (bool, error) {
	updated, current, err := capabilityFor(path)
	if err != nil {
		return false, err
	}
	return current != "" && updated != current, nil
}

func writeSplicedCapability(path string) error {
	updated, current, err := capabilityFor(path)
	if err != nil {
		return err
	}
	if current == "" || updated == current {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o644) //nolint:gosec // documentation is world-readable by design
}

func capabilityFor(path string) (updated, current string, err error) {
	if path == "" {
		return "", "", nil
	}
	body, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}
	if !strings.Contains(string(body), capabilityStartMarker) {
		return "", "", nil
	}
	out, err := spliceSection(string(body), capabilityStartMarker, capabilityEndMarker, renderCapabilityMatrix())
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", path, err)
	}
	return out, string(body), nil
}
