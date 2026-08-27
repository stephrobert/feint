package exoscale

import (
	"errors"
	"net/http"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// The instance lifecycle, measured against the real API through `feint proxy`
// on 2026-08-10 rather than transcribed from documentation.
//
// Two facts of that recording shape everything here:
//
//   - every mutation answers an Operation, and its reference names the read
//     that follows (command "get-instance", with the link beside it) — never
//     the mutation itself. The live API's start and stop answer a different
//     envelope again; startInstance records why it is not reproduced.
//   - reset refuses a running instance with 400 "The instance needs to be
//     stopped." — quoted verbatim, because the message is part of the surface.
//
// Only running and stopped were ever observed on the wire: the CLI learns the
// in-between from the operation it polls, never from the instance state, so the
// emulator's two-state machine is not a simplification a client can see.

// transitionInstance applies one change to one instance, holding that target
// for the read, the runtime work and the write.
//
// The mechanics — target serialised, existence before, runtime work outside
// the store lock, conditional write-back so a delete landing mid-transition
// wins — are machine.Binding.Transition, shared with the Outscale pack's
// transitionOne rather than written a second time. What stays here is this
// pack's dialect: a missing instance and one deleted mid-transition are the
// same bare-message 404.
// TestTwoLifecycleActionsOnOneInstanceDoNotRace fails without the lock.
func (p *Pack) transitionInstance(w http.ResponseWriter, r *http.Request, change func(*resource.Resource)) (id string, ok bool) {
	id = r.PathValue("id")

	if err := p.binding().Transition(p.env.Store, p.env.Now, kindInstance, id, change); err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return id, false
	}
	return id, true
}

// startInstance powers a stopped instance on.
//
// One measured divergence is deliberately not reproduced here. The live API
// answers start and stop with an Operation carrying `resource: {id, type}` and
// no reference — recorded on 2026-08-10 — while their published OpenAPI
// declares no such field on `operation`, and egoscale's own Operation struct
// cannot decode it. Emitting the recorded shape would fail this emulator's
// contract check for a field no official client can read; the reference
// envelope is what every decoder actually consumes, so it is what both verbs
// answer.
func (p *Pack) startInstance(w http.ResponseWriter, r *http.Request) {
	id, ok := p.transitionInstance(w, r, func(res *resource.Resource) {
		if res.State != runningState {
			p.start(r.Context(), res)
		}
	})
	if !ok {
		return
	}
	p.writeOperation(w, p.operationReferring(nounInstance, id))
}

// stopInstance powers it off and withdraws the address.
func (p *Pack) stopInstance(w http.ResponseWriter, r *http.Request) {
	id, ok := p.transitionInstance(w, r, func(res *resource.Resource) {
		if res.State != stoppedState {
			p.binding().PowerOff(r.Context(), res)
			res.State = stoppedState
		}
	})
	if !ok {
		return
	}
	p.writeOperation(w, p.operationReferring(nounInstance, id))
}

// rebootInstance is a stop then a start, under one hold of the target. The
// sequence is the shared layer's since #547: it was written here, in Outscale,
// and half-written in Scaleway, whose copy had lost its stop.
func (p *Pack) rebootInstance(w http.ResponseWriter, r *http.Request) {
	id, ok := p.transitionInstance(w, r, func(res *resource.Resource) {
		p.reboot(r.Context(), res)
	})
	if !ok {
		return
	}
	p.writeOperation(w, p.operationReferring(nounInstance, id))
}

type resetInstanceRequest struct {
	DiskSize int `json:"disk-size"`
	Template struct {
		ID string `json:"id"`
	} `json:"template"`
}

// resetInstance reinstalls the machine from its template.
//
// The precondition is the measured one: the real API answered 400 with exactly
// this message when the instance was running, and the emulator repeats their
// sentence rather than composing its own.
func (p *Pack) resetInstance(w http.ResponseWriter, r *http.Request) {
	var req resetInstanceRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	refused := false
	id, ok := p.transitionInstance(w, r, func(res *resource.Resource) {
		if res.State != stoppedState {
			refused = true
			return
		}
		// The disk is wiped: the backing machine goes, and the instance stays
		// stopped, holding whatever template and size the reset declared.
		p.binding().Destroy(r.Context(), res)
		if req.Template.ID != "" {
			res.Attrs["template"] = map[string]any{"id": req.Template.ID}
		}
		if req.DiskSize > 0 {
			res.Attrs["disk-size"] = req.DiskSize
		}
	})
	if !ok {
		return
	}
	if refused {
		writeError(w, http.StatusBadRequest, "The instance needs to be stopped.")
		return
	}
	p.writeOperation(w, p.operationReferring(nounInstance, id))
}

type scaleInstanceRequest struct {
	InstanceType struct {
		ID string `json:"id"`
	} `json:"instance-type"`
}

// scaleInstance moves the instance to another type. The client sends the whole
// catalogue entry — the recording shows cpus, memory and zones going past — and
// the identifier is the part that names anything.
func (p *Pack) scaleInstance(w http.ResponseWriter, r *http.Request) {
	var req scaleInstanceRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.InstanceType.ID == "" {
		writeError(w, http.StatusBadRequest, "instance-type is required")
		return
	}
	id, ok := p.transitionInstance(w, r, func(res *resource.Resource) {
		res.Attrs["instance-type"] = map[string]any{"id": req.InstanceType.ID}
	})
	if !ok {
		return
	}
	p.writeOperation(w, p.operationReferring(nounInstance, id))
}

type resizeInstanceDiskRequest struct {
	DiskSize int `json:"disk-size"`
}

func (p *Pack) resizeInstanceDisk(w http.ResponseWriter, r *http.Request) {
	var req resizeInstanceDiskRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DiskSize <= 0 {
		writeError(w, http.StatusBadRequest, "disk-size is required")
		return
	}
	id, ok := p.transitionInstance(w, r, func(res *resource.Resource) {
		res.Attrs["disk-size"] = req.DiskSize
	})
	if !ok {
		return
	}
	p.writeOperation(w, p.operationReferring(nounInstance, id))
}

// addInstanceProtection and its removal keep the flag out of the instance view:
// the measured body carries no protection field, so the state lives in Attrs
// and only the delete path reads it.
func (p *Pack) addInstanceProtection(w http.ResponseWriter, r *http.Request) {
	p.setInstanceProtection(w, r, true)
}

func (p *Pack) removeInstanceProtection(w http.ResponseWriter, r *http.Request) {
	p.setInstanceProtection(w, r, false)
}

func (p *Pack) setInstanceProtection(w http.ResponseWriter, r *http.Request, protected bool) {
	id := r.PathValue("id")
	err := p.env.Store.Update(Name, kindInstance, id, func(stored *resource.Resource) error {
		stored.Attrs[attrProtected] = protected
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounInstance, id))
}

type updateInstanceRequest struct {
	Name                                 *string           `json:"name"`
	Labels                               map[string]string `json:"labels"`
	UserData                             *string           `json:"user-data"`
	PublicIPAssignment                   *string           `json:"public-ip-assignment"`
	ApplicationConsistentSnapshotEnabled *bool             `json:"application-consistent-snapshot-enabled"`
}

// updateInstance applies the fields the client sent and only those: the
// recording shows `exo compute instance update --label` posting {"labels":…}
// and nothing else, so an absent field is one to keep, not one to clear.
func (p *Pack) updateInstance(w http.ResponseWriter, r *http.Request) {
	var req updateInstanceRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name != nil && strings.ContainsAny(*req.Name, "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}
	id := r.PathValue("id")
	err := p.env.Store.Update(Name, kindInstance, id, func(stored *resource.Resource) error {
		if req.Name != nil {
			stored.Attrs["name"] = *req.Name
		}
		if req.Labels != nil {
			stored.Attrs["labels"] = labelsToAttr(req.Labels)
		}
		if req.UserData != nil {
			stored.Attrs["user-data"] = *req.UserData
		}
		if req.PublicIPAssignment != nil {
			stored.Attrs["public-ip-assignment"] = *req.PublicIPAssignment
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p.writeOperation(w, p.operationReferring(nounInstance, id))
}

// labelsToAttr stores labels as the map[string]any every attrs consumer
// (snapshot round-trip included) expects.
func labelsToAttr(labels map[string]string) map[string]any {
	out := make(map[string]any, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}
