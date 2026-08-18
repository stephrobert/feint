package outscale

import (
	"net/http"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Images a client registers, beside the fixed catalogue.
//
// catalog.go serves three OMIs that always exist, because every client reads an
// inventory before it creates anything. This is the other half: an image the
// client made itself, from a snapshot or from a machine, which ReadImages must
// then list alongside the catalogue.
//
// Shape measured against a real account (lifecycle sweep, 2026-08-08). Every
// value below appears in that recording: ImageType "machine", RootDeviceType
// "bsu" (a Vm answers "ebs" for the same idea — their two names, and the
// recording is what says which goes where), BootModes ["legacy"], StateComment
// an empty object, PermissionsToLaunch {AccountIds: [], GlobalPermission:
// false}, and the BlockDeviceMappings carrying the snapshot the image was cut
// from with its size and type.
//
// What is NOT served: creating an image from a FileLocation or a
// SourceImageId — the first reads from Object Storage and the second copies
// across regions, and neither exists here. They are refused rather than
// silently ignored, which is the same rule the snapshot import follows.

const kindImage = "image"

type createImageRequest struct {
	ImageName           string           `json:"ImageName"`
	Description         string           `json:"Description"`
	VMID                string           `json:"VmId"`
	RootDeviceName      string           `json:"RootDeviceName"`
	Architecture        string           `json:"Architecture"`
	BootModes           []string         `json:"BootModes"`
	ProductCodes        []string         `json:"ProductCodes"`
	TpmMandatory        *bool            `json:"TpmMandatory"`
	NoReboot            *bool            `json:"NoReboot"`
	BlockDeviceMappings []map[string]any `json:"BlockDeviceMappings"`
	FileLocation        string           `json:"FileLocation"`
	SourceImageID       string           `json:"SourceImageId"`
	SourceRegionName    string           `json:"SourceRegionName"`
	DryRun              *bool            `json:"DryRun"`
}

func (p *Pack) createImage(w http.ResponseWriter, r *http.Request) {
	var req createImageRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.FileLocation != "" || req.SourceImageID != "" {
		p.badRequest(w, "creating an image from a FileLocation or a SourceImageId is not emulated: "+
			"the first reads from Object Storage and the second copies across regions, and neither exists here")
		return
	}
	if req.ImageName == "" {
		p.badRequest(w, "ImageName is required")
		return
	}
	// One name per account, which the real API enforces and Terraform's
	// create-before-destroy depends on being refused rather than overwritten.
	for _, other := range p.env.Store.List(kindImage, resource.Tenant{Provider: Name}) {
		if stringOf(other.Attrs["ImageName"]) == req.ImageName {
			p.conflict(w, "the image name "+req.ImageName+" is already used by "+other.ID)
			return
		}
	}

	// The mappings the client asked for, validated against what exists: an
	// image cut from a snapshot nobody created is a provenance invented out of
	// nothing, and the real API refuses it too.
	mappings := make([]any, 0, len(req.BlockDeviceMappings))
	for _, raw := range req.BlockDeviceMappings {
		bsu, _ := raw["Bsu"].(map[string]any)
		if bsu == nil {
			continue
		}
		snapshotID := stringOf(bsu["SnapshotId"])
		if snapshotID == "" {
			p.badRequest(w, "each BlockDeviceMappings entry needs a Bsu.SnapshotId")
			return
		}
		snapshot, found := p.env.Store.Get(Name, kindSnapshot, snapshotID)
		if !found {
			p.notFound(w, "snapshot", snapshotID)
			return
		}
		size, _ := snapshot.Attrs["VolumeSize"].(int)
		if asked := int(numOf(bsu["VolumeSize"])); asked > 0 {
			size = asked
		}
		// Iops mirrors what the client declared, with the platform's floor as
		// the default: the real cloud writes Iops on every image Bsu it
		// returns — measured in shapes/outscale.json, and the field gate (#88)
		// holds ReadImages to it — so omitting it here served a mapping no
		// real answer has.
		iops := int(numOf(bsu["Iops"]))
		if iops == 0 {
			iops = 100
		}
		mappings = append(mappings, map[string]any{
			"DeviceName": orDefault(stringOf(raw["DeviceName"]), defaultRootDevice),
			"Bsu": map[string]any{
				"SnapshotId":         snapshotID,
				"VolumeSize":         size,
				"VolumeType":         orDefault(stringOf(bsu["VolumeType"]), defaultVolumeType),
				"DeleteOnVmDeletion": true,
				"Iops":               iops,
			},
		})
	}
	// From a machine rather than from a snapshot: the real API accepts VmId and
	// derives the mappings. Refused when the machine is unknown, and the
	// mapping list stays empty because this emulator models no root volume —
	// stated in docs/limits.md rather than invented here.
	if req.VMID != "" {
		if _, found := p.env.Store.Get(Name, kindVM, req.VMID); !found {
			p.notFound(w, "Vm", req.VMID)
			return
		}
	}
	if len(mappings) == 0 && req.VMID == "" {
		p.badRequest(w, "an image needs a VmId or BlockDeviceMappings naming a snapshot")
		return
	}

	now := p.env.Now()
	res := resource.New(newID("ami", p.env.NewID()), kindImage, resource.Tenant{Provider: Name}, "available", now)
	res.Attrs = map[string]any{
		"ImageName":           req.ImageName,
		"Description":         req.Description,
		"Architecture":        orDefault(req.Architecture, "x86_64"),
		"RootDeviceName":      orDefault(req.RootDeviceName, defaultRootDevice),
		"RootDeviceType":      "bsu",
		"ImageType":           "machine",
		"BootModes":           bootModesOr(req.BootModes),
		"ProductCodes":        productCodesOr(req.ProductCodes),
		"SecureBoot":          false,
		"TpmMandatory":        boolOr(req.TpmMandatory, false),
		"StateComment":        map[string]any{},
		"BlockDeviceMappings": mappings,
		"PermissionsToLaunch": map[string]any{
			"AccountIds":       []any{},
			"GlobalPermission": false,
		},
		"Tags": []any{},
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Image":           imageView(res),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) updateImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImageID             string           `json:"ImageId"`
		Description         string           `json:"Description"`
		ProductCodes        []string         `json:"ProductCodes"`
		PermissionsToLaunch map[string]any   `json:"PermissionsToLaunch"`
		DryRun              *bool            `json:"DryRun"`
		Ignored             []map[string]any `json:"-"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if _, found := p.env.Store.Get(Name, kindImage, req.ImageID); !found {
		// A catalogue image is not the client's to change, and saying so beats
		// answering success on an object that will read back unchanged.
		if isCatalogueImage(req.ImageID) {
			p.conflict(w, "the image "+req.ImageID+" belongs to the emulated catalogue and cannot be updated")
			return
		}
		p.notFound(w, "image", req.ImageID)
		return
	}
	// PermissionsToLaunch is the cross-account grant, and this emulator has one
	// implicit account: the additions name nobody it knows. Refused rather than
	// recorded, for the reason UpdateSnapshot is declined outright.
	if len(req.PermissionsToLaunch) > 0 {
		p.badRequest(w, "PermissionsToLaunch grants to another account, and this emulator has one implicit account")
		return
	}
	// Inside the store lock: written back wholesale from a clone, this erased a
	// concurrent write to another field of the same image — its tags — after
	// their 200 (#295).
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindImage, req.ImageID, func(stored *resource.Resource) error {
		if req.Description != "" {
			stored.Attrs["Description"] = req.Description
		}
		if len(req.ProductCodes) > 0 {
			stored.Attrs["ProductCodes"] = toAnySlice(req.ProductCodes)
		}
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		p.notFound(w, "image", req.ImageID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Image":           imageView(updated),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImageID string `json:"ImageId"`
		DryRun  *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if _, found := p.env.Store.Get(Name, kindImage, req.ImageID); !found {
		if isCatalogueImage(req.ImageID) {
			p.conflict(w, "the image "+req.ImageID+" belongs to the emulated catalogue and cannot be deleted")
			return
		}
		p.notFound(w, "image", req.ImageID)
		return
	}
	p.env.Store.Delete(Name, kindImage, req.ImageID)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// imageView is the wire shape of a registered image.
func imageView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+4)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["ImageId"] = res.ID
	out["State"] = res.State
	out["AccountId"] = accountID
	out["CreationDate"] = res.Created.Format(time.RFC3339)
	return out
}

// defaultRootDevice is what both the real cloud and the catalogue use.
const defaultRootDevice = "/dev/sda1"

func bootModesOr(modes []string) []any {
	if len(modes) == 0 {
		// Measured: an image cut from a snapshot answers ["legacy"].
		return []any{"legacy"}
	}
	return toAnySlice(modes)
}

func productCodesOr(codes []string) []any {
	if len(codes) == 0 {
		return []any{linuxProductCode}
	}
	return toAnySlice(codes)
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// isCatalogueImage reports whether an id names one of the fixed OMIs. They are
// the emulator's, not the client's: a delete or an update on one is refused
// rather than answered with a success nothing acted on.
func isCatalogueImage(id string) bool {
	for _, image := range images {
		if image["ImageId"] == id {
			return true
		}
	}
	return false
}
