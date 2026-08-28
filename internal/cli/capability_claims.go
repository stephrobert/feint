package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Reading the claims a generated block makes, and refusing the ones no matrix
// row carries.
//
// This is the half of #592 that the matrix alone would not deliver. A table
// nothing reads is a table, and the sentence on `README.md:41` was wrong for
// two days precisely because no instrument was pointed at it. So the pages are
// read back: every generated block of the front pages is scanned, every pair of
// (a client this repository knows, a pack this emulator mounts) it asserts is
// looked up in capabilityMatrix, and a pair with no supported row is a problem
// `feint docs --check` exits 2 on.
//
// **What counts as one claim, and the rule is mechanical rather than
// linguistic.** Parsing English is how a control starts lying, so the unit is
// chosen so that co-occurrence is enough:
//
//   - a fenced code block is one unit, whole. `eval "$(feint env scaleway)"`
//     and `terraform apply` are two lines of one recipe, and the recipe is the
//     claim: a quick start showing `feint env exoscale` above `terraform apply`
//     is exactly the promise #525 refuses, and it has to redden;
//   - a table row is one unit. The generated tables put one client per line, and
//     joining them would pair every client with every provider on the page;
//   - the rest is prose, split into sentences on `.`, `!` and `?`. A sentence is
//     the smallest span in which naming a client and a pack together reads as
//     "this drives that".
//
// **And a refusal has to be sayable.** A sentence naming a refused pair passes
// only if it also carries that row's Marker — the upstream issue itself. That
// is what lets the promise block say *"Terraform joins that pack when a release
// carries exoscale/terraform-provider-exoscale#573"* while
// *"Run your Terraform against Scaleway, Outscale or Exoscale"* is refused: the
// second names the pair and carries nothing that would let a reader check.
//
// The reader is falsifiable in both directions, and both are tests:
// TestTheOldFalsePromiseIsCaughtByTheClaimReader plants the exact sentence #592
// measured and requires it to be reported, and
// TestTheClaimReaderAcceptsWhatTheMatrixCarries requires the sentences this
// repository really ships to pass. A reader that refused everything would pass
// the first alone.

// capabilityClaimPages are the documents whose generated blocks are read.
//
// The two READMEs because that is where #592 lived, and docs/clients.md because
// it is where the matrix itself is published — a page that prints the table has
// to survive its own rule.
func capabilityClaimPages(root string) []string {
	return []string{
		filepath.Join(root, "README.md"),
		filepath.Join(root, "README.fr.md"),
		filepath.Join(root, "docs", "clients.md"),
	}
}

var (
	// generatedBlock matches one `<!-- x:start -->…<!-- x:end -->` region and
	// captures the marker name and the body. Anchored on the marker shape every
	// generated section in this repository uses, so a block added later is read
	// without this file learning its name.
	generatedBlock = regexp.MustCompile(`(?s)<!-- ([a-z]+):start -->(.*?)<!-- [a-z]+:end -->`)
	// wordish is how a unit is tokenised. Lowercase runs of letters and digits,
	// so `exoscale/terraform-provider-exoscale#573` yields `terraform` and
	// `exoscale` — the pair is named there, and it is the marker that makes the
	// sentence legitimate rather than the punctuation hiding it. It also keeps
	// `exo` out of `exoscale`, which a substring search would not.
	wordish = regexp.MustCompile(`[a-z0-9]+`)
	// sentenceEnd splits prose. Deliberately not on `;` or `,`: a semicolon
	// joins two independent claims and splitting there would let one sentence
	// promise a client for a pack the other excuses.
	sentenceEnd = regexp.MustCompile(`[.!?](\s|$)`)
	// bulletStart marks a new list item, which starts a new unit.
	bulletStart = regexp.MustCompile(`^([-*+]\s|\d+\.\s)`)
)

// generatedBlockBodies returns the body of every generated section of a
// document, keyed for the message by the marker that names it.
type generatedSection struct {
	name string
	body string
}

func generatedBlockBodies(doc string) []generatedSection {
	var out []generatedSection
	for _, m := range generatedBlock.FindAllStringSubmatch(doc, -1) {
		out = append(out, generatedSection{name: m[1], body: m[2]})
	}
	return out
}

// capabilityUnits splits one block into the spans a claim is judged in.
func capabilityUnits(block string) []string {
	var units []string
	var paragraph []string
	var fence []string
	inFence := false

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		text := strings.Join(paragraph, " ")
		paragraph = paragraph[:0]
		start := 0
		for _, loc := range sentenceEnd.FindAllStringIndex(text, -1) {
			units = append(units, text[start:loc[1]])
			start = loc[1]
		}
		if rest := strings.TrimSpace(text[start:]); rest != "" {
			units = append(units, rest)
		}
	}

	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				units = append(units, strings.Join(fence, "\n"))
				fence = fence[:0]
				inFence = false
				continue
			}
			flushParagraph()
			inFence = true
			continue
		}
		if inFence {
			fence = append(fence, line)
			continue
		}
		if trimmed == "" {
			flushParagraph()
			continue
		}
		// A table row stands alone: the generated tables put one client on each
		// line, and a paragraph made of them would pair every client with every
		// provider the table names.
		if strings.HasPrefix(trimmed, "|") {
			flushParagraph()
			units = append(units, trimmed)
			continue
		}
		// So does a list item, for the same reason and with the same
		// consequence: the promise block names one pack per item, and two items
		// read as one paragraph would claim `scw` for Exoscale. A wrapped
		// continuation line carries no bullet marker and joins the item it
		// belongs to, which is what the accumulator below does.
		if bulletStart.MatchString(trimmed) {
			flushParagraph()
		}
		paragraph = append(paragraph, trimmed)
	}
	flushParagraph()
	// An unterminated fence is content nobody would otherwise read. Keep it
	// rather than drop it: dropping is how a reader stops finding things.
	if len(fence) > 0 {
		units = append(units, strings.Join(fence, "\n"))
	}
	return units
}

// capabilityPairsIn answers which (client, provider) pairs one unit names.
func capabilityPairsIn(unit string, providers []string) [][2]string {
	words := map[string]bool{}
	for _, w := range wordish.FindAllString(strings.ToLower(unit), -1) {
		words[w] = true
	}
	var clients []string
	for _, token := range capabilityClientTokens() {
		if words[token] {
			clients = append(clients, token)
		}
	}
	if len(clients) == 0 {
		return nil
	}
	var pairs [][2]string
	for _, provider := range providers {
		if !words[provider] {
			continue
		}
		for _, client := range clients {
			pairs = append(pairs, [2]string{client, provider})
		}
	}
	return pairs
}

// unownedCapabilityClaims reads one page and reports every claim no matrix row
// carries.
//
// A page that is not there is not a finding: `feint docs` regenerates a README
// outside this repository, where docs/clients.md does not exist. What keeps
// that from being a reader that skips itself is
// TestTheCapabilityChecksHaveASubjectToMeasure, which asserts here that the
// pages are read and that they carry blocks.
func unownedCapabilityClaims(path string, providers []string) []string {
	body, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []string{fmt.Sprintf("cannot read %s to check what it claims: %v", path, err)}
	}

	var problems []string
	for _, section := range generatedBlockBodies(string(body)) {
		for _, unit := range capabilityUnits(section.body) {
			for _, pair := range capabilityPairsIn(unit, providers) {
				client, provider := pair[0], pair[1]
				row := capabilityRowFor(provider, client)
				switch {
				case row == nil:
					problems = append(problems, fmt.Sprintf(
						"%s, in the generated `%s` block, puts %s and %s together and capabilityMatrix "+
							"carries no row for that pair: the sentence claims a capability nothing owns, "+
							"which is #592 exactly — %s",
						path, section.name, capabilityClientName(client), providerName(provider),
						quoteUnit(unit)))
				case row.Support == capabilityRefused &&
					!strings.Contains(strings.ToLower(unit), strings.ToLower(row.Marker)):
					problems = append(problems, fmt.Sprintf(
						"%s, in the generated `%s` block, puts %s and %s together and capabilityMatrix "+
							"refuses that pair: say why by naming %s, or stop claiming it — %s",
						path, section.name, capabilityClientName(client), providerName(provider),
						row.Marker, quoteUnit(unit)))
				}
			}
		}
	}
	return problems
}

// quoteUnit prints the offending span, shortened, so the message names the
// sentence rather than only the page.
func quoteUnit(unit string) string {
	flat := strings.Join(strings.Fields(unit), " ")
	const width = 120
	if len(flat) > width {
		flat = flat[:width] + "…"
	}
	return fmt.Sprintf("%q", flat)
}
