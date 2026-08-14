package emulator

// The schema versions of the /_feint surfaces a CI is allowed to depend on.
//
// The moment a pipeline runs `curl /_feint/conformance | jq .`, that shape is a
// public API, and until #132 nothing failed when it moved: a field could be
// renamed in a patch release and the first person to notice would be the owner
// of the broken pipeline. RELEASING.md named these surfaces as breaking; naming
// is a sentence, and a sentence is not a control.
//
// Each constant below is served inside its payload, so a consumer can branch on
// it. What keeps the field honest is the frozen fixture in
// internal/cli/testdata/frozen/: the field tree of each response is committed,
// TestTheFrozenSurfacesStillMatchTheirFixture compares the live answer against
// it, and TestASurfaceChangeDemandsItsVersionBump refuses a fixture whose
// latest entry does not carry the version served here. Changing a shape without
// touching its constant therefore fails `go test`, which runs on every pull
// request — the version field cannot lie, because the gate is what gives it its
// meaning.
//
// A bump is a statement to consumers, so it belongs in the CHANGELOG next to
// what moved. The procedure lives in RELEASING.md ("Frozen surfaces").
const (
	// HealthSchemaVersion is the shape of GET /_feint/health.
	HealthSchemaVersion = 1
	// RoutesSchemaVersion is the shape of GET /_feint/routes.
	//
	// This one is not on the wire: the endpoint answers a bare JSON array — the
	// README documents `jq -r '.[].operation'` against it — and wrapping the
	// array in an object to carry a version field would be exactly the break
	// this file exists to prevent. The constant still pins the fixture, so the
	// shape cannot move without a bump; if it ever must change, version 2 is
	// the wrapped object, which then carries its version like the others.
	RoutesSchemaVersion = 1
	// ConformanceSchemaVersion is the shape of GET /_feint/conformance.
	//
	// 2 since #156: `evidence.*[].probed` went from a bool to one of "response",
	// "refusal" or "none". A consumer branching on `probed === true` reads a
	// truthy string now and would count every refusal as a success — which is
	// the exact overstatement #156 removed, reappearing one layer out. The bump
	// is what lets it notice.
	ConformanceSchemaVersion = 2
	// TraceSchemaVersion is the shape of GET /_feint/trace.
	TraceSchemaVersion = 1
)
