package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// The declared catalogue (#126): with a declaration, a create outside it is
// refused in this API's own shapes, and so is the lookup the CLI makes first;
// without one, nothing changes.

func strictServer(t *testing.T, declaration string) *httptest.Server {
	t.Helper()
	env := emulator.DefaultEnv()
	declared, err := emulator.ParseDeclared(strings.NewReader(declaration))
	if err != nil {
		t.Fatalf("parse the declaration: %v", err)
	}
	env.Declared = declared
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

const (
	declaredLabel  = "debian_bookworm"
	undeclaredUUID = "1e4c8c1a-1b7a-4b3e-9a3a-6d2e4f5a6b7c" // no catalogue image carries it
)

func TestAStrictCatalogueRefusesAnUndeclaredImageAndType(t *testing.T) {
	ts := strictServer(t, `{"scaleway": {"images": ["`+declaredLabel+`"], "types": ["DEV1-S"]}}`)

	// The lookup the CLI makes first: a label outside the declaration is a
	// 404 not_found, the recorded shape.
	status, out := do(t, ts, "GET", "/marketplace/v2/local-images?image_label=ubuntu_noble", "")
	if status != http.StatusNotFound || out["type"] != "not_found" || out["resource"] != "image" {
		t.Errorf("an undeclared label answered %d %v", status, out)
	}
	// And the image read by id, which the CLI resolves before it creates.
	status, out = do(t, ts, "GET", zoneURL+"/images/"+undeclaredUUID, "")
	if status != http.StatusNotFound || out["type"] != "not_found" {
		t.Errorf("an undeclared image id answered %d %v", status, out)
	}
	// The declared label, by either of its names, is served: the marketplace
	// maps the label to a UUID and a declaration in one form covers the other.
	status, out = do(t, ts, "GET", "/marketplace/v2/local-images?image_label="+declaredLabel, "")
	if status != http.StatusOK {
		t.Fatalf("the declared label answered %d %v", status, out)
	}
	list, _ := out["local_images"].([]any)
	uuid, _ := list[0].(map[string]any)["id"].(string)
	if status, out = do(t, ts, "GET", zoneURL+"/images/"+uuid, ""); status != http.StatusOK {
		t.Errorf("the declared label's UUID answered %d %v", status, out)
	}

	// The create: an undeclared image is a 404 not_found, an undeclared type
	// an invalid_arguments on commercial_type.
	status, out = do(t, ts, "POST", zoneURL+"/servers", `{"name":"typo","commercial_type":"DEV1-S","image":"ubuntu_noble"}`)
	if status != http.StatusNotFound || out["type"] != "not_found" {
		t.Errorf("a create on an undeclared image answered %d %v", status, out)
	}
	status, out = do(t, ts, "POST", zoneURL+"/servers", `{"name":"typo","commercial_type":"PLAY2-PICO","image":"`+declaredLabel+`"}`)
	if status != http.StatusBadRequest || out["type"] != "invalid_arguments" {
		t.Errorf("a create on an undeclared type answered %d %v", status, out)
	}
	// The accepting half: the declared pair creates.
	status, out = do(t, ts, "POST", zoneURL+"/servers", `{"name":"declared","commercial_type":"DEV1-S","image":"`+declaredLabel+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("the declared image and type answered %d %v", status, out)
	}

	// The type catalogue the Terraform provider validates against lists the
	// declared types only.
	_, types := do(t, ts, "GET", zoneURL+"/products/servers", "")
	servers, _ := types["servers"].(map[string]any)
	if len(servers) != 1 || servers["DEV1-S"] == nil {
		t.Errorf("/products/servers lists %d types, and one is declared: %v", len(servers), types)
	}
}

// Without a declaration nothing changes: any label and any UUID are served
// and create, which is what docs/limits.md promises a team pointing an
// existing stack here.
func TestNoDeclarationChangesNothing(t *testing.T) {
	ts := newTestServer(t)
	if status, out := do(t, ts, "GET", zoneURL+"/images/"+undeclaredUUID, ""); status != http.StatusOK {
		t.Fatalf("without a declaration an unknown image answered %d %v", status, out)
	}
	if status, out := do(t, ts, "POST", zoneURL+"/servers", `{"name":"any","commercial_type":"PLAY2-PICO","image":"`+undeclaredUUID+`"}`); status != http.StatusCreated {
		t.Fatalf("without a declaration a create on an unknown image answered %d %v", status, out)
	}
}
