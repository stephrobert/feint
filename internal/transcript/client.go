package transcript

import (
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/trace"
)

// Clients are the families a recorded call can be attributed to.
//
// A closed vocabulary, and that is a hygiene decision rather than a tidiness
// one. The obvious implementation prints the User-Agent, which is the one
// request header the proxy's allowlist writes down in full — and a User-Agent
// carries whatever the client's build put in it: a version, a commit, a
// toolchain, and in some builds a path. docs/proxy.md's rule is that nothing
// derived from a recording enters a repository or an issue without being
// sanitised, and a ranking of what to implement next is exactly the kind of
// thing somebody pastes into an issue.
//
// So the answer is one of these words and the raw string never leaves the
// reader. TestTheObservedReportNamesNoRawUserAgent holds it.
const (
	ClientTerraform = "terraform"
	ClientOpenTofu  = "opentofu"
	ClientSCW       = "scw"
	ClientExo       = "exo"
	ClientOAPI      = "oapi-cli"
	ClientOCTL      = "octl"
	ClientSDK       = "sdk"
	ClientUnknown   = "unknown"
)

// agents maps a substring of a User-Agent to the family it names, first match
// winning.
//
// Every entry marked *measured* was read off a recording of that client driving
// this emulator through `feint proxy` on 2026-08-20, not off its documentation.
// Two of them contradict what a reasonable guess would have written, which is
// why the table is measured:
//
//   - **`scw` does not announce itself first.** Its agent is
//     "scaleway-sdk-go/… (go1.26.4; linux; amd64) scaleway-cli/2.56.3" — the SDK
//     leads and the CLI trails. A table keyed on a prefix would have attributed
//     every `scw` call to a bare SDK program.
//   - **`exo` is "exocli", not "exoscale-cli"** on the wire:
//     "exocli/1.95.1/7aaf76d0 egoscale/v3.1.36 (go1.26.4; linux/amd64)". The
//     spelling with the dash exists too — internal/providers/exoscale's own
//     entry test carries it — so both are here.
//
// Order matters more than any single entry: terraform-provider-scaleway
// announces "scaleway-sdk-go/… terraform-provider/2.81.0 terraform/1.15.4", so
// a table that checked "scaleway-cli" or "scaleway-sdk-go" first would file a
// plan's ninety calls under a CLI nobody ran. Terraform is therefore checked
// before every SDK and every CLI.
var agents = []struct{ needle, family string }{
	// OpenTofu announces itself as OpenTofu and keeps Terraform's provider
	// protocol, so it has to be tested before "terraform".
	{"opentofu", ClientOpenTofu},
	// measured: terraform-provider-scaleway 2.81.0 under terraform 1.15.4, and
	// Exoscale-Terraform-Provider/0.70.0 in internal/providers/exoscale.
	{"terraform", ClientTerraform},
	{"scaleway-cli", ClientSCW}, // measured: scw 2.56.3
	{"exocli", ClientExo},       // measured: exo 1.95.1
	{"exoscale-cli", ClientExo}, // the spelling internal/providers/exoscale's entry test carries
	// measured 2026-08-25 through `feint proxy`: "octl/v0.0.31", and nothing
	// else — unlike scw and exo, octl announces neither its SDK nor its
	// toolchain, so this is the whole header. It comes before the SDK entries
	// below for the same reason terraform does: octl carries osc-sdk-go.
	{"octl", ClientOCTL}, // measured: octl v0.0.31
	// The two archived CLIs, kept for the corpora recorded with them (#460):
	// outscale/oapi-cli and outscale/osc-cli are read-only upstream, so nothing
	// new will ever carry these agents, and everything already recorded does.
	{"oapi-cli", ClientOAPI}, // measured: oapi-cli 0.13.0
	{"osc-cli", ClientOAPI},  // Outscale's other CLI, archived upstream
	{"scaleway-sdk-go", ClientSDK},
	{"egoscale", ClientSDK},
	{"osc-sdk-c", ClientSDK},
	{"osc-sdk-go", ClientSDK},
}

// ClientOf attributes one recorded exchange to a client family.
//
// Never the raw agent string: see the vocabulary above. An exchange whose agent
// this table does not recognise is "unknown", which is a fact a reader can act
// on — add the family — where a printed agent would be an inventory leak nobody
// asked for.
func ClientOf(x *trace.Exchange) string {
	if x.Req == nil {
		return ClientUnknown
	}
	for name, value := range x.Req.Headers {
		if strings.EqualFold(name, "user-agent") {
			return familyOf(value)
		}
	}
	return ClientUnknown
}

// familyOf attributes one User-Agent string.
func familyOf(agent string) string {
	lower := strings.ToLower(agent)
	for _, a := range agents {
		if strings.Contains(lower, a.needle) {
			return a.family
		}
	}
	return ClientUnknown
}

// Needle returns the substring of a User-Agent that named its family, and ""
// for an agent this table does not recognise.
//
// Exported for internal/corpus, which writes it into a sanitised transcript in
// place of the agent itself. A User-Agent carries whatever the build put in it,
// so a committed corpus must not hold one; but dropping it outright would take
// the client column of `feint coverage --observed` with it, and the ranking of
// what a real client called is the reason that corpus exists. The needle is the
// least that keeps the attribution answering the same word — and it comes from
// this table, which is to say from this repository, not from the recording.
//
// TestTheClientOfASanitisedExchangeIsStillNamed (internal/corpus) fails without
// this, and it is the same table on both sides deliberately: a second list of
// spellings would drift from the one that does the attributing.
func Needle(agent string) string {
	lower := strings.ToLower(agent)
	for _, a := range agents {
		if strings.Contains(lower, a.needle) {
			return a.needle
		}
	}
	return ""
}

// Needles returns every spelling this table recognises.
//
// For internal/corpus's scan, which has to tell a User-Agent it wrote itself
// from one that survived a sanitisation. Reading the table rather than keeping
// a second list is the point: the day a family is added here, the scan accepts
// it, and no artefact needs regenerating for the two to agree.
func Needles() []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.needle)
	}
	return out
}

// ClientsOf names every family that produced a call among these exchanges,
// sorted and comma-joined, so a ranking row says who wanted the operation.
func ClientsOf(exs []*trace.Exchange) string {
	seen := map[string]bool{}
	for _, x := range exs {
		seen[ClientOf(x)] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
