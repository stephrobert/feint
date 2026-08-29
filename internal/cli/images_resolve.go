package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/stephrobert/feint/internal/core/machine"
)

// `feint images resolve <identifier>...` asks the providers' public listings
// what an opaque image identifier names, and prints the FEINT_BOOT_IMAGES
// declaration that makes it bootable here.
//
// It exists because a hardcoded image id in a published Terraform configuration
// is undecipherable to anyone without an account in the right region — and
// sometimes to them too: measured on 2026-08-25, the real Outscale answers zero
// images for ami-a3ca408c, ami-538af795 and ami-47899c77 to a valid account
// that sees 609 others. The public listings below answered all three, without
// credentials, because Outscale's reference page is historical.
//
// The lookups are deliberately here, in an explicit command, and never on the
// boot path: a create that waited on a third party's availability is the
// failure mode that cost the Exoscale Terraform provider a fork (an apply that
// splits between the emulator and a paid account). This command fetches, the
// operator declares, and the emulator works offline afterwards — the same
// shape as `mise run upstream:sync` feeding the drift scan.
//
// Sources, each measured without credentials on 2026-08-25:
//   - Outscale: the "Official OMIs Reference" documentation page (HTML, http
//     200, no auth). Historical — it lists withdrawn OMIs with their name and
//     deprecation date back to at least 2019, so it resolves identifiers the
//     live API no longer serves.
//   - Scaleway: the public marketplace API (JSON, http 200, no auth; an
//     unknown id answers a clean 404).
//   - Exoscale: the public template listing (JSON, http 200, no auth).
//     Private templates never appear there, and nothing public ever could
//     resolve them.
const (
	outscaleOMIsURL     = "https://docs.outscale.com/en/userguide/Official-OMIs-Reference.html"
	scalewayLocalImages = "https://api.scaleway.com/marketplace/v2/local-images/"
	scalewayImagesURL   = "https://api.scaleway.com/marketplace/v2/images?page_size=100"
	exoscaleTemplates   = "https://api-ch-gva-2.exoscale.com/v2/template"
)

// fetchListing is the network seam, injectable so the parsers are tested on
// fixtures and no test ever depends on a third party answering.
var fetchListing = func(url string) (status int, body []byte, err error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	// The reference page weighs ~540 KiB today; 8 MiB is well past every
	// listing and still refuses a runaway body.
	body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// # What the image server still publishes (#476)
//
// `resolve` exists to produce a declaration an operator pastes. Until 2026-08-29
// it printed one whose boot it could already know would fail: two of the three
// identifiers the surveyed stacks hardcode resolve to `ubuntu:18.04` and
// `debian:9`, and `images:` publishes neither. Replayed with that exact
// declaration on michaelcourcy/kasten-on-outscale: 29 applied, 0 machines
// started — the same figures as with no declaration at all. The declaration was
// honoured and bought nothing.
//
// The one command whose whole purpose is producing a working declaration was the
// one place that did not check whether the declaration works.

// imageStreamsIndex is the simplestreams product index the `images:` remote is
// built from — the same server `machine.SpecFor` names in every ImageSpec's
// Source, so this asks the thing that would refuse the build rather than a list
// written down beside it.
//
// Read rather than assumed: the index keys products by CODENAME
// (`ubuntu:jammy:amd64:cloud`) and carries the version aliases in each product's
// `aliases` field (`ubuntu/jammy/cloud,ubuntu/22.04/cloud`). A check keyed on
// `ubuntu:22.04:amd64:cloud` finds nothing and would report every working
// version as withdrawn, which is the first thing this reader was measured
// against.
const imageStreamsIndex = "https://images.linuxcontainers.org/streams/v1/images.json"

// publishedAliases answers the set of `family/version/variant` aliases the image
// server publishes today, and the versions each family still has.
//
// An error is an error, never an empty set: a server that could not be asked
// must not turn every declaration into "withdrawn", which is the three-outcomes
// rule imagesResolve already states for the listings above.
func publishedAliases(fetch func(string) (int, []byte, error)) (map[string]bool, map[string][]string, error) {
	status, body, err := fetch(imageStreamsIndex)
	if err != nil {
		return nil, nil, err
	}
	if status != http.StatusOK {
		return nil, nil, fmt.Errorf("the image server's index answered %d", status)
	}
	var doc struct {
		Products map[string]struct {
			Aliases string `json:"aliases"`
			Release string `json:"release"`
			Arch    string `json:"arch"`
			Variant string `json:"variant"`
		} `json:"products"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, nil, fmt.Errorf("reading the image server's index: %w", err)
	}
	aliases := map[string]bool{}
	versions := map[string][]string{}
	for _, product := range doc.Products {
		for _, alias := range strings.Split(product.Aliases, ",") {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				continue
			}
			aliases[alias] = true
			// family/version/variant — the shape SpecFor builds. Only the cloud
			// variant is collected for the suggestion list, because that is the
			// only one this emulator ever asks for.
			parts := strings.Split(alias, "/")
			if len(parts) == 3 && parts[2] == "cloud" {
				versions[parts[0]] = appendUnique(versions[parts[0]], parts[1])
			}
		}
	}
	if len(aliases) == 0 {
		return nil, nil, fmt.Errorf("the image server's index carries no aliases, so nothing here could be checked")
	}
	for family := range versions {
		sort.Strings(versions[family])
	}
	return aliases, versions, nil
}

func appendUnique(list []string, value string) []string {
	for _, held := range list {
		if held == value {
			return list
		}
	}
	return append(list, value)
}

// resolvedImage is what one lookup learned.
type resolvedImage struct {
	id     string
	name   string // what the listing calls it, verbatim
	source string // which listing answered
	ref    string // family:version derived from the name, "" when none derives
	login  string // the login the listing declares, when it declares one
}

// imagesResolve drives one lookup per identifier and prints what to declare.
//
// Three outcomes per identifier, kept distinct on purpose: found (the listing
// names it), absent (the listing answered and does not hold it), and failed
// (the listing could not be asked — which must never read as absent). Exit
// codes follow the repository's convention: 0 when every identifier resolved,
// 1 when a listing could not be asked, 2 when one is in no listing — the same
// "the world needs triage" the other gates use.
func imagesResolve(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: feint images resolve <identifier>...")
		fmt.Fprintln(stderr, "  looks each identifier up in the providers' public listings (network, no account)")
		fmt.Fprintln(stderr, "  and prints the FEINT_BOOT_IMAGES declaration that makes it bootable here")
		return 1
	}

	cache := map[string][]byte{}
	fetch := func(url string) (int, []byte, error) {
		if body, ok := cache[url]; ok {
			return http.StatusOK, body, nil
		}
		status, body, err := fetchListing(url)
		if err == nil && status == http.StatusOK {
			cache[url] = body
		}
		return status, body, err
	}

	// Asked once, before the loop, and never fatal on its own: an image server
	// that could not be reached leaves `withdrawn` nil, and the loop then prints
	// exactly what it printed before this check existed. A network failure must
	// not turn a working declaration into a refusal (#476).
	aliases, families, streamsErr := publishedAliases(fetch)
	if streamsErr != nil {
		fmt.Fprintf(stdout, "note: the image server's index could not be read (%v),\n", streamsErr)
		fmt.Fprintln(stdout, "      so whether each version below is still published was not checked")
	}

	failed, absent, unbuildable := false, false, false
	var declarations []string
	for _, id := range args {
		res, found, err := resolveOne(id, fetch)
		fmt.Fprintf(stdout, "%s\n", id)
		if err != nil {
			// A listing that could not be asked is not a listing that lacks the
			// id: said as a failure, never counted as an absence.
			fmt.Fprintf(stdout, "  could not ask the public listings: %v\n", err)
			failed = true
			continue
		}
		if !found {
			absent = true
			fmt.Fprintf(stdout, "  in none of the public listings consulted (%s)\n", strings.Join(sourcesFor(id), "; "))
			fmt.Fprintln(stdout, "  what remains: the stack's own docs, or whoever created the image;")
			fmt.Fprintf(stdout, "  once known, declare it: FEINT_BOOT_IMAGES='%s=<family>:<version>' (families: %s)\n",
				id, strings.Join(machine.Families(), ", "))
			continue
		}
		fmt.Fprintf(stdout, "  %s: %s\n", res.source, res.name)
		if res.ref == "" {
			fmt.Fprintln(stdout, "  the name yields no family:version this emulator could build")
			continue
		}
		if _, buildable := machine.SpecFor(res.ref); !buildable {
			fmt.Fprintf(stdout, "  → %s — no recipe for this family here (families: %s)\n",
				res.ref, strings.Join(machine.Families(), ", "))
			continue
		}
		// The version the reference names may be one the image server has
		// withdrawn, and then the declaration below cannot boot: the build fails
		// on "The requested image couldn't be found" and the machine never
		// starts (#476). Kept out of the paste line rather than printed with a
		// warning beside it, because the line exists to be pasted.
		if spec, ok := machine.SpecFor(res.ref); ok && aliases != nil {
			alias := strings.TrimPrefix(spec.Source, "images:")
			if !aliases[alias] {
				unbuildable = true
				fmt.Fprintf(stdout, "  → %s — the image server no longer publishes %s, so this declaration\n",
					res.ref, alias)
				fmt.Fprintln(stdout, "    would refuse to boot; it is left out of the line below.")
				family, _, _ := strings.Cut(res.ref, ":")
				if published := families[family]; len(published) > 0 {
					fmt.Fprintf(stdout, "    %s versions published today: %s\n",
						family, strings.Join(published, ", "))
					fmt.Fprintln(stdout, "    naming one of them is your call, not this command's: the identifier")
					fmt.Fprintln(stdout, "    means what the cloud says it means, and substituting a version here")
					fmt.Fprintln(stdout, "    would be the silent replacement #83 closed.")
				}
				continue
			}
		}
		entry := id + "=" + res.ref
		if res.login != "" {
			entry += "@" + res.login
		}
		fmt.Fprintf(stdout, "  → %s\n", res.ref)
		declarations = append(declarations, entry)
	}

	if len(declarations) > 0 {
		fmt.Fprintf(stdout, "\ndeclare and restart:\n  FEINT_BOOT_IMAGES='%s' feint serve ...\n",
			strings.Join(declarations, ","))
	}
	switch {
	case failed:
		return 1
	case absent:
		return 2
	case unbuildable && len(declarations) == 0:
		// Every identifier resolved to a version nothing can build, so this run
		// produced no line to paste. Exit 2, the same "the world needs triage"
		// the absent case uses — a zero here would let a script pipe an empty
		// declaration and call it a success (#476, shape 3).
		fmt.Fprintln(stdout, "\nnothing to declare: every identifier resolved to a version the image server")
		fmt.Fprintln(stdout, "no longer publishes.")
		return 2
	}
	return 0
}

// resolveOne picks the listings an identifier's shape can appear in. An OMI id
// is Outscale vocabulary; a UUID could be a Scaleway local image or an Exoscale
// template, so both are asked; anything else has no public listing wired.
func resolveOne(id string, fetch func(string) (int, []byte, error)) (resolvedImage, bool, error) {
	switch {
	case looksLikeOMI(id):
		return resolveOutscaleOMI(id, fetch)
	case looksLikeImageUUID(id):
		res, found, err := resolveScalewayImage(id, fetch)
		if err != nil || found {
			return res, found, err
		}
		return resolveExoscaleTemplate(id, fetch)
	}
	return resolvedImage{}, false, nil
}

// sourcesFor names, for the not-found message, which listings were consulted.
func sourcesFor(id string) []string {
	switch {
	case looksLikeOMI(id):
		return []string{"Outscale official OMIs reference, which lists withdrawn OMIs too — this one never was one, or predates the page"}
	case looksLikeImageUUID(id):
		return []string{"Scaleway marketplace", "Exoscale public templates — a private template never appears there"}
	}
	return []string{"no public listing is wired for this identifier's shape"}
}

func looksLikeOMI(id string) bool {
	if len(id) != 12 || !strings.HasPrefix(id, "ami-") {
		return false
	}
	for _, c := range id[4:] {
		if !unicode.Is(unicode.ASCII_Hex_Digit, c) {
			return false
		}
	}
	return true
}

func looksLikeImageUUID(id string) bool { return len(id) == 36 && strings.Count(id, "-") == 4 }

// resolveOutscaleOMI reads the reference page: rows of (id, name, published,
// replacement, deprecated) per region. The name is the cell right after the id
// when the id opens a row; the same id in a replacement cell is followed by a
// date, which is how the two positions are told apart.
func resolveOutscaleOMI(id string, fetch func(string) (int, []byte, error)) (resolvedImage, bool, error) {
	status, body, err := fetch(outscaleOMIsURL)
	if err != nil {
		return resolvedImage{}, false, err
	}
	if status != http.StatusOK {
		return resolvedImage{}, false, fmt.Errorf("the Outscale reference page answered %d", status)
	}
	name, found := outscaleNameFor(string(body), id)
	if !found {
		return resolvedImage{}, false, nil
	}
	return resolvedImage{
		id: id, name: name,
		source: "Outscale official OMIs reference",
		ref:    refFromDashedName(name),
	}, true, nil
}

// outscaleNameFor scans the page's cell text for the row the id opens.
//
// The page is HTML and this is a parser of last resort — Outscale publishes no
// machine-readable equivalent (the API answers 401 without credentials,
// measured). It anchors on content, not markup: a token equal to the id,
// followed by a token that starts with a letter, is the (id, name) pair; the
// id in a replacement cell is followed by a date and never matches.
func outscaleNameFor(page, id string) (string, bool) {
	tokens := cellTokens(page)
	for i, token := range tokens {
		if token != id || i+1 >= len(tokens) {
			continue
		}
		next := tokens[i+1]
		if next == "" || !unicode.IsLetter(rune(next[0])) {
			continue
		}
		return next, true
	}
	return "", false
}

// cellTokens strips markup and returns the visible cell texts.
func cellTokens(page string) []string {
	var tokens []string
	var cell strings.Builder
	inTag := false
	flush := func() {
		if text := strings.TrimSpace(cell.String()); text != "" {
			tokens = append(tokens, text)
		}
		cell.Reset()
	}
	for _, r := range page {
		switch {
		case r == '<':
			inTag = true
			flush()
		case r == '>':
			inTag = false
		case !inTag:
			cell.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// refFromDashedName turns an Outscale OMI name into a family:version ref:
// "Ubuntu-22.04-2023.12.04-0" yields ubuntu:22.04, "Debian-9-2019.11.29-0"
// yields debian:9. A name whose second segment is not a version — or that has
// none — yields nothing, and the caller says so instead of inventing one.
func refFromDashedName(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) < 2 || parts[1] == "" || !unicode.IsDigit(rune(parts[1][0])) {
		return ""
	}
	return strings.ToLower(parts[0]) + ":" + parts[1]
}

// resolveScalewayImage asks the public marketplace: the local image for the
// UUID (a clean 404 when unknown), then the image list for the label's human
// name, which carries family and version ("Ubuntu 24.04 Noble Numbat").
func resolveScalewayImage(id string, fetch func(string) (int, []byte, error)) (resolvedImage, bool, error) {
	status, body, err := fetch(scalewayLocalImages + id)
	if err != nil {
		return resolvedImage{}, false, err
	}
	if status == http.StatusNotFound {
		return resolvedImage{}, false, nil
	}
	if status != http.StatusOK {
		return resolvedImage{}, false, fmt.Errorf("the Scaleway marketplace answered %d", status)
	}
	var local struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal(body, &local); err != nil {
		return resolvedImage{}, false, fmt.Errorf("unreadable Scaleway local-image answer: %w", err)
	}
	if local.Label == "" {
		return resolvedImage{}, false, errors.New("the Scaleway local-image answer carries no label")
	}

	name := local.Label
	if status, body, err := fetch(scalewayImagesURL); err == nil && status == http.StatusOK {
		var list struct {
			Images []struct {
				Label string `json:"label"`
				Name  string `json:"name"`
			} `json:"images"`
		}
		if json.Unmarshal(body, &list) == nil {
			for _, img := range list.Images {
				if img.Label == local.Label && img.Name != "" {
					name = img.Name
					break
				}
			}
		}
	}
	return resolvedImage{
		id: id, name: name,
		source: "Scaleway marketplace (label " + local.Label + ")",
		ref:    refFromSpacedName(name),
	}, true, nil
}

// refFromSpacedName reads "Family Version …" marketplace names:
// "Debian 13 (Trixie)" yields debian:13. Same contract as refFromDashedName.
func refFromSpacedName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 || !unicode.IsDigit(rune(parts[1][0])) {
		return ""
	}
	return strings.ToLower(parts[0]) + ":" + parts[1]
}

// resolveExoscaleTemplate reads the public template listing, which carries
// family, version and the template's own default user — the login rides the
// declaration as @login, because at Exoscale it belongs to the template.
func resolveExoscaleTemplate(id string, fetch func(string) (int, []byte, error)) (resolvedImage, bool, error) {
	status, body, err := fetch(exoscaleTemplates)
	if err != nil {
		return resolvedImage{}, false, err
	}
	if status != http.StatusOK {
		return resolvedImage{}, false, fmt.Errorf("the Exoscale template listing answered %d", status)
	}
	var list struct {
		Templates []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Family  string `json:"family"`
			Version string `json:"version"`
			User    string `json:"default-user"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return resolvedImage{}, false, fmt.Errorf("unreadable Exoscale template answer: %w", err)
	}
	for _, t := range list.Templates {
		if t.ID != id {
			continue
		}
		ref := ""
		if t.Family != "" && t.Version != "" {
			ref = strings.ToLower(t.Family) + ":" + t.Version
		}
		return resolvedImage{
			id: id, name: t.Name,
			source: "Exoscale public templates",
			ref:    ref, login: t.User,
		}, true, nil
	}
	return resolvedImage{}, false, nil
}
