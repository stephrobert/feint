package scaleway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// errServerNotStopped is the retype refusal, answered from inside the store
// lock where the state it judges cannot move (#295).
var errServerNotStopped = errors.New("the server is not stopped")

// errServerNotStoppedForPlacement is the same refusal for joining a placement
// group: the provider's own update path guards it client-side ("instance must
// be stopped to change placement group"), so the emulator refusing it is the
// real API's behaviour, not an invention. Leaving a group has no such guard —
// the provider detaches on destroy "even if instance won't stop".
var errServerNotStoppedForPlacement = errors.New("the server is not stopped")

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
	// PlacementGroup is a bare ID on the way in and a full object on the way
	// out, the same asymmetry SecurityGroup carries (CreateServerRequest's
	// field is *string, Server.PlacementGroup is *PlacementGroup). The
	// Terraform provider sends it whenever the configuration names a
	// placement_group_id (#285).
	PlacementGroup string `json:"placement_group"`
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
	// PlacementGroup is a NullableStringValue upstream: `null` detaches, a
	// string attaches. RawMessage for the reason createIPRequest.Server is —
	// a *string cannot tell `"placement_group": null` from an absent field,
	// and the provider's own destroy path sends the null to free the group
	// before stopping the server.
	PlacementGroup json.RawMessage `json:"placement_group"`

	// PublicIPs is "a list of reserved IP IDs to attach to the Instance"
	// (UpdateServerRequest.PublicIPs, instance_sdk.go:3961) — the same field
	// the create reads, on the update door. It was declared upstream and read
	// by nobody here, the commercial_type defect one field over: a PATCH
	// naming it answered 200 and changed nothing (#320).
	PublicIPs *[]string `json:"public_ips"`
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
// The requested type is honoured for sbs_volume and refused for the LOCAL ones,
// and there are two reasons, not one. The single reason this comment used to
// give covered only half the values it refuses, which is how a reader came to
// lift the restriction for the other half — reported by @vde-dis on #8, with the
// measurement below. Both halves are stated here for that reason, and neither is
// repeated inside the function.
//
// Against a *local* type (l_ssd, scratch): the catalogue declares
// volumes_constraint.min_size at 0 and the CLI sums local volumes against it,
// so attaching one here would make the CLI refuse the very creation it just
// asked for. That is unchanged and it is why the local branch is still an
// override rather than an honouring.
//
// Against sbs_volume, which is block and sums to nothing there: honouring it
// sends the Terraform provider to GET /block/v1/zones/{zone}/volumes/{id} to
// read the volume back. No pack served block/v1 before SW-3, and the apply died
// on "waiting for Volume failed: http error 404 Not Found"; block/v1 is served
// now, so it is honoured — and since #365 it is also what a request naming no
// type gets, because it is what the cloud gives a DEV1-S.
//
// What that leaves a client: b_ssd is refused by the provider itself from 2.79
// on ("b_ssd volumes are not supported anymore"), sbs_volume works, and omitting
// the block gets sbs_volume too. The conformance fixture declares the block
// rather than omitting it, which is the whole point of #8 — a fixture that
// avoids the one input that breaks is a test that cannot fail. docs/limits.md
// carries what is still not emulated behind an SBS volume: the storage itself.
func (p *Pack) rootVolume(server *resource.Resource, name, project, organization, parentSnapshot string, wanted volumeTemplate) *resource.Resource {
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
	// The default is block, which is what the cloud does (#365).
	//
	// A DEV1-S created on a real fr-par-1 account was given an SBS root volume,
	// measured twice — 2026-08-21 and 2026-08-24, recorded in
	// corpus/scaleway/scw-instance.jsonl — and `scw` then read it back through
	// GET /block/v1alpha1/zones/fr-par-1/volumes/{id} and deleted it there. An
	// instance root made that read a 404 on the path the cloud answers 200 on,
	// on a command every client runs.
	//
	// This flip was tried on 2026-08-27 and reverted, because at the time it
	// moved every root disk into a product where nothing could reach it:
	// attach-volume, detach-volume, the update's volume map, a create naming a
	// volume and CreateSnapshot all resolved kindVolume alone. That is fixed
	// first and separately (#571, step 1): those five go through anyVolume, and
	// the ownership guard covers both products. The flip is this line only
	// because that work landed before it.
	//
	// The local types stay overridden for the reason above, which is unchanged:
	// the CLI sums LOCAL volumes against volumes_constraint.min_size and would
	// refuse the very creation it just asked for. A block root sums to nothing
	// local, which is why this default is reachable at all —
	// TestCatalogueKeepsTheLocalVolumeTrapDisarmed and the `scw instance server
	// create` of the conformance suite are the two halves of that check.
	//
	// TestADefaultRootVolumeLivesInBlockLikeTheCloud fails without this.
	if wanted.VolumeType == "" || wanted.VolumeType == "sbs_volume" {
		vol := p.newBlockRootVolume(server.Tenant.Zone, project, volumeName, size, parentSnapshot)
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

	q := r.URL.Query()
	all := filterServers(p.env.Store.List(kindServer, p.scopeOf(r, zone)), q)
	all = p.filterServersByLinks(all, q)
	// creation_date_desc when the client names no order — not this pack's
	// habit but the SDK's own default ("Default value: creation_date_desc"),
	// and the empty value every instance/v1 list sends: the request's Order is
	// a non-pointer enum, so its zero value marshals onto the wire as `order=`.
	if !orderResources(w, r, "order", "creation_date_desc", map[string]resourceCmp{
		"creation_date":     cmpCreated,
		"modification_date": cmpUpdated,
	}, all) {
		return
	}

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

	// The placement group is resolved before anything is stored, for the same
	// reason the flexible IPs below are: refusing after the Put would leave a
	// phantom server behind on the 400.
	var placementGroup any
	if req.PlacementGroup != "" {
		group, found := p.env.Store.Get(Name, kindPlacementGroup, req.PlacementGroup)
		if !found || group.Tenant.Zone != zone {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "placement_group",
				Reason:       "constraint",
				HelpMessage:  "unknown placement group " + req.PlacementGroup,
			})
			return
		}
		placementGroup = req.PlacementGroup
	}

	resolvedImageID, imageDisplay, imageLabel := resolveImage(req.Image)

	res := resource.New(p.env.NewID(), kindServer, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "stopped", now)
	rootVol := p.rootVolume(res, req.Name, project, organization, p.imageRootSnapshot(resolvedImageID), req.Volumes["0"])

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
		// The stored value is the bare ID (or nil); view() serves the object
		// shape the SDK declares. Stored under the response key on purpose,
		// so a snapshot round-trips the membership without a second name.
		attrServerPlacementGroup: placementGroup,
		"allowed_actions":        allowedActions(res.State, deref(req.Protected, false)),
		"maintenances":           []any{},
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
	for i, ip := range attach {
		p.attachIPToServer(r.Context(), ip, res, i)
	}

	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"server": p.view(res)})
}

func (p *Pack) getServer(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	// The zone is answered before anything else, and from a plain read: a server
	// of another zone does not exist for this caller, and asking a runtime about
	// it would be work done for a 404.
	if res, found := p.env.Store.Get(Name, kindServer, id); !found || res.Tenant.Zone != zone {
		writeNotFound(w, "server", id)
		return
	}

	// A virtual machine gets its address after it boots, so a read is where it
	// becomes visible — which makes this GET a writer, and it was the only one of
	// the three packs' reads that held nothing while it wrote (#211). Observe
	// holds the server across the runtime call and writes back conditionally, so
	// a poweroff landing in that window is not undone by the read that started
	// before it.
	refreshed := false
	res, err := p.binding().Observe(p.env.Store, p.env.Now, kindServer, id,
		func(res *resource.Resource) bool {
			refreshed = p.refreshMachine(r.Context(), res)
			return refreshed
		})
	if err != nil {
		writeNotFound(w, "server", id)
		return
	}
	if refreshed {
		// The machine just proved reachable, so the guest half of its public
		// addresses can finally land — a virtual machine's agent answers long
		// after poweron returned. Idempotent for a machine already served.
		p.reconciler().ReplayAddresses(r.Context(), res)
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"server": p.view(res)})
}

func (p *Pack) updateServer(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	// This hold was #211's answer to concurrent PATCHes losing each other's
	// fields, and it stopped being that when #295 moved the read-check-write
	// into Store.Update below — measured, not assumed: with this hold released
	// and only the store's critical section left, the
	// TestConcurrentUpdatesKeepEveryAcknowledgedField barrage stayed green
	// (falsification run of 2026-08-18), so citing that test here would be a
	// citation the test no longer backs.
	//
	// What the hold still buys is ordering against the lifecycle paths, whose
	// windows span runtime calls this handler never sees: serverAction takes
	// the same hold, boots for seconds, and commits — without this line a
	// commercial_type change could pass its stopped-state check against a
	// server whose boot is mid-flight and land on a running machine, which is
	// the retype the check exists to refuse.
	unlock := p.binding().Serialise(id)
	defer unlock()

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
	// An attach names a group that must exist in this zone, and the lookup
	// happens out here because the store's critical section is below.
	pgID, pgPresent, pgClears := serverField(req.PlacementGroup)
	if pgPresent && !pgClears {
		if group, found := p.env.Store.Get(Name, kindPlacementGroup, pgID); !found || group.Tenant.Zone != zone {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "placement_group",
				Reason:       "constraint",
				HelpMessage:  "unknown placement group " + pgID,
			})
			return
		}
	}
	// The volume moves touch other resources and cannot run under the store
	// lock, so they go first, on the clone; the map they computed is then
	// written with the scalar fields in one critical section below.
	var volumesView map[string]any
	if req.Volumes != nil {
		if err := p.setServerVolumes(res, *req.Volumes); err != nil {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "volumes",
				Reason:       "constraint",
				HelpMessage:  err.Error(),
			})
			return
		}
		volumesView, _ = res.Attrs["volumes"].(map[string]any)
	}

	// Like the volume moves above: the attachments live on the IP resources
	// and the reconciliation talks to the runtime, so it cannot run inside the
	// store lock. The order of the list is part of what is being written —
	// public_ips answers in this order from now on (#320).
	if req.PublicIPs != nil {
		if badID, ok := p.setServerIPs(r.Context(), res, *req.PublicIPs); !ok {
			writeNotFound(w, "ip", badID)
			return
		}
	}

	// Inside the store lock: the clone-mutate-Commit shape this replaces
	// erased a concurrent write to another field of the same server — the user
	// data another handler had just acknowledged, the state a poweron had just
	// reached — after its 200 (#295).
	// TestConcurrentServerUpdateAndActionKeepBothWrites fails without it.
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindServer, id, func(stored *resource.Resource) error {
		if req.Name != nil {
			stored.Attrs["name"] = *req.Name
		}
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		if req.Protected != nil {
			stored.Attrs["protected"] = *req.Protected
			// The list travels with the flag. On fr-par-1, a PATCH that protects a
			// running server makes the next GET answer ["backup"] alone, and the
			// client that reads it to decide what to offer must see that without
			// waiting for an action to be refused.
			stored.Attrs["allowed_actions"] = allowedActions(stored.State, *req.Protected)
		}
		// The restriction is the SDK's own, quoted in UpdateServerRequest:
		// "Cannot be changed if the Instance is not in `stopped` state." Refusing it
		// matters more than accepting it — a test that resizes a running server and
		// sees it work would ship code the real API rejects. Judged on the stored
		// state, inside the lock, where it cannot move underneath the check.
		if req.CommercialType != nil {
			if stored.State != "stopped" {
				return errServerNotStopped
			}
			stored.Attrs["commercial_type"] = *req.CommercialType
		}
		if pgPresent {
			if pgClears {
				// Leaving a group is allowed in any state: the provider's
				// destroy path frees the group before it stops the server.
				stored.Attrs[attrServerPlacementGroup] = nil
			} else {
				if stored.State != "stopped" {
					return errServerNotStoppedForPlacement
				}
				stored.Attrs[attrServerPlacementGroup] = pgID
			}
		}
		if volumesView != nil {
			stored.Attrs["volumes"] = volumesView
		}
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	switch {
	case errors.Is(err, errServerNotStopped):
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "commercial_type",
			Reason:       "constraint",
			HelpMessage:  "cannot be changed while the server is not stopped",
		})
		return
	case errors.Is(err, errServerNotStoppedForPlacement):
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "placement_group",
			Reason:       "constraint",
			HelpMessage:  "cannot be changed while the server is not stopped",
		})
		return
	case err != nil:
		writeNotFound(w, "server", id)
		return
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{"server": p.view(updated)})
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
	// No protection check here, and that is measured rather than overlooked.
	//
	// #212 asked for one, on the reasonable-sounding ground that a stored flag
	// named `protected` should protect. Two runs against fr-par-1, each setting
	// the flag and confirming it with a fresh GET before deleting, answered 204
	// and left a 404 behind. The flag guards the action endpoint — poweroff,
	// stop_in_place, reboot, terminate all answer precondition_failed — and this
	// verb is not one of them.
	//
	// Written down because the intuition is strong enough that somebody will come
	// back to add the check. TestProtectionDoesNotBlockTheDeleteVerb fails if they
	// do.
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
// Private NICs go with the server, and that is the third thing this function had
// to learn rather than the first: volumes, then addresses, then these. Each time
// the shape was the same — something the server held stayed in the store naming a
// server that answers 404 — and each time it took a different symptom to notice.
//
// Here the symptom is an address nobody can reach and nobody can reclaim: a NIC
// listing is scoped by server, so a client cannot see the NIC, while its IPAM
// address stays booked and the network's allocator, rebuilt from what IPAM holds,
// never offers it again. Measured against fr-par-1, which releases both with the
// server.
func (p *Pack) releaseServerResources(ctx context.Context, id, zone string) {
	for _, vol := range p.volumesOf(id) {
		p.detachStoredVolume(vol)
	}
	for _, nic := range p.privateNICsOf(id) {
		p.releaseNIC(ctx, nic)
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

	var req serverActionRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}

	now := p.env.Now()

	// The transactional half — one action at a time on one server, the
	// existence check inside the hold, the runtime work outside the store lock,
	// the conditional write-back scoped to what the action owns — is
	// Binding.Observe, shared with the two other packs' lifecycles instead of
	// staying this pack's inline copy (#510). What it holds is measured
	// history: two concurrent poweron each launched a container and the API
	// described as "stopped" a machine that was running
	// (TestConcurrentPowerOnStartsTheMachineOnce), and a tag acknowledged
	// while the machine boots must survive the action's write-back (#295,
	// TestConcurrentServerUpdateAndActionKeepBothWrites).
	//
	// The reply is decided under the hold and written after it: what to answer
	// depends on state read under the lock, and writing it there would hold
	// the target across the client's read of the response.
	var reply func()
	_, err := p.binding().Observe(p.env.Store, p.env.Now, kindServer, id, func(res *resource.Resource) bool {
		if res.Tenant.Zone != zone {
			reply = func() { writeNotFound(w, "server", id) }
			return false
		}

		// The protection flag was stored at create and at update, published on
		// every read, and consulted nowhere (#212). A field that round-trips
		// perfectly and governs nothing reads as a feature, which is the *un
		// commentaire n'est pas un contrôle* family arriving through data
		// rather than prose.
		//
		// Which door it closes was measured on fr-par-1, and the measurement
		// reversed the issue: DELETE removes a protected server with 204, twice
		// over. It is the action endpoint that refuses, for every action that
		// stops or destroys the machine. Implementing the intuition would have
		// made this emulator diverge from the cloud it imitates.
		//
		// TestProtectionRefusesEveryStoppingAction fails without this.
		if stopsTheMachine(req.Action) && protectedServer(res) {
			reply = func() { writePreconditionFailed(w, "protected_resource", "server is protected") }
			return false
		}

		reply = func() {
			emulator.WriteJSON(w, http.StatusAccepted, map[string]any{"task": task(p.env.NewID(), "server_"+req.Action, now)})
		}
		return p.applyServerAction(w, r, req, res, id, zone, now, &reply)
	})
	if err != nil {
		writeNotFound(w, "server", id)
		return
	}
	reply()
}

// applyServerAction runs one lifecycle action on the held server and reports
// whether the change must be written back. It rewrites reply when the action
// answers something other than the accepted task.
func (p *Pack) applyServerAction(w http.ResponseWriter, r *http.Request, req serverActionRequest,
	res *resource.Resource, id, zone string, now time.Time, reply *func()) bool {
	switch req.Action {
	case "poweron":
		// A poweron on a server that is already running is not a second launch.
		// The runtime refuses a name it has already handed out, so relaunching
		// reported a failed start on a machine that was running perfectly well:
		// the same lie as the race above, arriving through the other door. It is
		// also what the real API does, which lists poweron only for a server
		// that is not up.
		if res.State != "running" {
			// The ephemeral address dynamic_ip_required asks for, allocated
			// before the boot so it rides the launch like an attached flexible
			// IP does.
			p.ensureDynamicAddress(res)
			// The state is the binding's to set, not this switch's. With no
			// runtime the server reaches running, which is the documented
			// degraded mode; with a runtime that failed to start the machine it
			// stays stopped, because a server reported running while nothing
			// exists is the defect this project is built to avoid.
			//
			// The whole post-boot order rides inside: the promised addresses
			// (an address attached at create was once published and never
			// routed, #116), then the firewall, replayed by the shared
			// Reconciler in the one order every pack follows (#510).
			p.startMachine(r.Context(), res)
		}
		res.Attrs["allowed_actions"] = allowedActions(res.State, protectedServer(res))
	case "reboot":
		// A reboot is a stop then a start, which is what the two other packs
		// already asked of the runtime and what this one did not. Filed with
		// poweron under a comment saying the case was handled, it called the
		// start alone: the runtime refuses to relaunch a name it has already
		// served, so the action answered `success`, the API said `running`, and
		// the machine had the same container pid, an uptime still climbing and
		// a transient marker unit still alive (#547, measured 2026-08-27).
		//
		// The dynamic address is not released here, and that is the whole
		// difference with poweroff: upstream releases it on a stop, never on a
		// reboot, so a client's ephemeral address survives its own restart.
		p.ensureDynamicAddress(res)
		// A reboot of a server that is not up is a start: the SDK lists reboot
		// for a running server only, but nothing here leans on that list, and
		// inventing a refusal upstream may not answer would be a divergence
		// nobody measured. Reconciler.Reboot skips the stop for it.
		p.rebootMachine(r.Context(), res)
		res.Attrs["allowed_actions"] = allowedActions(res.State, protectedServer(res))
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
		res.Attrs["allowed_actions"] = allowedActions(res.State, protectedServer(res))
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
		// The server is gone: nothing to write back, and the accepted task is
		// the whole answer.
		*reply = func() {
			emulator.WriteJSON(w, http.StatusAccepted, map[string]any{"task": task(p.env.NewID(), "server_terminate", now)})
		}
		return false
	case "backup":
		// Accepted and ignored: backups need an image catalogue the emulator
		// does not have. Declared here so the action does not 400 in a script.
	default:
		*reply = func() {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "action",
				Reason:       "constraint",
				HelpMessage:  "unknown action " + req.Action,
			})
		}
		return false
	}
	// Starting a machine takes tens of seconds and runs outside the lock; the
	// conditional write-back is Observe's, so a terminate that landed meanwhile
	// stays landed — Put would bring the server back, address and all.
	return true
}

// stopsTheMachine names the actions protection refuses.
//
// The four were measured one by one against fr-par-1 on a protected running
// server, and every one answered precondition_failed / protected_resource. The
// two that do not appear were measured on the same server and are allowed:
// backup, and poweron on a stopped one. So the flag guards the machine's
// running state, not the record — which is also why DELETE goes through.
func stopsTheMachine(action string) bool {
	switch action {
	case "poweroff", "stop_in_place", "reboot", "terminate":
		return true
	}
	return false
}

// protectedServer reads the flag off a stored server.
//
// Through a type assertion because Attrs is map[string]any and a restored
// snapshot decodes JSON into it: whatever the pack wrote as a bool comes back a
// bool, but a hand-written snapshot may carry anything at all, and the zero
// value of a failed assertion is the safe reading of "not protected" — refusing
// an action nobody protected would be the worse failure.
func protectedServer(res *resource.Resource) bool {
	protected, _ := res.Attrs["protected"].(bool)
	return protected
}

// allowedActions is what a client may do next, which follows from the state
// rather than from the action that was asked for. Deriving it is what keeps a
// failed start from advertising poweroff on a server that never came up.
//
// Protection subtracts from that list rather than replacing it, which is the
// only reading consistent with what fr-par-1 answered: a protected running
// server lists ["backup"] alone, a protected stopped one ["poweron", "backup"],
// and those are exactly the lists left once the four refused actions are
// removed. Standby was not measured — no client this suite drives reaches it
// protected — so it is derived the same way rather than guessed at separately.
func allowedActions(state string, protected bool) []any {
	var actions []any
	switch state {
	case "running":
		// terminate belongs here and was missing until the same measurement
		// listed it: an unprotected running server answers ["poweroff",
		// "terminate", "reboot", "stop_in_place", "backup"]. A client reading
		// the list to decide whether it may destroy the server was being told
		// no.
		actions = []any{"poweroff", "terminate", "reboot", "stop_in_place", "backup"}
	case "stopped in place":
		// Standby keeps the machine and its local storage, so powering it fully
		// off is the operation that follows from here as much as starting it
		// again. Listed rather than omitted: a client reading allowed_actions to
		// decide would otherwise have no way out of standby but a boot.
		actions = []any{"poweron", "poweroff", "backup"}
	default:
		actions = []any{"poweron", "backup"}
	}
	if !protected {
		return actions
	}
	kept := make([]any, 0, len(actions))
	for _, action := range actions {
		if name, ok := action.(string); ok && stopsTheMachine(name) {
			continue
		}
		kept = append(kept, action)
	}
	return kept
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
	// Two keys the cloud writes and the SDK no longer declares (#366), and the
	// decision that issue asked for: SERVED, with the values two recordings of a
	// real fr-par account carry on every CreateServer, GetServer and
	// UpdateServer — corpus/scaleway/scw-instance.jsonl (2026-08-21) and
	// corpus/scaleway/scw-billed-shapes.jsonl (2026-08-24).
	//
	// Served rather than declined, because omitting a key is not the same answer
	// as writing it empty: a client reading server["bootscript"] finds a present
	// null on the cloud and nothing here, and DeclinedFields() is for a field
	// this emulator cannot answer truthfully, not for one whose whole content is
	// a constant the recording states.
	//
	// The SDK is not the source here and cannot be: type Server declares
	// neither field any more (instance_sdk.go, read 2026-08-27). That is the
	// case clientImageView already documents on the image — "here because a
	// recording said so, not because the SDK did" — and a recording of the wire
	// outranks the SDK on what is ON the wire.
	//
	// bootscript is null because the product is retired upstream and the key
	// survived it; extra_networks is an empty array on every answer recorded.
	//
	// TestAServerAnswersTheTwoKeysTheSDKNoLongerDeclares fails without this.
	out["bootscript"] = nil
	out["extra_networks"] = []any{}
	// Deprecated upstream and always null in routed IP mode, which is this
	// emulator's default; the real API still serves the key.
	out["ipv6"] = nil
	// The stored attribute is the bare group ID; what the SDK declares on a
	// server is the object (Server.PlacementGroup is *PlacementGroup), with
	// policy_respected pinned false because the SDK says the server endpoints
	// always answer false there. Null for a server in no group, which is what
	// the Terraform provider branches on (`if server.PlacementGroup != nil`).
	out[attrServerPlacementGroup] = p.serverPlacementGroupView(res)
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
		out = append(out, serverIPView(ip.ID, address, false, tagValues(ip.Attrs["tags"])))
	}
	if address := server.Runtime[runtimeDynamicIPKey]; address != "" {
		// A dynamic address is not a flexible IP upstream and never appears in
		// /ips, so it has no record and therefore no tags of its own.
		out = append(out, serverIPView(server.Runtime[runtimeDynamicIPIDKey], address, true, []any{}))
	}
	return out
}

// attachIPToServer gives the server one flexible IP, at position seq in its
// public_ips list. One body for the create loop and the update reconciliation,
// because the attach mechanics had already diverged between two paths once
// (create left State at "detached" while updateIP set it).
//
// Taken from whoever held it, the way updateIP does: it unroutes the previous
// machine before it routes the new one. Setting the new owner without that
// left the old machine carrying the address, so under a runtime two machines
// claimed the same /32 — and the first server lost its address with no error
// anywhere.
//
// TestCreatingAServerDoesNotStealALiveAddress fails without this.
// detachAddress reads the previous holder out of the record itself, so it has
// to run before the record is rewritten — the first version passed it the new
// server, which type-checks and reads no address at all, and the falsification
// caught it because the test kept passing with the call removed.
func (p *Pack) attachIPToServer(ctx context.Context, ip, server *resource.Resource, seq int) {
	if previous, _ := ip.Attrs["server"].(map[string]any); previous != nil {
		if id, _ := previous["id"].(string); id != "" && id != server.ID {
			p.detachAddress(ctx, ip)
		}
	}
	name, _ := server.Attrs["name"].(string)
	ip.Attrs["server"] = map[string]any{"id": server.ID, "name": name}
	// The state moves with the attachment. The create path used to set the
	// server and leave the state at "detached", so every address attached at
	// create described itself as free while carrying a machine — updateIP has
	// always set both, and an audit found the two paths disagreeing.
	ip.State = "attached"
	// The position travels with the attachment too: public_ips answers in the
	// order the client named, not the order the store holds (#320).
	recordAttachOrder(ip, seq)
	p.env.Store.Put(ip)
	p.attachAddress(ctx, ip, server)
}

// setServerIPs makes the server's attached flexible IPs be exactly the given
// list, in the given order — which is what the field means upstream: "A list
// of reserved IP IDs to attach to the Instance" (UpdateServerRequest.PublicIPs,
// instance_sdk.go:3961). An address dropped from the list detaches; it is not
// deleted, because on Scaleway a reserved address outlives its attachment.
//
// An unknown id is refused before anything is written, so a 404 cannot leave
// the reconciliation half-done.
func (p *Pack) setServerIPs(ctx context.Context, server *resource.Resource, ids []string) (badID string, ok bool) {
	attach := make([]*resource.Resource, 0, len(ids))
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		ip, found := p.env.Store.Get(Name, kindIP, id)
		if !found || ip.Tenant.Zone != server.Tenant.Zone {
			return id, false
		}
		attach = append(attach, ip)
		wanted[ip.ID] = true
	}
	for _, ip := range p.attachedIPsOf(server.ID, server.Tenant.Zone) {
		if wanted[ip.ID] {
			continue
		}
		p.detachAddress(ctx, ip)
		_ = p.env.Store.Update(Name, kindIP, ip.ID, func(stored *resource.Resource) error {
			stored.Attrs["server"] = nil
			stored.State = "detached"
			delete(stored.Runtime, runtimeAttachSeqKey)
			stored.Updated = p.env.Now()
			return nil
		})
	}
	for i, ip := range attach {
		p.attachIPToServer(ctx, ip, server, i)
	}
	return "", true
}

// serverIPView is one ServerIP entry. routed_ip_enabled is this emulator's
// default, so the machine carries the address itself rather than being NATed
// to it, flexible and dynamic alike.
//
// This one function renders BOTH Server.public_ip and every element of
// Server.public_ips, which is why #368 is one defect and not two: the gateway
// and the tags were missing from four places in three operations, all of them
// this map.
//
// TestAnAttachedAddressPublishesItsGatewayAndItsOwnTags fails without this.
func serverIPView(id, address string, dynamic bool, tags []any) map[string]any {
	return map[string]any{
		"id":      id,
		"address": address,
		// A STRING, never null (#368). A routed address is a /32 and still
		// routes off-link through a gateway: the cloud publishes one on every
		// element of public_ips, and a guest reading its own route off the
		// answer found nothing here.
		//
		// The value is this pack's own address plan rather than an invented
		// constant. flexibleBlock is 203.0.113.0/24 and allocateFlexibleAddress
		// reserves its first two addresses — the network address and one more,
		// because "the runtime answers on the first usable address" — so
		// 203.0.113.1 is the address no flexible IP can ever be handed and the
		// one a machine on this block would route through. One block, one
		// gateway, stable between two reads, which is what anything that stores
		// it needs.
		"gateway":           flexibleGateway,
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
		// The address's OWN tags (#368), not an empty list: the tags a client
		// sends on CreateIP reach the cloud's view of the attached address, and
		// this view answered [] for a recording whose address carried one. A
		// copy rather than the stored slice, because the response map outlives
		// this call and nothing may hand a caller a window onto the store —
		// TestTagValuesCopiesTheStoredSlice fails without the copy, and it is an
		// internal test because only a restored snapshot reaches the branch
		// where an alias is possible.
		"tags": tags,
	}
}

// flexibleGateway is the gateway every emulated public address routes through.
//
// Derived from flexibleBlock rather than written twice: the first usable
// address of the block the allocator hands out from, which is the one
// allocateFlexibleAddress reserves and can therefore never give to an IP.
var flexibleGateway = flexiblePrefix.Addr().Next().String()

// tagValues copies a stored tag list into the shape a response carries.
//
// Both stored shapes are read for the reason hasEveryTag states: a live create
// stores []string and a JSON snapshot restores []any, so a restored session
// must answer like the session that produced it. The copy is what keeps a
// response from aliasing the store.
func tagValues(stored any) []any {
	switch tags := stored.(type) {
	case []string:
		out := make([]any, 0, len(tags))
		for _, t := range tags {
			out = append(out, t)
		}
		return out
	case []any:
		return append([]any{}, tags...)
	default:
		return []any{}
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
			// Both products, through the shared resolver: the map named a
			// volume by id and only instance/v1 was searched, so
			// `volumes.0.id=<a block disk>` answered "volume … does not exist
			// in fr-par-1" about a disk the same emulator had just created
			// (#571).
			vol, found := p.anyVolume(tmpl.ID)
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
			if err := p.attachStoredVolume(vol, server, name); err != nil {
				return err
			}
			keep[vol.ID] = true
			view[key] = serverVolumeView(vol)
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
			p.detachStoredVolume(vol)
		}
	}
	server.Attrs["volumes"] = view
	return nil
}

// filterServers applies the ListServers filters a server's own record answers.
//
// It read `name` and nothing else, which is how `scw instance server list
// state=running` came back with five stopped servers. A list that silently
// ignores a filter is worse than one that refuses it: the caller gets an answer
// shaped exactly like the right one. The declared filters that reach beyond the
// record — addresses and NICs — live in filterServersByLinks, so between the
// two every parameter the operation declares is served (#277).
//
// tags is joined with commas by the SDK and treated as a conjunction: a server
// matches when it carries every tag asked for. That is the reading the CLI's own
// help implies; if the real API turns out to disjoin, this is the line to change
// and the conformance suite is where it should be caught.
func filterServers(all []*resource.Resource, q url.Values) []*resource.Resource {
	name := q.Get("name")
	state := q.Get("state")
	commercialType := q.Get("commercial_type")
	// csvValues rather than a split on q.Get: the SDK's AddToQuery emits one
	// `tags` key per element, and reading only the first counted one tag of
	// two — every extra tag silently widened the filter.
	tags := csvValues(q, "tags")
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

// filterServersByLinks applies the ListServers filters that reach beyond the
// server's own record: its identifiers, its private NICs and its addresses.
//
// The SDK's own comments give each its semantics — `servers` and
// `private_networks` are comma-separated lists, `private_nic_mac_address`
// matches a NIC's MAC, `with_ip` matches "both private_ip and public_ip",
// `without_ip` keeps the servers holding no public address, and the deprecated
// `private_ip` matches the address a NIC holds, which is where this emulator's
// machines carry their private address.
//
// `without_ip=false` keeps everything: the SDK documents only the true
// direction ("list Instances that are not attached to a public IP"), and
// serving an undocumented complement would be an invented semantic.
func (p *Pack) filterServersByLinks(all []*resource.Resource, q url.Values) []*resource.Resource {
	ids := idSet(q, "servers")
	mac := q.Get("private_nic_mac_address")
	networks := csvValues(q, "private_networks")
	if pn := q.Get("private_network"); pn != "" {
		networks = append(networks, pn)
	}
	withIP := q.Get("with_ip")
	privateIP := q.Get("private_ip")
	withoutIP, _ := queryBool(q, "without_ip")
	if ids == nil && mac == "" && len(networks) == 0 && withIP == "" && privateIP == "" && !withoutIP {
		return all
	}

	kept := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if ids != nil && !ids[res.ID] {
			continue
		}
		nics := p.privateNICsOf(res.ID)
		if mac != "" && !nicWithMAC(nics, mac) {
			continue
		}
		if len(networks) > 0 && !nicOnAnyNetwork(nics, networks) {
			continue
		}
		if privateIP != "" && !p.nicHoldsAddress(nics, privateIP) {
			continue
		}
		if withIP != "" && !contains(p.publicAddressesOf(res), withIP) && !p.nicHoldsAddress(nics, withIP) {
			continue
		}
		if withoutIP && len(p.publicAddressesOf(res)) > 0 {
			continue
		}
		kept = append(kept, res)
	}
	return kept
}

func nicWithMAC(nics []*resource.Resource, mac string) bool {
	for _, nic := range nics {
		if textOf(nic.Attrs["mac_address"]) == mac {
			return true
		}
	}
	return false
}

func nicOnAnyNetwork(nics []*resource.Resource, networks []string) bool {
	for _, nic := range nics {
		if contains(networks, nic.Runtime[runtimePrivateNetworkKey]) {
			return true
		}
	}
	return false
}

func (p *Pack) nicHoldsAddress(nics []*resource.Resource, address string) bool {
	for _, nic := range nics {
		if p.addressOfNIC(nic.ID) == address {
			return true
		}
	}
	return false
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

// orEmpty is how a list of strings enters Attrs: as the shape a snapshot gives
// back, and never null (#567).
//
// Two jobs in one line, and the second is the one that was missing. The empty
// list rather than nil, because every recording of this API answers `"tags":
// []` and a client reading null where the cloud sends an array branches
// differently. And []any rather than []string, because Attrs crosses
// encoding/json on every snapshot: a stored []string comes back a []any, so
// the pack's own value changes type behind the readers' backs the first time
// `feint snapshot load` or `PUT /_feint/state` is used. Measured on
// 2026-08-28: a barrage of this pack left 82 resources — security groups,
// snapshots, volumes and a VPC — carrying a []string in Attrs["tags"].
//
// Nothing broke, and that is the reason it lasted: hasEveryTag and tagsOf each
// carry a hand-written type switch tolerating both shapes, one per file, which
// is #542's seven copies one storey down. storetest.GoShapes now refuses the
// write instead of asking each reader to survive it, and
// TestABarrageLeavesTheStoreCoherent fails without this.
func orEmpty(tags []string) []any {
	out := make([]any, 0, len(tags))
	for _, tag := range tags {
		out = append(out, tag)
	}
	return out
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
	// A volume that lives in block gets block's rendering inside the server,
	// which is an instance VolumeServer carrying volume_type "sbs_volume" — the
	// value that sends the Terraform provider to the block fallback (#8).
	// Copying the instance view here would publish a volume with no type at all.
	out := map[string]any{"0": serverVolumeView(root)}
	for key, tpl := range templates {
		if key == "0" || tpl.ID == "" {
			continue
		}
		// Both products: `additional-volumes.0=<a block volume id>` resolved
		// instance/v1 alone, so a create naming a disk of the block product
		// skipped it silently and answered 201 with the volume unattached —
		// the same "declared, read, not to the end" shape this function was
		// written for, one product further on (#571).
		vol, ok := p.anyVolume(tpl.ID)
		if !ok || vol.Tenant.Zone != zone {
			continue
		}
		// Skipped rather than refused, like an unknown id above: the answer says
		// which volumes the server actually has, and a create must not take a
		// disk from a running machine.
		if err := p.attachStoredVolume(vol, server, serverName); err != nil {
			continue
		}
		out[key] = serverVolumeView(vol)
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
	// Both products. AttachServerVolumeRequest declares volume_type with
	// "sbs_volume" among its values (instance_sdk.go), so the operation this
	// route claims is defined over a block volume — and resolving kindVolume
	// alone answered 404 for every one of them, including a disk `scw block
	// volume create` had just made. Measured with scw 2.56.3 (#571).
	vol, ok := p.anyVolume(req.VolumeID)
	if !ok || vol.Tenant.Zone != zone {
		writeNotFound(w, "volume", req.VolumeID)
		return
	}
	// The API refuses to move a volume already in use, and Terraform reads that
	// error rather than guessing.
	//
	// This is also where the anti-theft guard of #202 starts covering a block
	// disk: while the resolution above found nothing, one server could not take
	// another's block root because nobody could reach it at all, which is an
	// accident and not a control. attachStoredVolume asks the question for both
	// kinds — TestAttachingDoesNotStealAnotherServersVolume now walks its three
	// doors twice, once per product.
	key := p.nextVolumeKey(res)
	serverName, _ := res.Attrs["name"].(string)
	if err := p.attachStoredVolume(vol, res, serverName); err != nil {
		writePrecondition(w, "volume", vol.ID, "the volume is attached to another server")
		return
	}

	// The server's map grows inside the store lock, on a fresh copy of the
	// stored map — never through the clone's, which resource.Clone shares with
	// the store, and never by Commit, whose wholesale write erased a concurrent
	// write to another field of the same server after its 200 (#295).
	entry := serverVolumeView(vol)
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindServer, id, func(stored *resource.Resource) error {
		volumes := make(map[string]any, len(volumeMapOf(stored))+1)
		for k, v := range volumeMapOf(stored) {
			volumes[k] = v
		}
		volumes[key] = entry
		stored.Attrs["volumes"] = volumes
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "server", id)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"server": p.view(updated)})
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

	if !volumeMapHas(volumeMapOf(res), req.VolumeID) {
		writeNotFound(w, "volume", req.VolumeID)
		return
	}
	// Both products, and this is the operation of the family that did not answer
	// 404 — it answered 200 and released nothing, which is worse.
	//
	// `scw instance server terminate` walks GetVolume (instance, 404) →
	// GetVolume (block, 200) → detach-volume → and then polls the block volume
	// until its status leaves `in_use`. With the resolution below reading
	// kindVolume alone, the detach came back 200 while the disk kept its server
	// in Runtime, so the status never moved and the CLI never returned: rc=124
	// at twenty-five seconds, five identical block GETs in its own -D trace,
	// measured on 2026-08-28 against a binary built from 3b00d23.
	//
	// TestTerminateReleasesABlockRootVolume fails without this.
	if vol, ok := p.anyVolume(req.VolumeID); ok && vol.Tenant.Zone == zone {
		p.detachStoredVolume(vol)
	}
	// The server's map shrinks inside the store lock, on a fresh copy — the
	// delete used to go through the clone's map, which resource.Clone shares
	// with the store, and the Commit that followed erased a concurrent write
	// to another field of the same server after its 200 (#295).
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindServer, id, func(stored *resource.Resource) error {
		current := volumeMapOf(stored)
		volumes := make(map[string]any, len(current))
		for k, v := range current {
			view, _ := v.(map[string]any)
			if view != nil && view["id"] == req.VolumeID {
				continue
			}
			volumes[k] = v
		}
		stored.Attrs["volumes"] = volumes
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "server", id)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"server": p.view(updated)})
}

// volumeMapHas reports whether the map lists a volume by id.
func volumeMapHas(volumes map[string]any, volumeID string) bool {
	for _, entry := range volumes {
		if view, _ := entry.(map[string]any); view != nil && view["id"] == volumeID {
			return true
		}
	}
	return false
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
