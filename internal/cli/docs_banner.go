package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The banner a reader meets first, generated rather than written (#177).
//
// What it replaced said, in both languages, "do not point anything you care
// about at it". That sentence was the most honest thing on the page when it was
// written, and read plainly it is an instruction not to use the software. Its
// information was real — two whole-pack adversarial audits found defects on
// every path the conformance suite does not walk — but its granularity was
// wrong: it said *nothing* is safe where this repository can say precisely which
// things are and which are not, and that distinction is the entire product.
//
// Three categories, and the reason this is generated rather than typed: a
// hand-written banner restates figures, and a restated figure is a second source
// of truth that disagrees with the first by the next release. The issue says so
// itself, under "what must not happen". So the counts come from the artefacts
// every other generated block reads, and `feint docs --check` fails when the
// page and the artefacts disagree.
//
// What is deliberately *not* generated is the sentence about the audits. It is
// not a count, it is a finding, and it stays until a measurement retires it.
//
// TestTheBannerCountsComeFromTheArtefacts fails without this.

const (
	safetyStartMarker = "<!-- safety:start -->"
	safetyEndMarker   = "<!-- safety:end -->"
)

// safetyFacts is what the banner is allowed to assert, and every field of it is
// read from an artefact rather than typed.
type safetyFacts struct {
	// Driven is the number of operations a real client has driven, and Mounted
	// the number mounted. The banner names the first as what is proven and the
	// difference as what is unknown, which is the same reading #174 makes.
	Driven  int
	Mounted int
	// Limits is how many sections docs/limits.md carries. The banner points at
	// the file rather than listing them: a list here would be the second source
	// this generator exists to avoid.
	Limits int
	// Explained is how many of the undriven operations say why no client
	// reaches them (Route.Undriven). Counted here because "unknown" was one
	// number for two facts (#174): an operation nobody has got to yet and one
	// no client path exists for read the same in a total, and only the first is
	// work. The banner prints both, so the figure a reader meets first is the
	// one the artefact can defend.
	Explained int
}

// readBannerFacts gathers the counts from the evidence record and the limits
// page.
//
// An artefact that cannot be read is an error rather than a zero: a banner
// claiming "0 operations are proven" because a file moved would be worse than
// no banner, and it is exactly the failure mode a generated block is supposed
// to remove.
func readSafetyFacts(evidencePath, limitsPath string) (safetyFacts, error) {
	raw, err := os.ReadFile(evidencePath) //nolint:gosec // a path this repository owns
	if err != nil {
		return safetyFacts{}, fmt.Errorf("read the evidence record: %w", err)
	}
	var record struct {
		Operations map[string]struct {
			Driven bool `json:"driven"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return safetyFacts{}, fmt.Errorf("decode the evidence record: %w", err)
	}
	facts := safetyFacts{Mounted: len(record.Operations)}
	for _, row := range record.Operations {
		if row.Driven {
			facts.Driven++
		}
	}

	// The reasons come from the packs mounted in this process, joined onto the
	// record: the same rule as everywhere else here, the binary that serves the
	// routes is what says what they are.
	srv, _, err := newServer(nil)
	if err != nil {
		return safetyFacts{}, err
	}
	for _, route := range srv.AllRoutes() {
		if route.Undriven == "" {
			continue
		}
		if row, recorded := record.Operations[route.Operation]; recorded && !row.Driven {
			facts.Explained++
		}
	}

	limits, err := os.ReadFile(limitsPath) //nolint:gosec // a path this repository owns
	if err != nil {
		return safetyFacts{}, fmt.Errorf("read the limits page: %w", err)
	}
	for _, line := range strings.Split(string(limits), "\n") {
		if strings.HasPrefix(line, "## ") {
			facts.Limits++
		}
	}
	if facts.Mounted == 0 || facts.Limits == 0 {
		return safetyFacts{}, fmt.Errorf("the artefacts report nothing to describe")
	}
	return facts, nil
}

// explainedClause says how the undriven operations divide, and says nothing
// about a remainder when there is none.
//
// The first version printed "N of them state why …; the rest are waiting for a
// scenario" unconditionally, and the day every one of them carried a reason it
// read "17 … 17 of them … the rest are waiting" — a remainder of zero described
// as if it existed. That is the half-truth this repository keeps catching in its
// own prose, and it is generated text, so nobody would have edited it.
//
// nolint:misspell // the French half is French.
func explainedClause(facts safetyFacts, french bool) string {
	undriven := facts.Mounted - facts.Driven
	waiting := undriven - facts.Explained
	switch {
	case undriven == 0:
		if french {
			return "Il n'en reste aucune."
		}
		return "There are none left."
	case waiting == 0:
		if french {
			return "Chacune dit pourquoi aucun client officiel ne l'atteint, à la route et dans " +
				"[docs/routes.md](docs/routes.md)."
		}
		return "Every one of them states why no official client reaches it, at the route and in " +
			"[docs/routes.md](docs/routes.md)."
	case french:
		return fmt.Sprintf("%d d'entre elles disent pourquoi aucun client officiel ne les atteint, à la "+
			"route et dans [docs/routes.md](docs/routes.md) ; les %d autres attendent un scénario.",
			facts.Explained, waiting)
	default:
		return fmt.Sprintf("%d of them state why no official client reaches them, at the route and in "+
			"[docs/routes.md](docs/routes.md); the other %d are waiting for a scenario.",
			facts.Explained, waiting)
	}
}

// bannerAnchors are the links the banner makes, and they are checked.
//
// A claim that resolves to nothing is a marketing sentence, which is the one
// thing the issue asks this page never to become. Listed here so the check and
// the rendering read the same set rather than agreeing by accident.
func safetyAnchors() []string {
	return []string{
		"docs/limits.md",
		"docs/conformance.md",
		"coverage/evidence.json",
	}
}

// nolint:misspell // the French half is French: "inventer" and "complets" are
// words, and the linter reads every Go string as English.
func renderSafety(facts safetyFacts, french bool) string {
	var b strings.Builder
	if french {
		b.WriteString("> [!IMPORTANT]\n")
		b.WriteString("> **Ce qu'on peut pointer vers cet émulateur, et ce qu'on ne peut pas.**\n>\n")
		fmt.Fprintf(&b, "> **Prouvé** : %d des %d opérations montées sont pilotées par un vrai client, "+
			"à chaque pull request. `scw`, `oapi-cli`, `exo`, Terraform et OpenTofu tournent contre "+
			"l'émulateur en CI, et les machines démarrent réellement : connexion ssh sur le compte par "+
			"défaut de chaque provider, subnets isolés, pare-feu qui filtre. La chaîne complète est "+
			"décrite dans [docs/conformance.md](docs/conformance.md).\n>\n", facts.Driven, facts.Mounted)
		fmt.Fprintf(&b, "> **Pas prouvé** : quotas, prix, capacité réelle, validation des identifiants, "+
			"authentification, cohérence à terme. Les %d sections de "+
			"[docs/limits.md](docs/limits.md) disent chacune ce qu'elle coûte. Un émulateur avec un "+
			"seul compte implicite et aucune grille tarifaire devrait inventer ces chiffres, et "+
			"quelqu'un agirait dessus.\n>\n", facts.Limits)
		fmt.Fprintf(&b, "> **Inconnu** : %d opérations sont montées et n'ont jamais été pilotées par un "+
			"client. %s Elles sont comptées plutôt qu'escamotées, une par une, dans "+
			"[coverage/evidence.json](coverage/evidence.json).\n>\n",
			facts.Mounted-facts.Driven, explainedClause(facts, true))
		b.WriteString("> L'avertissement qui reste, parce qu'il est mesuré et non compté : **ce que la " +
			"suite de conformance ne parcourt pas n'est pas prouvé**. Deux audits adverses complets, " +
			"un par provider, ont trouvé des défauts sur chacun de ces chemins, y compris dans les " +
			"correctifs du tour précédent. Signalez ce qui casse, c'est ce qui fait bouger les " +
			"chiffres ci-dessus.\n")
		return b.String()
	}
	b.WriteString("> [!IMPORTANT]\n")
	b.WriteString("> **What is safe to point at this emulator, and what is not.**\n>\n")
	fmt.Fprintf(&b, "> **Proven**: %d of the %d mounted operations are driven by a real client, on every "+
		"pull request. `scw`, `oapi-cli`, `exo`, Terraform and OpenTofu run against the emulator in "+
		"CI, and machines really boot: an ssh login on each provider's own default account, isolated "+
		"subnets, a firewall that filters. The whole chain is described in "+
		"[docs/conformance.md](docs/conformance.md).\n>\n", facts.Driven, facts.Mounted)
	fmt.Fprintf(&b, "> **Not proven**: quotas, prices, real capacity, identifier validation, "+
		"authentication, eventual consistency. The %d sections of "+
		"[docs/limits.md](docs/limits.md) each say what one costs. An emulator with a single implicit "+
		"account and no price list would have to invent those figures, and somebody would act on "+
		"them.\n>\n", facts.Limits)
	fmt.Fprintf(&b, "> **Unknown**: %d operations are mounted and have never been driven by a client. "+
		"%s They are counted rather than glossed, one by one, in "+
		"[coverage/evidence.json](coverage/evidence.json).\n>\n",
		facts.Mounted-facts.Driven, explainedClause(facts, false))
	b.WriteString("> The warning that stays, because it is measured rather than counted: **what the " +
		"conformance suite does not walk is unproven**. Two whole-pack adversarial audits, one per " +
		"provider, found defects on every such path — including in the fixes of the previous round. " +
		"Report what breaks; that is what moves the figures above.\n")
	return b.String()
}

// bannerPaths answers the documents carrying the banner and whether each is
// French, sorted so two runs report the same order.
func safetyPaths(root string) []struct {
	Path   string
	French bool
} {
	out := []struct {
		Path   string
		French bool
	}{
		{filepath.Join(root, "README.md"), false},
		{filepath.Join(root, "README.fr.md"), true},
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func spliceSafety(root string) (bool, error) {
	carriers, err := safetyCarriers(root)
	if err != nil || len(carriers) == 0 {
		return false, err
	}
	facts, err := readSafetyFacts(
		filepath.Join(root, "coverage", "evidence.json"),
		filepath.Join(root, "docs", "limits.md"))
	if err != nil {
		return false, err
	}
	for _, doc := range safetyPaths(root) {
		current, err := os.ReadFile(doc.Path) //nolint:gosec // a path this repository owns
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !strings.Contains(string(current), safetyStartMarker) {
			continue
		}
		updated, err := spliceSection(string(current), safetyStartMarker, safetyEndMarker,
			renderSafety(facts, doc.French))
		if err != nil {
			return false, err
		}
		if updated != string(current) {
			return true, nil
		}
	}
	return false, nil
}

func writeSplicedSafety(root string) error {
	carriers, err := safetyCarriers(root)
	if err != nil || len(carriers) == 0 {
		return err
	}
	facts, err := readSafetyFacts(
		filepath.Join(root, "coverage", "evidence.json"),
		filepath.Join(root, "docs", "limits.md"))
	if err != nil {
		return err
	}
	for _, doc := range safetyPaths(root) {
		current, err := os.ReadFile(doc.Path) //nolint:gosec // a path this repository owns
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !strings.Contains(string(current), safetyStartMarker) {
			continue
		}
		updated, err := spliceSection(string(current), safetyStartMarker, safetyEndMarker,
			renderSafety(facts, doc.French))
		if err != nil {
			return err
		}
		if err := os.WriteFile(doc.Path, []byte(updated), 0o644); err != nil { //nolint:gosec // documentation is world-readable by design
			return err
		}
	}
	return nil
}

// safetyCarriers answers the documents that actually claim the banner.
//
// Checked before the artefacts are read, and that order is the whole of it: a
// document set carrying no marker never asked for a banner, so a missing
// evidence record there is not its problem. Reading first turned a drift verdict
// into an error one for every fixture in the test suite, which is how this was
// found.
func safetyCarriers(root string) ([]string, error) {
	var carriers []string
	for _, doc := range safetyPaths(root) {
		current, err := os.ReadFile(doc.Path) //nolint:gosec // a path this repository owns
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if strings.Contains(string(current), safetyStartMarker) {
			carriers = append(carriers, doc.Path)
		}
	}
	return carriers, nil
}
