package exoscale

import "net/http"

// The declared catalogue (#126): an operator who wrote down the templates and
// instance types their project allows gets a create outside them refused, in
// this API's own error shape, and an operator who wrote nothing gets what they
// always got — any identifier accepted, docs/limits.md says why.
//
// A template the client registered itself is the client's own object and is
// never refused here. The refusal is this pack's 404, the shape the cloud
// answers for a resource that does not exist (corpus/exoscale/exo-refusals.jsonl
// on get-ssh-key and delete-ssh-key); its answer to a create naming a template
// it does not offer was not recorded, and docs/limits.md says so.

// DeclaredKinds implements emulator.Declaring.
func (p *Pack) DeclaredKinds() []string { return []string{"templates", "types"} }

// undeclaredTemplate reports whether the declaration refuses a template the
// emulator does not hold.
func (p *Pack) undeclaredTemplate(id string) bool {
	if _, registered := p.env.Store.Peek(Name, kindTemplate, id); registered {
		return false
	}
	return p.env.Declared.Refuses(Name, "templates", id)
}

// undeclaredInstanceType reports whether the declaration refuses an instance
// type.
func (p *Pack) undeclaredInstanceType(id string) bool {
	return p.env.Declared.Refuses(Name, "types", id)
}

// refuseUndeclared answers a create outside the declared catalogue.
// TestAStrictCatalogueRefusesAnUndeclaredTemplateAndType fails without this.
func (p *Pack) refuseUndeclared(w http.ResponseWriter, templateID, instanceTypeID string) bool {
	if p.undeclaredTemplate(templateID) {
		writeError(w, http.StatusNotFound, "no template "+templateID)
		return true
	}
	if p.undeclaredInstanceType(instanceTypeID) {
		writeError(w, http.StatusNotFound, "no instance type "+instanceTypeID)
		return true
	}
	return false
}
