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
	//
	// 2 since #180: the payload gained `enforced`, an object keyed by capability
	// naming the packs that hand work to it. Additive, but it changes what the
	// endpoint *means*: `capabilities.firewall` alone said the runtime can
	// enforce rules, and a consumer read it as "my security groups are
	// enforced". One pack of three handed a rule over. The honest check is both
	// keys, so a consumer that branches on the version can tell whether it is
	// talking to a build that can answer the second question at all.
	//
	// 3 since #309: the payload gained `instance`, the identity of the process
	// answering — its pid and start time. Additive, and it exists because its
	// absence was measured: a stale emulator on a shared port answered a probe
	// with the previous build's catalogue, and nothing in the answer could say
	// so. `feint start` now compares `instance.pid` against the process it
	// spawned and refuses a stranger; any harness that starts an emulator can
	// do the same.
	//
	// 4 since #315: `capabilities` gained `balancing` — an emulated load
	// balancer distributes real connections, for clients inside the network it
	// sits in. Additive, and a claim rather than a detail, which is why it
	// moves the version: a suite that wants to prove a balancer balances must
	// key on this and never on a mode name, and a build that cannot answer the
	// question at all is exactly what a version is for.
	//
	// 5 since #337: `capabilities` gained `firewall_public_only` — a security
	// group is enforced even on a machine that joins no emulated network. The
	// Incus driver declares it false, measured: a routed NIC, the interface of
	// a server carrying only its published public addresses, accepts no
	// security option at all (7.2 and 7.3). Publishing the refusal is the
	// point. `capabilities.firewall` alone read as "my security groups are
	// enforced", it was true for a machine on a private network and false for
	// one with only a public address, and nothing said which.
	HealthSchemaVersion = 5
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
	//
	// 3 since #88: the payload gained `fields`, the omission check's verdict —
	// the declared response fields a run's answers never carried, next to the
	// operations the comparison reached. Additive, but a consumer parsing the
	// whole object should know the shape moved.
	//
	// 4 since #26/#356: the payload gained `injected`, the answers the fault
	// injector produced per operation. Additive, and it changes what the whole
	// document *means* rather than only what it carries: every other counter
	// here describes what the emulator served, and this one names the answers
	// it staged. A consumer that reports "exercised" without reading it can be
	// looking at a run where a route only ever answered a fault somebody armed,
	// and the version is what lets it notice that the question now exists.
	ConformanceSchemaVersion = 4
	// TraceSchemaVersion is the shape of GET /_feint/trace.
	TraceSchemaVersion = 1
	// FaultsSchemaVersion is the shape of GET/PUT/DELETE /_feint/faults.
	//
	// 1 is the first: an object carrying `faults`, one entry per armed rule
	// with its target operation, what it answers, how often it may fire and how
	// often it has. It is frozen from the start rather than "once it settles",
	// because a suite that arms a fault from a committed file is a consumer on
	// day one — the same reasoning that put the other four here.
	FaultsSchemaVersion = 1
)
