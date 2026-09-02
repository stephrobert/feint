package scaleway

import (
	"fmt"
	"net/http"
	"sort"
)

// What instance/v1 does with a volume_type, measured against a real fr-par
// account on 2026-09-02 (#393). Two routes, two vocabularies, and the difference
// is not decoration: a client branches on argument_name, and the two help
// messages point at different products.
//
//	POST /volumes                     argument volume_type
//	  absent      400 required    "required key not provided"
//	  b_ssd       400 constraint  "b_ssd volumes are no longer supported. Use
//	                               Scaleway Block Storage (SBS) volumes instead…"
//	  sbs_volume  400 constraint  "not a valid value"  (block/v1 mints those)
//	  l_ssd       201
//	  scratch     201
//
//	POST /servers                     argument volumes.<key>.volume_type
//	  b_ssd       400 constraint  "b_ssd volumes are no longer supported. Create
//	                               volumes with volume_type=sbs_volume instead…"
//	  nonsense    400 constraint  "not a valid value"
//	  sbs_volume  201             (#365: it is what a DEV1-S is given)
//	  l_ssd       201, and the volume comes back l_ssd — honoured, not replaced
//	  scratch     400 constraint on argument image, not on the type:
//	                               "Cannot use an image with a scratch volume"
//
// This emulator answered 201 to all of them and stored b_ssd whatever was asked,
// standing on a type the platform has retired. The surface scan cannot see that:
// CreateVolume's Go signature never changed, and neither did CreateServer's.
// Only a recording of the cloud names this kind of drift, which is the whole
// argument for keeping one.
//
// One fact, three entry points: a create of a volume, a create of a server whose
// template asks for a disk, and an update that adds one. Written three times, a
// correction on one survives on the other two, and this file exists so that
// cannot happen.
var (
	// creatableVolumeTypes is what POST /volumes still mints, and it is exactly
	// what GET /products/volumes lists — measured on fr-par the same day, which
	// is what makes the two answers one fact rather than two tables.
	creatableVolumeTypes = map[string]bool{"l_ssd": true, "scratch": true}

	// serverVolumeTypes adds sbs_volume, which a server template may name and
	// POST /volumes may not: block/v1 is where the cloud moved that product, and
	// a server reaching into it is the normal path since #365.
	serverVolumeTypes = map[string]bool{"l_ssd": true, "scratch": true, "sbs_volume": true}
)

// volumeTypeError names what POST /volumes answers for a volume_type, or nil
// when the cloud creates it.
//
// TestCreateVolumeRefusesTheTypesTheCloudRefuses fails without this.
func volumeTypeError(volumeType string) *ArgumentError {
	switch {
	case volumeType == "":
		return &ArgumentError{
			ArgumentName: "volume_type",
			Reason:       "required",
			HelpMessage:  "required key not provided",
		}
	case volumeType == "b_ssd":
		return &ArgumentError{
			ArgumentName: "volume_type",
			Reason:       "constraint",
			HelpMessage: "b_ssd volumes are no longer supported. Use Scaleway Block Storage (SBS) " +
				"volumes instead. More details on " +
				"https://www.scaleway.com/en/developers/api/instance/#path-volumes-migrate-a-volume-andor-snapshots-to-sbs-scaleway-block-storage" +
				" for migration and https://www.scaleway.com/en/developers/api/block/#path-volume-create-a-volume" +
				" about Scaleway Block Storage volume",
		}
	case !creatableVolumeTypes[volumeType]:
		// sbs_volume lands here, and the cloud does not point at the product
		// that serves it either: it answers the same flat refusal as for a type
		// that never existed. Rule 4 says to copy the wording, not improve it.
		return &ArgumentError{
			ArgumentName: "volume_type",
			Reason:       "constraint",
			HelpMessage:  "not a valid value",
		}
	}
	return nil
}

// refuseRetiredVolumeType writes the refusal above and reports whether it did.
func refuseRetiredVolumeType(w http.ResponseWriter, volumeType string) bool {
	err := volumeTypeError(volumeType)
	if err == nil {
		return false
	}
	writeInvalidArguments(w, *err)
	return true
}

// serverVolumeTypeError names what POST /servers answers for one entry of its
// volumes map, or nil when the cloud builds it. An empty type is not an error
// here: a template naming no type gets the block root the cloud gives a DEV1-S
// (#365), and a template naming an existing volume by id carries no type at all.
//
// withImage carries the other half of the scratch measurement: the cloud refuses
// that combination on argument "image" rather than on the type, so a client
// reading argument_name is told which input to change.
func serverVolumeTypeError(key, volumeType string, withImage bool) *ArgumentError {
	field := "volumes." + key + ".volume_type"
	switch {
	case volumeType == "":
		return nil
	case volumeType == "b_ssd":
		return &ArgumentError{
			ArgumentName: field,
			Reason:       "constraint",
			HelpMessage: "b_ssd volumes are no longer supported. Create volumes with " +
				"volume_type=sbs_volume instead. More details at " +
				"https://www.scaleway.com/en/developers/api/instance/#technical-information",
		}
	case volumeType == "scratch" && withImage:
		return &ArgumentError{
			ArgumentName: "image",
			Reason:       "constraint",
			HelpMessage:  "Cannot use an image with a scratch volume",
		}
	case !serverVolumeTypes[volumeType]:
		return &ArgumentError{
			ArgumentName: field,
			Reason:       "constraint",
			HelpMessage:  "not a valid value",
		}
	}
	return nil
}

// refuseServerVolumeTypes answers a create whose volumes map names a type the
// cloud will not build, before anything is stored — the same ordering the
// flexible IPs are validated in, and for the same reason: a refusal written
// after the Put leaves a phantom server behind.
//
// The keys are walked in order so that a request naming two bad types always
// gets the same one named back. A map range would answer either, and a client
// diffing two runs would read a flapping API.
//
// TestCreateServerRefusesTheVolumeTypesTheCloudRefuses fails without this.
func refuseServerVolumeTypes(w http.ResponseWriter, volumes map[string]volumeTemplate, withImage bool) bool {
	keys := make([]string, 0, len(volumes))
	for key := range volumes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := serverVolumeTypeError(key, volumes[key].VolumeType, withImage); err != nil {
			writeInvalidArguments(w, *err)
			return true
		}
	}
	return false
}

// serverVolumeTypeFault is the same verdict for the update path, which reports
// through an error rather than writing the response itself.
//
// The wording is CreateServer's, because the shape of the field is
// CreateServer's — volumes.<key>.volume_type. UpdateServer was not measured, and
// this comment says so rather than letting the borrowed sentence read as a
// recording.
func serverVolumeTypeFault(key, volumeType string) error {
	if err := serverVolumeTypeError(key, volumeType, false); err != nil {
		return fmt.Errorf("%s: %s", err.ArgumentName, err.HelpMessage)
	}
	return nil
}
