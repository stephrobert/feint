package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// The closed-schema figure in the README, generated rather than typed.
//
// The paragraph arguing for `--contracts` said "643 of Outscale's 650 schemas
// declare additionalProperties: false" while contracts/outscale.json counted
// 648 of 655 — the route-count failure again, in the one sentence whose whole
// point is that the figure is the provider's own declaration. Nobody lied; the
// artefact had moved and the prose had not. So the sentence is spliced from
// the artefact the contract gate reads, and `feint docs --check` exits 2 when
// they disagree.
//
// The counts are rendered only for a provider whose closedPolicy is
// "declared". Under an "assumed" policy the closed flags record this project's
// own --assume-closed extraction flag, and printing them as the provider's
// would dress our assumption as their statement — the same refusal
// renderContracts documents for the policy table.
//
// TestTheClosedSchemaFigureComesFromTheArtefact and
// TestTheClosedSchemaFigureRefusesAnAssumedPolicy fail without this.

const (
	closedStartMarker = "<!-- closed:start -->"
	closedEndMarker   = "<!-- closed:end -->"
)

// closedContract is the slice of an API description artefact this renderer
// reads: the provider, who decided the schemas are closed, and the flags.
type closedContract struct {
	Provider     string `json:"provider"`
	ClosedPolicy string `json:"closedPolicy"`
	Schemas      map[string]struct {
		Closed bool `json:"closed"`
	} `json:"schemas"`
}

// renderClosedSchemas builds the paragraph from the artefact.
func renderClosedSchemas(contractPath string) (string, error) {
	raw, err := os.ReadFile(contractPath) //nolint:gosec // a path this repository owns
	if err != nil {
		return "", fmt.Errorf("read the API description: %w", err)
	}
	var doc closedContract
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("decode %s: %w", contractPath, err)
	}
	if doc.ClosedPolicy != "declared" {
		return "", fmt.Errorf(
			"%s: closedPolicy is %q — a closed-schema count under any policy but "+
				"\"declared\" measures this project's own --assume-closed flag, not "+
				"anything the provider wrote down", contractPath, doc.ClosedPolicy)
	}
	closed := 0
	for _, schema := range doc.Schemas {
		if schema.Closed {
			closed++
		}
	}
	if closed == 0 || len(doc.Schemas) == 0 {
		return "", fmt.Errorf("%s: no closed schema counted; the artefact is empty "+
			"or its shape moved, and a zero here would read as a claim", contractPath)
	}
	name := strings.ToUpper(doc.Provider[:1]) + doc.Provider[1:]
	return fmt.Sprintf("%s\n\nOn top of that, every response is validated against "+
		"the provider's own OpenAPI document when the emulator runs with "+
		"`--contracts`. That is not tidiness: %d of %s's %d schemas declare "+
		"`additionalProperties: false`, so a field they do not define is a "+
		"violation *they* wrote down.\n",
		docsGenerated, closed, name, len(doc.Schemas)), nil
}
