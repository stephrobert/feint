// Package outscale emulates the Outscale API.
//
// Outscale speaks a different dialect from the other two packs, and that is the
// point of hosting it here: the core must stay protocol-neutral. Every call is a
// POST on /api/v1/<Action> with a JSON body, responses carry a ResponseContext
// with a RequestId, and errors come back as an Errors array. Authentication is
// AWS Signature v4; the emulator accepts any signature without checking it.
//
// Shapes are read from Outscale's own OpenAPI document, which ships inside their
// Go SDK, and the emulator checks itself against it: contracts/outscale.json is
// extracted from that document and internal/contract fails a response carrying a
// field the API does not define. That is not a rule invented here — 643 of their
// 650 schemas declare additionalProperties: false.
//
// Everything the pack serves is declared in Routes with its upstream operation
// name, and everything it deliberately does not serve is declared in Declined.
package outscale

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// Name is the provider key.
const Name = "outscale"

// The resource kinds this pack stores.
const (
	kindVM      = "vm"
	kindKeypair = "keypair"
	kindVolume  = "volume"
)

// Pack implements emulator.Pack for Outscale.
type Pack struct {
	env *emulator.Env
	// addresses serialises the choice of a block, which is read-modify-write
	// over the store: what is free is computed from what exists, and two
	// concurrent creates without it both find the same range free and both take
	// it. Terraform creates ten resources at a time by default.
	addresses sync.Mutex
}

// New returns an Outscale pack backed by env.
func New(env *emulator.Env) *Pack { return &Pack{env: env} }

// Name implements emulator.Pack.
func (p *Pack) Name() string { return Name }

// operation returns the canonical name of an Outscale call, in the form the
// drift scan produces from their SDK. The action itself is what the contract
// keys on, which is why both live one string apart.
func operation(action string) string { return "osc/Client." + action }

// maxDryRunProbe bounds the body read to answer a dry run. Generous for a
// request document, and matched to what the server accepts rather than chosen
// below it: the probe puts the body back for the handler, so a smaller bound
// here truncated real requests.
const maxDryRunProbe = emulator.MaxBody

// route declares one action. Outscale has no path structure to speak of: every
// call is a POST on /api/v1/<Action>, so a route is fully described by its
// action name and its handler.
func (p *Pack) route(action string, handler http.HandlerFunc) emulator.Route {
	return emulator.Route{
		Method:    "POST",
		Path:      pathPrefix + action,
		Operation: operation(action),
		Handler:   p.dryRunnable(handler),
	}
}

// dryRunnable answers a DryRun without reaching the handler.
//
// Here rather than in each request struct, because the field is on all twenty
// served actions in Outscale's own API description and the first attempt at this
// honoured it in six: an audit then ran `DeleteVms {"DryRun": true}` and watched
// the machine be destroyed. A control implemented per handler is a control the
// destructive handlers were missing, which is the worst possible distribution of
// it.
//
// What this does NOT do is validate, and saying so is the point: the real API
// checks the request and reports what would happen. Here the body is read and
// the call stops. A client gets "nothing changed", which is true, and never
// "your request is valid", which would be invented. docs/limits.md records the
// difference.
//
// TestDryRunReachesNoHandler fails without this.
func (p *Pack) dryRunnable(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Read here rather than by a handler, so the unread-field report is
		// told: otherwise `DryRun: false` — a legitimate request every client
		// can send — counted as a field nobody read and failed the conformance
		// gate. TestDryRunFalseDoesNotFailTheGate holds it.
		emulator.MarkRead(r, "DryRun")
		// Bounded like every other body: a dry run must not be a way to make the
		// emulator read an unbounded request.
		body, err := io.ReadAll(io.LimitReader(r.Body, maxDryRunProbe))
		if err != nil {
			handler(w, r)
			return
		}
		// The body is put back whatever happens: the handler owns it from here.
		r.Body = io.NopCloser(bytes.NewReader(body))

		var probe struct {
			DryRun *bool `json:"DryRun"`
		}
		if err := json.Unmarshal(body, &probe); err != nil || probe.DryRun == nil || !*probe.DryRun {
			handler(w, r)
			return
		}
		// ResponseContext alone. The first version answered a top-level
		// "DryRun": true, which the pack's own contract rejects — the response
		// schemas are closed — and the comment claimed the SDK carries the
		// marker there. It does not: ResponseContext is {RequestId}.
		emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
	}
}

// Routes implements emulator.Pack.
func (p *Pack) Routes() []emulator.Route {
	return []emulator.Route{
		// Vms: the machine and its lifecycle.
		p.route("ReadVms", p.readVms),
		p.route("CreateVms", p.createVms),
		p.route("UpdateVm", p.updateVm),
		p.route("ReadAdminPassword", p.readAdminPassword),

		// Tags, which the Terraform provider calls on almost every resource.
		p.route("CreateTags", p.createTags),
		p.route("ReadTags", p.readTags),
		p.route("DeleteTags", p.deleteTags),

		// Volumes, which the Terraform provider creates and reads back, and the
		// lightweight state view it polls.
		p.route("CreateVolume", p.createVolume),
		p.route("ReadVolumes", p.readVolumes),
		p.route("UpdateVolume", p.updateVolume),
		p.route("DeleteVolume", p.deleteVolume),
		p.route("LinkVolume", p.linkVolume),
		p.route("UnlinkVolume", p.unlinkVolume),
		p.route("ReadVmsState", p.readVmsState),
		p.route("DeleteVms", p.deleteVms),
		p.route("StartVms", p.startVms),
		p.route("StopVms", p.stopVms),
		p.route("RebootVms", p.rebootVms),

		// The inventory every client reads before it creates anything. The
		// Scaleway pack learned this the hard way: decline the catalogue and the
		// official CLI cannot create a server at all.
		p.route("ReadVmTypes", p.readVmTypes),
		p.route("ReadImages", p.readImages),
		p.route("ReadRegions", p.readRegions),
		p.route("ReadSubregions", p.readSubregions),

		// Nets and Subnets: the addressing plane. A block is parsed, its mask
		// bounded, its containment and its overlap checked, and the address count
		// computed from the mask — none of which the emulators this project
		// measures itself against do.
		p.route("CreateNet", p.createNet),
		p.route("ReadNets", p.readNets),
		p.route("DeleteNet", p.deleteNet),
		p.route("UpdateNet", p.updateNet),
		p.route("CreateSubnet", p.createSubnet),
		p.route("ReadSubnets", p.readSubnets),
		p.route("DeleteSubnet", p.deleteSubnet),
		p.route("UpdateSubnet", p.updateSubnet),

		// Keypairs, on the critical path to a machine anyone can log into.
		p.route("CreateKeypair", p.createKeypair),
		p.route("ReadKeypairs", p.readKeypairs),
		p.route("DeleteKeypair", p.deleteKeypair),

		// What a Net is born with — its default security group, its main route
		// table, the account's DHCP options set — and the interfaces its
		// machines carry. The shapes are measured against a real account
		// (X-2 sweep, 2026-08-08); the DHCP lifecycle is #172's second tranche,
		// which is what lets a client create a set and point a Net at it.
		p.route("ReadSecurityGroups", p.readSecurityGroups),
		p.route("CreateSecurityGroup", p.createSecurityGroup),
		p.route("DeleteSecurityGroup", p.deleteSecurityGroup),
		p.route("CreateSecurityGroupRule", p.createSecurityGroupRule),
		p.route("DeleteSecurityGroupRule", p.deleteSecurityGroupRule),
		p.route("ReadRouteTables", p.readRouteTables),
		p.route("CreateRouteTable", p.createRouteTable),
		p.route("DeleteRouteTable", p.deleteRouteTable),
		p.route("LinkRouteTable", p.linkRouteTable),
		p.route("UnlinkRouteTable", p.unlinkRouteTable),
		p.route("UpdateRouteTableLink", p.updateRouteTableLink),
		p.route("CreateRoute", p.createRoute),
		p.route("DeleteRoute", p.deleteRoute),
		p.route("UpdateRoute", p.updateRoute),
		p.route("ReadDhcpOptions", p.readDhcpOptions),
		p.route("CreateDhcpOptions", p.createDhcpOptions),
		p.route("DeleteDhcpOptions", p.deleteDhcpOptions),
		p.route("ReadNics", p.readNics),
		p.route("CreateNic", p.createNic),
		p.route("DeleteNic", p.deleteNic),
		p.route("UpdateNic", p.updateNic),
		p.route("LinkNic", p.linkNic),
		p.route("UnlinkNic", p.unlinkNic),

		// The gateway a Net attaches, and the egress a subnet buys with an
		// address: the resource algebra Terraform's destroy order depends on.
		// Control plane only — internetservices.go says what does not flow.
		p.route("CreateInternetService", p.createInternetService),
		p.route("ReadInternetServices", p.readInternetServices),
		p.route("LinkInternetService", p.linkInternetService),
		p.route("UnlinkInternetService", p.unlinkInternetService),
		p.route("DeleteInternetService", p.deleteInternetService),
		p.route("CreatePublicIp", p.createPublicIP),
		p.route("ReadPublicIps", p.readPublicIPs),
		p.route("DeletePublicIp", p.deletePublicIP),
		p.route("LinkPublicIp", p.linkPublicIP),
		p.route("UnlinkPublicIp", p.unlinkPublicIP),
		p.route("CreateNatService", p.createNatService),
		p.route("ReadNatServices", p.readNatServices),
		p.route("DeleteNatService", p.deleteNatService),

		// Snapshots as control-plane records (OSC-4, #13); snapshots.go carries
		// the no-bytes caveat.
		p.route("CreateSnapshot", p.createSnapshot),
		p.route("ReadSnapshots", p.readSnapshots),
		p.route("CreateImage", p.createImage),
		p.route("UpdateImage", p.updateImage),
		p.route("DeleteImage", p.deleteImage),
		p.route("DeleteSnapshot", p.deleteSnapshot),

		// The region's fixed catalogues, same rule as ReadVmTypes: what a
		// client reads on its way to creating something is served, small and
		// fixed.
		// The inventory of load balancers, which is none. The rest of the
		// family stays declined; loadbalancers.go draws the line.
		p.route("ReadLoadBalancers", p.readLoadBalancers),

		p.route("ReadNetAccessPointServices", p.readNetAccessPointServices),
		p.route("ReadPublicIpRanges", p.readPublicIPRanges),
	}
}

// pathPrefix is the whole of Outscale's URL space: one flat namespace, every
// call a POST on /api/v1/<Action>.
const pathPrefix = "/api/v1/"

// Prefixes implements emulator.Unrouted.
// legacyPrefix is what the deprecated osc-cli addresses. Nothing is served
// under it and nothing ever will be, but claiming it is what lets this pack
// answer in its own dialect instead of letting net/http's one-line page tell a
// user nothing about which side is wrong (#179).
const legacyPrefix = "/api/latest/"

func (p *Pack) Prefixes() []string { return []string{pathPrefix, legacyPrefix} }

// NotFound implements emulator.Unrouted.
//
// The body is the SDK's ErrorResponse shape, so a client decodes an API error
// instead of choking on net/http's text/plain. Code is deliberately empty: the
// numeric codes are Outscale's own catalogue, this is the emulator speaking, and
// minting a number would be exactly the kind of invented format this project
// never ships: a shape comes from their SDK or not at all. An empty
// code is what "not one of theirs" looks like, and no upstream check matches it.
//
// 404 rather than 501, decided by what the real client does. 501 says what is
// true — the operation exists, this server does not implement it — but oapi-cli
// treats it as retryable and spends 12 seconds on three backed-off attempts
// before giving up, measured. On 404 it returns at once. With most of the
// surface still unserved, that answer is the common case, not the exception.
//
// The Go SDK would not have shown this: it retries through go-retryablehttp,
// whose default policy excludes 501 by name. Two official clients, two
// behaviours, and only the one that was run said anything.
func (p *Pack) NotFound(w http.ResponseWriter, r *http.Request) {
	// First contact is the one moment a user cannot yet tell a broken emulator
	// from a broken pointing, and the two shapes below are the ones the README
	// documents. Answering them with "feint does not serve X" was worse than
	// unhelpful: for a doubled prefix it names an operation that *is* served, so
	// a team's first oapi-cli call concludes the coverage table lied (#179).
	//
	// Still 404 in every case. The request stays refused; only the refusal
	// starts telling the truth.
	if strings.HasPrefix(r.URL.Path, legacyPrefix) {
		p.writeError(w, http.StatusNotFound, "", "OperationNotEmulated",
			"feint serves "+strings.TrimSuffix(pathPrefix, "/")+" and not "+
				strings.TrimSuffix(legacyPrefix, "/")+": osc-cli is deprecated and addresses "+
				"an API version this emulator does not serve. oapi-cli is the client the "+
				"conformance suite drives; `feint env outscale` prints its configuration")
		return
	}

	action := strings.TrimPrefix(r.URL.Path, pathPrefix)
	if doubled := strings.TrimPrefix(pathPrefix, "/"); strings.HasPrefix(action, doubled) {
		// The endpoint already carries the prefix and the client appended it
		// again. Naming the operation here would be the confident, wrong answer.
		p.writeError(w, http.StatusNotFound, "", "OperationNotEmulated",
			"the endpoint carries "+strings.TrimSuffix(pathPrefix, "/")+" twice: point the "+
				"client at the bare host, oapi-cli appends "+strings.TrimSuffix(pathPrefix, "/")+
				"/<Call> itself. `feint env outscale` prints the endpoint to use")
		return
	}

	p.writeError(w, http.StatusNotFound, "", "OperationNotEmulated",
		"feint does not serve "+action+"; see /_feint/routes for what it does")
}

type responseContext struct {
	RequestID string `json:"RequestId"`
}

func (p *Pack) context() responseContext {
	return responseContext{RequestID: p.env.NewID()}
}

// Outscale identifiers are a prefix and a hexadecimal suffix, not UUIDs. The
// prefixes and their lengths are read from their OpenAPI document, which spells
// them out in the field descriptions: 91 examples of vpc-, 63 of i-, 56 of sg-,
// all eight hexadecimal characters, and key- alone at thirty-two.
//
// It matters because clients validate the shape. A UUID where an i-<8> belongs
// is the kind of thing that passes every unit test and fails the first real
// call.
const (
	idLen        = 8
	keypairIDLen = 32
)

// newID builds one, prefix and eight hexadecimal characters.
func newID(prefix, seed string) string { return prefix + "-" + hexOf(seed, idLen) }

func newVMID(seed string) string      { return newID("i", seed) }
func newKeypairID(seed string) string { return "key-" + hexOf(seed, keypairIDLen) }

// hexOf takes the hexadecimal characters of a generated UUID, which is where the
// entropy is; the dashes carry none.
func hexOf(seed string, n int) string {
	trimmed := strings.ReplaceAll(seed, "-", "")
	if len(trimmed) < n {
		// Cannot happen with the env's own UUIDs, which give 32 characters.
		// Padding rather than panicking keeps a bad seed from taking the server
		// down over a cosmetic property.
		return trimmed + strings.Repeat("0", n-len(trimmed))
	}
	return trimmed[:n]
}

// boolOr reads an optional boolean the way the API treats one: absent means the
// default, which is not the same as false when the field is a refusal.
func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// Env implements emulator.Pack.
//
// The values come from tools/conformance/outscale/fake-credentials.env, so the
// CLI and the suite cannot drift apart. They are deliberately public and open
// nothing: the emulator accepts any signature without checking it, but the
// client still refuses to sign a request whose credentials are not well-formed.
//
// OSC_ENDPOINT_API takes the bare host. The CLI appends /api/v1/<Call> itself,
// so an endpoint carrying that prefix produces a request for
// /api/v1/api/v1/<Call> — a 404 that reads exactly like a missing route, and an
// afternoon to diagnose the first time.
func (p *Pack) Env(endpoint string) emulator.Environment {
	return emulator.Environment{
		Vars: map[string]string{
			"OSC_ACCESS_KEY":   "AAAAAAAAAAAAAAAAAAAA",
			"OSC_SECRET_KEY":   "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
			"OSC_REGION":       regionName,
			"OSC_PROTOCOL":     "http",
			"OSC_ENDPOINT_API": endpoint,
		},
		Note: "oapi-cli reads a JSON profile rather than the environment: --config, with endpoints.api set to " +
			endpoint + " (the bare host; it appends /api/v1 itself).",
	}
}
