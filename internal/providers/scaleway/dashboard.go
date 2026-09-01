package scaleway

import (
	"net/http"
	"sort"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Two declines re-examined against the real cloud, and withdrawn (#626).
//
// Both were filed under "capacity and quotas are the provider's fleet, and a
// local emulator that answered would be inventing headroom a client could plan
// against". That reason is exactly right for GetServerTypesAvailability, which
// still declines: fr-par-1 answered `shortage` for every type on 2026-09-01, and
// any value this emulator returned would be a claim about Scaleway's fleet.
//
// It was wrong about these two, and the measurement is what says so.
//
//   - compatible-types answers a plain list of type names and no headroom at
//     all. It is a property of the catalogue, not of the fleet.
//   - the dashboard answers counters over the tenant's own resources. The
//     decline said "every total would be short by the unemulated remainder"; the
//     remainder is empty. Every counter fr-par-1 returns names a family this pack
//     serves, and the two that name volume types it never makes are truly zero
//     rather than short.
//
// A decline is a decision and may be revisited; its reason is a claim about the
// code and about the cloud, and these two had stopped being either.

// serverCompatibleTypes answers which commercial types a server can move to.
//
// Derived from the catalogue this pack already serves rather than from a table
// of its own: a second list would be a second place to keep in step with the
// first, which is the reason ListServerActions is declined one block above.
//
// Same architecture, and nothing else. The real answer is longer than this one
// because Scaleway's catalogue is; what a client needs from it is that resizing
// to a listed type is accepted and to an unlisted one is not, and that holds
// against a catalogue of eighteen exactly as it does against theirs.
//
// TestCompatibleTypesComeFromTheCatalogueAndExcludeTheServersOwn fails without
// this.
func (p *Pack) serverCompatibleTypes(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	server, found := p.env.Store.Get(Name, kindServer, id)
	if !found || server.Tenant.Zone != zone {
		writeNotFound(w, "server", id)
		return
	}

	current, _ := server.Attrs["commercial_type"].(string)
	arch := ""
	if entry, known := catalogue[current]; known {
		arch = entry.Arch
	}

	compatible := make([]string, 0, len(catalogue))
	for name, entry := range catalogue {
		// Its own type is not among them, which is what the real answer does:
		// a resize to the type it already has is not a move.
		if name == current {
			continue
		}
		// An architecture change is not a resize; a guest built for x86_64 does
		// not boot on arm64. The catalogue carries the arch, so this is read
		// rather than assumed.
		if arch != "" && entry.Arch != arch {
			continue
		}
		compatible = append(compatible, name)
	}
	sort.Strings(compatible)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{"compatible_types": compatible})
}

// dashboard counts what the tenant holds.
//
// Every key here is one fr-par-1 answers, taken from a live read on 2026-09-01
// rather than from the SDK struct alone, and every one of them is computed from
// the store. The two volume-type counters this emulator never fills are zero
// because it makes none — which is the truth about it, not a total short of an
// unemulated remainder.
//
// What is deliberately not here: nothing. A counter this pack could not compute
// would have to be absent rather than zeroed, since an absent key says which
// part is missing and a zero does not — the shape the reporter of #626 proposed,
// and the one that would apply the day a counter names something unserved.
//
// TestTheDashboardCountsWhatTheStoreHolds fails without this.
func (p *Pack) dashboard(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	scope := p.scopeOf(r, zone)

	servers := p.env.Store.List(kindServer, scope)
	byType := map[string]int{}
	running := 0
	for _, s := range servers {
		if t, _ := s.Attrs["commercial_type"].(string); t != "" {
			byType[t]++
		}
		if s.State == "running" {
			running++
		}
	}

	volumes := p.env.Store.List(kindVolume, scope)
	bssd, bssdSize := 0, int64(0)
	for _, v := range volumes {
		if t, _ := v.Attrs["volume_type"].(string); t == "b_ssd" {
			bssd++
			// resource.Int64 rather than a type assertion: Attrs crosses
			// encoding/json on every snapshot, so a size stored as the uint64
			// newVolume writes comes back float64 and a bare assertion yields
			// zero. The first version of this made that exact mistake — the
			// count answered one while the total answered zero — and
			// internal/cli's TestNoPackReadsAStoredNumberByAssertion named
			// it before any test of mine did.
			bssdSize += resource.Int64(v, "size")
		}
	}

	ips := p.env.Store.List(kindIP, scope)
	unused := 0
	for _, ip := range ips {
		if summary, _ := ip.Attrs["server"].(map[string]any); summary == nil {
			unused++
		}
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{"dashboard": map[string]any{
		"servers_count":            len(servers),
		"servers_by_types":         countsOf(byType),
		"running_servers_count":    running,
		"volumes_count":            len(volumes),
		"volumes_b_ssd_count":      bssd,
		"volumes_b_ssd_total_size": bssdSize,
		// Zero because this emulator makes neither, which is true of it rather
		// than short of anything: /products/volumes on fr-par-1 lists l_ssd and
		// scratch, and nothing here creates one.
		"volumes_l_ssd_count":      0,
		"volumes_l_ssd_total_size": 0,
		"volumes_scratch_count":    0,
		"snapshots_count":          len(p.env.Store.List(kindSnapshot, scope)),
		"images_count":             len(p.env.Store.List(kindImage, scope)),
		"ips_count":                len(ips),
		"ips_unused":               unused,
		"security_groups_count":    len(p.env.Store.List(kindSecurityGroup, scope)),
		"placement_groups_count":   len(p.env.Store.List(kindPlacementGroup, scope)),
		"private_nics_count":       len(p.env.Store.List(kindPrivateNIC, scope)),
	}})
}

// countsOf renders the per-type map, empty rather than nil so a client reading
// servers_by_types finds an object on a fresh account, as fr-par-1 answers.
func countsOf(counts map[string]int) map[string]any {
	out := make(map[string]any, len(counts))
	for name, n := range counts {
		out[name] = n
	}
	return out
}
