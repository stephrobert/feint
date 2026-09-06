package outscale

import "net/http"

// The declared catalogue (#126): an operator who wrote down the images and
// machine types their project allows gets a create outside them refused, in
// this API's own error shape, and an operator who wrote nothing gets what they
// always got — any identifier accepted, docs/limits.md says why.
//
// Two kinds, because two things a CreateVms names come from a catalogue: the
// image and the VmType. An image the client registered itself (CreateImage) is
// the client's own object, not the catalogue's, and is never refused here.

// DeclaredKinds implements emulator.Declaring: what --strict-catalog may name
// for this pack. A declaration naming any other kind is refused by serve.
func (p *Pack) DeclaredKinds() []string { return []string{"images", "types"} }

// undeclaredImage reports whether the declaration refuses an image the
// emulator does not hold. Peek rather than Get: this read decides, it is not a
// client looking.
func (p *Pack) undeclaredImage(id string) bool {
	if _, registered := p.env.Store.Peek(Name, kindImage, id); registered {
		return false
	}
	return p.env.Declared.Refuses(Name, "images", id)
}

// undeclaredType reports whether the declaration refuses a VmType.
func (p *Pack) undeclaredType(name string) bool {
	return p.env.Declared.Refuses(Name, "types", name)
}

// refuseUndeclared answers a create outside the declared catalogue, and says
// which of the two it is. The image refusal is the cloud's, measured: 400,
// code 5023, InvalidResource, on an identifier that names nothing. The type
// refusal is this pack's ordinary bad-argument shape; the cloud's answer to a
// VmType it does not offer was not recorded.
//
// TestAStrictCatalogueRefusesAnUndeclaredImageAndType fails without this, and
// TestNoDeclarationChangesNothing fails the day it fires without a declaration.
func (p *Pack) refuseUndeclared(w http.ResponseWriter, imageID, vmType string) bool {
	if p.undeclaredImage(imageID) {
		p.writeError(w, http.StatusBadRequest, codeImageDoesNotExist, typeInvalidResource,
			"The ImageId '"+imageID+"' doesn't exist.")
		return true
	}
	if p.undeclaredType(vmType) {
		p.badRequest(w, "the VmType "+vmType+" is outside the declared catalogue")
		return true
	}
	return false
}
