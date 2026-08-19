package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// FEINT_EXOSCALE_ZONE is #278's knob, the Exoscale twin of
// FEINT_OUTSCALE_REGION next door: an Exoscale zone is a property of the
// endpoint a client points at (api-<zone>.exoscale.com), so it rides the
// environment into the composition root and is fixed for the process. These
// two tests hold both halves of the contract: the value selects the zone
// every mounted entry point will serve, and a value Exoscale does not publish
// refuses to serve instead of silently answering the default.

func TestTheExoscaleZoneRidesTheEnvironment(t *testing.T) {
	t.Setenv("FEINT_EXOSCALE_ZONE", "de-fra-1")

	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		t.Fatalf("packsFor with a zone Exoscale publishes: %v", err)
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/v2/zone") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("GET /v2/zone: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body := make([]byte, 4096)
	n, _ := res.Body.Read(body)
	if !strings.Contains(string(body[:n]), `"de-fra-1"`) {
		t.Errorf("the zone list does not name the selected zone: %s", body[:n])
	}
}

func TestAnUnknownExoscaleZoneRefusesToServe(t *testing.T) {
	t.Setenv("FEINT_EXOSCALE_ZONE", "ch-mars-1")

	if _, err := packsFor(emulator.DefaultEnv()); err == nil {
		t.Fatal("packsFor accepted ch-mars-1, a zone Exoscale does not publish")
	} else if !strings.Contains(err.Error(), "ch-mars-1") {
		t.Errorf("the refusal does not name the offending value: %v", err)
	}
}
