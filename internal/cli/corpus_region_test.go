package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A corpus is replayed against an emulator serving the endpoint it was recorded
// from, and the manifest is where that endpoint is named.
//
// At Outscale and Exoscale a region is not a property of the API surface, it is
// which endpoint the client was pointed at, and the pack refuses a create that
// names a subregion its deployment does not publish — knownSubregion, the #269
// invariant, which exists so that a create and the catalogue cannot disagree
// about which zones exist. A cloudgouv-eu-west-1 recording replayed against an
// eu-west-2 emulator therefore answers 400 to its own CreateSubnet, and to
// everything downstream of it: measured on 2026-08-21 against a real account,
// about a hundred findings and not one of them a defect of the emulator.
//
// Both halves are here, and the second is what makes the first mean anything:
// with the region named the recording replays clean, and with the entry
// stripped of it the same recording is refused. Without the region being read
// from the manifest the two halves are identical and this test cannot fail.
func TestACorpusIsReplayedInTheRegionItNames(t *testing.T) {
	const exchanges = `{"seq":1,"method":"POST","path":"/api/v1/CreateNet","host":"api.example.com","operation":"osc/Client.CreateNet","provider":"outscale","status":200,"mounted":true,"req":{"body":{"IpRange":"198.18.0.0/16"}},"res":{"body":{"Net":{"NetId":"vpc-00000001","IpRange":"198.18.0.0/16","State":"available","Tenancy":"default","Tags":[],"DhcpOptionsSetId":"dopt-00000002"}}}}
{"seq":2,"method":"POST","path":"/api/v1/CreateSubnet","host":"api.example.com","operation":"osc/Client.CreateSubnet","provider":"outscale","status":200,"mounted":true,"req":{"body":{"IpRange":"198.18.1.0/24","NetId":"vpc-00000001","SubregionName":"cloudgouv-eu-west-1a"}},"res":{"body":{"Subnet":{"SubnetId":"subnet-00000003","NetId":"vpc-00000001","IpRange":"198.18.1.0/24","State":"available","SubregionName":"cloudgouv-eu-west-1a","AvailableIpsCount":251,"MapPublicIpOnLaunch":false,"Tags":[]}}}}
`

	fixture := func(t *testing.T, region string) (dir, accepted string) {
		t.Helper()
		dir = filepath.Join(t.TempDir(), "corpus")
		if err := os.MkdirAll(filepath.Join(dir, "outscale"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "outscale", "sovereign.jsonl"), []byte(exchanges), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir, writeAccepted(t, corpusAcceptance{
			WarnAfterDays: 180,
			Recorded: []corpusRecording{{
				File: "outscale/sovereign.jsonl", At: "2026-08-21",
				Client: "oapi-cli", Cloud: "Outscale cloudgouv-eu-west-1", Region: region,
			}},
		})
	}

	// The subject is the divergences, not the exit code: a two-exchange fixture
	// reaches no order invariant, so both runs below are refused by the rule
	// that a declared comparison must actually run. Reading the exit code here
	// would compare two failures for the same unrelated reason and prove
	// nothing — the shape of mistake this repository files issues about.
	dir, accepted := fixture(t, "cloudgouv-eu-west-1")
	named, namedErr, _ := runCorpusGate(t, dir, accepted)
	if !strings.Contains(named, "\n0 divergent finding(s) nothing accepts") {
		t.Fatalf("a corpus naming its own region did not replay clean.\nstdout:\n%s\nstderr:\n%s",
			named, namedErr)
	}

	// The falsification, run rather than argued: with no region named, the pack
	// serves its default and refuses the subregion the recording carries.
	silentDir, silentAccepted := fixture(t, "")
	silent, silentErr, _ := runCorpusGate(t, silentDir, silentAccepted)
	if strings.Contains(silent, "\n0 divergent finding(s) nothing accepts") {
		t.Fatalf("the same corpus replayed clean with no region named, so nothing in this test "+
			"depends on the region being read from the manifest.\nstdout:\n%s\nstderr:\n%s",
			silent, silentErr)
	}
	// Named, because "it went red" is not the claim: the claim is that the
	// subregion is what it went red on.
	if !strings.Contains(silent+silentErr, "osc/Client.CreateSubnet") {
		t.Errorf("the run without a region diverged somewhere other than the subregion:\n%s%s", silent, silentErr)
	}
}
