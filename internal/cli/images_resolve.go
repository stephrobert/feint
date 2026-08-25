package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

	failed, absent := false, false
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
