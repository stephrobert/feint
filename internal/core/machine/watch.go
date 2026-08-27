package machine

import "context"

// What the runtime knows and the emulator does not say is where debugging time
// goes. A machine that refuses to start, a bridge whose DHCP service cannot
// bind, an instance the daemon reports in ERROR: all of it is in the runtime's
// own log, and an operator had to think of looking there.
//
// So the emulator listens, filters the runtime's stream down to what concerns
// the resources it created, and reports it in its own log. What is not ours is
// dropped: an operator's other instances are none of our business, and a stream
// nobody can read is as useless as no stream at all.
//
// The Incus half, including what its event stream looks like and the state race
// that reads as an error without being one, is in incus_watch.go.

// Event is one thing the runtime reported about a resource.
type Event struct {
	// Kind is "lifecycle" for state changes, "logging" for daemon messages.
	Kind string
	// Level is the runtime's severity for a logging event, empty otherwise.
	Level string
	// Action names a lifecycle change, such as "instance-started".
	Action string
	// Resource is the machine or network the event is about, when known.
	Resource string
	// Message is the human-readable text, for logging events.
	Message string
}

// Watcher is the optional half of a driver that can report what its runtime is
// doing. A driver without one leaves the operator reading the runtime's log by
// hand, which is what this exists to avoid.
type Watcher interface {
	// Watch streams events until the context is cancelled. The channel closes
	// when the stream ends, so a caller can tell a finished watch from a
	// blocked one.
	Watch(ctx context.Context) (<-chan Event, error)
}
