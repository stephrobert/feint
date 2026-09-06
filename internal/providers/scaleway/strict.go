package scaleway

import "net/http"

// The declared catalogue (#126): an operator who wrote down the images and
// commercial types their project allows gets a create outside them refused,
// in this API's own error shapes, and an operator who wrote nothing gets what
// they always got — any identifier accepted, docs/limits.md says why.
//
// An image is named two ways, by label (`debian_bookworm`) and by the UUID the
// marketplace maps it to, and a client sends either: the CLI resolves a label
// through the marketplace and the Terraform provider stores the UUID. A
// declaration written in one form covers the other, so an operator lists what
// they read off their own configuration. An image the client registered itself
// is the client's own object and is never refused here.

// DeclaredKinds implements emulator.Declaring.
func (p *Pack) DeclaredKinds() []string { return []string{"images", "types"} }

// undeclaredImage reports whether the declaration refuses an image the
// emulator does not hold, by either of its names.
func (p *Pack) undeclaredImage(requested string) bool {
	if requested == "" {
		requested = defaultImageLabel
	}
	if _, registered := p.env.Store.Peek(Name, kindImage, requested); registered {
		return false
	}
	other := ""
	if label, known := labelByID[requested]; known {
		other = label
	} else if entry, known := marketplaceImages[requested]; known {
		other = entry.ID
	}
	if !p.env.Declared.Refuses(Name, "images", requested) {
		return false
	}
	return other == "" || p.env.Declared.Refuses(Name, "images", other)
}

// undeclaredType reports whether the declaration refuses a commercial type.
func (p *Pack) undeclaredType(commercialType string) bool {
	return p.env.Declared.Refuses(Name, "types", commercialType)
}

// refuseUndeclaredImage answers the shape the cloud answers for an image that
// names nothing: 404 not_found on the image, the SDK's ResourceNotFoundError,
// recorded on GetImage and ListLocalImages (corpus/scaleway/scw-refusals.jsonl,
// 2026-08-21) and on every create the recording caught naming a missing
// resource.
func refuseUndeclaredImage(w http.ResponseWriter, requested string) {
	writeNotFound(w, "image", requested)
}

// refuseUndeclaredType answers the shape the cloud answers a create whose
// argument breaks a constraint: invalid_arguments on commercial_type, the
// SDK's InvalidArgumentsError. The cloud's exact answer to a type it does not
// sell was not recorded; the shape is the one it uses for every argument it
// refuses.
func refuseUndeclaredType(w http.ResponseWriter, commercialType string) {
	writeInvalidArguments(w, ArgumentError{
		ArgumentName: "commercial_type",
		Reason:       "constraint",
		HelpMessage:  "commercial_type " + commercialType + " is outside the declared catalogue",
	})
}
