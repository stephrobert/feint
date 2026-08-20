package transcript_test

import (
	"testing"

	"github.com/stephrobert/feint/internal/trace"
	"github.com/stephrobert/feint/internal/transcript"
)

// The agents below were read off recordings of those clients driving this
// emulator through `feint proxy` on 2026-08-20, except the two noted. They are
// held here verbatim, because the table that reads them is a set of substrings
// and a substring table is exactly the kind of thing that keeps compiling while
// it stops matching.
//
// Two of them are the reason this test exists rather than a review: `scw` leads
// with the SDK and trails with the CLI, and `exo` spells itself "exocli". Both
// contradict the obvious guess, and both were guessed wrong in the first
// version of the table.
func TestEveryClientFamilyIsAttributedFromAMeasuredAgent(t *testing.T) {
	cases := []struct {
		agent, want string
	}{
		{"scaleway-sdk-go/v1.0.0-beta.36.0.20260605152809-7cb474471d04 (go1.26.4; linux; amd64) scaleway-cli/2.56.3",
			transcript.ClientSCW},
		{"exocli/1.95.1/7aaf76d0 egoscale/v3.1.36 (go1.26.4; linux/amd64)",
			transcript.ClientExo},
		{"scaleway-sdk-go/v1.0.0-beta.37.0.20260805122205-729b6c016cc8 (go1.26.6; linux; amd64) terraform-provider/2.81.0 terraform/1.15.4",
			transcript.ClientTerraform},
		{"oapi-cli/0.13.0; osc-sdk-c/00.19.00",
			transcript.ClientOAPI},
		// Not measured here, and said so: this is the agent
		// internal/providers/exoscale's own entry test carries, which is where
		// the pack learned to recognise the provider.
		{"Exoscale-Terraform-Provider/0.70.0 (abc1234) Terraform-SDK/2.31.0",
			transcript.ClientTerraform},
		// A bare SDK program is a different fact from a CLI, and keeping them
		// apart is what stops a ranking claiming somebody ran a command.
		{"scaleway-sdk-go/v1.0.0-beta.36 (go1.26.4; linux; amd64)",
			transcript.ClientSDK},
		{"curl/8.5.0", transcript.ClientUnknown},
	}
	for _, c := range cases {
		got := transcript.ClientOf(&trace.Exchange{Req: &trace.Message{Headers: map[string]string{"User-Agent": c.agent}}})
		if got != c.want {
			t.Errorf("agent %q was attributed to %q, want %q", c.agent, got, c.want)
		}
	}
}

// The whole point of a closed vocabulary: whatever a client puts in its agent,
// what comes out is one of the words this package declares.
func TestNoAgentEverComesBackVerbatim(t *testing.T) {
	known := map[string]bool{
		transcript.ClientTerraform: true, transcript.ClientOpenTofu: true,
		transcript.ClientSCW: true, transcript.ClientExo: true,
		transcript.ClientOAPI: true, transcript.ClientSDK: true,
		transcript.ClientUnknown: true,
	}
	// An agent carrying a hostname and a path, which is what a build in a
	// pipeline sends and what must never be echoed into a report.
	for _, agent := range []string{
		"terraform/1.15.4 (/home/someone/infra/prod) build-runner-07",
		"exocli/1.95.1 (/opt/deploy/secrets)",
		"something-nobody-here-has-seen/1.0 (host-42.internal)",
	} {
		got := transcript.ClientOf(&trace.Exchange{Req: &trace.Message{Headers: map[string]string{"User-Agent": agent}}})
		if !known[got] {
			t.Errorf("agent %q came back as %q, which is not one of the declared families", agent, got)
		}
	}
}

// An exchange with no request recorded, or no agent, is "unknown" rather than
// missing: a row whose client column is blank reads as a bug in the reader.
func TestAnExchangeWithNoAgentIsUnknownRatherThanEmpty(t *testing.T) {
	if got := transcript.ClientOf(&trace.Exchange{}); got != transcript.ClientUnknown {
		t.Errorf("an exchange with no request came back %q, want %q", got, transcript.ClientUnknown)
	}
	if got := transcript.ClientOf(&trace.Exchange{Req: &trace.Message{}}); got != transcript.ClientUnknown {
		t.Errorf("an exchange with no agent came back %q, want %q", got, transcript.ClientUnknown)
	}
}
