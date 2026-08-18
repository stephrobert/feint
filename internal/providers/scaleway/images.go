package scaleway

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/resource"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// unknownImageID is the UUID answered for an image label no catalogue holds.
// The lookup still succeeds — docs/limits.md declares identifiers unchecked,
// deliberately — but this UUID maps back onto no bootable image, so a runtime
// refuses to boot it instead of substituting a distribution the client never
// named (#83). Fixed, like every catalogue UUID, because Terraform keeps it in
// state.
const unknownImageID = "99999999-9999-4999-8999-999999999999"

// getImage answers the lookup the CLI performs after resolving a label. An
// unknown ID is still served — refusing would break scripts that hardcode a
// real one (docs/limits.md) — under the default label, which is display
// fiction like the rest of the catalogue; a known ID answers its own label.
//
// The client's own images are read first, since SW-2. Without that, an image a
// client just cut reads back as the default catalogue entry: a create whose
// following GET answers something else is the single most common cause of
// "Provider produced inconsistent result after apply", and here it would also
// have made `scw instance image get` describe the wrong object.
// TestAnImageReadsBackAsItWasCreated fails without this.
func (p *Pack) getImage(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if res, found := p.env.Store.Get(Name, kindImage, id); found {
		emulator.WriteJSON(w, http.StatusOK, map[string]any{"image": p.clientImageView(res)})
		return
	}
	label := defaultImageLabel
	switch l, known := labelByID[id]; {
	case id == "":
		id = marketplaceImages[defaultImageLabel].ID
	case known:
		label = l
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"image": p.imageView(zone, id, label)})
}

// imageView is the shape the SDK decodes into instance.Image, shared by the
// image endpoint and by the image a server carries.
//
// A server must carry the object, never null. The Terraform provider reads the
// image of the server it just created without checking, so a null there does not
// produce a diff: it crashes the plugin, which surfaces as "Plugin did not
// respond" with nothing in the emulator's log.
// imageEpoch is when the emulated images were "built". A fixed instant, because
// the catalogue is fixed.
const imageEpoch = "2025-01-01T00:00:00Z"

func (p *Pack) imageView(zone, id, label string) map[string]any {
	// Fixed, not the wall clock. The catalogue is stable across runs by design,
	// and a real image's dates do not move: stamping each read with Now() gave a
	// client a modification_date that changed every time it looked, which is a
	// permanent diff for anything that compares.
	stamp := imageEpoch
	return map[string]any{
		"id":   id,
		"name": label,
		"arch": "x86_64",
		// Present and null on every image a real account returns, which is what
		// shapes/scaleway.json recorded. It was absent here for as long as
		// ListImages was declined and therefore incomparable; serving the list
		// made `feint shapes --check` see this view for the first time, and it
		// went red on the same day. The field is served rather than declined
		// because a caller reading it gets what the cloud gives.
		"default_bootscript": nil,
		"creation_date":      stamp,
		"modification_date":  stamp,
		"organization":       defaultOrganization,
		"project":            defaultProject,
		"public":             true,
		"state":              "available",
		"tags":               []string{},
		"zone":               zone,
		"extra_volumes":      map[string]any{},
		"from_server":        nil,
		"root_volume": map[string]any{
			"id":          "33333333-3333-4333-8333-333333333333",
			"name":        label + "-root",
			"size":        20_000_000_000,
			"volume_type": "b_ssd",
		},
	}
}

// resolveImage maps what a create request put in `image` onto what the
// emulator needs: the ID to publish, the name to display, and the catalogue
// label the machine driver turns into a base image.
//
// Clients send either form. The Terraform provider resolves the label through
// the marketplace first and sends a UUID; the CLI can send the label itself.
// Telling them apart is what lets `image = "debian_bookworm"` boot a Debian
// rather than the default.
//
// label is empty when the identifier is in no catalogue. The create still
// succeeds — deliberate, and documented in docs/limits.md — but a boot refuses
// an empty label rather than substituting a distribution the client never
// named (#83). display keeps the response behaviour of before: the requested
// label verbatim, or the default label for a foreign UUID, because an image
// name in a response is catalogue fiction either way.
func resolveImage(requested string) (id, display, label string) {
	switch {
	case requested == "":
		return marketplaceImages[defaultImageLabel].ID, defaultImageLabel, defaultImageLabel
	case looksLikeUUID(requested):
		if l, known := labelByID[requested]; known {
			return requested, l, l
		}
		return requested, defaultImageLabel, ""
	default:
		if entry, known := marketplaceImages[requested]; known {
			return entry.ID, requested, requested
		}
		return unknownImageID, requested, ""
	}
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// ---- Images a client creates, beside the fixed catalogue --------------------
//
// Served since SW-2. The refusal they replace read "the pack answers image
// lookups from the fixed catalogue the CLI resolves against", which described
// the implementation rather than a limit: Outscale has let a client register an
// image beside its catalogue since 0.6.0, and the golden-image sequence — snapshot
// a volume, cut an image from it, boot a server from that image — is a
// control-plane path this emulator can answer truthfully at every step.
//
// The bytes are the part it cannot provide, and that is #115: a server created
// from one of these refuses to boot rather than substituting a distribution
// nobody named, which is #83's rule applied to a resource the client made.

const kindImage = "instance/image"

// clientImageView renders an image the client created.
//
// Two fields are here because a recording said so, not because the SDK did.
// shapes/scaleway.json, taken from a real account, shows `default_bootscript`
// present and null on every image, and `from_server` typed as a string rather
// than the null this pack used to send. The SDK declares both as pointers, so
// reading it alone would have justified omitting one and nulling the other, and
// `feint shapes --check` would have gone red the moment ListImages started
// being served and comparable.
func (p *Pack) clientImageView(res *resource.Resource) map[string]any {
	return map[string]any{
		"id":                 res.ID,
		"name":               textOf(res.Attrs["name"]),
		"arch":               textOf(res.Attrs["arch"]),
		"creation_date":      res.Created.Format(time.RFC3339),
		"modification_date":  res.Updated.Format(time.RFC3339),
		"default_bootscript": nil,
		"extra_volumes":      map[string]any{},
		"from_server":        textOf(res.Attrs["from_server"]),
		"organization":       textOf(res.Attrs["organization"]),
		"project":            textOf(res.Attrs["project"]),
		"public":             false,
		"state":              res.State,
		"tags":               res.Attrs["tags"],
		"zone":               textOf(res.Attrs["zone"]),
		"root_volume":        res.Attrs["root_volume"],
	}
}

type createImageRequest struct {
	Name         string    `json:"name"`
	RootVolume   string    `json:"root_volume"`
	Arch         string    `json:"arch"`
	Tags         *[]string `json:"tags"`
	Organization *string   `json:"organization"`
	Project      *string   `json:"project"`
	Public       *bool     `json:"public"`
}

// createImage cuts an image from a snapshot.
//
// root_volume names the snapshot, which reads oddly and is what the API does:
// CreateImageRequest.RootVolume is the identifier of the snapshot the image is
// built from. A missing one is refused, for the reason createSnapshot refuses a
// missing volume — the client named a resource, and answering success about an
// image of nothing is the half-success this project exists to avoid.
// TestAnImageCutFromNothingIsRefused fails without this.
func (p *Pack) createImage(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req createImageRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "name", Reason: "required"})
		return
	}
	if req.RootVolume == "" {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "root_volume",
			Reason:       "required",
			HelpMessage:  "an image is cut from a snapshot, and this names which",
		})
		return
	}
	snapshot, found := p.env.Store.Get(Name, kindSnapshot, req.RootVolume)
	if !found {
		writeNotFound(w, "snapshot", req.RootVolume)
		return
	}

	project, organization := projectOf(textPtr(req.Project))
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindImage, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "available", now)
	res.Attrs = map[string]any{
		"name":         req.Name,
		"arch":         orDefault(req.Arch, "x86_64"),
		"organization": organization,
		"project":      project,
		"tags":         orEmpty(slicePtr(req.Tags)),
		"zone":         zone,
		"snapshot_id":  snapshot.ID,
		// The server the snapshot's volume came from, when there was one.
		// A string rather than null, which is what a real account returns.
		"from_server": textOf(snapshot.Attrs["from_server"]),
		"root_volume": map[string]any{
			"id":          snapshot.ID,
			"name":        textOf(snapshot.Attrs["name"]),
			"size":        snapshot.Attrs["size"],
			"volume_type": textOf(snapshot.Attrs["volume_type"]),
		},
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"image": p.clientImageView(res)})
}

// listImages answers the client's images and the catalogue's, in that order.
//
// Both, because both are real to a caller: `scw instance image list` is how
// someone checks the image they just cut exists, and the catalogue entries are
// what a create resolves against. Answering only one of the two would make the
// command lie by omission in whichever direction was chosen.
func (p *Pack) listImages(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	name := q.Get("name")
	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindImage, resource.Tenant{Provider: Name}) {
		if textOf(res.Attrs["zone"]) != zone {
			continue
		}
		if name != "" && !strings.Contains(textOf(res.Attrs["name"]), name) {
			continue
		}
		out = append(out, p.clientImageView(res))
	}
	for label, entry := range marketplaceImages {
		if name != "" && !strings.Contains(label, name) {
			continue
		}
		out = append(out, p.imageView(zone, entry.ID, label))
	}
	// The remaining declared filters read the assembled views, because the two
	// image populations answer them from different places: a client image
	// carries what its create stored, a catalogue image is public, x86_64 and
	// untagged by construction (#277).
	out = filterImageViews(out, q)
	// Paged like every other list of this pack. This was the one that ignored
	// per_page — five images whatever the client asked — found the day the
	// probe started holding paged answers to the size they asked, which is the
	// check that fails without this (TestScalewayProbesWhatItsDocumentAllows,
	// #156).
	page := parsePage(r)
	start, end := page.slice(len(out))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"images":      out[start:end],
		"total_count": len(out),
	})
}

// filterImageViews applies the ListImages filters that read the image itself:
// arch, public, organization, project and tags. The views carry every one of
// those fields for both image populations, so the filter reads what the client
// would.
func filterImageViews(views []map[string]any, q url.Values) []map[string]any {
	arch := q.Get("arch")
	// organization scopes to the whole account rather than comparing the
	// identifier — one organization lives here (scopeOf's rule), so a named
	// one keeps both image populations. project stays an equality: a project
	// is an isolation boundary this emulator does honour, and on the real
	// cloud filtering by yours excludes the public catalogue the same way.
	_ = q.Get("organization")
	project := q.Get("project")
	wantPublic, publicSet := queryBool(q, "public")
	tags := csvValues(q, "tags")
	if arch == "" && project == "" && !publicSet && len(tags) == 0 {
		return views
	}
	kept := views[:0]
	for _, view := range views {
		if arch != "" && view["arch"] != arch {
			continue
		}
		if project != "" && view["project"] != project {
			continue
		}
		if publicSet {
			isPublic, _ := view["public"].(bool)
			if isPublic != wantPublic {
				continue
			}
		}
		if len(tags) > 0 && !viewHasEveryTag(view["tags"], tags) {
			continue
		}
		kept = append(kept, view)
	}
	return kept
}

// viewHasEveryTag is hasEveryTag for a rendered view, whose tags are []string
// on a live image and []any after a snapshot restore.
func viewHasEveryTag(stored any, wanted []string) bool {
	have := map[string]bool{}
	switch tags := stored.(type) {
	case []string:
		for _, t := range tags {
			have[t] = true
		}
	case []any:
		for _, t := range tags {
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

// updateImage carries the name and the tags, which is what the SDK's
// UpdateImageRequest holds beyond the identifier. A catalogue image is not the
// client's to change, and saying so beats answering success on an object that
// reads back unchanged — the same answer the Outscale pack gives.
func (p *Pack) updateImage(w http.ResponseWriter, r *http.Request) {
	res, ok := p.clientImageOf(w, r)
	if !ok {
		return
	}
	var req struct {
		Name *string   `json:"name"`
		Tags *[]string `json:"tags"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	updated := res.Clone()
	if req.Name != nil {
		updated.Attrs["name"] = *req.Name
	}
	if req.Tags != nil {
		updated.Attrs["tags"] = *req.Tags
	}
	p.env.Store.Commit(updated, p.env.Now())
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"image": p.clientImageView(updated)})
}

func (p *Pack) deleteImage(w http.ResponseWriter, r *http.Request) {
	res, ok := p.clientImageOf(w, r)
	if !ok {
		return
	}
	p.env.Store.Delete(Name, kindImage, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

// clientImageOf resolves an image the client owns, refusing a catalogue one in
// the API's own dialect rather than with a 404 that would read as "gone".
func (p *Pack) clientImageOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	if _, ok := zoneOf(w, r); !ok {
		return nil, false
	}
	id := r.PathValue("id")
	if res, found := p.env.Store.Get(Name, kindImage, id); found {
		return res, true
	}
	if _, catalogue := labelByID[id]; catalogue {
		writePrecondition(w, "image", id,
			"this image belongs to the emulated catalogue and is not the client's to change")
		return nil, false
	}
	writeNotFound(w, "image", id)
	return nil, false
}
