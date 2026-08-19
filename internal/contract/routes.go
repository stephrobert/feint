package contract

import "strings"

// Checking a route against the contract catches what the drift report cannot.
//
// Drift joins on the operation name, so it says whether a route claims something
// upstream has. It never looks at the path or the method, and an SDK addresses
// exactly those: a route mounted at the right name and the wrong path is invisible
// to every mechanism this project had, right up until a client gets a 404 that
// reads like a missing feature.
//
// The path is composable because the artefact records the prefix the document's
// server URL declares. Outscale's /ReadVms is served at /api/v1/ReadVms,
// Exoscale's /instance at /v2/instance, and neither pack should have to repeat
// that constant.

// RouteMismatch is one route that does not match what the contract says.
type RouteMismatch struct {
	// Operation is the name the route declares.
	Operation string
	// Reason says what disagrees.
	Reason string
}

func (m RouteMismatch) String() string { return m.Operation + ": " + m.Reason }

// MountedRoute is what a pack claims to serve, in the terms this package needs.
// It mirrors emulator.Route without importing it: the dependency runs the other
// way, and a contract that knew about HTTP handlers would be harder to test than
// the thing it checks.
type MountedRoute struct {
	Method    string
	Path      string
	Operation string
	// Legacy marks a route mounted at a path an earlier SDK generation used
	// (emulator.Route.Legacy carries the measurement). The document describes
	// only the current spelling, so the check requires the operation to exist
	// and leaves the method and path to the primary mounting.
	Legacy bool
}

// CheckRoutes reports every mounted route the contract disagrees with: an
// operation it does not define, a method it defines differently, or a path it
// serves somewhere else.
func (d *Doc) CheckRoutes(routes []MountedRoute) []RouteMismatch {
	var out []RouteMismatch
	for _, r := range routes {
		op, _, known := d.OperationFor(r.Operation)
		if !known {
			out = append(out, RouteMismatch{r.Operation, "the API defines no such operation"})
			continue
		}
		if r.Legacy {
			// An old spelling the document no longer carries: the operation's
			// existence is all the document can vouch for.
			continue
		}
		if op.Method != r.Method {
			out = append(out, RouteMismatch{r.Operation,
				"served as " + r.Method + ", defined as " + op.Method})
		}
		if want := d.PathPrefix + op.Path; !samePath(want, r.Path) {
			out = append(out, RouteMismatch{r.Operation,
				"mounted at " + r.Path + ", defined at " + want})
		}
	}
	return out
}

// Owns reports whether this document actually describes one mounted route:
// the operation resolves, and the route serves it at the method and path the
// document declares.
//
// Resolving the name alone is not ownership. Outscale's contract keys on bare
// operationIds, so Scaleway's instance/v1/API.CreateImage resolves in it too —
// and the probe then planned Scaleway's route against Outscale's path, called
// /api/v1/CreateImage under a Scaleway label, and recorded the verdict on
// whichever route the call actually hit. One document, one route table:
// TestAProbePlansOnlyTheRoutesItsDocumentDescribes fails without this.
func (d *Doc) Owns(r MountedRoute) bool {
	op, _, known := d.OperationFor(r.Operation)
	if !known {
		return false
	}
	return op.Method == r.Method && samePath(d.PathPrefix+op.Path, r.Path)
}

// samePath compares two path templates, ignoring what the parameters are called.
//
// A document writes /instance/{id} where another writes /instance/{instance-id},
// and net/http patterns name them for the handler's own use. The segment being a
// parameter is the contract; its name is the pack's business.
func samePath(want, got string) bool {
	wantParts := strings.Split(strings.Trim(want, "/"), "/")
	gotParts := strings.Split(strings.Trim(got, "/"), "/")
	if len(wantParts) != len(gotParts) {
		return false
	}
	for i := range wantParts {
		w, g := wantParts[i], gotParts[i]
		if isParam(w) && isParam(g) {
			continue
		}
		// A segment may be a parameter followed by an ":action" suffix, which is
		// how these APIs spell a verb on a resource — /instance/{id}:start. The
		// name is still the pack's business and the action is still the
		// contract's, so they are compared apart.
		//
		// Whole-segment comparison worked here only by coincidence, while both
		// documents happened to name the parameter the same way. It stopped the
		// day a route had to be mounted as {id}:revert-snapshot against a
		// document writing {instance-id}:revert-snapshot — one mux group carries
		// one wildcard name, so the pack cannot always match the document's.
		//
		// TestAnActionOnAParameterComparesTheActionAndNotTheName fails without
		// this.
		wantParam, wantAction, wantSplit := splitAction(w)
		gotParam, gotAction, gotSplit := splitAction(g)
		if wantSplit && gotSplit && isParam(wantParam) && isParam(gotParam) {
			if wantAction != gotAction {
				return false
			}
			continue
		}
		if w != g {
			return false
		}
	}
	return true
}

func isParam(segment string) bool {
	return strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}

// splitAction cuts "{name}:action" into its two halves. It reports false for
// anything else, including a bare parameter, so a caller cannot mistake "no
// action" for "an empty action".
func splitAction(segment string) (param, action string, ok bool) {
	param, action, ok = strings.Cut(segment, ":")
	if !ok || action == "" {
		return "", "", false
	}
	return param, action, true
}

// SamePathForTest exposes the path comparison to the package's external test.
//
// Exported for a test rather than the test moved inside the package, because
// every other test of this package is external and reads the API as a caller
// does; one internal file would be the odd one out and would drift.
func SamePathForTest(want, got string) bool { return samePath(want, got) }
