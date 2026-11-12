package scaleway_test

import (
	"net/http"
	"testing"
)

// `image_id` names the same image the label names, and `version_id` is
// refused: this catalogue records no version for its images (docs/limits.md),
// so answering the one image for an arbitrary version would claim a version
// nobody recorded (#277).
func TestListLocalImagesImageIDAndVersionID(t *testing.T) {
	ts := newTestServer(t)
	jammy := "22222222-2222-4222-8222-222222222222"
	noble := "55555555-5555-4555-8555-555555555555"

	images := func(query string) []map[string]any {
		t.Helper()
		status, out := do(t, ts, "GET", "/marketplace/v2/local-images"+query, "")
		if status != http.StatusOK {
			t.Fatalf("GET /local-images%s: %d (%v)", query, status, out)
		}
		list, _ := out["local_images"].([]any)
		got := make([]map[string]any, 0, len(list))
		for _, raw := range list {
			img, _ := raw.(map[string]any)
			got = append(got, img)
		}
		return got
	}

	// An id alone names its image: the label is resolved from the id, not
	// from the default, so the noble id answers the noble image.
	if got := images("?image_id=" + noble); len(got) != 1 || got[0]["id"] != noble || got[0]["label"] != "ubuntu_noble" {
		t.Errorf("image_id=%s answers %v, want the one noble image", noble, got)
	}
	if got := images("?image_id=" + jammy); len(got) != 1 || got[0]["id"] != jammy {
		t.Errorf("image_id=%s answers %v, want the one jammy image", jammy, got)
	}
	// An id outside the table names nothing, and one that disagrees with a
	// label the client also sent names nothing either.
	if got := images("?image_id=99999999-9999-4999-8999-999999999999"); len(got) != 0 {
		t.Errorf("an unknown image_id answers %v, want none", got)
	}
	if got := images("?image_label=ubuntu_noble&image_id=" + jammy); len(got) != 0 {
		t.Errorf("a label and an id that disagree answer %v, want none", got)
	}
	// Any version_id is refused with a 400, not answered with the one image.
	if status, _ := do(t, ts, "GET", "/marketplace/v2/local-images?version_id=1", ""); status != http.StatusBadRequest {
		t.Errorf("version_id answered %d, want 400", status)
	}

	// And the refusal names version_id even when another filter would have
	// answered empty on its own. Asked after the image_id lookup, an unknown
	// id plus a version answered 200 with no image: that tells the client
	// "no such image" when the image was never the problem, and it hides a
	// filter this emulator cannot honour behind one it can. The refusal is
	// therefore asked before anything is resolved.
	status, out := do(t, ts, "GET",
		"/marketplace/v2/local-images?image_id=99999999-9999-4999-8999-999999999999&version_id=1", "")
	if status != http.StatusBadRequest {
		t.Fatalf("an unknown image_id with a version_id answered %d, want 400 (%v)", status, out)
	}
	details, _ := out["details"].([]any)
	named := ""
	if len(details) > 0 {
		first, _ := details[0].(map[string]any)
		named, _ = first["argument_name"].(string)
	}
	if named != "version_id" {
		t.Errorf("the refusal names %q, want version_id: a 400 that names the wrong argument sends the client to fix the wrong thing", named)
	}
}
