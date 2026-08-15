package scaleway

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// kindServer is the store kind for Instance servers.
const kindServer = "instance/server"

// defaultProject is used when the caller does not pass one. Scaleway's own
// tooling always sends a project ID, but the CLI can be configured without one
// and the API then falls back to the token's default project.
const defaultProject = "11111111-1111-1111-1111-111111111111"

// defaultOrganization is the account the emulated projects belong to.
//
// It is deliberately a different UUID from defaultProject. Scaleway nests
// projects inside an organization: infrastructure belongs to a project, while
// IAM, billing and quotas belong to the organization, and the two identifiers
// never coincide on a real account. Reusing one value for both is the shortcut
// that lets a client read an organization ID where it expected a project one and
// notice nothing until it talks to the real API.
const defaultOrganization = "99999999-9999-4999-8999-999999999999"

// knownZones is the closed list the emulator accepts. Rejecting an unknown zone
// early matches the API and, more importantly, catches the common local mistake
// of pointing a client at the wrong zone and getting a confusing empty list.
var knownZones = map[string]bool{
	"fr-par-1": true, "fr-par-2": true, "fr-par-3": true,
	"nl-ams-1": true, "nl-ams-2": true, "nl-ams-3": true,
	"pl-waw-1": true, "pl-waw-2": true, "pl-waw-3": true,
	"it-mil-1": true,
}

type createServerRequest struct {
	Name              string   `json:"name"`
	CommercialType    string   `json:"commercial_type"`
	Image             string   `json:"image"`
	Project           string   `json:"project"`
	Organization      string   `json:"organization"`
	Tags              []string `json:"tags"`
	DynamicIPRequired *bool    `json:"dynamic_ip_required"`
	RoutedIPEnabled   *bool    `json:"routed_ip_enabled"`
	Protected         *bool    `json:"protected"`
	BootType          string   `json:"boot_type"`
	// SecurityGroup is a bare ID on the way in and a {id, name} object on the
	// way out. The asymmetry is the SDK's (CreateServerRequest.SecurityGroup is
	// *string, Server.SecurityGroup is *SecurityGroupSummary), and mirroring the
	// request shape into the response is what breaks Terraform's round-trip.
	SecurityGroup string `json:"security_group"`
	// PublicIP and PublicIPs both name flexible IPs already allocated, which a
	// client attaches at creation: it posts /ips first, then names the result
	// here. The SDK carries both, singular deprecated and plural current, and
	// the CLI still sends the singular one — so honouring only the plural fixed
	// nothing, which the unread-field report said immediately.
	//
	// Ignoring them meant the address the client had just reserved for this
	// server was never attached to it, and nothing said so. That report is the
	// only thing that can see an argument we drop.
	PublicIP  string   `json:"public_ip"`
	PublicIPs []string `json:"public_ips"`
	// EnableIPv6 is honoured by storing it, not by serving IPv6: the emulated
	// network is v4 only, and docs/limits.md says so. Read here so the request
	// round-trips rather than the field vanishing.
	EnableIPv6 *bool `json:"enable_ipv6"`
	// Volumes is how a client sizes the disks it wants, keyed the way the server
	// carries them: "0" is the root volume. The Terraform provider sends it for
	// every root_volume block.
	//
	// It was not read at all, so a client asking for a 50 GB root disk got the
	// catalogue's 20 and nothing said so — found by the report of fields a
	// client sends and no handler declares.
	Volumes map[string]volumeTemplate `json:"volumes"`
}

// volumeTemplate is VolumeServerTemplate, cut to what the emulator honours.
type volumeTemplate struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	VolumeType string `json:"volume_type"`
	Boot       *bool  `json:"boot"`
	ID         string `json:"id"`
}

type updateServerRequest struct {
	Name      *string   `json:"name"`
	Tags      *[]string `json:"tags"`
	Protected *bool     `json:"protected"`
	// CommercialType and Volumes were declared by the SDK and read by nobody
	// here, which Terraform found before any test did: changing a server's type
	// answered 200, changed nothing, and the next plan asked for the same change
	// again. A permanent diff is the worst answer an emulator can give, because
	// it is indistinguishable from a provider bug.
	//
	// The emulator had even recorded it. /_feint/conformance listed both fields
	// under unread_request_fields for instance/v1/API.UpdateServer, run after
	// run, and nothing turned that into a failure.
	CommercialType *string                    `json:"commercial_type"`
	Volumes        *map[string]volumeTemplate `json:"volumes"`
}

type serverActionRequest struct {
	Action string `json:"action"`
	Name   string `json:"name"`
}

func (p *Pack) tenant(zone string) resource.Tenant {
	return resource.Tenant{Provider: Name, Zone: zone}
}

// scopeOf is the tenant a list must be restricted to.
//
// A project is an isolation boundary, not a label: two projects of the same
// organization do not see each other, and a client that names one must never be
// shown another one's resources. The rules follow the API. A `project` filter
// scopes to it. An `organization` filter alone scopes to the whole account,
// which here means every project, since the emulator hosts a single
// organization. Neither means the token's default project.
func (p *Pack) scopeOf(r *http.Request, zone string) resource.Tenant {
	q := r.URL.Query()
	if project := q.Get("project"); project != "" {
		return resource.Tenant{Provider: Name, Project: project, Zone: zone}
	}
	if q.Get("organization") != "" {
		return p.tenant(zone)
	}
	return resource.Tenant{Provider: Name, Project: defaultProject, Zone: zone}
}

// projectOf resolves the project a create request belongs to, and the
// organization above it. The organization is not taken from the request: the
// emulator hosts one, and honouring an arbitrary value would let a client
// believe in an account boundary that does not exist here.
func projectOf(requested string) (project, organization string) {
	return orDefault(requested, defaultProject), defaultOrganization
}

// rootVolumeSize is what the emulated image carries, matching the root_volume
// the image endpoint publishes. The two must agree: a client that sizes a disk
// from the image and reads it back from the server would otherwise see a diff.
const rootVolumeSize = 20_000_000_000

// rootVolume builds the volume a server always owns. The caller stores it and
// puts it in the server's map under the key "0".
//
// The key is not decoration. The Terraform provider reads the root volume as
// Volumes["0"] and sizes the additional ones with
// make([]string, 0, len(server.Volumes)-1): an empty map makes that capacity -1
// and panics the plugin outright, which surfaces as "Plugin did not respond"
// with nothing in the emulator's log. It then reads the volume by ID through the
// volumes endpoint, so the volume has to be a stored resource, not an inline
// object.
//
// The type stays b_ssd whatever is asked, and there are two reasons, not one.
// The single reason this comment used to give covered only half the values it
// refuses, which is how a reader came to lift the restriction for the other
// half — reported by @vde-dis on #8, with the measurement below.
//
// Against a *local* type (l_ssd, scratch): the catalogue declares
// volumes_constraint.min_size at 0 and the CLI sums local volumes against it,
// so attaching one here would make the CLI refuse the very creation it just
// asked for.
//
// Against sbs_volume, which is block and sums to nothing there: honouring it
// sends the Terraform provider to GET /block/v1/zones/{zone}/volumes/{id} to
// read the volume back, no pack serves block/v1, and the apply dies on
// "waiting for Volume failed: http error 404 Not Found". A permanent diff is
// bad; an apply that cannot finish is worse. It becomes honourable with #8
// (SW-3) and not before — the two belong in one batch.
//
// So today there is no writable value: the provider refuses b_ssd outright from
// 2.79 on ("b_ssd volumes are not supported anymore"), and sbs_volume plans for
// ever. Omitting the root_volume block is the way through, which is what the
// conformance fixture happens to do — which is also why nothing here shows it.
// docs/limits.md carries that as a stated limit rather than a surprise.
func (p *Pack) rootVolume(server *resource.Resource, name, project, organization string, wanted volumeTemplate) *resource.Resource {
	// The size the client asked for, when it asked. Ignoring it gave every
	// server the catalogue's disk whatever the request said.
	size := uint64(rootVolumeSize)
	if wanted.Size > 0 {
		size = uint64(wanted.Size)
	}
	volumeName := name + "-root"
	if wanted.Name != "" {
		volumeName = wanted.Name
	}
	// sbs_volume is honoured since SW-3, and it is the only asked-for type that
	// is: the local ones stay overridden for the reason above, which is unchanged.
	// The disk lands in block/v1 rather than instance/v1, which is where the
	// Terraform provider goes to read it back.
	if wanted.VolumeType == "sbs_volume" {
		vol := p.newBlockRootVolume(server.Tenant.Zone, project, volumeName, size)
		// A volume this call just built: it can belong to nobody else, so the
		// only error attachVolume returns cannot happen here.
		_ = p.attachVolume(vol, server, name)
		return vol
	}
	// Overridden rather than honoured, for the two reasons above. Not repeated
	// here: this comment used to state one of them a second time, and the two
	// copies had already drifted apart — one carried a clause the other did
	// not. A fact written twice is a fact that will one day be written
	// differently, and it was already halfway there.
	vol := p.newVolume(server.Tenant.Zone, project, organization, volumeName, "b_ssd", size)
	// A volume this call just built: it can belong to nobody else, so the only
	// error attachVolume returns cannot happen here.
	_ = p.attachVolume(vol, server, name)
	// Built, not stored. Storing here put the volume in the store before the
	// flexible IPs were validated, so a create refused with a 404 left an
	// orphan disk attached to a server that never existed — the very defect the
	// comment beside that validation says it fixed, half-fixed. The caller
	// stores it with the server, once nothing can refuse the request.
	return vol
}

// zoneOf validates the {zone} path segment and writes the error response itself
// when it is unknown, so handlers stay a single early return.
func zoneOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	zone := r.PathValue("zone")
	if !knownZones[zone] {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "zone",
			Reason:       "constraint",
			HelpMessage:  "unknown zone " + zone,
		})
		return "", false
	}
	return zone, true
}

func (p *Pack) listServers(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}

	all := filterServers(p.env.Store.List(kindServer, p.scopeOf(r, zone)), r.URL.Query())

	page := parsePage(r)
	start, end := page.slice(len(all))
	servers := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		servers = append(servers, p.view(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"servers":     servers,
		"total_count": len(all),
	})
}

func (p *Pack) createServer(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}

	var req createServerRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "name", Reason: "required"})
		return
	}
	if req.CommercialType == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "commercial_type", Reason: "required"})
		return
	}

	now := p.env.Now()
	project, organization := projectOf(req.Project)

	// Every server belongs to a security group, and the project default is what
	// it gets when the caller names none. Resolving it here also provisions that
	// default, so a client that creates before it lists still finds one.
	securityGroup, found := p.securityGroupSummary(zone, project, req.SecurityGroup)
	if !found {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "security_group",
			Reason:       "constraint",
			HelpMessage:  "unknown security group " + req.SecurityGroup,
		})
		return
	}

	resolvedImageID, imageDisplay, imageLabel := resolveImage(req.Image)

	res := &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindServer,
		Tenant:  resource.Tenant{Provider: Name, Project: project, Zone: zone},
		State:   "stopped",
		Created: now,
		Updated: now,
	}
	rootVol := p.rootVolume(res, req.Name, project, organization, req.Volumes["0"])

	res.Attrs = map[string]any{
		"name":            req.Name,
		"commercial_type": req.CommercialType,
		"project":         project,
		"organization":    organization,
		"hostname":        req.Name,
		"tags":            orEmpty(req.Tags),
		"image":           p.imageView(zone, resolvedImageID, imageDisplay),
		"volumes":         p.attachTemplateVolumes(req.Volumes, rootVol, res, zone, req.Name),
		// Deprecated upstream, and "always null when routed_ip_enabled is True",
		// which is this emulator's default. The address of a server on a private
		// network is read through its NIC and ipam/v1, not here.
		"private_ip":          nil,
		"arch":                "x86_64",
		"boot_type":           orDefault(req.BootType, "local"),
		"dynamic_ip_required": deref(req.DynamicIPRequired, false),
		"routed_ip_enabled":   deref(req.RoutedIPEnabled, true),
		"protected":           deref(req.Protected, false),
		"enable_ipv6":         deref(req.EnableIPv6, false),
		"state_detail":        "",
		"security_group":      securityGroup,
		"allowed_actions":     []any{"poweron", "backup"},
		"maintenances":        []any{},
		// Kept so the machine driver knows which catalogue image stands in for
		// the requested cloud image. Empty when the identifier resolved to
		// nothing, which a boot refuses rather than substituting (#83).
		// Harmless in the response: the real API exposes the image too, just
		// as an object.
		"image_label": imageLabel,
	}
	// Every flexible IP the caller named is resolved before anything is stored.
	// Validating after the Put left a server and its root volume behind on a
	// 404, so a client that retried accumulated phantom servers no destroy knew
	// about.
	requested := req.PublicIPs
	if req.PublicIP != "" {
		requested = append(requested, req.PublicIP)
	}
	attach := make([]*resource.Resource, 0, len(requested))
	for _, id := range requested {
		ip, found := p.env.Store.Get(Name, kindIP, id)
		if !found || ip.Tenant.Zone != zone {
			writeNotFound(w, "ip", id)
			return
		}
		attach = append(attach, ip)
	}

	p.env.Store.Put(rootVol)
	p.env.Store.Put(res)
	for _, ip := range attach {
		// Taken from whoever held it, the way updateIP does: it unroutes the
		// previous machine before it routes the new one. This loop set the new
		// owner and left the old machine carrying the address, so under a
		// runtime two machines claimed the same /32 — and the first server lost
		// its address with no error anywhere.
		//
		// TestCreatingAServerDoesNotStealALiveAddress fails without this.
		// detachAddress reads the previous holder out of the record itself, so
		// it has to run before the record is rewritten — the first version
		// passed it the new server, which type-checks and reads no address at
		// all, and the falsification caught it because the test kept passing
		// with the call removed.
		if previous, _ := ip.Attrs["server"].(map[string]any); previous != nil {
			if id, _ := previous["id"].(string); id != "" && id != res.ID {
				p.detachAddress(r.Context(), ip)
			}
		}
		ip.Attrs["server"] = map[string]any{"id": res.ID, "name": req.Name}
		// The state moves with the attachment. This path used to set the server
		// and leave the state at "detached", so every address attached at create
		// described itself as free while carrying a machine — updateIP has always
		// set both, and an audit found the two paths disagreeing.
		ip.State = "attached"
		p.env.Store.Put(ip)
		p.attachAddress(r.Context(), ip, res)
	}

	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"server": p.view(res)})
}

func (p *Pack) getServer(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindServer, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "server", id)
		return
	}
	// A virtual machine gets its address after it boots, so a read is where it
	// becomes visible.
	if p.refreshMachine(r.Context(), res) {
		if !p.env.Store.Commit(res, p.env.Now()) {
			// Deleted while its address was being discovered. Answering the copy
			// we hold would hand the client a server the next read denies.
			writeNotFound(w, "server", id)
			return
		}
		// The machine just proved reachable, so the guest half of its public
		// addresses can finally land — a virtual machine's agent answers long
		// after poweron returned. Idempotent for a machine already served.
		p.routeServerAddresses(r.Context(), res)
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"server": p.view(res)})
}

func (p *Pack) updateServer(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindServer, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "server", id)
		return
	}

	var req updateServerRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name != nil {
		res.Attrs["name"] = *req.Name
	}
	if req.Tags != nil {
		res.Attrs["tags"] = orEmpty(*req.Tags)
	}
	if req.Protected != nil {
		res.Attrs["protected"] = *req.Protected
	}
	// The restriction is the SDK's own, quoted in UpdateServerRequest:
	// "Cannot be changed if the Instance is not in `stopped` state." Refusing it
	// matters more than accepting it — a test that resizes a running server and
	// sees it work would ship code the real API rejects.
	if req.CommercialType != nil {
		if res.State != "stopped" {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "commercial_type",
				Reason:       "constraint",
				HelpMessage:  "cannot be changed while the server is not stopped",
			})
			return
		}
		res.Attrs["commercial_type"] = *req.CommercialType
	}
	if req.Volumes != nil {
		if err := p.setServerVolumes(res, *req.Volumes); err != nil {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "volumes",
				Reason:       "constraint",
				HelpMessage:  err.Error(),
			})
			return
		}
	}
	if !p.env.Store.Commit(res, p.env.Now()) {
		writeNotFound(w, "server", id)
		return
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{"server": p.view(res)})
}

func (p *Pack) deleteServer(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	// Same exclusion as serverAction, for the same reason: destroying the
	// machine runs outside the store lock, and a poweron landing in that window
	// would leave a container running with nothing left to describe it.
	unlock := p.binding().Serialise(id)
	defer unlock()

	res, found := p.env.Store.Get(Name, kindServer, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "server", id)
		return
	}
	// The API refuses to delete a running server; Terraform relies on that error
	// to know it must power off first.
	if res.State == "running" || res.State == "starting" {
		writeTransientState(w, "server", id, res.State)
		return
	}
	// A stopped server should hold no dynamic address; a restored snapshot may
	// claim otherwise, and the OVN uplink route would outlive the machine.
	p.releaseDynamicAddress(r.Context(), res)
	p.removeMachine(r.Context(), res)
	p.env.Store.Delete(Name, kindServer, id)
	p.releaseServerResources(r.Context(), id, zone)
	w.WriteHeader(http.StatusNoContent)
}

// releaseServerResources gives back what a gone server held. Both doors to that
// state call it — DELETE /servers/{id} and the terminate action — because an
// audit found them disagreeing twice: first about addresses, then about
// volumes, each time on the door the previous fix had not touched.
//
// Volumes are detached, not deleted: deleting a server does not delete its
// disks on Scaleway, which is why `scw instance server delete` takes a
// with-volumes flag and then removes them itself. The CLI polls each volume
// after the server is gone, so one that vanished with it fails the delete with
// "waiting for volume failed: resource volume is not found".
//
// Addresses go back to the pool. They used to keep naming the deleted server,
// so `scw instance ip list` showed an address attached to something that no
// longer existed — and with a runtime on, the route stayed on a machine that
// had just been destroyed.
//
// TestTerminateReleasesWhatDeleteReleases fails without this.
func (p *Pack) releaseServerResources(ctx context.Context, id, zone string) {
	for _, vol := range p.volumesOf(id) {
		p.detachVolume(vol)
		p.env.Store.Put(vol)
	}
	p.releaseAddressesOf(ctx, id, zone)
}

// serverAction drives the lifecycle. Transitions are immediate: a local emulator
// that lingers in "starting" would only make every client wait for a state
// change that carries no information here.
func (p *Pack) serverAction(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	// One action at a time on one server, held past the write-back below.
	//
	// The runtime call runs outside the store lock on purpose, and Commit only
	// refuses to resurrect a deleted resource: it does not order two writers.
	// Two concurrent poweron each launched a container, the second failed on the
	// name the first had taken, and the API then described as "stopped" a
	// machine that was running. TestConcurrentPowerOnStartsTheMachineOnce fails
	// without this line.
	unlock := p.binding().Serialise(id)
	defer unlock()

	res, found := p.env.Store.Get(Name, kindServer, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "server", id)
		return
	}

	var req serverActionRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}

	now := p.env.Now()
	switch req.Action {
	case "poweron", "reboot":
		// A poweron on a server that is already running is not a second launch.
		// The runtime refuses a name it has already handed out, so relaunching
		// reported a failed start on a machine that was running perfectly well:
		// the same lie as the race above, arriving through the other door. It is
		// also what the real API does, which lists poweron only for a server
		// that is not up. reboot is deliberately not covered: acting on a
		// running machine is its whole purpose.
		if req.Action == "reboot" || res.State != "running" {
			// The ephemeral address dynamic_ip_required asks for, allocated
			// before the boot so it rides the launch like an attached flexible
			// IP does.
			p.ensureDynamicAddress(res)
			// The state is the binding's to set, not this switch's. With no
			// runtime the server reaches running, which is the documented
			// degraded mode; with a runtime that failed to start the machine it
			// stays stopped, because a server reported running while nothing
			// exists is the defect this project is built to avoid.
			p.startMachine(r.Context(), res)
			// The security group binds once the machine exists, since a rule set
			// attaches to interfaces. A server powered on before its group was
			// edited still gets the current rules, because every group mutation
			// replays onto the machines carrying it.
			p.applyServerFirewall(r.Context(), res)
			// The launch installed the host half of every public route; this
			// hands the guest its addresses, and repairs a machine that already
			// existed. Without it, an address attached at create was published
			// and never routed (#116).
			p.routeServerAddresses(r.Context(), res)
		}
		res.Attrs["allowed_actions"] = allowedActions(res.State)
	case "poweroff", "stop_in_place":
		// Two actions, two states, and the difference is not cosmetic: the SDK
		// declares `stopped in place` alongside `stopped`
		// (ServerStateStoppedInPlace), and the Terraform provider polls for the
		// exact one its `state = "standby"` asked for. Collapsing both into
		// "stopped" made a plan fail with "expected state stopped in place but
		// found stopped" — an emulator answering a state nobody can reach.
		//
		// Neither `scw` nor the conformance suite exercised standby, which is
		// why this survived until a real provider asked for it.
		res.State = "stopped"
		if req.Action == "stop_in_place" {
			res.State = "stopped in place"
		}
		res.Attrs["allowed_actions"] = allowedActions(res.State)
		// Stopping withdraws the address: one the API publishes and nothing
		// answers on is the defect this project exists to avoid. The dynamic
		// address goes entirely — upstream releases it on stop, and that is
		// the whole difference between dynamic and flexible.
		p.releaseDynamicAddress(r.Context(), res)
		p.stopMachine(r.Context(), res)
	case "terminate":
		p.releaseDynamicAddress(r.Context(), res)
		p.removeMachine(r.Context(), res)
		p.env.Store.Delete(Name, kindServer, id)
		// One function for both doors, because writing the release twice is how
		// they came to disagree: delete detached its volumes and terminate did
		// not, so `tofu destroy` on a server with additional_volume_ids failed
		// with "volume is still attached to a server" on every retry — the
		// volume named a server that answered 404.
		//
		// Detached, not deleted, and that is upstream's own rule rather than a
		// simplification: instance_sdk.go:4670 says terminate "will result in
		// the deletion of l_ssd and scratch volumes types, sbs_volume volumes
		// will only be detached". Every volume this pack attaches is b_ssd, so
		// every one of them survives its server. A manual run of the whole
		// lifecycle expected the root volume to vanish here and was wrong; the
		// SDK is what settled it.
		p.releaseServerResources(r.Context(), id, zone)
		emulator.WriteJSON(w, http.StatusAccepted, map[string]any{"task": task(p.env.NewID(), "server_terminate", now)})
		return
	case "backup":
		// Accepted and ignored: backups need an image catalogue the emulator
		// does not have. Declared here so the action does not 400 in a script.
	default:
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "action",
			Reason:       "constraint",
			HelpMessage:  "unknown action " + req.Action,
		})
		return
	}

	// Starting a machine takes tens of seconds and runs outside the lock. A
	// terminate that landed meanwhile must stay landed: Put would bring the
	// server back, address and all.
	if !p.env.Store.Commit(res, now) {
		writeNotFound(w, "server", id)
		return
	}
	emulator.WriteJSON(w, http.StatusAccepted, map[string]any{"task": task(p.env.NewID(), "server_"+req.Action, now)})
}

// allowedActions is what a client may do next, which follows from the state
// rather than from the action that was asked for. Deriving it is what keeps a
// failed start from advertising poweroff on a server that never came up.
func allowedActions(state string) []any {
	switch state {
	case "running":
		return []any{"poweroff", "reboot", "stop_in_place", "backup"}
	case "stopped in place":
		// Standby keeps the machine and its local storage, so powering it fully
		// off is the operation that follows from here as much as starting it
		// again. Listed rather than omitted: a client reading allowed_actions to
		// decide would otherwise have no way out of standby but a boot.
		return []any{"poweron", "poweroff", "backup"}
	default:
		return []any{"poweron", "backup"}
	}
}

// task mirrors the Task object the API returns for asynchronous actions. The
// emulator applies the change synchronously, so the task is always already done.
func task(id, description string, now time.Time) map[string]any {
	return map[string]any{
		"id":            id,
		"description":   description,
		"status":        "success",
		"progress":      100,
		"started_at":    now.Format(time.RFC3339),
		"terminated_at": now.Format(time.RFC3339),
		"href_from":     "",
	}
}

// view renders the stored resource as the API serves it: the pack owns Attrs, so
// only the fields the core tracks (id, state, timestamps) are injected here.
// view renders a server. public_ips is computed rather than stored, because it
// is a fact about the IP resources rather than about the server: attaching an
// address writes to the IP, and a copy on the server would go stale the moment
// somebody attached one through PATCH /ips.
//
// It was stored, as a literal empty list written at creation and never touched.
// A server therefore reported no public address whatever was attached to it,
// and nothing noticed: the network suite reads the address through /ips, which
// is the other side of the same link.
func (p *Pack) view(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+6)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["id"] = res.ID
	out["zone"] = res.Tenant.Zone
	out["state"] = res.State
	out["creation_date"] = res.Created.Format(time.RFC3339)
	out["modification_date"] = res.Updated.Format(time.RFC3339)
	publicIPs := p.publicIPsOf(res)
	out["public_ips"] = publicIPs
	// Server.public_ip is the SDK's own field for the first address, and the
	// one `scw instance server list` renders. Null when the server has none,
	// which is what the real API answers.
	out["public_ip"] = nil
	if len(publicIPs) > 0 {
		out["public_ip"] = publicIPs[0]
	}

	// What follows was found missing by the field gate (#88): every field here
	// is declared by Scaleway's own document and present in a real account's
	// recorded answer (shapes/scaleway.json), and this view served none of
	// them. The Server struct in the SDK marks none of these omitempty, which
	// is why the real API serves the key on every server, null included.
	//
	// The values follow the vms.go doctrine on the Outscale side: fixed,
	// stable between two reads, describing the platform being emulated rather
	// than the local runtime. What matters to a client is that the field is
	// there, well-formed, and does not move.
	out["mac_address"] = serverMAC(res.ID)
	out["end_of_service"] = false
	out["filesystems"] = []any{}
	// Deprecated upstream and always null in routed IP mode, which is this
	// emulator's default; the real API still serves the key.
	out["ipv6"] = nil
	out["placement_group"] = nil
	nics := make([]any, 0)
	for _, nic := range p.privateNICsOf(res.ID) {
		nics = append(nics, p.privateNICView(nic))
	}
	out["private_nics"] = nics
	// A placed machine has a location, a stopped one has none — the null is
	// the real API's answer for a server that sits nowhere.
	out["location"] = nil
	out["dns"] = nil
	if res.State == "running" {
		out["location"] = map[string]any{
			"cluster_id":    "19",
			"hypervisor_id": "1201",
			"node_id":       "24",
			"platform_id":   "14",
			"zone_id":       res.Tenant.Zone,
		}
	}
	if len(publicIPs) > 0 {
		out["dns"] = res.ID + ".pub.instances.scw.cloud"
	}
	return out
}

// serverMAC derives a stable MAC from the server id. de:00:00 is the prefix
// Scaleway's own instances carry; the suffix reuses the id's leading hex so
// two reads — and two runs over a restored snapshot — agree.
func serverMAC(id string) string {
	hex := make([]byte, 0, 6)
	for i := 0; i < len(id) && len(hex) < 6; i++ {
		c := id[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			hex = append(hex, c)
		}
	}
	for len(hex) < 6 {
		hex = append(hex, '0')
	}
	return "de:00:00:" + string(hex[0:2]) + ":" + string(hex[2:4]) + ":" + string(hex[4:6])
}

// publicIPsOf lists a server's public addresses in the ServerIP shape the SDK
// declares on Server.PublicIPs — which is not the shape of an IP: it carries
// the address and its provisioning, not the project or the tags.
//
// Two kinds of entry: the attached flexible IPs, and the ephemeral address
// dynamic_ip_required allocated, marked dynamic the way upstream marks it. The
// dynamic one lives on the server itself because upstream never lists it in
// /ips, so a store record would leak it there.
func (p *Pack) publicIPsOf(server *resource.Resource) []any {
	out := make([]any, 0, 1)
	for _, ip := range p.attachedIPsOf(server.ID, server.Tenant.Zone) {
		address, _ := ip.Attrs["address"].(string)
		out = append(out, serverIPView(ip.ID, address, false))
	}
	if address := server.Runtime[runtimeDynamicIPKey]; address != "" {
		out = append(out, serverIPView(server.Runtime[runtimeDynamicIPIDKey], address, true))
	}
	return out
}

// serverIPView is one ServerIP entry. routed_ip_enabled is this emulator's
// default, so the machine carries the address itself rather than being NATed
// to it, flexible and dynamic alike.
func serverIPView(id, address string, dynamic bool) map[string]any {
	return map[string]any{
		"id":                id,
		"address":           address,
		"gateway":           nil,
		"netmask":           "32",
		"family":            "inet",
		"dynamic":           dynamic,
		"provisioning_mode": "dhcp",
		// The three fields below are declared on ServerIp and served by the
		// real API on every attached address (#88). An entry here is attached
		// by construction — that is what puts it on the server. ipam_id reuses
		// the address's own id: the emulated IPAM does not register flexible
		// addresses, and what a client needs is a stable, well-formed
		// identifier, not a second inventory.
		"ipam_id": id,
		"state":   "attached",
		"tags":    []any{},
	}
}

// setServerVolumes makes the server's attached volumes be exactly what the
// request lists, keyed the way the API keys them ("0" is the root disk).
//
// The map replaces rather than merges, which is what the field means upstream
// and what a Terraform plan expects: a volume dropped from the configuration
// must come off the server. Detached, never deleted — that is what deleting a
// server does here too, because on Scaleway the disk outlives the machine.
//
// A volume named by an id that does not exist is refused. Accepting it would
// attach nothing and answer 200, which is the shape of the defect this function
// was written to remove.
func (p *Pack) setServerVolumes(server *resource.Resource, wanted map[string]volumeTemplate) error {
	keep := make(map[string]bool, len(wanted))
	view := make(map[string]any, len(wanted))
	name, _ := server.Attrs["name"].(string)

	for key, tmpl := range wanted {
		switch {
		case tmpl.ID != "":
			vol, found := p.env.Store.Get(Name, kindVolume, tmpl.ID)
			if !found || vol.Tenant.Zone != server.Tenant.Zone {
				return fmt.Errorf("volume %s does not exist in %s", tmpl.ID, server.Tenant.Zone)
			}
			// An existing volume, which another live server may hold. The
			// verdict was thrown away here — under a comment copied from the
			// case below, where the volume really is new — so `scw instance
			// server update <thief> volumes.0.id=<their-root>` took it, both
			// servers listed it, and the patched server's own root was
			// detached with no error. A fourth audit found it: the shared
			// question was asked and its answer dropped, which is worse than
			// not asking, because the guard reads as present.
			//
			// TestAttachingDoesNotStealAnotherServersVolume fails without this.
			if err := p.attachVolume(vol, server, name); err != nil {
				return err
			}
			p.env.Store.Put(vol)
			keep[vol.ID] = true
			view[key] = volumeView(vol)
		case tmpl.Size > 0:
			// A template with a size and no id asks for a new disk, the way
			// creation does. Same helper, so the two paths cannot diverge.
			project, _ := server.Attrs["project"].(string)
			organization, _ := server.Attrs["organization"].(string)
			volumeName := tmpl.Name
			if volumeName == "" {
				volumeName = name + "-" + key
			}
			vol := p.newVolume(server.Tenant.Zone, project, organization, volumeName, orDefault(tmpl.VolumeType, "b_ssd"), uint64(tmpl.Size))
			// A volume this call just created: it can belong to nobody else.
			_ = p.attachVolume(vol, server, name)
			p.env.Store.Put(vol)
			keep[vol.ID] = true
			view[key] = volumeView(vol)
		default:
			return fmt.Errorf("volume %q needs an id or a size", key)
		}
	}

	for _, vol := range p.volumesOf(server.ID) {
		if !keep[vol.ID] {
			p.detachVolume(vol)
			p.env.Store.Put(vol)
		}
	}
	server.Attrs["volumes"] = view
	return nil
}

// filterServers applies the query filters ListServers accepts.
//
// It read `name` and nothing else, which is how `scw instance server list
// state=running` came back with five stopped servers. A list that silently
// ignores a filter is worse than one that refuses it: the caller gets an answer
// shaped exactly like the right one.
//
// The filters implemented are the ones the SDK sends and a caller actually uses:
// name, state, commercial_type and tags. The rest of what instance/v1 declares
// (private_ip, with_ip, without_ip, private_network, order, servers) is left
// alone rather than guessed at — an ignored filter and a wrongly implemented one
// fail the same way, so the ones here are the ones whose semantics the SDK makes
// unambiguous.
//
// tags is joined with commas by the SDK and treated as a conjunction: a server
// matches when it carries every tag asked for. That is the reading the CLI's own
// help implies; if the real API turns out to disjoin, this is the line to change
// and the conformance suite is where it should be caught.
func filterServers(all []*resource.Resource, q url.Values) []*resource.Resource {
	name := q.Get("name")
	state := q.Get("state")
	commercialType := q.Get("commercial_type")
	var tags []string
	if raw := q.Get("tags"); raw != "" {
		tags = strings.Split(raw, ",")
	}
	if name == "" && state == "" && commercialType == "" && len(tags) == 0 {
		return all
	}

	kept := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		// A substring, not an equality, and that is the SDK's own wording:
		// "filter Instances by name (eg. \"server1\" will return \"server100\"
		// and \"server1\" but not \"foo\")". Comparing for equality answered one
		// server where the API answers two, and the test that covered it used
		// single-letter names, under which the two readings are
		// indistinguishable.
		if name != "" {
			stored, _ := res.Attrs["name"].(string)
			if !strings.Contains(stored, name) {
				continue
			}
		}
		if state != "" && res.State != state {
			continue
		}
		if commercialType != "" && res.Attrs["commercial_type"] != commercialType {
			continue
		}
		if len(tags) > 0 && !hasEveryTag(res, tags) {
			continue
		}
		kept = append(kept, res)
	}
	return kept
}

// hasEveryTag reports whether the resource carries all of the wanted tags. The
// stored value comes back from a JSON snapshot as []any and from a live create
// as []string, so both are read: a restored session must filter like the session
// that produced it.
func hasEveryTag(res *resource.Resource, wanted []string) bool {
	have := make(map[string]bool)
	switch stored := res.Attrs["tags"].(type) {
	case []string:
		for _, t := range stored {
			have[t] = true
		}
	case []any:
		for _, t := range stored {
			if s, ok := t.(string); ok {
				have[s] = true
			}
		}
	}
	for _, t := range wanted {
		if !have[t] {
			return false
		}
	}
	return true
}

func orEmpty(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func deref(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}

// attachTemplateVolumes builds the server's volume map from what the client
// asked for, not from the root volume alone.
//
// Only Volumes["0"] was ever read, so a create naming an existing volume under
// "1" — which is exactly what the Terraform provider sends for
// additional_volume_ids — answered 201 with the volume left detached and nothing
// saying so. The unread-field report could not see it either: the field was
// declared and read, just not to the end.
//
// A key naming a volume that does not exist is skipped rather than refused: the
// real API errors, but refusing here would break a create over a volume the
// emulator may simply not model yet, and the response says which volumes the
// server actually has.
//
// TestAdditionalVolumesAreAttachedAtCreate fails without this.
func (p *Pack) attachTemplateVolumes(templates map[string]volumeTemplate, root, server *resource.Resource, zone, serverName string) map[string]any {
	// A root volume that lives in block gets block's rendering inside the server,
	// which is an instance VolumeServer carrying volume_type "sbs_volume" — the
	// value that sends the Terraform provider to the block fallback (#8).
	// Copying the instance view here would publish a volume with no type at all.
	out := map[string]any{"0": volumeView(root)}
	if root.Kind == kindBlockVolume {
		out["0"] = blockRootVolumeServerView(root)
	}
	for key, tpl := range templates {
		if key == "0" || tpl.ID == "" {
			continue
		}
		vol, ok := p.env.Store.Get(Name, kindVolume, tpl.ID)
		if !ok || vol.Tenant.Zone != zone {
			continue
		}
		// Skipped rather than refused, like an unknown id above: the answer says
		// which volumes the server actually has, and a create must not take a
		// disk from a running machine.
		if err := p.attachVolume(vol, server, serverName); err != nil {
			continue
		}
		p.env.Store.Put(vol)
		out[key] = volumeView(vol)
	}
	return out
}

// attachServerVolume and detachServerVolume are what `scw instance server
// terminate` walks before it terminates anything.
//
// Every emulated server owns a b_ssd root volume, so the CLI's terminate always
// issues POST /servers/{id}/detach-volume first. That operation was untriaged,
// answered 501, and the command failed outright on every server — an audit
// reproduced it. The same pair also backs `scw instance server attach-volume`
// and the Terraform provider's volume moves.
//
// TestTerminateWalksDetachVolume fails without them.
func (p *Pack) attachServerVolume(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	unlock := p.binding().Serialise(id)
	defer unlock()

	res, found := p.env.Store.Get(Name, kindServer, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "server", id)
		return
	}
	var req struct {
		VolumeID string `json:"volume_id"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	vol, ok := p.env.Store.Get(Name, kindVolume, req.VolumeID)
	if !ok || vol.Tenant.Zone != zone {
		writeNotFound(w, "volume", req.VolumeID)
		return
	}
	// The API refuses to move a volume already in use, and Terraform reads that
	// error rather than guessing.
	key := p.nextVolumeKey(res)
	serverName, _ := res.Attrs["name"].(string)
	if err := p.attachVolume(vol, res, serverName); err != nil {
		writePrecondition(w, "volume", vol.ID, "the volume is attached to another server")
		return
	}
	p.env.Store.Put(vol)

	volumes := volumeMapOf(res)
	volumes[key] = volumeView(vol)
	res.Attrs["volumes"] = volumes
	if !p.env.Store.Commit(res, p.env.Now()) {
		writeNotFound(w, "server", id)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"server": p.view(res)})
}

func (p *Pack) detachServerVolume(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	unlock := p.binding().Serialise(id)
	defer unlock()

	res, found := p.env.Store.Get(Name, kindServer, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, "server", id)
		return
	}
	var req struct {
		VolumeID string `json:"volume_id"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}

	volumes := volumeMapOf(res)
	for key, entry := range volumes {
		view, _ := entry.(map[string]any)
		if view == nil || view["id"] != req.VolumeID {
			continue
		}
		delete(volumes, key)
		if vol, ok := p.env.Store.Get(Name, kindVolume, req.VolumeID); ok {
			p.detachVolume(vol)
			p.env.Store.Put(vol)
		}
		res.Attrs["volumes"] = volumes
		if !p.env.Store.Commit(res, p.env.Now()) {
			writeNotFound(w, "server", id)
			return
		}
		emulator.WriteJSON(w, http.StatusOK, map[string]any{"server": p.view(res)})
		return
	}
	writeNotFound(w, "volume", req.VolumeID)
}

// volumeMapOf is the server's volume map, always a map even when the attribute
// is missing: the Terraform provider sizes a slice from its length and panics
// the plugin on a nil one.
func volumeMapOf(res *resource.Resource) map[string]any {
	volumes, _ := res.Attrs["volumes"].(map[string]any)
	if volumes == nil {
		volumes = map[string]any{}
	}
	return volumes
}

// nextVolumeKey is the first free slot. The keys are the API's own ordering and
// "0" is always the root volume.
func (p *Pack) nextVolumeKey(res *resource.Resource) string {
	volumes := volumeMapOf(res)
	for i := 1; ; i++ {
		key := strconv.Itoa(i)
		if _, taken := volumes[key]; !taken {
			return key
		}
	}
}
