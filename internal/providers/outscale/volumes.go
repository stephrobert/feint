package outscale

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Volumes, because the Terraform provider creates one and then reads it back.
//
// The pack served none, so `outscale_volume` failed on CreateVolume and the
// whole plan died before the machine it was for. A CLI never showed it: nothing
// in the oapi-cli suite creates a volume, which is the gap this batch exists to
// close.
//
// What is emulated is what the emulator can be honest about: a size, a type, a
// subregion, and a link to a Vm. There are no bytes behind it — that is stated
// in docs/limits.md and is why snapshots stay declined.

const (
	volumeStateAvailable = "available"
	volumeStateInUse     = "in-use"
	defaultVolumeType    = "standard"
)

type createVolumeRequest struct {
	SubregionName string `json:"SubregionName"`
	Size          int    `json:"Size"`
	VolumeType    string `json:"VolumeType"`
	Iops          int    `json:"Iops"`
	SnapshotID    string `json:"SnapshotId"`
	ClientToken   string `json:"ClientToken"`
	DryRun        *bool  `json:"DryRun"`
}

// createVolume answers the one field their schema requires, and refuses without
// it: SubregionName is `required` in CreateVolumeRequest, and a volume placed
// nowhere is not a volume a client asked for.
//
// TestAVolumeIsCreatedReadAndLinked fails without this.
func (p *Pack) createVolume(w http.ResponseWriter, r *http.Request) {
	var req createVolumeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.SubregionName == "" {
		p.badRequest(w, "SubregionName is required")
		return
	}
	// Required is not enough: a zone the catalogue does not declare is refused
	// rather than stored, so the write path and ReadSubregions cannot
	// contradict each other (#269).
	if !p.knownSubregion(req.SubregionName) {
		p.badRequest(w, "the Subregion "+req.SubregionName+" does not exist in "+p.region)
		return
	}
	size := req.Size
	if req.SnapshotID != "" {
		// A volume may come from a snapshot the emulator knows — a control-plane
		// record, snapshots.go says what that means — and inherits its size when
		// none is asked. An unknown SnapshotId is refused the way the real API
		// refuses one; the old blanket refusal predates served snapshots.
		snapshot, found := p.env.Store.Get(Name, kindSnapshot, req.SnapshotID)
		if !found {
			p.notFound(w, "snapshot", req.SnapshotID)
			return
		}
		if size == 0 {
			size, _ = snapshot.Attrs["VolumeSize"].(int)
		}
	}

	now := p.env.Now()
	res := resource.New(newID("vol", p.env.NewID()), kindVolume, resource.Tenant{Provider: Name}, volumeStateAvailable, now)
	res.Attrs = map[string]any{
		"SubregionName": req.SubregionName,
		"Size":          size,
		"VolumeType":    orDefault(req.VolumeType, defaultVolumeType),
		"Iops":          req.Iops,
		"ClientToken":   req.ClientToken,
		"Tags":          []any{},
	}
	if req.SnapshotID != "" {
		// Stored only when there is one: the view copies Attrs verbatim, and the
		// real cloud omits the key on a volume that has no provenance — measured
		// on a real account, where "" never appears. Absent and empty are not
		// the same claim.
		res.Attrs["SnapshotId"] = req.SnapshotID
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Volume":          p.volumeView(res),
		"ResponseContext": p.context(),
	})
}

type readVolumesRequest struct {
	Filters        filterSet `json:"Filters"`
	ResultsPerPage int       `json:"ResultsPerPage"`
	DryRun         *bool     `json:"DryRun"`
}

// volumeFilters are what a volume can answer from what is stored, the link
// filters included: the Terraform provider polls ReadVolumes filtered on
// LinkVolumeVmIds to wait for an attach and again for a detach, so refusing
// them fails `outscale_volume_link` on the apply and again on the destroy.
var volumeFilters = []string{
	"VolumeIds", "VolumeStates", "VolumeTypes", "SubregionNames",
	"SnapshotIds", "VolumeSizes", "ClientTokens",
	"LinkVolumeVmIds", "LinkVolumeDeviceNames", "LinkVolumeLinkStates",
	"LinkVolumeDeleteOnVmDeletion",
}

func (p *Pack) readVolumes(w http.ResponseWriter, r *http.Request) {
	var req readVolumesRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, volumeFilters...) {
		return
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindVolume, resource.Tenant{Provider: Name}) {
		if !volumeMatches(res, req.Filters) {
			continue
		}
		out = append(out, p.volumeView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Volumes":         page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

type updateVolumeRequest struct {
	VolumeID   string `json:"VolumeId"`
	Size       int    `json:"Size"`
	VolumeType string `json:"VolumeType"`
	Iops       int    `json:"Iops"`
	DryRun     *bool  `json:"DryRun"`
}

// updateVolume grows a volume, and refuses to shrink one.
//
// The real API refuses too: a filesystem does not survive its disk getting
// smaller. Accepting it would answer success to a request that destroys data
// everywhere else.
//
// TestAVolumeDoesNotShrink fails without this.
func (p *Pack) updateVolume(w http.ResponseWriter, r *http.Request) {
	var req updateVolumeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	// The read, the shrink check and the write are one critical section held
	// by the store: as a Get-check-Commit sequence this lost a concurrent write
	// to another field of the same volume — the tag the client had just been
	// acknowledged, measured at 11/200 trials (#295).
	// TestConcurrentTagAndLinkKeepBothVolumeWrites fails without this shape.
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindVolume, req.VolumeID, func(stored *resource.Resource) error {
		if req.Size > 0 {
			current, _ := stored.Attrs["Size"].(int)
			if req.Size < current {
				return errVolumeShrinks
			}
			stored.Attrs["Size"] = req.Size
		}
		if req.VolumeType != "" {
			stored.Attrs["VolumeType"] = req.VolumeType
		}
		if req.Iops > 0 {
			stored.Attrs["Iops"] = req.Iops
		}
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	switch {
	case errors.Is(err, errVolumeShrinks):
		p.conflict(w, "a volume cannot shrink: "+req.VolumeID+" is already larger than the size requested")
		return
	case err != nil:
		p.notFound(w, "volume", req.VolumeID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Volume":          p.volumeView(updated),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VolumeID string `json:"VolumeId"`
		DryRun   *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindVolume, req.VolumeID)
	if !found {
		p.notFound(w, "volume", req.VolumeID)
		return
	}
	// A linked volume does not go, which is what the real API answers and what a
	// client destroying in the wrong order depends on to retry.
	if vmID, _ := res.Attrs["LinkedVmId"].(string); vmID != "" {
		if vm, alive := p.env.Store.Get(Name, kindVM, vmID); alive && vm.State != stateTerminated {
			p.conflict(w, "the volume "+res.ID+" is still linked to "+vmID)
			return
		}
	}
	p.env.Store.Delete(Name, kindVolume, req.VolumeID)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// linkVolume attaches a volume to a Vm under a device name.
//
// The link is stored on the volume, once: LinkedVolumes is computed from it in
// the view. Holding the same fact on both sides is what let a Scaleway audit
// find one server's disk on another, and this pack is not repeating it.
func (p *Pack) linkVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VolumeID   string `json:"VolumeId"`
		VMID       string `json:"VmId"`
		DeviceName string `json:"DeviceName"`
		DryRun     *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if _, found := p.env.Store.Get(Name, kindVM, req.VMID); !found {
		p.notFound(w, "Vm", req.VMID)
		return
	}
	// The holder check runs on the stored volume, under the lock, never on a
	// stale clone: checked outside, two concurrent links on one free volume
	// both passed and the loser's 200 was erased — and the erased write was
	// sometimes the tag another handler had just acknowledged (#295, the
	// measured pair of this file).
	// TestConcurrentTagAndLinkKeepBothVolumeWrites fails without this shape.
	var holder string
	err := p.env.Store.Update(Name, kindVolume, req.VolumeID, func(stored *resource.Resource) error {
		if held, _ := stored.Attrs["LinkedVmId"].(string); held != "" && held != req.VMID {
			holder = held
			return errVolumeHeld
		}
		stored.Attrs["LinkedVmId"] = req.VMID
		stored.Attrs["DeviceName"] = req.DeviceName
		stored.State = volumeStateInUse
		stored.Updated = p.env.Now()
		return nil
	})
	switch {
	case errors.Is(err, errVolumeHeld):
		p.conflict(w, "the volume "+req.VolumeID+" is already linked to "+holder)
		return
	case err != nil:
		p.notFound(w, "volume", req.VolumeID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func (p *Pack) unlinkVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VolumeID string `json:"VolumeId"`
		// ForceUnlink is sent by the Terraform provider on every detach, and the
		// conformance gate caught it going unread — which is the one thing worse
		// than refusing it, because the client is told its flag was honoured.
		//
		// It is declared AND marked read here rather than silently accepted,
		// because declaring a field without reading it is exactly the blind spot
		// the unread report cannot see (the same trap SecurityGroupIds fell into
		// on CreateVms). What it does upstream is force a detach past a busy
		// device — a filesystem still mounted, or the root volume of a running
		// machine. Neither can arise here: the emulator holds no bytes and
		// models no root volume, so every detach it can be asked for is already
		// the unforced case. Honouring the flag would mean inventing a busy
		// state to force past.
		ForceUnlink *bool `json:"ForceUnlink"`
		DryRun      *bool `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	emulator.MarkRead(r, "ForceUnlink")
	_ = req.ForceUnlink
	// Same critical section as linkVolume, same reason (#295).
	err := p.env.Store.Update(Name, kindVolume, req.VolumeID, func(stored *resource.Resource) error {
		delete(stored.Attrs, "LinkedVmId")
		delete(stored.Attrs, "DeviceName")
		stored.State = volumeStateAvailable
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		p.notFound(w, "volume", req.VolumeID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// volumeMatches applies every declared filter, the link ones against the link
// the volume holds — which is where the Terraform provider waits for an attach
// and a detach.
func volumeMatches(res *resource.Resource, f filterSet) bool {
	linkedVM := stringOf(res.Attrs["LinkedVmId"])
	device := stringOf(res.Attrs["DeviceName"])
	// The link state, in the same words volumeView publishes: one fact, one
	// place, so a filter and a read cannot disagree about it.
	linkState := ""
	if linkedVM != "" {
		linkState = "attached"
	}
	size := ""
	if n, ok := res.Attrs["Size"].(int); ok {
		size = strconv.Itoa(n)
	}
	return matchesStrings(f, "VolumeIds", res.ID) &&
		matchesStrings(f, "VolumeStates", res.State) &&
		matchesStrings(f, "VolumeTypes", stringOf(res.Attrs["VolumeType"])) &&
		matchesStrings(f, "SubregionNames", stringOf(res.Attrs["SubregionName"])) &&
		matchesStrings(f, "SnapshotIds", stringOf(res.Attrs["SnapshotId"])) &&
		matchesStrings(f, "ClientTokens", stringOf(res.Attrs["ClientToken"])) &&
		matchesStrings(f, "VolumeSizes", size) &&
		matchesStrings(f, "LinkVolumeVmIds", linkedVM) &&
		matchesStrings(f, "LinkVolumeDeviceNames", device) &&
		matchesStrings(f, "LinkVolumeLinkStates", linkState) &&
		matchesBool(f, "LinkVolumeDeleteOnVmDeletion", false)
}

// volumeView is the wire shape. LinkedVolumes is derived from the link stored on
// the volume rather than kept beside it: two places for one fact is how they
// come to disagree.
func (p *Pack) volumeView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+4)
	for key, value := range res.Attrs {
		if key == "LinkedVmId" || key == "DeviceName" {
			continue
		}
		out[key] = value
	}
	out["VolumeId"] = res.ID
	out["State"] = res.State
	out["CreationDate"] = res.Created.Format(time.RFC3339)

	links := make([]any, 0, 1)
	if vmID, _ := res.Attrs["LinkedVmId"].(string); vmID != "" {
		device, _ := res.Attrs["DeviceName"].(string)
		links = append(links, map[string]any{
			"VolumeId":           res.ID,
			"VmId":               vmID,
			"DeviceName":         device,
			"State":              "attached",
			"DeleteOnVmDeletion": false,
		})
	}
	out["LinkedVolumes"] = links
	return out
}

// readVmsState answers the lightweight view a client polls: the state of every
// Vm, without the rest of the machine.
//
// MaintenanceEvents is always empty and that is the honest answer: this emulator
// schedules no maintenance, and inventing an event would put a date in a client's
// plan that nothing will ever act on.
func (p *Pack) readVmsState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		AllVms         *bool     `json:"AllVms"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, "VmIds", "VmStates", "SubregionNames") {
		return
	}

	// AllVms false means running machines only, which is the API's own default:
	// a client polling for what is up must not be handed what is terminated.
	all := boolOr(req.AllVms, false)

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindVM, resource.Tenant{Provider: Name}) {
		if !all && res.State != stateRunning {
			continue
		}
		// SubregionNames was declared to refuseUnsupported above and applied
		// nowhere — the exact blind spot placement.go documents, one call
		// over: a declared-and-unread field is invisible to the unread-field
		// report, and a client filtering by zone got every zone back.
		if !matchesStrings(req.Filters, "VmIds", res.ID) ||
			!matchesStrings(req.Filters, "VmStates", res.State) ||
			!matchesStrings(req.Filters, "SubregionNames", p.vmSubregion(res)) {
			continue
		}
		out = append(out, map[string]any{
			"VmId":    res.ID,
			"VmState": res.State,
			// The machine's own zone, through the same door every other read
			// answers from (#268) — this was the pack's constant too.
			"SubregionName":     p.vmSubregion(res),
			"MaintenanceEvents": []any{},
		})
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"VmStates":        page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

// detachVolumesOf releases the volumes a terminated Vm held, the way
// detachNicsOf releases its interfaces.
//
// Same invariant as the NICs and the same reason it was missed: an exclusive
// resource has one live owner, and the pack re-checked that on every link and on
// no death. A volume left carrying LinkedVmId names a machine that is gone, so
// LinkVolume refuses to attach it anywhere else and UnlinkVolume is the only way
// out — a call no client makes, because from the client's side the Vm is
// terminated and the volume is supposed to be free. The emulator has to be
// restarted.
//
// Unlinked and not deleted: this pack publishes DeleteOnVmDeletion false on every
// volume link, and upstream is explicit that false means "the volume is not
// deleted when terminating the VM". Deleting here would contradict what the same
// pack tells the client one field earlier.
//
// TestTerminatingAVmFreesItsVolumes fails without this.
func (p *Pack) detachVolumesOf(vmID string) {
	for _, vol := range p.env.Store.List(kindVolume, resource.Tenant{Provider: Name}) {
		if stringOf(vol.Attrs["LinkedVmId"]) != vmID {
			continue
		}
		// Re-checked under the lock: the link may have moved between the List
		// above and this write, and detaching somebody else's fresh link would
		// be this loop erasing a 200 it never saw (#295).
		_ = p.env.Store.Update(Name, kindVolume, vol.ID, func(stored *resource.Resource) error {
			if stringOf(stored.Attrs["LinkedVmId"]) != vmID {
				return errVolumeHeld
			}
			delete(stored.Attrs, "LinkedVmId")
			delete(stored.Attrs, "DeviceName")
			stored.State = volumeStateAvailable
			stored.Updated = p.env.Now()
			return nil
		})
	}
}

// The refusals updateVolume and linkVolume answer from inside the store lock,
// where the state they judge cannot move (#295).
var (
	errVolumeShrinks = errors.New("a volume cannot shrink")
	errVolumeHeld    = errors.New("the volume is linked elsewhere")
)
