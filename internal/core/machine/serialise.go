package machine

import "github.com/stephrobert/feint/internal/core/serialise"

// Serialising the lifecycle of one target is the half of the concurrency story
// the store lock cannot cover.
//
// Launching a container takes tens of seconds, so no pack holds the store lock
// across it: it reads a copy, calls the runtime, and writes the result back with
// Commit. Commit makes that write conditional, so a resource deleted meanwhile
// stays deleted. What it does not do is order two writers on the same resource.
//
// Two concurrent poweron on one server therefore each called the runtime, and
// the loser overwrote the winner's state: the second launch failed on a name the
// first had already taken, so the API ended up describing "stopped" a container
// that was running. A machine the control plane does not describe is a machine
// nobody thinks to stop, which is the exact failure this emulator exists to
// avoid reporting.
//
// The exclusion is named rather than widened. One lock per target, never one
// global lock: a global one would queue every server in the session behind a
// single launch, turning a correctness fix into a throughput bug. Interface
// allocation in the Incus driver is the same idea one layer down, and takes
// this same mechanism keyed by machine.
//
// Until #348 that last sentence was false, and this is why it is worth reading
// twice. The driver held one package-level attachMu, and this paragraph
// described it as an example of the rule it broke. Nothing was wrong with the
// sentence except that no test held it: six attachments on one stack took 26s,
// 31s, 42s, 52s, 63s and 73s, a flat ten seconds each, until the client gave up
// past its minute and its retry met the interface its own first attempt had
// created. A comment claiming a property is not the property.
//
// The mechanism itself lives in core/serialise, because it is not specific to
// machines: a pack's address allocation needs the same named exclusion, and
// the copy each pack used to carry is how a fixed race stayed alive elsewhere.

// Serialise excludes every other action on the same target until the returned
// function is called. A pack takes it before it reads the resource and holds it
// past the write-back, because the read, the runtime call and the Commit are one
// operation even though only the last of them touches the store.
//
//	unlock := p.binding().Serialise(id)
//	defer unlock()
//
// It is safe with no runtime configured, and packs take it there too: the window
// is shorter without a driver, not absent, and a lock a pack takes only in one
// mode is a lock somebody removes from the other.
//
// The key carries the provider, so two packs numbering their resources the same
// way do not wait on each other.
func (b Binding) Serialise(id string) func() {
	return serialise.Lock(b.Provider + "|" + id)
}
