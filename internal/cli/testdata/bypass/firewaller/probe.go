// Package bypass is one sentence a provider pack must not be able to write.
//
// Each directory here is a package of its own, holding a single expression,
// and every one of them but admitted/ must FAIL to compile.
// internal/cli's TestThePacksCannotNameTheDriver builds each in turn and
// requires the failure, naming the symbol; admitted/ is its positive control
// and must build, so a probe broken for any other reason — a wrong import
// path, a module the internal rule refuses, no toolchain — cannot read as the
// door being shut.
//
// They live under testdata/ so `go build ./...`, `go vet ./...` and
// golangci-lint never see them: a package that must not compile would
// otherwise break every build in the repository. `go list ./...` never names
// them either, so no coverage, evidence or drift artefact can count them.
package firewallerprobe

import "github.com/stephrobert/feint/internal/core/machine"

// The rule-set half. Reaching it is how a pack writes EnsureFirewall itself
// instead of going through GroupSync, which is the layer #475 was born in.
var _ machine.Firewaller
