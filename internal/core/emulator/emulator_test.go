package emulator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/exoscale"
	"github.com/stephrobert/feint/internal/providers/outscale"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// stubPack lets the routing tests avoid depending on a real provider's paths.
type stubPack struct {
	name   string
	routes []emulator.Route
}

func (s stubPack) Name() string                 { return s.name }
func (s stubPack) Routes() []emulator.Route     { return s.routes }
func (s stubPack) Declined() []emulator.Decline { return nil }

// Env is mandatory on the interface, and deliberately so: a pack that cannot say
// how to point a client at it is a pack nobody can use. A stub answers with one
// variable rather than nothing, so a test of the env command against it would
// exercise the same path a real pack does.
func (s stubPack) Env(endpoint string) emulator.Environment {
	return emulator.Environment{Vars: map[string]string{"STUB_ENDPOINT": endpoint}}
}
func noop(http.ResponseWriter, *http.Request) {}

func TestConflictingRoutesAreRejected(t *testing.T) {
	route := emulator.Route{Method: "GET", Path: "/v2/instance", Operation: "x", Handler: noop}
	_, err := emulator.NewServer(emulator.DefaultEnv(),
		stubPack{name: "a", routes: []emulator.Route{route}},
		stubPack{name: "b", routes: []emulator.Route{route}},
	)
	if err == nil {
		t.Fatal("expected two packs claiming the same route to be rejected at startup")
	}
}

// The three providers must be mountable together: that is the premise of the
// single-port design, and a URL-space collision would break it silently.
func TestAllProvidersShareOnePort(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env), outscale.New(env), exoscale.New(env))
	if err != nil {
		t.Fatalf("the three packs collide on one mux: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/_feint/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: expected 200, got %d", resp.StatusCode)
	}

	var health struct {
		Status    string   `json:"status"`
		Providers []string `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("health: decode: %v", err)
	}
	if health.Status != "ok" || len(health.Providers) != 3 {
		t.Fatalf("unexpected health payload: %+v", health)
	}
}

// Every route must declare the upstream operation it serves, otherwise the drift
// report cannot tell coverage from silence.
func TestEveryRouteDeclaresAnOperation(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, scaleway.New(env), outscale.New(env), exoscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	for _, r := range srv.AllRoutes() {
		if r.Operation == "" {
			t.Errorf("route %s %s has no Operation", r.Method, r.Path)
		}
		if r.Handler == nil {
			t.Errorf("route %s %s has no handler", r.Method, r.Path)
		}
	}
}

func TestNewUUIDFormat(t *testing.T) {
	// Scaleway and Exoscale SDKs validate identifiers, so the emulator cannot
	// hand out arbitrary strings.
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := make(map[string]bool, 100)
	for range 100 {
		id := emulator.NewUUID()
		if !re.MatchString(id) {
			t.Fatalf("not a v4 UUID: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate UUID generated: %q", id)
		}
		seen[id] = true
	}
}
