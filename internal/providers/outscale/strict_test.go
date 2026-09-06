package outscale_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// The declared catalogue (#126): with a declaration, a create outside it is
// refused in this API's own shape; without one, nothing changes.

func strictServer(t *testing.T, declaration string) *httptest.Server {
	t.Helper()
	env := emulator.DefaultEnv()
	declared, err := emulator.ParseDeclared(strings.NewReader(declaration))
	if err != nil {
		t.Fatalf("parse the declaration: %v", err)
	}
	env.Declared = declared
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestAStrictCatalogueRefusesAnUndeclaredImageAndType(t *testing.T) {
	ts := strictServer(t, `{"outscale": {"images": ["ami-00000001"], "types": ["tinav6.c1r1p2"]}}`)

	// The typo: 400, code 5023, InvalidResource, the recorded refusal of an
	// image that does not exist.
	status, out := post(t, ts, "CreateVms", `{"ImageId":"ami-00000002","VmType":"tinav6.c1r1p2"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("an undeclared image answered %d: %v", status, out)
	}
	errs, _ := out["Errors"].([]any)
	first, _ := errs[0].(map[string]any)
	if first["Code"] != "5023" || first["Type"] != "InvalidResource" || !strings.Contains(first["Details"].(string), "ami-00000002") {
		t.Errorf("the refusal is not the recorded one: %v", first)
	}

	// A type outside the declaration.
	status, out = post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","VmType":"tinav6.c2r4p2"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("an undeclared type answered %d: %v", status, out)
	}

	// The accepting half, or a guard refusing every create would pass the two
	// above: the declared pair creates a machine.
	status, out = post(t, ts, "CreateVms", `{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2"}`)
	if status != http.StatusOK {
		t.Fatalf("the declared image and type answered %d: %v", status, out)
	}
	vms, _ := out["Vms"].([]any)
	vmID, _ := vms[0].(map[string]any)["VmId"].(string)

	// An image the client registered is its own, not the catalogue's, and is
	// never refused by the declaration.
	status, out = post(t, ts, "CreateImage", `{"ImageName":"own","VmId":"`+vmID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateImage answered %d: %v", status, out)
	}
	own, _ := out["Image"].(map[string]any)["ImageId"].(string)
	if status, out = post(t, ts, "CreateVms", `{"ImageId":"`+own+`","VmType":"tinav6.c1r1p2"}`); status != http.StatusOK {
		t.Errorf("a registered image outside the declaration was refused: %d %v", status, out)
	}

	// The reads a client makes first list what the create would accept.
	_, images := post(t, ts, "ReadImages", `{}`)
	for _, entry := range images["Images"].([]any) {
		id, _ := entry.(map[string]any)["ImageId"].(string)
		if id == "ami-00000002" || id == "ami-00000003" {
			t.Errorf("ReadImages lists %s, outside the declaration", id)
		}
	}
	_, types := post(t, ts, "ReadVmTypes", `{}`)
	if list, _ := types["VmTypes"].([]any); len(list) != 1 {
		t.Errorf("ReadVmTypes lists %d types, and one is declared", len(list))
	}
}

// Without a declaration nothing changes: the compatibility mode is
// byte-identical, and the undeclared image of the test above creates a
// machine here, which is #392's standing decision.
func TestNoDeclarationChangesNothing(t *testing.T) {
	ts := newServer(t)
	if status, out := post(t, ts, "CreateVms", `{"ImageId":"ami-00000002","VmType":"tinav6.c2r4p2"}`); status != http.StatusOK {
		t.Fatalf("without a declaration a create was refused: %d %v", status, out)
	}
	if status, out := post(t, ts, "CreateVms", `{"ImageId":"ami-99999999","VmType":"tinav6.c1r1p2"}`); status != http.StatusOK {
		t.Fatalf("without a declaration an unknown image was refused, which docs/limits.md says is accepted: %d %v", status, out)
	}
}
