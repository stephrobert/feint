package scaleway

import (
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Four Load Balancer reads a Day-2 client asks for, and a provisioning client
// never does (#666). Measured on both sides on 2026-09-04 with one generated
// Ansible collection: 39 of its 46 modules play against a real account, 28
// against this emulator, and these four routes are most of the difference.
//
// What each one answers, and the line none of them crosses.
//
// The two stats routes report on servers this emulator already owns, in a
// backend it already serves: the backend and the instance are identifiers it
// minted, the address is the pool's, and the server's state is a fact the
// control plane holds. What NO route here reports is a health: nothing in
// this repository probes a backend. The decline that stood until now said
// exactly that, and it stays true in every mode — the Scaleway pack hands no
// balancer to the runtime (only the Outscale pack declares EnforcesBalancing),
// and even the runtime's balancing half reports what it distributes, never
// what answered (machine.BalancerDelivery). So last_health_check_status is
// always `unknown`, which is the contract's own default for that enum and the
// literal truth. A backend published UP that nothing checked would be the lie
// this emulator exists to refuse; a backend published `unknown` is the emulator
// saying so in the API's own word, and a playbook asserting `passed` learns the
// limit from the value rather than from a 501 — which is #658's argument, one
// product over: an _info module's whole purpose is the read.
//
// The two lists are empty, and that is what a balancer here holds. Nothing
// terminates TLS and nothing subscribes anybody, so CreateCertificate and
// CreateSubscriber stay declined; a list that can only ever be empty is still
// the true answer to "what does this balancer hold", and the collection already
// treats an empty list as one.

// backendServerStateOf maps a server's state onto the four the stats enum
// declares (stopped/starting/running/stopping). A server the pool names and
// nobody holds is stopped: the enum's own default, and what a pool address
// with no machine behind it is.
func backendServerStateOf(server *resource.Resource) string {
	if server == nil {
		return "stopped"
	}
	switch server.State {
	case "running", "starting", "stopping":
		return server.State
	}
	return "stopped"
}

// serverHoldingAddress answers the server whose private NIC holds an IPAM
// address, nil when no server does. Through IPAM and the NIC rather than
// through a server field, because that is the only chain the API publishes:
// a pool names addresses, an address names its NIC, a NIC names its server.
func (p *Pack) serverHoldingAddress(ip string) *resource.Resource {
	want, err := netip.ParseAddr(ip)
	if err != nil {
		return nil
	}
	for _, held := range p.env.Store.List(kindIPAMIP, resource.Tenant{Provider: Name}) {
		prefix, err := netip.ParsePrefix(textOf(held.Attrs["address"]))
		if err != nil || prefix.Addr() != want {
			continue
		}
		nicID := held.Runtime[runtimeNICKey]
		if nicID == "" {
			continue
		}
		nic, found := p.env.Store.Get(Name, kindPrivateNIC, nicID)
		if !found {
			continue
		}
		if server, found := p.env.Store.Get(Name, kindServer, nic.Runtime[runtimeServerKey]); found {
			return server
		}
	}
	return nil
}

// backendServerStat is one BackendServerStats entry, field for field in the
// contract's order.
//
// TestAHealthNobodyMeasuredIsNeverPassed holds the one field this emulator
// cannot fill: it stays `unknown` whatever the server's state.
func (p *Pack) backendServerStat(backend *resource.Resource, ip string) map[string]any {
	server := p.serverHoldingAddress(ip)
	entry := map[string]any{
		"instance_id":              "",
		"backend_id":               backend.ID,
		"ip":                       ip,
		"server_state":             backendServerStateOf(server),
		"server_state_changed_at":  nil,
		"last_health_check_status": "unknown",
	}
	if server != nil {
		entry["instance_id"] = server.ID
		entry["server_state_changed_at"] = server.Updated.Format(time.RFC3339)
	}
	return entry
}

// backendServerStatsOf lists one entry per pool address of every backend of a
// balancer, backends in creation order and addresses in pool order, so two
// reads answer the same list. backendID narrows it to one backend, the way the
// two routes' query does.
func (p *Pack) backendServerStatsOf(lbID, backendID string) []map[string]any {
	backends := filterResources(p.env.Store.List(kindLBBackend, resource.Tenant{Provider: Name}),
		func(res *resource.Resource) bool {
			return res.Attrs["lb_id"] == lbID && (backendID == "" || res.ID == backendID)
		})
	sort.Slice(backends, func(i, j int) bool { return backends[i].Created.Before(backends[j].Created) })
	out := make([]map[string]any, 0, len(backends))
	for _, backend := range backends {
		pool, _ := backend.Attrs["pool"].([]any)
		for _, entry := range pool {
			ip := textOf(entry)
			if ip == "" {
				continue
			}
			out = append(out, p.backendServerStat(backend, ip))
		}
	}
	return out
}

// getLBStats answers GetLBStats: every backend server of the balancer, or of
// the one backend the query names.
func (p *Pack) getLBStats(w http.ResponseWriter, r *http.Request) {
	lb, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	stats := p.backendServerStatsOf(lb.ID, r.URL.Query().Get("backend_id"))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"backend_servers_stats": stats})
}

// listLBBackendStats answers ListBackendStats: the same entries, as a window
// with the count of the whole.
func (p *Pack) listLBBackendStats(w http.ResponseWriter, r *http.Request) {
	lb, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	all := p.backendServerStatsOf(lb.ID, r.URL.Query().Get("backend_id"))
	page := parsePage(r)
	start, end := page.slice(len(all))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"backend_servers_stats": all[start:end],
		"total_count":           len(all),
	})
}

// listLBCertificates answers ListCertificates: the certificates a balancer
// here holds, which is none, and a 404 for a balancer that does not exist —
// the difference between "holds nothing" and "is nothing".
func (p *Pack) listLBCertificates(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb"); !ok {
		return
	}
	// The query the contract declares is read, on a list that holds nothing:
	// a name filter narrows nothing, an order the API does not offer is still
	// refused, and a window past the end is still an empty page. Dropping a
	// declared parameter is what #271 and #277 refuse, whatever the list.
	certificates := []*resource.Resource{}
	if name := r.URL.Query().Get("name"); name != "" {
		certificates = filterResources(certificates, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
	}, certificates) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(certificates))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"certificates": make([]any, 0, end-start),
		"total_count":  len(certificates),
	})
}

// listLBSubscribers answers ListSubscriber: the alerting subscriptions of the
// zone, which are none.
func (p *Pack) listLBSubscribers(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	// Scoped, filtered, ordered and paged the way ListLBs is, over a list that
	// holds nothing: the project and organization scope, the name filter and
	// the order the contract declares are each read (#271, #277).
	subscribers := p.env.Store.List(kindLBSubscriber, p.zoneProjectScopeOf(r, zone))
	if name := r.URL.Query().Get("name"); name != "" {
		subscribers = filterResources(subscribers, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
	}, subscribers) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(subscribers))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"subscribers": make([]any, 0, end-start),
		"total_count": len(subscribers),
	})
}

// kindLBSubscriber is the kind a subscriber would be stored under. Nothing
// creates one — CreateSubscriber is declined — so the list is read from a
// register that is empty by construction, through the same scope every other
// list reads through.
const kindLBSubscriber = "lb/subscriber"
