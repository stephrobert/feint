package exoscale_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/providers/exoscale"
)

// SOS is composed client-side and never reaches this emulator — both surveyed
// shapes proved it (#262, #284): an aws provider pointed at sos-<zone>.exo.io
// (403 on fake credentials, at the real endpoint) and an S3-on-SOS state
// backend that terraform init contacts before any resource exists. The #284
// triage declined serving SOS for exactly that reason, so naming the contact
// in the stack's own text is the only warning that can exist at all.
func TestStackHazardsNameTheSOSEscape(t *testing.T) {
	pack := exoscale.New(nil)

	clean := `
resource "exoscale_compute_instance" "local" {
  zone = "ch-dk-2"
  type = "standard.medium"
}
`
	if warnings := pack.StackHazards(clean); len(warnings) != 0 {
		t.Fatalf("a stack the emulator fully serves was warned about: %v", warnings)
	}

	for _, escape := range []string{
		`endpoint = "https://sos-ch-gva-2.exo.io"`,         // the eu-data-platform state backend
		`endpoints { s3 = "https://sos-de-fra-1.exo.io" }`, // the platform stack's aws provider
	} {
		warnings := pack.StackHazards(escape)
		if len(warnings) == 0 {
			t.Fatalf("a real *.exo.io host went unnamed; those requests never reach this emulator: %s", escape)
		}
		if !strings.Contains(warnings[0], ".exo.io") || !strings.Contains(warnings[0], "SOS") {
			t.Fatalf("the warning names neither the host family nor the product: %q", warnings[0])
		}
	}
}
