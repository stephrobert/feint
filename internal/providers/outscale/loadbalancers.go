package outscale

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"slices"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Load balancers: the LBU family, served exactly as far as the surveyed stacks
// exercise it (#281).
//
// Three of the five surveyed Outscale stacks (#262) create load balancers, and
// in two of them the LBU was the only re-plan residue. What they call was
// measured with `feint proxy --record` against their exact commits, and read
// from the provider's own resource code (terraform-provider-outscale v1.1.3 and
// v1.8.0), not guessed from the SDK's 23-operation surface:
//
//	CreateLoadBalancer            outscale_load_balancer create (tags inline)
//	ReadLoadBalancers             every resource's read; the destroy sweep asks
//	                              it with an empty body before removing an SG
//	UpdateLoadBalancer            outscale_load_balancer_attributes (health check)
//	RegisterVmsInLoadBalancer     outscale_load_balancer_vms create, provider 1.1.3
//	LinkLoadBalancerBackendMachines    the same create on provider 1.8.0 — the
//	                              measured trace overturned the 1.1.3 source
//	                              reading, which says Register: both are live
//	UnlinkLoadBalancerBackendMachines  outscale_load_balancer_vms destroy
//	DeleteLoadBalancer            destroy, then ReadLoadBalancers until gone
//
// Everything else in the family stays in Declined() by name; the first stack
// that calls one of those is the evidence that reopens it.
//
// Shapes come from the SDK: CreateLoadBalancerRequest at
// .upstream/osc-sdk-go/pkg/osc/client.gen.go:2346, LoadBalancer at :6254,
// HealthCheck at :5618, Listener at :6169. The DnsName format is measured on
// real accounts, not invented: `<name>-<digits>.<region>.lbu.outscale.com`
// (e.g. capstone-lbu-a-225682977.eu-west-2.lbu.outscale.com), and an internal
// load balancer carries an `internal-` prefix
// (internal-talos-prod-k8s-lb-640339891.eu-west-2.lbu.outscale.com).

const kindLoadBalancer = "loadbalancer"

// lbInternetFacing and lbInternal are the two kinds of load balancer, and the
// only two values a create accepts. Named rather than written inline because
// PublicVocabulary has to vouch for exactly the values this refuses, and a
// second copy of a closed list is a copy that drifts silently: the symptom is a
// corpus that replays 400 on every CreateLoadBalancer and reads like an
// emulator defect. Measured on the recording of 2026-08-21, where "internal"
// became "redacted-1208".
const (
	lbInternetFacing = "internet-facing"
	lbInternal       = "internal"
)

// stateDeleting is what the real API reports on the object a delete has just
// accepted, measured on a real account on 2026-08-21: DeleteLoadBalancer
// answers the balancer with State "deleting" (#381). This pack removes it from
// the store at once — #380 carries the whole of that difference — so the state
// is what the answer says rather than what the store holds a moment later.
const stateDeleting = "deleting"

// lbuPublicIPBase is the fictional block the emulator hands to internet-facing
// load balancers. It is TEST-NET-3, distinct from publicIPBase (TEST-NET-2) on
// purpose: the real service associates "a public IP owned by 3DS OUTSCALE"
// (client.gen.go:2360), which never appears in the account's ReadPublicIps, so
// these addresses must not collide with — nor consume — the user's pool.
const lbuPublicIPBase = "203.0.113."

var listenerProtocols = []string{"HTTP", "HTTPS", "TCP", "SSL"}

type listenerForCreation struct {
	BackendPort          int    `json:"BackendPort"`
	BackendProtocol      string `json:"BackendProtocol"`
	LoadBalancerPort     int    `json:"LoadBalancerPort"`
	LoadBalancerProtocol string `json:"LoadBalancerProtocol"`
	ServerCertificateID  string `json:"ServerCertificateId"`
}

type resourceTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type healthCheckBody struct {
	CheckInterval      int    `json:"CheckInterval"`
	HealthyThreshold   int    `json:"HealthyThreshold"`
	Path               string `json:"Path"`
	Port               int    `json:"Port"`
	Protocol           string `json:"Protocol"`
	Timeout            int    `json:"Timeout"`
	UnhealthyThreshold int    `json:"UnhealthyThreshold"`
}

type createLoadBalancerRequest struct {
	Listeners        []listenerForCreation `json:"Listeners"`
	LoadBalancerName string                `json:"LoadBalancerName"`
	LoadBalancerType string                `json:"LoadBalancerType"`
	PublicIP         string                `json:"PublicIp"`
	SecurityGroups   []string              `json:"SecurityGroups"`
	Subnets          []string              `json:"Subnets"`
	SubregionNames   []string              `json:"SubregionNames"`
	Tags             []resourceTag         `json:"Tags"`
	DryRun           *bool                 `json:"DryRun"`
}

// validLoadBalancerName enforces the SDK's own statement: "a maximum length of
// 32 alphanumeric characters and dashes (`-`). This name must not start or end
// with a dash" (client.gen.go:2353).
func validLoadBalancerName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return name[0] != '-' && name[len(name)-1] != '-'
}

func (p *Pack) createLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var req createLoadBalancerRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if !validLoadBalancerName(req.LoadBalancerName) {
		p.badRequest(w, "LoadBalancerName must be 1-32 alphanumeric characters or dashes, and must not start or end with a dash")
		return
	}
	if len(req.Listeners) == 0 {
		p.badRequest(w, "Listeners is required")
		return
	}
	// The same validation CreateLoadBalancerListeners runs (listeners.go), and
	// deliberately not a second copy of it: the two operations take the same
	// ListenerForCreation, and a rule written twice is a rule one of the two
	// paths loses. Nothing is taken yet, so no front port is held against it.
	listeners, refusal := listenerViews(req.Listeners, nil)
	if refusal != "" {
		p.badRequest(w, refusal)
		return
	}
	if req.PublicIP != "" {
		// The real service associates a caller-owned EIP. No surveyed stack
		// sends it; refusing beats storing an address this emulator would then
		// have to double-book against the PublicIp pool.
		p.badRequest(w, "PublicIp is not served on CreateLoadBalancer; the emulator associates its own fictional address")
		return
	}
	if len(req.Subnets) > 0 && len(req.SubregionNames) > 0 {
		p.badRequest(w, "Subnets and SubregionNames are exclusive: a load balancer is either in a Net or in the public Cloud")
		return
	}

	lbType := orDefault(req.LoadBalancerType, lbInternetFacing)
	if lbType != lbInternetFacing && lbType != lbInternal {
		p.badRequest(w, "LoadBalancerType must be internet-facing or internal")
		return
	}

	attrs := map[string]any{
		"Listeners": listeners,
		// The pristine health check, from the vendor's own defaults (Outscale
		// user guide, Configuring Health Checks: interval 30, timeout 5,
		// unhealthy 2, healthy 10): TCP on the first listener's backend port.
		"HealthCheck": map[string]any{
			"CheckInterval":      30,
			"HealthyThreshold":   10,
			"Port":               req.Listeners[0].BackendPort,
			"Protocol":           "TCP",
			"Timeout":            5,
			"UnhealthyThreshold": 2,
		},
		"AccessLog":                        map[string]any{"IsEnabled": false, "PublicationInterval": 60},
		"ApplicationStickyCookiePolicies":  []any{},
		"LoadBalancerStickyCookiePolicies": []any{},
		"BackendVmIds":                     []any{},
		"LoadBalancerType":                 lbType,
		"SecuredCookies":                   false,
		"Tags":                             tagList(req.Tags),
	}

	switch {
	case len(req.Subnets) == 1:
		subnet, found := p.env.Store.Get(Name, kindSubnet, req.Subnets[0])
		if !found {
			p.notFound(w, "Subnet", req.Subnets[0])
			return
		}
		netID := stringOf(subnet.Attrs["NetId"])
		groups := req.SecurityGroups
		if len(groups) == 0 {
			// "If not specified, the default security group of the Net is
			// assigned to the load balancer" (client.gen.go:2363).
			sgID, ok := p.netDefaultSG(netID)
			if !ok {
				p.notFound(w, "default security group of Net", netID)
				return
			}
			groups = []string{sgID}
		}
		for _, sg := range groups {
			if _, found := p.env.Store.Get(Name, kindSecurityGroup, sg); !found {
				p.notFound(w, "security group", sg)
				return
			}
		}
		attrs["Subnets"] = []any{subnet.ID}
		attrs["NetId"] = netID
		attrs["SecurityGroups"] = anyStrings(groups)
		attrs["SubregionNames"] = []any{p.subnetSubregion(subnet.ID)}
		attrs["SourceSecurityGroup"] = p.sourceSecurityGroup(groups[0])
	case len(req.Subnets) > 1:
		// "The ID of the Subnet in which you want to create the load balancer"
		// (client.gen.go:2366) — the parameter is a list carrying one element.
		p.badRequest(w, "Subnets takes exactly one Subnet ID")
		return
	case len(req.SubregionNames) > 0:
		// A public-Cloud load balancer sits outside any Net the emulator
		// models: no subnet to anchor it, no security group of the caller's,
		// and nothing measured about what a real account answers for its
		// SourceSecurityGroup. No surveyed stack takes this path (the one
		// conditional use, osc-k8s-rke-cluster, runs with public_cloud=false).
		// docs/limits.md names the gap.
		p.badRequest(w, "public-Cloud load balancers (SubregionNames) are not served: create the load balancer in a Net (Subnets); docs/limits.md carries the statement")
		return
	default:
		p.badRequest(w, "Subnets is required: this parameter is required in a Net (and the public-Cloud form is not served here)")
		return
	}

	// DnsName, measured format; the digits are minted once and stored, so the
	// name is identical on every later read — anything Terraform stores must be
	// deterministic.
	digits := dnsDigits(p.env.NewID())
	prefix := ""
	if lbType == lbInternal {
		prefix = "internal-"
	}
	attrs["DnsName"] = fmt.Sprintf("%s%s-%s.%s.lbu.outscale.com", prefix, req.LoadBalancerName, digits, p.region)

	// Both addresses under the allocation lock, and the Put inside it, so the
	// allocator a concurrent create rebuilds already sees this balancer's
	// reservations — the same discipline allocateVms follows. The duplicate
	// check lives in here and only here: outside the lock it would only narrow
	// the window, and two concurrent creates of one name must not silently
	// overwrite each other (the #289 shape, one level up from a rule).
	unlock := p.lockAddresses()
	if _, exists := p.env.Store.Get(Name, kindLoadBalancer, req.LoadBalancerName); exists {
		unlock()
		p.conflict(w, "a load balancer named "+req.LoadBalancerName+" already exists")
		return
	}
	pl, err := p.placeInSubnet(req.Subnets[0])
	if err != nil {
		unlock()
		p.badRequest(w, "the Subnet "+req.Subnets[0]+" cannot place the load balancer: "+err.Error())
		return
	}
	// "The primary private IP of the load balancer" (client.gen.go:6294), from
	// the subnet's own pool; subnetAllocator reserves it back.
	attrs["PrivateIp"] = pl.Address.String()
	if lbType == lbInternetFacing {
		address, ok := p.mintLBUAddress()
		if !ok {
			unlock()
			p.conflict(w, "the emulated block "+lbuPublicIPBase+"0/24 is exhausted; delete a load balancer first")
			return
		}
		attrs["PublicIp"] = address
	}

	now := p.env.Now()
	res := resource.New(req.LoadBalancerName, kindLoadBalancer, resource.Tenant{Provider: Name}, "active", now)
	res.Attrs = attrs
	p.env.Store.Put(res)
	unlock()

	// Outside the lock, because handing a balancer to the runtime is a call to
	// another process and the store's lock is measured in microseconds: the
	// rule the whole repository works under, and the one a slow effect inside
	// the lock breaks first.
	p.syncBalancer(r.Context(), res.ID)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"LoadBalancer":    p.loadBalancerView(res),
		"ResponseContext": p.context(),
	})
}

// mintLBUAddress picks the first free address of the load balancers' own
// fictional block. The caller holds the allocation lock.
func (p *Pack) mintLBUAddress() (string, bool) {
	taken := make(map[string]bool, 8)
	for _, res := range p.env.Store.List(kindLoadBalancer, resource.Tenant{Provider: Name}) {
		taken[stringOf(res.Attrs["PublicIp"])] = true
	}
	for host := 1; host < 255; host++ {
		candidate := fmt.Sprintf("%s%d", lbuPublicIPBase, host)
		if !taken[candidate] {
			return candidate, true
		}
	}
	return "", false
}

// dnsDigits turns a fresh ID into the numeric tail real DNS names carry
// (225682977, 640339891: nine digits, measured).
func dnsDigits(seed string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return fmt.Sprintf("%09d", 100000000+h.Sum32()%900000000)
}

func validPort(port int) bool { return port >= 1 && port <= 65535 }

func tagList(tags []resourceTag) []any {
	out := make([]any, 0, len(tags))
	for _, t := range tags {
		out = append(out, map[string]any{"Key": t.Key, "Value": t.Value})
	}
	return out
}

func anyStrings(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// sourceSecurityGroup names the group inbound rules can reference, from the
// group the balancer actually carries — the SDK describes it as "the source
// security group of the load balancer, which you can use as part of your
// inbound rules for your registered VMs" (client.gen.go:6311).
func (p *Pack) sourceSecurityGroup(sgID string) map[string]any {
	name := sgID
	if sg, found := p.env.Store.Get(Name, kindSecurityGroup, sgID); found {
		name = orDefault(stringOf(sg.Attrs["SecurityGroupName"]), sgID)
	}
	return map[string]any{
		"SecurityGroupAccountId": accountID,
		"SecurityGroupName":      name,
	}
}

var loadBalancerFilters = []string{"LoadBalancerNames"}

// readLoadBalancers answers the real inventory. It was the family's first
// served operation — before anything could be created here, the empty answer
// already mattered, because `terraform destroy` asks which load balancers are
// linked to a security group before removing it, and a 404 there failed the
// destroy of a fixture whose apply had just succeeded. That call is still in
// the measured trace: provider 1.8.0 sends ReadLoadBalancers with an empty
// body before each DeleteSecurityGroup.
func (p *Pack) readLoadBalancers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, loadBalancerFilters...) {
		return
	}
	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindLoadBalancer, resource.Tenant{Provider: Name}) {
		if !matchesStrings(req.Filters, "LoadBalancerNames", res.ID) {
			continue
		}
		out = append(out, p.loadBalancerView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"LoadBalancers":   page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

type updateLoadBalancerRequest struct {
	AccessLog           map[string]any   `json:"AccessLog"`
	HealthCheck         *healthCheckBody `json:"HealthCheck"`
	LoadBalancerName    string           `json:"LoadBalancerName"`
	LoadBalancerPort    *int             `json:"LoadBalancerPort"`
	PolicyNames         *[]string        `json:"PolicyNames"`
	PublicIP            *string          `json:"PublicIp"`
	SecuredCookies      *bool            `json:"SecuredCookies"`
	SecurityGroups      *[]string        `json:"SecurityGroups"`
	ServerCertificateID *string          `json:"ServerCertificateId"`
	DryRun              *bool            `json:"DryRun"`
}

func (p *Pack) updateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var req updateLoadBalancerRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.LoadBalancerName == "" {
		p.badRequest(w, "LoadBalancerName is required")
		return
	}
	if req.ServerCertificateID != nil {
		p.badRequest(w, "ServerCertificateId is not served: server certificates are declined, see /_feint/routes")
		return
	}
	if req.PolicyNames != nil && len(*req.PolicyNames) > 0 {
		// The empty list is what outscale_load_balancer_attributes sends on its
		// own update path to mean "no policy", and means nothing here; a named
		// policy would reference CreateLoadBalancerPolicy, which is declined.
		p.badRequest(w, "PolicyNames is not served: load balancer policies are declined, see /_feint/routes")
		return
	}
	if req.PublicIP != nil {
		p.badRequest(w, "PublicIp is not served on UpdateLoadBalancer")
		return
	}
	if req.HealthCheck != nil {
		if err := validateHealthCheck(*req.HealthCheck); err != "" {
			p.badRequest(w, err)
			return
		}
	}
	var groups []string
	if req.SecurityGroups != nil {
		groups = *req.SecurityGroups
		for _, sg := range groups {
			if _, found := p.env.Store.Get(Name, kindSecurityGroup, sg); !found {
				p.notFound(w, "security group", sg)
				return
			}
		}
	}
	if req.AccessLog != nil {
		if enabled, _ := req.AccessLog["IsEnabled"].(bool); enabled {
			p.badRequest(w, "AccessLog.IsEnabled is not served: there is no OOS bucket here to publish into; docs/limits.md carries the statement")
			return
		}
	}

	err := p.env.Store.Update(Name, kindLoadBalancer, req.LoadBalancerName, func(stored *resource.Resource) error {
		if req.HealthCheck != nil {
			hc := map[string]any{
				"CheckInterval":      req.HealthCheck.CheckInterval,
				"HealthyThreshold":   req.HealthCheck.HealthyThreshold,
				"Port":               req.HealthCheck.Port,
				"Protocol":           req.HealthCheck.Protocol,
				"Timeout":            req.HealthCheck.Timeout,
				"UnhealthyThreshold": req.HealthCheck.UnhealthyThreshold,
			}
			if req.HealthCheck.Path != "" {
				hc["Path"] = req.HealthCheck.Path
			}
			stored.Attrs["HealthCheck"] = hc
		}
		if req.SecurityGroups != nil {
			if len(groups) == 0 {
				// "If the list is empty, the default security group of the Net
				// is assigned" (client.gen.go:9969).
				if sgID, ok := p.netDefaultSG(stringOf(stored.Attrs["NetId"])); ok {
					groups = []string{sgID}
				}
			}
			if len(groups) == 0 {
				return nil
			}
			stored.Attrs["SecurityGroups"] = anyStrings(groups)
			stored.Attrs["SourceSecurityGroup"] = p.sourceSecurityGroup(groups[0])
		}
		if req.SecuredCookies != nil {
			stored.Attrs["SecuredCookies"] = *req.SecuredCookies
		}
		if req.AccessLog != nil {
			interval := 60
			if v, ok := req.AccessLog["PublicationInterval"].(float64); ok && v != 0 {
				interval = int(v)
			}
			stored.Attrs["AccessLog"] = map[string]any{"IsEnabled": false, "PublicationInterval": interval}
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		p.notFound(w, "load balancer", req.LoadBalancerName)
		return
	}
	res, found := p.env.Store.Get(Name, kindLoadBalancer, req.LoadBalancerName)
	if !found {
		p.notFound(w, "load balancer", req.LoadBalancerName)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"LoadBalancer":    p.loadBalancerView(res),
		"ResponseContext": p.context(),
	})
}

// validateHealthCheck holds the SDK's own ranges (client.gen.go:5618-5648):
// interval 5-600, thresholds 2-10, timeout 2-60, port 1-65535, protocol one of
// the listener protocols, path only meaningful for HTTP/HTTPS.
func validateHealthCheck(hc healthCheckBody) string {
	switch {
	case hc.CheckInterval < 5 || hc.CheckInterval > 600:
		return "HealthCheck.CheckInterval must be between 5 and 600"
	case hc.HealthyThreshold < 2 || hc.HealthyThreshold > 10:
		return "HealthCheck.HealthyThreshold must be between 2 and 10"
	case hc.UnhealthyThreshold < 2 || hc.UnhealthyThreshold > 10:
		return "HealthCheck.UnhealthyThreshold must be between 2 and 10"
	case hc.Timeout < 2 || hc.Timeout > 60:
		return "HealthCheck.Timeout must be between 2 and 60"
	case !validPort(hc.Port):
		return "HealthCheck.Port must be between 1 and 65535"
	case !slices.Contains(listenerProtocols, hc.Protocol):
		return "HealthCheck.Protocol must be HTTP, HTTPS, TCP or SSL"
	case hc.Path != "" && hc.Protocol != "HTTP" && hc.Protocol != "HTTPS":
		return "HealthCheck.Path is only valid with the HTTP or HTTPS protocols"
	case hc.Path != "" && !strings.HasPrefix(hc.Path, "/"):
		return "HealthCheck.Path always starts with a slash"
	}
	return ""
}

// registerVmsInLoadBalancer serves RegisterVmsInLoadBalancer and
// LinkLoadBalancerBackendMachines with one body: the SDK describes them
// identically, and the measurement forced both — provider 1.1.3 registers,
// provider 1.8.0 links, for the same resource's same create.
func (p *Pack) registerVmsInLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BackendVmIds     []string `json:"BackendVmIds"`
		LoadBalancerName string   `json:"LoadBalancerName"`
		DryRun           *bool    `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.LoadBalancerName == "" || len(req.BackendVmIds) == 0 {
		p.badRequest(w, "LoadBalancerName and BackendVmIds are required")
		return
	}
	for _, vmID := range req.BackendVmIds {
		if _, found := p.env.Store.Get(Name, kindVM, vmID); !found {
			p.notFound(w, "Vm", vmID)
			return
		}
	}
	// The backend list is a collection, so the write goes through Store.Update
	// (#289): two concurrent registrations both land, and neither resurrects a
	// balancer deleted in between.
	err := p.env.Store.Update(Name, kindLoadBalancer, req.LoadBalancerName, func(stored *resource.Resource) error {
		ids := stringsOf(stored.Attrs["BackendVmIds"])
		for _, vmID := range req.BackendVmIds {
			// "Specifying the same ID several times has no effect as each
			// backend VM has equal weight" (client.gen.go:8917).
			if !slices.Contains(ids, vmID) {
				ids = append(ids, vmID)
			}
		}
		stored.Attrs["BackendVmIds"] = anyStrings(ids)
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		p.notFound(w, "load balancer", req.LoadBalancerName)
		return
	}
	p.syncBalancer(r.Context(), req.LoadBalancerName)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func (p *Pack) unlinkLoadBalancerBackendMachines(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BackendIps       []string `json:"BackendIps"`
		BackendVmIds     []string `json:"BackendVmIds"`
		LoadBalancerName string   `json:"LoadBalancerName"`
		DryRun           *bool    `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.LoadBalancerName == "" {
		p.badRequest(w, "LoadBalancerName is required")
		return
	}
	remove := make(map[string]bool, len(req.BackendVmIds))
	for _, vmID := range req.BackendVmIds {
		remove[vmID] = true
	}
	for _, ip := range req.BackendIps {
		for _, vm := range p.env.Store.List(kindVM, resource.Tenant{Provider: Name}) {
			if p.publicIPOf(vm.ID) == ip {
				remove[vm.ID] = true
			}
		}
	}
	err := p.env.Store.Update(Name, kindLoadBalancer, req.LoadBalancerName, func(stored *resource.Resource) error {
		ids := stringsOf(stored.Attrs["BackendVmIds"])
		kept := make([]any, 0, len(ids))
		for _, id := range ids {
			if !remove[id] {
				kept = append(kept, id)
			}
		}
		stored.Attrs["BackendVmIds"] = kept
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		p.notFound(w, "load balancer", req.LoadBalancerName)
		return
	}
	// The unlink half of the same rule: a backend the API has stopped listing
	// must stop receiving connections, and only a replay of the whole set makes
	// that true — the reason EnsureBalancer replaces rather than patches.
	p.syncBalancer(r.Context(), req.LoadBalancerName)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func (p *Pack) deleteLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LoadBalancerName string `json:"LoadBalancerName"`
		DryRun           *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindLoadBalancer, req.LoadBalancerName)
	if !found {
		p.notFound(w, "load balancer", req.LoadBalancerName)
		return
	}
	// Before the store forgets where it was: balancerPlacement reads the
	// subnet's runtime network off the resource, and a deleted resource names
	// nothing. A balancer left behind holds an address of a network the next
	// create would reuse.
	p.removeBalancer(r.Context(), res)
	// The answer is rendered BEFORE the store forgets the balancer, because the
	// real API answers the object it is destroying (#381): DeleteLoadBalancer
	// carries a LoadBalancer, measured on a real account on 2026-08-21, and
	// this pack answered the envelope alone. Rendered from the resource as it
	// stood, with the state the cloud reports at that moment.
	view := p.loadBalancerView(res)
	view["State"] = stateDeleting
	p.env.Store.Delete(Name, kindLoadBalancer, req.LoadBalancerName)
	// The provider polls ReadLoadBalancers until the name is gone; deleting
	// immediately means the first poll already answers "none".
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"LoadBalancer":    view,
		"ResponseContext": p.context(),
	})
}

// loadBalancerView is the LoadBalancer the SDK describes (client.gen.go:6254).
// BackendIps joins the backends' current public addresses at read time — "one
// or more public IPs of backend VMs" — so it never outlives an address the
// machine lost.
func (p *Pack) loadBalancerView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+3)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["LoadBalancerName"] = res.ID
	out["State"] = res.State
	backendIps := make([]any, 0)
	for _, id := range stringsOf(res.Attrs["BackendVmIds"]) {
		if ip := p.publicIPOf(id); ip != "" {
			backendIps = append(backendIps, ip)
		}
	}
	out["BackendIps"] = backendIps
	return out
}

// netDefaultSG finds the stored default security group of a Net — the one the
// pack itself creates with the Net — without ever minting a new one.
func (p *Pack) netDefaultSG(netID string) (string, bool) {
	for _, sg := range p.env.Store.List(kindSecurityGroup, resource.Tenant{Provider: Name}) {
		if stringOf(sg.Attrs["NetId"]) == netID && stringOf(sg.Attrs["SecurityGroupName"]) == "default" {
			return sg.ID, true
		}
	}
	return "", false
}

// loadBalancersOn reports the load balancers holding a reference to the given
// subnet or security group, for the delete guards: removing a subnet or a
// group out from under a balancer is what the real API refuses.
func (p *Pack) loadBalancersOn(attr, id string) []string {
	holders := make([]string, 0, 1)
	for _, res := range p.env.Store.List(kindLoadBalancer, resource.Tenant{Provider: Name}) {
		if slices.Contains(stringsOf(res.Attrs[attr]), id) {
			holders = append(holders, res.ID)
		}
	}
	return holders
}
