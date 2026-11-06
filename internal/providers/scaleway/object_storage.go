package scaleway

import "github.com/stephrobert/feint/internal/s3"

// ObjectStorage declares the measured S3 dialect and configurable CLI endpoint.
// It does not advertise Terraform redirection or ancillary S3 operations.
func (p *Pack) ObjectStorage(region string) s3.Profile {
	return s3.Profile{Provider: Name, Region: region, MissingBucket: s3.ScalewayMissingBucket,
		Variables: []string{"SCW_S3_ENDPOINT"}, RegionVariable: "SCW_DEFAULT_REGION"}
}
