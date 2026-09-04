package outscale_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// gpu allocates one from the catalogue and answers its id.
func gpu(t *testing.T, ts *httptest.Server, body string) (string, map[string]any) {
	t.Helper()
	status, made := post(t, ts, "CreateFlexibleGpu", body)
	if status != http.StatusOK {
		t.Fatalf("CreateFlexibleGpu %s: %d (%v)", body, status, made)
	}
	inner, _ := made["FlexibleGpu"].(map[string]any)
	id, _ := inner["FlexibleGpuId"].(string)
	if id == "" {
		t.Fatalf("no FlexibleGpuId in %v", made)
	}
	return id, inner
}

// The catalogue is the one a real account answers (#619).
//
// Read 2026-09-04 through `octl iaas api ReadFlexibleGpuCatalog`: eleven models,
// with Generations, MaxCpu, MaxRam, ModelName and VRam. Verbatim rather than a
// plausible subset, because a client sizing a machine against this table reads a
// compatibility claim and an invented row is a claim nothing published.
func TestTheGpuCatalogueIsTheOneMeasured(t *testing.T) {
	ts := newServer(t)

	status, out := post(t, ts, "ReadFlexibleGpuCatalog", `{}`)
	if status != http.StatusOK {
		t.Fatalf("ReadFlexibleGpuCatalog: %d (%v)", status, out)
	}
	entries, _ := out["FlexibleGpuCatalog"].([]any)
	if len(entries) != 11 {
		t.Fatalf("the catalogue answers %d models, want the 11 a real account answers", len(entries))
	}

	byModel := map[string]map[string]any{}
	for _, entry := range entries {
		model, _ := entry.(map[string]any)
		name, _ := model["ModelName"].(string)
		byModel[name] = model
	}
	// One row checked field for field against the reading.
	h200, present := byModel["nvidia-h200"]
	if !present {
		t.Fatalf("nvidia-h200 is missing from the catalogue: %v", byModel)
	}
	for field, want := range map[string]any{
		"MaxCpu": float64(130),
		"MaxRam": float64(2300),
		"VRam":   float64(141000),
	} {
		if got := h200[field]; got != want {
			t.Errorf("nvidia-h200.%s is %v, want %v", field, got, want)
		}
	}
	generations, _ := h200["Generations"].([]any)
	if len(generations) != 2 || generations[0] != "v7" || generations[1] != "v104" {
		t.Errorf("nvidia-h200 is compatible with %v, want [v7 v104]", generations)
	}
}

// Every model the catalogue lists is one the create takes, and a model it does
// not list is refused (#619).
//
// The link #658 made explicit the day before: a catalogue whose items the create
// refuses is the ListVolumesTypes trap, and it is why this family is served
// whole rather than as a table on its own.
func TestCreatingAnFGpuChecksTheModelAgainstTheCatalogue(t *testing.T) {
	ts := newServer(t)

	status, out := post(t, ts, "ReadFlexibleGpuCatalog", `{}`)
	if status != http.StatusOK {
		t.Fatalf("catalogue: %d", status)
	}
	entries, _ := out["FlexibleGpuCatalog"].([]any)
	for _, entry := range entries {
		model, _ := entry.(map[string]any)
		name, _ := model["ModelName"].(string)
		generations, _ := model["Generations"].([]any)
		first, _ := generations[0].(string)
		id, made := gpu(t, ts, `{"ModelName":"`+name+`","Generation":"`+first+
			`","SubregionName":"eu-west-2a"}`)
		if id == "" {
			t.Errorf("the catalogue offers %q and the create did not take it", name)
		}
		if got, _ := made["State"].(string); got != "allocated" {
			t.Errorf("a fresh fGPU is %q, want allocated", got)
		}
		if got, _ := made["ModelName"].(string); got != name {
			t.Errorf("the fGPU answers model %q, want %q", got, name)
		}
	}

	// A model nothing offers.
	if status, refused := post(t, ts, "CreateFlexibleGpu",
		`{"ModelName":"nvidia-imaginary","SubregionName":"eu-west-2a"}`); status != http.StatusBadRequest {
		t.Errorf("an unlisted model answered %d, want 400: %v", status, refused)
	}
	// And a generation the model is not compatible with, which the catalogue
	// also states: nvidia-p100 is v5 only.
	if status, refused := post(t, ts, "CreateFlexibleGpu",
		`{"ModelName":"nvidia-p100","Generation":"v7","SubregionName":"eu-west-2a"}`); status != http.StatusBadRequest {
		t.Errorf("an incompatible generation answered %d, want 400: %v", status, refused)
	}
}

// Linking twice is refused, and so is unlinking what is attached to nothing
// (#619, on the shape #621 settled for volumes).
func TestLinkingAnFGpuTwiceIsRefused(t *testing.T) {
	ts := newServer(t)

	_, subnetID := netAndSubnet(t, ts, "10.95.0.0/16", "10.95.1.0/24")
	newVM := func() string {
		status, made := post(t, ts, "CreateVms",
			`{"ImageId":"ami-00000001","SubnetId":"`+subnetID+`","BootOnCreation":false}`)
		if status != http.StatusOK {
			t.Fatalf("CreateVms: %d (%v)", status, made)
		}
		vms, _ := made["Vms"].([]any)
		first, _ := vms[0].(map[string]any)
		id, _ := first["VmId"].(string)
		return id
	}
	one, two := newVM(), newVM()
	id, _ := gpu(t, ts, `{"ModelName":"nvidia-a10","Generation":"v6","SubregionName":"eu-west-2a"}`)

	// Unlinking one that is attached to nothing.
	if status, refused := post(t, ts, "UnlinkFlexibleGpu",
		`{"FlexibleGpuId":"`+id+`"}`); status != http.StatusConflict {
		t.Errorf("unlinking a free fGPU answered %d, want 409: %v", status, refused)
	}

	// The accepting half.
	if status, out := post(t, ts, "LinkFlexibleGpu",
		`{"FlexibleGpuId":"`+id+`","VmId":"`+one+`"}`); status != http.StatusOK {
		t.Fatalf("linking a free fGPU answered %d (%v)", status, out)
	}
	status, listed := post(t, ts, "ReadFlexibleGpus", `{"Filters":{"VmIds":["`+one+`"]}}`)
	if status != http.StatusOK {
		t.Fatalf("ReadFlexibleGpus: %d (%v)", status, listed)
	}
	gpus, _ := listed["FlexibleGpus"].([]any)
	if len(gpus) != 1 {
		t.Fatalf("the machine's fGPU is not listed against it: %v", listed)
	}
	attached, _ := gpus[0].(map[string]any)
	if got, _ := attached["State"].(string); got != "attached" {
		t.Errorf("a linked fGPU is %q, want attached", got)
	}

	// Linking it again, elsewhere.
	if status, refused := post(t, ts, "LinkFlexibleGpu",
		`{"FlexibleGpuId":"`+id+`","VmId":"`+two+`"}`); status != http.StatusConflict {
		t.Errorf("linking an attached fGPU answered %d, want 409: %v", status, refused)
	}
	// Deleting it while attached.
	if status, refused := post(t, ts, "DeleteFlexibleGpu",
		`{"FlexibleGpuId":"`+id+`"}`); status != http.StatusConflict {
		t.Errorf("deleting an attached fGPU answered %d, want 409: %v", status, refused)
	}
	// Detached, it deletes: a refusal that swallowed the ordinary path would
	// pass everything above.
	if status, out := post(t, ts, "UnlinkFlexibleGpu", `{"FlexibleGpuId":"`+id+`"}`); status != http.StatusOK {
		t.Fatalf("unlinking an attached fGPU answered %d (%v)", status, out)
	}
	if status, out := post(t, ts, "DeleteFlexibleGpu", `{"FlexibleGpuId":"`+id+`"}`); status != http.StatusOK {
		t.Errorf("deleting a free fGPU answered %d (%v)", status, out)
	}
}

// Update changes the one field its request carries, and nothing else (#619).
func TestUpdatingAnFGpuChangesTheOneFieldItOwns(t *testing.T) {
	ts := newServer(t)
	id, made := gpu(t, ts, `{"ModelName":"nvidia-v100","Generation":"v5","SubregionName":"eu-west-2a","DeleteOnVmDeletion":false}`)
	if got := made["DeleteOnVmDeletion"]; got != false {
		t.Fatalf("the fresh fGPU answers DeleteOnVmDeletion %v, want false", got)
	}

	status, out := post(t, ts, "UpdateFlexibleGpu",
		`{"FlexibleGpuId":"`+id+`","DeleteOnVmDeletion":true}`)
	if status != http.StatusOK {
		t.Fatalf("UpdateFlexibleGpu: %d (%v)", status, out)
	}
	updated, _ := out["FlexibleGpu"].(map[string]any)
	if got := updated["DeleteOnVmDeletion"]; got != true {
		t.Errorf("after the update DeleteOnVmDeletion is %v, want true", got)
	}
	// The rest is untouched, which is what "the one field it owns" means.
	if got, _ := updated["ModelName"].(string); got != "nvidia-v100" {
		t.Errorf("the update changed the model to %q", got)
	}
	if got, _ := updated["State"].(string); got != "allocated" {
		t.Errorf("the update changed the state to %q", got)
	}
}
