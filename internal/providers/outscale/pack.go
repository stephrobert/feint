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
// field the API does not define. That is not a rule invented here — nearly every
// schema they publish declares additionalProperties: false, and the artefact's
// closedPolicy records that the declaration is theirs. The exact count is
// rendered into the README from the artefact by `feint docs`, not restated here,
// because a restated figure was measured stale once already.
//
// Everything the pack serves is declared in Routes with its upstream operation
// name, and everything it deliberately does not serve is declared in Declined.
package outscale

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/serialise"
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
	// region is where this deployment claims to live — a datum, not a
	// constant (#290). At Outscale every region speaks the same API, so the
	// region is a property of the endpoint a client is pointed at, and an
	// emulator with a single endpoint chooses one at construction. Fixed for
	// the pack's lifetime, like a real endpoint: a region that moved mid-run
	// would leave the store holding zones the write paths then refuse.
	region string
	// subregions is the catalogue of the region's own subregions, and the
	// authority every write path validates against (knownSubregion): the
	// #269 invariant, kept whichever region is in force.
	subregions []map[string]any
	// defaultSubregion is where anything lands when the client names no
	// subregion — the API's own behaviour for a create outside a Net. The
	// region's first zone, which is the "<region>a" every region publishes.
	defaultSubregion string
}

// lockAddresses serialises the choice of a block or an address, which is
// read-modify-write over the store: what is free is computed from what exists,
// and two concurrent creates without it both find the same range free and both
// take it. Terraform creates ten resources at a time by default. The comments
// at the call sites name it the addressing lock, because it also orders the
// scans that keep a Net, a Subnet and a DHCP options set pointing at each
// other. The lock itself lives in core/serialise, shared with every pack and
// the machine binding, so the next pack does not have to rediscover it.
func (p *Pack) lockAddresses() func() { return serialise.Lock(Name + "/addresses") }

// New returns an Outscale pack backed by env, serving the default region.
// Nothing configured keeps today's behaviour: eu-west-2, exactly as before
// the region became selectable.
func New(env *emulator.Env) *Pack { return newInRegion(env, defaultRegionName) }

// NewInRegion returns a pack serving the named region, or an error naming
// the regions Outscale publishes when it publishes no such region. Refusing
// beats defaulting: an emulator that answered eu-west-2 to an operator who
// asked for cloudgouv-eu-west-1 would be the exact lie #268 was about, moved
// to startup.
func NewInRegion(env *emulator.Env, region string) (*Pack, error) {
	if _, ok := regionCatalogue[region]; !ok {
		return nil, fmt.Errorf("outscale publishes no region %q (it publishes %s)",
			region, strings.Join(regionNames(), ", "))
	}
	return newInRegion(env, region), nil
}

// newInRegion builds the pack with everything the region decides. Callers
// guarantee the region is in the catalogue.
func newInRegion(env *emulator.Env, region string) *Pack {
	subs, _ := subregionsOf(region)
	return &Pack{
		env:              env,
		region:           region,
		subregions:       subs,
		defaultSubregion: stringOf(subs[0]["SubregionName"]),
	}
}

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

// What this pack can never earn on the two axes a suite claims, and why.
//
// Every line below was measured before it was written, by opening a real
// behaviour span against a fresh emulator, driving the operation through a full
// cycle, and reading back what the span marked (2026-08-24). Two of the
// thirteen candidates came back earnable and are NOT declared here: DeleteTags
// is marked when the resource it untags is created and destroyed inside the
// span, and CreatePublicIp is refused when the address block runs out. Both are
// driven by octl.sh instead, which is the point of measuring first.

// statelessBehaviour declares a catalogue read out of reach on `behaviour`.
//
// The axis marks an operation whose store touches fall on a resource created
// and destroyed inside the span. These handlers answer from a table in the
// binary and touch no store at all, so there is nothing to attribute — measured,
// not assumed: TestAnUnearnableNoStoreTouchIsMeasured (internal/cli) drives
// each one and
// requires the call to be answered AND the store to see nothing.
func statelessBehaviour(what string) emulator.Unearnable {
	return emulator.Unearnable{
		Axis:  emulator.ProvesBehaviour,
		Cause: emulator.CauseNoStoreTouch,
		Reason: "no lifecycle can be attributed to it: it answers " + what +
			" from a fixed table and touches no store, so nothing it reads is ever created and destroyed",
	}
}

// keptSubjectBehaviour declares an operation out of reach on `behaviour`
// because its subject outlives its own delete here, as it does upstream.
func keptSubjectBehaviour(subject, upstream string) emulator.Unearnable {
	return emulator.Unearnable{
		Axis:  emulator.ProvesBehaviour,
		Cause: emulator.CauseNoDestruction,
		Reason: "its subject is " + subject + ", which this emulator marks rather than removes, because " + upstream +
			" — so the store never sees the destruction half of a lifecycle for it",
	}
}

// nothingToRefuse declares an operation out of reach on `negative`.
func nothingToRefuse(what string) emulator.Unearnable {
	return emulator.Unearnable{
		Axis:  emulator.ProvesNegative,
		Cause: emulator.CauseNoRefusableRequest,
		Reason: "no supported client can compose a request it must refuse: " + what +
			", and this emulator holds no state that can fail the call either",
	}
}

// unearnable attaches axis declarations to a route.
func unearnable(r emulator.Route, u ...emulator.Unearnable) emulator.Route {
	r.Unearnable = u
	return r
}

// Routes implements emulator.Pack.
func (p *Pack) Routes() []emulator.Route {
	return []emulator.Route{
		// Vms: the machine and its lifecycle.
		p.route("ReadVms", p.readVms),
		p.route("CreateVms", p.createVms),
		p.route("UpdateVm", p.updateVm),
		unearnable(p.route("ReadAdminPassword", p.readAdminPassword),
			keptSubjectBehaviour("a Vm", "a terminated machine stays readable on the real cloud and a client polls it there")),

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
		unearnable(p.route("ReadVmsState", p.readVmsState),
			keptSubjectBehaviour("a Vm", "a terminated machine stays readable on the real cloud and a client polls it there")),
		p.route("DeleteVms", p.deleteVms),
		p.route("StartVms", p.startVms),
		unearnable(p.route("StopVms", p.stopVms),
			keptSubjectBehaviour("a Vm", "a terminated machine stays readable on the real cloud and a client polls it there")),
		p.route("RebootVms", p.rebootVms),

		// The inventory every client reads before it creates anything. The
		// Scaleway pack learned this the hard way: decline the catalogue and the
		// official CLI cannot create a server at all.
		unearnable(p.route("ReadVmTypes", p.readVmTypes),
			statelessBehaviour("the type catalogue")),
		p.route("ReadImages", p.readImages),
		unearnable(p.route("ReadRegions", p.readRegions),
			statelessBehaviour("the region and the endpoint a client calls next"),
			nothingToRefuse("ReadRegionsRequest declares DryRun and nothing else")),
		unearnable(p.route("ReadSubregions", p.readSubregions),
			statelessBehaviour("the region's zones")),

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
		p.route("LinkPrivateIps", p.linkPrivateIps),
		p.route("UnlinkPrivateIps", p.unlinkPrivateIps),
		p.route("UnlinkNic", p.unlinkNic),

		// The gateway a Net attaches, and the egress a subnet buys with an
		// address: the resource algebra Terraform's destroy order depends on.
		// Control plane only — internetservices.go says what does not flow.
		unearnable(p.route("CreateInternetService", p.createInternetService),
			nothingToRefuse("CreateInternetServiceRequest declares DryRun and nothing else")),
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

		// Net peerings: both ends are Nets this emulator creates, which is what
		// kept the family off the declined list. The lifecycle states come from
		// the SDK's NetPeeringStateName enum; netpeerings.go names what
		// mono-tenancy makes indistinguishable.
		p.route("CreateNetPeering", p.createNetPeering),
		unearnable(p.route("AcceptNetPeering", p.acceptNetPeering),
			keptSubjectBehaviour("a Net peering", "a deleted peering stays readable in the deleted state, which the SDK's own StateNames filter enumerates")),
		unearnable(p.route("RejectNetPeering", p.rejectNetPeering),
			keptSubjectBehaviour("a Net peering", "a deleted peering stays readable in the deleted state, which the SDK's own StateNames filter enumerates")),
		unearnable(p.route("DeleteNetPeering", p.deleteNetPeering),
			keptSubjectBehaviour("a Net peering", "a deleted peering stays readable in the deleted state, which the SDK's own StateNames filter enumerates")),
		unearnable(p.route("ReadNetPeerings", p.readNetPeerings),
			keptSubjectBehaviour("a Net peering", "a deleted peering stays readable in the deleted state, which the SDK's own StateNames filter enumerates")),

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
		// The LBU family, exactly as far as the surveyed stacks exercise it
		// (#281): loadbalancers.go carries the measurement, declined.go the
		// remainder.
		p.route("CreateLoadBalancer", p.createLoadBalancer),
		p.route("ReadLoadBalancers", p.readLoadBalancers),
		p.route("UpdateLoadBalancer", p.updateLoadBalancer),
		p.route("CreateLoadBalancerListeners", p.createLoadBalancerListeners),
		p.route("DeleteLoadBalancerListeners", p.deleteLoadBalancerListeners),
		p.route("RegisterVmsInLoadBalancer", p.registerVmsInLoadBalancer),
		p.route("LinkLoadBalancerBackendMachines", p.registerVmsInLoadBalancer),
		p.route("UnlinkLoadBalancerBackendMachines", p.unlinkLoadBalancerBackendMachines),
		p.route("DeleteLoadBalancer", p.deleteLoadBalancer),

		unearnable(p.route("ReadNetAccessPointServices", p.readNetAccessPointServices),
			statelessBehaviour("the services a Net access point can target")),
		unearnable(p.route("ReadPublicIpRanges", p.readPublicIPRanges),
			statelessBehaviour("the public blocks the cloud routes")),
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
				"an API version this emulator does not serve. octl is the client the "+
				"conformance suite drives; `feint env outscale --client octl` prints its "+
				"configuration")
		return
	}

	action := strings.TrimPrefix(r.URL.Path, pathPrefix)
	if doubled := strings.TrimPrefix(pathPrefix, "/"); strings.HasPrefix(action, doubled) {
		// The endpoint already carries the prefix and the client appended it
		// again. Naming the operation here would be the confident, wrong answer.
		p.writeError(w, http.StatusNotFound, "", "OperationNotEmulated",
			"the endpoint carries "+strings.TrimSuffix(pathPrefix, "/")+" twice: point the "+
				"client at the bare host, oapi-cli and the Terraform provider 1.1.x append "+
				strings.TrimSuffix(pathPrefix, "/")+"/<Call> themselves. `feint env outscale "+
				"--client oapi-cli` prints the endpoint to use — the flagless default carries "+
				"the path, which is what octl and the Terraform provider >= 1.7 want")
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

// The client families this pack can point at the emulator, and the one fact
// that forces the split: they read the same variable, OSC_ENDPOINT_API, and
// disagree about its shape. Measured on 2026-08-19, each cell by running the
// real client against this emulator (#286):
//
//   - Terraform provider 1.8.0 (the current line, >= 1.7) wants the /api/v1
//     path IN the value; given the bare host it posts /<Action> at the root
//     and dies on a 404 that used to be a six-minute retry backoff before
//     the mispointed hint (#185) made it fast.
//   - Terraform provider 1.1.3 and oapi-cli append /api/v1 themselves; given
//     the path, 1.1.3 URL-escapes it and dies client-side on
//     `invalid port ":4599%2Fapi%2Fv1"`.
//
// And one more, measured on 2026-08-25 (#460):
//
//   - octl wants the path IN the value, like the modern provider. That is not
//     a coincidence to be folded away: both read it from osc-sdk-go, whose
//     default endpoint template is "%s://api.%s.outscale.com/api/v1", so the
//     path is part of the value by construction. It gets its own case because
//     what a reader needs from `feint env outscale --client <x>` is the name of
//     the client they are holding, not a mapping they have to work out.
//
// One variable, two shapes: no single printed value serves both families, so
// the choice is a client parameter rather than a Note a reader has to reverse-
// engineer. The default is the family a stranger meets first — the current
// Terraform provider line, which is what docs/adoption.md invites them to run.
const (
	clientTerraform   = "terraform"     // provider >= 1.7: the path belongs in the value
	clientOCTL        = "octl"          // the current CLI: the path belongs in the value too
	clientOAPICLI     = "oapi-cli"      // bare host: the archived CLI appends /api/v1 itself
	clientTerraform11 = "terraform-1.1" // bare host: the 1.1.x provider appends it too
)

// apiPath is what the modern Terraform provider expects to find inside
// OSC_ENDPOINT_API, and what the other two clients insist on appending
// themselves. It is pathPrefix without the trailing slash, kept as one
// declaration so the URL space and the environment cannot drift apart.
var apiPath = strings.TrimSuffix(pathPrefix, "/")

// Env implements emulator.Pack.
//
// The values come from tools/conformance/outscale/fake-credentials.env, so the
// CLI and the suite cannot drift apart. They are deliberately public and open
// nothing: the emulator accepts any signature without checking it, but the
// client still refuses to sign a request whose credentials are not well-formed.
//
// The default serves the current Terraform provider line (>= 1.7), because the
// documented first contact is `eval "$(feint env outscale)"` then `terraform
// plan` — and until #286 that exact recipe printed the bare host, which the
// modern provider cannot use. The other families are one --client away, and
// the Note names them; a client that gets the wrong shape fails fast and named
// on both sides (the pack's doubled-prefix 404 for oapi-cli, the provider's
// own parse error for 1.1.x), never with a silent stall.
func (p *Pack) Env(endpoint string) emulator.Environment {
	env, _ := p.EnvFor(endpoint, clientTerraform)
	return env
}

// EnvClients names the client families EnvFor accepts, sorted, for the CLI to
// print when a caller asks for one it does not know.
func (p *Pack) EnvClients() []string {
	return []string{clientOAPICLI, clientOCTL, clientTerraform, clientTerraform11}
}

// EnvFor answers the environment one client family needs, or ok=false for a
// family this pack has never measured — refusing beats guessing, because a
// guessed shape is exactly the wall #286 is about.
func (p *Pack) EnvFor(endpoint, client string) (emulator.Environment, bool) {
	bare := strings.TrimRight(endpoint, "/")
	vars := func(endpointAPI string) map[string]string {
		return map[string]string{
			"OSC_ACCESS_KEY":   "AAAAAAAAAAAAAAAAAAAA",
			"OSC_SECRET_KEY":   "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
			"OSC_REGION":       p.region,
			"OSC_PROTOCOL":     "http",
			"OSC_ENDPOINT_API": endpointAPI,
		}
	}
	switch client {
	case clientTerraform:
		return emulator.Environment{
			Vars: vars(bare + apiPath),
			Note: "this endpoint carries " + apiPath + ": the shape the Terraform provider >= 1.7 reads, " +
				"and the one octl reads. oapi-cli and the Terraform provider 1.1.x append " + apiPath +
				" themselves and want the bare host instead: eval \"$(feint env outscale --client oapi-cli)\". " +
				"The 0.x providers (outscale-dev/*) read no endpoint variable at all " +
				"(examples/stacks/surveyed.md).",
		}, true
	case clientOCTL:
		return emulator.Environment{
			Vars: vars(bare + apiPath),
			Note: "this endpoint carries " + apiPath + ", which is what octl reads: its SDK's default is " +
				"https://api.<region>.outscale.com" + apiPath + ", so the path is part of the value. " +
				"With --config or --profile, octl loads the FILE first and merges the environment into " +
				"what the file left empty — the opposite of oapi-cli — so endpoints.api in that file wins " +
				"over these variables and must be set to " + bare + apiPath + ". " +
				"octl also reads `region` and never `region_name`.",
		}, true
	case clientOAPICLI, clientTerraform11:
		return emulator.Environment{
			Vars: vars(bare),
			Note: "the bare host: oapi-cli and the Terraform provider 1.1.x append " + apiPath + " themselves. " +
				"octl and the Terraform provider >= 1.7 want the path in the value: --client octl, or drop " +
				"--client, for those. With --config, oapi-cli wants endpoints.api set to " + bare + ". " +
				"Note that outscale/oapi-cli is archived upstream and describes itself as deprecated; " +
				"octl is the client this project's conformance suite drives (#460).",
		}, true
	}
	return emulator.Environment{}, false
}

// EnvHazards names what, in the caller's own shell, would send this provider's
// clients to the real cloud no matter which values feint prints. The lookup is
// injected so the check is testable; the CLI passes os.LookupEnv.
//
// Both hazards were measured rather than assumed, on 2026-08-19 (#286):
//
//   - OSC_PROFILE set: provider 1.1.3 reads ~/.osc/config.json and skips the
//     environment entirely — its providerConfigureClient calls
//     setProviderDefaultEnv, the only reader of OSC_ENDPOINT_API, only when
//     IsOldProfileSet says no profile is in force. Reproduced: with
//     OSC_PROFILE=default and OSC_ENDPOINT_API pointing at this emulator, the
//     plan left for https://api.<region>.outscale.com and the emulator
//     received nothing. Provider 1.8.0 honours the endpoint despite the
//     profile, so the escape selects exactly the users on the legacy line.
//   - The legacy credential names alone do NOT redirect — four combinations
//     of OUTSCALE_ACCESSKEYID/OUTSCALE_SECRETKEYID against 1.1.3 all reached
//     the emulator while OSC_ENDPOINT_API was set, refuting the survey
//     register's earlier reading — but they are real-cloud credentials
//     sitting in the shell, and every Outscale client falls back to them the
//     moment the endpoint export is lost (a new terminal, an eval that
//     printed nothing). The warning says which of the two situations this is.
//
// TestEnvHazardsNameTheProfileEscape fails without the first check, and
// TestEnvHazardsNameLegacyCredentials without the second.
func (p *Pack) EnvHazards(lookup func(string) (string, bool)) []string {
	var warnings []string
	if _, ok := lookup("OSC_PROFILE"); ok {
		warnings = append(warnings,
			"OSC_PROFILE is set: the Outscale Terraform provider 1.1.x then reads ~/.osc/config.json and "+
				"ignores OSC_ENDPOINT_API entirely, so a run that looks local reaches "+
				"https://api.<region>.outscale.com with that profile's credentials (measured on 1.1.3). "+
				"unset OSC_PROFILE before running terraform against this emulator")
	}
	_, hasLegacyAccess := lookup("OUTSCALE_ACCESSKEYID")
	_, hasLegacySecret := lookup("OUTSCALE_SECRETKEYID")
	if hasLegacyAccess || hasLegacySecret {
		warnings = append(warnings,
			"OUTSCALE_ACCESSKEYID/OUTSCALE_SECRETKEYID are set: real-cloud credentials every Outscale "+
				"client falls back to when the endpoint export is missing. They lose to the values printed "+
				"here as long as this shell keeps them (measured on providers 1.1.3 and 1.8.0); a new "+
				"terminal or a failed eval does not. unset them for emulator work")
	}
	return warnings
}
