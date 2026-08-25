package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The fixtures mirror the shapes measured on 2026-08-25, without which these
// parsers would be tested against a page nobody serves. The Outscale excerpt
// keeps the trait the parser must survive: an id can appear in the replacement
// cell of an older row — followed by a date — before the row it opens.
const outscaleFixture = `
<tr>
<td class="tableblock"><p class="tableblock"><strong>ami-cd8d714e</strong></p></td>
<td class="tableblock"><p class="tableblock">Ubuntu-22.04-2023.02.21-0</p></td>
<td class="tableblock"><p class="tableblock">2023-02-21</p></td>
<td class="tableblock"><p class="tableblock">ami-a3ca408c</p></td>
<td class="tableblock"><p class="tableblock">2024-01-04</p></td>
</tr>
<tr>
<td class="tableblock"><p class="tableblock"><strong>ami-a3ca408c</strong></p></td>
<td class="tableblock"><p class="tableblock">Ubuntu-22.04-2023.12.04-0</p></td>
<td class="tableblock"><p class="tableblock">2024-01-04</p></td>
<td class="tableblock"><p class="tableblock">ami-70f5d073</p></td>
<td class="tableblock"><p class="tableblock">2025-04-16</p></td>
</tr>
<tr>
<td class="tableblock"><p class="tableblock"><strong>ami-47899c77</strong></p></td>
<td class="tableblock"><p class="tableblock">Debian-9-2019.11.29-0</p></td>
<td class="tableblock"><p class="tableblock">2019-11-29</p></td>
</tr>
<tr>
<td class="tableblock"><p class="tableblock"><strong>ami-69129fc8</strong></p></td>
<td class="tableblock"><p class="tableblock">RHEL-9-2023.11.17-0</p></td>
<td class="tableblock"><p class="tableblock">2024-01-04</p></td>
</tr>`

const exoscaleFixture = `{"templates":[
  {"id":"d4296d2e-3722-4b2e-ba4a-2a59918b9395","name":"Linux Ubuntu 22.04 LTS 64-bit",
   "family":"ubuntu","version":"22.04","default-user":"ubuntu"}]}`

const scalewayLocalFixture = `{"id":"08176977-2d0e-4db0-a604-f81daa390f81","zone":"fr-par-1","label":"ubuntu_noble"}`
const scalewayImagesFixture = `{"images":[{"label":"ubuntu_noble","name":"Ubuntu 24.04 Noble Numbat"}]}`

// withListings replaces the network seam for one test, mapping URL prefixes to
// canned answers. A URL outside the map answers 404, the clean absence the real
// Scaleway marketplace was measured to give.
func withListings(t *testing.T, answers map[string]string) {
	t.Helper()
	previous := fetchListing
	t.Cleanup(func() { fetchListing = previous })
	fetchListing = func(url string) (int, []byte, error) {
		for prefix, body := range answers {
			if strings.HasPrefix(url, prefix) {
				return http.StatusOK, []byte(body), nil
			}
		}
		return http.StatusNotFound, []byte(`{"type":"not_found"}`), nil
	}
}

func TestResolveReadsTheOutscaleReferencePage(t *testing.T) {
	withListings(t, map[string]string{outscaleOMIsURL: outscaleFixture})
	var out, errOut bytes.Buffer

	// The planted witnesses come first: a control that will later report an
	// absence has to prove it can find, and these three are the identifiers the
	// live Outscale API no longer serves (measured: rc=0, zero images).
	code := imagesResolve([]string{"ami-a3ca408c", "ami-47899c77", "ami-69129fc8"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, want 0 when every identifier resolves\n%s%s", code, out.String(), errOut.String())
	}
	for _, needle := range []string{
		// The id that also sits in an older row's replacement cell must take
		// its name from the row it opens, not a date from the cell it fills.
		"Ubuntu-22.04-2023.12.04-0",
		"Debian-9-2019.11.29-0",
		// A withdrawn version still resolves; making it bootable is the build's
		// business, and its failure names the source.
		"debian:9",
		// A family without a recipe is said, with the families that have one.
		"rhel:9",
		"no recipe",
		// The final line is the exact declaration to paste.
		"FEINT_BOOT_IMAGES='ami-a3ca408c=ubuntu:22.04,ami-47899c77=debian:9'",
	} {
		if !strings.Contains(out.String(), needle) {
			t.Errorf("output does not carry %q\n%s", needle, out.String())
		}
	}
}

func TestResolveDistinguishesAbsentFromUnaskable(t *testing.T) {
	t.Run("an identifier no listing holds is absent, exit 2", func(t *testing.T) {
		withListings(t, map[string]string{outscaleOMIsURL: outscaleFixture})
		var out, errOut bytes.Buffer
		if code := imagesResolve([]string{"ami-00c0ffee"}, &out, &errOut); code != 2 {
			t.Fatalf("exit %d, want 2 for an identifier in no listing\n%s", code, out.String())
		}
		if !strings.Contains(out.String(), "FEINT_BOOT_IMAGES='ami-00c0ffee=<family>:<version>'") {
			t.Errorf("the absence does not say what to do next\n%s", out.String())
		}
	})

	t.Run("a listing that cannot be asked is a failure, exit 1, never an absence", func(t *testing.T) {
		previous := fetchListing
		t.Cleanup(func() { fetchListing = previous })
		fetchListing = func(string) (int, []byte, error) {
			return 0, nil, errors.New("dial tcp: no route to host")
		}
		var out, errOut bytes.Buffer
		if code := imagesResolve([]string{"ami-a3ca408c"}, &out, &errOut); code != 1 {
			t.Fatalf("exit %d, want 1 when the listing could not be asked\n%s", code, out.String())
		}
		if strings.Contains(out.String(), "in none of the public listings") {
			t.Errorf("a network failure was reported as an absence\n%s", out.String())
		}
	})
}

func TestResolveReadsTheScalewayAndExoscaleListings(t *testing.T) {
	withListings(t, map[string]string{
		scalewayLocalImages + "08176977": scalewayLocalFixture,
		scalewayImagesURL:                scalewayImagesFixture,
		exoscaleTemplates:                exoscaleFixture,
	})
	var out, errOut bytes.Buffer

	code := imagesResolve([]string{
		"08176977-2d0e-4db0-a604-f81daa390f81", // Scaleway local image (witness measured 2026-08-25)
		"d4296d2e-3722-4b2e-ba4a-2a59918b9395", // Exoscale public template: the marketplace 404s, the template list answers
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s%s", code, out.String(), errOut.String())
	}
	for _, needle := range []string{
		"Ubuntu 24.04 Noble Numbat",
		"Linux Ubuntu 22.04 LTS 64-bit",
		// The Exoscale login rides the declaration: at Exoscale it belongs to
		// the template, and dropping it hands out a machine nobody can enter.
		"d4296d2e-3722-4b2e-ba4a-2a59918b9395=ubuntu:22.04@ubuntu",
		"08176977-2d0e-4db0-a604-f81daa390f81=ubuntu:24.04",
	} {
		if !strings.Contains(out.String(), needle) {
			t.Errorf("output does not carry %q\n%s", needle, out.String())
		}
	}
}

func TestResolveWithoutIdentifiersExplainsItself(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := imagesResolve(nil, &out, &errOut); code != 1 {
		t.Fatalf("exit %d, want a usage refusal", code)
	}
	if !strings.Contains(errOut.String(), "feint images resolve") {
		t.Errorf("the usage does not name the command: %s", errOut.String())
	}
}

// A guard against the trap the harness memo records: an instrument that
// under-reports. The tokeniser is what every lookup stands on, so it is pinned
// on the exact cell layout the real page serves.
func TestCellTokensSurviveTheReferenceMarkup(t *testing.T) {
	got := cellTokens(`<td><p><strong>ami-cd8d714e</strong></p></td>` + "\n" + `<td><p>Ubuntu-22.04</p></td>`)
	want := fmt.Sprintf("%v", []string{"ami-cd8d714e", "Ubuntu-22.04"})
	if fmt.Sprintf("%v", got) != want {
		t.Fatalf("tokens %v, want %s", got, want)
	}
}
