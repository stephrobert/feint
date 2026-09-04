package outscale

import (
	"errors"
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Flexible GPU, served since #619.
//
// It was declined with the rest of the products nobody had triaged. What #619
// adds is that a published training course spends a whole page on it
// (fondations/fgpu.mdx), so a reader following that page against this emulator
// met six 404s in a row.
//
// It is the shape of a volume rather than of a machine: an inventory, a create,
// a delete, and a link/unlink pair. Nothing here forwards a packet or executes
// an instruction, and an fGPU does neither on the control plane either: what a
// client can observe of one is which model it is, where it is, and whether it is
// attached. All three are facts this emulator can hold truthfully.
//
// The whole family or none of it. Serving the catalogue alone would publish a
// menu whose items no create takes, which is the ListVolumesTypes argument, and
// #658 landed on that line the day before this.

const kindFlexibleGpu = "flexiblegpu"

// The four states the API description declares for an fGPU:
// `allocated | attaching | attached | detaching`.
//
// Two of them are transient and this emulator settles at once, the way it
// settles every other lifecycle: attaching and detaching describe a hypervisor
// moving a device, and nothing here moves one. The two that remain are the two
// a client can act on, and they differ, so a client waiting on a link has
// something to observe (which is the property #654 is about).
const (
	gpuStateAllocated = "allocated"
	gpuStateAttached  = "attached"
)

// publishedGPUCatalog is what a real account answers for ReadFlexibleGpuCatalog,
// read on 2026-09-04 through `octl iaas api ReadFlexibleGpuCatalog`. Eleven
// models, field for field: Generations, MaxCpu, MaxRam, ModelName, VRam.
//
// Verbatim rather than a plausible subset, for the reason catalog_servers.json
// is verbatim: a client sizing a machine against this table reads a
// compatibility claim, and an invented row is a claim nothing published.
var publishedGPUCatalog = []struct {
	ModelName   string
	Generations []string
	MaxCpu      int
	MaxRam      int
	VRam        int
}{
	{"nvidia-l40", []string{"v7"}, 35, 256, 48000},
	{"nvidia-p100", []string{"v5"}, 80, 512, 16000},
	{"nvidia-m60", []string{"v3", "v4"}, 80, 512, 16000},
	{"nvidia-a10", []string{"v5", "v6"}, 35, 250, 24000},
	{"nvidia-p6", []string{"v5"}, 80, 512, 16000},
	{"nvidia-a100-80", []string{"v6", "v7"}, 35, 256, 80000},
	{"nvidia-h200", []string{"v7", "v104"}, 130, 2300, 141000},
	{"nvidia-k2", []string{"v3", "v4"}, 80, 512, 4096},
	{"nvidia-a100", []string{"v5", "v6"}, 35, 250, 40000},
	{"nvidia-h100", []string{"v7"}, 64, 512, 80000},
	{"nvidia-v100", []string{"v5"}, 35, 250, 16000},
}

// gpuModelOffered reports whether the catalogue carries this model, and answers
// the generations it is compatible with.
func gpuModelOffered(name string) ([]string, bool) {
	for _, offered := range publishedGPUCatalog {
		if offered.ModelName == name {
			return offered.Generations, true
		}
	}
	return nil, false
}

// flexibleGpuFilters is what a stored fGPU can answer, from FiltersFlexibleGpu.
// Exactly what FiltersFlexibleGpu declares, and no more: DeleteOnVmDeletion is
// singular here where every other boolean filter of this API is plural, and
// Tags is the only one of the three tag filters this family carries. Both were
// got wrong on the first pass and both were caught by
// TestEveryDeclaredFilterKindIsTheOneTheContractDeclares before any client
// could meet them.
var flexibleGpuFilters = joinFilters(
	stringFilters("FlexibleGpuIds", "Generations", "ModelNames", "States", "SubregionNames", "VmIds", "Tags"),
	boolFilters("DeleteOnVmDeletion"),
)

// readFlexibleGpuCatalog answers the public catalogue.
//
// TestTheGpuCatalogueIsTheOneMeasured fails without this.
func (p *Pack) readFlexibleGpuCatalog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DryRun *bool `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(publishedGPUCatalog))
	for _, offered := range publishedGPUCatalog {
		generations := make([]any, 0, len(offered.Generations))
		for _, generation := range offered.Generations {
			generations = append(generations, generation)
		}
		out = append(out, map[string]any{
			"ModelName":   offered.ModelName,
			"Generations": generations,
			"MaxCpu":      offered.MaxCpu,
			"MaxRam":      offered.MaxRam,
			"VRam":        offered.VRam,
		})
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"FlexibleGpuCatalog": out,
		"ResponseContext":    p.context(),
	})
}

// createFlexibleGpu allocates one.
//
// The model is checked against the catalogue, and that is a refusal this
// emulator can make truthfully: the catalogue is a reading of the real one, so a
// model outside it is a model the real cloud does not offer either. The
// generation is checked against the model's own list for the same reason.
//
// TestCreatingAnFGpuChecksTheModelAgainstTheCatalogue fails without this.
func (p *Pack) createFlexibleGpu(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ModelName          string `json:"ModelName"`
		Generation         string `json:"Generation"`
		SubregionName      string `json:"SubregionName"`
		DeleteOnVmDeletion *bool  `json:"DeleteOnVmDeletion"`
		DryRun             *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.ModelName == "" {
		p.badRequest(w, "ModelName is required")
		return
	}
	if req.SubregionName == "" {
		p.badRequest(w, "SubregionName is required")
		return
	}
	generations, offered := gpuModelOffered(req.ModelName)
	if !offered {
		p.badRequest(w, "the model "+req.ModelName+" is not in the fGPU catalogue")
		return
	}
	if req.Generation != "" {
		known := false
		for _, generation := range generations {
			if generation == req.Generation {
				known = true
			}
		}
		if !known {
			p.badRequest(w, "the model "+req.ModelName+" is not compatible with generation "+req.Generation)
			return
		}
	}

	deleteOnVmDeletion := false
	if req.DeleteOnVmDeletion != nil {
		deleteOnVmDeletion = *req.DeleteOnVmDeletion
	}
	res := resource.New(newID("fgpu", p.env.NewID()), kindFlexibleGpu,
		resource.Tenant{Provider: Name}, gpuStateAllocated, p.env.Now())
	res.Attrs = map[string]any{
		"ModelName":          req.ModelName,
		"Generation":         req.Generation,
		"SubregionName":      req.SubregionName,
		"DeleteOnVmDeletion": deleteOnVmDeletion,
		"VmId":               "",
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"FlexibleGpu":     p.flexibleGpuView(res),
		"ResponseContext": p.context(),
	})
}

// readFlexibleGpus lists them, filtered.
func (p *Pack) readFlexibleGpus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters filterSet `json:"Filters"`
		DryRun  *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseFilters(w, req.Filters, flexibleGpuFilters) {
		return
	}
	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindFlexibleGpu, resource.Tenant{Provider: Name}) {
		deleteOnVmDeletion, _ := res.Attrs["DeleteOnVmDeletion"].(bool)
		if !matchesStrings(req.Filters, "FlexibleGpuIds", res.ID) ||
			!matchesStrings(req.Filters, "Generations", stringOf(res.Attrs["Generation"])) ||
			!matchesStrings(req.Filters, "ModelNames", stringOf(res.Attrs["ModelName"])) ||
			!matchesStrings(req.Filters, "States", res.State) ||
			!matchesStrings(req.Filters, "SubregionNames", stringOf(res.Attrs["SubregionName"])) ||
			!matchesStrings(req.Filters, "VmIds", stringOf(res.Attrs["VmId"])) ||
			!matchesBool(req.Filters, "DeleteOnVmDeletion", deleteOnVmDeletion) ||
			!matchesTags(req.Filters, res) {
			continue
		}
		out = append(out, p.flexibleGpuView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"FlexibleGpus":    out,
		"ResponseContext": p.context(),
	})
}

// linkFlexibleGpu attaches one to a machine, and refuses one already attached.
//
// The refusal is the shape #621 settled for volumes, for the same reason: an
// emulator that says yes where the cloud says no teaches a client something the
// cloud will punish. Decided inside the store lock, where the state it judges
// cannot move while it judges it.
//
// TestLinkingAnFGpuTwiceIsRefused fails without this.
func (p *Pack) linkFlexibleGpu(w http.ResponseWriter, r *http.Request) {
	p.moveFlexibleGpu(w, r, true)
}

// unlinkFlexibleGpu detaches one, and refuses one attached to nothing.
//
// TestLinkingAnFGpuTwiceIsRefused fails without this: it is the test that
// drives the whole attach, detach and delete sequence, refusals included.
func (p *Pack) unlinkFlexibleGpu(w http.ResponseWriter, r *http.Request) {
	p.moveFlexibleGpu(w, r, false)
}

func (p *Pack) moveFlexibleGpu(w http.ResponseWriter, r *http.Request, attach bool) {
	var req struct {
		FlexibleGpuID string `json:"FlexibleGpuId"`
		VMID          string `json:"VmId"`
		DryRun        *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.FlexibleGpuID == "" {
		p.badRequest(w, "FlexibleGpuId is required")
		return
	}
	if attach {
		if req.VMID == "" {
			p.badRequest(w, "VmId is required")
			return
		}
		if _, found := p.env.Store.Get(Name, kindVM, req.VMID); !found {
			p.notFound(w, "Vm", req.VMID)
			return
		}
	}

	var held string
	err := p.env.Store.Update(Name, kindFlexibleGpu, req.FlexibleGpuID, func(stored *resource.Resource) error {
		attachedTo, _ := stored.Attrs["VmId"].(string)
		if attach && attachedTo != "" {
			held = attachedTo
			return errGpuHeld
		}
		if !attach && attachedTo == "" {
			return errGpuFree
		}
		if attach {
			stored.Attrs["VmId"] = req.VMID
			stored.State = gpuStateAttached
		} else {
			stored.Attrs["VmId"] = ""
			stored.State = gpuStateAllocated
		}
		stored.Updated = p.env.Now()
		return nil
	})
	switch {
	case errors.Is(err, errGpuHeld):
		p.conflict(w, "the fGPU "+req.FlexibleGpuID+" is already attached to "+held)
		return
	case errors.Is(err, errGpuFree):
		p.conflict(w, "the fGPU "+req.FlexibleGpuID+" is not attached to any Vm")
		return
	case err != nil:
		p.notFound(w, "flexible gpu", req.FlexibleGpuID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// deleteFlexibleGpu removes one, and refuses one still attached.
//
// The same rule the volume delete follows: a client that deletes what is still
// in use here would find the object gone and the machine still claiming it.
//
// TestLinkingAnFGpuTwiceIsRefused fails without this.
func (p *Pack) deleteFlexibleGpu(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FlexibleGpuID string `json:"FlexibleGpuId"`
		DryRun        *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindFlexibleGpu, req.FlexibleGpuID)
	if !found {
		p.notFound(w, "flexible gpu", req.FlexibleGpuID)
		return
	}
	if attachedTo, _ := res.Attrs["VmId"].(string); attachedTo != "" {
		p.conflict(w, "the fGPU "+req.FlexibleGpuID+" is still attached to "+attachedTo)
		return
	}
	p.env.Store.Delete(Name, kindFlexibleGpu, req.FlexibleGpuID)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// updateFlexibleGpu changes the one field UpdateFlexibleGpuRequest carries.
//
// Served with the rest of the family rather than left declined beside it: the
// field it changes is one this pack already stores and already answers, so a
// refusal here would be a hole in the middle of a served product rather than a
// boundary of it.
//
// TestUpdatingAnFGpuChangesTheOneFieldItOwns fails without this.
func (p *Pack) updateFlexibleGpu(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FlexibleGpuID      string `json:"FlexibleGpuId"`
		DeleteOnVmDeletion *bool  `json:"DeleteOnVmDeletion"`
		DryRun             *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.FlexibleGpuID == "" {
		p.badRequest(w, "FlexibleGpuId is required")
		return
	}
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindFlexibleGpu, req.FlexibleGpuID, func(stored *resource.Resource) error {
		if req.DeleteOnVmDeletion != nil {
			stored.Attrs["DeleteOnVmDeletion"] = *req.DeleteOnVmDeletion
		}
		stored.Updated = p.env.Now()
		updated = stored.Clone()
		return nil
	})
	if err != nil {
		p.notFound(w, "flexible gpu", req.FlexibleGpuID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"FlexibleGpu":     p.flexibleGpuView(updated),
		"ResponseContext": p.context(),
	})
}

// flexibleGpuView renders the FlexibleGpu schema: eight fields, and Tags among
// them, which is why the tag filters reach this list like every other.
func (p *Pack) flexibleGpuView(res *resource.Resource) map[string]any {
	return map[string]any{
		"FlexibleGpuId":      res.ID,
		"ModelName":          stringOf(res.Attrs["ModelName"]),
		"Generation":         stringOf(res.Attrs["Generation"]),
		"SubregionName":      stringOf(res.Attrs["SubregionName"]),
		"DeleteOnVmDeletion": res.Attrs["DeleteOnVmDeletion"],
		"State":              res.State,
		"VmId":               stringOf(res.Attrs["VmId"]),
		"Tags":               tagsOrEmpty(res),
	}
}

// The two refusals this family owns, as sentinels so the handler can tell them
// apart from a missing object (#621's shape, one product later).
var (
	errGpuHeld = errors.New("the fGPU is attached to a machine")
	errGpuFree = errors.New("the fGPU is attached to nothing")
)
