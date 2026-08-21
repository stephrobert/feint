package exoscale

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The Network Load Balancer (EXO-5, #345), and the one thing it is not.
//
// # What #14 refused, and what is answered here instead
//
// #14 declined the whole family on a schema reading: a service publishes
// `healthcheck-status`, a per-backend verdict whose enum is `success` or
// `failure` with no third value, so an emulator that probes no backend would
// have to invent one of the two for every server of every pool. That reading
// was right about the enum and wrong about the field. `healthcheck-status` is
// an **array** of `load-balancer-server-status`, and that schema declares no
// required property at all (contracts/exoscale.json, from their published
// document): an entry may carry a backend's address and no verdict. So the
// third value #14 could not find is not a third status — it is an entry with
// no status on it, and it is upstream's own shape rather than one invented
// here.
//
// That is what this serves. Every backend of a service is named, with the
// address a client would probe, and none of them carries a verdict:
//
//	"healthcheck-status": [{"public-ip": "192.0.2.3"}, {"public-ip": "192.0.2.4"}]
//
// which says exactly what is true — these are the servers behind the service,
// and nothing measured their health. An empty array was the other candidate and
// it is worse: it reads as "this service has no backend", which is a claim
// about the pool rather than about the measurement, and it would be false for
// every pool this emulator holds.
//
// TestNoBackendEverCarriesAHealthVerdict is the control, not this comment, and
// the falsification spec exoscale-load-balancer.json removes the omission to
// prove the test bites.
//
// # No dataplane, and the reason is the address
//
// #345 asked whether the Exoscale NLB could be the second customer of
// `machine.Balancer` — the interface #315 built for the Outscale LBU, which
// hands `incus network load-balancer` a listen address and a backend set. It
// cannot, and the reason is upstream's schema rather than the abstraction:
//
//   - `EnsureBalancer` refuses a listen address outside the network's own
//     block, because #315 measured such an address answering for two minutes
//     and going dark for ever (internal/core/machine/balancer.go carries the
//     figures);
//   - an Exoscale NLB publishes exactly one address, `ip`, and their
//     `load-balancer` schema declares no other — no subnet, no private
//     network, no private address. The LBU's `PrivateIp`, which is what makes
//     the Outscale balancer placeable, has no counterpart here;
//   - so the only address this family owns is the public one, drawn from
//     TEST-NET-1 like every other public address of this pack, and it is
//     precisely the address `EnsureBalancer` is specified to refuse.
//
// Measured rather than deduced, on 2026-08-21 against a live `incus-ovn` host,
// on an OVN network this emulator created (10.63.7.0/24). Three calls, and the
// second is the control that makes the first mean something:
//
//	EnsureBalancer, listen 192.0.2.1 (what this pack gives an NLB):
//	  balancer exoscale-nlb listens on 192.0.2.1, which is outside
//	  fnt-m345's own block 10.63.7.0/24: an address the runtime has to
//	  announce goes dark within minutes (#315)
//
//	EnsureBalancer, listen 10.63.7.240, one backend:  <nil>
//
//	the daemon itself, POST .../load-balancers, 192.0.2.1:
//	  Failed creating load balancer: Uplink network doesn't contain
//	  "192.0.2.1/32" in its routes
//
// So the interface is not the obstacle — an in-block address is accepted and
// created — and neither is a provider-shaped gap in it: the runtime refuses
// this pack's address on its own, before any guard of ours is consulted. What
// is missing is an address, and no field of `machine.Balancer` could supply
// one that upstream does not publish.
//
// Hence: this pack never calls the runtime, and it does not call it and swallow
// the error either. A call whose refusal is guaranteed is noise, and the honest
// structure is the one Outscale already has for a balancer with no subnet
// behind it — no placement, no runtime, and docs/limits.md saying so.
//
// # What is stored
//
// A service is a resource of its own rather than a member of an array on the
// balancer, because upstream addresses it by id under the balancer's path and
// mutates it there. That also puts it under the orphan sweep: `Owns` declares
// the service-to-balancer relation, so a service left naming a balancer that is
// gone is a finding rather than a leak.

const (
	kindLoadBalancer        = "load-balancer"
	kindLoadBalancerService = "load-balancer-service"
	nounLoadBalancer        = "load-balancer"

	// The steady state upstream declares for both. `creating`, `migrating` and
	// the rest are transient, and this emulator does not pass through them for
	// the reason docs/limits.md gives about every other transition: an emulated
	// wait is a wait invented for realism.
	loadBalancerRunning = "running"

	// runtimeLoadBalancerKey links a service to the balancer that holds it. In
	// Runtime rather than Attrs, like every other piece of this emulator's own
	// bookkeeping: the service view is the API's shape, and the API expresses
	// the relation through the path, never through a field.
	runtimeLoadBalancerKey = "load-balancer"
)

// loadBalancerView publishes a balancer in the shape their document declares.
// `services` is computed from the store at read time, so a service deleted by
// any path leaves the list without anybody maintaining it — the same reading
// poolView makes of `instances`.
func (p *Pack) loadBalancerView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":         res.ID,
		"state":      res.State,
		"created-at": res.Created.UTC().Format(time.RFC3339),
		"services":   p.loadBalancerServiceViews(res.ID),
	}
	for key, value := range res.Attrs {
		out[key] = value
	}
	return out
}

// loadBalancerServices answers a balancer's services, oldest first, so two
// reads of an unchanged balancer answer the same bytes.
func (p *Pack) loadBalancerServices(balancerID string) []*resource.Resource {
	list := p.env.Store.List(kindLoadBalancerService, resource.Tenant{Provider: Name})
	out := make([]*resource.Resource, 0, len(list))
	for _, res := range list {
		if res.Runtime[runtimeLoadBalancerKey] == balancerID {
			out = append(out, res)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.Before(out[j].Created)
	})
	return out
}

func (p *Pack) loadBalancerServiceViews(balancerID string) []any {
	services := p.loadBalancerServices(balancerID)
	out := make([]any, 0, len(services))
	for _, res := range services {
		out = append(out, p.loadBalancerServiceView(res))
	}
	return out
}

// loadBalancerServiceView renders one service, health included and verdict
// excluded. See the file header: the entries name the backends, and no entry
// carries a `status`.
func (p *Pack) loadBalancerServiceView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":                 res.ID,
		"state":              res.State,
		"healthcheck-status": p.backendStatuses(res),
	}
	for key, value := range res.Attrs {
		out[key] = value
	}
	return out
}

// backendStatuses names the servers a service forwards to, and gives none of
// them a verdict.
//
// The backends are the members of the instance pool the service targets, which
// is where upstream takes them from too — a service names a pool, never a list
// of machines. A member with no public address is skipped rather than named
// with an empty one: `load-balancer-server-status` identifies a backend by its
// address, and an entry identifying nothing would be worse than no entry.
//
// TestNoBackendEverCarriesAHealthVerdict fails if a status is ever attached
// here, and TestAServicesBackendsAreItsPoolsMembers fails if the entries stop
// following the pool.
func (p *Pack) backendStatuses(res *resource.Resource) []any {
	pool, _ := res.Attrs["instance-pool"].(map[string]any)
	poolID, _ := pool["id"].(string)
	out := make([]any, 0)
	if poolID == "" {
		return out
	}
	for _, member := range p.poolMembers(poolID) {
		address, _ := member.Attrs["public-ip"].(string)
		if address == "" {
			continue
		}
		// One key, and deliberately one: `status` is the verdict, nothing here
		// probes a backend, and inventing either enum value is the refusal #14
		// stated and #345 kept.
		out = append(out, map[string]any{"public-ip": address})
	}
	return out
}

type loadBalancerRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels"`
}

func (r loadBalancerRequest) invalid() string {
	for field, value := range map[string]string{"name": r.Name, "description": r.Description} {
		if strings.ContainsAny(value, "\n\r\x00") {
			return field + " carries control characters"
		}
	}
	return ""
}

// createLoadBalancer records the balancer and gives it the address its clients
// will read.
//
// The address comes from the pack's own TEST-NET-1 pool, under the same lock
// every other allocation here takes: it is chosen from what the store holds and
// only becomes taken when the resource lands in it. What it is not is routed —
// see the file header and docs/limits.md.
//
// TestABalancerAndAnElasticIPNeverShareAnAddress fails if the pool stops
// counting balancers.
func (p *Pack) createLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var req loadBalancerRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if bad := req.invalid(); bad != "" {
		writeError(w, http.StatusBadRequest, bad)
		return
	}

	unlock := p.lockAddresses()
	defer unlock()

	ip, ok := p.freeAddress()
	if !ok {
		writeError(w, http.StatusBadRequest, "no address left in the emulated public pool")
		return
	}
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindLoadBalancer, resource.Tenant{Provider: Name}, loadBalancerRunning, now)
	res.Attrs = map[string]any{
		"name":        req.Name,
		"description": req.Description,
		"ip":          ip,
		"labels":      labelsOrEmpty(req.Labels),
	}
	p.env.Store.Put(res)
	p.writeOperation(w, p.operationReferring(nounLoadBalancer, res.ID))
}

func (p *Pack) listLoadBalancers(w http.ResponseWriter, _ *http.Request) {
	list := p.env.Store.List(kindLoadBalancer, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	out := make([]map[string]any, 0, len(list))
	for _, res := range list {
		out = append(out, p.loadBalancerView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"load-balancers": out})
}

func (p *Pack) getLoadBalancer(w http.ResponseWriter, r *http.Request) {
	res, found := p.env.Store.Get(Name, kindLoadBalancer, r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.loadBalancerView(res))
}

// updateLoadBalancer writes the fields the client sent, and only those — the
// rule updateInstance states and every update in this pack follows.
//
// `labels` is written when present rather than when non-empty, for the reason
// updateInstancePool gives about its own lists: emptying a balancer's labels is
// a change a client makes, and reading an empty map as "said nothing" would
// make that call silently do nothing.
func (p *Pack) updateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var req loadBalancerRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if bad := req.invalid(); bad != "" {
		writeError(w, http.StatusBadRequest, bad)
		return
	}
	id := r.PathValue("id")
	err := p.env.Store.Update(Name, kindLoadBalancer, id, func(stored *resource.Resource) error {
		if req.Name != "" {
			stored.Attrs["name"] = req.Name
		}
		if req.Description != "" {
			stored.Attrs["description"] = req.Description
		}
		if req.Labels != nil {
			stored.Attrs["labels"] = req.Labels
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounLoadBalancer, id))
}

// deleteLoadBalancer takes its services with it.
//
// They exist under its path and are addressable nowhere else, so a service that
// outlived its balancer could never be read or removed by any client call —
// the orphan class #215 named, and the reason deleteInstancePool takes its
// members too.
//
// TestDeletingABalancerTakesItsServices fails without it.
func (p *Pack) deleteLoadBalancer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, found := p.env.Store.Get(Name, kindLoadBalancer, id); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	for _, service := range p.loadBalancerServices(id) {
		p.env.Store.Delete(Name, kindLoadBalancerService, service.ID)
	}
	p.env.Store.Delete(Name, kindLoadBalancer, id)
	p.writeOperation(w, p.operationReferring(nounLoadBalancer, id))
}

// resetLoadBalancerField clears one field, in the shape upstream declares for
// this family: a DELETE on the field rather than an update carrying an empty
// value.
//
// The two names are not a choice made here. Their document types the `field`
// path parameter as an enum of exactly `description` and `labels`
// (.upstream/exoscale-openapi.yaml, reset-load-balancer-field), so a third name
// is a request the real API refuses too. Accepting any attribute would let a
// client delete `ip` and leave the balancer answering nothing where an address
// belongs.
func (p *Pack) resetLoadBalancerField(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	field := r.PathValue("field")
	if field != "description" && field != "labels" {
		writeError(w, http.StatusBadRequest, "that field cannot be reset")
		return
	}
	err := p.env.Store.Update(Name, kindLoadBalancer, id, func(stored *resource.Resource) error {
		if field == "labels" {
			stored.Attrs["labels"] = map[string]string{}
			return nil
		}
		stored.Attrs["description"] = ""
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounLoadBalancer, id))
}

type serviceRequest struct {
	Name         string              `json:"name"`
	Description  string              `json:"description"`
	Protocol     string              `json:"protocol"`
	Port         *int64              `json:"port"`
	TargetPort   *int64              `json:"target-port"`
	Strategy     string              `json:"strategy"`
	InstancePool *ref                `json:"instance-pool"`
	Healthcheck  *serviceHealthcheck `json:"healthcheck"`
}

// serviceHealthcheck is their `load-balancer-service-healthcheck`, typed rather
// than kept as a map.
//
// Typed on purpose, and it is a control rather than tidiness: their schema is
// closed, and this emulator enforces closed schemas on what it answers. A block
// read as a map is stored as it arrived and echoed back, so a client sending
// `"uri": 7` or a key their document does not declare would make the emulator
// fail its own contract check on the next read — a response it would refuse
// itself. Decoding into this struct makes encoding/json refuse the wrong type
// at the door, and an undeclared key lands in the unread-field report that
// `mise run conformance` already fails on.
type serviceHealthcheck struct {
	Mode     string `json:"mode"`
	URI      string `json:"uri"`
	TLSSNI   string `json:"tls-sni"`
	Interval *int64 `json:"interval"`
	Timeout  *int64 `json:"timeout"`
	Retries  *int64 `json:"retries"`
	Port     *int64 `json:"port"`
}

// The enumerations their document declares, read from it rather than recalled.
// A value outside them is refused because upstream refuses it, and because a
// balancer storing `strategy: "least-conn"` would answer a client a word its
// own SDK cannot decode.
var (
	serviceProtocols  = map[string]bool{"tcp": true, "udp": true}
	serviceStrategies = map[string]bool{"round-robin": true, "maglev-hash": true, "source-hash": true}
	healthcheckModes  = map[string]bool{"tcp": true, "http": true, "https": true}
)

// healthcheckBounds are the ranges their document declares on each field
// (.upstream/exoscale-openapi.yaml, load-balancer-service-healthcheck). They
// are enforced for the reason the Outscale pack enforces its own: a value the
// real API refuses and this one stores is a plan that converges here and fails
// there, which is the difference this emulator exists to remove.
//
// TestAServiceRefusesAHealthcheckOutsideItsDeclaredRanges fails without them.
var healthcheckBounds = map[string][2]int64{
	"interval": {5, 300},
	"timeout":  {2, 60},
	"retries":  {1, 20},
	"port":     {1, 65535},
}

// invalid answers the first thing wrong with a service, or the empty string.
//
// `required` is asked only of the create: their update schema declares none,
// because an update carries the fields that move and nothing else.
func (r serviceRequest) invalid(creating bool) string {
	for field, value := range map[string]string{"name": r.Name, "description": r.Description} {
		if strings.ContainsAny(value, "\n\r\x00") {
			return field + " carries control characters"
		}
	}
	if creating {
		switch {
		case r.Name == "":
			return "name is required"
		case r.InstancePool == nil || r.InstancePool.ID == "":
			return "instance-pool is required"
		case r.Port == nil:
			return "port is required"
		case r.TargetPort == nil:
			return "target-port is required"
		}
	}
	if r.Protocol != "" && !serviceProtocols[r.Protocol] {
		return "protocol must be tcp or udp"
	}
	if r.Strategy != "" && !serviceStrategies[r.Strategy] {
		return "strategy must be round-robin, maglev-hash or source-hash"
	}
	for _, port := range []*int64{r.Port, r.TargetPort} {
		if port != nil && (*port < 1 || *port > 65535) {
			return "port and target-port must be between 1 and 65535"
		}
	}
	return r.invalidHealthcheck()
}

func (r serviceRequest) invalidHealthcheck() string {
	check := r.Healthcheck
	if check == nil {
		return ""
	}
	if check.Mode != "" && !healthcheckModes[check.Mode] {
		return "healthcheck mode must be tcp, http or https"
	}
	// In a fixed order, so a request breaking two bounds always names the same
	// one: an error message that changes between two identical calls is a
	// message nobody can test against.
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"interval", check.Interval},
		{"port", check.Port},
		{"retries", check.Retries},
		{"timeout", check.Timeout},
	} {
		if field.value == nil {
			continue
		}
		bounds := healthcheckBounds[field.name]
		if *field.value < bounds[0] || *field.value > bounds[1] {
			return "healthcheck " + field.name + " is outside the range the API declares"
		}
	}
	return ""
}

// addServiceToLoadBalancer creates one service under a balancer.
//
// The instance pool is checked to exist, the way createInstancePool checks its
// template: a service naming a pool nothing holds would answer 200 and then
// publish a backend list that can never be anything but empty, and the client
// would read that as a pool with no members rather than as a reference it got
// wrong.
func (p *Pack) addServiceToLoadBalancer(w http.ResponseWriter, r *http.Request) {
	balancerID := r.PathValue("id")
	if _, found := p.env.Store.Get(Name, kindLoadBalancer, balancerID); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	var req serviceRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if bad := req.invalid(true); bad != "" {
		writeError(w, http.StatusBadRequest, bad)
		return
	}
	if _, found := p.env.Store.Get(Name, kindPool, req.InstancePool.ID); !found {
		writeError(w, http.StatusBadRequest, "the instance pool is not one this zone holds")
		return
	}

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindLoadBalancerService, resource.Tenant{Provider: Name}, loadBalancerRunning, now)
	res.Attrs = map[string]any{
		"name":          req.Name,
		"description":   req.Description,
		"protocol":      orDefault(req.Protocol, "tcp"),
		"strategy":      orDefault(req.Strategy, "round-robin"),
		"port":          *req.Port,
		"target-port":   *req.TargetPort,
		"instance-pool": map[string]any{"id": req.InstancePool.ID},
		"healthcheck":   healthcheckOrDefault(req.Healthcheck, *req.TargetPort),
	}
	res.Runtime = map[string]string{runtimeLoadBalancerKey: balancerID}
	p.env.Store.Put(res)
	p.writeOperation(w, p.serviceOperation(balancerID))
}

// healthcheckOrDefault fills what a client left out with the defaults the
// official CLI documents on its own flags — mode tcp, interval 10, timeout 5,
// retries 1, and the port defaulting to the target port. Not invented: they are
// what `exo compute load-balancer service add --help` prints, so a client that
// omits the block gets what that client would have sent.
//
// `uri` and `tls-sni` have no default and are written only when the client sent
// one: their document gives neither a value, and a `""` on the wire is a field
// this emulator would have made up.
func healthcheckOrDefault(check *serviceHealthcheck, targetPort int64) map[string]any {
	out := map[string]any{
		"mode":     "tcp",
		"interval": int64(10),
		"timeout":  int64(5),
		"retries":  int64(1),
		"port":     targetPort,
	}
	if check == nil {
		return out
	}
	if check.Mode != "" {
		out["mode"] = check.Mode
	}
	if check.URI != "" {
		out["uri"] = check.URI
	}
	if check.TLSSNI != "" {
		out["tls-sni"] = check.TLSSNI
	}
	for name, value := range map[string]*int64{
		"interval": check.Interval,
		"timeout":  check.Timeout,
		"retries":  check.Retries,
		"port":     check.Port,
	} {
		if value != nil {
			out[name] = *value
		}
	}
	return out
}

func (p *Pack) getLoadBalancerService(w http.ResponseWriter, r *http.Request) {
	res, found := p.serviceOf(r.PathValue("id"), r.PathValue("serviceID"))
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.loadBalancerServiceView(res))
}

// serviceOf reads a service and checks it really hangs off the balancer named
// in the path.
//
// Asked rather than assumed: the id is a path segment a client composes, and
// answering a service that belongs to another balancer would let a client edit
// a service through a balancer that does not hold it — a well-formed request
// that is not an authorised one, the distinction CLAUDE.md devotes a section
// to.
//
// TestAServiceIsOnlyReachableThroughItsOwnBalancer fails without the check.
func (p *Pack) serviceOf(balancerID, serviceID string) (*resource.Resource, bool) {
	res, found := p.env.Store.Get(Name, kindLoadBalancerService, serviceID)
	if !found || res.Runtime[runtimeLoadBalancerKey] != balancerID {
		return nil, false
	}
	return res, true
}

// updateLoadBalancerService writes the fields the client sent, and only those.
func (p *Pack) updateLoadBalancerService(w http.ResponseWriter, r *http.Request) {
	balancerID := r.PathValue("id")
	serviceID := r.PathValue("serviceID")
	if _, found := p.serviceOf(balancerID, serviceID); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	var req serviceRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if bad := req.invalid(false); bad != "" {
		writeError(w, http.StatusBadRequest, bad)
		return
	}
	err := p.env.Store.Update(Name, kindLoadBalancerService, serviceID, func(stored *resource.Resource) error {
		for field, value := range map[string]string{
			"name": req.Name, "description": req.Description,
			"protocol": req.Protocol, "strategy": req.Strategy,
		} {
			if value != "" {
				stored.Attrs[field] = value
			}
		}
		if req.Port != nil {
			stored.Attrs["port"] = *req.Port
		}
		if req.TargetPort != nil {
			stored.Attrs["target-port"] = *req.TargetPort
		}
		if req.Healthcheck != nil {
			// Replaced whole rather than merged, which is what their update
			// schema describes: `healthcheck` is one property, and a client
			// sending it sends the block it wants to hold.
			stored.Attrs["healthcheck"] = healthcheckOrDefault(req.Healthcheck, int64Of(stored.Attrs["target-port"]))
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.serviceOperation(balancerID))
}

func (p *Pack) deleteLoadBalancerService(w http.ResponseWriter, r *http.Request) {
	balancerID := r.PathValue("id")
	serviceID := r.PathValue("serviceID")
	if _, found := p.serviceOf(balancerID, serviceID); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.env.Store.Delete(Name, kindLoadBalancerService, serviceID)
	p.writeOperation(w, p.serviceOperation(balancerID))
}

// resetLoadBalancerServiceField clears the one field their own enum names:
// their document types this `field` parameter as an enum of `description`
// alone.
func (p *Pack) resetLoadBalancerServiceField(w http.ResponseWriter, r *http.Request) {
	balancerID := r.PathValue("id")
	serviceID := r.PathValue("serviceID")
	if r.PathValue("field") != "description" {
		writeError(w, http.StatusBadRequest, "that field cannot be reset")
		return
	}
	if _, found := p.serviceOf(balancerID, serviceID); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	err := p.env.Store.Update(Name, kindLoadBalancerService, serviceID, func(stored *resource.Resource) error {
		stored.Attrs["description"] = ""
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.serviceOperation(balancerID))
}

// serviceOperation is the envelope a service mutation answers, and it names the
// **balancer** rather than the service.
//
// That is the opposite of what it looked like it should be, and a real client
// settled it. The first version referenced the service — the id and the link of
// the thing just created, which is what every other mutation of this pack does.
// `terraform apply` then failed on `exoscale_nlb_service` with
//
//	Error: Get ".../v2/load-balancer/81e3ad79-…": resource not found
//
// where 81e3ad79 was the service id: the provider had passed the reference
// straight to a load-balancer read. egoscale v2 says why, in its own words
// (v2/network_load_balancer_service.go:121 at v0.102.4):
//
//	// The API doesn't return the NLB service created directly, so in order to
//	// return a *NetworkLoadBalancerService … we have to manually compare the
//	// list of services on the NLB before and after the service creation
//
// and it does that by calling GetNetworkLoadBalancer with the reference's id.
// So on the real API this operation refers to the balancer, and the service is
// found by diffing its `services` list. Delete and update ignore the reference
// altogether, and are given the same envelope: nothing measured says they
// differ, and one shape for the family beats three guesses.
//
// The exo CLI could not have found this. It resolves every object by listing
// and filtering, so it never reads a reference at all — which is exactly why
// "a unit test passed" is not evidence here.
//
// TestAServiceMutationReferencesItsBalancer fails if the reference drifts back
// to the service.
func (p *Pack) serviceOperation(balancerID string) map[string]any {
	return p.operationReferring(nounLoadBalancer, balancerID)
}
