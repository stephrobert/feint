package emulator_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// TestHealthCarriesTheVerificationCounters: /_feint/health publishes what the
// runtime read back against every plan, as four counters (#670). The frozen
// fixture holds the shape and the version; this holds that the four are
// there and numeric on the ordinary runtime-less emulator, where they are
// honestly zero — nothing booted, so nothing was read.
func TestHealthCarriesTheVerificationCounters(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, stubPack{name: "stub"})
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/_feint/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var health struct {
		Schema       int `json:"schema_version"`
		Verification *struct {
			Held       *int64 `json:"held"`
			Broken     *int64 `json:"broken"`
			Unreadable *int64 `json:"unreadable"`
			Repaired   *int64 `json:"repaired"`
		} `json:"verification"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("health: decode: %v", err)
	}
	if health.Verification == nil {
		t.Fatal("health carries no verification: a verdict nobody reads is a confession nobody hears")
	}
	for name, value := range map[string]*int64{
		"held": health.Verification.Held, "broken": health.Verification.Broken,
		"unreadable": health.Verification.Unreadable, "repaired": health.Verification.Repaired,
	} {
		if value == nil {
			t.Errorf("verification.%s is absent, and a gate reading its absence as zero would pass every build", name)
		} else if *value != 0 {
			t.Errorf("verification.%s = %d on an emulator that booted nothing", name, *value)
		}
	}
	if health.Schema != emulator.HealthSchemaVersion || health.Schema < 8 {
		t.Errorf("schema_version %d: the counters are a surface change, and the version is what lets a consumer notice", health.Schema)
	}
}
