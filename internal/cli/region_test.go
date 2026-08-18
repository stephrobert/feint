package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// FEINT_OUTSCALE_REGION is the one knob #290 added: the Outscale region is a
// property of the deployment — every real region speaks the same API — so it
// rides the environment into the composition root and is fixed for the
// process. These two tests hold both halves of the contract: the value
// selects the region every mounted entry point will serve, and a value
// Outscale does not publish refuses to serve instead of silently answering
// the default.

func TestTheOutscaleRegionRidesTheEnvironment(t *testing.T) {
	t.Setenv("FEINT_OUTSCALE_REGION", "cloudgouv-eu-west-1")

	env := emulator.DefaultEnv()
	packs, err := packsFor(env)
	if err != nil {
		t.Fatalf("packsFor with a region Outscale publishes: %v", err)
	}
	srv, err := emulator.NewServer(env, packs...)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Post(ts.URL+"/api/v1/ReadRegions", "application/json", strings.NewReader(`{}`)) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("ReadRegions: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body := make([]byte, 4096)
	n, _ := res.Body.Read(body)
	if !strings.Contains(string(body[:n]), `"cloudgouv-eu-west-1"`) {
		t.Errorf("ReadRegions does not name the selected region: %s", body[:n])
	}
}

func TestAnUnknownOutscaleRegionRefusesToServe(t *testing.T) {
	t.Setenv("FEINT_OUTSCALE_REGION", "eu-mars-1")

	if _, err := packsFor(emulator.DefaultEnv()); err == nil {
		t.Fatal("packsFor accepted eu-mars-1, a region Outscale does not publish")
	} else if !strings.Contains(err.Error(), "eu-mars-1") {
		t.Errorf("the refusal does not name the offending value: %v", err)
	}
}
