package exoscale

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/s3"
)

// ObjectStorage declares the measured SOS dialect and configurable CLI endpoints.
// Native bucket usage is derived from the same published byte inventory.
// Default discovery errors follow the authorized empty-bucket SOS recording.
func (p *Pack) ObjectStorage(region string) s3.Profile {
	return s3.Profile{Provider: Name, Region: region, MissingBucket: s3.ExoscaleMissingBucket, HostEndpoint: true,
		Variables: []string{"EXOSCALE_SOS_ENDPOINT", "EXOSCALE_STORAGE_API_ENDPOINT"}, RegionVariable: "EXOSCALE_ZONE",
		BucketConfiguration: true, CloudHost: "sos-" + region + ".exo.io",
		AbsentConfigurations: map[string]s3.AbsentConfiguration{
			"cors":              {Status: 404, Code: "NoSuchCORSConfiguration", Message: "The CORS configuration does not exist.", BucketName: true},
			"ownershipControls": {Status: 400, Code: "OwnershipControlsNotFoundError", Message: "The bucket ownership controls were not found"},
		}}
}

type sosBucketUsage struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created-at"`
	Zone    string    `json:"zone-name"`
	Size    int64     `json:"size"`
}

func (p *Pack) listSOSBucketsUsage(w http.ResponseWriter, r *http.Request) {
	if p.env.Objects == nil {
		// The out-of-band marker #477 introduced, for its own reason: this is
		// the emulator refusing an operation, not a cloud answering, and a
		// status alone cannot carry that fact. A reader that only sees 503
		// cannot tell "the operator did not arm --objectstore" from "the
		// service is broken", and the probe proved it: it builds its request
		// from the operation's schema, that schema declares a 200 body, so it
		// reported a route answering the wrong shape on every run without the
		// flag — four CI jobs, with nothing saying the answer was a refusal.
		//
		// This does not excuse the flag being missing. The three conformance
		// starters arm it, and tools/conformance's TestEveryConformanceEmulatorArmsTheSameObjectStore
		// keeps them in step. The marker is for the one leg no flag reaches,
		// the shipped container run with no override.
		w.Header().Set(emulator.NotEmulatedHeader, p.Name())
		writeError(w, http.StatusServiceUnavailable, "Object Storage is disabled; start with --objectstore")
		return
	}
	inventory, err := p.env.Objects.Usage(r.Context())
	if err != nil {
		p.env.Log.ErrorContext(r.Context(), "read SOS bucket usage", "error", err)
		writeError(w, http.StatusInternalServerError, "unable to read persisted SOS bucket usage")
		return
	}
	items := make([]sosBucketUsage, 0)
	for scoped, bucket := range inventory {
		rest, ours := strings.CutPrefix(scoped, Name+"/")
		if !ours {
			continue
		}
		zone, name, valid := strings.Cut(rest, "/")
		created, dateErr := s3.BucketCreationTime(bucket.Metadata)
		if !valid || !publishedZone(zone) || name == "" || strings.Contains(name, "/") || dateErr != nil {
			p.env.Log.ErrorContext(r.Context(), "unsupported persisted SOS bucket scope or date", "error", dateErr)
			writeError(w, http.StatusInternalServerError, "persisted SOS bucket scope or creation date is unsupported")
			return
		}
		items = append(items, sosBucketUsage{Name: name, Zone: zone, Created: created, Size: bucket.Size})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Zone != items[j].Zone {
			return items[i].Zone < items[j].Zone
		}
		return items[i].Name < items[j].Name
	})
	emulator.WriteJSON(w, http.StatusOK, struct {
		Items []sosBucketUsage `json:"sos-buckets-usage"`
	}{items})
}
