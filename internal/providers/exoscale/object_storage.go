package exoscale

import "github.com/stephrobert/feint/internal/s3"

// ObjectStorage declares the measured SOS dialect and configurable CLI endpoints.
// It does not advertise control-plane bucket usage or multipart support.
func (p *Pack) ObjectStorage(region string) s3.Profile {
	return s3.Profile{Provider: Name, Region: region, MissingBucket: s3.ExoscaleMissingBucket, HostEndpoint: true,
		Variables: []string{"EXOSCALE_SOS_ENDPOINT", "EXOSCALE_STORAGE_API_ENDPOINT"}, RegionVariable: "EXOSCALE_ZONE"}
}
