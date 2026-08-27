package scaleway

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The Load Balancer's children: backends, frontends, ACLs and routes. Shapes
// come from the SDK (lb_sdk.go: Backend at 1408, Frontend at 1606, ACL at
// 1747, Route at 1850, and their Create/Update requests). Two wire details
// matter and are easy to get wrong:
//
//   - the timeouts (timeout_server, check_delay, …) ride as integer
//     milliseconds — marshaler.Duration in the SDK — while timeout_queue and
//     transient_check_delay are scw.Duration strings ("1.000000000s"). The
//     handlers store both verbatim and echo them back, so a client reads the
//     exact value it sent.
//   - a backend's health check carries precisely one of seven config objects;
//     the empty TCP config `{}` is a value, not an omission.

// healthCheckRequest mirrors lb/v1's HealthCheck for decoding. Config objects
// stay raw: their content is echoed, never interpreted, because nothing here
// probes a backend (see the file comment in loadbalancer.go).
type healthCheckRequest struct {
	Port                int32           `json:"port"`
	CheckDelay          *int64          `json:"check_delay"`
	CheckTimeout        *int64          `json:"check_timeout"`
	CheckMaxRetries     int32           `json:"check_max_retries"`
	CheckSendProxy      bool            `json:"check_send_proxy"`
	TCPConfig           json.RawMessage `json:"tcp_config"`
	MysqlConfig         json.RawMessage `json:"mysql_config"`
	PgsqlConfig         json.RawMessage `json:"pgsql_config"`
	LdapConfig          json.RawMessage `json:"ldap_config"`
	RedisConfig         json.RawMessage `json:"redis_config"`
	HTTPConfig          json.RawMessage `json:"http_config"`
	HTTPSConfig         json.RawMessage `json:"https_config"`
	TransientCheckDelay json.RawMessage `json:"transient_check_delay"`
}

// healthCheckAttrs normalises the request into the stored, servable object.
func healthCheckAttrs(req *healthCheckRequest, forwardPort int32) map[string]any {
	out := map[string]any{
		"port":              req.Port,
		"check_delay":       req.CheckDelay,
		"check_timeout":     req.CheckTimeout,
		"check_max_retries": req.CheckMaxRetries,
		"check_send_proxy":  req.CheckSendProxy,
		"tcp_config":        nil,
		"mysql_config":      nil,
		"pgsql_config":      nil,
		"ldap_config":       nil,
		"redis_config":      nil,
		"http_config":       nil,
		"https_config":      nil,
	}
	if req.Port == 0 {
		out["port"] = forwardPort
	}
	configured := false
	for key, raw := range map[string]json.RawMessage{
		"tcp_config":   req.TCPConfig,
		"mysql_config": req.MysqlConfig,
		"pgsql_config": req.PgsqlConfig,
		"ldap_config":  req.LdapConfig,
		"redis_config": req.RedisConfig,
		"http_config":  req.HTTPConfig,
		"https_config": req.HTTPSConfig,
	} {
		if len(raw) > 0 && string(raw) != "null" {
			var value map[string]any
			if json.Unmarshal(raw, &value) == nil {
				out[key] = normalizedHTTPCheck(key, value)
				configured = true
			}
		}
	}
	if !configured {
		out["tcp_config"] = map[string]any{}
	}
	if len(req.TransientCheckDelay) > 0 && string(req.TransientCheckDelay) != "null" {
		var value any
		if json.Unmarshal(req.TransientCheckDelay, &value) == nil {
			out["transient_check_delay"] = value
		}
	} else {
		out["transient_check_delay"] = nil
	}
	return out
}

// normalizedHTTPCheck fills the fields the SDK always renders on the HTTP and
// HTTPS configs, so a create that sent {"uri": "/healthz"} reads back the
// complete object the real API answers (uri, method, code, host_header, sni).
func normalizedHTTPCheck(key string, value map[string]any) map[string]any {
	if key != "http_config" && key != "https_config" {
		return value
	}
	for _, field := range []string{"uri", "method", "host_header"} {
		if _, ok := value[field]; !ok {
			value[field] = ""
		}
	}
	if _, ok := value["code"]; !ok {
		value["code"] = nil
	}
	if key == "https_config" {
		if _, ok := value["sni"]; !ok {
			value["sni"] = ""
		}
	}
	return value
}

type backendRequest struct {
	Name                     string              `json:"name"`
	ForwardProtocol          string              `json:"forward_protocol"`
	ForwardPort              int32               `json:"forward_port"`
	ForwardPortAlgorithm     string              `json:"forward_port_algorithm"`
	StickySessions           string              `json:"sticky_sessions"`
	StickySessionsCookieName string              `json:"sticky_sessions_cookie_name"`
	HealthCheck              *healthCheckRequest `json:"health_check"`
	ServerIP                 []string            `json:"server_ip"`
	SendProxyV2              *bool               `json:"send_proxy_v2"`
	TimeoutServer            *int64              `json:"timeout_server"`
	TimeoutConnect           *int64              `json:"timeout_connect"`
	TimeoutTunnel            *int64              `json:"timeout_tunnel"`
	OnMarkedDownAction       string              `json:"on_marked_down_action"`
	ProxyProtocol            string              `json:"proxy_protocol"`
	FailoverHost             *string             `json:"failover_host"`
	SslBridging              *bool               `json:"ssl_bridging"`
	IgnoreSslServerVerify    *bool               `json:"ignore_ssl_server_verify"`
	RedispatchAttemptCount   *int32              `json:"redispatch_attempt_count"`
	MaxRetries               *int32              `json:"max_retries"`
	MaxConnections           *int32              `json:"max_connections"`
	TimeoutQueue             json.RawMessage     `json:"timeout_queue"`
	Host                     *string             `json:"host"`
}

var lbProtocols = map[string]bool{"tcp": true, "http": true}

func (p *Pack) listLBBackends(w http.ResponseWriter, r *http.Request) {
	lb, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	all := p.env.Store.List(kindLBBackend, resource.Tenant{Provider: Name})
	all = filterResources(all, func(res *resource.Resource) bool {
		return res.Attrs["lb_id"] == lb.ID
	})
	if name := r.URL.Query().Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
	}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	backends := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		backends = append(backends, p.lbBackendView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"backends":    backends,
		"total_count": len(all),
	})
}

func (p *Pack) createLBBackend(w http.ResponseWriter, r *http.Request) {
	lb, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	var req backendRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if !lbProtocols[req.ForwardProtocol] {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "forward_protocol",
			Reason:       "constraint",
			HelpMessage:  "forward_protocol must be tcp or http",
		})
		return
	}
	if req.HealthCheck == nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "health_check", Reason: "required"})
		return
	}

	now := p.env.Now()
	id := p.env.NewID()
	res := resource.New(id, kindLBBackend, resource.Tenant{Provider: Name, Project: textOf(lb.Attrs["project_id"]), Zone: lb.Tenant.Zone}, "active", now)
	res.Attrs = backendAttrs(&req, lb.ID)
	res.Attrs["health_check"] = healthCheckAttrs(req.HealthCheck, req.ForwardPort)
	res.Attrs["pool"] = orEmpty(req.ServerIP)
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, p.lbBackendView(res))
}

// backendAttrs stores what create and update share; the pool and the health
// check have endpoints of their own and are set by the callers that own them.
func backendAttrs(req *backendRequest, lbID string) map[string]any {
	return map[string]any{
		"lb_id":                       lbID,
		"name":                        req.Name,
		"forward_protocol":            req.ForwardProtocol,
		"forward_port":                req.ForwardPort,
		"forward_port_algorithm":      orDefault(req.ForwardPortAlgorithm, "roundrobin"),
		"sticky_sessions":             orDefault(req.StickySessions, "none"),
		"sticky_sessions_cookie_name": req.StickySessionsCookieName,
		"send_proxy_v2":               falseIfAbsent(req.SendProxyV2),
		"timeout_server":              req.TimeoutServer,
		"timeout_connect":             req.TimeoutConnect,
		"timeout_tunnel":              req.TimeoutTunnel,
		"on_marked_down_action":       orDefault(req.OnMarkedDownAction, "on_marked_down_action_none"),
		"proxy_protocol":              orDefault(req.ProxyProtocol, "proxy_protocol_none"),
		"failover_host":               req.FailoverHost,
		"ssl_bridging":                falseIfAbsent(req.SslBridging),
		"ignore_ssl_server_verify":    req.IgnoreSslServerVerify,
		"redispatch_attempt_count":    req.RedispatchAttemptCount,
		"max_retries":                 req.MaxRetries,
		"max_connections":             req.MaxConnections,
		"timeout_queue":               rawOrNil(req.TimeoutQueue),
		"host":                        emptyIfAbsent(req.Host),
	}
}

// falseIfAbsent and emptyIfAbsent give a backend the concrete value the cloud
// answers where the request carried none, instead of the JSON null a nil
// pointer serialises to.
//
// Measured rather than guessed: corpus/scaleway/scw-billed-shapes.jsonl seq 13
// is a CreateBackend whose body names neither send_proxy_v2, ssl_bridging nor
// host, and fr-par answers false, false and "". The three other optional
// pointers of the same request — failover_host, ignore_ssl_server_verify,
// timeout_queue — come back null on that same exchange, so they keep their nil
// and are deliberately not routed through these.
//
// The three appear on eleven operations, because a backend is nested inside a
// frontend and a frontend inside an ACL, so 45 findings of
// `feint corpus --check` named this one default.
// TestABackendAnswersTheCloudsConcreteDefaults fails without this.
// The return type is `any` and not bool or string because the value this
// answers is the one the field carries, and the defect being prevented is
// precisely a *pointer* reaching the response: a typed return could not express
// the wrong answer, which would leave the mutation that proves these unwritable
// (tools/falsify/specs/recorded-lb-shapes.json).
func falseIfAbsent(v *bool) any {
	if v == nil {
		return false
	}
	return *v
}

func emptyIfAbsent(v *string) any {
	if v == nil {
		return ""
	}
	return *v
}

func rawOrNil(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func (p *Pack) getLBBackend(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBBackend, "backendID", "backend")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.lbBackendView(res))
}

func (p *Pack) updateLBBackend(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBBackend, "backendID", "backend")
	if !ok {
		return
	}
	var req backendRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if !lbProtocols[req.ForwardProtocol] {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "forward_protocol",
			Reason:       "constraint",
			HelpMessage:  "forward_protocol must be tcp or http",
		})
		return
	}
	err := p.env.Store.Update(Name, kindLBBackend, res.ID, func(stored *resource.Resource) error {
		fresh := backendAttrs(&req, textOf(stored.Attrs["lb_id"]))
		fresh["health_check"] = stored.Attrs["health_check"]
		fresh["pool"] = stored.Attrs["pool"]
		stored.Attrs = fresh
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "backend", res.ID)
		return
	}
	current, _ := p.env.Store.Get(Name, kindLBBackend, res.ID)
	emulator.WriteJSON(w, http.StatusOK, p.lbBackendView(current))
}

func (p *Pack) deleteLBBackend(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBBackend, "backendID", "backend")
	if !ok {
		return
	}
	// A backend a frontend still forwards to does not vanish under it.
	for _, fe := range p.env.Store.List(kindLBFrontend, resource.Tenant{Provider: Name}) {
		if fe.Attrs["backend_id"] == res.ID {
			writePrecondition(w, "backend", res.ID, "backend is still used by a frontend; delete the frontend first")
			return
		}
	}
	for _, route := range p.env.Store.List(kindLBRoute, resource.Tenant{Provider: Name}) {
		if route.Attrs["backend_id"] == res.ID {
			writePrecondition(w, "backend", res.ID, "backend is still the target of a route; delete the route first")
			return
		}
	}
	p.env.Store.Delete(Name, kindLBBackend, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

type backendServersRequest struct {
	ServerIP []string `json:"server_ip"`
}

func (p *Pack) setLBBackendServers(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBBackend, "backendID", "backend")
	if !ok {
		return
	}
	var req backendServersRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	err := p.env.Store.Update(Name, kindLBBackend, res.ID, func(stored *resource.Resource) error {
		stored.Attrs["pool"] = orEmpty(req.ServerIP)
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "backend", res.ID)
		return
	}
	current, _ := p.env.Store.Get(Name, kindLBBackend, res.ID)
	emulator.WriteJSON(w, http.StatusOK, p.lbBackendView(current))
}

func (p *Pack) updateLBHealthCheck(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBBackend, "backendID", "backend")
	if !ok {
		return
	}
	var req healthCheckRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	// Through the shared reader: forward_port is written as an int32 and comes
	// back a float64 once the store has crossed a snapshot, so the assertion
	// this replaces answered 0 and a restored backend's health check probed
	// port 0 (#542) — a check that can only ever fail, installed by a call that
	// answered 200. The bound is what a value from a restored snapshot needs
	// before it becomes an int32: a stored number is untrusted input.
	// TestARestoredBackendsHealthCheckKeepsItsForwardPort fails without this.
	var forwardPort int32
	if port := resource.Int64(res, "forward_port"); port > 0 && port <= math.MaxInt32 {
		forwardPort = int32(port)
	}
	attrs := healthCheckAttrs(&req, forwardPort)
	err := p.env.Store.Update(Name, kindLBBackend, res.ID, func(stored *resource.Resource) error {
		stored.Attrs["health_check"] = attrs
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "backend", res.ID)
		return
	}
	// UpdateHealthCheck answers the health check alone, not the backend.
	emulator.WriteJSON(w, http.StatusOK, attrs)
}

func (p *Pack) lbBackendView(res *resource.Resource) map[string]any {
	var lbView any
	if lb, found := p.env.Store.Get(Name, kindLB, textOf(res.Attrs["lb_id"])); found {
		lbView = p.lbView(lb)
	}
	return map[string]any{
		"id":                          res.ID,
		"name":                        res.Attrs["name"],
		"forward_protocol":            res.Attrs["forward_protocol"],
		"forward_port":                res.Attrs["forward_port"],
		"forward_port_algorithm":      res.Attrs["forward_port_algorithm"],
		"sticky_sessions":             res.Attrs["sticky_sessions"],
		"sticky_sessions_cookie_name": res.Attrs["sticky_sessions_cookie_name"],
		"health_check":                res.Attrs["health_check"],
		"pool":                        res.Attrs["pool"],
		"lb":                          lbView,
		"send_proxy_v2":               res.Attrs["send_proxy_v2"],
		"timeout_server":              res.Attrs["timeout_server"],
		"timeout_connect":             res.Attrs["timeout_connect"],
		"timeout_tunnel":              res.Attrs["timeout_tunnel"],
		"on_marked_down_action":       res.Attrs["on_marked_down_action"],
		"proxy_protocol":              res.Attrs["proxy_protocol"],
		"created_at":                  res.Created.Format(time.RFC3339),
		"updated_at":                  res.Updated.Format(time.RFC3339),
		"failover_host":               res.Attrs["failover_host"],
		"ssl_bridging":                res.Attrs["ssl_bridging"],
		"ignore_ssl_server_verify":    res.Attrs["ignore_ssl_server_verify"],
		"redispatch_attempt_count":    res.Attrs["redispatch_attempt_count"],
		"max_retries":                 res.Attrs["max_retries"],
		"max_connections":             res.Attrs["max_connections"],
		"timeout_queue":               res.Attrs["timeout_queue"],
		"host":                        res.Attrs["host"],
	}
}

// ---- Frontends ----------------------------------------------------------------

type frontendRequest struct {
	Name                string    `json:"name"`
	InboundPort         int32     `json:"inbound_port"`
	BackendID           string    `json:"backend_id"`
	TimeoutClient       *int64    `json:"timeout_client"`
	CertificateID       *string   `json:"certificate_id"`
	CertificateIDs      *[]string `json:"certificate_ids"`
	EnableHTTP3         bool      `json:"enable_http3"`
	ConnectionRateLimit *uint32   `json:"connection_rate_limit"`
	EnableAccessLogs    bool      `json:"enable_access_logs"`
}

func (p *Pack) listLBFrontends(w http.ResponseWriter, r *http.Request) {
	lb, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	all := p.env.Store.List(kindLBFrontend, resource.Tenant{Provider: Name})
	all = filterResources(all, func(res *resource.Resource) bool {
		return res.Attrs["lb_id"] == lb.ID
	})
	if name := r.URL.Query().Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
	}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	frontends := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		frontends = append(frontends, p.lbFrontendView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"frontends":   frontends,
		"total_count": len(all),
	})
}

func (p *Pack) createLBFrontend(w http.ResponseWriter, r *http.Request) {
	lb, ok := p.zonalResourceOf(w, r, kindLB, "lbID", "lb")
	if !ok {
		return
	}
	var req frontendRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	backend, found := p.env.Store.Get(Name, kindLBBackend, req.BackendID)
	if !found || backend.Attrs["lb_id"] != lb.ID {
		writeNotFound(w, "backend", req.BackendID)
		return
	}
	if req.CertificateID != nil || (req.CertificateIDs != nil && len(*req.CertificateIDs) > 0) {
		// Certificates are declined by name (pack.go); accepting the
		// reference would store an ID nothing can read back.
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "certificate_ids",
			Reason:       "constraint",
			HelpMessage:  "certificates are not served: nothing here terminates TLS, see /_feint/routes",
		})
		return
	}
	now := p.env.Now()
	id := p.env.NewID()
	res := resource.New(id, kindLBFrontend, resource.Tenant{Provider: Name, Project: textOf(lb.Attrs["project_id"]), Zone: lb.Tenant.Zone}, "active", now)
	res.Attrs = frontendAttrs(&req, lb.ID)
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, p.lbFrontendView(res))
}

func frontendAttrs(req *frontendRequest, lbID string) map[string]any {
	return map[string]any{
		"lb_id":                 lbID,
		"name":                  req.Name,
		"inbound_port":          req.InboundPort,
		"backend_id":            req.BackendID,
		"timeout_client":        req.TimeoutClient,
		"enable_http3":          req.EnableHTTP3,
		"connection_rate_limit": req.ConnectionRateLimit,
		"enable_access_logs":    req.EnableAccessLogs,
	}
}

func (p *Pack) getLBFrontend(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBFrontend, "frontendID", "frontend")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.lbFrontendView(res))
}

func (p *Pack) updateLBFrontend(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBFrontend, "frontendID", "frontend")
	if !ok {
		return
	}
	var req frontendRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	backend, found := p.env.Store.Get(Name, kindLBBackend, req.BackendID)
	if !found || backend.Attrs["lb_id"] != res.Attrs["lb_id"] {
		writeNotFound(w, "backend", req.BackendID)
		return
	}
	err := p.env.Store.Update(Name, kindLBFrontend, res.ID, func(stored *resource.Resource) error {
		stored.Attrs = frontendAttrs(&req, textOf(stored.Attrs["lb_id"]))
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "frontend", res.ID)
		return
	}
	current, _ := p.env.Store.Get(Name, kindLBFrontend, res.ID)
	emulator.WriteJSON(w, http.StatusOK, p.lbFrontendView(current))
}

func (p *Pack) deleteLBFrontend(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBFrontend, "frontendID", "frontend")
	if !ok {
		return
	}
	for _, route := range p.env.Store.List(kindLBRoute, resource.Tenant{Provider: Name}) {
		if route.Attrs["frontend_id"] == res.ID {
			writePrecondition(w, "frontend", res.ID, "frontend is still the source of a route; delete the route first")
			return
		}
	}
	p.deleteFrontendCascade(res.ID)
	w.WriteHeader(http.StatusNoContent)
}

// deleteFrontendCascade removes a frontend and the ACLs that live on it, which
// upstream owns through the frontend: an ACL cannot outlive it.
func (p *Pack) deleteFrontendCascade(frontendID string) {
	for _, acl := range p.env.Store.List(kindLBACL, resource.Tenant{Provider: Name}) {
		if acl.Attrs["frontend_id"] == frontendID {
			p.env.Store.Delete(Name, kindLBACL, acl.ID)
		}
	}
	p.env.Store.Delete(Name, kindLBFrontend, frontendID)
}

func (p *Pack) lbFrontendView(res *resource.Resource) map[string]any {
	var lbView any
	if lb, found := p.env.Store.Get(Name, kindLB, textOf(res.Attrs["lb_id"])); found {
		lbView = p.lbView(lb)
	}
	var backendView any
	if backend, found := p.env.Store.Get(Name, kindLBBackend, textOf(res.Attrs["backend_id"])); found {
		backendView = p.lbBackendView(backend)
	}
	return map[string]any{
		"id":             res.ID,
		"name":           res.Attrs["name"],
		"inbound_port":   res.Attrs["inbound_port"],
		"backend":        backendView,
		"lb":             lbView,
		"timeout_client": res.Attrs["timeout_client"],
		// The deprecated singular the SDK still declares beside the plural, and
		// which the real cloud answers on every frontend: null when none is
		// bound. Measured on 2026-08-24 on a real LB-S
		// (corpus/scaleway/scw-billed-shapes.jsonl, #427), where it is null on
		// the frontend and on the frontend an ACL carries.
		//
		// Serving null invents nothing: this emulator has no certificate
		// surface at all, so null is the only value it could ever have, and it
		// is exactly the value that was observed. Omitting the key was the
		// difference a client comparing field sets would see.
		//
		// TestAFrontendCarriesTheCertificateKeyItCanOnlyEverHoldNull fails
		// without it, and so does the omission gate of a conformance run
		// (tools/conformance/score.sh, FEINT_FIELD_GATE), which is what
		// reported it.
		"certificate":           nil,
		"certificate_ids":       []any{},
		"created_at":            res.Created.Format(time.RFC3339),
		"updated_at":            res.Updated.Format(time.RFC3339),
		"enable_http3":          res.Attrs["enable_http3"],
		"connection_rate_limit": res.Attrs["connection_rate_limit"],
		"enable_access_logs":    res.Attrs["enable_access_logs"],
	}
}

// ---- ACLs ---------------------------------------------------------------------

type aclRequest struct {
	Name        string          `json:"name"`
	Action      json.RawMessage `json:"action"`
	Match       json.RawMessage `json:"match"`
	Index       int32           `json:"index"`
	Description string          `json:"description"`
}

// aclAction validates the action's type and normalises the object. The type
// field is what the dataplane would branch on, so an unknown one is refused
// rather than recorded.
func aclAction(w http.ResponseWriter, raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "action", Reason: "required"})
		return nil, false
	}
	var action map[string]any
	if err := json.Unmarshal(raw, &action); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "action", Reason: "format", HelpMessage: err.Error()})
		return nil, false
	}
	kind, _ := action["type"].(string)
	switch kind {
	case "allow", "deny":
		action["redirect"] = nil
	case "redirect":
		// The redirect sub-object rides as sent.
	default:
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "action.type",
			Reason:       "constraint",
			HelpMessage:  "the ACL action must be allow, deny or redirect",
		})
		return nil, false
	}
	return action, true
}

// aclMatch normalises the match object to the complete shape the SDK renders.
func aclMatch(raw json.RawMessage) map[string]any {
	match := map[string]any{
		"ip_subnet":          []any{},
		"ips_edge_services":  false,
		"http_filter":        "acl_http_filter_none",
		"http_filter_value":  []any{},
		"http_filter_option": nil,
		"invert":             false,
	}
	if len(raw) == 0 || string(raw) == "null" {
		return match
	}
	var sent map[string]any
	if json.Unmarshal(raw, &sent) != nil {
		return match
	}
	for key, value := range sent {
		if value == nil {
			continue
		}
		if key == "http_filter" {
			if s, _ := value.(string); s == "" {
				continue
			}
		}
		match[key] = value
	}
	return match
}

func (p *Pack) listLBACLs(w http.ResponseWriter, r *http.Request) {
	fe, ok := p.zonalResourceOf(w, r, kindLBFrontend, "frontendID", "frontend")
	if !ok {
		return
	}
	all := p.env.Store.List(kindLBACL, resource.Tenant{Provider: Name})
	all = filterResources(all, func(res *resource.Resource) bool {
		return res.Attrs["frontend_id"] == fe.ID
	})
	if name := r.URL.Query().Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	// index ascending is the order the dataplane would apply them in, and the
	// SDK's default.
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
	}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	acls := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		acls = append(acls, p.lbACLView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"acls":        acls,
		"total_count": len(all),
	})
}

func (p *Pack) createLBACL(w http.ResponseWriter, r *http.Request) {
	fe, ok := p.zonalResourceOf(w, r, kindLBFrontend, "frontendID", "frontend")
	if !ok {
		return
	}
	var req aclRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	action, ok := aclAction(w, req.Action)
	if !ok {
		return
	}
	now := p.env.Now()
	id := p.env.NewID()
	res := resource.New(id, kindLBACL, resource.Tenant{Provider: Name, Project: fe.Tenant.Project, Zone: fe.Tenant.Zone}, "active", now)
	res.Attrs = map[string]any{
		"frontend_id": fe.ID,
		"name":        req.Name,
		"action":      action,
		"match":       aclMatch(req.Match),
		"index":       req.Index,
		"description": req.Description,
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, p.lbACLView(res))
}

func (p *Pack) getLBACL(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBACL, "aclID", "acl")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.lbACLView(res))
}

func (p *Pack) updateLBACL(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBACL, "aclID", "acl")
	if !ok {
		return
	}
	var req aclRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	action, ok := aclAction(w, req.Action)
	if !ok {
		return
	}
	err := p.env.Store.Update(Name, kindLBACL, res.ID, func(stored *resource.Resource) error {
		stored.Attrs["name"] = req.Name
		stored.Attrs["action"] = action
		stored.Attrs["match"] = aclMatch(req.Match)
		stored.Attrs["index"] = req.Index
		if req.Description != "" {
			stored.Attrs["description"] = req.Description
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "acl", res.ID)
		return
	}
	current, _ := p.env.Store.Get(Name, kindLBACL, res.ID)
	emulator.WriteJSON(w, http.StatusOK, p.lbACLView(current))
}

func (p *Pack) deleteLBACL(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBACL, "aclID", "acl")
	if !ok {
		return
	}
	p.env.Store.Delete(Name, kindLBACL, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Pack) lbACLView(res *resource.Resource) map[string]any {
	var frontendView any
	if fe, found := p.env.Store.Get(Name, kindLBFrontend, textOf(res.Attrs["frontend_id"])); found {
		frontendView = p.lbFrontendView(fe)
	}
	return map[string]any{
		"id":          res.ID,
		"name":        res.Attrs["name"],
		"match":       res.Attrs["match"],
		"action":      res.Attrs["action"],
		"frontend":    frontendView,
		"index":       res.Attrs["index"],
		"created_at":  res.Created.Format(time.RFC3339),
		"updated_at":  res.Updated.Format(time.RFC3339),
		"description": res.Attrs["description"],
	}
}

// ---- Routes -------------------------------------------------------------------

type routeRequest struct {
	FrontendID string          `json:"frontend_id"`
	BackendID  string          `json:"backend_id"`
	Match      json.RawMessage `json:"match"`
}

// routeMatch normalises the match to the complete shape the SDK renders: one
// of sni, host_header or path_begin, plus match_subdomains.
func routeMatch(raw json.RawMessage) map[string]any {
	match := map[string]any{
		"sni":              nil,
		"host_header":      nil,
		"path_begin":       nil,
		"match_subdomains": false,
	}
	if len(raw) == 0 || string(raw) == "null" {
		return match
	}
	var sent map[string]any
	if json.Unmarshal(raw, &sent) != nil {
		return match
	}
	for key, value := range sent {
		if value != nil {
			match[key] = value
		}
	}
	return match
}

func (p *Pack) listLBRoutes(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	all := p.env.Store.List(kindLBRoute, resource.Tenant{Provider: Name, Zone: zone})
	// A frontend_id naming nothing is a refusal, not an empty page. Measured
	// against the real cloud on 2026-08-21 (corpus/scaleway/scw-refusals.jsonl):
	// `scw lb route list frontend-id=<absent>` answers 404 "frontend not Found",
	// where this pack answered 200 with an empty list — the answer a client
	// reads as "that frontend carries no route" rather than "there is no such
	// frontend". TestListingRoutesOfAnAbsentFrontendIsRefused fails without it.
	if id := r.URL.Query().Get("frontend_id"); id != "" {
		fe, found := p.env.Store.Get(Name, kindLBFrontend, id)
		if !found || fe.Tenant.Zone != zone {
			writeNotFound(w, "frontend", id)
			return
		}
		all = filterResources(all, func(res *resource.Resource) bool {
			return res.Attrs["frontend_id"] == id
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
	}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	routes := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		routes = append(routes, p.lbRouteView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"routes":      routes,
		"total_count": len(all),
	})
}

func (p *Pack) createLBRoute(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req routeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	fe, found := p.env.Store.Get(Name, kindLBFrontend, req.FrontendID)
	if !found || fe.Tenant.Zone != zone {
		writeNotFound(w, "frontend", req.FrontendID)
		return
	}
	backend, found := p.env.Store.Get(Name, kindLBBackend, req.BackendID)
	if !found || backend.Attrs["lb_id"] != fe.Attrs["lb_id"] {
		writeNotFound(w, "backend", req.BackendID)
		return
	}
	now := p.env.Now()
	id := p.env.NewID()
	res := resource.New(id, kindLBRoute, resource.Tenant{Provider: Name, Project: fe.Tenant.Project, Zone: zone}, "active", now)
	res.Attrs = map[string]any{
		"frontend_id": fe.ID,
		"backend_id":  backend.ID,
		"match":       routeMatch(req.Match),
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, p.lbRouteView(res))
}

func (p *Pack) getLBRoute(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBRoute, "routeID", "route")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.lbRouteView(res))
}

func (p *Pack) updateLBRoute(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBRoute, "routeID", "route")
	if !ok {
		return
	}
	var req routeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	backend, found := p.env.Store.Get(Name, kindLBBackend, req.BackendID)
	if !found {
		writeNotFound(w, "backend", req.BackendID)
		return
	}
	err := p.env.Store.Update(Name, kindLBRoute, res.ID, func(stored *resource.Resource) error {
		stored.Attrs["backend_id"] = backend.ID
		stored.Attrs["match"] = routeMatch(req.Match)
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "route", res.ID)
		return
	}
	current, _ := p.env.Store.Get(Name, kindLBRoute, res.ID)
	emulator.WriteJSON(w, http.StatusOK, p.lbRouteView(current))
}

func (p *Pack) deleteLBRoute(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindLBRoute, "routeID", "route")
	if !ok {
		return
	}
	p.env.Store.Delete(Name, kindLBRoute, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Pack) lbRouteView(res *resource.Resource) map[string]any {
	return map[string]any{
		"id":          res.ID,
		"frontend_id": res.Attrs["frontend_id"],
		"backend_id":  res.Attrs["backend_id"],
		"match":       res.Attrs["match"],
		"created_at":  res.Created.Format(time.RFC3339),
		"updated_at":  res.Updated.Format(time.RFC3339),
	}
}
