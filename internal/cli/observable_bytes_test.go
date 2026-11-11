package cli

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/objectstore"
	"github.com/stephrobert/feint/internal/s3"
)

// The defect an external audit found in merged code, and the test that would
// have stopped it.
//
// The observable state took an object's digest from ListObjectsV2's ETag, and
// internal/s3/list.go emits no ETag: the field was always empty, so two
// different contents under one key compared EQUAL. A reset could report
// convergence over bytes that had changed, and the commit message claimed
// objects were compared by digest.
//
// What let it through is the shape of the test beside it:
// TestADifferenceIsNamedWhereverItIs built observedObject values BY HAND, with
// digests in them, so it exercised the comparison and never the observation.
// A fake that emitted an ETag would have hidden it just as well.
//
// So this one mounts a REAL door over a REAL object store. It is the only
// arrangement in which the absent field is visible at all.

// realDoorLab serves one scaleway/fr-par door over a byte store, plus the
// three endpoints observeLab reads. Nothing here is canned: the listing, the
// bytes and the report all come from internal/s3.
func realDoorLab(t *testing.T) (*httptest.Server, *objectstore.Store) {
	t.Helper()

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := objectstore.OpenWithLimit(t.Context(), filepath.Join(root, "objects"), 2<<20)
	if err != nil {
		t.Fatalf("open the byte store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	control := http.NewServeMux()
	control.HandleFunc("/_feint/resources", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":0,"resources":[]}`))
	})
	control.HandleFunc("/_feint/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"machines":"none","capabilities":null,"enforced":{}}`))
	})

	doors, err := s3.Mount(control, store, []s3.Profile{
		// The profile the Scaleway pack actually declares, not a minimal one:
		// a door without BucketConfiguration answers 501 to the bucket listing,
		// and the reader would then observe nothing at all. Copied from the
		// pack so this test measures the door an operator gets.
		{Provider: "scaleway", Region: "fr-par", MissingBucket: s3.ScalewayMissingBucket,
			BucketConfiguration: true, ObjectDiscovery: true, ListObjectsV1: true,
			PrivateACLWrite: true},
	}, 1<<20, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("mount the door: %v", err)
	}
	// The report the reader validates, served from the doors' own snapshot so
	// the test cannot disagree with the code about what is mounted.
	mounted := http.NewServeMux()
	mounted.HandleFunc(s3.ReportPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeReportJSON(t, w, doors)
	})
	mounted.Handle("/", doors)

	server := httptest.NewServer(mounted)
	t.Cleanup(server.Close)
	return server, store
}

func writeReportJSON(t *testing.T, w http.ResponseWriter, doors *s3.Doors) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(doors.Snapshot()); err != nil {
		t.Errorf("serve the S3 report: %v", err)
	}
}

// TestTwoObjectsOfOneSizeAndDifferentBytesDoNotConverge is cited by the
// observable state's doc, and it is the audit's own decisive case: two objects
// of the same key and the same size, with different bytes, must produce a
// different verdict.
func TestTwoObjectsOfOneSizeAndDifferentBytesDoNotConverge(t *testing.T) {
	lab, _ := realDoorLab(t)
	const bucket, key = "images", "disk.img"
	base := lab.URL + s3.DoorPath("scaleway", "fr-par")

	// Everything goes THROUGH the door, which is what a client does and what
	// this test is about. It also earned its place: a bucket created behind
	// the door, straight on the store, carries no creation metadata and the
	// door then refuses to list it with a 501 — "legacy bucket listing has no
	// measured creation metadata". The first version of this test did that
	// and spent three runs blaming the reader.
	doorPut(t, base+bucket, "")

	// Same length, different content. A comparison on size alone, or on an
	// absent digest, calls these two states equal.
	const before, after = "AAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBB"
	if len(before) != len(after) {
		t.Fatal("the two payloads must be the same length or this test proves nothing")
	}

	put := func(body string) {
		t.Helper()
		doorPut(t, base+bucket+"/"+key, body)
	}

	put(before)
	checkpoint, err := observeLab(t.Context(), lab.URL, machine.Runtime{})
	if err != nil {
		t.Fatalf("observe the checkpoint: %v", err)
	}
	if len(checkpoint.Objects) != 1 {
		t.Fatalf("the observation found %d object(s); the door or the reader is not wired",
			len(checkpoint.Objects))
	}
	// The digest must be REAL. An empty one is how the defect looked.
	if len(checkpoint.Objects[0].Digest) != 64 {
		t.Fatalf("the observation carries digest %q; an empty or short digest compares everything equal",
			checkpoint.Objects[0].Digest)
	}
	if checkpoint.Objects[0].Size != int64(len(before)) {
		t.Errorf("the observation read %d bytes", checkpoint.Objects[0].Size)
	}

	put(after)
	changed, err := observeLab(t.Context(), lab.URL, machine.Runtime{})
	if err != nil {
		t.Fatalf("observe the change: %v", err)
	}

	differences := checkpoint.diff(changed)
	if len(differences) == 0 {
		t.Fatal("two objects of one key and one size with different bytes compared EQUAL; " +
			"a reset would report convergence over content that changed")
	}
	joined := strings.Join(differences, "\n")
	if !strings.Contains(joined, key) {
		t.Errorf("the verdict does not name the object that changed:\n%s", joined)
	}

	// And the same bytes twice compare equal, so this cannot be satisfied by
	// a comparison that always differs — which would make every reset fail
	// and teach an operator to ignore the verdict.
	put(before)
	back, err := observeLab(t.Context(), lab.URL, machine.Runtime{})
	if err != nil {
		t.Fatalf("observe the restoration: %v", err)
	}
	if differences := checkpoint.diff(back); len(differences) != 0 {
		t.Errorf("the same bytes compared differently:\n%s", strings.Join(differences, "\n"))
	}
}

// doorPut writes through the door, so that what the listing and the GET see
// is what a client would have made.
func doorPut(t *testing.T, url, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		answer, _ := io.ReadAll(io.LimitReader(res.Body, 1<<12))
		t.Fatalf("PUT %s answered %d: %s", url, res.StatusCode, answer)
	}
}
