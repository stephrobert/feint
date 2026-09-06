package exoscale_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/exoscale"
)

// The declared catalogue (#126): with a declaration, a create outside it is
// refused, and the lists a client reads first answer the declared entries
// only; without one, nothing changes.

const (
	declaredTemplate   = "11111111-1111-4111-8111-111111111111"
	undeclaredTemplate = "22222222-2222-4222-8222-222222222222"
	declaredType       = "21624abb-764e-4def-81d7-9fc54b5957fb"
	undeclaredType     = "b6cd1ff5-3a2f-4e9d-a4d1-8988c1191fe8"
)

func strictHandler(t *testing.T, declaration string) http.Handler {
	t.Helper()
	env := emulator.DefaultEnv()
	declared, err := emulator.ParseDeclared(strings.NewReader(declaration))
	if err != nil {
		t.Fatalf("parse the declaration: %v", err)
	}
	env.Declared = declared
	srv, err := emulator.NewServer(env, exoscale.New(env))
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	return srv.Handler()
}

func instanceBody(template, instanceType string) string {
	return `{"name":"strict","template":{"id":"` + template + `"},"instance-type":{"id":"` + instanceType + `"},"disk-size":10}`
}

func TestAStrictCatalogueRefusesAnUndeclaredTemplateAndType(t *testing.T) {
	h := strictHandler(t, `{"exoscale": {"templates": ["`+declaredTemplate+`"], "types": ["`+declaredType+`"]}}`)

	rec, body := call(t, h, "POST", "/v2/instance", instanceBody(undeclaredTemplate, declaredType))
	if rec.Code != http.StatusNotFound {
		t.Errorf("an undeclared template answered %d: %v", rec.Code, body)
	}
	rec, body = call(t, h, "POST", "/v2/instance", instanceBody(declaredTemplate, undeclaredType))
	if rec.Code != http.StatusNotFound {
		t.Errorf("an undeclared instance type answered %d: %v", rec.Code, body)
	}
	// The accepting half.
	if rec, body = call(t, h, "POST", "/v2/instance", instanceBody(declaredTemplate, declaredType)); rec.Code != http.StatusOK {
		t.Fatalf("the declared template and type answered %d: %v", rec.Code, body)
	}
	// A pool names the same two objects, and the same declaration decides.
	rec, body = call(t, h, "POST", "/v2/instance-pool",
		`{"name":"pool","size":1,"template":{"id":"`+undeclaredTemplate+`"},"instance-type":{"id":"`+declaredType+`"},"disk-size":10}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("a pool on an undeclared template answered %d: %v", rec.Code, body)
	}

	// The lists a client reads first answer the declared entries only, and
	// the reads by id refuse what the lists omit.
	_, templates := call(t, h, "GET", "/v2/template", "")
	if list, _ := templates["templates"].([]any); len(list) != 1 {
		t.Errorf("list-templates answers %d templates, and one is declared", len(list))
	}
	if rec, _ := call(t, h, "GET", "/v2/template/"+undeclaredTemplate, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get-template served an undeclared template: %d", rec.Code)
	}
	_, types := call(t, h, "GET", "/v2/instance-type", "")
	if list, _ := types["instance-types"].([]any); len(list) != 1 {
		t.Errorf("list-instance-types answers %d types, and one is declared", len(list))
	}
	if rec, _ := call(t, h, "GET", "/v2/instance-type/"+undeclaredType, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get-instance-type served an undeclared type: %d", rec.Code)
	}
}

// Without a declaration nothing changes: an invented template id creates,
// which docs/limits.md promises.
func TestNoDeclarationChangesNothing(t *testing.T) {
	h := serve(t)
	rec, body := call(t, h, "POST", "/v2/instance", instanceBody("00000000-0000-4000-8000-00000000abcd", declaredType))
	if rec.Code != http.StatusOK {
		t.Fatalf("without a declaration an unknown template answered %d: %v", rec.Code, body)
	}
}
