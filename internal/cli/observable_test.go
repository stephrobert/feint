package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/s3"
)

// s3ReportVersionForTest keeps the fake honest: the reader refuses any other
// version, so the fake follows the constant rather than a copy of its value.
var s3ReportVersionForTest = fmt.Sprint(s3.ReportVersion)

// The observable state a reset is judged against (#853).
//
// These tests exist before the commands do, and that order is the point: the
// issue refuses a reset that reports success because no error occurred —
// "reset is not successful because JSON was restored" — so the verdict has to
// be a comparison, and a comparison is only worth the normalisation under it.

// fakeLab serves the three endpoints observeLab reads, with the bytes of one
// door, so that the reader can be driven without starting an emulator (`start`
// re-execs os.Executable(), which under `go test` is the test binary).
func fakeLab(t *testing.T, objects map[string]map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/_feint/resources", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":2,"resources":[
			{"id":"b","kind":"instance/server","state":"running",
			 "runtime":{"container":"feint-scw-b"},"created":"2026-11-11T09:00:00Z"},
			{"id":"a","kind":"instance/ip","state":"available","created":"2026-11-11T09:00:01Z"}]}`))
	})
	mux.HandleFunc("/_feint/s3", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The whole report, because fetchS3 validates it: a version, a
		// non-nil request list and limits that agree with the activation.
		// A fake that answered less would fail on the reader's own guard
		// rather than on what this test is about.
		_, _ = w.Write([]byte(`{"schema_version":` + s3ReportVersionForTest + `,
			"enabled":true,"max_blob_bytes":1073741824,"max_object_bytes":268435456,
			"http_requests":{"GET":1,"PUT":1},
			"doors":[{"provider":"scaleway","region":"fr-par",
			          "path":"/_feint/s3/scaleway/fr-par/"}]}`))
	})
	mux.HandleFunc("/_feint/s3/scaleway/fr-par/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		rest := strings.TrimPrefix(r.URL.Path, "/_feint/s3/scaleway/fr-par/")
		if rest == "" {
			var body strings.Builder
			body.WriteString("<ListAllMyBucketsResult><Buckets>")
			for bucket := range objects {
				body.WriteString("<Bucket><Name>" + bucket + "</Name></Bucket>")
			}
			body.WriteString("</Buckets></ListAllMyBucketsResult>")
			_, _ = w.Write([]byte(body.String()))
			return
		}
		var body strings.Builder
		body.WriteString("<ListBucketResult><IsTruncated>false</IsTruncated>")
		for key, digest := range objects[strings.TrimSuffix(rest, "/")] {
			body.WriteString("<Contents><Key>" + key + "</Key><ETag>" + digest +
				"</ETag><Size>7</Size></Contents>")
		}
		body.WriteString("</ListBucketResult>")
		_, _ = w.Write([]byte(body.String()))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestTwoReadsOfOneLabAreEqual is cited by the observable state's doc.
//
// Without this there is no verdict at all: a comparison whose two sides
// disagree about ordering reports a difference on every reset and an operator
// learns to ignore it. The fake answers its resources out of order and its
// objects from a map, which has no order at all, precisely so the sort is the
// thing under test.
func TestTwoReadsOfOneLabAreEqual(t *testing.T) {
	lab := fakeLab(t, map[string]map[string]string{
		"images": {"disk.qcow2": "d41d8", "boot.img": "a1b2c"},
		"backup": {"db.sql": "99aa0"},
	})
	rt := machine.Use(surveyingDriver{left: machine.Leftovers{
		Machines: []string{"feint-scw-b", "feint-scw-a"},
		Networks: []string{"scw-net-2", "scw-net-1"},
	}})

	first, err := observeLab(t.Context(), lab.URL, rt)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	second, err := observeLab(t.Context(), lab.URL, rt)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if diff := first.diff(second); len(diff) != 0 {
		t.Fatalf("two reads of one lab differ:\n%s", strings.Join(diff, "\n"))
	}

	// And it actually observed something: a comparison between two empty
	// states is equal too, and would pass this test while measuring nothing.
	if len(first.Resources) != 2 {
		t.Errorf("resources %+v", first.Resources)
	}
	if len(first.Objects) != 3 {
		t.Errorf("objects %+v", first.Objects)
	}
	if len(first.Machines) != 2 || len(first.Networks) != 2 {
		t.Errorf("host half %v %v", first.Machines, first.Networks)
	}
	// Sorted, not merely stable: the identity of a resource is its kind and
	// id, and a reader of the JSON must not have to sort it again.
	if first.Resources[0].ID != "a" || first.Objects[0].Bucket != "backup" {
		t.Errorf("not sorted: %+v %+v", first.Resources, first.Objects)
	}
	// The backing is carried, because a resource whose container changed
	// under it is a resource a reset did not reconcile even when the lists
	// are the same length.
	if first.Resources[1].Backing != "container=feint-scw-b" {
		t.Errorf("backing %q", first.Resources[1].Backing)
	}
}

// TestADifferenceIsNamedWhereverItIs is cited by the observable state's doc.
//
// #853 requires a reset to report "exactly what could not be reconciled", so
// one difference per axis is asserted here, in both directions. A verdict that
// found the extra container but not the changed digest would send an operator
// round the loop once per problem, and one that stopped at the first would do
// it every time.
func TestADifferenceIsNamedWhereverItIs(t *testing.T) {
	checkpoint := observed{
		Resources: []observedResource{{Kind: "instance/server", ID: "a", State: "running"}},
		Objects:   []observedObject{{Door: "d", Bucket: "b", Key: "k", Digest: "before"}},
		Machines:  []string{"feint-scw-a"},
		Networks:  []string{"scw-net-1"},
		Firewalls: []string{"scw-acl-1"},
	}
	now := observed{
		// The state moved, which is a difference even though the resource is
		// the same one: a reset that left a stopped server stopped has not
		// converged.
		Resources: []observedResource{
			{Kind: "instance/server", ID: "a", State: "stopped"},
			{Kind: "instance/ip", ID: "extra", State: "available"},
		},
		// Same door, same bucket, same key, different content. A comparison
		// on sizes would call these equal.
		Objects:   []observedObject{{Door: "d", Bucket: "b", Key: "k", Digest: "after"}},
		Machines:  []string{"feint-scw-a", "feint-scw-extra"},
		Networks:  nil,
		Firewalls: []string{"scw-acl-1"},
	}

	diff := checkpoint.diff(now)
	joined := strings.Join(diff, "\n")
	for _, want := range []string{
		"state=stopped",     // the moved state
		"instance/ip extra", // the resource nobody checkpointed
		"digest=after",      // the content that changed
		"digest=before",     // and the content that is gone
		"feint-scw-extra",   // the machine in excess
		"scw-net-1",         // the network a reset failed to bring back
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the verdict never mentions %q:\n%s", want, joined)
		}
	}
	// Both directions are worded apart, because "extra" and "missing" need
	// opposite fixes and a single word for both sends the reader the wrong way.
	if !strings.Contains(joined, "present and not in the checkpoint") ||
		!strings.Contains(joined, "in the checkpoint and not present") {
		t.Errorf("the verdict does not say which side a thing is on:\n%s", joined)
	}
	// And an unchanged axis says nothing at all: a verdict that listed the
	// firewall that did converge would bury the five that did not.
	if strings.Contains(joined, "scw-acl-1") {
		t.Errorf("an axis that converged was reported anyway:\n%s", joined)
	}

	// Equal states produce no verdict, which is the half that makes a second
	// reset a no-op (#853).
	if diff := checkpoint.diff(checkpoint); len(diff) != 0 {
		t.Errorf("a state differs from itself:\n%s", strings.Join(diff, "\n"))
	}
}
