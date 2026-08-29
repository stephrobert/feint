package environment

import (
	"fmt"
	"strconv"
	"strings"
)

// The ready conditions, parsed once at load so a file naming a form nobody
// serves is refused before anything is started rather than at the end of a wait
// (#190). Each carries a deadline and a description said out loud while it is
// waited on: a hang with no output is indistinguishable from a broken emulator,
// and this repository has already paid for that.
//
// Three forms, and every one of them is asserted against the emulator rather
// than against the engine's state file. That is #190's own rule, and it is what
// makes a green `up` mean the API answers rather than that Terraform wrote a
// file.

// ConditionKind is one of the forms a ready condition takes.
type ConditionKind string

const (
	// HTTPCondition waits for the emulator to answer a path below 400.
	HTTPCondition ConditionKind = "http"
	// TCPCondition waits for a connection to be accepted.
	TCPCondition ConditionKind = "tcp"
	// ResourceCondition waits for the emulator's own inventory to hold at least
	// Count resources of a kind. Provider-neutral by construction: the store
	// holds resource.Resource, whose kind is a value rather than a type.
	ResourceCondition ConditionKind = "resource"
	// ServiceCondition waits for the machine a named resource backs to accept a
	// connection on a port, at an address the emulator itself recorded.
	//
	// The three forms above are all control plane: a route that answers, a
	// socket the caller already knows the address of, a count in the inventory.
	// None of them can say that the workload is up, and that gap has a date. On
	// the scheduled night of 2026-08-29 the Scaleway stack's four conditions
	// were all confirmed, `feint up` returned, and 310 ms later its web machine
	// was listening on 53 and not on 443 — cloud-init had not reached the runcmd
	// that starts the declared unit. `up` did not lie; it confirmed exactly what
	// it had been asked to confirm.
	//
	// A stack that declares one of these cannot return before its workload
	// answers, so no suite downstream has to guess afterwards — which is what
	// every suite was doing, each at its own moment and its own budget.
	ServiceCondition ConditionKind = "service"
)

// ConditionKinds names every form, for the refusal. A name that is not one of
// these is refused with this list, never accepted and discovered later.
var ConditionKinds = []string{
	"http:<path>",
	"tcp:<host>:<port>",
	"resource:<kind>[:<count>]",
	"service:<resource name>:<port>",
}

// Condition is one parsed ready condition.
type Condition struct {
	Kind ConditionKind
	// Raw is what the file wrote, for the message said while waiting and for
	// the one printed when the deadline passes.
	Raw string
	// Path is set for HTTPCondition.
	Path string
	// Address is set for TCPCondition.
	Address string
	// Resource and Count are set for ResourceCondition.
	Resource string
	Count    int
	// Name and Port are set for ServiceCondition. Name is the name the client
	// gave the resource, as the stack wrote it, never the runtime object behind
	// it: a declaration that named a container would be a declaration coupled to
	// the runtime that happens to be configured.
	Name string
	Port int
}

// ParseCondition reads one condition and refuses everything else by name.
func ParseCondition(raw string) (Condition, error) {
	kind, rest, ok := strings.Cut(raw, ":")
	if !ok || rest == "" {
		return Condition{}, fmt.Errorf("%q is not a ready condition; the forms are %s",
			raw, strings.Join(ConditionKinds, ", "))
	}
	switch ConditionKind(kind) {
	case HTTPCondition:
		if !strings.HasPrefix(rest, "/") {
			return Condition{}, fmt.Errorf("%q: an http condition takes a path on the emulator, "+
				"starting with a slash", raw)
		}
		return Condition{Kind: HTTPCondition, Raw: raw, Path: rest}, nil
	case TCPCondition:
		host, port, split := strings.Cut(rest, ":")
		if !split || host == "" || port == "" {
			return Condition{}, fmt.Errorf("%q: a tcp condition takes host:port", raw)
		}
		if _, err := strconv.Atoi(port); err != nil {
			return Condition{}, fmt.Errorf("%q: %q is not a port number", raw, port)
		}
		return Condition{Kind: TCPCondition, Raw: raw, Address: rest}, nil
	case ResourceCondition:
		name, count, split := strings.Cut(rest, ":")
		if name == "" {
			return Condition{}, fmt.Errorf("%q: a resource condition takes the kind the emulator "+
				"stores, as `feint status` and the page name it", raw)
		}
		want := 1
		if split {
			n, err := strconv.Atoi(count)
			if err != nil || n < 1 {
				return Condition{}, fmt.Errorf("%q: %q is not a count of at least one", raw, count)
			}
			want = n
		}
		return Condition{Kind: ResourceCondition, Raw: raw, Resource: name, Count: want}, nil
	case ServiceCondition:
		name, port, split := strings.Cut(rest, ":")
		if !split || name == "" || port == "" {
			return Condition{}, fmt.Errorf("%q: a service condition takes the name the stack gave "+
				"the resource and the port its workload listens on, as `name:port`", raw)
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return Condition{}, fmt.Errorf("%q: %q is not a port number", raw, port)
		}
		return Condition{Kind: ServiceCondition, Raw: raw, Name: name, Port: n}, nil
	default:
		return Condition{}, fmt.Errorf("%q: %q is not a ready condition; the forms are %s",
			raw, kind, strings.Join(ConditionKinds, ", "))
	}
}

// Conditions parses every ready condition of the file. It cannot fail: Parse
// already refused a file carrying one that does not read, which is what makes
// the refusal early rather than at the end of a wait.
func (f *File) Conditions() []Condition {
	out := make([]Condition, 0, len(f.Ready))
	for _, raw := range f.Ready {
		c, err := ParseCondition(raw)
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Describe answers what this condition is waiting for, in a sentence a reader
// can act on when the deadline passes.
func (c Condition) Describe() string {
	switch c.Kind {
	case HTTPCondition:
		return "the emulator answers " + c.Path
	case TCPCondition:
		return c.Address + " accepts a connection"
	case ResourceCondition:
		if c.Count == 1 {
			return "the emulator holds a " + c.Resource
		}
		return fmt.Sprintf("the emulator holds %d %s resources", c.Count, c.Resource)
	case ServiceCondition:
		return fmt.Sprintf("%s answers on port %d", c.Name, c.Port)
	default:
		return c.Raw
	}
}
