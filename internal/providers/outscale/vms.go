package outscale

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/cloudinit"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
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
	// The group filters are what `terraform destroy` asks before removing a
	// security group: which machines still wear it. Without them the destroy
	// fails on the group, after the apply succeeded — so the whole fixture is
	// left standing by a filter nobody had declared.
	"SecurityGroupIds", "SecurityGroupNames",
}

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
		// read a client is making anyway.
		// This read writes: a virtual machine gets its address late, and the
		// refresh publishes it. So it is serialised like every other writer —
		// an audit noted that a read could otherwise lose a concurrent write,
		// which is the same defect as UpdateVm's, through a door nobody thinks
		// of as mutating.
		refreshed := func() bool {
			unlock := p.binding().Serialise(res.ID)
			defer unlock()
			if !p.refreshMachine(r.Context(), res) {
				return true
			}
			// Committed, not Put: this read ran outside the store lock and a
			// delete may have landed since. A false return means it did, and
			// the Vm belongs in nobody's answer.
			return p.env.Store.Commit(res, p.env.Now())
		}()
		if !refreshed {
			continue
		}
		vms = append(vms, p.vmView(res))
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
			p.powerOn(r.Context(), res)
			if !p.env.Store.Commit(res, p.env.Now()) {
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
	p.addresses.Lock()
	defer p.addresses.Unlock()

	created := make([]*resource.Resource, 0, count)
	for range count {
		res := &resource.Resource{
			ID:      newVMID(p.env.NewID()),
			Kind:    kindVM,
			Tenant:  resource.Tenant{Provider: Name},
			State:   stateStopped,
			Created: now,
			Updated: now,
			Attrs: map[string]any{
				"ImageId":              req.ImageID,
				"VmType":               orDefault(req.VMType, defaultVMType),
				"DeletionProtection":   boolOr(req.DeletionProtection, false),
				"NestedVirtualization": boolOr(req.NestedVirtualization, false),
			},
		}
		// The Subnet the client asked for, resolved before anything is stored: a
		// Vm placed nowhere is not a Vm the client asked for. Reading it here,
		// under the lock, is also what makes deleteSubnet's guard hold: the two
		// cannot interleave, so a Subnet either still exists for this batch or
		// is already gone for it.
		if req.SubnetID != "" {
			place, err := p.placeInSubnet(req.SubnetID)
			if err != nil {
				return created, err
			}
			place.apply(res)
		}
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

	res, ok := p.env.Store.Get(Name, kindVM, req.VMID)
	if !ok {
		p.notFound(w, "VM", req.VMID)
		return
	}

	// Outscale refuses to retype a running machine, and so does every other
	// cloud: the guest would have to be rebuilt underneath itself.
	if req.VMType != "" && req.VMType != res.Attrs["VmType"] {
		if res.State == stateRunning {
			p.conflict(w, "the VM "+res.ID+" must be stopped before its type can change")
			return
		}
		res.Attrs["VmType"] = req.VMType
	}
	if len(req.SecurityGroupIDs) > 0 {
		if !p.checkVMSecurityGroups(w, req.SecurityGroupIDs) {
			return
		}
		res.Attrs["SecurityGroupIds"] = req.SecurityGroupIDs
	}
	if req.KeypairName != "" {
		res.Attrs["KeypairName"] = req.KeypairName
	}
	if req.UserData != "" {
		res.Attrs["UserData"] = req.UserData
	}
	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "Vm", res.ID)
		return
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Vm":              p.vmView(res),
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
// The lock is what makes the read worth anything. The runtime call is outside
// the store lock on purpose, and Update only refuses to write back a resource
// that is gone: it does not order two writers. Two concurrent StartVms on one VM
// each launched a container, the second failed on the name the first had taken,
// and the API described as stopped a machine that was running.
//
// The resource is re-read here rather than reused from the resolution loop,
// because that copy was taken before the lock: deciding "already running" from a
// stale state is how the short-circuit above stops short-circuiting.
func (p *Pack) transitionOne(id string, change func(*resource.Resource) string) (map[string]any, bool) {
	unlock := p.binding().Serialise(id)
	defer unlock()

	res, ok := p.env.Store.Get(Name, kindVM, id)
	if !ok {
		return nil, false
	}

	previous := res.State
	// The runtime work runs outside the store lock. Launching a container takes
	// tens of seconds, and holding the write lock for that would queue every
	// other request behind one machine starting.
	current := change(res)

	// The result is applied atomically. Put replaces the whole resource, so it
	// silently discarded anything another request had written in the meantime —
	// a delete racing this loop used to be undone, and the VM came back.
	err := p.env.Store.Update(Name, kindVM, id, func(stored *resource.Resource) error {
		stored.State = res.State
		stored.Runtime = res.Runtime
		stored.Attrs = res.Attrs
		stored.Updated = p.env.Now()
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		// Deleted while its machine was transitioning. Not an error: the caller
		// asked for a state this resource no longer has.
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

			previous := res.State
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
			if !p.env.Store.Commit(res, p.env.Now()) {
				// Deleted underneath: nothing left to mark.
				return
			}
			// A secondary interface with DeleteOnVmDeletion false survives the
			// machine but detaches from it, which is what the real API does and
			// what stops a NIC from naming a terminated Vm for ever. The primary
			// is derived and needs nothing.
			p.detachNicsOf(res.ID)
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
		out["PublicDnsName"] = publicDNSName(public)
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
	out["BootMode"] = "uefi"
	out["Hypervisor"] = "xen"
	out["IsSourceDestChecked"] = true
	out["LaunchNumber"] = 0
	out["Performance"] = "high"
	out["RootDeviceName"] = "/dev/sda1"
	out["RootDeviceType"] = "ebs"
	out["BsuOptimized"] = false
	out["TpmEnabled"] = false
	out["VmInitiatedShutdownBehavior"] = "stop"
	out["StateReason"] = ""
	out["ActionsOnNextBoot"] = map[string]any{"SecureBoot": "none"}
	out["Placement"] = map[string]any{
		"SubregionName": subregionName,
		"Tenancy":       "default",
	}
	// Derived from the machine's own id so it cannot move between two reads:
	// anything Terraform stores has to be stable or it plans a change for ever.
	out["ReservationId"] = "r-" + hexOf(strings.TrimPrefix(res.ID, "i-")+res.ID, idLen)
	if dns := privateDNSName(p.addressOf(res)); dns != "" {
		out["PrivateDnsName"] = dns
	}
	// The interfaces, in the Light shape the Vm schema declares. The provider
	// reads them, and this was the largest single gap the diff found.
	if nics := p.nicsOfVM(res); len(nics) > 0 {
		out["Nics"] = nics
	}
	// ShutdownBehaviorConfiguration is deliberately NOT emitted, and the reason
	// is a finding rather than an omission: the real cloud returns it, and the
	// API description this pack is checked against does not declare it. Serving
	// it would fail the contract gate against Outscale's own document. It is
	// upstream running ahead of its published description — recorded here so
	// the next reader does not "fix" it.
	return out
}
