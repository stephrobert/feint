package cli

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The recommended Incus is the release the runtime proof installs, read from
// machine.IncusTested, never a second literal (#979). It was `[2]int{7, 2}` in
// doctor.go while the workflow installed whatever Zabbly published last and the
// station ran 7.6: three statements of one fact that nothing compared.
//
// TestTheRuntimeProofInstallsTheTestedIncus (tools/ci) holds the workflow to the
// same constant, and `feint docs --check` holds the pages to this table.
func TestTheRecommendedIncusIsTheTestedOne(t *testing.T) {
	if incusRecommended != machine.IncusTestedSeries() {
		t.Fatalf("incusRecommended is %v and machine.IncusTested is %s: the install page "+
			"recommends one release and the runtime proof installs another",
			incusRecommended, machine.IncusTested)
	}
	if want := versionText(incusRecommended[:]) + " recommended"; !strings.Contains(renderPrereq(""), want) {
		t.Errorf("the prerequisites table does not say %q:\n%s", want, renderPrereq(""))
	}
}
