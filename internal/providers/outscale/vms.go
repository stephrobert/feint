package outscale

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/cloudinit"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The Vm is Outscale's server. Field names and shapes come from the SDK: Vm for
// what a read returns, CreateVmsRequest for what a create takes, VmStateInfo for
// what a state change reports.
//
// The state names are the SDK's own VmState constants — pending, running,
// stopping, stopped, shutting-down, terminated — and not a set invented here. A
// client waiting for "running" waits forever on anything else.

const (
	stateRunning      = "running"
	stateStopped      = "stopped"
	stateTerminated   = "terminated"
	stateShuttingDown = "shutting-down"
)

// maxVMsPerCreate caps one create. The real API refuses past an account quota;
// this stands in for it, small enough that a runaway request fails fast and
// large enough that no honest workflow meets it.
const maxVMsPerCreate = 20

// defaultVmType is what a create with no VmType gets. Outscale's own default is
// account-dependent; this one at least exists in the emulated catalogue, which
// is what a client reading VmTypes back will look for.
const defaultVMType = "tinav6.c1r1p2"

type readVmsRequest struct {
	Filters filterSet `json:"Filters"`
	// The page size every Terraform read sends. Declared and unread, it was a
	// field the client believed was honoured: a client asking for ten rows and
	// getting a thousand pages nothing.
	ResultsPerPage int `json:"ResultsPerPage"`
}

// vmFilters are the ones a Vm can answer from what this pack stores. Everything
// else FiltersVm declares — 66 fields, most of them about block device
// mappings, NIC sub-objects and account ids the emulator has no model for — is
// refused rather than ignored.
var vmFilters = []string{
	"VmIds", "VmStates", "ImageIds", "VmTypes", "KeypairNames",
	"SubnetIds", "NetIds", "PrivateIps",
	// The zone filter FiltersVm declares (osc-sdk-go, client.gen.go:5304),
	// served since the subregion became a stored fact rather than a constant
	// (#268): a constant made this filter either a tautology or a void.
	"SubregionNames",
	// The group filters are what `terraform destroy` asks before removing a
	// security group: which machines still wear it. Without them the destroy
	// fails on the group, after the apply succeeded — so the whole fixture is
	// left standing by a filter nobody had declared.
	"SecurityGroupIds", "SecurityGroupNames",
}

// vmPlacement is CreateVmsRequest's Placement (osc-sdk-go,
// pkg/osc/client.gen.go:6804): the subregion the machine goes to, and its
// tenancy — `default`, `dedicated`, or a dedicated group ID, per the SDK's own
// description, which is why Tenancy is stored verbatim rather than checked
// against a closed list.
type vmPlacement struct {
	SubregionName string `json:"SubregionName"`
	Tenancy       string `json:"Tenancy"`
}

// defaultTenancy is what a machine placed with nothing said runs under.
const defaultTenancy = "default"

// createVmsRequest is the subset of CreateVmsRequest this pack honours. The
// count fields are MinVmsCount and MaxVmsCount: there is no VmCount, and reading
// one meant every request created exactly one machine whatever it asked for.
type createVmsRequest struct {
	// Sent by the Terraform provider on every create. DeletionProtection is a
	// refusal the API owes a client; NestedVirtualization is a fact it reads
	// back.
	DeletionProtection   *bool    `json:"DeletionProtection"`
	NestedVirtualization *bool    `json:"NestedVirtualization"`
	ImageID              string   `json:"ImageId"`
	VMType               string   `json:"VmType"`
	MinVMsCount          *int     `json:"MinVmsCount"`
	MaxVMsCount          *int     `json:"MaxVmsCount"`
	KeypairName          string   `json:"KeypairName"`
	UserData             string   `json:"UserData"`
	SubnetID             string   `json:"SubnetId"`
	SecurityGroupIDs     []string `json:"SecurityGroupIds"`
	BootOnCreation       *bool    `json:"BootOnCreation"`
	// Placement was the field of #268: accepted with a 200, never read, and
	// every read then answered the pack's constant. A machine created in
	// eu-west-2b read back in eu-west-2a, and a multi-AZ Terraform plan
	// re-planned the same in-place change for ever. The chain is
	// request → store → response now; vmPlacementView renders what this stored.
	Placement *vmPlacement `json:"Placement"`
	// The per-machine scalars of the same family (#276): accepted with a 200
	// while every read answered a constant — medium/restart/legacy asked,
	// high/stop/uefi answered. vmoptions.go carries the model and the SDK
	// citations. BootMode is create-only upstream: UpdateVmRequest declares
	// no such field.
	BootMode   *string `json:"BootMode"`
	TpmEnabled *bool   `json:"TpmEnabled"`
	// BsuOptimized is read and deliberately not stored: "This parameter is
	// not available. It is present in our API for the sake of historical
	// compatibility with AWS" (client.gen.go:3029) — the real cloud ignores
	// it too, so the constant false every read answers is upstream's own
	// behaviour, not this pack's invention.
	BsuOptimized *bool `json:"BsuOptimized"`
	vmOptionsRequest
}

type vmIDsRequest struct {
	// ForceStop, which StopVms carries and the Terraform provider sends on every
	// stop. Declared here because this is where the body is decoded, and an
	// undeclared field is one the unread-field report cannot see.
	ForceStop *bool    `json:"ForceStop"`
	VMIDs     []string `json:"VmIds"`
}

type updateVMRequest struct {
	VMID             string   `json:"VmId"`
	VMType           string   `json:"VmType"`
	KeypairName      string   `json:"KeypairName"`
	UserData         string   `json:"UserData"`
	SecurityGroupIDs []string `json:"SecurityGroupIds"`
	// DeletionProtection was declared by UpdateVmRequest upstream and read by
	// nobody here, so the flag could be set at create and never cleared: the
	// delete honoured it, the update silently dropped it, and a client that had
	// protected a machine had no way left to remove it. Answering 200 to that
	// request is the worse half — the client is told the change landed.
	//
	// A pointer because false is the value that matters: a plain bool cannot
	// tell "clear the protection" from "this request says nothing about it".
	DeletionProtection *bool `json:"DeletionProtection"`
	// "(Net only)" upstream; stored and restituted, enacted by nothing —
	// docs/limits.md carries what the flag does not do here.
	IsSourceDestChecked *bool `json:"IsSourceDestChecked"`
	vmOptionsRequest
}

func (p *Pack) readVms(w http.ResponseWriter, r *http.Request) {
	var req readVmsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}

	if p.refuseUnsupported(w, req.Filters, vmFilters...) {
		return
	}

	vms := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindVM, resource.Tenant{Provider: Name}) {
		if !p.vmMatches(res, req.Filters) {
			continue
		}
		// A virtual machine gets its address tens of seconds after it starts,
		// so a create cannot wait for one. It is filled in here instead, on the
		// read a client is making anyway. That makes this read a writer, through
		// a door nobody thinks of as mutating.
		//
		// This pack was the only one that had noticed, and it still wrote back a
		// copy List had handed out *before* the lock was taken. Observe re-reads
		// inside the hold, which is the half a per-pack version keeps missing —
		// and the reason the transactional shape lives in the shared layer now
		// rather than in whichever pack an audit reached first (#211).
		fresh, err := p.binding().Observe(p.env.Store, p.env.Now, kindVM, res.ID,
			func(res *resource.Resource) bool { return p.refreshMachine(r.Context(), res) })
		if err != nil {
			// Deleted while its address was being discovered: the Vm belongs in
			// nobody's answer.
			continue
		}
		vms = append(vms, p.vmView(fresh))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Vms":             page(vms, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

// page truncates a list to the size the client asked for.
//
// No NextPageToken is issued: this emulator holds a handful of resources, so
// there is never a second page to fetch, and a token pointing at nothing is
// worse than none. What matters is that a client asking for N rows is not
// handed more — the field was declared and unread, which is the shape that told
// a client its page size was honoured when it was not.
//
// TestResultsPerPageIsHonoured fails without this.
func page[T any](rows []T, size int) []T {
	if size <= 0 || len(rows) <= size {
		return rows
	}
	return rows[:size]
}

// vmMatches applies every filter this pack serves. A Vm has to pass all of
// them: Outscale's filters are conjunctive, and treating them as alternatives
// would answer more than was asked for, which is the defect this whole file
// exists to remove.
func (p *Pack) vmMatches(res *resource.Resource, f filterSet) bool {
	attr := func(key string) string {
		value, _ := res.Attrs[key].(string)
		return value
	}
	// The groups a machine wears, resolved the way the view resolves them, so a
	// filter and a read cannot disagree about what it carries.
	var groupIDs, groupNames []string
	for _, raw := range p.effectiveSecurityGroups(res) {
		group, _ := raw.(map[string]any)
		groupIDs = append(groupIDs, stringOf(group["SecurityGroupId"]))
		groupNames = append(groupNames, stringOf(group["SecurityGroupName"]))
	}

	return matchesStrings(f, "VmIds", res.ID) &&
		matchesStrings(f, "VmStates", res.State) &&
		matchesStrings(f, "ImageIds", attr("ImageId")) &&
		matchesStrings(f, "VmTypes", attr("VmType")) &&
		matchesStrings(f, "KeypairNames", attr("KeypairName")) &&
		matchesStrings(f, "SubnetIds", attr("SubnetId")) &&
		matchesStrings(f, "NetIds", attr("NetId")) &&
		matchesStrings(f, "PrivateIps", p.addressOf(res)) &&
		matchesStrings(f, "SubregionNames", p.vmSubregion(res)) &&
		matchesAny(f, "SecurityGroupIds", groupIDs...) &&
		matchesAny(f, "SecurityGroupNames", groupNames...)
}

func (p *Pack) createVms(w http.ResponseWriter, r *http.Request) {
	var req createVmsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.ImageID == "" {
		p.badRequest(w, "ImageId is required")
		return
	}
	if !p.validVmFields(w, req.KeypairName, req.UserData) {
		return
	}
	// The option enums, shared with UpdateVm; BootMode is create-only, so its
	// check lives here. Refused rather than stored: a stored value outside
	// the platform's enum is a read that can only lie one way or another.
	if !p.validVmOptions(w, req.vmOptionsRequest) {
		return
	}
	if req.BootMode != nil && !oneOf(*req.BootMode, "legacy", "uefi") {
		p.badRequest(w, "BootMode must be legacy or uefi")
		return
	}
	// A subregion the catalogue does not declare is refused here, not stored:
	// accepting it would recreate the contradiction #269 measured, where the
	// write path took any zone and ReadSubregions denied its existence.
	if req.Placement != nil && req.Placement.SubregionName != "" && !p.knownSubregion(req.Placement.SubregionName) {
		p.badRequest(w, "the Subregion "+req.Placement.SubregionName+" does not exist in "+p.region)
		return
	}

	// Outscale creates MaxVmsCount machines and fails if it cannot reach
	// MinVmsCount. An emulator has no capacity limit, so it always reaches the
	// maximum; honouring the field at all is what makes a request for three
	// machines produce three.
	count := 1
	switch {
	case req.MaxVMsCount != nil && *req.MaxVMsCount > 0:
		count = *req.MaxVMsCount
	case req.MinVMsCount != nil && *req.MinVMsCount > 0:
		count = *req.MinVMsCount
	}
	// Bounded, because the real API is: an account has a quota and a create
	// beyond it is refused. Unbounded, {"MaxVmsCount": 1000000} allocated a
	// million resources and, with a runtime, tried to start a million
	// containers one after another inside the handler.
	if count > maxVMsPerCreate {
		p.badRequest(w, "MaxVmsCount is capped at "+strconv.Itoa(maxVMsPerCreate)+" in this emulator")
		return
	}

	// Two flags the Terraform provider sends on every create, and that the pack
	// declared nowhere: they were accepted and dropped, which told a client its
	// machine was protected when nothing protected it. DeletionProtection is
	// enforced below, in deleteVms; NestedVirtualization is stored and published
	// because a client reads it back and an absent field is a permanent diff.
	//
	// TestDeletionProtectionRefusesTheDelete fails without the first.

	// BootOnCreation defaults to true upstream, so a create with nothing said
	// yields a running machine — which is what every client expects.
	boot := req.BootOnCreation == nil || *req.BootOnCreation

	// Security groups are validated and stored, not silently dropped: the old
	// refusal predates the group family being served. What is stored is the
	// ids; the view resolves them to {id, name} on every read, so a renamed or
	// deleted group cannot leave a stale copy here — one shape, one owner.
	// Enforcement in the runtime is a separate claim, and docs/limits.md
	// carries it: control-plane groups do not filter packets on their own.
	if !p.checkVMSecurityGroups(w, req.SecurityGroupIDs) {
		return
	}

	now := p.env.Now()
	vms := make([]map[string]any, 0, count)

	created, err := p.allocateVms(req, count, now)
	if err != nil {
		// An error answer that leaves machines running is worse than a refusal:
		// the client owns resources it was told it did not get, and Terraform
		// never tracks them. Undone outside the addressing lock, because
		// removeMachine reaches the runtime.
		for _, res := range created {
			p.removeMachine(r.Context(), res)
			p.env.Store.Delete(Name, kindVM, res.ID)
		}
		if errors.Is(err, errUnknownSubnet) {
			p.notFound(w, "Subnet", req.SubnetID)
			return
		}
		if errors.Is(err, errPlacementMismatch) {
			p.badRequest(w, err.Error())
			return
		}
		p.conflict(w, "cannot place a Vm in "+req.SubnetID+": "+err.Error())
		return
	}

	for _, res := range created {
		// Started after the address is reserved and before the answer is built,
		// so the create reports what the boot produced rather than what it
		// intended. The store clones on Put, so the machine name and the running
		// state have to be written back explicitly.
		if boot {
			// Serialised on the target, like every other lifecycle path here:
			// deleteVms and transitionOne both take it, and the create did not,
			// so a delete arriving mid-boot raced the start it was meant to
			// cancel.
			unlock := p.binding().Serialise(res.ID)
			// The base of the write-back: the boot owns the state and the
			// runtime name, and nothing else — a tag acknowledged between the
			// Put above and this Commit survives it (#295).
			base := res.Clone()
			p.powerOn(r.Context(), res)
			if !p.env.Store.Commit(base, res, p.env.Now()) {
				// Gone while it was starting — a delete, or a snapshot restored
				// over it, which `PUT /_feint/state` does and snapshot.go
				// documents as a designed path. The machine has to go with it:
				// the previous version just skipped the answer, leaving a
				// container running that the control plane no longer describes,
				// and a machine nobody describes is a machine nobody stops.
				p.removeMachine(r.Context(), res)
				unlock()
				continue
			}
			unlock()
		}
		vms = append(vms, p.vmView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Vms":             vms,
		"ResponseContext": p.context(),
	})
}

// allocateVms reserves count machines and stores them stopped, under the lock
// that guards the addressing plane. It never boots one: a container start takes
// tens of seconds, and this lock is what every other create on the pack waits
// behind — "un effet de bord lent ne tient pas dans le verrou".
//
// Allocation is read-modify-write over the store: what is free is computed from
// what exists. The mutex was written for exactly that and the Vm path never took
// it, so twelve concurrent creates handed one address to two machines — with a
// runtime, two containers configured with the same static IP.
// TestConcurrentCreatesDoNotShareAnAddress fails without it.
//
// It returns what it stored even when it fails, so the caller can undo the
// batch. Releasing between two Vms of one batch would let another create take an
// address this one is about to use, so the whole batch allocates at once.
func (p *Pack) allocateVms(req createVmsRequest, count int, now time.Time) ([]*resource.Resource, error) {
	unlock := p.lockAddresses()
	defer unlock()

	created := make([]*resource.Resource, 0, count)
	for range count {
		res := resource.New(newVMID(p.env.NewID()), kindVM, resource.Tenant{Provider: Name}, stateStopped, now)
		res.Attrs = map[string]any{
			"ImageId":              req.ImageID,
			"VmType":               orDefault(req.VMType, defaultVMType),
			"DeletionProtection":   boolOr(req.DeletionProtection, false),
			"NestedVirtualization": boolOr(req.NestedVirtualization, false),
			// Resolved once and stored, like Placement below: the chain #276
			// demands is request → store → response, never
			// request → constant → response.
			attrBootMode:   defaultBootMode,
			attrTpmEnabled: boolOr(req.TpmEnabled, false),
		}
		if req.BootMode != nil {
			res.Attrs[attrBootMode] = *req.BootMode
		}
		storeVmOptions(res, req.vmOptionsRequest, stringOf(res.Attrs["VmType"]))
		// The Subnet the client asked for, resolved before anything is stored: a
		// Vm placed nowhere is not a Vm the client asked for. Reading it here,
		// under the lock, is also what makes deleteSubnet's guard hold: the two
		// cannot interleave, so a Subnet either still exists for this batch or
		// is already gone for it.
		subnetSubregion := ""
		if req.SubnetID != "" {
			place, err := p.placeInSubnet(req.SubnetID)
			if err != nil {
				return created, err
			}
			// A Vm lives where its Subnet lives. A Placement naming another
			// zone is refused rather than stored: answering 200 while keeping
			// one of two contradicting facts is the lie either read would then
			// tell. (The exact upstream error shape for this is unmeasured; the
			// refusal itself is what must not be traded away.)
			if req.Placement != nil && req.Placement.SubregionName != "" &&
				req.Placement.SubregionName != place.SubregionName {
				return created, fmt.Errorf("%w: the Subnet %s sits in %s, not in %s",
					errPlacementMismatch, req.SubnetID, place.SubregionName, req.Placement.SubregionName)
			}
			place.apply(res)
			subnetSubregion = place.SubregionName
		}
		// The placement the reads will answer, resolved once and stored — the
		// chain #268 demands is request → store → response, never
		// request → constant → response.
		res.Attrs["Placement"] = p.resolvedPlacement(req.Placement, subnetSubregion)
		if req.KeypairName != "" {
			res.Attrs["KeypairName"] = req.KeypairName
		}
		if req.UserData != "" {
			res.Attrs["UserData"] = req.UserData
		}
		if len(req.SecurityGroupIDs) > 0 {
			res.Attrs["SecurityGroupIds"] = req.SecurityGroupIDs
		}
		p.env.Store.Put(res)
		created = append(created, res)
	}
	return created, nil
}

// validVmFields checks what both CreateVms and UpdateVm must check, and answers
// the client itself so the two cannot drift.
//
// They had drifted: UpdateVm took a 600 KiB user data the create refuses at 500,
// and a KeypairName no keypair answers to. The second is the worse of the two —
// at the next StartVms, authorizedKeys returns nothing, the machine boots with
// no key, nobody can log in, and the API goes on stating that a keypair is
// attached.
//
// TestUpdateVmValidatesWhatCreateValidates fails without this.
func (p *Pack) validVmFields(w http.ResponseWriter, keypair, userData string) bool {
	if keypair != "" && !p.keypairExists(keypair) {
		p.notFound(w, "keypair", keypair)
		return false
	}
	// The API caps user data at 500 KiB. Accepting more would let through a
	// script the real one refuses.
	if len(userData) > cloudinit.MaxUserData {
		p.badRequest(w, "UserData is limited to 500 kibibytes")
		return false
	}
	return true
}

// blockDeviceMappingsOf lists a Vm's device mappings from the volumes linked
// to it — the link lives on the volume (linkVolume), so this is the other side
// of the same single fact. The shape is BlockDeviceMappingCreated: DeviceName
// beside a Bsu naming the volume, its link date and state.
func (p *Pack) blockDeviceMappingsOf(vmID string) []any {
	out := make([]any, 0)
	for _, vol := range p.env.Store.List(kindVolume, resource.Tenant{Provider: Name}) {
		if stringOf(vol.Attrs["LinkedVmId"]) != vmID {
			continue
		}
		out = append(out, map[string]any{
			"DeviceName": orDefault(stringOf(vol.Attrs["DeviceName"]), defaultRootDevice),
			"Bsu": map[string]any{
				"DeleteOnVmDeletion": false,
				"LinkDate":           vol.Updated.Format(time.RFC3339),
				"State":              "attached",
				"VolumeId":           vol.ID,
			},
		})
	}
	return out
}

// readAdminPassword answers the call the Terraform provider makes on every Vm
// it reads back, Linux included.
//
// It is a Windows call: the password is what the guest generated at first boot,
// and a Linux instance has none. The provider asks anyway, so a 404 here failed
// `terraform apply` on the first machine — measured, on a fixture that creates a
// Net, a Subnet and a Vm: the Net and the Subnet landed, the Vm died on this.
//
// The answer is an empty password, never an invented one. A generated string
// would be a credential a client could try to use, and it would work nowhere.
//
// TestReadAdminPasswordAnswersEmpty fails without this.
func (p *Pack) readAdminPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VMID string `json:"VmId"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.VMID == "" {
		p.badRequest(w, "VmId is required")
		return
	}
	if _, found := p.env.Store.Get(Name, kindVM, req.VMID); !found {
		p.notFound(w, "Vm", req.VMID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"VmId":            req.VMID,
		"AdminPassword":   "",
		"ResponseContext": p.context(),
	})
}

func (p *Pack) updateVm(w http.ResponseWriter, r *http.Request) {
	var req updateVMRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if !p.validVmFields(w, req.KeypairName, req.UserData) {
		return
	}
	if !p.validVmOptions(w, req.vmOptionsRequest) {
		return
	}

	// Serialised on the target, like transitionOne and deleteVms. Without it,
	// Commit replaces State, Runtime and Attrs wholesale over whatever a
	// concurrent start had just written: an audit watched UpdateVm answer 200
	// with a UserData the store did not hold, and Terraform would re-propose
	// that change on every plan. In the other order it is a stopped state
	// overwriting a running one whose container is alive.
	//
	// TestUpdateVmAndStartVmsDoNotOverwriteEachOther fails without this.
	unlock := p.binding().Serialise(req.VMID)
	defer unlock()

	if _, ok := p.env.Store.Get(Name, kindVM, req.VMID); !ok {
		p.notFound(w, "VM", req.VMID)
		return
	}
	if len(req.SecurityGroupIDs) > 0 && !p.checkVMSecurityGroups(w, req.SecurityGroupIDs) {
		return
	}

	// The hold above orders this against the lifecycle paths; the store lock
	// below is what keeps a concurrent CreateTags on the same Vm intact. The
	// two are not redundant: the hold cannot cover a writer that does not take
	// it, and the wholesale Commit this used to end with erased exactly such a
	// writer's acknowledged tag (#295).
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindVM, req.VMID, func(stored *resource.Resource) error {
		// Outscale refuses to retype a running machine, and so does every other
		// cloud: the guest would have to be rebuilt underneath itself.
		if req.VMType != "" && req.VMType != stored.Attrs["VmType"] {
			if stored.State == stateRunning {
				return errVmRunning
			}
			stored.Attrs["VmType"] = req.VMType
		}
		if len(req.SecurityGroupIDs) > 0 {
			stored.Attrs["SecurityGroupIds"] = req.SecurityGroupIDs
		}
		if req.KeypairName != "" {
			stored.Attrs["KeypairName"] = req.KeypairName
		}
		if req.UserData != "" {
			stored.Attrs["UserData"] = req.UserData
		}
		// TestDeletionProtectionCanBeClearedByAnUpdate fails without this.
		if req.DeletionProtection != nil {
			stored.Attrs["DeletionProtection"] = *req.DeletionProtection
		}
		if req.IsSourceDestChecked != nil {
			stored.Attrs[attrSourceDestChecked] = *req.IsSourceDestChecked
		}
		// After the VmType block above, so a retype's performance flag wins over
		// whatever was stored — upstream's own precedence (vmoptions.go).
		storeVmOptions(stored, req.vmOptionsRequest, stringOf(stored.Attrs["VmType"]))
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	switch {
	case errors.Is(err, errVmRunning):
		p.conflict(w, "the VM "+req.VMID+" must be stopped before its type can change")
		return
	case err != nil:
		p.notFound(w, "Vm", req.VMID)
		return
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Vm":              p.vmView(updated),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) startVms(w http.ResponseWriter, r *http.Request) {
	p.transition(w, r, func(res *resource.Resource) string {
		if res.State == stateRunning {
			return stateRunning
		}
		p.powerOn(r.Context(), res)
		return res.State
	})
}

// stopVms honours ForceStop by reading it and doing the same thing.
//
// Upstream, the flag is the difference between asking the guest to shut down and
// cutting its power. Here there is no guest that can refuse: a container or a VM
// is stopped by the runtime either way, and the state reached is identical. So
// the field is declared and read rather than left out — an undeclared field is
// one the unread-field report cannot see, and this one is sent by the Terraform
// provider on every stop.
//
// What would be dishonest is claiming a graceful shutdown happened. Nothing here
// claims it: the answer carries the state the runtime produced.
func (p *Pack) stopVms(w http.ResponseWriter, r *http.Request) {
	p.transition(w, r, func(res *resource.Resource) string {
		if res.State == stateStopped {
			return stateStopped
		}
		p.powerOff(r.Context(), res)
		return res.State
	})
}

// transition runs one state change over a list of VmIds and answers with the
// VmStateInfo array the SDK expects: the previous state beside the new one, per
// machine, which is how a client tells "already stopped" from "stopping now".
func (p *Pack) transition(w http.ResponseWriter, r *http.Request, change func(*resource.Resource) string) {
	var req vmIDsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if len(req.VMIDs) == 0 {
		p.badRequest(w, "VmIds is required")
		return
	}

	// Every identifier is resolved before anything changes: a list with one bad
	// id must not leave half the machines transitioned.
	targets := make([]*resource.Resource, 0, len(req.VMIDs))
	for _, id := range req.VMIDs {
		res, ok := p.env.Store.Get(Name, kindVM, id)
		if !ok {
			p.notFound(w, "VM", id)
			return
		}
		targets = append(targets, res)
	}

	out := make([]map[string]any, 0, len(targets))
	for _, resolved := range targets {
		row, ok := p.transitionOne(resolved.ID, change)
		if !ok {
			continue
		}
		out = append(out, row)
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Vms":             out,
		"ResponseContext": p.context(),
	})
}

// transitionOne applies one state change to one VM, holding that target for the
// whole read, run and write.
//
// The mechanics are machine.Binding.Transition, shared with the Exoscale
// pack's transitionInstance rather than written a second time: the lock is
// what makes the read worth anything, the runtime call runs outside the store
// lock, and the write-back is conditional so a delete racing this loop wins
// instead of being undone. The resource Transition reads is re-read under the
// lock rather than reused from the resolution loop, because that copy was
// taken before it: deciding "already running" from a stale state is how the
// short-circuit above stops short-circuiting.
//
// What stays here is the shape a client observes: the VmStateInfo row with the
// previous state beside the new one, and a target deleted mid-transition
// silently dropped from the answer.
func (p *Pack) transitionOne(id string, change func(*resource.Resource) string) (map[string]any, bool) {
	var previous, current string
	err := p.binding().Transition(p.env.Store, p.env.Now, kindVM, id, func(res *resource.Resource) {
		previous = res.State
		current = change(res)
	})
	if err != nil {
		// Missing, or deleted while its machine was transitioning. Not an
		// error: the caller asked for a state this resource no longer has.
		return nil, false
	}
	return map[string]any{
		"VmId":          id,
		"PreviousState": previous,
		"CurrentState":  current,
	}, true
}

// rebootVms answers with the envelope alone: RebootVmsResponse carries no Vms
// field, so reporting one would be a shape no client asked for.
func (p *Pack) rebootVms(w http.ResponseWriter, r *http.Request) {
	var req vmIDsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	for _, id := range req.VMIDs {
		res, ok := p.env.Store.Get(Name, kindVM, id)
		if !ok {
			p.notFound(w, "VM", id)
			return
		}
		if res.State != stateRunning {
			p.conflict(w, "the VM "+id+" is not running")
			return
		}
	}
	for _, id := range req.VMIDs {
		// A reboot is a stop then a start, which is the longest window of the
		// three: another action landing between them would act on a VM that has
		// no machine yet and write its own state over this one's.
		p.transitionOne(id, func(res *resource.Resource) string {
			p.powerOff(r.Context(), res)
			p.powerOn(r.Context(), res)
			return res.State
		})
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteVms(w http.ResponseWriter, r *http.Request) {
	var req vmIDsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}

	targets := make([]*resource.Resource, 0, len(req.VMIDs))
	for _, id := range req.VMIDs {
		res, ok := p.env.Store.Get(Name, kindVM, id)
		if !ok {
			p.notFound(w, "VM", id)
			return
		}
		// Refused before anything is destroyed, and for the whole batch: a
		// delete of four machines where the third is protected must not leave
		// two gone. The API refuses the call, not the machine.
		if protected, _ := res.Attrs["DeletionProtection"].(bool); protected {
			p.conflict(w, "the Vm "+res.ID+" is protected against deletion")
			return
		}
		targets = append(targets, res)
	}

	deleted := make([]map[string]any, 0, len(targets))
	for _, res := range targets {
		// Held for the destroy and the delete together: a StartVms landing
		// between them leaves a container running with nothing left in the store
		// to describe it, and a machine the control plane does not describe is a
		// machine nobody thinks to stop.
		func() {
			unlock := p.binding().Serialise(res.ID)
			defer unlock()

			// The base of the write-back: the terminate owns the state, and a
			// tag acknowledged while the machine was being destroyed survives
			// it (#295).
			base := res.Clone()
			previous := res.State
			// Terminating releases the machine's public address, which is
			// upstream's own behaviour: the address stays allocated and stops
			// naming a machine that no longer exists. It must precede the
			// destroy, because on OVN the uplink route outlives the machine.
			p.releaseVmPublicIPs(r.Context(), res)
			p.removeMachine(r.Context(), res)
			// Terminated, not removed. The machine is destroyed, and the record
			// stays readable: the Terraform provider answers DeleteVms by
			// polling ReadVms until the Vm reports "terminated", and a Vm that
			// vanished makes it read an empty list — measured, it crashes the
			// plugin outright ("Plugin did not respond") on every destroy. The
			// real API keeps a terminated Vm visible for the same reason: the
			// state a client waits for has to be observable.
			//
			// TestATerminatedVmStaysReadable fails without this.
			res.State = stateTerminated
			res.Attrs["State"] = stateTerminated
			if !p.env.Store.Commit(base, res, p.env.Now()) {
				// Deleted underneath: nothing left to mark.
				return
			}
			// A secondary interface with DeleteOnVmDeletion false survives the
			// machine but detaches from it, which is what the real API does and
			// what stops a NIC from naming a terminated Vm for ever. The primary
			// is derived and needs nothing.
			p.detachNicsOf(res.ID)
			// And its volumes, for the same reason and by the same rule: an
			// exclusive resource has one live owner, and the owner just died.
			p.detachVolumesOf(res.ID)
			deleted = append(deleted, map[string]any{
				"VmId":          res.ID,
				"CurrentState":  stateTerminated,
				"PreviousState": previous,
			})
		}()
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Vms":             deleted,
		"ResponseContext": p.context(),
	})
}

// Gone reports a resource this pack keeps in the store although its API says
// it no longer exists. The terminated Vm is the one case: the record stays
// readable because the Terraform provider polls ReadVms until it observes
// "terminated" (TestATerminatedVmStaysReadable), but the machine is destroyed
// and holds nothing — upstream releases the private address at termination.
//
// This is the pack's word on liveness, in the pack because the vocabulary is
// the provider's (rule 5 keeps it out of the core). Two consumers must read
// the same answer or the instrument convicts the innocent: the barrage sweep
// takes it as its predicate, and subnetAllocator skips exactly what it names,
// so what the invariant excuses and what the pool reuses cannot disagree.
func Gone(res *resource.Resource) bool {
	return res.Kind == kindVM && res.State == stateTerminated
}

// errPlacementMismatch is what a create gets for a Placement naming a zone its
// own Subnet does not sit in.
var errPlacementMismatch = errors.New("the Placement contradicts the Subnet")

// errVmRunning is the retype refusal, answered from inside the store lock
// where the state cannot move underneath the check (#295).
var errVmRunning = errors.New("the VM is running")

// resolvedPlacement is the placement a create stores: what the client asked,
// else the Subnet's own zone, else the default — in that order, because the
// first two are facts the client can read back and the third is the emulator's
// only honest answer when nobody said anything.
//
// TestAVmCreatedInANamedSubregionReadsBackInIt fails when the request's half
// stops being read; TestAVmInheritsItsSubnetsSubregion when the Subnet's half
// does.
func (p *Pack) resolvedPlacement(requested *vmPlacement, subnetSubregion string) map[string]any {
	subregion := orDefault(subnetSubregion, p.defaultSubregion)
	tenancy := defaultTenancy
	if requested != nil {
		subregion = orDefault(requested.SubregionName, subregion)
		tenancy = orDefault(requested.Tenancy, tenancy)
	}
	return map[string]any{
		"SubregionName": subregion,
		"Tenancy":       tenancy,
	}
}

// vmPlacementView renders a Vm's Placement from what its create stored. The
// fallback exists for records that predate the stored field — a restored
// snapshot from an older emulator — because the Vm schema declares Placement
// on every machine (osc-sdk-go, client.gen.go:10576) and a client reads it
// unconditionally.
func (p *Pack) vmPlacementView(res *resource.Resource) map[string]any {
	if placement, ok := res.Attrs["Placement"].(map[string]any); ok {
		return placement
	}
	return p.resolvedPlacement(nil, "")
}

// vmSubregion is the zone a Vm's own reads answer, shared by every door that
// publishes it — the view, the state view (readVmsState) and the filters — so
// two doors cannot disagree about where a machine sits.
func (p *Pack) vmSubregion(res *resource.Resource) string {
	return stringOf(p.vmPlacementView(res)["SubregionName"])
}

// vmView renders a Vm. Only fields the pack actually knows are emitted: a
// PrivateIp of "" would tell a client the machine has no address, where an
// absent field tells it the emulator does not model one.
func (p *Pack) vmView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+4)
	for k, v := range res.Attrs {
		// The stored id list stays off the wire: the Vm schema publishes
		// SecurityGroups as {id, name} pairs, resolved below on every read so a
		// deleted or renamed group cannot leave a stale copy here.
		if k == "SecurityGroupIds" {
			continue
		}
		out[k] = v
	}
	if groups := p.effectiveSecurityGroups(res); groups != nil {
		out["SecurityGroups"] = groups
	}
	out["VmId"] = res.ID
	out["State"] = res.State
	// The two keys a real machine always carries and this pack wrote only when
	// it had something to put in them (#378). Both were measured against a real
	// account on 2026-08-21: a machine created with no user data and no tags
	// answers `"UserData": ""` and `"Tags": []`, and a client that iterates a
	// list or reads a string gets nothing here where the cloud gives it an
	// empty one.
	//
	// This is not the rule PrivateIp follows two lines down, and the difference
	// is what the cloud does rather than a preference: an absent PrivateIp says
	// this emulator models no address, while an absent UserData says nothing at
	// all — the cloud has the key on every machine. Every other kind in this
	// pack already writes `"Tags": []any{}` at create time; the Vm was the one
	// that did not.
	out["UserData"] = stringOf(res.Attrs["UserData"])
	out["Tags"] = tagsOrEmpty(res)
	// The field whose absence sends the Terraform provider to
	// ReadAdminPassword on every machine: it reads ProductCodes to tell a
	// Windows image from a Linux one, and an absent list reads as "unknown".
	// "0001" is Outscale's own code for a Linux instance.
	out["ProductCodes"] = []any{linuxProductCode}
	out["CreationDate"] = res.Created.Format(time.RFC3339)
	// Outscale's Vm schema declares PrivateIp and a real one carries a value,
	// so the address the machine answers on is published here.
	if address := p.addressOf(res); address != "" {
		out["PrivateIp"] = address
	}
	// A machine carrying a linked public address answers with it, which the
	// Terraform provider reads back into public_ip. Derived from the address
	// rather than stored on the machine: the link is one fact and it lives on
	// the address, so the two cannot disagree. Omitted when there is none —
	// absent is what "no public address" looks like.
	if public := p.publicIPOf(res.ID); public != "" {
		out["PublicIp"] = public
		out["PublicDnsName"] = p.publicDNSName(public)
	}

	// What follows was found missing by comparing a real account's ReadVms to
	// this one's, per operation, through `feint transcript --against` — twenty
	// fields the real cloud returns on every machine and the emulator returned
	// on none. No contract could see it: Outscale's Vm schema declares no
	// required field, so an omission is never a violation. No unit test saw it
	// either, because a test asserts what somebody thought to assert.
	//
	// The values are fixed and describe the platform being emulated rather than
	// the local runtime — catalog.go makes the same trade for the same reason,
	// and docs/limits.md records that a machine here is an Incus container
	// whatever Hypervisor says. What matters to a client is that the field is
	// there, well-formed, and stable between two reads.
	out["Architecture"] = "x86_64"
	out["Hypervisor"] = "xen"
	out["LaunchNumber"] = 0
	out["RootDeviceName"] = "/dev/sda1"
	out["RootDeviceType"] = "ebs"
	// Upstream's own constant, not this pack's: "This parameter is not
	// available. It is present in our API for the sake of historical
	// compatibility with AWS" (client.gen.go:3029), so false is what the real
	// cloud answers whatever a create asked.
	out["BsuOptimized"] = false
	out["StateReason"] = ""
	// What the create (and UpdateVm, where upstream declares it) stored,
	// never a constant — BootMode, Performance and
	// VmInitiatedShutdownBehavior were #276: medium/restart/legacy asked,
	// high/stop/uefi answered on the same create's 200, and a Terraform
	// stack setting any of them re-planned for ever. The neighbours carried
	// the same defect one field over. vmoptions.go holds the readers, the
	// defaults, and the SDK lines. TestAVmReadsBackItsOwnOptions fails
	// without this.
	out["BootMode"] = vmBootMode(res)
	out["Performance"] = vmPerformance(res)
	out["VmInitiatedShutdownBehavior"] = vmShutdownBehavior(res)
	out["ShutdownBehaviorConfiguration"] = vmShutdownConfiguration(res)
	out["ActionsOnNextBoot"] = vmActionsOnNextBoot(res)
	out["TpmEnabled"] = vmTpmEnabled(res)
	out["IsSourceDestChecked"] = vmSourceDestChecked(res)
	// What the create stored, never a constant. The constant here was #268: a
	// machine created in eu-west-2b read back in eu-west-2a, stable and wrong —
	// #250 had already named the pattern, "a constant in a view is a claim
	// nobody checks". TestAVmCreatedInANamedSubregionReadsBackInIt fails
	// without this.
	out["Placement"] = p.vmPlacementView(res)
	// Derived from the machine's own id so it cannot move between two reads:
	// anything Terraform stores has to be stable or it plans a change for ever.
	out["ReservationId"] = "r-" + hexOf(strings.TrimPrefix(res.ID, "i-")+res.ID, idLen)
	// From the same address the view publishes as PrivateIp, wherever it came
	// from — the runtime when a machine backs the Vm, the stored plan address
	// otherwise. Reading the runtime alone left PrivateDnsName absent on every
	// machines-off run while PrivateIp was served, which the field gate (#88)
	// measured against the real cloud: it writes the name on every Vm.
	dnsAddress := p.addressOf(res)
	if dnsAddress == "" {
		dnsAddress = stringOf(res.Attrs["PrivateIp"])
	}
	if dns := p.privateDNSName(dnsAddress); dns != "" {
		out["PrivateDnsName"] = dns
	}
	// The interfaces, in the Light shape the Vm schema declares. The provider
	// reads them, and this was the largest single gap the diff found.
	if nics := p.nicsOfVM(res); len(nics) > 0 {
		out["Nics"] = nics
	}
	// The device mappings, on every Vm — the real cloud writes the key on
	// each machine (measured in shapes/outscale.json, held by the field gate,
	// #88). Derived from the volumes actually linked to this Vm, never
	// invented: a first version wrote a fictional root VolumeId here, and the
	// Terraform provider promptly resolved it — "volume vol-rooti149 not
	// found" killed the whole suite. A mapping must name a volume ReadVolumes
	// can answer for, and a Vm with no linked volume has an empty list, which
	// this emulator's machines truthfully have (they model no root volume,
	// docs/limits.md).
	out["BlockDeviceMappings"] = p.blockDeviceMappingsOf(res.ID)
	return out
}

// Owns tells the shared orphan sweep which resources of this pack belong to
// another, and to which one.
//
// The vocabulary is the provider's — LinkedVmId on a volume, LinkVmId on a NIC —
// so it is declared here and the invariant lives in the core, the same split as
// Gone above. What it catches is the defect #215 named: a volume that outlived
// its Vm goes on refusing to attach anywhere else, and no client call frees it.
func Owns(res *resource.Resource) (kind, id string, ok bool) {
	switch res.Kind {
	case kindVolume:
		if vmID := stringOf(res.Attrs["LinkedVmId"]); vmID != "" {
			return kindVM, vmID, true
		}
	case kindNic:
		if vmID := stringOf(res.Attrs["LinkVmId"]); vmID != "" {
			return kindVM, vmID, true
		}
	}
	return "", "", false
}
