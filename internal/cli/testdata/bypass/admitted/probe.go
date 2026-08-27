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
package admittedprobe

import (
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
)

// The positive control. Everything here is something a pack may legitimately
// name — machine.PackSurface admits the three types, and emulator.Env.Store is
// the field every pack reads — so this package must build. If it stops
// building, the probes beside it stop measuring the door and start measuring
// whatever broke here, which is the exact shape of an instrument that reports
// success because it looked nowhere.
var (
	_ machine.Binding
	_ machine.Reconciler
	_ machine.GroupSync
	_ = emulator.Env{}.Store
	_ = machine.Binding{}.Prefix
)
