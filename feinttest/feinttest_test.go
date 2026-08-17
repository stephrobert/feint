package feinttest_test

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/stephrobert/feint/feinttest"
)

// requireOptIn keeps these out of a plain `go test ./...`: they pull a published
// image, and `mise run check` is offline by contract. The CI sets the variable
// on every leg, so the coverage is unchanged — the network is now asked for
// rather than assumed.
func requireOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("FEINT_TESTCONTAINER") == "" {
		t.Skip("feinttest: set FEINT_TESTCONTAINER=1 to run the container tests (they pull a published image)")
	}
}

// The package's own promise, exercised: a test asks for a cloud and gets one.
//
// It skips when no container runtime is installed, which is the behaviour under
// test as much as the rest — a suite that cannot find Docker has learned nothing
// about the code, and failing there teaches contributors to distrust it.
//
// It pulls the published image rather than building one, deliberately: what is
// being proven is that the artefact somebody else would consume works, and an
// image built locally proves something nobody can reproduce.
func TestStartHandsBackAnEmulatorThatAnswers(t *testing.T) {
	requireOptIn(t)
	cloud := feinttest.Start(t)

	res, err := http.Get(cloud.Endpoint() + "/_feint/health") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("the endpoint does not answer: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	var health struct {
		Providers []string `json:"providers"`
		Machines  string   `json:"machines"`
	}
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		t.Fatalf("decode the health answer: %v", err)
	}
	if len(health.Providers) != 3 {
		t.Errorf("the emulator serves %v, want the three packs", health.Providers)
	}
	// Control plane only, and the package says so in its own documentation: a
	// test that needs real machines drives the binary rather than the image.
	if health.Machines != "none" {
		t.Errorf("the image reports machines %q; it is control-plane only by design", health.Machines)
	}
}

// Two clouds at once do not fight over a port.
//
// The reason the port is asked of the kernel rather than fixed: `go test ./...`
// runs packages in parallel, and two emulators on 4599 would make one of them
// fail with a message naming a port rather than a cause.
func TestTwoCloudsGetTwoPorts(t *testing.T) {
	requireOptIn(t)
	first := feinttest.Start(t)
	second := feinttest.Start(t)

	if first.Endpoint() == second.Endpoint() {
		t.Fatalf("both emulators answer on %s", first.Endpoint())
	}
	for _, cloud := range []*feinttest.Cloud{first, second} {
		res, err := http.Get(cloud.Endpoint() + "/_feint/health") //nolint:noctx // test client
		if err != nil {
			t.Errorf("%s does not answer: %v", cloud.Endpoint(), err)
			continue
		}
		_ = res.Body.Close()
	}
}
