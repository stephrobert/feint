// Package storetest holds the invariant sweep the packs run after a barrage.
//
// It exists because of a defect shape no per-path test can see: two different
// routes, each correctly locked on its own target, jointly violating a
// cross-resource invariant. Every concurrency test in this repository stages one
// race, on one path, in one pack — `TestConcurrentPowerOnStartsTheMachineOnce`
// in internal/providers/scaleway, `TestConcurrentCreatesDoNotShareAnAddress` in
// internal/providers/outscale, and the seven others. They hold the
// defects that were once real, and none of them would notice an address handed
// out by the flexible-IP pool that the subnet allocator also handed out.
//
// So the sweep is not a second implementation of those tests. It is the question
// they cannot ask: after a barrage of concurrent traffic, is the store still
// internally coherent?
//
// It lives in the core and knows no provider, which is what makes it usable by
// the three packs and by a fourth that does not exist yet. It reads nothing but
// `resource.Resource`: identifiers, the addresses it finds by shape, and the
// runtime object names the emulator itself wrote. A sweep that had to be taught
// each pack's attribute names would be a sweep a new pack silently escapes.
package storetest

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/resource"
)

// Gone reports a resource the pack keeps in the store although its API says it
// no longer exists — Outscale's terminated Vm, kept readable because the
// Terraform provider polls ReadVms until it observes "terminated".
//
// The vocabulary of "no longer exists" is each provider's own (`terminated`,
// `deleted`), and this package must not learn those words: CLAUDE.md's rule 5
// names the watcher filter that once listed three provider prefixes in the
// core as the shape to refuse. So liveness arrives as a predicate the pack
// supplies, and the failure direction of forgetting it is the loud one: a nil
// or stale predicate treats every resource as alive, which makes the sweep
// stricter, never blinder.
type Gone func(*resource.Resource) bool

// Shared reports two resources that may hold one address without it being a
// defect — a record that *is* an address and the machine it is attached to, for
// instance. The relation is the pack's knowledge, so the pack declares it; nil
// means no pair is exempt, which is the strict reading.
type Shared func(a, b *resource.Resource) bool

// Sweep reports every invariant the store broke, one line each, sorted so two
// runs of the same failure read the same.
//
// Empty means coherent. The caller decides what to do about it — this returns
// findings rather than calling t.Error, so a barrage can sweep repeatedly and
// report only the first breach, and so the sweep itself is testable.
//
// gone is the pack's word on which resources no longer exist; nil means all of
// them do. Only the address invariant reads it — an identifier is issued once
// forever, and a runtime object claimed by a dead record is still an orphan.
//
// shared is the pack's word on which pairs may hold one address without it being
// a defect; nil means none may. Both are the pack's knowledge rather than the
// core's, for the reason CLAUDE.md's rule 5 gives: this package must not learn
// three providers' vocabularies to do its job.
func Sweep(resources []*resource.Resource, gone Gone, shared Shared) []string {
	var found []string
	found = append(found, duplicateIdentifiers(resources)...)
	found = append(found, sharedAddresses(resources, gone, shared)...)
	found = append(found, sharedRuntimeObjects(resources)...)
	sort.Strings(found)
	return found
}

// duplicateIdentifiers: an identifier is issued once.
//
// Two resources sharing one is not a cosmetic clash. The store keys on
// provider|kind|id, so the second Put silently replaces the first: a client
// holding the older identifier reads somebody else's resource, and a delete
// removes both at once.
func duplicateIdentifiers(resources []*resource.Resource) []string {
	seen := map[string]*resource.Resource{}
	var found []string
	for _, res := range resources {
		if res == nil || res.ID == "" {
			continue
		}
		if first, clash := seen[res.ID]; clash {
			found = append(found, fmt.Sprintf(
				"identifier %s was issued twice: %s/%s and %s/%s",
				res.ID, first.Tenant.Provider, first.Kind, res.Tenant.Provider, res.Kind))
			continue
		}
		seen[res.ID] = res
	}
	return found
}

// sharedAddresses: one address belongs to one living resource of a kind.
//
// Scoped to the kind on purpose, and that is the difference between an invariant
// and a false alarm: an address resource and the server it is attached to both
// carry the same string, legitimately, and a naive "this address appears twice"
// would fail every healthy store. Two *servers* carrying it, or two *flexible
// IPs*, is the double allocation this is for.
//
// Living, because a record a pack keeps for observability holds nothing. The
// first version compared every resource of a kind whatever its state, and that
// blind reading produced a false verdict, measured on 2026-08-16: a terminated
// Vm's address legitimately reused by a new Vm was reported as "held by two vm
// resources", and a correct allocator fix was reverted on the strength of it.
// A control that measures something other than what it announces does not fail
// loudly, it convicts the innocent. TestAGoneResourcesAddressIsNotShared fails
// without the predicate being read.
//
// Addresses are found by shape rather than by attribute name, because the names
// are each pack's own — `address`, `PublicIp`, `ipv4_address` — and a sweep
// keyed on them is a sweep the fourth pack escapes. Anything that parses as an
// IP is an address; a prefix like 10.0.0.0/24 is not, and is skipped, since a
// subnet legitimately appears on the network and on everything placed in it.
func sharedAddresses(resources []*resource.Resource, gone Gone, shared Shared) []string {
	// address -> the first resource holding it, across every kind (#210).
	//
	// This used to be keyed by kind, so it only compared machines with machines
	// and NICs with NICs. Its own header promises one address handed to two
	// resources, and the cross-kind case — the one that actually happens, since
	// a pool is shared by everything placed in it — was invisible. Proven by a
	// probe: two live resources of different kinds carrying one address, zero
	// findings.
	//
	// Some pairs share an address legitimately, and pretending otherwise would
	// only move the false control rather than remove it: a Scaleway server and
	// the instance/ip record that *is* its flexible address both carry it, and
	// widening the comparison made TestAHealthyStoreSweepsClean red on exactly
	// that pair. Measured, not foreseen — a probe over the three barrages had
	// shown nothing, because their workload does not build that pair.
	//
	// So the exemption is declared by the pack that knows the relation, through
	// Shared, and nil means the strict reading: a pack that forgets gets a loud
	// false positive naming both resources, never a silent blind spot.
	held := map[string]*resource.Resource{}
	var found []string

	for _, res := range resources {
		if res == nil {
			continue
		}
		if gone != nil && gone(res) {
			continue
		}
		for _, addr := range addressesIn(res.Attrs) {
			first, clash := held[addr]
			// The exemption is asked only once there is something to exempt:
			// computing it first passed a nil `first` to the pack's predicate,
			// which dereferenced it. The harness caught that by refusing to
			// measure on a red tree, which is what that refusal is for.
			if clash && first.ID != res.ID && (shared == nil || !shared(first, res)) {
				found = append(found, fmt.Sprintf(
					"address %s is held by two live resources: %s (%s) and %s (%s)",
					addr, first.ID, first.Kind, res.ID, res.Kind))
				continue
			}
			held[addr] = res
		}
	}
	return found
}

// sharedRuntimeObjects: one runtime object belongs to one resource.
//
// Runtime holds the names the emulator itself wrote for the machine, the network
// or the interface behind a resource. Two resources naming one object means a
// delete of either destroys the other's machine, and the API describes something
// that is not there — the orphan shape, seen from the store side, which is the
// half that holds even with --vm off.
func sharedRuntimeObjects(resources []*resource.Resource) []string {
	// key -> value -> first resource
	byKey := map[string]map[string]string{}
	var found []string

	for _, res := range resources {
		if res == nil {
			continue
		}
		for k, v := range res.Runtime {
			if v == "" {
				continue
			}
			held := byKey[k]
			if held == nil {
				held = map[string]string{}
				byKey[k] = held
			}
			// A runtime value that names another resource — a server id on a
			// volume, say — is a reference, not an object this resource owns,
			// and several volumes legitimately name one server. Only values the
			// emulator minted for itself are exclusive, and those are the ones
			// that do not look like an identifier it also stored.
			if isStoredIdentifier(resources, v) {
				continue
			}
			if first, clash := held[v]; clash && first != res.ID {
				found = append(found, fmt.Sprintf(
					"runtime object %s=%q is claimed by two resources: %s and %s",
					k, v, first, res.ID))
				continue
			}
			held[v] = res.ID
		}
	}
	return found
}

func isStoredIdentifier(resources []*resource.Resource, value string) bool {
	for _, res := range resources {
		if res != nil && res.ID == value {
			return true
		}
	}
	return false
}

// addressesIn walks an attribute tree and returns everything that parses as an
// IP address. Recursive because packs nest: a server carries its addresses under
// public_ips[], a NIC under its own object.
func addressesIn(attrs map[string]any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			if addr, err := netip.ParseAddr(strings.TrimSpace(t)); err == nil && !addr.IsUnspecified() {
				out = append(out, addr.String())
			}
		case map[string]any:
			for _, nested := range t {
				walk(nested)
			}
		case []any:
			for _, nested := range t {
				walk(nested)
			}
		case []string:
			for _, nested := range t {
				walk(nested)
			}
		case []map[string]any:
			for _, nested := range t {
				walk(nested)
			}
		}
	}
	for _, v := range attrs {
		walk(v)
	}
	return out
}
